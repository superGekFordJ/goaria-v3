package tasks

import (
	"net/http"
	"net/http/httptest"
	"strconv"
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
	id, url, scope, domain, envKey string, workers int,
) scheduler.AdmissionKey {
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
	for i := range 8 {
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
// and no reservation written for candidates beyond the cap. The boundary
// values sweep raw=1 through the 32 ceiling.
func TestAdmissionRuntime_OffModeUserCap(t *testing.T) {
	for _, raw := range []int{1, 5, 8, 32} {
		t.Run(strconv.Itoa(raw), func(t *testing.T) {
			tr := admissionSwapTracker(t)
			admissionTestConfig(t, false, strconv.Itoa(raw))
			pool, _, ctl, _ := admissionTestPool(t, 32)

			server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "4096")
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				<-r.Context().Done()
			}))
			defer server.Close()

			// Queue one candidate beyond the cap; the tail must stay unreserved.
			for i := 0; i <= raw; i++ {
				id := "m" + strconv.Itoa(i)
				queueManagedWaiting(t, pool, tr, id, server.URL+"/"+id, "wan", "a.com", "env1", 2)
			}

			ctl.evaluate(time.Now())
			// The cap invariant: committed never exceeds the user count, no
			// matter how worker claims settle (a failed claim may requeue and
			// drop its admitted flag before the snapshot).
			if snap := pool.AdmissionSnapshot(); snap.CommittedSlots > raw {
				t.Fatalf("off-mode committed = %d exceeds user cap %d", snap.CommittedSlots, raw)
			}
			// Admission-write evidence is the deterministic half: exactly raw
			// tracker reservations were created this round, regardless of how
			// physical claims fare afterwards.
			reserved := 0
			for i := 0; i <= raw; i++ {
				if _, ok := tr.PendingAdmissionTicket("sg_m" + strconv.Itoa(i)); ok {
					reserved++
				}
			}
			if reserved != raw {
				t.Fatalf("off-mode reservations = %d, want %d (user cap)", reserved, raw)
			}
			// The tail never reached Reserve — zero headroom skips the write.
			if _, pending := tr.PendingAdmissionTicket("sg_m" + strconv.Itoa(raw)); pending {
				t.Fatal("over-cap manual candidate holds a reservation")
			}
		})
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

	for i := range 7 {
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

// Scale evidence: a thousand gate-blocked candidates must not stop the FIFO
// scan from reaching a qualified tail. Blocked prefixes are rejected by the
// gate before Reserve/Admit, so the sweep stays in-memory and never dials —
// only the tail performs a real fetch.
func TestAdmissionRuntime_LargeQueueScansAll(t *testing.T) {
	tr := admissionSwapTracker(t)
	admissionTestConfig(t, true, "32")
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	pool, _, ctl, ch := admissionTestPool(t, 4)

	var hits atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer server.Close()

	// Scope+env peak 1MB/s fully reserved by a foreign-domain commitment:
	// every flood.com candidate is scope-blocked before the Admit write path.
	speedstats.AddRecordV2(1<<20, 1, 1<<20, false, 50, "peer.com", "wan", "envx")
	tr.EnsureTrackedFromEvent("sg_sat", 100, "http://h/sat", 1, "active")
	tr.ObserveActivation("sg_sat", 0, false)
	tr.SetThreadInfo("sg_sat", 1, false)
	tr.SetScopeAndEnv("sg_sat", "wan", 0, "other.com", "envx")
	tr.SetTargetBandwidth("sg_sat", 1<<20)

	const prefixN = 1000
	for i := range prefixN {
		id := "lq" + strconv.Itoa(i)
		// Unroutable URL: even a mistaken admit fails fast instead of
		// reaching the instrumented server.
		queueManagedWaiting(t, pool, tr, id, "http://127.0.0.1:1/"+id, "wan", "flood.com", "envx", 1)
	}
	// Tail lives in an untouched bucket — no measured block at all.
	keyTail := queueManagedWaiting(t, pool, tr, "lqtail", server.URL+"/tail", "wan", "tail.com", "envy", 1)

	start := time.Now()
	ctl.evaluate(time.Now())
	elapsed := time.Since(start)

	// Real dispatch of the tail: the scheduler worker performs the GET.
	admissionWaitEvent(t, ch, types.EventStarted, keyTail.ID)
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("admitted tail never reached the server")
	}

	// Every prefix candidate stayed unreserved and unadmitted.
	for i := range prefixN {
		if _, pending := tr.PendingAdmissionTicket("sg_lq" + strconv.Itoa(i)); pending {
			t.Fatalf("blocked prefix lq%d holds a reservation", i)
		}
	}
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key != keyTail && (c.Admitted || c.InFlight) {
			t.Fatalf("blocked prefix %s reached the write path", c.Key.ID)
		}
	}
	t.Logf("large queue: blocked=%d tail=%s evaluate=%s httpHits=%d",
		prefixN, keyTail.ID, elapsed, hits.Load())
}

// admissionTestConfigConns is admissionTestConfig with a caller-picked
// MaxConnections, so connection ceilings never shadow the measurement lock.
func admissionTestConfigConns(t *testing.T, smart bool, maxConcurrent, maxConns string) {
	t.Helper()
	orig := config.Get()
	cfg := config.DefaultConfig()
	cfg.SmartThreadMode = smart
	cfg.MaxConnections = maxConns
	cfg.MaxConcurrentDownloads = maxConcurrent
	cfg.MinThreadLife = 5
	config.SetTestConfig(&cfg)
	t.Cleanup(func() { config.SetTestConfig(orig) })
}

// queueManagedWaitingSized is queueManagedWaiting with an explicit
// (total, downloaded) size pair: record eligibility is decided on the
// original total, so tracker, record and progress totals stay consistent.
func queueManagedWaitingSized(t *testing.T, pool *scheduler.Scheduler, tr *monitor.TaskTracker,
	id, url, scope, domain, envKey string, workers int, totalSize, downloaded int64,
) scheduler.AdmissionKey {
	t.Helper()
	gid := "sg_" + id
	tr.EnsureTrackedFromEvent(gid, totalSize, url, workers, "waiting")
	tr.SetScopeAndEnv(gid, scope, 0, domain, envKey)
	dp := progress.New(id, totalSize)
	if downloaded > 0 {
		dp.Bytes.Downloaded.Store(downloaded)
		dp.Bytes.VerifiedProgress.Store(downloaded)
	}
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           url,
		OutputPath:    t.TempDir(),
		Filename:      id + ".bin",
		TotalSize:     totalSize,
		Downloaded:    downloaded,
		ProgressState: dp,
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

// admissionAdmitted reports whether id holds a committed opportunity: still
// queued and admitted, claimed in-flight, or already dispatched under a busy
// claim — a worker pickup can move it out of Candidates between evaluate and
// the assertion.
func admissionAdmitted(pool *scheduler.Scheduler, id string) bool {
	snap := pool.AdmissionSnapshot()
	for _, c := range snap.Candidates {
		if c.Key.ID == id {
			return c.Admitted || c.InFlight
		}
	}
	for _, s := range snap.Busy {
		if s.Key.ID == id {
			return true
		}
	}
	return false
}

// admissionBlockingServer answers headers then holds the request open until
// the caller cancels, so admitted tasks stay busy without real transfer.
// Cleanup joins the pool first: a blocked handler's ctx dies with the client
// conn, which only the worker's shutdown cancels.
func admissionBlockingServer(t *testing.T, pool *scheduler.Scheduler) *httptest.Server {
	t.Helper()
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusOK)
		<-r.Context().Done()
	}))
	t.Cleanup(func() {
		pool.GracefulShutdown()
		server.Close()
	})
	return server
}

const recordLevelTotal = speedstats.MinFileSize + (30 << 20) // 80 MiB total

// seedPendingReservation writes a live admitted-not-started reservation
// exactly like a prior round's accepted/retained ticket would have.
func seedPendingReservation(t *testing.T, tr *monitor.TaskTracker, gid string, token uint64, now time.Time) monitor.AdmissionTicket {
	t.Helper()
	ticket, created := tr.ReserveAdmission(gid, token, smartthread.ThreadParams{
		Split: 1, MinSize: 0, TargetBandwidth: 100 << 10,
	}, now, 0, true)
	if !created || ticket.Token != token {
		t.Fatalf("seed reservation on %s: ticket=%+v created=%v", gid, ticket, created)
	}
	return ticket
}

// TestAdmissionRuntime_ExplorationSerialMixedCandidates covers the mixed FIFO
// round: one record-level measurement per scope|domain, small candidates
// coexisting, foreign domains free, boundary sizes, resume totals, pending
// reservations never self-locking, retained-ticket semantics, restart-window
// fallback, and Aria2 occupancy participation.
func TestAdmissionRuntime_ExplorationSerialMixedCandidates(t *testing.T) {
	t.Run("same domain serializes while small and foreign candidates pass", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		queueManagedWaitingSized(t, pool, tr, "big1", server.URL+"/b1", "wan", "big.test", "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "big2", server.URL+"/b2", "wan", "big.test", "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "big3", server.URL+"/b3", "wan", "big.test", "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "small1", server.URL+"/s1", "wan", "big.test", "env1", 1, 4<<20, 0)
		queueManagedWaitingSized(t, pool, tr, "small2", server.URL+"/s2", "wan", "big.test", "env1", 1, 4<<20, 0)
		queueManagedWaitingSized(t, pool, tr, "far1", server.URL+"/f1", "wan", "other.test", "env1", 1, recordLevelTotal, 0)

		ctl.evaluate(time.Now())
		if !admissionAdmitted(pool, "big1") {
			t.Fatal("first record candidate not admitted")
		}
		if admissionAdmitted(pool, "big2") || admissionAdmitted(pool, "big3") {
			t.Fatal("second/third record candidate bypassed the serial lock")
		}
		if !admissionAdmitted(pool, "small1") || !admissionAdmitted(pool, "small2") {
			t.Fatal("small candidates must coexist with the measurement")
		}
		if !admissionAdmitted(pool, "far1") {
			t.Fatal("foreign-domain record candidate must not be blocked")
		}
		if tk := admissionPendingTicket(t, tr, "sg_big1"); tk.Token == 0 {
			t.Fatal("admitted holder lost its reservation")
		}

		// The live claim still serializes the domain on the next round.
		ctl.evaluate(time.Now())
		if admissionAdmitted(pool, "big2") || admissionAdmitted(pool, "big3") {
			t.Fatal("committed holder stopped locking on the next round")
		}
	})

	t.Run("exact MinFileSize is not record-level but still obeys other gates", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		queueManagedWaitingSized(t, pool, tr, "holder", server.URL+"/h", "wan", "edge.test", "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "exact", server.URL+"/e", "wan", "edge.test", "env1", 1, speedstats.MinFileSize, 0)
		queueManagedWaitingSized(t, pool, tr, "over", server.URL+"/o", "wan", "edge.test", "env1", 1, speedstats.MinFileSize+1, 0)
		queueManagedWaitingSized(t, pool, tr, "unk", server.URL+"/u", "wan", "edge.test", "env1", 1, 0, 0)

		ctl.evaluate(time.Now())
		if !admissionAdmitted(pool, "holder") || !admissionAdmitted(pool, "exact") {
			t.Fatal("holder + exact-boundary candidate must both pass")
		}
		if admissionAdmitted(pool, "over") {
			t.Fatal("50MiB+1 total must respect the serial lock")
		}
		if !admissionAdmitted(pool, "unk") {
			t.Fatal("unknown-size candidate must not be record-locked")
		}

		// A small candidate still obeys the ordinary exploration cap: after
		// five commitments the sixth small on the same domain is refused.
		// env2 plus a seeded scope record isolates the domain cap — the
		// cold-scope guard counts the same bucket and would fire at five too.
		speedstats.AddRecordV2(50<<20, 1, 100<<20, false, 50, "capseed.test", "wan", "env2")
		for i, id := range []string{"cap1", "cap2", "cap3", "cap4", "cap5"} {
			queueManagedWaitingSized(t, pool, tr, id, server.URL+"/c"+strconv.Itoa(i), "wan", "cap.test", "env2", 1, 4<<20, 0)
		}
		queueManagedWaitingSized(t, pool, tr, "cap6", server.URL+"/c6", "wan", "cap.test", "env2", 1, 4<<20, 0)
		ctl.evaluate(time.Now())
		for i, id := range []string{"cap1", "cap2", "cap3", "cap4", "cap5"} {
			if !admissionAdmitted(pool, id) {
				t.Fatalf("cap%d not admitted", i+1)
			}
		}
		if admissionAdmitted(pool, "cap6") {
			t.Fatal("small candidate bypassed the domain commitment cap")
		}
	})

	t.Run("resume keeps original-total eligibility", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		queueManagedWaitingSized(t, pool, tr, "holdr", server.URL+"/hr", "wan", "resume.test", "env1", 1, recordLevelTotal, 0)
		// Original total 80 MiB, 60 MiB already checkpointed → 20 MiB remain.
		queueManagedWaitingSized(t, pool, tr, "resumed", server.URL+"/rs", "wan", "resume.test", "env1", 1, recordLevelTotal, 60<<20)

		ctl.evaluate(time.Now())
		if !admissionAdmitted(pool, "holdr") {
			t.Fatal("holder not admitted")
		}
		if admissionAdmitted(pool, "resumed") {
			t.Fatal("80MiB-total resume must be record-eligible even with 20MiB remaining")
		}
	})

	t.Run("pending reservations never self-lock", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		k1 := queueManagedWaitingSized(t, pool, tr, "p1", server.URL+"/p1", "wan", "pend.test", "env1", 1, recordLevelTotal, 0)
		k2 := queueManagedWaitingSized(t, pool, tr, "p2", server.URL+"/p2", "wan", "pend.test", "env1", 1, recordLevelTotal, 0)
		t0 := time.Now()
		seedPendingReservation(t, tr, "sg_p1", k1.Token, t0)
		seedPendingReservation(t, tr, "sg_p2", k2.Token, t0)

		ctl.evaluate(t0.Add(time.Second))
		if !admissionAdmitted(pool, "p1") {
			t.Fatal("own pending reservation self-locked the candidate")
		}
		if admissionAdmitted(pool, "p2") {
			t.Fatal("sibling must be blocked by p1's same-round commit, not by pending")
		}
		// p2's own pending reservation survives the round unchanged.
		if tk := admissionPendingTicket(t, tr, "sg_p2"); tk.Token != k2.Token {
			t.Fatal("blocked candidate's pending reservation was disturbed")
		}
	})

	t.Run("retained accept keeps its anchor and still locks", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		k1 := queueManagedWaitingSized(t, pool, tr, "r1", server.URL+"/r1", "wan", "retain.test", "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "r2", server.URL+"/r2", "wan", "retain.test", "env1", 1, recordLevelTotal, 0)
		t0 := time.Now()
		seedPendingReservation(t, tr, "sg_r1", k1.Token, t0)

		ctl.evaluate(t0.Add(10 * time.Second))
		if !admissionAdmitted(pool, "r1") {
			t.Fatal("retained ticket was not accepted")
		}
		if admissionAdmitted(pool, "r2") {
			t.Fatal("live retained measurement did not lock the sibling")
		}
		// The reservation anchor is the original one — acceptance never renews.
		if tt := mustTracked(t, tr, "sg_r1"); !tt.ActivatedAt.Equal(t0) {
			t.Fatalf("retained accept re-anchored: ActivatedAt=%v want %v", tt.ActivatedAt, t0)
		}
	})

	t.Run("expired retained accept fakes no new lock", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		k1 := queueManagedWaitingSized(t, pool, tr, "e1", server.URL+"/e1", "wan", "expired.test", "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "e2", server.URL+"/e2", "wan", "expired.test", "env1", 1, recordLevelTotal, 0)
		t0 := time.Now()
		seedPendingReservation(t, tr, "sg_e1", k1.Token, t0)

		// 46s later: e1's pending lease is dead; its retained accept must not
		// mint a fresh measurement lock, so e2 can still commit this round.
		ctl.evaluate(t0.Add(46 * time.Second))
		if !admissionAdmitted(pool, "e1") {
			t.Fatal("expired retained ticket must still be acceptable")
		}
		if !admissionAdmitted(pool, "e2") {
			t.Fatal("expired retained accept forged a new lock on the domain")
		}
		if tt := mustTracked(t, tr, "sg_e1"); !tt.ActivatedAt.Equal(t0) {
			t.Fatalf("retained accept re-anchored: ActivatedAt=%v want %v", tt.ActivatedAt, t0)
		}
	})

	t.Run("controller restart window resolves committed holders via fallback", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, engine, ctl1, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		queueManagedWaitingSized(t, pool, tr, "hold1", server.URL+"/h1", "wan", "boot.test", "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "fresh", server.URL+"/f", "wan", "boot.test", "env1", 1, recordLevelTotal, 0)
		ctl1.evaluate(time.Now())
		if !admissionAdmitted(pool, "hold1") {
			t.Fatal("holder not admitted")
		}

		// A fresh controller owns no in-memory entries: classification and the
		// measurement check must come from tracker + occupancy fallback.
		ctl2 := NewAdmissionController(engine)
		ctl2.evaluate(time.Now())
		if admissionAdmitted(pool, "fresh") {
			t.Fatal("committed holder escaped serialization after controller restart")
		}
	})

	t.Run("live aria2 occupancy holds the domain lock for surge candidates", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		// Record-level Aria2 row inside the confirmed-add placeholder window.
		tr.EnsureTrackedFromEvent("ar_hold", recordLevelTotal, server.URL+"/ab", 4, "")
		tr.MarkAdded("ar_hold")
		tr.SetScopeAndEnv("ar_hold", "wan", 0, "aria.test", "env1")
		tr.SetTargetBandwidth("ar_hold", 200<<10)
		// Small Aria2 row on a different domain never counts.
		tr.EnsureTrackedFromEvent("ar_sml", 10<<20, server.URL+"/as", 4, "")
		tr.MarkAdded("ar_sml")
		tr.SetScopeAndEnv("ar_sml", "wan", 0, "ariasmall.test", "env1")
		tr.SetTargetBandwidth("ar_sml", 200<<10)

		queueManagedWaitingSized(t, pool, tr, "newbig", server.URL+"/nb", "wan", "aria.test", "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "newsml", server.URL+"/ns", "wan", "aria.test", "env1", 1, 4<<20, 0)
		queueManagedWaitingSized(t, pool, tr, "newbig2", server.URL+"/nb2", "wan", "ariasmall.test", "env1", 1, recordLevelTotal, 0)

		ctl.evaluate(time.Now())
		if admissionAdmitted(pool, "newbig") {
			t.Fatal("record candidate admitted under a live aria2 measurement")
		}
		if !admissionAdmitted(pool, "newsml") {
			t.Fatal("small candidate must coexist with the aria2 measurement")
		}
		if !admissionAdmitted(pool, "newbig2") {
			t.Fatal("small aria2 row must not hold the domain lock")
		}
	})

	t.Run("paused holder frees the domain and the resumed opportunity re-gates", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		k1 := queueManagedWaitingSized(t, pool, tr, "h1", server.URL+"/h1", "wan", "pause.test", "env1", 1, recordLevelTotal, 0)
		ctl.evaluate(time.Now())
		if !admissionAdmitted(pool, "h1") {
			t.Fatal("holder not admitted")
		}

		// The pause fact clears the reservation lineage and drops the claim.
		tr.SetStatusFromEvent("sg_h1", "paused")
		if tk, ok := tr.PendingAdmissionTicket("sg_h1"); ok {
			t.Fatalf("paused row kept its ticket: %+v", tk)
		}
		// The scheduler claim ends with the pause.
		pool.Pause("h1")

		// A different big candidate takes the freed measurement.
		queueManagedWaitingSized(t, pool, tr, "h2", server.URL+"/h2", "wan", "pause.test", "env1", 1, recordLevelTotal, 0)
		ctl.evaluate(time.Now())
		if !admissionAdmitted(pool, "h2") {
			t.Fatal("paused holder still locked the domain")
		}

		// Resume is a brand-new opportunity: same row back to waiting plus a
		// fresh scheduler token — it must re-pass the gate and lose to h2's
		// live measurement instead of pre-empting it. A claimed download parks
		// in downloads until extracted; a queued-but-unclaimed entry is simply
		// gone — both resume through Add with a fresh token.
		tr.SetStatusFromEvent("sg_h1", "waiting")
		var resumed *types.DownloadRecord
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if r := pool.ExtractPausedConfig("h1"); r != nil {
				resumed = r
				break
			}
			if pool.GetStatus("h1") == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if resumed == nil {
			if pool.GetStatus("h1") != nil {
				t.Fatal("paused slot never became extractable")
			}
			resumed = &types.DownloadRecord{
				ID:            "h1",
				URL:           server.URL + "/h1",
				OutputPath:    t.TempDir(),
				Filename:      "h1-resume.bin",
				TotalSize:     recordLevelTotal,
				ProgressState: progress.New("h1", recordLevelTotal),
				Runtime:       &types.RuntimeConfig{Workers: 1},
			}
		}
		if !pool.Add(*resumed) {
			t.Fatal("resume re-add refused")
		}
		k2, ok := admissionCandidate(pool, "h1")
		if !ok || k2.Key.Token == k1.Token {
			t.Fatalf("resume did not mint a fresh opportunity: %+v vs %+v", k2.Key, k1)
		}
		ctl.evaluate(time.Now())
		if admissionAdmitted(pool, "h1") {
			t.Fatal("resumed opportunity bypassed the live measurement")
		}
		if tk, ok := tr.PendingAdmissionTicket("sg_h1"); ok {
			t.Fatalf("gated resume still minted a reservation: %+v", tk)
		}
	})

	t.Run("short-admitted record candidate still occupies the measurement", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)

		// Scope peak exactly at the efficiency floor: a foreign-domain
		// commitment saturates wan|env1 so only the short exception applies.
		speedstats.AddRecordV2(100<<10, 1, 100<<20, false, 50, "sat-seed.test", "wan", "env1")
		queueManagedWaitingSized(t, pool, tr, "sat", server.URL+"/sat", "wan", "sat.test", "env1", 1, recordLevelTotal, 0)
		// Total 80 MiB, remaining 1 MiB — qualifies for the short path but is
		// still a record-level measurement identity.
		shortKey := queueManagedWaitingSized(t, pool, tr, "shortr", server.URL+"/sh", "wan", "short.test", "env1", 1, recordLevelTotal, recordLevelTotal-(1<<20))
		// Same scope|domain, different env: an idle channel bucket with full
		// worker headroom — any refusal can only come from the serial lock.
		queueManagedWaitingSized(t, pool, tr, "follow", server.URL+"/fo", "wan", "short.test", "env2", 1, recordLevelTotal, 0)

		ctl.evaluate(time.Now())
		if !admissionAdmitted(pool, "shortr") {
			t.Fatal("short-exception candidate was not admitted")
		}
		if e := ctl.entries[shortKey]; e == nil || !e.shortUsed {
			t.Fatal("short admission did not latch the qualification")
		}
		if admissionAdmitted(pool, "follow") {
			t.Fatal("short-path record candidate failed to hold the domain lock")
		}
	})
}

// admissionSwapCache installs an empty task cache for the duration of a test
// so telemetry assertions only see the rows the test itself wrote.
func admissionSwapCache(t *testing.T) {
	t.Helper()
	orig := monitor.Cache
	monitor.Cache = monitor.NewTaskCacheForTest()
	t.Cleanup(func() { monitor.Cache = orig })
}

// admissionCandidate returns the current snapshot row for id.
func admissionCandidate(pool *scheduler.Scheduler, id string) (scheduler.AdmissionCandidate, bool) {
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key.ID == id {
			return c, true
		}
	}
	return scheduler.AdmissionCandidate{}, false
}

// TestAdmissionRuntime_ExplorationGraduatesAfterRecord covers the graduation
// boundary: while no domain+scope+env record exists the serial gate holds;
// once a real completion's record lands, further record-level candidates are
// no longer gated by the measurement lock (other headroom permitting). Other
// envs keep exploring, and a completion without a valid record does not
// graduate the domain.
func TestAdmissionRuntime_ExplorationGraduatesAfterRecord(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	tr := admissionSwapTracker(t)
	admissionTestConfigConns(t, true, "32", "32")
	pool, _, ctl, _ := admissionTestPool(t, 32)
	server := admissionBlockingServer(t, pool)

	const dom = "grad.test"
	queueManagedWaitingSized(t, pool, tr, "g1", server.URL+"/g1", "wan", dom, "env1", 1, recordLevelTotal, 0)
	queueManagedWaitingSized(t, pool, tr, "g2", server.URL+"/g2", "wan", dom, "env1", 1, recordLevelTotal, 0)
	ctl.evaluate(time.Now())
	if !admissionAdmitted(pool, "g1") {
		t.Fatal("first record candidate not admitted")
	}
	if admissionAdmitted(pool, "g2") {
		t.Fatal("serial gate absent before graduation")
	}
	if speedstats.HasDomainScopeEnvRecord(dom, "wan", "env1") {
		t.Fatal("domain has a record before any completion")
	}

	// Terminal accept plus the record the completion path would write:
	// peak/threadCount/total/exploration/ttfb/domain/scope/envKey. Peak and
	// threads are sized so the post-graduation rounds still see headroom.
	tr.MarkCompleteFromEvent("sg_g1", "complete")
	speedstats.AddRecordV2(64<<20, 32, recordLevelTotal, true, 50, dom, "wan", "env1")
	if !speedstats.HasDomainScopeEnvRecord(dom, "wan", "env1") {
		t.Fatal("completion record did not register")
	}

	queueManagedWaitingSized(t, pool, tr, "g3", server.URL+"/g3", "wan", dom, "env1", 1, recordLevelTotal, 0)
	ctl.evaluate(time.Now())
	if !admissionAdmitted(pool, "g2") || !admissionAdmitted(pool, "g3") {
		t.Fatal("graduated domain still serialized at the measurement gate")
	}

	// env2 never recorded — it still explores: a live same-domain measurement
	// locks it out, and once free it serializes against its own env's second
	// candidate.
	queueManagedWaitingSized(t, pool, tr, "e2a", server.URL+"/e2a", "wan", dom, "env2", 1, recordLevelTotal, 0)
	queueManagedWaitingSized(t, pool, tr, "e2b", server.URL+"/e2b", "wan", dom, "env2", 1, recordLevelTotal, 0)
	ctl.evaluate(time.Now())
	if admissionAdmitted(pool, "e2a") {
		t.Fatal("env2 exploration bypassed a live same-domain measurement")
	}
	tr.MarkCompleteFromEvent("sg_g2", "complete")
	tr.MarkCompleteFromEvent("sg_g3", "complete")
	ctl.evaluate(time.Now())
	if !admissionAdmitted(pool, "e2a") {
		t.Fatal("env2's first candidate must pass once no measurement is live")
	}
	if admissionAdmitted(pool, "e2b") {
		t.Fatal("env2 still explores: second candidate escaped the serial gate")
	}

	// Completion alone — no valid record — must not graduate a domain: the
	// next record-level pair on that domain still serializes.
	const dom2 = "nograd.test"
	queueManagedWaitingSized(t, pool, tr, "n1", server.URL+"/n1", "wan", dom2, "env1", 1, recordLevelTotal, 0)
	ctl.evaluate(time.Now())
	if !admissionAdmitted(pool, "n1") {
		t.Fatal("nograd first candidate not admitted")
	}
	tr.MarkCompleteFromEvent("sg_n1", "complete")
	if speedstats.HasDomainScopeEnvRecord(dom2, "wan", "env1") {
		t.Fatal("phantom record for an unrecorded completion")
	}
	queueManagedWaitingSized(t, pool, tr, "n2", server.URL+"/n2", "wan", dom2, "env1", 1, recordLevelTotal, 0)
	queueManagedWaitingSized(t, pool, tr, "n3", server.URL+"/n3", "wan", dom2, "env1", 1, recordLevelTotal, 0)
	ctl.evaluate(time.Now())
	if !admissionAdmitted(pool, "n2") {
		t.Fatal("next explorer blocked after its predecessor retired")
	}
	if admissionAdmitted(pool, "n3") {
		t.Fatal("unrecorded completion graduated the domain")
	}
}

// TestAdmissionRuntime_ExplorationHealthyAfterLease protects the post-lease
// path: once first progress is older than the progress lease, only real
// measured throughput (>=100KiB/s cache telemetry) keeps the measurement
// claim — not the lease itself. Terminal facts still release it.
func TestAdmissionRuntime_ExplorationHealthyAfterLease(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	tr := admissionSwapTracker(t)
	admissionSwapCache(t)
	admissionTestConfigConns(t, true, "32", "32")
	pool, _, ctl, _ := admissionTestPool(t, 32)
	server := admissionBlockingServer(t, pool)

	const dom = "healthy.test"
	queueManagedWaitingSized(t, pool, tr, "hold", server.URL+"/h", "wan", dom, "env1", 1, recordLevelTotal, 0)
	queueManagedWaitingSized(t, pool, tr, "wait", server.URL+"/w", "wan", dom, "env1", 1, recordLevelTotal, 0)
	t0 := time.Now()
	ctl.evaluate(t0)
	if !admissionAdmitted(pool, "hold") {
		t.Fatal("holder not admitted")
	}
	if admissionAdmitted(pool, "wait") {
		t.Fatal("waiter bypassed the initial measurement")
	}

	// Genuine start, first growth inside the window, then measured
	// throughput — the claim now rests on telemetry, not the lease.
	tr.ObserveActivation("sg_hold", 0, true)
	tr.SetStatusFromEvent("sg_hold", "active")
	tr.UpdateProgressFromEvent("sg_hold", recordLevelTotal, 1<<20)
	fp := mustTracked(t, tr, "sg_hold").FirstProgressAt
	if fp.IsZero() {
		t.Fatal("first growth did not stamp FirstProgressAt")
	}
	monitor.Cache.AddSgTask(rpc.Task{GID: "sg_hold", Status: "active", DownloadSpeed: strconv.Itoa(500 << 10)}, "active")

	// Well past the 15s progress lease the measurement persists while the
	// download is measurably healthy.
	for _, off := range []time.Duration{30 * time.Second, 60 * time.Second} {
		ctl.evaluate(fp.Add(off))
		if admissionAdmitted(pool, "wait") {
			t.Fatalf("healthy measurement released the domain lock at +%v", off)
		}
	}

	// Terminal facts zero the claim and unblock the domain.
	tr.MarkCompleteFromEvent("sg_hold", "complete")
	ctl.evaluate(fp.Add(61 * time.Second))
	if !admissionAdmitted(pool, "wait") {
		t.Fatal("terminal state did not release the measurement lock")
	}
}

// TestAdmissionRuntime_ExplorationExpiredMeasurementReleases covers the
// release edges of the measurement claim: lease expiry without growth, the
// 15s post-first-progress boundary, the exact 100KiB/s telemetry threshold,
// and lifecycle cleanups. Candidates use 1 worker so that a refusal can only
// mean the measurement lock — every other resource has headroom.
func TestAdmissionRuntime_ExplorationExpiredMeasurementReleases(t *testing.T) {
	setup := func(t *testing.T, dom string) (*scheduler.Scheduler, *monitor.TaskTracker, *AdmissionController) {
		t.Helper()
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tr := admissionSwapTracker(t)
		admissionTestConfigConns(t, true, "32", "32")
		pool, _, ctl, _ := admissionTestPool(t, 32)
		server := admissionBlockingServer(t, pool)
		queueManagedWaitingSized(t, pool, tr, "hold", server.URL+"/h", "wan", dom, "env1", 1, recordLevelTotal, 0)
		queueManagedWaitingSized(t, pool, tr, "wait", server.URL+"/w", "wan", dom, "env1", 1, recordLevelTotal, 0)
		return pool, tr, ctl
	}

	t.Run("no growth and no telemetry holds 44s and releases at 45s", func(t *testing.T) {
		pool, _, ctl := setup(t, "expire45.test")
		t0 := time.Now()
		ctl.evaluate(t0)
		if !admissionAdmitted(pool, "hold") {
			t.Fatal("holder not admitted")
		}
		if admissionAdmitted(pool, "wait") {
			t.Fatal("waiter bypassed the live reservation")
		}
		ctl.evaluate(t0.Add(44 * time.Second))
		if admissionAdmitted(pool, "wait") {
			t.Fatal("reservation lease released before the 45s boundary")
		}
		ctl.evaluate(t0.Add(45 * time.Second))
		if !admissionAdmitted(pool, "wait") {
			t.Fatal("expired reservation still held the domain lock at 45s")
		}
	})

	t.Run("stalled growth holds under 15s and releases at the boundary", func(t *testing.T) {
		pool, tr, ctl := setup(t, "stall15.test")
		ctl.evaluate(time.Now())
		if !admissionAdmitted(pool, "hold") {
			t.Fatal("holder not admitted")
		}
		tr.ObserveActivation("sg_hold", 0, true)
		tr.SetStatusFromEvent("sg_hold", "active")
		tr.UpdateProgressFromEvent("sg_hold", recordLevelTotal, 1<<20)
		fp := mustTracked(t, tr, "sg_hold").FirstProgressAt
		if fp.IsZero() {
			t.Fatal("first growth did not stamp FirstProgressAt")
		}
		// No cache telemetry: the claim rides on the lease alone.
		ctl.evaluate(fp.Add(14 * time.Second))
		if admissionAdmitted(pool, "wait") {
			t.Fatal("post-growth lease released before 15s")
		}
		ctl.evaluate(fp.Add(15 * time.Second))
		if !admissionAdmitted(pool, "wait") {
			t.Fatal("stalled measurement still locked the domain at the 15s boundary")
		}
	})

	t.Run("exact threshold telemetry holds and below releases", func(t *testing.T) {
		admissionSwapCache(t)
		pool, tr, ctl := setup(t, "thresh.test")
		ctl.evaluate(time.Now())
		if !admissionAdmitted(pool, "hold") {
			t.Fatal("holder not admitted")
		}
		tr.ObserveActivation("sg_hold", 0, true)
		tr.SetStatusFromEvent("sg_hold", "active")
		tr.UpdateProgressFromEvent("sg_hold", recordLevelTotal, 1<<20)
		fp := mustTracked(t, tr, "sg_hold").FirstProgressAt
		if fp.IsZero() {
			t.Fatal("first growth did not stamp FirstProgressAt")
		}
		// Exactly 100KiB/s: the lease is dead but real throughput holds.
		monitor.Cache.AddSgTask(rpc.Task{GID: "sg_hold", Status: "active", DownloadSpeed: strconv.Itoa(100 << 10)}, "active")
		ctl.evaluate(fp.Add(30 * time.Second))
		if admissionAdmitted(pool, "wait") {
			t.Fatal("threshold telemetry released the measurement claim")
		}
		// One byte below the floor: no measured claim remains.
		monitor.Cache.AddSgTask(rpc.Task{GID: "sg_hold", Status: "active", DownloadSpeed: strconv.Itoa((100 << 10) - 1)}, "active")
		ctl.evaluate(fp.Add(31 * time.Second))
		if !admissionAdmitted(pool, "wait") {
			t.Fatal("sub-threshold telemetry still held the domain lock")
		}
	})

	// Lifecycle facts — paused, terminal, and removal — all clear the claim.
	for _, tc := range []struct {
		name  string
		apply func(t *testing.T, tr *monitor.TaskTracker)
	}{
		{name: "paused", apply: func(t *testing.T, tr *monitor.TaskTracker) {
			tr.SetStatusFromEvent("sg_hold", "paused")
		}},
		{name: "complete", apply: func(t *testing.T, tr *monitor.TaskTracker) {
			tr.MarkCompleteFromEvent("sg_hold", "complete")
		}},
		{name: "error", apply: func(t *testing.T, tr *monitor.TaskTracker) {
			tr.MarkCompleteFromEvent("sg_hold", "error")
		}},
		{name: "cancel removes the row", apply: func(t *testing.T, tr *monitor.TaskTracker) {
			tr.RemoveTask("sg_hold")
		}},
	} {
		t.Run("lifecycle "+tc.name+" releases the claim", func(t *testing.T) {
			pool, tr, ctl := setup(t, "lc-"+tc.name+".test")
			ctl.evaluate(time.Now())
			if !admissionAdmitted(pool, "hold") {
				t.Fatal("holder not admitted")
			}
			if admissionAdmitted(pool, "wait") {
				t.Fatal("waiter bypassed the live measurement")
			}
			tc.apply(t, tr)
			if tc.name != "cancel removes the row" {
				if tk, ok := tr.PendingAdmissionTicket("sg_hold"); ok {
					t.Fatalf("stale ticket survived %s: %+v", tc.name, tk)
				}
			}
			ctl.evaluate(time.Now())
			if !admissionAdmitted(pool, "wait") {
				t.Fatalf("%s did not release the measurement lock", tc.name)
			}
		})
	}
}

// TestAdmissionRuntime_ExplorationRetryRespectsNMax exercises the real retry
// loop: a genuine download failure requeues the opportunity with a fresh
// token and an engine-realtime retryAt, the stale ticket is reconciled away,
// and the re-admission clamps its measurement width to the domain's N_max.
// No virtual now ever skips the engine clock — the test waits out the real
// retry delay.
func TestAdmissionRuntime_ExplorationRetryRespectsNMax(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	tr := admissionSwapTracker(t)
	admissionTestConfigConns(t, true, "32", "32")
	pool, _, ctl, ch := admissionTestPool(t, 4)

	// Always-503 without Retry-After is non-permanent: the run fails and the
	// worker requeues with retryAt ≈ now+1s instead of going terminal.
	fails := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(func() {
		pool.GracefulShutdown()
		fails.Close()
	})

	k1 := queueManagedWaitingSized(t, pool, tr, "retry", fails.URL+"/r", "wan", "retry.test", "env1", 4, recordLevelTotal, 0)
	ctl.evaluate(time.Now())
	if !admissionAdmitted(pool, "retry") {
		t.Fatal("candidate not admitted")
	}
	if tk := admissionPendingTicket(t, tr, "sg_retry"); tk.Token != k1.Token {
		t.Fatalf("reservation token mismatch: %+v want %d", tk, k1.Token)
	}

	// Real failure → EventQueued carrying a fresh opportunity.
	admissionWaitEvent(t, ch, types.EventQueued, "retry")
	rc, ok := admissionCandidate(pool, "retry")
	if !ok {
		t.Fatal("requeued candidate missing from snapshot")
	}
	if rc.Key.Token == 0 || rc.Key.Token == k1.Token {
		t.Fatalf("requeue did not mint a fresh token: %+v", rc.Key)
	}
	if rc.Admitted {
		t.Fatal("requeued candidate kept the old admitted flag")
	}
	if rc.RetryAt.IsZero() || !time.Now().Before(rc.RetryAt) {
		t.Fatalf("retry backoff not armed for the real clock: %v", rc.RetryAt)
	}

	// Reconcile while the backoff is still pending: the dead key's
	// reservation is released, and the unripe candidate is never evaluated.
	ctl.evaluate(time.Now())
	if tk, ok := tr.PendingAdmissionTicket("sg_retry"); ok {
		t.Fatalf("stale ticket survived the requeue: %+v", tk)
	}

	// Simulate the circuit-breaker product: N_max=1 for the domain, and the
	// failed attempt must have written no speed record.
	limits := smartthread.GetDefaultServerLimits()
	limits.SetNMax("wan|retry.test", 1)
	t.Cleanup(func() { limits.Clear("wan|retry.test") })
	if speedstats.HasDomainScopeEnvRecord("retry.test", "wan", "env1") {
		t.Fatal("a failed attempt wrote a speedstats record")
	}

	// Before the real retryAt the engine itself must refuse re-admission.
	ctl.evaluate(time.Now())
	if tk, ok := tr.PendingAdmissionTicket("sg_retry"); ok {
		t.Fatalf("admitted inside the retry window: %+v", tk)
	}
	time.Sleep(time.Until(rc.RetryAt) + 50*time.Millisecond)

	// A sibling explorer queued now must stay serialized behind the fresh
	// measurement, not ride in on the retry's wake.
	queueManagedWaitingSized(t, pool, tr, "peer", fails.URL+"/p", "wan", "retry.test", "env1", 4, recordLevelTotal, 0)
	ctl.evaluate(time.Now())

	tk := admissionPendingTicket(t, tr, "sg_retry")
	if tk.Token == k1.Token {
		t.Fatal("the stale token was resurrected")
	}
	if tt := mustTracked(t, tr, "sg_retry"); tt.ThreadCount > 1 {
		t.Fatalf("re-measurement exceeded N_max: threads=%d", tt.ThreadCount)
	}
	if admissionAdmitted(pool, "peer") {
		t.Fatal("sibling candidate bypassed the retried measurement")
	}

	// Releasing with the dead token must not cancel the live ticket.
	if tr.ReleaseAdmission("sg_retry", k1.Token) {
		t.Fatal("stale token released the live reservation")
	}
	if tk2 := admissionPendingTicket(t, tr, "sg_retry"); tk2 != tk {
		t.Fatalf("live ticket disturbed by stale release: %+v -> %+v", tk, tk2)
	}
}
