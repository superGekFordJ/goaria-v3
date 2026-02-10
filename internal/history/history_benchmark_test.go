package history

import (
	"fmt"
	"testing"
)

func BenchmarkAdd_Update(b *testing.B) {
	DisableSaveForTest()
	// Setup: fill entries with N items
	n := 10000
	mu.Lock()
	entries = make([]HistoryEntry, n)
	gidIndex = make(map[string]int)
	for i := 0; i < n; i++ {
		gid := fmt.Sprintf("gid-%d", i)
		entries[i] = HistoryEntry{
			GID: gid,
		}
		gidIndex[gid] = i
	}
	mu.Unlock()

	// Reset timer to measure only the loop
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Update the last entry (worst case for linear scan)
		Add(HistoryEntry{
			GID: fmt.Sprintf("gid-%d", n-1),
		})
	}
}

func BenchmarkGetAll_Scan(b *testing.B) {
	DisableSaveForTest()
	// Setup: fill entries with N items
	n := 10000
	mu.Lock()
	entries = make([]HistoryEntry, n)
	gidIndex = make(map[string]int)
	for i := 0; i < n; i++ {
		gid := fmt.Sprintf("gid-%d", i)
		entries[i] = HistoryEntry{
			GID:    gid,
			Source: fmt.Sprintf("http://example.com/file-%d.zip", i),
		}
		gidIndex[gid] = i
	}
	mu.Unlock()

	target := "http://example.com/non-existent.zip"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate app.go logic: get all and iterate
		all := GetAll()
		found := false
		for _, h := range all {
			if h.Source == target {
				found = true
				break
			}
		}
		if found {
			// just to use the variable
		}
	}
}

func BenchmarkContainsSource(b *testing.B) {
	DisableSaveForTest()
	// Setup: fill entries with N items
	n := 10000
	mu.Lock()
	entries = make([]HistoryEntry, n)
	gidIndex = make(map[string]int)
	for i := 0; i < n; i++ {
		gid := fmt.Sprintf("gid-%d", i)
		entries[i] = HistoryEntry{
			GID:    gid,
			Source: fmt.Sprintf("http://example.com/file-%d.zip", i),
		}
		gidIndex[gid] = i
	}
	mu.Unlock()

	target := "http://example.com/non-existent.zip"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ContainsSource(target)
	}
}

func BenchmarkAdd_New(b *testing.B) {
	DisableSaveForTest()
	// Setup: fill entries with N items
	n := 10000
	mu.Lock()
	entries = make([]HistoryEntry, n)
	gidIndex = make(map[string]int)
	for i := 0; i < n; i++ {
		gid := fmt.Sprintf("gid-%d", i)
		entries[i] = HistoryEntry{
			GID: gid,
		}
		gidIndex[gid] = i
	}
	mu.Unlock()

	b.ResetTimer()
	// Use modulo to limit growth - cycle through 1000 new GIDs
	// This keeps the slice size bounded while still testing "add new" path
	poolSize := 1000
	for i := 0; i < b.N; i++ {
		Add(HistoryEntry{
			GID: fmt.Sprintf("new-gid-%d", i%poolSize),
		})
	}
}
