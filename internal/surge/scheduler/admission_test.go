package scheduler

import (
	"errors"
	"testing"

	"goaria-v3/internal/surge/types"
)

// A refused AddWithPreparation must not run prepare or disturb queue state —
// for every refusal reason.
func TestAddWithPreparation_RejectionsHaveNoSideEffects(t *testing.T) {
	for _, state := range []string{"queued", "active", "shutdown"} {
		t.Run(state, func(t *testing.T) {
			cfg := types.DownloadRecord{ID: "same-id"}
			var pool *Scheduler
			switch state {
			case "queued":
				pool = NewSchedulerForTesting(nil)
				if !pool.Add(cfg) {
					t.Fatal("initial add rejected")
				}
			case "active":
				pool = NewSchedulerForTesting(map[string]types.DownloadRecord{cfg.ID: cfg})
			case "shutdown":
				pool = NewSchedulerForTesting(nil)
				pool.isShuttingDown = true
			}
			t.Cleanup(pool.GracefulShutdown)
			called := false
			accepted, err := pool.AddWithPreparation(cfg, func() error { called = true; return nil })
			if accepted || err != nil || called {
				t.Fatalf("accepted=%v err=%v preparation called=%v", accepted, err, called)
			}
		})
	}
}

// A failing prepare is a clean refusal: nothing queued, no admission token
// minted, and the ID stays available for a later attempt.
func TestAddWithPreparation_FailureDoesNotQueue(t *testing.T) {
	pool := NewSchedulerForTesting(nil)
	t.Cleanup(pool.GracefulShutdown)
	want := errors.New("persistence failed")
	cfg := types.DownloadRecord{ID: "new-id"}
	tokenBefore := pool.lastToken
	accepted, err := pool.AddWithPreparation(cfg, func() error { return want })
	if accepted || !errors.Is(err, want) {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
	if len(pool.queued) != 0 || len(pool.queueOrder) != 0 {
		t.Fatal("failed preparation left queued task")
	}
	if pool.lastToken != tokenBefore {
		t.Fatal("failed preparation consumed an admission token")
	}
	if !pool.Add(cfg) {
		t.Fatal("failed preparation retained ownership")
	}
}

// The queued entry is invisible to workers until prepare completes, so
// persistence still precedes any EventStarted.
func TestAddWithPreparation_PrecedesRunnableTask(t *testing.T) {
	pool := NewSchedulerForTesting(nil)
	t.Cleanup(pool.GracefulShutdown)
	cfg := types.DownloadRecord{ID: "new-id"}
	called := false
	accepted, err := pool.AddWithPreparation(cfg, func() error {
		called = true
		if len(pool.queued) != 0 || len(pool.queueOrder) != 0 {
			// t.Fatal inside the lock would skip Unlock via Goexit and
			// deadlock GracefulShutdown; report and refuse instead.
			t.Error("task runnable before preparation")
			return errors.New("task runnable before preparation")
		}
		return nil
	})
	if !accepted || err != nil || !called || pool.queued[cfg.ID] == nil {
		t.Fatalf("accepted=%v err=%v preparation called=%v", accepted, err, called)
	}
}
