package hls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
)

const (
	// hlsMaxSegmentRetries bounds retries after the initial attempt — a
	// segment may see at most 6 attempts.
	hlsMaxSegmentRetries = 5
	// hlsNotFoundMaxRetries is the extra per-segment 404 cap (2 short
	// retries), never additive beyond the generic budget.
	hlsNotFoundMaxRetries = 2

	hlsRetryBaseDelay = 500 * time.Millisecond
	hlsRetryMaxDelay  = 8 * time.Second
	hlsRetryAfterMax  = 30 * time.Second
)

// errAttemptStall marks a stall-primitive cancel that exhausted the retry
// budget — a timeout-class cause, never the free kill path.
var errAttemptStall = errors.New("hls: segment attempt stalled out")

// hlsRetryDelay is the deterministic exponential ladder for the nth retry
// (1-based): 500ms, 1s, 2s, 4s, 8s — capped, never beyond.
func hlsRetryDelay(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > hlsMaxSegmentRetries {
		return hlsRetryMaxDelay
	}
	d := hlsRetryBaseDelay << (n - 1)
	if d <= 0 || d > hlsRetryMaxDelay {
		return hlsRetryMaxDelay
	}
	return d
}

// hlsValidRetryAfter re-validates transport.ParseRetryAfter locally: only a
// delta-seconds or HTTP-date delay within [0,30s] is honored. Negative/past
// values, malformed input, oversized seconds and Duration overflow all fall
// back to the exponential ladder — an out-of-range value is never clamped
// into looking valid.
func hlsValidRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(raw); err == nil {
		// Bound before the seconds multiplication so no overflow survives.
		if n < 0 || int64(n) > int64(hlsRetryAfterMax/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	d, ok := transport.ParseRetryAfter(raw, now)
	if !ok || d < 0 || d > hlsRetryAfterMax {
		return 0, false
	}
	return d, true
}

type outcomeAction int

const (
	outcomeDone    outcomeAction = iota // segment settled (accepted or duplicate)
	outcomeRetry                        // consume budget, wait delay, same lease retries
	outcomeRequeue                      // free requeue — kill/drain, no budget spent
	outcomeFatal                        // terminal — latch and stop the engine
	outcomeExit                         // leave quietly (external cancel or a latched fatal)
)

type attemptOutcome struct {
	action outcomeAction
	delay  time.Duration
	err    error
}

// settleOutcome classifies one finished attempt in strict priority:
//  1. Accepted/Duplicate settle (a kept credit and its error both stand),
//  2. storage/contract fatals — never swallowed by kill classification,
//  3. external ctx identity — an internal peer-stop can never mask a fatal,
//  4. explicit kill/drain/stall stop reasons,
//  5. HTTP status / transport / data / unknown cause routing.
func (e *segmentEngine) settleOutcome(
	sess *hlsWorkerSession,
	at *hlsAttempt,
	task *segmentTask,
	err error,
	attemptStart time.Time,
) attemptOutcome {
	// 1. Accepted first — the precise settlement authority.
	if at.accepted || at.duplicate {
		e.queue.finish(task)
		e.maybeComplete()
		if at.accepted && err != nil {
			return attemptOutcome{action: outcomeFatal, err: err}
		}
		if at.accepted && err == nil && e.onAccepted != nil {
			e.onAccepted(at.index, at.ingestRes, attemptStart)
		}
		return attemptOutcome{action: outcomeDone}
	}

	// 2. Fatal preservation — storage, disk-full and contract faults always
	// win over any cancel classification.
	var storeErr *storageError
	var contractErr *hlsContractError
	var refusalErr *hlsRedirectRefusalError
	switch {
	case errors.As(err, &storeErr),
		errors.As(err, &contractErr),
		errors.As(err, &refusalErr),
		errors.Is(err, types.ErrInsufficientDiskSpace),
		errors.Is(err, types.ErrPermanentHTTP),
		errors.Is(err, types.ErrHLSInvalidSegmentResponse),
		errors.Is(err, types.ErrHLSDRMNotSupported),
		errors.Is(err, types.ErrMaxRedirects),
		errors.Is(err, errRedirectUserinfo),
		errors.Is(err, errDirtySeed),
		errors.Is(err, errOffsetOverflow):
		return attemptOutcome{action: outcomeFatal, err: err}
	}
	if errors.Is(err, errWindowClosed) {
		if e.parent.Err() != nil {
			return attemptOutcome{action: outcomeExit}
		}
		return attemptOutcome{action: outcomeFatal, err: err}
	}

	// 3. External cancel — the engine/parent ctx identity owns the exit.
	if e.ctx.Err() != nil || e.parent.Err() != nil || e.fatalErr() != nil {
		return attemptOutcome{action: outcomeExit}
	}

	// 4. Explicit internal stop reasons — never read a bare child
	// context.Canceled as kill.
	switch at.stopReason {
	case hlsStopKill, hlsStopDrain:
		return attemptOutcome{action: outcomeRequeue}
	case hlsStopStall:
		task.retries++
		task.genericAttempt++
		if task.retries > hlsMaxSegmentRetries {
			return attemptOutcome{action: outcomeFatal, err: errAttemptStall}
		}
		return attemptOutcome{action: outcomeRetry, delay: hlsRetryDelay(task.retries)}
	}

	if err == nil {
		// An attempt that neither committed nor errored is an internal
		// contradiction — terminate rather than loop silently.
		return attemptOutcome{action: outcomeFatal, err: newHLSContractError(
			"segment %d attempt ended without result", at.index)}
	}

	// 5. Cause routing.
	if statusErr, ok := errors.AsType[*hlsStatusError](err); ok {
		return e.classifyStatus(task, statusErr)
	}
	if _, ok := errors.AsType[*segmentDataError](err); ok {
		// Corrupt data re-fetches the full ciphertext; not a connection
		// attempt, so genericAttempt stays put.
		return e.consumeRetry(task, false, err)
	}
	if isHlsTransportErr(err) {
		return e.consumeRetry(task, true, err)
	}
	// Unknown — bounded retries, then terminal with cause preserved.
	return e.consumeRetry(task, true, err)
}

// isHlsTransportErr reports whether err is a transient wire failure:
// timeouts, resets, EOF classes, connection refused, transport ctx
// deadlines and no-progress reads.
func isHlsTransportErr(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrNoProgress) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)
}

// consumeRetry spends one retry slot (and the genericAttempt telemetry
// counter for connection-class failures) or terminates the task when the
// budget is exhausted — the original cause is preserved either way.
func (e *segmentEngine) consumeRetry(task *segmentTask, generic bool, err error) attemptOutcome {
	task.retries++
	if generic {
		task.genericAttempt++
	}
	if task.retries > hlsMaxSegmentRetries {
		return attemptOutcome{action: outcomeFatal, err: err}
	}
	return attemptOutcome{action: outcomeRetry, delay: hlsRetryDelay(task.retries)}
}

// classifyStatus routes a non-2xx response by status. The generic attempt
// counter only moves on connection-class failures and RA-less 429/503 —
// never on 404, never on 5xx.
func (e *segmentEngine) classifyStatus(task *segmentTask, se *hlsStatusError) attemptOutcome {
	host := transport.MirrorHost(task.seg.URI)
	permanent := func() attemptOutcome {
		return attemptOutcome{action: outcomeFatal, err: fmt.Errorf(
			"hls %s (segment %d): status %d: %w",
			se.resource, se.index, se.status, types.ErrPermanentHTTP)}
	}
	switch {
	case se.status == http.StatusNotFound:
		// 404 gets two short retries inside the generic budget — the third
		// terminates as permanent.
		task.notFoundRetries++
		if task.notFoundRetries > hlsNotFoundMaxRetries {
			return permanent()
		}
		task.retries++
		if task.retries > hlsMaxSegmentRetries {
			return permanent()
		}
		return attemptOutcome{action: outcomeRetry, delay: hlsRetryDelay(task.notFoundRetries)}

	case se.status == http.StatusTooManyRequests || se.status == http.StatusServiceUnavailable:
		ra, explicit := hlsValidRetryAfter(se.retryAfter, e.now())
		e.hostLimiter.Penalize(host, ra, explicit, e.now())
		if !explicit {
			// Only an RA-less 429/503 feeds the generic attempt counter.
			task.genericAttempt++
		}
		task.retries++
		if task.retries > hlsMaxSegmentRetries {
			return attemptOutcome{action: outcomeFatal, err: se}
		}
		delay := hlsRetryDelay(task.retries)
		if explicit {
			delay = ra // RA is the earliest allowed time; host penalty may still extend it
		}
		return attemptOutcome{action: outcomeRetry, delay: delay}

	case se.status == http.StatusForbidden || se.status == http.StatusGone:
		if !e.admissionOpen.Load() {
			return permanent()
		}
		// Admission open: the URI may carry an expired signature — mark a
		// refresh-required candidate for a future consumer while still
		// retrying the original URI inside the ordinary budget.
		task.refreshRequired = true
		task.retries++
		if task.retries > hlsMaxSegmentRetries {
			return permanent()
		}
		return attemptOutcome{action: outcomeRetry, delay: hlsRetryDelay(task.retries)}

	case se.status == http.StatusRequestTimeout:
		return e.consumeRetry(task, true, se)

	case se.status >= 500:
		// 5xx retries on RA when valid, else the exponential ladder —
		// never the generic attempt counter.
		ra, explicit := hlsValidRetryAfter(se.retryAfter, e.now())
		task.retries++
		if task.retries > hlsMaxSegmentRetries {
			return attemptOutcome{action: outcomeFatal, err: se}
		}
		delay := hlsRetryDelay(task.retries)
		if explicit {
			delay = ra
		}
		return attemptOutcome{action: outcomeRetry, delay: delay}

	case se.status >= 400:
		// 401 and every unlisted 4xx is permanent — zero retries.
		return permanent()
	}
	// Sub-400 statuses never reach here from execMediaAttempt/fetchHLSKey —
	// an unexpected class is a contract-level invalid response.
	return attemptOutcome{action: outcomeFatal, err: fmt.Errorf(
		"hls %s (segment %d): unexpected status %d: %w",
		se.resource, se.index, se.status, types.ErrHLSInvalidSegmentResponse)}
}

// waitRetry sleeps once until max(segment retry deadline, host penalty
// deadline) — never two sequential sleeps. The wait is session-visible
// (protected) and wakes on drain/stop/ctx.
func (e *segmentEngine) waitRetry(sess *hlsWorkerSession, task *segmentTask, delay time.Duration) error {
	deadline := e.now().Add(delay)
	host := transport.MirrorHost(task.seg.URI)
	if until := e.hostLimiter.BlockedUntil(host, e.now()); until.After(deadline) {
		deadline = until
	}
	sess.protWait.Store(true)
	sess.markActivity(e.now())
	err := e.sleep(sess.ctx, deadline.Sub(e.now()))
	sess.protWait.Store(false)
	sess.markActivity(e.now())
	return err
}

// waitHostPenalty blocks the attempt on an active host penalty before the
// request — protected waiting semantics, interruptible by kill/drain/ctx,
// never stall-able.
func (e *segmentEngine) waitHostPenalty(sess *hlsWorkerSession, at *hlsAttempt, uri string) error {
	host := transport.MirrorHost(uri)
	until := e.hostLimiter.BlockedUntil(host, e.now())
	if until.IsZero() {
		return nil
	}
	d := until.Sub(e.now())
	if d <= 0 {
		return nil
	}
	at.noteActivity()
	at.waitingOnLimiter.Store(true)
	sess.setPhase(hlsPhaseWait)
	err := e.sleep(at.segCtx, d)
	at.noteActivity()
	at.waitingOnLimiter.Store(false)
	sess.setPhase(hlsPhaseNetwork)
	return err
}
