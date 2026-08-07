package monitor

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

func TestTick_StaleStopped_FreshActive_NoTwinNoSecondaryComplete(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	const gid = "ar_stale_active"
	staleStopped := rpc.Task{
		GID:             gid,
		Status:          "complete",
		TotalLength:     "1000",
		CompletedLength: "1000",
		DownloadSpeed:   "0",
		Files:           []rpc.File{{Path: "/tmp/stale.bin"}},
	}
	liveActive := rpc.Task{
		GID:             gid,
		Status:          "active",
		TotalLength:     "1000",
		CompletedLength: "100",
		DownloadSpeed:   "50",
		Files:           []rpc.File{{Path: "/tmp/stale.bin"}},
	}

	engine := &mockTickEngine{}
	engine.setLists([]rpc.Task{liveActive}, nil, nil)
	m := newTickRecoveryMonitor(t, engine)
	m.aria2Recovered.Store(true)
	m.lastStoppedFetchTime = time.Now()
	m.shouldFetchStopped = false
	m.lastStopped = []rpc.Task{staleStopped}

	tracker := m.tracker
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/stale.bin", 0, "complete")
	if completed := tracker.MarkCompleteFromEvent(gid, "complete"); completed == nil {
		t.Fatal("expected initial terminal marker")
	}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	Cache.UpdateFromAria2(nil, nil, []rpc.Task{staleStopped})
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/stale.bin", Status: "complete"})

	m.tick()

	assertExactlyOneActiveNoStopped(t, gid)
	assertNoTwinInGetTaskLists(t, gid)

	if _, ok := history.Get(gid); ok {
		t.Fatal("expected history retired and not rewritten by stale stopped")
	}
	if tracker.processedComplete[gid] {
		t.Fatal("expected tracker reopen without secondary terminal from stale stopped")
	}

	m.mu.Lock()
	lastStoppedHas := false
	for _, task := range m.lastStopped {
		if task.GID == gid {
			lastStoppedHas = true
			break
		}
	}
	m.mu.Unlock()
	if lastStoppedHas {
		t.Fatal("expected !fetchStopped reuse to persist normalized lastStopped without live GID")
	}
}

func TestTick_StaleStopped_WaitingOnly_RetiresOnce(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	const gid = "ar_stale_waiting"
	staleStopped := rpc.Task{
		GID: gid, Status: "error", ErrorCode: "1",
		Files: []rpc.File{{Path: "/tmp/wait.bin"}},
	}
	liveWaiting := rpc.Task{
		GID: gid, Status: "waiting",
		Files: []rpc.File{{Path: "/tmp/wait.bin"}},
	}

	engine := &mockTickEngine{}
	engine.setLists(nil, []rpc.Task{liveWaiting}, nil)
	m := newTickRecoveryMonitor(t, engine)
	m.aria2Recovered.Store(true)
	m.lastStoppedFetchTime = time.Now()
	m.shouldFetchStopped = false
	m.lastStopped = []rpc.Task{staleStopped}

	tracker := m.tracker
	tracker.EnsureTrackedFromEvent(gid, 500, "https://example.com/wait.bin", 0, "error")
	if completed := tracker.MarkCompleteFromEvent(gid, "error"); completed == nil {
		t.Fatal("expected initial terminal marker")
	}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	Cache.UpdateFromAria2(nil, nil, []rpc.Task{staleStopped})
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/wait.bin", Status: "error"})

	m.tick()

	waitingCount := 0
	for _, task := range Cache.GetWaiting() {
		if task.GID == gid {
			waitingCount++
		}
	}
	if waitingCount != 1 {
		t.Fatalf("expected exactly one waiting %s, got %d", gid, waitingCount)
	}
	if taskInCacheStopped(gid) {
		t.Fatalf("expected stopped stripped of %s", gid)
	}
	assertNoTwinInGetTaskLists(t, gid)

	if _, ok := history.Get(gid); ok {
		t.Fatal("expected waiting-only revival to retire history once")
	}
	if tracker.processedComplete[gid] {
		t.Fatal("expected reopen without secondary terminal from stale stopped")
	}
}

func TestTick_StaleStopped_FastRetryAppend_Stripped(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	const gid = "ar_stale_fastretry"
	const keepGID = "ar_keep_fastretry"
	staleStopped := rpc.Task{
		GID: gid, Status: "complete",
		TotalLength: "1000", CompletedLength: "1000",
		Files: []rpc.File{{Path: "/tmp/fast.bin"}},
	}
	keepStopped := rpc.Task{
		GID: keepGID, Status: "complete",
		TotalLength: "500", CompletedLength: "500",
		Files: []rpc.File{{Path: "/tmp/keep.bin"}},
	}
	liveActive := rpc.Task{
		GID: gid, Status: "active",
		TotalLength: "1000", CompletedLength: "200", DownloadSpeed: "10",
		Files: []rpc.File{{Path: "/tmp/fast.bin"}},
	}

	engine := &mockTickEngine{}
	// Engine stopped has a non-conflicting survivor; cache still has live-conflicting gid.
	// Fast-retry would re-append the conflict; normalize must strip it and keep ar_keep.
	engine.setLists([]rpc.Task{liveActive}, nil, []rpc.Task{keepStopped})
	m := newTickRecoveryMonitor(t, engine)
	m.aria2Recovered.Store(true)
	m.shouldFetchStopped = true
	m.shouldFetchStoppedUntil = time.Now().Add(5 * time.Second)
	m.lastStoppedFetchTime = time.Now().Add(-20 * time.Second)

	tracker := m.tracker
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/fast.bin", 0, "complete")
	if completed := tracker.MarkCompleteFromEvent(gid, "complete"); completed == nil {
		t.Fatal("expected initial terminal marker")
	}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	Cache.UpdateFromAria2(nil, nil, []rpc.Task{staleStopped, keepStopped})
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/fast.bin", Status: "complete"})

	m.tick()

	assertExactlyOneActiveNoStopped(t, gid)
	assertNoTwinInGetTaskLists(t, gid)
	if _, ok := history.Get(gid); ok {
		t.Fatal("expected history retired; fast-retry conflict must not rewrite terminal")
	}
	if tracker.processedComplete[gid] {
		t.Fatal("expected no secondary terminal after fast-retry strip")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	keepFound := false
	for _, task := range m.lastStopped {
		if task.GID == gid {
			t.Fatal("expected lastStopped written after normalize without live conflict GID")
		}
		if task.GID == keepGID {
			keepFound = true
		}
	}
	if !keepFound {
		t.Fatalf("expected lastStopped to retain fast-retry/engine survivor %s, got %v", keepGID, taskGIDs(m.lastStopped))
	}
}

func TestTick_StaleStopped_TellStoppedFailureFallback_Normalized(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	const gid = "ar_stale_tellfail"
	staleStopped := rpc.Task{
		GID: gid, Status: "complete",
		Files: []rpc.File{{Path: "/tmp/fail.bin"}},
	}
	liveActive := rpc.Task{
		GID: gid, Status: "active",
		Files: []rpc.File{{Path: "/tmp/fail.bin"}},
	}

	engine := &mockTickEngine{
		stoppedLiteErr: errors.New("lite stopped unavailable"),
		stoppedErr:     errors.New("full stopped unavailable"),
	}
	engine.setLists([]rpc.Task{liveActive}, nil, []rpc.Task{staleStopped})
	m := newTickRecoveryMonitor(t, engine)
	m.aria2Recovered.Store(true)
	m.shouldFetchStopped = true
	m.lastStoppedFetchTime = time.Now().Add(-20 * time.Second)
	m.lastStopped = []rpc.Task{staleStopped}

	tracker := m.tracker
	tracker.EnsureTrackedFromEvent(gid, 100, "https://example.com/fail.bin", 0, "complete")
	if completed := tracker.MarkCompleteFromEvent(gid, "complete"); completed == nil {
		t.Fatal("expected initial terminal marker")
	}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	Cache.UpdateFromAria2(nil, nil, []rpc.Task{staleStopped})
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/fail.bin", Status: "complete"})

	m.tick()

	assertExactlyOneActiveNoStopped(t, gid)
	assertNoTwinInGetTaskLists(t, gid)
	if _, ok := history.Get(gid); ok {
		t.Fatal("expected TellStopped fallback path to retire without stale rewrite")
	}
	if tracker.processedComplete[gid] {
		t.Fatal("expected no secondary terminal on TellStopped fallback")
	}
}

func TestTick_Normalize_ActiveWaitingAnomaly(t *testing.T) {
	const gid = "ar_active_waiting"
	active := rpc.Task{GID: gid, Status: "active"}
	waiting := rpc.Task{GID: gid, Status: "waiting"}
	stopped := rpc.Task{GID: gid, Status: "complete"}

	engine := &mockTickEngine{}
	engine.setLists([]rpc.Task{active}, []rpc.Task{waiting}, []rpc.Task{stopped})
	m := newTickRecoveryMonitor(t, engine)
	m.aria2Recovered.Store(true)
	m.shouldFetchStopped = true
	m.lastStoppedFetchTime = time.Now().Add(-20 * time.Second)

	m.tick()

	assertExactlyOneActiveNoStopped(t, gid)
	for _, task := range Cache.GetWaiting() {
		if task.GID == gid {
			t.Fatal("expected waiting stripped when same GID is active")
		}
	}
	assertNoTwinInGetTaskLists(t, gid)
}

func assertExactlyOneActiveNoStopped(t *testing.T, gid string) {
	t.Helper()
	activeCount := 0
	for _, task := range Cache.GetActive() {
		if task.GID == gid {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("expected exactly one active %s, got %d", gid, activeCount)
	}
	if taskInCacheStopped(gid) {
		t.Fatalf("expected cache stopped to exclude %s", gid)
	}
}

func assertNoTwinInGetTaskLists(t *testing.T, gid string) {
	t.Helper()
	active, waiting, stopped := Cache.GetTaskLists()
	inActive, inWaiting, inStopped := false, false, false
	for _, task := range active {
		if task.GID == gid {
			inActive = true
		}
	}
	for _, task := range waiting {
		if task.GID == gid {
			inWaiting = true
		}
	}
	for _, task := range stopped {
		if task.GID == gid {
			inStopped = true
		}
	}
	if inStopped && (inActive || inWaiting) {
		t.Fatalf("GetTaskLists twin for %s: active=%v waiting=%v stopped=%v", gid, inActive, inWaiting, inStopped)
	}
}

// blockingPrefetchTickEngine blocks inside TellStatusMulti so tests can run
// InvalidateTask after filterDeletedTasks and before lastStopped persist.
type blockingPrefetchTickEngine struct {
	mockTickEngine
	prefetchCalled  chan struct{}
	prefetchRelease chan struct{}
}

func (e *blockingPrefetchTickEngine) TellStatusMulti(gids []string, keys []string) ([]rpc.Task, error) {
	select {
	case e.prefetchCalled <- struct{}{}:
	default:
	}
	<-e.prefetchRelease
	out := make([]rpc.Task, 0, len(gids))
	for _, gid := range gids {
		out = append(out, rpc.Task{
			GID:    gid,
			Status: "complete",
			Files:  []rpc.File{{Path: "/tmp/" + gid + ".bin"}},
		})
	}
	return out, nil
}

// TestTick_StaleStopped_MidTickInvalidate_DoesNotResurrectInLastStopped plants
// InvalidateTask after the early filterDeletedTasks pass (while Prefetch blocks)
// and asserts the persist-time deletedGids scrub keeps the GID out of lastStopped
// and cache stopped despite the always-rewrite of lastStopped.
func TestTick_StaleStopped_MidTickInvalidate_DoesNotResurrectInLastStopped(t *testing.T) {
	const (
		targetGID = "ar_midtick_deleted"
		keepGID   = "ar_midtick_keep"
	)
	target := rpc.Task{
		GID: targetGID, Status: "complete",
		TotalLength: "1000", CompletedLength: "1000",
	}
	keep := rpc.Task{
		GID: keepGID, Status: "complete",
		TotalLength: "500", CompletedLength: "500",
	}

	engine := &blockingPrefetchTickEngine{
		prefetchCalled:  make(chan struct{}, 1),
		prefetchRelease: make(chan struct{}),
	}
	engine.setLists(nil, nil, []rpc.Task{keep, target})

	m := newTickRecoveryMonitor(t, engine)
	m.aria2Recovered.Store(true)
	m.shouldFetchStopped = true
	m.lastStoppedFetchTime = time.Now().Add(-20 * time.Second)
	m.lastStopped = []rpc.Task{keep, target}

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

	select {
	case <-engine.prefetchCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for PrefetchMetadataMulti (TellStatusMulti)")
	}

	// Past filterDeletedTasks; tombstone now so always-rewrite without scrub
	// would resurrect target into lastStopped / UpdateFromAria2 stopped.
	m.InvalidateTask(targetGID)

	close(engine.prefetchRelease)

	if err := <-tickDone; err != nil {
		t.Fatalf("tick failed: %v", err)
	}

	if taskInCacheStopped(targetGID) {
		t.Fatal("expected mid-tick InvalidateTask scrub to keep target out of cache stopped")
	}
	if !taskInCacheStopped(keepGID) {
		t.Fatal("expected non-deleted survivor to remain in cache stopped")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	hasTarget, hasKeep := false, false
	for _, task := range m.lastStopped {
		if task.GID == targetGID {
			hasTarget = true
		}
		if task.GID == keepGID {
			hasKeep = true
		}
	}
	if hasTarget {
		t.Fatal("BUG: always-rewrite resurrected tombstoned GID into lastStopped")
	}
	if !hasKeep {
		t.Fatalf("expected lastStopped to retain %s, got %v", keepGID, taskGIDs(m.lastStopped))
	}
	if _, deleted := m.deletedGids[targetGID]; !deleted {
		t.Fatal("expected deletedGids tombstone to remain after tick")
	}
}
