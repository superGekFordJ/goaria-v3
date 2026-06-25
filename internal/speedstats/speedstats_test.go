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

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now - 100, PeakSpeed: 10000000, Domain: "example.com", Scope: "wan"},
		{Timestamp: now - 50, PeakSpeed: 30000000, Domain: "example.com", Scope: "wan"},
		{Timestamp: now - 30, PeakSpeed: 5000000, Domain: "other.com", Scope: "wan"},
		{Timestamp: now - 400*24*3600, PeakSpeed: 99999999, Domain: "stale.com", Scope: "wan"},
	}
	mu.Unlock()

	peak, ok := GetDomainPeak("example.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true")
	}
	if peak != 30000000 {
		t.Errorf("GetDomainPeak(example.com) = %d, want 30000000", peak)
	}

	_, ok = GetDomainPeak("nonexistent.com", "wan")
	if ok {
		t.Error("Expected ok=false for nonexistent domain")
	}

	_, ok = GetDomainPeak("stale.com", "wan")
	if ok {
		t.Error("Expected ok=false for stale domain (400-day-old record, 365-day window)")
	}
}

func TestGetRTprop(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now - 100, TTFBMs: 200, Domain: "example.com", Scope: "wan"},
		{Timestamp: now - 50, TTFBMs: 100, Domain: "example.com", Scope: "wan"},
		{Timestamp: now - 40, TTFBMs: 0, Domain: "example.com", Scope: "wan"},
		{Timestamp: now - 30, TTFBMs: 50, Domain: "other.com", Scope: "wan"},
		{Timestamp: now - 400*24*3600, TTFBMs: 10, Domain: "stale.com", Scope: "wan"},
	}
	mu.Unlock()

	rtt, ok := GetRTprop("example.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true")
	}
	if rtt != 100 {
		t.Errorf("GetRTprop(example.com) = %d, want 100 (min, skip 0)", rtt)
	}

	rtt, ok = GetRTprop("nomatch.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true (same-scope fallback)")
	}
	if rtt != 50 {
		t.Errorf("GetRTprop(nomatch.com) same-scope fallback = %d, want 50", rtt)
	}

	rtt, ok = GetRTprop("stale.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true (same-scope fallback for stale domain)")
	}
	if rtt != 50 {
		t.Errorf("GetRTprop(stale.com) same-scope fallback = %d, want 50 (stale record excluded)", rtt)
	}
}

func TestGetRTprop_NoTTFBRecords(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now - 100, TTFBMs: 0, Domain: "a.com", Scope: "wan"},
	}
	mu.Unlock()

	_, ok := GetRTprop("a.com", "wan")
	if ok {
		t.Error("Expected ok=false when all TTFBMs=0")
	}
}

func TestGetRTprop_EmptyDomain_SkipsToGlobalFallback(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now - 100, TTFBMs: 0, Domain: "", Scope: "wan"},       // empty domain, TTFB=0 → should be skipped
		{Timestamp: now - 50, TTFBMs: 300, Domain: "x.com", Scope: "wan"}, // non-empty domain, TTFB=300
		{Timestamp: now - 30, TTFBMs: 100, Domain: "y.com", Scope: "wan"}, // non-empty domain, TTFB=100
	}
	mu.Unlock()

	// GetRTprop("") should skip domain matching entirely and return same-scope min TTFB
	rtt, ok := GetRTprop("", "wan")
	if !ok {
		t.Fatal("Expected ok=true (same-scope fallback)")
	}
	if rtt != 100 {
		t.Errorf("GetRTprop(\"\") = %d, want 100 (same-scope min, not matching Domain=\"\")", rtt)
	}
}

func TestGetRecentPeakByScope_365DayWindow(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	// A record 4 days old should still be included with a 365-day window
	fourDaysAgo := time.Now().Add(-4 * 24 * time.Hour).Unix()

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: fourDaysAgo, PeakSpeed: 6000000, ThreadCount: 6, Scope: "wan"},
	}
	mu.Unlock()

	v, ok := GetRecentPeakByScope("wan")
	if !ok {
		t.Fatal("Expected ok=true for 4-day-old record with 365-day window")
	}
	if v != 1000000 {
		t.Errorf("GetRecentPeakByScope(wan) = %d, want 1000000 (6000000/6)", v)
	}

	// A record 400 days old should be excluded
	fourHundredDaysAgo := time.Now().Add(-400 * 24 * time.Hour).Unix()

	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: fourHundredDaysAgo, PeakSpeed: 6000000, ThreadCount: 6, Scope: "wan"},
	}
	mu.Unlock()

	_, ok = GetRecentPeakByScope("wan")
	if ok {
		t.Error("Expected ok=false for 400-day-old record with 365-day window")
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

func TestGetRecentPeakByDomain(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now, PeakSpeed: 8 * 1024 * 1024, ThreadCount: 8, Domain: "example.com", Scope: "wan"},
		{Timestamp: now, PeakSpeed: 4 * 1024 * 1024, ThreadCount: 4, Domain: "other.com", Scope: "wan"},
	}
	mu.Unlock()

	v, ok := GetRecentPeakByDomain("example.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true for example.com")
	}
	if v != 1024*1024 {
		t.Errorf("GetRecentPeakByDomain(example.com) = %d, want %d", v, 1024*1024)
	}
}

func TestGetRecentPeakByDomain_EmptyDomain(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now, PeakSpeed: 8 * 1024 * 1024, ThreadCount: 8, Domain: "example.com", Scope: "wan"},
	}
	mu.Unlock()

	_, ok := GetRecentPeakByDomain("", "wan")
	if ok {
		t.Error("Expected ok=false for empty domain")
	}
}

func TestGetRecentPeakByDomain_NoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now, PeakSpeed: 8 * 1024 * 1024, ThreadCount: 8, Domain: "example.com", Scope: "wan"},
	}
	mu.Unlock()

	_, ok := GetRecentPeakByDomain("nonexistent.com", "wan")
	if ok {
		t.Error("Expected ok=false for nonexistent domain")
	}
}

func TestGetRecentPeakByDomain_CrossDomainIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		// Slow domain: 50KB/s with 8 threads → 6.25KB/s per thread
		{Timestamp: now, PeakSpeed: 50000, ThreadCount: 8, Domain: "slow.com", Scope: "wan"},
		// Fast domain: 8MB/s with 4 threads → 2MB/s per thread
		{Timestamp: now, PeakSpeed: 8 * 1024 * 1024, ThreadCount: 4, Domain: "fast.com", Scope: "wan"},
	}
	mu.Unlock()

	v, ok := GetRecentPeakByDomain("fast.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true for fast.com")
	}
	if v != 2*1024*1024 {
		t.Errorf("GetRecentPeakByDomain(fast.com) = %d, want %d (not polluted by slow.com)", v, 2*1024*1024)
	}

	v, ok = GetRecentPeakByDomain("slow.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true for slow.com")
	}
	if v != 6250 {
		t.Errorf("GetRecentPeakByDomain(slow.com) = %d, want 6250", v)
	}
}

func TestGetRecentPeakByDomain_CrossScopeIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		// wan: 8MB/s with 4 threads → 2MB/s per thread
		{Timestamp: now, PeakSpeed: 8 * 1024 * 1024, ThreadCount: 4, Domain: "example.com", Scope: "wan"},
		// lan: 40MB/s with 4 threads → 10MB/s per thread
		{Timestamp: now, PeakSpeed: 40 * 1024 * 1024, ThreadCount: 4, Domain: "example.com", Scope: "lan"},
	}
	mu.Unlock()

	v, ok := GetRecentPeakByDomain("example.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true for example.com+wan")
	}
	if v != 2*1024*1024 {
		t.Errorf("GetRecentPeakByDomain(example.com, wan) = %d, want %d (not polluted by lan 10MB/s)", v, 2*1024*1024)
	}

	v, ok = GetRecentPeakByDomain("example.com", "lan")
	if !ok {
		t.Fatal("Expected ok=true for example.com+lan")
	}
	if v != 10*1024*1024 {
		t.Errorf("GetRecentPeakByDomain(example.com, lan) = %d, want %d", v, 10*1024*1024)
	}
}

func TestGetDomainPeak_CrossScopeIsolation(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now, PeakSpeed: 5 * 1024 * 1024, Domain: "example.com", Scope: "wan"},
		{Timestamp: now, PeakSpeed: 50 * 1024 * 1024, Domain: "example.com", Scope: "lan"},
	}
	mu.Unlock()

	peak, ok := GetDomainPeak("example.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true for example.com+wan")
	}
	if peak != 5*1024*1024 {
		t.Errorf("GetDomainPeak(example.com, wan) = %d, want %d (not polluted by lan 50MB)", peak, 5*1024*1024)
	}

	peak, ok = GetDomainPeak("example.com", "lan")
	if !ok {
		t.Fatal("Expected ok=true for example.com+lan")
	}
	if peak != 50*1024*1024 {
		t.Errorf("GetDomainPeak(example.com, lan) = %d, want %d", peak, 50*1024*1024)
	}
}

func TestGetRTprop_CrossScopeFallback(t *testing.T) {
	tmpDir := t.TempDir()
	SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	SetSaveInterval(1 * time.Hour)

	now := time.Now().Unix()
	mu.Lock()
	records = []SpeedRecord{
		{Timestamp: now - 100, TTFBMs: 100, Domain: "a.com", Scope: "wan"},
		{Timestamp: now - 50, TTFBMs: 50, Domain: "other.com", Scope: "lan"},
	}
	mu.Unlock()

	// No domain match for "nomatch.com" → should fall back to same-scope (wan) min TTFB
	// wan scope only has a.com with TTFB=100ms, lan's 50ms should NOT pollute
	rtt, ok := GetRTprop("nomatch.com", "wan")
	if !ok {
		t.Fatal("Expected ok=true (same-scope fallback)")
	}
	if rtt != 100 {
		t.Errorf("GetRTprop(nomatch.com, wan) = %d, want 100 (same-scope wan min, not polluted by lan 50ms)", rtt)
	}
}
