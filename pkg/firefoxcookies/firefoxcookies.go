// Package firefoxcookies reads cookies from a local Firefox profile.
//
// Firefox stores cookie values unencrypted in cookies.sqlite, so a read-only
// sqlite connection is enough. The database is copied first because Firefox
// locks it while running.
package firefoxcookies

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

	_ "github.com/mattn/go-sqlite3"
)

// Regexes for parsing Firefox's profiles.ini. Compiled once: Go's regexp (RE2)
// has no lookahead, so section splitting is done by SplitIniSections below.
var (
	iniDefaultSectionRE = regexp.MustCompile(`(?m)^Default=1\s*$`)
	iniPathRE           = regexp.MustCompile(`(?m)^Path=(.+)$`)
	iniDefaultRE        = regexp.MustCompile(`(?m)^Default=(.+)$`)
)

type cookie struct {
	Name   string
	Value  string
	Expiry float64
}

func bases() []string {
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

// SplitIniSections splits an INI file into sections, each starting with '['.
// A lookahead regex (as used by the reference Python tool) is not supported by
// RE2 and would panic in MustCompile.
func SplitIniSections(data string) []string {
	var sections []string
	for _, chunk := range strings.Split(data, "[") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		sections = append(sections, "["+chunk)
	}
	return sections
}

// FindCookieDB locates cookies.sqlite for the default Firefox profile.
func FindCookieDB() (string, error) {
	var searched []string
	for _, base := range bases() {
		if info, err := os.Stat(base); err != nil || !info.IsDir() {
			continue
		}
		searched = append(searched, base)

		ini := filepath.Join(base, "profiles.ini")
		if data, err := os.ReadFile(ini); err == nil {
			sections := SplitIniSections(string(data))

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
		searched = bases()
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

// Jar returns non-expired cookies whose host contains domain, plus a ready-made
// Cookie header value. The map preserves last-write-wins semantics per name.
func Jar(domain string) (map[string]string, string, error) {
	db, err := FindCookieDB()
	if err != nil {
		return nil, "", err
	}

	tmpDir, err := os.MkdirTemp("", "xbvr-ffck")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmpDir)

	tmp := filepath.Join(tmpDir, "cookies.sqlite")
	if err := copyFile(db, tmp); err != nil {
		return nil, "", fmt.Errorf("copying Firefox cookie database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(db + suffix); err == nil {
			_ = copyFile(db+suffix, tmp+suffix)
		}
	}

	con, err := sql.Open("sqlite3", "file:"+tmp+"?mode=ro&immutable=1")
	if err != nil {
		return nil, "", err
	}
	defer con.Close()

	rows, err := con.Query(
		"SELECT name, value, expiry FROM moz_cookies WHERE host LIKE ?",
		"%"+domain+"%")
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	now := time.Now().Unix()
	jar := map[string]string{}
	var order []string
	for rows.Next() {
		var c cookie
		if err := rows.Scan(&c.Name, &c.Value, &c.Expiry); err != nil {
			return nil, "", err
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
		return nil, "", err
	}

	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, name+"="+jar[name])
	}
	return jar, strings.Join(parts, "; "), nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}
