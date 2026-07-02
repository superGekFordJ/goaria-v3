package monitor

import (
	"context"
	"fmt"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
)

// mockSurgeActiveEngine wraps Aria2Engine but reports IsSurgeActive()=true,
// simulating production where SurgeEngine always has a non-nil service.
type mockSurgeActiveEngine struct {
	rpc.Aria2Engine
}

func (e *mockSurgeActiveEngine) IsSurgeActive() bool {
	return true
}

// mockSafeEngine returns errors from TellStatus without panicking,
// for tests that trigger handleTaskComplete but don't need real RPC.

// mockSafeEngine returns errors from TellStatus without panicking,
// for tests that trigger handleTaskComplete but don't need real RPC.
type mockSafeEngine struct {
	mockSurgeActiveEngine
}

func (e *mockSafeEngine) TellStatus(gid string, keys []string) (rpc.Task, error) {
	return rpc.Task{}, fmt.Errorf("mock: no engine")
}

// speedstatsRecordCount returns the number of in-memory speedstats records.
func speedstatsRecordCount() int {
	return len(speedstats.GetAllRecords())
}

// findRecordByDomain returns the most recent speedstats record matching the given domain.

// findRecordByDomain returns the most recent speedstats record matching the given domain.
func findRecordByDomain(domain string) *speedstats.SpeedRecord {
	records := speedstats.GetAllRecords()
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Domain == domain {
			return &records[i]
		}
	}
	return nil
}

// TestHandleTaskComplete_PeakThreadCountFallback verifies that handleTaskComplete
// uses PeakThreadCount (from convergence) as the primary source for speedstats ThreadCount.

// mockStoppedEngine returns a fixed set of stopped tasks from TellStopped/
// TellStoppedLite, allowing tick-level tests to simulate a Surge engine whose
// DB still contains a completed task (the async DeleteState race window).
type mockStoppedEngine struct {
	mockSurgeActiveEngine
	stopped []rpc.Task
}

func (e *mockStoppedEngine) TellStopped(offset, num int) ([]rpc.Task, error) {
	return e.stopped, nil
}

func (e *mockStoppedEngine) TellStoppedLite(offset, num int) ([]rpc.Task, error) {
	return e.stopped, nil
}

func (e *mockStoppedEngine) TellActive() ([]rpc.Task, error) { return nil, nil }

func (e *mockStoppedEngine) TellActiveLite() ([]rpc.Task, error) { return nil, nil }

func (e *mockStoppedEngine) TellActiveProgress() ([]rpc.TaskProgress, error) {
	return nil, nil
}

func (e *mockStoppedEngine) TellWaiting(offset, num int) ([]rpc.Task, error) {
	return nil, nil
}

func (e *mockStoppedEngine) TellWaitingLite(offset, num int) ([]rpc.Task, error) {
	return nil, nil
}

func (e *mockStoppedEngine) GetGlobalStat() (rpc.GlobalStat, error) {
	return rpc.GlobalStat{}, nil
}

func (e *mockStoppedEngine) SaveSession() error { return nil }

func (e *mockStoppedEngine) ChangeGlobalOption(map[string]string) error {
	return nil
}

func (e *mockStoppedEngine) StreamEvents(ctx context.Context) (<-chan any, func(), error) {
	ch := make(chan any)
	cleanup := func() {}
	return ch, cleanup, nil
}

func (e *mockStoppedEngine) TellStatus(gid string, keys []string) (rpc.Task, error) {
	return rpc.Task{}, fmt.Errorf("mock: no engine")
}

func (e *mockStoppedEngine) TellStatusMulti(gids []string, keys []string) ([]rpc.Task, error) {
	return nil, nil
}

// TestRemoveGroupCompletedTaskReappears_NoResurrection reproduces the
// regression where deleting a download group containing a completed task
// causes the completed task to reappear in the stopped list.
//
// Root cause: InvalidateListCache() on the complete event causes the 1s TTL
// list cache to be repopulated with the completed task. During the deletion
// race (a tick fires between Cache.RemoveTask and InvalidateTask), TellStopped
// returns the completed task from the fresh list cache. filterDeletedTasks
// does not catch it (deletedGids not set yet). UpdateFromAria2 puts it back
// into Cache.GetStopped(). On the next tick, filterDeletedTasks catches it,
// but the shouldFetchStoppedUntil fast-retry preserve logic re-appends it from
// Cache.GetStopped(), bypassing filterDeletedTasks. The task persists.

func taskInCacheStopped(gid string) bool {
	Cache.mu.RLock()
	defer Cache.mu.RUnlock()
	for _, task := range Cache.stopped {
		if task.GID == gid {
			return true
		}
	}
	return false
}

// blockingStoppedEngine is a mockStoppedEngine whose TellStoppedLite/TellStopped
// block on a release channel, letting tests control exactly when tick() proceeds
// past the wg.Wait() barrier. A "called" channel signals that the engine method
// was entered.

// blockingStoppedEngine is a mockStoppedEngine whose TellStoppedLite/TellStopped
// block on a release channel, letting tests control exactly when tick() proceeds
// past the wg.Wait() barrier. A "called" channel signals that the engine method
// was entered.
type blockingStoppedEngine struct {
	mockStoppedEngine
	called  chan struct{}
	release chan struct{}
}

func (e *blockingStoppedEngine) TellStoppedLite(offset, num int) ([]rpc.Task, error) {
	e.called <- struct{}{}
	<-e.release
	return e.stopped, nil
}

func (e *blockingStoppedEngine) TellStopped(offset, num int) ([]rpc.Task, error) {
	e.called <- struct{}{}
	<-e.release
	return e.stopped, nil
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
