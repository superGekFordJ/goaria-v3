package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/surge/types"
)

func withBandwidthTestEnv(t *testing.T) (*TaskTracker, func()) {
	t.Helper()
	origTracker := State.GetTracker()
	origCache := Cache
	origMon := State.GetMonitor()
	tracker := NewTaskTracker()
	State.SetTracker(tracker)
	Cache = &TaskCache{
		metadata:         make(map[string]*TaskMetadata),
		pendingStartGids: make(map[string]time.Time),
	}
	return tracker, func() {
		State.SetTracker(origTracker)
		Cache = origCache
		State.SetMonitor(origMon)
	}
}

func newMacroTestTicker(gid string) *smartthread.ConvergenceTicker {
	return smartthread.NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		&macroBWTracker{tasks: []smartthread.TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "env1", Domain: "a.com"},
		}},
		&macroBWTelemetry{},
		&macroBWPeak{},
		&macroBWRate{},
		0, 0,
	)
}

func TestMacroBandwidthByScope_EnvAndScopeIsolation(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	tracker.SetScopeAndEnv("sg_wan_e1", "wan", 100, "a.com", "env1")
	tracker.SetScopeAndEnv("sg_wan_e2", "wan", 100, "b.com", "env2")
	tracker.SetScopeAndEnv("sg_lan_e1", "lan", 80, "c.local", "env1")
	tracker.SetScopeAndEnv("ar_wan_e1", "wan", 90, "d.com", "env1")

	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{
		{GID: "sg_wan_e1", DownloadSpeed: "5000000"},
		{GID: "sg_wan_e2", DownloadSpeed: "4000000"},
		{GID: "sg_lan_e1", DownloadSpeed: "2000000"},
	}
	Cache.sgMu.Unlock()
	Cache.arMu.Lock()
	Cache.arActive = []rpc.Task{
		{GID: "ar_wan_e1", DownloadSpeed: "3000000"},
	}
	Cache.arMu.Unlock()

	if got := MacroBandwidthByScope("wan", "env1"); got != 8000000 {
		t.Errorf("wan/env1 = %d, want 8000000 (sg cold + ar)", got)
	}
	if got := MacroBandwidthByScope("wan", "env2"); got != 4000000 {
		t.Errorf("wan/env2 = %d, want 4000000", got)
	}
	if got := MacroBandwidthByScope("lan", "env1"); got != 2000000 {
		t.Errorf("lan/env1 = %d, want 2000000", got)
	}
	if got := MacroBandwidthByScope("wan", "env3"); got != 0 {
		t.Errorf("cross-env = %d, want 0", got)
	}
}

func TestMacroBandwidthByScope_SurgeColdPadThenPermanentCut(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	gid := "sg_macro_cut"
	tracker.SetScopeAndEnv(gid, "wan", 100, "a.com", "env1")
	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: gid, DownloadSpeed: "9000000"}}
	Cache.sgMu.Unlock()

	if got := MacroBandwidthByScope("wan", "env1"); got != 9000000 {
		t.Errorf("cold pad = %d, want 9000000", got)
	}

	ct := newMacroTestTicker(gid)
	defer ct.Stop()
	State.SetMonitor(&Monitor{convergence: ct})
	ct.InjectMacroOccupancyForTest(gid, 1_000_000, true)

	if got := MacroBandwidthByScope("wan", "env1"); got != 1_000_000 {
		t.Errorf("ready macro = %d, want 1000000 (ignore Cache EMA 9MB/s)", got)
	}

	Cache.sgMu.Lock()
	Cache.sgActive[0].DownloadSpeed = "100"
	Cache.sgMu.Unlock()
	if got := MacroBandwidthByScope("wan", "env1"); got != 1_000_000 {
		t.Errorf("after EMA crater = %d, want 1000000 (permanent cut)", got)
	}

	ct.InjectMacroOccupancyForTest(gid, 0, true)
	Cache.sgMu.Lock()
	Cache.sgActive[0].DownloadSpeed = "8000000"
	Cache.sgMu.Unlock()
	if got := MacroBandwidthByScope("wan", "env1"); got != 0 {
		t.Errorf("ready zero = %d, want 0 (not EMA)", got)
	}
}

func TestMacroBandwidthByScope_NoDoubleCount(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	gid := "sg_nodouble"
	tracker.SetScopeAndEnv(gid, "wan", 100, "a.com", "env1")
	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: gid, DownloadSpeed: "5000000"}}
	Cache.sgMu.Unlock()

	ct := newMacroTestTicker(gid)
	defer ct.Stop()
	State.SetMonitor(&Monitor{convergence: ct})
	ct.InjectMacroOccupancyForTest(gid, 2_000_000, true)

	if got := MacroBandwidthByScope("wan", "env1"); got != 2_000_000 {
		t.Errorf("got %d, want 2000000 (macro only, not + Cache)", got)
	}
}

func TestMacroBandwidthByScope_Aria2UsesCacheSpeed(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	tracker.SetScopeAndEnv("ar_aria", "wan", 100, "a.com", "env1")
	Cache.arMu.Lock()
	Cache.arActive = []rpc.Task{{GID: "ar_aria", DownloadSpeed: "4500000"}}
	Cache.arMu.Unlock()

	if got := MacroBandwidthByScope("wan", "env1"); got != 4500000 {
		t.Errorf("aria2 = %d, want 4500000", got)
	}
}

func TestMacroBandwidthByScope_HybridSameBucket(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	sg := "sg_hybrid"
	ar := "ar_hybrid"
	tracker.SetScopeAndEnv(sg, "wan", 100, "a.com", "env1")
	tracker.SetScopeAndEnv(ar, "wan", 100, "b.com", "env1")
	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: sg, DownloadSpeed: "9999999"}}
	Cache.sgMu.Unlock()
	Cache.arMu.Lock()
	Cache.arActive = []rpc.Task{{GID: ar, DownloadSpeed: "3000000"}}
	Cache.arMu.Unlock()

	ct := newMacroTestTicker(sg)
	defer ct.Stop()
	State.SetMonitor(&Monitor{convergence: ct})
	ct.InjectMacroOccupancyForTest(sg, 1_500_000, true)

	if got := MacroBandwidthByScope("wan", "env1"); got != 4_500_000 {
		t.Errorf("hybrid = %d, want 4500000 (macro 1.5M + aria 3M)", got)
	}
}

func TestActiveBandwidthByScope_StillCacheOnly(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	gid := "sg_legacy"
	tracker.SetScopeAndEnv(gid, "wan", 100, "a.com", "env1")
	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: gid, DownloadSpeed: "7000000"}}
	Cache.sgMu.Unlock()

	ct := newMacroTestTicker(gid)
	defer ct.Stop()
	State.SetMonitor(&Monitor{convergence: ct})
	ct.InjectMacroOccupancyForTest(gid, 1_000_000, true)

	if got := ActiveBandwidthByScope("wan", "env1"); got != 7000000 {
		t.Errorf("legacy Active = %d, want 7000000 (Cache-only)", got)
	}
	if got := MacroBandwidthByScope("wan", "env1"); got != 1_000_000 {
		t.Errorf("Macro = %d, want 1000000", got)
	}
}

func TestMacroBandwidthByScope_ScopeMissingSkipped(t *testing.T) {
	tracker, cleanup := withBandwidthTestEnv(t)
	defer cleanup()

	tracker.SetThreadInfo("sg_no_scope", 4, false)
	Cache.sgMu.Lock()
	Cache.sgActive = []rpc.Task{{GID: "sg_no_scope", DownloadSpeed: "9999999"}}
	Cache.sgMu.Unlock()

	if got := MacroBandwidthByScope("wan", "env1"); got != 0 {
		t.Errorf("missing scope = %d, want 0", got)
	}
}

type macroBWTracker struct {
	tasks []smartthread.TrackedTaskInfo
}

func (m *macroBWTracker) GetActiveTrackedTasks() []smartthread.TrackedTaskInfo {
	return m.tasks
}

func (m *macroBWTracker) GetScopeAndEnv(gid string) (scope, domain, envKey string, ok bool) {
	for _, t := range m.tasks {
		if t.GID == gid {
			return t.Scope, t.Domain, t.EnvKey, true
		}
	}
	return "", "", "", false
}

type macroBWTelemetry struct{}

func (m *macroBWTelemetry) Get(string) []types.WorkerSnapshot { return nil }

type macroBWPeak struct{}

func (m *macroBWPeak) RecordPeakEfficiency(string, int64, int) {}

type macroBWRate struct{}

func (m *macroBWRate) GetRateLimit(string) (int64, bool) { return 0, false }
