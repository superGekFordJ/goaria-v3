package smartthread

import (
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
			Scope:           "wan",
			EnvKey:          "env1",
			Domain:          "a.com",
			TargetBandwidth: 8_000_000,
			TelemetryBps:    1_500_000,
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
			Scope:           "wan",
			EnvKey:          "env1",
			Domain:          "a.com",
			TargetBandwidth: 5_000_000,
			TelemetryBps:    200_000,
			AllocatedAt:     time.Now(),
		},
	})
	if got := young.Reserved("wan", "env1"); got != 5_000_000 {
		t.Errorf("young Reserved = %d, want 5000000", got)
	}

	aged := NewBandwidthLedger([]TrackedTaskInfo{
		{
			GID:             "ar_1",
			Scope:           "wan",
			EnvKey:          "env1",
			Domain:          "a.com",
			TargetBandwidth: 5_000_000,
			TelemetryBps:    200_000,
			AllocatedAt:     time.Now().Add(-aria2ColdSeedWindow - time.Second),
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
	activeBandwidthProvider = func(scope, envKey string) int64 {
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
	for i := 0; i < n; i++ {
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
