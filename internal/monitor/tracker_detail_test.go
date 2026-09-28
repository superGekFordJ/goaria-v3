package monitor

import (
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

// rewindActiveClock simulates a tick gap without sleeping.
func rewindActiveClock(tr *TaskTracker, gid string, d time.Duration) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tt := tr.tasks[gid]; tt != nil && !tt.lastActiveAt.IsZero() {
		tt.lastActiveAt = tt.lastActiveAt.Add(-d)
	}
}

func trackedSnapshot(t *testing.T, tr *TaskTracker, gid string) TrackedTask {
	t.Helper()
	got, ok := tr.GetTrackedTask(gid)
	if !ok {
		t.Fatalf("GetTrackedTask(%s) missing", gid)
	}
	return got
}

func TestTrackerMarkAdded_CreatesPlaceholder(t *testing.T) {
	tr := NewTaskTracker()
	before := time.Now()
	tr.MarkAdded("ar_new")

	got := trackedSnapshot(t, tr, "ar_new")
	if got.AddedAt.Before(before) || got.AddedAt.IsZero() {
		t.Fatalf("AddedAt = %v, want >= %v", got.AddedAt, before)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("placeholder CreatedAt must be set for grace period")
	}
	if got.lastActiveAt.IsZero() {
		t.Fatal("MarkAdded must start the active clock")
	}
}

func TestTrackerMarkAdded_ResetsExistingEntry(t *testing.T) {
	tr := NewTaskTracker()
	tr.SetThreadInfo("ar_x", 4, false)
	tr.mu.Lock()
	tt := tr.tasks["ar_x"]
	tt.activeElapsed = time.Minute
	tt.maxActiveGap = time.Second
	tt.terminalElapsed = time.Hour
	tt.peakFromAvgFallback = true
	tr.mu.Unlock()

	tr.MarkAdded("ar_x")
	got := trackedSnapshot(t, tr, "ar_x")
	if got.ThreadCount != 4 {
		t.Fatalf("ThreadCount = %d, want 4 (existing fields kept)", got.ThreadCount)
	}
	if got.AddedAt.IsZero() || got.activeElapsed != 0 || got.maxActiveGap != 0 ||
		got.terminalElapsed != 0 || got.peakFromAvgFallback {
		t.Fatalf("MarkAdded did not reset timing: %+v", got)
	}
}

func TestTrackerActiveElapsed_AccumulatesAcrossTicks(t *testing.T) {
	tr := NewTaskTracker()
	gid := "ar_acc"
	tr.MarkAdded(gid)
	active := createMockTask(gid, "active")

	for range 3 {
		rewindActiveClock(tr, gid, 2*time.Second)
		tr.Update([]rpc.Task{active}, nil, nil)
	}
	got := trackedSnapshot(t, tr, gid)
	if got.activeElapsed < 6*time.Second || got.activeElapsed > 7*time.Second {
		t.Fatalf("activeElapsed = %v, want ~6s", got.activeElapsed)
	}
	if got.maxActiveGap < 2*time.Second || got.maxActiveGap > 3*time.Second {
		t.Fatalf("maxActiveGap = %v, want ~2s", got.maxActiveGap)
	}
}

func TestTrackerActiveElapsed_WaitingPausesClock(t *testing.T) {
	tr := NewTaskTracker()
	gid := "ar_pause"
	tr.MarkAdded(gid)
	active := createMockTask(gid, "active")
	paused := createMockTask(gid, "paused")

	rewindActiveClock(tr, gid, 2*time.Second)
	tr.Update([]rpc.Task{active}, nil, nil)
	tr.Update(nil, []rpc.Task{paused}, nil)
	if got := trackedSnapshot(t, tr, gid); !got.lastActiveAt.IsZero() {
		t.Fatal("waiting tick must clear lastActiveAt")
	}
	// Resuming must not count the paused interval.
	tr.Update([]rpc.Task{active}, nil, nil)
	rewindActiveClock(tr, gid, time.Second)
	tr.Update([]rpc.Task{active}, nil, nil)

	got := trackedSnapshot(t, tr, gid)
	if got.activeElapsed < 3*time.Second || got.activeElapsed > 4*time.Second {
		t.Fatalf("activeElapsed = %v, want ~3s (paused gap excluded)", got.activeElapsed)
	}
}

func TestTrackerActiveElapsed_GapAboveCapIgnored(t *testing.T) {
	tr := NewTaskTracker()
	gid := "ar_gap"
	tr.MarkAdded(gid)
	active := createMockTask(gid, "active")

	rewindActiveClock(tr, gid, activeSampleGapCap+time.Second)
	tr.Update([]rpc.Task{active}, nil, nil)
	got := trackedSnapshot(t, tr, gid)
	if got.activeElapsed != 0 || got.maxActiveGap != 0 {
		t.Fatalf("gap > cap counted: elapsed=%v maxGap=%v", got.activeElapsed, got.maxActiveGap)
	}
	if got.lastActiveAt.IsZero() {
		t.Fatal("lastActiveAt must be refreshed even when the gap is dropped")
	}
}

func TestTrackerGetTrackedTask_ReturnsCopy(t *testing.T) {
	tr := NewTaskTracker()
	tr.SetTaskGroup("ar_copy", rpc.DownloadGroup{ID: "g1", Name: "orig"})
	got := trackedSnapshot(t, tr, "ar_copy")
	got.Status = "mutated"
	got.DownloadGroup.Name = "mutated"

	again := trackedSnapshot(t, tr, "ar_copy")
	if again.Status == "mutated" || again.DownloadGroup.Name != "orig" {
		t.Fatalf("GetTrackedTask leaked a reference: %+v", again)
	}
	if _, ok := tr.GetTrackedTask("missing"); ok {
		t.Fatal("missing gid must report ok=false")
	}
	var nilTracker *TaskTracker
	if _, ok := nilTracker.GetTrackedTask("x"); ok {
		t.Fatal("nil tracker must report ok=false")
	}
}

func TestTrackerGetTrackedTask_ConcurrentWithUpdate(t *testing.T) {
	tr := NewTaskTracker()
	gid := "ar_race"
	tr.MarkAdded(gid)
	active := createMockTask(gid, "active")

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				tr.Update([]rpc.Task{active}, nil, nil)
			}
		})
		wg.Go(func() {
			for range 200 {
				_, _ = tr.GetTrackedTask(gid)
				tr.MarkAdded(gid)
			}
		})
	}
	wg.Wait()
}
