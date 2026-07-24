package concurrent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
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

	// Handler must serve real data so the download can complete; tracking-only
	// handlers cause early-EOF after the early-EOF guard lands.
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

	state := progress.New("prewarm-test", fileSize)
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

// parseSimpleRange parses "bytes=start-end" (optional open end).
func parseSimpleRange(rangeHeader string, fileSize int64) (int64, int64, bool) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	if parts[1] == "" {
		if start >= fileSize {
			return 0, 0, false
		}
		return start, fileSize - 1, true
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start || end >= fileSize {
		return 0, 0, false
	}
	return start, end, true
}
