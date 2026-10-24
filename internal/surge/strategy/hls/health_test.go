package hls

import (
	"math"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// healthDownloader returns a downloader whose runCfg drives the health
// checks under test; publishEngine must be called before bridge use.
func healthDownloader(tweak func(*types.RuntimeConfig)) *HlsDownloader {
	rc := types.RuntimeConfig{StallTimeout: 50 * time.Millisecond}
	if tweak != nil {
		tweak(&rc)
	}
	d := NewHlsDownloader("health", nil, nil, nil)
	d.runCfg = rc
	return d
}

// blockedAttemptExec parks the attempt in the given phase with a stale
// activity stamp until its segCtx is canceled; the release channel offers
// a normal-exit path so the engine can still drain.
func blockedAttemptExec(phase hlsAttemptPhase, stale time.Time, release chan struct{}) func(*hlsWorkerSession, *hlsAttempt) error {
	return func(sess *hlsWorkerSession, at *hlsAttempt) error {
		sess.setPhase(phase)
		at.lastActivity.Store(stale.UnixNano())
		sess.lastSeen.Store(stale.UnixNano())
		select {
		case <-at.segCtx.Done():
			return at.segCtx.Err()
		case <-release:
			at.accepted = true
			return nil
		}
	}
}

func TestHLSHealth_StallCancelsStaleNetworkAttempt(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	stale := time.Now().Add(-time.Hour)
	e := testEngine(t, 2, blockedAttemptExec(hlsPhaseNetwork, stale, release), nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	d := healthDownloader(nil)
	prog := progress.New("health-stall", 0)

	deadline := time.Now().Add(2 * time.Second)
	var view hlsSessionView
	for {
		views := e.sessionViews()
		if len(views) > 0 && views[0].AttemptGen != 0 && views[0].Phase == hlsPhaseNetwork {
			view = views[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attempt never reached the network phase")
		}
		time.Sleep(time.Millisecond)
	}

	// The attempt is stale AND in the interruptible network phase — the
	// watchdog must cancel exactly this generation.
	d.healthTick(e, prog, time.Now())
	deadline = time.Now().Add(2 * time.Second)
	for {
		v := e.sessionByID(view.ID)
		if v == nil {
			break
		}
		v.pubMu.Lock()
		at := v.attempt
		var dead bool
		if at != nil && at.gen == view.AttemptGen {
			dead = at.segCtx.Err() != nil
		} else {
			dead = true // attempt retired or replaced — the cancel won either way
		}
		v.pubMu.Unlock()
		if dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale network attempt never canceled")
		}
		time.Sleep(time.Millisecond)
	}
	e.stop()
	_ = waitErrBounded(t, e, 3*time.Second)
}

func TestHLSHealth_ProtectedWaitNotTreatedAsStall(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	stale := time.Now().Add(-time.Hour)
	// hlsPhaseWait marks intentional limiter/host/backoff waits — stale
	// activity there is never a network stall.
	e := testEngine(t, 1, blockedAttemptExec(hlsPhaseWait, stale, release), nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	d := healthDownloader(nil)
	prog := progress.New("health-wait", 0)

	deadline := time.Now().Add(2 * time.Second)
	for {
		views := e.sessionViews()
		if len(views) > 0 && views[0].Phase == hlsPhaseWait {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attempt never reached the wait phase")
		}
		time.Sleep(time.Millisecond)
	}
	d.healthTick(e, prog, time.Now())
	// A few ticks later the attempt must still be alive — the watchdog has
	// no authority over protected waits.
	for range 3 {
		d.healthTick(e, prog, time.Now())
	}
	v := e.sessionViews()
	if len(v) == 0 || v[0].AttemptGen == 0 {
		t.Fatal("protected wait attempt was canceled by the watchdog")
	}
	sess := e.sessionByID(v[0].ID)
	sess.pubMu.Lock()
	canceled := sess.attempt.segCtx.Err() != nil
	sess.pubMu.Unlock()
	if canceled {
		t.Fatal("watchdog canceled a protected wait")
	}
	e.stop()
	_ = waitErrBounded(t, e, 3*time.Second)
}

func TestHLSHealth_StatsHaveZeroChunkFields(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	e := testEngine(t, 1, blockedAttemptExec(hlsPhaseNetwork, time.Now(), release), nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	d := healthDownloader(nil)
	prog := progress.New("health-stats", 0)

	deadline := time.Now().Add(2 * time.Second)
	var stats []types.WorkerSnapshot
	for len(stats) == 0 {
		d.healthTick(e, prog, time.Now())
		stats = prog.GetWorkerStats()
		if time.Now().After(deadline) {
			t.Fatal("health tick never published stats")
		}
		time.Sleep(time.Millisecond)
	}
	for _, s := range stats {
		if s.ChunkStart != 0 || s.ChunkOffset != 0 || s.ChunkLength != 0 {
			t.Fatalf("HLS telemetry leaked byte-range chunk fields: %+v", s)
		}
		if s.WorkerID < 0 {
			t.Fatalf("worker id = %d", s.WorkerID)
		}
	}
	e.stop()
	_ = waitErrBounded(t, e, 3*time.Second)
}

func TestHLSHealthBridges_ScaleKillDrainAndThreshold(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	e := testEngine(t, 4, blockedAttemptExec(hlsPhaseNetwork, time.Now(), release), nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	d := healthDownloader(nil)
	d.publishEngine(e)

	// Admission still closed — runtime spawn refuses.
	if got := d.ScaleWorkers(1); got != 0 {
		t.Fatalf("ScaleWorkers(+1) with closed admission = %d", got)
	}
	e.setAdmissionOpen(true)
	if got := d.ScaleWorkers(2); got != 2 {
		t.Fatalf("ScaleWorkers(+2) = %d, want 2", got)
	}
	if got := len(e.liveSessionIDs()); got != 3 {
		t.Fatalf("live workers = %d, want 3", got)
	}
	// Drain keeps one alive; kill/drain answer by identity.
	ids := e.liveSessionIDs()
	if !d.DrainWorker(ids[0]) {
		t.Fatalf("DrainWorker(%d) refused", ids[0])
	}
	if d.DrainWorker(1 << 20) {
		t.Fatal("DrainWorker accepted an unknown id")
	}
	// After the drain, kill a non-draining worker — but only once its
	// attempt is published (a worker between segments has nothing to kill).
	var alive int
	deadline := time.Now().Add(2 * time.Second)
	for {
		draining := map[int]bool{}
		for _, v := range e.sessionViews() {
			draining[v.ID] = v.Draining
			if !v.Draining && v.AttemptGen != 0 {
				alive = v.ID
			}
		}
		if alive != 0 && !draining[alive] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no non-draining worker ever published an attempt")
		}
		time.Sleep(time.Millisecond)
	}
	if !d.KillWorker(alive) {
		t.Fatalf("KillWorker(%d) refused a live non-draining worker", alive)
	}
	d.SetSlowWorkerThreshold(0.5)
	if got := d.slowThresholdOrDefault(0.3); got != 0.5 {
		t.Fatalf("threshold override = %v", got)
	}
	d.SetSlowWorkerThreshold(math.NaN())
	d.SetSlowWorkerThreshold(2)
	if got := d.slowThresholdOrDefault(0.3); got != 0.5 {
		t.Fatalf("invalid threshold clobbered the override: %v", got)
	}
	e.stop()
	_ = waitErrBounded(t, e, 3*time.Second)
}

func TestHLSHealthBridges_ScaleDownKeepsOneAlive(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	e := testEngine(t, 4, blockedAttemptExec(hlsPhaseNetwork, time.Now(), release), nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	d := healthDownloader(nil)
	d.publishEngine(e)
	e.setAdmissionOpen(true)
	_ = d.ScaleWorkers(2)
	// Scaling down by more than the pool never empties it — one worker must
	// stay alive to keep the claim moving.
	if got := d.ScaleWorkers(-10); got == 0 || got > 0 {
		t.Fatalf("ScaleWorkers(-10) = %d", got)
	}
	nonDraining := 0
	for _, v := range e.sessionViews() {
		if !v.Draining {
			nonDraining++
		}
	}
	if nonDraining != 1 {
		t.Fatalf("non-draining workers = %d, want exactly 1", nonDraining)
	}
	e.stop()
	_ = waitErrBounded(t, e, 3*time.Second)
}

func TestHLSHealthBridges_PostDetachAreInert(t *testing.T) {
	d := healthDownloader(nil)
	// No engine published — every bridge is a safe no-op.
	if got := d.ScaleWorkers(1); got != 0 {
		t.Fatalf("ScaleWorkers = %d", got)
	}
	if got := d.ScaleWorkers(-1); got != 0 {
		t.Fatalf("ScaleWorkers(-1) = %d", got)
	}
	if d.KillWorker(0) || d.DrainWorker(0) {
		t.Fatal("kill/drain acted without an engine")
	}
	d.SetSlowWorkerThreshold(0.25)
	if got := d.slowThresholdOrDefault(0.3); got != 0.25 {
		t.Fatalf("threshold = %v", got)
	}
}

func TestHLSHealth_StallChecksOnlyExpectedGeneration(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	stale := time.Now().Add(-time.Hour)
	e := testEngine(t, 2, blockedAttemptExec(hlsPhaseNetwork, stale, release), nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var view hlsSessionView
	for {
		views := e.sessionViews()
		if len(views) > 0 && views[0].AttemptGen != 0 {
			view = views[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("attempt never published")
		}
		time.Sleep(time.Millisecond)
	}
	// A stale generation must not cancel the live attempt — the gen gate
	// prevents a slow tick from killing the attempt's successor.
	if e.cancelAttemptStall(view.ID, view.AttemptGen+999) {
		t.Fatal("stall cancel accepted a wrong generation")
	}
	sess := e.sessionByID(view.ID)
	sess.pubMu.Lock()
	alive := sess.attempt != nil && sess.attempt.segCtx.Err() == nil
	sess.pubMu.Unlock()
	if !alive {
		t.Fatal("attempt died from a wrong-generation stall cancel")
	}
	if !e.cancelAttemptStall(view.ID, view.AttemptGen) {
		t.Fatal("stall cancel refused the live generation")
	}
	e.stop()
	_ = waitErrBounded(t, e, 3*time.Second)
}
