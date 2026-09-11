package monitor

import (
	"sync"
	"testing"

	"goaria-v3/internal/rpc"
)

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
		for range 1000 {
			_ = snapshot.DownloadGroup.Name
			_ = snapshot.DownloadGroup.NameStatus
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
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
