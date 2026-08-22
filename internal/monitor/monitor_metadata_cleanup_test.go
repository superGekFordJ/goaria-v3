package monitor

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

// resetCacheMetadataForTest clears the global Cache metadata and pending maps
// so tests start from a known state.
func resetCacheMetadataForTest() {
	Cache.mu.Lock()
	Cache.metadata = make(map[string]*TaskMetadata)
	Cache.pendingStartGids = make(map[string]time.Time)
	Cache.mu.Unlock()
}

// seedOrphanMetadata populates Cache.metadata with n orphan ar_ entries past
// the FetchedAt grace window and returns the seeded GIDs.
func seedOrphanMetadata(n int) []string {
	gids := make([]string, 0, n)
	old := time.Now().Add(-2 * metadataCleanupGrace)
	Cache.mu.Lock()
	for i := range n {
		gid := fmt.Sprintf("ar_orphan_%d", i)
		Cache.metadata[gid] = &TaskMetadata{GID: gid, FetchedAt: old}
		gids = append(gids, gid)
	}
	Cache.mu.Unlock()
	return gids
}

func TestMonitor_MetadataCleanup_ThrottleBehavior(t *testing.T) {
	resetCacheMetadataForTest()
	t.Cleanup(resetCacheMetadataForTest)
	seedOrphanMetadata(3)

	m := &Monitor{lastMetadataCleanup: time.Now()}
	m.aria2Recovered.Store(true)

	m.runMetadataCleanup(nil, nil, nil)
	for i := range 3 {
		if Cache.GetMetadata(fmt.Sprintf("ar_orphan_%d", i)) == nil {
			t.Fatal("expected no eviction while throttled")
		}
	}

	m.lastMetadataCleanup = time.Now().Add(-metadataCleanupInterval - time.Second)
	m.runMetadataCleanup(nil, nil, nil)
	for i := range 3 {
		if Cache.GetMetadata(fmt.Sprintf("ar_orphan_%d", i)) != nil {
			t.Fatalf("expected ar_orphan_%d evicted after throttle elapsed", i)
		}
	}
}

func TestMonitor_MetadataCleanup_RunsOnFirstRecoveryTick(t *testing.T) {
	resetCacheMetadataForTest()
	t.Cleanup(resetCacheMetadataForTest)
	seedOrphanMetadata(2)

	m := &Monitor{}
	m.aria2Recovered.Store(true)

	m.runMetadataCleanup(nil, nil, nil)

	for i := range 2 {
		if Cache.GetMetadata(fmt.Sprintf("ar_orphan_%d", i)) != nil {
			t.Fatalf("expected ar_orphan_%d evicted on first recovery tick", i)
		}
	}
}

func TestMonitor_MetadataCleanup_SkipsBeforeRecovery(t *testing.T) {
	resetCacheMetadataForTest()
	t.Cleanup(resetCacheMetadataForTest)
	seedOrphanMetadata(2)

	m := &Monitor{lastMetadataCleanup: time.Now().Add(-metadataCleanupInterval - time.Second)}

	m.runMetadataCleanup(nil, nil, nil)

	for i := range 2 {
		if Cache.GetMetadata(fmt.Sprintf("ar_orphan_%d", i)) == nil {
			t.Fatalf("expected ar_orphan_%d retained before recovery", i)
		}
	}
}

func TestMonitor_MetadataCleanup_RetainsActiveGids(t *testing.T) {
	resetCacheMetadataForTest()
	t.Cleanup(resetCacheMetadataForTest)
	seedOrphanMetadata(1)

	active := []rpc.Task{{GID: "ar_keep", Status: "active"}}
	Cache.mu.Lock()
	Cache.metadata["ar_keep"] = &TaskMetadata{GID: "ar_keep", FetchedAt: time.Now().Add(-2 * metadataCleanupGrace)}
	Cache.mu.Unlock()

	m := &Monitor{}
	m.aria2Recovered.Store(true)
	m.lastMetadataCleanup = time.Now().Add(-metadataCleanupInterval - time.Second)

	m.runMetadataCleanup(active, nil, nil)

	if Cache.GetMetadata("ar_keep") == nil {
		t.Fatal("expected ar_keep retained as active")
	}
	if Cache.GetMetadata("ar_orphan_0") != nil {
		t.Fatal("expected ar_orphan_0 evicted")
	}
}

func TestMonitor_MetadataCleanup_TickIntegration(t *testing.T) {
	resetCacheMetadataForTest()
	t.Cleanup(resetCacheMetadataForTest)
	resetCacheAr()
	seedOrphanMetadata(1)

	engine := &mockTickEngine{}
	engine.setLists(
		[]rpc.Task{{GID: "ar_active", Status: "active", TotalLength: "1000"}},
		nil, nil,
	)
	m := newTickRecoveryMonitor(t, engine)
	m.lastMetadataCleanup = time.Now().Add(-metadataCleanupInterval - time.Second)

	m.tick()

	if Cache.GetMetadata("ar_orphan_0") != nil {
		t.Fatal("expected orphan metadata evicted via tick")
	}
	if !m.aria2Recovered.Load() {
		t.Fatal("expected aria2Recovered after successful tick")
	}
}

func TestTaskCache_CleanupMetadata_ConcurrentWithGetMetadata(t *testing.T) {
	cache := newCleanupTestCache()
	old := time.Now().Add(-2 * metadataCleanupGrace)
	gids := make([]string, 1000)
	for i := range 1000 {
		gid := fmt.Sprintf("ar_conc_%d", i)
		gids[i] = gid
		cache.metadata[gid] = &TaskMetadata{GID: gid, FetchedAt: old}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			cache.GetMetadata(gids[rand.Intn(len(gids))])
		}
	}()

	cache.CleanupMetadata(map[string]bool{})
	<-done
}

func TestTaskCache_CleanupMetadata_ConcurrentWithUpdateFromAria2(t *testing.T) {
	cache := newCleanupTestCache()
	old := time.Now().Add(-2 * metadataCleanupGrace)
	tasks := make([]rpc.Task, 100)
	for i := range 100 {
		gid := fmt.Sprintf("ar_upd_%d", i)
		cache.metadata[gid] = &TaskMetadata{GID: gid, FetchedAt: old}
		tasks[i] = rpc.Task{GID: gid, Status: "active"}
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			cache.UpdateFromAria2(tasks, nil, nil)
		}
	})

	cache.CleanupMetadata(map[string]bool{})
	wg.Wait()
}
