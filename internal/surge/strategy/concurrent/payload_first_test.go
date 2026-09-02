package concurrent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func payloadFirstRuntime() *types.RuntimeConfig {
	return &types.RuntimeConfig{
		MaxConnectionsPerDownload: 4,
		Workers:                   4,
		MinChunkSize:              64 * 1024,
		WorkerBufferSize:          32 * 1024,
		DialHedgeCount:            4,
		MaxTaskRetries:            1,
	}
}

func servePayloadRange(t *testing.T, fileSize int64, mutate func(http.ResponseWriter, *http.Request, int64, int64) bool) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	blob := make([]byte, fileSize)
	for i := range blob {
		blob[i] = byte(i)
	}
	var zeroZero atomic.Int64
	var payload atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "bytes=0-0" {
			zeroZero.Add(1)
		} else if strings.HasPrefix(rangeHdr, "bytes=") {
			payload.Add(1)
		}
		start, end := int64(0), fileSize-1
		if strings.HasPrefix(rangeHdr, "bytes=") {
			n, _ := fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end)
			if n != 2 {
				start, end = 0, fileSize-1
			}
		}
		if mutate != nil && mutate(w, r, start, end) {
			return
		}
		if end >= fileSize {
			end = fileSize - 1
		}
		if start < 0 {
			start = 0
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(blob[start : end+1])
	}))
	t.Cleanup(server.Close)
	return server, &zeroZero, &payload
}

func seedPayloadFirstMaster(t *testing.T, url, destPath string, fileSize int64) {
	t.Helper()
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:                   "pf-id",
		URL:                  url,
		URLHash:              store.URLHash(url),
		DestPath:             destPath,
		Filename:             filepath.Base(destPath),
		TotalSize:            fileSize,
		Status:               "downloading",
		RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown,
		SkipServerProbe:      true,
	})
}

func newPayloadFirstDownloader(t *testing.T, destPath string, fileSize int64) *ConcurrentDownloader {
	t.Helper()
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	} else {
		t.Fatal(err)
	}
	state := progress.New("pf-id", fileSize)
	d := NewConcurrentDownloader("pf-id", nil, state, payloadFirstRuntime())
	d.RangeAcquisitionMode = types.RangeAcquirePayloadFirstUnknown
	d.SkipServerProbe = true
	return d
}

func installWriteAtCounter(t *testing.T) *atomic.Int64 {
	t.Helper()
	orig := writeAtFn
	var writes atomic.Int64
	writeAtFn = func(f *os.File, b []byte, off int64) (int, error) {
		writes.Add(1)
		return orig(f, b, off)
	}
	t.Cleanup(func() { writeAtFn = orig })
	return &writes
}

func TestPayloadFirst_206PlannedShardNoZeroZero(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * 1024)
	var firstRange string
	var mu sync.Mutex
	server, zeroZero, payload := servePayloadRange(t, fileSize, func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
		mu.Lock()
		if firstRange == "" && r.Header.Get("Range") != "bytes=0-0" {
			firstRange = r.Header.Get("Range")
		}
		mu.Unlock()
		return false
	})

	writes := installWriteAtCounter(t)
	destPath := filepath.Join(tmpDir, "pf_206.bin")
	d := newPayloadFirstDownloader(t, destPath, fileSize)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 requests = %d, want 0", zeroZero.Load())
	}
	if payload.Load() == 0 {
		t.Fatal("expected payload Range requests")
	}
	mu.Lock()
	gotFirst := firstRange
	mu.Unlock()
	if gotFirst == fmt.Sprintf("bytes=0-%d", fileSize-1) {
		t.Fatalf("first Range was whole file %s; planned shard should be smaller", gotFirst)
	}
	if !strings.HasPrefix(gotFirst, "bytes=0-") {
		t.Fatalf("first Range = %q, want bytes=0-<end>", gotFirst)
	}
	if writes.Load() == 0 {
		t.Fatal("expected payload writes after header validation")
	}
	if d.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("mode = %q, want range_supported", d.RangeAcquisitionMode)
	}
	if err := testutil.VerifyFileSize(destPath+types.IncompleteSuffix, fileSize); err != nil {
		t.Error(err)
	}
}

func TestPayloadFirst_200FullFileZeroWrite(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * 1024)
	var zeroZero atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			zeroZero.Add(1)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, fileSize))
	}))
	t.Cleanup(server.Close)

	writes := installWriteAtCounter(t)
	destPath := filepath.Join(tmpDir, "pf_200.bin")
	seedPayloadFirstMaster(t, server.URL, destPath, fileSize)
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if !errors.Is(err, types.ErrRangeUnsupported) {
		t.Fatalf("err = %v, want ErrRangeUnsupported", err)
	}
	if writes.Load() != 0 {
		t.Fatalf("WriteAt calls = %d, want 0", writes.Load())
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
	saved, loadErr := store.LoadState(server.URL, destPath)
	if loadErr == nil && saved != nil && len(saved.Tasks) > 0 {
		t.Fatal("unverified failure must not persist a task snapshot")
	}
}

func TestPayloadFirst_MismatchNoWrite(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(http.ResponseWriter, *http.Request, int64, int64) bool
	}{
		{
			name: "bad_cr",
			mutate: func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
				w.Header().Set("Content-Range", "garbage")
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte{0})
				return true
			},
		},
		{
			name: "missing_cr",
			mutate: func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
				w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(make([]byte, end-start+1))
				return true
			},
		},
		{
			name: "total_mismatch",
			mutate: func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, 999999))
				w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(make([]byte, end-start+1))
				return true
			},
		},
		{
			name: "status_416",
			mutate: func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return true
			},
		},
		{
			name: "200_wrong_length",
			mutate: func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
				w.Header().Set("Content-Length", "12")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("not-the-size"))
				return true
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir, cleanup := initTestState(t)
			defer cleanup()
			fileSize := int64(64 * 1024)
			server, _, _ := servePayloadRange(t, fileSize, tc.mutate)
			writes := installWriteAtCounter(t)
			destPath := filepath.Join(tmpDir, "pf_mismatch.bin")
			d := newPayloadFirstDownloader(t, destPath, fileSize)
			d.Runtime.Workers = 1
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
			if !errors.Is(err, types.ErrSourceMetadataMismatch) {
				t.Fatalf("err = %v, want ErrSourceMetadataMismatch", err)
			}
			if writes.Load() != 0 {
				t.Fatalf("WriteAt calls = %d, want 0", writes.Load())
			}
		})
	}
}

func TestPayloadFirst_NoZeroZeroOnScale(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(256 * 1024)
	server, zeroZero, _ := servePayloadRange(t, fileSize, nil)
	destPath := filepath.Join(tmpDir, "pf_scale.bin")
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 during payload-first session = %d, want 0", zeroZero.Load())
	}
}

func TestPayloadFirst_429NotRangeUnsupported(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(32 * 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	writes := installWriteAtCounter(t)
	destPath := filepath.Join(tmpDir, "pf_429.bin")
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1
	d.Runtime.MaxTaskRetries = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if errors.Is(err, types.ErrRangeUnsupported) {
		t.Fatal("429 must not be ErrRangeUnsupported")
	}
	if errors.Is(err, types.ErrSourceMetadataMismatch) {
		t.Fatal("429 must not be ErrSourceMetadataMismatch")
	}
	if writes.Load() != 0 {
		t.Fatalf("WriteAt calls = %d, want 0", writes.Load())
	}
}

func TestPayloadFirst_403NotRangeUnsupported(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(32 * 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	destPath := filepath.Join(tmpDir, "pf_403.bin")
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1
	d.Runtime.MaxTaskRetries = 1
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if errors.Is(err, types.ErrRangeUnsupported) {
		t.Fatal("403 must not be ErrRangeUnsupported")
	}
}

func TestPayloadFirst_TransportNotRangeUnsupported(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(32 * 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
	}))
	server.Close()
	destPath := filepath.Join(tmpDir, "pf_transport.bin")
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1
	d.Runtime.MaxTaskRetries = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if errors.Is(err, types.ErrRangeUnsupported) {
		t.Fatal("transport error must not be ErrRangeUnsupported")
	}
}

func TestPayloadFirst_ENOSPCNotFallbackSentinel(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(64 * 1024)
	server, _, _ := servePayloadRange(t, fileSize, nil)
	orig := writeAtFn
	writeAtFn = func(f *os.File, b []byte, off int64) (int, error) {
		return 0, types.ErrInsufficientDiskSpace
	}
	t.Cleanup(func() { writeAtFn = orig })
	destPath := filepath.Join(tmpDir, "pf_enospc.bin")
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if !types.IsInsufficientDiskSpace(err) {
		t.Fatalf("err = %v, want insufficient disk space", err)
	}
	if errors.Is(err, types.ErrRangeUnsupported) {
		t.Fatal("ENOSPC must not be Range-unsupported")
	}
}

func TestPayloadFirst_Later200KeepsSnapshot(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(256 * 1024)
	var firstDone atomic.Bool
	server, zeroZero, _ := servePayloadRange(t, fileSize, func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
		if start == 0 {
			firstDone.Store(true)
			return false
		}
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, fileSize))
		return true
	})
	destPath := filepath.Join(tmpDir, "pf_later200.bin")
	seedPayloadFirstMaster(t, server.URL, destPath, fileSize)
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if !errors.Is(err, types.ErrRangeUnsupported) {
		t.Fatalf("err = %v, want ErrRangeUnsupported from later 200", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
	saved, loadErr := store.LoadState(d.URL, destPath)
	if loadErr != nil || saved == nil {
		t.Fatalf("expected snapshot kept after verified bytes, err=%v", loadErr)
	}
	if saved.Downloaded <= 0 && len(saved.Tasks) == 0 {
		t.Fatal("snapshot must keep progress or remaining tasks")
	}
	if saved.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("mode = %q, want range_supported", saved.RangeAcquisitionMode)
	}
}

func TestPayloadFirst_PersistBeforeWriteResume(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(128 * 1024)
	releaseBody := make(chan struct{})
	server, zeroZero, _ := servePayloadRange(t, fileSize, func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-releaseBody:
		case <-r.Context().Done():
			return true
		}
		_, _ = w.Write(make([]byte, end-start+1))
		return true
	})
	writes := installWriteAtCounter(t)
	destPath := filepath.Join(tmpDir, "pf_persist.bin")
	seedPayloadFirstMaster(t, server.URL, destPath, fileSize)
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1

	errCh := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		errCh <- d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("Download returned before persist: %v", err)
		default:
		}
		saved, err := store.LoadState(server.URL, destPath)
		if err == nil && saved != nil && saved.RangeAcquisitionMode == types.RangeAcquireRangeSupported && len(saved.Tasks) > 0 {
			if writes.Load() != 0 {
				t.Fatal("payload write happened before RangeSupported persist")
			}
			if zeroZero.Load() != 0 {
				t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
			}
			close(releaseBody)
			cancel()
			<-errCh
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(releaseBody)
	cancel()
	<-errCh
	t.Fatal("timed out waiting for persist-before-write")
}

func TestScaleWorkers_Prewarm_PayloadFirstDisabled(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	var prewarmHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			prewarmHits.Add(1)
		}
		w.Header().Set("Content-Range", "bytes 0-0/1024")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "scale_pf.bin")
	f, err := os.Create(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
		DialHedgeCount:   0,
	})
	d.payloadFirstSession.Store(true)
	d.payloadFirstVerified.Store(true)
	d.payloadFirstWrote.Store(true)
	d.skipRangePrewarm.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := NewTaskQueue()
	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		mirrors:   []string{server.URL},
		file:      f,
		queue:     queue,
		totalSize: 1024,
		client:    server.Client(),
	})
	d.workersActive.Store(true)

	added := d.ScaleWorkers(1)
	if added != 1 {
		t.Fatalf("ScaleWorkers(1) returned %d, want 1", added)
	}
	if prewarmHits.Load() != 0 {
		t.Fatalf("payload-first session must not 0-0 prewarm, got %d", prewarmHits.Load())
	}
	queue.Close()
	waitScaledWorkers(t, d, cancel, 5*time.Second)
}

func TestPayloadFirst_PauseResumeKeepsProgress(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(256 * 1024)
	server, zeroZero, _ := servePayloadRange(t, fileSize, nil)
	destPath := filepath.Join(tmpDir, "pf_pause.bin")
	seedPayloadFirstMaster(t, server.URL, destPath, fileSize)
	d := newPayloadFirstDownloader(t, destPath, fileSize)

	orig := writeAtFn
	var paused atomic.Bool
	writeAtFn = func(f *os.File, b []byte, off int64) (int, error) {
		n, err := orig(f, b, off)
		if d.State != nil && paused.CompareAndSwap(false, true) {
			d.State.Pause()
		}
		return n, err
	}
	t.Cleanup(func() { writeAtFn = orig })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if err != nil && !errors.Is(err, types.ErrPaused) {
		t.Fatalf("pause Download: %v", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
	saved, loadErr := store.LoadState(server.URL, destPath)
	if loadErr != nil || saved == nil {
		t.Fatalf("LoadState after pause: %v", loadErr)
	}
	if saved.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("paused mode = %q, want range_supported", saved.RangeAcquisitionMode)
	}
	if saved.Downloaded <= 0 && len(saved.Tasks) == 0 {
		t.Fatal("pause snapshot lost progress")
	}
	before := saved.Downloaded

	d2 := NewConcurrentDownloader("pf-id", nil, progress.New("pf-id", fileSize), payloadFirstRuntime())
	d2.RangeAcquisitionMode = types.RangeAcquireRangeSupported
	d2.SkipServerProbe = true
	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel2()
	if err := d2.Download(ctx2, server.URL, nil, nil, destPath, fileSize); err != nil {
		t.Fatalf("resume Download: %v", err)
	}
	if err := testutil.VerifyFileSize(destPath+types.IncompleteSuffix, fileSize); err != nil {
		t.Error(err)
	}
	if d2.State == nil {
		t.Fatal("resume state missing")
	}
	if got := d2.State.Bytes.Downloaded.Load(); got < before {
		t.Fatalf("resume Downloaded=%d < paused %d", got, before)
	}
}

func TestPayloadFirst_NoBodyBeforeValidate(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(32 * 1024)
	var bodyReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "garbage")
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusPartialContent)
		n, _ := w.Write(make([]byte, fileSize))
		if n > 0 {
			bodyReads.Add(int64(n))
		}
	}))
	t.Cleanup(server.Close)
	writes := installWriteAtCounter(t)
	destPath := filepath.Join(tmpDir, "pf_nobody.bin")
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if !errors.Is(err, types.ErrSourceMetadataMismatch) {
		t.Fatalf("err = %v, want mismatch", err)
	}
	if writes.Load() != 0 {
		t.Fatalf("WriteAt = %d, want 0", writes.Load())
	}
}

func TestPayloadFirst_PersistFailureReturns(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(64 * 1024)
	server, _, _ := servePayloadRange(t, fileSize, nil)
	destPath := filepath.Join(tmpDir, "pf_persist_fail.bin")
	seedPayloadFirstMaster(t, server.URL, destPath, fileSize)
	details := filepath.Join(tmpDir, "details")
	_ = os.RemoveAll(details)
	if err := os.WriteFile(details, []byte("blocked"), 0o644); err != nil {
		t.Fatal(err)
	}
	writes := installWriteAtCounter(t)
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1
	d.Runtime.MaxTaskRetries = 3
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	if !errors.Is(err, types.ErrPayloadFirstPersist) {
		t.Fatalf("err = %v, want ErrPayloadFirstPersist", err)
	}
	if writes.Load() != 0 {
		t.Fatalf("WriteAt = %d, want 0", writes.Load())
	}
}

func TestScaleWorkers_NoOpUntilFirstWrite(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
	})
	d.payloadFirstSession.Store(true)
	d.payloadFirstVerified.Store(true)
	d.workersActive.Store(true)
	d.workerDepsPtr.Store(&workerDeps{ctx: context.Background()})
	if added := d.ScaleWorkers(2); added != 0 {
		t.Fatalf("ScaleWorkers before first write = %d, want 0", added)
	}
}

func TestPayloadFirst_Shorter206Completes(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(64 * 1024)
	short := int64(1024)
	server, zeroZero, _ := servePayloadRange(t, fileSize, func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
		if start == 0 && end > short-1 {
			end = short - 1
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			blob := make([]byte, end-start+1)
			for i := range blob {
				blob[i] = byte(start + int64(i))
			}
			_, _ = w.Write(blob)
			return true
		}
		return false
	})
	destPath := filepath.Join(tmpDir, "pf_short.bin")
	seedPayloadFirstMaster(t, server.URL, destPath, fileSize)
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 2
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
	if err := testutil.VerifyFileSize(destPath+types.IncompleteSuffix, fileSize); err != nil {
		t.Error(err)
	}
}

func TestPayloadFirst_UnpinsMirrorsAfterFirstWrite(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(256 * 1024)
	var mirrorHits atomic.Int64
	mirror, _, _ := servePayloadRange(t, fileSize, func(w http.ResponseWriter, r *http.Request, start, end int64) bool {
		mirrorHits.Add(1)
		return false
	})
	primary, zeroZero, _ := servePayloadRange(t, fileSize, nil)
	destPath := filepath.Join(tmpDir, "pf_unpin.bin")
	seedPayloadFirstMaster(t, primary.URL, destPath, fileSize)
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.Download(ctx, primary.URL, []string{mirror.URL}, nil, destPath, fileSize); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
	if mirrorHits.Load() == 0 {
		t.Fatal("expected later workers to use the unpinned mirror")
	}
}

func TestRangeSupported_SkipServerProbeNoZeroZero(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(64 * 1024)
	server, zeroZero, _ := servePayloadRange(t, fileSize, nil)
	destPath := filepath.Join(tmpDir, "skip_prewarm.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:                   "skip-prewarm",
		URL:                  server.URL,
		URLHash:              store.URLHash(server.URL),
		DestPath:             destPath,
		Filename:             filepath.Base(destPath),
		TotalSize:            fileSize,
		Status:               "downloading",
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		SkipServerProbe:      true,
	})
	state := progress.New("skip-prewarm", fileSize)
	d := NewConcurrentDownloader("skip-prewarm", nil, state, payloadFirstRuntime())
	d.RangeAcquisitionMode = types.RangeAcquireRangeSupported
	d.SkipServerProbe = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := d.Download(ctx, server.URL, nil, nil, destPath, fileSize); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0 on skip-origin RangeSupported", zeroZero.Load())
	}
}

func TestPayloadFirst_UnverifiedPauseDoesNotSnapshot(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(64 * 1024)
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	destPath := filepath.Join(tmpDir, "pf_unverified_pause.bin")
	seedPayloadFirstMaster(t, server.URL, destPath, fileSize)
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.Workers = 1
	errCh := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() {
		errCh <- d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first request")
	}
	d.State.Pause()
	close(release)
	err := <-errCh
	if err != nil && !errors.Is(err, types.ErrPaused) && !errors.Is(err, context.Canceled) {
		t.Fatalf("unverified pause err = %v", err)
	}
	saved, loadErr := store.LoadState(server.URL, destPath)
	if loadErr == nil && saved != nil && len(saved.Tasks) > 0 {
		t.Fatal("unverified pause must not persist a task snapshot")
	}
}

func TestPayloadFirst_Mirror200AfterVerifyRotates(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	fileSize := int64(256 * 1024)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, fileSize))
	}))
	t.Cleanup(mirror.Close)
	primary, zeroZero, _ := servePayloadRange(t, fileSize, nil)
	destPath := filepath.Join(tmpDir, "pf_mirror200.bin")
	seedPayloadFirstMaster(t, primary.URL, destPath, fileSize)
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := d.Download(ctx, primary.URL, []string{mirror.URL}, nil, destPath, fileSize); err != nil {
		t.Fatalf("Download: %v (mirror 200 must rotate, not abort as RangeUnsupported)", err)
	}
	if zeroZero.Load() != 0 {
		t.Fatalf("bytes=0-0 = %d, want 0", zeroZero.Load())
	}
}
