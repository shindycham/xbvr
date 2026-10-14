package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbapps/xbvr/pkg/common"
	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/downloadfile"
	"github.com/xbapps/xbvr/pkg/models"
	"github.com/xbapps/xbvr/pkg/slr"
	"github.com/xbapps/xbvr/pkg/vrsmash"
)

// DownloadTopic is the WAMP topic the UI subscribes to for live progress.
const DownloadTopic = "state.change.download"

// Download statuses.
const (
	DownloadStatusResolving   = "resolving"
	DownloadStatusQueued      = "queued"
	DownloadStatusDownloading = "downloading"
	DownloadStatusCompleted   = "completed"
	DownloadStatusFailed      = "failed"
	DownloadStatusCancelled   = "cancelled"
)

// DownloadRendition is one selectable quality from any provider.
type DownloadRendition struct {
	Provider string `json:"provider"`
	Codec    string `json:"codec"`
	Quality  string `json:"quality"`
	Height   int    `json:"height"`
	Width    int    `json:"width"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

// DownloadQuota is one allowance bucket.
type DownloadQuota struct {
	Used      int       `json:"used"`
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetsAt  time.Time `json:"resets_at"`
}

// DownloadQuotaInfo is a provider's remaining download allowance.
type DownloadQuotaInfo struct {
	Provider  string        `json:"provider"`
	Scene     DownloadQuota `json:"scene_downloads"`
	Script    DownloadQuota `json:"script_downloads"`
	FetchedAt time.Time     `json:"fetched_at"`
	Error     string        `json:"error,omitempty"`
}

// Download is a snapshot of one download, safe to serialise to the UI.
type Download struct {
	ID           string    `json:"id"`
	Provider     string    `json:"provider"`
	ProviderName string    `json:"provider_name"`
	SceneDBID    uint      `json:"scene_db_id"`
	SceneID      string    `json:"scene_id"`
	Title        string    `json:"title"`
	Quality      string    `json:"quality"`
	Height       int       `json:"height"`
	Codec        string    `json:"codec"`
	Status       string    `json:"status"`
	Downloaded   int64     `json:"downloaded"`
	Total        int64     `json:"total"`
	Progress     float64   `json:"progress"`
	Speed        int64     `json:"speed"`
	ETA          int64     `json:"eta"`
	Destination  string    `json:"destination"`
	Error        string    `json:"error"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}

type downloadState struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	snapshot Download

	lastBytes   int64
	lastTime    time.Time
	lastPublish time.Time
}

func (s *downloadState) pub() Download {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

type downloadManager struct {
	mu        sync.Mutex
	downloads map[string]*downloadState
}

var downloads = &downloadManager{downloads: map[string]*downloadState{}}

// --------------------------------------------------------------------------- //
// Provider registry
// --------------------------------------------------------------------------- //

type downloadProvider struct {
	ID             string
	Name           string
	Matches        func(models.Scene) bool
	Renditions     func(models.Scene) ([]DownloadRendition, error)
	Quota          func(force bool) (DownloadQuotaInfo, error)
	Filename       func(models.Scene, string, string) string
	Download       func(context.Context, string, string, downloadfile.ProgressFunc) (int64, error)
	Dir            func() string
	Enabled        func() bool
	PreferredCodec func() string
	Concurrent     func() int
	Rescan         func() bool
	// InvalidateQuota drops any cached allowance for this provider. A finished
	// download changes the count, and a cached one would report the old figure.
	InvalidateQuota func()
}

var downloadProviders []downloadProvider

func registerDownloadProvider(p downloadProvider) {
	downloadProviders = append(downloadProviders, p)
}

func providerByID(id string) *downloadProvider {
	for i := range downloadProviders {
		if strings.EqualFold(downloadProviders[i].ID, id) {
			return &downloadProviders[i]
		}
	}
	return nil
}

func providerForScene(scene models.Scene) *downloadProvider {
	for i := range downloadProviders {
		if downloadProviders[i].Matches(scene) {
			return &downloadProviders[i]
		}
	}
	return nil
}

// ProviderInfo describes a provider to the UI.
type ProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GetDownloadProviders lists the registered providers.
func GetDownloadProviders() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(downloadProviders))
	for _, p := range downloadProviders {
		out = append(out, ProviderInfo{ID: p.ID, Name: p.Name})
	}
	return out
}

func init() {
	registerDownloadProvider(downloadProvider{
		ID:      "slr",
		Name:    "SexLikeReal",
		Matches: slr.IsSLRScene,
		Renditions: func(scene models.Scene) ([]DownloadRendition, error) {
			rs, err := slr.GetRenditions(scene)
			if err != nil {
				return nil, err
			}
			out := make([]DownloadRendition, 0, len(rs))
			for _, r := range rs {
				out = append(out, DownloadRendition{
					Provider: "slr",
					Codec:    r.Codec,
					Quality:  fmt.Sprintf("%dp", r.Height),
					Height:   r.Height,
					Width:    r.Width,
					Size:     r.Size,
					URL:      r.URL,
				})
			}
			return out, nil
		},
		Quota: func(force bool) (DownloadQuotaInfo, error) {
			q := slr.FetchQuota(force)
			return DownloadQuotaInfo{
				Provider: "slr",
				Scene: DownloadQuota{
					Used: q.Scene.Used, Limit: q.Scene.Limit, Remaining: q.Scene.Remaining, ResetsAt: q.Scene.ResetsAt,
				},
				Script: DownloadQuota{
					Used: q.Script.Used, Limit: q.Script.Limit, Remaining: q.Script.Remaining, ResetsAt: q.Script.ResetsAt,
				},
				FetchedAt: q.FetchedAt,
				Error:     q.Error,
			}, nil
		},
		Filename: func(scene models.Scene, quality, codec string) string {
			return slr.ExpectedFilename(scene, heightFromQuality(quality), codec)
		},
		Download:        slr.DownloadFile,
		Dir:             slrDownloadDir,
		Enabled:         func() bool { return config.Config.SLRDownloader.Enabled },
		PreferredCodec:  func() string { return config.Config.SLRDownloader.PreferredCodec },
		Concurrent:      func() int { return config.Config.SLRDownloader.ConcurrentDownloads },
		Rescan:          func() bool { return config.Config.SLRDownloader.RescanAfterDownload },
		InvalidateQuota: slr.InvalidateQuota,
	})

	registerDownloadProvider(downloadProvider{
		ID:      "vrsmash",
		Name:    "VRSmash",
		Matches: vrsmash.IsScene,
		Renditions: func(scene models.Scene) ([]DownloadRendition, error) {
			rs, err := vrsmash.GetRenditions(scene)
			if err != nil {
				return nil, err
			}
			out := make([]DownloadRendition, 0, len(rs))
			for _, r := range rs {
				out = append(out, DownloadRendition{
					Provider: "vrsmash",
					Codec:    r.Codec,
					Quality:  r.Quality,
					Height:   r.Height,
					Width:    r.Width,
					Size:     r.Size,
					URL:      r.URL,
				})
			}
			return out, nil
		},
		Quota: func(force bool) (DownloadQuotaInfo, error) {
			return DownloadQuotaInfo{
				Provider:  "vrsmash",
				FetchedAt: time.Now(),
				Error:     "VRSmash does not report a download quota",
			}, nil
		},
		Filename: func(scene models.Scene, quality, codec string) string {
			return vrsmash.ExpectedFilename(scene, quality)
		},
		Download:       vrsmash.DownloadFile,
		Dir:            vrsmash.DownloadDir,
		Enabled:        func() bool { return config.Config.VRSMashDownloader.Enabled },
		PreferredCodec: func() string { return config.Config.VRSMashDownloader.PreferredCodec },
		Concurrent:     func() int { return config.Config.SLRDownloader.ConcurrentDownloads },
		Rescan:         func() bool { return config.Config.SLRDownloader.RescanAfterDownload },
	})
}

// --------------------------------------------------------------------------- //
// Public API
// --------------------------------------------------------------------------- //

// GetDownloadRenditions lists the available qualities for a scene.
func GetDownloadRenditions(scene models.Scene) ([]DownloadRendition, error) {
	p := providerForScene(scene)
	if p == nil {
		return nil, fmt.Errorf("no downloader supports this scene")
	}
	return p.Renditions(scene)
}

// GetDownloadQuota returns the remaining allowance for a provider.
func GetDownloadQuota(providerID string, force bool) (DownloadQuotaInfo, error) {
	p := providerByID(providerID)
	if p == nil {
		return DownloadQuotaInfo{}, fmt.Errorf("unknown download provider %q", providerID)
	}
	return p.Quota(force)
}

// StartDownload validates the request, registers the download and returns
// immediately while the transfer runs in the background.
func StartDownload(scene models.Scene, quality, codec string) (Download, error) {
	p := providerForScene(scene)
	if p == nil {
		return Download{}, fmt.Errorf("no downloader supports this scene")
	}
	if !p.Enabled() {
		return Download{}, fmt.Errorf("%s downloads are disabled", p.Name)
	}
	dir := p.Dir()
	if dir == "" {
		return Download{}, fmt.Errorf("no %s download folder configured; set one in Options -> Downloads", p.Name)
	}
	if codec == "" {
		codec = p.PreferredCodec()
	}
	slrSem.setLimit(p.Concurrent())

	key := fmt.Sprintf("%s:%d:%s:%s", p.ID, scene.ID, quality, codec)
	if quality == "" {
		key = fmt.Sprintf("%s:%d:best:%s", p.ID, scene.ID, codec)
	}

	downloads.mu.Lock()
	if existing, ok := downloads.downloads[key]; ok {
		snap := existing.pub()
		if isActiveStatus(snap.Status) {
			downloads.mu.Unlock()
			return snap, fmt.Errorf("a download for this scene is already running")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	state := &downloadState{
		cancel: cancel,
		snapshot: Download{
			ID:           key,
			Provider:     p.ID,
			ProviderName: p.Name,
			SceneDBID:    scene.ID,
			SceneID:      scene.SceneID,
			Title:        scene.Title,
			Quality:      quality,
			Codec:        codec,
			Status:       DownloadStatusQueued,
			StartedAt:    time.Now(),
		},
		lastTime: time.Now(),
	}
	downloads.downloads[key] = state
	downloads.mu.Unlock()

	publishDownload(state)

	// The allowance is about to change, so drop whatever was cached.
	invalidateProviderQuota(p)

	go runDownload(ctx, state, scene, quality, codec, dir, p)
	return state.pub(), nil
}

func runDownload(ctx context.Context, state *downloadState, scene models.Scene, quality, codec, dir string, p *downloadProvider) {
	// Whatever the outcome, the cached allowance is no longer trustworthy: a
	// successful download decrements it, and a failed one may still have.
	defer invalidateProviderQuota(p)

	defer func() {
		if r := recover(); r != nil {
			setDownloadFailure(state, fmt.Sprintf("unexpected error: %v", r))
		}
	}()

	// Wait for a download slot; stay "queued" until one is free.
	if err := slrSem.acquire(ctx); err != nil {
		state.mu.Lock()
		state.snapshot.Status = DownloadStatusCancelled
		state.snapshot.FinishedAt = time.Now()
		state.mu.Unlock()
		publishDownload(state)
		return
	}
	defer slrSem.release()

	state.mu.Lock()
	state.snapshot.Status = DownloadStatusResolving
	state.snapshot.StartedAt = time.Now()
	state.lastTime = time.Now()
	state.mu.Unlock()
	publishDownload(state)

	renditions, err := p.Renditions(scene)
	if err != nil {
		setDownloadFailure(state, err.Error())
		return
	}

	chosen, err := selectRendition(renditions, codec, quality)
	if err != nil {
		setDownloadFailure(state, err.Error())
		return
	}

	dest := filepath.Join(dir, p.Filename(scene, chosen.Quality, chosen.Codec))

	state.mu.Lock()
	state.snapshot.Status = DownloadStatusDownloading
	state.snapshot.Quality = chosen.Quality
	state.snapshot.Height = chosen.Height
	state.snapshot.Codec = chosen.Codec
	state.snapshot.Total = chosen.Size
	state.snapshot.Destination = dest
	state.lastTime = time.Now()
	state.mu.Unlock()
	publishDownload(state)

	log.WithFields(map[string]interface{}{
		"task":     "download",
		"provider": p.ID,
		"sceneID":  scene.SceneID,
		"quality":  chosen.Quality,
		"codec":    chosen.Codec,
	}).Infof("Downloading %s scene %s (%s %s)", p.Name, scene.Title, chosen.Quality, chosen.Codec)

	done, err := p.Download(ctx, chosen.URL, dest, func(done, total int64) {
		updateDownloadProgress(state, done, total)
	})
	if err != nil {
		if ctx.Err() != nil {
			state.mu.Lock()
			state.snapshot.Status = DownloadStatusCancelled
			state.snapshot.FinishedAt = time.Now()
			state.mu.Unlock()
			publishDownload(state)
			return
		}
		setDownloadFailure(state, err.Error())
		return
	}

	state.mu.Lock()
	state.snapshot.Status = DownloadStatusCompleted
	state.snapshot.Downloaded = done
	if state.snapshot.Total == 0 {
		state.snapshot.Total = done
	}
	state.snapshot.Progress = 100
	if state.snapshot.Total > 0 {
		state.snapshot.Progress = float64(done) * 100 / float64(state.snapshot.Total)
	}
	state.snapshot.FinishedAt = time.Now()
	state.snapshot.Speed = 0
	state.snapshot.ETA = 0
	state.mu.Unlock()
	publishDownload(state)

	log.WithFields(map[string]interface{}{
		"task":     "download",
		"provider": p.ID,
		"sceneID":  scene.SceneID,
	}).Infof("Finished downloading %s scene %s to %s", p.Name, scene.Title, dest)

	if p.Rescan() {
		log.WithField("task", "download").Infof("Rescanning volumes to link %s", filepath.Base(dest))
		RescanVolumes(-1)
	}
}

func updateDownloadProgress(state *downloadState, done, total int64) {
	state.mu.Lock()
	now := time.Now()
	elapsed := now.Sub(state.lastTime).Seconds()
	if elapsed > 0 {
		instant := int64(float64(done-state.lastBytes) / elapsed)
		if instant > 0 {
			state.snapshot.Speed = instant
		}
	}
	state.lastBytes = done
	state.lastTime = now

	state.snapshot.Downloaded = done
	if total > 0 {
		state.snapshot.Total = total
		state.snapshot.Progress = float64(done) * 100 / float64(total)
		if state.snapshot.Speed > 0 {
			state.snapshot.ETA = int64(float64(total-done) / float64(state.snapshot.Speed))
		}
	}
	state.mu.Unlock()
	publishDownload(state)
}

func setDownloadFailure(state *downloadState, message string) {
	state.mu.Lock()
	state.snapshot.Status = DownloadStatusFailed
	state.snapshot.Error = message
	state.snapshot.FinishedAt = time.Now()
	state.mu.Unlock()
	publishDownload(state)
	log.WithField("task", "download").Errorf("download failed: %s", message)
}

func publishDownload(state *downloadState) {
	state.mu.Lock()
	terminal := !isActiveStatus(state.snapshot.Status)
	if !terminal && !state.lastPublish.IsZero() && time.Since(state.lastPublish) < time.Second {
		state.mu.Unlock()
		return
	}
	state.lastPublish = time.Now()
	snap := state.snapshot
	state.mu.Unlock()

	_ = common.PublishWS(DownloadTopic, map[string]interface{}{
		"download": snap,
	})
}

// GetDownloads returns a snapshot of all tracked downloads.
func GetDownloads() []Download {
	downloads.mu.Lock()
	states := make([]*downloadState, 0, len(downloads.downloads))
	for _, s := range downloads.downloads {
		states = append(states, s)
	}
	downloads.mu.Unlock()

	out := make([]Download, 0, len(states))
	for _, s := range states {
		out = append(out, s.pub())
	}

	// The states come out of a map, and Go randomises map iteration order, so
	// without this the list would be in a different order on every poll and the
	// UI would visibly reshuffle. Newest first, with the id as a tie-break
	// because two downloads can start in the same nanosecond.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// CancelDownload stops an active download, keeping the partial file for resume.
func CancelDownload(id string) error {
	downloads.mu.Lock()
	state, ok := downloads.downloads[id]
	downloads.mu.Unlock()
	if !ok {
		return fmt.Errorf("download not found")
	}
	if !isActiveStatus(state.pub().Status) {
		return fmt.Errorf("download is not running")
	}
	state.cancel()
	return nil
}

// RemoveDownload forgets a finished download entry.
func RemoveDownload(id string) {
	downloads.mu.Lock()
	defer downloads.mu.Unlock()
	if state, ok := downloads.downloads[id]; ok {
		if isActiveStatus(state.pub().Status) {
			return
		}
		delete(downloads.downloads, id)
	}
}

// --------------------------------------------------------------------------- //
// Helpers
// --------------------------------------------------------------------------- //

func selectRendition(renditions []DownloadRendition, codec, quality string) (DownloadRendition, error) {
	if len(renditions) == 0 {
		return DownloadRendition{}, fmt.Errorf("no renditions available")
	}

	pool := make([]DownloadRendition, 0, len(renditions))
	for _, r := range renditions {
		if codec == "" || r.Codec == codec {
			pool = append(pool, r)
		}
	}
	if len(pool) == 0 {
		pool = renditions
	}

	if quality == "" {
		return highestRendition(pool), nil
	}
	for _, r := range pool {
		if strings.EqualFold(r.Quality, quality) {
			return r, nil
		}
	}
	if h := heightFromQuality(quality); h > 0 {
		for _, r := range pool {
			if r.Height == h {
				return r, nil
			}
		}
	}
	return highestRendition(pool), nil
}

func highestRendition(pool []DownloadRendition) DownloadRendition {
	best := pool[0]
	for _, r := range pool {
		if r.Height > best.Height {
			best = r
		}
	}
	return best
}

// heightFromQuality parses "2160p" / "2160" into a height.
func heightFromQuality(q string) int {
	h, err := strconv.Atoi(strings.TrimRight(strings.TrimSpace(q), "pP"))
	if err != nil {
		return 0
	}
	return h
}

func slrDownloadDir() string {
	dir := config.Config.SLRDownloader.DownloadDir
	if dir == "" {
		return ""
	}
	return filepath.Clean(os.ExpandEnv(dir))
}

// invalidateProviderQuota tells a provider that its cached allowance is stale.
func invalidateProviderQuota(p *downloadProvider) {
	if p != nil && p.InvalidateQuota != nil {
		p.InvalidateQuota()
	}
}

func isActiveStatus(status string) bool {
	switch status {
	case DownloadStatusResolving, DownloadStatusQueued, DownloadStatusDownloading:
		return true
	}
	return false
}
