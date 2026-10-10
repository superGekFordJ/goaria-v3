package monitor

import (
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
)

// TestOccupancyTaskInfos_CollectsFactsAndRawOverlay verifies the external
// collector returns tracker facts, cache telemetry, and the Surge raw overlay
// — the three layers the unified formula needs.
func TestOccupancyTaskInfos_CollectsFactsAndRawOverlay(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	tracker.EnsureTrackedFromEvent("sg_occ", 1000, "https://x", 4, "active")
	tracker.SetScopeAndEnv("sg_occ", "wan", 0, "a.com", "env1")
	tracker.SetTargetBandwidth("sg_occ", 5_000_000)
	tracker.ObserveActivation("sg_occ", 100, true)
	tracker.EnsureTrackedFromEvent("ar_occ", 1000, "https://y", 4, "active")
	tracker.SetScopeAndEnv("ar_occ", "wan", 0, "b.com", "env1")

	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: "sg_occ", DownloadSpeed: "2000000"}}
	Cache.sgMu.Unlock()
	Cache.arMu.Lock()
	Cache.arActive = []rpc.Task{{GID: "ar_occ", DownloadSpeed: "1500000"}}
	Cache.arMu.Unlock()

	ct := newMacroTestTicker("sg_occ")
	defer ct.Stop()
	State.SetMonitor(&Monitor{convergence: ct, tracker: tracker})
	ct.InjectMacroOccupancyForTest("sg_occ", 4_000_000, true)

	infos := OccupancyTaskInfos()
	byGID := map[string]smartthread.TrackedTaskInfo{}
	for _, info := range infos {
		byGID[info.GID] = info
	}
	sg := byGID["sg_occ"]
	if !sg.MacroReady || sg.RawBps != 4_000_000 {
		t.Errorf("surge overlay = ready:%v raw:%d, want true/4000000", sg.MacroReady, sg.RawBps)
	}
	if sg.ActivatedAt.IsZero() {
		t.Error("activation fact missing from collector output")
	}
	ar := byGID["ar_occ"]
	if !ar.TelemetryValid || ar.TelemetryBps != 1_500_000 {
		t.Errorf("aria2 telemetry = valid:%v bps:%d, want true/1500000", ar.TelemetryValid, ar.TelemetryBps)
	}
}

// TestOccupancyTaskInfos_NilMonitorDegrades verifies the collector still
// yields Target/compat inputs when no monitor/convergence exists — raw is
// simply unavailable, nothing is fabricated.
func TestOccupancyTaskInfos_NilMonitorDegrades(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	tracker.EnsureTrackedFromEvent("sg_cold", 1000, "https://x", 4, "active")
	tracker.SetScopeAndEnv("sg_cold", "wan", 0, "a.com", "env1")
	tracker.SetTargetBandwidth("sg_cold", 3_000_000)
	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: "sg_cold", DownloadSpeed: "700000"}}
	Cache.sgMu.Unlock()

	infos := OccupancyTaskInfos()
	if len(infos) != 1 {
		t.Fatalf("infos = %#v, want 1 entry", infos)
	}
	if infos[0].MacroReady || infos[0].RawBps != 0 {
		t.Error("nil convergence must not fabricate raw readiness")
	}
	if !infos[0].TelemetryValid || infos[0].TelemetryBps != 700_000 {
		t.Errorf("cold cache pad = valid:%v bps:%d, want true/700000", infos[0].TelemetryValid, infos[0].TelemetryBps)
	}
}

// TestMacroOccupancyByScope_UsesUnifiedFormula proves the scope provider now
// evaluates TaskOccupancy at the given instant: a live lease keeps the
// Target floor, an expired lease drops to telemetry.
func TestMacroOccupancyByScope_UsesUnifiedFormula(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	now := time.Now()
	tracker.EnsureTrackedFromEvent("sg_l", 1000, "https://x", 4, "active")
	tracker.SetScopeAndEnv("sg_l", "wan", 0, "a.com", "env1")
	tracker.SetTargetBandwidth("sg_l", 5_000_000)
	tracker.ObserveActivation("sg_l", 0, true)
	// ObserveActivation stamps its own time.Now() which may land after the
	// now captured above (now.Before(activatedAt) → lease dead). Pin the
	// stamp to now so the live-lease assertion is deterministic.
	tracker.tasks["sg_l"].ActivatedAt = now
	tracker.EnsureTrackedFromEvent("ar_l", 1000, "https://y", 4, "active")
	tracker.SetScopeAndEnv("ar_l", "wan", 0, "b.com", "env1")

	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: "sg_l", DownloadSpeed: "1000000"}}
	Cache.sgMu.Unlock()
	Cache.arMu.Lock()
	Cache.arActive = []rpc.Task{{GID: "ar_l", DownloadSpeed: "500000"}}
	Cache.arMu.Unlock()

	if got := MacroOccupancyByScope("wan", "env1", now); got != 5_500_000 {
		t.Errorf("live lease scope = %d, want 5500000 (5M floor + 0.5M aria2)", got)
	}
	// Lease expired (activation 60s ago, no growth): only telemetry counts.
	expiredNow := now.Add(-time.Minute)
	tracker.tasks["sg_l"].ActivatedAt = expiredNow
	if got := MacroOccupancyByScope("wan", "env1", now); got != 1_500_000 {
		t.Errorf("expired lease scope = %d, want 1500000 (telem only)", got)
	}
	if got := MacroOccupancyByScope("wan", "env2", now); got != 0 {
		t.Errorf("cross-env = %d, want 0", got)
	}
	if got := MacroOccupancyByScope("lan", "env1", now); got != 0 {
		t.Errorf("cross-scope = %d, want 0", got)
	}
}

// TestTrackerAdapter_OccupancyPassThrough verifies the adapter maps the full
// occupancy scalar set — and can be invoked on the real c.mu→adapter path
// (SumLastRawBps holds c.mu while calling GetActiveTrackedTasks) without
// self-locking, while the external collector runs concurrently.
func TestTrackerAdapter_OccupancyPassThrough(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	tracker.EnsureTrackedFromEvent("sg_p", 1000, "https://x", 4, "active")
	tracker.SetScopeAndEnv("sg_p", "wan", 0, "a.com", "env1")
	tracker.SetTargetBandwidth("sg_p", 5_000_000)
	tracker.ObserveActivation("sg_p", 0, true)
	tracker.SetThreadInfo("sg_p", 8, false)
	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: "sg_p", DownloadSpeed: "2000000"}}
	Cache.sgMu.Unlock()

	adapter := &trackerAdapter{TaskTracker: tracker}
	occ := adapter.GetOccupancyTrackedTasks()
	if len(occ) != 1 {
		t.Fatalf("adapter occupancy = %#v, want 1", occ)
	}
	info := occ[0]
	if info.ActivatedAt.IsZero() || info.TargetBandwidth != 5_000_000 ||
		info.ThreadCount != 8 || !info.TelemetryValid || info.TelemetryBps != 2_000_000 {
		t.Errorf("adapter dropped fields: %#v", info)
	}
	if info.MacroReady || info.RawBps != 0 {
		t.Error("adapter must not overlay raw (caller-side responsibility)")
	}

	// c.mu→adapter path: real ticker, real adapter. SumLastRawBps holds c.mu
	// while iterating the adapter's active snapshot — must not deadlock and
	// must see the same fields.
	he := rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil))
	ct := smartthread.NewConvergenceTicker(he, adapter, &macroBWTelemetry{}, adapter, &macroBWRate{}, 0, 256)
	defer ct.Stop()
	ct.InjectMacroOccupancyForTest("sg_p", 4_000_000, true)

	State.SetMonitor(&Monitor{convergence: ct, tracker: tracker})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				for j := 0; j < 200; j++ {
					switch n % 4 {
					case 0:
						_ = ct.SumLastRawBps("wan", "env1") // c.mu→adapter path
					case 1:
						_ = OccupancyTaskInfos() // takes c.mu via LastRawBps
					case 2:
						tracker.UpdateProgressFromEvent("sg_p", 1000, int64(100+j))
					case 3:
						tracker.SetStatusFromEvent("sg_p", "active")
					}
				}
			}(i)
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock on c.mu→adapter / collector / tracker path")
	}
	// Concurrent RemoveTask must also be safe.
	tracker.RemoveTask("sg_p")
}
