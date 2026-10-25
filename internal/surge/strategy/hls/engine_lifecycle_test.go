package hls

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
)

// rtFunc adapts a function to http.RoundTripper for transport-stub tests.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// waitErrBounded runs e.wait() and fails the test if it does not return
// within the deadline — waits that hang are bugs, not slow tests.
func waitErrBounded(t *testing.T, e *segmentEngine, bound time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(bound):
		t.Fatal("wait() did not return within bound")
		return nil
	}
}

// TestHLSEngineRespawnAfterDrainToZero is the epoch-channel regression: a
// drain-to-zero closes the done channel for that epoch, but a legal
// admission-open respawn must arm a fresh channel — the next worker's exit
// closes the new channel exactly once (no double-close panic) and wait()
// observes the second epoch instead of returning early on the stale close.
func TestHLSEngineRespawnAfterDrainToZero(t *testing.T) {
	gate := make(chan struct{})
	var gateOnce sync.Once
	// Unblock the parked first-GET handler on any failure path, or the
	// fixture's httptest.Server.Close wedges until the test timeout;
	// defer unwinds before any t.Cleanup, so the fixture's own
	// registration order can never strand this behind srv.Close.
	defer gateOnce.Do(func() { close(gate) })
	var calls atomic.Int32
	f := newHLSFixture(t, [][]byte{[]byte("respawn-payload")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				<-gate // hold the first GET so the drain lands mid-flight
			}
			_, _ = w.Write([]byte("respawn-payload"))
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.requests.count("/seg0") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first GET never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	if !e.markDraining(0) {
		t.Fatal("markDraining refused")
	}
	gateOnce.Do(func() { close(gate) })
	// wait() is terminal — observe drain-to-zero via the session registry,
	// then respawn before waiting.
	for len(e.liveSessionIDs()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker never drained out")
		}
		time.Sleep(time.Millisecond)
	}
	if rem := e.queue.remaining(); rem != 1 {
		t.Fatalf("remaining = %d, want 1 (drained lease requeued)", rem)
	}
	e.setAdmissionOpen(true)
	if !e.spawnWorker() {
		t.Fatal("spawnWorker after drain-to-zero refused")
	}
	if err := waitErrBounded(t, e, 3*time.Second); err != nil {
		t.Fatalf("post-respawn wait = %v", err)
	}
	if got := f.output(t); string(got) != "respawn-payload" {
		t.Fatalf("output %q", got)
	}
	if n := f.requests.count("/seg0"); n != 2 {
		t.Fatalf("requests = %d, want 2 (drained lease re-fetched whole)", n)
	}
}

// TestHLSEngineWaitBeforeStartReturns proves wait() on a never-started
// engine reports unfinished work instead of blocking on an unarmed channel.
func TestHLSEngineWaitBeforeStartReturns(t *testing.T) {
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{})
	e := f.buildEngine(t, fixtureOpts{})
	if err := waitErrBounded(t, e, time.Second); !errors.Is(err, errEngineIncomplete) {
		t.Fatalf("pre-start wait = %v, want errEngineIncomplete", err)
	}
}

// TestHLSEngineStopThenConcurrentWait exercises the stop primitive — seal,
// queue close and ctx cancel — and proves repeated and concurrent wait()
// calls all settle without a release-use race or channel reuse panic.
func TestHLSEngineStopThenConcurrentWait(t *testing.T) {
	hold := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			release := r.Context().Done()
			select {
			case <-hold:
			case <-release:
			}
			_, _ = w.Write([]byte("x"))
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.requests.count("/seg0") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("GET never arrived")
		}
		time.Sleep(time.Millisecond)
	}
	e.stop()
	release()

	errs := make(chan error, 3)
	for range 3 {
		go func() { errs <- waitErrBounded(t, e, 3*time.Second) }()
	}
	for range 3 {
		err := <-errs
		if !errors.Is(err, errEngineIncomplete) {
			t.Fatalf("post-stop wait = %v, want errEngineIncomplete", err)
		}
	}
	if n := len(e.liveSessionIDs()); n != 0 {
		t.Fatalf("sessions still live: %d", n)
	}
}

// TestHLSEngineRejectsMalformedSeed covers the construction-time seed
// contract: short bitmaps, row-count mismatches and out-of-order Index
// values must never reach the queue's completed-prefix filter.
func TestHLSEngineRejectsMalformedSeed(t *testing.T) {
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{})
	base := func() *types.HLSResumeState {
		return &types.HLSResumeState{
			ManifestURL:     f.srv.URL + "/media.m3u8",
			TotalSegments:   2,
			CompletedBitmap: make([]byte, 1),
			Segments: []types.HLSSegmentState{
				{Index: 0, URI: f.srv.URL + "/seg0"},
				{Index: 1, URI: f.srv.URL + "/seg1"},
			},
		}
	}
	for _, tc := range []struct {
		name string
		mut  func(s *types.HLSResumeState)
	}{
		{"ShortBitmap", func(s *types.HLSResumeState) { s.CompletedBitmap = nil }},
		{"RowCountMismatch", func(s *types.HLSResumeState) { s.Segments = s.Segments[:1] }},
		{"DuplicateIndex", func(s *types.HLSResumeState) { s.Segments[1].Index = 0 }},
		{"OverlongBitmap", func(s *types.HLSResumeState) { s.CompletedBitmap = make([]byte, 4) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := base()
			tc.mut(seed)
			_, err := newSegmentEngine(segmentEngineParams{
				Seed: seed, Window: f.window, Progress: f.prog,
				Client: f.srv.Client(),
			})
			if !errors.Is(err, errDirtySeed) {
				t.Fatalf("newSegmentEngine = %v, want errDirtySeed", err)
			}
		})
	}
}

// TestHLSBinaryRequestErrorHidesURL proves the transport wrapper never
// surfaces the request URL — signed query tokens and userinfo live in
// *url.Error's message, so Error() must report only the inner cause while
// errors.Is still reaches the original failure.
func TestHLSBinaryRequestErrorHidesURL(t *testing.T) {
	boom := errors.New("dial refused")
	client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return nil, boom
	})}
	f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{})
	f.seed.Segments[0].URI = f.srv.URL + "/seg0?token=SECRET-TOKEN&sig=SIGVAL"
	e := f.buildEngine(t, fixtureOpts{
		client: client,
		sleep:  func(ctx context.Context, d time.Duration) error { return nil },
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := waitErrBounded(t, e, 3*time.Second)
	if err == nil {
		t.Fatal("wait succeeded on dead transport")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("wait = %v — errors.Is must reach the inner cause through the wrapper", err)
	}
	for _, leak := range []string{"SECRET-TOKEN", "sig=SIGVAL", "token=", f.srv.URL} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error text leaks %q: %v", leak, err)
		}
	}
}

// TestHLSRedirectUserinfoErrorHidesTarget is the credential-anchor corner:
// a redirect carrying userinfo is rejected, and the resulting error must not
// echo the userinfo-bearing target URL while still matching the sentinel.
func TestHLSRedirectUserinfoErrorHidesTarget(t *testing.T) {
	f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://user:pass@127.0.0.1:1/final", http.StatusFound)
		},
	})
	e := f.buildEngine(t, fixtureOpts{
		sleep: func(ctx context.Context, d time.Duration) error { return nil },
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := waitErrBounded(t, e, 3*time.Second)
	if !errors.Is(err, errRedirectUserinfo) {
		t.Fatalf("wait = %v, want errRedirectUserinfo through the wrapper", err)
	}
	for _, leak := range []string{"user:pass", "pass@", "user@"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error text leaks %q: %v", leak, err)
		}
	}
}

// TestHLSKeyStatusPenaltyUsesKeyHost verifies the resource-host rule: a
// 429/503 on a key URI penalizes the key's host — never the segment's — and
// the retry wait consults the penalized host's deadline.
func TestHLSKeyStatusPenaltyUsesKeyHost(t *testing.T) {
	lim := transport.NewHostRateLimiter()
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{})
	e := f.buildEngine(t, fixtureOpts{hostLim: lim})
	now := e.now()
	keyHost := transport.MirrorHost("http://key.example/k.bin")
	segHost := transport.MirrorHost("http://seg.example/seg0")

	task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://seg.example/seg0"}}
	o := e.classifyStatus(task, &hlsStatusError{
		resource: "key", index: 0, status: http.StatusTooManyRequests,
		retryAfter: "30", host: keyHost,
	})
	if o.action != outcomeRetry || o.retryHost != keyHost {
		t.Fatalf("outcome = %+v, want retry on %s", o, keyHost)
	}
	if lim.BlockedUntil(keyHost, now).IsZero() {
		t.Fatal("key host not penalized")
	}
	if !lim.BlockedUntil(segHost, now).IsZero() {
		t.Fatal("segment host wrongly penalized for a key failure")
	}

	// Hand-built errors without a host fall back to the segment URI.
	task2 := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://seg.example/seg0"}}
	o2 := e.classifyStatus(task2, &hlsStatusError{
		resource: "segment", index: 0, status: http.StatusServiceUnavailable,
	})
	if o2.action != outcomeRetry || o2.retryHost != segHost {
		t.Fatalf("outcome = %+v, want retry on %s", o2, segHost)
	}
	if lim.BlockedUntil(segHost, now).IsZero() {
		t.Fatal("segment host not penalized on host-less status error")
	}
}

// TestHLSKeyFetchSuccessPrunesKeyHost proves §4.7's success-attribution: a
// successful key fetch records success on the key host, so a stale penalty
// entry is pruned and a later implicit penalty restarts at the base backoff.
func TestHLSKeyFetchSuccessPrunesKeyHost(t *testing.T) {
	key := make([]byte, 16)
	for i := range key {
		key[i] = byte(i + 1)
	}
	keySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(key)
	}))
	t.Cleanup(keySrv.Close)

	plain := []byte("prune-me")
	iv := make([]byte, 16)
	ct := encryptTestCBC(t, key, iv, plain)
	f := newHLSFixture(t, [][]byte{ct}, fixtureOpts{
		segTweak: func(i int, seg *types.HLSSegmentState) { seg.KeyIndex = 1 },
		keys:     []types.HLSKeyInfo{{Method: "AES-128", URI: keySrv.URL + "/k.bin"}},
	})
	lim := transport.NewHostRateLimiter()
	keyHost := transport.MirrorHost(keySrv.URL)
	// Stale, already-expired entry: an old penalty that would otherwise
	// leave its consecutive counter elevated for the next failure.
	lim.Penalize(keyHost, 0, false, time.Now().Add(-2*time.Second))

	e := f.buildEngine(t, fixtureOpts{
		hostLim: lim,
		sleep:   func(ctx context.Context, d time.Duration) error { return nil },
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := waitErrBounded(t, e, 3*time.Second); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); string(got) != "prune-me" {
		t.Fatalf("output %q", got)
	}
	// Base backoff = 1s ±20%; a doubled counter lands >=1.6s.
	until := lim.Penalize(keyHost, 0, false, time.Now())
	if d := time.Until(until); d > 1500*time.Millisecond {
		t.Fatalf("post-success implicit penalty %v — consecutive counter not reset", d)
	}
	if !lim.BlockedUntil(transport.MirrorHost(f.srv.URL), time.Now()).IsZero() {
		t.Fatal("segment host carries an unexplained penalty")
	}
}

// TestHLSKillDuringBackoffIsRefused documents the kill scope: between
// attempts (retry backoff, host penalties) no attempt is published, so the
// primitive reports false and the lease simply retries on schedule.
func TestHLSKillDuringBackoffIsRefused(t *testing.T) {
	var calls atomic.Int32
	slept := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	// Release a worker parked in the backoff sleep on any failure path —
	// a bare close after fallible assertions would strand it for the
	// rest of the package run.
	defer releaseOnce.Do(func() { close(release) })
	f := newHLSFixture(t, [][]byte{[]byte("backoff-payload")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte("backoff-payload"))
		},
	})
	e := f.buildEngine(t, fixtureOpts{
		sleep: func(ctx context.Context, d time.Duration) error {
			// A second backoff must not park on the full buffer and
			// strand the worker — the first observation still counts.
			select {
			case slept <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	select {
	case <-slept:
	case <-time.After(2 * time.Second):
		t.Fatal("engine never entered the retry backoff")
	}
	if e.cancelAttemptKill(0) {
		t.Fatal("kill reported success against a retired attempt")
	}
	if id := len(e.liveSessionIDs()); id != 1 {
		t.Fatalf("live sessions = %d, want 1 (kill must not touch the session)", id)
	}
	releaseOnce.Do(func() { close(release) })
	if err := waitErrBounded(t, e, 3*time.Second); err != nil {
		t.Fatalf("wait = %v — refused kill must leave the retry schedule intact", err)
	}
	if got := f.output(t); string(got) != "backoff-payload" {
		t.Fatalf("output %q", got)
	}
}

// TestHLSHostOverrideSurvivesSameSiteRedirect is the Go #22233 corner: the
// stdlib drops req.Host on redirects, so the guard must re-pin the anchor's
// override on same-site hops or the override silently dies at hop one.
func TestHLSHostOverrideSurvivesSameSiteRedirect(t *testing.T) {
	var final atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		final.Store(r.Host)
		_, _ = w.Write([]byte("host-payload"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{})
	f.seed.ManifestURL = srv.URL + "/media.m3u8"
	f.seed.Segments[0].URI = srv.URL + "/start"
	e, err := newSegmentEngine(segmentEngineParams{
		Seed: f.seed, Window: f.window, Progress: f.prog,
		Client: srv.Client(), TaskHeaders: map[string]string{"Host": "cdn.override.local"},
	})
	if err != nil {
		t.Fatalf("newSegmentEngine = %v", err)
	}
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := waitErrBounded(t, e, 3*time.Second); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := final.Load(); got != "cdn.override.local" {
		t.Fatalf("final hop Host = %v, want preserved override", got)
	}
}

// TestHLSStatusPermanentKeepsTypedError proves the terminal path keeps the
// *hlsStatusError reachable through errors.As — status and Retry-After stay
// readable after the permanent sentinel joins.
func TestHLSStatusPermanentKeepsTypedError(t *testing.T) {
	f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusUnauthorized)
		},
	})
	e := f.buildEngine(t, fixtureOpts{
		sleep: func(ctx context.Context, d time.Duration) error { return nil },
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := waitErrBounded(t, e, 3*time.Second)
	if !errors.Is(err, types.ErrPermanentHTTP) {
		t.Fatalf("wait = %v, want ErrPermanentHTTP", err)
	}
	se, ok := errors.AsType[*hlsStatusError](err)
	if !ok {
		t.Fatalf("wait = %v (%T), want *hlsStatusError reachable", err, err)
	}
	if se.status != http.StatusUnauthorized || se.retryAfter != "30" {
		t.Fatalf("typed status lost: %+v", se)
	}
	if n := f.requests.count("/seg0"); n != 1 {
		t.Fatalf("requests = %d, want exactly 1 (permanent never retries)", n)
	}
}

// TestHLSEngineRunCfgIsCloned pins the constructor contract: a
// caller-mutated RuntimeConfig must not leak into the engine's reads.
func TestHLSEngineRunCfgIsCloned(t *testing.T) {
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{})
	rc := types.DefaultRuntimeConfig()
	rc.SpeedEmaAlpha = 0.9
	e, err := newSegmentEngine(segmentEngineParams{
		Seed: f.seed, Window: f.window, Progress: f.prog,
		Client: f.srv.Client(), RunCfg: rc,
	})
	if err != nil {
		t.Fatalf("newSegmentEngine = %v", err)
	}
	rc.SpeedEmaAlpha = 0.01
	if e.runCfg == rc || e.runCfg.SpeedEmaAlpha != 0.9 {
		t.Fatal("engine shares the caller's RuntimeConfig pointer")
	}
}
