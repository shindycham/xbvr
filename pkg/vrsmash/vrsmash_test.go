package vrsmash

import (
	"strings"
	"testing"

	"github.com/xbapps/xbvr/pkg/models"
)

func TestSceneSlug(t *testing.T) {
	slug, err := SceneSlug(models.Scene{SceneURL: "https://www.vrsmash.com/video/3-different-sizes/"})
	if err != nil || slug != "3-different-sizes" {
		t.Fatalf("got slug=%q err=%v", slug, err)
	}
	if _, err := SceneSlug(models.Scene{SceneURL: "https://example.com/x"}); err == nil {
		t.Error("expected an error for a non-vrsmash URL")
	}
}

func TestIsScene(t *testing.T) {
	if !IsScene(models.Scene{SceneURL: "https://www.vrsmash.com/video/x/"}) {
		t.Error("expected URL match")
	}
	if !IsScene(models.Scene{SceneID: "vrsmash-abc"}) {
		t.Error("expected SceneID match")
	}
	if IsScene(models.Scene{SceneURL: "https://www.sexlikereal.com/scenes/x-1", SceneID: "slr-1"}) {
		t.Error("expected non-vrsmash scene to be rejected")
	}
}

func TestBuildFilenameFrom(t *testing.T) {
	got := BuildFilenameFrom("FuckPassVR (VRSmash)", "Passport: to Passion?", "passport-to-passion", "6K")
	want := "VRsMash_FuckPassVR_Passport; to Passion¿_6K_passport-to-passion.mp4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpectedFilenameUsesKnownList(t *testing.T) {
	scene := models.Scene{
		SceneURL:     "https://www.vrsmash.com/video/passport-to-passion/",
		Site:         "FuckPassVR (VRSmash)",
		Title:        "Passport to Passion",
		FilenamesArr: `["VRsMash_FuckPassVR_Passport to Passion_6K_passport-to-passion.mp4"]`,
	}
	got := ExpectedFilename(scene, "6K")
	want := "VRsMash_FuckPassVR_Passport to Passion_6K_passport-to-passion.mp4"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHeightFromQuality(t *testing.T) {
	cases := map[string]int{"4k": 2160, "6K": 3240, "8k": 4320, "2k": 1440, "bogus": 0}
	for in, want := range cases {
		if got := heightFromQuality(in); got != want {
			t.Errorf("heightFromQuality(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFilenameQualitiesIncludesCommon(t *testing.T) {
	got := FilenameQualities()
	for _, want := range []string{"4K", "6K", "8K"} {
		found := false
		for _, q := range got {
			if q == want {
				found = true
			}
		}
		if !found {
			t.Errorf("FilenameQualities missing %s", want)
		}
	}
}

func TestParseSourcesPrefersPaid(t *testing.T) {
	srcs := map[string]map[string]source{
		"free": {"4k": {Name: "4k", Path: "https://cdn/4k-trailer.mp4", Size: 10}},
		"paid": {"6k": {Name: "6k", Path: "https://cdn/6k-full.mp4", Size: 20}},
	}
	rs, err := parseSources(srcs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("expected only the paid group, got %+v", rs)
	}
	if rs[0].Quality != "6K" || rs[0].URL != "https://cdn/6k-full.mp4" || rs[0].Height != 3240 {
		t.Errorf("unexpected rendition: %+v", rs[0])
	}
}

func TestParseSourcesFreeOnlyIsAuthorisationError(t *testing.T) {
	_, err := parseSources(map[string]map[string]source{
		"free": {"4k": {Name: "4k", Path: "https://cdn/4k-trailer.mp4"}},
	})
	if err == nil || !strings.Contains(err.Error(), "trailer") {
		t.Fatalf("expected a trailer/authorisation error, got %v", err)
	}
}

func TestParseSourcesEmpty(t *testing.T) {
	if _, err := parseSources(nil); err == nil {
		t.Fatal("expected an error for empty sources")
	}
}
