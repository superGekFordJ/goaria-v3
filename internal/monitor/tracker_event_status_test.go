package monitor

import (
	"testing"
)

func TestEnsureTrackedFromEvent_SetsStatusOnExistingTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg-status-existing", 8, false)
	if tracked := tracker.tasks["sg-status-existing"]; tracked.Status != "" {
		t.Fatalf("seed Status = %q, want empty", tracked.Status)
	}
	tracker.EnsureTrackedFromEvent("sg-status-existing", 100000000, "https://example.com/file.zip", 8, "active")
	if tracked := tracker.tasks["sg-status-existing"]; tracked.Status != "active" {
		t.Errorf("Status = %q, want active", tracked.Status)
	}
}

func TestEnsureTrackedFromEvent_QueuedStatusNotActive(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-status-queued", 0, "https://example.com/file.zip", 8, "waiting")
	tracked := tracker.tasks["sg-status-queued"]
	if tracked.Status != "waiting" {
		t.Errorf("Status = %q, want waiting", tracked.Status)
	}
	if activeSetContains(tracker, "sg-status-queued") {
		t.Error("queued task should be excluded from GetActiveTrackedTasks")
	}
}

func TestEnsureTrackedFromEvent_StartedStatusActive(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-status-started", 100000000, "https://example.com/file.zip", 8, "active")
	if !activeSetContains(tracker, "sg-status-started") {
		t.Error("started task should be included in GetActiveTrackedTasks")
	}
}

func TestEnsureTrackedFromEvent_EmptyStatusDefaultsActive(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-status-empty", 0, "https://example.com/file.zip", 0, "")
	if tracked := tracker.tasks["sg-status-empty"]; tracked.Status != "active" {
		t.Errorf("Status = %q, want active fallback", tracked.Status)
	}
}

func TestEnsureTrackedFromEvent_EmptyStatusDoesNotClobberExisting(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-status-noclobber", 100000000, "https://example.com/file.zip", 8, "active")
	tracker.EnsureTrackedFromEvent("sg-status-noclobber", 100000000, "", 0, "")
	if tracked := tracker.tasks["sg-status-noclobber"]; tracked.Status != "active" {
		t.Errorf("Status = %q, want active (empty must not clobber)", tracked.Status)
	}
}

func TestSetStatusFromEvent_PauseResume(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-status-pauseresume", 100000000, "https://example.com/file.zip", 8, "active")
	if !activeSetContains(tracker, "sg-status-pauseresume") {
		t.Fatal("seeded task should be active")
	}
	tracker.SetStatusFromEvent("sg-status-pauseresume", "paused")
	if activeSetContains(tracker, "sg-status-pauseresume") {
		t.Error("paused task should be excluded from GetActiveTrackedTasks")
	}
	tracker.SetStatusFromEvent("sg-status-pauseresume", "active")
	if !activeSetContains(tracker, "sg-status-pauseresume") {
		t.Error("resumed task should be included in GetActiveTrackedTasks")
	}
}

func TestSetStatusFromEvent_NoOpOnUnknownGid(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetStatusFromEvent("sg-status-unknown", "paused")
	if tracked := tracker.tasks["sg-status-unknown"]; tracked != nil {
		t.Errorf("unknown gid should not be created, got %#v", tracked)
	}
}

func TestEnsureTrackedFromEvent_PlaceholderQueuedStarted(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg-status-placeholder", 8, false)
	tracker.EnsureTrackedFromEvent("sg-status-placeholder", 0, "https://example.com/file.zip", 8, "waiting")
	if tracked := tracker.tasks["sg-status-placeholder"]; tracked.Status != "waiting" {
		t.Errorf("after queued: Status = %q, want waiting", tracked.Status)
	}
	tracker.EnsureTrackedFromEvent("sg-status-placeholder", 100000000, "https://example.com/file.zip", 8, "active")
	if tracked := tracker.tasks["sg-status-placeholder"]; tracked.Status != "active" {
		t.Errorf("after started: Status = %q, want active", tracked.Status)
	}
}

func TestEnsureTrackedFromEvent_CompleteDoesNotClobber(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-status-complete", 100000000, "https://example.com/file.zip", 8, "active")
	tracker.EnsureTrackedFromEvent("sg-status-complete", 100000000, "", 0, "")
	if tracked := tracker.tasks["sg-status-complete"]; tracked.Status != "active" {
		t.Errorf("after empty ensure: Status = %q, want active", tracked.Status)
	}
	if completed := tracker.MarkCompleteFromEvent("sg-status-complete", "complete"); completed == nil {
		t.Fatal("MarkCompleteFromEvent returned nil")
	}
	if tracked := tracker.tasks["sg-status-complete"]; tracked.Status != "complete" {
		t.Errorf("after mark complete: Status = %q, want complete", tracked.Status)
	}
}

func TestSetStatusFromEvent_DoesNotResurrectTerminalTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-terminal-resurrect", 100000000, "https://example.com/file.zip", 8, "active")
	if completed := tracker.MarkCompleteFromEvent("sg-terminal-resurrect", "complete"); completed == nil {
		t.Fatal("MarkCompleteFromEvent returned nil")
	}
	if tracked := tracker.tasks["sg-terminal-resurrect"]; tracked.Status != "complete" {
		t.Fatalf("seed Status = %q, want complete", tracked.Status)
	}

	// SetStatusFromEvent must not flip a processedComplete task back to active.
	tracker.SetStatusFromEvent("sg-terminal-resurrect", "active")
	if tracked := tracker.tasks["sg-terminal-resurrect"]; tracked.Status != "complete" {
		t.Errorf("Status = %q, want complete (terminal must not resurrect)", tracked.Status)
	}
	if activeSetContains(tracker, "sg-terminal-resurrect") {
		t.Error("terminal task must not re-enter GetActiveTrackedTasks")
	}
}

func TestTaskTracker_ReopenAfterStoppedToLive_AllowsSecondComplete(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-reopen", 1000, "https://example.com/f.bin", 0, "active")
	if first := tracker.MarkCompleteFromEvent("sg-reopen", "error"); first == nil {
		t.Fatal("expected first complete")
	}
	tracker.SetStatusFromEvent("sg-reopen", "active")
	if tracker.tasks["sg-reopen"].Status != "error" {
		t.Fatal("SetStatusFromEvent must still refuse terminal without explicit reopen")
	}
	tracker.ReopenAfterStoppedToLive("sg-reopen", "active")
	if tracker.processedComplete["sg-reopen"] {
		t.Fatal("expected processedComplete cleared")
	}
	if tracker.tasks["sg-reopen"].Status != "active" {
		t.Fatalf("Status = %q, want active", tracker.tasks["sg-reopen"].Status)
	}
	if second := tracker.MarkCompleteFromEvent("sg-reopen", "error"); second == nil {
		t.Fatal("expected second MarkCompleteFromEvent after reopen")
	}
}

func TestEnsureTrackedFromEvent_DoesNotResurrectTerminalTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-terminal-ensure", 100000000, "https://example.com/file.zip", 8, "active")
	if completed := tracker.MarkCompleteFromEvent("sg-terminal-ensure", "complete"); completed == nil {
		t.Fatal("MarkCompleteFromEvent returned nil")
	}
	if tracked := tracker.tasks["sg-terminal-ensure"]; tracked.Status != "complete" {
		t.Fatalf("seed Status = %q, want complete", tracked.Status)
	}

	// EnsureTrackedFromEvent existing-branch must not flip a processedComplete
	// task back to active (e.g. a late DownloadStartedMsg on a completed gid).
	tracker.EnsureTrackedFromEvent("sg-terminal-ensure", 100000000, "https://example.com/file.zip", 8, "active")
	if tracked := tracker.tasks["sg-terminal-ensure"]; tracked.Status != "complete" {
		t.Errorf("Status = %q, want complete (terminal must not resurrect)", tracked.Status)
	}
	if activeSetContains(tracker, "sg-terminal-ensure") {
		t.Error("terminal task must not re-enter GetActiveTrackedTasks")
	}
}
