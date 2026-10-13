package transmission

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

// rpcBody decodes an RPC request captured by a test handler.
type rpcBody struct {
	Method    string          `json:"method"`
	Arguments json.RawMessage `json:"arguments"`
}

// decodeRequest reads and decodes an incoming RPC request body.
func decodeRequest(t *testing.T, r *http.Request) rpcBody {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var body rpcBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request body %q: %v", raw, err)
	}
	return body
}

// success writes a successful RPC response with the given arguments JSON.
func success(t *testing.T, w http.ResponseWriter, args string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if args == "" {
		args = "{}"
	}
	if _, err := io.WriteString(w, `{"result":"success","arguments":`+args+`}`); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func newTestClient(t *testing.T, cfg dlclient.Config) dlclient.Client {
	t.Helper()
	c, err := newClient(cfg)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	return c
}

// TestHandshake covers the 409 session handshake: the first request to a server
// without a session id is rejected, the id from the response header is cached
// and replayed on a retry, and later calls reuse it.
func TestHandshake(t *testing.T) {
	const sessionID = "session-abc-123"

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(sessionHeader)
		mu.Lock()
		seen = append(seen, got)
		mu.Unlock()

		if got != sessionID {
			w.Header().Set(sessionHeader, sessionID)
			w.WriteHeader(http.StatusConflict)
			return
		}
		success(t, w, `{"version":"4.0.5","rpc-version":17}`)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL})
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test after handshake: %v", err)
	}
	// A second call must reuse the cached id, so it is not rejected at all.
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("second Test: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"", sessionID, sessionID}
	if len(seen) != len(want) {
		t.Fatalf("requests = %d (%q), want %d (%q)", len(seen), seen, len(want), want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("request %d carried session id %q, want %q", i, seen[i], want[i])
		}
	}
}

// TestListNormalisation checks state mapping and field translation across a
// downloading, seeding, stopped and errored torrent.
func TestListNormalisation(t *testing.T) {
	const torrents = `{"torrents":[
		{"id":1,"hashString":"hash-downloading","name":"Downloading","status":4,"percentDone":0.25,
		 "totalSize":1000,"sizeWhenDone":1000,"downloadedEver":250,"rateDownload":100,"eta":30,
		 "downloadDir":"/downloads","error":0,"errorString":"","labels":["xbvr-ref-1"],
		 "isFinished":false,"uploadRatio":0.5,"leftUntilDone":750},
		{"id":2,"hashString":"hash-seeding","name":"Seeding","status":6,"percentDone":1,
		 "totalSize":2000,"downloadedEver":2000,"rateDownload":0,"eta":-1,
		 "downloadDir":"/downloads","error":0,"labels":["other","xbvr-ref-2"],"uploadRatio":2.5},
		{"id":3,"hashString":"hash-stopped","name":"Stopped","status":0,"percentDone":0.5,
		 "totalSize":3000,"downloadedEver":1500,"rateDownload":0,"eta":-2,
		 "downloadDir":"/downloads","error":0,"labels":[]},
		{"id":4,"hashString":"hash-errored","name":"Errored","status":4,"percentDone":0.1,
		 "totalSize":4000,"downloadedEver":400,"rateDownload":0,"eta":-1,
		 "downloadDir":"/downloads","error":3,"errorString":"tracker error"},
		{"id":5,"hashString":"","name":"NoHash","status":3,"percentDone":0,"eta":5}
	]}`

	var body rpcBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = decodeRequest(t, r)
		success(t, w, torrents)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL})
	jobs, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if body.Method != "torrent-get" {
		t.Errorf("method = %q, want torrent-get", body.Method)
	}
	if len(jobs) != 5 {
		t.Fatalf("jobs = %d, want 5", len(jobs))
	}

	down := jobs[0]
	if down.ID != "hash-downloading" || down.Name != "Downloading" {
		t.Errorf("downloading id/name = %q/%q", down.ID, down.Name)
	}
	if down.State != dlclient.StateDownloading {
		t.Errorf("downloading state = %q, want %q", down.State, dlclient.StateDownloading)
	}
	if down.Progress != 25 {
		t.Errorf("downloading progress = %v, want 25", down.Progress)
	}
	if down.Total != 1000 || down.Downloaded != 250 {
		t.Errorf("downloading bytes = %d/%d, want 250/1000", down.Downloaded, down.Total)
	}
	if down.Speed != 100 || down.ETA != 30 {
		t.Errorf("downloading speed/eta = %d/%d, want 100/30", down.Speed, down.ETA)
	}
	if down.SavePath != "/downloads" {
		t.Errorf("downloading save path = %q", down.SavePath)
	}
	if down.Ref != "ref-1" || down.Category != "ref-1" {
		t.Errorf("downloading ref/category = %q/%q, want ref-1/ref-1", down.Ref, down.Category)
	}
	if down.Seeding {
		t.Errorf("downloading torrent reported as seeding")
	}

	seed := jobs[1]
	if seed.State != dlclient.StateCompleted || !seed.Seeding {
		t.Errorf("seeding state/seeding = %q/%v, want completed/true", seed.State, seed.Seeding)
	}
	if seed.Ratio != 2.5 {
		t.Errorf("seeding ratio = %v, want 2.5", seed.Ratio)
	}
	if seed.Ref != "ref-2" {
		t.Errorf("seeding ref = %q, want ref-2", seed.Ref)
	}
	if seed.ETA != 0 {
		t.Errorf("negative eta not clamped: %d", seed.ETA)
	}
	if seed.Progress != 100 {
		t.Errorf("seeding progress = %v, want 100", seed.Progress)
	}

	stopped := jobs[2]
	if stopped.State != dlclient.StatePaused {
		t.Errorf("stopped state = %q, want %q", stopped.State, dlclient.StatePaused)
	}
	if stopped.ETA != 0 {
		t.Errorf("stopped negative eta not clamped: %d", stopped.ETA)
	}

	errored := jobs[3]
	if errored.State != dlclient.StateFailed {
		t.Errorf("errored state = %q, want %q", errored.State, dlclient.StateFailed)
	}
	if errored.Error != "tracker error" {
		t.Errorf("errored message = %q", errored.Error)
	}

	queued := jobs[4]
	if queued.State != dlclient.StateQueued {
		t.Errorf("queued state = %q, want %q", queued.State, dlclient.StateQueued)
	}
	if queued.ID != "5" {
		t.Errorf("missing hash should fall back to numeric id, got %q", queued.ID)
	}
}

// TestAddMagnet checks the torrent-add body for a magnet link.
func TestAddMagnet(t *testing.T) {
	var bodies []rpcBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, decodeRequest(t, r))
		success(t, w, `{"torrent-added":{"id":7,"hashString":"hash-7","name":"Release"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL, SavePath: "/config/default"})
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:    "ref-7",
		Name:   "Release",
		Magnet: "magnet:?xt=urn:btih:hash-7",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	body := rpcCall(t, bodies, "torrent-add")
	if body.Method != "torrent-add" {
		t.Errorf("method = %q, want torrent-add", body.Method)
	}

	var args struct {
		Filename    string   `json:"filename"`
		Metainfo    string   `json:"metainfo"`
		DownloadDir string   `json:"download-dir"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(body.Arguments, &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if args.Filename != "magnet:?xt=urn:btih:hash-7" {
		t.Errorf("filename = %q", args.Filename)
	}
	if args.Metainfo != "" {
		t.Errorf("metainfo = %q, want empty", args.Metainfo)
	}
	if args.DownloadDir != "/config/default" {
		t.Errorf("download-dir = %q, want config fallback", args.DownloadDir)
	}
	if len(args.Labels) != 1 || args.Labels[0] != "xbvr-ref-7" {
		t.Errorf("labels = %q, want [xbvr-ref-7]", args.Labels)
	}

	if job.ID != "hash-7" || job.Name != "Release" || job.State != dlclient.StateQueued {
		t.Errorf("job = %+v", job)
	}
}

// TestAddContent checks that raw torrent bytes are sent base64-encoded as
// metainfo, and that a per-job save path overrides the configured default.
func TestAddContent(t *testing.T) {
	content := []byte("d8:announce4:urlre")

	var bodies []rpcBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, decodeRequest(t, r))
		success(t, w, `{"torrent-duplicate":{"id":8,"hashString":"hash-8","name":"Dup"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL, SavePath: "/config/default"})
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:      "ref-8",
		Name:     "Dup",
		URL:      "http://example.invalid/a.torrent",
		Content:  content,
		SavePath: "/per-job",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	body := rpcCall(t, bodies, "torrent-add")
	var args struct {
		Filename    string `json:"filename"`
		Metainfo    string `json:"metainfo"`
		DownloadDir string `json:"download-dir"`
	}
	if err := json.Unmarshal(body.Arguments, &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if args.Metainfo != base64.StdEncoding.EncodeToString(content) {
		t.Errorf("metainfo = %q, want base64 of content", args.Metainfo)
	}
	if args.Filename != "" {
		t.Errorf("filename = %q, want empty when content is sent", args.Filename)
	}
	if args.DownloadDir != "/per-job" {
		t.Errorf("download-dir = %q, want /per-job", args.DownloadDir)
	}
	if job.ID != "hash-8" {
		t.Errorf("duplicate job id = %q, want hash-8", job.ID)
	}
}

// TestBasicAuth checks that credentials are sent when a username is configured.
func TestBasicAuth(t *testing.T) {
	var mu sync.Mutex
	var authHeader string
	var hadAuth bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		mu.Lock()
		authHeader = r.Header.Get("Authorization")
		hadAuth = ok && username == "alice" && password == "s3cret"
		mu.Unlock()
		success(t, w, `{"version":"4.0.5"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL, Username: "alice", Password: "s3cret"})
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !hadAuth {
		t.Fatalf("Authorization header = %q, want valid basic auth for alice", authHeader)
	}
	if !strings.HasPrefix(authHeader, "Basic ") {
		t.Errorf("Authorization = %q, want Basic scheme", authHeader)
	}
}

// TestRPCError checks that a non-success result surfaces as an error.
func TestRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":"no such method","arguments":{}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL})
	err := c.Test(context.Background())
	if err == nil {
		t.Fatal("Test succeeded, want RPC error")
	}
	if !strings.Contains(err.Error(), "no such method") {
		t.Errorf("error = %v, want it to mention the result string", err)
	}
}

// TestRemoveNotFound checks that the "no torrent" result maps to ErrNotFound.
func TestRemoveNotFound(t *testing.T) {
	var body rpcBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = decodeRequest(t, r)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"result":"no torrent with that id","arguments":{}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL})
	err := c.Remove(context.Background(), "hash-x", true)
	if !errors.Is(err, dlclient.ErrNotFound) {
		t.Fatalf("Remove error = %v, want ErrNotFound", err)
	}

	var args struct {
		IDs         []any `json:"ids"`
		DeleteLocal bool  `json:"delete-local-data"`
	}
	if err := json.Unmarshal(body.Arguments, &args); err != nil {
		t.Fatalf("decode arguments: %v", err)
	}
	if len(args.IDs) != 1 || args.IDs[0] != "hash-x" {
		t.Errorf("ids = %v, want [hash-x]", args.IDs)
	}
	if !args.DeleteLocal {
		t.Errorf("delete-local-data = false, want true")
	}
}

// TestNewClientRequiresURL checks the constructor rejects an empty URL.
func TestNewClientRequiresURL(t *testing.T) {
	if _, err := newClient(dlclient.Config{}); err == nil {
		t.Fatal("newClient accepted an empty URL")
	}
}

// TestTrailingSlash checks that a configured URL with a trailing slash still
// resolves to the RPC endpoint.
func TestTrailingSlash(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		success(t, w, `{"version":"4.0.5"}`)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL + "/"})
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if path != "/transmission/rpc" {
		t.Errorf("path = %q, want /transmission/rpc", path)
	}
}

// rpcCall returns the first recorded request for an RPC method.
func rpcCall(t *testing.T, bodies []rpcBody, method string) rpcBody {
	t.Helper()
	for _, b := range bodies {
		if b.Method == method {
			return b
		}
	}
	t.Fatalf("no %s request was sent (got %d requests)", method, len(bodies))
	return rpcBody{}
}

// TestAddAppliesLabelToOldClients covers the Transmission 3 case: torrent-add
// has no labels argument before RPC 17, so the xbvr ref has to be applied with a
// follow-up torrent-set or the job could only ever be matched by name.
func TestAddAppliesLabelToOldClients(t *testing.T) {
	var bodies []rpcBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, decodeRequest(t, r))
		// No labels in the torrent-added payload, exactly like Transmission 3.
		success(t, w, `{"torrent-added":{"id":9,"hashString":"hash-9","name":"Old"}}`)
	}))
	defer srv.Close()

	c := newTestClient(t, dlclient.Config{URL: srv.URL})
	job, err := c.Add(context.Background(), dlclient.AddRequest{
		Ref:    "ref-9",
		Name:   "Old",
		Magnet: "magnet:?xt=urn:btih:hash-9",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if job.Ref != "ref-9" {
		t.Errorf("job ref = %q, want ref-9", job.Ref)
	}

	set := rpcCall(t, bodies, "torrent-set")
	var args struct {
		IDs    []any    `json:"ids"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(set.Arguments, &args); err != nil {
		t.Fatalf("decode torrent-set arguments: %v", err)
	}
	if len(args.IDs) != 1 || args.IDs[0] != "hash-9" {
		t.Errorf("torrent-set ids = %v, want [hash-9]", args.IDs)
	}
	if len(args.Labels) != 1 || args.Labels[0] != "xbvr-ref-9" {
		t.Errorf("torrent-set labels = %v, want [xbvr-ref-9]", args.Labels)
	}
}

// TestJobFromTorrentErrorSeverity fixes an over-eager failure: Transmission's
// error field is a severity, and a tracker warning (1) or a tracker error (2)
// leaves the transfer working. Treating those as failures abandoned downloads
// that were about to finish.
func TestJobFromTorrentErrorSeverity(t *testing.T) {
	base := torrent{
		ID:             1,
		HashString:     "hash-1",
		Name:           "Release",
		Status:         statusDownload,
		PercentDone:    0.5,
		DownloadedEver: 50,
		TotalSize:      100,
		SizeWhenDone:   100,
	}

	cases := []struct {
		name      string
		err       int
		errString string
		wantState string
		wantError string
	}{
		{"no error", errorOK, "", dlclient.StateDownloading, ""},
		{"tracker warning keeps downloading", errorTrackerWarning, "unregistered torrent", dlclient.StateDownloading, "unregistered torrent"},
		{"tracker error keeps downloading", errorTrackerError, "all trackers failed", dlclient.StateDownloading, "all trackers failed"},
		{"local error fails", errorLocalError, "disk full", dlclient.StateFailed, "disk full"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := base
			tr.Error = tc.err
			tr.ErrorString = tc.errString

			job := jobFromTorrent(tr)
			if job.State != tc.wantState {
				t.Errorf("state = %q, want %q", job.State, tc.wantState)
			}
			if job.Error != tc.wantError {
				t.Errorf("error = %q, want %q", job.Error, tc.wantError)
			}
		})
	}
}

// TestJobFromTorrentTotalMatchesProgress checks that the reported total is the
// denominator percentDone is measured against, so the UI cannot show 100%
// alongside a half-empty size.
func TestJobFromTorrentTotalMatchesProgress(t *testing.T) {
	job := jobFromTorrent(torrent{
		ID:           1,
		HashString:   "hash-1",
		Name:         "Partial",
		Status:       statusDownload,
		PercentDone:  1,
		TotalSize:    100,
		SizeWhenDone: 40,
	})
	if job.Total != 40 {
		t.Errorf("total = %d, want sizeWhenDone (40)", job.Total)
	}

	// Older or unusual payloads without sizeWhenDone fall back to totalSize.
	job = jobFromTorrent(torrent{ID: 2, HashString: "hash-2", Status: statusDownload, TotalSize: 100})
	if job.Total != 100 {
		t.Errorf("total = %d, want the totalSize fallback (100)", job.Total)
	}
}
