package concurrent

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func TestResumeOnRetryOffset_SharedMaxOffsetCarryAndStopAtClamp(t *testing.T) {
	shared := &atomic.Int64{}
	shared.Store(1000)

	task := types.Task{
		Offset: 1000,
		Length: 4000, // original end = 5000
	}
	active := &ActiveTask{
		Task:            task,
		SharedMaxOffset: shared,
	}
	// No progress yet, but StealWork reduced StopAt to 3000.
	active.CurrentOffset.Store(1000)
	active.StopAt.Store(3000)

	var d ConcurrentDownloader
	d.resumeOnRetryOffset(&task, active)

	if task.Offset != 1000 {
		t.Fatalf("Offset = %d, want 1000", task.Offset)
	}
	if task.Length != 2000 {
		t.Fatalf("Length = %d, want 2000 (clamped to StopAt even when current == Offset)", task.Length)
	}
	if task.SharedMaxOffset != shared {
		t.Fatal("SharedMaxOffset was not carried onto the retried task")
	}
}

// TestWorkerPickup_SharedMaxOffsetMarksHedged pins the pickup invariant that
// keeps steal/hedge isolation intact: a task carrying SharedMaxOffset must
// register its ActiveTask with Hedged=1 so StealWork skips the racing pair.
// The mirror is unreachable — registration happens before any transfer.
func TestWorkerPickup_SharedMaxOffsetMarksHedged(t *testing.T) {
	d := NewConcurrentDownloader("pickup-hedged", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
	})

	shared := &atomic.Int64{}
	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: 64 * utils.KiB, SharedMaxOffset: shared})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		_ = d.worker(ctx, 0, []string{"http://localhost:1"}, nil, queue, 64*utils.KiB, &http.Client{})
	})

	deadline := time.Now().Add(3 * time.Second)
	var at *ActiveTask
	for time.Now().Before(deadline) {
		d.activeMu.Lock()
		at = d.activeTasks[0]
		d.activeMu.Unlock()
		if at != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	queue.Close() // unblock Pop; a residual requeue after Close is retained
	wg.Wait()

	if at == nil {
		t.Fatal("worker never registered the task as active")
	}
	if at.Hedged.Load() != 1 {
		t.Fatal("picked-up task with SharedMaxOffset must register Hedged=1")
	}
	at.SharedMaxOffsetMu.RLock()
	ptr := at.SharedMaxOffset
	at.SharedMaxOffsetMu.RUnlock()
	if ptr != shared {
		t.Fatal("picked-up task must propagate the SharedMaxOffset pointer")
	}
}

func TestStealWork_SkipsHedgedTasks(t *testing.T) {
	d := NewConcurrentDownloader("steal-hedged-id", nil, nil, &types.RuntimeConfig{
		MinChunkSize: 1 * utils.MiB,
	})
	queue := NewTaskQueue()

	active := &ActiveTask{}
	active.CurrentOffset.Store(0)
	active.StopAt.Store(8 * utils.MiB)
	active.Hedged.Store(1)

	d.activeMu.Lock()
	d.activeTasks[0] = active
	d.activeMu.Unlock()

	if d.StealWork(queue) {
		t.Fatal("StealWork must skip hedged tasks")
	}
	if queue.Len() != 0 {
		t.Fatal("expected empty queue when only hedged victim exists")
	}
}

func TestIsConnLimitError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("unexpected status: 404"), false},
		{errors.New("unexpected status: 429"), true},
		{errors.New("unexpected status: 503"), true},
		{errors.New("rate limited (429/503)"), true},
		{errors.New("connection refused"), true},
		{errors.New("connection reset by peer"), true},
		{errors.New("i/o timeout"), false},
	}
	for _, tc := range cases {
		if got := isConnLimitError(tc.err); got != tc.want {
			t.Errorf("isConnLimitError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
