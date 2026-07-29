package concurrent

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

func TestPreferMaxOffsetSameSharedMaxOffset_ActiveFirstKeepsAdvanced(t *testing.T) {
	shared := new(atomic.Int64)
	shared.Store(600)

	// Active-first collect order still yields 600/400 under max-Offset.
	got := preferMaxOffsetSameSharedMaxOffset([]types.Task{
		{Offset: 600, Length: 400, SharedMaxOffset: shared},
		{Offset: 500, Length: 500, SharedMaxOffset: shared},
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Offset != 600 || got[0].Length != 400 {
		t.Fatalf("kept offset=%d length=%d, want 600/400", got[0].Offset, got[0].Length)
	}
	if got[0].SharedMaxOffset != shared {
		t.Fatal("kept task should retain SharedMaxOffset pointer until snapshot clear")
	}
}

func TestPreferMaxOffsetSameSharedMaxOffset_QueueFirstKeepsAdvanced(t *testing.T) {
	shared := new(atomic.Int64)
	shared.Store(600)

	// Queue stale-first must still keep the advanced copy (order-independent).
	got := preferMaxOffsetSameSharedMaxOffset([]types.Task{
		{Offset: 500, Length: 500, SharedMaxOffset: shared},
		{Offset: 600, Length: 400, SharedMaxOffset: shared},
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Offset != 600 || got[0].Length != 400 {
		t.Fatalf("queue-first keep = %d/%d, want advanced 600/400", got[0].Offset, got[0].Length)
	}
}

func TestPreferMaxOffsetSameSharedMaxOffset_DistinctAndNilKept(t *testing.T) {
	a := new(atomic.Int64)
	b := new(atomic.Int64)
	a.Store(100)
	b.Store(300)

	got := preferMaxOffsetSameSharedMaxOffset([]types.Task{
		{Offset: 100, Length: 100, SharedMaxOffset: a},
		{Offset: 200, Length: 50}, // nil pointer — always keep
		{Offset: 300, Length: 100, SharedMaxOffset: b},
		{Offset: 50, Length: 50, SharedMaxOffset: a}, // stale duplicate of a — drop
	})
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (two distinct pointers + nil)", len(got))
	}
	if got[0].Offset != 100 || got[1].Offset != 200 || got[2].Offset != 300 {
		t.Fatalf("unexpected keep order: %+v", got)
	}
}

func TestHandlePause_QueuedStaleHedge_PrefersActiveRemaining(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "hedge_prefer.bin")
	state := progress.New("hedge-prefer", fileSize)
	state.Bytes.VerifiedProgress.Store(600)

	shared := new(atomic.Int64)
	shared.Store(600)

	active := &ActiveTask{
		Task:            types.Task{Offset: 500, Length: 500, SharedMaxOffset: shared},
		SharedMaxOffset: shared,
	}
	active.CurrentOffset.Store(600)
	active.StopAt.Store(1000)

	progressCh := make(chan types.DownloadEvent, 1)
	d := &ConcurrentDownloader{
		ID:           "hedge-prefer",
		URL:          "http://example.com/file.bin",
		State:        state,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
		activeTasks:  map[int]*ActiveTask{0: active},
	}

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 500, Length: 500, SharedMaxOffset: shared})

	err := d.handlePause(destPath, fileSize, queue, nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("expected ErrPaused, got %v", err)
	}

	ev := <-progressCh
	if ev.State == nil {
		t.Fatal("expected pause State")
	}
	tasks := ev.State.Tasks
	if len(tasks) != 1 {
		t.Fatalf("persisted tasks = %d, want 1", len(tasks))
	}
	if tasks[0].Offset != 600 || tasks[0].Length != 400 {
		t.Fatalf("persisted remaining = %d/%d, want 600/400", tasks[0].Offset, tasks[0].Length)
	}
	if tasks[0].SharedMaxOffset != nil {
		t.Fatal("pause snapshot SharedMaxOffset should be cleared")
	}
	if active.SharedMaxOffset != shared {
		t.Fatal("live ActiveTask SharedMaxOffset must not be cleared by pause")
	}
	// Classic fixture: remaining Length 400 + VP 600 → Downloaded 600.
	if ev.State.Downloaded != 600 {
		t.Fatalf("State.Downloaded = %d, want 600", ev.State.Downloaded)
	}
	if ev.Downloaded != 600 {
		t.Fatalf("event Downloaded = %d, want 600", ev.Downloaded)
	}
}

func TestHandlePause_EmptyActive_QueueStaleFirst_PrefersMaxOffset(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "empty_active_prefer.bin")
	state := progress.New("empty-active-prefer", fileSize)
	state.Bytes.VerifiedProgress.Store(600)

	shared := new(atomic.Int64)
	shared.Store(600)

	progressCh := make(chan types.DownloadEvent, 1)
	d := &ConcurrentDownloader{
		ID:           "empty-active-prefer",
		URL:          "http://example.com/file.bin",
		State:        state,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
		// Production pause after releaseActiveOnCancel: map emptied, both
		// copies already on the queue (stale FIFO-first, then advanced).
		activeTasks: map[int]*ActiveTask{},
	}

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 500, Length: 500, SharedMaxOffset: shared})
	queue.Push(types.Task{Offset: 600, Length: 400, SharedMaxOffset: shared})

	err := d.handlePause(destPath, fileSize, queue, nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("expected ErrPaused, got %v", err)
	}

	ev := <-progressCh
	if ev.State == nil {
		t.Fatal("expected pause State")
	}
	tasks := ev.State.Tasks
	if len(tasks) != 1 {
		t.Fatalf("persisted tasks = %d, want 1", len(tasks))
	}
	if tasks[0].Offset != 600 || tasks[0].Length != 400 {
		t.Fatalf("persisted remaining = %d/%d, want 600/400", tasks[0].Offset, tasks[0].Length)
	}
	if tasks[0].SharedMaxOffset != nil {
		t.Fatal("pause snapshot SharedMaxOffset should be cleared")
	}

	// Downloaded==600 alone is insufficient under max(VP, computed): wrong
	// prefer 500/500 still yields Downloaded=max(600,500)=600, but
	// Downloaded+ΣLength=1100≠fileSize. Correct prefer: 600+400=1000.
	var remainingSum int64
	for _, task := range tasks {
		remainingSum += task.Length
	}
	if ev.State.Downloaded+remainingSum != fileSize {
		t.Fatalf("Downloaded(%d)+ΣLength(%d)=%d, want fileSize %d",
			ev.State.Downloaded, remainingSum, ev.State.Downloaded+remainingSum, fileSize)
	}
	if ev.State.Downloaded != 600 {
		t.Fatalf("State.Downloaded = %d, want 600", ev.State.Downloaded)
	}
	if ev.Downloaded != 600 {
		t.Fatalf("event Downloaded = %d, want 600", ev.Downloaded)
	}
}
