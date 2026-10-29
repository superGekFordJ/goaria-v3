package orchestrator

import (
	"testing"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/types"
)

// Batch progress sub-events must report phase "verifying" with forced-zero
// speed while the artifact hash runs, and "downloading" with a positive EMA
// speed before it. The positive-speed warm-up proves the zero is the gate,
// not an idle sample.
func TestProgressBatchEvent_VerifyingPhaseZeroesSpeed(t *testing.T) {
	const id = "verify-phase-event"
	const total = int64(1 << 20)
	prog := progress.New(id, total)
	cfg := types.DownloadRecord{
		ID:            id,
		URL:           "https://example.com/blob.bin",
		Filename:      "blob.bin",
		DestPath:      "blob.bin",
		TotalSize:     total,
		ProgressState: prog,
	}
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{id: cfg})
	defer pool.GracefulShutdown()
	bus := NewEventBus()
	defer bus.Shutdown()
	events, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	agg := NewProgressAggregator(pool, bus, config.DefaultSettings())
	defer agg.Shutdown()

	advance := func(n int64) {
		prog.Bytes.VerifiedProgress.Add(n)
		prog.Bytes.Downloaded.Add(n)
	}
	subEvent := func(msg types.DownloadEvent) (types.DownloadEvent, bool) {
		if msg.Type != types.EventBatchProgress {
			return types.DownloadEvent{}, false
		}
		for _, ev := range msg.BatchEvents {
			if ev.Type == types.EventProgress && ev.DownloadID == id {
				return ev, true
			}
		}
		return types.DownloadEvent{}, false
	}

	deadline := time.Now().Add(10 * time.Second)
	sawPositiveSpeed := false
	for !sawPositiveSpeed {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("timed out waiting for a downloading-phase sub-event with positive speed")
		}
		select {
		case msg := <-events:
			if ev, ok := subEvent(msg); ok && ev.Phase == "downloading" && ev.Speed > 0 {
				sawPositiveSpeed = true
			}
			// Keep byte deltas positive between ticks so a later zero speed
			// can only come from the verifying gate.
			advance(256)
		case <-time.After(remaining):
			t.Fatal("timed out waiting for a downloading-phase sub-event with positive speed")
		}
	}

	prog.Verifying.Store(true)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("timed out waiting for a verifying-phase sub-event")
		}
		select {
		case msg := <-events:
			ev, ok := subEvent(msg)
			if !ok {
				advance(256)
				continue
			}
			if ev.Phase == "verifying" {
				if ev.Speed != 0 {
					t.Fatalf("verifying sub-event must report zero speed, got %v", ev.Speed)
				}
				return
			}
			if ev.Phase != "downloading" {
				t.Fatalf("unexpected phase %q before verifying flag propagated", ev.Phase)
			}
			advance(256)
		case <-time.After(remaining):
			t.Fatal("timed out waiting for a verifying-phase sub-event")
		}
	}
}
