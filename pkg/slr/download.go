package slr

import (
	"context"

	"github.com/xbapps/xbvr/pkg/downloadfile"
)

// ProgressFunc is the shared download progress callback.
type ProgressFunc = downloadfile.ProgressFunc

func downloadHeaders() map[string]string {
	headers := map[string]string{"User-Agent": UserAgent}
	for k, v := range ExtraHeaders() {
		headers[k] = v
	}
	return headers
}

// DownloadFile downloads url to dest, resuming from dest+".part" if present.
func DownloadFile(ctx context.Context, url, dest string, progress ProgressFunc) (int64, error) {
	return downloadfile.Download(ctx, url, dest, downloadHeaders(), progress)
}
