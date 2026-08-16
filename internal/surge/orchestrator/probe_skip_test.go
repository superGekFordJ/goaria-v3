package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	probing "goaria-v3/internal/surge/probe"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func boolPtr(v bool) *bool { return &v }

func newRangeProbeServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var probes atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			probes.Add(1)
			w.Header().Set("Content-Length", "1024")
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		// Fail payload GETs immediately so Enqueue tests can snapshot the
		// queued record without holding .surge open past cleanup.
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(ts.Close)
	return ts, &probes
}

func newSkipEnqueueManager(t *testing.T) (*LifecycleManager, *scheduler.Scheduler) {
	t.Helper()
	progressCh := make(chan types.DownloadEvent, 16)
	pool := scheduler.New(progressCh, 1)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	t.Cleanup(mgr.Shutdown)
	return mgr, pool
}

func recordByID(pool *scheduler.Scheduler, id string) (types.DownloadRecord, bool) {
	for _, cfg := range pool.GetAll() {
		if cfg.ID == id {
			return cfg, true
		}
	}
	return types.DownloadRecord{}, false
}

func drainProbeSem(mgr *LifecycleManager) int {
	n := 0
	for {
		select {
		case <-mgr.probeSem:
			n++
		default:
			return n
		}
	}
}

func restoreProbeSem(mgr *LifecycleManager, n int) {
	for i := 0; i < n; i++ {
		mgr.probeSem <- struct{}{}
	}
}

func cancelEnqueue(t *testing.T, mgr *LifecycleManager, id string) {
	t.Helper()
	if id == "" {
		return
	}
	t.Cleanup(func() { _ = mgr.Cancel(id) })
}

func TestEnqueue_SkipTrustedMetadataNoProbeGET(t *testing.T) {
	ts, probes := newRangeProbeServer(t)
	mgr, pool := newSkipEnqueueManager(t)
	defer mgr.Shutdown()
	destDir := t.TempDir()

	req := &DownloadRequest{
		URL:           ts.URL + "/skip.bin",
		Filename:      "skip.bin",
		Path:          destDir,
		FileSize:      1024,
		SupportsRange: boolPtr(false),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	id, finalName, err := mgr.Enqueue(ctx, req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	cancelEnqueue(t, mgr, id)
	if finalName != "skip.bin" {
		t.Fatalf("Filename = %q, want skip.bin", finalName)
	}
	if probes.Load() != 0 {
		t.Fatalf("Range bytes=0-0 probes = %d, want 0", probes.Load())
	}

	rec, ok := recordByID(pool, id)
	if !ok {
		t.Fatal("expected queued record")
	}
	if rec.TotalSize != 1024 {
		t.Fatalf("TotalSize = %d, want 1024", rec.TotalSize)
	}
	if rec.Filename != "skip.bin" {
		t.Fatalf("record Filename = %q, want skip.bin", rec.Filename)
	}
	if rec.SupportsRange {
		t.Fatal("SupportsRange = true, want false until first-shard verify")
	}
	if rec.RangeAcquisitionMode != types.RangeAcquirePayloadFirstUnknown {
		t.Fatalf("RangeAcquisitionMode = %q, want payload_first_unknown", rec.RangeAcquisitionMode)
	}
	if !rec.SkipServerProbe {
		t.Fatal("SkipServerProbe = false, want true")
	}

	surgePath := filepath.Join(destDir, finalName) + types.IncompleteSuffix
	if _, err := os.Stat(surgePath); err != nil {
		t.Fatalf("expected working file at %s: %v", surgePath, err)
	}
}

func TestEnqueue_SkipMissingFilenameStillProbes(t *testing.T) {
	ts, probes := newRangeProbeServer(t)
	mgr, _ := newSkipEnqueueManager(t)
	defer mgr.Shutdown()

	for _, name := range []string{"", "   "} {
		t.Run("filename="+name, func(t *testing.T) {
			probes.Store(0)
			req := &DownloadRequest{
				URL:           ts.URL + "/need-probe.bin",
				Filename:      name,
				Path:          t.TempDir(),
				FileSize:      4096,
				SupportsRange: boolPtr(false),
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			id, _, err := mgr.Enqueue(ctx, req)
			cancelEnqueue(t, mgr, id)
			if probes.Load() < 1 {
				t.Fatalf("Range bytes=0-0 probes = %d, want >= 1 (err=%v)", probes.Load(), err)
			}
		})
	}
}

func TestEnqueue_SkipFileSizeZeroStillProbes(t *testing.T) {
	ts, probes := newRangeProbeServer(t)
	mgr, _ := newSkipEnqueueManager(t)
	defer mgr.Shutdown()

	req := &DownloadRequest{
		URL:           ts.URL + "/zero.bin",
		Filename:      "zero.bin",
		Path:          t.TempDir(),
		FileSize:      0,
		SupportsRange: boolPtr(false),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if id, _, err := mgr.Enqueue(ctx, req); err != nil {
		t.Fatalf("Enqueue: %v", err)
	} else {
		cancelEnqueue(t, mgr, id)
	}
	if probes.Load() < 1 {
		t.Fatalf("Range bytes=0-0 probes = %d, want >= 1", probes.Load())
	}
}

func TestEnqueue_SkipNilRangeStillProbes(t *testing.T) {
	ts, probes := newRangeProbeServer(t)
	mgr, _ := newSkipEnqueueManager(t)
	defer mgr.Shutdown()

	req := &DownloadRequest{
		URL:      ts.URL + "/nil-range.bin",
		Filename: "nil-range.bin",
		Path:     t.TempDir(),
		FileSize: 4096,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if id, _, err := mgr.Enqueue(ctx, req); err != nil {
		t.Fatalf("Enqueue: %v", err)
	} else {
		cancelEnqueue(t, mgr, id)
	}
	if probes.Load() < 1 {
		t.Fatalf("Range bytes=0-0 probes = %d, want >= 1", probes.Load())
	}
}

func TestEnqueue_SkipKnownSizeIsPayloadFirstUnknown(t *testing.T) {
	ts, probes := newRangeProbeServer(t)
	mgr, pool := newSkipEnqueueManager(t)
	defer mgr.Shutdown()

	t.Run("false", func(t *testing.T) {
		req := &DownloadRequest{
			URL:           ts.URL + "/seq.bin",
			Filename:      "seq.bin",
			Path:          t.TempDir(),
			FileSize:      2048,
			SupportsRange: boolPtr(false),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		id, _, err := mgr.Enqueue(ctx, req)
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		cancelEnqueue(t, mgr, id)
		rec, ok := recordByID(pool, id)
		if !ok {
			t.Fatal("expected queued record")
		}
		if rec.SupportsRange {
			t.Fatal("SupportsRange = true, want false until verify")
		}
		if rec.RangeAcquisitionMode != types.RangeAcquirePayloadFirstUnknown {
			t.Fatalf("mode = %q, want payload_first_unknown", rec.RangeAcquisitionMode)
		}
		if !rec.SkipServerProbe {
			t.Fatal("SkipServerProbe = false, want true")
		}
		if probes.Load() != 0 {
			t.Fatalf("Range bytes=0-0 probes = %d, want 0", probes.Load())
		}
	})

	t.Run("true", func(t *testing.T) {
		probes.Store(0)
		req := &DownloadRequest{
			URL:           ts.URL + "/range.bin",
			Filename:      "range.bin",
			Path:          t.TempDir(),
			FileSize:      2048,
			SupportsRange: boolPtr(true),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		id, _, err := mgr.Enqueue(ctx, req)
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		cancelEnqueue(t, mgr, id)
		rec, ok := recordByID(pool, id)
		if !ok {
			t.Fatal("expected queued record")
		}
		if !rec.SupportsRange {
			t.Fatal("SupportsRange = false, want true")
		}
		if rec.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
			t.Fatalf("mode = %q, want range_supported", rec.RangeAcquisitionMode)
		}
		if probes.Load() != 0 {
			t.Fatalf("Range bytes=0-0 probes = %d, want 0", probes.Load())
		}
	})
}

func TestEnqueue_SkipDiskPrecheckReject(t *testing.T) {
	orig := freeDiskBytes
	t.Cleanup(func() { freeDiskBytes = orig })
	freeDiskBytes = func(string) (int64, error) {
		return utils.DiskSpaceSafetyBuffer, nil
	}

	ts, probes := newRangeProbeServer(t)
	mgr, _ := newSkipEnqueueManager(t)
	defer mgr.Shutdown()
	destDir := t.TempDir()

	req := &DownloadRequest{
		URL:           ts.URL + "/tight.bin",
		Filename:      "tight.bin",
		Path:          destDir,
		FileSize:      1024,
		SupportsRange: boolPtr(false),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := mgr.Enqueue(ctx, req)
	if !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("Enqueue error = %v, want ErrInsufficientDiskSpace", err)
	}
	if probes.Load() != 0 {
		t.Fatalf("Range bytes=0-0 probes = %d, want 0", probes.Load())
	}

	surgePath := filepath.Join(destDir, "tight.bin") + types.IncompleteSuffix
	if _, err := os.Stat(surgePath); !os.IsNotExist(err) {
		t.Fatalf("expected no .surge working file at %s, stat err=%v", surgePath, err)
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dest dir after reject, got %d entries", len(entries))
	}
}

func TestEnqueue_SkipPreservesAuthHeaders(t *testing.T) {
	ts, probes := newRangeProbeServer(t)
	mgr, pool := newSkipEnqueueManager(t)
	defer mgr.Shutdown()

	req := &DownloadRequest{
		URL:           ts.URL + "/auth.bin",
		Filename:      "auth.bin",
		Path:          t.TempDir(),
		FileSize:      1024,
		SupportsRange: boolPtr(false),
		Headers: map[string]string{
			"Cookie":        "sid=abc",
			"Authorization": "Bearer tok",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	id, _, err := mgr.Enqueue(ctx, req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	cancelEnqueue(t, mgr, id)
	if probes.Load() != 0 {
		t.Fatalf("Range bytes=0-0 probes = %d, want 0", probes.Load())
	}
	rec, ok := recordByID(pool, id)
	if !ok {
		t.Fatal("expected queued record")
	}
	if rec.Headers["Cookie"] != "sid=abc" {
		t.Fatalf("Cookie = %q, want sid=abc", rec.Headers["Cookie"])
	}
	if rec.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", rec.Headers["Authorization"])
	}
}

func TestEnqueue_SkipDoesNotTakeProbeSem(t *testing.T) {
	ts, _ := newRangeProbeServer(t)
	mgr, _ := newSkipEnqueueManager(t)
	defer mgr.Shutdown()

	drained := drainProbeSem(mgr)
	if drained == 0 {
		t.Fatal("expected probeSem tokens to drain")
	}
	t.Cleanup(func() { restoreProbeSem(mgr, drained) })

	skipReq := &DownloadRequest{
		URL:           ts.URL + "/sem-skip.bin",
		Filename:      "sem-skip.bin",
		Path:          t.TempDir(),
		FileSize:      1024,
		SupportsRange: boolPtr(false),
	}
	skipCtx, skipCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer skipCancel()
	if id, _, err := mgr.Enqueue(skipCtx, skipReq); err != nil {
		t.Fatalf("skip Enqueue with drained probeSem: %v", err)
	} else {
		cancelEnqueue(t, mgr, id)
	}

	probeReq := &DownloadRequest{
		URL:      ts.URL + "/sem-probe.bin",
		Filename: "sem-probe.bin",
		Path:     t.TempDir(),
	}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer probeCancel()
	_, _, err := mgr.Enqueue(probeCtx, probeReq)
	if err == nil {
		t.Fatal("non-skip Enqueue with drained probeSem succeeded, want ctx abort")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-skip Enqueue error = %v, want context.DeadlineExceeded", err)
	}
}

func TestBuildDownloadRecord_ProbeFalseWritesRangeUnsupported(t *testing.T) {
	mgr, _ := newSkipEnqueueManager(t)
	defer mgr.Shutdown()
	req := &DownloadRequest{
		URL:      "http://example.com/no-range.bin",
		Filename: "no-range.bin",
		Path:     t.TempDir(),
		FileSize: 2048,
	}
	rec, err := mgr.buildDownloadRecord(req, "probe-false-id", req.Path, req.Filename, &probing.ProbeResult{
		FileSize:      2048,
		SupportsRange: false,
		Filename:      "no-range.bin",
	})
	if err != nil {
		t.Fatalf("buildDownloadRecord: %v", err)
	}
	if rec.RangeAcquisitionMode != types.RangeAcquireRangeUnsupported {
		t.Fatalf("mode = %q, want range_unsupported", rec.RangeAcquisitionMode)
	}
}
