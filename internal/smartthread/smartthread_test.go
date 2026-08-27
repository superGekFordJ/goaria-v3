package smartthread

import (
	"math"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/speedstats"
)

func setupTestConfig(t *testing.T) {
	t.Helper()
	originalConfig := config.Get()
	t.Cleanup(func() {
		config.SetTestConfig(originalConfig)
	})
	config.SetTestConfig(&config.AppConfig{
		MinThreadLife: 5,
	})
}

func TestCalculate_CapMinSplitSize(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	tests := []struct {
		name           string
		fileSize       int64
		maxConnections int
		expectedMin    int64
	}{
		{
			name:           "16GB file with 8 max connections",
			fileSize:       16 * 1024 * 1024 * 1024, // 16GB
			maxConnections: 8,
			expectedMin:    1024 * 1024 * 1024, // should be capped at 1GB (1073741824 bytes)
		},
		{
			name:           "16GB file with 16 max connections",
			fileSize:       16 * 1024 * 1024 * 1024, // 16GB
			maxConnections: 16,
		},
		{
			name:           "Very small file",
			fileSize:       500 * 1024, // 500KB
			maxConnections: 8,
			expectedMin:    1024 * 1024, // should be at least 1MB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := Calculate(CalcParams{
				FileSize:       tt.fileSize,
				MaxConnections: tt.maxConnections,
			})
			if tt.expectedMin > 0 {
				if params.MinSize != tt.expectedMin {
					t.Errorf("expected MinSize %d, got %d", tt.expectedMin, params.MinSize)
				}
			}
			if params.MinSize > 1024*1024*1024 {
				t.Errorf("MinSize %d exceeds 1GB limit", params.MinSize)
			}
			if params.MinSize < 1024*1024 {
				t.Errorf("MinSize %d is less than 1MB minimum", params.MinSize)
			}
		})
	}
}

func TestCalculate_UnknownFileSize(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// No domain/scope → HasDomainScopeRecord returns false → exploration clamp to 4
	params := Calculate(CalcParams{
		FileSize:       0,
		MaxConnections: 8,
	})
	if params.Split != 4 {
		t.Errorf("Split = %d, want 4 (unknown size + new domain exploration clamp)", params.Split)
	}
	if params.MinSize != 0 {
		t.Errorf("MinSize = %d, want 0 for unknown file size", params.MinSize)
	}
	if params.TargetBandwidth != 0 {
		t.Errorf("TargetBandwidth = %d, want 0 for unknown file size", params.TargetBandwidth)
	}
}

func TestCalculate_ColdStart_LegacyFallback(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// No speed stats records → legacy fallback
	// No domain/scope specified → HasDomainScopeRecord returns false → exploration clamp
	params := Calculate(CalcParams{
		FileSize:       100 * 1024 * 1024, // 100MB
		MaxConnections: 8,
	})
	if params.Split != 4 {
		t.Errorf("Split = %d, want 4 (cold-start exploration clamp: max(8/4, 4) = 4)", params.Split)
	}
	if !params.IsExploration {
		t.Errorf("IsExploration = false, want true (no domain record)")
	}
	if params.MinSize < 1024*1024 {
		t.Errorf("MinSize = %d, want >= 1MB", params.MinSize)
	}
	if params.TargetBandwidth <= 0 {
		t.Errorf("TargetBandwidth = %d, want > 0", params.TargetBandwidth)
	}
}

func TestCalculate_BBR_BandwidthAware(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// V_thread_avg = 2MB/s (8MB/s peak / 4 threads)
	// V_global_peak = 8MB/s
	// V_domain_peak = 8MB/s
	speedstats.AddRecordV2(8000000, 4, 200*1024*1024, false, 100, "example.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:       1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections: 8,
		Scope:          "wan",
		EnvKey:         "testenv",
		Domain:         "example.com",
	})

	// V_target = min(8MB, 8MB) = 8MB
	// N_sat = ceil(8MB / 2MB) + 1 = 5
	// N_tmin = ceil(1GB / (2MB * 5)) = ceil(1GB / 10MB) = 103
	// N_final = min(5, 103, 8) = 5 (unless exploration triggers)
	if params.Split < 1 || params.Split > 8 {
		t.Errorf("Split = %d, want 1-8", params.Split)
	}
	if params.MinSize < 1024*1024 {
		t.Errorf("MinSize = %d, want >= 1MB", params.MinSize)
	}
	if params.TargetBandwidth <= 0 {
		t.Errorf("TargetBandwidth = %d, want > 0", params.TargetBandwidth)
	}
}

func TestCalculate_BBR_CongestionFloor(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// V_global_peak = 8MB/s, ReservedBandwidth = 8MB/s → V_available = 0
	speedstats.AddRecordV2(8000000, 4, 200*1024*1024, false, 100, "example.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:          1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections:    8,
		Scope:             "wan",
		EnvKey:            "testenv",
		Domain:            "example.com",
		ReservedBandwidth: 8000000, // fully saturated
	})

	// V_available = 0 → floor = 2
	if params.Split < 2 {
		t.Errorf("Split = %d, want >= 2 (congestion floor)", params.Split)
	}
}

func TestCalculate_BBR_NewDomain(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Records for wan scope but different domain
	speedstats.AddRecordV2(8000000, 4, 200*1024*1024, false, 100, "other.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:       500 * 1024 * 1024, // 500MB
		MaxConnections: 8,
		Scope:          "wan",
		EnvKey:         "testenv",
		Domain:         "newdomain.com", // no domain peak
	})

	// V_single_peak missing → V_target = V_available = V_global_peak
	// newdomain.com has no domain record → exploration clamp to 4
	if params.Split != 4 {
		t.Errorf("Split = %d, want 4 (new domain exploration clamp: max(8/4, 4) = 4)", params.Split)
	}
	if !params.IsExploration {
		t.Errorf("IsExploration = false, want true (new domain)")
	}
	if params.MinSize < 1024*1024 {
		t.Errorf("MinSize = %d, want >= 1MB", params.MinSize)
	}
}

func TestCalculate_BBR_MinSizeCapped(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// High V_thread_avg → MinChunk would be huge, should be capped at 1GB
	speedstats.AddRecordV2(8000000*1024, 1, 200*1024*1024, false, 100, "example.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:       100 * 1024 * 1024 * 1024, // 100GB
		MaxConnections: 8,
		Scope:          "wan",
		EnvKey:         "testenv",
		Domain:         "example.com",
	})

	if params.MinSize > 1024*1024*1024 {
		t.Errorf("MinSize = %d, exceeds 1GB cap", params.MinSize)
	}
}

func TestCalculate_Resume_RemainingSize(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Resume: fileSize = remaining = 50MB (out of 1GB total)
	params := Calculate(CalcParams{
		FileSize:       50 * 1024 * 1024, // 50MB remaining
		MaxConnections: 8,
	})

	// With no speedstats data → legacy fallback
	// N_tmin = ceil(50MB / (2MB * 5)) = ceil(50MB / 10MB) = 5
	if params.Split < 1 || params.Split > 8 {
		t.Errorf("Split = %d, want 1-8", params.Split)
	}
	if params.MinSize < 1024*1024 {
		t.Errorf("MinSize = %d, want >= 1MB", params.MinSize)
	}
}

func TestCalculate_DirtySampleClamp(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Single dirty sample: PeakSpeed=50KB/s with 8 threads → V_thread_avg = 6.25KB/s
	// This is well below the 100KB/s clamp threshold (minThreadEfficiency).
	// With only this record, GetRecentPeakByScope returns p75 = 6.25KB/s (n=1 ≡ median),
	// GetGlobalPeak returns 50KB/s, GetDomainPeak returns 50KB/s.
	speedstats.AddRecordV2(50000, 8, 200*1024*1024, false, 100, "example.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:       1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections: 8,
		Scope:          "wan",
		EnvKey:         "testenv",
		Domain:         "example.com",
	})

	// V_thread_avg = 6.25KB/s → clamped to 100KB/s (minThreadEfficiency).
	// V_global_peak = 50KB/s, V_domain_peak = 50KB/s
	// V_available = 50KB/s - 0 = 50KB/s
	// V_target = min(50KB/s, 50KB/s) = 50KB/s
	// N_sat = ceil(50KB / 100KB) + 1 = 1 + 1 = 2
	// N_tmin = ceil(1GB / (100KB * 5)) = ceil(1GB / 500KB) = 2099, clamped to 8
	// N_final = min(2, 8, 8) = 2
	// Without clamp: V_thread_avg = 6.25KB/s → N_sat = ceil(50KB/6.25KB)+1 = 9, clamped to 8
	//   N_tmin = ceil(1GB/(6.25KB*5)) = huge, clamped to 8 → N_final = 8
	// With clamp: N_final = 2 (because N_sat = 2 after clamp)
	// Key: clamp reduces N_sat from 9→2, proving the clamp is active.
	if params.Split != 2 {
		t.Errorf("Split = %d, want 2 (dirty sample clamped to 100KB/s → N_sat=2)", params.Split)
	}
}

func TestCalculate_BatchDegradation(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Seed: V_thread_avg = 2MB/s, V_global_peak = 8MB/s, V_domain_peak = 8MB/s
	speedstats.AddRecordV2(8000000, 4, 200*1024*1024, false, 100, "example.com", "wan", "testenv")

	// Simulate batch of 3 tasks on same scope using BandwidthLedger
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = noActiveBandwidth

	ledger := NewBandwidthLedger(nil)
	fileSize := int64(1 * 1024 * 1024 * 1024) // 1GB each

	var splits []int
	for range 3 {
		reserved := ledger.Reserved("wan", "testenv")
		params := Calculate(CalcParams{
			FileSize:          fileSize,
			MaxConnections:    8,
			Scope:             "wan",
			EnvKey:            "testenv",
			Domain:            "example.com",
			ReservedBandwidth: reserved,
		})
		splits = append(splits, params.Split)
		ledger.Reserve("wan", "testenv", params.TargetBandwidth)
	}

	// First task should get the most threads (full bandwidth available).
	// Later tasks should get fewer or equal as reserved bandwidth accumulates.
	if splits[0] < 1 {
		t.Fatalf("First task Split = %d, want >= 1", splits[0])
	}
	// Batch degradation: later tasks should not get more than the first
	for i := 1; i < len(splits); i++ {
		if splits[i] > splits[0] {
			t.Errorf("Task %d Split = %d > first task Split = %d (expected degradation)", i, splits[i], splits[0])
		}
	}
	// At least the last task should have fewer threads than the first
	// (V_available shrinks as we reserve, so N_sat should decrease)
	if splits[len(splits)-1] >= splits[0] {
		t.Errorf("Last task Split = %d, first = %d (expected last < first due to bandwidth reservation)", splits[len(splits)-1], splits[0])
	}
}

func TestCalculate_DomainIsolation_NoPollution(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = noActiveBandwidth

	// 3 slow.com records: 1MB/s, 1 thread → V_thread = 1MB/s each
	// 1 fast.com record: 67MB/s, 4 threads → V_thread = 16.75MB/s
	// Scope p75 of those four: [1MB, 1MB, 1MB, 16.75MB] → p75 = max = 16.75MB/s
	// Domain p75 for fast.com: 16.75MB/s (single sample, not polluted)
	for range 3 {
		speedstats.AddRecordV2(1*1024*1024, 1, 200*1024*1024, false, 100, "slow.com", "wan", "testenv")
	}
	speedstats.AddRecordV2(67*1024*1024, 4, 200*1024*1024, false, 100, "fast.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:       1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections: 8,
		Scope:          "wan",
		EnvKey:         "testenv",
		Domain:         "fast.com",
	})

	// With domain isolation: V_thread_avg = 16.75MB/s
	// V_target = min(67MB, 67MB) = 67MB
	// N_sat = ceil(67MB / 16.75MB) + 1 = 5
	// N_final = 5
	// Without isolation, scope p75 of the same four samples is also 16.75MB/s (max),
	// so the old "scope median=1MB → Split 8" counterfactual no longer applies under p75.
	if params.Split != 5 {
		t.Errorf("Split = %d, want 5 (domain-isolated V_thread_avg=16.75MB/s)", params.Split)
	}
}

func TestCalculate_NewDomainFallback_Penalty(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = noActiveBandwidth

	// known.com: 8MB/s, 4 threads → V_thread = 2MB/s (scope p75)
	speedstats.AddRecordV2(8*1024*1024, 4, 200*1024*1024, false, 100, "known.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:       500 * 1024 * 1024, // 500MB
		MaxConnections: 8,
		Scope:          "wan",
		EnvKey:         "testenv",
		Domain:         "unknown.com", // no domain record → fallback to scope with 0.5x penalty
	})

	// V_thread_avg 经过 0.5x 惩罚变为 1MB/s，理论 N_sat = 9
	// 但因无 domain 记录（新域名），触发冷启动保守防护（exploreLimit=4）
	// N_final 强行截断至 4，TargetBandwidth = 1MB/s * 4 = 4MB/s
	if params.Split != 4 {
		t.Errorf("Split = %d, want 4 (Conservative exploration clamp: max(8/4, 4) = 4)", params.Split)
	}
	if params.TargetBandwidth != 4*1024*1024 {
		t.Errorf("TargetBandwidth = %d, want %d (1MB/s * 4 = 4MB/s)", params.TargetBandwidth, 4*1024*1024)
	}
}

func TestCalculate_BatchMixedDomains(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = noActiveBandwidth

	// known.com: 8MB/s, 4 threads → V_thread = 2MB/s (domain and scope p75)
	speedstats.AddRecordV2(8*1024*1024, 4, 200*1024*1024, false, 100, "known.com", "wan", "testenv")

	ledger := NewBandwidthLedger(nil)
	fileSize := int64(100 * 1024 * 1024) // 100MB each

	domains := []string{"known.com", "unknown1.com", "unknown2.com"}
	var splits []int
	var bandwidths []int64

	for _, domain := range domains {
		reserved := ledger.Reserved("wan", "testenv")
		params := Calculate(CalcParams{
			FileSize:          fileSize,
			MaxConnections:    8,
			Scope:             "wan",
			EnvKey:            "testenv",
			Domain:            domain,
			ReservedBandwidth: reserved,
		})
		splits = append(splits, params.Split)
		bandwidths = append(bandwidths, params.TargetBandwidth)
		ledger.Reserve("wan", "testenv", params.TargetBandwidth)
	}

	// Task 0 (known.com): V_thread_avg = 2MB/s (domain-specific)
	// V_available = 8MB/s, V_target = min(8MB, 8MB) = 8MB/s
	// N_sat = ceil(8MB / 2MB) + 1 = 5, N_final = 5
	// targetBandwidth = 2MB/s * 5 = 10MB/s
	if splits[0] != 5 {
		t.Errorf("known.com Split = %d, want 5 (domain-specific V_thread_avg=2MB/s)", splits[0])
	}
	if bandwidths[0] != 10*1024*1024 {
		t.Errorf("known.com TargetBandwidth = %d, want %d", bandwidths[0], 10*1024*1024)
	}

	// Task 1 (unknown1.com): V_thread_avg = 1MB/s (0.5x penalized scope fallback)
	// V_available = 8MB/s - 10MB/s = 0 → floor = 2
	// N_final = 2, targetBandwidth = 1MB/s * 2 = 2MB/s
	if splits[1] != 2 {
		t.Errorf("unknown1.com Split = %d, want 2 (0.5x penalized fallback, congestion floor)", splits[1])
	}
	if bandwidths[1] != 2*1024*1024 {
		t.Errorf("unknown1.com TargetBandwidth = %d, want %d", bandwidths[1], 2*1024*1024)
	}

	// Task 2 (unknown2.com): same as task 1, V_available still 0
	if splits[2] != 2 {
		t.Errorf("unknown2.com Split = %d, want 2", splits[2])
	}
	if bandwidths[2] != 2*1024*1024 {
		t.Errorf("unknown2.com TargetBandwidth = %d, want %d", bandwidths[2], 2*1024*1024)
	}

	// Ledger per-scope accumulation: 10MB + 2MB + 2MB = 14MB
	totalReserved := ledger.Reserved("wan", "testenv")
	expected := int64(14 * 1024 * 1024)
	if totalReserved != expected {
		t.Errorf("ledger reserved(wan) = %d, want %d (10MB + 2MB + 2MB)", totalReserved, expected)
	}
}

func TestCalculate_CrossScopeIsolation(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = noActiveBandwidth

	// Same domain example.com, two scopes:
	// wan: 2MB/s, 1 thread → V_thread = 2MB/s
	// lan: 20MB/s, 1 thread → V_thread = 20MB/s
	speedstats.AddRecordV2(2*1024*1024, 1, 200*1024*1024, false, 100, "example.com", "wan", "testenv")
	speedstats.AddRecordV2(20*1024*1024, 1, 200*1024*1024, false, 100, "example.com", "lan", "testenv")

	params := Calculate(CalcParams{
		FileSize:       1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections: 8,
		Scope:          "wan",
		EnvKey:         "testenv",
		Domain:         "example.com",
	})

	// With cross-scope isolation: V_thread_avg = 2MB/s (wan only, not polluted by lan 20MB/s)
	// V_global_peak(wan) = 2MB/s, V_domain_peak(wan) = 2MB/s
	// V_available = 2MB/s, V_target = min(2MB, 2MB) = 2MB/s
	// N_sat = ceil(2MB / 2MB) + 1 = 2
	// N_tmin = ceil(1GB / (2MB * 5)) = 103 → clamped to 8
	// N_final = min(2, 8, 8) = 2
	// If polluted by lan: V_thread_avg = 20MB/s → N_sat = ceil(2MB/20MB)+1 = 1 → N_final = 1
	if params.Split != 2 {
		t.Errorf("Split = %d, want 2 (wan V_thread_avg=2MB/s; would be 1 if polluted by lan 20MB/s)", params.Split)
	}
}

func TestCalculate_DomainExhaustion_FloorOne(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// a.com: 8MB/s @ 8 threads → V_thread=1MB/s, V_single=8MB/s
	// big.com lifts global peak so domain exhaustion ≠ global congestion.
	speedstats.AddRecordV2(8*1024*1024, 8, 200*1024*1024, false, 100, "a.com", "wan", "testenv")
	speedstats.AddRecordV2(100*1024*1024, 1, 200*1024*1024, false, 100, "big.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:                1 * 1024 * 1024 * 1024,
		MaxConnections:          16,
		Scope:                   "wan",
		EnvKey:                  "testenv",
		Domain:                  "a.com",
		ReservedDomainBandwidth: 8 * 1024 * 1024,
	})
	if params.Split != 1 {
		t.Errorf("Split = %d, want 1 (domain exhausted, global healthy → floor 1 not congestionFloor 2)", params.Split)
	}
}

func TestCalculate_GlobalCongestion_FloorTwo(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	speedstats.AddRecordV2(8*1024*1024, 8, 200*1024*1024, false, 100, "a.com", "wan", "testenv")
	speedstats.AddRecordV2(100*1024*1024, 1, 200*1024*1024, false, 100, "big.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize:          1 * 1024 * 1024 * 1024,
		MaxConnections:    16,
		Scope:             "wan",
		EnvKey:            "testenv",
		Domain:            "a.com",
		ReservedBandwidth: 100 * 1024 * 1024,
	})
	if params.Split != congestionFloor {
		t.Errorf("Split = %d, want %d (global saturation → congestionFloor)", params.Split, congestionFloor)
	}
}

func TestCalculate_SameBatch_DomainReserve_Pattern9111(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	speedstats.AddRecordV2(8*1024*1024, 8, 200*1024*1024, false, 100, "a.com", "wan", "testenv")
	speedstats.AddRecordV2(100*1024*1024, 1, 200*1024*1024, false, 100, "big.com", "wan", "testenv")

	ledger := NewBandwidthLedger(nil)
	fileSize := int64(1 * 1024 * 1024 * 1024)
	var splits []int
	for range 4 {
		params := Calculate(CalcParams{
			FileSize:                fileSize,
			MaxConnections:          16,
			Scope:                   "wan",
			EnvKey:                  "testenv",
			Domain:                  "a.com",
			ReservedBandwidth:       ledger.Reserved("wan", "testenv"),
			ReservedDomainBandwidth: ledger.ReservedByDomain("wan", "a.com"),
		})
		splits = append(splits, params.Split)
		ledger.Reserve("wan", "testenv", params.TargetBandwidth)
		ledger.ReserveByDomain("wan", "a.com", params.TargetBandwidth)
	}

	if splits[0] != 9 {
		t.Fatalf("task0 Split = %d, want 9", splits[0])
	}
	for i := 1; i < 4; i++ {
		if splits[i] != 1 {
			t.Errorf("task%d Split = %d, want 1 (domain exhausted → floor 1)", i, splits[i])
		}
	}
}

func TestCalculate_DifferentDomainUnaffected(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	speedstats.AddRecordV2(8*1024*1024, 8, 200*1024*1024, false, 100, "a.com", "wan", "testenv")
	speedstats.AddRecordV2(8*1024*1024, 8, 200*1024*1024, false, 100, "b.com", "wan", "testenv")
	speedstats.AddRecordV2(100*1024*1024, 1, 200*1024*1024, false, 100, "big.com", "wan", "testenv")

	ledger := NewBandwidthLedger(nil)
	paramsA := Calculate(CalcParams{
		FileSize:                1 * 1024 * 1024 * 1024,
		MaxConnections:          16,
		Scope:                   "wan",
		EnvKey:                  "testenv",
		Domain:                  "a.com",
		ReservedDomainBandwidth: ledger.ReservedByDomain("wan", "a.com"),
	})
	ledger.ReserveByDomain("wan", "a.com", paramsA.TargetBandwidth)

	paramsB := Calculate(CalcParams{
		FileSize:                1 * 1024 * 1024 * 1024,
		MaxConnections:          16,
		Scope:                   "wan",
		EnvKey:                  "testenv",
		Domain:                  "b.com",
		ReservedDomainBandwidth: ledger.ReservedByDomain("wan", "b.com"),
	})
	if paramsB.Split != 9 {
		t.Errorf("b.com Split = %d, want 9 (unaffected by a.com domain reserve)", paramsB.Split)
	}
}

func TestExplorationLimit_Matrix(t *testing.T) {
	setupTestConfig(t)
	cases := []struct {
		wMax int
		want int
	}{
		{1, 4}, {3, 4}, {4, 4}, {31, 7}, {32, 8}, {35, 8}, {36, 8}, {64, 8}, {128, 8}, {256, 8},
	}
	for _, tc := range cases {
		if got := explorationLimit(tc.wMax); got != tc.want {
			t.Errorf("explorationLimit(%d) = %d, want %d", tc.wMax, got, tc.want)
		}
	}
	if explorationLimit(35) != explorationLimit(32) {
		t.Fatal("35 should still match old uncapped /4 until 36")
	}
	old36 := max(36/4, 4)
	if old36 != 9 {
		t.Fatalf("precondition: old cap at 36 is 9, got %d", old36)
	}
	if explorationLimit(36) != 8 {
		t.Fatal("first W_max changed by the 8-cap must be 36")
	}
}

func TestCalculate_ExplorationCap_UnknownLegacyBBR(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	for _, wMax := range []int{35, 36, 64, 128, 256} {
		unknown := Calculate(CalcParams{FileSize: 0, MaxConnections: wMax, Domain: "new.example", Scope: "wan", EnvKey: "e"})
		if !unknown.IsExploration {
			t.Fatalf("unknown size should explore")
		}
		if unknown.Split > explorationLimit(wMax) {
			t.Fatalf("unknown W_max=%d split %d > cap", wMax, unknown.Split)
		}
		if unknown.MinSize != 0 || unknown.TargetBandwidth != 0 || unknown.NSat != wMax {
			t.Fatalf("unknown-size extras: %+v", unknown)
		}

		legacy := Calculate(CalcParams{FileSize: 100 * 1024 * 1024, MaxConnections: wMax, Domain: "new.example", Scope: "wan", EnvKey: "e"})
		if !legacy.IsExploration || legacy.Split > explorationLimit(wMax) {
			t.Fatalf("legacy W_max=%d split %d", wMax, legacy.Split)
		}
	}

	speedstats.AddRecordV2(8*1024*1024, 8, 200*1024*1024, false, 100, "other.com", "wan", "testenv")
	for _, wMax := range []int{35, 36, 64, 128, 256} {
		bbr := Calculate(CalcParams{
			FileSize: 500 * 1024 * 1024, MaxConnections: wMax,
			Domain: "brand-new.com", Scope: "wan", EnvKey: "testenv",
		})
		if !bbr.IsExploration {
			t.Fatalf("BBR new domain should explore")
		}
		if bbr.Split > explorationLimit(wMax) {
			t.Fatalf("BBR W_max=%d split %d > cap", wMax, bbr.Split)
		}
		if bbr.TargetBandwidth < 0 {
			t.Fatalf("BBR TargetBandwidth negative")
		}
		if bbr.Split > 0 && bbr.TargetBandwidth == 0 {
			t.Fatalf("BBR capped split %d must recompute TargetBandwidth", bbr.Split)
		}
	}
}

func TestCalculate_ExplorationCap_KnownDomainUnaffected(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(8*1024*1024, 8, 200*1024*1024, false, 100, "known.com", "wan", "testenv")

	params := Calculate(CalcParams{
		FileSize: 1 * 1024 * 1024 * 1024, MaxConnections: 64,
		Domain: "known.com", Scope: "wan", EnvKey: "testenv",
	})
	if params.IsExploration {
		t.Fatal("known domain must not explore")
	}
	if params.Split < 1 || params.Split > 64 {
		t.Fatalf("known domain split %d outside [1,64]", params.Split)
	}
}

func TestCalculate_ExplorationCap_DoesNotRaiseBelowFour(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	unknown := Calculate(CalcParams{FileSize: 0, MaxConnections: 1, Domain: "x", Scope: "wan", EnvKey: "e"})
	if unknown.Split != 1 {
		t.Fatalf("W_max=1 unknown split=%d, want 1", unknown.Split)
	}
	// 2MiB cold start: N_tmin = ceil(2MiB / (2MiB/s * 5s)) = 1
	small := Calculate(CalcParams{FileSize: 2 * 1024 * 1024, MaxConnections: 8})
	if small.Split != 1 {
		t.Fatalf("2MiB cold start split=%d, want 1", small.Split)
	}
}

func TestCalculate_OverflowSafeMinThreadLife(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	config.SetTestConfig(&config.AppConfig{MinThreadLife: int(int64(^uint(0) >> 1))})

	params := Calculate(CalcParams{
		FileSize:       math.MaxInt64 / 4,
		MaxConnections: 256,
		Domain:         "huge.example",
		Scope:          "wan",
		EnvKey:         "e",
	})
	if params.Split < 1 || params.Split > 256 {
		t.Fatalf("split %d outside [1,256]", params.Split)
	}
}

func TestCalculate_DefaultAndUpperMaxConnections(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	zero := Calculate(CalcParams{FileSize: 0, MaxConnections: 0, Domain: "x", Scope: "wan", EnvKey: "e"})
	def := Calculate(CalcParams{FileSize: 0, MaxConnections: 16, Domain: "x", Scope: "wan", EnvKey: "e"})
	if zero.Split != def.Split || zero.NSat != 16 {
		t.Fatalf("zero W_max should default to 16: %+v vs %+v", zero, def)
	}
	huge := Calculate(CalcParams{FileSize: 0, MaxConnections: 999, Domain: "x", Scope: "wan", EnvKey: "e"})
	if huge.NSat != 256 {
		t.Fatalf("NSat=%d, want clamped 256", huge.NSat)
	}
}

func TestCalculate_LegacyNTminExample(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	// 20MB peak / 4 threads → 5MB/s V_thread_avg; extra record keeps global peak high so N_sat > N_tmin.
	speedstats.AddRecordV2(20*1024*1024, 4, 200*1024*1024, false, 100, "ntmin.example", "wan", "testenv")
	speedstats.AddRecordV2(200*1024*1024, 8, 200*1024*1024, false, 100, "bulk.example", "wan", "testenv")
	size := int64(7232) * 1024 * 1024 / 100
	params := Calculate(CalcParams{
		FileSize: size, MaxConnections: 16,
		Domain: "ntmin.example", Scope: "wan", EnvKey: "testenv",
	})
	// N_tmin = ceil(72.32MB / (5MB/s * 5s)) = 3
	if params.Split != 3 {
		t.Fatalf("N_tmin example split=%d, want 3 (params=%+v)", params.Split, params)
	}
}
