package tasks

import (
	"reflect"
	"testing"
	"time"
)

// idsOf harvests the ids of a download list, in order.
func downloadIDs(list []Download) []string {
	out := make([]string, 0, len(list))
	for _, d := range list {
		out = append(out, d.ID)
	}
	return out
}

func grabIDs(list []Grab) []string {
	out := make([]string, 0, len(list))
	for _, g := range list {
		out = append(out, g.ID)
	}
	return out
}

// TestGetDownloadsOrderIsStable is a regression test for the download list
// reshuffling itself in the UI.
//
// The states live in a map, and Go randomises map iteration order, so the
// endpoint used to return a different order on every call. The page polls once
// a second, which made the list appear to shuffle on its own whenever there were
// enough entries to notice.
func TestGetDownloadsOrderIsStable(t *testing.T) {
	downloads.mu.Lock()
	saved := downloads.downloads
	downloads.downloads = map[string]*downloadState{}
	downloads.mu.Unlock()
	t.Cleanup(func() {
		downloads.mu.Lock()
		downloads.downloads = saved
		downloads.mu.Unlock()
	})

	start := time.Now()

	// Five downloads sharing a start time: without the id tie-break their
	// relative order would still be arbitrary.
	for _, id := range []string{"d-c", "d-a", "d-e", "d-b", "d-d"} {
		state := &downloadState{}
		state.snapshot = Download{ID: id, Status: DownloadStatusDownloading, StartedAt: start}
		downloads.downloads[id] = state
	}
	// A newer one, which must sort ahead of all of them.
	newer := &downloadState{}
	newer.snapshot = Download{ID: "d-new", Status: DownloadStatusDownloading, StartedAt: start.Add(time.Minute)}
	downloads.downloads["d-new"] = newer

	first := downloadIDs(GetDownloads())
	if len(first) != 6 {
		t.Fatalf("got %d downloads, want 6", len(first))
	}
	if first[0] != "d-new" {
		t.Errorf("first entry = %q, want the newest download", first[0])
	}
	// Equal timestamps fall back to the id, so the order is defined rather than
	// whatever the map happened to yield.
	want := []string{"d-new", "d-a", "d-b", "d-c", "d-d", "d-e"}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("order = %v, want %v", first, want)
	}

	// The real point: repeated reads agree.
	for i := 0; i < 100; i++ {
		if got := downloadIDs(GetDownloads()); !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d returned %v, want %v", i, got, first)
		}
	}
}

// TestGetGrabsOrderIsStable covers the same hazard for grabs.
func TestGetGrabsOrderIsStable(t *testing.T) {
	grabStore.mu.Lock()
	saved := grabStore.grabs
	grabStore.grabs = map[string]*grabState{}
	grabStore.mu.Unlock()
	t.Cleanup(func() {
		grabStore.mu.Lock()
		grabStore.grabs = saved
		grabStore.mu.Unlock()
	})

	start := time.Now()
	for _, id := range []string{"g-c", "g-a", "g-b"} {
		state := &grabState{}
		state.snapshot = Grab{ID: id, Status: GrabStatusDownloading, StartedAt: start}
		grabStore.grabs[id] = state
	}
	older := &grabState{}
	older.snapshot = Grab{ID: "g-old", Status: GrabStatusDownloading, StartedAt: start.Add(-time.Hour)}
	grabStore.grabs["g-old"] = older

	first := grabIDs(GetGrabs())
	want := []string{"g-a", "g-b", "g-c", "g-old"}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("order = %v, want %v", first, want)
	}
	for i := 0; i < 100; i++ {
		if got := grabIDs(GetGrabs()); !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d returned %v, want %v", i, got, first)
		}
	}
}
