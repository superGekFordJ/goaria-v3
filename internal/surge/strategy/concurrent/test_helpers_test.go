package concurrent

import (
	"context"
	"testing"
	"time"
)

// downloadWithTimeout runs Download on a background goroutine and fails the
// test if it does not return within timeout (guards against tarpit hangs).
func downloadWithTimeout(t *testing.T, d *ConcurrentDownloader, ctx context.Context, url, destPath string, fileSize int64, activeMirrors []string, timeout time.Duration) error {
	t.Helper()
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		done <- result{err: d.Download(ctx, url, nil, activeMirrors, destPath, fileSize)}
	}()
	select {
	case r := <-done:
		return r.err
	case <-time.After(timeout):
		t.Fatalf("Download did not complete within %v", timeout)
		return nil
	}
}
