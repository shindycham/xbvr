package qbittorrent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xbapps/xbvr/pkg/dlclient"
)

const (
	testUser = "admin"
	testPass = "secret"
	testSID  = "test-session-id"
)

// torrentListJSON is the representative /api/v2/torrents/info payload used by
// the listing test: one downloading, one seeding/completed and one errored.
const torrentListJSON = `[
	{
		"hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"name": "Downloading Release",
		"state": "downloading",
		"category": "xbvr",
		"tags": "xbvr-ref-1, public",
		"save_path": "/downloads",
		"content_path": "/downloads/Downloading Release",
		"magnet_uri": "magnet:?xt=urn:btih:aaaa",
		"progress": 0.42,
		"size": 1000,
		"downloaded": 420,
		"dlspeed": 1234,
		"eta": 120,
		"ratio": 0.25,
		"amount_left": 580
	},
	{
		"hash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"name": "Seeding Release",
		"state": "uploading",
		"category": "xbvr",
		"tags": "xbvr-ref-2",
		"save_path": "/downloads",
		"content_path": "/downloads/Seeding Release",
		"magnet_uri": "magnet:?xt=urn:btih:bbbb",
		"progress": 1.0,
		"size": 2000,
		"downloaded": 2000,
		"dlspeed": 0,
		"eta": 8640000,
		"ratio": 2.5,
		"amount_left": 0
	},
	{
		"hash": "cccccccccccccccccccccccccccccccccccccccc",
		"name": "Errored Release",
		"state": "error",
		"category": "xbvr",
		"tags": "unrelated",
		"save_path": "/downloads",
		"content_path": "/downloads/Errored Release",
		"magnet_uri": "magnet:?xt=urn:btih:cccc",
		"progress": 0.1,
		"size": 500,
		"downloaded": 50,
		"dlspeed": 0,
		"eta": -1,
		"ratio": 0,
		"amount_left": 450
	}
]`

// loginHandler serves a successful qBittorrent login and sets the SID cookie.
func loginHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("login: parse form: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("username") != testUser || r.Form.Get("password") != testPass {
			io.WriteString(w, "Fails.")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: testSID, Path: "/"})
		io.WriteString(w, "Ok.")
	}
}

// requireSID enforces that authenticated endpoints received the session cookie.
func requireSID(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	cookie, err := r.Cookie("SID")
	if err != nil || cookie.Value != testSID {
		t.Errorf("%s: missing or wrong SID cookie: %v", r.URL.Path, err)
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func newTestClient(t *testing.T, srv *httptest.Server) dlclient.Client {
	t.Helper()
	c, err := New(dlclient.Config{
		ID:       "test-client",
		URL:      srv.URL,
		Username: testUser,
		Password: testPass,
		Category: "xbvr",
		SavePath: "/default-save",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestListNormalizesTorrents covers login plus List, asserting the normalised
// state, progress, ref extraction, ETA clamping and byte counts.
func TestListNormalizesTorrents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginHandler(t)(w, r)
		case "/api/v2/app/version":
			if !requireSID(t, w, r) {
				return
			}
			io.WriteString(w, "v4.6.0")
		case "/api/v2/torrents/info":
			if !requireSID(t, w, r) {
				return
			}
			if got := r.URL.Query().Get("category"); got != "xbvr" {
				t.Errorf("torrents/info category = %q, want %q", got, "xbvr")
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, torrentListJSON)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}

	jobs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("List returned %d jobs, want 3", len(jobs))
	}

	byID := make(map[string]dlclient.Job, len(jobs))
	for _, j := range jobs {
		byID[j.ID] = j
	}

	cases := []struct {
		name        string
		id          string
		wantState   string
		wantRef     string
		wantProg    float64
		wantDown    int64
		wantTotal   int64
		wantSpeed   int64
		wantETA     int64
		wantRatio   float64
		wantSeeding bool
		wantError   string
	}{
		{
			name:      "downloading",
			id:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			wantState: dlclient.StateDownloading,
			wantRef:   "ref-1",
			wantProg:  42,
			wantDown:  420,
			wantTotal: 1000,
			wantSpeed: 1234,
			wantETA:   120,
			wantRatio: 0.25,
		},
		{
			name:        "seeding",
			id:          "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			wantState:   dlclient.StateCompleted,
			wantRef:     "ref-2",
			wantProg:    100,
			wantDown:    2000,
			wantTotal:   2000,
			wantETA:     0,
			wantRatio:   2.5,
			wantSeeding: true,
		},
		{
			name:      "errored",
			id:        "cccccccccccccccccccccccccccccccccccccccc",
			wantState: dlclient.StateFailed,
			wantRef:   "",
			wantProg:  10,
			wantDown:  50,
			wantTotal: 500,
			wantETA:   0,
			wantError: "error",
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
			if job.Ref != tc.wantRef {
				t.Errorf("Ref = %q, want %q", job.Ref, tc.wantRef)
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
			if job.Speed != tc.wantSpeed {
				t.Errorf("Speed = %d, want %d", job.Speed, tc.wantSpeed)
			}
			if job.ETA != tc.wantETA {
				t.Errorf("ETA = %d, want %d", job.ETA, tc.wantETA)
			}
			if job.Ratio != tc.wantRatio {
				t.Errorf("Ratio = %v, want %v", job.Ratio, tc.wantRatio)
			}
			if job.Seeding != tc.wantSeeding {
				t.Errorf("Seeding = %v, want %v", job.Seeding, tc.wantSeeding)
			}
			if job.Error != tc.wantError {
				t.Errorf("Error = %q, want %q", job.Error, tc.wantError)
			}
			if job.SavePath != "/downloads" {
				t.Errorf("SavePath = %q, want %q", job.SavePath, "/downloads")
			}
			if job.Category != "xbvr" {
				t.Errorf("Category = %q, want %q", job.Category, "xbvr")
			}
			if job.ContentPath == "" {
				t.Error("ContentPath is empty")
			}
		})
	}
}

// TestNormalizeStateTable pins the full state mapping, including the fallback.
func TestNormalizeStateTable(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{"error", dlclient.StateFailed},
		{"missingFiles", dlclient.StateFailed},
		{"unknown", dlclient.StateFailed},
		{"uploading", dlclient.StateCompleted},
		{"pausedUP", dlclient.StateCompleted},
		{"queuedUP", dlclient.StateCompleted},
		{"stalledUP", dlclient.StateCompleted},
		{"checkingUP", dlclient.StateCompleted},
		{"forcedUP", dlclient.StateCompleted},
		{"downloading", dlclient.StateDownloading},
		{"metaDL", dlclient.StateDownloading},
		{"forcedDL", dlclient.StateDownloading},
		{"checkingDL", dlclient.StateDownloading},
		{"allocating", dlclient.StateDownloading},
		{"moving", dlclient.StateDownloading},
		{"stalledDL", dlclient.StateStalled},
		{"pausedDL", dlclient.StatePaused},
		{"queuedDL", dlclient.StateQueued},
		{"somethingElse", dlclient.StateQueued},
		{"", dlclient.StateQueued},
	}
	for _, tc := range cases {
		if got := normalizeState(tc.state); got != tc.want {
			t.Errorf("normalizeState(%q) = %q, want %q", tc.state, got, tc.want)
		}
	}
}

// TestAddMagnet asserts the multipart fields sent for a magnet add and that the
// returned job is resolved from the torrent listing.
func TestAddMagnet(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:deadbeef&dn=Test+Release"

	var (
		mu      sync.Mutex
		gotURLs string
		gotTags string
		gotCat  string
		gotSave string
		gotPaus string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginHandler(t)(w, r)
		case "/api/v2/torrents/add":
			if !requireSID(t, w, r) {
				return
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse multipart: %v", err)
				http.Error(w, "bad multipart", http.StatusBadRequest)
				return
			}
			mu.Lock()
			gotURLs = r.FormValue("urls")
			gotTags = r.FormValue("tags")
			gotCat = r.FormValue("category")
			gotSave = r.FormValue("savepath")
			gotPaus = r.FormValue("paused")
			mu.Unlock()
			io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			if !requireSID(t, w, r) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{
				"hash": "resolved-hash",
				"name": "Test Release",
				"state": "downloading",
				"category": "job-cat",
				"tags": "xbvr-ref-99",
				"progress": 0.05,
				"size": 4096,
				"downloaded": 204,
				"dlspeed": 512,
				"eta": 30,
				"ratio": 0,
				"amount_left": 3892
			}]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:      "ref-99",
		Name:     "Test Release",
		Magnet:   magnet,
		SavePath: "/job-save",
		Category: "job-cat",
		Paused:   true,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotURLs != magnet {
		t.Errorf("urls = %q, want %q", gotURLs, magnet)
	}
	if gotTags != "xbvr-ref-99" {
		t.Errorf("tags = %q, want %q", gotTags, "xbvr-ref-99")
	}
	if gotCat != "job-cat" {
		t.Errorf("category = %q, want %q", gotCat, "job-cat")
	}
	if gotSave != "/job-save" {
		t.Errorf("savepath = %q, want %q", gotSave, "/job-save")
	}
	if gotPaus != "true" {
		t.Errorf("paused = %q, want %q", gotPaus, "true")
	}

	if job.ID != "resolved-hash" {
		t.Errorf("resolved ID = %q, want %q", job.ID, "resolved-hash")
	}
	if job.Ref != "ref-99" {
		t.Errorf("resolved Ref = %q, want %q", job.Ref, "ref-99")
	}
	if job.State != dlclient.StateDownloading {
		t.Errorf("resolved State = %q, want %q", job.State, dlclient.StateDownloading)
	}
}

// TestAddFallsBackToRequestWhenNotListed checks the best-effort placeholder when
// the client has not registered the torrent yet.
func TestAddFallsBackToRequestWhenNotListed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginHandler(t)(w, r)
		case "/api/v2/torrents/add":
			io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:    "pending-ref",
		Name:   "Pending Release",
		Magnet: "magnet:?xt=urn:btih:pending",
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
		t.Errorf("placeholder Ref = %q, want %q", job.Ref, "pending-ref")
	}
}

// TestAddUploadsTorrentContent checks the torrents file part for raw content.
func TestAddUploadsTorrentContent(t *testing.T) {
	var (
		mu       sync.Mutex
		fileName string
		fileBody string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginHandler(t)(w, r)
		case "/api/v2/torrents/add":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse multipart: %v", err)
				return
			}
			file, header, err := r.FormFile("torrents")
			if err != nil {
				t.Errorf("torrents part missing: %v", err)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			mu.Lock()
			fileName = header.Filename
			fileBody = string(data)
			mu.Unlock()
			io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:     "content-ref",
		Content: []byte("d4:infod..."),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if fileName != "file.torrent" {
		t.Errorf("upload filename = %q, want %q", fileName, "file.torrent")
	}
	if fileBody != "d4:infod..." {
		t.Errorf("upload body = %q, want %q", fileBody, "d4:infod...")
	}
}

// TestAddRejections covers the failure bodies and statuses qBittorrent returns.
func TestAddRejections(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantErrSub string
	}{
		{name: "fails body", status: http.StatusOK, body: "Fails.", wantErrSub: "Fails."},
		{name: "forbidden", status: http.StatusForbidden, body: "Forbidden", wantErrSub: "403"},
		{name: "unsupported media", status: http.StatusUnsupportedMediaType, body: "Unsupported Media Type", wantErrSub: "415"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					loginHandler(t)(w, r)
				case "/api/v2/torrents/add":
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			_, err := c.Add(context.Background(), dlclient.AddRequest{Magnet: "magnet:?xt=urn:btih:x"})
			if err == nil {
				t.Fatal("Add returned nil error, want failure")
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

// TestAddRequiresSource guards the empty-request case.
func TestAddRequiresSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loginHandler(t)(w, r)
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.Add(context.Background(), dlclient.AddRequest{Name: "nothing"}); err == nil {
		t.Fatal("Add with no source returned nil error")
	}
}

// TestTestFailures covers credential failure, a 500 response and an
// unreachable server.
func TestTestFailures(t *testing.T) {
	t.Run("bad credentials", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "Fails.")
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		err := c.Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "authentication failed") {
			t.Fatalf("Test error = %v, want authentication failure", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		err := c.Test(context.Background())
		if err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("Test error = %v, want status 500", err)
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close() // no listener on this port any more

		c, err := New(dlclient.Config{URL: url, Username: testUser, Password: testPass})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Test(context.Background()); err == nil {
			t.Fatal("Test against a closed port returned nil error")
		}
	})
}

// TestVersionChecksAuthentication ensures Test requires authentication, not just
// a reachable version endpoint.
func TestVersionChecksAuthentication(t *testing.T) {
	var (
		mu         sync.Mutex
		versionHit bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginHandler(t)(w, r)
		case "/api/v2/app/version":
			if !requireSID(t, w, r) {
				return
			}
			mu.Lock()
			versionHit = true
			mu.Unlock()
			io.WriteString(w, "v4.6.0")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !versionHit {
		t.Error("version endpoint was never called")
	}
}

// TestRemove covers the deleteFiles flag and the not-found mapping.
func TestRemove(t *testing.T) {
	cases := []struct {
		name       string
		deleteData bool
		wantFiles  string
	}{
		{name: "with data", deleteData: true, wantFiles: "true"},
		{name: "keep data", deleteData: false, wantFiles: "false"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu       sync.Mutex
				gotHash  string
				gotFiles string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/auth/login":
					loginHandler(t)(w, r)
				case "/api/v2/torrents/delete":
					if !requireSID(t, w, r) {
						return
					}
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse form: %v", err)
						return
					}
					mu.Lock()
					gotHash = r.Form.Get("hashes")
					gotFiles = r.Form.Get("deleteFiles")
					mu.Unlock()
					io.WriteString(w, "Ok.")
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			if err := c.Remove(context.Background(), "somehash", tc.deleteData); err != nil {
				t.Fatalf("Remove: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			if gotHash != "somehash" {
				t.Errorf("hashes = %q, want %q", gotHash, "somehash")
			}
			if gotFiles != tc.wantFiles {
				t.Errorf("deleteFiles = %q, want %q", gotFiles, tc.wantFiles)
			}
		})
	}

	t.Run("not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v2/auth/login":
				loginHandler(t)(w, r)
			case "/api/v2/torrents/delete":
				http.Error(w, "not found", http.StatusNotFound)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		c := newTestClient(t, srv)
		err := c.Remove(context.Background(), "missing", false)
		if !errors.Is(err, dlclient.ErrNotFound) {
			t.Fatalf("Remove error = %v, want ErrNotFound", err)
		}
	})
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

// TestRegistered checks that the init registration is in place.
func TestRegistered(t *testing.T) {
	if _, err := dlclient.New(dlclient.Config{Client: clientID, URL: "http://127.0.0.1:8080"}); err != nil {
		t.Fatalf("dlclient.New(%q): %v", clientID, err)
	}
}

// TestTrailingSlashTrimmed verifies base URL normalisation.
func TestTrailingSlashTrimmed(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			loginHandler(t)(w, r)
		case "/api/v2/app/version":
			hitPath = r.URL.Path
			io.WriteString(w, "v4.6.0")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(dlclient.Config{
		URL:      srv.URL + "///",
		Username: testUser,
		Password: testPass,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if hitPath != "/api/v2/app/version" {
		t.Errorf("hit path = %q, want %q", hitPath, "/api/v2/app/version")
	}
}

// TestSessionRelogin verifies that a 403 triggers one automatic re-login and a
// successful retry.
func TestSessionRelogin(t *testing.T) {
	var (
		mu         sync.Mutex
		loginCount int
		listCount  int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			mu.Lock()
			loginCount++
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: testSID, Path: "/"})
			io.WriteString(w, "Ok.")
		case "/api/v2/torrents/info":
			mu.Lock()
			listCount++
			attempt := listCount
			mu.Unlock()
			if attempt == 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if listCount != 2 {
		t.Errorf("list calls = %d, want 2", listCount)
	}
	if loginCount != 2 {
		t.Errorf("login calls = %d, want 2 (initial login plus re-login)", loginCount)
	}
}

// TestStatusErrorTruncates checks the descriptive error helper.
func TestStatusErrorTruncates(t *testing.T) {
	long := strings.Repeat("x", maxErrorBody+100)
	err := statusError("/api/v2/torrents/add",
		&http.Response{StatusCode: http.StatusBadRequest}, []byte(long))
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

// TestNormalizeStateCoversQBittorrent5 fixes a real import outage: qBittorrent
// 5.0 renamed the stopped states to stoppedUP/stoppedDL, and stoppedUP is what a
// finished torrent reports once it stops seeding. Without these mappings a
// completed download reported as "queued" and was never imported.
func TestNormalizeStateCoversQBittorrent5(t *testing.T) {
	tests := map[string]string{
		// qBittorrent 4.x
		"uploading":    dlclient.StateCompleted,
		"pausedUP":     dlclient.StateCompleted,
		"queuedUP":     dlclient.StateCompleted,
		"stalledUP":    dlclient.StateCompleted,
		"checkingUP":   dlclient.StateCompleted,
		"forcedUP":     dlclient.StateCompleted,
		"pausedDL":     dlclient.StatePaused,
		"queuedDL":     dlclient.StateQueued,
		"stalledDL":    dlclient.StateStalled,
		"downloading":  dlclient.StateDownloading,
		"metaDL":       dlclient.StateDownloading,
		"forcedDL":     dlclient.StateDownloading,
		"error":        dlclient.StateFailed,
		"missingFiles": dlclient.StateFailed,
		// qBittorrent 5.x
		"stoppedUP":          dlclient.StateCompleted,
		"stoppedDL":          dlclient.StatePaused,
		"forcedMetaDL":       dlclient.StateDownloading,
		"checkingResumeData": dlclient.StateDownloading,
	}

	for state, want := range tests {
		if got := normalizeState(state); got != want {
			t.Errorf("normalizeState(%q) = %q, want %q", state, got, want)
		}
	}

	if !isSeedingState("stoppedUP") {
		t.Error("stoppedUP must count as seeding: it is a completed torrent")
	}
}

// TestLoginSuccessSignalsAcrossVersions pins the login handshake against the
// change qBittorrent 5.2 made. Earlier versions answer 200 with the body "Ok.";
// 5.2 and later answer 204 with no body at all. Requiring the old "Ok." body
// made a perfectly good 5.2+ setup fail with
// "unexpected status 204", which is exactly what a user hit.
func TestLoginSuccessSignalsAcrossVersions(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "qBittorrent <= 5.1: 200 with an Ok. body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "SID", Value: testSID, Path: "/"})
				io.WriteString(w, "Ok.")
			},
		},
		{
			name: "qBittorrent >= 5.2: 204 with no body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "SID", Value: testSID, Path: "/"})
				w.WriteHeader(http.StatusNoContent)
			},
		},
		{
			name: "200 with an empty body",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "SID", Value: testSID, Path: "/"})
			},
		},
		{
			name: "authentication disabled entirely: 200, no cookie",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "Ok.")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/auth/login" {
					tc.handler(w, r)
					return
				}
				io.WriteString(w, "v5.2.0")
			}))
			defer srv.Close()

			if err := newTestClient(t, srv).Test(context.Background()); err != nil {
				t.Fatalf("Test: %v", err)
			}
		})
	}
}

func TestLoginFailureSignals(t *testing.T) {
	t.Run("Fails. body reports the credentials", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "Fails.")
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Test(context.Background())
		if err == nil {
			t.Fatal("expected a failure")
		}
		if !strings.Contains(err.Error(), "username and password") {
			t.Errorf("error %q should point at the credentials", err)
		}
	})

	t.Run("403 surfaces qBittorrent's own message", func(t *testing.T) {
		// qBittorrent reports an IP ban as a 403 with an explanatory body, which
		// is a different problem from a wrong password and must not be hidden.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "Your IP address has been banned after too many failed authentication attempts.")
		}))
		defer srv.Close()

		err := newTestClient(t, srv).Test(context.Background())
		if err == nil {
			t.Fatal("expected a failure")
		}
		if !strings.Contains(err.Error(), "banned") {
			t.Errorf("error %q should carry the ban message", err)
		}
	})
}

// TestReloginAfterRejectedSession covers the "403 after accepted login" symptom:
// the login is accepted but the following call is refused, so the client must
// re-authenticate once and retry rather than reporting a failure.
func TestReloginAfterRejectedSession(t *testing.T) {
	var logins, versionCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			logins++
			// A 5.2-style login response.
			w.WriteHeader(http.StatusNoContent)
		case "/api/v2/app/version":
			versionCalls++
			if versionCalls == 1 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, "v5.2.0")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if err := newTestClient(t, srv).Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if logins != 2 {
		t.Errorf("logged in %d times, want 2 (initial plus the retry)", logins)
	}
	if versionCalls != 2 {
		t.Errorf("version called %d times, want 2", versionCalls)
	}
}

// TestFullFlowAgainstQBittorrent52 drives the client against a server that
// behaves like 5.2: the login answers 204, and every other endpoint refuses the
// request unless the SID cookie from that login is presented. This is the
// "403 after accepted login" trap, and it proves the session survives the
// body-less login.
func TestFullFlowAgainstQBittorrent52(t *testing.T) {
	const torrents = `[{"hash":"aaa","name":"Release","state":"uploading","progress":1,"size":100,"downloaded":100,"dlspeed":0,"eta":0,"ratio":1.2,"save_path":"/downloads","content_path":"/downloads/Release","tags":"xbvr-ref-1"}]`

	var addCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			// 5.2: no body, success signalled by the status alone.
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: testSID, Path: "/"})
			w.WriteHeader(http.StatusNoContent)

		case "/api/v2/app/version":
			if !requireSID(t, w, r) {
				return
			}
			io.WriteString(w, "v5.2.0")

		case "/api/v2/torrents/info":
			if !requireSID(t, w, r) {
				return
			}
			io.WriteString(w, torrents)

		case "/api/v2/torrents/add":
			if !requireSID(t, w, r) {
				return
			}
			addCalls++
			io.WriteString(w, "Ok.")

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	ctx := context.Background()

	if err := c.Test(ctx); err != nil {
		t.Fatalf("Test: %v", err)
	}

	jobs, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Ref != "ref-1" {
		t.Fatalf("jobs = %+v, want one with ref-1", jobs)
	}

	if _, err := c.Add(ctx, dlclient.AddRequest{Ref: "ref-2", Magnet: "magnet:?xt=urn:btih:x"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if addCalls != 1 {
		t.Errorf("add called %d times, want 1", addCalls)
	}
}
