package monitor

import (
	"path/filepath"
	"testing"

	"goaria-v3/internal/rpc"
)

func setupTaskGroupStoreTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "download_groups.json")
	ResetTaskGroupStoreForTest(path, true)
	t.Cleanup(func() {
		ResetTaskGroupStoreForTest("", true)
	})
	return path
}

func TestTaskGroupStore_PersistsLoadsAndHydratesTasks(t *testing.T) {
	setupTaskGroupStoreTest(t)
	group := testDownloadGroup("dg-store")

	RegisterTaskGroup("gid-store", group)
	ResetTaskGroupStoreForTest(groupStore.path, true)
	LoadTaskGroups()

	loaded := GetStoredTaskGroup("gid-store")
	if loaded == nil || loaded.ID != group.ID {
		t.Fatalf("expected stored group after reload, got %#v", loaded)
	}
	tasks := []rpc.Task{{GID: "gid-store", Status: "active"}}
	HydrateTaskGroups(tasks)
	if tasks[0].DownloadGroup == nil || tasks[0].DownloadGroup.ID != group.ID {
		t.Fatalf("expected hydrated task group, got %#v", tasks[0].DownloadGroup)
	}
}

func TestTaskGroupStore_RemovesAndClearsGroups(t *testing.T) {
	setupTaskGroupStoreTest(t)
	RegisterTaskGroup("gid-one", testDownloadGroup("dg-one"))
	RegisterTaskGroup("gid-two", testDownloadGroup("dg-two"))

	RemoveTaskGroup("gid-one")
	if got := GetStoredTaskGroup("gid-one"); got != nil {
		t.Fatalf("expected gid-one removed, got %#v", got)
	}
	if got := GetStoredTaskGroup("gid-two"); got == nil {
		t.Fatal("expected gid-two to remain")
	}

	ClearTaskGroups()
	if got := GetStoredTaskGroup("gid-two"); got != nil {
		t.Fatalf("expected all groups cleared, got %#v", got)
	}
}
