package orchestrator

import (
	"errors"
	"testing"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// dispatchLifecycleEvent runs one event through a real StartEventWorker on a
// bounded channel and returns once the worker has consumed it.
func dispatchLifecycleEvent(t *testing.T, mgr *LifecycleManager, event types.DownloadEvent) {
	t.Helper()
	ch := make(chan types.DownloadEvent, 1)
	ch <- event
	close(ch)
	mgr.StartEventWorker(ch)
}

func TestLatePauseCannotRegressCompletedRecord(t *testing.T) {
	testutil.SetupStateDB(t)
	mgr := NewLifecycleManager(nil, nil, config.DefaultSettings())
	t.Cleanup(mgr.Shutdown)
	testutil.SeedMasterList(t, types.DownloadRecord{ID: "finished", Status: "completed", TotalSize: 123, Downloaded: 123, CompletedAt: 42})
	for _, snapshot := range []*types.DownloadRecord{nil, {Downloaded: 10}} {
		dispatchLifecycleEvent(t, mgr, types.DownloadEvent{Type: types.EventPaused, DownloadID: "finished", Downloaded: 10, State: snapshot})
		got, err := store.GetDownload("finished")
		if err != nil || got.Status != "completed" || got.Downloaded != 123 || got.CompletedAt != 42 {
			t.Fatalf("completion regressed: %+v, %v", got, err)
		}
	}
}

func TestRepeatedPauseOfStoppedRecordsDoesNotPublish(t *testing.T) {
	testutil.SetupStateDB(t)
	bus := NewEventBus()
	pool := scheduler.New(bus.InputCh, 1)
	mgr := NewLifecycleManager(pool, bus, config.DefaultSettings())
	t.Cleanup(mgr.Shutdown)
	// The barrier rides an ordinary subscription: EventSystem is transient and
	// never enters the reliable feed.
	ch, cleanup := bus.Subscribe()
	t.Cleanup(cleanup)
	for _, status := range []string{"completed", "paused", "error"} {
		testutil.SeedMasterList(t, types.DownloadRecord{ID: status, Status: status})
		if err := mgr.Pause(status); err != nil {
			t.Fatal(err)
		}
		if err := mgr.Pause(status); err != nil {
			t.Fatal(err)
		}
	}
	if err := bus.Publish(types.DownloadEvent{Type: types.EventSystem, Message: "barrier"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-ch:
			if event.Type == types.EventPaused {
				t.Fatalf("published a spurious pause for %s", event.DownloadID)
			}
			if event.Message == "barrier" {
				return
			}
		case <-deadline.C:
			t.Fatal("event barrier was not delivered")
		}
	}
}

// Non-terminal stored records keep the synthetic-pause path: Pause still
// emits EventPaused and resolves nil, while a missing ID stays ErrNotFound.
func TestPauseOfLiveStoredRecordsStillSynthesizesEvent(t *testing.T) {
	testutil.SetupStateDB(t)
	bus := NewEventBus()
	pool := scheduler.New(bus.InputCh, 1)
	mgr := NewLifecycleManager(pool, bus, config.DefaultSettings())
	t.Cleanup(mgr.Shutdown)
	ch, cleanup := bus.Subscribe()
	t.Cleanup(cleanup)

	for _, status := range []string{"queued", "downloading"} {
		testutil.SeedMasterList(t, types.DownloadRecord{ID: status, Status: status})
		if err := mgr.Pause(status); err != nil {
			t.Fatalf("Pause(%s) = %v", status, err)
		}
		deadline := time.NewTimer(3 * time.Second)
		for seen := false; !seen; {
			select {
			case event := <-ch:
				if event.Type == types.EventPaused && event.DownloadID == status {
					seen = true
				}
			case <-deadline.C:
				deadline.Stop()
				t.Fatalf("no synthesized pause delivered for %s record", status)
			}
		}
		deadline.Stop()
	}

	if err := mgr.Pause("missing-id"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("Pause(missing) = %v, want ErrNotFound", err)
	}
}
