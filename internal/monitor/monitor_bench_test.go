package monitor

import (
	"fmt"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func makeBenchmarkTasks(n int, prefix string) []rpc.Task {
	tasks := make([]rpc.Task, n)
	for i := range n {
		tasks[i] = rpc.Task{
			GID:             fmt.Sprintf("%s%06d", prefix, i),
			Status:          "active",
			TotalLength:     "104857600",
			CompletedLength: "52428800",
			DownloadSpeed:   "1048576",
			Dir:             "D:/Downloads",
		}
	}
	return tasks
}

func setupBenchmarkCache(n int) (*TaskCache, []rpc.Task) {
	cache := NewTaskCacheForTest()
	tasks := makeBenchmarkTasks(n, "ar_")

	cache.mu.Lock()
	for i := range n {
		gid := fmt.Sprintf("ar_%06d", i)
		cache.metadata[gid] = &TaskMetadata{
			GID:         gid,
			Title:       fmt.Sprintf("Download_File_%d.zip", i),
			Dir:         "D:/Downloads",
			TotalLength: 104857600,
			Files:       []string{fmt.Sprintf("D:/Downloads/Download_File_%d.zip", i)},
			SourceURL:   fmt.Sprintf("https://example.com/file_%d.zip", i),
			FetchedAt:   time.Now(),
		}
	}
	cache.mu.Unlock()

	return cache, tasks
}

func BenchmarkTaskCache_EnrichTasks_100(b *testing.B) {
	cache, tasks := setupBenchmarkCache(100)

	b.ReportAllocs()
	for b.Loop() {
		cache.EnrichTasks(tasks)
	}
}

func BenchmarkTaskCache_EnrichTasks_500(b *testing.B) {
	cache, tasks := setupBenchmarkCache(500)

	b.ReportAllocs()
	for b.Loop() {
		cache.EnrichTasks(tasks)
	}
}

func BenchmarkTaskCache_UpdateFromAria2_100(b *testing.B) {
	cache := NewTaskCacheForTest()
	active := makeBenchmarkTasks(50, "ar_act_")
	waiting := makeBenchmarkTasks(25, "ar_wait_")
	stopped := makeBenchmarkTasks(25, "ar_stop_")

	b.ReportAllocs()
	for b.Loop() {
		cache.UpdateFromAria2(active, waiting, stopped)
	}
}

func BenchmarkTaskCache_GetLiveTaskLists(b *testing.B) {
	cache := NewTaskCacheForTest()
	active := makeBenchmarkTasks(50, "ar_act_")
	waiting := makeBenchmarkTasks(25, "ar_wait_")
	stopped := makeBenchmarkTasks(25, "ar_stop_")
	cache.UpdateFromAria2(active, waiting, stopped)

	b.ReportAllocs()
	for b.Loop() {
		_, _ = cache.GetLiveTaskLists()
	}
}

func BenchmarkTaskTracker_Update_100(b *testing.B) {
	tracker := NewTaskTracker()
	active := makeBenchmarkTasks(50, "ar_act_")
	waiting := makeBenchmarkTasks(25, "ar_wait_")
	stopped := makeBenchmarkTasks(25, "ar_stop_")

	b.ReportAllocs()
	for b.Loop() {
		tracker.Update(active, waiting, stopped)
	}
}
