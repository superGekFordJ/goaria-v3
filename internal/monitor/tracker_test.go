package monitor

import (
	"sync"
	"testing"
	"time"

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

// ==================== Lite Task Edge Cases ====================

// createLiteTask 创建 Lite 任务（无文件信息，模拟 TellActiveLite 返回）
func createLiteTask(gid, status string) rpc.Task {
	return rpc.Task{
		GID:             gid,
		Status:          status,
		TotalLength:     "100000000",
		CompletedLength: "50000000",
		DownloadSpeed:   "1000000",
		Dir:             "D:\\Downloads",
		Files:           nil, // Lite 任务无文件信息
	}
}

// createEnrichedTask 创建已丰富的任务（模拟 enrichTasks 后的结果）
func createEnrichedTask(gid, status string) rpc.Task {
	task := createLiteTask(gid, status)
	task.Files = []rpc.File{
		{
			Path: "D:\\Downloads\\file-" + gid + ".zip",
			Uris: []rpc.Uri{{Uri: "https://example.com/file.zip"}},
		},
	}
	return task
}

// TestTaskTracker_LiteTaskNoFiles 测试 Lite 任务（无文件）不会覆盖已有文件信息
func TestTaskTracker_LiteTaskNoFiles(t *testing.T) {
	tracker := NewTaskTracker()

	// 1. 首次更新：已丰富的任务（有文件信息）
	enriched := []rpc.Task{createEnrichedTask("gid-lite-001", "active")}
	tracker.Update(enriched, nil, nil)

	// 验证文件信息已设置
	tracked := tracker.tasks["gid-lite-001"]
	if tracked == nil {
		t.Fatal("Task should be tracked")
	}
	if tracked.FilePath == "" {
		t.Error("FilePath should be set from enriched task")
	}
	originalPath := tracked.FilePath

	// 2. 后续更新：Lite 任务（无文件信息）
	lite := []rpc.Task{createLiteTask("gid-lite-001", "active")}
	tracker.Update(lite, nil, nil)

	// 关键断言：FilePath 不应被空值覆盖
	if tracked.FilePath != originalPath {
		t.Errorf("FilePath should be preserved, got %q, want %q", tracked.FilePath, originalPath)
	}
}

// TestTaskTracker_LiteTaskLateEnrichment 测试 Lite 任务后期丰富文件信息
func TestTaskTracker_LiteTaskLateEnrichment(t *testing.T) {
	tracker := NewTaskTracker()

	// 1. 首次更新：Lite 任务（无文件信息，模拟首次 tick）
	lite := []rpc.Task{createLiteTask("gid-late-001", "active")}
	tracker.Update(lite, nil, nil)

	// 验证 FilePath 为空
	tracked := tracker.tasks["gid-late-001"]
	if tracked == nil {
		t.Fatal("Task should be tracked")
	}
	if tracked.FilePath != "" {
		t.Errorf("FilePath should be empty for Lite task, got %q", tracked.FilePath)
	}

	// 2. 后续更新：已丰富的任务（模拟 enrichTasks 后的 tick）
	enriched := []rpc.Task{createEnrichedTask("gid-late-001", "active")}
	tracker.Update(enriched, nil, nil)

	// 关键断言：FilePath 应该被更新
	if tracked.FilePath == "" {
		t.Error("FilePath should be set after enrichment")
	}
	if tracked.FilePath != "D:\\Downloads\\file-gid-late-001.zip" {
		t.Errorf("FilePath mismatch, got %q", tracked.FilePath)
	}
}

// TestTaskTracker_FastCompletingTask 测试快速完成的任务
func TestTaskTracker_FastCompletingTask(t *testing.T) {
	tracker := NewTaskTracker()

	// 场景：任务在首次 tick 时就已经完成（从未出现在 active 列表）
	// 直接以 complete 状态出现在 stopped 列表
	enrichedStopped := []rpc.Task{createEnrichedTask("gid-fast-001", "complete")}
	completed := tracker.Update(nil, nil, enrichedStopped)

	// 应该检测到新完成的任务
	if len(completed) != 1 {
		t.Fatalf("Expected 1 completed task, got %d", len(completed))
	}

	// 关键断言：即使是首次见到的已完成任务，也应有文件信息
	if completed[0].FilePath == "" {
		t.Error("FilePath should be set for fast-completing task")
	}
	if completed[0].Status != "complete" {
		t.Errorf("Status should be complete, got %q", completed[0].Status)
	}
}

// TestTaskTracker_CompletionWithLiteData 测试完成时使用 Lite 数据
func TestTaskTracker_CompletionWithLiteData(t *testing.T) {
	tracker := NewTaskTracker()

	// 1. 任务在 active 时有文件信息
	enrichedActive := []rpc.Task{createEnrichedTask("gid-comp-001", "active")}
	tracker.Update(enrichedActive, nil, nil)

	originalPath := tracker.tasks["gid-comp-001"].FilePath
	if originalPath == "" {
		t.Fatal("FilePath should be set during active phase")
	}

	// 2. 任务完成，但 stopped 列表是 Lite 数据（无文件）
	liteStopped := []rpc.Task{createLiteTask("gid-comp-001", "complete")}
	completed := tracker.Update(nil, nil, liteStopped)

	if len(completed) != 1 {
		t.Fatalf("Expected 1 completed task, got %d", len(completed))
	}

	// 关键断言：完成时应保留 active 阶段的文件信息
	if completed[0].FilePath != originalPath {
		t.Errorf("FilePath should be preserved from active phase, got %q, want %q",
			completed[0].FilePath, originalPath)
	}
}

// TestTaskTracker_EmptyPathNotOverwritten 测试空路径不覆盖有效路径
func TestTaskTracker_EmptyPathNotOverwritten(t *testing.T) {
	tracker := NewTaskTracker()

	// 1. 任务有文件信息
	task := createEnrichedTask("gid-empty-001", "active")
	tracker.Update([]rpc.Task{task}, nil, nil)

	tracked := tracker.tasks["gid-empty-001"]
	originalPath := tracked.FilePath

	// 2. 更新时传入空路径的文件（模拟某些边缘情况）
	taskWithEmptyPath := createLiteTask("gid-empty-001", "active")
	taskWithEmptyPath.Files = []rpc.File{{Path: ""}} // 有 Files 但 Path 为空
	tracker.Update([]rpc.Task{taskWithEmptyPath}, nil, nil)

	// 关键断言：空路径不应覆盖有效路径
	if tracked.FilePath != originalPath {
		t.Errorf("Empty path should not overwrite valid path, got %q, want %q",
			tracked.FilePath, originalPath)
	}
}

func TestTaskTracker_GroupPreservedAcrossLiteAndStoppedTransitions(t *testing.T) {
	tracker := NewTaskTracker()
	group := testDownloadGroup("dg-tracker")
	active := createEnrichedTask("gid-group", "active")
	active.DownloadGroup = &group

	tracker.Update([]rpc.Task{active}, nil, nil)
	tracked := tracker.tasks["gid-group"]
	if tracked == nil || tracked.DownloadGroup == nil || tracked.DownloadGroup.ID != group.ID {
		t.Fatalf("expected active group to be tracked, got %#v", tracked)
	}

	lite := createLiteTask("gid-group", "active")
	tracker.Update([]rpc.Task{lite}, nil, nil)
	if tracked.DownloadGroup == nil || tracked.DownloadGroup.ID != group.ID {
		t.Fatalf("expected lite task not to clear group, got %#v", tracked.DownloadGroup)
	}

	liteStopped := createLiteTask("gid-group", "complete")
	completed := tracker.Update(nil, nil, []rpc.Task{liteStopped})
	if len(completed) != 1 {
		t.Fatalf("expected one completed task, got %d", len(completed))
	}
	if completed[0].DownloadGroup == nil || completed[0].DownloadGroup.ID != group.ID {
		t.Fatalf("expected completed task to preserve group, got %#v", completed[0].DownloadGroup)
	}
}

func TestTaskTracker_CompletedTaskSnapshotDoesNotExposeInternalGroupPointer(t *testing.T) {
	tracker := NewTaskTracker()
	group := testDownloadGroup("dg-tracker-complete-snapshot")
	active := createEnrichedTask("gid-complete-snapshot", "active")
	active.DownloadGroup = &group
	tracker.Update([]rpc.Task{active}, nil, nil)

	completed := tracker.Update(nil, nil, []rpc.Task{createLiteTask("gid-complete-snapshot", "complete")})
	if len(completed) != 1 || completed[0].DownloadGroup == nil {
		t.Fatalf("expected completed snapshot with group, got %#v", completed)
	}
	snapshotGroup := completed[0].DownloadGroup
	internalGroup := tracker.tasks["gid-complete-snapshot"].DownloadGroup
	if snapshotGroup == internalGroup {
		t.Fatal("expected completed task to carry defensive group copy, not tracker internal pointer")
	}

	tracker.UpdateTaskGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)
	if snapshotGroup.Name == "Project Alpha" || snapshotGroup.NameStatus == rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected completed snapshot group not to be mutated by later tracker updates, got %#v", snapshotGroup)
	}
	if got := tracker.GetTaskGroup("gid-complete-snapshot"); got == nil || got.Name != "Project Alpha" || got.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected internal tracker group updated, got %#v", got)
	}
}

func TestTaskTracker_CompletedTaskSnapshotSafeWhileGroupNameUpdates(t *testing.T) {
	tracker := NewTaskTracker()
	group := testDownloadGroup("dg-tracker-complete-race")
	active := createEnrichedTask("gid-complete-race", "active")
	active.DownloadGroup = &group
	tracker.Update([]rpc.Task{active}, nil, nil)
	completed := tracker.Update(nil, nil, []rpc.Task{createLiteTask("gid-complete-race", "complete")})
	if len(completed) != 1 || completed[0].DownloadGroup == nil {
		t.Fatalf("expected completed snapshot with group, got %#v", completed)
	}
	snapshot := completed[0]

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = snapshot.DownloadGroup.Name
			_ = snapshot.DownloadGroup.NameStatus
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			tracker.UpdateTaskGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)
			tracker.UpdateTaskGroupName(group.ID, group.Name, rpc.DownloadGroupNameStatusFallback)
		}
	}()
	wg.Wait()
}

func TestTaskTracker_SetTaskGroupBeforeTaskAppears(t *testing.T) {
	tracker := NewTaskTracker()
	group := testDownloadGroup("dg-tracker-placeholder")
	tracker.SetTaskGroup("gid-placeholder", group)

	if got := tracker.GetTaskGroup("gid-placeholder"); got == nil || got.ID != group.ID {
		t.Fatalf("expected placeholder group, got %#v", got)
	}

	active := createLiteTask("gid-placeholder", "active")
	tracker.Update([]rpc.Task{active}, nil, nil)
	if got := tracker.GetTaskGroup("gid-placeholder"); got == nil || got.ID != group.ID {
		t.Fatalf("expected group to survive lite update, got %#v", got)
	}
}

func TestTaskTracker_UpdateTaskGroupNamePreservesFolderMetadata(t *testing.T) {
	tracker := NewTaskTracker()
	group := testDownloadGroup("dg-tracker-name")
	tracker.SetTaskGroup("gid-name", group)

	changed := tracker.UpdateTaskGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)

	if changed != 1 {
		t.Fatalf("expected one changed tracked group, got %d", changed)
	}
	got := tracker.GetTaskGroup("gid-name")
	if got == nil || got.Name != "Project Alpha" || got.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected updated tracked group, got %#v", got)
	}
	if got.FolderName != group.FolderName || got.Dir != group.Dir {
		t.Fatalf("expected folder metadata preserved, got %#v want folder=%q dir=%q", got, group.FolderName, group.Dir)
	}
}

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

func TestTaskTracker_SetScope_ExistingTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-009", 100000000, "https://example.com/file.zip", 8, "active")

	tracker.SetScope("sg-evt-009", "wan", 150, "example.com")

	tracked := tracker.tasks["sg-evt-009"]
	if tracked.Scope != "wan" {
		t.Errorf("Scope = %s, want wan", tracked.Scope)
	}
	if tracked.TTFBMs != 150 {
		t.Errorf("TTFBMs = %d, want 150", tracked.TTFBMs)
	}
	if tracked.Domain != "example.com" {
		t.Errorf("Domain = %s, want example.com", tracked.Domain)
	}
}

func TestTaskTracker_SetScope_NewTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScope("sg-evt-010", "lan", 50, "nas.local")

	tracked := tracker.tasks["sg-evt-010"]
	if tracked == nil {
		t.Fatal("Expected task to be created by SetScope")
	}
	if tracked.Scope != "lan" {
		t.Errorf("Scope = %s, want lan", tracked.Scope)
	}
	if tracked.TTFBMs != 50 {
		t.Errorf("TTFBMs = %d, want 50", tracked.TTFBMs)
	}
	if tracked.Domain != "nas.local" {
		t.Errorf("Domain = %s, want nas.local", tracked.Domain)
	}
}

func TestTaskTracker_SetMinChunk_ExistingTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg-minchunk-1", 4, false)

	minChunk := int64(4 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-1", minChunk)

	tracked := tracker.tasks["sg-minchunk-1"]
	if tracked == nil {
		t.Fatal("expected tracked task to exist")
	}
	if tracked.MinChunk != minChunk {
		t.Errorf("MinChunk = %d, want %d", tracked.MinChunk, minChunk)
	}
}

func TestTaskTracker_SetMinChunk_NewTask(t *testing.T) {
	tracker := NewTaskTracker()
	minChunk := int64(2 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-2", minChunk)

	tracked := tracker.tasks["sg-minchunk-2"]
	if tracked == nil {
		t.Fatal("Expected task to be created by SetMinChunk")
	}
	if tracked.MinChunk != minChunk {
		t.Errorf("MinChunk = %d, want %d", tracked.MinChunk, minChunk)
	}
}

// 第二次调用应覆盖第一次设置的值，防止未来重构丢失无条件覆盖语义。
func TestTaskTracker_SetMinChunk_OverwritesExistingValue(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg-minchunk-ow", 4, false)

	first := int64(4 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-ow", first)
	if tracked := tracker.tasks["sg-minchunk-ow"]; tracked == nil || tracked.MinChunk != first {
		t.Fatalf("first SetMinChunk: got MinChunk %v, want %d", tracked, first)
	}

	second := int64(16 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-ow", second)
	tracked := tracker.tasks["sg-minchunk-ow"]
	if tracked == nil {
		t.Fatal("expected tracked task to exist after second SetMinChunk")
	}
	if tracked.MinChunk != second {
		t.Errorf("MinChunk = %d, want %d (second call should overwrite first)", tracked.MinChunk, second)
	}
}

func TestTrackerAdapter_GetActiveTrackedTasks_PassesMinChunkThrough(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-minchunk-pass", 0, "", 4, "active")

	minChunk := int64(8 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-pass", minChunk)

	adapter := &trackerAdapter{TaskTracker: tracker}
	infos := adapter.GetActiveTrackedTasks()
	if len(infos) != 1 {
		t.Fatalf("got %d active tracked tasks, want 1", len(infos))
	}
	if infos[0].GID != "sg-minchunk-pass" {
		t.Errorf("GID = %s, want sg-minchunk-pass", infos[0].GID)
	}
	if infos[0].MinChunk != minChunk {
		t.Errorf("MinChunk = %d, want %d", infos[0].MinChunk, minChunk)
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

func TestTaskTracker_GetScope(t *testing.T) {
	tracker := NewTaskTracker()

	// Before SetScope, GetScope returns ok=false
	_, _, ok := tracker.GetScope("gid-scope-001")
	if ok {
		t.Error("Expected ok=false before SetScope")
	}

	tracker.SetScope("gid-scope-001", "wan", 120, "example.com")

	scope, domain, ok := tracker.GetScope("gid-scope-001")
	if !ok {
		t.Fatal("Expected ok=true after SetScope")
	}
	if scope != "wan" {
		t.Errorf("scope = %s, want wan", scope)
	}
	if domain != "example.com" {
		t.Errorf("domain = %s, want example.com", domain)
	}

	// Unknown gid
	_, _, ok = tracker.GetScope("nonexistent")
	if ok {
		t.Error("Expected ok=false for nonexistent gid")
	}
}

func TestTaskTracker_GetScope_EmptyScope(t *testing.T) {
	tracker := NewTaskTracker()

	// SetThreadInfo creates a tracked task with empty Scope
	tracker.SetThreadInfo("gid-empty-scope", 4, false)

	_, _, ok := tracker.GetScope("gid-empty-scope")
	if ok {
		t.Error("Expected ok=false when Scope is empty")
	}
}

// ==================== RecordPeakEfficiency Tests====================

func TestRecordPeakEfficiency_FirstRecording(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-001", 100000000, "https://example.com/file.zip", 8, "active")

	tracker.RecordPeakEfficiency("sg-peak-001", 50*1024*1024, 10)

	tracked := tracker.tasks["sg-peak-001"]
	if tracked.PeakSpeed != 50*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d", tracked.PeakSpeed, 50*1024*1024)
	}
	if tracked.PeakThreadCount != 10 {
		t.Errorf("PeakThreadCount = %d, want 10", tracked.PeakThreadCount)
	}
}

func TestRecordPeakEfficiency_HigherThroughputSameEfficiency(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-002", 100000000, "https://example.com/file.zip", 8, "active")

	// First record: 50MB/s @ 10 workers = 5MB/s/thread
	tracker.RecordPeakEfficiency("sg-peak-002", 50*1024*1024, 10)
	// Second record: 60MB/s @ 12 workers = 5MB/s/thread (same eff, higher throughput)
	tracker.RecordPeakEfficiency("sg-peak-002", 60*1024*1024, 12)

	tracked := tracker.tasks["sg-peak-002"]
	if tracked.PeakSpeed != 60*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (higher throughput)", tracked.PeakSpeed, 60*1024*1024)
	}
	if tracked.PeakThreadCount != 12 {
		t.Errorf("PeakThreadCount = %d, want 12 (accepted same-eff higher throughput)", tracked.PeakThreadCount)
	}
}

func TestRecordPeakEfficiency_SameThroughputFewerWorkers(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-003", 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv("sg-peak-003", "wan", 50, "example.com", "envA")

	// First: 50MB/s @ 10 workers
	tracker.RecordPeakEfficiency("sg-peak-003", 50*1024*1024, 10)
	if tracker.tasks["sg-peak-003"].PeakEnvKey != "envA" {
		t.Fatalf("setup PeakEnvKey = %q, want envA", tracker.tasks["sg-peak-003"].PeakEnvKey)
	}

	tracker.mu.Lock()
	tracker.tasks["sg-peak-003"].CurrentEnvKey = "envB"
	tracker.mu.Unlock()

	// Second: 50MB/s @ 8 workers (same throughput, fewer workers, higher eff).
	// ThreadCount-only accept — PeakSpeed unchanged → PeakEnvKey must stay envA.
	tracker.RecordPeakEfficiency("sg-peak-003", 50*1024*1024, 8)

	tracked := tracker.tasks["sg-peak-003"]
	if tracked.PeakThreadCount != 8 {
		t.Errorf("PeakThreadCount = %d, want 8 (fewer workers at same throughput)", tracked.PeakThreadCount)
	}
	if tracked.PeakSpeed != 50*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (unchanged on ThreadCount-only accept)", tracked.PeakSpeed, 50*1024*1024)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA (ThreadCount-only must not refresh)", tracked.PeakEnvKey)
	}
}

// TestRecordPeakEfficiency_RejectBloatedN is the critical regression test:
// 50MB/s@10 → 53MB/s@32 should NOT overwrite peakWorkers (efficiency crashes from 5MB/s to 1.66MB/s)
func TestRecordPeakEfficiency_RejectBloatedN(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-004", 100000000, "https://example.com/file.zip", 8, "active")

	// Record efficient working point: 50MB/s @ 10 workers = 5MB/s/thread
	tracker.RecordPeakEfficiency("sg-peak-004", 50*1024*1024, 10)

	// Attempt bloated overwrite: 53MB/s @ 32 workers = 1.66MB/s/thread (eff crashes -67%)
	tracker.RecordPeakEfficiency("sg-peak-004", 53*1024*1024, 32)

	tracked := tracker.tasks["sg-peak-004"]
	// PeakSpeed should update (53 > 50, absolute throughput signal for V_target)
	if tracked.PeakSpeed != 53*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (absolute throughput should update)", tracked.PeakSpeed, 53*1024*1024)
	}
	// PeakThreadCount must NOT change — efficiency crashed, reject bloated N
	if tracked.PeakThreadCount != 10 {
		t.Errorf("PeakThreadCount = %d, want 10 (reject bloated N — efficiency guard)", tracked.PeakThreadCount)
	}
}

func TestRecordPeakEfficiency_ZeroWorkers(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-005", 100000000, "https://example.com/file.zip", 8, "active")

	tracker.RecordPeakEfficiency("sg-peak-005", 100*1024*1024, 0)

	tracked := tracker.tasks["sg-peak-005"]
	if tracked.PeakThreadCount != 0 {
		t.Errorf("PeakThreadCount = %d, want 0 (zero workers should be rejected)", tracked.PeakThreadCount)
	}
}

func TestRecordPeakEfficiency_NonexistentTask(t *testing.T) {
	tracker := NewTaskTracker()
	// Should not panic
	tracker.RecordPeakEfficiency("nonexistent", 100*1024*1024, 10)
}

func TestRecordPeakEfficiency_ConcurrentRace(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-race", 100000000, "https://example.com/file.zip", 8, "active")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			speed := int64(50*1024*1024 + idx*1024*1024)
			workers := 10 + idx%5
			tracker.RecordPeakEfficiency("sg-peak-race", speed, workers)
		}(i)
	}
	wg.Wait()
	// Should not panic or race
	tracked := tracker.tasks["sg-peak-race"]
	if tracked.PeakThreadCount == 0 {
		t.Error("Expected PeakThreadCount to be set after concurrent writes")
	}
}

// TestRecordPeakEfficiency_M6_BestEffCreepPrevention verifies that the bestEff-anchored
// guard (C2 fix) prevents N creep via repeated borderline adoptions. With the old curEff
// anchoring, each marginal adoption lowered curEff, allowing progressively worse
// efficiency to pass. With bestEff anchoring, the guard is always referenced to the
// session-best efficiency, so borderline-high-N proposals are rejected.
func TestRecordPeakEfficiency_M6_BestEffCreepPrevention(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-creep", 100000000, "https://example.com/file.zip", 8, "active")

	// Record efficient working point: 50MB/s @ 10 workers = 5MB/s/thread
	tracker.RecordPeakEfficiency("sg-peak-creep", 50*1024*1024, 10)

	// Attempt borderline adoption: 56MB/s @ 16 workers = 3.5MB/s/thread
	// Throughput up 12% (>5% noise gate), but eff=3.5 < guardEff=4.25 (5*0.85)
	// → PeakSpeed updates, PeakThreadCount stays 10
	tracker.RecordPeakEfficiency("sg-peak-creep", 56*1024*1024, 16)

	tracked := tracker.tasks["sg-peak-creep"]
	if tracked.PeakThreadCount != 10 {
		t.Errorf("PeakThreadCount = %d, want 10 (bestEff guard rejects eff=3.5 < guardEff=4.25)", tracked.PeakThreadCount)
	}
	if tracked.PeakSpeed != 56*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (absolute throughput should still update)", tracked.PeakSpeed, 56*1024*1024)
	}

	// Second borderline attempt: 62MB/s @ 20 workers = 3.1MB/s/thread
	// With old curEff anchoring: curEff would be 3.5 (from previous rejected record's
	// PeakSpeed/PeakThreadCount = 56/16=3.5), guardEff=3.5*0.85=2.975, 3.1>2.975 → accepted!
	// With bestEff anchoring: guardEff=5*0.85=4.25, 3.1<4.25 → rejected.
	tracker.RecordPeakEfficiency("sg-peak-creep", 62*1024*1024, 20)

	tracked = tracker.tasks["sg-peak-creep"]
	if tracked.PeakThreadCount != 10 {
		t.Errorf("PeakThreadCount = %d, want 10 (bestEff guard prevents N creep on 2nd attempt)", tracked.PeakThreadCount)
	}
	if tracked.PeakSpeed != 62*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (absolute throughput should update)", tracked.PeakSpeed, 62*1024*1024)
	}

	// Verify BestEff is anchored to the session best (5MB/s/thread)
	if tracked.BestEff != 5*1024*1024 {
		t.Errorf("BestEff = %d, want %d (should be session best efficiency)", tracked.BestEff, 5*1024*1024)
	}
}

// TestRecordPeakEfficiency_RejectSewageMonster is the critical Bug 2 regression test:
// Record [32MB/s @ 32 threads] (eff=1MB/s/thread), then attempt [4MB/s @ 4 threads]
// (same eff=1MB/s/thread). The old code would adopt the smaller peakWorkers (4 < 32)
// while keeping the old PeakSpeed (32MB/s), creating a physically impossible
// "缝合怪" record: [32MB/s peak, 4 workers]. The new peakSpeedGuardBand (0.90)
// requires incoming speed ≥ 90% of peak (28.8MB/s) — 4MB/s is way below, so rejected.
func TestRecordPeakEfficiency_RejectSewageMonster(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-sewage", 100000000, "https://example.com/file.zip", 32, "active")

	// Record: 32MB/s @ 32 threads = 1MB/s/thread
	tracker.RecordPeakEfficiency("sg-peak-sewage", 32*1024*1024, 32)

	tracked := tracker.tasks["sg-peak-sewage"]
	if tracked.PeakSpeed != 32*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want %d (initial record)", tracked.PeakSpeed, 32*1024*1024)
	}
	if tracked.PeakThreadCount != 32 {
		t.Fatalf("PeakThreadCount = %d, want 32 (initial record)", tracked.PeakThreadCount)
	}

	// Attempt "缝合怪" adoption: 4MB/s @ 4 threads = 1MB/s/thread (same efficiency)
	// But speed is only 12.5% of peak — far below 90% guard.
	tracker.RecordPeakEfficiency("sg-peak-sewage", 4*1024*1024, 4)

	tracked = tracker.tasks["sg-peak-sewage"]
	// PeakThreadCount must NOT change — speed guard rejects fraction-of-peak speed
	if tracked.PeakThreadCount != 32 {
		t.Errorf("PeakThreadCount = %d, want 32 (peakSpeedGuardBand rejects 4MB/s < 32*0.9=28.8MB/s)",
			tracked.PeakThreadCount)
	}
	// PeakSpeed should also NOT change (4 < 32, no update)
	if tracked.PeakSpeed != 32*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (should not decrease)", tracked.PeakSpeed, 32*1024*1024)
	}
}

func TestRecordPeakEfficiency_WritesPeakEnvKeyOnFirstWrite(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg-peak-env-first"
	tracker.EnsureTrackedFromEvent(gid, 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracker.RecordPeakEfficiency(gid, 50*1024*1024, 10)

	tracked := tracker.tasks[gid]
	if tracked.PeakSpeed != 50*1024*1024 || tracked.PeakThreadCount != 10 {
		t.Fatalf("PeakSpeed=%d PeakThreadCount=%d", tracked.PeakSpeed, tracked.PeakThreadCount)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA on first PeakSpeed accept", tracked.PeakEnvKey)
	}
}

func TestRecordPeakEfficiency_PeakEnvKeyFollowsCurrentOnNewPeak(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg-peak-env-follow"
	tracker.EnsureTrackedFromEvent(gid, 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracker.RecordPeakEfficiency(gid, 50*1024*1024, 10)
	if tracker.tasks[gid].PeakEnvKey != "envA" {
		t.Fatalf("setup PeakEnvKey = %q, want envA", tracker.tasks[gid].PeakEnvKey)
	}

	tracker.mu.Lock()
	tracker.tasks[gid].CurrentEnvKey = "envB"
	tracker.mu.Unlock()

	// Higher throughput same-ish efficiency → accept PeakSpeed, refresh PeakEnvKey
	tracker.RecordPeakEfficiency(gid, 60*1024*1024, 12)

	tracked := tracker.tasks[gid]
	if tracked.PeakSpeed != 60*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want %d", tracked.PeakSpeed, 60*1024*1024)
	}
	if tracked.PeakEnvKey != "envB" {
		t.Errorf("PeakEnvKey = %q, want envB after mid-download env change", tracked.PeakEnvKey)
	}
}

func TestRecordPeakEfficiency_AbsoluteThroughputOnlyWritesPeakEnvKey(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg-peak-env-abs"
	tracker.EnsureTrackedFromEvent(gid, 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracker.RecordPeakEfficiency(gid, 50*1024*1024, 10)

	tracker.mu.Lock()
	tracker.tasks[gid].CurrentEnvKey = "envB"
	tracker.mu.Unlock()

	// Bloated N: PeakSpeed rises, PeakThreadCount stays — still accepts PeakSpeed
	tracker.RecordPeakEfficiency(gid, 53*1024*1024, 32)

	tracked := tracker.tasks[gid]
	if tracked.PeakSpeed != 53*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want %d", tracked.PeakSpeed, 53*1024*1024)
	}
	if tracked.PeakThreadCount != 10 {
		t.Fatalf("PeakThreadCount = %d, want 10", tracked.PeakThreadCount)
	}
	if tracked.PeakEnvKey != "envB" {
		t.Errorf("PeakEnvKey = %q, want envB on absolute-throughput-only PeakSpeed accept", tracked.PeakEnvKey)
	}
}

func TestRecordPeakEfficiency_EmptyCurrentEnvKeyDoesNotWipePeakEnvKey(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg-peak-env-empty"
	tracker.EnsureTrackedFromEvent(gid, 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracker.RecordPeakEfficiency(gid, 50*1024*1024, 10)
	if tracker.tasks[gid].PeakEnvKey != "envA" {
		t.Fatalf("setup PeakEnvKey = %q, want envA", tracker.tasks[gid].PeakEnvKey)
	}

	tracker.mu.Lock()
	tracker.tasks[gid].CurrentEnvKey = ""
	tracker.mu.Unlock()

	tracker.RecordPeakEfficiency(gid, 60*1024*1024, 12)

	tracked := tracker.tasks[gid]
	if tracked.PeakSpeed != 60*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want %d", tracked.PeakSpeed, 60*1024*1024)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA (empty CurrentEnvKey must not wipe)", tracked.PeakEnvKey)
	}
}

// TestTaskTracker_PeakEnvKeyAttribution verifies that PeakEnvKey is set to the
// CurrentEnvKey at the time PeakSpeed is achieved, and does NOT change when
// CurrentEnvKey later changes (if the new speed doesn't exceed the peak).
func TestTaskTracker_PeakEnvKeyAttribution(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	tracker := NewTaskTracker()

	// 1. Create task and set env=envA
	gid := "sg-peak-env-attribution"
	tracker.SetThreadInfo(gid, 8, false)
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected task to be tracked")
	}
	// Set CompletedLength > MinFileSize (50MB) so sampleSpeedInternal updates PeakSpeed.
	tracked.TotalLength = 100 * 1024 * 1024
	tracked.CompletedLength = 60 * 1024 * 1024

	// 2. Simulate speed sampling that achieves a new peak in envA.
	//    sampleSpeedInternal with threshold=1 (headless) updates PeakSpeed+PeakEnvKey.
	tracker.sampleSpeedInternal(tracked, 10*1024*1024, 1)

	if tracked.PeakSpeed != 10*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want 10MB", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Fatalf("PeakEnvKey = %q, want envA (should match CurrentEnvKey at peak time)", tracked.PeakEnvKey)
	}

	// 3. Network changes → CurrentEnvKey switches to envB.
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envB")
	if tracked.CurrentEnvKey != "envB" {
		t.Fatalf("CurrentEnvKey = %q, want envB", tracked.CurrentEnvKey)
	}

	// 4. Simulate speed sampling that does NOT exceed the peak.
	//    PeakEnvKey must remain envA (peak attribution is immutable unless exceeded).
	tracker.sampleSpeedInternal(tracked, 5*1024*1024, 1)

	if tracked.PeakSpeed != 10*1024*1024 {
		t.Errorf("PeakSpeed = %d, want 10MB (unchanged)", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA (peak attribution must not change when speed doesn't exceed peak)", tracked.PeakEnvKey)
	}

	// 5. Speed now exceeds peak in envB → PeakEnvKey should update to envB.
	tracker.sampleSpeedInternal(tracked, 20*1024*1024, 1)

	if tracked.PeakSpeed != 20*1024*1024 {
		t.Errorf("PeakSpeed = %d, want 20MB (new peak)", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envB" {
		t.Errorf("PeakEnvKey = %q, want envB (should update to current env when peak exceeded)", tracked.PeakEnvKey)
	}
}

// TestSampleSpeedInternal_EmptyCurrentEnvKeyDoesNotWipePeakEnvKey verifies Aria2
// peak accept via acceptPeakSpeed does not clear PeakEnvKey when Current is empty.
func TestSampleSpeedInternal_EmptyCurrentEnvKeyDoesNotWipePeakEnvKey(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	tracker := NewTaskTracker()
	gid := "sg-sample-empty-current"
	tracker.SetThreadInfo(gid, 8, false)
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected task to be tracked")
	}
	tracked.TotalLength = 100 * 1024 * 1024
	tracked.CompletedLength = 60 * 1024 * 1024
	tracked.CurrentEnvKey = ""

	const newPeak = int64(15 * 1024 * 1024)
	tracker.sampleSpeedInternal(tracked, newPeak, 1)

	if tracked.PeakSpeed != newPeak {
		t.Errorf("PeakSpeed = %d, want %d", tracked.PeakSpeed, newPeak)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA (empty Current must not wipe)", tracked.PeakEnvKey)
	}
}

func TestSetScopeAndEnv_ZeroTTFB_PreservesExistingTTFB(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-001", "wan", 120, "example.com", "envA")

	tracker.SetScopeAndEnv("sg-ttfb-001", "wan", 0, "example.com", "envB")

	tracked := tracker.tasks["sg-ttfb-001"]
	if tracked == nil {
		t.Fatal("expected tracked task to exist")
	}
	if tracked.TTFBMs != 120 {
		t.Errorf("TTFBMs = %d, want 120 (zero ttfbMs must not overwrite existing probe)", tracked.TTFBMs)
	}
	if tracked.CurrentEnvKey != "envB" {
		t.Errorf("CurrentEnvKey = %q, want envB (envKey should still update)", tracked.CurrentEnvKey)
	}
}

func TestSetScopeAndEnv_NegativeTTFB_PreservesExistingTTFB(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-002", "wan", 120, "example.com", "envA")

	tracker.SetScopeAndEnv("sg-ttfb-002", "wan", -1, "example.com", "envB")

	tracked := tracker.tasks["sg-ttfb-002"]
	if tracked.TTFBMs != 120 {
		t.Errorf("TTFBMs = %d, want 120 (negative ttfbMs must not overwrite existing probe)", tracked.TTFBMs)
	}
}

func TestSetScopeAndEnv_PositiveTTFB_OverwritesExisting(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-003", "wan", 120, "example.com", "envA")

	tracker.SetScopeAndEnv("sg-ttfb-003", "wan", 200, "example.com", "envB")

	tracked := tracker.tasks["sg-ttfb-003"]
	if tracked.TTFBMs != 200 {
		t.Errorf("TTFBMs = %d, want 200 (positive ttfbMs should overwrite)", tracked.TTFBMs)
	}
}

func TestSetScopeAndEnv_NewTask_ZeroTTFB_AcceptsZero(t *testing.T) {
	tracker := NewTaskTracker()

	tracker.SetScopeAndEnv("sg-ttfb-004", "wan", 0, "example.com", "envA")

	tracked := tracker.tasks["sg-ttfb-004"]
	if tracked == nil {
		t.Fatal("expected new tracked task to be created")
	}
	if tracked.TTFBMs != 0 {
		t.Errorf("TTFBMs = %d, want 0 (new task with no probe should start at 0)", tracked.TTFBMs)
	}
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

func TestSetTTFB_Positive_OverwritesZero(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-set-1", "wan", 0, "example.com", "envA")

	tracker.SetTTFB("sg-ttfb-set-1", 80)

	tracked := tracker.tasks["sg-ttfb-set-1"]
	if tracked == nil {
		t.Fatal("expected tracked task to exist")
	}
	if tracked.TTFBMs != 80 {
		t.Errorf("TTFBMs = %d, want 80", tracked.TTFBMs)
	}
	if tracked.Scope != "wan" || tracked.Domain != "example.com" || tracked.CurrentEnvKey != "envA" {
		t.Errorf("scope/domain/envKey overwritten: scope=%q domain=%q envKey=%q", tracked.Scope, tracked.Domain, tracked.CurrentEnvKey)
	}
}

func TestSetTTFB_Zero_DoesNotOverwrite(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-set-2", "wan", 100, "example.com", "envA")

	tracker.SetTTFB("sg-ttfb-set-2", 0)

	tracked := tracker.tasks["sg-ttfb-set-2"]
	if tracked.TTFBMs != 100 {
		t.Errorf("TTFBMs = %d, want 100 (zero must not overwrite)", tracked.TTFBMs)
	}
}

func TestSetTTFB_NonExistentTask_SilentSkip(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetTTFB("nonexistent", 80)
}

func TestSetTTFB_DoesNotOverwriteScopeDomainEnvKey(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-set-3", "wan", 0, "example.com", "envA")

	tracker.SetTTFB("sg-ttfb-set-3", 80)

	tracked := tracker.tasks["sg-ttfb-set-3"]
	if tracked.Scope != "wan" {
		t.Errorf("Scope = %q, want wan", tracked.Scope)
	}
	if tracked.Domain != "example.com" {
		t.Errorf("Domain = %q, want example.com", tracked.Domain)
	}
	if tracked.CurrentEnvKey != "envA" {
		t.Errorf("CurrentEnvKey = %q, want envA", tracked.CurrentEnvKey)
	}
}

// ==================== Status Desync Regression Tests ====================

// activeSetContains reports whether gid is present in GetActiveTrackedTasks.
func activeSetContains(tracker *TaskTracker, gid string) bool {
	for _, tt := range tracker.GetActiveTrackedTasks() {
		if tt.GID == gid {
			return true
		}
	}
	return false
}

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

func TestSetTargetBandwidth_PersistsAllocatedAtAndOccupancy(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg_occ_1", 8, false)
	createdAt := tracker.tasks["sg_occ_1"].CreatedAt

	time.Sleep(2 * time.Millisecond)
	tracker.SetTargetBandwidth("sg_occ_1", 5_000_000)

	tt := tracker.tasks["sg_occ_1"]
	if tt.TargetBandwidth != 5_000_000 {
		t.Fatalf("TargetBandwidth = %d, want 5000000", tt.TargetBandwidth)
	}
	if tt.AllocatedAt.IsZero() {
		t.Fatal("AllocatedAt should be set")
	}
	if !tt.CreatedAt.Equal(createdAt) {
		t.Error("SetTargetBandwidth must not refresh CreatedAt")
	}
	if tt.Status != "active" {
		t.Errorf("Status = %q, want active (empty → active on bw>0)", tt.Status)
	}

	occ := tracker.GetOccupancyTrackedTasks()
	found := false
	for _, o := range occ {
		if o.GID == "sg_occ_1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("placeholder with TargetBandwidth should appear in GetOccupancyTrackedTasks")
	}
}

// TestGetOccupancyTrackedTasks_ExcludesPausedWaitingComplete covers occupancy
// inclusion/exclusion after SPEC-247: waiting with TargetBandwidth>0 is
// intentionally included (reverses SPEC-242's blanket waiting exclusion).
// Waiting with bw==0, paused-without-hold, complete, and empty-without-bw
// remain excluded. GetActiveTrackedTasks stays active-only.
func TestGetOccupancyTrackedTasks_ExcludesPausedWaitingComplete(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg_active", 4, false)
	tracker.SetTargetBandwidth("sg_active", 1_000_000)

	tracker.SetThreadInfo("sg_paused", 4, false)
	tracker.SetTargetBandwidth("sg_paused", 1_000_000)
	tracker.SetStatusFromEvent("sg_paused", "paused")

	tracker.SetThreadInfo("sg_waiting", 4, false)
	tracker.EnsureTrackedFromEvent("sg_waiting", 0, "https://x", 4, "waiting")
	tracker.tasks["sg_waiting"].TargetBandwidth = 1_000_000

	tracker.SetThreadInfo("sg_waiting0", 2, false)
	tracker.EnsureTrackedFromEvent("sg_waiting0", 0, "https://x", 2, "waiting")
	// TargetBandwidth left at 0 — still excluded.

	tracker.EnsureTrackedFromEvent("sg_done", 100, "https://x", 4, "active")
	tracker.SetTargetBandwidth("sg_done", 1_000_000)
	_ = tracker.MarkCompleteFromEvent("sg_done", "complete")

	// Empty status without TargetBandwidth must be excluded.
	tracker.SetThreadInfo("sg_empty", 4, false)

	occ := tracker.GetOccupancyTrackedTasks()
	gids := map[string]bool{}
	for _, o := range occ {
		gids[o.GID] = true
	}
	if !gids["sg_active"] {
		t.Error("active occupancy missing")
	}
	if !gids["sg_waiting"] {
		t.Error("waiting with TargetBandwidth>0 must seed occupancy (SPEC-247)")
	}
	if gids["sg_paused"] || gids["sg_waiting0"] || gids["sg_done"] || gids["sg_empty"] {
		t.Errorf("unexpected occupancy gids: %v", gids)
	}
	if activeSetContains(tracker, "sg_waiting") {
		t.Error("waiting claim must not widen GetActiveTrackedTasks")
	}
}

func TestGetOccupancyTrackedTasks_WaitingClaimReleasedOnRemove(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_q", 0, "https://a.com/f", 9, "waiting")
	tracker.SetScopeAndEnv("sg_q", "wan", 0, "a.com", "env1")
	tracker.SetThreadInfo("sg_q", 9, false)
	tracker.SetTargetBandwidth("sg_q", 5_000_000)

	occ := tracker.GetOccupancyTrackedTasks()
	found := false
	for _, o := range occ {
		if o.GID == "sg_q" && o.TargetBandwidth == 5_000_000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("waiting+bw claim missing from occupancy: %#v", occ)
	}

	tracker.RemoveTask("sg_q")
	for _, o := range tracker.GetOccupancyTrackedTasks() {
		if o.GID == "sg_q" {
			t.Fatal("RemoveTask must drop waiting claim from occupancy")
		}
	}
}

func TestGetOccupancyTrackedTasks_IncludesEmptyStatusPlaceholder(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.tasks["sg_ph"] = &TrackedTask{
		GID:             "sg_ph",
		TargetBandwidth: 2_000_000,
		Status:          "",
	}
	occ := tracker.GetOccupancyTrackedTasks()
	if len(occ) != 1 || occ[0].GID != "sg_ph" {
		t.Fatalf("empty-status placeholder with TargetBandwidth should be included, got %#v", occ)
	}
}

func TestSetTargetBandwidth_ResumeHoldWhilePaused(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_p", 100, "https://x", 4, "active")
	tracker.SetStatusFromEvent("sg_p", "paused")
	tracker.SetTargetBandwidth("sg_p", 2_000_000)
	if tracker.tasks["sg_p"].Status != "paused" {
		t.Errorf("Status = %q, want paused (must not override non-empty)", tracker.tasks["sg_p"].Status)
	}
	if !tracker.tasks["sg_p"].resumeOccupancyHold {
		t.Error("expected resumeOccupancyHold after SetTargetBandwidth while paused")
	}
}

func TestGetOccupancyTrackedTasks_ResumeHoldWhilePaused(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_r1", 100, "https://x", 4, "active")
	tracker.SetScopeAndEnv("sg_r1", "wan", 0, "a.com", "env1")
	tracker.SetStatusFromEvent("sg_r1", "paused")

	// Long-paused without hold must stay excluded.
	tracker.tasks["sg_r1"].TargetBandwidth = 5_000_000
	tracker.tasks["sg_r1"].AllocatedAt = time.Now().Add(-time.Hour)
	if len(tracker.GetOccupancyTrackedTasks()) != 0 {
		t.Fatal("long-paused without hold must not seed occupancy")
	}

	// Resume hook write while still paused → hold → visible.
	tracker.SetTargetBandwidth("sg_r1", 8_000_000)
	occ := tracker.GetOccupancyTrackedTasks()
	if len(occ) != 1 || occ[0].GID != "sg_r1" || occ[0].TargetBandwidth != 8_000_000 {
		t.Fatalf("resume hold occupancy = %#v, want sg_r1 @ 8MB", occ)
	}
	if activeSetContains(tracker, "sg_r1") {
		t.Error("paused+hold must not widen GetActiveTrackedTasks")
	}

	// EventResumed clears hold; active path takes over.
	tracker.SetStatusFromEvent("sg_r1", "active")
	if tracker.tasks["sg_r1"].resumeOccupancyHold {
		t.Error("hold should clear on status transition")
	}
	if !activeSetContains(tracker, "sg_r1") {
		t.Error("active after resume should be in GetActiveTrackedTasks")
	}
	if len(tracker.GetOccupancyTrackedTasks()) != 1 {
		t.Fatal("active task should remain in occupancy")
	}
}

func TestUpdate_ClearsResumeOccupancyHold(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_tick", 100, "https://x", 4, "active")
	tracker.SetStatusFromEvent("sg_tick", "paused")
	tracker.SetTargetBandwidth("sg_tick", 4_000_000)
	if !tracker.tasks["sg_tick"].resumeOccupancyHold {
		t.Fatal("expected hold before Update")
	}

	tracker.Update([]rpc.Task{createMockTask("sg_tick", "active")}, nil, nil)
	if tracker.tasks["sg_tick"].resumeOccupancyHold {
		t.Error("Update/updateActiveTask must clear resumeOccupancyHold")
	}
	if tracker.tasks["sg_tick"].Status != "active" {
		t.Errorf("Status = %q, want active", tracker.tasks["sg_tick"].Status)
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
