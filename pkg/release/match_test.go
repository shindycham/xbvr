package release

import (
	"testing"
	"time"
)

func scene(id uint, sceneID, site, studio, scraperID, title string, cast []string, d time.Time) SceneCandidate {
	return SceneCandidate{
		ID:        id,
		SceneID:   sceneID,
		Site:      site,
		Studio:    studio,
		ScraperID: scraperID,
		Title:     title,
		Cast:      cast,
		Date:      d,
	}
}

func TestMatchExactStudioAndDate(t *testing.T) {
	rel := ParseName("VRBangers.23.10.14.Jane.Doe.XXX.2160p.MP4-WRB")

	scenes := []SceneCandidate{
		scene(1, "scene-b", "WankzVR", "WankzVR", "wankzvr", "Other", []string{"Someone Else"}, date(2023, time.October, 14)),
		scene(2, "scene-a", "VRBangers", "VRBangers", "vrbangers", "Hot Scene", []string{"Jane Doe"}, date(2023, time.October, 14)),
	}

	result := Match(rel, scenes, DefaultMatchOptions())
	if !result.Matched() {
		t.Fatalf("expected a match, got reason %q (runners %+v)", result.Reason, result.Runners)
	}
	if result.Best.ID != 2 {
		t.Errorf("matched scene %d, want 2", result.Best.ID)
	}
	if result.Best.Site != 6 || result.Best.Date != 5 {
		t.Errorf("site/date score = %d/%d, want 6/5", result.Best.Site, result.Best.Date)
	}
	if result.Best.Name != 3 {
		t.Errorf("name score = %d, want 3", result.Best.Name)
	}
}

func TestMatchStudioIsFoundInStudioOrScraperID(t *testing.T) {
	// An SLR-aggregated scene names the aggregator as its site but the actual
	// studio as its studio; the release names the studio.
	rel := ParseName("BadoinkVR.23.05.22.Alex.Coal.XXX.8K.HEVC-GROUP")
	scenes := []SceneCandidate{
		scene(7, "slr-123", "SexLikeReal", "BadoinkVR", "slrstudios", "Some Title", []string{"Alex Coal"}, date(2023, time.May, 22)),
	}

	result := Match(rel, scenes, DefaultMatchOptions())
	if !result.Matched() {
		t.Fatalf("expected a match via Studio, got %q", result.Reason)
	}
	if result.Best.ID != 7 {
		t.Errorf("matched scene %d, want 7", result.Best.ID)
	}
}

func TestMatchRejectsMissingStudioMatch(t *testing.T) {
	// Same date, same performer, different studio: must not match.
	rel := ParseName("VRBangers.23.10.14.Jane.Doe.XXX.2160p")
	scenes := []SceneCandidate{
		scene(1, "scene-a", "WankzVR", "WankzVR", "wankzvr", "Hot Scene", []string{"Jane Doe"}, date(2023, time.October, 14)),
	}

	result := Match(rel, scenes, DefaultMatchOptions())
	if result.Matched() {
		t.Fatalf("matched across studios: %+v", result.Best)
	}
	if len(result.Runners) != 0 {
		t.Errorf("expected no runners, got %+v", result.Runners)
	}
}

func TestMatchAmbiguousIsRejected(t *testing.T) {
	// Two scenes from the same studio on the same day with no distinguishing
	// cast or title: attaching the file to either would be a coin flip.
	rel := ParseName("VRBangers.23.10.14.Someone.Unknown.XXX.2160p")
	scenes := []SceneCandidate{
		scene(1, "scene-a", "VRBangers", "VRBangers", "vrbangers", "First", []string{"Alpha Beta"}, date(2023, time.October, 14)),
		scene(2, "scene-b", "VRBangers", "VRBangers", "vrbangers", "Second", []string{"Gamma Delta"}, date(2023, time.October, 14)),
	}

	result := Match(rel, scenes, DefaultMatchOptions())
	if result.Matched() {
		t.Fatalf("expected ambiguity, matched %+v", result.Best)
	}
	if !result.Ambiguous {
		t.Errorf("expected Ambiguous, got reason %q", result.Reason)
	}
}

func TestMatchAmbiguityBrokenByCast(t *testing.T) {
	rel := ParseName("VRBangers.23.10.14.Jane.Doe.XXX.2160p")
	scenes := []SceneCandidate{
		scene(1, "scene-a", "VRBangers", "VRBangers", "vrbangers", "First", []string{"Alpha Beta"}, date(2023, time.October, 14)),
		scene(2, "scene-b", "VRBangers", "VRBangers", "vrbangers", "Second", []string{"Jane Doe"}, date(2023, time.October, 14)),
	}

	result := Match(rel, scenes, DefaultMatchOptions())
	if !result.Matched() {
		t.Fatalf("expected the cast to break the tie, got %q", result.Reason)
	}
	if result.Best.ID != 2 {
		t.Errorf("matched scene %d, want 2", result.Best.ID)
	}
}

func TestMatchDateOffByOneNeedsCorroboration(t *testing.T) {
	// Studio + a date one day out is not enough on its own...
	rel := ParseName("VRBangers.23.10.15.Someone.Unknown.XXX.2160p")
	scenes := []SceneCandidate{
		scene(1, "scene-a", "VRBangers", "VRBangers", "vrbangers", "Some Title", nil, date(2023, time.October, 14)),
	}
	if result := Match(rel, scenes, DefaultMatchOptions()); result.Matched() {
		t.Errorf("studio plus a near date should not be conclusive: %+v", result.Best)
	}

	// ...but with the performer confirmed it is.
	rel = ParseName("VRBangers.23.10.15.Jane.Doe.XXX.2160p")
	scenes = []SceneCandidate{
		scene(1, "scene-a", "VRBangers", "VRBangers", "vrbangers", "Some Title", []string{"Jane Doe"}, date(2023, time.October, 14)),
	}
	result := Match(rel, scenes, DefaultMatchOptions())
	if !result.Matched() {
		t.Fatalf("expected a match, got %q", result.Reason)
	}
	if result.Best.Date != 3 {
		t.Errorf("date score = %d, want 3", result.Best.Date)
	}
}

func TestMatchDatelessReleaseNeedsTwoPerformers(t *testing.T) {
	rel := ParseName("VRBangers.Jane.Doe.XXX.2160p")
	scenes := []SceneCandidate{
		scene(1, "scene-a", "VRBangers", "VRBangers", "vrbangers", "Some Title", []string{"Jane Doe"}, time.Time{}),
	}
	result := Match(rel, scenes, DefaultMatchOptions())
	if result.Matched() {
		t.Errorf("a dateless release with one performer should not be conclusive: %+v", result.Best)
	}

	rel = ParseName("VRBangers.Jane.Doe.And.John.Smith.XXX.2160p")
	scenes = []SceneCandidate{
		scene(1, "scene-a", "VRBangers", "VRBangers", "vrbangers", "Some Title", []string{"Jane Doe", "John Smith"}, time.Time{}),
	}
	result = Match(rel, scenes, DefaultMatchOptions())
	if !result.Matched() {
		t.Fatalf("expected two performers to carry the match, got %q", result.Reason)
	}
}

func TestMatchByTitle(t *testing.T) {
	// Releases that name the scene rather than the cast still match on the
	// title when the studio and date line up.
	rel := ParseName("VRBangers.23.10.14.Midnight.Swim.XXX.2160p")
	scenes := []SceneCandidate{
		scene(1, "scene-a", "VRBangers", "VRBangers", "vrbangers", "Midnight Swim", nil, date(2023, time.October, 14)),
	}
	result := Match(rel, scenes, DefaultMatchOptions())
	if !result.Matched() {
		t.Fatalf("expected a title match, got %q", result.Reason)
	}
	// The title is fully present AND accounts for every name token in the
	// release, which is the strongest form of title match and scores the same as
	// a confirmed cast member.
	if result.Best.Name != 6 {
		t.Errorf("name score = %d, want 6", result.Best.Name)
	}
}

func TestMatchIgnoresUnrelatedScenes(t *testing.T) {
	rel := ParseName("VRBangers.23.10.14.Jane.Doe.XXX.2160p")
	scenes := []SceneCandidate{
		scene(1, "scene-a", "VRHush", "VRHush", "vrhush", "Something", []string{"Other Person"}, date(2022, time.January, 1)),
		scene(2, "scene-b", "POVR", "POVR", "povr", "Else", []string{"Nobody"}, date(2021, time.March, 3)),
	}
	result := Match(rel, scenes, DefaultMatchOptions())
	if result.Matched() {
		t.Errorf("unrelated scenes matched: %+v", result.Best)
	}
}

func TestMatchOptionsWindow(t *testing.T) {
	rel := ParseName("VRBangers.23.10.20.Jane.Doe.XXX.2160p")
	scenes := []SceneCandidate{
		scene(1, "scene-a", "VRBangers", "VRBangers", "vrbangers", "Some Title", []string{"Jane Doe"}, date(2023, time.October, 14)),
	}

	// Six days apart is inside the default window, so the date still scores 1.
	// 6 + 1 + 3 = 10, exactly the threshold.
	if result := Match(rel, scenes, DefaultMatchOptions()); !result.Matched() {
		t.Errorf("expected a match inside the date window, got %q (%+v)", result.Reason, result.Best)
	}

	// The same release with the window closed scores 6 + 0 + 3 = 9 and fails.
	strict := DefaultMatchOptions()
	strict.DateWindow = 1
	if result := Match(rel, scenes, strict); result.Matched() {
		t.Errorf("expected no match outside the date window, got %+v", result.Best)
	}
}

func TestMatchEmptyInputs(t *testing.T) {
	if result := Match(ParseName(""), nil, DefaultMatchOptions()); result.Matched() {
		t.Error("empty release matched")
	}
	rel := ParseName("VRBangers.23.10.14.Jane.Doe.XXX.2160p")
	if result := Match(rel, nil, DefaultMatchOptions()); result.Matched() {
		t.Error("empty scene list matched")
	}
}

func TestSignificantTokens(t *testing.T) {
	got := significantTokens("Jane Doe and the City")
	want := []string{"jane", "doe", "city"}
	if len(got) != len(want) {
		t.Fatalf("significantTokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("significantTokens = %v, want %v", got, want)
		}
	}
}
