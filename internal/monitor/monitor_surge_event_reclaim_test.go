package monitor

import (
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	surgeEvents "goaria-v3/internal/surge/types"
)

// mockStoppedEngine returns a fixed set of stopped tasks from TellStopped/
// TellStoppedLite, allowing tick-level tests to simulate a Surge engine whose
// DB still contains a completed task (the async DeleteState race window).

func TestScheduleIdleMemoryReclaim_DebouncedOnce(t *testing.T) {
	origDelay := idleReclaimDelay
	origAction := idleReclaimAction
	t.Cleanup(func() {
		idleReclaimDelay = origDelay
		idleReclaimAction = origAction
	})

	idleReclaimDelay = 50 * time.Millisecond
	var callCount atomic.Int32
	idleReclaimAction = func() {
		callCount.Add(1)
	}

	pool := scheduler.NewSchedulerForTesting(nil)
	se := rpc.NewSurgeEngineForTesting(pool)
	m := &Monitor{surgeEng: se}

	for range 5 {
		m.ScheduleIdleMemoryReclaim()
	}

	time.Sleep(120 * time.Millisecond)
	if count := callCount.Load(); count != 1 {
		t.Fatalf("expected action called exactly 1 time, got %d", count)
	}
}

func TestScheduleIdleMemoryReclaim_SkipsWhenActive(t *testing.T) {
	origDelay := idleReclaimDelay
	origAction := idleReclaimAction
	t.Cleanup(func() {
		idleReclaimDelay = origDelay
		idleReclaimAction = origAction
	})

	idleReclaimDelay = 30 * time.Millisecond
	var callCount atomic.Int32
	idleReclaimAction = func() {
		callCount.Add(1)
	}

	pool := scheduler.NewSchedulerForTesting(map[string]surgeEvents.DownloadRecord{
		"active-task": {
			ID:            "active-task",
			ProgressState: progress.New("active-task", 1000),
		},
	})
	se := rpc.NewSurgeEngineForTesting(pool)
	m := &Monitor{surgeEng: se}

	m.ScheduleIdleMemoryReclaim()

	time.Sleep(100 * time.Millisecond)
	if count := callCount.Load(); count != 0 {
		t.Fatalf("expected action to be skipped when active, got %d calls", count)
	}
}

func TestScheduleIdleMemoryReclaim_CancelledOnStop(t *testing.T) {
	origDelay := idleReclaimDelay
	origAction := idleReclaimAction
	t.Cleanup(func() {
		idleReclaimDelay = origDelay
		idleReclaimAction = origAction
	})

	idleReclaimDelay = 50 * time.Millisecond
	var callCount atomic.Int32
	idleReclaimAction = func() {
		callCount.Add(1)
	}

	pool := scheduler.NewSchedulerForTesting(nil)
	se := rpc.NewSurgeEngineForTesting(pool)
	m := &Monitor{
		surgeEng: se,
		stopChan: make(chan struct{}),
	}

	m.ScheduleIdleMemoryReclaim()
	m.Stop()

	// Calling ScheduleIdleMemoryReclaim after Stop should be rejected
	m.ScheduleIdleMemoryReclaim()

	time.Sleep(100 * time.Millisecond)
	if count := callCount.Load(); count != 0 {
		t.Fatalf("expected action not called after Stop, got %d", count)
	}
}

func TestHandleSurgeEvent_TriggersIdleMemoryReclaim(t *testing.T) {
	origDelay := idleReclaimDelay
	origAction := idleReclaimAction
	t.Cleanup(func() {
		idleReclaimDelay = origDelay
		idleReclaimAction = origAction
	})

	idleReclaimDelay = 30 * time.Millisecond
	var callCount atomic.Int32
	idleReclaimAction = func() {
		callCount.Add(1)
	}

	pool := scheduler.NewSchedulerForTesting(nil)
	se := rpc.NewSurgeEngineForTesting(pool)
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:      hub,
		surgeEng: se,
		stopChan: make(chan struct{}),
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "comp-reclaim-1",
	})

	time.Sleep(100 * time.Millisecond)
	if count := callCount.Load(); count != 1 {
		t.Fatalf("expected 1 call after complete event, got %d", count)
	}
}
