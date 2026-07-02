package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/engine/events"
)

func TestMonitor_FilterDeletedTasks_TombstoneWindow(t *testing.T) {
	m := &Monitor{
		deletedGids: map[string]time.Time{
			"ar_test": time.Now(),
		},
	}

	tasks := []rpc.Task{
		{GID: "ar_test", Status: "active"},
		{GID: "ar_other", Status: "active"},
	}

	filtered := m.filterDeletedTasks(tasks)

	if len(filtered) != 1 {
		t.Fatalf("expected 1 task after filtering, got %d", len(filtered))
	}
	if filtered[0].GID != "ar_other" {
		t.Errorf("expected ar_other to remain, got %s", filtered[0].GID)
	}
}

func TestMonitor_FilterDeletedTasks_ExpiredTombstone(t *testing.T) {
	m := &Monitor{
		deletedGids: map[string]time.Time{
			"ar_expired": time.Now().Add(-20 * time.Second),
		},
	}

	tasks := []rpc.Task{
		{GID: "ar_expired", Status: "complete"},
	}

	filtered := m.filterDeletedTasks(tasks)

	if len(filtered) != 1 {
		t.Fatalf("expected expired tombstone task to remain, got %d tasks", len(filtered))
	}
	if filtered[0].GID != "ar_expired" {
		t.Errorf("expected ar_expired to remain, got %s", filtered[0].GID)
	}

	m.mu.Lock()
	_, stillPresent := m.deletedGids["ar_expired"]
	m.mu.Unlock()
	if stillPresent {
		t.Error("expected expired tombstone to be cleaned up from deletedGids")
	}
}

func TestMonitor_HandleSurgeEvent_CompleteEventDispatches(t *testing.T) {
	hub := events.NewHub(nil)

	m := &Monitor{
		hub:      hub,
		stopChan: make(chan struct{}),
	}

	var receivedDelta *events.TaskDelta
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		receivedDelta = &delta
	})

	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "test-123",
	})

	if receivedDelta == nil {
		t.Fatal("expected non-nil TaskDelta dispatched synchronously")
	}
	if receivedDelta.Type != "complete" {
		t.Errorf("delta type = %q, want complete", receivedDelta.Type)
	}
	if receivedDelta.GID != "sg_test-123" {
		t.Errorf("delta GID = %q, want sg_test-123", receivedDelta.GID)
	}
}
