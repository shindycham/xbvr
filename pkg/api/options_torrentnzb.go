package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/emicklei/go-restful/v3"

	"github.com/xbapps/xbvr/pkg/config"
)

// RequestSaveOptionsGrab is the torrent/NZB section of the options UI.
type RequestSaveOptionsGrab struct {
	Enabled             bool                 `json:"enabled"`
	ImportDir           string               `json:"importDir"`
	ImportMode          string               `json:"importMode"`
	RemoveAfterImport   bool                 `json:"removeAfterImport"`
	PollSeconds         int                  `json:"pollSeconds"`
	MinSeeders          int                  `json:"minSeeders"`
	PreferredResolution int                  `json:"preferredResolution"`
	PathMappings        []config.PathMapping `json:"pathMappings"`

	Indexers        []config.IndexerConfig        `json:"indexers"`
	DownloadClients []config.DownloadClientConfig `json:"downloadClients"`

	ReleaseMatchingEnabled    bool `json:"releaseMatchingEnabled"`
	ReleaseMatchingAutoScrape bool `json:"releaseMatchingAutoScrape"`
}

func (i ConfigResource) saveOptionsGrab(req *restful.Request, resp *restful.Response) {
	var r RequestSaveOptionsGrab
	if err := req.ReadEntity(&r); err != nil {
		log.Error(err)
		APIError(req, resp, http.StatusBadRequest, err)
		return
	}

	switch strings.ToLower(strings.TrimSpace(r.ImportMode)) {
	case "copy", "move":
		config.Config.TorrentNZB.ImportMode = strings.ToLower(strings.TrimSpace(r.ImportMode))
	default:
		// A hardlink is the only mode that lets the client keep seeding without
		// duplicating the file, so it is the safe default.
		config.Config.TorrentNZB.ImportMode = "hardlink"
	}
	if r.PollSeconds < 5 {
		r.PollSeconds = 15
	}

	config.Config.TorrentNZB.Enabled = r.Enabled
	config.Config.TorrentNZB.ImportDir = strings.TrimSpace(r.ImportDir)
	config.Config.TorrentNZB.RemoveAfterImport = r.RemoveAfterImport
	config.Config.TorrentNZB.PollSeconds = r.PollSeconds
	config.Config.TorrentNZB.MinSeeders = r.MinSeeders
	config.Config.TorrentNZB.PreferredResolution = r.PreferredResolution
	config.Config.TorrentNZB.PathMappings = normalisePathMappings(r.PathMappings)

	config.Config.ReleaseMatching.Enabled = r.ReleaseMatchingEnabled
	config.Config.ReleaseMatching.AutoScrape = r.ReleaseMatchingAutoScrape

	config.Config.Indexers = normaliseIndexers(r.Indexers)
	config.Config.DownloadClients = normaliseDownloadClients(r.DownloadClients)

	config.SaveConfig()

	// Return the same shape the status endpoint serves, so a client can replace
	// its state wholesale rather than merging and risking stale indexer or
	// client ids.
	resp.WriteHeaderAndEntity(http.StatusOK, GrabStatus())
}

// normalisePathMappings trims the submitted mappings and drops the useless
// ones, so a half-filled row cannot rewrite a path into nonsense.
func normalisePathMappings(submitted []config.PathMapping) []config.PathMapping {
	out := make([]config.PathMapping, 0, len(submitted))
	for _, mapping := range submitted {
		from := strings.TrimRight(strings.TrimSpace(mapping.From), "/")
		to := strings.TrimRight(strings.TrimSpace(mapping.To), "/")
		if from == "" || to == "" || from == to {
			continue
		}
		out = append(out, config.PathMapping{From: from, To: to})
	}
	return out
}

// normaliseIndexers trims the submitted indexers, assigns identifiers to new
// ones, and drops entries with no URL.
//
// Identifiers submitted by the UI are kept as they are: a grab in flight refers
// to its indexer by id, so regenerating them would orphan it.
func normaliseIndexers(submitted []config.IndexerConfig) []config.IndexerConfig {
	out := make([]config.IndexerConfig, 0, len(submitted))
	seen := map[string]bool{}

	for position, cfg := range submitted {
		cfg.Name = strings.TrimSpace(cfg.Name)
		cfg.URL = strings.TrimSpace(cfg.URL)
		cfg.APIKey = strings.TrimSpace(cfg.APIKey)
		if cfg.URL == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(cfg.Type), "newznab") {
			cfg.Type = "newznab"
		} else {
			cfg.Type = "torznab"
		}
		for i := range cfg.Categories {
			cfg.Categories[i] = strings.TrimSpace(cfg.Categories[i])
		}
		if cfg.ID == "" || seen[cfg.ID] {
			cfg.ID = newConfigID("indexer", cfg.Name, position, seen)
		}
		seen[cfg.ID] = true
		out = append(out, cfg)
	}
	return out
}

func normaliseDownloadClients(submitted []config.DownloadClientConfig) []config.DownloadClientConfig {
	out := make([]config.DownloadClientConfig, 0, len(submitted))
	seen := map[string]bool{}

	for position, cfg := range submitted {
		cfg.Name = strings.TrimSpace(cfg.Name)
		cfg.URL = strings.TrimSpace(cfg.URL)
		cfg.Client = strings.ToLower(strings.TrimSpace(cfg.Client))
		cfg.Category = strings.TrimSpace(cfg.Category)
		cfg.SavePath = strings.TrimSpace(cfg.SavePath)
		if cfg.URL == "" || cfg.Client == "" {
			continue
		}
		// Only two kinds exist, so anything that is not explicitly Usenet is a
		// torrent client.
		if strings.EqualFold(strings.TrimSpace(cfg.Kind), "nzb") {
			cfg.Kind = "nzb"
		} else {
			cfg.Kind = "torrent"
		}
		if cfg.ID == "" || seen[cfg.ID] {
			cfg.ID = newConfigID("client", cfg.Name, position, seen)
		}
		seen[cfg.ID] = true
		out = append(out, cfg)
	}
	return out
}

// newConfigID builds a readable, unique identifier for a new entry from its
// display name, falling back to the position when there is no usable name.
func newConfigID(prefix, name string, position int, taken map[string]bool) string {
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, name)

	parts := strings.FieldsFunc(base, func(r rune) bool { return r == '-' })
	base = strings.Join(parts, "-")
	if base == "" {
		base = fmt.Sprintf("%s-%d", prefix, position+1)
	}

	candidate := base
	for i := 2; taken[candidate]; i++ {
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	return candidate
}
