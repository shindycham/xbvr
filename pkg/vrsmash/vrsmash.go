// Package vrsmash implements scene discovery helpers and full-video downloads
// for vrsmash.com from inside xbvr.
//
// Authentication: the site uses short-lived JWTs. Requests need the atoken in
// an `Authorization: Bearer <atoken>` header, plus the Cloudflare cookies
// (cf_clearance/__cf_bm), so the whole cookie jar is sent. Cookies come from a
// collector config (Options -> Advanced) or a local Firefox profile. xbvr never
// refreshes the token server-side: if it has expired the user reloads
// vrsmash.com in their browser and xbvr picks up the fresh cookies.
package vrsmash

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xbapps/xbvr/pkg/config"
	"github.com/xbapps/xbvr/pkg/firefoxcookies"
	"github.com/xbapps/xbvr/pkg/models"
)

const (
	// DefaultCookieConfigKey is the collector-config key users edit in
	// Options -> Advanced to paste their vrsmash.com cookies.
	DefaultCookieConfigKey = "vrsmash.com-downloader"

	// UserAgent must look like the browser that obtained cf_clearance.
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:135.0) " +
		"Gecko/20100101 Firefox/135.0"

	cookieDomain = "vrsmash.com"
)

// apiBase and jarFn are variables so tests can point the provider at a local
// server and a fixed cookie jar.
var (
	apiBase = "https://www.vrsmash.com/api/content/v1"
	jarFn   = Jar
)

// Rendition is one downloadable quality.
type Rendition struct {
	Provider string `json:"provider"`
	Codec    string `json:"codec"`
	Quality  string `json:"quality"` // display label, e.g. "6K"
	Height   int    `json:"height"`  // per-eye height when known
	Width    int    `json:"width"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

// CookieConfigKey returns the configured collector-config key.
func CookieConfigKey() string {
	key := strings.TrimSpace(config.Config.VRSMashDownloader.CookieConfigKey)
	if key == "" {
		return DefaultCookieConfigKey
	}
	return key
}

type configCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// configCookies reads the collector config without importing pkg/scrape, which
// would create an import cycle (the scraper imports this package).
func configCookies() []configCookie {
	db, _ := models.GetCommonDB()
	if db == nil {
		return nil
	}
	var kv models.KV
	kv.Key = CookieConfigKey()
	db.Find(&kv)
	if kv.Value == "" {
		return nil
	}

	var parsed struct {
		Cookies []configCookie `json:"cookies"`
	}
	if err := json.Unmarshal([]byte(kv.Value), &parsed); err != nil {
		return nil
	}
	return parsed.Cookies
}

// Jar returns the vrsmash cookie jar from config first, then Firefox.
func Jar() (map[string]string, error) {
	jar := map[string]string{}
	for _, c := range configCookies() {
		if strings.TrimSpace(c.Name) != "" && c.Value != "" {
			jar[c.Name] = c.Value
		}
	}
	if len(jar) > 0 {
		return jar, nil
	}

	if !config.Config.VRSMashDownloader.UseFirefoxCookies {
		return nil, fmt.Errorf("no vrsmash.com cookies configured")
	}
	ffJar, _, err := firefoxcookies.Jar(cookieDomain)
	if err != nil {
		return nil, err
	}
	if len(ffJar) == 0 {
		return nil, fmt.Errorf("no vrsmash.com cookies found in the Firefox profile")
	}
	return ffJar, nil
}

// CookieHeader builds a Cookie header value for vrsmash.com.
func CookieHeader() (string, error) {
	jar, err := Jar()
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(jar))
	for name, value := range jar {
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; "), nil
}

var httpClient = &http.Client{Timeout: 60 * time.Second}

// apiGet performs an authenticated GET against the content API, refreshing the
// access token first when automatic refresh is enabled.
func apiGet(path string, out interface{}) error {
	atoken, err := currentAccessToken()
	if err != nil {
		return err
	}
	jar, _ := jarFn()

	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")
	for name, value := range jar {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	req.Header.Set("Authorization", "Bearer "+atoken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("vrsmash session expired (atoken). Open vrsmash.com in your browser, or enable 'keep the session alive' in Options -> VRSmash Downloader")
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("vrsmash blocked the request (HTTP 403, Cloudflare). Make sure Firefox is logged in and its cookies are fresh")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from vrsmash", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// --------------------------------------------------------------------------- //
// Scene parsing
// --------------------------------------------------------------------------- //

var slugRE = regexp.MustCompile(`/video/([^/?#]+)`)

// SceneSlug extracts the vrsmash slug from a scene URL.
func SceneSlug(scene models.Scene) (string, error) {
	if m := slugRE.FindStringSubmatch(scene.SceneURL); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("could not determine vrsmash slug for scene %q", scene.SceneID)
}

// IsScene reports whether a scene originates from vrsmash.com.
func IsScene(scene models.Scene) bool {
	if strings.Contains(strings.ToLower(scene.SceneURL), "vrsmash.com") {
		return true
	}
	return strings.HasPrefix(scene.SceneID, "vrsmash-")
}

type apiResponse struct {
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	Data struct {
		Item struct {
			ID         string                       `json:"id"`
			Name       string                       `json:"name"`
			Slug       string                       `json:"slug"`
			IsFree     bool                         `json:"isFreeVideo"`
			HideDl     bool                         `json:"isHideDownload"`
			Sources    map[string]map[string]source `json:"sources"`
			ShortVideo struct {
				Path string `json:"path"`
				Size int64  `json:"size"`
			} `json:"shortVideo"`
		} `json:"item"`
	} `json:"data"`
}

// source is one downloadable file in the API's sources object.
type source struct {
	Size int64  `json:"size"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// GetRenditions returns the downloadable full-video qualities for a scene.
//
// The paid sources require a valid session; without one the API only exposes
// trailers, and we report that clearly rather than downloading a preview.
func GetRenditions(scene models.Scene) ([]Rendition, error) {
	slug, err := SceneSlug(scene)
	if err != nil {
		return nil, err
	}

	var resp apiResponse
	if err := apiGet(apiBase+"/videos/"+slug, &resp); err != nil {
		return nil, err
	}
	return parseSources(resp.Data.Item.Sources)
}

// parseSources selects the full-video (paid) group when present, otherwise the
// free (trailer) group, which is reported as an authorisation error. The paid
// group is normally "paid", but any non-"free" group is treated as full video
// so a renamed group (e.g. "premium") still works.
func parseSources(sources map[string]map[string]source) ([]Rendition, error) {
	group := ""
	if qualities, ok := sources["paid"]; ok && len(qualities) > 0 {
		group = "paid"
	}
	if group == "" {
		for name, qualities := range sources {
			if name != "free" && len(qualities) > 0 {
				group = name
				break
			}
		}
	}
	if group == "" {
		if qualities, ok := sources["free"]; ok && len(qualities) > 0 {
			group = "free"
		}
	}

	var out []Rendition
	for _, q := range sources[group] {
		if q.Path == "" {
			continue
		}
		out = append(out, Rendition{
			Provider: "vrsmash",
			Codec:    "h264",
			Quality:  strings.ToUpper(q.Name),
			Height:   heightFromQuality(q.Name),
			Size:     q.Size,
			URL:      q.Path,
		})
	}

	if group == "" || len(out) == 0 {
		return nil, fmt.Errorf("no downloadable sources found for this scene (are you logged in and subscribed?)")
	}
	if group == "free" {
		return nil, fmt.Errorf("vrsmash returned only trailer sources; your session is not authorised for full downloads. Open vrsmash.com in your browser to refresh the session, then retry")
	}

	sortRenditions(out)
	return out, nil
}

// heightFromQuality maps vrsmash quality labels (4K/6K/8K) to a per-eye height
// used by xbvr for sorting and filenames.
func heightFromQuality(q string) int {
	switch strings.ToLower(strings.TrimSpace(q)) {
	case "8k":
		return 4320
	case "6k":
		return 3240
	case "5k":
		return 2700
	case "4k":
		return 2160
	case "2k":
		return 1440
	}
	return 0
}

func sortRenditions(rs []Rendition) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j-1].Height > rs[j].Height; j-- {
			rs[j-1], rs[j] = rs[j], rs[j-1]
		}
	}
}

// --------------------------------------------------------------------------- //
// Filenames
// --------------------------------------------------------------------------- //

var filenameReplacements = map[string]string{
	":": ";",
	"/": "⁄",
	"|": "¦",
	"*": "#",
	"?": "¿",
}

// FilenameQualities lists the quality tokens xbvr generates filenames for.
func FilenameQualities() []string {
	return []string{"4K", "5K", "6K", "8K"}
}

// BuildFilenameFrom constructs the canonical VRsMash filename from primitives,
// so the scraper and the downloader produce identical names.
func BuildFilenameFrom(site, title, slug, quality string) string {
	for old, replacement := range filenameReplacements {
		title = strings.ReplaceAll(title, old, replacement)
	}
	site = strings.TrimSuffix(site, " (VRSmash)")
	if site == "" {
		site = "VRSmash"
	}
	return fmt.Sprintf("VRsMash_%s_%s_%s_%s.mp4", site, title, sanitizeToken(quality), slug)
}

// ExpectedFilename returns the name xbvr expects for this scene at the given
// quality. The scraper generates the same names, so downloads auto-link on
// rescan.
func ExpectedFilename(scene models.Scene, quality string) string {
	if name := knownFilename(scene, quality); name != "" {
		return name
	}
	slug, err := SceneSlug(scene)
	if err != nil {
		slug = scene.SceneID
	}
	return BuildFilenameFrom(scene.Site, scene.Title, slug, quality)
}

func knownFilename(scene models.Scene, quality string) string {
	if strings.TrimSpace(scene.FilenamesArr) == "" {
		return ""
	}
	var names []string
	if err := json.Unmarshal([]byte(scene.FilenamesArr), &names); err != nil {
		return ""
	}
	token := "_" + sanitizeToken(quality) + "_"
	var mp4, any string
	for _, name := range names {
		if !strings.Contains(name, token) {
			continue
		}
		if strings.HasSuffix(strings.ToLower(name), ".mp4") {
			if mp4 == "" {
				mp4 = name
			}
		} else if any == "" {
			any = name
		}
	}
	if mp4 != "" {
		return mp4
	}
	return any
}

// BuildFilename constructs the canonical VRsMash filename for a scene.
func BuildFilename(scene models.Scene, quality string) string {
	title := scene.Title
	for old, replacement := range filenameReplacements {
		title = strings.ReplaceAll(title, old, replacement)
	}
	site := strings.TrimSuffix(scene.Site, " (VRSmash)")
	if site == "" {
		site = "VRSmash"
	}
	slug, err := SceneSlug(scene)
	if err != nil {
		slug = scene.SceneID
	}
	return fmt.Sprintf("VRsMash_%s_%s_%s_%s.mp4", site, title, sanitizeToken(quality), slug)
}

func sanitizeToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return "video"
	}
	return s
}

// DownloadDir returns the configured folder (shared with the SLR downloader
// unless a VRsMash-specific one is set).
func DownloadDir() string {
	dir := config.Config.VRSMashDownloader.DownloadDir
	if dir == "" {
		dir = config.Config.SLRDownloader.DownloadDir
	}
	if dir == "" {
		return ""
	}
	return filepath.Clean(os.ExpandEnv(dir))
}

// PreferredCodec returns the configured codec.
func PreferredCodec() string {
	if c := strings.TrimSpace(config.Config.VRSMashDownloader.PreferredCodec); c != "" {
		return c
	}
	return "h264"
}
