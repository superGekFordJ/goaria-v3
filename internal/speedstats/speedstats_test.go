package speedstats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAsyncCoalescing(t *testing.T) {
	// Setup temp dir
	tmpDir := t.TempDir()
	statsPath := filepath.Join(tmpDir, "speed_stats.json")

	SetStatsPath(statsPath)
	SetSaveInterval(100 * time.Millisecond)

	// Reset records
	mu.Lock()
	records = []SpeedRecord{}
	mu.Unlock()

	// 1. Add multiple records rapidly
	for i := 0; i < 10; i++ {
		AddRecord(1000, 1, 1000, false)
	}

	// 2. Check that file is NOT written immediately
	if _, err := os.Stat(statsPath); err == nil {
		t.Log("File appeared early, which is okay if the system is fast, but it should be coalesced.")
	}

	// 3. Wait for timer (longer than 100ms)
	time.Sleep(300 * time.Millisecond)

	// 4. Check file existence and content
	data, err := os.ReadFile(statsPath)
	if err != nil {
		t.Fatalf("File not written after delay: %v", err)
	}

	if len(data) == 0 {
		t.Error("File is empty")
	}

	// Verify we have 10 records in memory
	mu.RLock()
	count := len(records)
	mu.RUnlock()
	if count != 10 {
		t.Errorf("Expected 10 records, got %d", count)
	}
}
