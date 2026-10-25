package scheduler

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

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// hlsVODOrigin serves a VOD media playlist plus segment bodies for
// scheduler-level tests.
type hlsVODOrigin struct {
	srv      *httptest.Server
	requests atomic.Int32
}

func newHLSVODOrigin(t *testing.T, manifestStatus int) *hlsVODOrigin {
	t.Helper()
	o := &hlsVODOrigin{}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.requests.Add(1)
		switch r.URL.Path {
		case "/media.m3u8":
			if manifestStatus != http.StatusOK {
				w.WriteHeader(manifestStatus)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:4.0,\ns0.ts\n#EXTINF:4.0,\ns1.ts\n#EXT-X-ENDLIST\n"))
		case "/s0.ts":
			_, _ = w.Write([]byte("seg-0"))
		case "/s1.ts":
			_, _ = w.Write([]byte("seg-1"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(o.srv.Close)
	return o
}

// pausedHLSActive registers a fully-settled paused HLS download directly in
// the pool — the UpdateURL acceptance path requires an active entry.
func pausedHLSActive(pool *Scheduler, id, url string, hr *types.HLSResumeState) *activeDownload {
	prog := progress.New(id, 0)
	prog.Paused.Store(true)
	ad := &activeDownload{
		config: types.DownloadRecord{
			ID:            id,
			URL:           url,
			URLHash:       store.URLHash(url),
			Strategy:      types.StrategyHLS,
			HLSResume:     hr,
			Downloaded:    64,
			TotalSize:     64,
			ProgressState: prog,
		},
		done: make(chan struct{}),
	}
	pool.mu.Lock()
	pool.downloads[id] = ad
	pool.mu.Unlock()
	return ad
}

func TestHLSUpdateURL_SchedulerMatrix(t *testing.T) {
	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	oldURL := "http://a.test/media.m3u8"
	newURL := "http://b.test/v2.m3u8"

	// Queued HLS entries refuse — the queue owns the claim. The entry is
	// injected straight into p.queued: no queueOrder seat and no Signal, so
	// the parked worker can never claim it and nothing here touches the
	// network. The wg.Add balances the entry GracefulShutdown releases.
	pool.mu.Lock()
	token, tokOK := pool.allocTokenLocked()
	if !tokOK {
		pool.mu.Unlock()
		t.Fatal("token mint failed")
	}
	pool.queued["hls-queued"] = &queuedTask{
		cfg: types.DownloadRecord{
			ID: "hls-queued", URL: oldURL, OutputPath: t.TempDir(), Filename: "q.ts",
			ProgressState: progress.New("hls-queued", 0), Runtime: &types.RuntimeConfig{},
			Strategy: types.StrategyHLS,
		},
		token: token,
	}
	pool.wg.Add(1)
	pool.mu.Unlock()
	if err := pool.UpdateURL("hls-queued", newURL); !errors.Is(err, types.ErrQueuedUpdate) {
		t.Fatalf("queued UpdateURL = %v, want ErrQueuedUpdate", err)
	}

	// A running HLS claim refuses even when the paused flag is set — the
	// physical claim is what matters.
	running := pausedHLSActive(pool, "hls-running", oldURL, nil)
	running.running.Store(true)
	if err := pool.UpdateURL("hls-running", newURL); !errors.Is(err, types.ErrActiveUpdate) {
		t.Fatalf("running UpdateURL = %v, want ErrActiveUpdate", err)
	}
	running.running.Store(false)

	// An unsettled busy claim for the ID refuses even with no downloads
	// entry at all — claim-before-registration must stay observable.
	pool.mu.Lock()
	pool.busy[AdmissionKey{ID: "hls-busy", Token: 7}] = &busySlot{strategy: types.StrategyHLS}
	pool.mu.Unlock()
	if err := pool.UpdateURL("hls-busy", newURL); !errors.Is(err, types.ErrActiveUpdate) {
		t.Fatalf("busy-claim UpdateURL = %v, want ErrActiveUpdate", err)
	}
	pool.mu.Lock()
	delete(pool.busy, AdmissionKey{ID: "hls-busy", Token: 7})
	pool.mu.Unlock()

	// A fully-settled paused HLS task accepts the rebase: URL/hash swapped,
	// progress zeroed, the checkpoint collapsed to a minimal anchor-only
	// seed, and the progress state follows.
	prior := &types.HLSResumeState{
		ManifestURL:      oldURL,
		MediaPlaylistURL: oldURL,
		TotalSegments:    3,
		CompletedBitmap:  []byte{0x01},
		MergedSegments:   1,
		MergedBytes:      64,
		VerifiedBytes:    64,
		Segments:         []types.HLSSegmentState{{Index: 0, URI: "http://a.test/s0.ts"}},
	}
	ad := pausedHLSActive(pool, "hls-paused", oldURL, prior)
	progress.CfgProgress(&ad.config).SetPendingResumeState(&types.DownloadRecord{ID: "hls-paused"})
	if err := pool.UpdateURL("hls-paused", newURL); err != nil {
		t.Fatalf("paused UpdateURL = %v", err)
	}
	cfg := ad.config
	if cfg.URL != newURL || cfg.URLHash != store.URLHash(newURL) {
		t.Fatalf("URL/URLHash = %q/%q", cfg.URL, cfg.URLHash)
	}
	if cfg.Downloaded != 0 || cfg.TotalSize != 0 {
		t.Fatalf("progress not zeroed: downloaded=%d total=%d", cfg.Downloaded, cfg.TotalSize)
	}
	hr := cfg.HLSResume
	if hr == nil || hr.ManifestURL != newURL || hr.MediaPlaylistURL != "" ||
		hr.TotalSegments != 0 || len(hr.Segments) != 0 || hr.MergedBytes != 0 ||
		!hr.RecordEligible {
		t.Fatalf("rebased HLSResume = %+v", hr)
	}
	if prior.ManifestURL != oldURL || prior.MergedSegments != 1 {
		t.Fatal("UpdateURL mutated the prior checkpoint object")
	}
	prog := progress.CfgProgress(&ad.config)
	if prog.GetURL() != newURL {
		t.Fatalf("progress URL = %q", prog.GetURL())
	}
	if prog.TakePendingResumeState() != nil {
		t.Fatal("pending resume state survived the rebase")
	}

	// Non-HLS paused entries keep the legacy shallow update — URL only, no
	// progress zeroing.
	nonHLS := pausedHLSActive(pool, "paused-plain", oldURL, nil)
	nonHLS.config.Strategy = types.StrategyAuto
	nonHLS.config.Downloaded = 500
	nonHLS.config.TotalSize = 1000
	if err := pool.UpdateURL("paused-plain", newURL); err != nil {
		t.Fatalf("non-HLS UpdateURL = %v", err)
	}
	if nonHLS.config.URL != newURL || nonHLS.config.Downloaded != 500 || nonHLS.config.TotalSize != 1000 {
		t.Fatalf("non-HLS update clobbered progress: %+v", nonHLS.config)
	}
}

func TestScheduler_HLSCancelSuppressesPostRemovalError(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	o := newHLSVODOrigin(t, http.StatusInternalServerError)

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-cancel-fence"
	// Fire the cancel inside the worker-done window: the run has already
	// failed, but its error event must still be suppressed — removal owns
	// the lifecycle from here on.
	var once atomic.Bool
	doneGate := func() {
		if once.CompareAndSwap(false, true) {
			res := pool.Cancel(id)
			if !res.Found {
				t.Error("Cancel inside done gate found no claim")
			}
		}
	}
	oldDone := pool.workerDoneGate.Load()
	t.Cleanup(func() {
		pool.workerDoneGate.Store(oldDone)
		pool.workerDoneGate.Store(nil)
	})
	pool.workerDoneGate.Store(&doneGate)

	if !pool.Add(types.DownloadRecord{
		ID: id, URL: o.srv.URL + "/media.m3u8", OutputPath: t.TempDir(), Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: &types.RuntimeConfig{},
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}

	// Observe the full event stream: EventStarted is fine, but neither
	// EventError nor an EventQueued retry may follow a removed claim.
	deadline := time.After(4 * time.Second)
	var sawStarted bool
	for {
		select {
		case ev := <-ch:
			if ev.DownloadID != id {
				continue
			}
			switch ev.Type {
			case types.EventStarted:
				sawStarted = true
			case types.EventError:
				t.Fatalf("EventError leaked past the removal fence: %v", ev.Err)
			case types.EventQueued:
				t.Fatal("removed claim requeued")
			case types.EventComplete:
				t.Fatal("removed claim completed")
			}
		case <-deadline:
			if !sawStarted {
				t.Fatal("run never started")
			}
			if !once.Load() {
				t.Fatal("cancel gate never fired")
			}
			return
		}
	}
}

func TestScheduler_HLSOrdinaryErrorRetries(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	// First manifest request 500s (non-permanent), the retry succeeds.
	o := newHLSVODOrigin(t, http.StatusOK)
	var manifestCalls atomic.Int32
	base := o.srv.Config.Handler
	o.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media.m3u8" && manifestCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		base.ServeHTTP(w, r)
	})

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-retry"
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: o.srv.URL + "/media.m3u8", OutputPath: t.TempDir(), Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: &types.RuntimeConfig{},
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}

	starts, queued, completes := 0, 0, 0
	deadline := time.After(15 * time.Second)
	for completes == 0 {
		select {
		case ev := <-ch:
			if ev.DownloadID != id {
				continue
			}
			switch ev.Type {
			case types.EventStarted:
				starts++
			case types.EventQueued:
				queued++
			case types.EventComplete:
				completes++
			case types.EventError:
				t.Fatalf("EventError = %v — a transient failure must retry, not terminate", ev.Err)
			}
		case <-deadline:
			t.Fatalf("retry never completed (starts=%d queued=%d)", starts, queued)
		}
	}
	if starts != 2 || queued != 1 {
		t.Fatalf("event counts: starts=%d queued=%d, want 2 starts + 1 requeue", starts, queued)
	}
	if manifestCalls.Load() != 2 {
		t.Fatalf("manifest fetches = %d, want 2 (failed attempt + retry)", manifestCalls.Load())
	}
}

func TestScheduler_HLSPermanentErrorIsTerminal(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	var manifestCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifestCalls.Add(1)
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:4.0,\ns0.ts\n")) // no ENDLIST — live
	}))
	t.Cleanup(srv.Close)

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)
	const id = "hls-perm"
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: srv.URL + "/media.m3u8", OutputPath: t.TempDir(), Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: &types.RuntimeConfig{},
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}
	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.DownloadID != id {
				continue
			}
			if ev.Type == types.EventQueued {
				t.Fatal("permanent HLS error requeued")
			}
			if ev.Type == types.EventError {
				if !types.IsPermanentHLSError(ev.Err) {
					t.Fatalf("EventError.Err = %v, want permanent HLS class", ev.Err)
				}
				// Terminal: exactly one fetch, no retry.
				if manifestCalls.Load() != 1 {
					t.Fatalf("manifest fetches = %d, want 1", manifestCalls.Load())
				}
				return
			}
		case <-deadline:
			t.Fatal("terminal EventError never arrived")
		}
	}
}

func TestHLSUpdateURL_SchedulerBusyClaimIdentity(t *testing.T) {
	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	oldURL := "http://a.test/media.m3u8"
	newURL := "http://b.test/v2.m3u8"

	// A fully settled paused ad, plus a stale busy claim whose ad is NOT
	// the downloads entry but still names the ID — a late checkpoint from
	// that claim could rewrite the rebased detail, so the update refuses.
	pausedHLSActive(pool, "hls-stale-claim", oldURL, nil)
	stale := &activeDownload{config: types.DownloadRecord{ID: "hls-stale-claim", Strategy: types.StrategyHLS}}
	pool.mu.Lock()
	pool.busy[AdmissionKey{ID: "hls-stale-claim", Token: 41}] = &busySlot{ad: stale}
	pool.mu.Unlock()
	if err := pool.UpdateURL("hls-stale-claim", newURL); !errors.Is(err, types.ErrActiveUpdate) {
		t.Fatalf("stale busy-claim UpdateURL = %v, want ErrActiveUpdate", err)
	}
	pool.mu.RLock()
	gotURL := pool.downloads["hls-stale-claim"].config.URL
	pool.mu.RUnlock()
	if gotURL != oldURL {
		t.Fatalf("refused update still rewrote the URL: %q", gotURL)
	}
	pool.mu.Lock()
	delete(pool.busy, AdmissionKey{ID: "hls-stale-claim", Token: 41})
	pool.mu.Unlock()

	// A same-ID busy slot with no ad yet is a claim headed for the
	// occupancy drop — while a downloads entry exists it can never
	// register, so it does not block the rebase.
	pausedHLSActive(pool, "hls-pre-register", oldURL, nil)
	pool.mu.Lock()
	pool.busy[AdmissionKey{ID: "hls-pre-register", Token: 7}] = &busySlot{strategy: types.StrategyHLS}
	pool.mu.Unlock()
	if err := pool.UpdateURL("hls-pre-register", newURL); err != nil {
		t.Fatalf("pre-register claim UpdateURL = %v", err)
	}
	pool.mu.Lock()
	delete(pool.busy, AdmissionKey{ID: "hls-pre-register", Token: 7})
	pool.mu.Unlock()
}

// TestScheduler_HLSCancelCrossesBlockedErrorSend pins the full-channel
// interleave: the worker holds the claim fence while its EventError send
// is wedged on a saturated channel; Cancel must cancel the claim ctx so
// the send drops and the removal barrier completes promptly — and no
// EventError may ever land.
func TestScheduler_HLSCancelCrossesBlockedErrorSend(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	var manifestCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifestCalls.Add(1)
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:4.0,\ns0.ts\n")) // no ENDLIST — permanent
	}))
	t.Cleanup(srv.Close)

	// Capacity 1: EventStarted occupies the channel; the terminal send is
	// then guaranteed to block inside the fence.
	ch := make(chan types.DownloadEvent, 1)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-blocked-send"
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: srv.URL + "/index.m3u8", OutputPath: t.TempDir(), Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: &types.RuntimeConfig{},
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}

	// The claim outlives the downloads entry here: the worker removes
	// downloads[id] before the wedged terminal send, so the busy slot is the
	// only reliable handle while the send blocks.
	var ad *activeDownload
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && ad == nil {
		pool.mu.RLock()
		for _, slot := range pool.busy {
			if slot.ad != nil && slot.ad.config.ID == id {
				ad = slot.ad
			}
		}
		pool.mu.RUnlock()
		if ad == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if ad == nil {
		t.Fatal("claim never registered")
	}
	// The fence is held from the terminal decision through the send —
	// a failed TryLock means the worker is inside it (the send cannot
	// complete on a permanently full channel).
	fenced := false
	for time.Now().Before(deadline) && !fenced {
		if ad.hlsEventMu.TryLock() {
			ad.hlsEventMu.Unlock()
			time.Sleep(time.Millisecond)
			continue
		}
		fenced = true
	}
	if !fenced {
		t.Fatal("worker never reached the claim fence")
	}

	start := time.Now()
	pool.Cancel(id)
	if elapsed := time.Since(start); elapsed >= terminalSendTimeout {
		t.Fatalf("Cancel waited %v on a blocked send — the claim ctx must interrupt it", elapsed)
	}

	for len(ch) > 0 {
		ev := <-ch
		if ev.DownloadID != id {
			continue
		}
		switch ev.Type {
		case types.EventError:
			t.Fatalf("EventError leaked past the removal fence: %v", ev.Err)
		case types.EventQueued:
			t.Fatal("removed claim requeued")
		}
	}
}

// TestScheduler_HLSQueuedEventFencedByCancel pins the requeue window: the
// retry is registered and the worker is about to publish EventQueued when
// Cancel claims it — the queued event must never land after removal.
func TestScheduler_HLSQueuedEventFencedByCancel(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	o := newHLSVODOrigin(t, http.StatusOK)
	var manifestCalls atomic.Int32
	base := o.srv.Config.Handler
	o.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/media.m3u8" && manifestCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		base.ServeHTTP(w, r)
	})

	// Capacity 1: EventStarted occupies the channel, so the EventQueued
	// send can only resolve by suppression or claim-ctx cancel.
	ch := make(chan types.DownloadEvent, 1)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-queued-fence"
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: o.srv.URL + "/media.m3u8", OutputPath: t.TempDir(), Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: &types.RuntimeConfig{},
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}

	// The requeue lands in p.queued before the EventQueued send — cancel in
	// that window so the marker is set before (or while) the fence is taken.
	deadline := time.Now().Add(8 * time.Second)
	requeued := false
	for time.Now().Before(deadline) && !requeued {
		pool.mu.RLock()
		_, requeued = pool.queued[id]
		pool.mu.RUnlock()
		if !requeued {
			time.Sleep(time.Millisecond)
		}
	}
	if !requeued {
		t.Fatal("transient failure never requeued")
	}
	res := pool.Cancel(id)
	if !res.Found {
		t.Fatal("Cancel found no claim")
	}

	for len(ch) > 0 {
		ev := <-ch
		if ev.DownloadID != id {
			continue
		}
		switch ev.Type {
		case types.EventQueued:
			t.Fatal("EventQueued landed after the claim was removed")
		case types.EventError:
			t.Fatalf("EventError leaked past the removal fence: %v", ev.Err)
		}
	}
	pool.mu.RLock()
	_, still := pool.queued[id]
	pool.mu.RUnlock()
	if still {
		t.Fatal("removed claim still queued")
	}
}

// TestScheduler_HLSPausedEventFencedByCancel pins the pause path: removal
// marked inside the worker-done gate must suppress the sparse EventPaused
// even though the send check happens after the run returned.
func TestScheduler_HLSPausedEventFencedByCancel(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	o := newHLSVODOrigin(t, http.StatusOK)
	// Keep the run in flight long enough for the pause to land mid-download.
	base := o.srv.Config.Handler
	o.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/s1.ts" {
			time.Sleep(300 * time.Millisecond)
		}
		base.ServeHTTP(w, r)
	})

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-paused-fence"
	var once atomic.Bool
	doneGate := func() {
		// Removal wins inside the done-gate window: the pause-path send
		// below must observe the marker through the claim fence.
		if once.CompareAndSwap(false, true) {
			pool.Cancel(id)
		}
	}
	oldDone := pool.workerDoneGate.Load()
	t.Cleanup(func() { pool.workerDoneGate.Store(oldDone) })
	pool.workerDoneGate.Store(&doneGate)

	if !pool.Add(types.DownloadRecord{
		ID: id, URL: o.srv.URL + "/media.m3u8", OutputPath: t.TempDir(), Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: &types.RuntimeConfig{},
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}

	// Pause the claim once it is running; the canceled run then exits
	// through the sparse EventPaused path.
	deadline := time.Now().Add(8 * time.Second)
	paused := false
	for time.Now().Before(deadline) && !paused {
		pool.mu.RLock()
		ad := pool.downloads[id]
		running := ad != nil && ad.running.Load()
		pool.mu.RUnlock()
		if running {
			if r := pool.Pause(id); r.Found {
				paused = true
			}
		}
		if !paused {
			time.Sleep(time.Millisecond)
		}
	}
	if !paused {
		t.Fatal("claim never became pausable")
	}

	// Drain with a deadline: after the fenced settle, no EventPaused,
	// EventError or EventQueued may exist for a removed claim.
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-ch:
			if ev.DownloadID != id {
				continue
			}
			switch ev.Type {
			case types.EventPaused:
				t.Fatal("EventPaused landed after the claim was removed")
			case types.EventError:
				t.Fatalf("EventError leaked past the removal fence: %v", ev.Err)
			case types.EventQueued:
				t.Fatal("removed claim requeued")
			}
		case <-timer.C:
			if !once.Load() {
				t.Fatal("cancel gate never fired")
			}
			return
		}
	}
}

func TestRunDownload_HLSStrategy_MutuallyExclusiveNeverFallsBackToSingle(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)

	segPayload := []byte("SEGMENT-PAYLOAD-0123456789")
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:4.0,\nseg0.ts\n#EXT-X-ENDLIST\n"))
		case "/seg0.ts":
			_, _ = w.Write(segPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	progressCh := make(chan types.DownloadEvent, 16)
	cfg := types.DownloadRecord{
		URL:           ts.URL + "/index.m3u8",
		OutputPath:    tmpDir,
		Filename:      "video.ts",
		ID:            "hls-dispatch",
		ProgressCh:    progressCh,
		ProgressState: progress.New("hls-dispatch", 0),
		Runtime:       &types.RuntimeConfig{},
		// A single-stream-looking config on purpose: the strategy branch must
		// win before the concurrent/single selection even runs.
		SupportsRange: false,
		Strategy:      types.StrategyHLS,
	}

	if err := RunDownload(context.Background(), &cfg); err != nil {
		t.Fatalf("RunDownload = %v", err)
	}
	// The HLS pipeline ran: manifest fetch plus the segment fetch — a
	// single-file fallback would have written the playlist body itself.
	if requests.Load() < 2 {
		t.Fatalf("server requests = %d, want manifest+segment fetches", requests.Load())
	}
	surge, err := os.ReadFile(filepath.Join(tmpDir, "video.ts") + types.IncompleteSuffix)
	if err != nil {
		t.Fatalf("read .surge: %v", err)
	}
	if string(surge) != string(segPayload) {
		t.Fatalf(".surge content %q, want segment payload (playlist body must never land on disk)", surge)
	}
	if cfg.TotalSize != int64(len(segPayload)) {
		t.Fatalf("cfg.TotalSize = %d, want %d (physical merged size)", cfg.TotalSize, len(segPayload))
	}
	var sawComplete bool
	for len(progressCh) > 0 {
		ev := <-progressCh
		if ev.Type == types.EventComplete {
			sawComplete = true
			if ev.Total != int64(len(segPayload)) {
				t.Fatalf("EventComplete.Total = %d, want %d", ev.Total, len(segPayload))
			}
		}
	}
	if !sawComplete {
		t.Fatal("HLS run finished without EventComplete")
	}

	// A pre-canceled context reports the context error without touching the
	// network.
	requests.Store(0)
	cfg2 := cfg
	cfg2.ProgressCh = nil
	cfg2.ID = "hls-dispatch-canceled"
	cfg2.OutputPath = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunDownload(ctx, &cfg2); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RunDownload = %v, want context.Canceled", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("canceled run still hit the server %d times", requests.Load())
	}
}

func TestScheduler_HLSStrategy_TerminalNotRetried(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)

	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer ts.Close()

	ch := make(chan types.DownloadEvent, 16)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-terminal"
	tmpDir := t.TempDir()
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           ts.URL + "/index.m3u8",
		OutputPath:    tmpDir,
		Filename:      "v.ts",
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{},
		Strategy:      types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}

	sawStarted := false
	sawError := false
	deadline := time.After(10 * time.Second)
	for !sawError {
		select {
		case ev := <-ch:
			if ev.DownloadID != id {
				continue
			}
			switch ev.Type {
			case types.EventStarted:
				sawStarted = true
			case types.EventQueued:
				t.Fatal("a permanent manifest error must not requeue the task")
			case types.EventComplete:
				t.Fatal("an invalid manifest must never complete")
			case types.EventError:
				if !types.IsPermanentHLSError(ev.Err) {
					t.Fatalf("EventError.Err = %v, want a permanent HLS error", ev.Err)
				}
				sawError = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for terminal EventError")
		}
	}
	if !sawStarted {
		t.Fatal("expected EventStarted before the terminal error")
	}
	// Exactly one manifest fetch: the permanent error ends the task at
	// bootstrap — no segment fetches, no retries.
	if requests.Load() != 1 {
		t.Fatalf("server requests = %d, want 1", requests.Load())
	}
}
