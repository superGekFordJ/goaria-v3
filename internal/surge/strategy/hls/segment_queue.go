package hls

import (
	"context"
	"sync"

	"goaria-v3/internal/surge/types"
)

// segmentTask is the internal per-segment work item: the media descriptor
// plus the in-process retry counters that must survive worker switches.
// It is not the persisted HLSSegmentState and never becomes types.Task —
// no byte-range bitmap, stealing, or hedging semantics attach to it.
type segmentTask struct {
	seg             types.HLSSegmentState
	retries         int  // generic retry budget consumed (cap hlsMaxSegmentRetries)
	notFoundRetries int  // 404 short-retry count (cap 2)
	genericAttempt  int  // connection-class failures, projected to RetryCount
	refreshRequired bool // 403/410 under open admission — consumed by the shared manifest refresh
	// lastSeenRefreshGen records which refresh generation produced the URI
	// snapshot this task's last request actually used. Stamped at attempt
	// snapshot time and cleared lazily — the refresh publisher never moves
	// it, so a late old-generation 403 can not be misread as the new URI
	// failing. Guarded by q.mu together with seg.URI and refreshRequired.
	lastSeenRefreshGen uint64
}

type segLeaseState int

const (
	segReady segLeaseState = iota
	segLeased
	segDone
)

// segmentQueue is the HLS-owned FIFO: ready indexes, one lease per pop,
// per-index state, and a wake channel instead of polling. A temporarily
// empty ready list is not completion — pending counts unfinished leases and
// backoff-held work until close.
type segmentQueue struct {
	mu      sync.Mutex
	notify  chan struct{} // closed+recreated on every state change
	ready   []int
	tasks   map[int]*segmentTask
	states  map[int]segLeaseState
	pending int // indexes not yet done (ready + leased)
	closed  bool
}

// newSegmentQueue indexes segs by Index; entries with completed(index) true
// start done and never enter the ready list — the prepared resume prefix is
// not re-fetched.
func newSegmentQueue(segs []types.HLSSegmentState, completed func(index int) bool) *segmentQueue {
	q := &segmentQueue{
		notify: make(chan struct{}),
		tasks:  make(map[int]*segmentTask, len(segs)),
		states: make(map[int]segLeaseState, len(segs)),
	}
	for _, seg := range segs {
		q.tasks[seg.Index] = &segmentTask{seg: seg}
		if completed != nil && completed(seg.Index) {
			q.states[seg.Index] = segDone
			continue
		}
		q.states[seg.Index] = segReady
		q.ready = append(q.ready, seg.Index)
		q.pending++
	}
	return q
}

func (q *segmentQueue) wakeLocked() {
	close(q.notify)
	q.notify = make(chan struct{})
}

// pop leases the head segment; it returns false only when no more work can
// ever arrive (queue closed) or ctx fired while waiting.
func (q *segmentQueue) pop(ctx context.Context) (*segmentTask, bool) {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return nil, false
		}
		if len(q.ready) > 0 {
			idx := q.ready[0]
			q.ready = q.ready[1:]
			q.states[idx] = segLeased
			t := q.tasks[idx]
			q.mu.Unlock()
			return t, true
		}
		ch := q.notify
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false
		case <-ch:
		}
	}
}

// requeue ends the caller's lease and returns the whole segment to the head
// of ready exactly once — kill/drain re-dispatch never sends a mid-segment
// Range, and retry counters ride along on the task.
func (q *segmentQueue) requeue(t *segmentTask) {
	if t == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.states[t.seg.Index] != segLeased {
		return // finished or never leased — no double queue
	}
	q.states[t.seg.Index] = segReady
	if q.closed {
		return
	}
	q.ready = append([]int{t.seg.Index}, q.ready...)
	q.wakeLocked()
}

// finish settles a leased task as done (accepted or duplicate). Calling it
// on a done/ready index is a no-op — a duplicate finish can neither
// double-count pending nor resurrect a lease.
func (q *segmentQueue) finish(t *segmentTask) {
	if t == nil {
		return
	}
	q.mu.Lock()
	if q.states[t.seg.Index] == segLeased {
		q.states[t.seg.Index] = segDone
		q.pending--
	}
	q.mu.Unlock()
}

// push adds one untracked segment to the tail of ready. It is idempotent by
// index — an existing (ready/leased/done) index or a closed queue returns
// false rather than fabricating a second lease or reporting a false success.
func (q *segmentQueue) push(seg types.HLSSegmentState) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	if _, exists := q.tasks[seg.Index]; exists {
		return false
	}
	q.tasks[seg.Index] = &segmentTask{seg: seg}
	q.states[seg.Index] = segReady
	q.ready = append(q.ready, seg.Index)
	q.pending++
	q.wakeLocked()
	return true
}

// remaining reports unfinished work: ready plus leased (including leases
// held during backoff). An empty ready list alone is not completion.
func (q *segmentQueue) remaining() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pending
}

func (q *segmentQueue) readyCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ready)
}

// doneCount reports settled segments (accepted or duplicate).
func (q *segmentQueue) doneCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.tasks) - q.pending
}

// state exposes one index's lease state for tests and lifecycle checks.
func (q *segmentQueue) state(index int) (segLeaseState, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s, ok := q.states[index]
	return s, ok
}

// close is idempotent: every blocked or future pop observes it, and pushes
// after close fail rather than silently dropping work.
func (q *segmentQueue) close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.wakeLocked()
	}
	q.mu.Unlock()
}
