package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jinzhu/gorm"

	"github.com/xbapps/xbvr/pkg/common"
	"github.com/xbapps/xbvr/pkg/models"
)

// Duplicate scene detection and merging
// -------------------------------------
//
// The aggregator sites overlap heavily: the same scene is listed by its own
// studio, by SexLikeReal and by VRSmash, and xbvr creates a scene row for each
// because the scrapers cannot know they are the same thing. The result is a
// library with the same scene two or three times.
//
// Combining them is deliberately a two step process. xbvr finds candidates, and
// a person decides: fusing two genuinely different scenes is far worse than
// leaving a duplicate, and the mistake is invisible until you go to watch
// something. A merge is also reversible, so the decision costs nothing.

// DuplicateScene is one scene inside a candidate group.
type DuplicateScene struct {
	ID          uint      `json:"id"`
	SceneID     string    `json:"scene_id"`
	Title       string    `json:"title"`
	Site        string    `json:"site"`
	Studio      string    `json:"studio"`
	ScraperID   string    `json:"scraper_id"`
	ReleaseDate time.Time `json:"release_date"`
	// Duration is in minutes, which is how the library stores it and how the
	// scene card renders it ("{{item.duration}}m"). Sites round it differently,
	// hence the tolerance in durationsAgree.
	Duration    int      `json:"duration"`
	Cast        []string `json:"cast"`
	CoverURL    string   `json:"cover_url"`
	IsHidden    bool     `json:"is_hidden"`
	FileCount   int      `json:"file_count"`
	ScriptCount int      `json:"script_count"`
	// MergedInto is set when this scene has already been merged into another,
	// which is worth showing so the same scene is not merged twice.
	MergedInto uint `json:"merged_into"`
}

// DuplicateGroup is a set of scenes that look like the same scene.
type DuplicateGroup struct {
	// Key is the normalised title the group was built from.
	Key     string           `json:"key"`
	Title   string           `json:"title"`
	Score   int              `json:"score"`
	Reasons []string         `json:"reasons"`
	Scenes  []DuplicateScene `json:"scenes"`
}

// duplicateTextStopWords carry no identifying weight in a scene title.
// duplicateDateWindow is how far apart two release dates may be and still count
// as the same release. Sites disagree about a scene's date by a day often enough
// that requiring an exact match would miss real duplicates.
const duplicateDateWindow = 24 * time.Hour

// MaxDuplicateGroupSize bounds a group. An exact cast and runtime can still be
// shared by a series of scenes, and a group nobody can review is not useful.
const MaxDuplicateGroupSize = 12

// duplicateKey identifies scenes that are the same scene.
//
// The rule is exact on purpose: the runtime must be identical, the cast must be
// identical, and the release dates must fall within a day of each other (see
// duplicateDateWindow, applied by the caller). Nothing is inferred from a title
// and there is no confidence to interpret, because looser signals - one
// performer in common, a title that matched on one word, a missing date compared
// as though it were a date - produced pairs that were obviously unrelated and
// made the whole review screen untrustworthy.
//
// It reports false for a scene with no runtime or no cast, because "both are
// unknown" is not a match.
func duplicateKey(duration int, cast []string) (string, bool) {
	if duration <= 0 {
		return "", false
	}

	names := make([]string, 0, len(cast))
	seen := map[string]bool{}
	for _, name := range cast {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		names = append(names, key)
	}
	if len(names) == 0 {
		return "", false
	}

	// Sorted so the order the site happened to list the cast in cannot matter.
	sort.Strings(names)
	return strconv.Itoa(duration) + "|" + strings.Join(names, "|"), true
}

// FindDuplicateScenes returns sets of scenes that are the same scene listed more
// than once.
func FindDuplicateScenes(maxGroups int) ([]DuplicateGroup, error) {
	db, err := models.GetDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	return findDuplicateScenes(db, maxGroups)
}

// findDuplicateScenes does the work against a given handle, so it can be tested
// without the application database.
func findDuplicateScenes(db *gorm.DB, maxGroups int) ([]DuplicateGroup, error) {
	type row struct {
		ID          uint
		SceneID     string
		Title       string
		Site        string
		Studio      string
		ScraperID   string
		ReleaseDate time.Time
		Duration    int
		CoverURL    string
		IsHidden    bool
	}

	var rows []row
	if err := db.Table("scenes").
		Select("id, scene_id, title, site, studio, scraper_id, release_date, duration, cover_url, is_hidden").
		Where("title <> '' and duration > 0").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// The cast is needed for every scene now, since it is half of the rule.
	type castRow struct {
		SceneID uint
		Name    string
	}
	var castRows []castRow
	if err := db.Table("scene_cast").
		Select("scene_cast.scene_id as scene_id, actors.name as name").
		Joins("join actors on actors.id = scene_cast.actor_id").
		Scan(&castRows).Error; err != nil {
		return nil, err
	}
	castByScene := map[uint][]string{}
	for _, c := range castRows {
		castByScene[c.SceneID] = append(castByScene[c.SceneID], c.Name)
	}

	byKey := map[string][]row{}
	for _, r := range rows {
		// A scene with no release date cannot be placed in time, and two scenes
		// with no date are not within a day of each other: treating "unknown" as
		// a date is what grouped unrelated scenes together before.
		if r.ReleaseDate.IsZero() {
			continue
		}
		key, ok := duplicateKey(r.Duration, castByScene[r.ID])
		if !ok {
			continue
		}
		byKey[key] = append(byKey[key], r)
	}

	// Only scenes that are in a group of two or more are worth describing.
	type countRow struct {
		SceneID uint
		Type    string
		N       int
	}
	var counts []countRow
	if err := db.Table("files").
		Select("scene_id, type, count(*) as n").
		Where("scene_id <> 0").
		Group("scene_id, type").
		Scan(&counts).Error; err != nil {
		return nil, err
	}
	fileCount := map[uint]int{}
	scriptCount := map[uint]int{}
	for _, c := range counts {
		switch c.Type {
		case "video":
			fileCount[c.SceneID] += c.N
		case "script":
			scriptCount[c.SceneID] += c.N
		}
	}

	var merges []models.SceneMerge
	if err := db.Find(&merges).Error; err != nil {
		return nil, err
	}
	mergedInto := map[uint]uint{}
	for _, m := range merges {
		mergedInto[m.DuplicateSceneID] = m.PrimarySceneID
	}

	// A key can cover scenes released years apart - the same performers in the
	// same length of scene is a series as well as a duplicate - so each key is
	// split into runs of dates within a day of each other.
	var clusters [][]row
	for _, scenes := range byKey {
		if len(scenes) < 2 {
			continue
		}
		sort.Slice(scenes, func(i, j int) bool {
			return scenes[i].ReleaseDate.Before(scenes[j].ReleaseDate)
		})

		var current []row
		flush := func() {
			if len(current) >= 2 {
				clusters = append(clusters, append([]row{}, current...))
			}
			current = nil
		}
		for _, r := range scenes {
			if len(current) > 0 && r.ReleaseDate.Sub(current[0].ReleaseDate) > duplicateDateWindow {
				flush()
			}
			current = append(current, r)
		}
		flush()
	}

	var groups []DuplicateGroup
	for _, scenes := range clusters {
		if len(scenes) > MaxDuplicateGroupSize {
			scenes = scenes[:MaxDuplicateGroupSize]
		}
		sort.Slice(scenes, func(i, j int) bool {
			return scenes[i].ReleaseDate.After(scenes[j].ReleaseDate)
		})
		key := ""

		group := DuplicateGroup{
			Key:    key,
			Title:  scenes[0].Title,
			Scenes: make([]DuplicateScene, 0, len(scenes)),
		}
		for _, r := range scenes {
			group.Scenes = append(group.Scenes, DuplicateScene{
				ID:          r.ID,
				SceneID:     r.SceneID,
				Title:       r.Title,
				Site:        r.Site,
				Studio:      r.Studio,
				ScraperID:   r.ScraperID,
				ReleaseDate: r.ReleaseDate,
				Duration:    r.Duration,
				Cast:        castByScene[r.ID],
				CoverURL:    r.CoverURL,
				IsHidden:    r.IsHidden,
				FileCount:   fileCount[r.ID],
				ScriptCount: scriptCount[r.ID],
				MergedInto:  mergedInto[r.ID],
			})
		}

		// Score is only used to order the list now: a group with more performers
		// in common is a stronger coincidence than one with a single name.
		castCounts := map[string]int{}
		for _, r := range scenes {
			seen := map[string]bool{}
			for _, name := range castByScene[r.ID] {
				n := strings.ToLower(strings.TrimSpace(name))
				if n == "" || seen[n] {
					continue
				}
				seen[n] = true
				castCounts[n]++
			}
		}
		matching := 0
		for _, n := range castCounts {
			if n > 1 {
				matching++
			}
		}
		group.Score = 10 + matching
		group.Reasons = []string{"identical cast", "identical length", "release date within a day"}
		groups = append(groups, group)
	}

	// Most scenes first, then the strongest, then newest.
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i].Scenes) != len(groups[j].Scenes) {
			return len(groups[i].Scenes) > len(groups[j].Scenes)
		}
		if groups[i].Score != groups[j].Score {
			return groups[i].Score > groups[j].Score
		}
		return groups[i].Scenes[0].ReleaseDate.After(groups[j].Scenes[0].ReleaseDate)
	})

	if maxGroups > 0 && len(groups) > maxGroups {
		groups = groups[:maxGroups]
	}
	return groups, nil
}

// MergeScenes combines duplicate scenes into a primary one.
//
// Files are moved to the primary so nothing becomes unplayable, and the
// duplicate is hidden rather than deleted, with enough recorded to reverse it.
func MergeScenes(primaryID uint, duplicateIDs []uint) (int, error) {
	db, err := models.GetDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()

	merged, touched, err := mergeScenes(db, primaryID, duplicateIDs)
	if err != nil && merged == 0 {
		return merged, err
	}

	// Refresh the affected scenes only after the database work is committed, so
	// their availability reflects the files they now own.
	for _, id := range touched {
		var scene models.Scene
		if err := scene.GetIfExistByPK(id); err == nil {
			scene.UpdateStatus()
		}
	}
	for i := 1; i < len(touched); i++ {
		var primary, duplicate models.Scene
		if err := primary.GetIfExistByPK(touched[0]); err == nil {
			if err := duplicate.GetIfExistByPK(touched[i]); err == nil {
				models.AddAction(primary.SceneID, "merge", "scene", "merged "+duplicate.SceneID+" into this scene")
				models.AddAction(duplicate.SceneID, "merge", "scene", "merged into "+primary.SceneID)
			}
		}
	}

	if merged > 0 {
		_ = common.PublishWS("state.change.scenes", nil)
	}
	return merged, err
}

// mergeScenes performs the database work of a merge and returns the scenes that
// changed, so the caller can refresh their status and write the audit trail.
func mergeScenes(db *gorm.DB, primaryID uint, duplicateIDs []uint) (int, []uint, error) {
	var primary models.Scene
	if err := db.First(&primary, primaryID).Error; err != nil {
		return 0, nil, errors.New("the scene to keep does not exist")
	}

	touched := []uint{primaryID}
	merged := 0

	for _, duplicateID := range duplicateIDs {
		if duplicateID == primaryID || duplicateID == 0 {
			continue
		}

		var duplicate models.Scene
		if err := db.First(&duplicate, duplicateID).Error; err != nil {
			return merged, touched, fmt.Errorf("scene %d does not exist", duplicateID)
		}

		var existing models.SceneMerge
		if err := db.Where(&models.SceneMerge{DuplicateSceneID: duplicateID}).First(&existing).Error; err == nil {
			// Already merged somewhere. Refuse rather than quietly re-pointing it.
			return merged, touched, fmt.Errorf("scene %d is already merged into another scene", duplicateID)
		}

		// Hand the files over, remembering which ones so an undo can give them
		// back. Without this a merged-away scene's downloaded file would drop
		// out of the library entirely.
		var files []models.File
		if err := db.Where("scene_id = ?", duplicateID).Find(&files).Error; err != nil {
			return merged, touched, err
		}
		movedIDs := make([]uint, 0, len(files))
		for _, f := range files {
			if err := db.Model(&models.File{}).Where("id = ?", f.ID).Update("scene_id", primaryID).Error; err != nil {
				return merged, touched, err
			}
			movedIDs = append(movedIDs, f.ID)
		}
		encoded, _ := json.Marshal(movedIDs)

		record := models.SceneMerge{
			PrimarySceneID:     primaryID,
			DuplicateSceneID:   duplicateID,
			DuplicateWasHidden: duplicate.IsHidden,
			MovedFileIDs:       string(encoded),
		}
		if err := db.Create(&record).Error; err != nil {
			return merged, touched, err
		}

		if err := db.Model(&models.Scene{}).Where("id = ?", duplicateID).
			Update("is_hidden", true).Error; err != nil {
			return merged, touched, err
		}

		touched = append(touched, duplicateID)
		merged++
	}

	return merged, touched, nil
}

// UnmergeScene reverses a merge, giving the duplicate back its files and its
// previous hidden state.
func UnmergeScene(duplicateID uint) error {
	db, err := models.GetDB()
	if err != nil {
		return err
	}
	defer db.Close()

	primaryID, err := unmergeScene(db, duplicateID)
	if err != nil {
		return err
	}

	if scene, err := loadScene(db, duplicateID); err == nil {
		scene.UpdateStatus()
		models.AddAction(scene.SceneID, "merge", "scene", "unmerged")
	}
	if scene, err := loadScene(db, primaryID); err == nil {
		scene.UpdateStatus()
		models.AddAction(scene.SceneID, "merge", "scene", "unmerged")
	}

	_ = common.PublishWS("state.change.scenes", nil)
	return nil
}

// unmergeScene performs the database work of an undo, returning the scene the
// duplicate had been merged into.
func unmergeScene(db *gorm.DB, duplicateID uint) (uint, error) {
	var record models.SceneMerge
	if err := db.Where(&models.SceneMerge{DuplicateSceneID: duplicateID}).First(&record).Error; err != nil {
		return 0, errors.New("that scene is not merged into another")
	}

	var movedIDs []uint
	if record.MovedFileIDs != "" {
		_ = json.Unmarshal([]byte(record.MovedFileIDs), &movedIDs)
	}
	for _, id := range movedIDs {
		if err := db.Model(&models.File{}).Where("id = ?", id).
			Update("scene_id", duplicateID).Error; err != nil {
			return record.PrimarySceneID, err
		}
	}

	if err := db.Model(&models.Scene{}).Where("id = ?", duplicateID).
		Update("is_hidden", record.DuplicateWasHidden).Error; err != nil {
		return record.PrimarySceneID, err
	}
	if err := db.Delete(&record).Error; err != nil {
		return record.PrimarySceneID, err
	}

	return record.PrimarySceneID, nil
}

// loadScene reads a scene through a given handle rather than the application
// database.
func loadScene(db *gorm.DB, id uint) (models.Scene, error) {
	var scene models.Scene
	err := db.First(&scene, id).Error
	return scene, err
}

// MergedScene describes one existing merge, for the undo list.
type MergedScene struct {
	PrimaryID        uint      `json:"primary_id"`
	PrimarySceneID   string    `json:"primary_scene_id"`
	PrimaryTitle     string    `json:"primary_title"`
	DuplicateID      uint      `json:"duplicate_id"`
	DuplicateSceneID string    `json:"duplicate_scene_id"`
	DuplicateTitle   string    `json:"duplicate_title"`
	CreatedAt        time.Time `json:"created_at"`
}

// GetMergedScenes lists every merge, newest first.
func GetMergedScenes() ([]MergedScene, error) {
	db, err := models.GetDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var records []models.SceneMerge
	if err := db.Order("created_at desc").Find(&records).Error; err != nil {
		return nil, err
	}

	out := make([]MergedScene, 0, len(records))
	for _, r := range records {
		var primary, duplicate models.Scene
		_ = primary.GetIfExistByPK(r.PrimarySceneID)
		_ = duplicate.GetIfExistByPK(r.DuplicateSceneID)
		out = append(out, MergedScene{
			PrimaryID:        primary.ID,
			PrimarySceneID:   primary.SceneID,
			PrimaryTitle:     primary.Title,
			DuplicateID:      duplicate.ID,
			DuplicateSceneID: duplicate.SceneID,
			DuplicateTitle:   duplicate.Title,
			CreatedAt:        r.CreatedAt,
		})
	}
	return out, nil
}
