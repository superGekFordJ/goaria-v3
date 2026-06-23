package speedstats

import (
	"bytes"
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveAsyncCoalescing(t *testing.T) {
	tmpDir := t.TempDir()
	statsPath := filepath.Join(tmpDir, "speed_stats.gob")

	SetStatsPath(statsPath)
	SetSaveInterval(100 * time.Millisecond)

	mu.Lock()
	records = []SpeedRecord{}
	mu.Unlock()

	for i := 0; i < 10; i++ {
		AddRecord(1000, 1, 1000, false)
	}

	if _, err := os.Stat(statsPath); err == nil {
		t.Log("File appeared early, which is okay if the system is fast, but it should be coalesced.")
	}

	time.Sleep(300 * time.Millisecond)

	data, err := os.ReadFile(statsPath)
	if err != nil {
		t.Fatalf("File not written after delay: %v", err)
	}

	if len(data) == 0 {
		t.Error("File is empty")
	}

	var loaded []SpeedRecord
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&loaded); err != nil {
		t.Fatalf("Failed to gob-decode: %v", err)
	}
	if len(loaded) != 10 {
		t.Errorf("Expected 10 records in gob file, got %d", len(loaded))
	}

	mu.RLock()
	count := len(records)
	mu.RUnlock()
	if count != 10 {
		t.Errorf("Expected 10 records in memory, got %d", count)
	}
}

func TestAddRecordV2(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(100 * time.Millisecond)

	mu.Lock()
	records = []SpeedRecord{}
	mu.Unlock()

	AddRecordV2(5000000, 8, 100000000, false, 120, "example.com", "wan")

	mu.RLock()
	if len(records) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(records))
	}
	r := records[0]
	mu.RUnlock()

	if r.TTFBMs != 120 {
		t.Errorf("TTFBMs = %d, want 120", r.TTFBMs)
	}
	if r.Domain != "example.com" {
		t.Errorf("Domain = %s, want example.com", r.Domain)
	}
	if r.Scope != "wan" {
		t.Errorf("Scope = %s, want wan", r.Scope)
	}
}

func TestAddRecordV2_DefaultScope(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(100 * time.Millisecond)

	mu.Lock()
	records = []SpeedRecord{}
	mu.Unlock()

	AddRecordV2(5000000, 8, 100000000, false, 0, "test.com", "")

	mu.RLock()
	r := records[0]
	mu.RUnlock()

	if r.Scope != "wan" {
		t.Errorf("Default scope = %s, want wan", r.Scope)
	}
}

func TestAddRecord_BackwardCompat(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(100 * time.Millisecond)

	mu.Lock()
	records = []SpeedRecord{}
	mu.Unlock()

	AddRecord(5000000, 4, 100000000, true)

	mu.RLock()
	r := records[0]
	mu.RUnlock()

	if r.TTFBMs != 0 {
		t.Errorf("TTFBMs = %d, want 0 (backward compat)", r.TTFBMs)
	}
	if r.Domain != "" {
		t.Errorf("Domain = %s, want empty (backward compat)", r.Domain)
	}
	if r.Scope != "wan" {
		t.Errorf("Scope = %s, want wan (default)", r.Scope)
	}
}

func TestGobRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	statsPath := filepath.Join(tmpDir, "speed_stats.gob")
	SetStatsPath(statsPath)
	SetSaveInterval(100 * time.Millisecond)

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: 1000, PeakSpeed: 5000, ThreadCount: 8, FileSize: 100000000, IsExploration: false, TTFBMs: 150, Domain: "a.com", Scope: "wan"},
		{Timestamp: 2000, PeakSpeed: 8000, ThreadCount: 16, FileSize: 200000000, IsExploration: true, TTFBMs: 80, Domain: "b.com", Scope: "lan"},
	}
	mu.Unlock()

	if err := Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	mu.Lock()
	records = nil
	mu.Unlock()

	Load()

	mu.RLock()
	loaded := records
	mu.RUnlock()

	if len(loaded) != 2 {
		t.Fatalf("Expected 2 records after round-trip, got %d", len(loaded))
	}
	if loaded[0].Domain != "a.com" || loaded[0].TTFBMs != 150 || loaded[0].Scope != "wan" {
		t.Errorf("Record 0 mismatch: %+v", loaded[0])
	}
	if loaded[1].Domain != "b.com" || loaded[1].TTFBMs != 80 || loaded[1].Scope != "lan" {
		t.Errorf("Record 1 mismatch: %+v", loaded[1])
	}
}

func TestGetGlobalPeak_ByScope(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: time.Now().Unix(), PeakSpeed: 10000000, ThreadCount: 8, Scope: "wan", Domain: "a.com"},
		{Timestamp: time.Now().Unix(), PeakSpeed: 5000000, ThreadCount: 4, Scope: "lan", Domain: "b.com"},
		{Timestamp: time.Now().Unix(), PeakSpeed: 20000000, ThreadCount: 16, Scope: "wan", Domain: "c.com"},
	}
	mu.Unlock()

	peak, ok := GetGlobalPeak("wan")
	if !ok {
		t.Fatal("Expected ok=true")
	}
	if peak != 20000000 {
		t.Errorf("GetGlobalPeak(wan) = %d, want 20000000", peak)
	}

	peak, ok = GetGlobalPeak("lan")
	if !ok {
		t.Fatal("Expected ok=true for lan")
	}
	if peak != 5000000 {
		t.Errorf("GetGlobalPeak(lan) = %d, want 5000000", peak)
	}

	peak, ok = GetGlobalPeak("")
	if !ok {
		t.Fatal("Expected ok=true for no scope")
	}
	if peak != 20000000 {
		t.Errorf("GetGlobalPeak() = %d, want 20000000", peak)
	}
}

func TestGetDomainPeak(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: 1000, PeakSpeed: 10000000, Domain: "example.com"},
		{Timestamp: 2000, PeakSpeed: 30000000, Domain: "example.com"},
		{Timestamp: 3000, PeakSpeed: 5000000, Domain: "other.com"},
	}
	mu.Unlock()

	peak, ok := GetDomainPeak("example.com")
	if !ok {
		t.Fatal("Expected ok=true")
	}
	if peak != 30000000 {
		t.Errorf("GetDomainPeak(example.com) = %d, want 30000000", peak)
	}

	_, ok = GetDomainPeak("nonexistent.com")
	if ok {
		t.Error("Expected ok=false for nonexistent domain")
	}
}

func TestGetRTprop(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: 1000, TTFBMs: 200, Domain: "example.com"},
		{Timestamp: 2000, TTFBMs: 100, Domain: "example.com"},
		{Timestamp: 3000, TTFBMs: 0, Domain: "example.com"},
		{Timestamp: 4000, TTFBMs: 50, Domain: "other.com"},
	}
	mu.Unlock()

	rtt, ok := GetRTprop("example.com")
	if !ok {
		t.Fatal("Expected ok=true")
	}
	if rtt != 100 {
		t.Errorf("GetRTprop(example.com) = %d, want 100 (min, skip 0)", rtt)
	}

	rtt, ok = GetRTprop("nomatch.com")
	if !ok {
		t.Fatal("Expected ok=true (global fallback)")
	}
	if rtt != 50 {
		t.Errorf("GetRTprop(nomatch.com) global fallback = %d, want 50", rtt)
	}
}

func TestGetRTprop_NoTTFBRecords(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: 1000, TTFBMs: 0, Domain: "a.com"},
	}
	mu.Unlock()

	_, ok := GetRTprop("a.com")
	if ok {
		t.Error("Expected ok=false when all TTFBMs=0")
	}
}

func TestGetRTprop_EmptyDomain_SkipsToGlobalFallback(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: 1000, TTFBMs: 0, Domain: ""},        // empty domain, TTFB=0 → should be skipped
		{Timestamp: 2000, TTFBMs: 300, Domain: "x.com"}, // non-empty domain, TTFB=300
		{Timestamp: 3000, TTFBMs: 100, Domain: "y.com"}, // non-empty domain, TTFB=100
	}
	mu.Unlock()

	// GetRTprop("") should skip domain matching entirely and return global min TTFB
	rtt, ok := GetRTprop("")
	if !ok {
		t.Fatal("Expected ok=true (global fallback)")
	}
	if rtt != 100 {
		t.Errorf("GetRTprop(\"\") = %d, want 100 (global min, not matching Domain=\"\")", rtt)
	}
}

func TestGetRecentPeakByScope(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: time.Now().Unix(), PeakSpeed: 8000000, ThreadCount: 8, Scope: "wan"},
		{Timestamp: time.Now().Unix(), PeakSpeed: 4000000, ThreadCount: 4, Scope: "lan"},
	}
	mu.Unlock()

	v, ok := GetRecentPeakByScope("wan")
	if !ok {
		t.Fatal("Expected ok=true for wan")
	}
	if v != 1000000 {
		t.Errorf("GetRecentPeakByScope(wan) = %d, want 1000000", v)
	}

	v, ok = GetRecentPeakByScope("lan")
	if !ok {
		t.Fatal("Expected ok=true for lan")
	}
	if v != 1000000 {
		t.Errorf("GetRecentPeakByScope(lan) = %d, want 1000000", v)
	}

	_, ok = GetRecentPeakByScope("")
	if !ok {
		t.Fatal("Expected ok=true for no scope")
	}
}
