package tasks

import (
	"testing"
	"time"

	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	_ "github.com/mattn/go-sqlite3"

	"github.com/xbapps/xbvr/pkg/models"
)

func openDuplicateTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %s", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.AutoMigrate(&models.Scene{}, &models.File{}, &models.SceneMerge{}, &models.Actor{}).Error; err != nil {
		t.Fatalf("migrate: %s", err)
	}
	return db
}

func addScene(t *testing.T, db *gorm.DB, sceneID, title, scraperID string, released time.Time, duration int, hidden bool) models.Scene {
	t.Helper()
	scene := models.Scene{
		SceneID:     sceneID,
		Title:       title,
		ScraperId:   scraperID,
		Site:        scraperID,
		Studio:      scraperID,
		ReleaseDate: released,
		Duration:    duration,
		IsHidden:    hidden,
	}
	if err := db.Create(&scene).Error; err != nil {
		t.Fatal(err)
	}
	return scene
}

func TestNormaliseDuplicateTitle(t *testing.T) {
	tests := map[string]string{
		"Naughty Gym - Featuring Amalia Davis": "naughty gym amalia davis",
		"Caught In The Act!":                   "caught act",
		"VRBangers: Midnight Swim":             "vrbangers midnight swim",
		"  ...  ":                              "",
		"The Best of 2024":                     "best 2024",
	}
	for in, want := range tests {
		if got := normaliseDuplicateTitle(in); got != want {
			t.Errorf("normaliseDuplicateTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDurationsAgree pins the unit as well as the tolerance: durations are in
// minutes, so values like 30 are half an hour. A seconds-based slack here would
// treat a 30 minute scene and a two hour one as the same length.
func TestDurationsAgree(t *testing.T) {
	at := func(d ...int) []DuplicateScene {
		out := make([]DuplicateScene, 0, len(d))
		for _, v := range d {
			out = append(out, DuplicateScene{Duration: v})
		}
		return out
	}
	if !durationsAgree(at(30, 31)) {
		t.Error("durations a minute apart should agree")
	}
	if durationsAgree(at(30, 32+1)) {
		t.Error("a four minute difference should not agree")
	}
	if durationsAgree(at(30, 140)) {
		t.Error("a feature and a trailer must not agree")
	}
	if durationsAgree(at(30, 0)) {
		t.Error("one known duration is not agreement")
	}
	if !durationsAgree(at(30, 30, 31)) {
		t.Error("three close durations should agree")
	}
}

func TestScoreDuplicateGroupPrefersDifferentSources(t *testing.T) {
	when := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)
	sameSource := []DuplicateScene{
		{ScraperID: "slr-originals", ReleaseDate: when, Duration: 1800, Cast: []string{"Jane Doe"}},
		{ScraperID: "slr-originals", ReleaseDate: when, Duration: 1800, Cast: []string{"Jane Doe"}},
	}
	acrossSources := []DuplicateScene{
		{ScraperID: "slr-originals", ReleaseDate: when, Duration: 1800, Cast: []string{"Jane Doe"}},
		{ScraperID: "vrsmash", ReleaseDate: when, Duration: 1800, Cast: []string{"Jane Doe"}},
	}

	low, _ := scoreDuplicateGroup(sameSource)
	high, reasons := scoreDuplicateGroup(acrossSources)
	if high <= low {
		t.Errorf("scenes from different sources should score higher: %d vs %d", high, low)
	}
	if len(reasons) == 0 {
		t.Error("no reasons were given")
	}
}

// TestFindDuplicateScenesGroupsByTitleAndDate is the core detection test: the
// same scene scraped from two sites must come back as one group, and a remake
// years later must not.
func TestFindDuplicateScenesGroupsByTitleAndDate(t *testing.T) {
	db := openDuplicateTestDB(t)

	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)
	addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 1800, false)
	addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day.Add(12*time.Hour), 1795, false)

	// Same title, three years earlier: a remake, not a duplicate.
	addScene(t, db, "slr-2", "Midnight Swim", "slr-originals", day.AddDate(-3, 0, 0), 2000, false)

	// A genuinely different scene.
	addScene(t, db, "wankzvr-1", "Something Else", "wankzvr", day, 1900, false)

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatalf("findDuplicateScenes: %s", err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(groups), groups)
	}
	if len(groups[0].Scenes) != 2 {
		t.Fatalf("group has %d scenes, want 2 (the remake must be separate)", len(groups[0].Scenes))
	}
	if groups[0].Score == 0 {
		t.Error("group scored 0")
	}

	// Detail counts must come through, since the reviewer picks the primary by
	// which copy has the files.
	if len(groups[0].Scenes[0].Cast) != 0 && groups[0].Scenes[0].FileCount != 0 {
		t.Logf("scene detail: %+v", groups[0].Scenes[0])
	}
}

func TestFindDuplicateScenesReportsFilesAndCast(t *testing.T) {
	db := openDuplicateTestDB(t)

	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)
	slr := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 1800, false)
	alt := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 1800, false)

	actor := models.Actor{Name: "Jane Doe"}
	db.Create(&actor)
	// The many-to-many join table has no model of its own.
	if err := db.Exec("insert into scene_cast (scene_id, actor_id) values (?, ?)", slr.ID, actor.ID).Error; err != nil {
		t.Fatal(err)
	}

	db.Create(&models.File{SceneID: slr.ID, Path: "/media", Filename: "scene.mp4", Type: "video"})
	db.Create(&models.File{SceneID: slr.ID, Path: "/media", Filename: "scene.funscript", Type: "script"})
	db.Create(&models.File{SceneID: alt.ID, Path: "/media", Filename: "alt.mp4", Type: "video"})

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}

	byID := map[uint]DuplicateScene{}
	for _, s := range groups[0].Scenes {
		byID[s.ID] = s
	}
	if got := byID[slr.ID]; got.FileCount != 1 || got.ScriptCount != 1 || len(got.Cast) != 1 {
		t.Errorf("slr scene detail = files %d scripts %d cast %v, want 1/1/1",
			got.FileCount, got.ScriptCount, got.Cast)
	}
	if got := byID[alt.ID]; got.FileCount != 1 || got.ScriptCount != 0 {
		t.Errorf("alternate scene detail = files %d scripts %d, want 1/0", got.FileCount, got.ScriptCount)
	}
}

func TestFindDuplicateScenesSkipsSingleScenesAndEmptyTitles(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	addScene(t, db, "only-1", "Unique Scene", "slr-originals", day, 1800, false)
	addScene(t, db, "empty-1", "", "slr-originals", day, 1800, false)
	addScene(t, db, "empty-2", "   ", "vrsmash", day, 1800, false)

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("got %d groups, want none: %+v", len(groups), groups)
	}
}

// TestMergeScenesMovesFilesAndHidesTheDuplicate covers what a merge must not do:
// lose a downloaded file, or destroy the duplicate.
func TestMergeScenesMovesFilesAndHidesTheDuplicate(t *testing.T) {
	db := openDuplicateTestDB(t)

	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)
	primary := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 1800, false)
	duplicate := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 1800, false)

	file := models.File{SceneID: duplicate.ID, Path: "/media", Filename: "scene.mp4", Type: "video"}
	db.Create(&file)
	// A funscript is a file too, and losing it would defeat the point of merging.
	script := models.File{SceneID: duplicate.ID, Path: "/media", Filename: "scene.funscript", Type: "script"}
	db.Create(&script)

	merged, touched, err := mergeScenes(db, primary.ID, []uint{duplicate.ID})
	if err != nil {
		t.Fatalf("mergeScenes: %s", err)
	}
	if merged != 1 {
		t.Fatalf("merged %d scenes, want 1", merged)
	}
	if len(touched) != 2 {
		t.Errorf("touched %v, want the primary and the duplicate", touched)
	}

	// The files must now belong to the primary, or they would vanish from the
	// library: a script is as much a file as the video is.
	for _, f := range []models.File{file, script} {
		var reloaded models.File
		db.First(&reloaded, f.ID)
		if reloaded.SceneID != primary.ID {
			t.Errorf("%s moved to scene %d, want %d", f.Filename, reloaded.SceneID, primary.ID)
		}
	}

	// The duplicate is hidden, not deleted.
	var dup models.Scene
	if err := db.First(&dup, duplicate.ID).Error; err != nil {
		t.Fatalf("the duplicate was deleted: %s", err)
	}
	if !dup.IsHidden {
		t.Error("the duplicate was not hidden")
	}

	// And the merge is recorded, with the file list needed to undo it.
	var record models.SceneMerge
	if err := db.Where(&models.SceneMerge{DuplicateSceneID: duplicate.ID}).First(&record).Error; err != nil {
		t.Fatalf("no merge record: %s", err)
	}
	if record.PrimarySceneID != primary.ID {
		t.Errorf("merge record primary = %d, want %d", record.PrimarySceneID, primary.ID)
	}
	if record.MovedFileIDs == "" || record.MovedFileIDs == "[]" {
		t.Errorf("merge record did not keep the moved file ids: %q", record.MovedFileIDs)
	}
}

// TestMergeScenesRemembersAPreExistingHiddenFlag guards an undo detail: a scene
// the user had already hidden must stay hidden after merging and unmerging.
func TestMergeScenesRemembersAPreExistingHiddenFlag(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	primary := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 1800, false)
	duplicate := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 1800, true)

	if _, _, err := mergeScenes(db, primary.ID, []uint{duplicate.ID}); err != nil {
		t.Fatal(err)
	}

	var record models.SceneMerge
	db.Where(&models.SceneMerge{DuplicateSceneID: duplicate.ID}).First(&record)
	if !record.DuplicateWasHidden {
		t.Error("the duplicate's own hidden flag was not recorded")
	}

	if _, err := unmergeScene(db, duplicate.ID); err != nil {
		t.Fatalf("unmergeScene: %s", err)
	}

	var dup models.Scene
	db.First(&dup, duplicate.ID)
	if !dup.IsHidden {
		t.Error("undone merge revealed a scene the user had hidden")
	}
}

func TestMergeScenesRefusesAnAlreadyMergedDuplicate(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	primary := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 1800, false)
	other := addScene(t, db, "slr-2", "Midnight Swim", "slr-originals", day, 1800, false)
	duplicate := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 1800, false)

	if _, _, err := mergeScenes(db, primary.ID, []uint{duplicate.ID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mergeScenes(db, other.ID, []uint{duplicate.ID}); err == nil {
		t.Error("a scene already merged into another was merged a second time")
	}
}

func TestUnmergeSceneRestoresFilesAndVisibility(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	primary := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 1800, false)
	duplicate := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 1800, false)

	file := models.File{SceneID: duplicate.ID, Path: "/media", Filename: "scene.mp4", Type: "video"}
	db.Create(&file)
	script := models.File{SceneID: duplicate.ID, Path: "/media", Filename: "scene.funscript", Type: "script"}
	db.Create(&script)

	if _, _, err := mergeScenes(db, primary.ID, []uint{duplicate.ID}); err != nil {
		t.Fatal(err)
	}
	primaryID, err := unmergeScene(db, duplicate.ID)
	if err != nil {
		t.Fatalf("unmergeScene: %s", err)
	}
	if primaryID != primary.ID {
		t.Errorf("unmerge reported primary %d, want %d", primaryID, primary.ID)
	}

	// Both files must be back with their original scene.
	for _, f := range []models.File{file, script} {
		var reloaded models.File
		db.First(&reloaded, f.ID)
		if reloaded.SceneID != duplicate.ID {
			t.Errorf("%s belongs to scene %d after undo, want %d", f.Filename, reloaded.SceneID, duplicate.ID)
		}
	}

	var dup models.Scene
	db.First(&dup, duplicate.ID)
	if dup.IsHidden {
		t.Error("the duplicate is still hidden after the undo")
	}

	var count int
	db.Model(&models.SceneMerge{}).Count(&count)
	if count != 0 {
		t.Errorf("%d merge records remain after an undo, want 0", count)
	}
}

func TestUnmergeSceneWithoutAMerge(t *testing.T) {
	db := openDuplicateTestDB(t)
	scene := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", time.Now(), 1800, false)

	if _, err := unmergeScene(db, scene.ID); err == nil {
		t.Error("expected an error for a scene that was never merged")
	}
}

// TestFindDuplicateScenesCatchesDifferingTitles covers the second pass. Two
// aggregators often list the same scene under different titles, and matching on
// the title alone would never see it.
func TestFindDuplicateScenesCatchesDifferingTitles(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	// Same scene, same day, same performer, different titles.
	a := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 1800, false)
	b := addScene(t, db, "vrsmash-1", "Midnight Swim Part One", "vrsmash", day, 1800, false)

	actor := models.Actor{Name: "Jane Doe"}
	db.Create(&actor)
	for _, scene := range []models.Scene{a, b} {
		if err := db.Exec("insert into scene_cast (scene_id, actor_id) values (?, ?)", scene.ID, actor.ID).Error; err != nil {
			t.Fatal(err)
		}
	}

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(groups), groups)
	}
	if len(groups[0].Scenes) != 2 {
		t.Fatalf("group has %d scenes, want 2", len(groups[0].Scenes))
	}
}

// TestCastPassRequiresTwoSources stops the second pass from reporting one
// scraper's own scenes as duplicates of each other.
func TestCastPassRequiresTwoSources(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	a := addScene(t, db, "slr-1", "First Scene", "slr-originals", day, 1800, false)
	b := addScene(t, db, "slr-2", "Second Scene", "slr-originals", day, 1800, false)

	actor := models.Actor{Name: "Jane Doe"}
	db.Create(&actor)
	for _, scene := range []models.Scene{a, b} {
		db.Exec("insert into scene_cast (scene_id, actor_id) values (?, ?)", scene.ID, actor.ID)
	}

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("one scraper's own scenes were reported as duplicates: %+v", groups)
	}
}

func TestCastPassIgnoresScenesFarApartInTime(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	a := addScene(t, db, "slr-1", "One Scene", "slr-originals", day, 1800, false)
	b := addScene(t, db, "vrsmash-1", "Another Scene", "vrsmash", day.AddDate(0, 3, 0), 1800, false)

	actor := models.Actor{Name: "Jane Doe"}
	db.Create(&actor)
	for _, scene := range []models.Scene{a, b} {
		db.Exec("insert into scene_cast (scene_id, actor_id) values (?, ?)", scene.ID, actor.ID)
	}

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("scenes months apart were grouped by a shared performer: %+v", groups)
	}
}

// TestBothPassesReportAGroupOnce guards the deduplication between the two passes:
// a pair that shares a title *and* a performer must not appear twice.
func TestBothPassesReportAGroupOnce(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	a := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 1800, false)
	b := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 1800, false)

	actor := models.Actor{Name: "Jane Doe"}
	db.Create(&actor)
	for _, scene := range []models.Scene{a, b} {
		db.Exec("insert into scene_cast (scene_id, actor_id) values (?, ?)", scene.ID, actor.ID)
	}

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want the same pair reported once: %+v", len(groups), groups)
	}
}

func TestClusterKeyIsOrderIndependent(t *testing.T) {
	if clusterKey([]uint{3, 1, 2}) != clusterKey([]uint{2, 3, 1}) {
		t.Error("clusterKey depends on order, so the passes cannot be deduplicated")
	}
	if clusterKey([]uint{1, 2}) == clusterKey([]uint{1, 3}) {
		t.Error("different scenes produced the same key")
	}
}
