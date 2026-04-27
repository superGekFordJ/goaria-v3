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
