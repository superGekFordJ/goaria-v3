package monitor

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

func newChunkTestMonitor() *Monitor {
	hub := events.NewHub(nil)
	return &Monitor{
		hub:            hub,
		pusher:         NewPusher(hub),
		tracker:        NewTaskTracker(),
		engine:         &mockSafeEngine{},
		chunkSnapshots: NewChunkSnapshotCache(),
		deletedGids:    make(map[string]time.Time),
	}
}

func chunkEvent(id string, bitmap []byte, width int, size int64) surgeEvents.DownloadEvent {
	return surgeEvents.DownloadEvent{
		DownloadID:  id,
		Downloaded:  500,
		Total:       1000,
		ChunkBitmap: bitmap,
		BitmapWidth: width,
		ChunkSize:   size,
	}
}

func batchEvent(subs ...surgeEvents.DownloadEvent) surgeEvents.DownloadEvent {
	return surgeEvents.DownloadEvent{Type: surgeEvents.EventBatchProgress, BatchEvents: subs}
}

func TestChunkSnapshotCache_SetGetCopyRemove(t *testing.T) {
	c := NewChunkSnapshotCache()
	if _, ok := c.Get("sg_x"); ok {
		t.Fatal("expected miss on empty cache")
	}
	c.Set("", []byte{1}, 4, 10)
	c.Set("sg_x", nil, 4, 10)
	c.Set("sg_x", []byte{1}, 0, 10)
	if _, ok := c.Get("sg_x"); ok {
		t.Fatal("defensive Set inputs must be ignored")
	}

	c.Set("sg_x", []byte{26, 36}, 8, 1<<20)
	snap, ok := c.Get("sg_x")
	if !ok {
		t.Fatal("expected snapshot hit")
	}
	if snap.ChunkCount != 8 || snap.ChunkSize != 1<<20 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !bytes.Equal(snap.Bitmap, []byte{26, 36}) {
		t.Fatalf("bitmap = %v", snap.Bitmap)
	}
	if _, ok := c.UpdatedAt("sg_x"); !ok {
		t.Fatal("expected UpdatedAt")
	}

	// Mutating the returned copy must not affect the stored snapshot.
	snap.Bitmap[0] = 0xFF
	again, _ := c.Get("sg_x")
	if again.Bitmap[0] == 0xFF {
		t.Fatal("Get must return an independent bitmap copy")
	}

	c.Remove("sg_x")
	if _, ok := c.Get("sg_x"); ok {
		t.Fatal("expected miss after Remove")
	}
	if _, ok := c.UpdatedAt("sg_x"); ok {
		t.Fatal("expected UpdatedAt miss after Remove")
	}
}

func TestChunkSnapshotCache_NilSafe(t *testing.T) {
	var c *ChunkSnapshotCache
	c.Set("sg_x", []byte{1}, 4, 10)
	if _, ok := c.Get("sg_x"); ok {
		t.Fatal("nil cache Get must miss")
	}
	c.Remove("sg_x")
	if _, ok := c.UpdatedAt("sg_x"); ok {
		t.Fatal("nil cache UpdatedAt must miss")
	}
	var m *Monitor
	if m.GetChunkSnapshots() != nil {
		t.Fatal("nil monitor must return nil cache")
	}
}

func TestHandleSurgeEvent_BatchProgressWritesChunkSnapshot(t *testing.T) {
	m := newChunkTestMonitor()
	m.handleSurgeEvent(batchEvent(chunkEvent("dl1", []byte{26, 36}, 8, 1<<20)))

	snap, ok := m.chunkSnapshots.Get("sg_dl1")
	if !ok {
		t.Fatal("expected snapshot for sg_dl1")
	}
	if snap.ChunkCount != 8 || snap.ChunkSize != 1<<20 || !bytes.Equal(snap.Bitmap, []byte{26, 36}) {
		t.Fatalf("snapshot = %+v", snap)
	}

	// Frames without chunk fields must not clear or overwrite the snapshot.
	m.handleSurgeEvent(batchEvent(surgeEvents.DownloadEvent{DownloadID: "dl1", Downloaded: 900, Total: 1000}))
	again, ok := m.chunkSnapshots.Get("sg_dl1")
	if !ok || !bytes.Equal(again.Bitmap, []byte{26, 36}) {
		t.Fatalf("field-less batch clobbered snapshot: %+v ok=%v", again, ok)
	}

	// A later frame carrying fields replaces the snapshot.
	m.handleSurgeEvent(batchEvent(chunkEvent("dl1", []byte{0xAA}, 4, 2<<20)))
	snap, ok = m.chunkSnapshots.Get("sg_dl1")
	if !ok || snap.ChunkCount != 4 || snap.ChunkSize != 2<<20 || !bytes.Equal(snap.Bitmap, []byte{0xAA}) {
		t.Fatalf("snapshot not refreshed: %+v ok=%v", snap, ok)
	}
}

// Single EventProgress has no production sender, but the intake stays armed
// for compatibility.
func TestHandleSurgeEvent_SingleProgressWritesChunkSnapshot(t *testing.T) {
	m := newChunkTestMonitor()
	ev := chunkEvent("dl2", []byte{0x55}, 4, 512)
	ev.Type = surgeEvents.EventProgress
	m.handleSurgeEvent(ev)

	if _, ok := m.chunkSnapshots.Get("sg_dl2"); !ok {
		t.Fatal("expected defensive snapshot write for EventProgress")
	}
}

// Paused tasks keep their last snapshot: the engine stops emitting progress
// for paused downloads, so the bitmap freezes at its last attached frame.
func TestHandleSurgeEvent_PausedSnapshotFrozen(t *testing.T) {
	m := newChunkTestMonitor()
	m.handleSurgeEvent(batchEvent(chunkEvent("dl3", []byte{0x11}, 4, 512)))
	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "dl3"})

	if _, ok := m.chunkSnapshots.Get("sg_dl3"); !ok {
		t.Fatal("paused snapshot must remain readable (frozen)")
	}
}

// EventStarted marks a new download generation: a stale bitmap under a reused
// gid must not leak into it.
func TestHandleSurgeEvent_StartedClearsStaleSnapshot(t *testing.T) {
	m := newChunkTestMonitor()
	m.handleSurgeEvent(batchEvent(chunkEvent("dl4", []byte{0x11}, 4, 512)))
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventStarted, DownloadID: "dl4", URL: "http://x/f", Total: 1000,
	})
	if _, ok := m.chunkSnapshots.Get("sg_dl4"); ok {
		t.Fatal("EventStarted must clear a stale snapshot for the new generation")
	}
}

func TestHandleSurgeEvent_TerminalClearsSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   surgeEvents.DownloadEvent
	}{
		{"complete", surgeEvents.DownloadEvent{Type: surgeEvents.EventComplete, DownloadID: "dl", Total: 1000}},
		{"error", surgeEvents.DownloadEvent{Type: surgeEvents.EventError, DownloadID: "dl"}},
		{"removed", surgeEvents.DownloadEvent{Type: surgeEvents.EventRemoved, DownloadID: "dl"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newChunkTestMonitor()
			resetCacheSg()
			t.Cleanup(resetCacheSg)
			Cache.AddSgTask(rpc.Task{GID: "sg_dl", Status: "active", TotalLength: "1000"}, "active")
			m.tracker.EnsureTrackedFromEvent("sg_dl", 1000, "http://x/f", 4, "active")
			m.handleSurgeEvent(batchEvent(chunkEvent("dl", []byte{0x55}, 4, 512)))
			if _, ok := m.chunkSnapshots.Get("sg_dl"); !ok {
				t.Fatal("setup: expected snapshot before terminal event")
			}

			m.handleSurgeEvent(tc.ev)
			if _, ok := m.chunkSnapshots.Get("sg_dl"); ok {
				t.Fatalf("%s must clear the chunk snapshot", tc.name)
			}
		})
	}
}

// A terminal for an untracked gid still clears the snapshot:
// MarkCompleteFromEvent returns nil but the clear sits above it.
func TestMarkCompleteAndHandleLocked_UntrackedStillClearsSnapshot(t *testing.T) {
	m := newChunkTestMonitor()
	m.chunkSnapshots.Set("sg_ghost", []byte{0x55}, 4, 512)

	m.tracker.RunUnderLifecycle("sg_ghost", func() {
		m.markCompleteAndHandleLocked("sg_ghost", "complete", nil)
	})
	if _, ok := m.chunkSnapshots.Get("sg_ghost"); ok {
		t.Fatal("terminal accept must clear snapshot even when tracker never tracked the gid")
	}
}

func TestInvalidateTask_ClearsChunkSnapshot(t *testing.T) {
	m := newChunkTestMonitor()
	m.chunkSnapshots.Set("sg_rm", []byte{0x55}, 4, 512)

	m.InvalidateTask("sg_rm")
	if _, ok := m.chunkSnapshots.Get("sg_rm"); ok {
		t.Fatal("InvalidateTask must clear the chunk snapshot")
	}
}

func TestReconcileSurgeCache_VanishClearsChunkSnapshot(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	m.chunkSnapshots = NewChunkSnapshotCache()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "active"}, "active")
	m.chunkSnapshots.Set("sg_task1", []byte{0x55}, 4, 512)
	reader.setLists(nil, nil, nil)

	m.reconcileSurgeCache()
	if _, ok := m.chunkSnapshots.Get("sg_task1"); ok {
		t.Fatal("reconcile vanish must clear the chunk snapshot")
	}
}

// A missed terminal picked up by reconcile (engine reports stopped while the
// cache still holds the task) funnels through markCompleteAndHandleLocked and
// must clear the snapshot too.
func TestReconcileSurgeCache_MissedTerminalClearsChunkSnapshot(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	m.chunkSnapshots = NewChunkSnapshotCache()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "active", TotalLength: "1000"}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/f.zip", 4, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{GID: "sg_task1", Files: []string{"/d/f.zip"}, Dir: "/d"}
	m.chunkSnapshots.Set("sg_task1", []byte{0x55}, 4, 512)

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})
	m.reconcileSurgeCache()

	if _, ok := m.chunkSnapshots.Get("sg_task1"); ok {
		t.Fatal("reconcile terminal accept must clear the chunk snapshot")
	}
}

// Concurrent batch writes and pull reads must stay race-free under -race.
func TestChunkSnapshotCache_ConcurrentReadWrite(t *testing.T) {
	m := newChunkTestMonitor()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			m.handleSurgeEvent(batchEvent(chunkEvent("dlc", []byte{byte(i)}, 4, 512)))
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			_, _ = m.chunkSnapshots.Get("sg_dlc")
			_, _ = m.chunkSnapshots.UpdatedAt("sg_dlc")
		}
	}()
	wg.Wait()
}
