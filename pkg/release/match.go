package release

import (
	"sort"
	"strings"
	"time"
)

// SceneCandidate is the scene data the matcher needs. It deliberately mirrors
// only a few fields of the database model so that matching stays a pure
// function that can be tested without a database.
type SceneCandidate struct {
	// ID is the database primary key, echoed back in scores.
	ID uint
	// SceneID is the external scene identifier, for logging.
	SceneID string
	// Title, Site, Studio and ScraperID are the places a studio name might live.
	Site      string
	Studio    string
	ScraperID string
	Title     string
	// Cast is the list of performer names attached to the scene.
	Cast []string
	// Date is the scene's release date.
	Date time.Time
}

// MatchOptions tunes the matcher's strictness.
type MatchOptions struct {
	// MinScore is the total a candidate must reach to be considered at all.
	MinScore int
	// MinMargin is how far the best candidate must beat the runner-up before
	// the match is trusted. A near-tie means the release cannot be told apart
	// between two scenes, and guessing would attach the file to the wrong one.
	MinMargin int
	// DateWindow is how many days either side of the scene date a release date
	// is allowed to fall for partial credit.
	DateWindow int
}

// DefaultMatchOptions returns the strictness used unless overridden.
func DefaultMatchOptions() MatchOptions {
	return MatchOptions{MinScore: 10, MinMargin: 3, DateWindow: 7}
}

// Score breaks down how well one scene matches a release.
type Score struct {
	// ID and SceneID identify the scene.
	ID      uint
	SceneID string
	Title   string
	// Site, Date and Name are the component scores.
	Site int
	Date int
	Name int
	// Total is their sum.
	Total int
	// Reasons explains the score, for logging and for the UI.
	Reasons []string
}

// MatchResult reports the outcome of matching a release against a set of scenes.
type MatchResult struct {
	// Best is the highest scoring candidate.
	Best Score
	// Runners holds every candidate that scored above zero, best first. It is
	// populated even when the match is rejected, so callers can log why.
	Runners []Score
	// Ambiguous is set when two candidates were too close to choose between.
	Ambiguous bool
	// Reason explains a rejection.
	Reason string
}

// Matched reports whether the result should be acted on.
func (m MatchResult) Matched() bool {
	return m.Reason == "" && m.Best.Total > 0
}

// Match compares a parsed release against scene candidates and returns the best
// match, if the evidence is strong enough and no second scene is close behind.
//
// The signal is deliberately site-first: a release name always carries the
// studio, and a studio normally publishes one scene per day, so an exact site
// plus an exact date is already conclusive. Performer and title overlap then
// either confirms that or has to carry the match when the date is missing.
func Match(rel Release, scenes []SceneCandidate, opts MatchOptions) MatchResult {
	if opts.MinScore == 0 {
		opts = DefaultMatchOptions()
	}
	if rel.SiteKey == "" && len(rel.Performers) == 0 {
		return MatchResult{Reason: "release name carries no studio and no performers"}
	}

	tokens := relTokenSet(rel)

	scores := make([]Score, 0, len(scenes))
	for _, scene := range scenes {
		s := scoreScene(rel, tokens, scene, opts)
		if s.Total > 0 {
			scores = append(scores, s)
		}
	}

	// Highest total first; ties broken by the more specific (name-heavy) match so
	// the ordering is deterministic.
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Total != scores[j].Total {
			return scores[i].Total > scores[j].Total
		}
		return scores[i].Name > scores[j].Name
	})

	result := MatchResult{Runners: scores}
	if len(scores) == 0 {
		result.Reason = "no scene shares the release's studio or performers"
		return result
	}

	result.Best = scores[0]
	if result.Best.Total < opts.MinScore {
		result.Reason = "best candidate scored too low"
		return result
	}
	if result.Best.Date == 0 && result.Best.Name < 6 {
		// A studio match with no date has to be corroborated by the names: a
		// long-lived studio has many scenes that share nothing else, so studio
		// alone is not evidence.
		result.Reason = "a studio match without a date needs corroborating performers"
		return result
	}
	if len(scores) > 1 {
		margin := result.Best.Total - scores[1].Total
		if margin < opts.MinMargin {
			result.Ambiguous = true
			result.Reason = "two scenes match the release equally well"
			return result
		}
	}
	return result
}

// scoreScene scores one candidate.
func scoreScene(rel Release, tokens map[string]bool, scene SceneCandidate, opts MatchOptions) Score {
	s := Score{ID: scene.ID, SceneID: scene.SceneID, Title: scene.Title}

	s.Site, _ = siteScore(rel.SiteKey, scene)
	if s.Site == 0 {
		// Without a studio in common there is nothing to anchor on. A performer
		// list alone is not enough to attach a file to a scene.
		return Score{}
	}
	s.Reasons = append(s.Reasons, "studio")

	s.Date = dateScore(rel, scene.Date, opts.DateWindow)
	if s.Date > 0 {
		s.Reasons = append(s.Reasons, "date")
	}

	s.Name, _ = nameScore(tokens, scene)
	if s.Name > 0 {
		s.Reasons = append(s.Reasons, "performers")
	}

	s.Total = s.Site + s.Date + s.Name

	// The score is reported as computed, even when it is too weak to act on, so
	// that the release-search UI can show *how* close a candidate was. Whether a
	// score is good enough to attach a file is decided once, in Match.
	return s
}

// SiteKeysMatch reports whether two normalised studio keys refer to the same
// studio. Exact equality is the common case; the containment rule then catches
// a release naming "vrbangers" where xbvr stores "VRBangers Network", at the
// cost of also treating a short studio name as matching a longer one that
// merely contains it.
//
// Both the in-memory matcher and the database candidate lookup use this, so the
// two can never disagree about which scenes are worth scoring.
func SiteKeysMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) >= 5 && len(b) >= 5 {
		return strings.Contains(a, b) || strings.Contains(b, a)
	}
	return false
}

// siteScore compares the release's studio key against every name a scene might
// carry a studio under, returning the strongest agreement.
func siteScore(releaseKey string, scene SceneCandidate) (int, string) {
	if releaseKey == "" {
		return 0, ""
	}
	best, bestName := 0, ""
	for _, candidate := range []string{scene.Site, scene.Studio, scene.ScraperID} {
		key := NormalizeSiteKey(candidate)
		if key == "" {
			continue
		}
		switch {
		case key == releaseKey:
			if 6 > best {
				best, bestName = 6, candidate
			}
		case SiteKeysMatch(key, releaseKey):
			// "vrbangers" against a site named "VRBangers Network", or a release
			// tagged "vrbangers" against the scraper id "vrbangers".
			if 4 > best {
				best, bestName = 4, candidate
			}
		}
	}
	return best, bestName
}

// dateScore rewards a release date that lands on, or near, the scene's date.
func dateScore(rel Release, sceneDate time.Time, window int) int {
	if !rel.HasDate {
		return 0
	}
	if sceneDate.IsZero() {
		// A year-only release date against a dateless scene proves nothing.
		return 0
	}
	if !rel.DateIsExact {
		if rel.Date.Year() == sceneDate.Year() {
			return 1
		}
		return 0
	}
	diff := rel.Date.Sub(sceneDate)
	days := int(diff.Hours() / 24)
	if days < 0 {
		days = -days
	}
	switch {
	case days == 0:
		return 5
	case days <= 1:
		return 3
	case window > 0 && days <= window:
		return 1
	}
	return 0
}

// nameScore rewards overlap between the release's name tokens and the scene's
// cast and title.
func nameScore(tokens map[string]bool, scene SceneCandidate) (int, []string) {
	if len(tokens) == 0 {
		return 0, nil
	}

	total := 0
	var matched []string

	// Performers: a cast member whose every name token appears in the release is
	// a strong signal; a single shared surname is only a hint.
	for _, member := range scene.Cast {
		memberTokens := significantTokens(member)
		if len(memberTokens) == 0 {
			continue
		}
		all := true
		any := false
		for _, tok := range memberTokens {
			if tokens[tok] {
				any = true
			} else {
				all = false
			}
		}
		switch {
		case all && len(memberTokens) >= 2:
			total += 3
			matched = append(matched, member)
		case all && len(memberTokens) == 1 && len(memberTokens[0]) >= 5:
			total += 2
			matched = append(matched, member)
		case any:
			total += 1
			matched = append(matched, member)
		}
	}
	if total > 6 {
		total = 6
	}

	// Title: releases that name the scene instead of its cast. Scored on the
	// fraction of the title's significant words that appear.
	titleTokens := significantTokens(scene.Title)
	if len(titleTokens) >= 2 {
		found := 0
		for _, tok := range titleTokens {
			if tokens[tok] {
				found++
			}
		}
		fraction := float64(found) / float64(len(titleTokens))
		switch {
		case fraction >= 0.99:
			total += 3
			matched = append(matched, "title")
		case fraction >= 0.6:
			total += 2
			matched = append(matched, "title")
		case fraction >= 0.34:
			total += 1
			matched = append(matched, "title")
		}
	}

	if total > 8 {
		total = 8
	}
	return total, matched
}

// relTokenSet is the set of significant tokens in a release's name section.
func relTokenSet(rel Release) map[string]bool {
	out := map[string]bool{}
	for _, p := range rel.Performers {
		for _, tok := range significantTokens(p) {
			out[tok] = true
		}
	}
	return out
}

// nameStopWords are words that carry no identifying weight in a scene or
// performer name.
var nameStopWords = map[string]bool{
	"the": true, "and": true, "with": true, "from": true, "of": true,
	"a": true, "an": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "her": true, "his": true, "my": true, "vs": true, "feat": true,
	"xxx": true, "com": true, "www": true, "part": true, "scene": true,
}

// significantTokens splits a name into comparable words, dropping the ones too
// short or too generic to identify anything.
func significantTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '\'' && r != '\u2019'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, "'\u2019")
		if len(f) < 2 || nameStopWords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}
