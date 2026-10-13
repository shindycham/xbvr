package nzbget

import (
	"context"
	"encoding/base64"
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

// groupTestJSON is a representative listgroups result: a downloading group, a
// paused group with an unknown size and one whose NZBID arrives as a string.
const groupTestJSON = `[
	{
		"NZBID": 1,
		"NZBName": "Downloading Release",
		"Status": "DOWNLOADING",
		"DownloadedSizeMB": 50,
		"FileSizeMB": 100,
		"RemainingSizeMB": 50,
		"DestDir": "/downloads/g1",
		"Category": "xbvr",
		"Health": 1000
	},
	{
		"NZBID": 2,
		"NZBName": "Paused Release",
		"Status": "PAUSED",
		"DownloadedSizeMB": 0,
		"FileSizeMB": 0,
		"RemainingSizeMB": 0,
		"DestDir": "",
		"Category": "xbvr",
		"Health": 0
	},
	{
		"NZBID": "3",
		"NZBName": "String Id Release",
		"Status": "QUEUED",
		"DownloadedSizeMB": 0,
		"FileSizeMB": 0,
		"RemainingSizeMB": 0,
		"DestDir": "/downloads/g3",
		"Category": "xbvr"
	}
]`

// historyTestJSON is a representative history result. The SUCCESS entry is the
// regression that matters: it has left the active queue and lives only here.
const historyTestJSON = `[
	{
		"NZBID": 10,
		"Name": "Completed Release",
		"Status": "SUCCESS",
		"FileSizeMB": 1,
		"DestDir": "/downloads/completed",
		"Category": "xbvr"
	},
	{
		"NZBID": 11,
		"Name": "Failed Release",
		"Status": "FAILURE",
		"FileSizeMB": 2,
		"DestDir": "/downloads/failed",
		"Category": "xbvr",
		"FailMessage": "Unpack failed"
	},
	{
		"NZBID": 12,
		"Name": "Deleted Release",
		"Status": "DELETED",
		"FileSizeLo": 1024,
		"FileSizeHi": 0,
		"DestDir": "/downloads/deleted",
		"Category": "xbvr"
	},
	{
		"NZBID": 13,
		"Name": "Warning Release",
		"Status": "WARNING",
		"FileSizeMB": 1,
		"DestDir": "/downloads/warn",
		"Category": "xbvr"
	}
]`

// rpcCall is the decoded JSON-RPC request a test server received.
type rpcCall struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     int               `json:"id"`
}

func newTestClient(t *testing.T, srv *httptest.Server) dlclient.Client {
	t.Helper()
	c, err := New(dlclient.Config{
		ID:       "test-client",
		URL:      srv.URL,
		Username: "nzbget",
		Password: "secret",
		Category: "xbvr",
		SavePath: "/default-save",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// decodeCall reads the JSON-RPC request body.
func decodeCall(t *testing.T, r *http.Request) rpcCall {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", r.Method)
	}
	if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type = %q, want application/json", got)
	}
	var call rpcCall
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		t.Errorf("decode rpc request: %v", err)
	}
	if call.ID != rpcID {
		t.Errorf("rpc id = %d, want %d", call.ID, rpcID)
	}
	return call
}

// decodeParams turns the raw params into plain Go values for assertions.
func decodeParams(t *testing.T, call rpcCall) []any {
	t.Helper()
	params := make([]any, 0, len(call.Params))
	for _, raw := range call.Params {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("decode param: %v", err)
		}
		params = append(params, v)
	}
	return params
}

// writeResult writes a successful JSON-RPC reply.
func writeResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"result": result, "id": rpcID}); err != nil {
		t.Errorf("encode result: %v", err)
	}
}

// writeRPCError writes a JSON-RPC error object with HTTP 200, as NZBGet does.
func writeRPCError(t *testing.T, w http.ResponseWriter, message string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": 1, "message": message},
		"id":    rpcID,
	}); err != nil {
		t.Errorf("encode rpc error: %v", err)
	}
}

// writeRaw writes a body verbatim, for shapes the helpers cannot produce.
func writeRaw(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write body: %v", err)
	}
}

// TestListMergesGroupsAndHistory is the regression that matters most: a finished
// NZBGet download lives only in the history, so List must merge both lists and
// report the successful entry as StateCompleted with the unpacked path.
func TestListMergesGroupsAndHistory(t *testing.T) {
	var mu sync.Mutex
	var methods []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeCall(t, r)
		mu.Lock()
		methods = append(methods, call.Method)
		mu.Unlock()

		switch call.Method {
		case "listgroups":
			writeRaw(t, w, `{"result": `+groupTestJSON+`, "id": 1}`)
		case "history":
			writeRaw(t, w, `{"result": `+historyTestJSON+`, "id": 1}`)
		default:
			t.Errorf("unexpected method %q", call.Method)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	jobs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 7 {
		t.Fatalf("List returned %d jobs, want 7 (3 groups + 4 history)", len(jobs))
	}

	mu.Lock()
	gotMethods := strings.Join(methods, ",")
	mu.Unlock()
	if gotMethods != "listgroups,history" {
		t.Errorf("methods = %q, want %q", gotMethods, "listgroups,history")
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
		wantError   string
		wantContent string
	}{
		{
			name:        "group downloading",
			id:          "1",
			wantState:   dlclient.StateDownloading,
			wantProg:    50,
			wantDown:    50 * mib,
			wantTotal:   100 * mib,
			wantContent: "/downloads/g1",
		},
		{
			name:        "group zero size falls back to configured save path",
			id:          "2",
			wantState:   dlclient.StatePaused,
			wantProg:    0,
			wantDown:    0,
			wantTotal:   0,
			wantContent: "/default-save",
		},
		{
			name:        "group string id",
			id:          "3",
			wantState:   dlclient.StateQueued,
			wantProg:    0,
			wantDown:    0,
			wantTotal:   0,
			wantContent: "/downloads/g3",
		},
		{
			name:        "history success",
			id:          "10",
			wantState:   dlclient.StateCompleted,
			wantProg:    100,
			wantDown:    mib,
			wantTotal:   mib,
			wantContent: "/downloads/completed",
		},
		{
			name:        "history failure",
			id:          "11",
			wantState:   dlclient.StateFailed,
			wantProg:    0,
			wantDown:    0,
			wantTotal:   2 * mib,
			wantError:   "Unpack failed",
			wantContent: "/downloads/failed",
		},
		{
			name:        "history deleted",
			id:          "12",
			wantState:   dlclient.StateFailed,
			wantProg:    0,
			wantTotal:   1024,
			wantError:   "DELETED",
			wantContent: "/downloads/deleted",
		},
		{
			name:        "history warning is transient",
			id:          "13",
			wantState:   dlclient.StateDownloading,
			wantProg:    0,
			wantDown:    mib,
			wantTotal:   mib,
			wantContent: "/downloads/warn",
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

// TestListHistoryBareArray checks the older NZBGet shape where history answers
// with a bare array rather than a result envelope.
func TestListHistoryBareArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeCall(t, r)
		switch call.Method {
		case "listgroups":
			writeResult(t, w, []any{})
		case "history":
			writeRaw(t, w, `[{"NZBID": 7, "Name": "Bare Release", "Status": "SUCCESS", "FileSizeMB": 1, "DestDir": "/bare", "Category": "xbvr"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	jobs, err := newTestClient(t, srv).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("List returned %d jobs, want 1", len(jobs))
	}
	if jobs[0].ID != "7" {
		t.Errorf("ID = %q, want 7", jobs[0].ID)
	}
	if jobs[0].State != dlclient.StateCompleted {
		t.Errorf("State = %q, want %q", jobs[0].State, dlclient.StateCompleted)
	}
	if jobs[0].ContentPath != "/bare" {
		t.Errorf("ContentPath = %q, want /bare", jobs[0].ContentPath)
	}
}

// TestListEmptyReturnsNonNilSlice guards the "nothing to report" case, including
// the null result older builds return.
func TestListEmptyReturnsNonNilSlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeCall(t, r)
		writeRaw(t, w, `{"result": null, "id": 1}`)
	}))
	defer srv.Close()

	jobs, err := newTestClient(t, srv).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if jobs == nil {
		t.Fatal("List returned a nil slice, want a non-nil empty slice")
	}
	if len(jobs) != 0 {
		t.Fatalf("List returned %d jobs, want 0", len(jobs))
	}
}

// TestAddURL checks the append params for the URL case and the returned job.
func TestAddURL(t *testing.T) {
	const nzbURL = "https://example.invalid/release.nzb"

	var (
		mu     sync.Mutex
		method string
		params []any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "nzbget" || pass != "secret" {
			t.Errorf("basic auth = %q/%q ok=%v, want nzbget/secret", user, pass, ok)
		}
		call := decodeCall(t, r)
		mu.Lock()
		method = call.Method
		params = decodeParams(t, call)
		mu.Unlock()
		writeResult(t, w, 5)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:      "ref-1",
		Name:     "Release Name",
		URL:      nzbURL,
		Category: "job-cat",
		Paused:   true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if method != "append" {
		t.Errorf("method = %q, want append", method)
	}
	if len(params) != 9 {
		t.Fatalf("append params = %d, want 9", len(params))
	}
	want := []any{"Release Name", nzbURL, "job-cat", float64(0), false, true, "", float64(0), "FORCE"}
	for i, w := range want {
		if params[i] != w {
			t.Errorf("param %d = %#v, want %#v", i, params[i], w)
		}
	}

	if job.ID != "5" {
		t.Errorf("ID = %q, want 5", job.ID)
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
	if job.SavePath != "/default-save" {
		t.Errorf("SavePath = %q, want /default-save", job.SavePath)
	}
}

// TestAddContentUsesBase64 checks that raw .nzb bytes are base64 encoded.
func TestAddContentUsesBase64(t *testing.T) {
	content := []byte("raw nzb \x00 body")

	var (
		mu     sync.Mutex
		params []any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := decodeCall(t, r)
		mu.Lock()
		params = decodeParams(t, call)
		mu.Unlock()
		writeResult(t, w, 9)
	}))
	defer srv.Close()

	job, err := newTestClient(t, srv).Add(context.Background(), dlclient.AddRequest{
		Ref:     "content-ref",
		Name:    "Content Release",
		Content: content,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	wantContent := base64.StdEncoding.EncodeToString(content)
	if params[1] != wantContent {
		t.Errorf("content param = %v, want base64 %q", params[1], wantContent)
	}
	if params[0] != "Content Release" {
		t.Errorf("name param = %v, want Content Release", params[0])
	}
	if params[2] != "xbvr" {
		t.Errorf("category param = %v, want the configured category xbvr", params[2])
	}
	if job.ID != "9" {
		t.Errorf("ID = %q, want 9", job.ID)
	}
}

// TestAddRequiresSource guards the empty request.
func TestAddRequiresSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for an add with no source")
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).Add(context.Background(), dlclient.AddRequest{Name: "nothing"}); err == nil {
		t.Fatal("Add with no source returned nil error")
	}
}

// TestTest covers success, a JSON-RPC error returned with HTTP 200, a server
// error, an empty body and an unreachable server.
func TestTest(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			call := decodeCall(t, r)
			if call.Method != "version" {
				t.Errorf("method = %q, want version", call.Method)
			}
			writeResult(t, w, "21.1")
		}))
		defer srv.Close()

		if err := newTestClient(t, srv).Test(context.Background()); err != nil {
			t.Fatalf("Test: %v", err)
		}
	})

	t.Run("rpc error with http 200", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeRPCError(t, w, "Authorization failed")
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "Authorization failed") {
			t.Fatalf("Test error = %v, want the RPC error message", err)
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

	t.Run("empty result", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeRaw(t, w, `{"result": null, "id": 1}`)
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("Test error = %v, want an empty version error", err)
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()

		c, err := New(dlclient.Config{URL: url})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Test(context.Background()); err == nil {
			t.Fatal("Test against a closed port returned nil error")
		}
	})
}

// TestRemove covers deleting from the active queue, falling back to the history
// and the not-found mapping.
func TestRemove(t *testing.T) {
	t.Run("active queue", func(t *testing.T) {
		var (
			mu    sync.Mutex
			calls []string
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			call := decodeCall(t, r)
			params := decodeParams(t, call)
			mu.Lock()
			calls = append(calls, call.Method)
			mu.Unlock()
			if params[0] == "GroupFinalDelete" {
				writeResult(t, w, true)
				return
			}
			t.Errorf("unexpected editqueue command %v", params[0])
			writeRPCError(t, w, "not found")
		}))
		defer srv.Close()

		if err := newTestClient(t, srv).Remove(context.Background(), "7", true); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 1 {
			t.Errorf("editqueue calls = %d, want 1", len(calls))
		}
	})

	t.Run("history fallback", func(t *testing.T) {
		var (
			mu       sync.Mutex
			commands []string
			ids      []any
		)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			call := decodeCall(t, r)
			params := decodeParams(t, call)
			command, _ := params[0].(string)
			mu.Lock()
			commands = append(commands, command)
			ids = append(ids, params[2])
			mu.Unlock()

			if params[1] != "" {
				t.Errorf("editqueue id param = %v, want empty string", params[1])
			}
			if command == "GroupFinalDelete" {
				writeRPCError(t, w, "Could not find group")
				return
			}
			if command != "HistoryFinalDelete" {
				t.Errorf("unexpected command %q", command)
			}
			writeResult(t, w, true)
		}))
		defer srv.Close()

		if err := newTestClient(t, srv).Remove(context.Background(), "42", true); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if strings.Join(commands, ",") != "GroupFinalDelete,HistoryFinalDelete" {
			t.Errorf("commands = %v, want the history fallback", commands)
		}
		want := []any{float64(42)}
		for _, got := range ids {
			if len(got.([]any)) != 1 || got.([]any)[0] != want[0] {
				t.Errorf("editqueue ids = %v, want %v", got, want)
			}
		}
	})

	t.Run("not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			decodeCall(t, r)
			writeRPCError(t, w, "Could not find group")
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Remove(context.Background(), "99", false)
		if !errors.Is(err, dlclient.ErrNotFound) {
			t.Fatalf("Remove error = %v, want ErrNotFound", err)
		}
	})

	t.Run("empty id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("server should not be called for an empty id")
		}))
		defer srv.Close()

		if err := newTestClient(t, srv).Remove(context.Background(), "  ", false); !errors.Is(err, dlclient.ErrNotFound) {
			t.Fatalf("Remove error = %v, want ErrNotFound", err)
		}
	})

	t.Run("non-numeric id", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("server should not be called for a non-numeric id")
		}))
		defer srv.Close()

		if err := newTestClient(t, srv).Remove(context.Background(), "not-an-id", false); !errors.Is(err, dlclient.ErrNotFound) {
			t.Fatalf("Remove error = %v, want ErrNotFound", err)
		}
	})
}

// TestMalformedBodies verifies bad bodies are reported and never panic.
func TestMalformedBodies(t *testing.T) {
	t.Run("malformed response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			decodeCall(t, r)
			writeRaw(t, w, "not json at all")
		}))
		defer srv.Close()

		if _, err := newTestClient(t, srv).List(context.Background()); err == nil {
			t.Fatal("List with a malformed body returned nil error")
		}
	})

	t.Run("empty body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			decodeCall(t, r)
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
}

// TestAPIKeyNotSentAsQuery checks the deliberate decision to use basic auth
// rather than the newer API-key-in-path scheme.
func TestAPIKeyNotSentAsQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jsonrpc" {
			t.Errorf("path = %q, want /jsonrpc", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want empty", r.URL.RawQuery)
		}
		writeResult(t, w, "21.1")
	}))
	defer srv.Close()

	c, err := New(dlclient.Config{URL: srv.URL, APIKey: "should-not-be-sent"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

// TestStatusErrorTruncates checks the descriptive error helper.
func TestStatusErrorTruncates(t *testing.T) {
	long := strings.Repeat("x", maxErrorBody+100)
	err := statusError("listgroups", http.StatusBadRequest, []byte(long))
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

// TestProgressPercent pins the progress computation and its zero-size guard.
func TestProgressPercent(t *testing.T) {
	cases := []struct {
		total, remaining, want float64
	}{
		{100, 50, 50},
		{100, 0, 100},
		{100, 100, 0},
		{100, 150, 0},
		{100, -10, 100},
		{0, 0, 0},
		{0, 10, 0},
		{-5, -5, 0},
	}
	for _, tc := range cases {
		if got := progressPercent(tc.total, tc.remaining); got != tc.want {
			t.Errorf("progressPercent(%v, %v) = %v, want %v", tc.total, tc.remaining, got, tc.want)
		}
	}
}

// TestMegabytesToBytes pins the MB-to-byte conversion, including the guard.
func TestMegabytesToBytes(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{0, 0},
		{-5, 0},
		{1, 1024 * 1024},
		{2.5, int64(2.5 * 1024 * 1024)},
	}
	for _, tc := range cases {
		if got := megabytesToBytes(tc.in); got != tc.want {
			t.Errorf("megabytesToBytes(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestTotalBytesFallbacks pins the history size field precedence.
func TestTotalBytesFallbacks(t *testing.T) {
	cases := []struct {
		name string
		slot historySlot
		want int64
	}{
		{"megabytes", historySlot{FileSizeMB: 2, FileSizeLo: 1}, 2 * 1024 * 1024},
		{"words", historySlot{FileSizeLo: 4096}, 4096},
		{"high word", historySlot{FileSizeHi: 1}, 1 << 32},
		{"nothing", historySlot{}, 0},
	}
	for _, tc := range cases {
		if got := tc.slot.totalBytes(); got != tc.want {
			t.Errorf("%s: totalBytes() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestJobStateTable pins the status mapping.
func TestJobStateTable(t *testing.T) {
	cases := map[string]string{
		"SUCCESS":           dlclient.StateCompleted,
		"FAILURE":           dlclient.StateFailed,
		"DELETED":           dlclient.StateFailed,
		"PAUSED":            dlclient.StatePaused,
		"QUEUED":            dlclient.StateQueued,
		"DOWNLOADING":       dlclient.StateDownloading,
		"LOADING_PARS":      dlclient.StateDownloading,
		"VERIFYING_SOURCES": dlclient.StateDownloading,
		"REPAIRING":         dlclient.StateDownloading,
		"UNPACKING":         dlclient.StateDownloading,
		"MOVING":            dlclient.StateDownloading,
		"WARNING":           dlclient.StateDownloading,
		"":                  dlclient.StateDownloading,
	}
	for status, want := range cases {
		if got := jobState(status); got != want {
			t.Errorf("jobState(%q) = %q, want %q", status, got, want)
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
	c, err := dlclient.New(dlclient.Config{Client: clientID, URL: "http://127.0.0.1:6789"})
	if err != nil {
		t.Fatalf("dlclient.New(%q): %v", clientID, err)
	}
	if c.Kind() != dlclient.KindNZB {
		t.Errorf("Kind = %q, want %q", c.Kind(), dlclient.KindNZB)
	}
	if c.Name() != "NZBGet" {
		t.Errorf("Name = %q, want NZBGet", c.Name())
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
		writeResult(t, w, "21.1")
	}))
	defer srv.Close()

	c, err := New(dlclient.Config{URL: srv.URL + "///"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if hitPath != "/jsonrpc" {
		t.Errorf("hit path = %q, want /jsonrpc", hitPath)
	}
}
