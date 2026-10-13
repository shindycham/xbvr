package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/dlclient"
	"github.com/xbapps/xbvr/pkg/indexer"
	"github.com/xbapps/xbvr/pkg/models"
	"github.com/xbapps/xbvr/pkg/release"
)

// --------------------------------------------------------------------------- //
// Stubs
// --------------------------------------------------------------------------- //

type stubIndexer struct {
	cfg      indexer.Config
	releases []indexer.Release
	queries  []string
	err      error
}

func (s *stubIndexer) ID() string                                       { return s.cfg.ID }
func (s *stubIndexer) Name() string                                     { return s.cfg.Name }
func (s *stubIndexer) Type() string                                     { return s.cfg.Type }
func (s *stubIndexer) Test(context.Context) error                       { return s.err }
func (s *stubIndexer) Caps(context.Context) ([]indexer.Category, error) { return nil, s.err }

func (s *stubIndexer) Search(_ context.Context, q indexer.Query) ([]indexer.Release, error) {
	s.queries = append(s.queries, q.Q)
	if s.err != nil {
		return nil, s.err
	}
	return s.releases, nil
}

type stubClient struct {
	cfg   dlclient.Config
	jobs  []dlclient.Job
	added []dlclient.AddRequest

	removed     []string
	removeError error
	listError   error
	addError    error
	addReturns  dlclient.Job
}

func (c *stubClient) ID() string                 { return c.cfg.ID }
func (c *stubClient) Name() string               { return c.cfg.Name }
func (c *stubClient) Kind() string               { return c.cfg.Kind }
func (c *stubClient) Test(context.Context) error { return c.listError }

func (c *stubClient) List(context.Context) ([]dlclient.Job, error) { return c.jobs, c.listError }

func (c *stubClient) Add(_ context.Context, req dlclient.AddRequest) (dlclient.Job, error) {
	c.added = append(c.added, req)
	if c.addError != nil {
		return dlclient.Job{}, c.addError
	}
	return c.addReturns, nil
}

func (c *stubClient) Remove(_ context.Context, id string, _ bool) error {
	c.removed = append(c.removed, id)
	return c.removeError
}

// --------------------------------------------------------------------------- //
// Fixtures
// --------------------------------------------------------------------------- //

// withGrabConfig snapshots the global configuration *and* the grab store,
// applies mutate, and restores everything when the test ends.
//
// Isolating the grab store matters: a grab left behind by an earlier test would
// otherwise be polled by this test's stub client and imported a second time.
func withGrabConfig(t *testing.T, mutate func()) {
	t.Helper()

	savedConfig := config.Config
	savedLoader := loadSceneForGrab
	savedSceneIDLookup := lookupSceneForGrab

	grabStore.mu.Lock()
	savedGrabs := grabStore.grabs
	grabStore.grabs = map[string]*grabState{}
	grabStore.mu.Unlock()

	t.Cleanup(func() {
		config.Config = savedConfig
		loadSceneForGrab = savedLoader
		lookupSceneForGrab = savedSceneIDLookup
		grabStore.mu.Lock()
		grabStore.grabs = savedGrabs
		grabStore.mu.Unlock()
	})

	// Force SearchSceneReleases to use the scene it is handed rather than
	// looking one up in the real library database.
	loadSceneForGrab = func(uint) (models.Scene, error) {
		return models.Scene{}, errors.New("no database in tests")
	}
	lookupSceneForGrab = func(uint) string { return "" }

	mutate()
}

// grabConfig enables torrent/NZB grabbing with a working indexer and client.
// PollSeconds is set high so the background poller never interferes.
func grabConfig(extra ...func()) func() {
	return func() {
		config.Config.TorrentNZB.Enabled = true
		config.Config.TorrentNZB.ImportMode = "hardlink"
		config.Config.TorrentNZB.PollSeconds = 3600
		config.Config.TorrentNZB.MinSeeders = 0
		config.Config.Indexers = []config.IndexerConfig{
			{ID: "idx-1", Name: "Prowlarr", Type: "torznab", URL: "http://indexer/api", Enabled: true},
		}
		config.Config.DownloadClients = []config.DownloadClientConfig{
			{
				ID: "qb-1", Name: "qBittorrent", Client: "qbittorrent", Kind: "torrent",
				URL: "http://127.0.0.1:8080", Category: "xbvr", SavePath: "/downloads", Enabled: true,
			},
		}
		for _, e := range extra {
			e()
		}
	}
}

func grabTestScene() models.Scene {
	return models.Scene{
		ID:          42,
		SceneID:     "vrbangers-123",
		Title:       "Midnight Swim",
		Site:        "VRBangers",
		Studio:      "VRBangers",
		ScraperId:   "vrbangers",
		ReleaseDate: time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC),
		Cast:        []models.Actor{{Name: "Jane Doe"}},
	}
}

const testReleaseTitle = "VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB"

func indexerRelease(title string) indexer.Release {
	return indexer.Release{
		Indexer:     "Prowlarr",
		IndexerID:   "idx-1",
		Title:       title,
		DownloadURL: "https://indexer/download/abc.torrent",
		Size:        3930769406,
		Seeders:     12,
		Leechers:    3,
		Protocol:    "torrent",
	}
}

func useIndexer(t *testing.T, stub *stubIndexer) {
	t.Helper()
	newIndexerClient = func(indexer.Config) (indexer.Client, error) { return stub, nil }
	t.Cleanup(func() { newIndexerClient = indexer.New })
}

func useClient(t *testing.T, stub *stubClient) {
	t.Helper()
	newDownloadClient = func(dlclient.Config) (dlclient.Client, error) { return stub, nil }
	t.Cleanup(func() { newDownloadClient = dlclient.New })
}

// noRescan replaces the rescan seam and reports how often it ran.
func noRescan(t *testing.T) *int {
	t.Helper()
	calls := new(int)
	original := rescanAfterImport
	rescanAfterImport = func() { *calls++ }
	t.Cleanup(func() { rescanAfterImport = original })
	return calls
}

// --------------------------------------------------------------------------- //
// Search
// --------------------------------------------------------------------------- //

func TestSearchSceneReleasesScoresAndSorts(t *testing.T) {
	withGrabConfig(t, grabConfig())

	stub := &stubIndexer{
		cfg: indexer.Config{ID: "idx-1", Name: "Prowlarr"},
		releases: []indexer.Release{
			indexerRelease("SomeOtherSite.23.10.14.Nobody.XXX.2160p-GRP"),
			indexerRelease(testReleaseTitle),
			indexerRelease("VRBangers.23.12.01.Jane.Doe.XXX.2160p.MP4-WRB"),
		},
	}
	useIndexer(t, stub)

	candidates, err := SearchSceneReleases(grabTestScene(), "", "")
	if err != nil {
		t.Fatalf("SearchSceneReleases: %s", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3", len(candidates))
	}

	// The scene's own release ranks first and is flagged as a match.
	best := candidates[0]
	if best.Title != testReleaseTitle {
		t.Errorf("best candidate = %q, want the matching release", best.Title)
	}
	if !best.Match {
		t.Errorf("matching release not flagged: score %d reasons %v", best.Score, best.Reasons)
	}
	// "MP4" is a container, not a codec, so no codec is expected here.
	if best.Release.Resolution != 2160 || best.Release.Group != "WRB" {
		t.Errorf("parsed release = res %d codec %q group %q", best.Release.Resolution, best.Release.Codec, best.Release.Group)
	}

	// The same studio on the wrong date scores, but is not a match.
	if candidates[1].Match {
		t.Errorf("wrong-date release flagged as a match: %+v", candidates[1])
	}
	if !strings.HasPrefix(candidates[1].Title, "VRBangers.23.12.01") {
		t.Errorf("second candidate = %q, want the same-studio wrong-date release", candidates[1].Title)
	}
	if candidates[1].Score == 0 {
		t.Error("a same-studio candidate should still report a score")
	}

	// A studio with nothing in common sorts last and scores nothing.
	if !strings.HasPrefix(candidates[2].Title, "SomeOtherSite") {
		t.Errorf("third candidate = %q, want the unrelated release", candidates[2].Title)
	}
	if candidates[2].Score != 0 {
		t.Errorf("unrelated release scored %d, want 0", candidates[2].Score)
	}

	if len(stub.queries) == 0 {
		t.Fatal("no search query was sent")
	}
	if !strings.Contains(stub.queries[0], "VRBangers") || !strings.Contains(stub.queries[0], "Jane Doe") {
		t.Errorf("first query = %q, want studio and cast", stub.queries[0])
	}
}

func TestSearchSceneReleasesDeduplicatesAcrossQueries(t *testing.T) {
	withGrabConfig(t, grabConfig())

	stub := &stubIndexer{cfg: indexer.Config{ID: "idx-1", Name: "Prowlarr"}, releases: []indexer.Release{indexerRelease(testReleaseTitle)}}
	useIndexer(t, stub)

	candidates, err := SearchSceneReleases(grabTestScene(), "", "")
	if err != nil {
		t.Fatalf("SearchSceneReleases: %s", err)
	}
	if len(stub.queries) < 2 {
		t.Fatalf("expected several fallback queries, got %v", stub.queries)
	}
	if len(candidates) != 1 {
		t.Errorf("got %d candidates, want the single release deduplicated", len(candidates))
	}
}

func TestSearchSceneReleasesFiltersLowSeeders(t *testing.T) {
	withGrabConfig(t, grabConfig(func() { config.Config.TorrentNZB.MinSeeders = 5 }))

	dead := indexerRelease(testReleaseTitle)
	dead.Seeders = 1
	unknown := indexerRelease("VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-OTHER")
	unknown.Seeders = -1

	useIndexer(t, &stubIndexer{cfg: indexer.Config{ID: "idx-1", Name: "Prowlarr"}, releases: []indexer.Release{dead, unknown}})

	candidates, err := SearchSceneReleases(grabTestScene(), "", "")
	if err != nil {
		t.Fatalf("SearchSceneReleases: %s", err)
	}
	if len(candidates) != 1 || candidates[0].Title != unknown.Title {
		t.Fatalf("got %+v, want only the release with unknown seeders", candidates)
	}
}

func TestSearchSceneReleasesDisabled(t *testing.T) {
	withGrabConfig(t, func() { config.Config.TorrentNZB.Enabled = false })
	if _, err := SearchSceneReleases(grabTestScene(), "", ""); err == nil {
		t.Fatal("expected an error when torrent/NZB downloads are disabled")
	}
}

func TestSearchSceneReleasesNoIndexers(t *testing.T) {
	withGrabConfig(t, func() {
		config.Config.TorrentNZB.Enabled = true
		config.Config.Indexers = nil
	})
	if _, err := SearchSceneReleases(grabTestScene(), "", ""); err == nil {
		t.Fatal("expected an error with no indexers configured")
	}
}

func TestSearchSceneReleasesSkipsDisabledIndexer(t *testing.T) {
	withGrabConfig(t, grabConfig(func() { config.Config.Indexers[0].Enabled = false }))
	if _, err := SearchSceneReleases(grabTestScene(), "", ""); err == nil {
		t.Fatal("expected no enabled indexers")
	}
}

func TestSearchSceneReleasesReportsIndexerFailure(t *testing.T) {
	withGrabConfig(t, grabConfig())
	useIndexer(t, &stubIndexer{cfg: indexer.Config{ID: "idx-1", Name: "Prowlarr"}, err: context.DeadlineExceeded})

	if _, err := SearchSceneReleases(grabTestScene(), "", ""); err == nil {
		t.Fatal("expected the indexer failure to surface when nothing was found")
	}
}

func TestSearchSceneReleasesReturnsResultsDespiteOneBrokenIndexer(t *testing.T) {
	withGrabConfig(t, grabConfig(func() {
		config.Config.Indexers = append(config.Config.Indexers,
			config.IndexerConfig{ID: "idx-2", Name: "Broken", Type: "torznab", URL: "http://broken/api", Enabled: true})
	}))

	broken := &stubIndexer{cfg: indexer.Config{ID: "idx-2", Name: "Broken"}, err: context.DeadlineExceeded}
	good := &stubIndexer{cfg: indexer.Config{ID: "idx-1", Name: "Prowlarr"}, releases: []indexer.Release{indexerRelease(testReleaseTitle)}}

	newIndexerClient = func(cfg indexer.Config) (indexer.Client, error) {
		if cfg.ID == "idx-2" {
			return broken, nil
		}
		return good, nil
	}
	t.Cleanup(func() { newIndexerClient = indexer.New })

	candidates, err := SearchSceneReleases(grabTestScene(), "", "")
	if err != nil {
		t.Fatalf("one broken indexer must not fail the search: %s", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
}

// --------------------------------------------------------------------------- //
// Grab
// --------------------------------------------------------------------------- //

func TestStartSceneGrabHandsReleaseToClient(t *testing.T) {
	withGrabConfig(t, grabConfig())

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", Name: testReleaseTitle, State: dlclient.StateQueued}
	useClient(t, stub)

	candidate := ReleaseCandidate{Title: testReleaseTitle, Protocol: "torrent", MagnetURL: "magnet:?xt=urn:btih:ABC", Size: 3930769406}
	grab, err := StartSceneGrab(grabTestScene(), candidate)
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(grab.ID) })

	if len(stub.added) != 1 {
		t.Fatalf("client received %d adds, want 1", len(stub.added))
	}
	req := stub.added[0]
	if req.Magnet != candidate.MagnetURL {
		t.Errorf("magnet = %q, want %q", req.Magnet, candidate.MagnetURL)
	}
	if req.Name != testReleaseTitle {
		t.Errorf("name = %q, want the release title", req.Name)
	}
	if !strings.HasPrefix(req.Ref, grabRefPrefix) {
		t.Errorf("ref = %q, want a %q prefix", req.Ref, grabRefPrefix)
	}
	if id, ok := sceneIDFromRef(req.Ref); !ok || id != 42 {
		t.Errorf("ref %q does not carry scene id 42 (%d, %v)", req.Ref, id, ok)
	}
	if req.Category != "xbvr" || req.SavePath != "/downloads" {
		t.Errorf("category/savePath = %q/%q", req.Category, req.SavePath)
	}

	if grab.JobID != "abc123" {
		t.Errorf("job id = %q, want abc123", grab.JobID)
	}
	if grab.Status != GrabStatusQueued {
		t.Errorf("status = %q, want queued", grab.Status)
	}
	if grab.ClientName != "qBittorrent" || grab.Provider != "torrent" {
		t.Errorf("client/provider = %q/%q", grab.ClientName, grab.Provider)
	}
	if grab.Ref != req.Ref {
		t.Errorf("grab ref %q does not match the ref handed to the client %q", grab.Ref, req.Ref)
	}
	if grab.SceneDBID != 42 {
		t.Errorf("scene db id = %d, want 42", grab.SceneDBID)
	}
}

func TestStartSceneGrabRejectsReleaseWithoutSource(t *testing.T) {
	withGrabConfig(t, grabConfig())
	if _, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: "nothing"}); err == nil {
		t.Fatal("expected an error for a release with no magnet and no URL")
	}
}

func TestStartSceneGrabWithoutClient(t *testing.T) {
	withGrabConfig(t, grabConfig(func() { config.Config.DownloadClients = nil }))

	_, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: "x", MagnetURL: "magnet:?xt=urn:btih:A"})
	if err == nil || !strings.Contains(err.Error(), "no torrent download client") {
		t.Fatalf("error = %v, want a missing-client error", err)
	}
}

func TestStartSceneGrabSurfacesClientError(t *testing.T) {
	withGrabConfig(t, grabConfig())
	useClient(t, &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}, addError: dlclient.ErrNotFound})

	_, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: "x", MagnetURL: "magnet:?xt=urn:btih:A"})
	if err == nil || !strings.Contains(err.Error(), "qBittorrent") {
		t.Fatalf("error = %v, want it to name the client", err)
	}
}

// --------------------------------------------------------------------------- //
// Polling and import
// --------------------------------------------------------------------------- //

// completedDownload lays out a finished torrent: the feature plus a sample.
func completedDownload(t *testing.T) (savePath, feature string) {
	t.Helper()
	savePath = filepath.Join(t.TempDir(), "downloads")
	if err := os.MkdirAll(savePath, 0o755); err != nil {
		t.Fatal(err)
	}
	feature = filepath.Join(savePath, testReleaseTitle+".mp4")
	if err := os.WriteFile(feature, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(savePath, "sample.mp4"), make([]byte, 16), 0o644); err != nil {
		t.Fatal(err)
	}
	return savePath, feature
}

func TestPollGrabsReportsProgressThenImports(t *testing.T) {
	savePath, feature := completedDownload(t)
	importDir := filepath.Join(t.TempDir(), "library")

	withGrabConfig(t, grabConfig(func() { config.Config.TorrentNZB.ImportDir = importDir }))

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)
	rescans := noRescan(t)

	grab, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: testReleaseTitle, Protocol: "torrent", MagnetURL: "magnet:?xt=urn:btih:ABC"})
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(grab.ID) })

	// First poll: still downloading.
	stub.jobs = []dlclient.Job{{
		ID: "abc123", Ref: grab.Ref, Name: grab.ReleaseTitle,
		State: dlclient.StateDownloading, Progress: 42, Downloaded: 1720, Total: 4096, Speed: 100, ETA: 24,
		SavePath: savePath,
	}}
	PollGrabs()

	state := grabByID(t, grab.ID)
	if state.Status != GrabStatusDownloading {
		t.Errorf("status = %q, want downloading", state.Status)
	}
	if state.Progress != 42 {
		t.Errorf("progress = %v, want 42", state.Progress)
	}
	if *rescans != 0 {
		t.Error("rescan ran before the download finished")
	}

	// Second poll: completed, so the largest video file gets imported.
	stub.jobs = []dlclient.Job{{
		ID: "abc123", Ref: grab.Ref, Name: grab.ReleaseTitle,
		State: dlclient.StateCompleted, Progress: 100, Downloaded: 4096, Total: 4096,
		SavePath: savePath, ContentPath: feature,
	}}
	PollGrabs()

	state = grabByID(t, grab.ID)
	if state.Status != GrabStatusCompleted {
		t.Fatalf("status = %q, want completed (%s)", state.Status, state.Error)
	}
	want := filepath.Join(importDir, filepath.Base(feature))
	if state.Destination != want {
		t.Errorf("destination = %q, want %q", state.Destination, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("imported file missing: %s", err)
	}
	if *rescans != 1 {
		t.Errorf("rescan ran %d times, want 1", *rescans)
	}

	// Polling again must not import a second time.
	PollGrabs()
	if *rescans != 1 {
		t.Errorf("rescan ran %d times after a second poll, want 1", *rescans)
	}
}

func TestPollGrabsMatchesJobByNameWithoutRef(t *testing.T) {
	savePath, feature := completedDownload(t)
	importDir := filepath.Join(t.TempDir(), "library")

	withGrabConfig(t, grabConfig(func() { config.Config.TorrentNZB.ImportDir = importDir }))

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)
	noRescan(t)

	grab, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: testReleaseTitle, MagnetURL: "magnet:?xt=urn:btih:A"})
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(grab.ID) })

	// Transmission 3 cannot carry labels, so the job arrives with no ref.
	stub.jobs = []dlclient.Job{{
		ID: "abc123", Name: grab.ReleaseTitle,
		State: dlclient.StateCompleted, Progress: 100, SavePath: savePath, ContentPath: feature,
	}}
	PollGrabs()

	if state := grabByID(t, grab.ID); state.Status != GrabStatusCompleted {
		t.Fatalf("status = %q, want completed (%s)", state.Status, state.Error)
	}
}

func TestPollGrabsRemovesJobAfterImport(t *testing.T) {
	savePath, feature := completedDownload(t)
	withGrabConfig(t, grabConfig(func() {
		config.Config.TorrentNZB.ImportDir = filepath.Join(t.TempDir(), "library")
		config.Config.TorrentNZB.RemoveAfterImport = true
	}))

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)
	noRescan(t)

	grab, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: testReleaseTitle, MagnetURL: "magnet:?xt=urn:btih:A"})
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(grab.ID) })

	stub.jobs = []dlclient.Job{{
		ID: "abc123", Ref: grab.Ref, Name: grab.ReleaseTitle,
		State: dlclient.StateCompleted, Progress: 100, SavePath: savePath, ContentPath: feature,
	}}
	PollGrabs()

	if len(stub.removed) != 1 || stub.removed[0] != "abc123" {
		t.Errorf("client removals = %v, want [abc123]", stub.removed)
	}
}

func TestPollGrabsFailsWhenImportFails(t *testing.T) {
	savePath, feature := completedDownload(t)
	withGrabConfig(t, grabConfig(func() { config.Config.TorrentNZB.ImportDir = "" }))

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)
	noRescan(t)

	grab, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: testReleaseTitle, MagnetURL: "magnet:?xt=urn:btih:A"})
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(grab.ID) })

	stub.jobs = []dlclient.Job{{
		ID: "abc123", Ref: grab.Ref, Name: grab.ReleaseTitle,
		State: dlclient.StateCompleted, Progress: 100, SavePath: savePath, ContentPath: feature,
	}}
	PollGrabs()

	state := grabByID(t, grab.ID)
	if state.Status != GrabStatusFailed {
		t.Fatalf("status = %q, want failed after an import error", state.Status)
	}
	if !strings.Contains(state.Error, "import folder") {
		t.Errorf("error = %q, want it to mention the import folder", state.Error)
	}
}

func TestPollGrabsFailsWhenClientIsGone(t *testing.T) {
	withGrabConfig(t, grabConfig())

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)

	grab, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: "x", MagnetURL: "magnet:?xt=urn:btih:A"})
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(grab.ID) })

	// The client configuration disappears while the grab is running.
	config.Config.DownloadClients = nil
	PollGrabs()

	state := grabByID(t, grab.ID)
	if state.Status != GrabStatusFailed {
		t.Fatalf("status = %q, want failed", state.Status)
	}
	if !strings.Contains(state.Error, "no longer configured") {
		t.Errorf("error = %q", state.Error)
	}
}

func TestPollGrabsToleratesListFailure(t *testing.T) {
	withGrabConfig(t, grabConfig())

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}, listError: context.DeadlineExceeded}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)

	grab, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: "x", MagnetURL: "magnet:?xt=urn:btih:A"})
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(grab.ID) })

	// A transient failure to reach the client must leave the grab running.
	PollGrabs()
	if state := grabByID(t, grab.ID); state.Status != GrabStatusQueued {
		t.Errorf("status = %q, want the grab left alone", state.Status)
	}
}

func TestCancelGrabRemovesFromClient(t *testing.T) {
	withGrabConfig(t, grabConfig())

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)

	grab, err := StartSceneGrab(grabTestScene(), ReleaseCandidate{Title: "x", MagnetURL: "magnet:?xt=urn:btih:A"})
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(grab.ID) })

	if err := CancelGrab(grab.ID); err != nil {
		t.Fatalf("CancelGrab: %s", err)
	}
	if len(stub.removed) != 1 || stub.removed[0] != "abc123" {
		t.Errorf("client removals = %v, want [abc123]", stub.removed)
	}
	if state := grabByID(t, grab.ID); state.Status != GrabStatusCancelled {
		t.Errorf("status = %q, want cancelled", state.Status)
	}
	if err := CancelGrab(grab.ID); err == nil {
		t.Error("cancelling a cancelled grab should fail")
	}
}

func TestCancelUnknownGrab(t *testing.T) {
	if err := CancelGrab("nope"); err == nil {
		t.Error("expected an error for an unknown grab")
	}
}

// --------------------------------------------------------------------------- //
// Import helpers
// --------------------------------------------------------------------------- //

func TestLargestVideoFilePicksTheFeature(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Sample"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]int{
		filepath.Join(dir, "sample.mp4"):          64,
		filepath.Join(dir, "Sample", "clip.mp4"):  128,
		filepath.Join(dir, "VRBangers.scene.mp4"): 8192,
		filepath.Join(dir, "cover.jpg"):           1024,
		filepath.Join(dir, ".hidden.mp4"):         65536,
	}
	for path, size := range files {
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := largestVideoFile(dir)
	if err != nil {
		t.Fatalf("largestVideoFile: %s", err)
	}
	want := filepath.Join(dir, "VRBangers.scene.mp4")
	if got != want {
		t.Errorf("largestVideoFile = %q, want %q (hidden files are skipped)", got, want)
	}
}

func TestLargestVideoFileWithoutVideo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := largestVideoFile(dir); err == nil {
		t.Fatal("expected an error when there is no video file")
	}
}

func TestLocateCompletedContent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, testReleaseTitle+".mp4")
	if err := os.WriteFile(file, make([]byte, 32), 0o644); err != nil {
		t.Fatal(err)
	}

	// An explicit content path wins.
	got, err := locateCompletedContent(dlclient.Job{ContentPath: file})
	if err != nil || got != file {
		t.Errorf("locateCompletedContent = %q, %v; want %q", got, err, file)
	}

	// A directory content path falls back to the largest file inside.
	got, err = locateCompletedContent(dlclient.Job{ContentPath: dir})
	if err != nil || got != file {
		t.Errorf("locateCompletedContent(dir) = %q, %v; want %q", got, err, file)
	}

	// With nothing reported at all it fails rather than guessing.
	if _, err := locateCompletedContent(dlclient.Job{}); err == nil {
		t.Error("expected an error when the client reports no location")
	}
}

func TestImportReleaseFileModes(t *testing.T) {
	setup := func(t *testing.T) (workDir, source string) {
		t.Helper()
		workDir = t.TempDir()
		source = filepath.Join(workDir, "src", "scene.mp4")
		if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		return workDir, source
	}

	t.Run("hardlink keeps the original", func(t *testing.T) {
		workDir, source := setup(t)
		importDir := filepath.Join(workDir, "library")
		withGrabConfig(t, grabConfig(func() { config.Config.TorrentNZB.ImportMode = "hardlink" }))

		got, err := importReleaseFile(source, importDir)
		if err != nil {
			t.Fatalf("importReleaseFile: %s", err)
		}
		if got != filepath.Join(importDir, "scene.mp4") {
			t.Errorf("destination = %q", got)
		}
		if _, err := os.Stat(source); err != nil {
			t.Errorf("the original must survive a hardlink: %s", err)
		}
	})

	t.Run("copy keeps the original", func(t *testing.T) {
		workDir, source := setup(t)
		withGrabConfig(t, grabConfig(func() { config.Config.TorrentNZB.ImportMode = "copy" }))

		got, err := importReleaseFile(source, filepath.Join(workDir, "library"))
		if err != nil {
			t.Fatalf("importReleaseFile: %s", err)
		}
		content, err := os.ReadFile(got)
		if err != nil || string(content) != "payload" {
			t.Errorf("copied content = %q, %v", content, err)
		}
		if _, err := os.Stat(source); err != nil {
			t.Errorf("the original must survive a copy: %s", err)
		}
	})

	t.Run("move removes the original", func(t *testing.T) {
		workDir, source := setup(t)
		withGrabConfig(t, grabConfig(func() { config.Config.TorrentNZB.ImportMode = "move" }))

		got, err := importReleaseFile(source, filepath.Join(workDir, "library"))
		if err != nil {
			t.Fatalf("importReleaseFile: %s", err)
		}
		if _, err := os.Stat(got); err != nil {
			t.Errorf("destination missing: %s", err)
		}
		if _, err := os.Stat(source); !os.IsNotExist(err) {
			t.Errorf("the original should be gone after a move, stat err = %v", err)
		}
	})

	t.Run("an existing destination is left alone", func(t *testing.T) {
		workDir, source := setup(t)
		importDir := filepath.Join(workDir, "library")
		if err := os.MkdirAll(importDir, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(importDir, "scene.mp4")
		if err := os.WriteFile(target, []byte("already here"), 0o644); err != nil {
			t.Fatal(err)
		}

		withGrabConfig(t, grabConfig())
		if _, err := importReleaseFile(source, importDir); err != nil {
			t.Fatalf("importReleaseFile: %s", err)
		}
		content, _ := os.ReadFile(target)
		if string(content) != "already here" {
			t.Error("an existing library file must not be overwritten")
		}
	})

	t.Run("a missing source is reported", func(t *testing.T) {
		withGrabConfig(t, grabConfig())
		if _, err := importReleaseFile(filepath.Join(t.TempDir(), "gone.mp4"), t.TempDir()); err == nil {
			t.Error("expected an error for a missing source")
		}
	})

	t.Run("without an import folder it refuses", func(t *testing.T) {
		withGrabConfig(t, grabConfig())
		if _, err := importReleaseFile("/tmp/whatever.mp4", ""); err == nil {
			t.Error("expected an error with no import folder configured")
		}
	})

	t.Run("unknown mode is rejected", func(t *testing.T) {
		workDir, source := setup(t)
		withGrabConfig(t, grabConfig(func() { config.Config.TorrentNZB.ImportMode = "teleport" }))
		if _, err := importReleaseFile(source, filepath.Join(workDir, "library")); err == nil {
			t.Error("expected an error for an unknown import mode")
		}
	})
}

func TestBuildGrabQueries(t *testing.T) {
	scene := grabTestScene()
	queries := buildGrabQueries(scene, "")
	if len(queries) < 3 {
		t.Fatalf("expected several queries, got %v", queries)
	}
	if queries[0] != "VRBangers Jane Doe" {
		t.Errorf("first query = %q, want %q", queries[0], "VRBangers Jane Doe")
	}
	for _, q := range queries {
		if strings.TrimSpace(q) == "" {
			t.Errorf("empty query in %v", queries)
		}
	}

	if got := buildGrabQueries(scene, "  custom query  "); len(got) != 1 || got[0] != "custom query" {
		t.Errorf("custom query = %v", got)
	}
}

func TestBuildGrabQueriesWithoutCast(t *testing.T) {
	scene := grabTestScene()
	scene.Cast = nil
	queries := buildGrabQueries(scene, "")
	if len(queries) == 0 {
		t.Fatal("expected at least one query")
	}
	if queries[0] != "VRBangers" {
		t.Errorf("first query = %q, want the studio alone", queries[0])
	}
}

func TestIndexerProtocol(t *testing.T) {
	if got := indexerProtocol(config.IndexerConfig{Type: "newznab"}); got != dlclient.KindNZB {
		t.Errorf("newznab protocol = %q, want nzb", got)
	}
	if got := indexerProtocol(config.IndexerConfig{Type: "torznab"}); got != dlclient.KindTorrent {
		t.Errorf("torznab protocol = %q, want torrent", got)
	}
	if got := indexerProtocol(config.IndexerConfig{}); got != dlclient.KindTorrent {
		t.Errorf("default protocol = %q, want torrent", got)
	}
}

func TestPickDownloadClientPrefersTheMatchingKind(t *testing.T) {
	withGrabConfig(t, func() {
		config.Config.DownloadClients = []config.DownloadClientConfig{
			{ID: "sab-1", Client: "sabnzbd", Kind: "nzb", URL: "http://sab", Enabled: true},
			{ID: "qb-1", Client: "qbittorrent", Kind: "torrent", URL: "http://qb", Enabled: true},
		}
	})

	got, err := pickDownloadClient(dlclient.KindNZB)
	if err != nil || got.ID != "sab-1" {
		t.Errorf("nzb client = %q, %v; want sab-1", got.ID, err)
	}
	got, err = pickDownloadClient(dlclient.KindTorrent)
	if err != nil || got.ID != "qb-1" {
		t.Errorf("torrent client = %q, %v; want qb-1", got.ID, err)
	}
}

// grabByID fetches a grab snapshot by id.
func grabByID(t *testing.T, id string) Grab {
	t.Helper()
	for _, g := range GetGrabs() {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("grab %q not found", id)
	return Grab{}
}

// --------------------------------------------------------------------------- //
// Release-name candidate selection
// --------------------------------------------------------------------------- //

func TestCandidateSiteValues(t *testing.T) {
	studios := []sceneStudio{
		{column: "site", value: "VRBangers", key: "vrbangers"},
		{column: "site", value: "SexLikeReal", key: "sexlikereal"},
		{column: "studio", value: "BadoinkVR", key: "badoinkvr"},
		{column: "scraper_id", value: "vrbangers", key: "vrbangers"},
		{column: "studio", value: "VRBangers Network", key: "vrbangersnetwork"},
	}

	values := candidateSiteValues(release.ParseName(testReleaseTitle), studios)

	// The exact site and scraper id both match, and the containment rule also
	// catches "VRBangers Network".
	for _, column := range []string{"site", "studio", "scraper_id"} {
		if len(values[column]) == 0 {
			t.Errorf("column %q matched nothing: %+v", column, values)
		}
	}
	if len(values["site"]) != 1 || values["site"][0] != "VRBangers" {
		t.Errorf("site values = %v, want [VRBangers]", values["site"])
	}
	if len(values["studio"]) != 1 || values["studio"][0] != "VRBangers Network" {
		t.Errorf("studio values = %v, want [VRBangers Network] (containment rule)", values["studio"])
	}
	if len(values["scraper_id"]) != 1 || values["scraper_id"][0] != "vrbangers" {
		t.Errorf("scraper_id values = %v, want [vrbangers]", values["scraper_id"])
	}

	// A studio with nothing in common must contribute nothing, so a release can
	// never pull in an unrelated studio's scenes.
	for _, v := range values["site"] {
		if v == "SexLikeReal" {
			t.Error("an unrelated studio was matched")
		}
	}
}

func TestCandidateSiteValuesShortKeys(t *testing.T) {
	rel := release.ParseName(testReleaseTitle) // site key "vrbangers"
	studios := []sceneStudio{
		// Neither equal to "vrbangers" nor long enough for the containment rule,
		// so this must not match: a two-letter key would otherwise match half the
		// library.
		{column: "site", value: "VR", key: "vr"},
		{column: "site", value: "", key: ""},
	}
	if values := candidateSiteValues(rel, studios); len(values) != 0 {
		t.Errorf("short studio keys must not match by containment: %+v", values)
	}

	// Identical short keys are still the same studio, and must match.
	short := release.ParseName("VR.23.10.14.Actor.XXX.2160p")
	values := candidateSiteValues(short, studios)
	if len(values["site"]) != 1 || values["site"][0] != "VR" {
		t.Errorf("identical studio keys should match: %+v", values)
	}

	// An unparsable release matches nothing at all.
	if values := candidateSiteValues(release.Release{}, studios); len(values) != 0 {
		t.Errorf("an empty release matched: %+v", values)
	}
}

func TestResolveScrapersMatchesStudioNames(t *testing.T) {
	// The scraper registry is populated by package initialisation, so this only
	// asserts the matching rule rather than a specific scraper list.
	rel := release.ParseName(testReleaseTitle)
	for _, id := range resolveScrapers(rel) {
		if strings.HasSuffix(id, "-single_scene") {
			t.Errorf("single-scene scrapers must be skipped, got %q", id)
		}
	}
	if got := resolveScrapers(release.Release{}); got != nil {
		t.Errorf("an empty release should resolve no scrapers, got %v", got)
	}
}

// --------------------------------------------------------------------------- //
// Restart recovery
// --------------------------------------------------------------------------- //

func TestGrabRefRoundTrip(t *testing.T) {
	ref := grabRef(42)
	if !strings.HasPrefix(ref, grabRefPrefix) {
		t.Fatalf("ref %q lacks the xbvr prefix", ref)
	}
	id, ok := sceneIDFromRef(ref)
	if !ok || id != 42 {
		t.Errorf("sceneIDFromRef(%q) = %d, %v; want 42, true", ref, id, ok)
	}

	// Tags other tools own, or our own malformed ones, must be ignored rather
	// than adopted as a grab.
	for _, bad := range []string{"", "grab-123", "xbvr-", "xbvr-abc-1", "xbvr--1", "something-ref"} {
		if _, ok := sceneIDFromRef(bad); ok {
			t.Errorf("sceneIDFromRef(%q) unexpectedly succeeded", bad)
		}
	}

	if a, b := grabRef(1), grabRef(1); a == b {
		t.Error("two refs for the same scene must differ")
	}
}

func TestAdoptOrphanedGrabsResumesAnUnfinishedDownload(t *testing.T) {
	savePath, feature := completedDownload(t)
	importDir := filepath.Join(t.TempDir(), "library")

	withGrabConfig(t, grabConfig(func() {
		config.Config.TorrentNZB.ImportDir = importDir
		lookupSceneForGrab = func(uint) string { return "vrbangers-123" }
	}))

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)
	rescans := noRescan(t)

	// xbvr restarted while this download was still running, so it has no record
	// of it - only the tag the client kept.
	ref := grabRef(42)
	stub.jobs = []dlclient.Job{{
		ID: "abc123", Ref: ref, Name: testReleaseTitle,
		State: dlclient.StateDownloading, Progress: 30, Downloaded: 1200, Total: 4096,
		SavePath: savePath,
	}}

	AdoptOrphanedGrabs()

	grabs := GetGrabs()
	if len(grabs) != 1 {
		t.Fatalf("adopted %d grabs, want 1", len(grabs))
	}
	adopted := grabs[0]
	if adopted.SceneDBID != 42 || adopted.Ref != ref {
		t.Errorf("adopted grab = %+v, want scene 42 and ref %q", adopted, ref)
	}
	if adopted.SceneID != "vrbangers-123" {
		t.Errorf("scene id = %q, want the looked-up scene id", adopted.SceneID)
	}
	if adopted.Status != GrabStatusDownloading {
		t.Errorf("status = %q, want downloading", adopted.Status)
	}

	// It must then be polled to completion and imported like any other grab.
	stub.jobs = []dlclient.Job{{
		ID: "abc123", Ref: ref, Name: testReleaseTitle,
		State: dlclient.StateCompleted, Progress: 100, SavePath: savePath, ContentPath: feature,
	}}
	PollGrabs()

	if state := grabByID(t, adopted.ID); state.Status != GrabStatusCompleted {
		t.Fatalf("status = %q, want completed (%s)", state.Status, state.Error)
	}
	if *rescans != 1 {
		t.Errorf("rescan ran %d times, want 1", *rescans)
	}
}

func TestAdoptOrphanedGrabsIgnoresForeignAndFinishedJobs(t *testing.T) {
	withGrabConfig(t, grabConfig())

	foreign := grabRef(42)
	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	useClient(t, stub)
	stub.jobs = []dlclient.Job{
		// Not tagged by xbvr.
		{ID: "a", Ref: "someone-elses-tag", State: dlclient.StateDownloading},
		// Already finished, so there is nothing to resume.
		{ID: "b", Ref: foreign, State: dlclient.StateCompleted, Progress: 100},
		// A grab this session already tracks.
		{ID: "c", Ref: grabRef(7), State: dlclient.StateDownloading},
	}

	grabStore.mu.Lock()
	grabStore.grabs["existing"] = &grabState{snapshot: Grab{Ref: "xbvr-7-1", ID: "existing", Status: GrabStatusDownloading}}
	grabStore.mu.Unlock()

	AdoptOrphanedGrabs()

	// Only the one unrelated to the already-tracked ref may be adopted; the
	// completed and foreign jobs must be left alone.
	for _, g := range GetGrabs() {
		if g.Ref == "someone-elses-tag" {
			t.Error("a job xbvr did not tag was adopted")
		}
		if g.Ref == foreign {
			t.Error("a finished job was adopted")
		}
	}
}

func TestAdoptOrphanedGrabsDoesNothingWhenDisabled(t *testing.T) {
	withGrabConfig(t, func() {
		config.Config.TorrentNZB.Enabled = false
		config.Config.DownloadClients = []config.DownloadClientConfig{
			{ID: "qb-1", Client: "qbittorrent", Kind: "torrent", URL: "http://x", Enabled: true},
		}
	})

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	useClient(t, stub)
	stub.jobs = []dlclient.Job{{ID: "a", Ref: grabRef(42), State: dlclient.StateDownloading}}

	AdoptOrphanedGrabs()
	if len(GetGrabs()) != 0 {
		t.Error("nothing should be adopted while grabbing is disabled")
	}
}

func TestStartSceneGrabRefusesADuplicate(t *testing.T) {
	withGrabConfig(t, grabConfig())

	stub := &stubClient{cfg: dlclient.Config{ID: "qb-1", Kind: "torrent"}}
	stub.addReturns = dlclient.Job{ID: "abc123", State: dlclient.StateQueued}
	useClient(t, stub)

	candidate := ReleaseCandidate{Title: testReleaseTitle, Protocol: "torrent", MagnetURL: "magnet:?xt=urn:btih:A"}
	first, err := StartSceneGrab(grabTestScene(), candidate)
	if err != nil {
		t.Fatalf("StartSceneGrab: %s", err)
	}
	t.Cleanup(func() { RemoveGrab(first.ID) })

	if _, err := StartSceneGrab(grabTestScene(), candidate); err == nil {
		t.Fatal("expected a second grab for the same scene to be refused")
	}
	if len(stub.added) != 1 {
		t.Errorf("client received %d adds, want 1", len(stub.added))
	}

	// Once the first grab has finished, a new one is allowed.
	if err := CancelGrab(first.ID); err != nil {
		t.Fatalf("CancelGrab: %s", err)
	}
	if _, err := StartSceneGrab(grabTestScene(), candidate); err != nil {
		t.Errorf("a new grab should be allowed after the previous one finished: %s", err)
	}
}
