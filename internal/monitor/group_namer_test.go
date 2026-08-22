package monitor

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

func setupDownloadGroupNamerTest(t *testing.T) *TaskTracker {
	t.Helper()
	ResetDownloadGroupNamerForTest()
	restoreNamer := ConfigureDownloadGroupNamerForTest(10*time.Second, 10*time.Second, 1)
	originalCache := Cache
	originalTracker := State.GetTracker()
	originalSaveEnabled := history.SaveEnabled
	Cache = &TaskCache{
		metadata:         make(map[string]*TaskMetadata),
		pendingStartGids: make(map[string]time.Time),
	}
	tracker := NewTaskTracker()
	State.SetTracker(tracker)
	ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	history.DisableSaveForTest()
	history.Clear()
	t.Cleanup(func() {
		ResetDownloadGroupNamerForTest()
		restoreNamer()
		history.Clear()
		ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		Cache = originalCache
		State.SetTracker(originalTracker)
	})
	return tracker
}

func namerTestGroup(id string, count int) rpc.DownloadGroup {
	return rpc.DownloadGroup{
		ID:         id,
		Kind:       "batch",
		Name:       "Batch 2026-05-18 10-00-00",
		NameStatus: rpc.DownloadGroupNameStatusFallback,
		FolderName: "Batch 2026-05-18 10-00-00 " + id,
		Dir:        filepath.Join("downloads", id),
		ItemCount:  count,
		CreatedAt:  1779098400,
	}
}

func namerTask(gid string, group rpc.DownloadGroup, path string) rpc.Task {
	return rpc.Task{
		GID:             gid,
		Status:          "active",
		TotalLength:     "100",
		CompletedLength: "0",
		Dir:             group.Dir,
		Files:           []rpc.File{{Path: path}},
		DownloadGroup:   copyDownloadGroup(&group),
	}
}

func currentStoredGroupByKey(t *testing.T, groupKey string) rpc.DownloadGroup {
	t.Helper()
	for _, group := range ListStoredTaskGroups() {
		if group.ID == groupKey {
			return group
		}
	}
	t.Fatalf("expected stored group %q", groupKey)
	return rpc.DownloadGroup{}
}

func TestDownloadGroupNamer_LCPStableNameFromSanitizedBasenames(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	group := namerTestGroup("dg-lcp", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)
	Cache.UpdateFromAria2([]rpc.Task{
		namerTask("gid-one", group, filepath.Join("downloads", "Project Alpha Part 01.bin")),
		namerTask("gid-two", group, filepath.Join("downloads", "Project Alpha Part 02.bin")),
	}, nil, nil)

	result := RunDownloadGroupNameJobForTest(group.ID)

	if result.Status != rpc.DownloadGroupNameStatusStable || result.Name != "Project Alpha Part" {
		t.Fatalf("expected stable LCP name, got %#v", result)
	}
	stored := currentStoredGroupByKey(t, group.ID)
	if stored.Name != "Project Alpha Part" || stored.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected stored stable name, got %#v", stored)
	}
}

func TestDownloadGroupNamer_CommonSubstringStableNameWhenPrefixWeak(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	group := namerTestGroup("dg-lcs", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)
	Cache.UpdateFromAria2([]rpc.Task{
		namerTask("gid-one", group, filepath.Join("downloads", "01 Project Alpha Segment.bin")),
		namerTask("gid-two", group, filepath.Join("downloads", "02 Project Alpha Segment.bin")),
	}, nil, nil)

	result := RunDownloadGroupNameJobForTest(group.ID)

	if result.Status != rpc.DownloadGroupNameStatusStable || result.Name != "Project Alpha Segment" {
		t.Fatalf("expected stable LCS name, got %#v", result)
	}
}

func TestDownloadGroupNamer_FallbackWhenNoSafeCommonCandidate(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	group := namerTestGroup("dg-fallback", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)
	Cache.UpdateFromAria2([]rpc.Task{
		namerTask("gid-one", group, filepath.Join("downloads", "Alpha.bin")),
		namerTask("gid-two", group, filepath.Join("downloads", "Beta.bin")),
	}, nil, nil)

	result := RunDownloadGroupNameJobForTest(group.ID)

	if result.Status != rpc.DownloadGroupNameStatusFallback || result.Name != "Batch 2026-05-18 10-00-00" {
		t.Fatalf("expected fallback result, got %#v", result)
	}
}

func TestDownloadGroupNamer_StripsSecretURLPathAndTokenLikeSegments(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	group := namerTestGroup("dg-secret", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)
	Cache.UpdateFromAria2([]rpc.Task{
		namerTask("gid-one", group, filepath.Join("downloads", "report-token-0123456789abcdef.bin")),
		namerTask("gid-two", group, filepath.Join("downloads", "report-secret-abcdef0123456789.bin")),
	}, nil, nil)

	result := RunDownloadGroupNameJobForTest(group.ID)

	if result.Status != rpc.DownloadGroupNameStatusFallback {
		t.Fatalf("expected unsafe secret-like names to fallback, got %#v", result)
	}
	if result.Name == "report" || result.Name == "token" {
		t.Fatalf("unexpected unsafe derived name %q", result.Name)
	}
}

func TestDownloadGroupNamer_DeterministicAcrossMemberOrder(t *testing.T) {
	paths := []string{
		filepath.Join("downloads", "Project Alpha Part 03.bin"),
		filepath.Join("downloads", "Project Alpha Part 01.bin"),
		filepath.Join("downloads", "Project Alpha Part 02.bin"),
	}
	var names []string
	for i := range 2 {
		func(i int) {
			ResetDownloadGroupNamerForTest()
			Cache = &TaskCache{
				metadata:         make(map[string]*TaskMetadata),
				pendingStartGids: make(map[string]time.Time),
			}
			State.SetTracker(NewTaskTracker())
			ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
			history.Clear()
			group := namerTestGroup(fmt.Sprintf("dg-deterministic-%d", i), 3)
			order := paths
			if i == 1 {
				order = []string{paths[2], paths[0], paths[1]}
			}
			for index := range order {
				gid := fmt.Sprintf("gid-%d", index)
				Cache.RegisterTaskGroup(gid, group)
				Cache.SetTaskGroup(gid, group)
			}
			tasks := make([]rpc.Task, 0, len(order))
			for index, path := range order {
				tasks = append(tasks, namerTask(fmt.Sprintf("gid-%d", index), group, path))
			}
			Cache.UpdateFromAria2(tasks, nil, nil)
			names = append(names, RunDownloadGroupNameJobForTest(group.ID).Name)
		}(i)
	}
	if !reflect.DeepEqual(names, []string{"Project Alpha Part", "Project Alpha Part"}) {
		t.Fatalf("expected deterministic names, got %#v", names)
	}
}

func TestDownloadGroupNamer_PendingThenStableAfterMetadataArrives(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	group := namerTestGroup("dg-pending", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)

	pending := RunDownloadGroupNameJobForTest(group.ID)
	if pending.Status != rpc.DownloadGroupNameStatusPending {
		t.Fatalf("expected pending before metadata, got %#v", pending)
	}

	Cache.UpdateFromAria2([]rpc.Task{
		namerTask("gid-one", group, filepath.Join("downloads", "Project Alpha Part 01.bin")),
		namerTask("gid-two", group, filepath.Join("downloads", "Project Alpha Part 02.bin")),
	}, nil, nil)
	stable := RunDownloadGroupNameJobForTest(group.ID)
	if stable.Status != rpc.DownloadGroupNameStatusStable || stable.Name != "Project Alpha Part" {
		t.Fatalf("expected stable after metadata, got %#v", stable)
	}
}

func TestDownloadGroupNamer_DebouncesMemberUpdatesByGroupKey(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	restore := ConfigureDownloadGroupNamerForTest(10*time.Second, 0, 1)
	defer restore()
	group := namerTestGroup("dg-debounce", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)
	if stored := currentStoredGroupByKey(t, group.ID); stored.NameStatus != rpc.DownloadGroupNameStatusFallback {
		t.Fatalf("expected registration to leave fallback status before async queue runs, got %#v", stored)
	}
	QueueDownloadGroupName(group.ID)
	QueueDownloadGroupName(" " + group.ID + " ")
	if stored := currentStoredGroupByKey(t, group.ID); stored.NameStatus != rpc.DownloadGroupNameStatusFallback {
		t.Fatalf("expected QueueDownloadGroupName to return before pending mark, got %#v", stored)
	}

	if got := PendingDownloadGroupNameJobCountForTest(); got != 1 {
		t.Fatalf("expected one coalesced pending timer, got %d", got)
	}
}

func TestDownloadGroupNamer_QueueDoesNotSynchronouslySnapshotOrApplyPending(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	restore := ConfigureDownloadGroupNamerForTest(10*time.Second, 10*time.Second, 1)
	defer restore()
	group := namerTestGroup("dg-queue-nonblocking", 2)
	Cache.SetTaskGroup("gid-one", group)
	RegisterTaskGroup("gid-one", group)
	before := time.Now()
	QueueDownloadGroupName(group.ID)
	elapsed := time.Since(before)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected queue to return quickly without snapshot/apply work, elapsed=%v", elapsed)
	}
	stored := currentStoredGroupByKey(t, group.ID)
	if stored.NameStatus != rpc.DownloadGroupNameStatusFallback {
		t.Fatalf("expected queued group to remain fallback until worker runs, got %#v", stored)
	}
}

func TestDownloadGroupNamer_DegradedAfterBoundedMetadataRetryFailure(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	restore := ConfigureDownloadGroupNamerForTest(10*time.Second, 10*time.Second, 1)
	defer restore()
	group := namerTestGroup("dg-degraded", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)

	first := RunDownloadGroupNameJobForTest(group.ID)
	second := RunDownloadGroupNameJobForTest(group.ID)

	if first.Status != rpc.DownloadGroupNameStatusPending || second.Status != rpc.DownloadGroupNameStatusDegraded {
		t.Fatalf("expected pending then degraded, first=%#v second=%#v", first, second)
	}
}

func TestDownloadGroupNamer_StripsDashedUUIDLikeIdentifiersBeforeDisplayInfluence(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	group := namerTestGroup("dg-uuid", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)
	Cache.UpdateFromAria2([]rpc.Task{
		namerTask("gid-one", group, filepath.Join("downloads", "Project Alpha 550e8400-e29b-41d4-a716-446655440000.bin")),
		namerTask("gid-two", group, filepath.Join("downloads", "Project Alpha 550e8400-e29b-41d4-a716-446655440000.bin")),
	}, nil, nil)

	result := RunDownloadGroupNameJobForTest(group.ID)

	if result.Status != rpc.DownloadGroupNameStatusFallback {
		t.Fatalf("expected UUID-like member stems to be rejected/fallback, got %#v", result)
	}
	if strings.Contains(result.Name, "550e8400") || strings.Contains(result.Name, "446655440000") {
		t.Fatalf("expected UUID fragments not to influence display name, got %q", result.Name)
	}
}

func TestDownloadGroupNamer_DoesNotRenameFolderNameOrDir(t *testing.T) {
	setupDownloadGroupNamerTest(t)
	group := namerTestGroup("dg-no-rename", 2)
	folderName := group.FolderName
	dir := group.Dir
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)
	Cache.UpdateFromAria2([]rpc.Task{
		namerTask("gid-one", group, filepath.Join("downloads", "Project Alpha Part 01.bin")),
		namerTask("gid-two", group, filepath.Join("downloads", "Project Alpha Part 02.bin")),
	}, nil, nil)

	RunDownloadGroupNameJobForTest(group.ID)
	stored := currentStoredGroupByKey(t, group.ID)
	if stored.FolderName != folderName || stored.Dir != dir {
		t.Fatalf("expected folder metadata unchanged, before=(%q,%q) after=(%q,%q)", folderName, dir, stored.FolderName, stored.Dir)
	}
}

func TestDownloadGroupNamer_AppliesNameToStoreCacheTrackerAndHistory(t *testing.T) {
	tracker := setupDownloadGroupNamerTest(t)
	group := namerTestGroup("dg-apply", 2)
	Cache.RegisterTaskGroup("gid-one", group)
	Cache.RegisterTaskGroup("gid-two", group)
	tracker.SetTaskGroup("gid-one", group)
	history.Add(history.HistoryEntry{
		GID:           "gid-two",
		Path:          filepath.Join("history", "Project Alpha Part 02.bin"),
		DownloadGroup: copyDownloadGroup(&group),
	})
	Cache.UpdateFromAria2([]rpc.Task{namerTask("gid-one", group, filepath.Join("downloads", "Project Alpha Part 01.bin"))}, nil, nil)

	result := RunDownloadGroupNameJobForTest(group.ID)
	if result.Status != rpc.DownloadGroupNameStatusStable || result.Name != "Project Alpha Part" {
		t.Fatalf("expected stable applied name, got %#v", result)
	}
	if stored := currentStoredGroupByKey(t, group.ID); stored.Name != result.Name || stored.NameStatus != result.Status {
		t.Fatalf("store not updated: %#v", stored)
	}
	if got := Cache.GetTaskGroup("gid-one"); got == nil || got.Name != result.Name || got.NameStatus != result.Status {
		t.Fatalf("cache not updated: %#v", got)
	}
	if got := tracker.GetTaskGroup("gid-one"); got == nil || got.Name != result.Name || got.NameStatus != result.Status {
		t.Fatalf("tracker not updated: %#v", got)
	}
	entry, ok := history.Get("gid-two")
	if !ok || entry.DownloadGroup == nil || entry.DownloadGroup.Name != result.Name || entry.DownloadGroup.NameStatus != result.Status {
		t.Fatalf("history not updated: %#v ok=%v", entry, ok)
	}
}
