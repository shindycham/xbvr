package slr

import (
	"os"
	"testing"

	"github.com/xbapps/xbvr/pkg/models"
)

// Live check: set XBVR_SLR_LIVE=1 to fetch the real renditions for a scene,
// confirming sizes are read from the page payload.
func TestGetRenditionsLive(t *testing.T) {
	if os.Getenv("XBVR_SLR_LIVE") != "1" {
		t.Skip("set XBVR_SLR_LIVE=1 to run the live SLR renditions check")
	}

	scene := models.Scene{
		SceneID:  "slr-99768",
		SceneURL: "https://www.sexlikereal.com/scenes/she-the-best-99768",
	}

	rs, err := GetRenditions(scene)
	if err != nil {
		t.Fatalf("GetRenditions: %v", err)
	}
	if len(rs) == 0 {
		t.Fatal("no renditions returned")
	}
	for _, r := range rs {
		u := r.URL
		if len(u) > 70 {
			u = u[:70]
		}
		t.Logf("%s %dp width=%d size=%d url=%s", r.Codec, r.Height, r.Width, r.Size, u)
		if r.Size == 0 {
			t.Errorf("expected a size for %s %dp", r.Codec, r.Height)
		}
	}
}
