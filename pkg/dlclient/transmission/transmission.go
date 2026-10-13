// Package transmission implements the dlclient.Client interface on top of the
// Transmission RPC API (Transmission 3 and 4).
//
// Transmission exposes a single JSON endpoint, {base}/transmission/rpc, and
// protects it with a session id that it hands out on the first 409 response.
// The RPC supports only one torrent client, so Kind is always torrent.
package transmission

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbapps/xbvr/pkg/dlclient"
)

func init() {
	dlclient.Register("transmission", newClient)
}

const (
	// sessionHeader carries the RPC session id in both directions.
	sessionHeader = "X-Transmission-Session-Id"
	// requestTimeout bounds every individual HTTP round trip, including the
	// automatic retry after a 409: each attempt gets its own deadline.
	requestTimeout = 15 * time.Second
	// maxResponseBody caps how much of a response we are willing to read.
	maxResponseBody = 64 << 20
	// labelPrefix marks the tags xbvr owns on a torrent. Transmission 4 stores
	// arbitrary strings as labels; the xbvr ref travels in one of them.
	labelPrefix = "xbvr-"
)

// Transmission torrent status codes.
const (
	statusStopped      = 0
	statusCheckWait    = 1
	statusCheck        = 2
	statusDownloadWait = 3
	statusDownload     = 4
	statusSeedWait     = 5
	statusSeed         = 6
)

// Transmission's error field is a severity, not a boolean: only a local error
// (disk full, permissions) means the transfer cannot proceed. A tracker warning
// or a "all trackers failed" notice is reported on torrents that keep working,
// often via DHT, so treating them as failures would abandon a download that is
// about to finish.
const (
	errorOK             = 0
	errorTrackerWarning = 1
	errorTrackerError   = 2
	errorLocalError     = 3
)

// torrentFields is the set of torrent-get fields xbvr needs. Requesting an
// explicit field list keeps the response small when a client manages hundreds
// of torrents.
var torrentFields = []string{
	"id",
	"hashString",
	"name",
	"status",
	"percentDone",
	"totalSize",
	"sizeWhenDone",
	"downloadedEver",
	"rateDownload",
	"eta",
	"downloadDir",
	"error",
	"errorString",
	"labels",
	"isFinished",
	"uploadRatio",
	"leftUntilDone",
}

// client is the dlclient.Client implementation.
type client struct {
	cfg  dlclient.Config
	http *http.Client

	mu        sync.Mutex
	sessionID string
}

// newClient is the dlclient.Factory for Transmission.
func newClient(cfg dlclient.Config) (dlclient.Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("transmission: url is required")
	}
	return &client{
		cfg:  cfg,
		http: &http.Client{},
	}, nil
}

// ID returns the xbvr-side identifier from Config.
func (c *client) ID() string { return c.cfg.ID }

// Name returns the display name, defaulting to the client's own name.
func (c *client) Name() string {
	if c.cfg.Name != "" {
		return c.cfg.Name
	}
	return "Transmission"
}

// Kind returns KindTorrent: Transmission only speaks BitTorrent.
func (c *client) Kind() string { return dlclient.KindTorrent }

// Test performs a session-get, which proves both reachability and credentials.
func (c *client) Test(ctx context.Context) error {
	args := map[string]any{"fields": []string{"version", "rpc-version"}}
	var out struct {
		Version    string `json:"version"`
		RPCVersion int    `json:"rpc-version"`
	}
	return c.do(ctx, "session-get", args, &out)
}

// List returns every torrent Transmission knows about, normalised to dlclient.Job.
func (c *client) List(ctx context.Context) ([]dlclient.Job, error) {
	var resp torrentGetResponse
	if err := c.do(ctx, "torrent-get", map[string]any{"fields": torrentFields}, &resp); err != nil {
		return nil, err
	}
	jobs := make([]dlclient.Job, 0, len(resp.Torrents))
	for _, t := range resp.Torrents {
		jobs = append(jobs, jobFromTorrent(t))
	}
	return jobs, nil
}

// Add hands a magnet, a .torrent URL or raw .torrent bytes to Transmission and
// returns the resulting job. Transmission 3 lacks labels, so the ref is merely
// best-effort there; the download directory still comes from xbvr.
func (c *client) Add(ctx context.Context, req dlclient.AddRequest) (dlclient.Job, error) {
	args := torrentAddRequest{Paused: req.Paused}

	if len(req.Content) > 0 {
		args.Metainfo = base64.StdEncoding.EncodeToString(req.Content)
	} else if req.Magnet != "" {
		args.Filename = req.Magnet
	} else {
		args.Filename = req.URL
	}

	args.DownloadDir = req.SavePath
	if args.DownloadDir == "" {
		args.DownloadDir = c.cfg.SavePath
	}
	if req.Ref != "" {
		args.Labels = []string{labelPrefix + req.Ref}
	}

	var resp torrentAddResponse
	if err := c.do(ctx, "torrent-add", args, &resp); err != nil {
		return dlclient.Job{}, err
	}

	added := resp.TorrentAdded
	if added == nil {
		added = resp.TorrentDuplicate
	}
	if added != nil {
		job := jobFromAdded(*added)
		// torrent-add only learned about labels in RPC 17 (Transmission 4).
		// Transmission 3 returns the hash but silently ignores the argument, so
		// the ref is applied afterwards with torrent-set, which every version
		// supports. Without this the caller could only match the job by name.
		if req.Ref != "" && job.Ref == "" && job.ID != "" {
			if err := c.setLabel(ctx, job.ID, labelPrefix+req.Ref); err == nil {
				job.Ref = req.Ref
			}
		}
		return job, nil
	}

	// Older or unusual builds return an empty arguments object. Resolve the job
	// by looking for it in the list; if that fails too, return a best-effort job
	// with an empty id rather than an error, since the torrent was accepted.
	if job, ok := c.findAdded(ctx, req); ok {
		return job, nil
	}
	return dlclient.Job{
		Ref:   req.Ref,
		Name:  req.Name,
		State: dlclient.StateQueued,
	}, nil
}

// Remove forgets a torrent, optionally deleting its data.
//
// Transmission reports success whether or not the id matched anything, so this
// is idempotent: removing an already-absent torrent is not an error.
func (c *client) Remove(ctx context.Context, id string, deleteData bool) error {
	return c.do(ctx, "torrent-remove", map[string]any{
		"ids":               []any{id},
		"delete-local-data": deleteData,
	}, nil)
}

// setLabel applies a label to an existing torrent, which is how the xbvr ref
// reaches clients whose torrent-add predates the labels argument.
func (c *client) setLabel(ctx context.Context, id, label string) error {
	return c.do(ctx, "torrent-set", map[string]any{
		"ids":    []any{id},
		"labels": []string{label},
	}, nil)
}

// findAdded looks the just-added torrent up by xbvr ref label, then by name.
func (c *client) findAdded(ctx context.Context, req dlclient.AddRequest) (dlclient.Job, bool) {
	jobs, err := c.List(ctx)
	if err != nil {
		return dlclient.Job{}, false
	}
	if req.Ref != "" {
		for _, job := range jobs {
			if job.Ref == req.Ref {
				return job, true
			}
		}
	}
	if req.Name != "" {
		for _, job := range jobs {
			if job.Name == req.Name {
				return job, true
			}
		}
	}
	return dlclient.Job{}, false
}

// ---- RPC plumbing ----------------------------------------------------------

// rpcRequest is the envelope every RPC method shares.
type rpcRequest struct {
	Method    string `json:"method"`
	Arguments any    `json:"arguments,omitempty"`
}

// rpcResponse is the envelope every RPC method answers with. Arguments is kept
// raw so each caller can decode only its own shape.
type rpcResponse struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

// do performs one RPC call, transparently handling the session-id handshake and
// decoding the response into out (which may be nil).
//
// It is the single place that touches HTTP, so the public methods only have to
// build arguments and interpret the decoded shape.
func (c *client) do(ctx context.Context, method string, args any, out any) error {
	payload, err := json.Marshal(rpcRequest{Method: method, Arguments: args})
	if err != nil {
		return fmt.Errorf("transmission: encode %s request: %w", method, err)
	}

	for attempt := 0; ; attempt++ {
		resp, err := c.post(ctx, payload)
		if err != nil {
			return fmt.Errorf("transmission: %s: %w", method, err)
		}

		// A 409 means our session id was missing or stale. Transmission sends a
		// fresh one in the response header; store it and replay the request once.
		if resp.StatusCode == http.StatusConflict {
			sessionID := resp.Header.Get(sessionHeader)
			resp.Body.Close()
			if sessionID == "" {
				return fmt.Errorf("transmission: %s: 409 without %s header", method, sessionHeader)
			}
			c.setSession(sessionID)
			if attempt == 0 {
				continue
			}
			return fmt.Errorf("transmission: %s: session handshake rejected", method)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("transmission: %s: read response: %w", method, readErr)
		}

		// A 404 on the RPC endpoint means the base URL is wrong (a path typo, or
		// a reverse proxy answering instead), not that a job is missing, so it
		// is reported as an ordinary error.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("transmission: %s: http %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var rpcResp rpcResponse
		if err := json.Unmarshal(body, &rpcResp); err != nil {
			return fmt.Errorf("transmission: %s: decode response: %w", method, err)
		}
		if rpcResp.Result != "success" {
			if strings.Contains(strings.ToLower(rpcResp.Result), "no torrent") {
				return fmt.Errorf("transmission: %s: %s: %w", method, rpcResp.Result, dlclient.ErrNotFound)
			}
			return fmt.Errorf("transmission: %s: %s", method, rpcResp.Result)
		}
		if out != nil && len(rpcResp.Arguments) > 0 {
			if err := json.Unmarshal(rpcResp.Arguments, out); err != nil {
				return fmt.Errorf("transmission: %s: decode arguments: %w", method, err)
			}
		}
		return nil
	}
}

// post sends one already-encoded request with the current session id and
// credentials. It never logs anything, so credentials cannot leak.
func (c *client) post(ctx context.Context, payload []byte) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	// The caller owns the returned body; the context is cancelled once it is
	// closed, which is safe because we fully read the body inside do.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sessionID := c.session(); sessionID != "" {
		req.Header.Set(sessionHeader, sessionID)
	}
	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	// Wrap the body so the deadline is released as soon as the body is closed.
	resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// endpoint is the RPC URL, tolerating a trailing slash in the configured base.
func (c *client) endpoint() string {
	return strings.TrimRight(c.cfg.URL, "/") + "/transmission/rpc"
}

// session returns the cached session id, safe for concurrent use.
func (c *client) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// setSession caches a freshly issued session id, safe for concurrent use.
func (c *client) setSession(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = id
}

// cancelReadCloser releases a context deadline when the body is closed.
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// ---- Payload shapes --------------------------------------------------------

type torrentGetResponse struct {
	Torrents []torrent `json:"torrents"`
}

// torrent is the subset of a torrent-get entry xbvr reads.
type torrent struct {
	ID             int64    `json:"id"`
	HashString     string   `json:"hashString"`
	Name           string   `json:"name"`
	Status         int      `json:"status"`
	PercentDone    float64  `json:"percentDone"`
	TotalSize      int64    `json:"totalSize"`
	SizeWhenDone   int64    `json:"sizeWhenDone"`
	DownloadedEver int64    `json:"downloadedEver"`
	RateDownload   int64    `json:"rateDownload"`
	ETA            int64    `json:"eta"`
	DownloadDir    string   `json:"downloadDir"`
	Error          int      `json:"error"`
	ErrorString    string   `json:"errorString"`
	Labels         []string `json:"labels"`
	IsFinished     bool     `json:"isFinished"`
	UploadRatio    float64  `json:"uploadRatio"`
	LeftUntilDone  int64    `json:"leftUntilDone"`
}

type torrentAddRequest struct {
	Filename    string   `json:"filename,omitempty"`
	Metainfo    string   `json:"metainfo,omitempty"`
	DownloadDir string   `json:"download-dir,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Paused      bool     `json:"paused"`
}

type torrentAddResponse struct {
	TorrentAdded     *addedTorrent `json:"torrent-added"`
	TorrentDuplicate *addedTorrent `json:"torrent-duplicate"`
}

type addedTorrent struct {
	ID         int64  `json:"id"`
	HashString string `json:"hashString"`
	Name       string `json:"name"`
}

// ---- Normalisation ---------------------------------------------------------

// jobFromTorrent maps one Transmission torrent onto the shared job shape.
func jobFromTorrent(t torrent) dlclient.Job {
	// percentDone is measured against sizeWhenDone - the bytes of the files the
	// user actually selected - while totalSize also counts unselected files.
	// Reporting totalSize alongside percentDone would show "100%" next to a
	// half-empty size, so the matching denominator wins.
	total := t.SizeWhenDone
	if total <= 0 {
		total = t.TotalSize
	}

	job := dlclient.Job{
		ID:         torrentID(t.ID, t.HashString),
		Name:       t.Name,
		State:      stateFor(t),
		Progress:   t.PercentDone * 100,
		Total:      total,
		Downloaded: t.DownloadedEver,
		Speed:      t.RateDownload,
		ETA:        t.ETA,
		SavePath:   t.DownloadDir,
		Category:   categoryLabel(t.Labels),
		Ratio:      t.UploadRatio,
		Seeding:    t.Status == statusSeedWait || t.Status == statusSeed,
		Ref:        refLabel(t.Labels),
	}
	if job.ETA < 0 {
		job.ETA = 0
	}

	switch t.Error {
	case errorLocalError:
		// A local error really does stop the transfer.
		job.State = dlclient.StateFailed
		job.Error = t.ErrorString
	case errorTrackerWarning, errorTrackerError:
		// Reported, but the download keeps going; surface the text without
		// changing the state. The poller only acts on StateFailed.
		job.Error = t.ErrorString
	}
	return job
}

// jobFromAdded builds the queued job reported by torrent-add.
func jobFromAdded(t addedTorrent) dlclient.Job {
	return dlclient.Job{
		ID:    torrentID(t.ID, t.HashString),
		Name:  t.Name,
		State: dlclient.StateQueued,
	}
}

// stateFor translates the numeric status, honouring the error flag.
func stateFor(t torrent) string {
	switch t.Status {
	case statusStopped:
		if t.PercentDone >= 1 {
			return dlclient.StateCompleted
		}
		return dlclient.StatePaused
	case statusCheckWait, statusCheck, statusDownloadWait, statusSeedWait:
		return dlclient.StateQueued
	case statusDownload:
		return dlclient.StateDownloading
	case statusSeed:
		return dlclient.StateCompleted
	default:
		return dlclient.StateQueued
	}
}

// torrentID prefers the stable hash and falls back to the numeric id.
func torrentID(id int64, hash string) string {
	if hash != "" {
		return hash
	}
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// refLabel extracts the xbvr ref from a torrent's labels, if present.
func refLabel(labels []string) string {
	for _, label := range labels {
		if strings.HasPrefix(label, labelPrefix) {
			return strings.TrimPrefix(label, labelPrefix)
		}
	}
	return ""
}

// categoryLabel returns the first xbvr-owned label's value.
func categoryLabel(labels []string) string {
	return refLabel(labels)
}
