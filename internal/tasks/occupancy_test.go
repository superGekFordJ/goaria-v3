package tasks

import (
	"testing"

	"goaria-v3/internal/monitor"
	"goaria-v3/internal/smartthread"
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
	tr.EnsureTrackedFromEvent("sg_paused1", 100, "https://a.com/1", 4, "active")
	tr.SetScopeAndEnv("sg_paused1", "wan", 0, "a.com", "env1")
	tr.SetStatusFromEvent("sg_paused1", "paused")
	// Simulate Resume hook write before EventResumed.
	tr.SetTargetBandwidth("sg_paused1", 9_000_000)

	tr.EnsureTrackedFromEvent("sg_paused2", 100, "https://a.com/2", 4, "active")
	tr.SetScopeAndEnv("sg_paused2", "wan", 0, "a.com", "env1")
	tr.SetStatusFromEvent("sg_paused2", "paused")
	// Second resume still paused, no Target yet — must see first claim.
	monitor.State.SetTracker(tr)
	monitor.State.SetMonitor(nil)

	infos := BuildOccupancyTaskInfos()
	ledger := smartthread.NewBandwidthLedger(infos)
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 9_000_000 {
		t.Errorf("ReservedByDomain = %d, want 9000000 (resume-hold seed for ResumeBatch)", got)
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
	// First batch waiter holds claim while still queued.
	tr.EnsureTrackedFromEvent("sg_wait", 0, "https://a.com/1", 9, "waiting")
	tr.SetScopeAndEnv("sg_wait", "wan", 0, "a.com", "env1")
	tr.SetThreadInfo("sg_wait", 9, false)
	tr.SetTargetBandwidth("sg_wait", 7_000_000)
	monitor.State.SetTracker(tr)
	monitor.State.SetMonitor(nil)

	infos := BuildOccupancyTaskInfos()
	ledger := smartthread.NewBandwidthLedger(infos)
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 7_000_000 {
		t.Errorf("ReservedByDomain = %d, want 7000000 (waiting claim visible to later AddUri)", got)
	}
	if ExistingDomainWorkersFromTelemetry("wan", "a.com") != 9 {
		t.Errorf("ExistingDomainWorkers = %d, want 9 from waiting ThreadCount",
			ExistingDomainWorkersFromTelemetry("wan", "a.com"))
	}
}
