package scheduler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// gatedHLSOrigin serves a 4-segment VOD: s0 instant, s1 under a gate, s2/s3
// instant — so an in-flight run parks with two out-of-order pending payloads.
type gatedHLSOrigin struct {
	srv   *httptest.Server
	gate  chan struct{}
	mu    sync.Mutex
	count map[string]int
	s1    http.HandlerFunc
}

func newGatedHLSOrigin(t *testing.T, s1 http.HandlerFunc) *gatedHLSOrigin {
	t.Helper()
	o := &gatedHLSOrigin{gate: make(chan struct{}), count: make(map[string]int), s1: s1}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.count[r.URL.Path]++
		o.mu.Unlock()
		switch r.URL.Path {
		case "/media.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:4.0,\ns0.ts\n#EXTINF:4.0,\ns1.ts\n#EXTINF:4.0,\ns2.ts\n#EXTINF:4.0,\ns3.ts\n#EXT-X-ENDLIST\n"))
		case "/s0.ts":
			_, _ = w.Write([]byte("seg-0"))
		case "/s1.ts":
			if o.s1 != nil {
				o.s1.ServeHTTP(w, r)
				return
			}
			<-o.gate
			_, _ = w.Write([]byte("seg-1"))
		case "/s2.ts":
			_, _ = w.Write([]byte("seg-2"))
		case "/s3.ts":
			_, _ = w.Write([]byte("seg-3"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *gatedHLSOrigin) hits(path string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.count[path]
}

// blockedOnRequest honors client cancellation so a pause-settled run never
// wedges the handler.
func blockedOnRequest(gate <-chan struct{}, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write(body)
	}
}

// waitPendingSettle polls until s2 and s3 have been fetched (and held), so a
// pause/error observes a pending-backed checkpoint rather than a prefix-only
// one.
func waitPendingSettle(t *testing.T, o *gatedHLSOrigin) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for o.hits("/s2.ts") == 0 || o.hits("/s3.ts") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("out-of-order segments never fetched: s2=%d s3=%d", o.hits("/s2.ts"), o.hits("/s3.ts"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Small settle for the ingest path past the observed requests.
	time.Sleep(60 * time.Millisecond)
}

func TestScheduler_HLSPauseEventCarriesSpillCheckpoint(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	gate := make(chan struct{})
	o := newGatedHLSOrigin(t, blockedOnRequest(gate, []byte("seg-1")))
	defer close(gate)

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-pause-full"
	destDir := t.TempDir()
	rt := &types.RuntimeConfig{MaxConnectionsPerDownload: 4}
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: o.srv.URL + "/media.m3u8", OutputPath: destDir, Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: rt,
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}
	waitPendingSettle(t, o)

	if r := pool.Pause(id); !r.Found {
		t.Fatal("Pause found no claim")
	}

	var pausedEv *types.DownloadEvent
	pausedCount := 0
	deadline := time.After(6 * time.Second)
	for pausedEv == nil {
		select {
		case ev := <-ch:
			if ev.DownloadID != id {
				continue
			}
			switch ev.Type {
			case types.EventPaused:
				pausedCount++
				if pausedEv == nil {
					e := ev
					pausedEv = &e
				}
			case types.EventError:
				t.Fatalf("pause leaked EventError: %v", ev.Err)
			case types.EventQueued:
				t.Fatal("paused task requeued")
			}
		case <-deadline:
			t.Fatal("EventPaused never arrived")
		}
	}
	// No second pause event may follow — drain whatever remains briefly.
	drainTimer := time.NewTimer(300 * time.Millisecond)
	for draining := true; draining; {
		select {
		case ev := <-ch:
			if ev.DownloadID == id && ev.Type == types.EventPaused {
				pausedCount++
			}
		case <-drainTimer.C:
			draining = false
		}
	}
	if pausedCount != 1 {
		t.Fatalf("EventPaused count = %d, want exactly 1", pausedCount)
	}

	if pausedEv.State == nil || pausedEv.State.HLSResume == nil {
		t.Fatal("EventPaused carried no checkpoint state")
	}
	hr := pausedEv.State.HLSResume
	wantV := int64(len("seg-0") + len("seg-2") + len("seg-3"))
	if pausedEv.State.Downloaded != hr.VerifiedBytes || pausedEv.Downloaded != hr.VerifiedBytes {
		t.Fatalf("event D=%d state D=%d V=%d", pausedEv.Downloaded, pausedEv.State.Downloaded, hr.VerifiedBytes)
	}
	if hr.VerifiedBytes != wantV || hr.MergedSegments != 1 {
		t.Fatalf("checkpoint V=%d merged=%d, want V=%d", hr.VerifiedBytes, hr.MergedSegments, wantV)
	}
	if len(hr.SpillEntries) != 2 || hr.SpillEntries[0].SegmentIndex != 2 || hr.SpillEntries[1].SegmentIndex != 3 {
		t.Fatalf("spill entries = %+v", hr.SpillEntries)
	}
	if pausedEv.State.Strategy != types.StrategyHLS || pausedEv.State.ID != id {
		t.Fatalf("event state identity wrong: %q %q", pausedEv.State.Strategy, pausedEv.State.ID)
	}
	// The fenced publisher also persisted the checkpoint into the detail gob.
	details, err := store.LoadStates([]string{id})
	if err != nil || details[id] == nil || details[id].HLSResume == nil {
		t.Fatalf("pause checkpoint not persisted: %v", err)
	}
	saved := details[id]
	if saved.HLSResume.VerifiedBytes != wantV || len(saved.HLSResume.SpillEntries) != 2 {
		t.Fatalf("persisted checkpoint V=%d entries=%d", saved.HLSResume.VerifiedBytes, len(saved.HLSResume.SpillEntries))
	}
	if saved.Downloaded != wantV {
		t.Fatalf("persisted Downloaded = %d, want %d", saved.Downloaded, wantV)
	}
	// The Pausing transition gate must be open again so Resume can proceed.
	pool.mu.RLock()
	ad := pool.downloads[id]
	pool.mu.RUnlock()
	if ad == nil {
		t.Fatal("paused claim vanished")
	}
	if progress.CfgProgress(&ad.config).IsPausing() {
		t.Fatal("Pausing gate never cleared after pause publication")
	}
}

// TestScheduler_HLSPauseSaveFailureStillPublishes pins the fenced pause
// publisher's degraded path: when the detail save fails inside the claim
// fence, EventPaused still carries the synced checkpoint (the lifecycle
// consumer may persist it later) — and a repeated Pause stays a no-op so
// the publication remains exactly-once.
func TestScheduler_HLSPauseSaveFailureStillPublishes(t *testing.T) {
	stateRoot := t.TempDir()
	store.Configure(filepath.Join(stateRoot, "state.db"))
	t.Cleanup(store.CloseDB)
	gate := make(chan struct{})
	o := newGatedHLSOrigin(t, blockedOnRequest(gate, []byte("seg-1")))
	defer close(gate)

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-pause-savefail"
	destDir := t.TempDir()
	rt := &types.RuntimeConfig{MaxConnectionsPerDownload: 4}
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: o.srv.URL + "/media.m3u8", OutputPath: destDir, Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: rt,
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}
	waitPendingSettle(t, o)

	// Break exactly this task's detail path: the atomic rename inside the
	// fenced save cannot replace a directory, so the save fails for sure
	// once the pause publisher reaches it.
	gobPath := filepath.Join(stateRoot, "details", id+".gob")
	if err := os.Remove(gobPath); err != nil {
		t.Fatalf("first-segment detail gob missing: %v", err)
	}
	if err := os.Mkdir(gobPath, 0o700); err != nil {
		t.Fatalf("block detail path: %v", err)
	}

	if r := pool.Pause(id); !r.Found {
		t.Fatal("Pause found no claim")
	}
	// A repeated Pause while the flag is already set must stay a no-op —
	// the publication below must still be exactly once.
	if r := pool.Pause(id); !r.Found {
		t.Fatal("second Pause found no claim")
	}

	var pausedEv *types.DownloadEvent
	pausedCount := 0
	deadline := time.After(6 * time.Second)
	for pausedEv == nil {
		select {
		case ev := <-ch:
			if ev.DownloadID != id {
				continue
			}
			switch ev.Type {
			case types.EventPaused:
				pausedCount++
				if pausedEv == nil {
					e := ev
					pausedEv = &e
				}
			case types.EventError:
				t.Fatalf("pause leaked EventError: %v", ev.Err)
			case types.EventQueued:
				t.Fatal("paused task requeued")
			}
		case <-deadline:
			t.Fatal("EventPaused never arrived")
		}
	}
	drainTimer := time.NewTimer(300 * time.Millisecond)
	for draining := true; draining; {
		select {
		case ev := <-ch:
			if ev.DownloadID == id && ev.Type == types.EventPaused {
				pausedCount++
			}
		case <-drainTimer.C:
			draining = false
		}
	}
	if pausedCount != 1 {
		t.Fatalf("EventPaused count = %d, want exactly 1", pausedCount)
	}
	// The save failed — yet the event still carries the synced checkpoint.
	if pausedEv.State == nil || pausedEv.State.HLSResume == nil {
		t.Fatal("EventPaused carried no checkpoint state")
	}
	hr := pausedEv.State.HLSResume
	wantV := int64(len("seg-0") + len("seg-2") + len("seg-3"))
	if pausedEv.State.Downloaded != hr.VerifiedBytes || pausedEv.Downloaded != hr.VerifiedBytes {
		t.Fatalf("event D=%d state D=%d V=%d", pausedEv.Downloaded, pausedEv.State.Downloaded, hr.VerifiedBytes)
	}
	if hr.VerifiedBytes != wantV || len(hr.SpillEntries) != 2 {
		t.Fatalf("checkpoint V=%d entries=%d, want V=%d + 2 entries", hr.VerifiedBytes, len(hr.SpillEntries), wantV)
	}
	// The detail gob was never written — the blocked path is still a dir.
	if fi, err := os.Lstat(gobPath); err != nil || !fi.IsDir() {
		t.Fatalf("detail path state = %v — the failed save must not have landed", err)
	}
	states, _ := store.LoadStates([]string{id})
	if rec := states[id]; rec != nil {
		t.Fatalf("detail persisted despite the forced failure: %+v", rec)
	}
	// The Pausing transition gate must be open again even after a failed save.
	pool.mu.RLock()
	ad := pool.downloads[id]
	pool.mu.RUnlock()
	if ad == nil {
		t.Fatal("paused claim vanished")
	}
	if progress.CfgProgress(&ad.config).IsPausing() {
		t.Fatal("Pausing gate never cleared after the failed save")
	}
}

func TestScheduler_HLSErrorEventCarriesPendingCheckpoint(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	gate := make(chan struct{})
	failAfter := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusNotFound) // permanent — no retry
	}
	o := newGatedHLSOrigin(t, http.HandlerFunc(failAfter))

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-err-full"
	destDir := t.TempDir()
	rt := &types.RuntimeConfig{MaxConnectionsPerDownload: 4}
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: o.srv.URL + "/media.m3u8", OutputPath: destDir, Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: rt,
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}
	waitPendingSettle(t, o)
	close(gate)

	var errEv *types.DownloadEvent
	errCount := 0
	deadline := time.After(6 * time.Second)
	for errEv == nil {
		select {
		case ev := <-ch:
			if ev.DownloadID != id {
				continue
			}
			switch ev.Type {
			case types.EventError:
				errCount++
				if errEv == nil {
					e := ev
					errEv = &e
				}
			case types.EventQueued:
				t.Fatal("permanent error requeued")
			case types.EventPaused:
				t.Fatal("error published as pause")
			}
		case <-deadline:
			t.Fatal("EventError never arrived")
		}
	}
	drainTimer := time.NewTimer(300 * time.Millisecond)
	for draining := true; draining; {
		select {
		case ev := <-ch:
			if ev.DownloadID == id && ev.Type == types.EventError {
				errCount++
			}
		case <-drainTimer.C:
			draining = false
		}
	}
	if errCount != 1 {
		t.Fatalf("EventError count = %d, want exactly 1", errCount)
	}
	if errEv.State == nil || errEv.State.HLSResume == nil {
		t.Fatal("EventError carried no checkpoint state")
	}
	hr := errEv.State.HLSResume
	wantV := int64(len("seg-0") + len("seg-2") + len("seg-3"))
	if errEv.Downloaded != hr.VerifiedBytes || hr.VerifiedBytes != wantV {
		t.Fatalf("event D=%d V=%d, want %d", errEv.Downloaded, hr.VerifiedBytes, wantV)
	}
	if len(hr.SpillEntries) != 2 {
		t.Fatalf("spill entries = %+v", hr.SpillEntries)
	}
	// The downloader saved the checkpoint itself — the persisted detail
	// already carries the full ledger.
	details, err := store.LoadStates([]string{id})
	if err != nil || details[id] == nil || details[id].HLSResume == nil {
		t.Fatalf("error checkpoint not persisted: %v", err)
	}
	saved := details[id]
	if saved.HLSResume.VerifiedBytes != wantV || len(saved.HLSResume.SpillEntries) != 2 {
		t.Fatalf("persisted V=%d entries=%d", saved.HLSResume.VerifiedBytes, len(saved.HLSResume.SpillEntries))
	}
}

// TestScheduler_HLSRetryConsumesSinglePendingSnapshot pins the single-take
// contract: a transient failure with pending payloads produces exactly one
// EventQueued requeue — and the retried run adopts the spill ledger, so the
// final state completes without re-fetching the spilled segments.
func TestScheduler_HLSRetryAdoptsSpillCheckpoint(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	var s1Calls atomic.Int32
	gate := make(chan struct{})
	// The first attempt parks until pending forms; attempts 1..6 of run 1 all
	// 500 with Retry-After: 0 so the per-segment budget burns without sleeps —
	// a whole-run transient failure. Run 2's first attempt (call 7) serves.
	flaky := func(w http.ResponseWriter, r *http.Request) {
		n := s1Calls.Add(1)
		if n == 1 {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if n <= 6 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("seg-1"))
	}
	o := newGatedHLSOrigin(t, http.HandlerFunc(flaky))

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-retry-spill"
	destDir := t.TempDir()
	dest := filepath.Join(destDir, "v.ts")
	rt := &types.RuntimeConfig{MaxConnectionsPerDownload: 4}
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: o.srv.URL + "/media.m3u8", OutputPath: destDir, Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: rt,
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}
	waitPendingSettle(t, o)
	close(gate)

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
				t.Fatalf("transient failure must not terminate: %v", ev.Err)
			case types.EventPaused:
				t.Fatal("unexpected pause")
			}
		case <-deadline:
			t.Fatalf("retry never completed (starts=%d queued=%d)", starts, queued)
		}
	}
	if starts != 2 || queued != 1 {
		t.Fatalf("starts=%d queued=%d, want 2+1", starts, queued)
	}
	if s1Calls.Load() != 7 {
		t.Fatalf("s1 fetches = %d, want 7 (6 budget + retry success)", s1Calls.Load())
	}
	got, err := os.ReadFile(dest + types.IncompleteSuffix)
	if err != nil {
		t.Fatalf("final file: %v", err)
	}
	want := "seg-0seg-1seg-2seg-3"
	if string(got) != want {
		t.Fatalf("final content = %q, want %q", got, want)
	}
	// The retry reused the captured spill bytes — s2/s3 must each have been
	// fetched exactly once.
	if o.hits("/s2.ts") != 1 || o.hits("/s3.ts") != 1 {
		t.Fatalf("spilled segments refetched: s2=%d s3=%d", o.hits("/s2.ts"), o.hits("/s3.ts"))
	}
}
