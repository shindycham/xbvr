package slr

import (
	"regexp"
	"sync"
	"time"
)

// Quota is one download allowance bucket (used / limit / reset time).
type Quota struct {
	Used      int       `json:"used"`
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetsAt  time.Time `json:"resets_at"`
}

// QuotaInfo is the download allowance reported by the SexLikeReal session.
type QuotaInfo struct {
	Scene     Quota     `json:"scene_downloads"`
	Script    Quota     `json:"script_downloads"`
	FetchedAt time.Time `json:"fetched_at"`
	Error     string    `json:"error,omitempty"`
}

const (
	quotaPageURL = "https://www.sexlikereal.com/"
	quotaTTL     = 5 * time.Minute
)

var (
	countRE  = regexp.MustCompile(`"count":\[\d+,(\d+)\]`)
	limitRE  = regexp.MustCompile(`"limit":\[\d+,(\d+)\]`)
	backInRE = regexp.MustCompile(`"backIn":\[\d+,(\d+)\]`)
)

var quotaCache struct {
	mu   sync.Mutex
	info QuotaInfo
	at   time.Time
}

// FetchQuota returns the account's remaining download allowance, cached for a
// few minutes so the Downloads view does not hammer the site. Pass force to
// bypass the cache.
func FetchQuota(force bool) QuotaInfo {
	quotaCache.mu.Lock()
	defer quotaCache.mu.Unlock()

	if !force && quotaCache.info.Error == "" && time.Since(quotaCache.at) < quotaTTL {
		return quotaCache.info
	}

	info := fetchQuotaUncached()
	// Keep serving the last good value if a refresh fails.
	if info.Error != "" && quotaCache.info.Error == "" && !quotaCache.at.IsZero() {
		stale := quotaCache.info
		stale.Error = info.Error
		quotaCache.at = time.Now()
		return stale
	}

	quotaCache.info = info
	quotaCache.at = time.Now()
	return info
}

func fetchQuotaUncached() QuotaInfo {
	now := time.Now()

	cookie, err := CookieHeader()
	if err != nil {
		return QuotaInfo{FetchedAt: now, Error: err.Error()}
	}

	body, err := doGet(quotaPageURL, cookie, map[string]string{
		"Accept-Language": "en-US,en;q=0.5",
	})
	if err != nil {
		return QuotaInfo{FetchedAt: now, Error: err.Error()}
	}

	payload := unescapePayload(body)

	scene, ok := extractQuota(payload, "sceneDownloadLimits")
	if !ok {
		return QuotaInfo{FetchedAt: now, Error: "could not find download limits on the SexLikeReal page (session expired or page layout changed)"}
	}
	script, _ := extractQuota(payload, "scriptDownloadLimits")

	return QuotaInfo{Scene: scene, Script: script, FetchedAt: now}
}

// extractQuota reads an Astro-serialised limits object such as:
//
//	"sceneDownloadLimits":[0,{"count":[0,19],"limit":[0,35],"backIn":[0,1791849600]}]
func extractQuota(payload, key string) (Quota, bool) {
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `":\[\d+,\{([^}]*)\}\]`)
	m := re.FindStringSubmatch(payload)
	if m == nil {
		return Quota{}, false
	}
	inner := m[1]

	q := Quota{
		Used:  firstInt(inner, countRE),
		Limit: firstInt(inner, limitRE),
	}
	if ts := firstInt64(inner, backInRE); ts > 0 {
		q.ResetsAt = time.Unix(ts, 0)
	}
	if q.Limit > 0 {
		q.Remaining = q.Limit - q.Used
		if q.Remaining < 0 {
			q.Remaining = 0
		}
	}
	return q, true
}

func firstInt(s string, re *regexp.Regexp) int {
	if m := re.FindStringSubmatch(s); m != nil {
		var v int
		for _, r := range m[1] {
			v = v*10 + int(r-'0')
		}
		return v
	}
	return 0
}

func firstInt64(s string, re *regexp.Regexp) int64 {
	if m := re.FindStringSubmatch(s); m != nil {
		var v int64
		for _, r := range m[1] {
			v = v*10 + int64(r-'0')
		}
		return v
	}
	return 0
}
