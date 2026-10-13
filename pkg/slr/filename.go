package slr

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xbapps/xbvr/pkg/models"
)

// filenameReplacements mirrors the sanitisation the SLR scraper applies when it
// generates the filenames xbvr matches against.
var filenameReplacements = map[string]string{
	":": ";",
	"/": "⁄",
	"|": "¦",
	"*": "#",
	"?": "¿",
}

// ExpectedFilename returns the filename xbvr expects for this scene at the
// given per-eye height. Saving the download under this name lets xbvr's volume
// scan link the file to the scene automatically.
//
// It prefers a name already present in the scene's known-filenames list; if the
// list is unavailable it reconstructs the SLR naming convention.
func ExpectedFilename(scene models.Scene, height int, codec string) string {
	if name := knownFilename(scene, height); name != "" {
		return name
	}
	return fallbackFilename(scene, height)
}

func knownFilename(scene models.Scene, height int) string {
	if strings.TrimSpace(scene.FilenamesArr) == "" {
		return ""
	}
	var names []string
	if err := json.Unmarshal([]byte(scene.FilenamesArr), &names); err != nil {
		return ""
	}

	token := fmt.Sprintf("_%dp_", height)
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

func fallbackFilename(scene models.Scene, height int) string {
	title := scene.Title
	for old, replacement := range filenameReplacements {
		title = strings.ReplaceAll(title, old, replacement)
	}
	title = strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(title, " "))

	site := strings.TrimSuffix(scene.Site, " (SLR)")
	if site == "" {
		site = "SexLikeReal"
	}

	ref, err := GetSceneRef(scene)
	sceneID := scene.SceneID
	if err == nil {
		sceneID = ref.ID
	}

	return fmt.Sprintf("SLR_%s_%s_%dp_%s_LR.mp4", site, title, height, sceneID)
}
