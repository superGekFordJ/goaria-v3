package scheduler

import (
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestScheduler_HLSUpdateURLMatrix(t *testing.T) {
	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	oldURL := "http://a.test/media.m3u8"
	newURL := "http://b.test/v2.m3u8"

	// Queued HLS entries refuse — the queue owns the claim.
	if !pool.Add(types.DownloadRecord{
		ID: "hls-queued", URL: oldURL, OutputPath: t.TempDir(), Filename: "q.ts",
		ProgressState: progress.New("hls-queued", 0), Runtime: &types.RuntimeConfig{},
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("queued add refused")
	}
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
