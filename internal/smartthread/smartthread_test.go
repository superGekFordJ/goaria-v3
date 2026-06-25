package smartthread

import (
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/speedstats"
)

func setupTestConfig(t *testing.T) {
	t.Helper()
	originalConfig := config.Current
	t.Cleanup(func() {
		config.Current = originalConfig
	})
	config.Current = &config.AppConfig{
		MinThreadLife: 5,
	}
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

	params := Calculate(CalcParams{
		FileSize:       0,
		MaxConnections: 8,
	})
	if params.Split != 8 {
		t.Errorf("Split = %d, want 8 for unknown file size", params.Split)
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
	params := Calculate(CalcParams{
		FileSize:       100 * 1024 * 1024, // 100MB
		MaxConnections: 8,
	})
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

func TestCalculate_BBR_BandwidthAware(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// V_thread_avg = 2MB/s (8MB/s peak / 4 threads)
	// V_global_peak = 8MB/s
	// V_domain_peak = 8MB/s
	speedstats.AddRecordV2(8000000, 4, 200*1024*1024, false, 100, "example.com", "wan")

	params := Calculate(CalcParams{
		FileSize:       1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections: 8,
		Scope:          "wan",
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
	speedstats.AddRecordV2(8000000, 4, 200*1024*1024, false, 100, "example.com", "wan")

	params := Calculate(CalcParams{
		FileSize:          1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections:    8,
		Scope:             "wan",
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
	speedstats.AddRecordV2(8000000, 4, 200*1024*1024, false, 100, "other.com", "wan")

	params := Calculate(CalcParams{
		FileSize:       500 * 1024 * 1024, // 500MB
		MaxConnections: 8,
		Scope:          "wan",
		Domain:         "newdomain.com", // no domain peak
	})

	// V_single_peak missing → V_target = V_available = V_global_peak
	// Should still produce valid params
	if params.Split < 1 || params.Split > 8 {
		t.Errorf("Split = %d, want 1-8", params.Split)
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
	speedstats.AddRecordV2(8000000*1024, 1, 200*1024*1024, false, 100, "example.com", "wan")

	params := Calculate(CalcParams{
		FileSize:       100 * 1024 * 1024 * 1024, // 100GB
		MaxConnections: 8,
		Scope:          "wan",
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
	// With only this record, GetRecentPeakByScope returns median = 6.25KB/s,
	// GetGlobalPeak returns 50KB/s, GetDomainPeak returns 50KB/s.
	speedstats.AddRecordV2(50000, 8, 200*1024*1024, false, 100, "example.com", "wan")

	params := Calculate(CalcParams{
		FileSize:       1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections: 8,
		Scope:          "wan",
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
	speedstats.AddRecordV2(8000000, 4, 200*1024*1024, false, 100, "example.com", "wan")

	// Simulate batch of 3 tasks on same scope using BandwidthLedger
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = noActiveBandwidth

	ledger := NewBandwidthLedger()
	fileSize := int64(1 * 1024 * 1024 * 1024) // 1GB each

	var splits []int
	for i := 0; i < 3; i++ {
		reserved := ledger.Reserved("wan")
		params := Calculate(CalcParams{
			FileSize:          fileSize,
			MaxConnections:    8,
			Scope:             "wan",
			Domain:            "example.com",
			ReservedBandwidth: reserved,
		})
		splits = append(splits, params.Split)
		ledger.Reserve("wan", params.TargetBandwidth)
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
	// Scope median (polluted): [1MB, 1MB, 1MB, 16.75MB] → median = 1MB/s
	// Domain median for fast.com: 16.75MB/s (not polluted)
	for i := 0; i < 3; i++ {
		speedstats.AddRecordV2(1*1024*1024, 1, 200*1024*1024, false, 100, "slow.com", "wan")
	}
	speedstats.AddRecordV2(67*1024*1024, 4, 200*1024*1024, false, 100, "fast.com", "wan")

	params := Calculate(CalcParams{
		FileSize:       1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections: 8,
		Scope:          "wan",
		Domain:         "fast.com",
	})

	// With domain isolation: V_thread_avg = 16.75MB/s
	// V_target = min(67MB, 67MB) = 67MB
	// N_sat = ceil(67MB / 16.75MB) + 1 = 5
	// N_final = 5
	// Without isolation (scope median = 1MB/s): N_sat = ceil(67MB / 1MB) + 1 = 68 → clamped to 8
	if params.Split != 5 {
		t.Errorf("Split = %d, want 5 (domain-isolated V_thread_avg=16.75MB/s; would be 8 if polluted by scope median=1MB/s)", params.Split)
	}
}

func TestCalculate_NewDomainFallback_Penalty(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = noActiveBandwidth

	// known.com: 8MB/s, 4 threads → V_thread = 2MB/s (scope median)
	speedstats.AddRecordV2(8*1024*1024, 4, 200*1024*1024, false, 100, "known.com", "wan")

	params := Calculate(CalcParams{
		FileSize:       500 * 1024 * 1024, // 500MB
		MaxConnections: 8,
		Scope:          "wan",
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

	// known.com: 8MB/s, 4 threads → V_thread = 2MB/s (domain and scope median)
	speedstats.AddRecordV2(8*1024*1024, 4, 200*1024*1024, false, 100, "known.com", "wan")

	ledger := NewBandwidthLedger()
	fileSize := int64(100 * 1024 * 1024) // 100MB each

	domains := []string{"known.com", "unknown1.com", "unknown2.com"}
	var splits []int
	var bandwidths []int64

	for _, domain := range domains {
		reserved := ledger.Reserved("wan")
		params := Calculate(CalcParams{
			FileSize:          fileSize,
			MaxConnections:    8,
			Scope:             "wan",
			Domain:            domain,
			ReservedBandwidth: reserved,
		})
		splits = append(splits, params.Split)
		bandwidths = append(bandwidths, params.TargetBandwidth)
		ledger.Reserve("wan", params.TargetBandwidth)
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
	totalReserved := ledger.Reserved("wan")
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
	speedstats.AddRecordV2(2*1024*1024, 1, 200*1024*1024, false, 100, "example.com", "wan")
	speedstats.AddRecordV2(20*1024*1024, 1, 200*1024*1024, false, 100, "example.com", "lan")

	params := Calculate(CalcParams{
		FileSize:       1 * 1024 * 1024 * 1024, // 1GB
		MaxConnections: 8,
		Scope:          "wan",
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
