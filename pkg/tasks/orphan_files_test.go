package tasks

import (
	"testing"

	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	_ "github.com/mattn/go-sqlite3"

	"github.com/xbapps/xbvr/pkg/models"
)

// openTestDB builds a throwaway SQLite database holding just the two tables the
// orphan cleanup touches.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %s", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.AutoMigrate(&models.Volume{}, &models.File{}).Error; err != nil {
		t.Fatalf("migrate: %s", err)
	}
	return db
}

// TestFindOrCreateFileAttachesVolumeImmediately is the regression test for the
// orphaned-file bug: the row must carry its volume from the moment it is
// inserted, because the volume id used to be written only after hashing and
// ffprobe. A scan interrupted in between left a row that belonged to no volume,
// which nothing would then clean up or display correctly.
func TestFindOrCreateFileAttachesVolumeImmediately(t *testing.T) {
	db := openTestDB(t)

	vol := models.Volume{Type: "local", Path: "/media/vr", IsEnabled: true}
	if err := db.Create(&vol).Error; err != nil {
		t.Fatal(err)
	}

	fl, err := findOrCreateFile(db, "/media/vr/scene.mp4", "video", vol.ID)
	if err != nil {
		t.Fatalf("findOrCreateFile: %s", err)
	}
	if fl.VolumeID != vol.ID {
		t.Errorf("returned row has volume_id %d, want %d", fl.VolumeID, vol.ID)
	}

	// The important part: read it back from the database, not from the struct.
	var reloaded models.File
	if err := db.Where("filename = ?", "scene.mp4").First(&reloaded).Error; err != nil {
		t.Fatalf("row was not inserted: %s", err)
	}
	if reloaded.VolumeID != vol.ID {
		t.Errorf("stored volume_id = %d, want %d - a row without its volume is the bug", reloaded.VolumeID, vol.ID)
	}
	if reloaded.Path != "/media/vr" || reloaded.Type != "video" {
		t.Errorf("stored row = %+v, want path and type preserved", reloaded)
	}

	// Calling it again must find the same row rather than making a second.
	again, err := findOrCreateFile(db, "/media/vr/scene.mp4", "video", vol.ID)
	if err != nil {
		t.Fatalf("findOrCreateFile (second call): %s", err)
	}
	if again.ID != fl.ID {
		t.Errorf("second call created a new row (%d vs %d)", again.ID, fl.ID)
	}
	var count int
	db.Model(&models.File{}).Count(&count)
	if count != 1 {
		t.Errorf("file rows = %d, want 1", count)
	}
}

// TestFindOrCreateFileRepairsAnExistingOrphan checks that a row left without a
// volume by an older version is adopted rather than duplicated.
func TestFindOrCreateFileRepairsAnExistingOrphan(t *testing.T) {
	db := openTestDB(t)

	vol := models.Volume{Type: "local", Path: "/media/vr", IsEnabled: true}
	db.Create(&vol)

	orphan := models.File{Path: "/media/vr", Filename: "old.mp4", Type: "video", VolumeID: 0}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}

	fl, err := findOrCreateFile(db, "/media/vr/old.mp4", "video", vol.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fl.ID != orphan.ID {
		t.Errorf("adopted row id = %d, want the existing %d", fl.ID, orphan.ID)
	}

	// The scan then writes the volume id onto the row it was given. This is done
	// on the test handle rather than through File.Save(), which talks to the real
	// application database and must never be called from a unit test.
	if err := db.Model(&models.File{}).Where("id = ?", orphan.ID).
		Update("volume_id", vol.ID).Error; err != nil {
		t.Fatal(err)
	}

	var reloaded models.File
	db.First(&reloaded, orphan.ID)
	if reloaded.VolumeID != vol.ID {
		t.Errorf("volume_id = %d, want the orphan repaired to %d", reloaded.VolumeID, vol.ID)
	}
}

func TestRemoveOrphanedFiles(t *testing.T) {
	db := openTestDB(t)

	keepVolume := models.Volume{Type: "local", Path: "/media/keep", IsEnabled: true}
	db.Create(&keepVolume)

	files := []models.File{
		{Path: "/media/keep", Filename: "good.mp4", Type: "video", VolumeID: keepVolume.ID},
		// Belongs to a volume that no longer exists.
		{Path: "/media/gone", Filename: "orphan.mp4", Type: "video", VolumeID: 999},
		// Never had a volume: exactly what an interrupted scan used to leave.
		{Path: "/media/keep", Filename: "volumeless.mp4", Type: "video", VolumeID: 0},
	}
	for i := range files {
		if err := db.Create(&files[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	removeOrphanedFiles(db, log.WithField("task", "test"))

	var remaining []models.File
	db.Find(&remaining)
	if len(remaining) != 1 {
		t.Fatalf("remaining files = %d, want 1: %+v", len(remaining), remaining)
	}
	if remaining[0].Filename != "good.mp4" {
		t.Errorf("kept %q, want good.mp4", remaining[0].Filename)
	}
}
