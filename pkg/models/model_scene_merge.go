package models

import "time"

// SceneMerge records that one scene duplicates another, and that the duplicate
// has been merged into it.
//
// A merge hides the duplicate rather than deleting it, and remembers enough to
// put everything back: which files were handed to the primary, and whether the
// duplicate had been hidden by the user beforehand. Without that last detail an
// undo would reveal a scene the user had deliberately hidden.
//
// The duplicate keeps its row, its metadata and its identifier, so the merge can
// be reversed years later and nothing is lost in the meantime.
type SceneMerge struct {
	ID        uint      `gorm:"primary_key" json:"id"`
	CreatedAt time.Time `json:"created_at"`

	// PrimarySceneID is the scene that was kept.
	PrimarySceneID uint `gorm:"index" json:"primary_scene_id"`
	// DuplicateSceneID is the scene that was hidden. Unique, because a scene can
	// only have been merged into one other scene.
	DuplicateSceneID uint `gorm:"unique_index" json:"duplicate_scene_id"`

	// DuplicateWasHidden is the duplicate's own hidden flag before the merge.
	DuplicateWasHidden bool `json:"duplicate_was_hidden"`
	// MovedFileIDs is a JSON array of the file rows re-pointed at the primary,
	// so an undo can give them back.
	MovedFileIDs string `json:"moved_file_ids"`
}

func (o *SceneMerge) Save() error {
	db, _ := GetDB()
	defer db.Close()
	return db.Save(o).Error
}

// Delete removes the merge record, which is what completes an undo.
func (o *SceneMerge) Delete() error {
	db, _ := GetDB()
	defer db.Close()
	return db.Delete(o).Error
}

// GetMergeForDuplicate finds the merge a scene is the duplicate side of.
func GetMergeForDuplicate(sceneID uint) (SceneMerge, error) {
	db, _ := GetDB()
	defer db.Close()

	var merge SceneMerge
	err := db.Where(&SceneMerge{DuplicateSceneID: sceneID}).First(&merge).Error
	return merge, err
}

// GetMergesForPrimary lists every scene merged into this one.
func GetMergesForPrimary(sceneID uint) ([]SceneMerge, error) {
	db, _ := GetDB()
	defer db.Close()

	var merges []SceneMerge
	err := db.Where(&SceneMerge{PrimarySceneID: sceneID}).Find(&merges).Error
	return merges, err
}
