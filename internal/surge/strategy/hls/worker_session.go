package hls

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/types"
)

// hlsAttemptPhase is the interruptibility projection for control primitives.
// Only the owner worker and the publish lock mutate it.
type hlsAttemptPhase int32

const (
	hlsPhaseNone    hlsAttemptPhase = iota // no attempt published
	hlsPhaseWait                           // protected intentional wait (limiter/host/backoff)
	hlsPhaseNetwork                        // TTFB/body/key on-wire phase — kill and stall interruptible
	hlsPhaseDecrypt                        // local in-place decrypt — kill still wins the pre-commit check
	hlsPhaseCommit                         // window lock + Ingest/drain/spill I/O — uninterruptible
)

// interruptibleByKill reports whether the phase still accepts a kill/drain
// cancel: everything before the commit gate. Stall is stricter — network only.
func (p hlsAttemptPhase) interruptibleByKill() bool {
	switch p {
	case hlsPhaseWait, hlsPhaseNetwork, hlsPhaseDecrypt:
		return true
	}
	return false
}

// hlsStopReason records why an attempt's segCtx was cancelled internally.
// External parent cancellation is read from ctx, never stored here.
type hlsStopReason int32

const (
	hlsStopNone  hlsStopReason = iota
	hlsStopKill                // free requeue, worker stays alive
	hlsStopDrain               // free requeue, worker exits after settle
	hlsStopStall               // stall cancel — consumes retry budget, not a kill
)

// hlsAttempt is one published in-flight segment attempt. Unique gen identity,
// the segCtx/cancel pair, telemetry atomics, and the ledger are all owned by
// the worker goroutine; phase/stopReason transitions serialize under the
// session publish lock so a kill can never tear a commit.
type hlsAttempt struct {
	gen    uint64
	index  int
	task   *segmentTask
	sess   *hlsWorkerSession
	segCtx context.Context
	cancel context.CancelFunc

	lastActivity     atomic.Int64
	waitingOnLimiter atomic.Bool // intentional ByteLimiter block
	retryCount       atomic.Int32
	lastHTTPStatus   atomic.Int32

	phase      hlsAttemptPhase // under sess.pubMu
	stopReason hlsStopReason   // under sess.pubMu
	accepted   bool            // credit kept — owner writes, owner reads
	duplicate  bool            // admitted elsewhere — settle without retry
	commitDone bool            // entered the commit gate
	ingestRes  IngestResult    // last Ingest outcome for the acceptance seam

	credited int64 // published to both counters, not yet settled
}

// hlsWorkerSession is the per-worker connection-granularity substrate: it
// persists EMA/window/session byte telemetry across segment switches and
// carries the persistent drain marker plus the currently published attempt.
type hlsWorkerSession struct {
	id        int
	engine    *segmentEngine
	startUnix int64

	ctx    context.Context // session lifetime — cancelled on drain/engine stop
	cancel context.CancelFunc

	draining  atomic.Bool // persistent marker, survives attempt switches
	queueWait atomic.Bool // blocked in queue.pop — idle projection
	protWait  atomic.Bool // inter-attempt protected wait (backoff/host penalty)

	sessionBytes atomic.Int64 // media transport bytes, monotonic
	windowBytes  atomic.Int64
	lastSeen     atomic.Int64 // LastActivity floor for attempt=nil gaps

	speedMu     sync.Mutex // guards speed + windowStart (windowBytes is atomic)
	speed       float64    // EMA bytes/sec
	windowStart time.Time  // rolling window start — never reset on segment switch

	pubMu   sync.Mutex
	attempt *hlsAttempt
	// retained telemetry for the attempt=nil projection
	retained struct {
		retry  int32
		status int32
	}
}

// markActivity stamps both the attempt (when set) and the session floor.
func (s *hlsWorkerSession) markActivity(now time.Time) {
	s.lastSeen.Store(now.UnixNano())
	s.pubMu.Lock()
	if at := s.attempt; at != nil {
		at.lastActivity.Store(now.UnixNano())
	}
	s.pubMu.Unlock()
}

func (s *hlsWorkerSession) setPhase(p hlsAttemptPhase) {
	s.pubMu.Lock()
	if at := s.attempt; at != nil {
		at.phase = p
	}
	s.pubMu.Unlock()
}

// publishAttempt installs a fresh attempt under the publish lock. A drain
// marker already recorded wins the registration seam: the new attempt is
// published with stopReason=drain and an already-fired cancel so the worker
// aborts it without reaching the network.
func (s *hlsWorkerSession) publishAttempt(at *hlsAttempt) {
	s.pubMu.Lock()
	at.phase = hlsPhaseNetwork
	if s.draining.Load() {
		at.stopReason = hlsStopDrain
		at.cancel()
	}
	s.attempt = at
	s.pubMu.Unlock()
}

// retireAttempt detaches the handle, spends the cancel (it can never fire
// into the next segment), and copies terminal telemetry into the retained
// projection.
func (s *hlsWorkerSession) retireAttempt(at *hlsAttempt, now time.Time) {
	s.pubMu.Lock()
	if s.attempt == at {
		s.attempt = nil
	}
	s.retained.retry = at.retryCount.Load()
	s.retained.status = at.lastHTTPStatus.Load()
	s.pubMu.Unlock()
	at.cancel()
	s.lastSeen.Store(now.UnixNano())
}

// cancelAttemptKill cancels the current attempt when it is still
// interruptible; free-requeue classification happens worker-side via
// stopReason. No attempt, a mid-commit attempt, or a spent cancel → false.
func (s *hlsWorkerSession) cancelAttemptKill() bool {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	at := s.attempt
	if at == nil || at.cancel == nil || !at.phase.interruptibleByKill() {
		return false
	}
	at.stopReason = hlsStopKill
	at.cancel()
	return true
}

// markDraining records the persistent drain marker before touching the
// attempt, so the Pop→publish seam and idle waits both inherit it. The
// session ctx cancel wakes a blocked pop and interrupts any interruptible
// attempt; a committing attempt is left to finish — its synchronous Ingest
// cannot be aborted anyway, and the worker exits right after settle.
func (s *hlsWorkerSession) markDraining() {
	s.draining.Store(true)
	s.cancel()
	s.pubMu.Lock()
	if at := s.attempt; at != nil && at.phase != hlsPhaseCommit {
		if at.stopReason == hlsStopNone {
			at.stopReason = hlsStopDrain
		}
		if at.cancel != nil {
			at.cancel()
		}
	}
	s.pubMu.Unlock()
}

// cancelAttemptStall cancels only when the expected attempt generation is
// still published AND it sits in the interruptible network phase — a
// limiter/host/backoff wait, local decrypt, or a commit in flight returns
// false. Stall is not a kill: it consumes retry budget worker-side.
func (s *hlsWorkerSession) cancelAttemptStall(expected uint64) bool {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	at := s.attempt
	if at == nil || at.gen != expected || at.cancel == nil {
		return false
	}
	if at.phase != hlsPhaseNetwork {
		return false
	}
	at.stopReason = hlsStopStall
	at.cancel()
	return true
}

// enterCommit is the commit gate: under the publish lock the worker claims
// the uninterruptible phase or loses to a kill/drain already recorded. The
// winner order is linearized — a kill recorded first forbids Ingest; the
// gate claimed first makes a later kill return false.
func (s *hlsWorkerSession) enterCommit(at *hlsAttempt) bool {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.attempt != at || at.stopReason != hlsStopNone {
		return false
	}
	at.phase = hlsPhaseCommit
	return true
}

// settleSpeedLocked rolls the 2s window into the EMA — the concurrent-shape
// update: alpha<=0 or a cold speed uses the window speed directly.
// Never called during commit; the window start is never reset on segment
// switches, so sub-2s segments still accumulate into a nonzero rate.
func (s *hlsWorkerSession) settleSpeedLocked(now time.Time, alpha float64) {
	elapsed := now.Sub(s.windowStart).Seconds()
	if elapsed < 2.0 {
		return
	}
	recent := float64(s.windowBytes.Swap(0)) / elapsed
	if alpha <= 0 || s.speed == 0 {
		s.speed = recent
	} else {
		s.speed = (1-alpha)*s.speed + alpha*recent
	}
	s.windowStart = now
}

// excludeLocked pushes windowStart forward by d so local non-network time
// (commit/disk) does not dilute the throughput measurement — the #719 shape.
func (s *hlsWorkerSession) excludeLocked(d time.Duration) {
	if d > 0 {
		s.windowStart = s.windowStart.Add(d)
	}
}

// snapshot builds one WorkerSnapshot without waiting on network or window
// locks. Chunk* stay zero (HLS has no byte-range chunks); the waiting
// projection folds limiter/backoff waits and protected local phases (queue
// wait, decrypt, commit) into WaitingOnLimiter — the only bit the existing
// CDN warmup detector understands — while normal TTFB/body/key reads stay
// false so a real network stall never hides behind the wait bit.
func (s *hlsWorkerSession) snapshot(now time.Time, alpha float64) types.WorkerSnapshot {
	s.pubMu.Lock()
	at := s.attempt
	var phase hlsAttemptPhase
	retry, status := s.retained.retry, s.retained.status
	var lastAct int64
	var internalWait bool
	if at != nil {
		phase = at.phase
		lastAct = at.lastActivity.Load()
		retry = at.retryCount.Load()
		status = at.lastHTTPStatus.Load()
		internalWait = at.waitingOnLimiter.Load()
	}
	if floor := s.lastSeen.Load(); floor > lastAct {
		lastAct = floor
	}
	s.pubMu.Unlock()

	idleWait := s.queueWait.Load()
	protected := internalWait || idleWait || s.protWait.Load() ||
		phase == hlsPhaseWait || phase == hlsPhaseCommit || phase == hlsPhaseDecrypt

	s.speedMu.Lock()
	if phase != hlsPhaseCommit {
		// Roll the window into EMA on the same lock the reader holds; during
		// commit the snapshot keeps the existing EMA untouched.
		s.settleSpeedLocked(now, alpha)
	}
	speed := s.speed
	s.speedMu.Unlock()

	if protected && lastAct != 0 {
		// Long intentional waits keep the session looking alive.
		lastAct = now.UnixNano()
	}
	if lastAct != 0 {
		if since := now.Sub(time.Unix(0, lastAct)); since > 2*time.Second {
			speed *= (2 * time.Second).Seconds() / since.Seconds()
		}
	}

	return types.WorkerSnapshot{
		WorkerID:         s.id,
		EMASpeed:         speed,
		LastActivityUnix: lastAct,
		WorkerStartUnix:  s.startUnix,
		SessionBytes:     s.sessionBytes.Load(),
		RetryCount:       retry,
		HTTPStatus:       status,
		WaitingOnLimiter: protected,
	}
}

// hlsSessionView is the internal precise read model (phases, attempt gen)
// that the stall primitive and tests consume — richer than the fixed
// WorkerSnapshot shape, which cannot carry an attempt identity.
type hlsSessionView struct {
	ID           int
	AttemptGen   uint64 // 0 when idle
	Phase        hlsAttemptPhase
	Index        int // current segment index; -1 when idle
	LastActivity int64
	Draining     bool
}

func (s *hlsWorkerSession) view() hlsSessionView {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	v := hlsSessionView{
		ID:           s.id,
		Index:        -1,
		LastActivity: s.lastSeen.Load(),
		Draining:     s.draining.Load(),
	}
	if at := s.attempt; at != nil {
		v.AttemptGen = at.gen
		v.Phase = at.phase
		v.Index = at.index
		if la := at.lastActivity.Load(); la > v.LastActivity {
			v.LastActivity = la
		}
	}
	return v
}

// hooks adapts the attempt to the hlsAttemptHooks seam: activity stamps, the
// HTTP status projection, and the limiter gate wrapped in the
// waiting/protected-phase protocol (activity refresh before and after,
// paired flag cleanup).
func (a *hlsAttempt) noteActivity() {
	a.lastActivity.Store(a.sess.engine.now().UnixNano())
}

func (a *hlsAttempt) noteStatus(status int) {
	a.lastHTTPStatus.Store(int32(status))
}

func (a *hlsAttempt) throttle(ctx context.Context, n int64) error {
	limiter := a.sess.engine.limiter
	if limiter == nil {
		return nil
	}
	a.noteActivity()
	a.waitingOnLimiter.Store(true)
	a.sess.setPhase(hlsPhaseWait)
	err := limiter.WaitN(ctx, n)
	a.noteActivity()
	a.waitingOnLimiter.Store(false)
	a.sess.setPhase(hlsPhaseNetwork)
	return err
}
