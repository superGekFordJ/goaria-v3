package scheduler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func TestRunDownload_Later200DoesNotTruncateRangeSupported(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	fileSize := int64(256 * 1024)
	blob := make([]byte, fileSize)
	var first atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		start, end := int64(0), fileSize-1
		if strings.HasPrefix(rangeHdr, "bytes=") {
			_, _ = fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end)
		}
		if start == 0 && first.CompareAndSwap(false, true) {
			if end >= fileSize {
				end = fileSize - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(blob[start : end+1])
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(blob)
	}))
	t.Cleanup(server.Close)

	destPath := filepath.Join(tmpDir, "later200.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:                   "pf-sched",
		URL:                  server.URL,
		URLHash:              store.URLHash(server.URL),
		DestPath:             destPath,
		Filename:             filepath.Base(destPath),
		Status:               "downloading",
		TotalSize:            fileSize,
		RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown,
		SkipServerProbe:      true,
	})

	cfg := &types.DownloadRecord{
		ID:                   "pf-sched",
		URL:                  server.URL,
		OutputPath:           tmpDir,
		Filename:             filepath.Base(destPath),
		DestPath:             destPath,
		TotalSize:            fileSize,
		RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown,
		SkipServerProbe:      true,
		ProgressState:        progress.New("pf-sched", fileSize),
		Runtime: &types.RuntimeConfig{
			MaxConnectionsPerDownload: 4,
			Workers:                   4,
			MinChunkSize:              64 * 1024,
			WorkerBufferSize:          32 * 1024,
			DialHedgeCount:            4,
			MaxTaskRetries:            1,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := RunDownload(ctx, cfg)
	if !errors.Is(err, types.ErrRangeUnsupported) {
		t.Fatalf("err = %v, want ErrRangeUnsupported", err)
	}
	if cfg.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("mode = %q, want range_supported", cfg.RangeAcquisitionMode)
	}
	info, statErr := os.Stat(destPath + types.IncompleteSuffix)
	if statErr != nil {
		t.Fatalf("stat working file: %v", statErr)
	}
	if info.Size() == 0 {
		t.Fatal("working file was Truncate'd to 0")
	}
	saved, loadErr := store.LoadState(server.URL, destPath)
	if loadErr != nil || saved == nil {
		t.Fatalf("LoadState: %v", loadErr)
	}
	if saved.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("saved mode = %q, want range_supported", saved.RangeAcquisitionMode)
	}
	if len(saved.Tasks) == 0 && saved.Downloaded <= 0 {
		t.Fatal("snapshot must keep tasks or progress")
	}
	master, getErr := store.GetDownload("pf-sched")
	if getErr != nil || master == nil {
		t.Fatalf("GetDownload: %v", getErr)
	}
	if master.RangeAcquisitionMode == types.RangeAcquireRangeUnsupported {
		t.Fatal("master mode wiped to range_unsupported after later 200")
	}
}

func TestSaveStateWithOptions_EmptyModeDoesNotWipe(t *testing.T) {
	_ = testutil.SetupStateDB(t)
	url := "http://example.com/mode.bin"
	destPath := filepath.Join(t.TempDir(), "mode.bin")
	id := "mode-id"
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:                   id,
		URL:                  url,
		URLHash:              store.URLHash(url),
		DestPath:             destPath,
		Filename:             "mode.bin",
		Status:               "downloading",
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		SkipServerProbe:      true,
	})

	sparse := &types.DownloadRecord{
		ID:       id,
		URL:      url,
		DestPath: destPath,
		Filename: "mode.bin",
	}
	if err := store.SaveStateWithOptions(url, destPath, sparse, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}
	got, err := store.GetDownload(id)
	if err != nil || got == nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if got.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("mode = %q, want range_supported", got.RangeAcquisitionMode)
	}
	if !got.SkipServerProbe {
		t.Fatal("SkipServerProbe wiped by empty snapshot")
	}
}
