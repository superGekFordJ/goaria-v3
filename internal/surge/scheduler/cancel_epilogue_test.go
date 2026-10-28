package scheduler

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// A Cancel that reaps the claim while the worker is parked between
// RunDownload's return and the failure epilogue must leave no resurrection
// vector: the late failure settles silently — no requeue, no
// EventQueued/EventError, no SetError — while the physical claim's
// bookkeeping still completes exactly once.
func TestScheduler_CancelBeforeEpilogueDoesNotRevive(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		// 503 without Retry-After is non-permanent: without the guard the
		// stale qt would requeue (zombie queued entry + EventQueued).
		{"retryable", http.StatusServiceUnavailable},
		// 403 is a permanent HTTP sentinel: without the guard the terminal
		// path would emit EventError and record it on the progress state.
		{"terminal", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			ch := make(chan types.DownloadEvent, 32)
			pool := newManagedPool(t, ch, 1)

			// Pin the worker between close(ad.done) and the epilogue. The gate
			// self-clears on entry, announces itself, then parks on channels
			// only — no sleeps, no test calls from inside the worker.
			entered := make(chan struct{})
			releaseCh := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
			// Unpark before the pool's shutdown cleanup runs (LIFO order).
			t.Cleanup(release)
			oldGate := pool.workerDoneGate.Load()
			t.Cleanup(func() { pool.workerDoneGate.Store(oldGate) })
			gateFn := func() {
				pool.workerDoneGate.Store(nil)
				close(entered)
				<-releaseCh
			}
			pool.workerDoneGate.Store(&gateFn)

			// Settlement barrier: the admission wake fires after the physical
			// claim's busy slot is released — the observable end of the
			// epilogue, not a guessed delay.
			settled := make(chan struct{}, 4)
			pool.SetAdmissionWake(func() {
				select {
				case settled <- struct{}{}:
				default:
				}
			})
			t.Cleanup(func() { pool.SetAdmissionWake(nil) })

			const id = "epilogue-detached"
			tmpDir := t.TempDir()
			filename := "epilogue.bin"
			if err := os.WriteFile(filepath.Join(tmpDir, filename)+types.IncompleteSuffix, nil, 0o644); err != nil {
				t.Fatalf("pre-create incomplete file: %v", err)
			}
			progState := progress.New(id, 0)
			if !pool.Add(types.DownloadRecord{
				ID:            id,
				URL:           server.URL,
				OutputPath:    tmpDir,
				Filename:      filename,
				ProgressState: progState,
				Runtime:       &types.RuntimeConfig{Workers: 1},
			}) {
				t.Fatal("Add was refused")
			}

			// Capture the queued identity before Admit: a zombie requeue would
			// reinsert this same qt object with bumped retries and a new token.
			pool.mu.RLock()
			qt := pool.queued[id]
			limiter := pool.downloadLimiters[id]
			pool.mu.RUnlock()
			if qt == nil {
				t.Fatal("queued entry missing after Add")
			}
			if limiter == nil {
				t.Fatal("download limiter missing after Add")
			}
			savedRetries, savedToken := qt.retries, qt.token

			key := admissionKeyFor(t, pool, id)
			if key.Token != savedToken {
				t.Fatalf("candidate token %d != queued token %d", key.Token, savedToken)
			}
			if res := pool.Admit(key, 1, 0); res != AdmitAccepted {
				t.Fatalf("Admit = %v, want AdmitAccepted", res)
			}

			// The worker ran the request to failure and parked inside the
			// gate: its ad is registered, already not-running, and done is
			// closed — but copy-back/epilogue cannot have run yet.
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("worker never reached the post-download gate")
			}
			pool.mu.RLock()
			ad := pool.downloads[id]
			pool.mu.RUnlock()
			if ad == nil {
				t.Fatal("ad unregistered before the failure epilogue")
			}
			if ad.running.Load() {
				t.Fatal("ad still marked running inside the gate")
			}
			select {
			case <-ad.done:
			default:
				t.Fatal("ad.done still open inside the gate")
			}

			// Cancel must fully return while the gate is still parked — the
			// only thing it could wait on (running/done) already settled.
			cancelRes := make(chan types.CancelResult, 1)
			go func() { cancelRes <- pool.Cancel(id) }()
			select {
			case res := <-cancelRes:
				if !res.Found || res.WasQueued {
					t.Fatalf("Cancel = %+v, want Found && !WasQueued", res)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Cancel did not return while the gate was parked")
			}

			// Still parked: the reap is complete and no replacement exists.
			pool.mu.RLock()
			reaped := pool.downloads[id] == nil &&
				pool.queued[id] == nil &&
				pool.downloadLimiters[id] == nil
			shutting := pool.isShuttingDown
			pool.mu.RUnlock()
			if !reaped {
				t.Fatal("Cancel left claim state behind before the epilogue")
			}
			if shutting {
				t.Fatal("pool unexpectedly shutting down")
			}
			select {
			case <-pool.progressDone:
				t.Fatal("progressDone closed before shutdown")
			default:
			}
			if progState.IsPaused() {
				t.Fatal("progress unexpectedly paused")
			}

			release()

			// The physical claim settles exactly once: the busy slot release
			// is what fires the admission wake.
			select {
			case <-settled:
			case <-time.After(10 * time.Second):
				t.Fatal("claim never settled — no admission wake")
			}

			// Before any Pause/Shutdown/second Cancel: no trace of the ID may
			// exist anywhere, and the stale qt was never requeued.
			pool.mu.RLock()
			residue := pool.downloads[id] != nil ||
				pool.queued[id] != nil ||
				pool.downloadLimiters[id] != nil
			inOrder := false
			for _, qid := range pool.queueOrder {
				if qid == id {
					inOrder = true
				}
			}
			busyN := len(pool.busy)
			gotRetries, gotToken := qt.retries, qt.token
			pool.mu.RUnlock()
			if residue || inOrder {
				t.Fatal("detached claim was requeued or re-registered")
			}
			if busyN != 0 {
				t.Fatalf("busy still holds %d claim(s)", busyN)
			}
			if gotRetries != savedRetries || gotToken != savedToken {
				t.Fatalf("stale qt was requeued: retries %d→%d, token %d→%d",
					savedRetries, gotRetries, savedToken, gotToken)
			}
			if snap := pool.AdmissionSnapshot(); len(snap.Candidates) != 0 || len(snap.Busy) != 0 || snap.CommittedSlots != 0 {
				t.Fatalf("snapshot not empty after settle: %+v", snap)
			}

			// Add's single wait-group count must be settled by the worker's
			// shared tail — before shutdown sweeps the queue, so a missing
			// Done cannot hide behind GracefulShutdown.
			wgDone := make(chan struct{})
			go func() { pool.wg.Wait(); close(wgDone) }()
			select {
			case <-wgDone:
			case <-time.After(10 * time.Second):
				t.Fatal("wg.Wait hung — claim wait-group count leaked")
			}

			// Event stream: EventStarted is expected; the settled-away claim
			// must not emit queued/error/complete events.
			sawStarted := false
		drain:
			for {
				select {
				case ev := <-ch:
					if ev.DownloadID != id {
						continue
					}
					switch ev.Type {
					case types.EventStarted:
						sawStarted = true
					case types.EventQueued, types.EventError, types.EventComplete:
						t.Fatalf("detached claim emitted %+v", ev)
					}
				default:
					break drain
				}
			}
			if !sawStarted {
				t.Fatal("missing EventStarted for the physical claim")
			}
			if err := progState.GetError(); err != nil {
				t.Fatalf("GetError = %v, want nil — SetError must not run for a detached claim", err)
			}

			awaitShutdown(t, pool)
		})
	}
}
