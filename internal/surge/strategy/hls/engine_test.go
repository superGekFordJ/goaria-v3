package hls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// testEngine builds an engine with a fake window (never touched by fake
// attempts), a real progress tracker, and an injectable execAttempt.
func testEngine(t *testing.T, nSegs int, exec func(sess *hlsWorkerSession, at *hlsAttempt) error, tweak func(*segmentEngineParams)) *segmentEngine {
	t.Helper()
	seed := &types.HLSResumeState{
		ManifestURL:     "http://media.example.com/media.m3u8",
		TotalSegments:   nSegs,
		CompletedBitmap: make([]byte, (nSegs+7)/8),
	}
	segs := make([]types.HLSSegmentState, nSegs)
	for i := range segs {
		segs[i] = types.HLSSegmentState{
			Index: i, Sequence: int64(i),
			URI: fmt.Sprintf("http://media.example.com/s%d.ts", i),
		}
	}
	seed.Segments = segs
	p := segmentEngineParams{
		Seed:        seed,
		Window:      &ReorderWindow{},
		Progress:    progress.New("test-engine", 0),
		Client:      &http.Client{},
		RunCfg:      types.DefaultRuntimeConfig(),
		Parent:      context.Background(),
		MaxWorkers:  8,
		execAttempt: exec,
	}
	if tweak != nil {
		tweak(&p)
	}
	e, err := newSegmentEngine(p)
	if err != nil {
		t.Fatalf("newSegmentEngine = %v", err)
	}
	return e
}

func TestHLSEngineConstructValidation(t *testing.T) {
	base := func() segmentEngineParams {
		seed := &types.HLSResumeState{
			ManifestURL:     "http://m.example.com/x.m3u8",
			TotalSegments:   1,
			CompletedBitmap: make([]byte, 1),
			Segments:        []types.HLSSegmentState{{Index: 0, URI: "http://m.example.com/s.ts"}},
		}
		return segmentEngineParams{
			Seed:     seed,
			Window:   &ReorderWindow{},
			Progress: progress.New("v", 0),
			Client:   &http.Client{},
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*segmentEngineParams)
	}{
		{"NilSeed", func(p *segmentEngineParams) { p.Seed = nil }},
		{"ZeroSegments", func(p *segmentEngineParams) { p.Seed.TotalSegments = 0 }},
		{"NilWindow", func(p *segmentEngineParams) { p.Window = nil }},
		{"NilProgress", func(p *segmentEngineParams) { p.Progress = nil }},
		{"RelativeManifest", func(p *segmentEngineParams) { p.Seed.ManifestURL = "rel/x.m3u8" }},
		{"FTPManifest", func(p *segmentEngineParams) { p.Seed.ManifestURL = "ftp://m.example.com/x" }},
		{"UserinfoManifest", func(p *segmentEngineParams) { p.Seed.ManifestURL = "http://u:p@m.example.com/x" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base()
			tc.mutate(&p)
			if _, err := newSegmentEngine(p); err == nil {
				t.Fatal("construction succeeded on invalid input")
			}
		})
	}
}

func TestHLSEngineStartRejectsBadArgs(t *testing.T) {
	e := testEngine(t, 1, func(*hlsWorkerSession, *hlsAttempt) error { return nil }, nil)
	if err := e.start(0); err == nil {
		t.Fatal("start(0) accepted")
	}
	if err := e.start(-2); err == nil {
		t.Fatal("start(-2) accepted")
	}
}

func acceptExec(sess *hlsWorkerSession, at *hlsAttempt) error {
	at.accepted = true
	return nil
}

func TestHLSEngineCompletesAll(t *testing.T) {
	e := testEngine(t, 4, acceptExec, nil)
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if e.queue.remaining() != 0 || e.queue.doneCount() != 4 {
		t.Fatalf("queue state: remaining=%d done=%d", e.queue.remaining(), e.queue.doneCount())
	}
	if got := e.prog.ActiveWorkers.Load(); got != 0 {
		t.Fatalf("ActiveWorkers = %d", got)
	}
	if snaps := e.snapshotWorkers(time.Now()); len(snaps) != 0 {
		t.Fatalf("post-wait snapshots = %d", len(snaps))
	}
}

func TestHLSEngineSpawnGate(t *testing.T) {
	release := make(chan struct{})
	e := testEngine(t, 1, func(sess *hlsWorkerSession, at *hlsAttempt) error {
		select {
		case <-release:
			at.accepted = true
			return nil
		case <-at.segCtx.Done():
			return at.segCtx.Err()
		}
	}, nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	// Admission closed: runtime spawn refuses even though work remains.
	if e.spawnWorker() {
		t.Fatal("spawnWorker succeeded with admission closed")
	}
	e.setAdmissionOpen(true)
	if !e.spawnWorker() {
		t.Fatal("spawnWorker refused with admission open")
	}
	close(release)
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	// After completion spawn refuses — sealed by emptiness.
	e.setAdmissionOpen(true)
	if e.spawnWorker() {
		t.Fatal("spawnWorker succeeded after completion")
	}
}

func TestHLSEngineKillRequeuesFree(t *testing.T) {
	var attempts atomic.Int64
	killed := make(chan struct{})
	releaseSecond := make(chan struct{})
	e := testEngine(t, 1, func(sess *hlsWorkerSession, at *hlsAttempt) error {
		n := attempts.Add(1)
		if n == 1 {
			close(killed) // signal attempt published
			<-at.segCtx.Done()
			return at.segCtx.Err()
		}
		<-releaseSecond
		at.accepted = true
		return nil
	}, nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	<-killed
	ids := e.liveSessionIDs()
	if len(ids) != 1 {
		t.Fatalf("live sessions = %v", ids)
	}
	if !e.cancelAttemptKill(ids[0]) {
		t.Fatal("cancelAttemptKill returned false")
	}
	// Let the second attempt land.
	time.Sleep(30 * time.Millisecond)
	close(releaseSecond)
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	// Kill must not consume retry budget.
	task := e.queue.tasks[0]
	if task.retries != 0 || task.genericAttempt != 0 {
		t.Fatalf("kill consumed budget: retries=%d generic=%d", task.retries, task.genericAttempt)
	}
}

func TestHLSEngineDrainIdleWorker(t *testing.T) {
	e := testEngine(t, 3, acceptExec, nil)
	// No work picked yet — worker sits in pop.
	blockPop := make(chan struct{})
	e.execAttempt = func(sess *hlsWorkerSession, at *hlsAttempt) error {
		close(blockPop)
		<-at.segCtx.Done()
		return at.segCtx.Err()
	}
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	<-blockPop
	ids := e.liveSessionIDs()
	if !e.markDraining(ids[0]) {
		t.Fatal("markDraining returned false")
	}
	err := e.wait()
	if !errors.Is(err, errEngineIncomplete) {
		t.Fatalf("wait = %v, want errEngineIncomplete", err)
	}
	// The leased segment was requeued — all three remain unfinished.
	if e.queue.remaining() != 3 {
		t.Fatalf("remaining = %d, want 3", e.queue.remaining())
	}
}

func TestHLSEngineDrainDuringCommit(t *testing.T) {
	inCommit := make(chan struct{})
	e := testEngine(t, 1, func(sess *hlsWorkerSession, at *hlsAttempt) error {
		sess.setPhase(hlsPhaseCommit)
		close(inCommit)
		time.Sleep(30 * time.Millisecond)
		at.accepted = true
		return nil
	}, nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	<-inCommit
	ids := e.liveSessionIDs()
	if !e.markDraining(ids[0]) {
		t.Fatal("markDraining returned false")
	}
	// The commit settles fully; wait reports success (queue empty).
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if e.queue.remaining() != 0 {
		t.Fatalf("remaining = %d", e.queue.remaining())
	}
}

func TestHLSEngineParentCancelIdentity(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	e := testEngine(t, 2, func(sess *hlsWorkerSession, at *hlsAttempt) error {
		<-at.segCtx.Done()
		return at.segCtx.Err()
	}, func(p *segmentEngineParams) { p.Parent = parent })
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	err := e.wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait = %v, want context.Canceled", err)
	}
}

func TestHLSEngineFatalStopsPeers(t *testing.T) {
	var once sync.Once
	secondBlocked := make(chan struct{})
	storeErr := newStorageError("output write", "/tmp/x", errors.New("boom"))
	e := testEngine(t, 2, func(sess *hlsWorkerSession, at *hlsAttempt) error {
		if at.index == 0 {
			return storeErr
		}
		once.Do(func() { close(secondBlocked) })
		<-at.segCtx.Done()
		return at.segCtx.Err()
	}, nil)
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if _, ok := errors.AsType[*storageError](err); !ok {
		t.Fatalf("wait = %v, want *storageError", err)
	}
	// Both workers exited; nothing left running.
	if len(e.liveSessionIDs()) != 0 {
		t.Fatalf("sessions still live: %v", e.liveSessionIDs())
	}
}

func TestHLSEngineSnapshotDuringRun(t *testing.T) {
	published := make(chan struct{})
	release := make(chan struct{})
	e := testEngine(t, 1, func(sess *hlsWorkerSession, at *hlsAttempt) error {
		close(published)
		select {
		case <-release:
			at.accepted = true
			return nil
		case <-at.segCtx.Done():
			return at.segCtx.Err()
		}
	}, nil)
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	<-published
	snaps := e.snapshotWorkers(time.Now())
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
	s := snaps[0]
	if s.WorkerID != 0 || s.WorkerStartUnix == 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.WaitingOnLimiter {
		t.Fatal("network-phase attempt must not project waiting")
	}
	views := e.sessionViews()
	if len(views) != 1 || views[0].AttemptGen == 0 || views[0].Index != 0 {
		t.Fatalf("views = %+v", views)
	}
	close(release)
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
}

func TestHLSEngineAcceptedNotify(t *testing.T) {
	var mu sync.Mutex
	var got []int
	e := testEngine(t, 3, acceptExec, func(p *segmentEngineParams) {
		p.OnAccepted = func(index int, res IngestResult, started time.Time) {
			mu.Lock()
			got = append(got, index)
			mu.Unlock()
		}
	})
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("onAccepted calls = %v", got)
	}
}

func TestHLSEngineRetryConsumesBudget(t *testing.T) {
	var attempts atomic.Int64
	var slept []time.Duration
	var mu sync.Mutex
	e := testEngine(t, 1, func(sess *hlsWorkerSession, at *hlsAttempt) error {
		if attempts.Add(1) == 1 {
			return fmt.Errorf("read: %w", io.ErrUnexpectedEOF)
		}
		at.accepted = true
		return nil
	}, func(p *segmentEngineParams) {
		p.sleep = func(ctx context.Context, d time.Duration) error {
			mu.Lock()
			slept = append(slept, d)
			mu.Unlock()
			return nil
		}
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
	task := e.queue.tasks[0]
	if task.retries != 1 || task.genericAttempt != 1 {
		t.Fatalf("retries=%d generic=%d", task.retries, task.genericAttempt)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(slept) != 1 || slept[0] != hlsRetryDelay(1) {
		t.Fatalf("slept = %v, want [500ms]", slept)
	}
}
