package tasks

import (
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

func withPickupTightenFixture(t *testing.T, smartOn bool) *monitor.TaskTracker {
	t.Helper()
	origCfg := config.Get()
	cfgCopy := *origCfg
	cfgCopy.SmartThreadMode = smartOn
	cfgCopy.MaxConnections = "16"
	config.SetTestConfig(&cfgCopy)
	t.Cleanup(func() { config.SetTestConfig(origCfg) })

	origTr := monitor.State.GetTracker()
	origMon := monitor.State.GetMonitor()
	tr := monitor.NewTaskTracker()
	monitor.State.SetTracker(tr)
	monitor.State.SetMonitor(nil)
	t.Cleanup(func() {
		monitor.State.SetTracker(origTr)
		monitor.State.SetMonitor(origMon)
	})
	return tr
}

func TestApplyPickupTighten_CongestedDomainClampsDown(t *testing.T) {
	tr := withPickupTightenFixture(t, true)
	limits := smartthread.GetDefaultServerLimits()
	limits.SetNMax("wan|a.com", 8)
	t.Cleanup(func() { limits.Clear("wan|a.com") })

	// Peers already hold 7 workers near N_max → Clamp floors new Split to 1.
	tr.EnsureTrackedFromEvent("sg_peer", 100, "https://a.com/p", 7, "active")
	tr.SetScopeAndEnv("sg_peer", "wan", 0, "a.com", "env1")
	tr.SetThreadInfo("sg_peer", 7, false)
	tr.SetTargetBandwidth("sg_peer", 10_000_000)

	tr.EnsureTrackedFromEvent("sg_promo", 100_000_000, "https://a.com/f", 9, "waiting")
	tr.SetScopeAndEnv("sg_promo", "wan", 0, "a.com", "env1")
	tr.SetThreadInfo("sg_promo", 9, false)
	tr.SetTargetBandwidth("sg_promo", 8_000_000)

	cfg := &types.DownloadRecord{
		ID:        "promo",
		TotalSize: 100_000_000,
		Runtime:   &types.RuntimeConfig{Workers: 9, MinChunkSize: 4 * 1024 * 1024},
	}
	ApplyPickupTighten(cfg)

	if cfg.Runtime.Workers != 1 {
		t.Fatalf("Workers = %d, want 1 (N_max fuse clamp)", cfg.Runtime.Workers)
	}
	tt := tr.GetOccupancyTrackedTasks()
	found := false
	for _, o := range tt {
		if o.GID == "sg_promo" {
			found = true
			if o.ThreadCount != 1 {
				t.Errorf("tracker ThreadCount = %d, want 1", o.ThreadCount)
			}
			// Shrink path always calls SetTargetBandwidth; N_max fuse yields a
			// scaled-down claim, not the pre-promote 8MB waiter bake.
			if o.TargetBandwidth <= 0 {
				t.Error("SetTargetBandwidth must refresh claim after shrink")
			}
			if o.TargetBandwidth == 8_000_000 {
				t.Error("TargetBandwidth still 8MB — expected recalculated/scaled claim")
			}
		}
	}
	if !found {
		t.Error("sg_promo missing from occupancy after tighten")
	}
}

func TestApplyPickupTighten_FreeDomainNoGrow(t *testing.T) {
	tr := withPickupTightenFixture(t, true)
	// Clear any N_max so Clamp is a no-op; Calculate on large file may suggest
	// high Split — apply gate must refuse growth.
	tr.EnsureTrackedFromEvent("sg_free", 100_000_000, "https://free.example/f", 2, "waiting")
	tr.SetScopeAndEnv("sg_free", "wan", 0, "free.example", "env1")
	tr.SetThreadInfo("sg_free", 2, false)
	tr.SetTargetBandwidth("sg_free", 1_000_000)

	cfg := &types.DownloadRecord{
		ID:        "free",
		TotalSize: 100_000_000,
		Runtime:   &types.RuntimeConfig{Workers: 2, MinChunkSize: 1024 * 1024},
	}
	beforeMin := cfg.Runtime.MinChunkSize
	ApplyPickupTighten(cfg)

	if cfg.Runtime.Workers != 2 {
		t.Fatalf("Workers = %d, want 2 (no growth)", cfg.Runtime.Workers)
	}
	if cfg.Runtime.MinChunkSize != beforeMin {
		t.Fatalf("MinChunkSize changed to %d (must never raise)", cfg.Runtime.MinChunkSize)
	}
}

func TestApplyPickupTighten_SelfExclusion(t *testing.T) {
	tr := withPickupTightenFixture(t, true)
	limits := smartthread.GetDefaultServerLimits()
	limits.SetNMax("wan|a.com", 9)
	t.Cleanup(func() { limits.Clear("wan|a.com") })

	// Alone with ThreadCount=9 and N_max=9: without self-exclude, existing=9
	// → Clamp to 1. With self-exclude, existing=0 → N_max fuse must not fire.
	tr.EnsureTrackedFromEvent("sg_solo", 100_000_000, "https://a.com/f", 9, "waiting")
	tr.SetScopeAndEnv("sg_solo", "wan", 0, "a.com", "env1")
	tr.SetThreadInfo("sg_solo", 9, false)
	tr.SetTargetBandwidth("sg_solo", 8_000_000)

	if got := ExistingDomainWorkersFromTelemetry("wan", "a.com"); got != 9 {
		t.Fatalf("ExistingDomainWorkers (with self) = %d, want 9", got)
	}
	if got := ExistingDomainWorkersFromTelemetryExcluding("wan", "a.com", "sg_solo"); got != 0 {
		t.Fatalf("ExistingDomainWorkers excluding self = %d, want 0", got)
	}

	cfg := &types.DownloadRecord{
		ID:        "solo",
		TotalSize: 100_000_000,
		Runtime:   &types.RuntimeConfig{Workers: 9, MinChunkSize: 1024 * 1024},
	}
	ApplyPickupTighten(cfg)

	// Without self-exclude, existing=9 + nMax=9 → Clamp fuses to 1.
	// With self-exclude, N_max fuse must not fire; Calculate may still
	// tighten for file-size reasons (allowed), but must not force 1 via self.
	if cfg.Runtime.Workers == 1 {
		t.Fatalf("Workers fused to 1 — promoting GID was double-counted as existing")
	}
	if cfg.Runtime.Workers > 9 {
		t.Fatalf("Workers = %d grew above 9", cfg.Runtime.Workers)
	}
}

func TestApplyPickupTighten_ProgressTotalWhenCfgTotalUnknown(t *testing.T) {
	tr := withPickupTightenFixture(t, true)
	limits := smartthread.GetDefaultServerLimits()
	limits.SetNMax("wan|prog.com", 4)
	t.Cleanup(func() { limits.Clear("wan|prog.com") })

	tr.EnsureTrackedFromEvent("sg_peer", 50, "https://prog.com/p", 4, "active")
	tr.SetScopeAndEnv("sg_peer", "wan", 0, "prog.com", "env1")
	tr.SetThreadInfo("sg_peer", 4, false)
	tr.SetTargetBandwidth("sg_peer", 3_000_000)

	tr.EnsureTrackedFromEvent("sg_p", 0, "https://prog.com/f", 6, "waiting")
	tr.SetScopeAndEnv("sg_p", "wan", 0, "prog.com", "env1")
	tr.SetThreadInfo("sg_p", 6, false)
	tr.SetTargetBandwidth("sg_p", 2_000_000)

	ps := progress.New("p", 40_000_000)
	cfg := &types.DownloadRecord{
		ID:            "p",
		TotalSize:     0, // unknown on record; progress knows size
		ProgressState: ps,
		Runtime:       &types.RuntimeConfig{Workers: 6, MinChunkSize: 1024 * 1024},
	}
	ApplyPickupTighten(cfg)
	if cfg.Runtime.Workers != 1 {
		t.Fatalf("Workers = %d, want 1 (progress total must unlock clamp)", cfg.Runtime.Workers)
	}
}

func TestApplyPickupTighten_NMaxFuseToOne(t *testing.T) {
	tr := withPickupTightenFixture(t, true)
	limits := smartthread.GetDefaultServerLimits()
	limits.SetNMax("wan|fused.com", 5)
	t.Cleanup(func() { limits.Clear("wan|fused.com") })

	tr.EnsureTrackedFromEvent("sg_a", 50, "https://fused.com/a", 5, "active")
	tr.SetScopeAndEnv("sg_a", "wan", 0, "fused.com", "env1")
	tr.SetThreadInfo("sg_a", 5, false)
	tr.SetTargetBandwidth("sg_a", 4_000_000)

	tr.EnsureTrackedFromEvent("sg_b", 50_000_000, "https://fused.com/b", 8, "waiting")
	tr.SetScopeAndEnv("sg_b", "wan", 0, "fused.com", "env1")
	tr.SetThreadInfo("sg_b", 8, false)
	tr.SetTargetBandwidth("sg_b", 3_000_000)

	cfg := &types.DownloadRecord{
		ID:        "b",
		TotalSize: 50_000_000,
		Runtime:   &types.RuntimeConfig{Workers: 8, MinChunkSize: 1024 * 1024},
	}
	ApplyPickupTighten(cfg)
	if cfg.Runtime.Workers != 1 {
		t.Fatalf("Workers = %d, want 1 when existing >= nMax", cfg.Runtime.Workers)
	}
}

func TestApplyPickupTighten_RetryPickupStillClamps(t *testing.T) {
	tr := withPickupTightenFixture(t, true)
	limits := smartthread.GetDefaultServerLimits()
	limits.SetNMax("wan|retry.com", 6)
	t.Cleanup(func() { limits.Clear("wan|retry.com") })

	tr.EnsureTrackedFromEvent("sg_peer", 50, "https://retry.com/p", 6, "active")
	tr.SetScopeAndEnv("sg_peer", "wan", 0, "retry.com", "env1")
	tr.SetThreadInfo("sg_peer", 6, false)
	tr.SetTargetBandwidth("sg_peer", 5_000_000)

	tr.EnsureTrackedFromEvent("sg_r", 40_000_000, "https://retry.com/r", 7, "waiting")
	tr.SetScopeAndEnv("sg_r", "wan", 0, "retry.com", "env1")
	tr.SetThreadInfo("sg_r", 7, false)
	tr.SetTargetBandwidth("sg_r", 4_000_000)

	cfg := &types.DownloadRecord{
		ID:        "r",
		TotalSize: 40_000_000,
		Runtime:   &types.RuntimeConfig{Workers: 7, MinChunkSize: 1024 * 1024},
	}
	ApplyPickupTighten(cfg)
	if cfg.Runtime.Workers != 1 {
		t.Fatalf("first pickup Workers = %d, want 1", cfg.Runtime.Workers)
	}
	// Simulate retry requeue restoring over-baked Workers then second pickup.
	cfg.Runtime.Workers = 7
	ApplyPickupTighten(cfg)
	if cfg.Runtime.Workers != 1 {
		t.Fatalf("retry pickup Workers = %d, want 1", cfg.Runtime.Workers)
	}
}

func TestApplyPickupTighten_SmartThreadOffNoOp(t *testing.T) {
	_ = withPickupTightenFixture(t, false)
	cfg := &types.DownloadRecord{
		ID:        "off",
		TotalSize: 10_000_000,
		Runtime:   &types.RuntimeConfig{Workers: 9},
	}
	ApplyPickupTighten(cfg)
	if cfg.Runtime.Workers != 9 {
		t.Fatalf("Workers = %d, want 9 when SmartThreadMode off", cfg.Runtime.Workers)
	}
}

// Same-scope / other-domain waiter must shrink promote via ledger.Reserved
// (scope BW), not domain N_max / ReservedByDomain — Macro alone would miss it.
func TestApplyPickupTighten_SameScopeOtherDomainWaiterShrinksViaLedger(t *testing.T) {
	tr := withPickupTightenFixture(t, true)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Seed BBR so Calculate uses ReservedBandwidth (legacy ignores it).
	// Global peak 20MB/s; per-thread ~2MB/s on promo.com.
	speedstats.AddRecordV2(20*1024*1024, 10, 100*1024*1024, false, 50, "promo.com", "wan", "env1")

	// Waiter on a different domain, same scope+env, claims most of global BW.
	tr.EnsureTrackedFromEvent("sg_waiter", 0, "https://other.com/w", 8, "waiting")
	tr.SetScopeAndEnv("sg_waiter", "wan", 0, "other.com", "env1")
	tr.SetThreadInfo("sg_waiter", 8, false)
	tr.SetTargetBandwidth("sg_waiter", 18*1024*1024)

	tr.EnsureTrackedFromEvent("sg_promo", 100_000_000, "https://promo.com/f", 9, "waiting")
	tr.SetScopeAndEnv("sg_promo", "wan", 0, "promo.com", "env1")
	tr.SetThreadInfo("sg_promo", 9, false)
	tr.SetTargetBandwidth("sg_promo", 10*1024*1024)

	// Domain worker path must be free — prove shrink is scope-ledger driven.
	if got := ExistingDomainWorkersFromTelemetryExcluding("wan", "promo.com", "sg_promo"); got != 0 {
		t.Fatalf("promo domain existing workers = %d, want 0", got)
	}
	infos := BuildOccupancyTaskInfosExcluding("sg_promo")
	ledger := smartthread.NewBandwidthLedger(infos)
	if got := ledger.Reserved("wan", "env1"); got < 18*1024*1024 {
		t.Fatalf("ledger.Reserved = %d, want >= 18MB from other-domain waiter", got)
	}
	if got := ledger.ReservedByDomain("wan", "promo.com"); got != 0 {
		t.Fatalf("ReservedByDomain(promo) = %d, want 0 (waiter is other domain)", got)
	}

	cfg := &types.DownloadRecord{
		ID:        "promo",
		TotalSize: 100_000_000,
		Runtime:   &types.RuntimeConfig{Workers: 9, MinChunkSize: 1024 * 1024},
	}
	ApplyPickupTighten(cfg)

	if cfg.Runtime.Workers >= 9 {
		t.Fatalf("Workers = %d, want shrink from other-domain waiter via ledger.Reserved", cfg.Runtime.Workers)
	}
	if cfg.Runtime.Workers > 3 {
		t.Fatalf("Workers = %d, want <= 3 under ~2MB scope headroom", cfg.Runtime.Workers)
	}
}

func TestBuildOccupancyTaskInfosExcluding_DropsSelf(t *testing.T) {
	tr := withPickupTightenFixture(t, true)
	tr.EnsureTrackedFromEvent("sg_a", 0, "https://a.com/1", 4, "waiting")
	tr.SetScopeAndEnv("sg_a", "wan", 0, "a.com", "env1")
	tr.SetThreadInfo("sg_a", 4, false)
	tr.SetTargetBandwidth("sg_a", 2_000_000)

	tr.EnsureTrackedFromEvent("sg_b", 0, "https://a.com/2", 3, "waiting")
	tr.SetScopeAndEnv("sg_b", "wan", 0, "a.com", "env1")
	tr.SetThreadInfo("sg_b", 3, false)
	tr.SetTargetBandwidth("sg_b", 1_000_000)

	infos := BuildOccupancyTaskInfosExcluding("sg_a")
	if len(infos) != 1 || infos[0].GID != "sg_b" {
		t.Fatalf("excluding sg_a: got %#v", infos)
	}
	if ExistingDomainWorkersFromTelemetryExcluding("wan", "a.com", "sg_a") != 3 {
		t.Fatalf("existing workers excluding self = %d, want 3",
			ExistingDomainWorkersFromTelemetryExcluding("wan", "a.com", "sg_a"))
	}
}
