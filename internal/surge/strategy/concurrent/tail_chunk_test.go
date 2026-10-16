package concurrent

import (
	"fmt"
	"sync/atomic"
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

func TestStealWork_DynamicFloorExactly(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 2 * floor},
		{offset: 2 * floor, length: 2 * floor},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(2)

	if !d.StealWork(queue) {
		t.Error("StealWork should succeed when dynamicMinChunk lands exactly on the floor")
	}

	if queue.Len() != 1 {
		t.Errorf("queue should have 1 stolen task, got %d", queue.Len())
	}

	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("should be able to pop stolen task")
	}
	if stolen.Length != floor {
		t.Errorf("stolen task length = %d, want %d", stolen.Length, floor)
	}

	queue.Close()
}

func TestStealWork_HalfEqualsDynamicMinChunk(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	d := makeStealTestDownloader([]struct {
		offset int64
		length int64
	}{
		{offset: 0, length: 2 * floor},
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
	if stolen.Length != floor {
		t.Errorf("stolen task length = %d, want %d", stolen.Length, floor)
	}

	queue.Close()
}

// TestAlignedSplitSizeWithMin_HedgeBoundary pins the exact byte boundary where
// the aligned half first reaches the floor: remaining >= 2*floor splits while
// anything below does not. MaxHedgeChunk mirrors that same boundary so steal
// and hedge partition the size axis with no dead-zone.
func TestAlignedSplitSizeWithMin_HedgeBoundary(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	align := int64(types.AlignSize)
	boundary := int64(types.MaxHedgeChunk)

	if floor%align != 0 {
		t.Fatalf("floor %d must be a multiple of AlignSize %d for an exact boundary", floor, align)
	}
	if boundary != 2*floor {
		t.Fatalf("MaxHedgeChunk = %d, want %d (2*floor)", boundary, 2*floor)
	}

	cases := []struct {
		remaining int64
		want      int64
	}{
		{boundary - align, 0},
		{boundary - 1, 0},
		{boundary, floor},
		{boundary + 1, floor},
	}
	for _, tc := range cases {
		if got := alignedSplitSizeWithMin(tc.remaining, floor); got != tc.want {
			t.Errorf("alignedSplitSizeWithMin(%d, floor) = %d, want %d", tc.remaining, got, tc.want)
		}
	}

	// A dynamic floor above the package floor lifts the boundary to
	// 2*AlignSize*ceil(minChunk/AlignSize); here minChunk=floor+1 rounds up to
	// floor+align, so the split gate sits at 2*(floor+align).
	raised := 2 * (floor + align)
	if got := alignedSplitSizeWithMin(raised-1, floor+1); got != 0 {
		t.Errorf("alignedSplitSizeWithMin(%d, floor+1) = %d, want 0", raised-1, got)
	}
	if got := alignedSplitSizeWithMin(raised, floor+1); got != floor+align {
		t.Errorf("alignedSplitSizeWithMin(%d, floor+1) = %d, want %d", raised, got, floor+align)
	}
}

// TestTailPhase_BoundaryHandoff_NoDeadZone verifies the steal/hedge handoff at
// MaxHedgeChunk: with the dynamic floor sitting on the package floor,
// remaining < boundary cannot split but can be hedged, while remaining >=
// boundary is refused by both hedge entries and splits cleanly.
func TestTailPhase_BoundaryHandoff_NoDeadZone(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	align := int64(types.AlignSize)
	boundary := int64(types.MaxHedgeChunk)

	for _, rem := range []int64{boundary - align, boundary - 1, boundary, boundary + 1} {
		t.Run(fmt.Sprintf("remaining_%d", rem), func(t *testing.T) {
			d := makeStealTestDownloader([]struct {
				offset int64
				length int64
			}{
				{offset: 0, length: rem},
			})
			queue := NewTaskQueue()
			queue.idleWorkers.Add(1)
			defer queue.Close()

			at := d.activeTasks[0]
			stopBefore := at.StopAt.Load()

			if rem < boundary {
				if d.StealWork(queue) {
					t.Fatalf("StealWork should fail below the %d boundary", boundary)
				}
				if got := at.StopAt.Load(); got != stopBefore {
					t.Fatalf("failed steal must not move StopAt: %d, want %d", got, stopBefore)
				}
				if !d.HedgeWork(queue) {
					t.Fatalf("HedgeWork should hedge a sub-%d tail", boundary)
				}
				if queue.Len() != 1 {
					t.Fatalf("queue.Len() = %d, want 1 hedge racer", queue.Len())
				}
				tsk, _ := queue.Pop()
				if tsk.Offset != 0 || tsk.Length != rem {
					t.Fatalf("hedge task = {%d,%d}, want {0,%d}", tsk.Offset, tsk.Length, rem)
				}
				at.SharedMaxOffsetMu.RLock()
				ptr := at.SharedMaxOffset
				at.SharedMaxOffsetMu.RUnlock()
				if ptr == nil || tsk.SharedMaxOffset != ptr {
					t.Fatal("hedge task must share the victim's SharedMaxOffset pointer")
				}
				if at.Hedged.Load() != 1 {
					t.Fatal("victim Hedged should be 1 after hedge")
				}
				return
			}

			if d.HedgeWork(queue) {
				t.Fatalf("HedgeWork must reject remaining %d >= %d", rem, boundary)
			}
			if n := d.HedgeAll(queue); n != 0 {
				t.Fatalf("HedgeAll must reject remaining %d >= %d, got %d", rem, boundary, n)
			}
			if queue.Len() != 0 {
				t.Fatalf("rejected hedge must not enqueue, len=%d", queue.Len())
			}
			if at.Hedged.Load() != 0 {
				t.Fatal("rejected hedge must not set Hedged")
			}
			if got := at.StopAt.Load(); got != stopBefore {
				t.Fatalf("rejected hedge must not move StopAt: %d, want %d", got, stopBefore)
			}

			if !d.StealWork(queue) {
				t.Fatalf("StealWork should split remaining %d", rem)
			}
			if queue.Len() != 1 {
				t.Fatalf("queue.Len() = %d, want 1 stolen task", queue.Len())
			}
			tsk, _ := queue.Pop()
			if tsk.Offset != floor || tsk.Length != rem-floor {
				t.Fatalf("stolen task = {%d,%d}, want {%d,%d}", tsk.Offset, tsk.Length, floor, rem-floor)
			}
			if got := at.StopAt.Load(); got != floor {
				t.Fatalf("victim StopAt = %d, want %d (aligned half)", got, floor)
			}
		})
	}
}

// TestStealWork_TailSingleIdleManyActive verifies the halved dynamic budget:
// with 7 actives each holding 9MiB/5 and a single idle worker, the per-worker
// share no longer self-locks — the first idle tick can already split.
func TestStealWork_TailSingleIdleManyActive(t *testing.T) {
	const activeCount = 7
	chunk := int64(9 * utils.MiB / 5) // 1887436 bytes, non-aligned on purpose
	tasks := make([]struct {
		offset int64
		length int64
	}, activeCount)
	for i := range tasks {
		tasks[i].offset = int64(i) * chunk
		tasks[i].length = chunk
	}
	d := makeStealTestDownloader(tasks)

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)
	defer queue.Close()

	if !d.StealWork(queue) {
		t.Fatal("StealWork should succeed on the first idle tick in the tail phase")
	}
	if queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want exactly 1 stolen task", queue.Len())
	}

	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("should be able to pop stolen task")
	}

	// Exactly one victim must have a reduced StopAt; the stolen range must
	// seamlessly cover [newStopAt, originalEnd) of that victim.
	var victim *ActiveTask
	var victimEnd int64
	victimCount := 0
	for _, at := range d.activeTasks {
		end := at.Task.Offset + at.Task.Length
		if at.StopAt.Load() != end {
			victimCount++
			victim = at
			victimEnd = end
		}
	}
	if victimCount != 1 {
		t.Fatalf("exactly one active should be split, got %d", victimCount)
	}
	if stolen.Offset != victim.StopAt.Load() {
		t.Fatalf("stolen.Offset = %d, want victim newStopAt %d", stolen.Offset, victim.StopAt.Load())
	}
	if stolen.Offset+stolen.Length != victimEnd {
		t.Fatalf("stolen end = %d, want victim original end %d", stolen.Offset+stolen.Length, victimEnd)
	}
	// Aligned half of 9MiB/5 under the halved budget (dynamic floor 825753).
	if front := victim.StopAt.Load() - victim.Task.Offset; front != 920*utils.KiB {
		t.Fatalf("victim keeps %d bytes, want %d", front, 920*utils.KiB)
	}
	if stolen.SharedMaxOffset == nil {
		t.Fatal("stolen task must carry an independent SharedMaxOffset")
	}
	if got := stolen.SharedMaxOffset.Load(); got != stolen.Offset {
		t.Fatalf("stolen SharedMaxOffset = %d, want %d (stolenStart)", got, stolen.Offset)
	}
	victim.SharedMaxOffsetMu.RLock()
	vptr := victim.SharedMaxOffset
	victim.SharedMaxOffsetMu.RUnlock()
	if vptr != nil {
		t.Fatal("never-hedged victim must keep SharedMaxOffset nil")
	}
}

// TestStealWork_StolenEndMatchesStopAt pins the stolen range's far edge to the
// victim's pre-steal StopAt. A shared progress pointer makes the scanned
// RemainingBytes diverge from the raw CurrentOffset read at steal time; the
// stolen task must still cover the range up to StopAt exactly — neither
// undershoot (bytes left uncovered) nor overshoot (into the next chunk).
func TestStealWork_StolenEndMatchesStopAt(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	stopAt := int64(2 * utils.MiB)

	shared := &atomic.Int64{}
	shared.Store(utils.MiB)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	victim := newActiveFixture(0, stopAt, 0, shared)
	d.activeTasks[0] = victim

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)
	defer queue.Close()

	if !d.StealWork(queue) {
		t.Fatal("StealWork should split the effective remaining")
	}
	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("expected one stolen task")
	}
	if stolen.Offset != floor {
		t.Fatalf("stolen.Offset = %d, want aligned half %d", stolen.Offset, floor)
	}
	if end := stolen.Offset + stolen.Length; end != stopAt {
		t.Fatalf("stolen end = %d, want victim pre-steal StopAt %d", end, stopAt)
	}
	if got := victim.StopAt.Load(); got != floor {
		t.Fatalf("victim StopAt = %d, want %d", got, floor)
	}
}

// TestStealWork_DynamicBudgetEdges pins the integer edge cases of the dynamic
// budget: no-active, idle=0 direct calls, tiny remainders floored to zero, and
// fully-completed tasks must never panic nor enqueue zero-length work.
func TestStealWork_DynamicBudgetEdges(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	boundary := int64(types.MaxHedgeChunk)

	t.Run("no active workers", func(t *testing.T) {
		d := makeStealTestDownloader(nil)
		queue := NewTaskQueue()
		queue.idleWorkers.Add(4)
		defer queue.Close()
		if d.StealWork(queue) {
			t.Error("StealWork should return false with no active workers")
		}
		if queue.Len() != 0 {
			t.Errorf("queue should stay empty, got %d", queue.Len())
		}
	})

	t.Run("single active idle zero still splits", func(t *testing.T) {
		d := makeStealTestDownloader([]struct {
			offset int64
			length int64
		}{
			{offset: 0, length: boundary},
		})
		queue := NewTaskQueue()
		defer queue.Close()
		// IdleWorkers()==0 must not gate a direct call: the divisor stays
		// positive via activeWorkers alone.
		if !d.StealWork(queue) {
			t.Error("StealWork should split a boundary-sized task even with idle=0")
		}
		stolen, ok := queue.Pop()
		if !ok {
			t.Fatal("expected one stolen task")
		}
		if stolen.Offset != floor || stolen.Length != boundary-floor {
			t.Errorf("stolen = {%d,%d}, want {%d,%d}", stolen.Offset, stolen.Length, floor, boundary-floor)
		}
	})

	t.Run("tiny remaining floors to zero", func(t *testing.T) {
		d := makeStealTestDownloader([]struct {
			offset int64
			length int64
		}{
			{offset: 0, length: 1},
		})
		queue := NewTaskQueue()
		queue.idleWorkers.Add(8)
		defer queue.Close()
		if d.StealWork(queue) {
			t.Error("StealWork should fail when remaining collapses under the floor")
		}
		if queue.Len() != 0 {
			t.Errorf("no zero-length task may be enqueued, got %d", queue.Len())
		}
	})

	t.Run("completed task produces nothing", func(t *testing.T) {
		d := makeStealTestDownloader([]struct {
			offset int64
			length int64
		}{
			{offset: 64, length: 0},
		})
		d.activeTasks[0].CurrentOffset.Store(64)
		d.activeTasks[0].StopAt.Store(64)
		queue := NewTaskQueue()
		queue.idleWorkers.Add(2)
		defer queue.Close()
		if d.StealWork(queue) {
			t.Error("StealWork should return false for a fully-completed task")
		}
		if queue.Len() != 0 {
			t.Errorf("queue should stay empty, got %d", queue.Len())
		}
	})
}
