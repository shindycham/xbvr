// Package nzbget implements the dlclient.Client contract for the NZBGet
// JSON-RPC API (NZBGet 15 and later).
//
// NZBGet exposes a single endpoint, {base}/jsonrpc, and answers JSON-RPC 2.0
// calls. Authentication is normally HTTP basic; newer versions also accept an
// API key in the URL path, but that is not used here because basic auth is by
// far the more common setup.
//
// As with SABnzbd, a finished download leaves the active queue and moves into a
// history list, so List merges listgroups with history. A queue-only
// implementation would never report StateCompleted and the caller would never
// import anything.
package nzbget

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
	"time"

	"github.com/xbapps/xbvr/pkg/dlclient"
)

const (
	// clientID is the name this implementation registers under.
	clientID = "nzbget"

	// requestTimeout bounds every individual HTTP call, including the body read.
	requestTimeout = 20 * time.Second

	// maxBodySize caps how much of a response we are willing to buffer.
	maxBodySize = 64 << 20

	// maxErrorBody is how much of a failing response body ends up in an error.
	maxErrorBody = 512

	// jsonRPCPath is appended to the configured base URL.
	jsonRPCPath = "/jsonrpc"

	// rpcID is the JSON-RPC request id. NZBGet never correlates replies, so a
	// constant is enough.
	rpcID = 1
)

func init() {
	dlclient.Register(clientID, newClient)
}

// client is the dlclient.Client implementation.
type client struct {
	cfg  dlclient.Config
	base string
	http *http.Client
}

// newClient is the dlclient.Factory for NZBGet.
func newClient(cfg dlclient.Config) (dlclient.Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("nzbget: config URL is required")
	}
	return &client{
		cfg:  cfg,
		base: strings.TrimRight(strings.TrimSpace(cfg.URL), "/"),
		http: &http.Client{},
	}, nil
}

// New builds an NZBGet client from its configuration.
func New(cfg dlclient.Config) (dlclient.Client, error) {
	return newClient(cfg)
}

// ID returns the xbvr-side identifier from Config.
func (c *client) ID() string { return c.cfg.ID }

// Name returns a display name, falling back to the client's own name.
func (c *client) Name() string {
	if strings.TrimSpace(c.cfg.Name) != "" {
		return c.cfg.Name
	}
	return "NZBGet"
}

// Kind returns KindNZB: NZBGet only speaks Usenet.
func (c *client) Kind() string { return dlclient.KindNZB }

// Test calls the version method, which proves reachability and credentials.
func (c *client) Test(ctx context.Context) error {
	var version string
	if err := c.call(ctx, "version", nil, &version); err != nil {
		return err
	}
	if strings.TrimSpace(version) == "" {
		return errors.New("nzbget: version response was empty")
	}
	return nil
}

// Add hands NZB content or a URL to NZBGet with append and returns the resulting
// job. Raw .nzb bytes are base64 encoded; an http(s) URL is passed through and
// fetched by NZBGet itself.
func (c *client) Add(ctx context.Context, req dlclient.AddRequest) (dlclient.Job, error) {
	content := ""
	if len(req.Content) > 0 {
		content = base64.StdEncoding.EncodeToString(req.Content)
	} else {
		content = firstNonEmpty(req.URL, req.Magnet)
	}
	if strings.TrimSpace(content) == "" {
		return dlclient.Job{}, errors.New("nzbget: add requires a URL or NZB content")
	}

	category := firstNonEmpty(req.Category, c.cfg.Category)
	savePath := firstNonEmpty(req.SavePath, c.cfg.SavePath)

	// append params, in order:
	//   NZBFilename, Content, Category, Priority, AddToTop, AddPaused,
	//   DupeKey, DupeScore, DupeMode.
	// DupeMode FORCE never rejects an add as a duplicate, which is what xbvr
	// wants: it has already decided the release is worth downloading.
	params := []any{
		req.Name,
		content,
		category,
		0,
		false,
		req.Paused,
		"",
		0,
		"FORCE",
	}

	var id nzbID
	if err := c.call(ctx, "append", params, &id); err != nil {
		return dlclient.Job{}, err
	}

	return dlclient.Job{
		ID: strconv.FormatInt(int64(id), 10),
		// NZBGet has no tag mechanism, so the xbvr ref rides on the returned job
		// for the caller to match against.
		Ref:      req.Ref,
		Name:     req.Name,
		State:    dlclient.StateQueued,
		SavePath: savePath,
		Category: category,
	}, nil
}

// List returns the active groups merged with the history. The queue alone can
// never contain a finished download, so the history half is what lets the caller
// observe StateCompleted and import the result.
func (c *client) List(ctx context.Context) ([]dlclient.Job, error) {
	var groups []group
	if err := c.call(ctx, "listgroups", nil, &groups); err != nil {
		return nil, err
	}
	var history []historySlot
	if err := c.call(ctx, "history", nil, &history); err != nil {
		return nil, err
	}

	jobs := make([]dlclient.Job, 0, len(groups)+len(history))
	for _, g := range groups {
		jobs = append(jobs, g.job(c.cfg.SavePath))
	}
	for _, h := range history {
		jobs = append(jobs, h.job(c.cfg.SavePath))
	}
	return jobs, nil
}

// Remove deletes a job. An id is in either the active queue or the history but
// never both, so the queue command is tried first and the history command after
// it fails. The deleteData argument is unused: both commands delete the data.
func (c *client) Remove(ctx context.Context, id string, deleteData bool) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return dlclient.ErrNotFound
	}
	nzbID, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return fmt.Errorf("nzbget: delete %q: %w", trimmed, dlclient.ErrNotFound)
	}

	if err := c.call(ctx, "editqueue", []any{"GroupFinalDelete", "", []any{nzbID}}, nil); err == nil {
		return nil
	}
	if err := c.call(ctx, "editqueue", []any{"HistoryFinalDelete", "", []any{nzbID}}, nil); err == nil {
		return nil
	}
	return fmt.Errorf("nzbget: delete %q: %w", trimmed, dlclient.ErrNotFound)
}

// ---- RPC plumbing ----------------------------------------------------------

// rpcRequest is the JSON-RPC 2.0 envelope every call shares.
type rpcRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int    `json:"id"`
}

// rpcResponse is the reply envelope. Result is kept raw so each caller can
// decode only its own shape, and Error may arrive with HTTP 200.
type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	ID     int             `json:"id"`
}

// rpcError is NZBGet's JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call is the single place that touches HTTP: it encodes the request, applies
// basic auth, enforces the timeout, checks both the HTTP status and the JSON-RPC
// error object, and decodes the result into out (which may be nil). The public
// methods only have to supply the method, the params and the decoded shape.
func (c *client) call(ctx context.Context, method string, params []any, out any) error {
	if params == nil {
		params = []any{}
	}
	payload, err := json.Marshal(rpcRequest{Method: method, Params: params, ID: rpcID})
	if err != nil {
		return fmt.Errorf("nzbget: encode %s request: %w", method, err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("nzbget: build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	// cfg.APIKey is deliberately not sent: only newer NZBGet builds accept a key
	// in the URL path, and basic auth is the common setup.

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nzbget: %s: %w", method, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return fmt.Errorf("nzbget: %s: read response: %w", method, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(method, resp.StatusCode, data)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		// An empty body is not fatal: leave out at its zero value rather than
		// failing the decode and panicking on nothing.
		return nil
	}
	// A few NZBGet builds answer some methods, notably history, with a bare JSON
	// array instead of the usual result envelope.
	if trimmed[0] == '[' {
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(trimmed, out); err != nil {
			return fmt.Errorf("nzbget: %s: decode array response: %w", method, err)
		}
		return nil
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(trimmed, &rpcResp); err != nil {
		return fmt.Errorf("nzbget: %s: decode response: %w", method, err)
	}
	if rpcResp.Error != nil {
		message := strings.TrimSpace(rpcResp.Error.Message)
		if strings.Contains(strings.ToLower(message), "not found") {
			return fmt.Errorf("nzbget: %s: %s: %w", method, message, dlclient.ErrNotFound)
		}
		return fmt.Errorf("nzbget: %s: %s", method, message)
	}
	if out == nil || len(rpcResp.Result) == 0 || string(rpcResp.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(rpcResp.Result, out); err != nil {
		return fmt.Errorf("nzbget: %s: decode result: %w", method, err)
	}
	return nil
}

// endpoint is the JSON-RPC URL, tolerating a trailing slash in the configured
// base.
func (c *client) endpoint() string {
	return c.base + jsonRPCPath
}

// ---- Payload shapes --------------------------------------------------------

// group is one entry of listgroups: a job still in the active queue.
type group struct {
	NZBID            nzbID   `json:"NZBID"`
	NZBName          string  `json:"NZBName"`
	Status           string  `json:"Status"`
	DownloadedSizeMB float64 `json:"DownloadedSizeMB"`
	FileSizeMB       float64 `json:"FileSizeMB"`
	RemainingSizeMB  float64 `json:"RemainingSizeMB"`
	DestDir          string  `json:"DestDir"`
	Category         string  `json:"Category"`
	Health           int     `json:"Health"`
}

// historySlot is one entry of the history list, which is where a finished
// download ends up once it leaves the queue.
type historySlot struct {
	NZBID        nzbID   `json:"NZBID"`
	Name         string  `json:"Name"`
	Status       string  `json:"Status"`
	FileSizeMB   float64 `json:"FileSizeMB"`
	FileSizeLo   int64   `json:"FileSizeLo"`
	FileSizeHi   int64   `json:"FileSizeHi"`
	DestDir      string  `json:"DestDir"`
	Category     string  `json:"Category"`
	FailMessage  string  `json:"FailMessage"`
	DeleteStatus string  `json:"DeleteStatus"`
}

// nzbID is NZBGet's numeric job id. Some builds return it as a JSON number and
// others as a string, so both are accepted.
type nzbID int64

// UnmarshalJSON accepts a JSON number, a numeric string, null or nothing.
func (n *nzbID) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	switch trimmed {
	case "", "null":
		*n = 0
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*n = 0
			return nil
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid id %q", s)
		}
		*n = nzbID(v)
		return nil
	}
	v, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid id %q", trimmed)
	}
	*n = nzbID(v)
	return nil
}

// ---- Normalisation ---------------------------------------------------------

// job maps an active group onto the shared job shape, falling back to the
// configured save path until NZBGet reports a destination.
func (g group) job(defaultSavePath string) dlclient.Job {
	state := jobState(g.Status)
	dest := firstNonEmpty(g.DestDir, defaultSavePath)

	job := dlclient.Job{
		ID:          strconv.FormatInt(int64(g.NZBID), 10),
		Name:        g.NZBName,
		State:       state,
		Progress:    progressPercent(g.FileSizeMB, g.RemainingSizeMB),
		Downloaded:  megabytesToBytes(g.DownloadedSizeMB),
		Total:       megabytesToBytes(g.FileSizeMB),
		SavePath:    dest,
		ContentPath: dest,
		Category:    g.Category,
	}
	if state == dlclient.StateCompleted {
		job.Progress = 100
	}
	if state == dlclient.StateFailed {
		job.Error = g.Status
	}
	return job
}

// job maps a history entry onto the shared job shape. Passed a completed entry,
// DestDir is the folder the unpacked file ended up in, which is exactly what the
// caller needs to locate the video.
func (h historySlot) job(defaultSavePath string) dlclient.Job {
	state := jobState(h.Status)
	dest := firstNonEmpty(h.DestDir, defaultSavePath)

	job := dlclient.Job{
		ID:          strconv.FormatInt(int64(h.NZBID), 10),
		Name:        h.Name,
		State:       state,
		Total:       h.totalBytes(),
		SavePath:    dest,
		ContentPath: dest,
		Category:    h.Category,
	}
	switch state {
	case dlclient.StateCompleted:
		job.Progress = 100
		job.Downloaded = job.Total
	case dlclient.StateFailed:
		job.Progress = 0
		job.Error = firstNonEmpty(h.FailMessage, h.Status)
	default:
		// A transient status such as WARNING: the download itself has finished,
		// but the job has not changed state on its own.
		job.Downloaded = job.Total
	}
	return job
}

// totalBytes returns the entry size, preferring the megabyte field and falling
// back to the low/high 32-bit word pair older builds report.
func (h historySlot) totalBytes() int64 {
	if h.FileSizeMB > 0 {
		return megabytesToBytes(h.FileSizeMB)
	}
	if h.FileSizeHi == 0 && h.FileSizeLo == 0 {
		return 0
	}
	return (h.FileSizeHi << 32) | (h.FileSizeLo & 0xffffffff)
}

// jobState maps an NZBGet status onto a dlclient.State* value. SUCCESS is what
// both a finished group and a finished history entry report; everything that is
// not explicitly finished is still being worked on.
func jobState(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCESS":
		return dlclient.StateCompleted
	case "FAILURE", "DELETED":
		return dlclient.StateFailed
	case "PAUSED":
		return dlclient.StatePaused
	case "QUEUED":
		return dlclient.StateQueued
	default:
		return dlclient.StateDownloading
	}
}

// progressPercent computes how far a group has got, guarding against an unknown
// or zero total size and clamping the result to 0..100.
func progressPercent(fileSizeMB, remainingSizeMB float64) float64 {
	if fileSizeMB <= 0 {
		return 0
	}
	done := (fileSizeMB - remainingSizeMB) / fileSizeMB * 100
	if done < 0 {
		return 0
	}
	if done > 100 {
		return 100
	}
	return done
}

// megabytesToBytes converts a size reported in megabytes into bytes.
func megabytesToBytes(mb float64) int64 {
	if mb <= 0 {
		return 0
	}
	return int64(mb * 1024 * 1024)
}

// ---- Helpers ---------------------------------------------------------------

// statusError builds a descriptive error for an unexpected HTTP status.
func statusError(method string, statusCode int, body []byte) error {
	return fmt.Errorf("nzbget: %s: unexpected status %d: %s",
		method, statusCode, truncate(body, maxErrorBody))
}

// truncate collapses whitespace and shortens a response body for inclusion in
// an error message.
func truncate(body []byte, max int) string {
	s := strings.TrimSpace(string(body))
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
