package monitor

import (
	"testing"

	"goaria-v3/internal/rpc"
)

// ==================== Event-Driven Speed Sampling Tests ====================

func TestTaskTracker_EnsureTrackedFromEvent_CreatesNew(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-001", 100000000, "https://example.com/file.zip", 8, "active")

	tracked := tracker.tasks["sg-evt-001"]
	if tracked == nil {
		t.Fatal("Expected task to be tracked after EnsureTrackedFromEvent")
	}
	if tracked.TotalLength != 100000000 {
		t.Errorf("TotalLength = %d, want 100000000", tracked.TotalLength)
	}
	if tracked.SourceURL != "https://example.com/file.zip" {
		t.Errorf("SourceURL = %s, want https://example.com/file.zip", tracked.SourceURL)
	}
	if tracked.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d, want 8", tracked.ThreadCount)
	}
}

func TestTaskTracker_EnsureTrackedFromEvent_UpdatesExisting(t *testing.T) {
	tracker := NewTaskTracker()
	// Pre-populate via Update
	active := []rpc.Task{createMockTask("sg-evt-002", "active")}
	tracker.Update(active, nil, nil)

	// EnsureTrackedFromEvent should update, not overwrite
	tracker.EnsureTrackedFromEvent("sg-evt-002", 200000000, "https://new.com/file.zip", 16, "active")

	tracked := tracker.tasks["sg-evt-002"]
	if tracked == nil {
		t.Fatal("Expected task to be tracked")
	}
	if tracked.TotalLength != 200000000 {
		t.Errorf("TotalLength = %d, want 200000000 (updated)", tracked.TotalLength)
	}
}

func TestTaskTracker_EnsureTrackedFromEvent_DoesNotOverwriteThreadCount(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg-evt-003", 4, true)

	tracker.EnsureTrackedFromEvent("sg-evt-003", 100000000, "https://example.com/file.zip", 8, "active")

	tracked := tracker.tasks["sg-evt-003"]
	if tracked == nil {
		t.Fatal("Expected task to be tracked")
	}
	if tracked.ThreadCount != 4 {
		t.Errorf("ThreadCount = %d, want 4 (should not be overwritten)", tracked.ThreadCount)
	}
}

func TestTaskTracker_UpdateProgressFromEvent_LargeFile(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-004", 100000000, "https://example.com/file.zip", 8, "active")

	tracker.UpdateProgressFromEvent("sg-evt-004", 100000000, 60000000)
	tracked := tracker.tasks["sg-evt-004"]
	if tracked.CompletedLength != 60000000 {
		t.Errorf("CompletedLength = %d, want 60000000", tracked.CompletedLength)
	}
	if tracked.PeakSpeed != 0 {
		t.Errorf("PeakSpeed = %d, want 0 (event path does not sample peak)", tracked.PeakSpeed)
	}

	tracker.UpdateProgressFromEvent("sg-evt-004", 100000000, 70000000)
	if tracked.CompletedLength != 70000000 {
		t.Errorf("CompletedLength = %d, want 70000000", tracked.CompletedLength)
	}
	if tracked.PeakSpeed != 0 {
		t.Errorf("PeakSpeed = %d, want 0 after second progress event", tracked.PeakSpeed)
	}
}

func TestTaskTracker_UpdateProgressFromEvent_SmallFile(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-005", 1000000, "https://example.com/small.zip", 4, "active")

	tracker.UpdateProgressFromEvent("sg-evt-005", 1000000, 500000)
	tracked := tracker.tasks["sg-evt-005"]
	if tracked.CompletedLength != 500000 {
		t.Errorf("CompletedLength = %d, want 500000 (small files still update lengths)", tracked.CompletedLength)
	}
	if tracked.TotalLength != 1000000 {
		t.Errorf("TotalLength = %d, want 1000000", tracked.TotalLength)
	}
	if tracked.PeakSpeed != 0 {
		t.Errorf("PeakSpeed = %d, want 0", tracked.PeakSpeed)
	}
}

func TestTaskTracker_UpdateProgressFromEvent_NonexistentTask(t *testing.T) {
	tracker := NewTaskTracker()
	// Should not panic
	tracker.UpdateProgressFromEvent("nonexistent", 100000000, 1000000)
}

func TestTaskTracker_MarkCompleteFromEvent(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-006", 100000000, "https://example.com/file.zip", 8, "active")

	completed := tracker.MarkCompleteFromEvent("sg-evt-006", "complete")
	if completed == nil {
		t.Fatal("Expected non-nil completed task")
	}
	if completed.Status != "complete" {
		t.Errorf("Status = %s, want complete", completed.Status)
	}
	if completed.GID != "sg-evt-006" {
		t.Errorf("GID = %s, want sg-evt-006", completed.GID)
	}
}

func TestTaskTracker_MarkCompleteFromEvent_Idempotent(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-007", 100000000, "https://example.com/file.zip", 8, "active")

	first := tracker.MarkCompleteFromEvent("sg-evt-007", "complete")
	if first == nil {
		t.Fatal("Expected non-nil on first call")
	}

	second := tracker.MarkCompleteFromEvent("sg-evt-007", "complete")
	if second != nil {
		t.Fatal("Expected nil on second call (idempotent)")
	}
}

func TestTaskTracker_MarkCompleteFromEvent_NonexistentTask(t *testing.T) {
	tracker := NewTaskTracker()
	completed := tracker.MarkCompleteFromEvent("nonexistent", "complete")
	if completed != nil {
		t.Fatal("Expected nil for nonexistent task")
	}
}

func TestTaskTracker_MarkCompleteFromEvent_ErrorStatus(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-008", 100000000, "https://example.com/file.zip", 8, "active")

	completed := tracker.MarkCompleteFromEvent("sg-evt-008", "error")
	if completed == nil {
		t.Fatal("Expected non-nil completed task")
	}
	if completed.Status != "error" {
		t.Errorf("Status = %s, want error", completed.Status)
	}
}

func TestTaskTracker_UpdateProgressFromEvent_UpdatesTotalAndCompleted(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-011", 0, "https://example.com/file.zip", 8, "active")

	tracker.UpdateProgressFromEvent("sg-evt-011", 200000000, 10000000)
	tracked := tracker.tasks["sg-evt-011"]
	if tracked.TotalLength != 200000000 {
		t.Errorf("TotalLength = %d, want 200000000", tracked.TotalLength)
	}
	if tracked.CompletedLength != 10000000 {
		t.Errorf("CompletedLength = %d, want 10000000", tracked.CompletedLength)
	}
}

// TestEventAndTickPath_NoDoubleComplete 验证事件路径先标记完成后，
// tick 路径 Update 不会重复触发 handleTaskComplete。
func TestEventAndTickPath_NoDoubleComplete(t *testing.T) {
	tracker := NewTaskTracker()

	// 1. 事件路径创建并刷新进度（不再采样 PeakSpeed）
	tracker.EnsureTrackedFromEvent("sg-cross-001", 100000000, "https://example.com/file.zip", 8, "active")
	tracker.UpdateProgressFromEvent("sg-cross-001", 100000000, 60000000)
	tracker.UpdateProgressFromEvent("sg-cross-001", 100000000, 70000000)

	// 2. 事件路径标记完成
	completed := tracker.MarkCompleteFromEvent("sg-cross-001", "complete")
	if completed == nil {
		t.Fatal("Expected non-nil from event-path MarkCompleteFromEvent")
	}
	if completed.PeakSpeed != 0 {
		t.Errorf("PeakSpeed = %d, want 0 (event path does not sample peak)", completed.PeakSpeed)
	}
	if completed.CompletedLength != 70000000 {
		t.Errorf("CompletedLength = %d, want 70000000", completed.CompletedLength)
	}

	// 3. tick 路径 Update 收到 stopped 列表中有该任务
	stopped := []rpc.Task{createMockTask("sg-cross-001", "complete")}
	tickCompleted := tracker.Update(nil, nil, stopped)

	// 4. tick 路径不应重复返回已完成任务
	if len(tickCompleted) != 0 {
		t.Errorf("Expected 0 completed from tick path (already processed by event), got %d", len(tickCompleted))
		for i, tc := range tickCompleted {
			t.Errorf("  tick completed[%d]: GID=%s Status=%s", i, tc.GID, tc.Status)
		}
	}
}

// TestEventAndTickPath_TickCompletesFirst 验证反向场景：
// tick 路径先标记完成后，事件路径 MarkCompleteFromEvent 返回 nil。
func TestEventAndTickPath_TickCompletesFirst(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	tracker := NewTaskTracker()

	// 1. 事件路径创建任务
	tracker.EnsureTrackedFromEvent("sg-cross-002", 100000000, "https://example.com/file.zip", 8, "active")

	// 2. tick 路径先检测到完成
	stopped := []rpc.Task{createMockTask("sg-cross-002", "complete")}
	tickCompleted := tracker.Update(nil, nil, stopped)
	if len(tickCompleted) != 1 {
		t.Fatalf("Expected 1 completed from tick path, got %d", len(tickCompleted))
	}

	// 3. 事件路径随后收到 DownloadCompleteMsg，应返回 nil
	eventCompleted := tracker.MarkCompleteFromEvent("sg-cross-002", "complete")
	if eventCompleted != nil {
		t.Error("Expected nil from event-path MarkCompleteFromEvent (already processed by tick)")
	}
}
