package monitor

import (
	"fmt"
	"io"
	"log"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
)

// TestRemoveGroupCompletedTaskReappears_NoResurrection reproduces the
// regression where deleting a download group containing a completed aria2c
// task causes the completed task to reappear in the stopped list.
//
// Root cause: during the deletion race (a tick fires between Cache.RemoveTask
// and InvalidateTask), TellStopped returns the completed task from the engine.
// filterDeletedTasks does not catch it (deletedGids not set yet).
// UpdateFromAria2 puts it back into Cache.GetStopped(). On the next tick,
// filterDeletedTasks catches it, but the shouldFetchStoppedUntil fast-retry
// preserve logic re-appends it from Cache.GetStopped(), bypassing
// filterDeletedTasks. The task persists.
func TestRemoveGroupCompletedTaskReappears_NoResurrection(t *testing.T) {
	completedGID := "ar_completed-1"
	completedTask := rpc.Task{
		GID:             completedGID,
		Status:          "complete",
		TotalLength:     "1000",
		CompletedLength: "1000",
		DownloadSpeed:   "0",
	}

	engine := &mockStoppedEngine{stopped: []rpc.Task{completedTask}}
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:              hub,
		pusher:           pusher,
		tracker:          tracker,
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * time.Second,
		windowInterval:   1 * time.Second,
		deletedGids:      make(map[string]time.Time),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.arActive = nil
		Cache.arWaiting = nil
		Cache.arStopped = nil
		Cache.engine = nil
	}()

	// Wire the mock engine into Cache so PrefetchMetadataMulti doesn't panic.
	Cache.engine = engine

	// Phase 1: Simulate the complete event. MoveTaskToStopped puts the task
	// into Cache.GetStopped(). shouldFetchStoppedUntil is set (1.5s window).
	Cache.arActive = []rpc.Task{{GID: completedGID, Status: "active", TotalLength: "1000"}}
	Cache.MoveTaskToStopped(completedGID, "complete")

	m.mu.Lock()
	m.shouldFetchStopped = true
	m.shouldFetchStoppedUntil = time.Now().Add(1500 * time.Millisecond)
	m.mu.Unlock()

	// Phase 2: Run the complete event's force tick. TellStopped returns the
	// completed task. filterDeletedTasks does not catch it (not deleted yet).
	// UpdateFromAria2 keeps the task in Cache.GetStopped().
	m.tick()

	if !taskInCacheStopped(completedGID) {
		t.Fatal("Phase 2: expected completed task in cache stopped after complete-event tick")
	}

	// Phase 3: Simulate the deletion race. Cache.RemoveTask runs (from
	// cleanupRemovedTask), then a tick fires BEFORE InvalidateTask sets
	// deletedGids. The tick's TellStopped returns the task, filterDeletedTasks
	// doesn't catch it, UpdateFromAria2 puts it back.
	Cache.RemoveTask(completedGID)
	if taskInCacheStopped(completedGID) {
		t.Fatal("Phase 3a: expected task removed from cache after Cache.RemoveTask")
	}

	// Race tick: deletedGids NOT set yet (InvalidateTask hasn't run).
	m.mu.Lock()
	m.shouldFetchStopped = true
	m.mu.Unlock()
	m.tick()

	// After the race tick, the task is back in cache (UpdateFromAria2 re-added it).
	if !taskInCacheStopped(completedGID) {
		t.Fatal("Phase 3b: expected task re-added to cache after race tick (this is the race)")
	}

	// Phase 4: InvalidateTask finally runs, setting deletedGids.
	m.mu.Lock()
	m.deletedGids[completedGID] = time.Now()
	m.shouldFetchStopped = true
	m.mu.Unlock()

	// Phase 5: Run the next tick. filterDeletedTasks should catch the task.
	// BUT the fast-retry preserve logic re-appends it from Cache.GetStopped(),
	// bypassing filterDeletedTasks. The task persists — this is the bug.
	m.tick()

	if taskInCacheStopped(completedGID) {
		t.Fatal("Phase 5: BUG REPRODUCED — completed task reappeared in cache stopped " +
			"after deletion due to fast-retry preserve bypassing filterDeletedTasks")
	}
}

// TestRemoveGroupCompletedTaskReappears_ConcurrentInvalidateTask verifies that
// per-iteration locking in the fast-retry preserve loop allows InvalidateTask to
// set deletedGids between iterations, preventing resurrection of a concurrently
// deleted task. With hoisted (whole-loop) locking, InvalidateTask is blocked for
// the entire loop and the task is re-appended.
//
// The target task is placed LAST in Cache.GetStopped() behind many filler tasks
// so the preserve loop runs long enough for InvalidateTask to contend on m.mu
// mid-loop. A busy-wait delay (instead of time.Sleep) skips the enrich phase that
// precedes the preserve loop, then signals InvalidateTask to start. With
// per-task locking, InvalidateTask acquires m.mu between iterations and sets
// deletedGids before the target is iterated. With hoisted locking, it is blocked
// for the entire loop and the target is appended → resurrected.
func TestRemoveGroupCompletedTaskReappears_ConcurrentInvalidateTask(t *testing.T) {
	// Silence log output — tick's log.Printf calls are slow under test
	// output capture (~25ms per call), which makes timing-based concurrency
	// windows unreliable. With log discarded, the gap between wg.Wait and
	// the preserve loop is < 1ms.
	prevLogOut := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prevLogOut)

	targetGID := "ar_concurrent-target"
	targetTask := rpc.Task{
		GID:             targetGID,
		Status:          "complete",
		TotalLength:     "1000",
		CompletedLength: "1000",
		DownloadSpeed:   "0",
	}

	// Filler tasks make the preserve loop run long enough for InvalidateTask
	// to arrive during the loop. The target is placed last so InvalidateTask
	// has time to set deletedGids before the target's iteration.
	const numFillers = 50000
	allStopped := make([]rpc.Task, 0, numFillers+1)
	for i := range numFillers {
		allStopped = append(allStopped, rpc.Task{
			GID:             fmt.Sprintf("ar_filler-%d", i),
			Status:          "complete",
			TotalLength:     "100",
			CompletedLength: "100",
			DownloadSpeed:   "0",
		})
	}
	allStopped = append(allStopped, targetTask)

	engine := &blockingStoppedEngine{
		stopped: nil, // engine reports no stopped tasks
		called:  make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:              hub,
		pusher:           pusher,
		tracker:          tracker,
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * time.Second,
		windowInterval:   1 * time.Second,
		deletedGids:      make(map[string]time.Time),
		prevActiveGids:   map[string]bool{},
		prevWaitingGids:  map[string]bool{},
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.arActive = nil
		Cache.arWaiting = nil
		Cache.arStopped = nil
		Cache.engine = nil
	}()

	Cache.engine = engine

	// Pre-process fillers in the tracker so they are not returned as newly
	// completed (avoids 50k handleTaskComplete calls during the test tick).
	tracker.Update(nil, nil, allStopped[:numFillers])

	// Seed Cache.arStopped directly — simulates a prior tick's UpdateFromAria2
	// having re-added the target after the engine deleted it.
	Cache.arStopped = copyTaskSlice(allStopped)

	// Activate the fast-retry preserve window.
	m.mu.Lock()
	m.shouldFetchStopped = true
	m.shouldFetchStoppedUntil = time.Now().Add(1500 * time.Millisecond)
	m.mu.Unlock()

	// Start tick() on a goroutine. TellStoppedLite blocks on the release channel.
	tickDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				tickDone <- fmt.Errorf("tick panicked: %v", r)
			}
		}()
		m.tick()
		tickDone <- nil
	}()

	// Wait for TellStoppedLite to be called (tick is now in wg.Wait).
	<-engine.called

	// Prepare InvalidateTask on a separate goroutine, gated on a signal.
	// The signal is sent after a busy-wait that skips the enrich phase.
	invalidateStart := make(chan struct{})
	invalidateDone := make(chan struct{})
	go func() {
		<-invalidateStart
		m.InvalidateTask(targetGID)
		close(invalidateDone)
	}()

	// Release TellStoppedLite — tick proceeds into filterDeletedTasks, enrich,
	// then the preserve loop. With log output discarded, the enrich phase is
	// < 1ms, so a short busy-wait reliably skips it.
	close(engine.release)

	// Busy-wait ~500µs to let tick pass the enrich phase and enter the
	// preserve loop. A busy-wait (not time.Sleep) gives sub-millisecond
	// precision on Windows.
	busyStart := time.Now()
	for time.Since(busyStart) < 500*time.Microsecond {
	}

	// Signal InvalidateTask. With per-task locking, it acquires m.mu between
	// preserve iterations and sets deletedGids before the target (last entry)
	// is iterated. With hoisted locking, it is blocked for the entire loop
	// and the target is appended → resurrected.
	close(invalidateStart)

	// Wait for tick and InvalidateTask to finish.
	if err := <-tickDone; err != nil {
		t.Fatalf("tick failed: %v", err)
	}
	<-invalidateDone

	if taskInCacheStopped(targetGID) {
		t.Fatal("BUG: target task resurrected in cache stopped — " +
			"concurrent InvalidateTask was blocked during the preserve loop " +
			"(hoisted locking prevents deletedGids from being set mid-loop)")
	}
}
