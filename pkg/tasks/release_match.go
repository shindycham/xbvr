package tasks

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jinzhu/gorm"
	"github.com/sirupsen/logrus"

	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/models"
	"github.com/xbapps/xbvr/pkg/release"
)

// Release-name matching
// ---------------------
//
// Torrents and NZBs are named "Sitename.dd.mm.yyyy.actor.releasegroup". That is
// never the filename a scraper recorded on the scene, so the exact
// filenames_arr comparison in RescanVolumes cannot attach such a file to
// anything. This pass parses the name with pkg/release and matches on studio
// plus date plus performers instead.
//
// A successful match records the release filename on the scene, so any later
// scan of the same file matches exactly and stays matched even if the matcher's
// thresholds are tightened.

// MatchUnmatchedFiles attaches unmatched video files to scenes using the parsed
// release name, and returns how many it attached.
//
// It is called from RescanVolumes and again after an auto-triggered scrape, so
// it must be cheap when there is nothing to do.
func releaseMatchOptions() release.MatchOptions {
	opts := release.DefaultMatchOptions()
	if config.Config.ReleaseMatching.MinScore > 0 {
		opts.MinScore = config.Config.ReleaseMatching.MinScore
	}
	if config.Config.ReleaseMatching.DateWindow > 0 {
		opts.DateWindow = config.Config.ReleaseMatching.DateWindow
	}
	return opts
}

// MatchUnmatchedFiles is the entry point for the release-name pass.
func MatchUnmatchedFiles(db *gorm.DB, tlog *logrus.Entry) int {
	if !config.Config.ReleaseMatching.Enabled {
		return 0
	}

	var files []models.File
	if err := db.Where("scene_id = 0 AND type = 'video'").Find(&files).Error; err != nil {
		tlog.Errorf("Release matching: could not list unmatched files: %s", err)
		return 0
	}
	if len(files) == 0 {
		return 0
	}

	opts := releaseMatchOptions()

	// One query for every distinct studio naming in the library. It is small -
	// the number of studios, not the number of scenes - and lets the per-file
	// queries below be indexed lookups instead of table scans.
	studios := loadSceneStudios(db)

	matched := 0
	unmatchedSites := map[string]bool{}

	for i := range files {
		rel := release.ParseFilename(files[i].Filename)
		if rel.SiteKey == "" {
			continue
		}

		scenes, err := candidateScenesFor(db, rel, studios, opts)
		if err != nil {
			tlog.Errorf("Release matching: candidate lookup failed for %s: %s", files[i].Filename, err)
			continue
		}
		if len(scenes) == 0 {
			continue
		}

		candidates := make([]release.SceneCandidate, 0, len(scenes))
		for j := range scenes {
			candidates = append(candidates, sceneCandidate(scenes[j]))
		}

		result := release.Match(rel, candidates, opts)
		if !result.Matched() {
			tlog.Debugf("Release matching: %s not matched (%s, best %d)", files[i].Filename, result.Reason, result.Best.Total)
			if config.Config.ReleaseMatching.AutoScrape {
				for _, id := range resolveScrapers(rel) {
					unmatchedSites[id] = true
				}
			}
			continue
		}

		// Map the winning candidate back to its row.
		var winner *models.Scene
		for j := range scenes {
			if scenes[j].ID == result.Best.ID {
				winner = &scenes[j]
				break
			}
		}
		if winner == nil {
			continue
		}

		applyReleaseMatch(&files[i], winner, result, tlog)
		matched++
	}

	if len(unmatchedSites) > 0 {
		ids := make([]string, 0, len(unmatchedSites))
		for id := range unmatchedSites {
			ids = append(ids, id)
		}
		scheduleAutoScrape(ids, tlog)
	}

	return matched
}

// applyReleaseMatch records the match on both the file and the scene.
func applyReleaseMatch(file *models.File, scene *models.Scene, result release.MatchResult, tlog *logrus.Entry) {
	file.SceneID = scene.ID
	if err := file.Save(); err != nil {
		tlog.Errorf("Release matching: could not save file %s: %s", file.Filename, err)
		return
	}

	// Remember the release filename on the scene, the same way the os_hash
	// matcher does, so the association is durable and later scans hit the exact
	// filenames_arr path.
	var names []string
	if scene.FilenamesArr != "" {
		if err := json.Unmarshal([]byte(scene.FilenamesArr), &names); err != nil {
			names = nil
		}
	}
	known := false
	for _, n := range names {
		if n == file.Filename {
			known = true
			break
		}
	}
	if !known {
		names = append(names, file.Filename)
		if encoded, err := json.Marshal(names); err == nil {
			scene.FilenamesArr = string(encoded)
			if err := scene.Save(); err != nil {
				tlog.Errorf("Release matching: could not save scene %s: %s", scene.SceneID, err)
			} else {
				models.AddAction(scene.SceneID, "match", "filenames_arr", scene.FilenamesArr)
			}
		}
	}

	scene.UpdateStatus()

	tlog.Infof("Matched %s to scene %q (%s) by release name: studio %d, date %d, names %d, total %d",
		file.Filename, scene.Title, scene.SceneID,
		result.Best.Site, result.Best.Date, result.Best.Name, result.Best.Total)
}

// sceneCandidate projects a scene row into the matcher's view of it.
func sceneCandidate(scene models.Scene) release.SceneCandidate {
	cast := make([]string, 0, len(scene.Cast))
	for _, actor := range scene.Cast {
		cast = append(cast, actor.Name)
	}
	return release.SceneCandidate{
		ID:        scene.ID,
		SceneID:   scene.SceneID,
		Site:      scene.Site,
		Studio:    scene.Studio,
		ScraperID: scene.ScraperId,
		Title:     scene.Title,
		Cast:      cast,
		Date:      scene.ReleaseDate,
	}
}

// sceneStudio is one distinct studio naming found in the scene table, reduced to
// the comparable form the matcher uses.
type sceneStudio struct {
	column string
	value  string
	key    string
}

// loadSceneStudios returns every distinct site, studio and scraper id in the
// library, normalised.
func loadSceneStudios(db *gorm.DB) []sceneStudio {
	var rows []struct {
		Site      string
		Studio    string
		ScraperID string
	}
	if err := db.Raw("SELECT DISTINCT site, studio, scraper_id FROM scenes").Scan(&rows).Error; err != nil {
		log.Errorf("Release matching: could not list studios: %s", err)
		return nil
	}

	seen := map[string]bool{}
	out := make([]sceneStudio, 0, len(rows)*2)
	for _, row := range rows {
		for _, column := range []struct{ name, value string }{
			{"site", row.Site},
			{"studio", row.Studio},
			{"scraper_id", row.ScraperID},
		} {
			if column.value == "" {
				continue
			}
			key := release.NormalizeSiteKey(column.value)
			if key == "" {
				continue
			}
			dedupe := column.name + "\x00" + column.value
			if seen[dedupe] {
				continue
			}
			seen[dedupe] = true
			out = append(out, sceneStudio{column: column.name, value: column.value, key: key})
		}
	}
	return out
}

// candidateScenesFor loads the scenes that could possibly match a release:
// those sharing its studio, and, when the release carries a date, those falling
// inside the date window.
func candidateScenesFor(db *gorm.DB, rel release.Release, studios []sceneStudio, opts release.MatchOptions) ([]models.Scene, error) {
	values := candidateSiteValues(rel, studios)
	if len(values) == 0 {
		return nil, nil
	}

	// Fixed column order keeps the generated SQL (and the query plan cache)
	// stable across files.
	clauses := make([]string, 0, 3)
	args := make([]interface{}, 0, 8)
	for _, column := range []string{"site", "studio", "scraper_id"} {
		columnValues := values[column]
		if len(columnValues) == 0 {
			continue
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(columnValues)), ",")
		clauses = append(clauses, fmt.Sprintf("%s IN (%s)", column, placeholders))
		for _, v := range columnValues {
			args = append(args, v)
		}
	}

	where := "(" + strings.Join(clauses, " OR ") + ")"
	if rel.HasDate {
		// Two days of slack beyond the scoring window so that rows in a
		// neighbouring timezone are still fetched; the scorer applies the real
		// window.
		slack := opts.DateWindow + 2
		where += " AND release_date >= ? AND release_date <= ?"
		args = append(args, rel.Date.AddDate(0, 0, -slack), rel.Date.AddDate(0, 0, slack))
	}

	var scenes []models.Scene
	if err := db.Preload("Cast").Where(where, args...).Find(&scenes).Error; err != nil {
		return nil, err
	}
	return scenes, nil
}

// candidateSiteValues picks, from every distinct studio naming in the library,
// the column/value pairs that could belong to the release's studio.
//
// It is separated from the query below so the selection rule can be tested
// without a database.
func candidateSiteValues(rel release.Release, studios []sceneStudio) map[string][]string {
	values := map[string][]string{}
	if rel.SiteKey == "" {
		return values
	}
	for _, studio := range studios {
		if release.SiteKeysMatch(rel.SiteKey, studio.key) {
			values[studio.column] = append(values[studio.column], studio.value)
		}
	}
	return values
}

// resolveScrapers maps a release's studio onto the scrapers that could fetch it.
func resolveScrapers(rel release.Release) []string {
	if rel.SiteKey == "" {
		return nil
	}
	var out []string
	for _, scraper := range models.GetScrapers() {
		if strings.HasSuffix(scraper.ID, "-single_scene") {
			continue
		}
		if release.SiteKeysMatch(rel.SiteKey, release.NormalizeSiteKey(scraper.Name)) ||
			release.SiteKeysMatch(rel.SiteKey, release.NormalizeSiteKey(scraper.ID)) {
			out = append(out, scraper.ID)
		}
	}
	return out
}

// autoScrapeCooldown stops an unmatched file from re-triggering a scrape of the
// same studio on every rescan.
const autoScrapeCooldown = 6 * time.Hour

var (
	autoScrapeMu   sync.Mutex
	autoScrapeLast = map[string]time.Time{}
)

// scheduleAutoScrape scrapes the studios responsible for unmatched files in the
// background and then re-runs matching, so a release that arrived before its
// scene was ever scraped resolves itself.
func scheduleAutoScrape(siteIDs []string, tlog *logrus.Entry) {
	now := time.Now()
	due := make([]string, 0, len(siteIDs))

	autoScrapeMu.Lock()
	for _, id := range siteIDs {
		if last, seen := autoScrapeLast[id]; seen && now.Sub(last) < autoScrapeCooldown {
			continue
		}
		// Only scrape studios that are enabled.
		var site models.Site
		if err := site.GetIfExist(id); err != nil || !site.IsEnabled {
			continue
		}
		due = append(due, id)
	}
	autoScrapeMu.Unlock()

	if len(due) == 0 {
		return
	}

	go func() {
		scraped := false
		for _, id := range due {
			if models.CheckLock("scrape") {
				// Deliberately not recorded as attempted: the cooldown exists to
				// stop repeated scrapes, and this scrape never happened.
				tlog.Infof("Release matching: a scrape is already running, not scraping %s", id)
				continue
			}
			// Record the attempt only once it is really going to happen.
			autoScrapeMu.Lock()
			autoScrapeLast[id] = time.Now()
			autoScrapeMu.Unlock()

			tlog.Infof("Release matching: scraping %s to identify unmatched downloads", id)
			Scrape(id, "", "")
			scraped = true
		}
		if !scraped {
			return
		}
		db, err := models.GetDB()
		if err != nil {
			return
		}
		defer db.Close()
		if n := MatchUnmatchedFiles(db, tlog); n > 0 {
			tlog.Infof("Release matching: attached %d file(s) after scraping", n)
		}
	}()
}
