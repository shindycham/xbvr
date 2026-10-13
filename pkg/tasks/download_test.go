package tasks

import (
	"testing"

	"github.com/xbapps/xbvr/pkg/models"
)

func TestSelectRendition(t *testing.T) {
	renditions := []DownloadRendition{
		{Provider: "slr", Codec: "h264", Quality: "1080p", Height: 1080},
		{Provider: "slr", Codec: "h264", Quality: "2160p", Height: 2160},
		{Provider: "slr", Codec: "h265", Quality: "2700p", Height: 2700},
	}

	if got, _ := selectRendition(renditions, "h264", "2160p"); got.Height != 2160 {
		t.Errorf("exact quality match failed: %+v", got)
	}
	if got, _ := selectRendition(renditions, "h264", "2160"); got.Height != 2160 {
		t.Errorf("numeric quality match failed: %+v", got)
	}
	if got, _ := selectRendition(renditions, "h264", ""); got.Height != 2160 {
		t.Errorf("expected highest h264 (2160), got %+v", got)
	}
	if got, _ := selectRendition(renditions, "av1", ""); got.Height != 2700 {
		t.Errorf("expected best available 2700, got %+v", got)
	}
}

func TestSelectRenditionVRSMashLabels(t *testing.T) {
	renditions := []DownloadRendition{
		{Provider: "vrsmash", Codec: "h264", Quality: "4K", Height: 2160},
		{Provider: "vrsmash", Codec: "h264", Quality: "6K", Height: 3240},
	}
	if got, _ := selectRendition(renditions, "h264", "6K"); got.Height != 3240 {
		t.Errorf("expected 6K, got %+v", got)
	}
	if got, _ := selectRendition(renditions, "", "4K"); got.Quality != "4K" {
		t.Errorf("expected 4K, got %+v", got)
	}
}

func TestHeightFromQuality(t *testing.T) {
	cases := map[string]int{"2160p": 2160, "1080": 1080, "6K": 0, "": 0}
	for in, want := range cases {
		if got := heightFromQuality(in); got != want {
			t.Errorf("heightFromQuality(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestProviderMatching(t *testing.T) {
	cases := []struct {
		scene models.Scene
		want  string
	}{
		{models.Scene{SceneURL: "https://www.sexlikereal.com/scenes/x-1/", SceneID: "slr-1"}, "slr"},
		{models.Scene{SceneURL: "https://www.vrsmash.com/video/x/", SceneID: "vrsmash-abc"}, "vrsmash"},
		{models.Scene{SceneURL: "https://example.com/x", SceneID: "other"}, ""},
	}
	for _, c := range cases {
		p := providerForScene(c.scene)
		got := ""
		if p != nil {
			got = p.ID
		}
		if got != c.want {
			t.Errorf("providerForScene(%s) = %q, want %q", c.scene.SceneID, got, c.want)
		}
	}
}
