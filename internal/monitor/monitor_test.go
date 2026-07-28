package monitor

import (
	"strings"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
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

func TestMonitor_FilterDeletedTasks_OnlyFiltersArPrefix(t *testing.T) {
	m := &Monitor{
		deletedGids: map[string]time.Time{
			"ar_deleted": time.Now(),
			"sg_deleted": time.Now(),
		},
	}

	tasks := []rpc.Task{
		{GID: "ar_deleted", Status: "active"},
		{GID: "ar_keep", Status: "active"},
		{GID: "sg_deleted", Status: "active"},
		{GID: "sg_keep", Status: "active"},
	}

	filtered := m.filterDeletedTasks(tasks)

	got := make(map[string]bool, len(filtered))
	for _, task := range filtered {
		got[task.GID] = true
	}

	if got["ar_deleted"] {
		t.Error("expected ar_deleted to be tombstone-filtered")
	}
	if !got["ar_keep"] {
		t.Error("expected ar_keep to remain")
	}
	if !got["sg_deleted"] {
		t.Error("expected sg_deleted to pass through (sg_ not tombstone-filtered)")
	}
	if !got["sg_keep"] {
		t.Error("expected sg_keep to remain")
	}
}

func TestMonitor_DetectAndEmitTaskMoves_OnlyArPrefix(t *testing.T) {
	hub := events.NewHub(nil)
	var moves []events.TaskMove
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		moves = append(moves, move)
	})

	m := &Monitor{
		hub:             hub,
		prevActiveGids:  map[string]bool{},
		prevWaitingGids: map[string]bool{"ar_prior": true, "sg_prior": true},
	}

	active := []rpc.Task{
		{GID: "sg_prior", Status: "active"},
		{GID: "ar_prior", Status: "active"},
	}
	waiting := []rpc.Task{}

	m.detectAndEmitTaskMoves(active, waiting)

	for _, move := range moves {
		if strings.HasPrefix(move.GID, "sg_") {
			t.Errorf("expected no task:move for sg_ task, got GID=%s", move.GID)
		}
	}
	foundArMove := false
	for _, move := range moves {
		if move.GID == "ar_prior" && move.From == "waiting" && move.To == "active" {
			foundArMove = true
		}
	}
	if !foundArMove {
		t.Error("expected task:move for ar_prior (waiting -> active)")
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

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
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
