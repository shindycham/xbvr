// Package indexer searches torrent and Usenet indexers.
//
// The wire protocol is Torznab/Newznab, the de-facto standard spoken by
// Prowlarr, Jackett and most indexers directly. One search returns a list of
// releases; the caller then parses each title with pkg/release to decide which
// one actually belongs to the scene it is looking for.
//
// Concrete implementations live in subpackages and register themselves:
//
//	package torznab
//
//	func init() { indexer.Register("torznab", newClient) }
//
// The wiring import lives in pkg/tasks, which keeps this package free of any
// dependency on its own implementations.
package indexer

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Protocol identifiers.
const (
	ProtocolTorznab = "torznab"
	ProtocolNewznab = "newznab"
)

// Config describes one configured indexer.
type Config struct {
	// ID is the xbvr-side identifier, unique across indexers.
	ID string `json:"id"`
	// Name is a display name, e.g. "Prowlarr".
	Name string `json:"name"`
	// Type selects the implementation: "torznab" or "newznab".
	Type string `json:"type"`
	// URL is the indexer's API endpoint, e.g.
	// "http://127.0.0.1:9696/1/api" for a Prowlarr indexer, or
	// "http://127.0.0.1:9117/api/v2.0/indexers/all/results/torznab/api"
	// for Jackett's aggregate endpoint.
	URL string `json:"url"`
	// APIKey is the indexer's API key, sent as "apikey".
	APIKey string `json:"apiKey"`
	// Categories limits searches to these newznab category ids, e.g. "6000".
	Categories []string `json:"categories"`
	// Enabled turns this indexer off without deleting its configuration.
	Enabled bool `json:"enabled"`
}

// Query is one search request.
type Query struct {
	// Q is the free text query.
	Q string
	// Categories overrides the indexer's configured categories when non-empty.
	Categories []string
	// Limit caps the number of results the indexer should return.
	Limit int
	// Types restricts the search by protocol: "torrent", "nzb" or "" for both.
	Types string
}

// Release is one search result.
type Release struct {
	// Indexer is the display name of the indexer that produced this release.
	Indexer string `json:"indexer"`
	// IndexerID is the xbvr-side id of that indexer.
	IndexerID string `json:"indexer_id"`
	// Title is the release name, which pkg/release parses.
	Title string `json:"title"`
	// DownloadURL fetches the .torrent or .nzb.
	DownloadURL string `json:"download_url"`
	// MagnetURL is the magnet URI, when the indexer supplies one.
	MagnetURL string `json:"magnet_url,omitempty"`
	// InfoHash is the torrent infohash, when known.
	InfoHash string `json:"info_hash,omitempty"`
	// Size is the payload size in bytes, 0 when unknown.
	Size int64 `json:"size"`
	// Seeders and Leechers are torrent-only, -1 when the indexer does not say.
	Seeders  int `json:"seeders"`
	Leechers int `json:"leechers"`
	// Categories are the newznab category ids the release belongs to.
	Categories []string `json:"categories,omitempty"`
	// PublishedAt is the release publication time, zero when unknown.
	PublishedAt time.Time `json:"published_at"`
	// Protocol is "torrent" or "nzb".
	Protocol string `json:"protocol"`
	// Grabs is the number of times the indexer has served this release, 0 when
	// unknown.
	Grabs int `json:"grabs,omitempty"`
}

// Category is one entry from an indexer's capabilities.
type Category struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Children []Category `json:"children,omitempty"`
}

// Client is implemented by every supported indexer protocol.
type Client interface {
	// ID returns the xbvr-side identifier from Config.
	ID() string
	// Name returns a display name for the indexer.
	Name() string
	// Type returns the protocol identifier.
	Type() string
	// Test verifies that the indexer answers and the API key is accepted.
	Test(ctx context.Context) error
	// Caps returns the indexer's categories.
	Caps(ctx context.Context) ([]Category, error)
	// Search runs one query.
	Search(ctx context.Context, q Query) ([]Release, error)
}

// Factory builds an indexer client from its configuration.
type Factory func(Config) (Client, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register makes a protocol implementation available under an id. It is called
// from init functions and is safe for concurrent use.
func Register(protocol string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[protocol] = factory
}

// New builds the client described by cfg.Type.
func New(cfg Config) (Client, error) {
	if cfg.Type == "" {
		return nil, errors.New("no indexer type selected")
	}
	if cfg.URL == "" {
		return nil, errors.New("indexer URL is required")
	}
	registryMu.RLock()
	factory, ok := registry[cfg.Type]
	registryMu.RUnlock()
	if !ok {
		return nil, errors.New("unsupported indexer type: " + cfg.Type)
	}
	return factory(cfg)
}

// SupportedProtocols lists the registered protocol ids, sorted.
func SupportedProtocols() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
