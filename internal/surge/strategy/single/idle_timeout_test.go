package single

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// blockedBody blocks in Read until Close unblocks it, modeling a mid-body
// tarpit (headers delivered, then permanent silence).
type blockedBody struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockedBody() *blockedBody {
	return &blockedBody{closed: make(chan struct{})}
}

func (b *blockedBody) Read(p []byte) (int, error) {
	<-b.closed
	return 0, errors.New("read on closed body")
}

func (b *blockedBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// instantBody returns its payload immediately and tracks whether Close ran.
type instantBody struct {
	data   []byte
	closed atomic.Bool
}

func (b *instantBody) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *instantBody) Close() error {
	b.closed.Store(true)
	return nil
}

// sleepingLimiter spends a fixed delay per WaitN call — used to prove the
// idle watchdog wraps the body below the throttle layer (throttle wait time
// must never consume the per-Read idle budget).
type sleepingLimiter struct {
	delay time.Duration
}

func (l *sleepingLimiter) WaitN(ctx context.Context, n int64) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(l.delay):
		return nil
	}
}

func setBodyIdleTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := bodyIdleTimeout
	bodyIdleTimeout = d
	t.Cleanup(func() { bodyIdleTimeout = prev })
}

func TestIdleTimeoutReader_Tarpit(t *testing.T) {
	body := newBlockedBody()
	r := &idleTimeoutReader{body: body, timeout: 50 * time.Millisecond}

	start := time.Now()
	_, err := r.Read(make([]byte, 4096))
	if err == nil {
		t.Fatal("expected error after idle timeout closed the body")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Read blocked %v; idle timeout (~50ms) did not fire", elapsed)
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("body was not closed by the idle timer")
	}
}

func TestIdleTimeoutReader_DataFlows(t *testing.T) {
	body := &instantBody{data: []byte("hello")}
	r := &idleTimeoutReader{body: body, timeout: 200 * time.Millisecond}

	buf := make([]byte, 16)
	n, err := r.Read(buf)
	if err != nil || n != 5 || string(buf[:n]) != "hello" {
		t.Fatalf("Read = (%d, %v, %q), want (5, nil, hello)", n, err, buf[:n])
	}
	if body.closed.Load() {
		t.Fatal("body was closed on the fast path")
	}
	n, err = r.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("second Read = (%d, %v), want (0, EOF)", n, err)
	}
}

// TestSingleDownloader_BodyTarpitIdleTimeout: server sends 200 + Content-Length
// then goes silent forever. Without the idle reader this hangs io.CopyBuffer
// until ctx deadline; the watchdog must surface a bounded copy error.
func TestSingleDownloader_BodyTarpitIdleTimeout(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-single-tarpit")
	defer cleanup()

	setBodyIdleTimeout(t, 300*time.Millisecond)

	fileSize := int64(64 * utils.KiB)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "tarpit.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	} else {
		t.Fatal(err)
	}

	state := progress.New("single-tarpit", fileSize)
	d := NewSingleDownloader("single-tarpit", nil, state, &types.RuntimeConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	err := d.Download(ctx, server.URL, destPath, fileSize, "tarpit.bin")
	if err == nil {
		t.Fatal("expected bounded copy error from body idle timeout")
	}
	if ctx.Err() != nil {
		t.Fatalf("returned only on ctx deadline — idle watchdog did not fire: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Download took %v; idle timeout (~300ms) did not bound the tarpit", elapsed)
	}
}

// TestSingleDownloader_IdleReaderInsideThrottle locks the wrap order:
// idleTimeoutReader must sit BELOW throttledReader so per-WaitN throttle
// sleeps (400ms each, exceeding the 150ms idle budget) never trip the
// watchdog. With the order reversed the first WaitN would close the body.
func TestSingleDownloader_IdleReaderInsideThrottle(t *testing.T) {
	tmpDir, cleanup, _ := testutil.TempDir("surge-single-throttle")
	defer cleanup()

	setBodyIdleTimeout(t, 150*time.Millisecond)

	fileSize := int64(64 * utils.KiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(false),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "throttle.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	} else {
		t.Fatal(err)
	}

	state := progress.New("single-throttle", fileSize)
	d := NewSingleDownloader("single-throttle", nil, state, &types.RuntimeConfig{})
	d.Limiter = &sleepingLimiter{delay: 400 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := d.Download(ctx, server.URL(), destPath, fileSize, "throttle.bin"); err != nil {
		t.Fatalf("throttled download failed (idle timer wrongly covers WaitN): %v", err)
	}
	if err := testutil.VerifyFileSize(destPath+types.IncompleteSuffix, fileSize); err != nil {
		t.Error(err)
	}
}
