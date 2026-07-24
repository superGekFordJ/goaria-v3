package concurrent

import (
	"testing"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func TestAlignedSplitSizeWithMin_DefaultMinChunk(t *testing.T) {
	tests := []struct {
		remaining int64
		wantZero  bool
	}{
		{types.MinChunk, true},
		{2 * types.MinChunk, false},
		{4 * types.MinChunk, false},
		{10 * types.MinChunk, false},
	}

	for _, tt := range tests {
		got := alignedSplitSizeWithMin(tt.remaining, types.MinChunk)
		if tt.wantZero && got != 0 {
			t.Errorf("alignedSplitSizeWithMin(%d, MinChunk) = %d, want 0", tt.remaining, got)
		}
		if !tt.wantZero && got == 0 {
			t.Errorf("alignedSplitSizeWithMin(%d, MinChunk) = 0, want non-zero", tt.remaining)
		}
		if got != 0 && got%types.AlignSize != 0 {
			t.Errorf("alignedSplitSizeWithMin(%d, MinChunk) = %d, not aligned", tt.remaining, got)
		}
	}
}

func TestAlignedSplitSizeWithMin_DynamicFloor(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)

	got := alignedSplitSizeWithMin(2*floor, floor)
	if got != floor {
		t.Errorf("alignedSplitSizeWithMin(2*floor, floor) = %d, want %d", got, floor)
	}

	got = alignedSplitSizeWithMin(floor, floor)
	if got != 0 {
		t.Errorf("alignedSplitSizeWithMin(floor, floor) = %d, want 0", got)
	}

	got = alignedSplitSizeWithMin(4*floor, floor)
	if got != 2*floor {
		t.Errorf("alignedSplitSizeWithMin(4*floor, floor) = %d, want %d", got, 2*floor)
	}
}

func TestAlignedSplitSizeWithMin_Alignment(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	remaining := 2*floor + 7
	got := alignedSplitSizeWithMin(remaining, floor)
	if got%types.AlignSize != 0 {
		t.Errorf("alignedSplitSizeWithMin(%d, floor) = %d, not aligned to %d", remaining, got, types.AlignSize)
	}
}

func makeStealTestDownloader(tasks []struct {
	offset int64
	length int64
},
) *ConcurrentDownloader {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	for i, t := range tasks {
		at := &ActiveTask{
			Task: types.Task{Offset: t.offset, Length: t.length},
		}
		at.CurrentOffset.Store(t.offset)
		at.StopAt.Store(t.offset + t.length)
		d.activeTasks[i] = at
	}
	return d
}

func TestStealWork_TailEndDynamicChunk(t *testing.T) {
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 3 * utils.MiB / 2},
		{offset: 3 * utils.MiB / 2, length: 3 * utils.MiB / 2},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(3)

	if !d.StealWork(queue) {
		t.Error("StealWork should succeed in tail-end phase with dynamic MinChunk")
	}

	if queue.Len() != 1 {
		t.Errorf("queue should have 1 stolen task, got %d", queue.Len())
	}

	queue.Close()
}

func TestStealWork_NormalPhase_NoDegradation(t *testing.T) {
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 10 * utils.MiB},
		{offset: 10 * utils.MiB, length: 10 * utils.MiB},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)

	if !d.StealWork(queue) {
		t.Error("StealWork should succeed in normal phase")
	}

	if queue.Len() != 1 {
		t.Errorf("queue should have 1 stolen task, got %d", queue.Len())
	}

	queue.Close()
}

func TestStealWork_TinyRemaining_FloorKicksIn(t *testing.T) {
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 300 * utils.KiB},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)

	if d.StealWork(queue) {
		t.Error("StealWork should fail when remaining is too small even for dynamic floor")
	}

	if queue.Len() != 0 {
		t.Errorf("queue should be empty, got %d", queue.Len())
	}

	queue.Close()
}

func TestStealWork_NoActiveWorkers(t *testing.T) {
	d := makeStealTestDownloader(nil)

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)

	if d.StealWork(queue) {
		t.Error("StealWork should return false with no active workers")
	}

	queue.Close()
}

func TestStealWork_SingleActiveNoIdle(t *testing.T) {
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 5 * utils.MiB},
	})

	queue := NewTaskQueue()

	if !d.StealWork(queue) {
		t.Error("StealWork should succeed with 1 active worker and enough remaining")
	}

	queue.Close()
}

func TestStealWork_DynamicFloorExactly256KB(t *testing.T) {
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 512 * utils.KiB},
		{offset: 512 * utils.KiB, length: 512 * utils.KiB},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(2)

	if !d.StealWork(queue) {
		t.Error("StealWork should succeed when dynamicMinChunk is exactly 256KB floor")
	}

	if queue.Len() != 1 {
		t.Errorf("queue should have 1 stolen task, got %d", queue.Len())
	}

	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("should be able to pop stolen task")
	}
	if stolen.Length != 256*utils.KiB {
		t.Errorf("stolen task length = %d, want %d", stolen.Length, 256*utils.KiB)
	}

	queue.Close()
}

func TestStealWork_HalfEqualsDynamicMinChunk(t *testing.T) {
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 1 * utils.MiB},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)

	if !d.StealWork(queue) {
		t.Error("StealWork should succeed when half exactly equals dynamicMinChunk")
	}

	if queue.Len() != 1 {
		t.Errorf("queue should have 1 stolen task, got %d", queue.Len())
	}

	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("should be able to pop stolen task")
	}
	if stolen.Length != 512*utils.KiB {
		t.Errorf("stolen task length = %d, want %d", stolen.Length, 512*utils.KiB)
	}

	queue.Close()
}
