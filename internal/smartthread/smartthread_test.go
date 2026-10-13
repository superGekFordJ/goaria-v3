package smartthread

import (
	"math"
	"testing"
	"time"

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
		if bbr.TargetBandwidth != saturatingMulPositive(512*1024, int64(bbr.Split)) {
			t.Fatalf("BBR W_max=%d TargetBandwidth %d want %d * split %d",
				wMax, bbr.TargetBandwidth, 512*1024, bbr.Split)
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
	if params.Split <= explorationLimit(64) {
		t.Fatalf("known domain split %d must exceed 8-cap %d", params.Split, explorationLimit(64))
	}
	if params.Split > 64 {
		t.Fatalf("known domain split %d above W_max", params.Split)
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

func TestCalculate_NilPublishedConfigDoesNotPanic(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	config.SetTestConfig(nil)
	t.Cleanup(func() {
		cfg := config.DefaultConfig()
		config.SetTestConfig(&cfg)
	})
	params := Calculate(CalcParams{FileSize: 2 * 1024 * 1024, MaxConnections: 8})
	if params.Split < 1 {
		t.Fatalf("split %d", params.Split)
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

// injection stubs that leave the physical dimension unknown (no MACs).
func gateFixture() AdmissionParams {
	return AdmissionParams{
		Calc: CalcParams{
			FileSize:       50 << 20,
			MaxConnections: 16,
			Scope:          "wan",
			Domain:         "a.com",
			EnvKey:         "envA",
			Ledger:         NewBandwidthLedger(nil),
			ActiveMACsFunc: func() []string { return nil },
			ComputeEnvKeyFunc: func(route, mac string) string {
				return route + "|" + mac
			},
			MinThreadLife: 5,
		},
		Upper:                ThreadParams{Split: 8, MinSize: 1 << 20, TargetBandwidth: 0},
		Smart:                true,
		SizeKnown:            true,
		SlotHeadroom:         5,
		DomainWorkerHeadroom: 16,
	}
}

// seedEfficiency records one sample: domain p75 = peak/threads, domain peak
// and scope global peak = peak for (scope, env).
func seedEfficiency(t *testing.T, peak, threads int64, domain, scope, env string) {
	t.Helper()
	speedstats.AddRecordV2(peak, int(threads), 100<<20, false, 50, domain, scope, env)
}

// --- Off mode: manual params pass through; only slot headroom + basic input gate ---

func TestAdmissionGate_Off(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	upper := ThreadParams{Split: 4, MinSize: 1 << 20, TargetBandwidth: 2 << 20, IsExploration: true, NSat: 7}
	for _, headroom := range []int{1, 5, 8, 32} {
		p := AdmissionParams{
			Calc:         CalcParams{FileSize: 10 << 20}, // no injections at all
			Upper:        upper,
			Smart:        false,
			SizeKnown:    true,
			SlotHeadroom: headroom,
		}
		d := EvaluateAdmission(p)
		if !d.Allowed || d.Blocked != 0 || d.Short {
			t.Fatalf("headroom %d: decision = %+v", headroom, d)
		}
		if d.Params != upper {
			t.Fatalf("headroom %d: params = %+v, want exact upper %+v", headroom, d.Params, upper)
		}
	}

	// No slots → Global rejection with zero params.
	p := gateFixture()
	p.Smart = false
	p.SlotHeadroom = 0
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked != AdmissionBlockGlobal || d.Params != (ThreadParams{}) {
		t.Fatalf("slot=0: %+v", d)
	}
	// Invalid bounds → zero decision.
	for _, headroom := range []int{-1, 33} {
		p.SlotHeadroom = headroom
		if d := EvaluateAdmission(p); d != (AdmissionDecision{}) {
			t.Fatalf("headroom %d: %+v", headroom, d)
		}
	}
	p.SlotHeadroom = 5
	p.Upper.Split = 0
	if d := EvaluateAdmission(p); d != (AdmissionDecision{}) {
		t.Fatalf("split=0: %+v", d)
	}
	p.Upper.Split = 257
	if d := EvaluateAdmission(p); d != (AdmissionDecision{}) {
		t.Fatalf("split>256: %+v", d)
	}
}

// --- Smart input gates: scope/domain/injection requirements and local counts ---

func TestAdmissionGate_Counts(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Cold scope (no global peak for lan+envA) with a hot WAN must still
	// refuse past 5 commitments — scope+env local, not global.
	seedEfficiency(t, 4<<20, 4, "a.com", "wan", "envA")
	p := gateFixture()
	p.Calc.Scope = "lan"
	p.ScopeEnvCommitted = 5
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked != AdmissionBlockScope {
		t.Fatalf("cold scope committed=5: %+v", d)
	}
	p.ScopeEnvCommitted = 4
	// Lan scope unknown + no domain record → exploration; DomainCommitted is
	// env-blind, so 5 blocks even though envB holds no domain records.
	p.DomainCommitted = 5
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked != AdmissionBlockDomain {
		t.Fatalf("exploration committed=5: %+v", d)
	}
	p.DomainCommitted = 4
	if d := EvaluateAdmission(p); !d.Allowed || d.Blocked != 0 {
		t.Fatalf("cold scope under limits: %+v", d)
	}

	// A known (non-exploration) domain ignores the committed ceiling.
	speedstats.ResetRecordsForTest()
	seedEfficiency(t, 4<<20, 4, "a.com", "wan", "envA")
	p = gateFixture()
	p.DomainCommitted = 99
	if d := EvaluateAdmission(p); !d.Allowed {
		t.Fatalf("known domain over-committed: %+v", d)
	}

	// Smart requires injections and an explicit MinThreadLife.
	for _, mutate := range []func(*AdmissionParams){
		func(p *AdmissionParams) { p.Calc.Scope = "global" },
		func(p *AdmissionParams) { p.Calc.Domain = "" },
		func(p *AdmissionParams) { p.Calc.Ledger = nil },
		func(p *AdmissionParams) { p.Calc.ActiveMACsFunc = nil },
		func(p *AdmissionParams) { p.Calc.ComputeEnvKeyFunc = nil },
		func(p *AdmissionParams) { p.Calc.MinThreadLife = 0 },
		func(p *AdmissionParams) { p.ScopeEnvCommitted = -1 },
		func(p *AdmissionParams) { p.DomainWorkerHeadroom = -1 },
		func(p *AdmissionParams) { p.Calc.MaxConnections = -1 },
	} {
		q := gateFixture()
		mutate(&q)
		if d := EvaluateAdmission(q); d != (AdmissionDecision{}) {
			t.Fatalf("smart input gate: %+v", d)
		}
	}
}

// --- Connections: base headroom minus this round's reservations, once ---

func TestAdmissionGate_Connections(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	seedEfficiency(t, 8<<20, 4, "a.com", "wan", "envA") // eff=2MB/s, peaks=8MB/s

	p := gateFixture()
	// Base headroom zero rejects outright — no floor=1 rescue.
	p.DomainWorkerHeadroom = 0
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked != AdmissionBlockDomain {
		t.Fatalf("conn headroom 0: %+v", d)
	}

	// Headroom 1 is valid and tightens the suggestion to one worker.
	p.DomainWorkerHeadroom = 1
	d := EvaluateAdmission(p)
	if !d.Allowed || d.Params.Split != 1 {
		t.Fatalf("conn headroom 1: %+v", d)
	}

	// This round's ledger reservations deduct once on top of the base.
	p.DomainWorkerHeadroom = 5
	p.Calc.Ledger.ReserveWorkers("wan", "a.com", 3)
	d = EvaluateAdmission(p)
	if !d.Allowed || d.Params.Split != 2 {
		t.Fatalf("conn headroom 5 reserved 3: split=%d %+v", d.Params.Split, d)
	}
	// Zero-bandwidth reservations must not erase connection headroom.
	p.Calc.Ledger.Reserve("wan", "envA", 0)
	p.Calc.Ledger.ReserveByDomain("wan", "a.com", 0)
	if d := EvaluateAdmission(p); !d.Allowed || d.Params.Split != 2 {
		t.Fatalf("zero-bw reserve ate connections: %+v", d)
	}
	// The gate never writes the ledger: reservations unchanged.
	if got := p.Calc.Ledger.ReservedWorkers("wan", "a.com"); got != 3 {
		t.Fatalf("gate mutated ledger workers to %d", got)
	}
}

// --- Bandwidth: each known dimension is checked independently ---

func TestAdmissionGate_Bandwidth(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	// eff = 4MB/s per-thread, peaks 16MB/s on domain a.com wan envA.
	seedEfficiency(t, 16<<20, 4, "a.com", "wan", "envA")

	// All dimensions satisfied → ordinary allow with Target=eff×split.
	p := gateFixture()
	d := EvaluateAdmission(p)
	if !d.Allowed || d.Blocked != 0 || d.Short {
		t.Fatalf("clean allow: %+v", d)
	}
	wantTarget := saturatingMulPositive(4<<20, int64(d.Params.Split))
	if d.Params.TargetBandwidth != wantTarget {
		t.Fatalf("target = %d, want eff*split %d", d.Params.TargetBandwidth, wantTarget)
	}
	if d.Params.Split > p.Upper.Split || d.Params.MinSize > p.Upper.MinSize {
		t.Fatalf("suggestion widened upper: %+v vs %+v", d.Params, p.Upper)
	}

	// Scope exhausted (reserved ≥ global peak) → Scope block.
	p = gateFixture()
	p.Calc.Ledger.Reserve("wan", "envA", 16<<20)
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked != AdmissionBlockScope {
		t.Fatalf("scope exhausted: %+v", d)
	}

	// Domain exhausted → Domain block. Scope is not consulted for rescue.
	p = gateFixture()
	p.Calc.Ledger.ReserveByDomain("wan", "a.com", 16<<20)
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked != AdmissionBlockDomain {
		t.Fatalf("domain exhausted: %+v", d)
	}

	// Physical exhausted while logical dimensions stay free → Physical block.
	p = gateFixture()
	p.Calc.ActiveMACsFunc = func() []string { return []string{"mac1"} }
	p.Calc.ComputeEnvKeyFunc = func(route, mac string) string {
		if mac == "mac1" && route == "0" {
			return "envA"
		}
		return route + "|" + mac
	}
	p.Calc.Ledger.Reserve("wan", "envA", 16<<20) // eats the wan|direct bucket
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked&AdmissionBlockPhysical == 0 {
		t.Fatalf("physical exhausted: %+v", d)
	}

	// Unknown scope must not skip a known-exhausted domain.
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(16<<20, 4, 100<<20, false, 50, "a.com", "lan", "envB")
	p = gateFixture()
	p.Calc.Scope = "lan"
	p.Calc.EnvKey = "envB"
	p.DomainCommitted = 0
	p.Calc.Ledger.ReserveByDomain("lan", "a.com", 16<<20)
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked != AdmissionBlockDomain {
		t.Fatalf("unknown scope + exhausted domain: %+v", d)
	}
}

// --- Size semantics: known-zero vs unknown ---

func TestAdmissionGate_ZeroRemaining(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	seedEfficiency(t, 16<<20, 4, "a.com", "wan", "envA")

	// Known size 0 is an empty/checkpoint candidate: one thread, no unknown
	// branch, MinSize inherits Upper.
	p := gateFixture()
	p.Calc.FileSize = 0
	d := EvaluateAdmission(p)
	if !d.Allowed || d.Params.Split != 1 || d.Params.NSat != 1 || d.Params.MinSize != p.Upper.MinSize {
		t.Fatalf("known zero remaining: %+v", d)
	}
	if d.Params.TargetBandwidth != 4<<20 {
		t.Fatalf("known-zero target = %d, want eff 4MB/s", d.Params.TargetBandwidth)
	}

	// Unknown size uses the exploration/upper clamp path and still gets a
	// saturating efficiency×split target — never a fake "complete".
	p = gateFixture()
	p.SizeKnown = false
	p.Calc.FileSize = 0
	d = EvaluateAdmission(p)
	if !d.Allowed {
		t.Fatalf("unknown size rejected: %+v", d)
	}
	if d.Params.TargetBandwidth != saturatingMulPositive(4<<20, int64(d.Params.Split)) {
		t.Fatalf("unknown target = %d split=%d", d.Params.TargetBandwidth, d.Params.Split)
	}

	// Mismatched size flags are invalid input.
	p = gateFixture()
	p.SizeKnown = false
	p.Calc.FileSize = 10 << 20
	if d := EvaluateAdmission(p); d != (AdmissionDecision{}) {
		t.Fatalf("unknown flag with nonzero size: %+v", d)
	}
	p = gateFixture()
	p.Calc.FileSize = -5
	if d := EvaluateAdmission(p); d != (AdmissionDecision{}) {
		t.Fatalf("negative known size: %+v", d)
	}
}

// --- Short-candidate exception ---

func TestAdmissionGate_Short(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	// eff = 1MB/s (4MB peak / 4 threads) → short window = 15MB.
	seedEfficiency(t, 4<<20, 4, "a.com", "wan", "envA")
	const shortCap = int64(1 << 20 * 15)

	// Scope exhausted + qualified short candidate at the threshold → allowed,
	// single thread, Target=eff, Blocked retains the real mask.
	p := gateFixture()
	p.Calc.FileSize = shortCap
	p.ShortAvailable = true
	p.Calc.Ledger.Reserve("wan", "envA", 4<<20)
	d := EvaluateAdmission(p)
	if !d.Allowed || !d.Short || d.Blocked != AdmissionBlockScope {
		t.Fatalf("short at threshold: %+v", d)
	}
	if d.Params.Split != 1 || d.Params.TargetBandwidth != 1<<20 {
		t.Fatalf("short params = %+v", d.Params)
	}

	// One byte over the threshold → no exception.
	p.Calc.FileSize = shortCap + 1
	if d := EvaluateAdmission(p); d.Allowed || d.Blocked != AdmissionBlockScope || d.Short {
		t.Fatalf("short over threshold: %+v", d)
	}

	// Domain block is not exemptible.
	p = gateFixture()
	p.Calc.FileSize = shortCap
	p.ShortAvailable = true
	p.Calc.Ledger.ReserveByDomain("wan", "a.com", 4<<20)
	if d := EvaluateAdmission(p); d.Allowed || d.Short {
		t.Fatalf("short with domain block: %+v", d)
	}

	// No short qualification → plain rejection.
	p.Calc.Ledger = NewBandwidthLedger(nil)
	p.Calc.Ledger.Reserve("wan", "envA", 4<<20)
	p.ShortAvailable = false
	if d := EvaluateAdmission(p); d.Allowed || d.Short {
		t.Fatalf("unqualified short: %+v", d)
	}

	// Unknown size never qualifies.
	p.ShortAvailable = true
	p.SizeKnown = false
	p.Calc.FileSize = 0
	if d := EvaluateAdmission(p); d.Allowed || d.Short {
		t.Fatalf("unknown-size short: %+v", d)
	}

	// Non-exempt earlier gates still decide first: zero connection headroom
	// blocks even a fully-qualified short candidate.
	p = gateFixture()
	p.Calc.FileSize = shortCap
	p.ShortAvailable = true
	p.DomainWorkerHeadroom = 0
	p.Calc.Ledger.Reserve("wan", "envA", 4<<20)
	if d := EvaluateAdmission(p); d.Allowed || d.Short || d.Blocked != AdmissionBlockDomain {
		t.Fatalf("short bypassed conn gate: %+v", d)
	}
}

// --- Internal resource helper coverage ---

func TestAdmissionResources_EfficiencyChain(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// No history → floor.
	res := admissionResourcesFor(gateFixture().Calc)
	if res.threadEfficiency != minThreadEfficiency {
		t.Fatalf("no-history eff = %d", res.threadEfficiency)
	}
	if !res.exploration {
		t.Fatal("no domain record must mark exploration")
	}

	// Scope-only history → halved scope p75.
	speedstats.AddRecordV2(8<<20, 4, 100<<20, false, 50, "other.com", "wan", "envA")
	res = admissionResourcesFor(gateFixture().Calc)
	if res.threadEfficiency != 1<<20 { // (8MB/4)/2
		t.Fatalf("scope-fallback eff = %d, want 1MB/s", res.threadEfficiency)
	}
	if !res.exploration {
		t.Fatal("domain-without-record must stay exploration")
	}

	// Domain record wins over the halved scope fallback.
	seedEfficiency(t, 12<<20, 4, "a.com", "wan", "envA")
	res = admissionResourcesFor(gateFixture().Calc)
	if res.threadEfficiency != 3<<20 {
		t.Fatalf("domain eff = %d, want 3MB/s", res.threadEfficiency)
	}
	if res.exploration {
		t.Fatal("domain record must clear exploration")
	}
}

func TestAdmissionResources_Dimensions(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	seedEfficiency(t, 16<<20, 4, "a.com", "wan", "envA")

	calc := gateFixture().Calc
	res := admissionResourcesFor(calc)
	if !res.scopeKnown || res.scopeAvailable != 16<<20 {
		t.Fatalf("scope = %+v", res)
	}
	if !res.domainKnown || res.domainAvailable != 16<<20 {
		t.Fatalf("domain = %+v", res)
	}
	if res.physicalKnown {
		t.Fatal("no-MACs physical must stay unknown")
	}

	// Ledger reservations subtract symmetrically per dimension.
	calc.Ledger.Reserve("wan", "envA", 6<<20)
	calc.Ledger.ReserveByDomain("wan", "a.com", 6<<20)
	res = admissionResourcesFor(calc)
	if res.scopeAvailable != 10<<20 || res.domainAvailable != 10<<20 {
		t.Fatalf("reserved dims = %+v", res)
	}

	// Physical: matched NIC reports the 4-bucket max minus total reserved.
	calc.ActiveMACsFunc = func() []string { return []string{"mac1"} }
	calc.ComputeEnvKeyFunc = func(route, mac string) string {
		if mac == "mac1" && route == "0" {
			return "envA"
		}
		return route + "|" + mac
	}
	res = admissionResourcesFor(calc)
	if !res.physicalKnown || res.physicalAvailable != 10<<20 {
		t.Fatalf("physical = %+v", res)
	}

	// Collision failure → physical unknown again (stale envKey).
	calc.EnvKey = "stale-env"
	res = admissionResourcesFor(calc)
	if res.physicalKnown {
		t.Fatal("collision-failed physical must be unknown")
	}
}

// Lease termination on the explicit reservation branch: live → Target floor;
// expired/no-anchor → telemetry only (never a legacy waiting/compat claim).
func TestAdmissionResources_LeaseBoundary(t *testing.T) {
	now := time.Now()
	mk := func(status string, activatedAt, firstProgress time.Time) TrackedTaskInfo {
		return TrackedTaskInfo{
			GID:               "sg_le",
			Status:            status,
			AdmissionReserved: true,
			TargetBandwidth:   3_000_000,
			ActivatedAt:       activatedAt,
			FirstProgressAt:   firstProgress,
		}
	}

	// Live lease (44s) claims the floor; expiry (46s) drops to zero.
	if got := TaskOccupancy(mk("waiting", now.Add(-44*time.Second), time.Time{}), now); got != 3_000_000 {
		t.Fatalf("live lease occupancy = %d", got)
	}
	if got := TaskOccupancy(mk("waiting", now.Add(-46*time.Second), time.Time{}), now); got != 0 {
		t.Fatalf("expired lease occupancy = %d, want 0 (no legacy fallback)", got)
	}
	// No anchor → zero even though a Target is recorded.
	if got := TaskOccupancy(mk("waiting", time.Time{}, time.Time{}), now); got != 0 {
		t.Fatalf("anchor-less reservation = %d", got)
	}
	// Paused pending contributes zero.
	if got := TaskOccupancy(mk("paused", now.Add(-10*time.Second), time.Time{}), now); got != 0 {
		t.Fatalf("paused reservation = %d", got)
	}
	// Abnormal active+pending keeps S1 telemetry on expiry, floor while live.
	m := mk("active", now.Add(-46*time.Second), time.Time{})
	m.TelemetryBps, m.TelemetryValid = 700_000, true
	if got := TaskOccupancy(m, now); got != 700_000 {
		t.Fatalf("active+expired = %d, want telemetry", got)
	}
	m.ActivatedAt = now.Add(-10 * time.Second)
	if got := TaskOccupancy(m, now); got != 3_000_000 {
		t.Fatalf("active+live = %d, want max(target,telem)", got)
	}
	// First-progress extension: growth inside the 45s window extends the
	// lease to 15s past that growth (activated−58s, first progress−14s →
	// alive until +1s); growth at/after 45s or past the progress window dies.
	if got := TaskOccupancy(mk("waiting", now.Add(-58*time.Second), now.Add(-14*time.Second)), now); got != 3_000_000 {
		t.Fatalf("44s-growth extended lease = %d", got)
	}
	if got := TaskOccupancy(mk("waiting", now.Add(-60*time.Second), now.Add(-20*time.Second)), now); got != 0 {
		t.Fatalf("past progress window = %d", got)
	}
	if got := TaskOccupancy(mk("waiting", now.Add(-61*time.Second), now.Add(-16*time.Second)), now); got != 0 {
		t.Fatalf("growth at/after 45s must not revive the lease = %d", got)
	}
}

// Ledger reservation accumulation saturates instead of overflowing.
func TestAdmissionResources_LedgerSaturation(t *testing.T) {
	l := NewBandwidthLedger(nil)
	l.Reserve("wan", "envA", math.MaxInt64)
	l.Reserve("wan", "envA", 5)
	if got := l.Reserved("wan", "envA"); got != math.MaxInt64 {
		t.Fatalf("reserved = %d, want saturated MaxInt64", got)
	}
	l.ReserveByDomain("wan", "a.com", math.MaxInt64)
	l.ReserveByDomain("wan", "a.com", 5)
	if got := l.ReservedByDomain("wan", "a.com"); got != math.MaxInt64 {
		t.Fatalf("domain reserved = %d", got)
	}
	l.ReserveWorkers("wan", "a.com", math.MaxInt)
	l.ReserveWorkers("wan", "a.com", 5)
	if got := l.ReservedWorkers("wan", "a.com"); got != math.MaxInt {
		t.Fatalf("workers = %d", got)
	}
}

// Explicit MinThreadLife overrides the global config; zero keeps it.
func TestAdmissionResources_MinThreadLife(t *testing.T) {
	// Large file + low efficiency → nTmin dominates: fileSize/(eff*tMin).
	// eff = 1MB/s; fileSize = 120MB → tMin=5 → 24; tMin=60 → 2.
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	seedEfficiency(t, 4<<20, 4, "a.com", "wan", "envA")

	base := CalcParams{
		FileSize:       120 << 20,
		MaxConnections: 256,
		Scope:          "wan",
		Domain:         "a.com",
		EnvKey:         "envA",
	}

	fast := base
	fast.MinThreadLife = 5
	slow := base
	slow.MinThreadLife = 60
	if gotFast, gotSlow := Calculate(fast).Split, Calculate(slow).Split; gotFast <= gotSlow {
		t.Fatalf("MinThreadLife not honored: tMin5 split=%d tMin60 split=%d", gotFast, gotSlow)
	}
	// 0 keeps the config/default path — identical to an unset field.
	zero := base
	zero.MinThreadLife = 0
	if Calculate(zero).Split != Calculate(base).Split {
		t.Fatal("MinThreadLife=0 changed legacy behavior")
	}
}
