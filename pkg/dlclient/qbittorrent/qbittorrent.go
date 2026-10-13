// Package qbittorrent implements the dlclient.Client contract for the
// qBittorrent WebUI API v2 (qBittorrent 4.1 and later).
//
// It authenticates with the /api/v2/auth/login endpoint and keeps the resulting
// SID cookie in a private cookie jar. Sessions expire, so any call that comes
// back 401 or 403 is retried once after an automatic re-login.
package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbapps/xbvr/pkg/dlclient"
)

const (
	// clientID is the name this implementation registers under.
	clientID = "qbittorrent"

	// requestTimeout bounds every individual HTTP call, including the body read.
	requestTimeout = 15 * time.Second

	// maxBodySize caps how much of a response we are willing to buffer. Torrent
	// listings for very large instances can be sizeable, so this is generous.
	maxBodySize = 64 << 20

	// maxErrorBody is how much of a failing response body ends up in an error.
	maxErrorBody = 512

	// etaInfinity is qBittorrent's sentinel for "eta unknown / infinite".
	etaInfinity = 8640000

	// tagPrefix marks xbvr-owned torrents and carries the xbvr job ref.
	tagPrefix = "xbvr-"

	baseAuthPath    = "/api/v2/auth/login"
	baseVersionPath = "/api/v2/app/version"
	baseListPath    = "/api/v2/torrents/info"
	baseAddPath     = "/api/v2/torrents/add"
	baseDeletePath  = "/api/v2/torrents/delete"
)

func init() {
	dlclient.Register(clientID, newClient)
}

// torrent mirrors the fields of one /api/v2/torrents/info entry that we care
// about. All numeric fields are JSON numbers; everything else is a string.
type torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Category    string  `json:"category"`
	Tags        string  `json:"tags"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	MagnetURI   string  `json:"magnet_uri"`
	Progress    float64 `json:"progress"`
	Size        int64   `json:"size"`
	Downloaded  int64   `json:"downloaded"`
	DlSpeed     int64   `json:"dlspeed"`
	ETA         int64   `json:"eta"`
	Ratio       float64 `json:"ratio"`
	AmountLeft  int64   `json:"amount_left"`
}

// client is the dlclient.Client implementation.
type client struct {
	cfg  dlclient.Config
	base string
	http *http.Client

	// loginMu serialises (re-)authentication so concurrent calls do not race
	// each other into a login storm. loggedIn is guarded by it.
	loginMu  sync.Mutex
	loggedIn bool
}

// newClient builds a qBittorrent client from its configuration.
func newClient(cfg dlclient.Config) (dlclient.Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("qbittorrent: config URL is required")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent: create cookie jar: %w", err)
	}
	return &client{
		cfg:  cfg,
		base: strings.TrimRight(strings.TrimSpace(cfg.URL), "/"),
		http: &http.Client{Jar: jar},
	}, nil
}

// New builds a qBittorrent client from its configuration.
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
	return "qBittorrent"
}

// Kind returns the payload kind, defaulting to torrent.
func (c *client) Kind() string {
	if c.cfg.Kind != "" {
		return c.cfg.Kind
	}
	return dlclient.KindTorrent
}

// Test logs in and then reads the application version to prove the session
// actually works.
func (c *client) Test(ctx context.Context) error {
	if err := c.login(ctx); err != nil {
		return err
	}
	resp, data, err := c.call(ctx, http.MethodGet, baseVersionPath, nil, nil, "")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return statusError(baseVersionPath, resp, data)
	}
	return nil
}

// List returns every torrent the client can see, optionally narrowed to the
// configured category.
func (c *client) List(ctx context.Context) ([]dlclient.Job, error) {
	var query url.Values
	if cat := strings.TrimSpace(c.cfg.Category); cat != "" {
		query = url.Values{"category": {cat}}
	}
	resp, data, err := c.call(ctx, http.MethodGet, baseListPath, query, nil, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(baseListPath, resp, data)
	}

	var torrents []torrent
	if err := json.Unmarshal(data, &torrents); err != nil {
		return nil, fmt.Errorf("qbittorrent: decode torrent list: %w", err)
	}

	jobs := make([]dlclient.Job, 0, len(torrents))
	for _, t := range torrents {
		jobs = append(jobs, t.job())
	}
	return jobs, nil
}

// Add hands a magnet, URL or raw .torrent to qBittorrent and then tries to
// resolve the resulting job. qBittorrent's add endpoint returns no identifier,
// so the job is located by its xbvr tag (or by name as a fallback). When the
// torrent has not appeared yet the best-effort job is returned without error;
// callers are expected to re-poll with List.
func (c *client) Add(ctx context.Context, req dlclient.AddRequest) (dlclient.Job, error) {
	source := strings.TrimSpace(req.Magnet)
	if source == "" {
		source = strings.TrimSpace(req.URL)
	}
	if source == "" && len(req.Content) == 0 {
		return dlclient.Job{}, errors.New("qbittorrent: add requires a magnet, a URL or torrent content")
	}

	savePath := firstNonEmpty(req.SavePath, c.cfg.SavePath)
	category := firstNonEmpty(req.Category, c.cfg.Category)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if source != "" {
		if err := mw.WriteField("urls", source); err != nil {
			return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
		}
	}
	if len(req.Content) > 0 {
		part, err := mw.CreateFormFile("torrents", "file.torrent")
		if err != nil {
			return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
		}
		if _, err := part.Write(req.Content); err != nil {
			return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
		}
	}
	if savePath != "" {
		if err := mw.WriteField("savepath", savePath); err != nil {
			return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
		}
	}
	if category != "" {
		if err := mw.WriteField("category", category); err != nil {
			return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
		}
	}
	if ref := strings.TrimSpace(req.Ref); ref != "" {
		if err := mw.WriteField("tags", tagPrefix+ref); err != nil {
			return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
		}
	}
	// "paused" was renamed to "stopped" in qBittorrent 5.0, with no alias, and
	// each version ignores the other's key. Sending both keeps the flag working
	// across versions.
	paused := strconv.FormatBool(req.Paused)
	if err := mw.WriteField("paused", paused); err != nil {
		return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
	}
	if err := mw.WriteField("stopped", paused); err != nil {
		return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
	}
	if err := mw.Close(); err != nil {
		return dlclient.Job{}, fmt.Errorf("qbittorrent: build add request: %w", err)
	}

	resp, data, err := c.call(ctx, http.MethodPost, baseAddPath, nil, body.Bytes(), mw.FormDataContentType())
	if err != nil {
		return dlclient.Job{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return dlclient.Job{}, statusError(baseAddPath, resp, data)
	}
	if got := strings.TrimSpace(string(data)); got != "Ok." {
		return dlclient.Job{}, fmt.Errorf("qbittorrent: add rejected: %s", truncate(data, maxErrorBody))
	}

	// Resolve the freshly added torrent when it is already visible.
	if ref := strings.TrimSpace(req.Ref); ref != "" {
		if job, ok := c.lookup(ctx, func(j dlclient.Job) bool { return j.Ref == ref }); ok {
			return job, nil
		}
	} else if name := strings.TrimSpace(req.Name); name != "" {
		if job, ok := c.lookup(ctx, func(j dlclient.Job) bool { return j.Name == name }); ok {
			return job, nil
		}
	}

	// Not visible yet: report a best-effort placeholder rather than failing.
	return dlclient.Job{
		Ref:      req.Ref,
		Name:     req.Name,
		State:    dlclient.StateQueued,
		SavePath: savePath,
		Category: category,
	}, nil
}

// lookup lists torrents and returns the first match. A listing failure is not
// fatal here: Add falls back to a best-effort job.
func (c *client) lookup(ctx context.Context, match func(dlclient.Job) bool) (dlclient.Job, bool) {
	jobs, err := c.List(ctx)
	if err != nil {
		return dlclient.Job{}, false
	}
	for _, j := range jobs {
		if match(j) {
			return j, true
		}
	}
	return dlclient.Job{}, false
}

// Remove deletes a torrent, optionally with its downloaded data.
func (c *client) Remove(ctx context.Context, id string, deleteData bool) error {
	if strings.TrimSpace(id) == "" {
		return dlclient.ErrNotFound
	}
	form := url.Values{}
	form.Set("hashes", id)
	form.Set("deleteFiles", strconv.FormatBool(deleteData))

	resp, data, err := c.call(ctx, http.MethodPost, baseDeletePath, nil,
		[]byte(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return dlclient.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return statusError(baseDeletePath, resp, data)
	}
	return nil
}

// login authenticates unconditionally and stores the SID cookie in the
// client's jar.
func (c *client) login(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()

	if err := c.doLogin(ctx); err != nil {
		return err
	}
	c.loggedIn = true
	return nil
}

// ensureLogin authenticates only when no successful login has happened yet.
func (c *client) ensureLogin(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()

	if c.loggedIn {
		return nil
	}
	if err := c.doLogin(ctx); err != nil {
		return err
	}
	c.loggedIn = true
	return nil
}

// doLogin performs the actual login request. Callers must hold loginMu.
func (c *client) doLogin(ctx context.Context) error {
	form := url.Values{}
	form.Set("username", c.cfg.Username)
	form.Set("password", c.cfg.Password)

	resp, data, err := c.attempt(ctx, http.MethodPost, baseAuthPath, nil,
		[]byte(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusForbidden {
		return errors.New("qbittorrent: authentication failed (HTTP 403)")
	}
	if resp.StatusCode != http.StatusOK {
		return statusError(baseAuthPath, resp, data)
	}
	switch strings.TrimSpace(string(data)) {
	case "Ok.":
		return nil
	case "Fails.":
		return errors.New("qbittorrent: authentication failed: invalid username or password")
	default:
		return fmt.Errorf("qbittorrent: authentication failed: unexpected response %q",
			truncate(data, maxErrorBody))
	}
}

// call performs one authenticated API call, logging in first when needed and
// transparently re-logging-in and retrying once when the session has expired.
func (c *client) call(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) (*http.Response, []byte, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, nil, err
	}

	resp, data, err := c.attempt(ctx, method, path, query, body, contentType)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return resp, data, nil
	}

	// Session expired. Drop the cached session and re-authenticate, then retry
	// once; if the login itself fails, surface the original response instead.
	c.loginMu.Lock()
	c.loggedIn = false
	c.loginMu.Unlock()
	if err := c.ensureLogin(ctx); err != nil {
		return resp, data, nil
	}
	return c.attempt(ctx, method, path, query, body, contentType)
}

// attempt performs a single HTTP round trip and buffers the response body. The
// caller's context is the parent of a 15 second timeout, so cancellation
// propagates from above.
func (c *client) attempt(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) (*http.Response, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := c.base + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, endpoint, reader)
	if err != nil {
		return nil, nil, fmt.Errorf("qbittorrent: build %s %s: %w", method, path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Referer", c.base)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("qbittorrent: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, nil, fmt.Errorf("qbittorrent: read %s %s response: %w", method, path, err)
	}
	return resp, data, nil
}

// job converts a raw torrent into the normalised job representation.
func (t torrent) job() dlclient.Job {
	job := dlclient.Job{
		ID:          t.Hash,
		Ref:         refFromTags(t.Tags),
		Name:        t.Name,
		State:       normalizeState(t.State),
		Progress:    t.Progress * 100,
		Downloaded:  t.Downloaded,
		Total:       t.Size,
		Speed:       t.DlSpeed,
		ETA:         normalizeETA(t.ETA),
		SavePath:    t.SavePath,
		ContentPath: t.ContentPath,
		Category:    t.Category,
		Ratio:       t.Ratio,
		Seeding:     isSeedingState(t.State),
	}
	if isErrorState(t.State) {
		job.Error = t.State
	}
	return job
}

// refFromTags extracts the first xbvr-<ref> tag from a comma separated tag list.
func refFromTags(tags string) string {
	for _, tag := range strings.Split(tags, ",") {
		tag = strings.TrimSpace(tag)
		if ref, ok := strings.CutPrefix(tag, tagPrefix); ok && ref != "" {
			return ref
		}
	}
	return ""
}

// normalizeState maps a qBittorrent state string onto a dlclient.State* value.
//
// The UP/DL families cover 4.x. qBittorrent 5.0 renamed the stopped states to
// stoppedUP/stoppedDL, and stoppedUP is what a completed torrent reports once it
// stops seeding, so both names must be handled: without them a finished download
// would report as queued and never be imported.
func normalizeState(state string) string {
	switch state {
	case "error", "missingFiles", "unknown":
		return dlclient.StateFailed
	case "uploading", "pausedUP", "queuedUP", "stalledUP", "checkingUP", "forcedUP", "stoppedUP":
		return dlclient.StateCompleted
	case "downloading", "metaDL", "forcedMetaDL", "forcedDL", "checkingDL", "allocating", "moving", "checkingResumeData":
		return dlclient.StateDownloading
	case "stalledDL":
		return dlclient.StateStalled
	case "pausedDL", "stoppedDL":
		return dlclient.StatePaused
	case "queuedDL":
		return dlclient.StateQueued
	default:
		return dlclient.StateQueued
	}
}

// isErrorState reports whether the state is one we map to StateFailed.
func isErrorState(state string) bool {
	return normalizeState(state) == dlclient.StateFailed
}

// isSeedingState reports whether the state belongs to the uploading family.
func isSeedingState(state string) bool {
	switch state {
	case "uploading", "pausedUP", "queuedUP", "stalledUP", "checkingUP", "forcedUP", "stoppedUP":
		return true
	default:
		return false
	}
}

// normalizeETA turns qBittorrent's unknown/infinite sentinel into zero.
func normalizeETA(eta int64) int64 {
	if eta == etaInfinity || eta < 0 {
		return 0
	}
	return eta
}

// statusError builds a descriptive error for an unexpected HTTP status.
func statusError(path string, resp *http.Response, body []byte) error {
	return fmt.Errorf("qbittorrent: %s: unexpected status %d: %s",
		path, resp.StatusCode, truncate(body, maxErrorBody))
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
