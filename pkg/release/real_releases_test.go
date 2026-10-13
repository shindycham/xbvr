package release

import (
	"strings"
	"testing"
	"time"
)

// The names in this file are real releases, not invented ones. They were the
// cases that showed the parser had two blind spots: a source tag ("SLR") being
// read as a performer, and a scene number being discarded as numeric noise even
// though it is the most identifying token an undated release carries.

func TestParseNameRealReleases(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		siteKey    string
		hasDate    bool
		performers []string
		ids        []string
		resolution int
		quality    string
		projection string
	}{
		{
			name:       "realjamvr, no date in the name",
			raw:        "RealJamVR.In.Your.Space.Megan.Marx.Oculus.8K.mp4",
			siteKey:    "realjamvr",
			hasDate:    false,
			performers: []string{"In", "Your", "Space", "Megan", "Marx"},
			resolution: 4320,
			quality:    "8K",
		},
		{
			name:       "wankitnowvr with an SLR scene number, no date",
			raw:        "WankitnowVR.Baby.Kitten.Cant.Tame.Me.SLR.30605.2880p",
			siteKey:    "wankitnowvr",
			hasDate:    false,
			performers: []string{"Baby", "Kitten", "Cant", "Tame", "Me"},
			ids:        []string{"30605"},
			resolution: 2880,
			quality:    "2880p",
		},
		{
			name:       "a dated version of the same name still parses",
			raw:        "WankitnowVR.23.10.14.Baby.Kitten.Cant.Tame.Me.SLR.30605.2880p",
			siteKey:    "wankitnowvr",
			hasDate:    true,
			performers: []string{"Baby", "Kitten", "Cant", "Tame", "Me"},
			ids:        []string{"30605"},
			resolution: 2880,
			quality:    "2880p",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseName(tt.raw)

			if got.SiteKey != tt.siteKey {
				t.Errorf("SiteKey = %q, want %q", got.SiteKey, tt.siteKey)
			}
			if got.HasDate != tt.hasDate {
				t.Errorf("HasDate = %v, want %v", got.HasDate, tt.hasDate)
			}
			if tt.hasDate && !got.Date.Equal(date(2023, time.October, 14)) {
				t.Errorf("Date = %s, want 2023-10-14", got.Date.Format("2006-01-02"))
			}
			if strings.Join(got.Performers, " ") != strings.Join(tt.performers, " ") {
				t.Errorf("Performers = %v, want %v", got.Performers, tt.performers)
			}
			if strings.Join(got.IDs, " ") != strings.Join(tt.ids, " ") {
				t.Errorf("IDs = %v, want %v", got.IDs, tt.ids)
			}
			if got.Resolution != tt.resolution || got.Quality != tt.quality {
				t.Errorf("resolution/quality = %d/%q, want %d/%q", got.Resolution, got.Quality, tt.resolution, tt.quality)
			}

			// "SLR" names the source, not a performer.
			for _, p := range got.Performers {
				if strings.EqualFold(p, "slr") {
					t.Error("SLR must not be treated as a performer")
				}
			}
		})
	}
}

func TestParseNameDiscardsResolutionAndYearAsIDs(t *testing.T) {
	// A bare resolution or a year is not an identifier.
	for _, raw := range []string{
		"VRBangers.23.10.14.Actor.XXX.1080.mp4",
		"VRBangers.23.10.14.Actor.XXX.2160.mp4",
		"VRBangers.23.10.14.Actor.XXX.2023.mp4",
	} {
		if ids := ParseName(raw).IDs; len(ids) != 0 {
			t.Errorf("ParseName(%q).IDs = %v, want none", raw, ids)
		}
	}

	// A plausible scene number is kept.
	if ids := ParseName("VRBangers.23.10.14.Actor.XXX.30605.2160p").IDs; len(ids) != 1 || ids[0] != "30605" {
		t.Errorf("scene number was not kept: %v", ids)
	}
}

func TestSceneNumbers(t *testing.T) {
	tests := map[string]string{
		"slr-30605":          "30605",
		"slr-gay-30605":      "30605",
		"slr-99768":          "99768",
		"vrbangers-in-space": "",
		"scene-42":           "",
		"":                   "",
		"123":                "123",
	}
	for in, want := range tests {
		if got := SceneNumbers(in); got != want {
			t.Errorf("SceneNumbers(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMatchRealReleases covers the four outcomes that matter for these names.
func TestMatchRealReleases(t *testing.T) {
	realJamRelease := "RealJamVR.In.Your.Space.Megan.Marx.Oculus.8K.mp4"
	wankRelease := "WankitnowVR.Baby.Kitten.Cant.Tame.Me.SLR.30605.2880p"

	t.Run("undated release matched by studio and names", func(t *testing.T) {
		scene := SceneCandidate{
			ID: 1, SceneID: "realjamvr-in-your-space", Title: "In Your Space",
			Site: "RealJamVR", Studio: "RealJamVR", ScraperID: "realjamvr",
			Cast: []string{"Megan Marx"},
		}
		result := Match(ParseName(realJamRelease), []SceneCandidate{scene}, DefaultMatchOptions())
		if !result.Matched() {
			t.Fatalf("not matched: %s (%+v)", result.Reason, result.Best)
		}
		if result.Best.ID != 1 {
			t.Errorf("matched scene %d, want 1", result.Best.ID)
		}
		// No date, so it rests on the names: the cast member is fully matched
		// (+3) and the title is fully present but leaves the performer tokens
		// unexplained (+4).
		if result.Best.Date != 0 || result.Best.Name != 7 {
			t.Errorf("date/name = %d/%d, want 0/7", result.Best.Date, result.Best.Name)
		}
	})

	t.Run("undated SLR release matched by its scene number despite no title overlap", func(t *testing.T) {
		// This is the case the identifier work was for: the scene's title shares
		// no words with the release, so only the scene number can identify it.
		scene := SceneCandidate{
			ID: 2, SceneID: "slr-30605", Title: "A Completely Different Title",
			Site: "SexLikeReal", Studio: "WankItNowVR", ScraperID: "slrstudios",
			Cast: []string{"Nobody In The Release"},
		}
		result := Match(ParseName(wankRelease), []SceneCandidate{scene}, DefaultMatchOptions())
		if !result.Matched() {
			t.Fatalf("the scene number did not carry the match: %s (%+v)", result.Reason, result.Best)
		}
		if result.Best.IDMatch != 8 {
			t.Errorf("id score = %d, want 8", result.Best.IDMatch)
		}
		if !contains(result.Best.Reasons, "scene id") {
			t.Errorf("reasons = %v, want them to include the scene id", result.Best.Reasons)
		}
	})

	t.Run("a release number only matches the scene that owns it", func(t *testing.T) {
		right := SceneCandidate{
			ID: 10, SceneID: "slr-30605", Title: "Right",
			Site: "SexLikeReal", Studio: "WankItNowVR", ScraperID: "slrstudios",
			Cast: []string{"Baby Kitten"},
		}
		neighbour := SceneCandidate{
			ID: 11, SceneID: "slr-30606", Title: "Neighbour",
			Site: "SexLikeReal", Studio: "WankItNowVR", ScraperID: "slrstudios",
			Cast: []string{"Baby Kitten"},
		}
		result := Match(ParseName(wankRelease), []SceneCandidate{neighbour, right}, DefaultMatchOptions())
		if !result.Matched() {
			t.Fatalf("not matched: %s", result.Reason)
		}
		if result.Best.ID != 10 {
			t.Errorf("matched scene %d, want the one owning the number (10)", result.Best.ID)
		}
		if result.Best.Total-result.Runners[1].Total < 8 {
			t.Errorf("the scene number should separate the two clearly: %+v", result.Runners)
		}
	})

	t.Run("the same studio on an unrelated scene is refused", func(t *testing.T) {
		scene := SceneCandidate{
			ID: 3, SceneID: "realjamvr-other", Title: "Something Else Entirely",
			Site: "RealJamVR", Studio: "RealJamVR", ScraperID: "realjamvr",
			Cast: []string{"Other Person"}, Date: date(2023, time.October, 14),
		}
		result := Match(ParseName(realJamRelease), []SceneCandidate{scene}, DefaultMatchOptions())
		if result.Matched() {
			t.Fatalf("an unrelated scene matched: %+v", result.Best)
		}
		if result.Best.Total != 6 {
			t.Errorf("total = %d, want the studio-only score of 6", result.Best.Total)
		}
	})

	t.Run("the wrong studio is refused even with a shared performer", func(t *testing.T) {
		scene := SceneCandidate{
			ID: 4, SceneID: "wankzvr-x", Title: "In Your Space",
			Site: "WankzVR", Studio: "WankzVR", ScraperID: "wankzvr",
			Cast: []string{"Megan Marx"},
		}
		if result := Match(ParseName(realJamRelease), []SceneCandidate{scene}, DefaultMatchOptions()); result.Matched() {
			t.Fatalf("matched across studios: %+v", result.Best)
		}
	})
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestParseNameTrackerPrefixed covers a third naming style: a tracker tag in
// brackets, underscore separators, and no performer at all, so the title is the
// only thing identifying the release.
func TestParseNameTrackerPrefixed(t *testing.T) {
	for _, raw := range []string{
		"[xxxtor.com]VRLatina_Up_All_Day_4096p_UHD_180_LR",
		"[xxxtor.com]VRLatina_Up_All_Day_4096p_UHD_180_LR.mp4",
		"VRLatina_Up_All_Day_4096p_UHD_180_LR",
	} {
		got := ParseName(raw)
		if got.SiteKey != "vrlatina" {
			t.Errorf("ParseName(%q).SiteKey = %q, want vrlatina", raw, got.SiteKey)
		}
		if got.HasDate {
			t.Errorf("ParseName(%q) invented a date: %s", raw, got.Date.Format("2006-01-02"))
		}
		if strings.Join(got.Performers, " ") != "Up All Day" {
			t.Errorf("ParseName(%q).Performers = %v, want [Up All Day]", raw, got.Performers)
		}
		if got.Resolution != 4096 || got.Quality != "4096p" {
			t.Errorf("ParseName(%q) resolution/quality = %d/%q", raw, got.Resolution, got.Quality)
		}
		if got.Projection != "180" {
			t.Errorf("ParseName(%q).Projection = %q, want 180", raw, got.Projection)
		}
	}
}

// TestMatchTitleOnlyUndatedRelease covers the case the tracker-prefixed name
// exposed: no date and no performer, so the title has to carry the match. It
// also pins the rule that a weak runner-up cannot make a good match look
// ambiguous.
func TestMatchTitleOnlyUndatedRelease(t *testing.T) {
	rel := ParseName("[xxxtor.com]VRLatina_Up_All_Day_4096p_UHD_180_LR")

	target := SceneCandidate{
		ID: 1, SceneID: "slr-90001", Title: "Up All Day",
		Site: "VRLatina", Studio: "VRLatina", ScraperID: "vrlatina",
		Cast: []string{"Someone Not Named In The Release"},
	}

	result := Match(rel, []SceneCandidate{target}, DefaultMatchOptions())
	if !result.Matched() {
		t.Fatalf("a title-only undated release did not match its own scene: %s (%+v)", result.Reason, result.Best)
	}
	if !contains(result.Best.Reasons, "title") {
		t.Errorf("reasons = %v, want them to credit the title", result.Best.Reasons)
	}
	if contains(result.Best.Reasons, "performers") {
		t.Errorf("reasons = %v, but no performer appears in this release", result.Best.Reasons)
	}

	t.Run("a partial title overlap does not create ambiguity", func(t *testing.T) {
		// "Day Off" shares one word. It fails the acceptance gate on its own, so
		// it must not be treated as a rival worth hesitating over.
		decoy := SceneCandidate{
			ID: 2, SceneID: "slr-90002", Title: "Day Off",
			Site: "VRLatina", Studio: "VRLatina", ScraperID: "vrlatina",
			Cast: []string{"Someone Else"},
		}
		result := Match(rel, []SceneCandidate{decoy, target}, DefaultMatchOptions())
		if !result.Matched() {
			t.Fatalf("a non-viable runner-up blocked the match: %s (%+v)", result.Reason, result.Runners)
		}
		if result.Ambiguous {
			t.Error("a runner-up that cannot be accepted alone must not create ambiguity")
		}
		if result.Best.ID != 1 {
			t.Errorf("matched scene %d, want 1", result.Best.ID)
		}
	})

	t.Run("a scene from the same studio with a different title is refused", func(t *testing.T) {
		other := SceneCandidate{
			ID: 3, SceneID: "slr-90003", Title: "Tan Lines Are Fine",
			Site: "VRLatina", Studio: "VRLatina", ScraperID: "vrlatina",
			Cast: []string{"Lele Mantilla"},
		}
		if result := Match(rel, []SceneCandidate{other}, DefaultMatchOptions()); result.Matched() {
			t.Fatalf("matched an unrelated scene: %+v", result.Best)
		}
	})
}
