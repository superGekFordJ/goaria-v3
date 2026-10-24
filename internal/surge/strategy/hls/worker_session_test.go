package hls

import (
	"context"
	"errors"
	"testing"
	"time"
)

// testSession builds a session over a bare engine (now/limiter only) so
// session mechanics can be exercised without a full transport engine.
func testSession(t *testing.T, id int) *hlsWorkerSession {
	t.Helper()
	e := &segmentEngine{now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	now := time.Now()
	return &hlsWorkerSession{
		id:          id,
		engine:      e,
		startUnix:   now.UnixNano(),
		ctx:         ctx,
		cancel:      cancel,
		windowStart: now,
	}
}

func publishTestAttempt(s *hlsWorkerSession, gen uint64, index int) *hlsAttempt {
	at := &hlsAttempt{
		gen:   gen,
		index: index,
		task:  &segmentTask{seg: segRow(index)},
		sess:  s,
		phase: hlsPhaseNetwork,
	}
	at.segCtx, at.cancel = context.WithCancel(s.ctx)
	s.publishAttempt(at)
	return at
}

func TestHLSWorkerSessionSnapshotIdleProjection(t *testing.T) {
	s := testSession(t, 3)
	s.sessionBytes.Store(1234)
	now := time.Now()

	snap := s.snapshot(now, 0.1)
	if snap.WorkerID != 3 || snap.SessionBytes != 1234 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.WorkerStartUnix != s.startUnix {
		t.Fatalf("WorkerStartUnix = %d, want %d", snap.WorkerStartUnix, s.startUnix)
	}
	if snap.ChunkStart != 0 || snap.ChunkOffset != 0 || snap.ChunkLength != 0 {
		t.Fatalf("chunk fields must stay zero: %+v", snap)
	}
	if snap.Hedged {
		t.Fatal("Hedged must stay false")
	}
	if snap.WaitingOnLimiter {
		t.Fatal("idle without queue wait must not project waiting")
	}

	// Blocked in queue.pop is an intentional wait.
	s.queueWait.Store(true)
	snap = s.snapshot(now, 0.1)
	if !snap.WaitingOnLimiter {
		t.Fatal("queue wait must project WaitingOnLimiter")
	}
}

func TestHLSWorkerSessionSnapshotAttemptPhases(t *testing.T) {
	s := testSession(t, 0)
	now := time.Now()
	at := publishTestAttempt(s, 7, 4)
	at.retryCount.Store(2)
	at.lastHTTPStatus.Store(503)
	at.lastActivity.Store(now.UnixNano())

	for _, tc := range []struct {
		phase   hlsAttemptPhase
		waiting bool
	}{
		{hlsPhaseNetwork, false},
		{hlsPhaseWait, true},
		{hlsPhaseDecrypt, true},
		{hlsPhaseCommit, true},
	} {
		s.setPhase(tc.phase)
		snap := s.snapshot(now, 0.1)
		if snap.WaitingOnLimiter != tc.waiting {
			t.Fatalf("phase %v: waiting = %v, want %v", tc.phase, snap.WaitingOnLimiter, tc.waiting)
		}
		if snap.RetryCount != 2 || snap.HTTPStatus != 503 {
			t.Fatalf("phase %v: telemetry = %d/%d", tc.phase, snap.RetryCount, snap.HTTPStatus)
		}
	}
}

func TestHLSWorkerSessionKillStates(t *testing.T) {
	s := testSession(t, 0)
	if s.cancelAttemptKill() {
		t.Fatal("kill with no attempt returned true")
	}
	at := publishTestAttempt(s, 1, 0)
	if !s.cancelAttemptKill() {
		t.Fatal("kill during network phase refused")
	}
	if at.stopReason != hlsStopKill {
		t.Fatalf("stopReason = %v, want kill", at.stopReason)
	}
	if at.segCtx.Err() == nil {
		t.Fatal("kill did not cancel segCtx")
	}
	s.retireAttempt(at, time.Now())

	// A new generation publishes cleanly — the spent cancel is gone.
	at2 := publishTestAttempt(s, 2, 1)
	if at2.stopReason != hlsStopNone {
		t.Fatalf("new attempt inherited stopReason %v", at2.stopReason)
	}
	s.setPhase(hlsPhaseCommit)
	if s.cancelAttemptKill() {
		t.Fatal("kill during commit returned true")
	}
	if at2.segCtx.Err() != nil {
		t.Fatal("committing attempt was cancelled")
	}
}

func TestHLSWorkerSessionDrainMarkerAndIdleWake(t *testing.T) {
	s := testSession(t, 0)
	// Idle session: markDraining must wake a blocked pop via session ctx.
	s.markDraining()
	if !s.draining.Load() {
		t.Fatal("drain marker not recorded")
	}
	if s.ctx.Err() == nil {
		t.Fatal("drain did not cancel session ctx")
	}
	// A later-published attempt inherits the drain immediately.
	at := publishTestAttempt(s, 1, 0)
	if at.stopReason != hlsStopDrain || at.segCtx.Err() == nil {
		t.Fatalf("post-drain attempt: reason=%v ctx=%v", at.stopReason, at.segCtx.Err())
	}
}

func TestHLSWorkerSessionDrainSkipsCommit(t *testing.T) {
	s := testSession(t, 0)
	at := publishTestAttempt(s, 1, 0)
	s.setPhase(hlsPhaseCommit)
	s.markDraining()
	// The marker records intent; the committing attempt keeps no stopReason —
	// its synchronous Ingest finishes and the worker exits after settle.
	if at.stopReason != hlsStopNone {
		t.Fatalf("commit attempt got stopReason %v during drain", at.stopReason)
	}
	if !s.draining.Load() {
		t.Fatal("drain marker missing")
	}
}

func TestHLSWorkerSessionStallIdentity(t *testing.T) {
	s := testSession(t, 0)
	at := publishTestAttempt(s, 42, 3)
	if s.cancelAttemptStall(41) {
		t.Fatal("stall with wrong generation accepted")
	}
	if s.cancelAttemptStall(0) {
		t.Fatal("stall with zero generation accepted")
	}
	if !s.cancelAttemptStall(42) {
		t.Fatal("stall on live network attempt refused")
	}
	if at.stopReason != hlsStopStall {
		t.Fatalf("stopReason = %v, want stall", at.stopReason)
	}

	// A wait-protected phase is not stall-interruptible.
	s.retireAttempt(at, time.Now())
	publishTestAttempt(s, 43, 3)
	s.setPhase(hlsPhaseWait)
	if s.cancelAttemptStall(43) {
		t.Fatal("stall during protected wait accepted")
	}
	s.setPhase(hlsPhaseDecrypt)
	if s.cancelAttemptStall(43) {
		t.Fatal("stall during decrypt accepted")
	}
	s.setPhase(hlsPhaseCommit)
	if s.cancelAttemptStall(43) {
		t.Fatal("stall during commit accepted")
	}
}

func TestHLSWorkerSessionThrottlePairing(t *testing.T) {
	s := testSession(t, 0)
	at := publishTestAttempt(s, 1, 0)
	release := make(chan struct{})
	s.engine.limiter = limiterFunc(func(ctx context.Context, n int64) error {
		close(release)
		<-ctx.Done()
		return ctx.Err()
	})
	errCh := make(chan error, 1)
	go func() { errCh <- at.throttle(at.segCtx, 16) }()
	<-release
	if !at.waitingOnLimiter.Load() {
		t.Fatal("waiting flag not set inside WaitN")
	}
	s.pubMu.Lock()
	ph := at.phase
	s.pubMu.Unlock()
	if ph != hlsPhaseWait {
		t.Fatalf("phase = %v during WaitN, want wait", ph)
	}
	at.cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("throttle err = %v, want ctx cancel", err)
	}
	if at.waitingOnLimiter.Load() {
		t.Fatal("waiting flag not cleared after WaitN")
	}
}

type limiterFunc func(ctx context.Context, n int64) error

func (f limiterFunc) WaitN(ctx context.Context, n int64) error { return f(ctx, n) }

func TestHLSWorkerSessionEMAWindowSurvivesSegments(t *testing.T) {
	s := testSession(t, 0)
	base := time.Now()
	s.windowStart = base
	// Two sub-2s segments push media bytes — the window must not reset
	// between segment switches.
	s.windowBytes.Add(4096)
	s.windowBytes.Add(4096)

	// Roll manually past the 2s boundary.
	s.speedMu.Lock()
	s.settleSpeedLocked(base.Add(2500*time.Millisecond), 0.5)
	got := s.speed
	s.speedMu.Unlock()
	want := float64(8192) / 2.5
	if got != want {
		t.Fatalf("EMA = %v, want %v", got, want)
	}
	if s.windowBytes.Load() != 0 {
		t.Fatalf("window bytes not reset: %d", s.windowBytes.Load())
	}
	if !s.windowStart.Equal(base.Add(2500 * time.Millisecond)) {
		t.Fatalf("windowStart = %v", s.windowStart)
	}

	// During commit the window must not roll — the snapshot keeps EMA frozen.
	s.windowStart = time.Now().Add(-3 * time.Second)
	s.windowBytes.Add(100000)
	at := publishTestAttempt(s, 1, 0)
	s.setPhase(hlsPhaseCommit)
	before := s.speed
	snap := s.snapshot(time.Now(), 0.5)
	if snap.EMASpeed != before {
		t.Fatalf("snapshot rolled window during commit: %v -> %v", before, snap.EMASpeed)
	}
	_ = at
}

func TestHLSWorkerSessionView(t *testing.T) {
	s := testSession(t, 5)
	v := s.view()
	if v.AttemptGen != 0 || v.Index != -1 {
		t.Fatalf("idle view = %+v", v)
	}
	at := publishTestAttempt(s, 9, 12)
	v = s.view()
	if v.AttemptGen != 9 || v.Index != 12 || v.Phase != hlsPhaseNetwork {
		t.Fatalf("view = %+v", v)
	}
	at.lastActivity.Store(12345)
	if v2 := s.view(); v2.LastActivity < 12345 {
		t.Fatalf("view LastActivity = %d", v2.LastActivity)
	}
}

func TestHLSWorkerSessionRetireKeepsTelemetry(t *testing.T) {
	s := testSession(t, 0)
	at := publishTestAttempt(s, 1, 0)
	at.retryCount.Store(4)
	at.lastHTTPStatus.Store(404)
	s.retireAttempt(at, time.Now())
	snap := s.snapshot(time.Now(), 0.1)
	if snap.RetryCount != 4 || snap.HTTPStatus != 404 {
		t.Fatalf("retained telemetry = %d/%d", snap.RetryCount, snap.HTTPStatus)
	}
	// Session bytes never shrink on rollback.
	s.sessionBytes.Store(777)
	if got := s.snapshot(time.Now(), 0.1).SessionBytes; got != 777 {
		t.Fatalf("SessionBytes = %d", got)
	}
}

func TestHLSWorkerSessionDecaysWhenStale(t *testing.T) {
	s := testSession(t, 0)
	s.speed = 1000
	stale := time.Now().Add(-10 * time.Second)
	s.lastSeen.Store(stale.UnixNano())
	snap := s.snapshot(time.Now(), 0.1)
	if snap.EMASpeed >= 1000 {
		t.Fatalf("EMA = %v, want decayed below 1000", snap.EMASpeed)
	}
	// ~2s/10s decay factor.
	if snap.EMASpeed < 100 || snap.EMASpeed > 300 {
		t.Fatalf("EMA = %v, out of decay band", snap.EMASpeed)
	}
}

func TestHLSWorkerSessionMarkActivityFloor(t *testing.T) {
	s := testSession(t, 0)
	at := publishTestAttempt(s, 1, 0)
	now := time.Now()
	s.markActivity(now)
	if at.lastActivity.Load() != now.UnixNano() {
		t.Fatal("markActivity did not stamp attempt")
	}
	if s.lastSeen.Load() != now.UnixNano() {
		t.Fatal("markActivity did not stamp session floor")
	}
}
