package concurrent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/utils"
)

func TestConcurrentDownloader_PrewarmConnections(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	destPath := filepath.Join(tmpDir, "prewarm_test.bin")

	var mu sync.Mutex
	prewarmSeen := false
	downloadSeen := false

	// Create mock server to track request order. The handler must serve real
	// data so the download can complete; a handler that only tracks requests
	// without writing a body causes early-EOF errors after the Bug B guard.
	data := make([]byte, fileSize)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithHandler(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			rng := r.Header.Get("Range")
			if rng == "bytes=0-0" {
				prewarmSeen = true
			} else if rng != "" {
				downloadSeen = true
			}
			mu.Unlock()

			// Serve the requested range so the download completes.
			start := int64(0)
			end := fileSize - 1
			if rng != "" {
				var ok bool
				start, end, ok = parseSimpleRange(rng, fileSize)
				if !ok {
					http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
				w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
				w.WriteHeader(http.StatusPartialContent)
			} else {
				w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
				w.WriteHeader(http.StatusOK)
			}
			_, _ = w.Write(data[start : end+1])
		}),
	)
	defer server.Close()

	// Ensure incomplete file exists
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := types.NewProgressState("prewarm-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		DialHedgeCount:            2, // Enable hedging
		MinChunkSize:              256 * utils.KiB,
	}

	downloader := NewConcurrentDownloader("prewarm-id", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := downloader.Download(ctx, server.URL(), []string{server.URL()}, []string{server.URL()}, destPath, fileSize)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !prewarmSeen {
		t.Error("Expected to see pre-warm request (bytes=0-0), but none were recorded")
	}
	if !downloadSeen {
		t.Error("Expected to see download requests, but none were recorded")
	}
}
