package sabnzbd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xbapps/xbvr/pkg/dlclient"
)

const testAPIKey = "test-api-key"

// queueTestJSON is a representative mode=queue payload: one slot whose sizes
// arrive as JSON strings and one whose sizes arrive as JSON numbers, plus a
// paused slot and a zero-size slot.
const queueTestJSON = `{
	"queue": {
		"slots": [
			{
				"nzo_id": "SABnzbd_nzo_queue1",
				"filename": "String MB Release",
				"percentage": "42",
				"mb": "100",
				"mbleft": "58",
				"timeleft": "0:01:23",
				"status": "Downloading",
				"cat": "xbvr",
				"storage": "/downloads/queue1"
			},
			{
				"nzo_id": "SABnzbd_nzo_queue2",
				"filename": "Numeric MB Release",
				"percentage": 10,
				"mb": 200,
				"mbleft": 180,
				"timeleft": "",
				"status": "Paused",
				"cat": "xbvr",
				"storage": "/downloads/queue2"
			},
			{
				"nzo_id": "SABnzbd_nzo_queue3",
				"filename": "Zero Size Release",
				"percentage": "0",
				"mb": "0",
				"mbleft": "0",
				"timeleft": null,
				"status": "Queued",
				"cat": "xbvr",
				"storage": "/downloads/queue3"
			}
		]
	}
}`

// historyTestJSON is a representative mode=history payload. The completed entry
// is the regression that matters: it is no longer in the queue.
const historyTestJSON = `{
	"history": {
		"slots": [
			{
				"nzo_id": "SABnzbd_nzo_hist1",
				"name": "Completed Release",
				"status": "Completed",
				"bytes": 1048576,
				"storage": "/downloads/completed",
				"fail_message": "",
				"category": "xbvr"
			},
			{
				"nzo_id": "SABnzbd_nzo_hist2",
				"name": "Failed Release",
				"status": "Failed",
				"bytes": 2097152,
				"storage": "/downloads/failed",
				"fail_message": "Unpack failed",
				"category": "xbvr"
			},
			{
				"nzo_id": "SABnzbd_nzo_hist3",
				"name": "Extracting Release",
				"status": "Extracting",
				"bytes": 4096,
				"storage": "/downloads/extracting",
				"fail_message": "",
				"category": "xbvr"
			}
		]
	}
}`

func newTestClient(t *testing.T, srv *httptest.Server) dlclient.Client {
	t.Helper()
	c, err := New(dlclient.Config{
		ID:       "test-client",
		URL:      srv.URL,
		APIKey:   testAPIKey,
		Category: "xbvr",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// writeJSON writes a body with the right content type.
func writeJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write body: %v", err)
	}
}

// TestListMergesQueueAndHistory is the regression that matters most: a finished
// SABnzbd download lives only in the history, so List must merge both lists and
// report the completed entry as StateCompleted with the unpacked path.
func TestListMergesQueueAndHistory(t *testing.T) {
	var mu sync.Mutex
	var modes []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("mode")
		mu.Lock()
		modes = append(modes, mode)
		mu.Unlock()

		if got := r.URL.Query().Get("apikey"); got != testAPIKey {
			t.Errorf("apikey = %q, want %q", got, testAPIKey)
		}
		if got := r.URL.Query().Get("output"); got != "json" {
			t.Errorf("output = %q, want json", got)
		}

		switch mode {
		case "queue":
			writeJSON(t, w, queueTestJSON)
		case "history":
			writeJSON(t, w, historyTestJSON)
		default:
			t.Errorf("unexpected mode %q", mode)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	jobs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 6 {
		t.Fatalf("List returned %d jobs, want 6 (3 queue + 3 history)", len(jobs))
	}

	mu.Lock()
	gotModes := strings.Join(modes, ",")
	mu.Unlock()
	if gotModes != "queue,history" {
		t.Errorf("modes = %q, want %q", gotModes, "queue,history")
	}

	byID := make(map[string]dlclient.Job, len(jobs))
	for _, j := range jobs {
		byID[j.ID] = j
	}

	mib := int64(1024 * 1024)
	cases := []struct {
		name        string
		id          string
		wantState   string
		wantProg    float64
		wantDown    int64
		wantTotal   int64
		wantETA     int64
		wantError   string
		wantContent string
	}{
		{
			name:        "queue string megabytes",
			id:          "SABnzbd_nzo_queue1",
			wantState:   dlclient.StateDownloading,
			wantProg:    42,
			wantDown:    42 * mib,
			wantTotal:   100 * mib,
			wantETA:     83,
			wantContent: "/downloads/queue1",
		},
		{
			name:        "queue numeric megabytes",
			id:          "SABnzbd_nzo_queue2",
			wantState:   dlclient.StatePaused,
			wantProg:    10,
			wantDown:    20 * mib,
			wantTotal:   200 * mib,
			wantETA:     0,
			wantContent: "/downloads/queue2",
		},
		{
			name:        "queue zero size",
			id:          "SABnzbd_nzo_queue3",
			wantState:   dlclient.StateQueued,
			wantProg:    0,
			wantDown:    0,
			wantTotal:   0,
			wantETA:     0,
			wantContent: "/downloads/queue3",
		},
		{
			name:        "history completed",
			id:          "SABnzbd_nzo_hist1",
			wantState:   dlclient.StateCompleted,
			wantProg:    100,
			wantDown:    mib,
			wantTotal:   mib,
			wantETA:     0,
			wantContent: "/downloads/completed",
		},
		{
			name:        "history failed",
			id:          "SABnzbd_nzo_hist2",
			wantState:   dlclient.StateFailed,
			wantProg:    0,
			wantDown:    0,
			wantTotal:   2 * mib,
			wantError:   "Unpack failed",
			wantContent: "/downloads/failed",
		},
		{
			name:        "history transient",
			id:          "SABnzbd_nzo_hist3",
			wantState:   dlclient.StateDownloading,
			wantProg:    0,
			wantDown:    4096,
			wantTotal:   4096,
			wantContent: "/downloads/extracting",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job, ok := byID[tc.id]
			if !ok {
				t.Fatalf("no job with id %q", tc.id)
			}
			if job.State != tc.wantState {
				t.Errorf("State = %q, want %q", job.State, tc.wantState)
			}
			if job.Progress != tc.wantProg {
				t.Errorf("Progress = %v, want %v", job.Progress, tc.wantProg)
			}
			if job.Downloaded != tc.wantDown {
				t.Errorf("Downloaded = %d, want %d", job.Downloaded, tc.wantDown)
			}
			if job.Total != tc.wantTotal {
				t.Errorf("Total = %d, want %d", job.Total, tc.wantTotal)
			}
			if job.ETA != tc.wantETA {
				t.Errorf("ETA = %d, want %d", job.ETA, tc.wantETA)
			}
			if job.Error != tc.wantError {
				t.Errorf("Error = %q, want %q", job.Error, tc.wantError)
			}
			if job.ContentPath != tc.wantContent {
				t.Errorf("ContentPath = %q, want %q", job.ContentPath, tc.wantContent)
			}
			if job.SavePath != tc.wantContent {
				t.Errorf("SavePath = %q, want %q", job.SavePath, tc.wantContent)
			}
			if job.Category != "xbvr" {
				t.Errorf("Category = %q, want %q", job.Category, "xbvr")
			}
		})
	}
}

// TestListEmptyReturnsNonNilSlice guards the "nothing to report" case.
func TestListEmptyReturnsNonNilSlice(t *testing.T) {
	bodies := map[string]string{
		"empty arrays": `{"queue":{"slots":[]}}`,
		"null slots":   `{"queue":{"slots":null}}`,
		"missing key":  `{"queue":{}}`,
	}
	for name, queueBody := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Query().Get("mode") {
				case "queue":
					writeJSON(t, w, queueBody)
				case "history":
					writeJSON(t, w, `{"history":{"slots":[]}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			jobs, err := c.List(context.Background())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if jobs == nil {
				t.Fatal("List returned a nil slice, want a non-nil empty slice")
			}
			if len(jobs) != 0 {
				t.Fatalf("List returned %d jobs, want 0", len(jobs))
			}
		})
	}
}

// TestAddURL checks the addurl query and the returned job.
func TestAddURL(t *testing.T) {
	const nzbURL = "https://example.invalid/release.nzb"

	var (
		mu    sync.Mutex
		gotQ  string
		mode  string
		query = map[string]string{}
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mode = r.URL.Query().Get("mode")
		gotQ = r.URL.RawQuery
		query["name"] = r.URL.Query().Get("name")
		query["cat"] = r.URL.Query().Get("cat")
		query["apikey"] = r.URL.Query().Get("apikey")
		mu.Unlock()
		writeJSON(t, w, `{"status": true, "nzo_ids": ["SABnzbd_nzo_added"]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:      "ref-1",
		Name:     "Release Name",
		URL:      nzbURL,
		Category: "job-cat",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if mode != "addurl" {
		t.Errorf("mode = %q, want addurl", mode)
	}
	if query["name"] != nzbURL {
		t.Errorf("name = %q, want %q", query["name"], nzbURL)
	}
	if query["cat"] != "job-cat" {
		t.Errorf("cat = %q, want job-cat", query["cat"])
	}
	if query["apikey"] != testAPIKey {
		t.Errorf("apikey = %q, want %q", query["apikey"], testAPIKey)
	}
	if !strings.Contains(gotQ, "mode=addurl") {
		t.Errorf("raw query %q missing mode", gotQ)
	}

	if job.ID != "SABnzbd_nzo_added" {
		t.Errorf("ID = %q, want SABnzbd_nzo_added", job.ID)
	}
	if job.Ref != "ref-1" {
		t.Errorf("Ref = %q, want ref-1", job.Ref)
	}
	if job.Name != "Release Name" {
		t.Errorf("Name = %q, want Release Name", job.Name)
	}
	if job.State != dlclient.StateQueued {
		t.Errorf("State = %q, want %q", job.State, dlclient.StateQueued)
	}
	if job.Category != "job-cat" {
		t.Errorf("Category = %q, want job-cat", job.Category)
	}
}

// TestAddFile checks the multipart upload, including the nzbfile field name.
func TestAddFile(t *testing.T) {
	const body = "raw nzb body"

	var (
		mu       sync.Mutex
		fileName string
		fileBody string
		nzbName  string
		cat      string
		mode     string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mode = r.URL.Query().Get("mode")
		mu.Unlock()

		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("nzbfile")
		if err != nil {
			t.Errorf("nzbfile part missing: %v", err)
			http.Error(w, "no nzbfile", http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)

		mu.Lock()
		fileName = header.Filename
		fileBody = string(data)
		nzbName = r.FormValue("nzbname")
		cat = r.FormValue("cat")
		mu.Unlock()

		writeJSON(t, w, `{"status": true, "nzo_ids": ["SABnzbd_nzo_file"]}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:      "content-ref",
		Name:     "Content Release",
		Content:  []byte(body),
		Category: "job-cat",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if mode != "addfile" {
		t.Errorf("mode = %q, want addfile", mode)
	}
	if fileName != "file.nzb" {
		t.Errorf("upload filename = %q, want file.nzb", fileName)
	}
	if fileBody != body {
		t.Errorf("upload body = %q, want %q", fileBody, body)
	}
	if nzbName != "Content Release" {
		t.Errorf("nzbname = %q, want Content Release", nzbName)
	}
	if cat != "job-cat" {
		t.Errorf("cat = %q, want job-cat", cat)
	}
	if job.ID != "SABnzbd_nzo_file" {
		t.Errorf("ID = %q, want SABnzbd_nzo_file", job.ID)
	}
	if job.Ref != "content-ref" {
		t.Errorf("Ref = %q, want content-ref", job.Ref)
	}
}

// TestAddWithoutNzoIDs checks the best-effort placeholder SABnzbd's asynchronous
// add response can require.
func TestAddWithoutNzoIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, `{"status": true}`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:  "pending-ref",
		Name: "Pending Release",
		URL:  "https://example.invalid/pending.nzb",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if job.ID != "" {
		t.Errorf("placeholder ID = %q, want empty", job.ID)
	}
	if job.State != dlclient.StateQueued {
		t.Errorf("placeholder State = %q, want %q", job.State, dlclient.StateQueued)
	}
	if job.Ref != "pending-ref" {
		t.Errorf("placeholder Ref = %q, want pending-ref", job.Ref)
	}
}

// TestAddRequiresSource guards the empty request.
func TestAddRequiresSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be called, got mode %q", r.URL.Query().Get("mode"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.Add(context.Background(), dlclient.AddRequest{Name: "nothing"}); err == nil {
		t.Fatal("Add with no source returned nil error")
	}
}

// TestTest covers success, a bad API key reported with HTTP 200, a server error
// and an unreachable server.
func TestTest(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("mode"); got != "version" {
				t.Errorf("mode = %q, want version", got)
			}
			writeJSON(t, w, `{"version": "3.7.2"}`)
		}))
		defer srv.Close()

		if err := newTestClient(t, srv).Test(context.Background()); err != nil {
			t.Fatalf("Test: %v", err)
		}
	})

	t.Run("bad api key", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// SABnzbd reports authentication failure with HTTP 200.
			writeJSON(t, w, `{"status": false, "error": "API Key Incorrect"}`)
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "API Key Incorrect") {
			t.Fatalf("Test error = %v, want API key rejection", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("Test error = %v, want status 500", err)
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()

		c, err := New(dlclient.Config{URL: url, APIKey: testAPIKey})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Test(context.Background()); err == nil {
			t.Fatal("Test against a closed port returned nil error")
		}
	})
}

// TestErrorLeavesAPIKeyOut verifies a URL-derived error cannot leak the key.
func TestErrorLeavesAPIKeyOut(t *testing.T) {
	const key = "supersecretapikey"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c, err := New(dlclient.Config{URL: url, APIKey: key})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = c.Test(context.Background())
	if err == nil {
		t.Fatal("Test against a closed port returned nil error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

// TestBasicAuthApplied checks the optional reverse-proxy credentials.
func TestBasicAuthApplied(t *testing.T) {
	const user, pass = "proxy", "proxy-pass"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok := r.BasicAuth()
		if !ok || gotUser != user || gotPass != pass {
			t.Errorf("basic auth = %q/%q ok=%v, want %q/%q", gotUser, gotPass, ok, user, pass)
		}
		writeJSON(t, w, `{"version": "3.7.2"}`)
	}))
	defer srv.Close()

	c, err := New(dlclient.Config{URL: srv.URL, Username: user, Password: pass, APIKey: testAPIKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

// TestRemove covers a successful delete and the not-found mapping.
func TestRemove(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "deleted", body: `{"status": true}`, wantErr: nil},
		{name: "not found", body: `{"status": false, "error": "not found"}`, wantErr: dlclient.ErrNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				mode string
				name string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				mode = r.URL.Query().Get("mode")
				name = r.URL.Query().Get("name")
				mu.Unlock()
				writeJSON(t, w, tc.body)
			}))
			defer srv.Close()

			err := newTestClient(t, srv).Remove(context.Background(), "SABnzbd_nzo_x", true)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Remove: %v", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Remove error = %v, want %v", err, tc.wantErr)
			}

			mu.Lock()
			defer mu.Unlock()
			if mode != "delete" {
				t.Errorf("mode = %q, want delete", mode)
			}
			if name != "SABnzbd_nzo_x" {
				t.Errorf("name = %q, want SABnzbd_nzo_x", name)
			}
		})
	}

	t.Run("empty id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("server should not be called for an empty id")
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Remove(context.Background(), "  ", false)
		if !errors.Is(err, dlclient.ErrNotFound) {
			t.Fatalf("Remove error = %v, want ErrNotFound", err)
		}
	})

	t.Run("http 404 from an intermediary", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Remove(context.Background(), "SABnzbd_nzo_x", false)
		if !errors.Is(err, dlclient.ErrNotFound) {
			t.Fatalf("Remove error = %v, want ErrNotFound", err)
		}
	})

	t.Run("transport failure is not not-found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()

		c, err := New(dlclient.Config{URL: url, APIKey: testAPIKey})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		err = c.Remove(context.Background(), "SABnzbd_nzo_x", false)
		if err == nil || errors.Is(err, dlclient.ErrNotFound) {
			t.Fatalf("Remove error = %v, want a transport error rather than ErrNotFound", err)
		}
	})
}

// TestMalformedAndEmptyBodies verifies bad bodies are reported and never panic.
func TestMalformedAndEmptyBodies(t *testing.T) {
	t.Run("malformed queue", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("mode") {
			case "queue":
				writeJSON(t, w, "not json at all")
			case "history":
				writeJSON(t, w, `{"history":{"slots":[]}}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		if _, err := newTestClient(t, srv).List(context.Background()); err == nil {
			t.Fatal("List with a malformed queue body returned nil error")
		}
	})

	t.Run("empty bodies", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
		}))
		defer srv.Close()

		jobs, err := newTestClient(t, srv).List(context.Background())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if jobs == nil || len(jobs) != 0 {
			t.Fatalf("List returned %#v, want a non-nil empty slice", jobs)
		}
	})

	t.Run("null version body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, "null")
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no version") {
			t.Fatalf("Test error = %v, want a missing version error", err)
		}
	})
}

// TestStatusErrorTruncates checks the descriptive error helper.
func TestStatusErrorTruncates(t *testing.T) {
	long := strings.Repeat("x", maxErrorBody+100)
	err := statusError("queue", http.StatusBadRequest, []byte(long))
	msg := err.Error()
	if !strings.Contains(msg, "400") {
		t.Errorf("error %q missing status", msg)
	}
	if !strings.Contains(msg, "(truncated)") {
		t.Errorf("error %q missing truncation marker", msg)
	}
	if len(msg) > maxErrorBody+100 {
		t.Errorf("error message not truncated: %d bytes", len(msg))
	}
}

// TestJSONNumberAcceptsStringAndNumber pins the tolerant field type directly.
func TestJSONNumberAcceptsStringAndNumber(t *testing.T) {
	var out struct {
		StringValue jsonNumber `json:"s"`
		NumberValue jsonNumber `json:"n"`
		NullValue   jsonNumber `json:"x"`
		EmptyValue  jsonNumber `json:"e"`
	}
	body := `{"s": "1.5", "n": 2.5, "x": null, "e": ""}`
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.StringValue.value() != 1.5 {
		t.Errorf("string = %v, want 1.5", out.StringValue.value())
	}
	if out.NumberValue.value() != 2.5 {
		t.Errorf("number = %v, want 2.5", out.NumberValue.value())
	}
	if out.NullValue.value() != 0 || out.EmptyValue.value() != 0 {
		t.Errorf("null/empty = %v/%v, want 0/0", out.NullValue.value(), out.EmptyValue.value())
	}

	if err := json.Unmarshal([]byte(`{"s": "not-a-number"}`), &out); err == nil {
		t.Error("unmarshal of a non-numeric string returned nil error")
	}
}

// TestMegabytesConversion pins the MB-to-byte conversion, including the guard.
func TestMegabytesConversion(t *testing.T) {
	cases := []struct {
		in   jsonNumber
		want int64
	}{
		{0, 0},
		{-5, 0},
		{1, 1024 * 1024},
		{2.5, int64(2.5 * 1024 * 1024)},
	}
	for _, tc := range cases {
		if got := tc.in.megabytes(); got != tc.want {
			t.Errorf("megabytes(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestParseTimeLeft pins the "H:MM:SS" parser and its zero fallbacks.
func TestParseTimeLeft(t *testing.T) {
	cases := []struct {
		in   timeLeft
		want int64
	}{
		{"", 0},
		{"0:00:00", 0},
		{"0:01:23", 83},
		{"1:00:00", 3600},
		{"12:34:56", 45296},
		{"90", 90},
		{"garbage", 0},
		{"1:2", 0},
		{"-1:00:00", 0},
	}
	for _, tc := range cases {
		if got := parseTimeLeft(tc.in); got != tc.want {
			t.Errorf("parseTimeLeft(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestStateTables pins the queue and history status mappings.
func TestStateTables(t *testing.T) {
	queueCases := map[string]string{
		"Downloading":  dlclient.StateDownloading,
		"Running":      dlclient.StateDownloading,
		"Paused":       dlclient.StatePaused,
		"Queued":       dlclient.StateQueued,
		"SomethingNew": dlclient.StateDownloading,
		"":             dlclient.StateDownloading,
	}
	for status, want := range queueCases {
		if got := queueState(status); got != want {
			t.Errorf("queueState(%q) = %q, want %q", status, got, want)
		}
	}

	historyCases := map[string]string{
		"Completed":  dlclient.StateCompleted,
		"Failed":     dlclient.StateFailed,
		"Running":    dlclient.StateDownloading,
		"Queued":     dlclient.StateQueued,
		"Paused":     dlclient.StatePaused,
		"Extracting": dlclient.StateDownloading,
		"Verifying":  dlclient.StateDownloading,
		"Repairing":  dlclient.StateDownloading,
		"":           dlclient.StateDownloading,
	}
	for status, want := range historyCases {
		if got := historyState(status); got != want {
			t.Errorf("historyState(%q) = %q, want %q", status, got, want)
		}
	}
}

// TestClampProgress pins the percentage clamp.
func TestClampProgress(t *testing.T) {
	cases := map[float64]float64{-10: 0, 0: 0, 42: 42, 100: 100, 120: 100}
	for in, want := range cases {
		if got := clampProgress(in); got != want {
			t.Errorf("clampProgress(%v) = %v, want %v", in, got, want)
		}
	}
}

// TestNewRequiresURL guards the zero-value config.
func TestNewRequiresURL(t *testing.T) {
	if _, err := New(dlclient.Config{}); err == nil {
		t.Fatal("New with empty URL returned nil error")
	}
	if _, err := New(dlclient.Config{URL: "   "}); err == nil {
		t.Fatal("New with blank URL returned nil error")
	}
}

// TestRegistered checks that the init registration is in place and reports NZB.
func TestRegistered(t *testing.T) {
	c, err := dlclient.New(dlclient.Config{Client: clientID, URL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("dlclient.New(%q): %v", clientID, err)
	}
	if c.Kind() != dlclient.KindNZB {
		t.Errorf("Kind = %q, want %q", c.Kind(), dlclient.KindNZB)
	}
	if c.Name() != "SABnzbd" {
		t.Errorf("Name = %q, want SABnzbd", c.Name())
	}
	if c.ID() != "" {
		t.Errorf("ID = %q, want empty", c.ID())
	}
}

// TestTrailingSlashTrimmed verifies base URL normalisation.
func TestTrailingSlashTrimmed(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		writeJSON(t, w, `{"version": "3.7.2"}`)
	}))
	defer srv.Close()

	c, err := New(dlclient.Config{URL: srv.URL + "///", APIKey: testAPIKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if hitPath != "/api" {
		t.Errorf("hit path = %q, want /api", hitPath)
	}
}
