package tasks

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// admissionTestConfig installs a deterministic config snapshot: smart mode is
// toggled per test, connection ceilings fixed, no daemon dependencies.
func admissionTestConfig(t *testing.T, smart bool, maxConcurrent string) {
	t.Helper()
	orig := config.Get()
	cfg := config.DefaultConfig()
	cfg.SmartThreadMode = smart
	cfg.MaxConnections = "8"
	cfg.MaxConcurrentDownloads = maxConcurrent
	cfg.MinThreadLife = 5
	config.SetTestConfig(&cfg)
	t.Cleanup(func() { config.SetTestConfig(orig) })
}

// admissionTestPool builds a real managed pool, a testing engine wrapper, and
// the controller bound to it. The controller is NOT started — tests either
// drive evaluate() directly or Start it explicitly.
func admissionTestPool(t *testing.T, capacity int) (*scheduler.Scheduler, *rpc.SurgeEngine, *AdmissionController, chan types.DownloadEvent) {
	t.Helper()
	ch := make(chan types.DownloadEvent, 64)
	pool, err := scheduler.NewManaged(ch, capacity)
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	t.Cleanup(pool.GracefulShutdown)
	engine := rpc.NewSurgeEngineForTesting(pool)
	ctl := NewAdmissionController(engine)
	return pool, engine, ctl, ch
}

// queueManagedWaiting registers a queued managed candidate plus its
// monitor-accepted waiting tracker row with settled scope/env metadata.
func queueManagedWaiting(t *testing.T, pool *scheduler.Scheduler, tr *monitor.TaskTracker,
	id, url, scope, domain, envKey string, workers int) scheduler.AdmissionKey {
	t.Helper()
	gid := "sg_" + id
	tr.EnsureTrackedFromEvent(gid, 4096, url, workers, "waiting")
	tr.SetScopeAndEnv(gid, scope, 0, domain, envKey)
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           url,
		OutputPath:    t.TempDir(),
		Filename:      id + ".bin",
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: workers},
	}) {
		t.Fatalf("managed Add refused for %s", id)
	}
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key.ID == id {
			return c.Key
		}
	}
	t.Fatalf("candidate %s missing from snapshot", id)
	return scheduler.AdmissionKey{}
}

func admissionPendingTicket(t *testing.T, tr *monitor.TaskTracker, gid string) monitor.AdmissionTicket {
	t.Helper()
	ticket, ok := tr.PendingAdmissionTicket(gid)
	if !ok {
		t.Fatalf("no pending admission ticket on %s", gid)
	}
	return ticket
}

// The real closed loop: queued candidate holds until the controller admits;
// the scheduler worker then performs a genuine HTTP GET and reports started.
func TestAdmissionRuntime_DispatchesRealDownload(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, false, "32")
	pool, engine, ctl, ch := admissionTestPool(t, 2)
	engine.SetAdmissionWake(ctl.Kick)

	var hits atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer server.Close()

	key := queueManagedWaiting(t, pool, tr, "rt1", server.URL, "wan", "local.test", "env1", 1)

	// The managed worker must not self-pick before an Admit ticket.
	select {
	case ev := <-ch:
		t.Fatalf("dispatched without admission: %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
	if hits.Load() != 0 {
		t.Fatalf("worker GET before admission: %d hits", hits.Load())
	}

	ctl.Start()
	defer ctl.Stop()

	admissionWaitEvent(t, ch, types.EventStarted, key.ID)
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("admitted task never reached the server")
	}
	// The reservation was consumed by the real start path (no monitor here):
	// the ticket stays recorded against the entry for reconciliation.
	tt := mustTracked(t, tr, "sg_rt1")
	if tt.Status == "active" {
		t.Fatal("controller must not synthesize active status")
	}
}

// Capacity is physical: a candidate beyond the slot limit stays queued
// without a reservation, and is dispatched once a busy slot is released.
// Committed never exceeds Capacity on any round.
func TestAdmissionRuntime_CapacityGateAndRefill(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, false, "32")
	pool, _, ctl, ch := admissionTestPool(t, 1)

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		// Keep the first download in-flight until the test cancels it.
		time.Sleep(1500 * time.Millisecond)
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer server.Close()

	keyA := queueManagedWaiting(t, pool, tr, "capa", server.URL, "wan", "local.test", "env1", 1)
	keyB := queueManagedWaiting(t, pool, tr, "capb", server.URL, "wan", "local.test", "env1", 1)

	ctl.evaluate(time.Now())

	// A admitted; B hit zero slot headroom and was never reserved — no ticket,
	// no occupancy, no phantom commitment.
	if snap := pool.AdmissionSnapshot(); snap.CommittedSlots != 1 {
		t.Fatalf("committed = %d, want 1", snap.CommittedSlots)
	}
	if _, pending := tr.PendingAdmissionTicket("sg_capb"); pending {
		t.Fatal("over-capacity candidate must not hold a reservation")
	}
	ttB := mustTracked(t, tr, "sg_capb")
	if ttB.Status != "waiting" {
		t.Fatalf("B status = %q, want waiting", ttB.Status)
	}

	// Free the physical slot: cancel A, then the next round admits B.
	admissionWaitEvent(t, ch, types.EventStarted, keyA.ID)
	pool.Cancel(keyA.ID)
	deadline := time.Now().Add(5 * time.Second)
	for pool.AdmissionSnapshot().CommittedSlots != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ctl.evaluate(time.Now())
	admissionWaitEvent(t, ch, types.EventStarted, keyB.ID)
	if snap := pool.AdmissionSnapshot(); snap.CommittedSlots > 1 {
		t.Fatalf("committed = %d after refill, capacity exceeded", snap.CommittedSlots)
	}
}

// A reservation that survives into a later round (created by an earlier
// evaluation or an interrupted attempt) is re-evaluated against a
// self-excluded occupancy view, then admitted on the SAME ticket without
// re-reserving or renewing its anchor (R4).
func TestAdmissionRuntime_RetainedPendingReattemptAccepted(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, false, "32")
	pool, _, ctl, _ := admissionTestPool(t, 2)

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer server.Close()

	key := queueManagedWaiting(t, pool, tr, "pend", server.URL, "wan", "local.test", "env1", 2)

	// Plant the reservation exactly as a previous round would have left it
	// after an unavailable Admit — pending ticket, waiting row, lease anchor.
	seedParams := smartthread.ThreadParams{Split: 2, MinSize: 1 << 20, TargetBandwidth: 500_000}
	ticket, created := tr.ReserveAdmission("sg_pend", key.Token, seedParams, time.Now(), 0, true)
	if !created {
		t.Fatal("seeded reservation refused")
	}
	anchor := mustTracked(t, tr, "sg_pend").ActivatedAt

	ctl.evaluate(time.Now())

	// Accepted on the retained ticket; the row's anchor and estimate are not
	// refined mid-flight (only a fresh Admit applies tighter values).
	found := false
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key == key && c.Admitted {
			found = true
		}
	}
	if !found {
		t.Fatal("retained pending candidate was not admitted")
	}
	if got := admissionPendingTicket(t, tr, "sg_pend"); got != ticket {
		t.Fatalf("ticket changed across re-admission: %+v -> %+v", ticket, got)
	}
	if !mustTracked(t, tr, "sg_pend").ActivatedAt.Equal(anchor) {
		t.Fatal("retained reservation anchor renewed on re-admission")
	}
	if snap := pool.AdmissionSnapshot(); snap.CommittedSlots != 1 {
		t.Fatalf("committed = %d, want 1", snap.CommittedSlots)
	}
}

// A blocked gate decision is a local rejection, not a revocation: the
// candidate keeps both its queue position and a live retained reservation.
func TestAdmissionRuntime_BlockedGateKeepsPendingTicket(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, true, "8")
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	pool, _, ctl, _ := admissionTestPool(t, 4)

	// Saturate the domain connection budget: eight active rows each holding
	// one worker consume the whole default domain allowance (maxConn=8).
	for i := 0; i < 8; i++ {
		gid := "sg_busy" + string(rune('a'+i))
		tr.EnsureTrackedFromEvent(gid, 100, "http://h/"+gid, 1, "active")
		tr.ObserveActivation(gid, 0, false)
		tr.SetThreadInfo(gid, 1, false)
		tr.SetScopeAndEnv(gid, "wan", 0, "full.com", "env1")
	}
	key := queueManagedWaiting(t, pool, tr, "held", "http://h/held", "wan", "full.com", "env1", 2)
	if _, created := tr.ReserveAdmission("sg_held", key.Token,
		smartthread.ThreadParams{Split: 2, MinSize: 1 << 20, TargetBandwidth: 400_000},
		time.Now(), 0, true); !created {
		t.Fatal("seeded reservation refused")
	}

	ctl.evaluate(time.Now())

	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key == key && c.Admitted {
			t.Fatal("candidate admitted despite saturated domain connections")
		}
	}
	if got := admissionPendingTicket(t, tr, "sg_held"); got.Token != key.Token {
		t.Fatal("gate rejection revoked a retained ticket")
	}
}

// A queued opportunity that dies after its reservation was written has its
// self-held ticket released by reconciliation — a stale ticket can never
// revoke a newer reservation.
func TestAdmissionRuntime_ReconcileReleasesDeadKey(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, false, "32")
	pool, _, ctl, _ := admissionTestPool(t, 1)

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer server.Close()

	key := queueManagedWaiting(t, pool, tr, "dead", server.URL, "wan", "local.test", "env1", 1)
	ctl.evaluate(time.Now())
	admissionPendingTicket(t, tr, "sg_dead")

	// Cancel the queued candidate; once the key leaves candidates+busy the
	// next round reconciles: the self-held reservation is conditionally
	// released. Poll because an async worker claim may hold the key briefly.
	pool.Cancel(key.ID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctl.evaluate(time.Now())
		if _, pending := tr.PendingAdmissionTicket("sg_dead"); !pending {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, pending := tr.PendingAdmissionTicket("sg_dead"); pending {
		t.Fatal("dead key's reservation survived reconciliation")
	}
	if _, live := ctl.entries[key]; live {
		t.Fatal("dead key entry survived reconciliation")
	}
}

// Candidates whose Add-path metadata has not settled are skipped, not
// dropped — they dispatch once scope/env lands.
func TestAdmissionRuntime_MetadataUnsettledWaits(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, false, "32")
	pool, _, ctl, _ := admissionTestPool(t, 2)

	if !pool.Add(types.DownloadRecord{
		ID:            "meta1",
		URL:           "http://127.0.0.1/f",
		OutputPath:    t.TempDir(),
		Filename:      "meta1.bin",
		ProgressState: progress.New("meta1", 0),
		Runtime:       &types.RuntimeConfig{Workers: 1},
	}) {
		t.Fatal("Add refused")
	}
	tr.EnsureTrackedFromEvent("sg_meta1", 0, "http://127.0.0.1/f", 1, "waiting")
	// No SetScopeAndEnv — metadata unsettled.

	ctl.evaluate(time.Now())
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Admitted {
			t.Fatalf("unsettled candidate admitted: %+v", c)
		}
	}
	if _, pending := tr.PendingAdmissionTicket("sg_meta1"); pending {
		t.Fatal("unsettled candidate holds a reservation")
	}

	tr.SetScopeAndEnv("sg_meta1", "wan", 0, "local.test", "env1")
	ctl.evaluate(time.Now())
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key.ID == "meta1" && !c.Admitted {
			t.Fatal("settled candidate still unadmitted")
		}
	}
}

// Off mode is a manual policy: EvaluateAdmission with Smart=false applies the
// user's own normalized count as the global cap — no smart resource checks
// and no reservation written for candidates beyond the cap.
func TestAdmissionRuntime_OffModeUserCap(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, false, "2")
	pool, _, ctl, _ := admissionTestPool(t, 32)

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		time.Sleep(1200 * time.Millisecond)
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer server.Close()

	queueManagedWaiting(t, pool, tr, "m1", server.URL+"/1", "wan", "a.com", "env1", 2)
	queueManagedWaiting(t, pool, tr, "m2", server.URL+"/2", "wan", "a.com", "env1", 2)
	queueManagedWaiting(t, pool, tr, "m3", server.URL+"/3", "wan", "a.com", "env1", 2)

	ctl.evaluate(time.Now())
	// Admitted candidates may already be claimed as busy; committed slots is
	// the invariant the manual cap actually bounds.
	if snap := pool.AdmissionSnapshot(); snap.CommittedSlots != 2 {
		t.Fatalf("off-mode committed = %d, want 2 (user cap)", snap.CommittedSlots)
	}
	// The third never reached Reserve — zero headroom skips the write path.
	if _, pending := tr.PendingAdmissionTicket("sg_m3"); pending {
		t.Fatal("over-cap manual candidate holds a reservation")
	}
}

// Smart mode enforces the cold-scope guard: with no measured history a
// scope+env bucket stops accepting after the cold limit even when global
// headroom remains.
func TestAdmissionRuntime_SmartColdScopeStopsFlood(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, true, "32")
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	// Generous connection budget so the cold-scope count — not workers —
	// is the blocking dimension.
	cfg := *config.Get()
	cfg.MaxConnections = "64"
	config.SetTestConfig(&cfg)
	pool, _, ctl, _ := admissionTestPool(t, 32)

	// Claims must stay busy for the whole observation: a completed download
	// releases its busy slot and would masquerade as "never admitted".
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	for i := 0; i < 7; i++ {
		id := "cold" + string(rune('a'+i))
		queueManagedWaiting(t, pool, tr, id, server.URL+"/"+id, "wan", "cold.com", "envx", 2)
	}
	ctl.evaluate(time.Now())

	// CommittedSlots covers every admission outcome uniformly: admitted-not-
	// claimed candidates plus live Busy claims, however far a worker got.
	if snap := pool.AdmissionSnapshot(); snap.CommittedSlots != 5 {
		t.Fatalf("smart cold-scope committed = %d, want 5 (cold limit)", snap.CommittedSlots)
	}
	// Overflow candidates keep waiting without burning resources.
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Admitted || c.InFlight {
			continue
		}
		if _, pending := tr.PendingAdmissionTicket("sg_" + c.Key.ID); pending {
			t.Fatalf("rejected candidate %s holds a reservation", c.Key.ID)
		}
	}
}

// The 2s fallback ticker finds a queued candidate even when every Kick edge
// was missed — a real dispatch, not a simulated notify.
func TestAdmissionRuntime_TickerFallbackDispatches(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, false, "32")
	pool, _, ctl, ch := admissionTestPool(t, 1)

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer server.Close()

	ctl.Start()
	defer ctl.Stop()
	// Let the initial kick drain on the empty queue, then add the candidate
	// with no further notification — the ticker alone must deliver it.
	time.Sleep(400 * time.Millisecond)
	key := queueManagedWaiting(t, pool, tr, "tick1", server.URL, "wan", "local.test", "env1", 1)
	admissionWaitEvent(t, ch, types.EventStarted, key.ID)
}

// Stop joins the sole goroutine; a stopped or never-started instance admits
// nothing and Start cannot resurrect it.
func TestAdmissionRuntime_StopJoinsAndIsFinal(t *testing.T) {
	pool, engine, ctl, _ := admissionTestPool(t, 1)
	_ = pool

	// Never started: Stop must return immediately.
	done := make(chan struct{})
	go func() { ctl.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop on unstarted controller blocked")
	}

	ctl2 := NewAdmissionController(engine)
	ctl2.Start()
	ctl2.Stop()
	ctl2.Stop() // idempotent
	ctl2.Start()
	ctl2.Stop()
}

// Kick must never block its callers: more sends than the channel capacity
// simply merge.
func TestAdmissionRuntime_KickMergesWithoutBlocking(t *testing.T) {
	ctl := NewAdmissionController(nil)
	done := make(chan struct{})
	go func() {
		for range 200 {
			ctl.Kick()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Kick blocked on a full channel")
	}
}

// A nil engine yields an unmanaged snapshot — evaluate is a no-op, never a
// panic or a phantom dispatch.
func TestAdmissionRuntime_NilEngineNoDispatch(t *testing.T) {
	ctl := NewAdmissionController(nil)
	ctl.evaluate(time.Now())
	ctl.Start()
	ctl.Stop()
}

// A retained reservation (created=false on re-admission) that decides Short
// must still latch shortUsed — otherwise the scope+env bucket would hand the
// short exception to a second holder while the first is live.
func TestAdmissionRuntime_RetainedShortLatchesBucket(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, true, "32")
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	pool, _, ctl, _ := admissionTestPool(t, 4)

	// Keep admitted claims busy for the whole observation.
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	// Scope+env peak measured at 1MB/s and fully reserved by a foreign-domain
	// commitment: scope is the only live block, so a progress-lease-sized file
	// in this bucket qualifies for the short exception.
	speedstats.AddRecordV2(1<<20, 1, 1<<20, false, 50, "peer.com", "wan", "envx")
	tr.EnsureTrackedFromEvent("sg_sat", 100, "http://h/sat", 1, "active")
	tr.ObserveActivation("sg_sat", 0, false)
	tr.SetThreadInfo("sg_sat", 1, false)
	tr.SetScopeAndEnv("sg_sat", "wan", 0, "other.com", "envx")
	tr.SetTargetBandwidth("sg_sat", 1<<20)

	// Queue a small candidate and seed the pending ticket exactly as an
	// interrupted earlier round leaves it — live reservation on this key.
	queueShort := func(id string) scheduler.AdmissionKey {
		t.Helper()
		gid := "sg_" + id
		tr.EnsureTrackedFromEvent(gid, 1<<20, server.URL+"/"+id, 1, "waiting")
		tr.SetScopeAndEnv(gid, "wan", 0, "short.com", "envx")
		if !pool.Add(types.DownloadRecord{
			ID:            id,
			URL:           server.URL + "/" + id,
			OutputPath:    t.TempDir(),
			Filename:      id + ".bin",
			TotalSize:     1 << 20,
			ProgressState: progress.New(id, 0),
			Runtime:       &types.RuntimeConfig{Workers: 1},
		}) {
			t.Fatalf("managed Add refused for %s", id)
		}
		for _, c := range pool.AdmissionSnapshot().Candidates {
			if c.Key.ID == id {
				return c.Key
			}
		}
		t.Fatalf("candidate %s missing from snapshot", id)
		return scheduler.AdmissionKey{}
	}
	key1 := queueShort("sh1")
	if _, created := tr.ReserveAdmission("sg_sh1", key1.Token,
		smartthread.ThreadParams{Split: 1, MinSize: 0, TargetBandwidth: 100_000},
		time.Now(), 0, true); !created {
		t.Fatal("seeded reservation refused")
	}

	ctl.evaluate(time.Now())

	// R4: the retained ticket is admitted on a Short decision; the bucket
	// latch must follow the live qualification despite created == false.
	if entry := ctl.entries[key1]; entry == nil || !entry.shortUsed {
		t.Fatal("retained short admission did not latch shortUsed")
	}
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key == key1 && !c.Admitted {
			t.Fatal("retained short candidate was not admitted")
		}
	}

	// A second small candidate in the same bucket: the live holder excludes
	// it from the short exception, so the scope block stands — no ticket.
	queueShort("sh2")
	ctl.evaluate(time.Now())
	if _, pending := tr.PendingAdmissionTicket("sg_sh2"); pending {
		t.Fatal("second short candidate in a held bucket holds a reservation")
	}
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key.ID == "sh2" && c.Admitted {
			t.Fatal("second short candidate admitted despite held bucket")
		}
	}
}
