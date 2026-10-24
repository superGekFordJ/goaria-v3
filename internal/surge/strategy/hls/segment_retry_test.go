package hls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
)

// retryFixture builds an un-started engine over the shared fixture so the
// classifier can be exercised directly. now/sleep stay injectable.
func retryFixture(t *testing.T, sleep func(context.Context, time.Duration) error, now func() time.Time) (*hlsFixture, *segmentEngine) {
	t.Helper()
	f := newHLSFixture(t, [][]byte{[]byte("body")}, fixtureOpts{})
	e := f.buildEngine(t, fixtureOpts{sleep: sleep, now: now})
	return f, e
}

func newUnitAttempt(e *segmentEngine, task *segmentTask) (*hlsWorkerSession, *hlsAttempt) {
	sess := e.newSession(1)
	at := &hlsAttempt{gen: 1, index: task.seg.Index, task: task, sess: sess}
	return sess, at
}

func TestHLSSegmentRetryDelayLadder(t *testing.T) {
	want := []time.Duration{
		500 * time.Millisecond, time.Second,
		2 * time.Second, 4 * time.Second, 8 * time.Second,
	}
	for i, w := range want {
		if got := hlsRetryDelay(i + 1); got != w {
			t.Fatalf("delay(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := hlsRetryDelay(0); got != 500*time.Millisecond {
		t.Fatalf("delay(0) = %v", got)
	}
	if got := hlsRetryDelay(99); got != hlsRetryMaxDelay {
		t.Fatalf("delay(99) = %v, want capped %v", got, hlsRetryMaxDelay)
	}
}

func TestHLSSegmentRetryAfterValidation(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw    string
		want   time.Duration
		wantOK bool
	}{
		{"0", 0, true},
		{"5", 5 * time.Second, true},
		{"30", 30 * time.Second, true},
		{" 7 ", 7 * time.Second, true},
		{"31", 0, false},
		{"-1", 0, false},
		{"99999999999999999999", 0, false},
		{"abc", 0, false},
		{"", 0, false},
		{"5.5", 0, false},
		{now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second, true},
		{now.Add(-3 * time.Second).Format(http.TimeFormat), 0, false},
		{now.Add(31 * time.Second).Format(http.TimeFormat), 0, false},
	} {
		got, ok := hlsValidRetryAfter(tc.raw, now)
		if ok != tc.wantOK {
			t.Fatalf("Retry-After %q: ok = %v, want %v", tc.raw, ok, tc.wantOK)
		}
		if ok && (got < tc.want-time.Second || got > tc.want+time.Second) {
			t.Fatalf("Retry-After %q: d = %v, want ~%v", tc.raw, got, tc.want)
		}
	}
}

func TestHLSSegmentRetrySettleClassification(t *testing.T) {
	rows := []struct {
		name    string
		err     error
		stop    hlsStopReason
		pre     func(task *segmentTask)
		action  outcomeAction
		retries int
		generic int
		check   func(t *testing.T, o attemptOutcome)
	}{
		{
			name:   "SegmentDataErrorRetries",
			err:    newSegmentDataError(0, "bad block"),
			action: outcomeRetry, retries: 1, generic: 0,
			check: func(t *testing.T, o attemptOutcome) {
				if o.delay != 500*time.Millisecond {
					t.Fatalf("delay = %v", o.delay)
				}
			},
		},
		{
			name:   "UnexpectedEOFGeneric",
			err:    fmt.Errorf("body: %w", io.ErrUnexpectedEOF),
			action: outcomeRetry, retries: 1, generic: 1,
		},
		{
			name:   "NoProgressGeneric",
			err:    io.ErrNoProgress,
			action: outcomeRetry, retries: 1, generic: 1,
		},
		{
			name:   "DeadlineGeneric",
			err:    fmt.Errorf("read: %w", context.DeadlineExceeded),
			action: outcomeRetry, retries: 1, generic: 1,
		},
		{
			name:   "StorageFatal",
			err:    newStorageError("write", "/x", errors.New("io boom")),
			action: outcomeFatal,
			check: func(t *testing.T, o attemptOutcome) {
				if _, ok := errors.AsType[*storageError](o.err); !ok {
					t.Fatalf("err = %v, want *storageError", o.err)
				}
			},
		},
		{
			name:   "StorageBeatsKillReason",
			err:    newStorageError("write", "/x", errors.New("io")),
			stop:   hlsStopKill,
			action: outcomeFatal,
		},
		{
			name:   "ContractFatal",
			err:    newHLSContractError("impossible"),
			action: outcomeFatal,
		},
		{
			name:   "PermanentHTTPFatal",
			err:    fmt.Errorf("wrap: %w", types.ErrPermanentHTTP),
			action: outcomeFatal,
		},
		{
			name:   "InvalidResponseFatal",
			err:    fmt.Errorf("wrap: %w", types.ErrHLSInvalidSegmentResponse),
			action: outcomeFatal,
		},
		{
			name:   "WindowClosedFatal",
			err:    errWindowClosed,
			action: outcomeFatal,
		},
		{
			name:   "RedirectRefusalFatal",
			err:    &hlsRedirectRefusalError{err: errors.New("caller said no")},
			action: outcomeFatal,
		},
		{
			name:   "RedirectUserinfoFatal",
			err:    errRedirectUserinfo,
			action: outcomeFatal,
		},
		{
			name:   "MaxRedirectsFatal",
			err:    fmt.Errorf("wrap: %w", types.ErrMaxRedirects),
			action: outcomeFatal,
		},
		{
			name:   "KillFreeRequeue",
			err:    context.Canceled,
			stop:   hlsStopKill,
			action: outcomeRequeue, retries: 0, generic: 0,
		},
		{
			name:   "DrainFreeRequeue",
			err:    context.Canceled,
			stop:   hlsStopDrain,
			action: outcomeRequeue, retries: 0, generic: 0,
		},
		{
			name:   "StallConsumesBudget",
			err:    context.Canceled,
			stop:   hlsStopStall,
			action: outcomeRetry, retries: 1, generic: 1,
		},
		{
			name:   "StallExhaustsBudget",
			err:    context.Canceled,
			stop:   hlsStopStall,
			pre:    func(task *segmentTask) { task.retries = hlsMaxSegmentRetries },
			action: outcomeFatal, retries: hlsMaxSegmentRetries + 1, generic: 1,
			check: func(t *testing.T, o attemptOutcome) {
				if !errors.Is(o.err, errAttemptStall) {
					t.Fatalf("err = %v, want errAttemptStall", o.err)
				}
			},
		},
		{
			name:   "ImpossibleNoResult",
			err:    nil,
			action: outcomeFatal,
			check: func(t *testing.T, o attemptOutcome) {
				if _, ok := errors.AsType[*hlsContractError](o.err); !ok {
					t.Fatalf("err = %v, want *hlsContractError", o.err)
				}
			},
		},
		{
			name:   "BudgetExhaustedPreservesCause",
			err:    io.ErrUnexpectedEOF,
			pre:    func(task *segmentTask) { task.retries = hlsMaxSegmentRetries },
			action: outcomeFatal, retries: hlsMaxSegmentRetries + 1, generic: 1,
			check: func(t *testing.T, o attemptOutcome) {
				if !errors.Is(o.err, io.ErrUnexpectedEOF) {
					t.Fatalf("err = %v, want cause preserved", o.err)
				}
			},
		},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			_, e := retryFixture(t, func(ctx context.Context, d time.Duration) error { return nil }, nil)
			task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://example.test/seg0"}}
			if tc.pre != nil {
				tc.pre(task)
			}
			sess, at := newUnitAttempt(e, task)
			at.stopReason = tc.stop
			o := e.settleOutcome(sess, at, task, tc.err, e.now())
			if o.action != tc.action {
				t.Fatalf("action = %v, want %v (err=%v)", o.action, tc.action, o.err)
			}
			if task.retries != tc.retries || task.genericAttempt != tc.generic {
				t.Fatalf("retries=%d generic=%d, want %d/%d",
					task.retries, task.genericAttempt, tc.retries, tc.generic)
			}
			if tc.check != nil {
				tc.check(t, o)
			}
		})
	}
}

func TestHLSSegmentRetryStatusClassification(t *testing.T) {
	// The fake clock stays ahead of the real one: the shared host limiter's
	// cleanup uses wall time and would instantly evict a past deadline.
	now := time.Now().Add(time.Hour)
	newE := func(t *testing.T) *segmentEngine {
		_, e := retryFixture(t, func(ctx context.Context, d time.Duration) error { return nil },
			func() time.Time { return now })
		return e
	}
	mkTask := func() *segmentTask {
		return &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://cdn.test/seg0"}}
	}
	se := func(status int, ra string) *hlsStatusError {
		return &hlsStatusError{resource: "segment", index: 0, status: status, retryAfter: ra}
	}

	t.Run("NotFoundShortBudget", func(t *testing.T) {
		e := newE(t)
		task := mkTask()
		for i := range hlsNotFoundMaxRetries {
			o := e.classifyStatus(task, se(404, ""))
			if o.action != outcomeRetry {
				t.Fatalf("404 #%d: action = %v", i+1, o.action)
			}
		}
		o := e.classifyStatus(task, se(404, ""))
		if o.action != outcomeFatal || !errors.Is(o.err, types.ErrPermanentHTTP) {
			t.Fatalf("404 #%d: action = %v err = %v", hlsNotFoundMaxRetries+1, o.action, o.err)
		}
		if task.notFoundRetries != 3 || task.genericAttempt != 0 {
			t.Fatalf("notFound=%d generic=%d", task.notFoundRetries, task.genericAttempt)
		}
	})

	t.Run("NotFoundNeverExceedsGlobalBudget", func(t *testing.T) {
		e := newE(t)
		task := mkTask()
		task.retries = hlsMaxSegmentRetries // generic budget spent elsewhere
		o := e.classifyStatus(task, se(404, ""))
		if o.action != outcomeFatal || !errors.Is(o.err, types.ErrPermanentHTTP) {
			t.Fatalf("action = %v err = %v, want permanent", o.action, o.err)
		}
	})

	t.Run("TooManyRequestsRetryAfter", func(t *testing.T) {
		e := newE(t)
		task := mkTask()
		o := e.classifyStatus(task, se(429, "2"))
		if o.action != outcomeRetry || o.delay != 2*time.Second {
			t.Fatalf("action = %v delay = %v, want retry 2s", o.action, o.delay)
		}
		if task.genericAttempt != 0 {
			t.Fatalf("explicit RA moved genericAttempt: %d", task.genericAttempt)
		}
		host := transport.MirrorHost(task.seg.URI)
		if until := e.hostLimiter.BlockedUntil(host, now); !until.After(now) {
			t.Fatalf("host not penalized, blockedUntil = %v", until)
		}
	})

	t.Run("TooManyRequestsNoRetryAfter", func(t *testing.T) {
		e := newE(t)
		task := mkTask()
		o := e.classifyStatus(task, se(429, ""))
		if o.action != outcomeRetry || o.delay != 500*time.Millisecond {
			t.Fatalf("action = %v delay = %v", o.action, o.delay)
		}
		if task.genericAttempt != 1 {
			t.Fatalf("RA-less 429 must count generic attempt, got %d", task.genericAttempt)
		}
	})

	t.Run("ServiceUnavailableRetryAfter", func(t *testing.T) {
		e := newE(t)
		task := mkTask()
		o := e.classifyStatus(task, se(503, "5"))
		if o.action != outcomeRetry || o.delay != 5*time.Second {
			t.Fatalf("action = %v delay = %v", o.action, o.delay)
		}
		if task.genericAttempt != 0 {
			t.Fatalf("genericAttempt = %d", task.genericAttempt)
		}
	})

	t.Run("ServerErrorsExponential", func(t *testing.T) {
		for _, status := range []int{500, 502, 504} {
			e := newE(t)
			task := mkTask()
			o := e.classifyStatus(task, se(status, ""))
			if o.action != outcomeRetry || o.delay != 500*time.Millisecond {
				t.Fatalf("%d: action = %v delay = %v", status, o.action, o.delay)
			}
			if task.genericAttempt != 0 {
				t.Fatalf("%d moved genericAttempt", status)
			}
		}
	})

	t.Run("ServerErrorRetryAfterHonored", func(t *testing.T) {
		e := newE(t)
		task := mkTask()
		o := e.classifyStatus(task, se(503, "9"))
		if o.action != outcomeRetry || o.delay != 9*time.Second {
			t.Fatalf("action = %v delay = %v, want retry 9s", o.action, o.delay)
		}
	})

	t.Run("ForbiddenGateClosedPermanent", func(t *testing.T) {
		e := newE(t)
		o := e.classifyStatus(mkTask(), se(403, ""))
		if o.action != outcomeFatal || !errors.Is(o.err, types.ErrPermanentHTTP) {
			t.Fatalf("403 closed: action = %v err = %v", o.action, o.err)
		}
	})

	t.Run("GoneGateClosedPermanent", func(t *testing.T) {
		e := newE(t)
		o := e.classifyStatus(mkTask(), se(410, ""))
		if o.action != outcomeFatal || !errors.Is(o.err, types.ErrPermanentHTTP) {
			t.Fatalf("410 closed: action = %v err = %v", o.action, o.err)
		}
	})

	t.Run("ForbiddenGateOpenRefreshCandidate", func(t *testing.T) {
		e := newE(t)
		e.setAdmissionOpen(true)
		task := mkTask()
		o := e.classifyStatus(task, se(403, ""))
		if o.action != outcomeRetry {
			t.Fatalf("403 open: action = %v, want retry on original URI", o.action)
		}
		if !task.refreshRequired {
			t.Fatal("403 open did not mark refreshRequired")
		}
	})

	t.Run("GoneGateOpenRefreshCandidate", func(t *testing.T) {
		e := newE(t)
		e.setAdmissionOpen(true)
		task := mkTask()
		o := e.classifyStatus(task, se(410, ""))
		if o.action != outcomeRetry || !task.refreshRequired {
			t.Fatalf("410 open: action = %v refreshRequired = %v", o.action, task.refreshRequired)
		}
	})

	t.Run("RequestTimeoutRetries", func(t *testing.T) {
		e := newE(t)
		task := mkTask()
		o := e.classifyStatus(task, se(408, ""))
		if o.action != outcomeRetry || task.genericAttempt != 1 {
			t.Fatalf("408: action = %v generic = %d", o.action, task.genericAttempt)
		}
	})

	t.Run("ClientErrorsPermanent", func(t *testing.T) {
		for _, status := range []int{400, 401, 402, 405, 418, 451} {
			e := newE(t)
			task := mkTask()
			o := e.classifyStatus(task, se(status, ""))
			if o.action != outcomeFatal || !errors.Is(o.err, types.ErrPermanentHTTP) {
				t.Fatalf("%d: action = %v err = %v, want permanent", status, o.action, o.err)
			}
			if task.retries != 0 {
				t.Fatalf("%d consumed retries", status)
			}
		}
	})

	t.Run("Sub400IsContractViolation", func(t *testing.T) {
		e := newE(t)
		o := e.classifyStatus(mkTask(), se(301, ""))
		if o.action != outcomeFatal || !errors.Is(o.err, types.ErrHLSInvalidSegmentResponse) {
			t.Fatalf("301: action = %v err = %v", o.action, o.err)
		}
	})
}

// TestHLSSegmentRetryDeadlineCombinesMax proves waitRetry sleeps ONCE until
// max(retry deadline, host penalty) — never two sequential sleeps.
func TestHLSSegmentRetryDeadlineCombinesMax(t *testing.T) {
	// Ahead-of-wall fake clock so the limiter cleanup never evicts it.
	base := time.Now().Add(time.Hour)
	cur := base
	var mu sync.Mutex
	var calls []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		calls = append(calls, d)
		mu.Unlock()
		return nil
	}
	_, e := retryFixture(t, sleep, func() time.Time { return cur })
	task := &segmentTask{seg: types.HLSSegmentState{Index: 0, URI: "http://cdn.test/seg0"}}
	sess := e.newSession(1)
	host := transport.MirrorHost(task.seg.URI)

	// Host penalty dominates: one sleep until the penalty deadline — read
	// the stored deadline back so the limiter's clamp+jitter stays internal.
	e.hostLimiter.Penalize(host, 30*time.Second, true, cur)
	until := e.hostLimiter.BlockedUntil(host, cur)
	if !until.After(cur) {
		t.Fatal("penalty did not stick")
	}
	if err := e.waitRetry(sess, task, time.Second); err != nil {
		t.Fatalf("waitRetry = %v", err)
	}
	if len(calls) != 1 || calls[0] != until.Sub(cur) {
		t.Fatalf("sleeps = %v, want one %v sleep (max, not additive)", calls, until.Sub(cur))
	}

	// Once the fake clock passes the penalty deadline the segment's own
	// retry deadline dominates again — still a single sleep.
	cur = until.Add(time.Second)
	if err := e.waitRetry(sess, task, 5*time.Second); err != nil {
		t.Fatalf("waitRetry = %v", err)
	}
	if len(calls) != 2 || calls[1] != 5*time.Second {
		t.Fatalf("sleeps = %v, want trailing 5s sleep", calls)
	}
}
