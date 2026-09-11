package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func TestTaskTracker_PreservesErrorStatus(t *testing.T) {
	tracker := NewTaskTracker()

	// 模拟任务先为 active
	active := []rpc.Task{createMockTask("gid-001", "active")}
	completed := tracker.Update(active, nil, nil)
	if len(completed) != 0 {
		t.Errorf("Expected no completed tasks, got %d", len(completed))
	}

	// 现在任务变为 error 状态进入 stopped
	stopped := []rpc.Task{createMockTask("gid-001", "error")}
	completed = tracker.Update(nil, nil, stopped)

	if len(completed) != 1 {
		t.Fatalf("Expected 1 completed task, got %d", len(completed))
	}

	// 关键断言：状态应该是 error，而不是 complete
	if completed[0].Status != "error" {
		t.Errorf("Expected status 'error', got '%s'", completed[0].Status)
	}
}

func TestTaskTracker_PreservesCompleteStatus(t *testing.T) {
	tracker := NewTaskTracker()

	// 模拟任务先为 active
	active := []rpc.Task{createMockTask("gid-002", "active")}
	tracker.Update(active, nil, nil)

	// 任务正常完成
	stopped := []rpc.Task{createMockTask("gid-002", "complete")}
	completed := tracker.Update(nil, nil, stopped)

	if len(completed) != 1 {
		t.Fatalf("Expected 1 completed task, got %d", len(completed))
	}

	if completed[0].Status != "complete" {
		t.Errorf("Expected status 'complete', got '%s'", completed[0].Status)
	}
}

func TestTaskTracker_HandlesNewStoppedTask(t *testing.T) {
	tracker := NewTaskTracker()

	// 直接发现一个 stopped 任务（例如应用重启后）
	stopped := []rpc.Task{createMockTask("gid-003", "error")}
	completed := tracker.Update(nil, nil, stopped)

	if len(completed) != 1 {
		t.Fatalf("Expected 1 completed task, got %d", len(completed))
	}

	// 应该保留原始的 error 状态
	if completed[0].Status != "error" {
		t.Errorf("Expected status 'error', got '%s'", completed[0].Status)
	}
}

func TestTaskTracker_PreservesVariousStatuses(t *testing.T) {
	// 测试 complete 和 error 状态都被正确保留
	// 注意：tracker 只处理 complete 和 error 状态的任务作为"已完成"
	statuses := []string{"error", "complete"}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			tracker := NewTaskTracker()

			// active -> stopped with specific status
			active := []rpc.Task{createMockTask("gid-test", "active")}
			tracker.Update(active, nil, nil)

			stopped := []rpc.Task{createMockTask("gid-test", status)}
			completed := tracker.Update(nil, nil, stopped)

			if len(completed) != 1 {
				t.Fatalf("Expected 1 completed task for status %s, got %d", status, len(completed))
			}

			if completed[0].Status != status {
				t.Errorf("Expected status '%s', got '%s'", status, completed[0].Status)
			}
		})
	}
}

func TestTaskTracker_IdempotentProcessing(t *testing.T) {
	tracker := NewTaskTracker()

	// 第一次处理
	stopped := []rpc.Task{createMockTask("gid-004", "complete")}
	completed1 := tracker.Update(nil, nil, stopped)

	if len(completed1) != 1 {
		t.Errorf("Expected 1 completed task on first update, got %d", len(completed1))
	}

	// 第二次处理相同的 stopped 任务，不应再次触发
	completed2 := tracker.Update(nil, nil, stopped)

	if len(completed2) != 0 {
		t.Errorf("Expected 0 completed tasks on second update (idempotent), got %d", len(completed2))
	}
}

func TestTaskTracker_CleansRemovedTasks(t *testing.T) {
	tracker := NewTaskTracker()

	// 添加任务
	active := []rpc.Task{createMockTask("gid-005", "active")}
	tracker.Update(active, nil, nil)

	// 修改任务创建时间，使其超过宽限期 (模拟旧任务)
	tracked := tracker.tasks["gid-005"]
	tracked.CreatedAt = time.Now().Add(-10 * time.Second)

	// 任务被移除（不在任何列表中）
	tracker.Update(nil, nil, nil)

	// 内部 map 应该被清理
	if len(tracker.tasks) != 0 {
		t.Errorf("Expected 0 tracked tasks after removal, got %d", len(tracker.tasks))
	}
}

func TestTaskTracker_ConcurrentAccess(t *testing.T) {
	tracker := NewTaskTracker()

	// 并发测试
	done := make(chan bool, 10)

	for i := range 10 {
		go func(idx int) {
			gid := "gid-concurrent-" + string(rune('A'+idx))
			active := []rpc.Task{createMockTask(gid, "active")}
			tracker.Update(active, nil, nil)
			tracker.SetThreadInfo(gid, idx, false)
			tracker.GetThreadInfo(gid)
			done <- true
		}(i)
	}

	for range 10 {
		<-done
	}

	// 应该没有 panic
	t.Log("Concurrent access test passed")
}

// TestTaskTracker_Update_DoesNotCleanSgTasks verifies that Tracker.Update
// does not remove sg_ prefixed tasks from the tracker, even when they are
// absent from the active/waiting/stopped lists. Surge tasks are maintained
// by the event-driven path, not by tick polling.
func TestTaskTracker_Update_DoesNotCleanSgTasks(t *testing.T) {
	tracker := NewTaskTracker()

	// Seed a sg_ task via EnsureTrackedFromEvent
	tracker.EnsureTrackedFromEvent("sg_survive-1", 100000, "https://example.com", 4, "active")
	tracker.EnsureTrackedFromEvent("sg_survive-2", 200000, "https://example.com", 8, "active")

	// Also seed an ar_ task
	active := []rpc.Task{createMockTask("ar_active-1", "active")}
	tracker.Update(active, nil, nil)

	// Set ar_active-1's CreatedAt beyond grace period so it can be cleaned up
	tracker.mu.Lock()
	if t := tracker.tasks["ar_active-1"]; t != nil {
		t.CreatedAt = time.Now().Add(-TaskGracePeriod * 2)
	}
	tracker.mu.Unlock()

	// Verify both exist
	if tracker.tasks["sg_survive-1"] == nil {
		t.Fatal("expected sg_survive-1 to exist before cleanup tick")
	}
	if tracker.tasks["sg_survive-2"] == nil {
		t.Fatal("expected sg_survive-2 to exist before cleanup tick")
	}
	if tracker.tasks["ar_active-1"] == nil {
		t.Fatal("expected ar_active-1 to exist before cleanup tick")
	}

	// Update with NO sg_ tasks and NO ar_active-1 — only ar_ tasks that are different
	tracker.Update(
		[]rpc.Task{createMockTask("ar_active-2", "active")},
		nil, nil,
	)

	// sg_ tasks should survive (not cleaned up by tick)
	if tracker.tasks["sg_survive-1"] == nil {
		t.Fatal("expected sg_survive-1 to survive tick cleanup (sg_ prefix exempt)")
	}
	if tracker.tasks["sg_survive-2"] == nil {
		t.Fatal("expected sg_survive-2 to survive tick cleanup (sg_ prefix exempt)")
	}

	// ar_active-1 should be cleaned up (not in current lists, past grace period)
	if tracker.tasks["ar_active-1"] != nil {
		t.Fatal("expected ar_active-1 to be cleaned up by tick (not in current lists)")
	}
}

// TestTaskTracker_Update_CleansArTasks verifies that ar_ prefixed tasks
// ARE cleaned up by Tracker.Update when absent from current lists.
func TestTaskTracker_Update_CleansArTasks(t *testing.T) {
	tracker := NewTaskTracker()

	// Seed ar_ tasks
	active := []rpc.Task{
		createMockTask("ar_clean-1", "active"),
		createMockTask("ar_clean-2", "active"),
	}
	tracker.Update(active, nil, nil)

	// Set CreatedAt beyond grace period so they can be cleaned up
	tracker.mu.Lock()
	for _, gid := range []string{"ar_clean-1", "ar_clean-2"} {
		if t := tracker.tasks[gid]; t != nil {
			t.CreatedAt = time.Now().Add(-TaskGracePeriod * 2)
		}
	}
	tracker.mu.Unlock()

	// Update with only ar_clean-1 (ar_clean-2 is gone)
	tracker.Update(
		[]rpc.Task{createMockTask("ar_clean-1", "active")},
		nil, nil,
	)

	// ar_clean-1 should survive
	if tracker.tasks["ar_clean-1"] == nil {
		t.Fatal("expected ar_clean-1 to survive (still in active list)")
	}

	// ar_clean-2 should be cleaned up
	if tracker.tasks["ar_clean-2"] != nil {
		t.Fatal("expected ar_clean-2 to be cleaned up (not in current lists)")
	}
}

func TestEngineStatusForTask_Mapping(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"active", "active"},
		{"downloading", "active"},
		{"waiting", "waiting"},
		{"paused", "paused"},
		{"complete", "complete"},
		{"error", "error"},
		{"", "active"},
		{"unknown", "active"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := engineStatusForTask(c.in); got != c.want {
				t.Errorf("engineStatusForTask(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
