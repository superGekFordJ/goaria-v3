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
