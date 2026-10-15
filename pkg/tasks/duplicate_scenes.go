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
	Duration    int       `json:"duration"`
	Cast        []string  `json:"cast"`
	CoverURL    string    `json:"cover_url"`
	IsHidden    bool      `json:"is_hidden"`
	FileCount   int       `json:"file_count"`
	ScriptCount int       `json:"script_count"`
	// MergedInto is set when this scene has already been merged into another,
	// which is worth showing so the same scene is not merged twice.
	MergedInto uint `json:"merged_into,omitempty"`
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
var duplicateTextStopWords = map[string]bool{
	"the": true, "and": true, "with": true, "from": true, "of": true, "a": true,
	"an": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"her": true, "his": true, "my": true, "vs": true, "feat": true, "featuring": true,
	"part": true, "scene": true, "vr": true, "xxx": true,
}

// normaliseDuplicateTitle reduces a title to the words that identify it.
func normaliseDuplicateTitle(title string) string {
	fields := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 || duplicateTextStopWords[f] {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

// duplicateDateWindow is how far apart two release dates may be and still count
// as the same release. Sites disagree about a scene's date by a day often enough
// that requiring an exact match would miss real duplicates.
const duplicateDateWindow = 3 * time.Hour * 24

// MaxDuplicateGroupSize bounds a group. A very common title such as a single
// word can pull in unrelated scenes, and a group nobody can meaningfully review
// is worse than no group.
const MaxDuplicateGroupSize = 12

// FindDuplicateScenes looks for scenes that should probably be one scene.
//
// It groups on the normalised title and then splits by release date, which is
// high precision: two scenes released within a few days of each other under the
// same title are almost always the same scene on two sites. Cast and duration
// only adjust the confidence, they never decide on their own.
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
//
// Two passes feed one set of candidate groups. The first matches on the
// normalised title and release date, which is precise. The second catches the
// same scene listed under different titles by different sites, using a shared
// performer and a near-identical date. Anything either finds is put in front of
// a person, because neither signal is good enough to merge on its own.
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
		Where("title <> ''").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	rowByID := make(map[uint]row, len(rows))
	byTitle := map[string][]row{}
	for _, r := range rows {
		rowByID[r.ID] = r
		key := normaliseDuplicateTitle(r.Title)
		if key == "" {
			continue
		}
		byTitle[key] = append(byTitle[key], r)
	}

	var clusters [][]uint

	// Pass one: same normalised title, released within a few days of each other.
	for _, scenes := range byTitle {
		// A title shared by a great many scenes is not identifying anything.
		if len(scenes) < 2 || len(scenes) > MaxDuplicateGroupSize*4 {
			continue
		}
		sort.Slice(scenes, func(i, j int) bool { return scenes[i].ReleaseDate.Before(scenes[j].ReleaseDate) })

		var current []row
		flush := func() {
			if len(current) >= 2 && len(current) <= MaxDuplicateGroupSize {
				ids := make([]uint, 0, len(current))
				for _, r := range current {
					ids = append(ids, r.ID)
				}
				clusters = append(clusters, ids)
			}
			current = nil
		}
		for _, r := range scenes {
			// Compared against the first of the run so a long series of near
			// dates cannot drift into grouping unrelated scenes.
			if len(current) > 0 && r.ReleaseDate.Sub(current[0].ReleaseDate) > duplicateDateWindow {
				flush()
			}
			current = append(current, r)
		}
		flush()
	}

	// Pass two: different titles, same performer, same few days.
	clusters = append(clusters, castDateClusters(db)...)

	// The two passes can describe the same set of scenes; report each once.
	seen := map[string]bool{}
	unique := make([][]uint, 0, len(clusters))
	for _, ids := range clusters {
		key := clusterKey(ids)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, ids)
	}

	// Detail is loaded only once something actually needs showing. For a library
	// of tens of thousands of scenes the cast join is the expensive part, and
	// most scans find nothing.
	castByScene := map[uint][]string{}
	fileCount := map[uint]int{}
	scriptCount := map[uint]int{}
	mergedInto := map[uint]uint{}
	if len(unique) > 0 {
		type castRow struct {
			SceneID uint
			Name    string
		}
		var cast []castRow
		if err := db.Table("scene_cast").
			Select("scene_cast.scene_id as scene_id, actors.name as name").
			Joins("join actors on actors.id = scene_cast.actor_id").
			Scan(&cast).Error; err != nil {
			return nil, err
		}
		for _, c := range cast {
			castByScene[c.SceneID] = append(castByScene[c.SceneID], c.Name)
		}

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
		for _, m := range merges {
			mergedInto[m.DuplicateSceneID] = m.PrimarySceneID
		}
	}

	var groups []DuplicateGroup
	for _, ids := range unique {
		group := DuplicateGroup{Scenes: make([]DuplicateScene, 0, len(ids))}
		for _, id := range ids {
			r, ok := rowByID[id]
			if !ok {
				continue
			}
			if group.Title == "" {
				group.Title = r.Title
				group.Key = normaliseDuplicateTitle(r.Title)
			}
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
		if len(group.Scenes) < 2 {
			continue
		}
		group.Score, group.Reasons = scoreDuplicateGroup(group.Scenes)
		groups = append(groups, group)
	}

	// Most scenes first, then the most confident, then newest.
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

// clusterKey is a stable identity for a set of scenes, so the two detection
// passes cannot report the same group twice in different orders.
func clusterKey(ids []uint) string {
	sorted := append([]uint{}, ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	parts := make([]string, 0, len(sorted))
	for _, id := range sorted {
		parts = append(parts, strconv.FormatUint(uint64(id), 10))
	}
	return strings.Join(parts, ",")
}

// castDateClusters finds groups the title pass cannot see: the same scene listed
// by two sites under different titles, recognised by a shared performer and a
// near-identical release date.
//
// Aggregators rename scenes often enough that title matching alone misses real
// duplicates, and one performer appearing on two sites on the same day is a
// strong enough signal to put in front of a person.
func castDateClusters(db *gorm.DB) [][]uint {
	type castRow struct {
		SceneID   uint
		ScraperID string
		Site      string
		Released  time.Time
		Name      string
	}

	var rows []castRow
	if err := db.Table("scene_cast").
		Select("scene_cast.scene_id as scene_id, actors.name as name, " +
			"scenes.scraper_id as scraper_id, scenes.site as site, scenes.release_date as released").
		Joins("join actors on actors.id = scene_cast.actor_id").
		Joins("join scenes on scenes.id = scene_cast.scene_id").
		Where("scenes.title <> ''").
		Scan(&rows).Error; err != nil {
		return nil
	}

	byActor := map[string][]castRow{}
	for _, r := range rows {
		name := strings.ToLower(strings.TrimSpace(r.Name))
		if name == "" {
			continue
		}
		byActor[name] = append(byActor[name], r)
	}

	var clusters [][]uint
	for _, scenes := range byActor {
		if len(scenes) < 2 {
			continue
		}
		sort.Slice(scenes, func(i, j int) bool { return scenes[i].Released.Before(scenes[j].Released) })

		var current []castRow
		flush := func() {
			if len(current) >= 2 && len(current) <= MaxDuplicateGroupSize {
				sources := map[string]bool{}
				ids := make([]uint, 0, len(current))
				for _, r := range current {
					source := r.ScraperID
					if source == "" {
						source = r.Site
					}
					sources[source] = true
					ids = append(ids, r.SceneID)
				}
				// One source means one scraper listing the same performer twice on
				// a day, which is not a duplicate worth reviewing.
				if len(sources) > 1 {
					clusters = append(clusters, ids)
				}
			}
			current = nil
		}
		for _, r := range scenes {
			if len(current) > 0 && r.Released.Sub(current[0].Released) > duplicateDateWindow {
				flush()
			}
			current = append(current, r)
		}
		flush()
	}
	return clusters
}

// scoreDuplicateGroup rates how sure we are that these scenes are one scene.
func scoreDuplicateGroup(scenes []DuplicateScene) (int, []string) {
	if len(scenes) < 2 {
		return 0, nil
	}

	score := 0
	var reasons []string

	// Different sources is the whole point: two rows from the same scraper are
	// more likely a scraper bug than a duplicate.
	sources := map[string]bool{}
	for _, s := range scenes {
		source := s.ScraperID
		if source == "" {
			source = s.Site
		}
		sources[source] = true
	}
	if len(sources) > 1 {
		score += 5
		reasons = append(reasons, "different sources")
	}

	// Dates agreeing closely is strong evidence.
	spread := scenes[len(scenes)-1].ReleaseDate.Sub(scenes[0].ReleaseDate)
	switch {
	case spread == 0:
		score += 4
		reasons = append(reasons, "same release date")
	case spread <= 36*time.Hour:
		score += 3
		reasons = append(reasons, "release dates within a day")
	default:
		score += 1
		reasons = append(reasons, "release dates close")
	}

	// A shared performer confirms it, but is never required: sites disagree on
	// names and on whether the cast is listed at all.
	if shared, _ := sharedCast(scenes); shared > 0 {
		score += 2
		reasons = append(reasons, "shared cast")
	}

	// Durations that agree rule out a trailer sitting next to the feature.
	if durationsAgree(scenes) {
		score += 2
		reasons = append(reasons, "matching duration")
	}

	return score, reasons
}

func sharedCast(scenes []DuplicateScene) (int, string) {
	if len(scenes) == 0 {
		return 0, ""
	}
	counts := map[string]int{}
	for _, s := range scenes {
		seen := map[string]bool{}
		for _, name := range s.Cast {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			counts[key]++
		}
	}
	for name, n := range counts {
		if n > 1 {
			return n, name
		}
	}
	return 0, ""
}

// durationsAgree reports whether every scene with a known duration is within a
// couple of minutes of the others.
func durationsAgree(scenes []DuplicateScene) bool {
	var known []int
	for _, s := range scenes {
		if s.Duration > 0 {
			known = append(known, s.Duration)
		}
	}
	if len(known) < 2 {
		return false
	}
	min, max := known[0], known[0]
	for _, d := range known {
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	// 120 seconds of slack: sites round durations differently, and a trailer is
	// minutes shorter rather than seconds.
	return max-min <= 120
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
