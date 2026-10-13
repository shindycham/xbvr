package slr

import (
	"testing"

	"github.com/xbapps/xbvr/pkg/models"
)

func TestParseRenditionsUnescapesAndDeduplicates(t *testing.T) {
	payload := `{
	  "encodings": [
	    {"name":"h264","videoSources":[
	      {"resolution":1080,"url":"https:\/\/cdn-vr.sexlikereal.com\/full_videos_app\/h264\/68823_1080p.mp4?Expires=1\u0026Signature=abc"},
	      {"resolution":2160,"url":"https://cdn-vr.sexlikereal.com/full_videos_app/h264/68823_2160p.mp4?Expires=1%26Signature=def"}
	    ]},
	    {"name":"h265","videoSources":[
	      {"resolution":2160,"url":"https://cdn-vr.sexlikereal.com/full_videos_app/h265/68823_2160p.mp4?Expires=1%26Signature=ghi"}
	    ]}
	  ]
	}`

	got := ParseRenditions(payload)
	if len(got) != 3 {
		t.Fatalf("expected 3 renditions, got %d: %+v", len(got), got)
	}
	// Sorted by height then codec: 1080 h264, 2160 h264, 2160 h265
	if got[0].Codec != "h264" || got[0].Height != 1080 {
		t.Errorf("unexpected first rendition: %+v", got[0])
	}
	if got[1].Codec != "h264" || got[1].Height != 2160 {
		t.Errorf("unexpected second rendition: %+v", got[1])
	}
	if got[2].Codec != "h265" || got[2].Height != 2160 {
		t.Errorf("unexpected third rendition: %+v", got[2])
	}
	if got[0].Width != 2160 {
		t.Errorf("expected side-by-side width 2160, got %d", got[0].Width)
	}
}

func TestParseRenditionsIgnoresTrailerURLs(t *testing.T) {
	payload := `https://cdn-vr.sexlikereal.com/videos_app/h264/68823_720p.mp4?Expires=1`
	if got := ParseRenditions(payload); len(got) != 0 {
		t.Fatalf("expected no renditions from trailer URL, got %+v", got)
	}
}

func TestSelectRendition(t *testing.T) {
	renditions := []Rendition{
		{Codec: "h264", Height: 1080},
		{Codec: "h264", Height: 2160},
		{Codec: "h265", Height: 2700},
	}

	if got, _ := SelectRendition(renditions, "h264", 2160); got.Height != 2160 || got.Codec != "h264" {
		t.Errorf("exact match failed: %+v", got)
	}
	if got, _ := SelectRendition(renditions, "h264", 2500); got.Height != 2160 {
		t.Errorf("expected closest lower quality 2160, got %+v", got)
	}
	if got, _ := SelectRendition(renditions, "h264", 0); got.Height != 2160 {
		t.Errorf("expected best h264 (2160), got %+v", got)
	}
	// Requesting a codec that does not exist must fall back, not fail.
	if got, _ := SelectRendition(renditions, "av1", 0); got.Height != 2700 {
		t.Errorf("expected best available 2700, got %+v", got)
	}
}

func TestGetSceneRef(t *testing.T) {
	cases := []struct {
		sceneID string
		id      string
		project string
	}{
		{"slr-68823", "68823", "1"},
		{"slr-trans-1234", "1234", "3"},
		{"slr-gay-99", "99", "4"},
	}
	for _, c := range cases {
		ref, err := GetSceneRef(models.Scene{SceneID: c.sceneID})
		if err != nil {
			t.Fatalf("%s: unexpected error %v", c.sceneID, err)
		}
		if ref.ID != c.id || ref.Project != c.project {
			t.Errorf("%s: got id=%s project=%s", c.sceneID, ref.ID, ref.Project)
		}
	}
}

func TestGetSceneRefFallsBackToUrl(t *testing.T) {
	ref, err := GetSceneRef(models.Scene{
		SceneID:  "unknown",
		SceneURL: "https://www.sexlikereal.com/scenes/some-title-4242",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.ID != "4242" {
		t.Errorf("expected id 4242, got %s", ref.ID)
	}
}

func TestExpectedFilenameUsesKnownList(t *testing.T) {
	scene := models.Scene{
		SceneID:      "slr-68823",
		FilenamesArr: `["SLR_VRAllure_Unwiring Her Desires_1080p_68823_LR.mp4","SLR_VRAllure_Unwiring Her Desires_2160p_68823_LR.mp4"]`,
	}
	got := ExpectedFilename(scene, 2160, "h264")
	want := "SLR_VRAllure_Unwiring Her Desires_2160p_68823_LR.mp4"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExpectedFilenameFallback(t *testing.T) {
	scene := models.Scene{
		SceneID: "slr-68823",
		Title:   "Unwiring: Her Desires?",
		Site:    "VRAllure (SLR)",
	}
	got := ExpectedFilename(scene, 1080, "h264")
	want := "SLR_VRAllure_Unwiring; Her Desires¿_1080p_68823_LR.mp4"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestParseRenditionsReadsSizes(t *testing.T) {
	// JS object form used by the scene page payload.
	jsPayload := `videoSources:[{resolution:1080,height:1080,width:2160,size:1113056466,url:"https://cdn-vr.sexlikereal.com/full_videos_app/h264/99768_1080p.mp4?Expires=1&Signature=a"},{resolution:2160,height:2160,width:4320,size:2222222222,url:"https://cdn-vr.sexlikereal.com/full_videos_app/h264/99768_2160p.mp4?Expires=1&Signature=b"}]`
	rs := ParseRenditions(jsPayload)
	if len(rs) != 2 {
		t.Fatalf("expected 2 renditions, got %d: %+v", len(rs), rs)
	}
	if rs[0].Height != 1080 || rs[0].Size != 1113056466 || rs[0].Width != 2160 {
		t.Errorf("unexpected 1080 rendition: %+v", rs[0])
	}
	if rs[1].Height != 2160 || rs[1].Size != 2222222222 || rs[1].Width != 4320 {
		t.Errorf("unexpected 2160 rendition: %+v", rs[1])
	}

	// JSON form used by the API payload.
	jsonPayload := `{"resolution":480,"height":480,"width":960,"size":8848917,"url":"https://cdn-vr.sexlikereal.com/full_videos_app/h264/99768_480p.mp4?x=1"}`
	rs2 := ParseRenditions(jsonPayload)
	if len(rs2) != 1 || rs2[0].Size != 8848917 || rs2[0].Width != 960 {
		t.Fatalf("unexpected JSON rendition: %+v", rs2)
	}
}
