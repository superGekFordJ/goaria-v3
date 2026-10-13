package tasks

import (
	"net/http"
	"testing"
	"time"

	"goaria-v3/internal/monitor"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func TestExistingDomainWorkersFromTelemetry_ThreadCountFallback(t *testing.T) {
	origTr := monitor.State.GetTracker()
	origMon := monitor.State.GetMonitor()
	t.Cleanup(func() {
		monitor.State.SetTracker(origTr)
		monitor.State.SetMonitor(origMon)
	})

	tr := monitor.NewTaskTracker()
	tr.EnsureTrackedFromEvent("sg_cold", 100, "https://a.com/f", 7, "active")
	tr.SetThreadInfo("sg_cold", 7, false)
	tr.SetScopeAndEnv("sg_cold", "wan", 0, "a.com", "env1")
	tr.SetTargetBandwidth("sg_cold", 1_000_000)
	monitor.State.SetTracker(tr)
	monitor.State.SetMonitor(nil)

	if got := ExistingDomainWorkersFromTelemetry("wan", "a.com"); got != 7 {
		t.Errorf("ThreadCount fallback = %d, want 7", got)
	}
	if got := ExistingDomainWorkersFromTelemetry("", "a.com"); got != 0 {
		t.Errorf("empty scope = %d, want 0", got)
	}
	if got := ExistingDomainWorkersFromTelemetry("wan", ""); got != 0 {
		t.Errorf("empty domain = %d, want 0", got)
	}
	if got := ExistingDomainWorkersFromTelemetry("wan", "other.com"); got != 0 {
		t.Errorf("other domain = %d, want 0", got)
	}
}

func TestExistingDomainWorkersFromTelemetry_MaxSnapshotsAndThreadCount(t *testing.T) {
	origTr := monitor.State.GetTracker()
	origMon := monitor.State.GetMonitor()
	t.Cleanup(func() {
		monitor.State.SetTracker(origTr)
		monitor.State.SetMonitor(origMon)
	})

	tr := monitor.NewTaskTracker()
	tr.EnsureTrackedFromEvent("sg_a", 100, "https://a.com/f", 7, "active")
	tr.SetThreadInfo("sg_a", 7, false)
	tr.SetScopeAndEnv("sg_a", "wan", 0, "a.com", "env1")
	tr.SetTargetBandwidth("sg_a", 1_000_000)
	monitor.State.SetTracker(tr)

	tc := monitor.NewTelemetryCache()
	mon := monitor.NewMonitorWithTelemetryForTest(tc)
	monitor.State.SetMonitor(mon)

	if got := ExistingDomainWorkersFromTelemetry("wan", "a.com"); got != 7 {
		t.Errorf("empty snapshots = %d, want 7", got)
	}

	snaps := make([]types.WorkerSnapshot, 10)
	tc.Set("sg_a", snaps)
	if got := ExistingDomainWorkersFromTelemetry("wan", "a.com"); got != 10 {
		t.Errorf("snapshots count = %d, want 10", got)
	}

	tc.Set("sg_a", make([]types.WorkerSnapshot, 3))
	if got := ExistingDomainWorkersFromTelemetry("wan", "a.com"); got != 7 {
		t.Errorf("max(3, ThreadCount=7) = %d, want 7", got)
	}
}

func TestBuildOccupancyTaskInfos_SeedsLedger(t *testing.T) {
	origTr := monitor.State.GetTracker()
	origMon := monitor.State.GetMonitor()
	t.Cleanup(func() {
		monitor.State.SetTracker(origTr)
		monitor.State.SetMonitor(origMon)
	})

	tr := monitor.NewTaskTracker()
	tr.EnsureTrackedFromEvent("sg_ph", 100, "https://a.com/f", 4, "active")
	tr.ObserveActivation("sg_ph", 0, false)
	tr.SetThreadInfo("sg_ph", 4, false)
	tr.SetScopeAndEnv("sg_ph", "wan", 50, "a.com", "env1")
	tr.SetTargetBandwidth("sg_ph", 3_000_000)
	monitor.State.SetTracker(tr)
	monitor.State.SetMonitor(nil)

	infos := BuildOccupancyTaskInfos()
	if len(infos) != 1 {
		t.Fatalf("len(infos) = %d, want 1", len(infos))
	}
	if infos[0].TargetBandwidth != 3_000_000 {
		t.Errorf("TargetBandwidth = %d, want 3000000", infos[0].TargetBandwidth)
	}
	if infos[0].Domain != "a.com" || infos[0].ThreadCount != 4 {
		t.Errorf("Domain=%q ThreadCount=%d, want a.com/4", infos[0].Domain, infos[0].ThreadCount)
	}

	ledger := smartthread.NewBandwidthLedger(infos)
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 3_000_000 {
		t.Errorf("ReservedByDomain = %d, want 3000000 (cold hybrid seed)", got)
	}
}

func TestBuildOccupancyTaskInfos_ResumeHoldSeedsDomainReserved(t *testing.T) {
	origTr := monitor.State.GetTracker()
	origMon := monitor.State.GetMonitor()
	t.Cleanup(func() {
		monitor.State.SetTracker(origTr)
		monitor.State.SetMonitor(origMon)
	})

	tr := monitor.NewTaskTracker()
	// A paused row holding only stale claim parameters contributes nothing;
	// resume claims now live exclusively on explicit admission reservations.
	tr.EnsureTrackedFromEvent("sg_paused1", 100, "https://a.com/1", 4, "active")
	tr.SetScopeAndEnv("sg_paused1", "wan", 0, "a.com", "env1")
	tr.SetStatusFromEvent("sg_paused1", "paused")
	tr.SetTargetBandwidth("sg_paused1", 9_000_000)

	// The first resumed candidate sits waiting with a live reservation; a
	// second batch member must see its floor in the domain seed.
	tr.EnsureTrackedFromEvent("sg_wait1", 100, "https://a.com/2", 4, "waiting")
	tr.SetScopeAndEnv("sg_wait1", "wan", 0, "a.com", "env1")
	if _, created := tr.ReserveAdmission("sg_wait1", 1,
		smartthread.ThreadParams{Split: 4, MinSize: 1 << 20, TargetBandwidth: 9_000_000},
		time.Now(), 0, false); !created {
		t.Fatal("reservation for waiting resume candidate refused")
	}
	monitor.State.SetTracker(tr)
	monitor.State.SetMonitor(nil)

	infos := BuildOccupancyTaskInfos()
	ledger := smartthread.NewBandwidthLedger(infos)
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 9_000_000 {
		t.Errorf("ReservedByDomain = %d, want 9000000 (pending reservation seed)", got)
	}
}

func TestBuildOccupancyTaskInfos_WaitingClaimSeedsDomainReserved(t *testing.T) {
	origTr := monitor.State.GetTracker()
	origMon := monitor.State.GetMonitor()
	t.Cleanup(func() {
		monitor.State.SetTracker(origTr)
		monitor.State.SetMonitor(origMon)
	})

	tr := monitor.NewTaskTracker()
	// Ordinary waiting rows hold no claim: params alone contribute nothing.
	tr.EnsureTrackedFromEvent("sg_plain", 0, "https://a.com/0", 9, "waiting")
	tr.SetScopeAndEnv("sg_plain", "wan", 0, "a.com", "env1")
	tr.SetThreadInfo("sg_plain", 9, false)
	tr.SetTargetBandwidth("sg_plain", 3_000_000)
	// The queued candidate only counts once the controller writes an
	// explicit admitted-not-started reservation for it.
	tr.EnsureTrackedFromEvent("sg_wait", 0, "https://a.com/1", 9, "waiting")
	tr.SetScopeAndEnv("sg_wait", "wan", 0, "a.com", "env1")
	if _, created := tr.ReserveAdmission("sg_wait", 1,
		smartthread.ThreadParams{Split: 9, MinSize: 1 << 20, TargetBandwidth: 7_000_000},
		time.Now(), 0, false); !created {
		t.Fatal("reservation for waiting candidate refused")
	}
	monitor.State.SetTracker(tr)
	monitor.State.SetMonitor(nil)

	infos := BuildOccupancyTaskInfos()
	ledger := smartthread.NewBandwidthLedger(infos)
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 7_000_000 {
		t.Errorf("ReservedByDomain = %d, want 7000000 (pending reservation visible to later AddUri)", got)
	}
	if got := ExistingDomainWorkersFromTelemetry("wan", "a.com"); got != 9 {
		t.Errorf("ExistingDomainWorkers = %d, want 9 from reserved ThreadCount", got)
	}
}

// ==================== S4 admission primitives: real join ====================
//
// These tests compose the real scheduler pool, tracker, occupancy collector,
// ledger and gate exactly as a future controller would consume them — scalar
// snapshot out, gate evaluate, tracker reservation, scheduler Admit. They are
// deliberately not wired into any production path.

// admissionSwapTracker installs an isolated tracker on the global monitor state
// (the collector's only read path) and restores the previous state on cleanup.
func admissionSwapTracker(t *testing.T) *monitor.TaskTracker {
	t.Helper()
	origTr := monitor.State.GetTracker()
	origMon := monitor.State.GetMonitor()
	t.Cleanup(func() {
		monitor.State.SetTracker(origTr)
		monitor.State.SetMonitor(origMon)
	})
	tr := monitor.NewTaskTracker()
	monitor.State.SetTracker(tr)
	monitor.State.SetMonitor(nil)
	return tr
}

// admissionOccupancy finds gid in the real collector output or fails.
func admissionOccupancy(t *testing.T, gid string) smartthread.TrackedTaskInfo {
	t.Helper()
	for _, info := range BuildOccupancyTaskInfos() {
		if info.GID == gid {
			return info
		}
	}
	t.Fatalf("BuildOccupancyTaskInfos has no entry for %s", gid)
	return smartthread.TrackedTaskInfo{}
}

// admissionWaitEvent drains ch until a matching event arrives.
func admissionWaitEvent(t *testing.T, ch chan types.DownloadEvent, typ types.EventType, id string) types.DownloadEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == typ && ev.DownloadID == id {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %v event for %s", typ, id)
		}
	}
}

// gateFixtureFor builds the gate input for candidate id from a live snapshot,
// a real ledger seeded by the collector, and synthesized speedstats history.
func admissionGateParams(pool *scheduler.Scheduler, key scheduler.AdmissionKey) smartthread.AdmissionParams {
	snap := pool.AdmissionSnapshot()
	headroom := snap.Capacity - snap.CommittedSlots
	return smartthread.AdmissionParams{
		Calc: smartthread.CalcParams{
			FileSize:       32 << 10,
			MaxConnections: 16,
			Scope:          "wan",
			Domain:         "local.test",
			EnvKey:         "env1",
			Ledger:         smartthread.NewBandwidthLedger(BuildOccupancyTaskInfos()),
			ActiveMACsFunc: func() []string { return nil },
			ComputeEnvKeyFunc: func(route, mac string) string {
				return route + "|" + mac
			},
			MinThreadLife: 5,
		},
		Upper:                smartthread.ThreadParams{Split: 4, MinSize: 1 << 20},
		Smart:                true,
		SizeKnown:            true,
		SlotHeadroom:         headroom,
		ScopeEnvCommitted:    0,
		DomainCommitted:      0,
		DomainWorkerHeadroom: 16,
	}
}

// Full join: a managed pool holds the queued task until an accepted Admit;
// the tracker reservation becomes visible to the next ledger seed; a real
// EventStarted consumes the pending reservation exactly once.
func TestAdmissionPrimitives_JoinStartsDownload(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	tr := admissionSwapTracker(t)

	const body = 32 << 10
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "32768")
		_, _ = w.Write(make([]byte, body))
	}))
	defer server.Close()

	ch := make(chan types.DownloadEvent, 32)
	pool, err := scheduler.NewManaged(ch, 1)
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	t.Cleanup(pool.GracefulShutdown)

	const id, gid = "join1", "sg_join1"
	tr.EnsureTrackedFromEvent(gid, body, server.URL, 4, "waiting")
	tr.SetScopeAndEnv(gid, "wan", 0, "local.test", "env1")

	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		OutputPath:    t.TempDir(),
		Filename:      "join.bin",
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 4},
	}) {
		t.Fatal("managed Add refused")
	}

	// Without Admit the managed worker must not self-pick the task.
	select {
	case ev := <-ch:
		t.Fatalf("dispatched before Admit: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}

	var cand scheduler.AdmissionCandidate
	for _, c := range pool.AdmissionSnapshot().Candidates {
		if c.Key.ID == id {
			cand = c
		}
	}
	if cand.Key.Token == 0 || cand.Admitted {
		t.Fatalf("candidate = %+v", cand)
	}

	// Seed enough history for a clean smart-mode allow (domain p75 chain).
	speedstats.AddRecordV2(16<<20, 4, 100<<20, false, 50, "local.test", "wan", "env1")
	params := admissionGateParams(pool, cand.Key)
	decision := smartthread.EvaluateAdmission(params)
	if !decision.Allowed || decision.Blocked != 0 {
		t.Fatalf("gate decision = %+v", decision)
	}

	ticket, created := tr.ReserveAdmission(gid, cand.Key.Token, decision.Params, time.Now(), 0, false)
	if !created || ticket != (monitor.AdmissionTicket{GID: gid, Token: cand.Key.Token}) {
		t.Fatalf("ReserveAdmission = %+v created=%v", ticket, created)
	}

	// The reservation is visible to the next ledger seed via the collector:
	// a waiting row with a live lease claims its Target floor.
	reserved := admissionOccupancy(t, gid)
	if !reserved.AdmissionReserved {
		t.Fatalf("occupancy info lost AdmissionReserved: %+v", reserved)
	}
	ledger := smartthread.NewBandwidthLedger(BuildOccupancyTaskInfos())
	if got := ledger.Reserved("wan", "env1"); got != decision.Params.TargetBandwidth {
		t.Fatalf("ledger reserved = %d, want target %d", got, decision.Params.TargetBandwidth)
	}

	if res := pool.Admit(cand.Key, decision.Params.Split, decision.Params.MinSize); res != scheduler.AdmitAccepted {
		t.Fatalf("Admit = %v, want Accepted", res)
	}
	// A duplicate Admit must not re-account; a duplicate reservation must not
	// double-seed or renew the anchor.
	if res := pool.Admit(cand.Key, decision.Params.Split, decision.Params.MinSize); res != scheduler.AdmitAlreadyAdmitted {
		t.Fatalf("repeat Admit = %v, want AlreadyAdmitted", res)
	}
	before := mustTracked(t, tr, gid)
	if _, again := tr.ReserveAdmission(gid, cand.Key.Token, decision.Params, time.Now(), 0, false); again {
		t.Fatal("duplicate ReserveAdmission reported created")
	}
	after := mustTracked(t, tr, gid)
	if !after.ActivatedAt.Equal(before.ActivatedAt) {
		t.Fatal("duplicate reservation renewed the lease anchor")
	}
	if pool.AdmissionSnapshot().CommittedSlots != 1 {
		t.Fatal("CommittedSlots drifted after duplicate Admit")
	}

	// The accepted opportunity produces a real start; mirror the monitor's
	// EventStarted → tracker wiring (Ensure + status + ObserveActivation).
	ev := admissionWaitEvent(t, ch, types.EventStarted, id)
	tr.EnsureTrackedFromEvent(gid, ev.Total, ev.URL, ev.Workers, "active")
	tr.SetStatusFromEvent(gid, "active")
	tr.ObserveActivation(gid, ev.Downloaded, ev.Downloaded > 0)

	// A started task's pending reservation was consumed: the same ticket can
	// no longer roll it back, and occupancy keeps the activated row.
	if tr.ReleaseAdmission(gid, cand.Key.Token) {
		t.Fatal("started task released by its own ticket")
	}
}

func mustTracked(t *testing.T, tr *monitor.TaskTracker, gid string) monitor.TrackedTask {
	t.Helper()
	tt, ok := tr.GetTrackedTask(gid)
	if !ok {
		t.Fatalf("tracker lost %s", gid)
	}
	return tt
}

// Stale lifecycle: a cancelled candidate's key goes stale on the pool, and a
// rolled-back token can neither re-reserve nor revoke a newer reservation.
func TestAdmissionPrimitives_StaleTokenAndConditionalRelease(t *testing.T) {
	tr := admissionSwapTracker(t)

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	ch := make(chan types.DownloadEvent, 16)
	pool, err := scheduler.NewManaged(ch, 1)
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	t.Cleanup(pool.GracefulShutdown)

	const id, gid = "st1", "sg_st1"
	tr.EnsureTrackedFromEvent(gid, 1, server.URL, 4, "waiting")
	tr.SetScopeAndEnv(gid, "wan", 0, "local.test", "env1")

	add := func() scheduler.AdmissionKey {
		if !pool.Add(types.DownloadRecord{
			ID:            id,
			URL:           server.URL,
			OutputPath:    t.TempDir(),
			Filename:      "st.bin",
			ProgressState: progress.New(id, 0),
			Runtime:       &types.RuntimeConfig{Workers: 2},
		}) {
			t.Fatal("Add refused")
		}
		for _, c := range pool.AdmissionSnapshot().Candidates {
			if c.Key.ID == id {
				return c.Key
			}
		}
		t.Fatal("snapshot missing candidate")
		return scheduler.AdmissionKey{}
	}
	upper := smartthread.ThreadParams{Split: 2, MinSize: 1 << 20, TargetBandwidth: 1 << 20}

	key1 := add()
	if _, created := tr.ReserveAdmission(gid, key1.Token, upper, time.Now(), 0, false); !created {
		t.Fatal("first reservation refused")
	}

	// Cancelling the queued opportunity retires its token on both halves.
	pool.Cancel(id)
	if res := pool.Admit(key1, 2, 0); res != scheduler.AdmitStale {
		t.Fatalf("Admit after Cancel = %v, want Stale", res)
	}
	if !tr.ReleaseAdmission(gid, key1.Token) {
		t.Fatal("release of a fresh matching reservation refused")
	}
	if tr.ReleaseAdmission(gid, key1.Token) {
		t.Fatal("second release rolled back twice")
	}
	// A rolled-back token stays below the high-water mark: it cannot
	// re-reserve, even while the row is still waiting.
	if _, created := tr.ReserveAdmission(gid, key1.Token, upper, time.Now(), 0, false); created {
		t.Fatal("rolled-back token re-reserved")
	}

	// A same-ID re-add mints a strictly newer token; the stale ticket's
	// cleanup can never revoke the new reservation.
	key2 := add()
	if key2.Token <= key1.Token {
		t.Fatalf("re-add token %d not greater than %d", key2.Token, key1.Token)
	}
	if _, created := tr.ReserveAdmission(gid, key2.Token, upper, time.Now(), 0, false); !created {
		t.Fatal("reservation for replacement refused")
	}
	if tr.ReleaseAdmission(gid, key1.Token) {
		t.Fatal("stale ticket revoked the replacement reservation")
	}
	// The replacement reservation still holds its Target floor for the
	// collector until its own ticket releases it.
	if got := smartthread.TaskOccupancy(admissionOccupancy(t, gid), time.Now()); got != upper.TargetBandwidth {
		t.Fatalf("replacement reservation occupancy = %d, want %d", got, upper.TargetBandwidth)
	}
	if !tr.ReleaseAdmission(gid, key2.Token) {
		t.Fatal("release of the replacement reservation refused")
	}
	if tr.ReleaseAdmission(gid, key2.Token) {
		t.Fatal("repeat release of replacement rolled back twice")
	}
	for _, info := range BuildOccupancyTaskInfos() {
		if info.GID == gid && info.AdmissionReserved {
			t.Fatal("released reservation still reports AdmissionReserved")
		}
	}

	// The stale key stays stale for the pool too.
	if res := pool.Admit(key1, 2, 0); res != scheduler.AdmitStale {
		t.Fatalf("stale key Admit = %v, want Stale", res)
	}

	// Pausing the still-queued replacement retires its token as well: the
	// cancelled/paused key can never admit a later opportunity.
	paused := pool.Pause(id)
	if !paused.Found {
		t.Fatalf("Pause on queued candidate = %+v", paused)
	}
	if res := pool.Admit(key2, 2, 0); res != scheduler.AdmitStale {
		t.Fatalf("post-pause Admit = %v, want Stale", res)
	}
}

// Aria2 tasks feed occupancy/domain inputs but can never enter the Surge
// admission path: no scheduler candidate, no Admit, no sg_ reservation.
func TestAdmissionPrimitives_Aria2StaysOutside(t *testing.T) {
	tr := admissionSwapTracker(t)

	ch := make(chan types.DownloadEvent, 8)
	pool, err := scheduler.NewManaged(ch, 1)
	if err != nil {
		t.Fatalf("NewManaged: %v", err)
	}
	t.Cleanup(pool.GracefulShutdown)

	// A confirmed-add aria2 task inside its placeholder window contributes
	// occupancy to the shared ledger like any S1 input.
	tr.EnsureTrackedFromEvent("a_1", 100, "http://local.test/big", 4, "")
	tr.MarkAdded("a_1")
	tr.SetScopeAndEnv("a_1", "wan", 0, "local.test", "env1")
	tr.SetTargetBandwidth("a_1", 5_000_000)
	// An authoritative aria2 waiting row contributes nothing.
	tr.EnsureTrackedFromEvent("a_2", 100, "http://local.test/wait", 4, "waiting")

	monitor.State.SetTracker(tr)
	infos := BuildOccupancyTaskInfos()
	var sawA1, sawA2 bool
	for _, info := range infos {
		switch info.GID {
		case "a_1":
			sawA1 = true
			if got := smartthread.TaskOccupancy(info, time.Now()); got != 5_000_000 {
				t.Fatalf("aria2 placeholder occupancy = %d, want 5000000", got)
			}
		case "a_2":
			sawA2 = true
		}
	}
	if !sawA1 {
		t.Fatal("aria2 active task missing from occupancy collector")
	}
	if sawA2 {
		t.Fatal("authoritative aria2 waiting entered the occupancy set")
	}
	if got := smartthread.NewBandwidthLedger(infos).ReservedByDomain("wan", "local.test"); got != 5_000_000 {
		t.Fatalf("domain reserved = %d, want 5000000", got)
	}

	// The pool knows nothing about aria2 IDs: no candidate, Admit is stale.
	if snap := pool.AdmissionSnapshot(); len(snap.Candidates) != 0 || snap.CommittedSlots != 0 {
		t.Fatalf("pool snapshot = %+v", snap)
	}
	if res := pool.Admit(scheduler.AdmissionKey{ID: "a_1", Token: 1}, 4, 1<<20); res != scheduler.AdmitStale {
		t.Fatalf("aria2 Admit = %v, want Stale", res)
	}
	// Non-sg GIDs cannot hold an admission reservation at all.
	if _, created := tr.ReserveAdmission("a_1", 7, smartthread.ThreadParams{Split: 1}, time.Now(), 0, false); created {
		t.Fatal("aria2 reservation was created")
	}
}
