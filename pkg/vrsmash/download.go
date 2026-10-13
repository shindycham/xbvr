package vrsmash

import (
	"context"
	"strings"

	"github.com/xbapps/xbvr/pkg/downloadfile"
)

func downloadHeaders() (map[string]string, error) {
	headers := map[string]string{
		"User-Agent": UserAgent,
		"Referer":    "https://www.vrsmash.com/",
	}

	// The paid URLs carry their own `secure` token, so credentials are not
	// strictly required to transfer the bytes. A missing cookie source (for
	// example no Firefox installed and no collector config) must not block a
	// download.
	if jar, err := jarFn(); err == nil && len(jar) > 0 {
		parts := make([]string, 0, len(jar))
		for name, value := range jar {
			parts = append(parts, name+"="+value)
		}
		headers["Cookie"] = strings.Join(parts, "; ")
	}
	if atoken, err := currentAccessToken(); err == nil && atoken != "" {
		headers["Authorization"] = "Bearer " + atoken
	}
	return headers, nil
}

// DownloadFile downloads a VRsMash source, sending the session cookies and the
// bearer token required by the CDN.
func DownloadFile(ctx context.Context, url, dest string, progress downloadfile.ProgressFunc) (int64, error) {
	headers, err := downloadHeaders()
	if err != nil {
		return 0, err
	}
	return downloadfile.Download(ctx, url, dest, headers, progress)
}
