package hls

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
)

// errEngineIncomplete reports an engine that stopped (drained/stopped)
// before every segment was accepted — never a success.
var errEngineIncomplete = errors.New("hls: engine stopped with segments unaccepted")

// segmentEngineParams are the construction inputs; every caller-mutable
// map/slice is copied or cloned inside, never borrowed.
type segmentEngineParams struct {
	Seed        *types.HLSResumeState // cloned, frozen
	Window      *ReorderWindow        // borrowed, closed by the owner
	Progress    *progress.DownloadProgress
	Client      *http.Client // nil borrows the shared pool
	RunCfg      *types.RuntimeConfig
	TaskHeaders map[string]string // copied
	Limiter     types.ByteLimiter
	HostLimiter *transport.HostRateLimiter // nil uses transport.DefaultHostRateLimiter
	Parent      context.Context            // nil falls back to Background
	MaxWorkers  int                        // caller-side spawn cap

	// OnAccepted fires once per newly accepted segment after queue settle —
	// index, IngestResult, and the attempt start time (356's TTFB input).
	// It runs synchronously outside every registry/queue lock; nil is a
	// no-op, and it is never wired to events or spawn by this engine.
	OnAccepted func(index int, res IngestResult, attemptStart time.Time)

	// Test seams — instance-scoped, never package globals.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	// execAttempt is the per-attempt body (fetch/read/decrypt/commit).
	// Production wires execMediaAttempt; tests may substitute a fake.
	execAttempt func(sess *hlsWorkerSession, at *hlsAttempt) error
}

// segmentEngine aggregates the queue, key cache, borrowed window, progress,
// shared gated client and the live-session registry.
type segmentEngine struct {
	ctx    context.Context
	cancel context.CancelFunc
	parent context.Context

	seed        *types.HLSResumeState
	taskHeaders map[string]string
	runCfg      *types.RuntimeConfig
	credBase    *url.URL
	client      *http.Client
	release     func()

	queue  *segmentQueue
	keys   *hlsKeyCache
	window *ReorderWindow
	prog   *progress.DownloadProgress

	limiter     types.ByteLimiter
	hostLimiter *transport.HostRateLimiter
	maxWorkers  int
	onAccepted  func(index int, res IngestResult, attemptStart time.Time)

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	admissionOpen atomic.Bool
	attemptGen    atomic.Uint64

	lifeMu      sync.Mutex
	running     bool
	sealed      bool
	workers     int
	workersDone chan struct{}
	nextID      int64 // lifeMu only — monotonic worker IDs

	sessMu   sync.Mutex
	sessions map[int]*hlsWorkerSession

	fatalMu sync.Mutex
	fatal   error

	execAttempt func(sess *hlsWorkerSession, at *hlsAttempt) error
}

func newSegmentEngine(p segmentEngineParams) (*segmentEngine, error) {
	if p.Seed == nil || p.Seed.TotalSegments <= 0 {
		return nil, newHLSContractError("seed is not a prepared media table")
	}
	// The seed must satisfy the same prepared-prefix contract the window
	// enforces — segment count, ordered Index and an exact bitmap — or the
	// queue would lease phantom/duplicate work at construction.
	if err := validateSeed(p.Seed); err != nil {
		return nil, err
	}
	if p.Window == nil {
		return nil, newHLSContractError("nil reorder window")
	}
	if p.Progress == nil {
		return nil, newHLSContractError("nil progress")
	}
	credBase, err := parseHLSBinaryURL(p.Seed.ManifestURL)
	if err != nil {
		return nil, err
	}
	runCfg := types.DefaultRuntimeConfig()
	if p.RunCfg != nil {
		rc := *p.RunCfg
		runCfg = &rc
	}
	parent := p.Parent
	if parent == nil {
		parent = context.Background()
	}
	seed := p.Seed.Clone()
	headers := make(map[string]string, len(p.TaskHeaders))
	maps.Copy(headers, p.TaskHeaders)
	client, release := newHLSClient(p.Client, runCfg, credBase)
	hostLimiter := p.HostLimiter
	if hostLimiter == nil {
		hostLimiter = transport.DefaultHostRateLimiter
	}
	now := p.now
	if now == nil {
		now = time.Now
	}
	sleep := p.sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			if d <= 0 {
				return nil
			}
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	completed := func(i int) bool {
		if i < 0 || i >= seed.TotalSegments {
			return false
		}
		return seed.CompletedBitmap[i>>3]&(1<<uint(i&7)) != 0 || i < seed.MergedSegments
	}
	ctx, cancel := context.WithCancel(parent)
	e := &segmentEngine{
		ctx:         ctx,
		cancel:      cancel,
		parent:      parent,
		seed:        seed,
		taskHeaders: headers,
		runCfg:      runCfg,
		credBase:    credBase,
		client:      client,
		release:     release,
		keys:        newHLSKeyCache(),
		window:      p.Window,
		prog:        p.Progress,
		limiter:     p.Limiter,
		hostLimiter: hostLimiter,
		maxWorkers:  p.MaxWorkers,
		onAccepted:  p.OnAccepted,
		now:         now,
		sleep:       sleep,
		sessions:    make(map[int]*hlsWorkerSession),
		workersDone: make(chan struct{}),
		execAttempt: p.execAttempt,
	}
	e.queue = newSegmentQueue(seed.Segments, completed)
	if e.execAttempt == nil {
		e.execAttempt = e.execMediaAttempt
	}
	if e.maxWorkers <= 0 {
		e.maxWorkers = 1 << 20
	}
	return e, nil
}

func (e *segmentEngine) fatalErr() error {
	e.fatalMu.Lock()
	defer e.fatalMu.Unlock()
	return e.fatal
}

// latchFatal records the first fatal error, seals spawn, wakes the queue and
// cancels the engine ctx so every session and attempt unwinds — the first
// latch wins, later failures never overwrite it.
func (e *segmentEngine) latchFatal(err error) {
	if err == nil {
		return
	}
	e.fatalMu.Lock()
	if e.fatal == nil {
		e.fatal = err
	}
	e.fatalMu.Unlock()
	e.seal()
	e.queue.close()
	e.cancel()
}

func (e *segmentEngine) seal() {
	e.lifeMu.Lock()
	e.sealed = true
	e.lifeMu.Unlock()
}

// setAdmissionOpen flips the atomic spawn gate; initial start workers are
// exempt, and nothing in this engine opens it automatically.
func (e *segmentEngine) setAdmissionOpen(open bool) {
	e.admissionOpen.Store(open)
}

// start launches the initial worker pool; it must be positive and is the
// only path exempt from the admission gate.
func (e *segmentEngine) start(initialWorkers int) error {
	if initialWorkers <= 0 {
		return newHLSContractError("initialWorkers must be positive, got %d", initialWorkers)
	}
	e.lifeMu.Lock()
	if e.running {
		e.lifeMu.Unlock()
		return newHLSContractError("engine already started")
	}
	if e.sealed {
		e.lifeMu.Unlock()
		return newHLSContractError("engine sealed by wait/stop")
	}
	e.running = true
	e.lifeMu.Unlock()
	for i := 0; i < initialWorkers && i < e.maxWorkers; i++ {
		if !e.spawn(true) {
			break
		}
	}
	return nil
}

// spawnWorker is the runtime spawn gate — refused while admission is closed,
// stopped/sealed/cancelled/fatal, out of work, or at the caller cap.
func (e *segmentEngine) spawnWorker() bool {
	return e.spawn(false)
}

// lifecycleView is a consistent read-only snapshot of the worker-count
// epoch under lifeMu: the live worker count plus the channel that closes on
// the next drain-to-zero. Readers must re-view after every wake — a
// zero-to-one transition arms a fresh channel. This is the runtime
// monitoring path; wait() stays reserved for the terminal seal.
type engineLifecycleView struct {
	workers     int
	workersDone chan struct{}
	running     bool
	sealed      bool
}

func (e *segmentEngine) lifecycleView() engineLifecycleView {
	e.lifeMu.Lock()
	defer e.lifeMu.Unlock()
	return engineLifecycleView{
		workers:     e.workers,
		workersDone: e.workersDone,
		running:     e.running,
		sealed:      e.sealed,
	}
}

func (e *segmentEngine) spawn(initial bool) bool {
	if !initial && !e.admissionOpen.Load() {
		return false
	}
	e.lifeMu.Lock()
	if !e.running || e.sealed || e.workers >= e.maxWorkers ||
		e.ctx.Err() != nil || e.queue.remaining() == 0 {
		e.lifeMu.Unlock()
		return false
	}
	id := int(e.nextID)
	e.nextID++
	e.workers++
	if e.workers == 1 {
		// Every zero-to-one transition arms a fresh epoch channel — the
		// previous one already closed when the last epoch drained to zero.
		e.workersDone = make(chan struct{})
	}
	e.lifeMu.Unlock()

	sess := e.newSession(id)
	e.sessMu.Lock()
	e.sessions[id] = sess
	e.sessMu.Unlock()
	e.prog.ActiveWorkers.Add(1)
	go e.workerLoop(sess)
	return true
}

func (e *segmentEngine) newSession(id int) *hlsWorkerSession {
	ctx, cancel := context.WithCancel(e.ctx)
	now := e.now()
	return &hlsWorkerSession{
		id:          id,
		engine:      e,
		startUnix:   now.UnixNano(),
		ctx:         ctx,
		cancel:      cancel,
		windowStart: now,
	}
}

func (e *segmentEngine) releaseWorker(sess *hlsWorkerSession) {
	sess.cancel()
	e.sessMu.Lock()
	delete(e.sessions, sess.id)
	e.sessMu.Unlock()
	e.prog.ActiveWorkers.Add(-1)
	e.lifeMu.Lock()
	e.workers--
	if e.workers == 0 {
		close(e.workersDone)
	}
	e.lifeMu.Unlock()
}

// liveSessionIDs returns the sorted IDs of every live session — workers in
// queue wait, backoff, key fetch, limiter or commit all count.
func (e *segmentEngine) liveSessionIDs() []int {
	e.sessMu.Lock()
	defer e.sessMu.Unlock()
	ids := make([]int, 0, len(e.sessions))
	for id := range e.sessions {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func (e *segmentEngine) sessionByID(id int) *hlsWorkerSession {
	e.sessMu.Lock()
	defer e.sessMu.Unlock()
	return e.sessions[id]
}

// snapshotWorkers allocates a fresh ordered snapshot of every live session.
func (e *segmentEngine) snapshotWorkers(now time.Time) []types.WorkerSnapshot {
	ids := e.liveSessionIDs()
	out := make([]types.WorkerSnapshot, 0, len(ids))
	alpha := e.runCfg.GetSpeedEmaAlpha()
	for _, id := range ids {
		if s := e.sessionByID(id); s != nil {
			out = append(out, s.snapshot(now, alpha))
		}
	}
	return out
}

// sessionViews is the internal precise read model for stall detection.
func (e *segmentEngine) sessionViews() []hlsSessionView {
	ids := e.liveSessionIDs()
	out := make([]hlsSessionView, 0, len(ids))
	for _, id := range ids {
		if s := e.sessionByID(id); s != nil {
			out = append(out, s.view())
		}
	}
	return out
}

// cancelAttemptKill cancels only the worker's still-interruptible attempt:
// the unaccepted credit is rolled back and the whole segment requeued once,
// without consuming retry budget; the goroutine, session and ID persist.
// Between attempts (retry backoff, host-penalty waits) there is no attempt
// to kill — the lease simply retries on schedule, so false is correct; the
// drain primitive still wakes those protected waits immediately.
func (e *segmentEngine) cancelAttemptKill(id int) bool {
	s := e.sessionByID(id)
	if s == nil {
		return false
	}
	return s.cancelAttemptKill()
}

// markDraining records the persistent drain marker and interrupts whatever
// the worker is doing now — network, wait, or idle pop — but never a commit
// in flight; the worker exits once its settle completes.
func (e *segmentEngine) markDraining(id int) bool {
	s := e.sessionByID(id)
	if s == nil {
		return false
	}
	s.markDraining()
	return true
}

// cancelAttemptStall cancels a network-phase attempt identified by its
// generation; it consumes retry budget unlike kill, and refuses protected
// waits and commits outright.
func (e *segmentEngine) cancelAttemptStall(id int, expectedAttempt uint64) bool {
	s := e.sessionByID(id)
	if s == nil {
		return false
	}
	return s.cancelAttemptStall(expectedAttempt)
}

// maybeComplete closes the queue once nothing remains — the finishing
// worker seals dispatch so idle pops wake and every worker drains out.
// A temporarily empty ready list never triggers this: pending leases and
// backoff-held work still count.
func (e *segmentEngine) maybeComplete() {
	if e.queue.remaining() == 0 {
		e.queue.close()
	}
}

// stopped reports whether the engine context ended or a fatal latched.
func (e *segmentEngine) stopped() bool {
	return e.ctx.Err() != nil || e.fatalErr() != nil
}

// stop seals spawn, closes the queue and cancels every session — in-flight
// commits still settle, then workers exit. Unfinished work is reported by
// wait() as errEngineIncomplete.
func (e *segmentEngine) stop() {
	e.seal()
	e.queue.close()
	e.cancel()
}

// wait blocks until every worker exits, then releases the pooled transport
// lease last. It reports the first fatal (joined with external ctx identity
// when both fired), the parent ctx identity on external cancel, nil on a
// fully accepted queue, and errEngineIncomplete when the engine stopped
// with segments unaccepted — an empty ready list alone is never success.
func (e *segmentEngine) wait() error {
	// Terminal operation: no spawn may outlive the wait — a late respawn
	// would resurrect a worker on the already-returned transport lease.
	e.seal()
	// The done channel is per-epoch: a drain-to-zero closes the current one,
	// a respawn arms the next. Wait until workers hit zero and stay zero at
	// observation time — never block on an unarmed pre-start channel.
	for {
		e.lifeMu.Lock()
		workers := e.workers
		done := e.workersDone
		e.lifeMu.Unlock()
		if workers == 0 {
			break
		}
		<-done
	}
	e.lifeMu.Lock()
	release := e.release
	e.release = nil
	e.lifeMu.Unlock()
	if release != nil {
		release()
	}
	fatal := e.fatalErr()
	parentErr := e.parent.Err()
	if fatal != nil && parentErr != nil {
		return errors.Join(fatal, parentErr)
	}
	if fatal != nil {
		return fatal
	}
	if parentErr != nil {
		return parentErr
	}
	if e.queue.remaining() == 0 {
		return nil
	}
	return errEngineIncomplete
}

// workerLoop is the per-session loop: pop, run the segment's attempt cycle,
// repeat until drain/cancel/fatal/queue-close. The whole leased segment is
// requeued exactly once whenever the worker is drained mid-lease.
func (e *segmentEngine) workerLoop(sess *hlsWorkerSession) {
	defer e.releaseWorker(sess)
	for {
		if sess.draining.Load() || e.stopped() {
			return
		}
		sess.queueWait.Store(true)
		sess.markActivity(e.now())
		task, ok := e.queue.pop(sess.ctx)
		sess.queueWait.Store(false)
		sess.markActivity(e.now())
		if !ok {
			return
		}
		if sess.draining.Load() {
			e.queue.requeue(task)
			return
		}
		if e.stopped() {
			return
		}
		if err := e.runTask(sess, task); err != nil {
			e.latchFatal(err)
			return
		}
	}
}

// newAttempt builds the attempt substrate for one leased segment: a fresh
// generation, a per-attempt context derived from the session, and the
// retry/activity bookkeeping seeded from the task lease.
func (e *segmentEngine) newAttempt(sess *hlsWorkerSession, task *segmentTask) *hlsAttempt {
	at := &hlsAttempt{
		gen:   e.attemptGen.Add(1),
		index: task.seg.Index,
		task:  task,
		sess:  sess,
	}
	at.segCtx, at.cancel = context.WithCancel(sess.ctx)
	at.retryCount.Store(int32(task.genericAttempt))
	at.lastActivity.Store(e.now().UnixNano())
	return at
}

// runTask executes the attempt cycle for one leased segment: backoff waits
// between attempts, publish/run/retire, and the outcome decision. It
// returns nil when the segment settled or was requeued for another worker,
// and the fatal error when the engine must stop.
func (e *segmentEngine) runTask(sess *hlsWorkerSession, task *segmentTask) error {
	for {
		if sess.draining.Load() {
			e.queue.requeue(task)
			return nil
		}
		if e.stopped() {
			return nil
		}
		at := e.newAttempt(sess, task)
		sess.publishAttempt(at)
		attemptStart := e.now()
		err := e.execAttempt(sess, at)
		sess.retireAttempt(at, e.now())

		outcome := e.settleOutcome(sess, at, task, err, attemptStart)
		switch outcome.action {
		case outcomeDone:
			return nil
		case outcomeRetry:
			if werr := e.waitRetry(sess, task, outcome); werr != nil {
				// Wait interrupted: drain requeues free; cancel/fatal exits.
				if sess.draining.Load() {
					e.queue.requeue(task)
				}
				return nil
			}
			continue
		case outcomeRequeue:
			e.queue.requeue(task)
			return nil
		case outcomeFatal:
			return outcome.err
		case outcomeExit:
			return nil
		}
	}
}
