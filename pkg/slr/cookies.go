// Package slr implements downloading full-length videos from sexlikereal.com
// from inside xbvr.
//
// SexLikeReal does not use DRM or bot fingerprinting on scene pages. Full
// videos are gated by two things:
//
//  1. An age-verification + login cookie. Without it, scene URLs redirect to
//     /all-age. The single load-bearing cookie is `refresh_jwt`.
//  2. CloudFront *signed* URLs that the server-rendered payload already
//     contains. These expire, so the page/files payload must be fetched and
//     downloaded in the same session.
//
// Cookies are resolved from xbvr's per-domain collector config (the same
// mechanism used for scrapers), falling back to a local Firefox profile - see
// CookieHeader.
package slr

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/scrape"

	_ "github.com/mattn/go-sqlite3"
)

const (
	// DefaultCookieConfigKey is the collector-config key users edit in
	// Options -> Advanced to paste their sexlikereal.com cookies.
	DefaultCookieConfigKey = "sexlikereal.com-downloader"

	// UserAgent mirrors the Firefox user-agent used by the reference
	// slr-downloader so the site is served the same payload.
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:135.0) " +
		"Gecko/20100101 Firefox/135.0"

	cookieDomain = "sexlikereal.com"
)

// Regexes for parsing Firefox's profiles.ini. Compiled once: Go's regexp (RE2)
// has no lookahead, so section splitting is done by splitIniSections below.
var (
	iniDefaultSectionRE = regexp.MustCompile(`(?m)^Default=1\s*$`)
	iniPathRE           = regexp.MustCompile(`(?m)^Path=(.+)$`)
	iniDefaultRE        = regexp.MustCompile(`(?m)^Default=(.+)$`)
)

// splitIniSections splits an INI file into sections, each starting with '['.
// The reference Python implementation used re.split(r"(?=\[)"), but RE2 does
// not support lookahead; using MustCompile with that pattern panics at runtime.
func splitIniSections(data string) []string {
	var sections []string
	for _, chunk := range strings.Split(data, "[") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		sections = append(sections, "["+chunk)
	}
	return sections
}

// CookieConfigKey returns the configured collector-config key holding the SLR
// cookies, defaulting to DefaultCookieConfigKey.
func CookieConfigKey() string {
	key := strings.TrimSpace(config.Config.SLRDownloader.CookieConfigKey)
	if key == "" {
		return DefaultCookieConfigKey
	}
	return key
}

// CookieHeader builds a `Cookie:` header value for sexlikereal.com.
//
// Resolution order:
//  1. Cookies stored in xbvr's collector config (Options -> Advanced). This is
//     the only option that works on a headless server.
//  2. If the config has no cookies and UseFirefoxCookies is enabled, a local
//     Firefox profile is read (matching the standalone slr-downloader).
func CookieHeader() (string, error) {
	if header := cookieHeaderFromConfig(); header != "" {
		return header, nil
	}

	if config.Config.SLRDownloader.UseFirefoxCookies {
		if header, err := cookieHeaderFromFirefox(); err == nil && header != "" {
			return header, nil
		}
	}

	return "", fmt.Errorf(
		"no sexlikereal.com cookies found. Add a `%s` collector config with your `refresh_jwt` cookie in Options -> Advanced, or log into sexlikereal.com in Firefox on this machine",
		CookieConfigKey())
}

// cookieHeaderFromConfig reads cookies from xbvr's per-domain collector config.
func cookieHeaderFromConfig() string {
	conf := scrape.GetScrapeHttpConfig(CookieConfigKey())
	parts := make([]string, 0, len(conf.Cookies))
	for _, c := range conf.Cookies {
		name := strings.TrimSpace(c.Name)
		if name == "" || c.Value == "" {
			continue
		}
		parts = append(parts, name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// ExtraHeaders returns any configured headers for the SLR downloader config, so
// users can override the user-agent or add site-specific headers.
func ExtraHeaders() map[string]string {
	conf := scrape.GetScrapeHttpConfig(CookieConfigKey())
	headers := make(map[string]string, len(conf.Headers))
	for _, h := range conf.Headers {
		if strings.TrimSpace(h.Key) != "" {
			headers[h.Key] = h.Value
		}
	}
	return headers
}

// --------------------------------------------------------------------------- //
// Firefox cookie extraction
// --------------------------------------------------------------------------- //

type firefoxCookie struct {
	Name   string
	Value  string
	Expiry float64
}

func firefoxBases() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Firefox")}
	case "windows":
		appdata := os.Getenv("APPDATA")
		if appdata == "" {
			return nil
		}
		return []string{filepath.Join(appdata, "Mozilla", "Firefox")}
	default:
		return []string{
			filepath.Join(home, ".mozilla", "firefox"),
			filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
			filepath.Join(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox"),
		}
	}
}

// FindFirefoxCookieDB locates cookies.sqlite for the default Firefox profile.
// It mirrors the profile discovery of the standalone slr-downloader.
func FindFirefoxCookieDB() (string, error) {
	var searched []string
	for _, base := range firefoxBases() {
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			continue
		}
		searched = append(searched, base)

		ini := filepath.Join(base, "profiles.ini")
		if data, err := os.ReadFile(ini); err == nil {
			sections := splitIniSections(string(data))

			// Linux/Windows style: [ProfileN] with Default=1 and Path=...
			for _, section := range sections {
				if !iniDefaultSectionRE.MatchString(section) {
					continue
				}
				if m := iniPathRE.FindStringSubmatch(section); m != nil {
					if db, ok := existingCookieDB(filepath.Join(base, strings.TrimSpace(m[1]))); ok {
						return db, nil
					}
				}
			}
			// macOS style: [InstallXXX] with Default=<profile dir>
			for _, section := range sections {
				if !strings.HasPrefix(section, "[Install") || !strings.Contains(section, "Default=") {
					continue
				}
				if m := iniDefaultRE.FindStringSubmatch(section); m != nil {
					if db, ok := existingCookieDB(filepath.Join(base, strings.TrimSpace(m[1]))); ok {
						return db, nil
					}
				}
			}
		}

		// Fall back to any profile holding a cookie database.
		var matches []string
		for _, pattern := range []string{
			filepath.Join(base, "Profiles", "*", "cookies.sqlite"),
			filepath.Join(base, "*", "cookies.sqlite"),
		} {
			found, _ := filepath.Glob(pattern)
			matches = append(matches, found...)
		}
		if len(matches) > 0 {
			sort.SliceStable(matches, func(i, j int) bool {
				return strings.Contains(matches[i], "default-release") && !strings.Contains(matches[j], "default-release")
			})
			return matches[0], nil
		}
	}

	if len(searched) == 0 {
		searched = firefoxBases()
	}
	return "", fmt.Errorf("could not find a Firefox cookie database (looked in %s)", strings.Join(searched, ", "))
}

func existingCookieDB(profileDir string) (string, bool) {
	db := filepath.Join(profileDir, "cookies.sqlite")
	if info, err := os.Stat(db); err == nil && !info.IsDir() {
		return db, true
	}
	return "", false
}

// cookieHeaderFromFirefox reads unencrypted cookie values from the local
// Firefox profile. The database is copied first because Firefox locks it while
// running.
func cookieHeaderFromFirefox() (string, error) {
	db, err := FindFirefoxCookieDB()
	if err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "xbvr-slrck")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	tmp := filepath.Join(tmpDir, "cookies.sqlite")
	if err := copyFile(db, tmp); err != nil {
		return "", fmt.Errorf("copying Firefox cookie database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(db + suffix); err == nil {
			_ = copyFile(db+suffix, tmp+suffix)
		}
	}

	con, err := sql.Open("sqlite3", "file:"+tmp+"?mode=ro&immutable=1")
	if err != nil {
		return "", err
	}
	defer con.Close()

	rows, err := con.Query(
		"SELECT name, value, expiry FROM moz_cookies WHERE host LIKE ?",
		"%"+cookieDomain+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	now := time.Now().Unix()
	jar := map[string]string{}
	var order []string
	for rows.Next() {
		var c firefoxCookie
		if err := rows.Scan(&c.Name, &c.Value, &c.Expiry); err != nil {
			return "", err
		}
		// Firefox stores expiry in seconds on older builds and milliseconds on
		// newer ones; normalise to seconds before checking expiry.
		expiry := c.Expiry
		if expiry > 1e10 {
			expiry /= 1000
		}
		if expiry > 0 && int64(expiry) < now {
			continue
		}
		if _, seen := jar[c.Name]; !seen {
			order = append(order, c.Name)
		}
		jar[c.Name] = c.Value
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	if len(order) == 0 {
		return "", fmt.Errorf("no sexlikereal.com cookies found in the Firefox profile")
	}

	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, name+"="+jar[name])
	}
	return strings.Join(parts, "; "), nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
