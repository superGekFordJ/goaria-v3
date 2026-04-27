package history

import (
	"fmt"
	"testing"
)

var benchmarkSourceFound bool

type benchmarkRemoveShape struct {
	name       string
	historyLen int
	removeGIDs []string
}

func setupBenchmarkHistoryEntries(n int) {
	mu.Lock()
	defer mu.Unlock()

	entries = make([]HistoryEntry, n)
	gidIndex = make(map[string]int, n)
	sourceIndex = make(map[string]int, n)
	for i := 0; i < n; i++ {
		gid := fmt.Sprintf("gid-%06d", i)
		source := fmt.Sprintf("https://example.com/source-%06d", i%1000)
		entries[i] = HistoryEntry{
			GID:    gid,
			Title:  fmt.Sprintf("Task %06d", i),
			Source: source,
		}
		gidIndex[gid] = i
		sourceIndex[source]++
	}
}

func benchmarkSpreadRemovalGIDs(historyLen, removeCount int) []string {
	if removeCount <= 0 || historyLen <= 0 {
		return nil
	}
	if removeCount == 1 {
		return []string{fmt.Sprintf("gid-%06d", historyLen/2)}
	}

	gids := make([]string, 0, removeCount)
	for i := 0; i < removeCount; i++ {
		idx := i * (historyLen - 1) / (removeCount - 1)
		gids = append(gids, fmt.Sprintf("gid-%06d", idx))
	}
	return gids
}

func benchmarkFrontRemovalGIDs(removeCount int) []string {
	gids := make([]string, 0, removeCount)
	for i := 0; i < removeCount; i++ {
		gids = append(gids, fmt.Sprintf("gid-%06d", i))
	}
	return gids
}

func benchmarkRemoveShapes() []benchmarkRemoveShape {
	return []benchmarkRemoveShape{
		{
			name:       "Spread100From10000",
			historyLen: 10000,
			removeGIDs: benchmarkSpreadRemovalGIDs(10000, 100),
		},
		{
			name:       "FrontHeavy100From10000",
			historyLen: 10000,
			removeGIDs: benchmarkFrontRemovalGIDs(100),
		},
		{
			name:       "Spread1000From50000",
			historyLen: 50000,
			removeGIDs: benchmarkSpreadRemovalGIDs(50000, 1000),
		},
	}
}

func BenchmarkRemoveBatchCurrent(b *testing.B) {
	DisableSaveForTest()
	for _, shape := range benchmarkRemoveShapes() {
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				setupBenchmarkHistoryEntries(shape.historyLen)
				b.StartTimer()
				for _, gid := range shape.removeGIDs {
					Remove(gid)
				}
				b.StopTimer()
			}
		})
	}
}

func BenchmarkRemoveManyBatch(b *testing.B) {
	DisableSaveForTest()
	for _, shape := range benchmarkRemoveShapes() {
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				setupBenchmarkHistoryEntries(shape.historyLen)
				b.StartTimer()
				RemoveMany(shape.removeGIDs)
				b.StopTimer()
			}
		})
	}
}

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
		benchmarkSourceFound = found
	}
}

func BenchmarkContainsSource(b *testing.B) {
	DisableSaveForTest()
	// Setup: fill entries with N items
	n := 10000
	mu.Lock()
	entries = make([]HistoryEntry, n)
	gidIndex = make(map[string]int)
	sourceIndex = make(map[string]int)
	for i := 0; i < n; i++ {
		gid := fmt.Sprintf("gid-%d", i)
		source := fmt.Sprintf("http://example.com/file-%d.zip", i)
		entries[i] = HistoryEntry{
			GID:    gid,
			Source: source,
		}
		gidIndex[gid] = i
		sourceIndex[source]++
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
