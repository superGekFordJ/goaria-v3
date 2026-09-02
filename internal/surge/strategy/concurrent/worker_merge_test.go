package concurrent

import (
	"errors"
	"sync/atomic"
	"testing"

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
