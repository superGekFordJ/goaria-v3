package concurrent

import (
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
)

// TestCheckWorkerHealth_PublishesTelemetry verifies that checkWorkerHealth
// publishes per-worker telemetry snapshots to ProgressState.
func TestCheckWorkerHealth_PublishesTelemetry(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := types.NewProgressState("test-download", 1024*1024)
	d.State = state

	now := time.Now()
	active := &ActiveTask{
		Task:        types.Task{Offset: 4096, Length: 1024},
		StartTime:   now,
		WindowStart: now,
	}
	active.CurrentOffset.Store(4608)
	active.StopAt.Store(5120)
	active.LastActivity.Store(now.UnixNano())
	active.RetryCount.Store(2)
	active.Hedged.Store(1)
	active.SpeedMu.Lock()
	active.Speed = 500000.0
	active.SpeedMu.Unlock()

	d.activeTasks[0] = active

	d.checkWorkerHealth()

	stats := state.GetWorkerStats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 worker snapshot, got %d", len(stats))
	}

	s := stats[0]
	if s.WorkerID != 0 {
		t.Errorf("WorkerID = %d, want 0", s.WorkerID)
	}
	if s.EMASpeed != 500000.0 {
		t.Errorf("EMASpeed = %f, want 500000", s.EMASpeed)
	}
	if s.RetryCount != 2 {
		t.Errorf("RetryCount = %d, want 2", s.RetryCount)
	}
	if s.ChunkStart != 4096 {
		t.Errorf("ChunkStart = %d, want 4096", s.ChunkStart)
	}
	if s.ChunkOffset != 4608 {
		t.Errorf("ChunkOffset = %d, want 4608", s.ChunkOffset)
	}
	if s.ChunkLength != 1024 {
		t.Errorf("ChunkLength = %d, want 1024 (StopAt-ChunkStart=5120-4096)", s.ChunkLength)
	}
	if !s.Hedged {
		t.Error("Hedged = false, want true")
	}
}

// TestCheckWorkerHealth_ClearsTelemetryOnEmpty verifies that telemetry is
// cleared when there are no active tasks.
func TestCheckWorkerHealth_ClearsTelemetryOnEmpty(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := types.NewProgressState("test-download", 1024)
	d.State = state

	// Pre-populate telemetry
	state.SetWorkerStats([]types.WorkerSnapshot{{WorkerID: 0, EMASpeed: 100}})

	d.checkWorkerHealth()

	stats := state.GetWorkerStats()
	if stats != nil {
		t.Fatalf("expected nil stats after empty check, got %d entries", len(stats))
	}
}

// TestCheckWorkerHealth_TelemetryRace exercises concurrent reads of
// GetWorkerStats while checkWorkerHealth writes telemetry.
func TestCheckWorkerHealth_TelemetryRace(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := types.NewProgressState("race-download", 1024*1024)
	d.State = state

	now := time.Now()
	active := &ActiveTask{
		Task:        types.Task{Offset: 0, Length: 1024},
		StartTime:   now,
		WindowStart: now,
	}
	active.CurrentOffset.Store(0)
	active.StopAt.Store(1024)
	active.LastActivity.Store(now.UnixNano())

	d.activeTasks[0] = active

	var wg sync.WaitGroup
	wg.Add(2)

	// Writer goroutine: repeatedly call checkWorkerHealth
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			d.checkWorkerHealth()
			time.Sleep(time.Microsecond)
		}
	}()

	// Reader goroutine: repeatedly read telemetry
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = state.GetWorkerStats()
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()
}

// TestCheckWorkerHealth_WorkStealing verifies that ChunkLength reflects
// the effective chunk size after work stealing reduces StopAt.
func TestCheckWorkerHealth_WorkStealing(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := types.NewProgressState("steal-test", 1024*1024)
	d.State = state

	now := time.Now()
	active := &ActiveTask{
		Task:        types.Task{Offset: 0, Length: 4096},
		StartTime:   now,
		WindowStart: now,
	}
	active.CurrentOffset.Store(1024)
	active.StopAt.Store(2048) // StealWork reduced StopAt from 4096 to 2048
	active.LastActivity.Store(now.UnixNano())

	d.activeTasks[0] = active
	d.checkWorkerHealth()

	stats := state.GetWorkerStats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 worker snapshot, got %d", len(stats))
	}

	s := stats[0]
	if s.ChunkLength != 2048 {
		t.Errorf("ChunkLength = %d, want 2048 (effective length after stealing)", s.ChunkLength)
	}
	if s.ChunkStart != 0 {
		t.Errorf("ChunkStart = %d, want 0", s.ChunkStart)
	}
}

// TestWorkerStats_RetryCount verifies that RetryCount is set correctly
// when creating ActiveTask with different attempt values, simulating the
// worker retry loop behavior (worker.go: activeTask.RetryCount.Store(int32(attempt))).
func TestWorkerStats_RetryCount(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := types.NewProgressState("retry-test", 1024*1024)
	d.State = state

	now := time.Now()

	// Simulate worker retry loop: attempt 0 (fresh), attempt 1 (first retry), attempt 2 (second retry)
	attempts := []int32{0, 1, 2}
	for i, attempt := range attempts {
		active := &ActiveTask{
			Task:        types.Task{Offset: int64(i * 1024), Length: 1024},
			StartTime:   now,
			WindowStart: now,
		}
		active.StopAt.Store(int64(i*1024 + 1024))
		active.LastActivity.Store(now.UnixNano())
		active.RetryCount.Store(attempt)
		d.activeTasks[i] = active
	}

	d.checkWorkerHealth()

	stats := state.GetWorkerStats()
	if len(stats) != 3 {
		t.Fatalf("expected 3 worker snapshots, got %d", len(stats))
	}

	for i, attempt := range attempts {
		found := false
		for _, s := range stats {
			if s.WorkerID == i {
				if s.RetryCount != attempt {
					t.Errorf("WorkerID %d: RetryCount = %d, want %d", i, s.RetryCount, attempt)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("WorkerID %d not found in snapshots", i)
		}
	}
}

// TestProgressState_SessionReset_ClearsTelemetry verifies that SessionReset
// clears the worker telemetry storage.
func TestProgressState_SessionReset_ClearsTelemetry(t *testing.T) {
	state := types.NewProgressState("test-download", 1024)

	state.SetWorkerStats([]types.WorkerSnapshot{
		{WorkerID: 0, EMASpeed: 1000},
		{WorkerID: 1, EMASpeed: 2000},
	})

	stats := state.GetWorkerStats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 stats before reset, got %d", len(stats))
	}

	state.SessionReset()

	stats = state.GetWorkerStats()
	if stats != nil {
		t.Fatalf("expected nil stats after SessionReset, got %d entries", len(stats))
	}
}
