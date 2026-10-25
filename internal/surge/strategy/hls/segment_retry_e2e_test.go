package hls

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// --- end-to-end through the real worker path ---

type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	slept []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Now()} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// instant records and advances the virtual clock without real sleeping.
func (c *fakeClock) instant(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

func (c *fakeClock) durations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

func TestHLSSegmentRetryNotFoundEndsPermanent(t *testing.T) {
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusNotFound)
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if !errors.Is(err, types.ErrPermanentHTTP) {
		t.Fatalf("wait = %v, want ErrPermanentHTTP", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3 (initial + 2 short retries)", got)
	}
	slept := clk.durations()
	if len(slept) != 2 || slept[0] != 500*time.Millisecond || slept[1] != time.Second {
		t.Fatalf("short-retry delays = %v", slept)
	}
}

func TestHLSSegmentRetryServerErrorExhausts(t *testing.T) {
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if se, ok := errors.AsType[*hlsStatusError](err); !ok || se.status != http.StatusInternalServerError {
		t.Fatalf("wait = %v (%T), want *hlsStatusError 500", err, err)
	}
	if got := calls.Load(); got != hlsMaxSegmentRetries+1 {
		t.Fatalf("requests = %d, want %d (budget exhausted, no extra request)",
			got, hlsMaxSegmentRetries+1)
	}
	want := []time.Duration{
		500 * time.Millisecond, time.Second,
		2 * time.Second, 4 * time.Second, 8 * time.Second,
	}
	slept := clk.durations()
	if len(slept) != len(want) {
		t.Fatalf("sleeps = %v, want ladder %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("sleep[%d] = %v, want %v", i, slept[i], want[i])
		}
	}
}

func TestHLSSegmentRetryTooManyRequestsHonorsRetryAfter(t *testing.T) {
	body := []byte("recovered-body")
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{body}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= 2 {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write(body)
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
	// Retry-After is the earliest allowed time; the shared host penalty may
	// extend the wait by clamp+jitter — never less, bounded above.
	for i, d := range clk.durations() {
		if d < 2*time.Second || d > 3*time.Second {
			t.Fatalf("sleep[%d] = %v, want RA-honored wait in [2s,3s]", i, d)
		}
	}
	if got := f.output(t); !bytes.Equal(got, body) {
		t.Fatalf("output = %q", got)
	}
}

func TestHLSSegmentRetryGateOpen403RefreshesURI(t *testing.T) {
	body := []byte("after-refresh")
	f := newHLSFixture(t, [][]byte{body}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/media.m3u8":
				_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:4.0,\nv2/seg0\n#EXT-X-ENDLIST\n"))
			case "/seg0":
				w.WriteHeader(http.StatusForbidden)
			case "/v2/seg0":
				_, _ = w.Write(body)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	e.setAdmissionOpen(true)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if n := f.requests.count("/media.m3u8"); n != 1 {
		t.Fatalf("manifest requests = %d, want exactly one shared refresh", n)
	}
	if n := f.requests.count("/seg0"); n != 1 {
		t.Fatalf("/seg0 requests = %d, want the single expired 403", n)
	}
	if n := f.requests.count("/v2/seg0"); n != 1 {
		t.Fatalf("/v2/seg0 requests = %d, want exactly one refreshed fetch", n)
	}
	if got := f.output(t); !bytes.Equal(got, body) {
		t.Fatalf("output = %q", got)
	}
	if e.refreshGen != 1 {
		t.Fatalf("refreshGen = %d, want 1 after one adoption", e.refreshGen)
	}
}

func TestHLSSegmentRetryUnauthorizedPermanent(t *testing.T) {
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	e := f.buildEngine(t, fixtureOpts{sleep: func(ctx context.Context, d time.Duration) error { return nil }})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if !errors.Is(err, types.ErrPermanentHTTP) {
		t.Fatalf("wait = %v, want ErrPermanentHTTP", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d — 401 must never retry", calls.Load())
	}
}

func TestHLSSegmentRetryRequestTimeoutRetries(t *testing.T) {
	body := []byte("after-408")
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{body}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusRequestTimeout)
				return
			}
			_, _ = w.Write(body)
		},
	})
	e := f.buildEngine(t, fixtureOpts{sleep: func(ctx context.Context, d time.Duration) error { return nil }})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	// The 408 burned one retry slot; the connection-attempt counter stays
	// at zero because the response proves the connection was established.
	if calls.Load() != 2 || e.queue.tasks[0].genericAttempt != 0 || e.queue.tasks[0].retries != 1 {
		t.Fatalf("requests = %d generic = %d retries = %d",
			calls.Load(), e.queue.tasks[0].genericAttempt, e.queue.tasks[0].retries)
	}
}

// TestHLSSegmentRetryTransportResetRetries covers a bare connection reset —
// a wire-class failure that must consume generic budget and recover.
func TestHLSSegmentRetryTransportResetRetries(t *testing.T) {
	body := []byte("after-reset")
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{body}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, herr := hj.Hijack()
					if herr == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(body)
		},
	})
	e := f.buildEngine(t, fixtureOpts{sleep: func(ctx context.Context, d time.Duration) error { return nil }})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2", calls.Load())
	}
	if got := f.output(t); !bytes.Equal(got, body) {
		t.Fatalf("output = %q", got)
	}
}

func TestHLSSegmentRetryInvalidURLTerminal(t *testing.T) {
	f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{
		segTweak: func(i int, seg *types.HLSSegmentState) {
			seg.URI = "ftp://unreachable.invalid/seg0"
		},
	})
	e := f.buildEngine(t, fixtureOpts{sleep: func(ctx context.Context, d time.Duration) error { return nil }})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if _, ok := errors.AsType[*hlsContractError](err); !ok {
		t.Fatalf("wait = %v (%T), want *hlsContractError", err, err)
	}
	if got := f.requests.count("/seg0"); got != 0 {
		t.Fatalf("requests = %d — no request may follow a contract violation", got)
	}
}

func TestHLSSegmentRetryRedirectUserinfoTerminal(t *testing.T) {
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			host := r.Host
			w.Header().Set("Location", "http://user:pass@"+host+"/trap")
			w.WriteHeader(http.StatusFound)
		},
	})
	e := f.buildEngine(t, fixtureOpts{sleep: func(ctx context.Context, d time.Duration) error { return nil }})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if !errors.Is(err, errRedirectUserinfo) {
		t.Fatalf("wait = %v, want errRedirectUserinfo", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d — userinfo refusal must not retry", calls.Load())
	}
}

func TestHLSSegmentRetryRedirectOverflowTerminal(t *testing.T) {
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Location", r.URL.Path+"x")
			w.WriteHeader(http.StatusFound)
		},
	})
	e := f.buildEngine(t, fixtureOpts{sleep: func(ctx context.Context, d time.Duration) error { return nil }})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if !errors.Is(err, types.ErrMaxRedirects) {
		t.Fatalf("wait = %v, want ErrMaxRedirects", err)
	}
	if calls.Load() > hlsMaxRedirects+1 {
		t.Fatalf("requests = %d exceeds redirect bound", calls.Load())
	}
}

// TestHLSSegmentRetryCancelDuringBackoffKeepsIdentity: the backoff wait is
// session-visible and cancellable; a parent cancel during it exits with the
// parent's context identity and never issues another request.
func TestHLSSegmentRetryCancelDuringBackoffKeepsIdentity(t *testing.T) {
	var calls atomic.Int64
	inSleep := make(chan struct{})
	var sleepOnce sync.Once
	sleep := func(ctx context.Context, d time.Duration) error {
		sleepOnce.Do(func() { close(inSleep) })
		<-ctx.Done()
		return ctx.Err()
	}
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, err := newSegmentEngine(segmentEngineParams{
		Seed: f.seed, Window: f.window, Progress: f.prog, Client: f.srv.Client(),
		RunCfg: types.DefaultRuntimeConfig(), Parent: parent, MaxWorkers: 4,
		sleep: sleep,
	})
	if err != nil {
		t.Fatalf("engine = %v", err)
	}
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	select {
	case <-inSleep:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never reached the backoff wait")
	}
	// The backoff wait projects as a protected wait in the worker snapshot.
	snaps := e.snapshotWorkers(e.now())
	protected := false
	for _, s := range snaps {
		if s.WaitingOnLimiter {
			protected = true
		}
	}
	if !protected {
		t.Fatalf("backoff not visible as protected wait: %+v", snaps)
	}
	cancel()
	err = e.wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait = %v, want parent context.Canceled", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d — a cancelled backoff must never re-request", calls.Load())
	}
}

// TestHLSSegmentRetryDrainMidBackoffRequeues: drain during the backoff wait
// returns the lease to the ready list with no budget spent.
func TestHLSSegmentRetryDrainMidBackoffRequeues(t *testing.T) {
	var calls atomic.Int64
	inSleep := make(chan struct{})
	var sleepOnce sync.Once
	sleep := func(ctx context.Context, d time.Duration) error {
		sleepOnce.Do(func() { close(inSleep) })
		<-ctx.Done()
		return ctx.Err()
	}
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	e := f.buildEngine(t, fixtureOpts{sleep: sleep})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	select {
	case <-inSleep:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never reached backoff")
	}
	ids := e.liveSessionIDs()
	if len(ids) != 1 || !e.markDraining(ids[0]) {
		t.Fatalf("drain failed: sessions=%v", ids)
	}
	err := e.wait()
	if !errors.Is(err, errEngineIncomplete) {
		t.Fatalf("wait = %v, want errEngineIncomplete", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d", calls.Load())
	}
	if st, _ := e.queue.state(0); st != segReady {
		t.Fatalf("seg0 state = %v, want requeued ready", st)
	}
	if e.queue.tasks[0].retries != 1 {
		t.Fatalf("drain moved retry counters: %d", e.queue.tasks[0].retries)
	}
}

// TestHLSSegmentRetryStallExhaustsBudget drives real stall cancels against a
// never-completing body until the retry budget dies with errAttemptStall.
func TestHLSSegmentRetryStallExhaustsBudget(t *testing.T) {
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done() // headers out, body never — stall target
		},
	})
	e := f.buildEngine(t, fixtureOpts{sleep: func(ctx context.Context, d time.Duration) error { return nil }})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		seen := map[uint64]bool{}
		stalls := 0
		deadline := time.Now().Add(15 * time.Second)
		for stalls < hlsMaxSegmentRetries+1 && time.Now().Before(deadline) {
			for _, v := range e.sessionViews() {
				if v.Phase == hlsPhaseNetwork && v.AttemptGen != 0 && !seen[v.AttemptGen] {
					if e.cancelAttemptStall(v.ID, v.AttemptGen) {
						seen[v.AttemptGen] = true
						stalls++
					}
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	err := e.wait()
	<-done
	if !errors.Is(err, errAttemptStall) {
		t.Fatalf("wait = %v, want errAttemptStall", err)
	}
	// A stall landing between phase-publish and client.Do cancels the
	// request before it leaves — the budget is spent either way, so the
	// request count is bounded, not exact.
	if n := calls.Load(); n < 1 || n > hlsMaxSegmentRetries+1 {
		t.Fatalf("requests = %d, want within [1,%d]", n, hlsMaxSegmentRetries+1)
	}
	task := e.queue.tasks[0]
	if task.retries != hlsMaxSegmentRetries+1 {
		t.Fatalf("stall retries = %d, want %d", task.retries, hlsMaxSegmentRetries+1)
	}
}

// TestHLSSegmentRetryKeyStatusClassified proves the same classifier governs
// key fetches: a key 404 consumes the short budget through the engine.
func TestHLSSegmentRetryKeyStatusClassified(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 16)
	var segCalls, keyCalls atomic.Int64
	f := newHLSFixture(t, [][]byte{[]byte("media")}, fixtureOpts{
		key: key,
		handler: func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/key.bin":
				keyCalls.Add(1)
				w.WriteHeader(http.StatusNotFound)
			default:
				segCalls.Add(1)
				_, _ = w.Write([]byte("media"))
			}
		},
	})
	clk := newFakeClock()
	e := f.buildEngine(t, fixtureOpts{now: clk.now, sleep: clk.instant})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if !errors.Is(err, types.ErrPermanentHTTP) {
		t.Fatalf("wait = %v, want ErrPermanentHTTP via key 404", err)
	}
	// Every retry re-reads the full segment ciphertext before the key miss —
	// the shared 404 short budget applies unchanged.
	if segCalls.Load() != 3 || keyCalls.Load() != 3 {
		t.Fatalf("seg = %d key = %d, want 3/3", segCalls.Load(), keyCalls.Load())
	}
}
