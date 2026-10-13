// Package sabnzbd implements the dlclient.Client contract for the SABnzbd API.
//
// SABnzbd exposes one endpoint, {base}/api, which takes a mode parameter and
// answers JSON. Authentication is by API key rather than a session, so there is
// no login handshake to manage; HTTP basic auth is still applied when a username
// is configured, because a reverse proxy in front of SABnzbd often demands it.
//
// Unlike a torrent client, SABnzbd moves a finished download out of the active
// queue and into a separate history list. List therefore merges the queue with
// the history: a caller that watched only the queue would never see a completed
// job, and would never import anything.
package sabnzbd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xbapps/xbvr/pkg/dlclient"
)

const (
	// clientID is the name this implementation registers under.
	clientID = "sabnzbd"

	// requestTimeout bounds every individual HTTP call, including the body read.
	requestTimeout = 20 * time.Second

	// maxBodySize caps how much of a response we are willing to buffer.
	maxBodySize = 64 << 20

	// maxErrorBody is how much of a failing response body ends up in an error.
	maxErrorBody = 512

	// redacted replaces the API key in anything bound for an error message.
	redacted = "REDACTED"

	// apiPath is appended to the configured base URL.
	apiPath = "/api"
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

// newClient is the dlclient.Factory for SABnzbd.
func newClient(cfg dlclient.Config) (dlclient.Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("sabnzbd: config URL is required")
	}
	return &client{
		cfg:  cfg,
		base: strings.TrimRight(strings.TrimSpace(cfg.URL), "/"),
		http: &http.Client{},
	}, nil
}

// New builds a SABnzbd client from its configuration.
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
	return "SABnzbd"
}

// Kind returns KindNZB: SABnzbd only speaks Usenet.
func (c *client) Kind() string { return dlclient.KindNZB }

// Test calls mode=version. SABnzbd answers HTTP 200 with {"status": false} for a
// bad API key, so the envelope check in call is as important as the HTTP status.
func (c *client) Test(ctx context.Context) error {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, "version", nil, nil, "", &out); err != nil {
		return err
	}
	if strings.TrimSpace(out.Version) == "" {
		return errors.New("sabnzbd: version response contained no version")
	}
	return nil
}

// Add hands a .nzb URL or raw .nzb body to SABnzbd and returns the resulting
// job. SABnzbd's add response is asynchronous: when it omits nzo_ids the job was
// still accepted, so a best-effort placeholder with an empty id is returned
// instead of an error, and the caller re-polls with List.
func (c *client) Add(ctx context.Context, req dlclient.AddRequest) (dlclient.Job, error) {
	name := strings.TrimSpace(req.Name)
	category := firstNonEmpty(req.Category, c.cfg.Category)

	var out addResponse
	if len(req.Content) > 0 {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, err := mw.CreateFormFile("nzbfile", "file.nzb")
		if err != nil {
			return dlclient.Job{}, fmt.Errorf("sabnzbd: build addfile request: %w", err)
		}
		if _, err := part.Write(req.Content); err != nil {
			return dlclient.Job{}, fmt.Errorf("sabnzbd: build addfile request: %w", err)
		}
		if err := mw.WriteField("nzbname", name); err != nil {
			return dlclient.Job{}, fmt.Errorf("sabnzbd: build addfile request: %w", err)
		}
		if category != "" {
			if err := mw.WriteField("cat", category); err != nil {
				return dlclient.Job{}, fmt.Errorf("sabnzbd: build addfile request: %w", err)
			}
		}
		if err := mw.Close(); err != nil {
			return dlclient.Job{}, fmt.Errorf("sabnzbd: build addfile request: %w", err)
		}
		if err := c.call(ctx, "addfile", nil, body.Bytes(), mw.FormDataContentType(), &out); err != nil {
			return dlclient.Job{}, err
		}
	} else {
		source := firstNonEmpty(req.URL, req.Magnet)
		if source == "" {
			return dlclient.Job{}, errors.New("sabnzbd: add requires a URL or NZB content")
		}
		query := url.Values{}
		query.Set("name", source)
		if category != "" {
			query.Set("cat", category)
		}
		if err := c.call(ctx, "addurl", query, nil, "", &out); err != nil {
			return dlclient.Job{}, err
		}
	}

	job := dlclient.Job{
		// SABnzbd has no free-form tag mechanism, so the xbvr ref rides on the
		// returned job for the caller to match against.
		Ref:      req.Ref,
		Name:     name,
		State:    dlclient.StateQueued,
		Category: category,
	}
	if len(out.NzoIDs) > 0 {
		job.ID = strings.TrimSpace(out.NzoIDs[0])
	}
	return job, nil
}

// List returns the active queue merged with the history. The queue alone can
// never contain a finished download, so the history half is what lets the caller
// observe StateCompleted and import the result.
func (c *client) List(ctx context.Context) ([]dlclient.Job, error) {
	var queue queueResponse
	if err := c.call(ctx, "queue", nil, nil, "", &queue); err != nil {
		return nil, err
	}
	var history historyResponse
	if err := c.call(ctx, "history", nil, nil, "", &history); err != nil {
		return nil, err
	}

	jobs := make([]dlclient.Job, 0, len(queue.Queue.Slots)+len(history.History.Slots))
	for _, slot := range queue.Queue.Slots {
		jobs = append(jobs, slot.job())
	}
	for _, slot := range history.History.Slots {
		jobs = append(jobs, slot.job())
	}
	return jobs, nil
}

// Remove deletes a job. SABnzbd removes it from the queue or the history as
// appropriate. The deleteData argument is unused: SABnzbd always deletes the
// data along with the job.
func (c *client) Remove(ctx context.Context, id string, deleteData bool) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return dlclient.ErrNotFound
	}
	query := url.Values{}
	query.Set("name", trimmed)

	err := c.call(ctx, "delete", query, nil, "", nil)
	if err == nil {
		return nil
	}
	// SABnzbd reports {"status": false, "error": ...} for an id it does not
	// know, which is the only realistic failure of delete, so any service-level
	// rejection maps to ErrNotFound. A 404 from an intermediary in front of
	// SABnzbd means the same thing here.
	var apiErr *apiError
	var statusErr *httpStatusError
	switch {
	case errors.As(err, &apiErr):
		return fmt.Errorf("sabnzbd: delete %q: %w", trimmed, dlclient.ErrNotFound)
	case errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound:
		return fmt.Errorf("sabnzbd: delete %q: %w", trimmed, dlclient.ErrNotFound)
	}
	return err
}

// ---- API plumbing ----------------------------------------------------------

// envelope is the status/error pair SABnzbd includes on a rejected call. Status
// is a pointer so that its absence, the norm on success, is distinguishable from
// an explicit false.
type envelope struct {
	Status *bool  `json:"status"`
	Error  string `json:"error"`
}

// apiError is a service-level failure reported inside the JSON envelope rather
// than as an HTTP status.
type apiError struct {
	mode    string
	message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("sabnzbd: %s: %s", e.mode, e.message)
}

// httpStatusError is an unexpected HTTP status. It is a distinct type so Remove
// can recognise a 404 from an intermediary and map it to ErrNotFound.
type httpStatusError struct {
	mode   string
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("sabnzbd: %s: unexpected status %d: %s", e.mode, e.status, e.body)
}

// call is the single place that touches HTTP: it builds the request, applies the
// API key and basic auth, enforces the timeout, checks the HTTP status, decodes
// the body into out and surfaces a service-level error. The public methods only
// have to supply the mode and interpret the decoded shape.
//
// POST is used for every mode, including the long addurl queries, because an
// oversized URL is what breaks GET.
func (c *client) call(ctx context.Context, mode string, query url.Values, body []byte, contentType string, out any) error {
	values := url.Values{}
	for key, vals := range query {
		values[key] = vals
	}
	values.Set("mode", mode)
	values.Set("output", "json")
	if key := strings.TrimSpace(c.cfg.APIKey); key != "" {
		values.Set("apikey", key)
	}
	endpoint := c.base + apiPath + "?" + values.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, reader)
	if err != nil {
		return fmt.Errorf("sabnzbd: build %s request: %s", mode, c.redact(err.Error()))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sabnzbd: %s: %s", mode, c.redact(err.Error()))
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return fmt.Errorf("sabnzbd: %s: read response: %w", mode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(mode, resp.StatusCode, data)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		// An empty body is not fatal: leave out at its zero value rather than
		// failing the decode and panicking on nothing.
		return nil
	}

	var env envelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return fmt.Errorf("sabnzbd: %s: decode response: %w", mode, err)
	}
	if env.Status != nil && !*env.Status {
		message := strings.TrimSpace(env.Error)
		if message == "" {
			message = "request rejected"
		}
		return &apiError{mode: mode, message: message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(trimmed, out); err != nil {
		return fmt.Errorf("sabnzbd: %s: decode response: %w", mode, err)
	}
	return nil
}

// redact removes the API key from a string bound for an error message. The key
// travels in the query string of every URL, so anything derived from a URL has
// to pass through here first.
func (c *client) redact(s string) string {
	key := strings.TrimSpace(c.cfg.APIKey)
	if key == "" {
		return s
	}
	s = strings.ReplaceAll(s, key, redacted)
	s = strings.ReplaceAll(s, url.QueryEscape(key), redacted)
	return s
}

// ---- Payload shapes --------------------------------------------------------

// addResponse is the reply to addfile and addurl. The nzo_ids array carries the
// job ids SABnzbd assigned, and is occasionally absent.
type addResponse struct {
	envelope
	NzoIDs []string `json:"nzo_ids"`
}

// queueResponse wraps mode=queue. Its slot fields use short names (mb, cat) that
// do not match the history list, so the two are decoded separately.
type queueResponse struct {
	envelope
	Queue struct {
		Slots []queueSlot `json:"slots"`
	} `json:"queue"`
}

// queueSlot is one entry of the active queue.
type queueSlot struct {
	NzoID      string     `json:"nzo_id"`
	Filename   string     `json:"filename"`
	Percentage jsonNumber `json:"percentage"`
	MB         jsonNumber `json:"mb"`
	MBLeft     jsonNumber `json:"mbleft"`
	TimeLeft   timeLeft   `json:"timeleft"`
	Status     string     `json:"status"`
	Cat        string     `json:"cat"`
	Storage    string     `json:"storage"`
}

// historyResponse wraps mode=history.
type historyResponse struct {
	envelope
	History struct {
		Slots []historySlot `json:"slots"`
	} `json:"history"`
}

// historySlot is one entry of SABnzbd's history list, which is where a finished
// download ends up once it leaves the queue.
type historySlot struct {
	NzoID       string     `json:"nzo_id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Bytes       jsonNumber `json:"bytes"`
	Storage     string     `json:"storage"`
	FailMessage string     `json:"fail_message"`
	Category    string     `json:"category"`
}

// ---- Normalisation ---------------------------------------------------------

// job maps a queue slot onto the shared job shape.
func (s queueSlot) job() dlclient.Job {
	total := s.MB.megabytes()
	left := s.MBLeft.megabytes()
	downloaded := total - left
	if downloaded < 0 {
		downloaded = 0
	}
	return dlclient.Job{
		ID:          s.NzoID,
		Name:        s.Filename,
		State:       queueState(s.Status),
		Progress:    clampProgress(s.Percentage.value()),
		Downloaded:  downloaded,
		Total:       total,
		ETA:         parseTimeLeft(s.TimeLeft),
		SavePath:    s.Storage,
		ContentPath: s.Storage,
		Category:    s.Cat,
	}
}

// job maps a history slot onto the shared job shape. Passed a completed entry,
// Storage is the directory the unpacked file ended up in, which is exactly what
// the caller needs to locate the video.
func (s historySlot) job() dlclient.Job {
	state := historyState(s.Status)
	size := s.Bytes.count()
	job := dlclient.Job{
		ID:          s.NzoID,
		Name:        s.Name,
		State:       state,
		Total:       size,
		SavePath:    s.Storage,
		ContentPath: s.Storage,
		Category:    s.Category,
	}
	switch state {
	case dlclient.StateCompleted:
		job.Progress = 100
		job.Downloaded = size
	case dlclient.StateFailed:
		job.Progress = 0
		job.Error = s.FailMessage
	default:
		// Extracting, Verifying, Repairing and friends: the download itself has
		// finished, but the job has not. Progress stays at zero because the
		// history entry does not report one.
		job.Downloaded = size
	}
	return job
}

// queueState maps a queue status onto a dlclient.State* value. The queue only
// holds unfinished jobs, so anything unrecognised counts as downloading.
func queueState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "paused":
		return dlclient.StatePaused
	case "queued":
		return dlclient.StateQueued
	default:
		return dlclient.StateDownloading
	}
}

// historyState maps a history status onto a dlclient.State* value. Anything not
// explicitly finished is still being worked on.
func historyState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return dlclient.StateCompleted
	case "failed":
		return dlclient.StateFailed
	case "paused":
		return dlclient.StatePaused
	case "queued":
		return dlclient.StateQueued
	default:
		return dlclient.StateDownloading
	}
}

// ---- Tolerant field types --------------------------------------------------

// jsonNumber is a JSON number that SABnzbd sometimes encodes as a string. Both
// forms turn up in the same slot depending on version, and decoding into a plain
// float64 either fails the whole response or silently reports zero.
type jsonNumber float64

// UnmarshalJSON accepts a JSON number, a numeric string, null or nothing.
func (n *jsonNumber) UnmarshalJSON(data []byte) error {
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
		if s == "" || s == "null" {
			*n = 0
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("invalid number %q", s)
		}
		*n = jsonNumber(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*n = jsonNumber(v)
	return nil
}

// value returns the number as a float64.
func (n jsonNumber) value() float64 { return float64(n) }

// megabytes converts a size reported in megabytes into bytes.
func (n jsonNumber) megabytes() int64 {
	if n <= 0 {
		return 0
	}
	return int64(float64(n) * 1024 * 1024)
}

// count converts a raw byte count into an int64.
func (n jsonNumber) count() int64 {
	if n <= 0 {
		return 0
	}
	return int64(n)
}

// timeLeft is SABnzbd's timeleft, normally "H:MM:SS" but accepted as a bare
// number of seconds too, since a type mismatch here would fail the whole decode.
type timeLeft string

// UnmarshalJSON accepts a string, a number, null or nothing.
func (t *timeLeft) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	switch trimmed {
	case "", "null":
		*t = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*t = timeLeft(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*t = timeLeft(n.String())
	return nil
}

// parseTimeLeft turns "H:MM:SS" into seconds, and a bare number of seconds into
// itself. Anything absent or malformed is zero.
func parseTimeLeft(value timeLeft) int64 {
	s := strings.TrimSpace(string(value))
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			return n
		}
		return 0
	}
	var total int64
	for _, part := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || n < 0 {
			return 0
		}
		total = total*60 + n
	}
	return total
}

// clampProgress keeps a reported percentage inside 0..100.
func clampProgress(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// ---- Helpers ---------------------------------------------------------------

// statusError builds a descriptive error for an unexpected HTTP status.
func statusError(mode string, statusCode int, body []byte) error {
	return &httpStatusError{mode: mode, status: statusCode, body: truncate(body, maxErrorBody)}
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
