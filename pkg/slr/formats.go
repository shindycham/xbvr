package slr

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xbapps/xbvr/pkg/models"
)

// Rendition is one downloadable full-video quality.
type Rendition struct {
	Codec  string `json:"codec"`
	Height int    `json:"height"`
	Width  int    `json:"width"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
}

// fullVideoRE matches the signed full-video URLs embedded in the site payload.
// The resolution in the path is the per-eye height for VR scenes.
var fullVideoRE = regexp.MustCompile(`https://cdn-vr\.sexlikereal\.com/full_videos_app/(h26[45])/(\d+)_(\d+)p\.mp4(?:\?[^"'\s\\<>]*)?`)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// IsSLRScene reports whether a scene originates from sexlikereal.com.
func IsSLRScene(scene models.Scene) bool {
	lowerURL := strings.ToLower(scene.SceneURL)
	if strings.Contains(lowerURL, "sexlikereal.com") {
		return true
	}
	return strings.HasPrefix(scene.SceneID, "slr-")
}

// SceneRef describes how to talk to the SLR API for a scene.
type SceneRef struct {
	ID      string // numeric SLR scene id
	Project string // 1 = main, 3 = trans, 4 = gay
	PageURL string // human-facing scene URL
}

// GetSceneRef derives the API coordinates for a scene from its stored ids/URL.
func GetSceneRef(scene models.Scene) (SceneRef, error) {
	ref := SceneRef{Project: "1"}

	id := scene.SceneID
	switch {
	case strings.HasPrefix(id, "slr-gay-"):
		ref.Project = "4"
		id = strings.TrimPrefix(id, "slr-gay-")
	case strings.HasPrefix(id, "slr-trans-"):
		ref.Project = "3"
		id = strings.TrimPrefix(id, "slr-trans-")
	default:
		id = strings.TrimPrefix(id, "slr-")
	}
	if isNumeric(id) {
		ref.ID = id
	}

	if ref.ID == "" {
		// Fall back to the trailing id of the scene slug, e.g. "...-68823".
		m := regexp.MustCompile(`-(\d+)(?:[/?#]|$)`).FindStringSubmatch(scene.SceneURL)
		if m != nil {
			ref.ID = m[1]
		}
	}
	if ref.ID == "" {
		return ref, fmt.Errorf("could not determine sexlikereal scene id for %q", scene.SceneID)
	}

	ref.PageURL = scene.SceneURL
	if ref.PageURL == "" {
		ref.PageURL = sceneHomepageURL(ref)
	}
	return ref, nil
}

func sceneHomepageURL(ref SceneRef) string {
	prefix := "https://www.sexlikereal.com/"
	switch ref.Project {
	case "3":
		prefix += "trans/"
	case "4":
		prefix += "gay/"
	}
	return prefix + "scenes/" + ref.ID
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// GetRenditions returns the downloadable full-video qualities for a scene.
//
// The authenticated `/v3/scenes/<id>/files` API is tried first; if it does not
// expose full-video URLs, the server-rendered scene page is scraped exactly
// like the standalone slr-downloader does.
func GetRenditions(scene models.Scene) ([]Rendition, error) {
	if !IsSLRScene(scene) {
		return nil, fmt.Errorf("scene %q does not originate from sexlikereal.com", scene.SceneID)
	}

	ref, err := GetSceneRef(scene)
	if err != nil {
		return nil, err
	}

	cookie, cookieErr := CookieHeader()
	if cookie != "" {
		for _, project := range projectCandidates(ref.Project) {
			apiURL := "https://api.sexlikereal.com/v3/scenes/" + ref.ID + "/files"
			body, err := doGet(apiURL, cookie, map[string]string{
				"Client-Type": "web",
				"project":     project,
			})
			if err != nil {
				continue
			}
			if renditions := ParseRenditions(body); len(renditions) > 0 {
				return renditions, nil
			}
		}

		if body, err := doGet(ref.PageURL, cookie, map[string]string{
			"Accept-Language": "en-US,en;q=0.5",
		}); err == nil {
			if renditions := ParseRenditions(body); len(renditions) > 0 {
				return renditions, nil
			}
		} else if strings.Contains(err.Error(), "age gate") {
			return nil, err
		}
	}

	if cookieErr != nil {
		return nil, cookieErr
	}
	return nil, fmt.Errorf("no full-video renditions found. Either the sexlikereal.com session cookie is missing/expired, you are not subscribed to this scene, or the page layout changed")
}

func projectCandidates(project string) []string {
	if project == "1" {
		return []string{"1", "0"}
	}
	return []string{project, "1"}
}

// The payload embeds size/width just before each URL, as JS
// (`size:1113056466`) or JSON (`"size":1113056466`).
var (
	sizeRE  = regexp.MustCompile(`"?size"?\s*:\s*(\d+)`)
	widthRE = regexp.MustCompile(`"?width"?\s*:\s*(\d+)`)
)

// ParseRenditions extracts signed full-video URLs (with their sizes) from a
// page/API payload.
func ParseRenditions(payload string) []Rendition {
	payload = unescapePayload(payload)

	found := map[string]Rendition{}
	for _, idx := range fullVideoRE.FindAllStringSubmatchIndex(payload, -1) {
		codec := payload[idx[2]:idx[3]]
		heightStr := payload[idx[6]:idx[7]]
		height, err := strconv.Atoi(heightStr)
		if err != nil {
			continue
		}

		key := codec + ":" + heightStr
		if _, exists := found[key]; exists {
			continue
		}

		// Look back for the size/width of this source, which appear just
		// before the url in the same object.
		start := idx[0] - 400
		if start < 0 {
			start = 0
		}
		window := payload[start:idx[0]]

		width := lastInt(widthRE, window)
		if width == 0 {
			width = height * 2 // side-by-side VR frame
		}

		found[key] = Rendition{
			Codec:  codec,
			Height: height,
			Width:  width,
			Size:   int64(lastInt(sizeRE, window)),
			URL:    payload[idx[0]:idx[1]],
		}
	}

	out := make([]Rendition, 0, len(found))
	for _, r := range found {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Height != out[j].Height {
			return out[i].Height < out[j].Height
		}
		return out[i].Codec < out[j].Codec
	})
	return out
}

// lastInt returns the value of the last submatch in s, or 0.
func lastInt(re *regexp.Regexp, s string) int {
	matches := re.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return 0
	}
	v, err := strconv.Atoi(matches[len(matches)-1][1])
	if err != nil {
		return 0
	}
	return v
}

func unescapePayload(text string) string {
	text = html.UnescapeString(text)
	for _, repl := range [][2]string{
		{`\/`, `/`},
		{`\u0026`, `&`},
		{`\u003d`, `=`},
		{`\u003F`, `?`},
		{`\u002F`, `/`},
	} {
		text = strings.ReplaceAll(text, repl[0], repl[1])
	}
	return text
}

func doGet(url, cookie string, headers map[string]string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	for k, v := range ExtraHeaders() {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("HTTP %d from %s (check the sexlikereal.com session cookie)", resp.StatusCode, url)
	}
	if resp.Request != nil && resp.Request.URL != nil && strings.Contains(resp.Request.URL.Path, "/all-age") {
		return "", fmt.Errorf("redirected to the age gate (/all-age); the sexlikereal.com session cookie is missing or expired")
	}
	return string(body), nil
}

// SelectRendition picks the requested quality/codec, degrading gracefully the
// way the standalone downloader does.
func SelectRendition(renditions []Rendition, codec string, height int) (Rendition, error) {
	if len(renditions) == 0 {
		return Rendition{}, fmt.Errorf("no renditions available")
	}

	pool := make([]Rendition, 0, len(renditions))
	for _, r := range renditions {
		if r.Codec == codec {
			pool = append(pool, r)
		}
	}
	if len(pool) == 0 {
		pool = renditions
	}

	if height <= 0 {
		return highest(pool), nil
	}
	for _, r := range pool {
		if r.Height == height {
			return r, nil
		}
	}
	// Fall back to the closest lower quality, then the lowest available.
	best := Rendition{}
	for _, r := range pool {
		if r.Height < height && r.Height > best.Height {
			best = r
		}
	}
	if best.Height > 0 {
		return best, nil
	}
	return lowest(pool), nil
}

func highest(pool []Rendition) Rendition {
	best := pool[0]
	for _, r := range pool {
		if r.Height > best.Height {
			best = r
		}
	}
	return best
}

func lowest(pool []Rendition) Rendition {
	best := pool[0]
	for _, r := range pool {
		if r.Height < best.Height {
			best = r
		}
	}
	return best
}
