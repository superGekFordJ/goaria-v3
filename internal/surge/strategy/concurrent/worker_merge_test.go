package concurrent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/testutil"
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

// parkedHeaderHandler holds the request without writing response headers
// until the request context ends, so a real worker pickup registers the task
// in activeTasks but never transfers a byte.
type parkedHeaderHandler struct{}

func (parkedHeaderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	<-r.Context().Done()
}

func newParkedHeaderServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := testutil.NewHTTPServerT(t, parkedHeaderHandler{})
	t.Cleanup(srv.Close)
	return srv
}

// TestWorkerPickup_StolenTaskRemainsUnhedged drives the full stolen-worker
// eligibility chain through a real StealWork split and a real worker pickup:
// the stolen range registers Hedged=0 with no dedup pointer, stays splittable
// by a second StealWork, and becomes a real hedge racer once its remaining
// tail fits under MaxHedgeChunk.
func TestWorkerPickup_StolenTaskRemainsUnhedged(t *testing.T) {
	fileSize := int64(16 * utils.MiB)
	d := NewConcurrentDownloader("pickup-stolen", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
	})
	srv := newParkedHeaderServer(t)

	// Seed a single victim [0,16MiB) and run a real steal → stolen [8,16MiB).
	victim := &ActiveTask{Task: types.Task{Offset: 0, Length: fileSize}}
	victim.CurrentOffset.Store(0)
	victim.StopAt.Store(fileSize)
	d.activeMu.Lock()
	d.activeTasks[0] = victim
	d.activeMu.Unlock()

	queue := NewTaskQueue()
	if !d.StealWork(queue) {
		t.Fatal("StealWork should split the 16MiB victim")
	}
	stolen, ok := queue.Pop()
	if !ok {
		t.Fatal("expected a stolen task")
	}
	if stolen.Offset != 8*utils.MiB || stolen.Length != 8*utils.MiB {
		t.Fatalf("stolen = {%d,%d}, want {%d,%d}", stolen.Offset, stolen.Length, 8*utils.MiB, 8*utils.MiB)
	}
	if stolen.SharedMaxOffset != nil {
		t.Fatal("stolen task must be a plain disjoint range (nil SharedMaxOffset)")
	}
	queue.Push(stolen) // hand back to the picking worker

	// A real worker pops the stolen task and blocks inside the parked server.
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		_ = d.worker(ctx, 1, []string{srv.URL}, nil, queue, fileSize, &http.Client{})
	})
	t.Cleanup(func() {
		cancel()
		queue.Close()
		wg.Wait()
	})
	var at *ActiveTask
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.activeMu.Lock()
		at = d.activeTasks[1]
		d.activeMu.Unlock()
		if at != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if at == nil {
		t.Fatal("worker never registered the stolen task")
	}

	// Pickup must not mark a plain stolen range as a hedge racer.
	d.activeMu.Lock()
	hedged := at.Hedged.Load()
	d.activeMu.Unlock()
	if hedged != 0 {
		t.Error("stolen pickup must not mark the task Hedged")
	}
	at.SharedMaxOffsetMu.RLock()
	ptr := at.SharedMaxOffset
	at.SharedMaxOffsetMu.RUnlock()
	if ptr != nil {
		t.Error("stolen pickup must not install SharedMaxOffset")
	}

	// The stolen task stays splittable: shrink the victim below it so the
	// stolen worker is the largest-remaining candidate, then steal again.
	victim.CurrentOffset.Store(4 * utils.MiB)
	if !d.StealWork(queue) {
		t.Fatal("a picked-up stolen task must be stealable again")
	}
	second, ok := queue.Pop()
	if !ok {
		t.Fatal("expected a second stolen task")
	}
	if second.Offset != 12*utils.MiB || second.Length != 4*utils.MiB {
		t.Fatalf("second stolen = {%d,%d}, want {%d,%d}", second.Offset, second.Length, 12*utils.MiB, 4*utils.MiB)
	}
	if second.SharedMaxOffset != nil {
		t.Error("second-generation stolen task must keep SharedMaxOffset nil")
	}

	// Advance the stolen worker to a <MaxHedgeChunk tail: the same task must
	// now be hedgeable as a real racer.
	at.CurrentOffset.Store(at.StopAt.Load() - types.MinChunkDynamicFloor)
	if !d.HedgeWork(queue) {
		t.Fatal("a stolen task must be hedgeable once it is a small tail")
	}
	if at.Hedged.Load() != 1 {
		t.Fatal("a real hedge must mark the stolen worker Hedged")
	}
	at.SharedMaxOffsetMu.RLock()
	shared := at.SharedMaxOffset
	at.SharedMaxOffsetMu.RUnlock()
	if shared == nil {
		t.Fatal("a real hedge must install SharedMaxOffset")
	}
	racer, ok := queue.Pop()
	if !ok {
		t.Fatal("expected a racer task")
	}
	if racer.SharedMaxOffset != shared {
		t.Fatal("racer must carry the same SharedMaxOffset pointer")
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
