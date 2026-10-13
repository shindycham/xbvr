// Package downloadfile implements a resumable HTTP download used by the
// scene downloaders. It streams to <name>.part via HTTP Range and renames on
// completion, so an interrupted transfer resumes instead of restarting.
package downloadfile

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ProgressFunc receives cumulative bytes written and the total size when known
// (0 when the server does not report a Content-Length).
type ProgressFunc func(done, total int64)

var client = &http.Client{Timeout: 0} // no overall timeout; large files

// Download fetches url to dest, resuming from dest+".part" if present. Headers
// are applied to every request (user-agent, cookies, authorization, ...).
func Download(ctx context.Context, url, dest string, headers map[string]string, progress ProgressFunc) (int64, error) {
	const retries = 5

	part := dest + ".part"
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}

	for attempt := 1; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		var existing int64
		if info, err := os.Stat(part); err == nil {
			existing = info.Size()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if existing > 0 {
			req.Header.Set("Range", "bytes="+strconv.FormatInt(existing, 10)+"-")
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			if attempt == retries {
				return 0, fmt.Errorf("network error: %w", err)
			}
			if err := sleepBackoff(ctx, attempt); err != nil {
				return 0, err
			}
			continue
		}

		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && existing > 0 {
			resp.Body.Close()
			break // already fully downloaded
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			if attempt == retries {
				return 0, fmt.Errorf("HTTP %d downloading %s", resp.StatusCode, url)
			}
			if err := sleepBackoff(ctx, attempt); err != nil {
				return 0, err
			}
			continue
		}

		// The server ignored our Range header: restart cleanly.
		if existing > 0 && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			_ = os.Remove(part)
			continue
		}

		done := existing
		if resp.StatusCode != http.StatusPartialContent {
			done = 0
		}
		var length int64
		if resp.ContentLength > 0 {
			length = resp.ContentLength
		}
		total := done + length

		err = writeToFile(ctx, part, resp.Body, resp.StatusCode == http.StatusPartialContent, done, total, progress)
		resp.Body.Close()

		if err != nil {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			if attempt == retries {
				return done, err
			}
			if err := sleepBackoff(ctx, attempt); err != nil {
				return done, err
			}
			continue
		}

		if total > 0 && done < total {
			continue // short read; loop resumes from bytes on disk
		}

		if err := os.Rename(part, dest); err != nil {
			return done, err
		}
		return done, nil
	}

	if info, err := os.Stat(part); err == nil && info.Size() > 0 {
		if err := os.Rename(part, dest); err != nil {
			return info.Size(), err
		}
		return info.Size(), nil
	}
	return 0, fmt.Errorf("download did not complete")
}

func writeToFile(ctx context.Context, path string, r io.Reader, appendMode bool, done, total int64, progress ProgressFunc) error {
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 256*1024)
	lastReport := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			if progress != nil && (time.Since(lastReport) > 500*time.Millisecond || (total > 0 && done >= total)) {
				lastReport = time.Now()
				progress(done, total)
			}
		}
		if readErr == io.EOF {
			if progress != nil {
				progress(done, total)
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func sleepBackoff(ctx context.Context, attempt int) error {
	delay := time.Duration(1<<uint(attempt)) * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
