package hls

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
)

// Drain/retire lost-race end state: markDraining killed sess.ctx but the
// retire won the pubMu race, so no stopReason was ever published and the
// attempt surfaces a bare context.Canceled. The drain witness must route
// it as a free requeue — a genuine transport cancel on a live session
// still spends budget.
func TestHLSSettleDrainWitnessRequeuesFree(t *testing.T) {
	for _, retries := range []int{0, hlsMaxSegmentRetries} {
		_, e := retryFixture(t, nil, nil)
		task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://cdn.test/seg0"}}
		task.retries = retries
		sess, at := newUnitAttempt(e, task)
		sess.cancel() // markDraining's kill; the mark was lost to the retire
		o := e.settleOutcome(sess, at, task, context.Canceled, e.now())
		if o.action != outcomeRequeue {
			t.Fatalf("retries=%d: action = %v, want requeue (err=%v)", retries, o.action, o.err)
		}
		if task.retries != retries || task.genericAttempt != 0 {
			t.Fatalf("retries=%d: counters = %d/%d, drain must stay free",
				retries, task.retries, task.genericAttempt)
		}
	}
}

func TestHLSSettleCancelOnLiveSessionBurnsBudget(t *testing.T) {
	_, e := retryFixture(t, nil, nil)
	task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://cdn.test/seg0"}}
	sess, at := newUnitAttempt(e, task)
	// sess.ctx alive — the cancel came from the transport, not drain.
	o := e.settleOutcome(sess, at, task, context.Canceled, e.now())
	if o.action != outcomeRetry {
		t.Fatalf("action = %v, want retry", o.action)
	}
	if task.retries != 1 || task.genericAttempt != 1 {
		t.Fatalf("counters = %d/%d, want 1/1", task.retries, task.genericAttempt)
	}
}

// A snapshot taken while the task sits in backoff must project the retry
// count the settle just bumped — retirement copied the pre-bump seed.
func TestHLSRetainedRetryTracksSettle(t *testing.T) {
	_, e := retryFixture(t, nil, nil)
	task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://cdn.test/seg0"}}
	sess := e.newSession(0)
	at := e.newAttempt(sess, task)
	sess.publishAttempt(at)
	sess.retireAttempt(at, e.now())

	o := e.settleOutcome(sess, at, task, context.Canceled, e.now())
	if o.action != outcomeRetry {
		t.Fatalf("action = %v, want retry", o.action)
	}
	sess.pubMu.Lock()
	got := sess.retained.retry
	sess.pubMu.Unlock()
	if got != 1 {
		t.Fatalf("retained.retry = %d, want 1 — stale projection during backoff", got)
	}
}

// wait() is terminal: after it observes, no spawn may resurrect a worker
// on the released transport lease, and start() is a contract fault.
func TestHLSWaitSealsEngine(t *testing.T) {
	_, e := retryFixture(t, nil, nil)
	e.setAdmissionOpen(true)
	// The fixture queue still holds an unaccepted segment — the wait itself
	// reports incomplete; the seal is what this test asserts.
	if err := e.wait(); err != nil && !errors.Is(err, errEngineIncomplete) {
		t.Fatalf("wait on unstarted engine = %v", err)
	}
	if e.spawnWorker() {
		t.Fatal("spawnWorker after wait accepted")
	}
	if err := e.start(1); err == nil {
		t.Fatal("start after wait accepted")
	} else if _, ok := errors.AsType[*hlsContractError](err); !ok {
		t.Fatalf("start err = %v, want *hlsContractError", err)
	}
}

func TestHLSStopThenSpawnRefused(t *testing.T) {
	_, e := retryFixture(t, nil, nil)
	e.setAdmissionOpen(true)
	e.stop()
	if e.spawnWorker() {
		t.Fatal("spawnWorker after stop accepted")
	}
}

// Inside the host-penalty window the attempt is wait-protected: the stall
// gate must refuse while the limiter wait is in flight.
func TestHLSHostPenaltyWindowRefusesStall(t *testing.T) {
	now := time.Now().Add(time.Hour)
	inWait := make(chan struct{})
	_, e := retryFixture(t, func(ctx context.Context, d time.Duration) error {
		select {
		case <-inWait:
		default:
			close(inWait)
		}
		<-ctx.Done()
		return ctx.Err()
	}, func() time.Time { return now })
	task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://cdn.test/seg0"}}
	host := transport.MirrorHost(task.seg.URI)
	e.hostLimiter.Penalize(host, 2*time.Hour, true, now)

	sess := e.newSession(0)
	at := e.newAttempt(sess, task)
	sess.publishAttempt(at)
	done := make(chan error, 1)
	go func() { done <- e.waitHostPenalty(sess, at, task.seg.URI) }()
	deadline := time.Now().Add(3 * time.Second)
	for !at.waitingOnLimiter.Load() {
		if time.Now().After(deadline) {
			t.Fatal("attempt never entered the host-penalty wait")
		}
		time.Sleep(time.Millisecond)
	}
	sess.pubMu.Lock()
	ph := at.phase
	sess.pubMu.Unlock()
	if ph != hlsPhaseWait {
		t.Fatalf("phase = %v inside host penalty, want wait", ph)
	}
	if sess.cancelAttemptStall(at.gen) {
		t.Fatal("stall accepted during host-penalty wait")
	}
	at.cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waitHostPenalty err = %v, want ctx cancel", err)
	}
	if at.waitingOnLimiter.Load() {
		t.Fatal("waiting flag leaked past the wait")
	}
	sess.pubMu.Lock()
	ph = at.phase
	sess.pubMu.Unlock()
	if ph != hlsPhaseNetwork {
		t.Fatalf("phase = %v after host penalty, want network restored", ph)
	}
}

func TestHLSClassifyRequestTimeoutKeepsGenericZero(t *testing.T) {
	_, e := retryFixture(t, nil, nil)
	task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://cdn.test/seg0"}}
	se := &hlsStatusError{resource: "segment", index: 0, status: http.StatusRequestTimeout}
	o := e.classifyStatus(task, se)
	if o.action != outcomeRetry {
		t.Fatalf("408 action = %v, want retry", o.action)
	}
	if task.retries != 1 || task.genericAttempt != 0 {
		t.Fatalf("counters = %d/%d — 408 burns retries, never the connection counter",
			task.retries, task.genericAttempt)
	}
}

func TestHLSClassifyRangeNotSatisfiableSentinel(t *testing.T) {
	_, e := retryFixture(t, nil, nil)
	task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://cdn.test/seg0"}}
	se := &hlsStatusError{resource: "segment", index: 0, status: http.StatusRequestedRangeNotSatisfiable}
	o := e.classifyStatus(task, se)
	if o.action != outcomeFatal {
		t.Fatalf("416 action = %v, want fatal", o.action)
	}
	if !errors.Is(o.err, types.ErrHLSInvalidSegmentResponse) {
		t.Fatalf("416 err = %v, want ErrHLSInvalidSegmentResponse", o.err)
	}
	if errors.Is(o.err, types.ErrPermanentHTTP) {
		t.Fatalf("416 err = %v, must not alias permanent-HTTP", o.err)
	}
	var typed *hlsStatusError
	if !errors.As(o.err, &typed) || typed.status != 416 {
		t.Fatalf("416 lost typed status: %v", o.err)
	}
	if task.retries != 0 {
		t.Fatalf("416 moved retries to %d, want zero-retry terminal", task.retries)
	}
}

// Content-Range totals are exclusive of the inclusive end index: the
// smallest legal total is end+1; anything at or below end is impossible.
func TestHLSValidateSegmentResponseRangeTotalBoundary(t *testing.T) {
	resp := func(cr string) *http.Response {
		return &http.Response{
			Header:        http.Header{"Content-Range": {cr}},
			ContentLength: -1,
			Body:          http.NoBody,
		}
	}
	br := &hlsByteRange{offset: 0, length: 100} // end = 99
	for _, tc := range []struct {
		cr      string
		wantErr bool
	}{
		{"bytes 0-99/100", false}, // total == end+1 — the boundary
		{"bytes 0-99/150", false}, // range inside a larger object
		{"bytes 0-99/*", false},
		{"bytes 0-99/99", true}, // total == end — impossible
		{"bytes 0-99/50", true},
		{"bytes 0-99/0", true},
	} {
		r := resp(tc.cr)
		err := validateSegmentResponse(r, br, 0)
		_ = r.Body.Close()
		if tc.wantErr {
			if !errors.Is(err, types.ErrHLSInvalidSegmentResponse) {
				t.Fatalf("%s: err = %v, want invalid-response sentinel", tc.cr, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: err = %v, want accepted", tc.cr, err)
		}
	}
}

// Live drain barrier: the attempt is parked inside the network phase when
// markDraining lands. Whichever side of the retire race wins, the segment
// must come back free — never a budget burn, never a fatal latch.
func TestHLSDrainDuringNetworkPhaseKeepsBudget(t *testing.T) {
	release := make(chan struct{})
	inflight := make(chan struct{})
	var inflightOnce sync.Once
	var releaseOnce sync.Once
	// Unblock the parked segment handler on any failure path, or the
	// fixture's httptest.Server.Close wedges until the test timeout;
	// defer unwinds before any t.Cleanup, so the fixture's own
	// registration order can never strand this behind srv.Close.
	defer releaseOnce.Do(func() { close(release) })
	f := newHLSFixture(t, [][]byte{[]byte("x")}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			inflightOnce.Do(func() { close(inflight) })
			<-release
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	e := f.buildEngine(t, fixtureOpts{
		sleep: func(ctx context.Context, d time.Duration) error { return nil },
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	select {
	case <-inflight:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never reached the network phase")
	}
	ids := e.liveSessionIDs()
	if len(ids) != 1 || !e.markDraining(ids[0]) {
		t.Fatalf("drain failed: sessions=%v", ids)
	}
	releaseOnce.Do(func() { close(release) })
	if err := e.wait(); !errors.Is(err, errEngineIncomplete) {
		t.Fatalf("wait = %v, want errEngineIncomplete (lease requeued)", err)
	}
	if e.queue.tasks[0].retries != 0 || e.queue.tasks[0].genericAttempt != 0 {
		t.Fatalf("drain burned budget: retries=%d generic=%d",
			e.queue.tasks[0].retries, e.queue.tasks[0].genericAttempt)
	}
	if st, _ := e.queue.state(0); st != segReady {
		t.Fatalf("seg0 state = %v, want requeued ready", st)
	}
}

func TestHLSKeyTableNilSegmentIsContract(t *testing.T) {
	cache := newHLSKeyCache()
	_, err := cache.keyTable(nil, nil, func(string) ([16]byte, error) {
		t.Fatal("fetch invoked for nil segment")
		return [16]byte{}, nil
	})
	if _, ok := errors.AsType[*hlsContractError](err); !ok {
		t.Fatalf("nil segment err = %v, want *hlsContractError", err)
	}
}
