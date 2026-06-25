package concurrent

import (
	"testing"

	"goaria-v3/internal/surge/engine/types"
)

// --- alignedSplitSizeWithMin tests ---

func TestAlignedSplitSizeWithMin_DefaultMinChunk(t *testing.T) {
	// With default MinChunk, should behave identically to alignedSplitSize
	tests := []struct {
		remaining int64
		wantZero  bool
	}{
		{types.MinChunk, true},       // half < MinChunk → 0
		{2 * types.MinChunk, false},  // half = MinChunk → valid
		{4 * types.MinChunk, false},  // half = 2*MinChunk → valid
		{10 * types.MinChunk, false}, // half = 5*MinChunk → valid
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
	// With a smaller minChunk (256KB floor), splitting should succeed for smaller remainings
	floor := int64(types.MinChunkDynamicFloor)

	// remaining = 2 * floor → half = floor → valid split
	got := alignedSplitSizeWithMin(2*floor, floor)
	if got != floor {
		t.Errorf("alignedSplitSizeWithMin(2*floor, floor) = %d, want %d", got, floor)
	}

	// remaining = floor → half = floor/2 < floor → 0
	got = alignedSplitSizeWithMin(floor, floor)
	if got != 0 {
		t.Errorf("alignedSplitSizeWithMin(floor, floor) = %d, want 0", got)
	}

	// remaining = 4 * floor → half = 2*floor → valid
	got = alignedSplitSizeWithMin(4*floor, floor)
	if got != 2*floor {
		t.Errorf("alignedSplitSizeWithMin(4*floor, floor) = %d, want %d", got, 2*floor)
	}
}

func TestAlignedSplitSizeWithMin_Alignment(t *testing.T) {
	// Ensure result is always aligned to AlignSize even with unaligned remaining
	floor := int64(types.MinChunkDynamicFloor)
	remaining := 2*floor + 7 // unaligned
	got := alignedSplitSizeWithMin(remaining, floor)
	if got%types.AlignSize != 0 {
		t.Errorf("alignedSplitSizeWithMin(%d, floor) = %d, not aligned to %d", remaining, got, types.AlignSize)
	}
}

// --- StealWork tail-end dynamic chunk tests ---

// makeStealTestDownloader creates a ConcurrentDownloader with pre-populated
// activeTasks for StealWork testing. Each task has CurrentOffset and StopAt
// set to simulate a specific remaining range.
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
	// 2 active workers each with 1.5MB, 3 idle workers.
	// totalRemaining = 3MB < 2×2MB → degradation.
	// dynamicMinChunk = max(3MB/5, 256KB) = max(629145, 262144) = 629145 bytes (~614KB).
	// Best: 1.5MB > 614KB → candidate; half = 768KB ≥ 614KB → steal succeeds.
	// Without degradation: 1.5MB < MinChunk(2MB) → no candidate → steal fails.
	//
	// NOTE: The original plan specified 1 idle worker, but with 1 idle:
	// totalWorkers=3, dynamicMinChunk=3MB/3=1MB, half=768KB < 1MB →
	// steal fails. Using 3 idle workers instead so the test validates the
	// intended behavior (steal succeeds in tail-end phase).

	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 3 * types.MB / 2},
		{offset: 3 * types.MB / 2, length: 3 * types.MB / 2},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(3) // simulate 3 idle workers

	// Without degradation: 1.5MB < MinChunk(2MB) → no candidate → false
	// With degradation: dynamicMinChunk = max(3MB/5, 256KB) = ~614KB
	// 1.5MB > 614KB → candidate; half = 768KB ≥ 614KB → steal succeeds
	if !d.StealWork(queue) {
		t.Error("StealWork should succeed in tail-end phase with dynamic MinChunk")
	}

	if queue.Len() != 1 {
		t.Errorf("queue should have 1 stolen task, got %d", queue.Len())
	}

	queue.Close()
}

func TestStealWork_NormalPhase_NoDegradation(t *testing.T) {
	// 2 active workers each with 10MB, 1 idle.
	// totalRemaining = 20MB ≥ 2*2MB=4MB → no degradation.
	// dynamicMinChunk = MinChunk = 2MB.
	// Best: 10MB > 2MB → candidate; half = 5MB ≥ 2MB → steal succeeds.
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 10 * types.MB},
		{offset: 10 * types.MB, length: 10 * types.MB},
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
	// 1 active worker with 300KB, 1 idle.
	// totalRemaining = 300KB < 1*2MB=2MB → degradation.
	// dynamicMinChunk = max(300KB/2, 256KB) = max(150KB, 256KB) = 256KB (floor).
	// Best: 300KB > 256KB → candidate.
	// half = 150KB < 256KB → splitSize = 0 → steal fails.
	// This is correct: even with floor, the chunk is too small to split in half.
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 300 * types.KB},
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
	// 0 active workers → early return, no division by zero
	d := makeStealTestDownloader(nil)

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)

	if d.StealWork(queue) {
		t.Error("StealWork should return false with no active workers")
	}

	queue.Close()
}

func TestStealWork_SingleActiveNoIdle(t *testing.T) {
	// 1 active worker with 5MB, 0 idle workers.
	// totalRemaining = 5MB, activeWorkers=1, 5MB < 1*2MB=2MB? No → no degradation.
	// But even if degradation triggered, no idle workers means balancer won't call StealWork.
	// This test verifies no crash.
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 5 * types.MB},
	})

	queue := NewTaskQueue()
	// 0 idle workers

	// StealWork itself doesn't check idle workers (balancer does), but it should still work
	// dynamicMinChunk = MinChunk = 2MB (no degradation since 5MB > 2MB)
	// 5MB > 2MB → candidate; half = 2.5MB ≥ 2MB → steal succeeds
	if !d.StealWork(queue) {
		t.Error("StealWork should succeed with 1 active worker and enough remaining")
	}

	queue.Close()
}

func TestStealWork_DynamicFloorExactly256KB(t *testing.T) {
	// 2 active workers each with 512KB, 2 idle.
	// totalRemaining = 1MB < 2*2MB=4MB → degradation.
	// dynamicMinChunk = max(1MB/4, 256KB) = max(256KB, 256KB) = 256KB.
	// Best: 512KB > 256KB → candidate.
	// half = 256KB ≥ 256KB → splitSize = 256KB → steal succeeds!
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 512 * types.KB},
		{offset: 512 * types.KB, length: 512 * types.KB},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(2)

	if !d.StealWork(queue) {
		t.Error("StealWork should succeed when dynamicMinChunk is exactly 256KB floor")
	}

	if queue.Len() != 1 {
		t.Errorf("queue should have 1 stolen task, got %d", queue.Len())
	}

	// Verify the stolen task size
	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("should be able to pop stolen task")
	}
	// half of 512KB = 256KB, aligned
	if stolen.Length != 256*types.KB {
		t.Errorf("stolen task length = %d, want %d", stolen.Length, 256*types.KB)
	}

	queue.Close()
}

func TestStealWork_HalfEqualsDynamicMinChunk(t *testing.T) {
	// 1 active worker with 1MB, 1 idle worker.
	// totalRemaining = 1MB < 1*2MB=2MB → degradation.
	// dynamicMinChunk = 1MB/2 = 512KB (> 256KB floor, so floor doesn't apply).
	// half = (1MB/2/4KB)*4KB = 512KB = dynamicMinChunk → exact equality → steal succeeds.
	// This covers the boundary case where half == dynamicMinChunk.
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 1 * types.MB},
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
	// half of 1MB = 512KB, aligned
	if stolen.Length != 512*types.KB {
		t.Errorf("stolen task length = %d, want %d", stolen.Length, 512*types.KB)
	}

	queue.Close()
}
