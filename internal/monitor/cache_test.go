package monitor

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func TestTaskCache_HasValidMetadataRequiresNonEmptyPath(t *testing.T) {
	const gid = "gid-empty-path"
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}

	cache.metadata[gid] = &TaskMetadata{GID: gid, Files: []string{""}}
	if cache.HasValidMetadata(gid) {
		t.Fatal("expected metadata with an empty path to be invalid")
	}

	cache.metadata[gid] = &TaskMetadata{GID: gid, Files: []string{"   "}}
	if cache.HasValidMetadata(gid) {
		t.Fatal("expected metadata with a whitespace-only path to be invalid")
	}

	cache.metadata[gid] = &TaskMetadata{GID: gid, Files: []string{"/downloads/file.zip"}}
	if !cache.HasValidMetadata(gid) {
		t.Fatal("expected metadata with a non-empty path to be valid")
	}
}

func TestTaskCache_GettersReturnDefensiveGroupCopies(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := testDownloadGroup("dg-cache-defensive")
	task := rpc.Task{GID: "gid-defensive", Status: "active", DownloadGroup: &group}
	cache.UpdateFromAria2([]rpc.Task{task}, nil, nil)
	cache.SetTaskGroup("gid-defensive", group)

	active := cache.GetActive()
	active[0].DownloadGroup.Name = "mutated outside cache"
	active[0].DownloadGroup.NameStatus = rpc.DownloadGroupNameStatusStable
	if got := cache.GetActive()[0].DownloadGroup; got == nil || got.Name == "mutated outside cache" || got.NameStatus == rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected cache active group defensive copy, got %#v", got)
	}

	meta := cache.GetMetadata("gid-defensive")
	meta.DownloadGroup.Name = "mutated metadata"
	if got := cache.GetMetadata("gid-defensive"); got == nil || got.DownloadGroup == nil || got.DownloadGroup.Name == "mutated metadata" {
		t.Fatalf("expected metadata defensive copy, got %#v", got)
	}
}

func TestTaskCache_EnsureMetadataSkipsEmptyPath(t *testing.T) {
	const gid = "gid-skip-empty-path"
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}

	cache.ensureMetadata(rpc.Task{
		GID:         gid,
		Dir:         "/downloads",
		TotalLength: "1024",
		Files:       []rpc.File{{Path: ""}},
	})

	if cache.HasValidMetadata(gid) {
		t.Fatal("expected empty-path metadata to remain invalid")
	}
	meta := cache.GetMetadata(gid)
	if meta == nil {
		return
	}
	for _, path := range meta.Files {
		if metadataPathValid(path) {
			t.Fatalf("expected no non-empty paths to be cached, got %q", path)
		}
	}
}

func TestTaskCache_EnsureMetadataReplacesPollutedEmptyPath(t *testing.T) {
	const gid = "gid-replace-polluted-path"
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.metadata[gid] = &TaskMetadata{GID: gid, Files: []string{""}}

	cache.ensureMetadata(rpc.Task{
		GID:             gid,
		Dir:             "/downloads",
		TotalLength:     "2048",
		CompletedLength: "128",
		Files: []rpc.File{{
			Path: "/downloads/real-file.zip",
			Uris: []rpc.Uri{{Uri: "https://example.com/real-file.zip", Status: "used"}},
		}},
	})

	if !cache.HasValidMetadata(gid) {
		t.Fatal("expected later valid metadata to replace polluted empty-path metadata")
	}
	meta := cache.GetMetadata(gid)
	if meta == nil {
		t.Fatal("expected metadata to be cached")
	}
	if got, want := meta.Files[0], "/downloads/real-file.zip"; got != want {
		t.Fatalf("expected cached path %q, got %q", want, got)
	}
	if got, want := meta.Dir, "/downloads"; got != want {
		t.Fatalf("expected dir %q, got %q", want, got)
	}
	if got, want := meta.SourceURL, "https://example.com/real-file.zip"; got != want {
		t.Fatalf("expected source URL %q, got %q", want, got)
	}
	if got, want := meta.TotalLength, int64(2048); got != want {
		t.Fatalf("expected total length %d, got %d", want, got)
	}
}

func testDownloadGroup(id string) rpc.DownloadGroup {
	return rpc.DownloadGroup{
		ID:         id,
		Kind:       "batch",
		Name:       "Batch 2026-05-07 15-04-05",
		FolderName: "Batch 2026-05-07 15-04-05 " + id,
		Dir:        "/downloads/" + id,
		ItemCount:  5,
		CreatedAt:  1778166245,
	}
}

func TestTaskCache_SetTaskGroupEnrichesLiteTaskWithoutValidPath(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := testDownloadGroup("dg-cache-lite")
	cache.SetTaskGroup("gid-group", group)

	if cache.HasValidMetadata("gid-group") {
		t.Fatal("group-only metadata should not count as valid file metadata")
	}

	tasks := []rpc.Task{{GID: "gid-group", Status: "active"}}
	cache.EnrichTasks(tasks)

	if tasks[0].DownloadGroup == nil || tasks[0].DownloadGroup.ID != group.ID {
		t.Fatalf("expected lite task to receive group metadata, got %#v", tasks[0].DownloadGroup)
	}
}

func TestTaskCache_GroupPreservedWhenFullMetadataArrivesWithoutGroup(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := testDownloadGroup("dg-cache-full")
	cache.SetTaskGroup("gid-group", group)

	cache.ensureMetadata(rpc.Task{
		GID:         "gid-group",
		Dir:         "/downloads/dg-cache-full",
		TotalLength: "2048",
		Files: []rpc.File{{
			Path: "/downloads/dg-cache-full/file.bin",
			Uris: []rpc.Uri{{Uri: "https://example.com/file.bin"}},
		}},
	})

	meta := cache.GetMetadata("gid-group")
	if meta == nil || meta.DownloadGroup == nil || meta.DownloadGroup.ID != group.ID {
		t.Fatalf("expected full metadata to preserve group, got %#v", meta)
	}
	if !cache.HasValidMetadata("gid-group") {
		t.Fatal("expected valid file metadata after full payload")
	}

	tasks := []rpc.Task{{GID: "gid-group", Status: "active", Dir: "/downloads/dg-cache-full"}}
	cache.EnrichTasks(tasks)
	if tasks[0].DownloadGroup == nil || tasks[0].DownloadGroup.ID != group.ID {
		t.Fatalf("expected enriched task group, got %#v", tasks[0].DownloadGroup)
	}
	if len(tasks[0].Files) == 0 || tasks[0].Files[0].Path == "" {
		t.Fatalf("expected enriched files, got %#v", tasks[0].Files)
	}
}

func TestTaskCache_EnsureMetadataStoresIncomingGroupAndInvalidateRemovesIt(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := testDownloadGroup("dg-cache-incoming")
	cache.ensureMetadata(rpc.Task{GID: "gid-incoming", DownloadGroup: &group})

	if cache.HasValidMetadata("gid-incoming") {
		t.Fatal("incoming group without files should not count as valid file metadata")
	}
	if got := cache.GetTaskGroup("gid-incoming"); got == nil || got.ID != group.ID {
		t.Fatalf("expected incoming group to be cached, got %#v", got)
	}

	cache.InvalidateMetadata("gid-incoming")
	if got := cache.GetTaskGroup("gid-incoming"); got != nil {
		t.Fatalf("expected group to be invalidated with metadata, got %#v", got)
	}
}

func TestTaskCache_UpdateTaskGroupNamePreservesFileMetadata(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := testDownloadGroup("dg-cache-name")
	cache.SetTaskGroup("gid-group", group)
	cache.ensureMetadata(rpc.Task{
		GID:         "gid-group",
		Dir:         "/downloads/dg-cache-name",
		TotalLength: "2048",
		Files: []rpc.File{{
			Path: "/downloads/dg-cache-name/file.bin",
			Uris: []rpc.Uri{{Uri: "https://example.com/file.bin"}},
		}},
	})

	changed := cache.UpdateTaskGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)

	if changed == 0 {
		t.Fatal("expected group name update to change cache")
	}
	meta := cache.GetMetadata("gid-group")
	if meta == nil || meta.DownloadGroup == nil || meta.DownloadGroup.Name != "Project Alpha" || meta.DownloadGroup.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected updated group name/status, got %#v", meta)
	}
	if !cache.HasValidMetadata("gid-group") || len(meta.Files) == 0 || meta.Files[0] != "/downloads/dg-cache-name/file.bin" {
		t.Fatalf("expected file metadata preserved, got %#v", meta)
	}
}

func TestTaskCache_UpdateTaskGroupNameReplacesSharedGroupPointers(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := testDownloadGroup("dg-cache-replace")
	cache.UpdateFromAria2([]rpc.Task{{GID: "gid-one", Status: "active", DownloadGroup: &group}}, nil, nil)
	before := cache.GetActive()[0].DownloadGroup

	cache.UpdateTaskGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)
	after := cache.GetActive()[0].DownloadGroup

	if before == nil || after == nil {
		t.Fatalf("expected group pointers before/after, before=%#v after=%#v", before, after)
	}
	if before == after {
		t.Fatal("expected update to replace cached group pointer instead of mutating it in place")
	}
	if before.Name == "Project Alpha" || before.NameStatus == rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected old snapshot pointer unchanged, got %#v", before)
	}
	if after.Name != "Project Alpha" || after.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected new snapshot pointer updated, got %#v", after)
	}
}

func TestTaskCache_UpdateTaskGroupNameConcurrentReaders(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := testDownloadGroup("dg-cache-race")
	cache.UpdateFromAria2([]rpc.Task{{GID: "gid-race", Status: "active", DownloadGroup: &group}}, nil, nil)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = cache.GetActive()
				cache.UpdateTaskGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)
				cache.UpdateTaskGroupName(group.ID, group.Name, rpc.DownloadGroupNameStatusFallback)
			}
		}()
	}
	wg.Wait()
}

func TestTaskCache_MoveTaskToStopped_FromActive(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_1", Status: "active", Title: "file.zip"}, "active")

	cache.MoveTaskToStopped("sg_1", "complete")

	if len(cache.GetActive()) != 0 {
		t.Fatalf("expected active empty, got %d", len(cache.GetActive()))
	}
	stopped := cache.GetStopped()
	if len(stopped) != 1 || stopped[0].GID != "sg_1" || stopped[0].Status != "complete" {
		t.Fatalf("expected stopped=[sg_1 complete], got %#v", stopped)
	}
	if stopped[0].DownloadSpeed != "0" {
		t.Errorf("expected DownloadSpeed=0, got %s", stopped[0].DownloadSpeed)
	}
}

func TestTaskCache_MoveTaskToStopped_FromWaiting(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_2", Status: "waiting"}, "waiting")

	cache.MoveTaskToStopped("sg_2", "error")

	if len(cache.GetWaiting()) != 0 {
		t.Fatalf("expected waiting empty, got %d", len(cache.GetWaiting()))
	}
	stopped := cache.GetStopped()
	if len(stopped) != 1 || stopped[0].GID != "sg_2" || stopped[0].Status != "error" {
		t.Fatalf("expected stopped=[sg_2 error], got %#v", stopped)
	}
}

func TestTaskCache_MoveTaskToStopped_NotFound(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_1", Status: "active"}, "active")

	cache.MoveTaskToStopped("sg_nonexistent", "complete")

	if len(cache.GetActive()) != 1 {
		t.Fatalf("expected active unchanged, got %d", len(cache.GetActive()))
	}
	if len(cache.GetStopped()) != 0 {
		t.Fatalf("expected stopped empty, got %d", len(cache.GetStopped()))
	}
}

func TestTaskCache_MoveTaskToWaiting_FromActive(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_3", Status: "active", DownloadSpeed: "1000"}, "active")

	cache.MoveTaskToWaiting("sg_3", "paused")

	if len(cache.GetActive()) != 0 {
		t.Fatalf("expected active empty, got %d", len(cache.GetActive()))
	}
	waiting := cache.GetWaiting()
	if len(waiting) != 1 || waiting[0].GID != "sg_3" || waiting[0].Status != "paused" {
		t.Fatalf("expected waiting=[sg_3 paused], got %#v", waiting)
	}
	if waiting[0].DownloadSpeed != "0" {
		t.Errorf("expected DownloadSpeed=0, got %s", waiting[0].DownloadSpeed)
	}
}

func TestTaskCache_MoveTaskToActive_FromWaiting(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_4", Status: "paused"}, "waiting")

	from := cache.MoveTaskToActive("sg_4", "active")
	if from != "waiting" {
		t.Fatalf("expected from=waiting, got %q", from)
	}

	if len(cache.GetWaiting()) != 0 {
		t.Fatalf("expected waiting empty, got %d", len(cache.GetWaiting()))
	}
	active := cache.GetActive()
	if len(active) != 1 || active[0].GID != "sg_4" || active[0].Status != "active" {
		t.Fatalf("expected active=[sg_4 active], got %#v", active)
	}
	if active[0].DownloadSpeed != "0" {
		t.Errorf("expected DownloadSpeed=0, got %s", active[0].DownloadSpeed)
	}
}

func TestTaskCache_GetTaskLists_CoherentPerEngine(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_a", Status: "active"}, "active")
	cache.AddSgTask(rpc.Task{GID: "sg_w", Status: "paused"}, "waiting")
	cache.AddSgTask(rpc.Task{GID: "sg_s", Status: "complete"}, "stopped")
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_a", Status: "active"}},
		[]rpc.Task{{GID: "ar_w", Status: "waiting"}},
		[]rpc.Task{{GID: "ar_s", Status: "complete"}},
	)

	active, waiting, stopped := cache.GetTaskLists()
	wantActive := map[string]bool{"sg_a": true, "ar_a": true}
	wantWaiting := map[string]bool{"sg_w": true, "ar_w": true}
	wantStopped := map[string]bool{"sg_s": true, "ar_s": true}
	for _, task := range active {
		if !wantActive[task.GID] {
			t.Fatalf("unexpected active gid %q", task.GID)
		}
		delete(wantActive, task.GID)
	}
	for _, task := range waiting {
		if !wantWaiting[task.GID] {
			t.Fatalf("unexpected waiting gid %q", task.GID)
		}
		delete(wantWaiting, task.GID)
	}
	for _, task := range stopped {
		if !wantStopped[task.GID] {
			t.Fatalf("unexpected stopped gid %q", task.GID)
		}
		delete(wantStopped, task.GID)
	}
	if len(wantActive) != 0 || len(wantWaiting) != 0 || len(wantStopped) != 0 {
		t.Fatalf("missing gids active=%v waiting=%v stopped=%v", wantActive, wantWaiting, wantStopped)
	}

	active[0].Status = "mutated"
	freshActive, _, _ := cache.GetTaskLists()
	if freshActive[0].Status == "mutated" {
		t.Fatal("expected GetTaskLists to return defensive copies")
	}
}

func TestTaskCache_MoveTaskToActive_FromStopped(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := &rpc.DownloadGroup{ID: "dg-1", Kind: "batch", Name: "Batch"}
	cache.AddSgTask(rpc.Task{
		GID:             "sg_err",
		Status:          "error",
		ErrorCode:       "9",
		ErrorMessage:    "disk full",
		CompletedLength: "500",
		TotalLength:     "1000",
		Dir:             "/downloads",
		Files:           []rpc.File{{Path: "/downloads/err.bin"}},
		DownloadGroup:   group,
		DownloadSpeed:   "0",
	}, "stopped")

	from := cache.MoveTaskToActive("sg_err", "active")
	if from != "stopped" {
		t.Fatalf("expected from=stopped, got %q", from)
	}
	if len(cache.GetStopped()) != 0 {
		t.Fatalf("expected stopped empty, got %d", len(cache.GetStopped()))
	}
	active := cache.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active, got %#v", active)
	}
	got := active[0]
	if got.Status != "active" {
		t.Errorf("Status = %q, want active", got.Status)
	}
	if got.DownloadSpeed != "0" {
		t.Errorf("DownloadSpeed = %q, want 0", got.DownloadSpeed)
	}
	if got.ErrorCode != "" || got.ErrorMessage != "" {
		t.Errorf("expected cleared error fields, got code=%q msg=%q", got.ErrorCode, got.ErrorMessage)
	}
	if got.CompletedLength != "500" || got.TotalLength != "1000" {
		t.Errorf("lengths not preserved: completed=%q total=%q", got.CompletedLength, got.TotalLength)
	}
	if got.Dir != "/downloads" || len(got.Files) == 0 || got.Files[0].Path != "/downloads/err.bin" {
		t.Errorf("files/dir not preserved: dir=%q files=%#v", got.Dir, got.Files)
	}
	if got.DownloadGroup == nil || got.DownloadGroup.ID != "dg-1" {
		t.Errorf("DownloadGroup not preserved: %#v", got.DownloadGroup)
	}
}

func TestTaskCache_MoveTaskToWaiting_FromStopped(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{
		GID:          "sg_err_w",
		Status:       "error",
		ErrorCode:    "1",
		ErrorMessage: "fail",
		Dir:          "/dl",
		Files:        []rpc.File{{Path: "/dl/a.bin"}},
	}, "stopped")

	from := cache.MoveTaskToWaiting("sg_err_w", "paused")
	if from != "stopped" {
		t.Fatalf("expected from=stopped, got %q", from)
	}
	waiting := cache.GetWaiting()
	if len(waiting) != 1 || waiting[0].Status != "paused" {
		t.Fatalf("expected waiting=[sg_err_w paused], got %#v", waiting)
	}
	if waiting[0].ErrorCode != "" || waiting[0].ErrorMessage != "" {
		t.Errorf("expected cleared error fields, got code=%q msg=%q", waiting[0].ErrorCode, waiting[0].ErrorMessage)
	}
	if len(cache.GetStopped()) != 0 {
		t.Fatalf("expected stopped empty, got %d", len(cache.GetStopped()))
	}
}

func TestTaskCache_MoveTaskToWaitingFromLive_RefusesStoppedPreservesError(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{
		GID:          "sg_term_refuse",
		Status:       "error",
		ErrorCode:    "1",
		ErrorMessage: "fail",
	}, "stopped")

	from := cache.MoveTaskToWaitingFromLive("sg_term_refuse", "paused")
	if from != "" {
		t.Fatalf("expected refuse-stopped move to return empty, got %q", from)
	}
	if !cache.IsInStopped("sg_term_refuse") {
		t.Fatal("expected task to remain stopped")
	}
	stopped := cache.GetStopped()
	if len(stopped) != 1 || stopped[0].ErrorCode != "1" || stopped[0].ErrorMessage != "fail" {
		t.Fatalf("expected ErrorCode preserved on refuse, got %#v", stopped)
	}
	if len(cache.GetWaiting()) != 0 {
		t.Fatalf("expected waiting empty, got %#v", cache.GetWaiting())
	}
}

func TestTaskCache_GetLiveTaskLists_SkipsStoppedCopy(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_a", Status: "active"}, "active")
	cache.AddSgTask(rpc.Task{GID: "sg_w", Status: "paused"}, "waiting")
	cache.AddSgTask(rpc.Task{GID: "sg_s", Status: "error", ErrorCode: "1"}, "stopped")

	active, waiting := cache.GetLiveTaskLists()
	if len(active) != 1 || active[0].GID != "sg_a" {
		t.Fatalf("active=%#v", active)
	}
	if len(waiting) != 1 || waiting[0].GID != "sg_w" {
		t.Fatalf("waiting=%#v", waiting)
	}
}

func TestTaskCache_MoveTaskToActive_ReturnValues(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_already", Status: "active"}, "active")

	if from := cache.MoveTaskToActive("sg_already", "active"); from != "active" {
		t.Fatalf("already-in-destination: got %q, want active", from)
	}
	if from := cache.MoveTaskToActive("sg_missing", "active"); from != "" {
		t.Fatalf("unknown GID: got %q, want empty", from)
	}
}

func TestTaskCache_MoveTaskToWaiting_ReturnValues(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_already_w", Status: "paused"}, "waiting")

	if from := cache.MoveTaskToWaiting("sg_already_w", "paused"); from != "waiting" {
		t.Fatalf("already-in-destination: got %q, want waiting", from)
	}
	if from := cache.MoveTaskToWaiting("sg_missing_w", "paused"); from != "" {
		t.Fatalf("unknown GID: got %q, want empty", from)
	}
}

func TestTaskCache_MoveTaskToActive_SweepsCorruptSibling(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_dup", Status: "active"}, "active")
	// Simulate corrupt multi-membership without going through public APIs.
	cache.sgMu.Lock()
	cache.sgWaiting = append(cache.sgWaiting, rpc.Task{GID: "sg_dup", Status: "paused"})
	cache.sgMu.Unlock()

	from := cache.MoveTaskToActive("sg_dup", "active")
	if from != "active" {
		t.Fatalf("expected from=active (in-place), got %q", from)
	}
	if len(cache.GetActive()) != 1 {
		t.Fatalf("expected 1 active, got %#v", cache.GetActive())
	}
	if len(cache.GetWaiting()) != 0 {
		t.Fatalf("expected waiting purged of sibling, got %#v", cache.GetWaiting())
	}
}

func TestTaskCache_MoveTaskToWaiting_SweepsCorruptStoppedSibling(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_w_dup", Status: "paused"}, "waiting")
	cache.sgMu.Lock()
	cache.sgStopped = append(cache.sgStopped, rpc.Task{
		GID: "sg_w_dup", Status: "error", ErrorCode: "9", ErrorMessage: "stale",
	})
	cache.sgMu.Unlock()

	from := cache.MoveTaskToWaiting("sg_w_dup", "paused")
	if from != "waiting" {
		t.Fatalf("expected from=waiting (in-place), got %q", from)
	}
	if len(cache.GetWaiting()) != 1 {
		t.Fatalf("expected 1 waiting, got %#v", cache.GetWaiting())
	}
	if len(cache.GetStopped()) != 0 {
		t.Fatalf("expected stopped twin purged, got %#v", cache.GetStopped())
	}
}

func TestTaskCache_MoveTaskToActive_CrossListSweepsRemainingTwin(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_cross", Status: "paused"}, "waiting")
	cache.sgMu.Lock()
	cache.sgStopped = append(cache.sgStopped, rpc.Task{
		GID: "sg_cross", Status: "error", ErrorCode: "1",
	})
	cache.sgMu.Unlock()

	from := cache.MoveTaskToActive("sg_cross", "active")
	if from != "waiting" {
		t.Fatalf("expected from=waiting, got %q", from)
	}
	if len(cache.GetActive()) != 1 || cache.GetActive()[0].GID != "sg_cross" {
		t.Fatalf("expected active=[sg_cross], got %#v", cache.GetActive())
	}
	if len(cache.GetWaiting()) != 0 {
		t.Fatalf("expected waiting empty, got %#v", cache.GetWaiting())
	}
	if len(cache.GetStopped()) != 0 {
		t.Fatalf("expected stopped twin purged after cross-list move, got %#v", cache.GetStopped())
	}
}

func TestTaskCache_MoveTaskToActive_FromStopped_Aria2(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.UpdateFromAria2(nil, nil, []rpc.Task{{
		GID:          "ar_err",
		Status:       "error",
		ErrorCode:    "3",
		ErrorMessage: "not found",
		Dir:          "/aria",
		Files:        []rpc.File{{Path: "/aria/x.bin"}},
	}})

	from := cache.MoveTaskToActive("ar_err", "active")
	if from != "stopped" {
		t.Fatalf("expected from=stopped, got %q", from)
	}
	active := cache.GetActive()
	found := false
	for _, tsk := range active {
		if tsk.GID == "ar_err" {
			found = true
			if tsk.ErrorCode != "" || tsk.ErrorMessage != "" {
				t.Errorf("expected cleared errors, got code=%q msg=%q", tsk.ErrorCode, tsk.ErrorMessage)
			}
			if tsk.Status != "active" {
				t.Errorf("Status = %q, want active", tsk.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected ar_err in active")
	}
	for _, tsk := range cache.GetStopped() {
		if tsk.GID == "ar_err" {
			t.Fatal("ar_err should not remain in stopped")
		}
	}
}

// --- prefix routing & concurrent write tests ---

func TestTaskCache_ConcurrentSurgeEventAndAria2Tick(t *testing.T) {
	cache := &TaskCache{
		metadata:         make(map[string]*TaskMetadata),
		pendingStartGids: make(map[string]time.Time),
	}
	cache.AddSgTask(rpc.Task{GID: "sg_a1", Status: "active", DownloadSpeed: "100"}, "active")
	cache.AddSgTask(rpc.Task{GID: "sg_a2", Status: "active", DownloadSpeed: "200"}, "active")
	cache.UpdateFromAria2(
		[]rpc.Task{
			{GID: "ar_a1", Status: "active", DownloadSpeed: "50"},
		},
		[]rpc.Task{{GID: "ar_w1", Status: "waiting"}},
		nil,
	)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			cache.MoveTaskToStopped("sg_a1", "complete")
			cache.MoveTaskToWaiting("sg_a2", "paused")
			cache.MoveTaskToActive("sg_a2", "active")
			cache.PatchTaskProgress("sg_a1", "100", "10", "1000")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			cache.UpdateFromAria2(
				[]rpc.Task{
					{GID: "ar_a1", Status: "active", DownloadSpeed: "50"},
					{GID: "ar_a2", Status: "active", DownloadSpeed: "60"},
				},
				[]rpc.Task{{GID: "ar_w1", Status: "waiting"}},
				[]rpc.Task{{GID: "ar_s1", Status: "complete"}},
			)
		}
	}()

	wg.Wait()

	active := cache.GetActive()
	waiting := cache.GetWaiting()
	stopped := cache.GetStopped()

	for _, task := range active {
		if task.GID == "" {
			t.Fatal("expected non-empty GID in active")
		}
	}
	for _, task := range waiting {
		if task.GID == "" {
			t.Fatal("expected non-empty GID in waiting")
		}
	}
	for _, task := range stopped {
		if task.GID == "" {
			t.Fatal("expected non-empty GID in stopped")
		}
	}
}

func TestTaskCache_UpdateFromAria2_SplitsByEnginePrefix(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	// sg_ tasks are seeded via AddSgTask (event-driven path)
	cache.AddSgTask(rpc.Task{GID: "sg_1", Status: "active"}, "active")
	cache.AddSgTask(rpc.Task{GID: "sg_2", Status: "waiting"}, "waiting")
	cache.AddSgTask(rpc.Task{GID: "sg_3", Status: "complete"}, "stopped")
	// ar_ tasks are seeded via UpdateFromAria2 (tick polling path)
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_1", Status: "active"}},
		[]rpc.Task{{GID: "ar_2", Status: "waiting"}},
		[]rpc.Task{{GID: "ar_3", Status: "complete"}},
	)

	active := cache.GetActive()
	waiting := cache.GetWaiting()
	stopped := cache.GetStopped()

	if len(active) != 2 || len(waiting) != 2 || len(stopped) != 2 {
		t.Fatalf("expected 2/2/2, got %d/%d/%d", len(active), len(waiting), len(stopped))
	}

	activeGids := gidsFromTasks(active)
	if !containsGid(activeGids, "sg_1") || !containsGid(activeGids, "ar_1") {
		t.Fatalf("expected active to contain sg_1 and ar_1, got %v", activeGids)
	}
	waitingGids := gidsFromTasks(waiting)
	if !containsGid(waitingGids, "sg_2") || !containsGid(waitingGids, "ar_2") {
		t.Fatalf("expected waiting to contain sg_2 and ar_2, got %v", waitingGids)
	}
	stoppedGids := gidsFromTasks(stopped)
	if !containsGid(stoppedGids, "sg_3") || !containsGid(stoppedGids, "ar_3") {
		t.Fatalf("expected stopped to contain sg_3 and ar_3, got %v", stoppedGids)
	}
}

func TestTaskCache_UpdateFromAria2_OnlyReplacesArSlices(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	// Seed sg_ task via event path
	cache.AddSgTask(rpc.Task{GID: "sg_1", Status: "active"}, "active")
	// Seed ar_ task via UpdateFromAria2
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_1", Status: "active"}},
		nil, nil,
	)

	// Update ar_ slice — sg_ slice should be preserved
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_2", Status: "active"}},
		nil, nil,
	)

	active := cache.GetActive()
	if len(active) != 2 {
		t.Fatalf("expected 2 active (sg_1 + ar_2), got %d: %v", len(active), gidsFromTasks(active))
	}
	gids := gidsFromTasks(active)
	if !containsGid(gids, "sg_1") {
		t.Fatal("expected sg_1 preserved in active (event-driven, not touched by UpdateFromAria2)")
	}
	if !containsGid(gids, "ar_2") {
		t.Fatal("expected ar_2 in active")
	}
	if containsGid(gids, "ar_1") {
		t.Fatal("expected ar_1 replaced by ar_2")
	}
}

func TestTaskCache_MoveTaskToStopped_RoutesByPrefix(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_1", Status: "active"}, "active")
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_1", Status: "active"}},
		nil, nil,
	)

	cache.MoveTaskToStopped("sg_1", "complete")

	active := cache.GetActive()
	if len(active) != 1 || active[0].GID != "ar_1" {
		t.Fatalf("expected only ar_1 in active, got %v", gidsFromTasks(active))
	}
	stopped := cache.GetStopped()
	if len(stopped) != 1 || stopped[0].GID != "sg_1" || stopped[0].Status != "complete" {
		t.Fatalf("expected sg_1 complete in stopped, got %v", stopped)
	}

	cache.MoveTaskToStopped("ar_1", "error")
	active = cache.GetActive()
	if len(active) != 0 {
		t.Fatalf("expected active empty, got %v", gidsFromTasks(active))
	}
	stopped = cache.GetStopped()
	if len(stopped) != 2 {
		t.Fatalf("expected 2 stopped, got %d", len(stopped))
	}
}

func TestTaskCache_RemoveTask_RoutesByPrefix(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_1", Status: "active"}, "active")
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_1", Status: "active"}},
		nil, nil,
	)

	cache.RemoveTask("sg_1")

	active := cache.GetActive()
	if len(active) != 1 || active[0].GID != "ar_1" {
		t.Fatalf("expected only ar_1 in active after sg_1 removal, got %v", gidsFromTasks(active))
	}

	cache.RemoveTask("ar_1")
	active = cache.GetActive()
	if len(active) != 0 {
		t.Fatalf("expected active empty, got %v", gidsFromTasks(active))
	}
}

func TestTaskCache_PatchTaskProgress_RoutesByPrefix(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_1", Status: "active", CompletedLength: "0", DownloadSpeed: "0", TotalLength: "1000"}, "active")
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_1", Status: "active", CompletedLength: "0", DownloadSpeed: "0", TotalLength: "2000"}},
		nil, nil,
	)

	cache.PatchTaskProgress("sg_1", "500", "100", "1000")

	sgFound := false
	cache.sgMu.RLock()
	for _, task := range cache.sgActive {
		if task.GID == "sg_1" {
			if task.CompletedLength != "500" || task.DownloadSpeed != "100" || task.TotalLength != "1000" {
				t.Fatalf("expected sg_1 patched, got %#v", task)
			}
			sgFound = true
		}
	}
	cache.sgMu.RUnlock()
	if !sgFound {
		t.Fatal("expected sg_1 in sgActive")
	}

	cache.arMu.RLock()
	for _, task := range cache.arActive {
		if task.GID == "ar_1" {
			if task.CompletedLength != "0" {
				t.Fatalf("expected ar_1 unpatched, got %#v", task)
			}
		}
	}
	cache.arMu.RUnlock()
}

func TestTaskCache_NoPrefixGidDefaultsToAr(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "plain-1", Status: "active"}},
		nil, nil,
	)

	cache.arMu.RLock()
	if len(cache.arActive) != 1 || cache.arActive[0].GID != "plain-1" {
		t.Fatalf("expected plain-1 in arActive, got %v", cache.arActive)
	}
	cache.arMu.RUnlock()

	cache.sgMu.RLock()
	if len(cache.sgActive) != 0 {
		t.Fatalf("expected sgActive empty, got %v", cache.sgActive)
	}
	cache.sgMu.RUnlock()

	cache.MoveTaskToStopped("plain-1", "complete")
	stopped := cache.GetStopped()
	if len(stopped) != 1 || stopped[0].GID != "plain-1" {
		t.Fatalf("expected plain-1 in stopped, got %v", stopped)
	}

	cache.RemoveTask("plain-1")
	if len(cache.GetStopped()) != 0 {
		t.Fatal("expected stopped empty after remove")
	}
}

func TestTaskCache_DuplicateGidFilter(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.UpdateFromAria2(
		[]rpc.Task{
			{GID: "ar_1", Status: "active"},
			{GID: "ar_1", Status: "active"},
			{GID: "ar_1", Status: "active"},
		},
		nil, nil,
	)

	active := cache.GetActive()
	if len(active) != 3 {
		t.Fatalf("expected 3 entries (full replace, no dedup), got %d", len(active))
	}
	for _, task := range active {
		if task.GID != "ar_1" {
			t.Fatalf("expected all ar_1, got %s", task.GID)
		}
	}
}

func TestTaskCache_EnrichTasks_WorksWithSplitSlices(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	group := testDownloadGroup("dg-split-enrich")
	cache.AddSgTask(rpc.Task{GID: "sg_lite", Status: "active"}, "active")
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_lite", Status: "active"}},
		nil, nil,
	)
	cache.SetTaskGroup("sg_lite", group)
	cache.SetTaskGroup("ar_lite", group)

	tasks := cache.GetActive()
	cache.EnrichTasks(tasks)

	if tasks[0].DownloadGroup == nil || tasks[0].DownloadGroup.ID != group.ID {
		t.Fatalf("expected sg_lite enriched with group, got %#v", tasks[0].DownloadGroup)
	}
	if tasks[1].DownloadGroup == nil || tasks[1].DownloadGroup.ID != group.ID {
		t.Fatalf("expected ar_lite enriched with group, got %#v", tasks[1].DownloadGroup)
	}
}

func TestTaskCache_LiteTaskNotOverwrittenByUpdateFromAria2(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.SetTaskGroup("sg_full", testDownloadGroup("dg-lite-update"))
	cache.ensureMetadata(rpc.Task{
		GID:         "sg_full",
		Dir:         "/downloads",
		TotalLength: "2048",
		Files: []rpc.File{{
			Path: "/downloads/file.bin",
			Uris: []rpc.Uri{{Uri: "https://example.com/file.bin"}},
		}},
	})

	if !cache.HasValidMetadata("sg_full") {
		t.Fatal("expected valid metadata before UpdateFromAria2")
	}

	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "sg_full", Status: "active"}},
		nil, nil,
	)

	if !cache.HasValidMetadata("sg_full") {
		t.Fatal("expected valid metadata preserved after UpdateFromAria2")
	}
	tasks := []rpc.Task{{GID: "sg_full", Status: "active"}}
	cache.EnrichTasks(tasks)
	if len(tasks[0].Files) == 0 || tasks[0].Files[0].Path != "/downloads/file.bin" {
		t.Fatalf("expected enriched files preserved, got %#v", tasks[0].Files)
	}
}

func TestTaskCache_CrossListMovePreservesMetadata(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{GID: "sg_move", Status: "active"}, "active")
	cache.ensureMetadata(rpc.Task{
		GID:         "sg_move",
		Dir:         "/downloads",
		TotalLength: "1024",
		Files: []rpc.File{{
			Path: "/downloads/move.bin",
			Uris: []rpc.Uri{{Uri: "https://example.com/move.bin"}},
		}},
	})

	if !cache.HasValidMetadata("sg_move") {
		t.Fatal("expected valid metadata before move")
	}

	cache.MoveTaskToStopped("sg_move", "complete")

	if !cache.HasValidMetadata("sg_move") {
		t.Fatal("expected valid metadata preserved after MoveTaskToStopped")
	}
	meta := cache.GetMetadata("sg_move")
	if meta == nil || len(meta.Files) == 0 || meta.Files[0] != "/downloads/move.bin" {
		t.Fatalf("expected metadata preserved after cross-list move, got %#v", meta)
	}
}

func gidsFromTasks(tasks []rpc.Task) []string {
	gids := make([]string, len(tasks))
	for i, task := range tasks {
		gids[i] = task.GID
	}
	return gids
}

func containsGid(gids []string, gid string) bool {
	for _, g := range gids {
		if g == gid {
			return true
		}
	}
	return false
}

// --- AddSgTask tests ---

func TestTaskCache_AddSgTask_NewToActive(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{
		GID:           "sg_new-1",
		Status:        "active",
		TotalLength:   "1000",
		DownloadSpeed: "100",
	}, "active")

	active := cache.GetActive()
	if !containsGid(gidsFromTasks(active), "sg_new-1") {
		t.Fatalf("expected sg_new-1 in active, got %v", gidsFromTasks(active))
	}
}

func TestTaskCache_AddSgTask_NewToWaiting(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{
		GID:    "sg_wait-1",
		Status: "waiting",
	}, "waiting")

	waiting := cache.GetWaiting()
	if !containsGid(gidsFromTasks(waiting), "sg_wait-1") {
		t.Fatalf("expected sg_wait-1 in waiting, got %v", gidsFromTasks(waiting))
	}
}

func TestTaskCache_AddSgTask_NewToStopped(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{
		GID:    "sg_stop-1",
		Status: "complete",
	}, "stopped")

	stopped := cache.GetStopped()
	if !containsGid(gidsFromTasks(stopped), "sg_stop-1") {
		t.Fatalf("expected sg_stop-1 in stopped, got %v", gidsFromTasks(stopped))
	}
}

func TestTaskCache_AddSgTask_UpdatesExistingInPlace(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{
		GID:           "sg_upd-1",
		Status:        "active",
		TotalLength:   "1000",
		DownloadSpeed: "100",
	}, "active")

	// Update with new speed
	cache.AddSgTask(rpc.Task{
		GID:           "sg_upd-1",
		Status:        "active",
		DownloadSpeed: "500",
	}, "active")

	active := cache.GetActive()
	if len(active) != 1 {
		t.Fatalf("expected 1 active task, got %d", len(active))
	}
	if active[0].DownloadSpeed != "500" {
		t.Errorf("DownloadSpeed = %s, want 500", active[0].DownloadSpeed)
	}
	if active[0].TotalLength != "1000" {
		t.Errorf("TotalLength = %s, want 1000 (preserved from prior)", active[0].TotalLength)
	}
}

func TestTaskCache_AddSgTask_MovesBetweenSlices(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.AddSgTask(rpc.Task{
		GID:    "sg_move-1",
		Status: "active",
	}, "active")

	// Move to waiting
	cache.AddSgTask(rpc.Task{
		GID:    "sg_move-1",
		Status: "paused",
	}, "waiting")

	active := cache.GetActive()
	for _, task := range active {
		if task.GID == "sg_move-1" {
			t.Fatal("expected sg_move-1 NOT in active after move to waiting")
		}
	}
	waiting := cache.GetWaiting()
	if !containsGid(gidsFromTasks(waiting), "sg_move-1") {
		t.Fatalf("expected sg_move-1 in waiting after move, got %v", gidsFromTasks(waiting))
	}

	// Move to stopped
	cache.AddSgTask(rpc.Task{
		GID:    "sg_move-1",
		Status: "complete",
	}, "stopped")

	waiting = cache.GetWaiting()
	for _, task := range waiting {
		if task.GID == "sg_move-1" {
			t.Fatal("expected sg_move-1 NOT in waiting after move to stopped")
		}
	}
	stopped := cache.GetStopped()
	if !containsGid(gidsFromTasks(stopped), "sg_move-1") {
		t.Fatalf("expected sg_move-1 in stopped after move, got %v", gidsFromTasks(stopped))
	}
}

func TestTaskCache_AddSgTask_DoesNotTouchAriaSlices(t *testing.T) {
	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.UpdateFromAria2(
		[]rpc.Task{{GID: "ar_a1", Status: "active", DownloadSpeed: "50"}},
		nil, nil,
	)

	cache.AddSgTask(rpc.Task{
		GID:    "sg_new-1",
		Status: "active",
	}, "active")

	active := cache.GetActive()
	sgCount := 0
	arCount := 0
	for _, task := range active {
		if task.GID == "sg_new-1" {
			sgCount++
		}
		if task.GID == "ar_a1" {
			arCount++
		}
	}
	if sgCount != 1 {
		t.Errorf("expected 1 sg_ task in active, got %d", sgCount)
	}
	if arCount != 1 {
		t.Errorf("expected 1 ar_ task in active, got %d", arCount)
	}
}

func newCleanupTestCache() *TaskCache {
	return &TaskCache{
		metadata:         make(map[string]*TaskMetadata),
		pendingStartGids: make(map[string]time.Time),
	}
}

func TestTaskCache_CleanupMetadata_EvictsOrphans(t *testing.T) {
	cache := newCleanupTestCache()
	cache.metadata["ar_1"] = &TaskMetadata{GID: "ar_1", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}
	cache.metadata["ar_2"] = &TaskMetadata{GID: "ar_2", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}
	cache.metadata["ar_3"] = &TaskMetadata{GID: "ar_3", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}

	evicted := cache.CleanupMetadata(map[string]bool{"ar_1": true})

	if evicted != 2 {
		t.Fatalf("expected 2 evicted, got %d", evicted)
	}
	if cache.GetMetadata("ar_1") == nil {
		t.Fatal("expected ar_1 retained")
	}
	if cache.GetMetadata("ar_2") != nil {
		t.Fatal("expected ar_2 evicted")
	}
	if cache.GetMetadata("ar_3") != nil {
		t.Fatal("expected ar_3 evicted")
	}
}

func TestTaskCache_CleanupMetadata_ProtectsPendingStart(t *testing.T) {
	cache := newCleanupTestCache()
	cache.metadata["ar_orphan"] = &TaskMetadata{GID: "ar_orphan", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}
	cache.metadata["ar_pending"] = &TaskMetadata{GID: "ar_pending", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}
	cache.markPendingStart("ar_pending")

	evicted := cache.CleanupMetadata(map[string]bool{})

	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}
	if cache.GetMetadata("ar_orphan") != nil {
		t.Fatal("expected ar_orphan evicted")
	}
	if cache.GetMetadata("ar_pending") == nil {
		t.Fatal("expected ar_pending retained")
	}
}

func TestTaskCache_CleanupMetadata_ProtectsRecentFetchedAt(t *testing.T) {
	cache := newCleanupTestCache()
	cache.metadata["ar_recent"] = &TaskMetadata{GID: "ar_recent", FetchedAt: time.Now()}
	cache.metadata["ar_old"] = &TaskMetadata{GID: "ar_old", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}

	evicted := cache.CleanupMetadata(map[string]bool{})

	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}
	if cache.GetMetadata("ar_recent") == nil {
		t.Fatal("expected ar_recent retained by grace")
	}
	if cache.GetMetadata("ar_old") != nil {
		t.Fatal("expected ar_old evicted")
	}
}

func TestTaskCache_CleanupMetadata_SkipsSgGids(t *testing.T) {
	cache := newCleanupTestCache()
	cache.metadata["sg_1"] = &TaskMetadata{GID: "sg_1", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}
	cache.metadata["ar_orphan"] = &TaskMetadata{GID: "ar_orphan", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}

	evicted := cache.CleanupMetadata(map[string]bool{})

	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}
	if cache.GetMetadata("sg_1") == nil {
		t.Fatal("expected sg_1 retained")
	}
	if cache.GetMetadata("ar_orphan") != nil {
		t.Fatal("expected ar_orphan evicted")
	}
}

func TestTaskCache_CleanupMetadata_DoesNotTouchGroupStore(t *testing.T) {
	dir := t.TempDir()
	ResetTaskGroupStoreForTest(filepath.Join(dir, "groups.json"), true)
	t.Cleanup(func() {
		ResetTaskGroupStoreForTest("", true)
	})

	cache := newCleanupTestCache()
	group := testDownloadGroup("dg-cleanup")
	cache.RegisterTaskGroup("ar_orphan", group)
	cache.metadata["ar_orphan"].FetchedAt = time.Now().Add(-2 * metadataCleanupGrace)

	evicted := cache.CleanupMetadata(map[string]bool{})

	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}
	if cache.GetMetadata("ar_orphan") != nil {
		t.Fatal("expected ar_orphan metadata evicted")
	}
	if got := GetStoredTaskGroup("ar_orphan"); got == nil || got.ID != group.ID {
		t.Fatalf("expected durable group store intact, got %#v", got)
	}
}

func TestTaskCache_CleanupMetadata_KeepsActiveInAllLists(t *testing.T) {
	cache := newCleanupTestCache()
	cache.metadata["ar_a"] = &TaskMetadata{GID: "ar_a", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}
	cache.metadata["ar_w"] = &TaskMetadata{GID: "ar_w", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}
	cache.metadata["ar_s"] = &TaskMetadata{GID: "ar_s", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}

	evicted := cache.CleanupMetadata(map[string]bool{"ar_a": true, "ar_w": true, "ar_s": true})

	if evicted != 0 {
		t.Fatalf("expected 0 evicted, got %d", evicted)
	}
	if cache.GetMetadata("ar_a") == nil || cache.GetMetadata("ar_w") == nil || cache.GetMetadata("ar_s") == nil {
		t.Fatal("expected all retained")
	}
}
