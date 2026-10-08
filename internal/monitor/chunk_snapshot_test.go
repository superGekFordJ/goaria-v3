package monitor

import (
	"bytes"
	"reflect"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
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

func chunkEventProg(id string, bitmap []byte, width int, size int64, progress []int64) surgeEvents.DownloadEvent {
	ev := chunkEvent(id, bitmap, width, size)
	ev.ChunkProgress = progress
	return ev
}

func batchEvent(subs ...surgeEvents.DownloadEvent) surgeEvents.DownloadEvent {
	return surgeEvents.DownloadEvent{Type: surgeEvents.EventBatchProgress, BatchEvents: subs}
}

func TestChunkSnapshotCache_SetGetCopyRemove(t *testing.T) {
	c := NewChunkSnapshotCache()
	if _, ok := c.Get("sg_x"); ok {
		t.Fatal("expected miss on empty cache")
	}
	c.Set("", []byte{1}, 4, 10, nil)
	c.Set("sg_x", nil, 4, 10, nil)
	c.Set("sg_x", []byte{1}, 0, 10, nil)
	c.Set("sg_x", []byte{1}, 4, 0, nil)  // zero size: same-frame contract
	c.Set("sg_x", []byte{1}, 5, 10, nil) // count exceeds what one byte encodes
	if _, ok := c.Get("sg_x"); ok {
		t.Fatal("defensive Set inputs must be ignored")
	}

	src := []byte{26, 36}
	prog := []int64{0, 64, 128, 0, 0, 0, 0, 0}
	c.Set("sg_x", src, 8, 1<<20, prog)
	src[0] = 0xFF  // mutating the caller buffer must not leak into the store
	prog[0] = 9999 // same for the progress buffer
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
	wantProg := []int64{0, 64, 128, 0, 0, 0, 0, 0}
	if !reflect.DeepEqual(snap.ChunkProgress, wantProg) {
		t.Fatalf("progress = %v, want %v", snap.ChunkProgress, wantProg)
	}
	if _, ok := c.UpdatedAt("sg_x"); !ok {
		t.Fatal("expected UpdatedAt")
	}

	// Mutating the returned copy must not affect the stored snapshot.
	snap.Bitmap[0] = 0xFF
	snap.ChunkProgress[1] = 9999
	again, _ := c.Get("sg_x")
	if again.Bitmap[0] == 0xFF {
		t.Fatal("Get must return an independent bitmap copy")
	}
	if again.ChunkProgress[1] != 64 {
		t.Fatal("Get must return an independent progress copy")
	}

	// A frame without progress stores a valid snapshot with nil progress.
	c.Set("sg_y", []byte{0x11}, 4, 512, nil)
	noprog, ok := c.Get("sg_y")
	if !ok || noprog.ChunkProgress != nil {
		t.Fatalf("nil progress frame: %+v ok=%v", noprog, ok)
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
	c.Set("sg_x", []byte{1}, 4, 10, nil)
	if _, ok := c.Get("sg_x"); ok {
		t.Fatal("nil cache Get must miss")
	}
	c.Remove("sg_x")
	if _, ok := c.UpdatedAt("sg_x"); ok {
		t.Fatal("nil cache UpdatedAt must miss")
	}
	if c.GIDs() != nil {
		t.Fatal("nil cache GIDs must return nil")
	}
	var m *Monitor
	if m.GetChunkSnapshots() != nil {
		t.Fatal("nil monitor must return nil cache")
	}
}

// A zero-value cache (not built via NewChunkSnapshotCache) must not panic on
// first write: the map initializes lazily under the lock.
func TestChunkSnapshotCache_ZeroValue(t *testing.T) {
	var c ChunkSnapshotCache
	c.Set("sg_x", []byte{1}, 4, 512, nil)
	if _, ok := c.Get("sg_x"); !ok {
		t.Fatal("zero-value cache must accept Set")
	}
	c.Remove("sg_x")
	if _, ok := c.Get("sg_x"); ok {
		t.Fatal("expected miss after Remove")
	}
}

func TestHandleSurgeEvent_BatchProgressWritesChunkSnapshot(t *testing.T) {
	m := newChunkTestMonitor()
	m.handleSurgeEvent(batchEvent(chunkEventProg("dl1", []byte{26, 36}, 8, 1<<20, []int64{0, 11, 22, 33, 0, 0, 0, 0})))

	snap, ok := m.chunkSnapshots.Get("sg_dl1")
	if !ok {
		t.Fatal("expected snapshot for sg_dl1")
	}
	if snap.ChunkCount != 8 || snap.ChunkSize != 1<<20 || !bytes.Equal(snap.Bitmap, []byte{26, 36}) {
		t.Fatalf("snapshot = %+v", snap)
	}
	if want := []int64{0, 11, 22, 33, 0, 0, 0, 0}; !reflect.DeepEqual(snap.ChunkProgress, want) {
		t.Fatalf("progress = %v, want %v", snap.ChunkProgress, want)
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
	m.handleSurgeEvent(batchEvent(chunkEventProg("dl3", []byte{0x11}, 4, 512, []int64{10, 20, 30, 40})))
	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "dl3"})

	snap, ok := m.chunkSnapshots.Get("sg_dl3")
	if !ok {
		t.Fatal("paused snapshot must remain readable (frozen)")
	}
	if want := []int64{10, 20, 30, 40}; !reflect.DeepEqual(snap.ChunkProgress, want) {
		t.Fatalf("frozen progress = %v, want %v", snap.ChunkProgress, want)
	}
}

// EventStarted without a resume marker marks a new download generation: a
// stale bitmap under a reused gid must not leak into it.
func TestHandleSurgeEvent_StartedClearsStaleSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state *surgeEvents.DownloadRecord
	}{
		{"nil state", nil},
		{"non-resume state", &surgeEvents.DownloadRecord{IsResume: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newChunkTestMonitor()
			m.handleSurgeEvent(batchEvent(chunkEvent("dl4", []byte{0x11}, 4, 512)))
			m.handleSurgeEvent(surgeEvents.DownloadEvent{
				Type: surgeEvents.EventStarted, DownloadID: "dl4", URL: "http://x/f", Total: 1000,
				State: tc.state,
			})
			if _, ok := m.chunkSnapshots.Get("sg_dl4"); ok {
				t.Fatal("EventStarted must clear a stale snapshot for the new generation")
			}
		})
	}
}

// A self-reported resume is the same generation: the frozen snapshot stays
// until the next attached frame replaces it atomically — no empty window.
func TestHandleSurgeEvent_StartedKeepsSnapshotOnResume(t *testing.T) {
	m := newChunkTestMonitor()
	m.handleSurgeEvent(batchEvent(chunkEventProg("dl5", []byte{0x11}, 4, 512, []int64{1, 2, 3, 4})))
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventStarted, DownloadID: "dl5", URL: "http://x/f", Total: 1000,
		State: &surgeEvents.DownloadRecord{IsResume: true},
	})

	snap, ok := m.chunkSnapshots.Get("sg_dl5")
	if !ok {
		t.Fatal("resume EventStarted must keep the frozen snapshot")
	}
	if !bytes.Equal(snap.Bitmap, []byte{0x11}) || !reflect.DeepEqual(snap.ChunkProgress, []int64{1, 2, 3, 4}) {
		t.Fatalf("frozen frame mutated: %+v", snap)
	}

	// The next attached frame overwrites every field in one Set.
	m.handleSurgeEvent(batchEvent(chunkEventProg("dl5", []byte{0xEE}, 4, 1024, []int64{9, 9, 9, 9})))
	snap, ok = m.chunkSnapshots.Get("sg_dl5")
	if !ok || snap.ChunkSize != 1024 || !bytes.Equal(snap.Bitmap, []byte{0xEE}) ||
		!reflect.DeepEqual(snap.ChunkProgress, []int64{9, 9, 9, 9}) {
		t.Fatalf("attached frame must replace the snapshot atomically: %+v ok=%v", snap, ok)
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
	m.chunkSnapshots.Set("sg_ghost", []byte{0x55}, 4, 512, nil)

	m.tracker.RunUnderLifecycle("sg_ghost", func() {
		m.markCompleteAndHandleLocked("sg_ghost", "complete", nil)
	})
	if _, ok := m.chunkSnapshots.Get("sg_ghost"); ok {
		t.Fatal("terminal accept must clear snapshot even when tracker never tracked the gid")
	}
}

// The non-Locked wrapper matches: with no tracker there is no gate to take,
// but the snapshot still dies with the generation.
func TestMarkCompleteAndHandle_NilTrackerStillClearsSnapshot(t *testing.T) {
	m := &Monitor{chunkSnapshots: NewChunkSnapshotCache()}
	m.chunkSnapshots.Set("sg_ghost", []byte{0x55}, 4, 512, nil)

	m.markCompleteAndHandle("sg_ghost", "complete", nil)
	if _, ok := m.chunkSnapshots.Get("sg_ghost"); ok {
		t.Fatal("terminal accept must clear snapshot even with nil tracker")
	}
}

// The stopped-move path matches: no tracker skips the gate and seeding, but
// the Locked body still runs so the snapshot dies with the generation.
func TestMoveToStoppedAndHandle_NilTrackerStillClearsSnapshot(t *testing.T) {
	resetCacheSg()
	t.Cleanup(resetCacheSg)
	Cache.AddSgTask(rpc.Task{GID: "sg_ghost", Status: "active", TotalLength: "1000"}, "active")

	m := &Monitor{chunkSnapshots: NewChunkSnapshotCache()}
	m.chunkSnapshots.Set("sg_ghost", []byte{0x55}, 4, 512, nil)

	m.moveToStoppedAndHandle("sg_ghost", "complete", "", "", 0, nil)
	if _, ok := m.chunkSnapshots.Get("sg_ghost"); ok {
		t.Fatal("stopped move must clear snapshot even with nil tracker")
	}
	stopped := Cache.GetStopped()
	if len(stopped) != 1 || stopped[0].GID != "sg_ghost" || stopped[0].Status != "complete" {
		t.Fatalf("cache stopped move must still run: %#v", stopped)
	}
}

func TestInvalidateTask_ClearsChunkSnapshot(t *testing.T) {
	m := newChunkTestMonitor()
	m.chunkSnapshots.Set("sg_rm", []byte{0x55}, 4, 512, nil)

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
	m.chunkSnapshots.Set("sg_task1", []byte{0x55}, 4, 512, nil)
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
	m.chunkSnapshots.Set("sg_task1", []byte{0x55}, 4, 512, nil)

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})
	m.reconcileSurgeCache()

	if _, ok := m.chunkSnapshots.Get("sg_task1"); ok {
		t.Fatal("reconcile terminal accept must clear the chunk snapshot")
	}
}

// A snapshot key absent from every engine list is orphaned (its start and
// remove events were both lost); reconcile sweeps it. Engine-live keys
// survive: their generation can still produce frames.
func TestReconcileSurgeCache_OrphanSnapshotSweep(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	m.chunkSnapshots = NewChunkSnapshotCache()

	m.chunkSnapshots.Set("sg_orphan", []byte{0x55}, 4, 512, nil)
	m.chunkSnapshots.Set("sg_task1", []byte{0x55}, 4, 512, nil)
	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading", TotalLength: "1000"}}, nil, nil)

	m.reconcileSurgeCache()
	if _, ok := m.chunkSnapshots.Get("sg_orphan"); ok {
		t.Fatal("orphaned snapshot must be swept")
	}
	if _, ok := m.chunkSnapshots.Get("sg_task1"); !ok {
		t.Fatal("engine-live snapshot must survive the sweep")
	}
}

// The reconcile stopped-admit (gid missing from cache, engine reports
// stopped) funnels through markCompleteAndHandleLocked and clears too.
func TestReconcileSurgeCache_StoppedAdmitClearsChunkSnapshot(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	m.chunkSnapshots = NewChunkSnapshotCache()

	m.chunkSnapshots.Set("sg_task1", []byte{0x55}, 4, 512, nil)
	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()
	if _, ok := m.chunkSnapshots.Get("sg_task1"); ok {
		t.Fatal("reconcile stopped-admit must clear the chunk snapshot")
	}
}

// The history-terminal waiting admit bypasses the terminal funnel but is
// still a terminal accept: a snapshot left by a lost-event generation dies.
func TestReconcileSurgeCache_WaitingAdmitClearsChunkSnapshot(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	m.chunkSnapshots = NewChunkSnapshotCache()

	history.Add(history.HistoryEntry{GID: "sg_task1", Path: "/tmp/f.zip", Status: "complete"})
	m.chunkSnapshots.Set("sg_task1", []byte{0x55}, 4, 512, nil)
	reader.setLists(nil, []rpc.Task{{GID: "task1", Status: "waiting"}}, nil)

	m.reconcileSurgeCache()
	if _, ok := m.chunkSnapshots.Get("sg_task1"); ok {
		t.Fatal("history-terminal waiting admit must clear the chunk snapshot")
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
			m.handleSurgeEvent(batchEvent(chunkEventProg("dlc", []byte{byte(i)}, 4, 512, []int64{int64(i), 1, 2, 3})))
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			snap, _ := m.chunkSnapshots.Get("sg_dlc")
			if len(snap.ChunkProgress) > 0 {
				_ = snap.ChunkProgress[0]
			}
			_, _ = m.chunkSnapshots.UpdatedAt("sg_dlc")
		}
	}()
	wg.Wait()
}
