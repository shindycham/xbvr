package vrsmash

import (
	"os"
	"strings"
	"testing"

	"github.com/xbapps/xbvr/pkg/models"
)

// Live check: set XBVR_VRSMASH_LIVE=1 to query the real VRsMash content API.
// It passes whether or not the current session is authorised: without a valid
// atoken the API exposes only trailer sources, which GetRenditions must reject
// with a clear message rather than downloading a preview.
func TestGetRenditionsLive(t *testing.T) {
	if os.Getenv("XBVR_VRSMASH_LIVE") != "1" {
		t.Skip("set XBVR_VRSMASH_LIVE=1 to run the live VRsMash source check")
	}

	scene := models.Scene{
		SceneID:  "vrsmash-live-test",
		SceneURL: "https://www.vrsmash.com/video/3-different-sizes/",
	}

	renditions, err := GetRenditions(scene)
	if err != nil {
		t.Logf("GetRenditions error (expected without an authorised session): %v", err)
		if !strings.Contains(err.Error(), "trailer") &&
			!strings.Contains(err.Error(), "session expired") &&
			!strings.Contains(err.Error(), "cookies") {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}

	if len(renditions) == 0 {
		t.Fatal("expected at least one rendition")
	}
	t.Logf("got %d renditions; first=%+v", len(renditions), renditions[0])
	for _, r := range renditions {
		if r.Provider != "vrsmash" || r.URL == "" || r.Quality == "" {
			t.Errorf("incomplete rendition: %+v", r)
		}
	}
}
