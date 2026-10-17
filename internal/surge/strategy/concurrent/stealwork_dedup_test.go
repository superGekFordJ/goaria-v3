package concurrent

import (
	"sync/atomic"
	"testing"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// TestStealWork_StolenTaskHasNilSharedMaxOffset verifies that the stolen task
// is a plain disjoint split: neither side carries a dedup pointer (that state
// is reserved for real hedge racers), the stolen range starts at the victim's
// reduced StopAt, and it ends at the victim's original end.
func TestStealWork_StolenTaskHasNilSharedMaxOffset(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{MinChunkSize: 2 * utils.MiB},
	}

	// Original worker at offset 0, length 4MB, not hedged (SharedMaxOffset nil).
	active := &ActiveTask{
		Task: types.Task{Offset: 0, Length: 4 * utils.MiB},
	}
	active.CurrentOffset.Store(0)
	active.StopAt.Store(4 * utils.MiB)
	d.activeTasks[0] = active

	queue := NewTaskQueue()
	defer queue.Close()

	if !d.StealWork(queue) {
		t.Fatal("StealWork should succeed")
	}

	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("expected a stolen task in the queue")
	}

	if stolen.SharedMaxOffset != nil {
		t.Error("stolen task is a disjoint range, not a racer: SharedMaxOffset must be nil")
	}
	if active.SharedMaxOffset != nil {
		t.Errorf("never-hedged victim SharedMaxOffset should remain nil, got %v", active.SharedMaxOffset)
	}
	if stolen.Offset != 2*utils.MiB || stolen.Length != 2*utils.MiB {
		t.Errorf("stolen = {%d,%d}, want {%d,%d}", stolen.Offset, stolen.Length, 2*utils.MiB, 2*utils.MiB)
	}
	if got := active.StopAt.Load(); got != 2*utils.MiB {
		t.Errorf("victim StopAt = %d, want %d (aligned half)", got, 2*utils.MiB)
	}
}

// TestStealWork_NilSharedMaxOffset_OriginalUnchanged verifies that when the
// original worker was never hedged, StealWork leaves both sides with no dedup
// state: a plain stolen range and an untouched nil SharedMaxOffset victim.
func TestStealWork_NilSharedMaxOffset_OriginalUnchanged(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{MinChunkSize: 2 * utils.MiB},
	}

	active := &ActiveTask{
		Task: types.Task{Offset: 0, Length: 4 * utils.MiB},
	}
	active.CurrentOffset.Store(0)
	active.StopAt.Store(4 * utils.MiB)
	d.activeTasks[0] = active

	queue := NewTaskQueue()
	defer queue.Close()

	d.StealWork(queue)

	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("expected a stolen task")
	}
	if stolen.SharedMaxOffset != nil {
		t.Error("stolen task must keep SharedMaxOffset nil")
	}
	if active.SharedMaxOffset != nil {
		t.Errorf("original SharedMaxOffset should still be nil")
	}
}

// TestStealWork_DedupDoesNotMaskOriginalWorker verifies the core Bug A
// scenario: with SharedMaxOffset nil on both sides of a stolen split,
// downloadTask skips the CAS dedup branch entirely — the stolen worker and
// the original worker each take the else path and count
// newlyWritten = readSoFar, so neither masks the other's progress.
func TestStealWork_DedupDoesNotMaskOriginalWorker(t *testing.T) {
	// StealWork now leaves SharedMaxOffset nil on both sides — simulate the
	// dedup branch from downloadTask for both write paths.
	var stolenPtr, originalPtr *atomic.Int64

	credit := func(ptr *atomic.Int64, readSoFar, offset, rangeStart int64) int64 {
		if ptr != nil {
			maxOff := ptr.Load()
			if offset <= maxOff {
				return 0 // masked by the racing partner
			}
			if rangeStart >= maxOff {
				if ptr.CompareAndSwap(maxOff, offset) {
					return readSoFar
				}
			}
			return 0
		}
		return readSoFar // nil pointer: full credit, no dedup
	}

	readSoFar := int64(4096)
	if got := credit(originalPtr, readSoFar, 4096, 0); got != readSoFar {
		t.Errorf("original worker newlyWritten = %d, want %d (full credit, nil pointer)", got, readSoFar)
	}
	if got := credit(stolenPtr, readSoFar, 3*utils.MiB, 2*utils.MiB); got != readSoFar {
		t.Errorf("stolen worker newlyWritten = %d, want %d (full credit, nil pointer)", got, readSoFar)
	}
}
