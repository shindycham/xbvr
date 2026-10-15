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

func TestFindDuplicateScenesReportsFilesAndCast(t *testing.T) {
	db := openDuplicateTestDB(t)

	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)
	// Equal runtimes and equal casts, which is the whole rule.
	slr := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 30, false)
	alt := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 30, false)
	addCast(t, db, slr, "Jane Doe")
	addCast(t, db, alt, "Jane Doe")

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
func addCast(t *testing.T, db *gorm.DB, scene models.Scene, names ...string) {
	t.Helper()
	for _, name := range names {
		var actor models.Actor
		if err := db.Where(&models.Actor{Name: name}).FirstOrCreate(&actor).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("insert into scene_cast (scene_id, actor_id) values (?, ?)", scene.ID, actor.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestOneSharedPerformerIsNotADuplicate is the regression test for the false
// positives this feature shipped with. Performers appear in many scenes and
// aggregators publish on the same dates, so a single performer in common on the
// same day says almost nothing - and treating it as a duplicate produced pairs
// that were plainly unrelated.
func TestOneSharedPerformerIsNotADuplicate(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	// Same performer, same day, different studios, different titles: two
	// genuinely different scenes.
	a := addScene(t, db, "vrbangers-1", "Midnight Swim", "vrbangers", day, 30, false)
	b := addScene(t, db, "wankzvr-1", "Something Else Entirely", "wankzvr", day, 32, false)
	addCast(t, db, a, "Jane Doe", "Alex Coal")
	addCast(t, db, b, "Jane Doe", "Someone Else")

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("scenes sharing one performer were reported as duplicates: %+v", groups)
	}
}

// TestDuplicateKey pins the rule: the runtime must be identical and the cast must
// be identical. Anything less is not a duplicate.
func TestDuplicateKey(t *testing.T) {
	castA := []string{"Jane Doe", "John Smith"}
	castB := []string{"Jane Doe"}

	keyA, ok := duplicateKey(30, castA)
	if !ok {
		t.Fatal("an exact pair was rejected")
	}
	// Order and case must not matter, or the same cast would look different
	// depending on how the site listed it.
	keyReordered, ok := duplicateKey(30, []string{"JOHN SMITH", "jane doe"})
	if !ok || keyReordered != keyA {
		t.Errorf("cast order or case changed the key: %q vs %q", keyReordered, keyA)
	}

	// A different runtime is not a duplicate.
	if other, ok := duplicateKey(31, castA); !ok || other == keyA {
		t.Error("a one minute difference produced the same key")
	}
	// A different cast is not a duplicate.
	if other, ok := duplicateKey(30, castB); !ok || other == keyA {
		t.Error("a different cast produced the same key")
	}

	// Unknown runtime or unknown cast is not a match: "both unknown" is not
	// evidence of anything.
	if _, ok := duplicateKey(0, castA); ok {
		t.Error("a scene with no runtime was accepted")
	}
	if _, ok := duplicateKey(30, nil); ok {
		t.Error("a scene with no cast was accepted")
	}
	if _, ok := duplicateKey(30, []string{"  ", ""}); ok {
		t.Error("a scene with only blank cast names was accepted")
	}
}

// TestFindDuplicateScenesMatchesExactCastAndLength is the core case: the same
// scene scraped from two sites, with the same runtime and the same cast.
func TestFindDuplicateScenesMatchesExactCastAndLength(t *testing.T) {
	db := openDuplicateTestDB(t)

	a := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC), 30, false)
	b := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC), 30, false)
	addCast(t, db, a, "Jane Doe", "John Smith")
	addCast(t, db, b, "John Smith", "Jane Doe") // different order on purpose

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

// TestADifferentRuntimeIsNotADuplicate covers the half of the rule that used to
// be a tolerance instead of an exact comparison.
func TestADifferentRuntimeIsNotADuplicate(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	a := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 30, false)
	b := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 31, false)
	addCast(t, db, a, "Jane Doe", "John Smith")
	addCast(t, db, b, "Jane Doe", "John Smith")

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("scenes of different lengths were grouped: %+v", groups)
	}
}

// TestADifferentCastIsNotADuplicate covers the other half, including the case
// that caused the worst false positives: the same two performers plus a third
// one is a different scene, not the same one.
func TestADifferentCastIsNotADuplicate(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	a := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", day, 30, false)
	b := addScene(t, db, "vrsmash-1", "Midnight Swim", "vrsmash", day, 30, false)
	addCast(t, db, a, "Jane Doe", "John Smith")
	addCast(t, db, b, "Jane Doe", "John Smith", "Alex Coal")

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("scenes with different casts were grouped: %+v", groups)
	}
}

// TestScenesWithoutCastOrRuntimeAreIgnored guards the case where a missing value
// would otherwise compare equal to another missing value.
func TestScenesWithoutCastOrRuntimeAreIgnored(t *testing.T) {
	db := openDuplicateTestDB(t)
	day := time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC)

	// Two undated-by-cast scenes: no cast rows at all, same runtime.
	addScene(t, db, "slr-1", "First", "slr-originals", day, 30, false)
	addScene(t, db, "vrsmash-1", "Second", "vrsmash", day, 30, false)

	// Two scenes with cast but no runtime.
	c := addScene(t, db, "slr-2", "Third", "slr-originals", day, 0, false)
	d := addScene(t, db, "vrsmash-2", "Fourth", "vrsmash", day, 0, false)
	addCast(t, db, c, "Jane Doe", "John Smith")
	addCast(t, db, d, "Jane Doe", "John Smith")

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Errorf("scenes missing cast or runtime were grouped: %+v", groups)
	}
}

// TestExactMatchIgnoresTitleAndDate makes the change explicit: under the exact
// rule two scenes are the same scene even when the sites disagree about the
// title and the date, because cast and runtime are what decide.
func TestExactMatchIgnoresTitleAndDate(t *testing.T) {
	db := openDuplicateTestDB(t)

	a := addScene(t, db, "slr-1", "Midnight Swim", "slr-originals", time.Date(2023, time.October, 14, 0, 0, 0, 0, time.UTC), 30, false)
	b := addScene(t, db, "vrsmash-1", "Midnight Swim Part One", "vrsmash", time.Date(2020, time.January, 2, 0, 0, 0, 0, time.UTC), 30, false)
	addCast(t, db, a, "Jane Doe", "John Smith")
	addCast(t, db, b, "Jane Doe", "John Smith")

	groups, err := findDuplicateScenes(db, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %+v", len(groups), groups)
	}
}
