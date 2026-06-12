package smartthread

import (
	"testing"

	"goaria-v3/internal/config"
)

func TestCalculate_CapMinSplitSize(t *testing.T) {
	// Backup original config and restore it after test execution
	originalConfig := config.Current
	defer func() {
		config.Current = originalConfig
	}()

	config.Current = &config.AppConfig{
		MinThreadLife: 5,
	}

	tests := []struct {
		name           string
		fileSize       int64
		maxConnections int
		url            string
		expectedSplit  int
		expectedMin    int64
	}{
		{
			name:           "16GB file with 8 max connections",
			fileSize:       16 * 1024 * 1024 * 1024, // 16GB
			maxConnections: 8,
			url:            "https://example.com/largefile.zip",
			expectedMin:    1024 * 1024 * 1024, // should be capped at 1GB (1073741824 bytes)
		},
		{
			name:           "16GB file with 16 max connections",
			fileSize:       16 * 1024 * 1024 * 1024, // 16GB
			maxConnections: 16,
			url:            "https://example.com/largefile.zip",
			// 17179869184 / 16 * 0.99 = 1062904396 bytes, which is < 1GB, so it shouldn't be capped (unless exploration triggers and splits)
		},
		{
			name:           "Very small file",
			fileSize:       500 * 1024, // 500KB
			maxConnections: 8,
			url:            "https://example.com/small.zip",
			expectedMin:    1024 * 1024, // should be at least 1MB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := Calculate(tt.fileSize, tt.maxConnections, tt.url)
			if tt.expectedMin > 0 {
				if params.MinSize != tt.expectedMin {
					t.Errorf("expected MinSize %d, got %d", tt.expectedMin, params.MinSize)
				}
			}
			// Verify it does not exceed 1GB (1024M) in any case
			if params.MinSize > 1024*1024*1024 {
				t.Errorf("MinSize %d exceeds 1GB limit", params.MinSize)
			}
			// Verify it is at least 1MB
			if params.MinSize < 1024*1024 {
				t.Errorf("MinSize %d is less than 1MB minimum", params.MinSize)
			}
		})
	}
}
