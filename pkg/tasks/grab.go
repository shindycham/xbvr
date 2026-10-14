package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbapps/xbvr/pkg/common"
	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/dlclient"
	"github.com/xbapps/xbvr/pkg/indexer"
	"github.com/xbapps/xbvr/pkg/models"
	"github.com/xbapps/xbvr/pkg/release"

	// Download client and indexer implementations register themselves.
	_ "github.com/xbapps/xbvr/pkg/dlclient/nzbget"
	_ "github.com/xbapps/xbvr/pkg/dlclient/qbittorrent"
	_ "github.com/xbapps/xbvr/pkg/dlclient/sabnzbd"
	_ "github.com/xbapps/xbvr/pkg/dlclient/transmission"
	_ "github.com/xbapps/xbvr/pkg/indexer/torznab"
)

// Torrent and NZB grabbing
// ------------------------
//
// Unlike the SexLikeReal and VRSmash downloaders, which stream a signed URL
// straight to disk, a torrent or NZB is a *release*: it has to be found on an
// indexer, handed to a client that xbvr does not control, and then imported once
// that client finishes with it. The flow is therefore:
//
//	search   indexers return releases; each title is parsed with pkg/release and
//	         scored against the scene so the best candidate sorts first
//	grab     the release is handed to an external client tagged "xbvr-<ref>"
//	poll     the client is polled for progress, published to the UI like any
//	         other download
//	import   the finished file is hardlinked/copied/moved into the library and
//	         a rescan attaches it to the scene

// Grab statuses.
const (
	GrabStatusQueued      = "queued"
	GrabStatusDownloading = "downloading"
	GrabStatusImporting   = "importing"
	GrabStatusCompleted   = "completed"
	GrabStatusFailed      = "failed"
	GrabStatusCancelled   = "cancelled"
)

// Grab is a snapshot of one torrent or NZB download, safe to serialise.
type Grab struct {
	ID           string    `json:"id"`
	Ref          string    `json:"ref"`
	Provider     string    `json:"provider"`
	SceneDBID    uint      `json:"scene_db_id"`
	SceneID      string    `json:"scene_id"`
	Title        string    `json:"title"`
	ReleaseTitle string    `json:"release_title"`
	IndexerID    string    `json:"indexer_id"`
	IndexerName  string    `json:"indexer_name"`
	ClientID     string    `json:"client_id"`
	ClientName   string    `json:"client_name"`
	JobID        string    `json:"job_id"`
	Quality      string    `json:"quality"`
	Resolution   int       `json:"resolution"`
	Codec        string    `json:"codec"`
	Size         int64     `json:"size"`
	Status       string    `json:"status"`
	Downloaded   int64     `json:"downloaded"`
	Total        int64     `json:"total"`
	Progress     float64   `json:"progress"`
	Speed        int64     `json:"speed"`
	ETA          int64     `json:"eta"`
	Seeders      int       `json:"seeders"`
	Destination  string    `json:"destination"`
	Error        string    `json:"error"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}

// ReleaseCandidate is one indexer search result, parsed and scored against the
// scene it was searched for.
type ReleaseCandidate struct {
	Title       string          `json:"title"`
	IndexerID   string          `json:"indexer_id"`
	IndexerName string          `json:"indexer_name"`
	Protocol    string          `json:"protocol"`
	DownloadURL string          `json:"download_url"`
	MagnetURL   string          `json:"magnet_url"`
	Size        int64           `json:"size"`
	Seeders     int             `json:"seeders"`
	Leechers    int             `json:"leechers"`
	PublishedAt time.Time       `json:"published_at"`
	Release     release.Release `json:"release"`
	// Score is how strongly the release matches the scene, and Match reports
	// whether it cleared the matcher's threshold.
	Score   int      `json:"score"`
	Match   bool     `json:"match"`
	Reasons []string `json:"reasons"`
}

type grabState struct {
	mu       sync.Mutex
	snapshot Grab
	imported bool

	lastPublish time.Time
}

func (s *grabState) pub() Grab {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

type grabManager struct {
	mu         sync.Mutex
	grabs      map[string]*grabState
	pollerOnce sync.Once
}

var grabStore = &grabManager{grabs: map[string]*grabState{}}

// Test seams so the orchestrator can be tested without real indexers or clients.
var (
	newIndexerClient  = indexer.New
	newDownloadClient = dlclient.New
)

// --------------------------------------------------------------------------- //
// Search
// --------------------------------------------------------------------------- //

// SearchSceneReleases queries every enabled indexer for releases of a scene and
// returns them parsed and scored, best first.
//
// query overrides the generated search string; when empty a string is built from
// the scene's studio and cast.
func SearchSceneReleases(scene models.Scene, protocol, query string) ([]ReleaseCandidate, error) {
	if !config.Config.TorrentNZB.Enabled {
		return nil, errors.New("torrent and NZB downloads are disabled")
	}
	indexers := enabledIndexers(protocol)
	if len(indexers) == 0 {
		return nil, errors.New("no indexers are enabled; add one in Options -> Downloads")
	}

	// Reload the scene with its cast, which the generated queries and the
	// matcher both need. A failure here is not fatal: the caller's copy is
	// still usable, just without cast.
	if loaded, err := loadSceneForGrab(scene.ID); err == nil {
		scene = loaded
	}
	sceneCandidate := sceneCandidate(scene)
	opts := releaseMatchOptions()

	type hit struct {
		cfg  config.IndexerConfig
		item indexer.Release
		rel  release.Release
	}
	var hits []hit
	seen := map[string]bool{}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	queries := buildGrabQueries(scene, query)
	var searchErrors []string

	for _, cfg := range indexers {
		client, err := newIndexerClient(indexerConfigFrom(cfg))
		if err != nil {
			searchErrors = append(searchErrors, fmt.Sprintf("%s: %s", indexerDisplayName(cfg), err))
			continue
		}
		for _, q := range queries {
			results, err := client.Search(ctx, indexer.Query{
				Q:          q,
				Categories: cfg.Categories,
				Limit:      100,
			})
			if err != nil {
				searchErrors = append(searchErrors, fmt.Sprintf("%s: %s", indexerDisplayName(cfg), err))
				continue
			}
			for _, item := range results {
				key := strings.ToLower(strings.TrimSpace(item.Title))
				if key == "" || seen[key] {
					continue
				}
				seen[key] = true
				hits = append(hits, hit{cfg: cfg, item: item, rel: release.ParseName(item.Title)})
			}
		}
	}

	minSeeders := config.Config.TorrentNZB.MinSeeders
	preferred := config.Config.TorrentNZB.PreferredResolution

	out := make([]ReleaseCandidate, 0, len(hits))
	for _, h := range hits {
		protocolName := h.item.Protocol
		if protocolName == "" {
			protocolName = indexerProtocol(h.cfg)
		}
		// Hide barely-seeded torrents; an indexer that does not report seeders
		// reports -1 and is left alone.
		if protocolName == dlclient.KindTorrent && minSeeders > 0 && h.item.Seeders >= 0 && h.item.Seeders < minSeeders {
			continue
		}
		result := release.Match(h.rel, []release.SceneCandidate{sceneCandidate}, opts)
		out = append(out, ReleaseCandidate{
			Title:       h.item.Title,
			IndexerID:   h.cfg.ID,
			IndexerName: indexerDisplayName(h.cfg),
			Protocol:    protocolName,
			DownloadURL: h.item.DownloadURL,
			MagnetURL:   h.item.MagnetURL,
			Size:        h.item.Size,
			Seeders:     h.item.Seeders,
			Leechers:    h.item.Leechers,
			PublishedAt: h.item.PublishedAt,
			Release:     h.rel,
			Score:       result.Best.Total,
			Match:       result.Matched(),
			Reasons:     result.Best.Reasons,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Match != b.Match {
			return a.Match
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if preferred > 0 {
			ap, bp := a.Release.Resolution == preferred, b.Release.Resolution == preferred
			if ap != bp {
				return ap
			}
		}
		if a.Release.Resolution != b.Release.Resolution {
			return a.Release.Resolution > b.Release.Resolution
		}
		if a.Seeders != b.Seeders {
			return a.Seeders > b.Seeders
		}
		return a.Size > b.Size
	})

	// Only report an error when nothing at all came back, so one broken indexer
	// does not hide results from the others.
	if len(out) == 0 && len(searchErrors) > 0 {
		return nil, errors.New(strings.Join(searchErrors, "; "))
	}
	return out, nil
}

// GrabSearchQueries reports the indexer search strings a scene would produce,
// so the UI can show what was actually searched.
func GrabSearchQueries(scene models.Scene, query string) []string {
	if loaded, err := loadSceneForGrab(scene.ID); err == nil {
		scene = loaded
	}
	return buildGrabQueries(scene, query)
}

// buildGrabQueries turns a scene into indexer search strings.
func buildGrabQueries(scene models.Scene, custom string) []string {
	if strings.TrimSpace(custom) != "" {
		return []string{strings.TrimSpace(custom)}
	}

	studio := scene.Studio
	if studio == "" {
		studio = scene.Site
	}

	cast := make([]string, 0, len(scene.Cast))
	for _, actor := range scene.Cast {
		if actor.Name != "" {
			cast = append(cast, actor.Name)
		}
	}

	var queries []string
	add := func(parts ...string) {
		joined := strings.TrimSpace(strings.Join(parts, " "))
		if joined == "" {
			return
		}
		for _, q := range queries {
			if q == joined {
				return
			}
		}
		queries = append(queries, joined)
	}

	if studio != "" && len(cast) > 0 {
		add(studio, strings.Join(cast, " "))
	}
	if len(cast) > 0 {
		add(strings.Join(cast, " "))
	}
	if studio != "" {
		add(studio)
	}
	if scene.Title != "" {
		// Titles are long; the first few words are the distinguishing part and
		// keep the query from becoming an AND of every word.
		words := strings.Fields(scene.Title)
		if len(words) > 5 {
			words = words[:5]
		}
		add(studio, strings.Join(words, " "))
	}
	return queries
}

// --------------------------------------------------------------------------- //
// Grab
// --------------------------------------------------------------------------- //

// StartSceneGrab hands a chosen release to the download client configured for
// its protocol.
func StartSceneGrab(scene models.Scene, candidate ReleaseCandidate) (Grab, error) {
	if !config.Config.TorrentNZB.Enabled {
		return Grab{}, errors.New("torrent and NZB downloads are disabled")
	}
	if candidate.MagnetURL == "" && candidate.DownloadURL == "" {
		return Grab{}, errors.New("release has no magnet link or download URL")
	}

	protocol := candidate.Protocol
	if protocol == "" {
		protocol = dlclient.KindTorrent
	}
	// Refuse a second concurrent grab for the same scene, the way the direct
	// downloaders do, so a double click cannot queue the release twice.
	for _, existing := range GetGrabs() {
		if existing.SceneDBID == scene.ID && existing.Provider == protocol && !grabIsTerminal(existing.Status) {
			return Grab{}, errors.New("a download for this scene is already running")
		}
	}

	clientCfg, err := pickDownloadClient(protocol)
	if err != nil {
		return Grab{}, err
	}
	client, err := newDownloadClient(dlclientConfigFrom(clientCfg))
	if err != nil {
		return Grab{}, err
	}

	// The ref is both the client-side tag and the key that lets a restarted
	// xbvr re-adopt this job: the scene id is embedded so an orphaned job can be
	// traced back to its scene without any persisted state.
	ref := grabRef(scene.ID)
	key := fmt.Sprintf("%s:%d:%s", protocol, scene.ID, ref)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	job, err := client.Add(ctx, dlclient.AddRequest{
		Ref:      ref,
		Name:     candidate.Title,
		Magnet:   candidate.MagnetURL,
		URL:      candidate.DownloadURL,
		SavePath: clientCfg.SavePath,
		Category: clientCfg.Category,
	})
	if err != nil {
		return Grab{}, fmt.Errorf("%s: %w", downloadClientDisplayName(clientCfg), err)
	}

	state := &grabState{
		snapshot: Grab{
			ID:           key,
			Ref:          ref,
			Provider:     protocol,
			SceneDBID:    scene.ID,
			SceneID:      scene.SceneID,
			Title:        scene.Title,
			ReleaseTitle: candidate.Title,
			IndexerID:    candidate.IndexerID,
			IndexerName:  candidate.IndexerName,
			ClientID:     clientCfg.ID,
			ClientName:   downloadClientDisplayName(clientCfg),
			JobID:        job.ID,
			Quality:      candidate.Release.Quality,
			Resolution:   candidate.Release.Resolution,
			Codec:        candidate.Release.Codec,
			Size:         candidate.Size,
			Seeders:      candidate.Seeders,
			Status:       GrabStatusQueued,
			StartedAt:    time.Now(),
		},
	}

	grabStore.mu.Lock()
	grabStore.grabs[key] = state
	grabStore.mu.Unlock()

	if job.State == dlclient.StateDownloading {
		state.mu.Lock()
		state.snapshot.Status = GrabStatusDownloading
		state.mu.Unlock()
	}

	publishGrab(state)
	StartGrabPoller()

	log.WithFields(map[string]interface{}{
		"task":     "grab",
		"provider": protocol,
		"sceneID":  scene.SceneID,
		"release":  candidate.Title,
		"client":   clientCfg.Client,
	}).Infof("Grabbed %s for scene %s", candidate.Title, scene.Title)

	return state.pub(), nil
}

// GetGrabs returns a snapshot of every tracked grab.
func GetGrabs() []Grab {
	grabStore.mu.Lock()
	states := make([]*grabState, 0, len(grabStore.grabs))
	for _, s := range grabStore.grabs {
		states = append(states, s)
	}
	grabStore.mu.Unlock()

	out := make([]Grab, 0, len(states))
	for _, s := range states {
		out = append(out, s.pub())
	}
	// Same reasoning as GetDownloads: the order must be stable across polls, and
	// SliceStable alone is not enough when two grabs share a start time.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// CancelGrab stops tracking a grab and removes it from its client.
func CancelGrab(id string) error {
	grabStore.mu.Lock()
	state, ok := grabStore.grabs[id]
	grabStore.mu.Unlock()
	if !ok {
		return errors.New("grab not found")
	}
	if grabIsTerminal(state.pub().Status) {
		return errors.New("grab is not running")
	}

	snapshot := state.pub()
	if snapshot.ClientID != "" {
		if cfg, ok := downloadClientByID(snapshot.ClientID); ok {
			if client, err := newDownloadClient(dlclientConfigFrom(cfg)); err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				// Delete the partial data too: a cancelled grab is not wanted.
				err = client.Remove(ctx, snapshot.JobID, true)
				cancel()
				if err != nil && !errors.Is(err, dlclient.ErrNotFound) {
					log.WithField("task", "grab").Warnf("Could not remove %s from the client: %s", snapshot.ReleaseTitle, err)
				}
			}
		}
	}

	state.mu.Lock()
	state.snapshot.Status = GrabStatusCancelled
	state.snapshot.FinishedAt = time.Now()
	state.mu.Unlock()
	publishGrab(state)
	return nil
}

// RemoveGrab forgets a finished grab.
func RemoveGrab(id string) {
	grabStore.mu.Lock()
	defer grabStore.mu.Unlock()
	if state, ok := grabStore.grabs[id]; ok {
		if !grabIsTerminal(state.pub().Status) {
			return
		}
		delete(grabStore.grabs, id)
	}
}

// StartGrabPoller begins the background loop that refreshes active grabs from
// their clients. It is safe to call repeatedly.
func StartGrabPoller() {
	grabStore.pollerOnce.Do(func() {
		go func() {
			for {
				interval := config.Config.TorrentNZB.PollSeconds
				if interval < 5 {
					interval = 15
				}
				time.Sleep(time.Duration(interval) * time.Second)
				PollGrabs()
			}
		}()
	})
}

// PollGrabs refreshes every active grab once. Exported so it can be driven
// directly by tests and by the API.
func PollGrabs() {
	active := make([]*grabState, 0)
	grabStore.mu.Lock()
	for _, s := range grabStore.grabs {
		active = append(active, s)
	}
	grabStore.mu.Unlock()

	byClient := map[string][]*grabState{}
	for _, s := range active {
		snapshot := s.pub()
		if grabIsTerminal(snapshot.Status) || snapshot.ClientID == "" {
			continue
		}
		byClient[snapshot.ClientID] = append(byClient[snapshot.ClientID], s)
	}
	if len(byClient) == 0 {
		return
	}

	for clientID, states := range byClient {
		cfg, ok := downloadClientByID(clientID)
		if !ok {
			for _, s := range states {
				setGrabFailure(s, "the download client is no longer configured")
			}
			continue
		}
		client, err := newDownloadClient(dlclientConfigFrom(cfg))
		if err != nil {
			for _, s := range states {
				setGrabFailure(s, err.Error())
			}
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		jobs, err := client.List(ctx)
		cancel()
		if err != nil {
			log.WithField("task", "grab").Warnf("Could not list %s: %s", downloadClientDisplayName(cfg), err)
			continue
		}

		byRef := map[string]dlclient.Job{}
		byName := map[string]dlclient.Job{}
		for _, job := range jobs {
			if job.Ref != "" {
				byRef[job.Ref] = job
			}
			byName[strings.ToLower(job.Name)] = job
		}

		for _, state := range states {
			snapshot := state.pub()
			job, found := byRef[snapshot.Ref]
			if !found {
				// Transmission 3 has no labels, so name matching is the only
				// fallback there.
				job, found = byName[strings.ToLower(snapshot.ReleaseTitle)]
			}
			if !found {
				continue
			}
			applyJobToGrab(state, job)
			publishGrab(state)

			switch job.State {
			case dlclient.StateFailed:
				setGrabFailure(state, nonEmpty(job.Error, "the download client reported a failure"))
			case dlclient.StateCompleted:
				importGrab(state, client, job)
			}
		}
	}
}

// applyJobToGrab copies progress from a client job onto a grab snapshot. The
// caller publishes afterwards.
func applyJobToGrab(state *grabState, job dlclient.Job) {
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.snapshot.Status == GrabStatusCompleted || state.snapshot.Status == GrabStatusImporting {
		return
	}
	if job.ID != "" {
		state.snapshot.JobID = job.ID
	}
	state.snapshot.Progress = job.Progress
	state.snapshot.Downloaded = job.Downloaded
	state.snapshot.Total = job.Total
	state.snapshot.Speed = job.Speed
	state.snapshot.ETA = job.ETA
	if job.State == dlclient.StateDownloading || job.State == dlclient.StateStalled {
		state.snapshot.Status = GrabStatusDownloading
	}
}

// importGrab places a finished download into the library and rescans.
func importGrab(state *grabState, client dlclient.Client, job dlclient.Job) {
	state.mu.Lock()
	if state.imported {
		state.mu.Unlock()
		return
	}
	state.imported = true
	state.snapshot.Status = GrabStatusImporting
	snapshot := state.snapshot
	state.mu.Unlock()
	publishGrab(state)

	source, err := locateCompletedContent(job)
	if err != nil {
		state.mu.Lock()
		state.imported = false
		state.mu.Unlock()
		setGrabFailure(state, err.Error())
		return
	}

	destination, err := importReleaseFile(source, config.Config.TorrentNZB.ImportDir)
	if err != nil {
		state.mu.Lock()
		state.imported = false
		state.mu.Unlock()
		setGrabFailure(state, err.Error())
		return
	}

	state.mu.Lock()
	state.snapshot.Status = GrabStatusCompleted
	state.snapshot.Destination = destination
	state.snapshot.Progress = 100
	state.snapshot.FinishedAt = time.Now()
	state.snapshot.Speed = 0
	state.snapshot.ETA = 0
	state.mu.Unlock()
	publishGrab(state)

	log.WithFields(map[string]interface{}{
		"task": "grab",
		"file": filepath.Base(destination),
	}).Infof("Imported %s into the library", filepath.Base(destination))

	if config.Config.TorrentNZB.RemoveAfterImport && snapshot.JobID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := client.Remove(ctx, snapshot.JobID, true); err != nil && !errors.Is(err, dlclient.ErrNotFound) {
			log.WithField("task", "grab").Warnf("Could not remove %s from the client: %s", snapshot.ReleaseTitle, err)
		}
		cancel()
	}

	rescanAfterImport()
}

// rescanAfterImport is a seam: tests replace it so importing a finished grab can
// be exercised without a library database.
var rescanAfterImport = func() { RescanVolumes(-1) }

// grabRefPrefix marks the client-side tags xbvr owns.
const grabRefPrefix = "xbvr-"

// grabRef builds the reference handed to a download client for a scene.
//
// The nanosecond timestamp keeps refs distinct across restarts; the counter
// keeps them distinct within a process, because the clock is not fine-grained
// enough to guarantee two calls never return the same value.
func grabRef(sceneID uint) string {
	seq := atomic.AddUint64(&grabRefSeq, 1)
	return fmt.Sprintf("%s%d-%d-%d", grabRefPrefix, sceneID, time.Now().UnixNano(), seq)
}

var grabRefSeq uint64

// sceneIDFromRef recovers the scene id embedded in a ref, reporting false for a
// tag xbvr did not create.
func sceneIDFromRef(ref string) (uint, bool) {
	if !strings.HasPrefix(ref, grabRefPrefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(ref, grabRefPrefix)
	dash := strings.Index(rest, "-")
	if dash <= 0 {
		return 0, false
	}
	id, err := strconv.ParseUint(rest[:dash], 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(id), true
}

// AdoptOrphanedGrabs re-attaches to downloads that were already running in a
// configured client when xbvr started.
//
// Grabs live in memory, so a restart would otherwise orphan a torrent that is
// still downloading: the client would finish it and nothing would ever import
// the file. Every job the client tagged with an xbvr ref is picked back up.
//
// It runs once at startup rather than on every poll, so removing a finished grab
// from the Downloads page does not bring it back.
func AdoptOrphanedGrabs() {
	if !config.Config.TorrentNZB.Enabled {
		return
	}

	for _, cfg := range config.Config.DownloadClients {
		if !cfg.Enabled || strings.TrimSpace(cfg.URL) == "" {
			continue
		}
		client, err := newDownloadClient(dlclientConfigFrom(cfg))
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		jobs, err := client.List(ctx)
		cancel()
		if err != nil {
			log.WithField("task", "grab").Warnf("Could not check %s for unfinished downloads: %s", downloadClientDisplayName(cfg), err)
			continue
		}

		adopted := 0
		for _, job := range jobs {
			if job.Ref == "" || job.IsTerminal() {
				continue
			}
			sceneID, ok := sceneIDFromRef(job.Ref)
			if !ok {
				continue
			}
			if grabTracked(job.Ref) {
				continue
			}

			key := fmt.Sprintf("%s:%d:%s", cfg.Kind, sceneID, job.Ref)
			state := &grabState{
				snapshot: Grab{
					ID:           key,
					Ref:          job.Ref,
					Provider:     cfg.Kind,
					SceneDBID:    sceneID,
					SceneID:      lookupSceneForGrab(sceneID),
					ReleaseTitle: job.Name,
					Title:        job.Name,
					ClientID:     cfg.ID,
					ClientName:   downloadClientDisplayName(cfg),
					JobID:        job.ID,
					Status:       GrabStatusDownloading,
					StartedAt:    time.Now(),
				},
			}
			grabStore.mu.Lock()
			grabStore.grabs[key] = state
			grabStore.mu.Unlock()
			publishGrab(state)
			adopted++
		}

		if adopted > 0 {
			log.WithField("task", "grab").Infof("Re-adopted %d unfinished download(s) from %s", adopted, downloadClientDisplayName(cfg))
		}
	}
}

func grabTracked(ref string) bool {
	grabStore.mu.Lock()
	defer grabStore.mu.Unlock()
	for _, state := range grabStore.grabs {
		if state.pub().Ref == ref {
			return true
		}
	}
	return false
}

// lookupSceneForGrab is a seam: tests replace it so adoption never reads the
// real library database.
var lookupSceneForGrab = lookupSceneID

// lookupSceneID returns a scene's external id, or an empty string when the scene
// is unknown.
func lookupSceneID(id uint) string {
	db, err := models.GetDB()
	if err != nil {
		return ""
	}
	defer db.Close()

	var scene models.Scene
	if err := db.Select("scene_id").First(&scene, id).Error; err != nil {
		return ""
	}
	return scene.SceneID
}

// --------------------------------------------------------------------------- //
// Importing
// --------------------------------------------------------------------------- //

// locateCompletedContent finds the video file a finished job produced.
func locateCompletedContent(job dlclient.Job) (string, error) {
	if job.ContentPath != "" {
		if info, err := os.Stat(job.ContentPath); err == nil {
			if !info.IsDir() {
				return job.ContentPath, nil
			}
			if file, err := largestVideoFile(job.ContentPath); err == nil {
				return file, nil
			}
		}
	}
	if job.SavePath == "" {
		return "", errors.New("the download client did not report where the download landed")
	}
	file, err := largestVideoFile(job.SavePath)
	if err != nil {
		return "", fmt.Errorf("no video file found in %s", job.SavePath)
	}
	return file, nil
}

// largestVideoFile returns the biggest video file under root.
//
// A multi-file torrent carries the scene alongside samples, subtitles and
// artwork, and the scene is overwhelmingly the largest file.
func largestVideoFile(root string) (string, error) {
	allowed := map[string]bool{}
	for _, ext := range getAllowedVideoExt() {
		allowed[ext] = true
	}

	var best string
	var bestSize int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		if !allowed[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if info.Size() > bestSize {
			best, bestSize = path, info.Size()
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if best == "" {
		return "", errors.New("no video file found")
	}
	return best, nil
}

// importReleaseFile places a finished download in the library directory using
// the configured mode, returning the resulting path.
func importReleaseFile(source, importDir string) (string, error) {
	if importDir == "" {
		return "", errors.New("no import folder is configured; set one in Options -> Downloads")
	}
	if _, err := os.Stat(source); err != nil {
		return "", fmt.Errorf("the downloaded file is no longer there: %w", err)
	}

	if err := os.MkdirAll(importDir, 0o755); err != nil {
		return "", err
	}
	destination := filepath.Join(importDir, filepath.Base(source))

	// Already in place, which happens when the client downloads straight into a
	// scanned folder.
	if absSource, err1 := filepath.Abs(source); err1 == nil {
		if absDestination, err2 := filepath.Abs(destination); err2 == nil && absSource == absDestination {
			return destination, nil
		}
	}
	if _, err := os.Stat(destination); err == nil {
		return destination, nil
	}

	mode := strings.ToLower(config.Config.TorrentNZB.ImportMode)
	switch mode {
	case "", "hardlink":
		if err := os.Link(source, destination); err == nil {
			return destination, nil
		}
		// Hardlinks cannot cross filesystems, which is the normal case when the
		// client downloads to a different disk. Fall back to copying so the
		// original keeps seeding.
		if err := copyFile(source, destination); err != nil {
			return "", err
		}
		return destination, nil
	case "copy":
		if err := copyFile(source, destination); err != nil {
			return "", err
		}
		return destination, nil
	case "move":
		if err := os.Rename(source, destination); err == nil {
			return destination, nil
		}
		if err := copyFile(source, destination); err != nil {
			return "", err
		}
		if err := os.Remove(source); err != nil {
			log.WithField("task", "grab").Warnf("Imported %s but could not remove the original: %s", filepath.Base(destination), err)
		}
		return destination, nil
	default:
		return "", fmt.Errorf("unknown import mode %q", config.Config.TorrentNZB.ImportMode)
	}
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(destination)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(destination)
		return err
	}
	// Match the source permissions so a downloaded file stays as private as it
	// was.
	if info, err := os.Stat(source); err == nil {
		_ = os.Chmod(destination, info.Mode().Perm())
	}
	return nil
}

// --------------------------------------------------------------------------- //
// Helpers
// --------------------------------------------------------------------------- //

// publishGrab publishes the current snapshot over WAMP.
//
// Progress is throttled to one message a second so a fast download does not
// flood the websocket, but a terminal state always goes out immediately.
// Publishing happens outside the lock and synchronously, so the UI sees states
// in the order they happened.
func publishGrab(state *grabState) {
	state.mu.Lock()
	terminal := grabIsTerminal(state.snapshot.Status)
	if !terminal && !state.lastPublish.IsZero() && time.Since(state.lastPublish) < time.Second {
		state.mu.Unlock()
		return
	}
	state.lastPublish = time.Now()
	snapshot := state.snapshot
	state.mu.Unlock()

	_ = common.PublishWS(DownloadTopic, map[string]interface{}{"grab": snapshot})
}

func setGrabFailure(state *grabState, message string) {
	state.mu.Lock()
	if state.snapshot.Status == GrabStatusCompleted {
		state.mu.Unlock()
		return
	}
	state.snapshot.Status = GrabStatusFailed
	state.snapshot.Error = message
	state.snapshot.FinishedAt = time.Now()
	state.mu.Unlock()
	publishGrab(state)
	log.WithField("task", "grab").Errorf("grab failed: %s", message)
}

func grabIsTerminal(status string) bool {
	switch status {
	case GrabStatusCompleted, GrabStatusFailed, GrabStatusCancelled:
		return true
	}
	return false
}

// loadSceneForGrab is a seam: tests replace it so a search never reaches the
// real library database.
var loadSceneForGrab = loadSceneWithCast

// loadSceneWithCast reloads a scene with its cast, which the search queries and
// the matcher both need.
func loadSceneWithCast(id uint) (models.Scene, error) {
	db, err := models.GetDB()
	if err != nil {
		return models.Scene{}, err
	}
	defer db.Close()

	var scene models.Scene
	if err := db.Preload("Cast").First(&scene, id).Error; err != nil {
		return models.Scene{}, err
	}
	return scene, nil
}

func enabledIndexers(protocol string) []config.IndexerConfig {
	out := make([]config.IndexerConfig, 0, len(config.Config.Indexers))
	for _, cfg := range config.Config.Indexers {
		if !cfg.Enabled || strings.TrimSpace(cfg.URL) == "" {
			continue
		}
		if protocol != "" && indexerProtocol(cfg) != protocol {
			continue
		}
		out = append(out, cfg)
	}
	return out
}

func indexerProtocol(cfg config.IndexerConfig) string {
	if strings.EqualFold(cfg.Type, indexer.ProtocolNewznab) {
		return dlclient.KindNZB
	}
	return dlclient.KindTorrent
}

func indexerDisplayName(cfg config.IndexerConfig) string {
	if strings.TrimSpace(cfg.Name) != "" {
		return cfg.Name
	}
	return cfg.URL
}

func indexerConfigFrom(cfg config.IndexerConfig) indexer.Config {
	return indexer.Config{
		ID:         cfg.ID,
		Name:       cfg.Name,
		Type:       cfg.Type,
		URL:        cfg.URL,
		APIKey:     cfg.APIKey,
		Categories: cfg.Categories,
		Enabled:    cfg.Enabled,
	}
}

func downloadClientByID(id string) (config.DownloadClientConfig, bool) {
	for _, cfg := range config.Config.DownloadClients {
		if cfg.ID == id {
			return cfg, true
		}
	}
	return config.DownloadClientConfig{}, false
}

func pickDownloadClient(kind string) (config.DownloadClientConfig, error) {
	for _, cfg := range config.Config.DownloadClients {
		if !cfg.Enabled || strings.TrimSpace(cfg.URL) == "" {
			continue
		}
		if kind == "" || cfg.Kind == kind {
			return cfg, nil
		}
	}
	return config.DownloadClientConfig{}, fmt.Errorf("no %s download client is enabled; add one in Options -> Downloads", kind)
}

func downloadClientDisplayName(cfg config.DownloadClientConfig) string {
	if strings.TrimSpace(cfg.Name) != "" {
		return cfg.Name
	}
	return cfg.Client
}

func dlclientConfigFrom(cfg config.DownloadClientConfig) dlclient.Config {
	return dlclient.Config{
		ID:       cfg.ID,
		Name:     cfg.Name,
		Client:   cfg.Client,
		Kind:     cfg.Kind,
		URL:      cfg.URL,
		Username: cfg.Username,
		Password: cfg.Password,
		APIKey:   cfg.APIKey,
		Category: cfg.Category,
		SavePath: cfg.SavePath,
		Enabled:  cfg.Enabled,
	}
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// TestIndexerConfig verifies an indexer is reachable and its key is accepted.
func TestIndexerConfig(cfg config.IndexerConfig) error {
	if strings.TrimSpace(cfg.URL) == "" {
		return errors.New("indexer URL is required")
	}
	client, err := newIndexerClient(indexerConfigFrom(cfg))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.Test(ctx)
}

// TestDownloadClientConfig verifies a download client is reachable and its
// credentials are accepted.
func TestDownloadClientConfig(cfg config.DownloadClientConfig) error {
	if strings.TrimSpace(cfg.URL) == "" {
		return errors.New("client URL is required")
	}
	client, err := newDownloadClient(dlclientConfigFrom(cfg))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.Test(ctx)
}

// SupportedIndexerTypes lists the indexer protocols this build speaks.
func SupportedIndexerTypes() []string {
	return indexer.SupportedProtocols()
}

// SupportedDownloadClients lists the download clients this build speaks.
func SupportedDownloadClients() []string {
	return dlclient.SupportedClients()
}
