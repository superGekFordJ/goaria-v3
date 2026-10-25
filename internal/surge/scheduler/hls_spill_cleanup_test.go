package scheduler

import (
	"context"
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

// TestScheduler_HLSRetryRefreshesZeroByteSeed pins the auto-retry resume
// mark: a first run that dies before any byte commits leaves a zero-byte
// prepared table — the requeue must flag IsResume so bootstrap re-resolves
// signatures rather than trusting the dead URIs of run one.
func TestScheduler_HLSRetryRefreshesZeroByteSeed(t *testing.T) {
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
	var s0Calls, manifestCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/media.m3u8":
			manifestCalls.Add(1)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n" +
				"#EXTINF:4.0,\ns0.ts\n#EXTINF:4.0,\ns1.ts\n#EXT-X-ENDLIST\n"))
		case "/s0.ts":
			if s0Calls.Add(1) <= 6 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte("seg-0"))
		case "/s1.ts":
			_, _ = w.Write([]byte("seg-1"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-retry-zero"
	destDir := t.TempDir()
	rt := &types.RuntimeConfig{MaxConnectionsPerDownload: 1}
	if !pool.Add(types.DownloadRecord{
		ID: id, URL: srv.URL + "/media.m3u8", OutputPath: destDir, Filename: "v.ts",
		ProgressState: progress.New(id, 0), Runtime: rt,
		Strategy: types.StrategyHLS,
	}) {
		t.Fatal("Add refused")
	}
	starts, completes := 0, 0
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
			case types.EventComplete:
				completes++
			case types.EventError:
				t.Fatalf("transient failure must not terminate: %v", ev.Err)
			case types.EventPaused:
				t.Fatal("unexpected pause")
			}
		case <-deadline:
			t.Fatalf("retry never completed (starts=%d)", starts)
		}
	}
	if starts != 2 {
		t.Fatalf("starts = %d, want first run + one retry", starts)
	}
	// Run one fetched the manifest once; the retried run's resume gate
	// fetched it again — without the IsResume mark the prepared table
	// would have skipped the second GET entirely.
	if n := manifestCalls.Load(); n != 2 {
		t.Fatalf("manifest fetches = %d, want 2 (initial + resume refresh)", n)
	}
	if n := s0Calls.Load(); n != 7 {
		t.Fatalf("s0 fetches = %d, want 6 budget + 1 retry success", n)
	}
	// RunDownload leaves the payload in the .surge; promotion to the final
	// name is the service layer's job on EventComplete.
	got, err := os.ReadFile(filepath.Join(destDir, "v.ts"+types.IncompleteSuffix))
	if err != nil {
		t.Fatalf("final file: %v", err)
	}
	if string(got) != "seg-0seg-1" {
		t.Fatalf("final content = %q", got)
	}
}

// TestScheduler_HLSCancelWaitsForFileWriters pins the removal barrier: an
// HLS Cancel must wait for every collected claim's done — past the legacy
// non-HLS timeout — so a pause-time Flush/close can never land after the
// .surge/.spill delete. A concurrent second Cancel joins the same wait
// through the busy-claim path.
func TestScheduler_HLSCancelWaitsForFileWriters(t *testing.T) {
	ch := make(chan types.DownloadEvent, 8)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-cancel-barrier"
	busyKey := AdmissionKey{ID: id, Token: 1}
	// GracefulShutdown drains p.busy before returning: release the fake
	// claim the way a worker's end-of-claim bookkeeping would. Registered
	// after GracefulShutdown so LIFO runs it first even on a Fatal path.
	t.Cleanup(func() {
		pool.mu.Lock()
		wake := pool.releaseBusyLocked(busyKey)
		pool.mu.Unlock()
		if wake != nil {
			wake()
		}
	})
	ad := &activeDownload{
		config: types.DownloadRecord{
			ID:         id,
			Strategy:   types.StrategyHLS,
			Filename:   "v.ts",
			OutputPath: t.TempDir(),
		},
		done: make(chan struct{}),
	}
	ad.running.Store(true)
	_, ad.cancel = context.WithCancel(context.Background())

	pool.mu.Lock()
	pool.downloads[id] = ad
	pool.busy[busyKey] = &busySlot{ad: ad, strategy: types.StrategyHLS}
	pool.mu.Unlock()

	// A tiny legacy timeout: the HLS barrier must ignore it entirely.
	oldTimeout := cancelStopWaitTimeout
	cancelStopWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { cancelStopWaitTimeout = oldTimeout })

	first := make(chan types.CancelResult, 1)
	go func() { first <- pool.Cancel(id) }()
	// Pin the ordering: wait until the first Cancel has claimed the removal
	// (hlsRemoved flips under p.mu after the downloads slot is deleted) so
	// the second Cancel provably joins through the busy scan only.
	deadline := time.Now().Add(2 * time.Second)
	for !ad.hlsRemoved.Load() {
		if time.Now().After(deadline) {
			t.Fatal("first Cancel never marked the claim")
		}
		time.Sleep(time.Millisecond)
	}
	second := make(chan types.CancelResult, 1)
	go func() { second <- pool.Cancel(id) }()

	select {
	case <-first:
		t.Fatal("first Cancel returned while the claim's writer was still live")
	case <-second:
		t.Fatal("second Cancel returned while the claim's writer was still live")
	case <-time.After(4 * cancelStopWaitTimeout):
		// Far past the legacy bound — the claim's done is still open and
		// neither Cancel may overtake the file teardown.
	}
	close(ad.done)

	res1 := <-first
	if !res1.Found {
		t.Fatal("first Cancel reported not found")
	}
	select {
	case res2 := <-second:
		// Neither slot maps the ID anymore — the busy claim is found
		// through the scan alone, so Found is false yet the wait held.
		if res2.Found {
			t.Fatal("second Cancel claimed a downloads/queued hit that was already gone")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Cancel never joined the done wait")
	}
}

// TestScheduler_HLSCancelQueuedSkipsDoneWait proves a queued HLS task —
// which never minted a claim — removes without touching any done channel.
func TestScheduler_HLSCancelQueuedSkipsDoneWait(t *testing.T) {
	ch := make(chan types.DownloadEvent, 8)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "hls-cancel-queued"
	// Mirror Add's accounting: every queued entry holds one wg count that
	// Cancel's droppedQueued path releases. The entry stays out of
	// queueOrder so a worker's waitForTask scan can never claim it — the
	// test needs a settled queued claim, not a race with dispatch.
	pool.wg.Add(1)
	pool.mu.Lock()
	pool.queued[id] = &queuedTask{cfg: types.DownloadRecord{
		ID:         id,
		Strategy:   types.StrategyHLS,
		Filename:   "v.ts",
		OutputPath: t.TempDir(),
	}}
	pool.mu.Unlock()

	done := make(chan types.CancelResult, 1)
	go func() { done <- pool.Cancel(id) }()
	select {
	case res := <-done:
		if !res.Found || !res.WasQueued {
			t.Fatalf("Cancel = %+v, want found queued removal", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued HLS Cancel blocked on a done that never existed")
	}
}
