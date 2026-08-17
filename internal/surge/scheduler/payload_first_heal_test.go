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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

const healHeaderName = "X-Goaria-Heal"

func healRuntime() *types.RuntimeConfig {
	return &types.RuntimeConfig{
		MaxConnectionsPerDownload: 4,
		Workers:                   4,
		MinChunkSize:              64 * 1024,
		WorkerBufferSize:          32 * 1024,
		DialHedgeCount:            4,
		MaxTaskRetries:            1,
	}
}

type healProbe struct {
	mu          sync.Mutex
	zeroZero    int64
	payload     int64
	ranges      []string
	headerHits  int64
	retryMode   types.RangeAcquisitionMode
	retrySize   int64
	retrySnap   bool
	skipOnRetry bool
}

func (p *healProbe) note(r *http.Request, id string) {
	rangeHdr := r.Header.Get("Range")
	if rangeHdr == "bytes=0-0" {
		atomic.AddInt64(&p.zeroZero, 1)
	} else if strings.HasPrefix(rangeHdr, "bytes=") {
		n := atomic.AddInt64(&p.payload, 1)
		p.mu.Lock()
		p.ranges = append(p.ranges, rangeHdr)
		p.mu.Unlock()
		if r.Header.Get(healHeaderName) == "1" {
			atomic.AddInt64(&p.headerHits, 1)
		}
		if n >= 2 && !p.retrySnap {
			rec, err := store.GetDownload(id)
			if err == nil && rec != nil {
				p.mu.Lock()
				p.retryMode = rec.RangeAcquisitionMode
				p.retrySize = rec.TotalSize
				p.skipOnRetry = rec.SkipServerProbe
				p.retrySnap = true
				p.mu.Unlock()
			}
		}
	}
}

func newHealCfg(t *testing.T, tmpDir, id, url string, destPath string, fileSize int64) *types.DownloadRecord {
	t.Helper()
	if f, err := os.Create(destPath + types.IncompleteSuffix); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:                   id,
		URL:                  url,
		URLHash:              store.URLHash(url),
		DestPath:             destPath,
		Filename:             filepath.Base(destPath),
		Status:               "downloading",
		TotalSize:            fileSize,
		RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown,
		SkipServerProbe:      true,
	})
	return &types.DownloadRecord{
		ID:                   id,
		URL:                  url,
		OutputPath:           tmpDir,
		Filename:             filepath.Base(destPath),
		DestPath:             destPath,
		TotalSize:            fileSize,
		RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown,
		SkipServerProbe:      true,
		Headers:              map[string]string{healHeaderName: "1"},
		ProgressState:        progress.New(id, fileSize),
		Runtime:              healRuntime(),
	}
}

func TestRunDownload_Heal206TotalSuccess(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	trusted := int64(256 * 1024)
	observed := int64(128 * 1024)
	blob := make([]byte, observed)
	for i := range blob {
		blob[i] = byte(i)
	}
	id := "pf-heal-206"
	probe := &healProbe{}
	var first atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.note(r, id)
		start, end := int64(0), observed-1
		rangeHdr := r.Header.Get("Range")
		if strings.HasPrefix(rangeHdr, "bytes=") {
			_, _ = fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end)
		}
		if first.CompareAndSwap(false, true) {
			if end >= trusted {
				end = trusted - 1
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, observed))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
			w.WriteHeader(http.StatusPartialContent)
			return
		}
		if end >= observed {
			end = observed - 1
		}
		if start < 0 {
			start = 0
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, observed))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(blob[start : end+1])
	}))
	t.Cleanup(server.Close)

	destPath := filepath.Join(tmpDir, "heal206.bin")
	cfg := newHealCfg(t, tmpDir, id, server.URL, destPath, trusted)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := RunDownload(ctx, cfg); err != nil {
		t.Fatalf("RunDownload: %v", err)
	}
	if atomic.LoadInt64(&probe.zeroZero) != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", probe.zeroZero)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.ranges) < 2 {
		t.Fatalf("payload ranges = %v, want at least first+retry", probe.ranges)
	}
	if probe.ranges[0] == "bytes=0-0" || !strings.HasPrefix(probe.ranges[0], "bytes=0-") {
		t.Fatalf("first Range = %q", probe.ranges[0])
	}
	if probe.ranges[1] == "bytes=0-0" || !strings.HasPrefix(probe.ranges[1], "bytes=0-") {
		t.Fatalf("retry Range = %q", probe.ranges[1])
	}
	if !probe.retrySnap {
		t.Fatal("missing retry-GET master snapshot")
	}
	if probe.retrySize != observed {
		t.Fatalf("retry TotalSize = %d, want %d", probe.retrySize, observed)
	}
	if probe.retryMode != types.RangeAcquirePayloadFirstUnknown {
		t.Fatalf("retry mode = %q, want payload_first_unknown", probe.retryMode)
	}
	if !probe.skipOnRetry {
		t.Fatal("SkipServerProbe lost before retry persist")
	}
	if !cfg.SkipServerProbe {
		t.Fatal("SkipServerProbe must stay true")
	}
	if cfg.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("after success mode = %q, want range_supported", cfg.RangeAcquisitionMode)
	}
	if atomic.LoadInt64(&probe.headerHits) < 2 {
		t.Fatalf("custom header hits = %d, want >=2", probe.headerHits)
	}
}

func TestRunDownload_Heal416StarTotalSuccess(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	trusted := int64(256 * 1024)
	observed := int64(128 * 1024)
	blob := make([]byte, observed)
	id := "pf-heal-416"
	probe := &healProbe{}
	var first atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.note(r, id)
		if first.CompareAndSwap(false, true) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", observed))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start, end := int64(0), observed-1
		rangeHdr := r.Header.Get("Range")
		if strings.HasPrefix(rangeHdr, "bytes=") {
			_, _ = fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end)
		}
		if end >= observed {
			end = observed - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, observed))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(blob[start : end+1])
	}))
	t.Cleanup(server.Close)

	destPath := filepath.Join(tmpDir, "heal416.bin")
	cfg := newHealCfg(t, tmpDir, id, server.URL, destPath, trusted)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := RunDownload(ctx, cfg); err != nil {
		t.Fatalf("RunDownload: %v", err)
	}
	if atomic.LoadInt64(&probe.zeroZero) != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", probe.zeroZero)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if !probe.retrySnap {
		t.Fatal("missing retry-GET master snapshot")
	}
	if probe.retrySize != observed || probe.retryMode != types.RangeAcquirePayloadFirstUnknown {
		t.Fatalf("retry size=%d mode=%q", probe.retrySize, probe.retryMode)
	}
	if cfg.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("after success mode = %q", cfg.RangeAcquisitionMode)
	}
	if !cfg.SkipServerProbe {
		t.Fatal("SkipServerProbe must stay true")
	}
}

func TestRunDownload_HealOnceDifferentTotalIsTerminal(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	trusted := int64(256 * 1024)
	firstTotal := int64(128 * 1024)
	retryTotal := int64(64 * 1024)
	id := "pf-heal-once"
	probe := &healProbe{}
	var n atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.note(r, id)
		start, end := int64(0), int64(64*1024-1)
		rangeHdr := r.Header.Get("Range")
		if strings.HasPrefix(rangeHdr, "bytes=") {
			_, _ = fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end)
		}
		total := firstTotal
		if n.Add(1) > 1 {
			total = retryTotal
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
	}))
	t.Cleanup(server.Close)

	destPath := filepath.Join(tmpDir, "healonce.bin")
	cfg := newHealCfg(t, tmpDir, id, server.URL, destPath, trusted)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := RunDownload(ctx, cfg)
	if !errors.Is(err, types.ErrSourceMetadataMismatch) {
		t.Fatalf("err = %v, want mismatch", err)
	}
	if atomic.LoadInt64(&probe.payload) != 2 {
		t.Fatalf("payload GETs = %d, want 2 (no third storm)", probe.payload)
	}
	if atomic.LoadInt64(&probe.zeroZero) != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", probe.zeroZero)
	}
	if cfg.RangeAcquisitionMode == types.RangeAcquireRangeUnsupported {
		t.Fatal("healedOnce mismatch must not sequentialize")
	}
}

func TestRunDownload_200WrongLengthStaysTerminal(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	fileSize := int64(64 * 1024)
	id := "pf-200-cl"
	var zeroZero, payload atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			zeroZero.Add(1)
		} else if strings.HasPrefix(r.Header.Get("Range"), "bytes=") {
			payload.Add(1)
		}
		w.Header().Set("Content-Length", "12")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-the-size"))
	}))
	t.Cleanup(server.Close)

	destPath := filepath.Join(tmpDir, "wronglen.bin")
	cfg := newHealCfg(t, tmpDir, id, server.URL, destPath, fileSize)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := RunDownload(ctx, cfg)
	if !errors.Is(err, types.ErrSourceMetadataMismatch) {
		t.Fatalf("err = %v, want mismatch", err)
	}
	if payload.Load() != 1 {
		t.Fatalf("payload GETs = %d, want 1", payload.Load())
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
	master, getErr := store.GetDownload(id)
	if getErr != nil || master == nil {
		t.Fatalf("GetDownload: %v", getErr)
	}
	if master.RangeAcquisitionMode == types.RangeAcquireRangeUnsupported {
		t.Fatal("200_cl must not persist range_unsupported")
	}
	if master.TotalSize != fileSize {
		t.Fatalf("TotalSize rewritten to %d", master.TotalSize)
	}
	if cfg.RangeAcquisitionMode != types.RangeAcquirePayloadFirstUnknown {
		t.Fatalf("mode = %q, want payload_first_unknown", cfg.RangeAcquisitionMode)
	}
	info, statErr := os.Stat(destPath + types.IncompleteSuffix)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if info.Size() == 0 {
		t.Fatal("200_cl must not Truncate-to-sequential")
	}
}

func TestRunDownload_200MatchingSizeFallsBackToSingle(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	fileSize := int64(64 * 1024)
	blob := make([]byte, fileSize)
	id := "pf-200-match"
	var zeroZero atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			zeroZero.Add(1)
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(blob)
	}))
	t.Cleanup(server.Close)

	destPath := filepath.Join(tmpDir, "match200.bin")
	cfg := newHealCfg(t, tmpDir, id, server.URL, destPath, fileSize)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := RunDownload(ctx, cfg); err != nil {
		t.Fatalf("RunDownload: %v", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
	if cfg.RangeAcquisitionMode != types.RangeAcquireRangeUnsupported {
		t.Fatalf("mode = %q, want range_unsupported", cfg.RangeAcquisitionMode)
	}
}

func TestRunDownload_PersistThen206WrongTotalNoTruncate(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	fileSize := int64(256 * 1024)
	blob := make([]byte, fileSize)
	id := "pf-later-mismatch"
	var first atomic.Bool
	var zeroZero atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			zeroZero.Add(1)
		}
		start, end := int64(0), fileSize-1
		rangeHdr := r.Header.Get("Range")
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
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, 999999))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
	}))
	t.Cleanup(server.Close)

	destPath := filepath.Join(tmpDir, "later_mismatch.bin")
	cfg := newHealCfg(t, tmpDir, id, server.URL, destPath, fileSize)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := RunDownload(ctx, cfg)
	if !errors.Is(err, types.ErrSourceMetadataMismatch) {
		t.Fatalf("err = %v, want mismatch", err)
	}
	if cfg.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("mode = %q, want range_supported", cfg.RangeAcquisitionMode)
	}
	info, statErr := os.Stat(destPath + types.IncompleteSuffix)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if info.Size() == 0 {
		t.Fatal("working file was Truncate'd after range_supported")
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
}

func TestRunDownload_CanceledContextSkipsHeal(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	trusted := int64(256 * 1024)
	observed := int64(128 * 1024)
	id := "pf-heal-cancel"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var payload atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Range"), "bytes=") && r.Header.Get("Range") != "bytes=0-0" {
			payload.Add(1)
		}
		start, end := int64(0), int64(64*1024-1)
		rangeHdr := r.Header.Get("Range")
		if strings.HasPrefix(rangeHdr, "bytes=") {
			_, _ = fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end)
		}
		cancel()
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, observed))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.WriteHeader(http.StatusPartialContent)
	}))
	t.Cleanup(server.Close)

	destPath := filepath.Join(tmpDir, "healcancel.bin")
	cfg := newHealCfg(t, tmpDir, id, server.URL, destPath, trusted)
	err := RunDownload(ctx, cfg)
	if payload.Load() > 1 {
		t.Fatalf("payload GETs = %d, want no retry after cancel", payload.Load())
	}
	if err == nil {
		t.Fatal("expected cancel or mismatch, got nil")
	}
}
