package concurrent

import (
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// TestHedgeSharedMaxOffsetRace exercises concurrent hedging and pointer reads.
// It runs HedgeWork in parallel with a reader that repeatedly accesses
// ActiveTask.SharedMaxOffset to ensure there is no data race under -race.
func TestHedgeSharedMaxOffsetRace(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	active := &ActiveTask{}
	active.CurrentOffset.Store(0)
	active.StopAt.Store(types.MaxHedgeChunk / 2)

	d.activeTasks[0] = active

	queue := NewTaskQueue()

	var wg sync.WaitGroup
	wg.Add(2)

	// Hedge goroutine
	go func() {
		defer wg.Done()
		for range 500 {
			d.HedgeWork(queue)
			time.Sleep(time.Microsecond)
		}
	}()

	// Reader goroutine: repeatedly read the shared pointer (mimics downloadTask access)
	go func() {
		defer wg.Done()
		for range 500 {
			// Read under the task's RLock, matching production downloadTask behaviour.
			active.SharedMaxOffsetMu.RLock()
			ptr := active.SharedMaxOffset
			active.SharedMaxOffsetMu.RUnlock()
			if ptr != nil {
				_ = ptr.Load()
				// harmless CAS attempt
				ptr.CompareAndSwap(ptr.Load(), ptr.Load())
			}
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()

	// The hedge path must actually have run: exactly one racer enqueued, the
	// victim marked, and a shared dedup pointer installed.
	if got := active.Hedged.Load(); got != 1 {
		t.Fatalf("Hedged = %d, want 1 — hedge path never ran", got)
	}
	if got := queue.Len(); got != 1 {
		t.Fatalf("queue.Len() = %d, want exactly 1 racer task", got)
	}

	// Close the queue and drain remaining tasks without blocking.
	queue.Close()
	var drained []types.Task
	for {
		tsk, ok := queue.Pop()
		if !ok {
			break
		}
		drained = append(drained, tsk)
	}
	if len(drained) != 1 {
		t.Fatalf("drained %d tasks, want 1", len(drained))
	}

	active.SharedMaxOffsetMu.RLock()
	shared := active.SharedMaxOffset
	active.SharedMaxOffsetMu.RUnlock()
	if shared == nil {
		t.Fatal("SharedMaxOffset must be non-nil after a successful hedge")
	}
	if drained[0].SharedMaxOffset != shared {
		t.Fatal("racer task must carry the same SharedMaxOffset pointer as the victim")
	}
}
