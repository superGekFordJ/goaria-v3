package concurrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/testutil"
)

// earlyEOFHandler serves a 206 Partial Content response but only sends
// partialBytes of the requested range, then returns (closing the connection).
// It deliberately omits Content-Length so Go uses chunked transfer encoding,
// ensuring resp.Body.Read returns (n>0, io.EOF) rather than io.ErrUnexpectedEOF
// (which happens when a declared Content-Length is not satisfied).
type earlyEOFHandler struct {
	fileSize     int64
	partialBytes int64
}

func (h *earlyEOFHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start, end, ok := parseSimpleRange(r.Header.Get("Range"), h.fileSize)
	if !ok {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	length := end - start + 1
	w.Header().Set("Content-Type", "application/octet-stream")
	// Deliberately do NOT set Content-Length — chunked encoding ensures
	// the client gets io.EOF (not ErrUnexpectedEOF) on short read.
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, h.fileSize))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)

	toSend := h.partialBytes
	if toSend > length {
		toSend = length
	}
	data := make([]byte, toSend)
	for i := range data {
		data[i] = 0xFF
	}
	_, _ = w.Write(data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Returning here closes the chunked stream → client Read gets io.EOF.
}

// newEarlyEOFServer creates an httptest.Server that sends partial data then EOF.
func newEarlyEOFServer(t *testing.T, fileSize, partialBytes int64) *httptest.Server {
	t.Helper()
	handler := &earlyEOFHandler{
		fileSize:     fileSize,
		partialBytes: partialBytes,
	}
	srv := testutil.NewHTTPServerT(t, handler)
	t.Cleanup(srv.Close)
	return srv
}

// TestEarlyEOF_TaskRequeuedNotLost verifies that when a server sends partial
// data then EOF (n>0, io.EOF), the early-EOF guard returns an error, the task
// is retried and requeued, and the download eventually completes via a healthy
// mirror. Without the guard the undownloaded bytes would be silently lost.
func TestEarlyEOF_TaskRequeuedNotLost(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * types.KB)
	// Early-EOF server: sends only 64KB of the 256KB range, then EOF.
	eofSrv := newEarlyEOFServer(t, fileSize, 64*types.KB)
	// Normal server completes the full range on retry (mirror failover).
	normalSrv := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer normalSrv.Close()

	destPath := filepath.Join(tmpDir, "early_eof.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := types.NewProgressState("early-eof", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		MinChunkSize:              256 * types.KB,
	}
	d := NewConcurrentDownloader("early-eof", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Primary mirror is the early-EOF server; normal server is the failover.
	err := downloadWithTimeout(t, d, ctx, eofSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL()}, 30*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if got := state.VerifiedProgress.Load(); got != fileSize {
		t.Errorf("VerifiedProgress = %d, want %d (task should not be lost)", got, fileSize)
	}
	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
}

// TestEarlyEOF_NormalCompletion_Unaffected verifies that the early-EOF guard
// does not fire on normal completion (offset == StopAt → return nil at the
// loop top, guard unreachable).
func TestEarlyEOF_NormalCompletion_Unaffected(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * types.KB)
	normalSrv := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer normalSrv.Close()

	destPath := filepath.Join(tmpDir, "normal_complete.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := types.NewProgressState("normal-complete", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		MinChunkSize:              256 * types.KB,
	}
	d := NewConcurrentDownloader("normal-complete", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, normalSrv.URL(), destPath, fileSize,
		nil, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if got := state.VerifiedProgress.Load(); got != fileSize {
		t.Errorf("VerifiedProgress = %d, want %d", got, fileSize)
	}
}
