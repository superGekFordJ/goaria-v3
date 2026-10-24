package hls

import (
	"context"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

func segRow(i int) types.HLSSegmentState {
	return types.HLSSegmentState{Index: i, Sequence: int64(i), URI: "s.ts"}
}

func TestSegmentQueueConstructFiltersCompletedPrefix(t *testing.T) {
	segs := []types.HLSSegmentState{segRow(0), segRow(1), segRow(2), segRow(3)}
	q := newSegmentQueue(segs, func(i int) bool { return i < 2 })
	if q.remaining() != 2 {
		t.Fatalf("remaining = %d, want 2", q.remaining())
	}
	if q.readyCount() != 2 {
		t.Fatalf("ready = %d, want 2", q.readyCount())
	}
	for i := range 2 {
		if st, _ := q.state(i); st != segDone {
			t.Fatalf("state[%d] = %v, want done", i, st)
		}
	}
}

func TestSegmentQueuePopLeaseOrderAndExclusive(t *testing.T) {
	segs := []types.HLSSegmentState{segRow(0), segRow(1), segRow(2)}
	q := newSegmentQueue(segs, nil)
	t1, ok := q.pop(context.Background())
	if !ok || t1.seg.Index != 0 {
		t.Fatalf("pop1 = %v,%v", t1, ok)
	}
	t2, ok := q.pop(context.Background())
	if !ok || t2.seg.Index != 1 {
		t.Fatalf("pop2 = %v,%v", t2, ok)
	}
	if st, _ := q.state(0); st != segLeased {
		t.Fatalf("state[0] = %v, want leased", st)
	}
	// A leased index cannot be popped again.
	if q.remaining() != 3 || q.readyCount() != 1 {
		t.Fatalf("remaining=%d ready=%d", q.remaining(), q.readyCount())
	}
}

func TestSegmentQueueRequeueExactlyOnce(t *testing.T) {
	q := newSegmentQueue([]types.HLSSegmentState{segRow(0), segRow(1)}, nil)
	t1, _ := q.pop(context.Background())
	q.requeue(t1)
	// Requeued segment returns to the FRONT.
	t2, ok := q.pop(context.Background())
	if !ok || t2 != t1 {
		t.Fatalf("requeue pop = %v,%v, want same task", t2, ok)
	}
	// A requeue of the still-leased task puts it back to ready exactly once;
	// a second call on the now-ready task is a no-op.
	q.requeue(t2)
	if st, _ := q.state(0); st != segReady {
		t.Fatalf("state[0] = %v after requeue, want ready", st)
	}
	q.requeue(t2)
	if st, _ := q.state(0); st != segReady {
		t.Fatalf("state[0] = %v after double requeue", st)
	}
	if q.readyCount() != 2 {
		t.Fatalf("readyCount = %d, want 2 — index 0 once, index 1 still queued", q.readyCount())
	}
	// Requeue after finish is a no-op.
	t3, ok := q.pop(context.Background())
	if !ok || t3 != t1 {
		t.Fatalf("pop3 = %v,%v, want same task", t3, ok)
	}
	q.finish(t3)
	q.requeue(t3)
	if st, _ := q.state(0); st != segDone {
		t.Fatalf("state[0] = %v after finish+requeue", st)
	}
	if q.remaining() != 1 {
		t.Fatalf("remaining = %d, want 1", q.remaining())
	}
}

func TestSegmentQueueFinishIdempotent(t *testing.T) {
	q := newSegmentQueue([]types.HLSSegmentState{segRow(0)}, nil)
	t1, _ := q.pop(context.Background())
	q.finish(t1)
	q.finish(t1) // duplicate finish must not double-count
	if q.remaining() != 0 || q.doneCount() != 1 {
		t.Fatalf("remaining=%d done=%d", q.remaining(), q.doneCount())
	}
	// Finish on a never-leased task is a no-op.
	q.finish(&segmentTask{seg: segRow(9)})
	if q.doneCount() != 1 {
		t.Fatalf("doneCount = %d", q.doneCount())
	}
}

func TestSegmentQueuePushIdempotent(t *testing.T) {
	q := newSegmentQueue([]types.HLSSegmentState{segRow(0)}, nil)
	if !q.push(segRow(5)) {
		t.Fatal("push new index refused")
	}
	if q.push(segRow(5)) {
		t.Fatal("duplicate push accepted")
	}
	if q.push(segRow(0)) {
		t.Fatal("existing index push accepted")
	}
	if q.remaining() != 2 {
		t.Fatalf("remaining = %d, want 2", q.remaining())
	}
	q.close()
	if q.push(segRow(6)) {
		t.Fatal("push after close accepted")
	}
}

func TestSegmentQueueRemainingIsNotCompletion(t *testing.T) {
	q := newSegmentQueue([]types.HLSSegmentState{segRow(0), segRow(1)}, nil)
	if _, ok := q.pop(context.Background()); !ok {
		t.Fatal("pop1")
	}
	if _, ok := q.pop(context.Background()); !ok {
		t.Fatal("pop2")
	}
	// Ready is empty but two leases are outstanding — not done.
	if q.readyCount() != 0 {
		t.Fatalf("ready = %d", q.readyCount())
	}
	if q.remaining() != 2 {
		t.Fatalf("remaining = %d, want 2 — leased work still counts", q.remaining())
	}
}

func TestSegmentQueuePopWakesOnClose(t *testing.T) {
	q := newSegmentQueue(nil, nil)
	done := make(chan bool, 1)
	go func() {
		_, ok := q.pop(context.Background())
		done <- ok
	}()
	time.Sleep(20 * time.Millisecond)
	q.close()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("pop returned work after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pop did not wake on close")
	}
	// Second close is idempotent.
	q.close()
}

func TestSegmentQueuePopWakesOnCancel(t *testing.T) {
	q := newSegmentQueue(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		_, ok := q.pop(ctx)
		done <- ok
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("pop returned work after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pop did not wake on cancel")
	}
}

func TestSegmentQueueConcurrentPopRequeue(t *testing.T) {
	segs := make([]types.HLSSegmentState, 50)
	for i := range segs {
		segs[i] = segRow(i)
	}
	q := newSegmentQueue(segs, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	finished := make(map[int]int)
	for range 8 {
		wg.Go(func() {
			for {
				t1, ok := q.pop(context.Background())
				if !ok {
					return
				}
				// Requeue a quarter once to stress the lease churn, then
				// finish — a requeued segment may lease twice but settles once.
				if t1.seg.Index%4 == 0 && t1.retries == 0 {
					t1.retries++
					q.requeue(t1)
					continue
				}
				q.finish(t1)
				mu.Lock()
				finished[t1.seg.Index]++
				mu.Unlock()
			}
		})
	}
	// Terminator: once everything is done, close so pops drain.
	go func() {
		for {
			if q.remaining() == 0 {
				q.close()
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	wg.Wait()
	if q.remaining() != 0 {
		t.Fatalf("remaining = %d after all work", q.remaining())
	}
	for i := range 50 {
		if finished[i] != 1 {
			t.Fatalf("segment %d finished %d times, want exactly once", i, finished[i])
		}
	}
}
