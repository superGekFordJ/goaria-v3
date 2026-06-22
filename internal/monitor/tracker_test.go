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
	tracker.EnsureTrackedFromEvent("sg-evt-001", 100000000, "https://example.com/file.zip", 8)

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
	tracker.EnsureTrackedFromEvent("sg-evt-002", 200000000, "https://new.com/file.zip", 16)

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

	tracker.EnsureTrackedFromEvent("sg-evt-003", 100000000, "https://example.com/file.zip", 8)

	tracked := tracker.tasks["sg-evt-003"]
	if tracked == nil {
		t.Fatal("Expected task to be tracked")
	}
	if tracked.ThreadCount != 4 {
		t.Errorf("ThreadCount = %d, want 4 (should not be overwritten)", tracked.ThreadCount)
	}
}

func TestTaskTracker_SampleSpeedFromEvent_LargeFile(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-004", 100000000, "https://example.com/file.zip", 8)

	// First sample — not enough sustained count (threshold=2 for event path)
	tracker.SampleSpeedFromEvent("sg-evt-004", 10000000, 100000000, 60000000)
	tracked := tracker.tasks["sg-evt-004"]
	if tracked.PeakSpeed != 0 {
		t.Errorf("PeakSpeed = %d, want 0 (first sample, not enough sustained)", tracked.PeakSpeed)
	}

	// Second sample — now sustained count = 2, should record
	tracker.SampleSpeedFromEvent("sg-evt-004", 10000000, 100000000, 70000000)
	if tracked.PeakSpeed != 10000000 {
		t.Errorf("PeakSpeed = %d, want 10000000 (after 2 stable samples)", tracked.PeakSpeed)
	}
}

func TestTaskTracker_SampleSpeedFromEvent_SmallFileSkipped(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-005", 1000000, "https://example.com/small.zip", 4)

	// File < 50MB, should not sample
	tracker.SampleSpeedFromEvent("sg-evt-005", 5000000, 1000000, 500000)
	tracked := tracker.tasks["sg-evt-005"]
	if tracked.PeakSpeed != 0 {
		t.Errorf("PeakSpeed = %d, want 0 (small file skipped)", tracked.PeakSpeed)
	}
}

func TestTaskTracker_SampleSpeedFromEvent_NonexistentTask(t *testing.T) {
	tracker := NewTaskTracker()
	// Should not panic
	tracker.SampleSpeedFromEvent("nonexistent", 10000000, 100000000, 1000000)
}

func TestTaskTracker_MarkCompleteFromEvent(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-006", 100000000, "https://example.com/file.zip", 8)

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
	tracker.EnsureTrackedFromEvent("sg-evt-007", 100000000, "https://example.com/file.zip", 8)

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
	tracker.EnsureTrackedFromEvent("sg-evt-008", 100000000, "https://example.com/file.zip", 8)

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
	tracker.EnsureTrackedFromEvent("sg-evt-009", 100000000, "https://example.com/file.zip", 8)

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

func TestTaskTracker_SampleSpeedFromEvent_UpdatesTotalAndCompleted(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-011", 0, "https://example.com/file.zip", 8)

	// First event provides total and completed
	tracker.SampleSpeedFromEvent("sg-evt-011", 5000000, 200000000, 10000000)
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
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	tracker := NewTaskTracker()

	// 1. 事件路径创建并采样
	tracker.EnsureTrackedFromEvent("sg-cross-001", 100000000, "https://example.com/file.zip", 8)
	tracker.SampleSpeedFromEvent("sg-cross-001", 10000000, 100000000, 60000000)
	tracker.SampleSpeedFromEvent("sg-cross-001", 10000000, 100000000, 70000000)

	// 2. 事件路径标记完成
	completed := tracker.MarkCompleteFromEvent("sg-cross-001", "complete")
	if completed == nil {
		t.Fatal("Expected non-nil from event-path MarkCompleteFromEvent")
	}
	if completed.PeakSpeed != 10000000 {
		t.Errorf("PeakSpeed = %d, want 10000000", completed.PeakSpeed)
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
	tracker.EnsureTrackedFromEvent("sg-cross-002", 100000000, "https://example.com/file.zip", 8)

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
