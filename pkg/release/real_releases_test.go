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
		if result.Best.Date != 0 || result.Best.Name != 6 {
			t.Errorf("date/name = %d/%d, want 0/6", result.Best.Date, result.Best.Name)
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
