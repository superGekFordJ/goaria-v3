package monitor

import (
	"sync"
	"testing"

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
