// Package dlclient talks to external download clients.
//
// xbvr does not speak BitTorrent or NNTP itself. Torrents and NZBs are handed to
// a client the user already runs (qBittorrent, Transmission, SABnzbd, NZBGet),
// which owns the hard parts: peer swarms, seeding, article assembly, PAR2 repair
// and unpacking. xbvr's job is to decide *what* to download and to import the
// result into the library afterwards.
//
// Each concrete client lives in its own subpackage and registers itself here
// from an init function, so adding a client never means editing this file:
//
//	package qbittorrent
//
//	func init() { dlclient.Register("qbittorrent", newClient) }
//
// The wiring import lives in pkg/tasks, which keeps this package free of any
// dependency on its own implementations.
package dlclient

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// Kinds of payload a client can handle.
const (
	KindTorrent = "torrent"
	KindNZB     = "nzb"
)

// Normalised job states. Every client reports progress in these terms so the
// download queue and the UI never have to know which client is underneath.
const (
	StateQueued      = "queued"
	StateDownloading = "downloading"
	StateStalled     = "stalled"
	StatePaused      = "paused"
	StateCompleted   = "completed"
	StateFailed      = "failed"
)

// ErrNotFound is returned when a job is no longer known to the client.
var ErrNotFound = errors.New("download client: job not found")

// Config describes one configured client. Only the fields relevant to a given
// client are populated.
type Config struct {
	// ID is the xbvr-side identifier for this client, unique across clients.
	ID string `json:"id"`
	// Name is a display name, defaulting to the client's own name.
	Name string `json:"name"`
	// Client selects the implementation, e.g. "qbittorrent" or "transmission".
	Client string `json:"client"`
	// Kind is KindTorrent or KindNZB.
	Kind string `json:"kind"`
	// URL is the base URL, e.g. "http://127.0.0.1:8080".
	URL string `json:"url"`
	// Username and Password are used by clients that authenticate with a login.
	Username string `json:"username"`
	Password string `json:"password"`
	// APIKey is used by clients that authenticate with a key (SABnzbd).
	APIKey string `json:"apiKey"`
	// Category is applied to every job xbvr adds, which is also how xbvr finds
	// its own jobs again in the client's list.
	Category string `json:"category"`
	// SavePath is passed to the client as the download destination. When empty
	// the client's own default is used.
	SavePath string `json:"savePath"`
	// Enabled turns this client off without deleting its configuration.
	Enabled bool `json:"enabled"`
}

// AddRequest is a payload handed to a download client.
type AddRequest struct {
	// Ref is xbvr's own identifier for the job. Clients that cannot store
	// arbitrary metadata (qBittorrent, Transmission) get it as a tag, so the
	// job can be recognised again in List without relying on the name.
	Ref string `json:"ref"`
	// Name is the release name, used for logging and to name the job.
	Name string `json:"name"`
	// Magnet is a magnet URI, for torrent clients.
	Magnet string `json:"magnet"`
	// URL is an http(s) URL to a .torrent or .nzb, for clients that fetch it.
	URL string `json:"url"`
	// Content is a raw .torrent or .nzb body, for clients that accept an upload.
	Content []byte `json:"-"`
	// SavePath and Category override the client defaults for this one job.
	SavePath string `json:"savePath"`
	Category string `json:"category"`
	// Paused asks the client to add the job without starting it, where supported.
	Paused bool `json:"paused"`
}

// Job is a snapshot of one job in a client.
type Job struct {
	// ID is the client's own job identifier: a torrent hash, or an NZO id.
	ID string `json:"id"`
	// Ref is the xbvr identifier handed over in AddRequest, when the client
	// supports carrying one.
	Ref string `json:"ref,omitempty"`
	// Name is the job name as the client knows it.
	Name string `json:"name"`
	// State is one of the normalised State* values.
	State string `json:"state"`
	// Progress is 0-100.
	Progress float64 `json:"progress"`
	// Downloaded and Total are in bytes; Total may be 0 when unknown.
	Downloaded int64 `json:"downloaded"`
	Total      int64 `json:"total"`
	// Speed is in bytes per second, ETA in seconds.
	Speed int64 `json:"speed"`
	ETA   int64 `json:"eta"`
	// SavePath is the directory the client is downloading into, and
	// ContentPath is the specific file or directory produced, once known.
	SavePath    string `json:"save_path,omitempty"`
	ContentPath string `json:"content_path,omitempty"`
	// Category the client holds the job under.
	Category string `json:"category,omitempty"`
	// Error carries the client's failure message, if any.
	Error string `json:"error,omitempty"`
	// Ratio and Seeding are torrent-specific, zero for NZB clients.
	Ratio   float64 `json:"ratio,omitempty"`
	Seeding bool    `json:"seeding,omitempty"`
}

// IsTerminal reports whether a job has stopped changing on its own.
func (j Job) IsTerminal() bool {
	return j.State == StateCompleted || j.State == StateFailed
}

// Client is implemented by every supported download client.
type Client interface {
	// ID returns the xbvr-side identifier from Config.
	ID() string
	// Name returns a display name for the client.
	Name() string
	// Kind returns KindTorrent or KindNZB.
	Kind() string
	// Test verifies that the client is reachable and the credentials are right.
	Test(ctx context.Context) error
	// Add hands a release to the client and returns the resulting job.
	Add(ctx context.Context, req AddRequest) (Job, error)
	// List returns the client's jobs. Implementations should return every job
	// they can see; the caller filters by category or ref.
	List(ctx context.Context) ([]Job, error)
	// Remove forgets a job, optionally deleting the downloaded data. Removing an
	// unknown job returns ErrNotFound.
	Remove(ctx context.Context, id string, deleteData bool) error
}

// Factory builds a client from its configuration.
type Factory func(Config) (Client, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes a client implementation available under an id. It is called
// from init functions and is safe for concurrent use.
func Register(clientID string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[clientID] = factory
}

// New builds the client named in cfg.Client.
func New(cfg Config) (Client, error) {
	if cfg.Client == "" {
		return nil, errors.New("no download client selected")
	}
	registryMu.RLock()
	factory, ok := registry[cfg.Client]
	registryMu.RUnlock()
	if !ok {
		return nil, errors.New("unsupported download client: " + cfg.Client)
	}
	return factory(cfg)
}

// SupportedClients lists the registered client ids, sorted.
func SupportedClients() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Validate reports whether a configuration is complete enough to use.
func (c Config) Validate() error {
	if c.Client == "" {
		return errors.New("no client selected")
	}
	if c.Kind != KindTorrent && c.Kind != KindNZB {
		return errors.New("client kind must be " + KindTorrent + " or " + KindNZB)
	}
	if c.URL == "" {
		return errors.New("client URL is required")
	}
	return nil
}
