package monitor

import (
	"testing"

	"goaria-v3/internal/rpc"
)

// createMockTask 创建模拟任务
func createMockTask(gid, status string) rpc.Task {
	return rpc.Task{
		GID:             gid,
		Status:          status,
		TotalLength:     "100000000",
		CompletedLength: "50000000",
		DownloadSpeed:   "1000000",
		Dir:             "D:\\Downloads",
		ErrorCode:       "",
		ErrorMessage:    "",
		Files: []rpc.File{
			{
				Path: "D:\\Downloads\\file-" + gid + ".zip",
				Uris: []rpc.Uri{
					{Uri: "https://example.com/file.zip", Status: "used"},
				},
			},
		},
	}
}

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

	// 任务被移除（不在任何列表中）
	tracker.Update(nil, nil, nil)

	// 内部 map 应该被清理
	if len(tracker.tasks) != 0 {
		t.Errorf("Expected 0 tracked tasks after removal, got %d", len(tracker.tasks))
	}
}

func TestTaskTracker_SpeedSampling(t *testing.T) {
	tracker := NewTaskTracker()

	// 创建大文件任务 (>50MB)
	task := createMockTask("gid-006", "active")
	task.TotalLength = "100000000"  // 100MB
	task.DownloadSpeed = "10000000" // 10MB/s

	active := []rpc.Task{task}
	tracker.Update(active, nil, nil)

	tracked := tracker.tasks["gid-006"]
	if tracked == nil {
		t.Fatal("Expected task to be tracked")
	}

	// 速度应该被记录
	if tracked.SustainedSpeed == 0 {
		t.Error("Expected sustained speed to be recorded")
	}
}

func TestTaskTracker_ThreadInfoTracking(t *testing.T) {
	tracker := NewTaskTracker()

	// 设置线程信息
	tracker.SetThreadInfo("gid-007", 16, true)

	threadCount, isExploration, ok := tracker.GetThreadInfo("gid-007")
	if !ok {
		t.Fatal("Expected thread info to be found")
	}

	if threadCount != 16 {
		t.Errorf("Expected thread count 16, got %d", threadCount)
	}

	if !isExploration {
		t.Error("Expected isExploration to be true")
	}
}

func TestTaskTracker_ConcurrentAccess(t *testing.T) {
	tracker := NewTaskTracker()

	// 并发测试
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(idx int) {
			gid := "gid-concurrent-" + string(rune('A'+idx))
			active := []rpc.Task{createMockTask(gid, "active")}
			tracker.Update(active, nil, nil)
			tracker.SetThreadInfo(gid, idx, false)
			tracker.GetThreadInfo(gid)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// 应该没有 panic
	t.Log("Concurrent access test passed")
}
