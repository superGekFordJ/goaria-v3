package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

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

func TestTaskTracker_UpdateThreadCount(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg_test_update"

	// 1. Initial split set
	tracker.SetThreadInfo(gid, 2, true)
	tc, isExp, ok := tracker.GetThreadInfo(gid)
	if !ok || tc != 2 || !isExp {
		t.Fatalf("expected 2, true, true; got %d, %v, %v", tc, isExp, ok)
	}

	// 2. UpdateLiveConnections updates live connections without mutating ThreadCount
	tracker.UpdateLiveConnections(gid, 6)
	tc, isExp, ok = tracker.GetThreadInfo(gid)
	if !ok || tc != 2 || !isExp {
		t.Fatalf("expected ThreadCount to remain 2, true, true; got %d, %v, %v", tc, isExp, ok)
	}
	if live := tracker.GetLiveConnections(gid); live != 6 {
		t.Fatalf("expected live connections 6; got %d", live)
	}

	// 3. Deprecated UpdateThreadCount delegates to UpdateLiveConnections and also preserves ThreadCount
	tracker.UpdateThreadCount(gid, 8)
	tc, isExp, ok = tracker.GetThreadInfo(gid)
	if !ok || tc != 2 || !isExp {
		t.Fatalf("expected ThreadCount to remain 2; got %d, %v, %v", tc, isExp, ok)
	}
	if live := tracker.GetLiveConnections(gid); live != 8 {
		t.Fatalf("expected live connections 8; got %d", live)
	}

	// 4. UpdateLiveConnections on unknown task creates it with LiveConnections, not ThreadCount
	tracker.UpdateLiveConnections("sg_unknown", 4)
	if live := tracker.GetLiveConnections("sg_unknown"); live != 4 {
		t.Fatalf("expected 4 live connections on unknown; got %d", live)
	}
	if tc, _, ok := tracker.GetThreadInfo("sg_unknown"); ok || tc != 0 {
		t.Fatalf("expected no ThreadCount on unknown task; got %d, %v", tc, ok)
	}

	// 5. Zero count updates live connections to 0 (worker drain) without touching ThreadCount
	tracker.UpdateLiveConnections(gid, 0)
	if live := tracker.GetLiveConnections(gid); live != 0 {
		t.Fatalf("expected live connections to update to 0 on drain; got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 2 {
		t.Fatalf("expected ThreadCount to remain 2 after drain to 0; got %d", tc)
	}

	// 6. Negative count is ignored
	tracker.UpdateLiveConnections(gid, -1)
	if live := tracker.GetLiveConnections(gid); live != 0 {
		t.Fatalf("expected negative count to be ignored, live remained 0; got %d", live)
	}

	// 7. Unknown task with connections=0 does not create a placeholder
	tracker.UpdateLiveConnections("sg_nonexistent_zero", 0)
	tracker.mu.RLock()
	_, exists := tracker.tasks["sg_nonexistent_zero"]
	tracker.mu.RUnlock()
	if exists {
		t.Fatal("expected connections=0 on unknown task to NOT create a TrackedTask placeholder")
	}
}

func TestTaskTracker_ThreadInfoPersistence(t *testing.T) {
	tracker := NewTaskTracker()

	// 1. 设置线程信息 (模拟 AddUri)
	gid := "gid-persistence-001"
	tracker.SetThreadInfo(gid, 8, true)

	// 2. 模拟 Update 在任务出现在列表前触发 (空列表)
	// 此时应该触发宽限期保护，任务不应被删除
	tracker.Update(nil, nil, nil)

	// 验证：任务仍在，且线程信息保留
	threadCount, isExploration, ok := tracker.GetThreadInfo(gid)
	if !ok {
		t.Fatal("Task info missing prematurely (race condition detected)")
	}
	if threadCount != 8 {
		t.Errorf("Thread info lost/corrupted, got %d, want 8", threadCount)
	}
	if !isExploration {
		t.Error("Exploration flag lost")
	}

	// 3. 模拟多次更新，仍在宽限期内
	time.Sleep(100 * time.Millisecond) // 稍微等一下
	tracker.Update(nil, nil, nil)
	if _, _, ok := tracker.GetThreadInfo(gid); !ok {
		t.Fatal("Task info missing within grace period")
	}

	// 4. 终于，任务出现在 Aria2 列表中
	active := []rpc.Task{createMockTask(gid, "active")}
	tracker.Update(active, nil, nil)

	// 验证：最终信息正确
	threadCount, _, ok = tracker.GetThreadInfo(gid)
	if !ok {
		t.Fatal("Task info missing after appearing in active list")
	}
	if threadCount != 8 {
		t.Errorf("Thread info overwritten/lost, got %d", threadCount)
	}
}
