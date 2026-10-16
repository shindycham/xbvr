package scrape

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xbapps/xbvr/pkg/models"
)

// Live check: set XBVR_VRSMASH_LIVE=1 to scrape one real VRsMash scene through
// the public content API.
func TestVRSmashScrapeSingleLive(t *testing.T) {
	if os.Getenv("XBVR_VRSMASH_LIVE") != "1" {
		t.Skip("set XBVR_VRSMASH_LIVE=1 to run the live VRsMash scrape")
	}

	wg := &models.ScrapeWG{}
	wg.Add(1)
	out := make(chan models.ScrapedScene, 4)

	go func() {
		_ = VRSmash(wg, false, nil, out, "https://www.vrsmash.com/video/3-different-sizes/", "vrsmash", "VRSmash", "VRSmash", "https://www.vrsmash.com", "", true, "")
	}()

	select {
	case sc := <-out:
		t.Logf("scene id=%s title=%q studio=%q tags=%d cast=%d covers=%d filenames=%d trailer=%s",
			sc.SceneID, sc.Title, sc.Studio, len(sc.Tags), len(sc.Cast), len(sc.Covers), len(sc.Filenames), sc.TrailerType)
		if sc.SceneID == "" || sc.Title == "" {
			t.Fatalf("incomplete scraped scene: %+v", sc)
		}
		if len(sc.Filenames) == 0 {
			t.Error("expected generated filenames")
		}
	case <-time.After(90 * time.Second):
		t.Fatal("timed out waiting for a scraped scene")
	}
}

// Live check of the paged list path: XBVR_VRSMASH_LIVE=1 scrapes page 1 and
// verifies each item produced a complete ScrapedScene.
func TestVRSmashScrapeListLive(t *testing.T) {
	if os.Getenv("XBVR_VRSMASH_LIVE") != "1" {
		t.Skip("set XBVR_VRSMASH_LIVE=1 to run the live VRsMash scrape")
	}

	wg := &models.ScrapeWG{}
	wg.Add(1)
	out := make(chan models.ScrapedScene, 128)

	go func() {
		_ = VRSmash(wg, false, nil, out, "", "vrsmash", "VRSmash", "VRSmash", "https://www.vrsmash.com", "", true, "")
		close(out)
	}()

	count := 0
	for sc := range out {
		count++
		if sc.SceneID == "" || sc.Title == "" || sc.HomepageURL == "" {
			t.Errorf("incomplete scene: %+v", sc)
		}
		if len(sc.Filenames) == 0 {
			t.Errorf("scene %s has no filenames", sc.SceneID)
		}
	}
	if count == 0 {
		t.Fatal("page 1 produced no scenes")
	}
	t.Logf("scraped %d scenes from page 1", count)
}

// Live check that the extra catalog feeds are walked: set XBVR_VRSMASH_LIVE=1.
//
// It scrapes with limitScraping so only one page of each feed is read. A single
// page of the main feed holds one or two trans scenes, while the trans feed holds
// a whole page of them, so the count only reaches the threshold if the feed was
// actually walked.
func TestVRSmashScrapesTheTransFeedLive(t *testing.T) {
	if os.Getenv("XBVR_VRSMASH_LIVE") != "1" {
		t.Skip("set XBVR_VRSMASH_LIVE=1 to run the live VRsMash scrape")
	}

	wg := &models.ScrapeWG{}
	wg.Add(1)
	out := make(chan models.ScrapedScene, 256)

	go func() {
		_ = VRSmash(wg, false, nil, out, "", "vrsmash", "VRSmash", "VRSmash", "https://www.vrsmash.com", "", true, "")
		close(out)
	}()

	trans := 0
	seen := map[string]bool{}
	count := 0
	for sc := range out {
		count++
		if sc.SceneID == "" {
			t.Errorf("scene with no id: %+v", sc)
		}
		// A scene appearing in both feeds must still only be emitted once.
		if seen[sc.SceneID] {
			t.Errorf("scene %s was emitted twice", sc.SceneID)
		}
		seen[sc.SceneID] = true

		for _, tag := range sc.Tags {
			if strings.EqualFold(tag, "Trans") {
				trans++
				break
			}
		}
	}

	t.Logf("scraped %d scenes, %d of them trans", count, trans)
	if count == 0 {
		t.Fatal("no scenes were scraped")
	}
	if trans < 5 {
		t.Errorf("only %d trans scenes scraped: the trans feed was probably not walked", trans)
	}
}
