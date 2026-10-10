package smartthread

import (
	"math"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/speedstats"
)

func TestBandwidthLedger_SeedsFromActiveBandwidth(t *testing.T) {
	// Hybrid seed replaces MacroBandwidth lump: seed from per-task TargetBandwidth
	// while cold, not from activeBandwidthProvider.
	ledger := NewBandwidthLedger([]TrackedTaskInfo{
		{GID: "sg_1", Scope: "wan", EnvKey: "env1", Domain: "a.com", TargetBandwidth: 5_000_000, MacroReady: false},
	})
	reserved := ledger.Reserved("wan", "env1")
	if reserved != 5_000_000 {
		t.Errorf("Reserved(wan, env1) = %d, want 5000000 (hybrid cold seed)", reserved)
	}
}

func TestBandwidthLedger_ReserveAccumulates(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000},
	}

	ledger.Reserve("wan", "env1", 2000000)
	if got := ledger.Reserved("wan", "env1"); got != 3000000 {
		t.Errorf("Reserved(wan, env1) = %d, want 3000000", got)
	}

	ledger.Reserve("wan", "env1", 500000)
	if got := ledger.Reserved("wan", "env1"); got != 3500000 {
		t.Errorf("Reserved(wan, env1) = %d, want 3500000", got)
	}
}

func TestBandwidthLedger_ScopeIsolation(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000, "lanenv1": 500000},
	}

	ledger.Reserve("wan", "env1", 2000000)
	if got := ledger.Reserved("lan", "env1"); got != 500000 {
		t.Errorf("Reserved(lan, env1) = %d, want 500000 (unaffected by wan reserve)", got)
	}
}

func TestBandwidthLedger_EnvIsolation(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000, "wanenv2": 2000000},
	}

	ledger.Reserve("wan", "env1", 500000)
	if got := ledger.Reserved("wan", "env2"); got != 2000000 {
		t.Errorf("Reserved(wan, env2) = %d, want 2000000 (unaffected by env1 reserve)", got)
	}
}

func TestBandwidthLedger_EmptyScopeDefaultsWan(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000},
	}

	if got := ledger.Reserved("", "env1"); got != 1000000 {
		t.Errorf("Reserved(\"\", env1) = %d, want 1000000 (defaults to wan)", got)
	}

	ledger.Reserve("", "env1", 500000)
	if got := ledger.Reserved("wan", "env1"); got != 1500000 {
		t.Errorf("Reserved(wan, env1) after Reserve(\"\", env1) = %d, want 1500000", got)
	}
}

func TestBandwidthLedger_NilSafety(t *testing.T) {
	var ledger *BandwidthLedger

	if got := ledger.Reserved("wan", "env1"); got != 0 {
		t.Errorf("nil.Reserved = %d, want 0", got)
	}

	ledger.Reserve("wan", "env1", 1000000) // should not panic
	ledger.Release("wan", "env1", 1000000)
	ledger.ReserveByDomain("wan", "a.com", 1000000)
	ledger.ReleaseByDomain("wan", "a.com", 1000000)
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 0 {
		t.Errorf("nil.ReservedByDomain = %d, want 0", got)
	}
}

func TestBandwidthLedger_NegativeBandwidthIgnored(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved:         map[string]int64{"wanenv1": 1000000},
		reservedByDomain: map[string]int64{},
	}

	ledger.Reserve("wan", "env1", -500000)
	if got := ledger.Reserved("wan", "env1"); got != 1000000 {
		t.Errorf("Reserved(wan, env1) = %d, want 1000000 (negative ignored)", got)
	}
}

func TestBandwidthLedger_HybridSeed_SurgeColdUsesTarget(t *testing.T) {
	ledger := NewBandwidthLedger([]TrackedTaskInfo{
		{
			GID:             "sg_1",
			Scope:           "wan",
			EnvKey:          "env1",
			Domain:          "a.com",
			TargetBandwidth: 8_000_000,
			TelemetryBps:    100_000,
			MacroReady:      false,
		},
	})
	if got := ledger.Reserved("wan", "env1"); got != 8_000_000 {
		t.Errorf("Reserved = %d, want 8000000 (cold max target)", got)
	}
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 8_000_000 {
		t.Errorf("ReservedByDomain = %d, want 8000000", got)
	}
}

func TestBandwidthLedger_HybridSeed_SurgeReadyUsesTelem(t *testing.T) {
	ledger := NewBandwidthLedger([]TrackedTaskInfo{
		{
			GID:             "sg_1",
			Status:          "active",
			Scope:           "wan",
			EnvKey:          "env1",
			Domain:          "a.com",
			TargetBandwidth: 8_000_000,
			RawBps:          1_500_000,
			MacroReady:      true,
		},
	})
	if got := ledger.Reserved("wan", "env1"); got != 1_500_000 {
		t.Errorf("Reserved = %d, want 1500000 (ready telem only)", got)
	}
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 1_500_000 {
		t.Errorf("ReservedByDomain = %d, want 1500000", got)
	}
}

func TestBandwidthLedger_HybridSeed_Aria2AgeWindow(t *testing.T) {
	young := NewBandwidthLedger([]TrackedTaskInfo{
		{
			GID:             "ar_1",
			Status:          "active",
			Scope:           "wan",
			EnvKey:          "env1",
			Domain:          "a.com",
			TargetBandwidth: 5_000_000,
			TelemetryBps:    200_000,
			TelemetryValid:  true,
			CompatClaim:     CompatAria2Add,
			AddAnchorAt:     time.Now(),
		},
	})
	if got := young.Reserved("wan", "env1"); got != 5_000_000 {
		t.Errorf("young Reserved = %d, want 5000000", got)
	}

	aged := NewBandwidthLedger([]TrackedTaskInfo{
		{
			GID:             "ar_1",
			Status:          "active",
			Scope:           "wan",
			EnvKey:          "env1",
			Domain:          "a.com",
			TargetBandwidth: 5_000_000,
			TelemetryBps:    200_000,
			TelemetryValid:  true,
			CompatClaim:     CompatAria2Add,
			AddAnchorAt:     time.Now().Add(-aria2AddPlaceholderWindow - time.Second),
		},
	})
	if got := aged.Reserved("wan", "env1"); got != 200_000 {
		t.Errorf("aged Reserved = %d, want 200000 (telem only)", got)
	}
}

func TestBandwidthLedger_HybridSeed_SameDomainAccumulates(t *testing.T) {
	ledger := NewBandwidthLedger([]TrackedTaskInfo{
		{GID: "sg_1", Scope: "wan", EnvKey: "e", Domain: "a.com", TargetBandwidth: 3_000_000, MacroReady: false},
		{GID: "sg_2", Scope: "wan", EnvKey: "e", Domain: "a.com", TargetBandwidth: 4_000_000, MacroReady: false},
	})
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 7_000_000 {
		t.Errorf("ReservedByDomain = %d, want 7000000 (sum of cold targets)", got)
	}
	if got := ledger.Reserved("wan", "e"); got != 7_000_000 {
		t.Errorf("Reserved = %d, want 7000000", got)
	}
}

// Env-blind domain keys: same scope|domain across different envs aggregates
// into one domain occupancy figure while scope+env stays isolated.
func TestBandwidthLedger_HybridSeed_DomainEnvBlind(t *testing.T) {
	ledger := NewBandwidthLedger([]TrackedTaskInfo{
		{GID: "sg_1", Scope: "wan", EnvKey: "envA", Domain: "a.com", TargetBandwidth: 3_000_000},
		{GID: "sg_2", Scope: "wan", EnvKey: "envB", Domain: "a.com", TargetBandwidth: 4_000_000},
	})
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 7_000_000 {
		t.Errorf("ReservedByDomain = %d, want 7000000 (env-blind domain sum)", got)
	}
	if got := ledger.Reserved("wan", "envA"); got != 3_000_000 {
		t.Errorf("Reserved(envA) = %d, want 3000000", got)
	}
	if got := ledger.Reserved("wan", "envB"); got != 4_000_000 {
		t.Errorf("Reserved(envB) = %d, want 4000000", got)
	}
}

func TestBandwidthLedger_DomainIsolation(t *testing.T) {
	ledger := NewBandwidthLedger([]TrackedTaskInfo{
		{GID: "sg_1", Scope: "wan", EnvKey: "e", Domain: "a.com", TargetBandwidth: 9_000_000, MacroReady: false},
	})
	ledger.ReserveByDomain("wan", "a.com", 1_000_000)
	if got := ledger.ReservedByDomain("wan", "b.com"); got != 0 {
		t.Errorf("ReservedByDomain(b.com) = %d, want 0", got)
	}
	if got := ledger.ReservedByDomain("lan", "a.com"); got != 0 {
		t.Errorf("ReservedByDomain(lan,a.com) = %d, want 0", got)
	}
}

func TestBandwidthLedger_ReleaseAndReleaseByDomainFloor(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved:         map[string]int64{"wanenv1": 1_000_000},
		reservedByDomain: map[string]int64{"wan|a.com": 2_000_000},
		reservedWorkers:  map[string]int{},
	}
	ledger.Release("wan", "env1", 5_000_000)
	if got := ledger.Reserved("wan", "env1"); got != 0 {
		t.Errorf("Reserved after over-release = %d, want 0", got)
	}
	ledger.ReleaseByDomain("wan", "a.com", 9_000_000)
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 0 {
		t.Errorf("ReservedByDomain after over-release = %d, want 0", got)
	}
}

func TestBandwidthLedger_NoProviderLumpDoubleCount(t *testing.T) {
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string, now time.Time) int64 {
		return 99_000_000 // must not be lumped on top of hybrid seed
	}
	ledger := NewBandwidthLedger([]TrackedTaskInfo{
		{GID: "sg_1", Scope: "wan", EnvKey: "env1", Domain: "a.com", TargetBandwidth: 1_000_000, MacroReady: false},
	})
	if got := ledger.Reserved("wan", "env1"); got != 1_000_000 {
		t.Errorf("Reserved = %d, want 1000000 (hybrid only, no provider lump)", got)
	}
}

func TestBandwidthLedger_AddUriFailureReleaseAllDimensions(t *testing.T) {
	ledger := NewBandwidthLedger(nil)
	ledger.Reserve("wan", "env1", 4_000_000)
	ledger.ReserveByDomain("wan", "a.com", 4_000_000)
	ledger.ReserveWorkers("wan", "a.com", 9)

	ledger.Release("wan", "env1", 4_000_000)
	ledger.ReleaseByDomain("wan", "a.com", 4_000_000)
	ledger.ReleaseWorkers("wan", "a.com", 9)

	if got := ledger.Reserved("wan", "env1"); got != 0 {
		t.Errorf("Reserved after failure release = %d, want 0", got)
	}
	if got := ledger.ReservedByDomain("wan", "a.com"); got != 0 {
		t.Errorf("ReservedByDomain after failure release = %d, want 0", got)
	}
	if got := ledger.ReservedWorkers("wan", "a.com"); got != 0 {
		t.Errorf("ReservedWorkers after failure release = %d, want 0", got)
	}
}

func TestBandwidthLedger_ConcurrentAlloc_SerializesDomainClaim(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	speedstats.AddRecordV2(8*1024*1024, 8, 200*1024*1024, false, 100, "a.com", "wan", "testenv")
	speedstats.AddRecordV2(100*1024*1024, 1, 200*1024*1024, false, 100, "big.com", "wan", "testenv")

	ledger := NewBandwidthLedger(nil)
	fileSize := int64(1 * 1024 * 1024 * 1024)
	const n = 8
	splits := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			var params ThreadParams
			ledger.WithAlloc(func() {
				params = Calculate(CalcParams{
					FileSize:                fileSize,
					MaxConnections:          16,
					Scope:                   "wan",
					EnvKey:                  "testenv",
					Domain:                  "a.com",
					ReservedBandwidth:       ledger.Reserved("wan", "testenv"),
					ReservedDomainBandwidth: ledger.ReservedByDomain("wan", "a.com"),
				})
				ledger.Reserve("wan", "testenv", params.TargetBandwidth)
				ledger.ReserveByDomain("wan", "a.com", params.TargetBandwidth)
				ledger.ReserveWorkers("wan", "a.com", params.Split)
			})
			splits[idx] = params.Split
		}(i)
	}
	wg.Wait()

	high, floor1 := 0, 0
	for _, s := range splits {
		switch s {
		case 9:
			high++
		case 1:
			floor1++
		default:
			t.Errorf("unexpected Split=%d in %v", s, splits)
		}
	}
	if high != 1 || floor1 != n-1 {
		t.Fatalf("concurrent domain claims: splits=%v want exactly one 9 and %d ones", splits, n-1)
	}
}

// --- TaskOccupancy unified formula tests (fixed now) ---

func TestTaskOccupancy_ActivationLeaseEdges(t *testing.T) {
	base := time.Now()
	cases := []struct {
		name           string
		activatedAgo   time.Duration // -1 => no activation fact
		progressOffset time.Duration // -1 => no first progress; else offset after activation
		nowOffset      time.Duration
		wantClaim      bool
	}{
		{"start", 0, -1, 0, true},
		{"mid-lease", 20 * time.Second, -1, 0, true},
		{"edge-44s", 44 * time.Second, -1, 0, true},
		{"edge-45s-expired", 45 * time.Second, -1, 0, false},
		{"growth-at-44s-claim-until-59s", 58 * time.Second, 44 * time.Second, 0, true},
		{"growth-at-44s-expired-at-59s", 59 * time.Second, 44 * time.Second, 0, false},
		{"growth-at-45s-no-revival", 50 * time.Second, 45 * time.Second, 0, false},
		{"growth-after-45s-no-revival", 60 * time.Second, 50 * time.Second, 0, false},
		{"now-before-activation", -time.Second, -1, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := TrackedTaskInfo{
				GID:             "sg_x",
				Status:          "active",
				Scope:           "wan",
				EnvKey:          "e",
				Domain:          "a.com",
				TargetBandwidth: 5_000_000,
			}
			if tc.activatedAgo >= 0 {
				info.ActivatedAt = base.Add(-tc.activatedAgo)
			}
			if tc.progressOffset >= 0 && tc.activatedAgo >= 0 {
				info.FirstProgressAt = info.ActivatedAt.Add(tc.progressOffset)
			}
			got := TaskOccupancy(info, base.Add(tc.nowOffset))
			want := int64(0)
			if tc.wantClaim {
				want = 5_000_000
			}
			if got != want {
				t.Errorf("TaskOccupancy = %d, want %d", got, want)
			}
		})
	}
}

func TestTaskOccupancy_OldActiveNoFactsIsTelemOnly(t *testing.T) {
	now := time.Now()
	// A pre-facts "old active" with a Target must not gain a floor from the
	// parameter hint alone — only explicit compat/admission sources claim.
	info := TrackedTaskInfo{
		GID:             "sg_old",
		Status:          "active",
		Scope:           "wan",
		EnvKey:          "e",
		Domain:          "a.com",
		TargetBandwidth: 9_000_000,
		TelemetryBps:    1_000_000,
		TelemetryValid:  true,
	}
	for i := 0; i < 3; i++ {
		if got := TaskOccupancy(info, now); got != 1_000_000 {
			t.Fatalf("read %d: got %d, want telem 1000000 (no write/no grant)", i, got)
		}
	}
}

func TestTaskOccupancy_SurgeReadyZeroNeverFallsBackToCache(t *testing.T) {
	now := time.Now()
	// Ready raw=0 is a measured zero: cache EMA must not substitute, and once
	// the lease ends the contribution is 0 (not the stale cache pad).
	leased := TrackedTaskInfo{
		GID: "sg_z", Status: "active", Scope: "wan", EnvKey: "e", Domain: "a.com",
		TargetBandwidth: 5_000_000, TelemetryBps: 9_000_000, TelemetryValid: true,
		RawBps: 0, MacroReady: true, ActivatedAt: now.Add(-time.Second),
	}
	if got := TaskOccupancy(leased, now); got != 5_000_000 {
		t.Errorf("leased ready-0 = %d, want max(target,0)=5000000", got)
	}
	expired := leased
	expired.ActivatedAt = now.Add(-time.Minute)
	if got := TaskOccupancy(expired, now); got != 0 {
		t.Errorf("expired ready-0 = %d, want 0 (no cache fallback)", got)
	}
}

func TestTaskOccupancy_MeasuredZeroDistinctFromMissing(t *testing.T) {
	now := time.Now()
	// Both contribute 0, but through different paths: the measured zero is a
	// real telemetry pick, the missing one degrades. Assert the outcomes the
	// callers observe — contribution value — plus that neither fabricates a
	// Target floor without a claim source.
	measured := TrackedTaskInfo{
		GID: "ar_m", Status: "active", Scope: "wan", EnvKey: "e", Domain: "a.com",
		TelemetryBps: 0, TelemetryValid: true, TargetBandwidth: 5_000_000,
	}
	missing := measured
	missing.TelemetryValid = false
	if got := TaskOccupancy(measured, now); got != 0 {
		t.Errorf("measured zero = %d, want 0", got)
	}
	if got := TaskOccupancy(missing, now); got != 0 {
		t.Errorf("missing telemetry = %d, want 0", got)
	}
}

func TestTaskOccupancy_WaitingAndPausedRules(t *testing.T) {
	now := time.Now()
	surgeWaiting := TrackedTaskInfo{
		GID: "sg_w", Status: "waiting", Scope: "wan", EnvKey: "e", Domain: "a.com",
		TargetBandwidth: 5_000_000,
	}
	if got := TaskOccupancy(surgeWaiting, now); got != 5_000_000 {
		t.Errorf("surge waiting claim = %d, want 5000000 (never lease-expired)", got)
	}
	aria2Waiting := surgeWaiting
	aria2Waiting.GID = "ar_w"
	if got := TaskOccupancy(aria2Waiting, now); got != 0 {
		t.Errorf("aria2 waiting = %d, want 0 immediately", got)
	}
	// Paused + add-time claim → 0 (claim lineage is not the resume hold).
	addClaimPaused := TrackedTaskInfo{
		GID: "sg_p", Status: "paused", Scope: "wan", EnvKey: "e", Domain: "a.com",
		TargetBandwidth: 5_000_000, CompatClaim: CompatSurgeClaim,
	}
	if got := TaskOccupancy(addClaimPaused, now); got != 0 {
		t.Errorf("paused add-claim = %d, want 0", got)
	}
	// Paused + resume-hold lineage → claim.
	resumePaused := addClaimPaused
	resumePaused.CompatClaim = CompatSurgeResume
	if got := TaskOccupancy(resumePaused, now); got != 5_000_000 {
		t.Errorf("paused resume-claim = %d, want 5000000", got)
	}
	// Active resume-claim pre-start also claims (protection until real start).
	resumeActive := resumePaused
	resumeActive.Status = "active"
	if got := TaskOccupancy(resumeActive, now); got != 5_000_000 {
		t.Errorf("active resume-claim = %d, want 5000000", got)
	}
}

func TestTaskOccupancy_Aria2AddPlaceholderWindow(t *testing.T) {
	now := time.Now()
	anchor := now.Add(-10 * time.Second)
	live := TrackedTaskInfo{
		GID: "ar_a", Status: "active", Scope: "wan", EnvKey: "e", Domain: "a.com",
		TargetBandwidth: 5_000_000, CompatClaim: CompatAria2Add, AddAnchorAt: anchor,
	}
	if got := TaskOccupancy(live, now); got != 5_000_000 {
		t.Errorf("live placeholder = %d, want 5000000", got)
	}
	expired := live
	expired.AddAnchorAt = now.Add(-aria2AddPlaceholderWindow - time.Second)
	if got := TaskOccupancy(expired, now); got != 0 {
		t.Errorf("expired placeholder = %d, want 0 (telem-only)", got)
	}
	// Zero anchor is never a valid placeholder — no guessing "now".
	noAnchor := live
	noAnchor.AddAnchorAt = time.Time{}
	if got := TaskOccupancy(noAnchor, now); got != 0 {
		t.Errorf("zero anchor = %d, want 0", got)
	}
	// Empty-status placeholder also claims within the window.
	emptyStatus := live
	emptyStatus.Status = ""
	if got := TaskOccupancy(emptyStatus, now); got != 5_000_000 {
		t.Errorf("empty-status live placeholder = %d, want 5000000", got)
	}
}

func TestTaskOccupancy_AdmissionReservedScalar(t *testing.T) {
	now := time.Now()
	reserved := TrackedTaskInfo{
		GID: "sg_adm", Status: "waiting", Scope: "wan", EnvKey: "e", Domain: "a.com",
		TargetBandwidth: 5_000_000, AdmissionReserved: true,
	}
	if got := TaskOccupancy(reserved, now); got != 5_000_000 {
		t.Errorf("admitted waiting = %d, want 5000000", got)
	}
	// Terminal always excluded, even with the explicit input.
	terminal := reserved
	terminal.Status = "complete"
	if got := TaskOccupancy(terminal, now); got != 0 {
		t.Errorf("admitted terminal = %d, want 0", got)
	}
}

func TestTaskOccupancy_NegativeAndSaturating(t *testing.T) {
	now := time.Now()
	neg := TrackedTaskInfo{
		GID: "sg_n", Status: "active", Scope: "wan", EnvKey: "e", Domain: "a.com",
		TargetBandwidth: -5, TelemetryBps: -10, TelemetryValid: true,
		ActivatedAt: now,
	}
	if got := TaskOccupancy(neg, now); got != 0 {
		t.Errorf("negative inputs = %d, want 0", got)
	}
	if got := saturatingAdd64(math.MaxInt64, 1); got != math.MaxInt64 {
		t.Errorf("saturating add overflowed: %d", got)
	}
	if got := saturatingAdd64(5, -3); got != 5 {
		t.Errorf("saturating add negative = %d, want 5", got)
	}
}

// --- reservedWorkers (TOCTOU guard) tests ---

func TestBandwidthLedger_ReserveWorkersAccumulates(t *testing.T) {
	ledger := &BandwidthLedger{
		reservedWorkers: make(map[string]int),
	}

	ledger.ReserveWorkers("wan", "example.com", 3)
	if got := ledger.ReservedWorkers("wan", "example.com"); got != 3 {
		t.Errorf("ReservedWorkers(wan, example.com) = %d, want 3", got)
	}

	ledger.ReserveWorkers("wan", "example.com", 2)
	if got := ledger.ReservedWorkers("wan", "example.com"); got != 5 {
		t.Errorf("ReservedWorkers(wan, example.com) = %d, want 5", got)
	}
}

func TestBandwidthLedger_ReserveWorkersScopeDomainIsolation(t *testing.T) {
	ledger := &BandwidthLedger{
		reservedWorkers: make(map[string]int),
	}

	ledger.ReserveWorkers("wan", "a.com", 3)
	if got := ledger.ReservedWorkers("wan", "b.com"); got != 0 {
		t.Errorf("ReservedWorkers(wan, b.com) = %d, want 0 (domain isolation)", got)
	}
	if got := ledger.ReservedWorkers("lan", "a.com"); got != 0 {
		t.Errorf("ReservedWorkers(lan, a.com) = %d, want 0 (scope isolation)", got)
	}
}

func TestBandwidthLedger_ReserveWorkersNilSafety(t *testing.T) {
	var ledger *BandwidthLedger

	if got := ledger.ReservedWorkers("wan", "example.com"); got != 0 {
		t.Errorf("nil.ReservedWorkers = %d, want 0", got)
	}

	ledger.ReserveWorkers("wan", "example.com", 3) // should not panic
}

func TestBandwidthLedger_ReserveWorkersEmptyScopeDomain(t *testing.T) {
	ledger := &BandwidthLedger{
		reservedWorkers: make(map[string]int),
	}

	ledger.ReserveWorkers("", "example.com", 3)
	if got := ledger.ReservedWorkers("", "example.com"); got != 0 {
		t.Errorf("ReservedWorkers(empty scope) = %d, want 0", got)
	}

	ledger.ReserveWorkers("wan", "", 3)
	if got := ledger.ReservedWorkers("wan", ""); got != 0 {
		t.Errorf("ReservedWorkers(empty domain) = %d, want 0", got)
	}
}

func TestBandwidthLedger_ReleaseWorkersFloor(t *testing.T) {
	ledger := &BandwidthLedger{
		reservedWorkers: make(map[string]int),
	}

	ledger.ReserveWorkers("wan", "a.com", 3)
	ledger.ReleaseWorkers("wan", "a.com", 5) // release more than reserved
	if got := ledger.ReservedWorkers("wan", "a.com"); got != 0 {
		t.Errorf("ReservedWorkers after over-release = %d, want 0 (floor)", got)
	}
}
