package concurrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/testutil"
)

// nonzeroTarpitHandler sends a configurable number of non-zero bytes (0xFF)
// for each Range request, then holds the connection. This lets tests
// distinguish tarpit-written data from preallocate zero-fill holes.
type nonzeroTarpitHandler struct {
	fileSize     int64
	partialBytes int64
}

func (h *nonzeroTarpitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start, end, ok := parseSimpleRange(r.Header.Get("Range"), h.fileSize)
	if !ok {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	length := end - start + 1
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, h.fileSize))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)

	toSend := h.partialBytes
	if toSend > length {
		toSend = length
	}
	if toSend > 0 {
		data := make([]byte, toSend)
		for i := range data {
			data[i] = 0xFF
		}
		w.Write(data)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	<-r.Context().Done()
}

func newNonzeroTarpitServer(t *testing.T, fileSize, partialBytes int64) *httptest.Server {
	t.Helper()
	srv := testutil.NewHTTPServerT(t, &nonzeroTarpitHandler{fileSize: fileSize, partialBytes: partialBytes})
	t.Cleanup(srv.Close)
	return srv
}

// patternHandler serves deterministic non-zero data (byte = offset%251+1)
// for each Range request, so downloaded content can be verified against
// zero-fill corruption holes left by preallocate.
type patternHandler struct {
	fileSize int64
}

func (h *patternHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start, end, ok := parseSimpleRange(r.Header.Get("Range"), h.fileSize)
	if !ok {
		start = 0
		end = h.fileSize - 1
	}
	length := end - start + 1
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, h.fileSize))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)

	buf := make([]byte, 4096)
	remaining := length
	offset := start
	for remaining > 0 {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		for i := int64(0); i < n; i++ {
			buf[i] = byte((offset+i)%251) + 1 // 1..251, never 0
		}
		w.Write(buf[:n])
		offset += n
		remaining -= n
	}
}

func newPatternServer(t *testing.T, fileSize int64) *httptest.Server {
	t.Helper()
	srv := testutil.NewHTTPServerT(t, &patternHandler{fileSize: fileSize})
	t.Cleanup(srv.Close)
	return srv
}

// verifyNoZeroHoles reads the file and fails if any byte is 0, indicating a
// preallocate zero-fill region that was never overwritten with real data.
func verifyNoZeroHoles(t *testing.T, path string, fileSize int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 4096)
	offset := int64(0)
	for offset < fileSize {
		n, err := f.ReadAt(buf, offset)
		if err != nil && n == 0 {
			t.Fatalf("read at %d: %v", offset, err)
		}
		for i := 0; i < n; i++ {
			if buf[i] == 0 {
				t.Errorf("zero byte at offset %d — preallocate hole not overwritten", offset+int64(i))
				return
			}
		}
		offset += int64(n)
	}
}

// TestHealthCancelRequeue_PreservesSharedMaxOffset verifies that when a
// health-cancelled worker's remaining task is requeued, the SharedMaxOffset
// pointer is preserved so the new worker deduplicates writes with the hedge
// partner. Without the fix, Downloaded overcounts and runCompletionMonitor
// kills workers prematurely, leaving zero-fill holes.
func TestHealthCancelRequeue_PreservesSharedMaxOffset(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(512 * types.KB)
	// Tarpit: sends 128KB of 0xFF then holds (stall triggers health-cancel).
	tarpitSrv := newNonzeroTarpitServer(t, fileSize, 128*types.KB)
	// Pattern server: serves non-zero deterministic data, fast completion.
	normalSrv := newPatternServer(t, fileSize)

	destPath := filepath.Join(tmpDir, "health_requeue.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := types.NewProgressState("health-requeue", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              64 * types.KB,
		StallTimeout:              500 * time.Millisecond,
	}
	d := NewConcurrentDownloader("health-requeue", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL}, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	// Downloaded must not overcount (SharedMaxOffset dedup prevents double-count).
	if got := state.Downloaded.Load(); got > fileSize {
		t.Errorf("Downloaded = %d, want <= %d (overcount detected)", got, fileSize)
	}

	// VerifiedProgress must reach full size (all chunks completed).
	if got := state.VerifiedProgress.Load(); got != fileSize {
		t.Errorf("VerifiedProgress = %d, want %d", got, fileSize)
	}

	// File size must be correct.
	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}

	// File content must have no zero-fill holes (corruption check).
	// Both tarpit (0xFF) and pattern (1..251) servers write non-zero data,
	// so any zero byte indicates a preallocate hole that was never written.
	verifyNoZeroHoles(t, workingPath, fileSize)
}

// TestHealthCancelRequeue_NilSharedMaxOffset_NoOvercount verifies that a
// non-hedge requeue (SharedMaxOffset=nil, single worker) does not cause
// overcount. The worker stalls on the tarpit, health-cancel requeues the
// remaining task, and the worker retries from the normal mirror.
func TestHealthCancelRequeue_NilSharedMaxOffset_NoOvercount(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * types.KB)
	// Tarpit: sends 64KB of 0xFF then holds.
	tarpitSrv := newNonzeroTarpitServer(t, fileSize, 64*types.KB)
	// Pattern server for retry after mirror rotation.
	normalSrv := newPatternServer(t, fileSize)

	destPath := filepath.Join(tmpDir, "nil_shared.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := types.NewProgressState("nil-shared", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		MinChunkSize:              64 * types.KB,
		StallTimeout:              500 * time.Millisecond,
	}
	d := NewConcurrentDownloader("nil-shared", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL}, 30*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if got := state.VerifiedProgress.Load(); got != fileSize {
		t.Errorf("VerifiedProgress = %d, want %d", got, fileSize)
	}
	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
}
