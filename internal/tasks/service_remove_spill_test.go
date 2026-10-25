package tasks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/monitor"
)

// waitFileAbsent polls until os.Stat reports the path gone — the removal
// cleanup runs asynchronously after getRemoveFileCleanupDelay.
func waitFileAbsent(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still exists after cleanup", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func setupCleanupRemovedTaskStub(t *testing.T) {
	t.Helper()
	monitor.ResetDownloadGroupNamerForTest()
	origCache := monitor.Cache
	origTracker := monitor.State.GetTracker()
	origMonitor := monitor.State.GetMonitor()
	origDelay := getRemoveFileCleanupDelay()

	monitor.Cache = monitor.NewTaskCacheForTest()
	monitor.State.SetTracker(nil)
	monitor.State.SetMonitor(nil)
	setRemoveFileCleanupDelay(10 * time.Millisecond)

	t.Cleanup(func() {
		monitor.Cache = origCache
		monitor.State.SetTracker(origTracker)
		monitor.State.SetMonitor(origMonitor)
		setRemoveFileCleanupDelay(origDelay)
		monitor.ResetDownloadGroupNamerForTest()
	})
}

func TestCleanupRemovedTask_RemovesSurgeSpillArena(t *testing.T) {
	setupCleanupRemovedTaskStub(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "video.ts")
	spill := filepath.Join(dir, ".hlsrawid.spill")
	sidecar := target + ".aria2"
	for _, p := range []string{target, spill, sidecar} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	cleanupRemovedTask("sg_hlsrawid", removalTarget{path: target}, false)

	waitFileAbsent(t, spill)
	waitFileAbsent(t, sidecar)
	// Keep-file removal never touches the payload itself.
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("keep-file removal deleted the payload: %v", err)
	}
}

func TestCleanupRemovedTask_RemovesSpillArenaWithDeleteFile(t *testing.T) {
	setupCleanupRemovedTaskStub(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "video.ts")
	spill := filepath.Join(dir, ".hlsrawid.spill")
	for _, p := range []string{target, spill} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	cleanupRemovedTask("sg_hlsrawid", removalTarget{path: target}, true)

	waitFileAbsent(t, target)
	waitFileAbsent(t, spill)
}

func TestCleanupRemovedTask_LeavesNonSurgeGIDSpill(t *testing.T) {
	setupCleanupRemovedTaskStub(t)

	dir := t.TempDir()
	target := filepath.Join(dir, "video.ts")
	// An aria2 GID never produces a .<gid>.spill sidecar; a stray file
	// bearing that name is not ours to delete.
	spill := filepath.Join(dir, ".abc123.spill")
	for _, p := range []string{target, spill} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}

	cleanupRemovedTask("abc123", removalTarget{path: target}, false)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(spill); err != nil {
		t.Fatalf("non-Surge GID deleted a foreign .spill file: %v", err)
	}
}

func TestSurgeSpillID(t *testing.T) {
	cases := []struct {
		gid   string
		want  string
		valid bool
	}{
		{"sg_hlsrawid", "hlsrawid", true},
		{"sg_a-b.c_d", "a-b.c_d", true},
		{"abc123", "", false},
		{"sg_", "", false},
		{"sg_ ", "", false},
		{"sg_ id", "", false},
		// The whole-GID trim runs before the raw checks, so a trailing
		// space normalizes away — a trailing dot does not.
		{"sg_id ", "id", true},
		{"sg_id.", "", false},
		{"sg_a..b", "", false},
		{"sg_a/b", "", false},
		{`sg_a\b`, "", false},
		{"sg_a:b", "", false},
		{"  sg_padded  ", "padded", true},
	}
	for _, tc := range cases {
		got, ok := surgeSpillID(tc.gid)
		if got != tc.want || ok != tc.valid {
			t.Fatalf("surgeSpillID(%q) = (%q, %v), want (%q, %v)", tc.gid, got, ok, tc.want, tc.valid)
		}
	}
}
