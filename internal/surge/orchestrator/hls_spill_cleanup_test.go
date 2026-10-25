package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// spillArenaPath is the deterministic hidden arena name sitting next to the
// download's destination: ".<raw id>.spill" in the same directory.
func spillArenaPath(dir, id string) string {
	return filepath.Join(dir, "."+id+".spill")
}

func waitSpillArena(t *testing.T, path string, present bool) {
	t.Helper()
	msg := "spill arena on disk"
	if !present {
		msg = "spill arena deleted"
	}
	waitForCond(t, 10*time.Second, msg, func() bool {
		_, err := os.Stat(path)
		if present {
			return err == nil
		}
		return os.IsNotExist(err)
	})
}

// TestHLSCancel_RemovesSpillArena cancels a live run whose out-of-order
// pending set has already overflowed into a real spill arena: the
// EventRemoved path must delete the hidden .<id>.spill sidecar together
// with the .surge working file.
func TestHLSCancel_RemovesSpillArena(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	g := newGatedSpillOrigin(t, nil)
	defer g.openGate()

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, g.o.url("/media.m3u8"), dir, "video.ts")
	g.waitOutOfOrder(t)

	spill := spillArenaPath(dir, id)
	waitSpillArena(t, spill, true)

	if err := s.mgr.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitForCond(t, 10*time.Second, "EventRemoved observed", func() bool {
		return s.countEvents(id, types.EventRemoved) >= 1
	})
	waitSpillArena(t, spill, false)

	surgePath := filepath.Join(dir, "video.ts"+types.IncompleteSuffix)
	if _, err := os.Stat(surgePath); !os.IsNotExist(err) {
		t.Fatalf(".surge survived cancel: %v", err)
	}
	if states, _ := store.LoadStates([]string{id}); states[id] != nil {
		t.Fatal("detail record survived cancel")
	}
}

// TestHLSCancelPaused_RemovesSpillArena pauses first so the checkpoint and
// arena are durable on disk, then removes the paused task: the same
// EventRemoved sweep must take the arena even though the worker already
// left its file handles.
func TestHLSCancelPaused_RemovesSpillArena(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	g := newGatedSpillOrigin(t, nil)
	defer g.openGate()

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, g.o.url("/media.m3u8"), dir, "video.ts")
	g.waitOutOfOrder(t)

	if err := s.mgr.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	s.waitMasterStatus(t, id, "paused", 10*time.Second)
	assertSpillCheckpoint(t, id, g.spillPendingSum(), spillResumeSegs-2)

	spill := spillArenaPath(dir, id)
	if _, err := os.Stat(spill); err != nil {
		t.Fatalf("durable spill arena missing before removal: %v", err)
	}

	if err := s.mgr.Cancel(id); err != nil {
		t.Fatalf("Cancel paused task: %v", err)
	}
	waitForCond(t, 10*time.Second, "EventRemoved observed", func() bool {
		return s.countEvents(id, types.EventRemoved) >= 1
	})
	waitSpillArena(t, spill, false)

	surgePath := filepath.Join(dir, "video.ts"+types.IncompleteSuffix)
	if _, err := os.Stat(surgePath); !os.IsNotExist(err) {
		t.Fatalf(".surge survived paused-task removal: %v", err)
	}
	if states, _ := store.LoadStates([]string{id}); states[id] != nil {
		t.Fatal("detail record survived paused-task removal")
	}
}

// TestRemoveSpillFile pins the helper contract: it deletes exactly the
// enumerated ".<id>.spill" file, treats missing files and empty inputs as
// success, and refuses IDs that could escape the destination directory.
func TestRemoveSpillFile(t *testing.T) {
	dir := t.TempDir()
	destPath := filepath.Join(dir, "video.ts")

	t.Run("deletes the enumerated arena", func(t *testing.T) {
		spill := filepath.Join(dir, ".hls-id.spill")
		if err := os.WriteFile(spill, []byte("ledger"), 0o644); err != nil {
			t.Fatalf("seed arena: %v", err)
		}
		if err := RemoveSpillFile(destPath, "hls-id"); err != nil {
			t.Fatalf("RemoveSpillFile: %v", err)
		}
		if _, err := os.Stat(spill); !os.IsNotExist(err) {
			t.Fatalf("arena survived: %v", err)
		}
	})

	t.Run("missing arena is success", func(t *testing.T) {
		if err := RemoveSpillFile(destPath, "hls-id"); err != nil {
			t.Fatalf("RemoveSpillFile on absent file: %v", err)
		}
	})

	t.Run("empty inputs are success", func(t *testing.T) {
		for _, tc := range [][2]string{{"", "id"}, {".", "id"}, {destPath, ""}} {
			if err := RemoveSpillFile(tc[0], tc[1]); err != nil {
				t.Fatalf("RemoveSpillFile(%q, %q): %v", tc[0], tc[1], err)
			}
		}
	})

	t.Run("illegal ids are refused", func(t *testing.T) {
		// A decoy one directory up must survive a traversal attempt.
		parent := filepath.Dir(dir)
		decoy := filepath.Join(parent, ".escape.spill")
		if err := os.WriteFile(decoy, []byte("keep"), 0o644); err != nil {
			t.Fatalf("seed decoy: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(decoy) })

		for _, id := range []string{"..", "a/b", `a\b`, "a..b", "id.", " id", "a:b"} {
			if err := RemoveSpillFile(destPath, id); err == nil {
				t.Fatalf("RemoveSpillFile accepted illegal id %q", id)
			}
		}
		if _, err := os.Stat(decoy); err != nil {
			t.Fatalf("traversal deleted the decoy: %v", err)
		}
	})
}
