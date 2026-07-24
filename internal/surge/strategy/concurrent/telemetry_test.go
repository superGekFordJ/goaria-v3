package concurrent

import (
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// TestCheckWorkerHealth_PublishesTelemetry focuses on fields not covered by
// health PublishesWorkerStats (EMASpeed / Hedged).
func TestCheckWorkerHealth_PublishesTelemetry(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := progress.New("test-download", 1024*1024)
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
	if s.EMASpeed != 500000.0 {
		t.Errorf("EMASpeed = %f, want 500000", s.EMASpeed)
	}
	if !s.Hedged {
		t.Error("Hedged = false, want true")
	}
}

func TestCheckWorkerHealth_ClearsTelemetryOnEmpty(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := progress.New("test-download", 1024)
	d.State = state

	state.SetWorkerStats([]types.WorkerSnapshot{{WorkerID: 0, EMASpeed: 100}})

	d.checkWorkerHealth()

	stats := state.GetWorkerStats()
	if stats != nil {
		t.Fatalf("expected nil stats after empty check, got %d entries", len(stats))
	}
}

func TestCheckWorkerHealth_TelemetryRace(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := progress.New("race-download", 1024*1024)
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

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			d.checkWorkerHealth()
			time.Sleep(time.Microsecond)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = state.GetWorkerStats()
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()
}

func TestCheckWorkerHealth_WorkStealing(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := progress.New("steal-test", 1024*1024)
	d.State = state

	now := time.Now()
	active := &ActiveTask{
		Task:        types.Task{Offset: 0, Length: 4096},
		StartTime:   now,
		WindowStart: now,
	}
	active.CurrentOffset.Store(1024)
	active.StopAt.Store(2048)
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

func TestWorkerStats_RetryCount(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := progress.New("retry-test", 1024*1024)
	d.State = state

	now := time.Now()

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

func TestProgressState_SessionReset_ClearsTelemetry(t *testing.T) {
	state := progress.New("test-download", 1024)

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
