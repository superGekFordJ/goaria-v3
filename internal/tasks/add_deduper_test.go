package tasks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

func deduperClaimCount(d *AddDeduper) int {
	count := 0
	d.claims.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func deduperHasKey(d *AddDeduper, key string) bool {
	_, ok := d.claims.Load(key)
	return ok
}

func TestAddDeduper_OwnerWaiterAndResultPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &AddDeduper{}
		claim, res, owner := d.acquire(context.Background(), "k")
		if !owner || res.available || res.err != nil || res.gid != "" {
			t.Fatalf("first acquire = (owner=%v, res=%+v), want clean owner", owner, res)
		}

		type waiterOutcome struct {
			res   addClaimResult
			owner bool
		}
		waiterOut := make(chan waiterOutcome, 1)
		go func() {
			_, r, o := d.acquire(context.Background(), "k")
			waiterOut <- waiterOutcome{res: r, owner: o}
		}()
		synctest.Wait() // waiter is attached and durably blocked

		select {
		case got := <-waiterOut:
			t.Fatalf("waiter returned before owner settled: %+v", got)
		default:
		}

		d.finish(claim, addClaimResult{gid: "gid-1", available: true})
		got := <-waiterOut
		if got.owner {
			t.Fatal("attached caller must never become owner of a taken claim")
		}
		if !got.res.available || got.res.gid != "gid-1" || got.res.err != nil {
			t.Fatalf("waiter result = %+v, want available gid-1", got.res)
		}

		// The fresh done-record keeps coalescing new callers.
		_, res2, owner2 := d.acquire(context.Background(), "k")
		if owner2 || !res2.available || res2.gid != "gid-1" {
			t.Fatalf("fresh hit = (owner=%v, res=%+v), want available gid-1", owner2, res2)
		}
	})
}

func TestAddDeduper_OwnerErrorPropagatesAndReleasesKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &AddDeduper{}
		claim, _, owner := d.acquire(context.Background(), "k")
		if !owner {
			t.Fatal("first acquire must be owner")
		}

		sentinel := errors.New("disk full")
		wrapped := fmt.Errorf("add failed: %w", sentinel)
		waiterOut := make(chan addClaimResult, 1)
		go func() {
			_, r, _ := d.acquire(context.Background(), "k")
			waiterOut <- r
		}()
		synctest.Wait()

		d.finish(claim, addClaimResult{err: wrapped})
		got := <-waiterOut
		if !errors.Is(got.err, sentinel) {
			t.Fatalf("waiter err = %v, want errors.Is(%v)", got.err, sentinel)
		}

		// Failed claims release immediately: the key is free for a new owner
		// and the token leaves no residue behind.
		if deduperHasKey(d, "k") {
			t.Fatal("failed claim must not occupy the registry")
		}
		retry, _, owner := d.acquire(context.Background(), "k")
		if !owner {
			t.Fatal("a new caller must become owner after owner failure")
		}
		d.finish(retry, addClaimResult{err: errors.New("still failing")})
		if deduperHasKey(d, "k") {
			t.Fatal("failed retry claim must leave no residue")
		}
	})
}

func TestAddDeduper_FreshHitDoesNotExtendTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &AddDeduper{}
		claim, _, _ := d.acquire(context.Background(), "k")
		d.finish(claim, addClaimResult{gid: "g", available: true})

		// T0+9s: still inside the 10s window — a hit, and it must not
		// extend the original expiry.
		time.Sleep(9 * time.Second)
		_, res, owner := d.acquire(context.Background(), "k")
		if owner || !res.available || res.gid != "g" {
			t.Fatalf("fresh hit = (owner=%v, res=%+v)", owner, res)
		}

		// T0+11s: the record expired on the original completion time.
		time.Sleep(2 * time.Second)
		claim2, _, owner := d.acquire(context.Background(), "k")
		if !owner {
			t.Fatal("expired record must yield a new owner")
		}
		d.finish(claim2, addClaimResult{available: true})
	})
}

func TestAddDeduper_InFlightSurvivesTTLAndSweep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &AddDeduper{}
		ownerClaim, _, _ := d.acquire(context.Background(), "k")

		// Keep the claim in-flight well past the TTL, then force activity on
		// another key so the throttled sweep runs.
		time.Sleep(15 * time.Second)
		other, _, owner := d.acquire(context.Background(), "other")
		if !owner {
			t.Fatal("unrelated key must still own its claim")
		}

		// The aged in-flight claim must not have been swept: a same-key
		// caller still attaches as waiter instead of becoming owner.
		waiterOut := make(chan addClaimResult, 1)
		go func() {
			_, r, _ := d.acquire(context.Background(), "k")
			waiterOut <- r
		}()
		synctest.Wait()
		select {
		case r := <-waiterOut:
			t.Fatalf("in-flight claim was swept; waiter returned %+v", r)
		default:
		}

		d.finish(ownerClaim, addClaimResult{gid: "g", available: true})
		got := <-waiterOut
		if !got.available || got.gid != "g" {
			t.Fatalf("waiter got %+v, want available g", got)
		}
		d.finish(other, addClaimResult{available: true})

		// The long-lived owner still earns a full TTL from completion time:
		// live at finish+9s, gone at finish+11s.
		time.Sleep(9 * time.Second)
		_, res, _ := d.acquire(context.Background(), "k")
		if !res.available {
			t.Fatalf("record must live a full TTL after completion, got %+v", res)
		}
		time.Sleep(2 * time.Second)
		if _, _, owner := d.acquire(context.Background(), "k"); !owner {
			t.Fatal("record must expire TTL after completion")
		}
	})
}

func TestAddDeduper_WaiterContextCancelKeepsOwnerClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &AddDeduper{}
		claim, _, _ := d.acquire(context.Background(), "k")

		// An already-cancelled context errors out without disturbing the
		// owner token or creating a second claim.
		cancelled, cancelNow := context.WithCancel(context.Background())
		cancelNow()
		_, res, owner := d.acquire(cancelled, "k")
		if owner || !errors.Is(res.err, context.Canceled) {
			t.Fatalf("cancelled acquire = (owner=%v, res=%+v), want context.Canceled", owner, res)
		}
		if got := deduperClaimCount(d); got != 1 {
			t.Fatalf("claims = %d, want 1 (owner token untouched)", got)
		}

		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan addClaimResult, 1)
		go func() {
			_, r, _ := d.acquire(ctx, "k")
			out <- r
		}()
		// A second waiter stays attached through the first one's cancel.
		out2 := make(chan addClaimResult, 1)
		go func() {
			_, r, _ := d.acquire(context.Background(), "k")
			out2 <- r
		}()
		synctest.Wait()

		cancel()
		got := <-out
		if !errors.Is(got.err, context.Canceled) || got.available {
			t.Fatalf("cancelled waiter got %+v, want context.Canceled", got)
		}
		if !deduperHasKey(d, "k") {
			t.Fatal("cancelled waiter must not delete the owner token")
		}

		d.finish(claim, addClaimResult{gid: "g", available: true})
		got2 := <-out2
		if !got2.available || got2.gid != "g" {
			t.Fatalf("surviving waiter got %+v, want available g", got2)
		}
	})
}

func TestAddDeduper_ExpiredTokenRemovalProtectsReplacement(t *testing.T) {
	d := &AddDeduper{}

	// The ABA guard: removal is conditioned on the exact token pointer, so a
	// stale pointer can never delete a replacement owner.
	stale := &addClaim{key: "k", done: make(chan struct{})}
	stale.available = true
	stale.expiresAt = time.Now().Add(-time.Second)
	close(stale.done)
	d.claims.Store("k", stale)

	replacement := &addClaim{key: "k", done: make(chan struct{})}
	d.claims.Store("k", replacement) // a new owner landed in between
	if d.claims.CompareAndDelete("k", stale) {
		t.Fatal("CompareAndDelete removed a different token")
	}
	if !deduperHasKey(d, "k") {
		t.Fatal("replacement token must survive the stale removal")
	}
	d.finish(replacement, addClaimResult{available: true})
}

func TestAddDeduper_ExpiredRecordRacesToSingleOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &AddDeduper{}
		old, _, _ := d.acquire(context.Background(), "k")
		d.finish(old, addClaimResult{available: true})
		time.Sleep(addClaimRecordTTL + time.Second)

		const racers = 4
		ownerCh := make(chan *addClaim, racers)
		results := make(chan addClaimResult, racers)
		for i := 0; i < racers; i++ {
			go func() {
				c, r, o := d.acquire(context.Background(), "k")
				if o {
					ownerCh <- c
					return
				}
				results <- r
			}()
		}

		ownerClaim := <-ownerCh
		d.finish(ownerClaim, addClaimResult{gid: "g2", available: true})
		for i := 0; i < racers-1; i++ {
			r := <-results
			if !r.available || r.gid != "g2" {
				t.Fatalf("racer %d got %+v, want coalesced available g2", i, r)
			}
		}
		select {
		case extra := <-ownerCh:
			t.Fatalf("second owner appeared: %v", extra)
		case extra := <-results:
			t.Fatalf("extra racer result: %+v", extra)
		default:
		}
	})
}

func TestAddDeduper_SweepRemovesOnlyExpiredSettled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &AddDeduper{}
		stale, _, _ := d.acquire(context.Background(), "stale")
		d.finish(stale, addClaimResult{available: true})

		time.Sleep(2 * time.Second)
		fresh, _, _ := d.acquire(context.Background(), "fresh")
		d.finish(fresh, addClaimResult{available: true})
		inflight, _, _ := d.acquire(context.Background(), "inflight")
		failed, _, _ := d.acquire(context.Background(), "failed")
		d.finish(failed, addClaimResult{err: errors.New("nope")})

		if deduperHasKey(d, "failed") {
			t.Fatal("failed claim must be removed at settlement")
		}

		// Now "stale" (settled at T0) is expired while "fresh" (T0+2) still
		// lives; an unrelated acquire triggers the sweep.
		time.Sleep(9 * time.Second)
		trigger, _, _ := d.acquire(context.Background(), "trigger")
		if deduperHasKey(d, "stale") {
			t.Fatal("sweep must remove the expired settled token")
		}
		if !deduperHasKey(d, "fresh") {
			t.Fatal("sweep must keep fresh done-records")
		}
		if !deduperHasKey(d, "inflight") {
			t.Fatal("sweep must keep in-flight claims")
		}

		d.finish(inflight, addClaimResult{available: true})
		d.finish(trigger, addClaimResult{available: true})
	})
}

func TestAddDeduper_FreshRecordsHaveNoCapacityEviction(t *testing.T) {
	d := &AddDeduper{}
	const n = 3000
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k-%d", i)
		claim, _, owner := d.acquire(context.Background(), key)
		if !owner {
			t.Fatalf("key %s unexpectedly collided", key)
		}
		d.finish(claim, addClaimResult{gid: "g", available: true})
	}
	if got := deduperClaimCount(d); got != n {
		t.Fatalf("claims = %d, want %d: fresh records must not be evicted", got, n)
	}
	_, res, owner := d.acquire(context.Background(), "k-42")
	if owner || !res.available || res.gid != "g" {
		t.Fatalf("sampled fresh hit = (owner=%v, res=%+v)", owner, res)
	}
}

func TestAddDeduper_ZeroValueUsable(t *testing.T) {
	var d AddDeduper
	claim, _, owner := d.acquire(context.Background(), "k")
	if !owner {
		t.Fatal("zero-value deduper must grant ownership")
	}
	d.finish(claim, addClaimResult{available: true})
	if _, res, owner := d.acquire(context.Background(), "k"); owner || !res.available {
		t.Fatalf("zero-value deduper must retain the record, got (owner=%v, res=%+v)", owner, res)
	}
}

// --- Shared fixtures for the dedup seam tests ---

// dedupGateEngine wraps the disk-space stub with an admission gate: every
// AddUri reports entry on entered, then blocks on gate until released — the
// deterministic proof of who actually reached the engine.
type dedupGateEngine struct {
	diskSpaceStubEngine

	gmu      sync.Mutex
	addN     int
	inFlight int
	peak     int
	urls     []string
	entered  chan struct{}
	gate     chan struct{}
	active   []rpc.Task
	waiting  []rpc.Task
	stopped  []rpc.Task

	tellEntered chan struct{}
	tellGate    chan struct{}

	failOne  atomic.Bool
	failErr  error
	panicOne atomic.Bool
}

func (e *dedupGateEngine) AddUri(url string, _ rpc.AddURIOptions) (string, error) {
	e.gmu.Lock()
	e.addN++
	n := e.addN
	e.inFlight++
	if e.inFlight > e.peak {
		e.peak = e.inFlight
	}
	e.urls = append(e.urls, url)
	entered, gate := e.entered, e.gate
	e.gmu.Unlock()

	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		<-gate
	}

	e.gmu.Lock()
	e.inFlight--
	e.gmu.Unlock()

	if e.panicOne.CompareAndSwap(true, false) {
		panic("dedup gate engine panic")
	}
	if e.failOne.CompareAndSwap(true, false) {
		return "", e.failErr
	}
	return fmt.Sprintf("gid-%d", n), nil
}

func (e *dedupGateEngine) TellActive() ([]rpc.Task, error) {
	e.gmu.Lock()
	entered, gate, active := e.tellEntered, e.tellGate, e.active
	e.gmu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	return active, nil
}

func (e *dedupGateEngine) TellWaiting(int, int) ([]rpc.Task, error) {
	e.gmu.Lock()
	defer e.gmu.Unlock()
	return e.waiting, nil
}

func (e *dedupGateEngine) TellStopped(int, int) ([]rpc.Task, error) {
	e.gmu.Lock()
	defer e.gmu.Unlock()
	return e.stopped, nil
}

func (e *dedupGateEngine) addCount() int {
	e.gmu.Lock()
	defer e.gmu.Unlock()
	return e.addN
}

func (e *dedupGateEngine) peakInFlight() int {
	e.gmu.Lock()
	defer e.gmu.Unlock()
	return e.peak
}

func (e *dedupGateEngine) resetGate() {
	e.gmu.Lock()
	defer e.gmu.Unlock()
	e.gate = make(chan struct{})
}

func (e *dedupGateEngine) disarm() {
	e.gmu.Lock()
	defer e.gmu.Unlock()
	e.gate = nil
}

func (e *dedupGateEngine) release() {
	e.gmu.Lock()
	defer e.gmu.Unlock()
	if e.gate != nil {
		close(e.gate)
	}
}

func setupDedupEnv(t *testing.T) {
	t.Helper()
	originalConfig := config.Get()
	originalSaveEnabled := history.SaveEnabled
	monitor.ResetDownloadGroupNamerForTest()
	restoreNamer := monitor.ConfigureDownloadGroupNamerForTest(10*time.Second, 10*time.Second, 1)
	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	history.DisableSaveForTest()
	history.Clear()
	config.SetTestConfig(&config.AppConfig{
		DownloadDir:     t.TempDir(),
		SmartThreadMode: false,
	})
	t.Cleanup(func() {
		monitor.ResetDownloadGroupNamerForTest()
		restoreNamer()
		history.Clear()
		monitor.ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		config.SetTestConfig(originalConfig)
	})
}

// refRecordingAdapter records every neutral-ref Release so tests can prove
// losers also release their own reference exactly once.
type refRecordingAdapter struct {
	fakePortAdapter
	mu       sync.Mutex
	released []string
}

func (a *refRecordingAdapter) Release(ref string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.released = append(a.released, ref)
}

func (a *refRecordingAdapter) releasedSnapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.released...)
}

type extAddResult struct {
	gid string
	err error
}

func callExtensionAdd(svc *Service, req extension.DownloadRequest, out chan<- extAddResult) {
	go func() {
		gid, err := svc.AddUriFromExtension(req)
		out <- extAddResult{gid, err}
	}()
}
