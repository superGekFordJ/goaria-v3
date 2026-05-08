package monitor

import (
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
