package main

import (
	"fmt"
	"strings"
	"testing"

	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

var benchmarkURLFound bool

func BenchmarkSliceConcatenation(b *testing.B) {
	// Create large slices
	size := 1000
	active := make([]rpc.Task, size)
	waiting := make([]rpc.Task, size)
	stopped := make([]rpc.Task, size)

	// Fill with dummy data
	fillTasks(active, "active")
	fillTasks(waiting, "waiting")
	fillTasks(stopped, "stopped")

	normalizedUrl := "http://example.com/unique-url"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate the inefficient code
		allTasks := append([]rpc.Task{}, active...)
		allTasks = append(allTasks, waiting...)
		allTasks = append(allTasks, stopped...)

		benchmarkURLFound = false
		for _, t := range allTasks {
			for _, f := range t.Files {
				for _, u := range f.Uris {
					if strings.TrimSpace(u.Uri) == normalizedUrl {
						benchmarkURLFound = true
						break
					}
				}
			}
		}
	}
}

func BenchmarkSeparateLoops(b *testing.B) {
	// Create large slices
	size := 1000
	active := make([]rpc.Task, size)
	waiting := make([]rpc.Task, size)
	stopped := make([]rpc.Task, size)

	// Fill with dummy data
	fillTasks(active, "active")
	fillTasks(waiting, "waiting")
	fillTasks(stopped, "stopped")

	normalizedUrl := "http://example.com/unique-url"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Optimized code
		check := func(tasks []rpc.Task) bool {
			for _, t := range tasks {
				for _, f := range t.Files {
					for _, u := range f.Uris {
						if strings.TrimSpace(u.Uri) == normalizedUrl {
							return true
						}
					}
				}
			}
			return false
		}
		benchmarkURLFound = check(active) || check(waiting) || check(stopped)
	}
}

func fillTasks(tasks []rpc.Task, status string) {
	for i := range tasks {
		tasks[i] = rpc.Task{
			GID:    "gid",
			Status: status,
			Files: []rpc.File{
				{
					Path: "/path/to/file",
					Uris: []rpc.Uri{
						{Uri: "http://example.com/file"},
					},
				},
			},
		}
	}
}

func BenchmarkRemovalTargetResolution(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			gids, active, waiting, stopped, historyMap := buildRemovalBenchmarkFixtures(size)

			b.Run("per_gid_live_scan", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					for _, gid := range gids {
						_ = legacyRemovalLookup(gid, active, waiting, stopped, historyMap)
					}
				}
			})

			b.Run("batch_lookup_once", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					lookup := buildRemovalTargetLookup(active, waiting, stopped, historyMap)
					for _, gid := range gids {
						_ = lookup[gid]
					}
				}
			})
		})
	}
}

func buildRemovalBenchmarkFixtures(size int) ([]string, []rpc.Task, []rpc.Task, []rpc.Task, map[string]history.HistoryEntry) {
	gids := make([]string, 0, size)
	active := make([]rpc.Task, 0, size/3+1)
	waiting := make([]rpc.Task, 0, size/3+1)
	stopped := make([]rpc.Task, 0, size/3+1)
	historyMap := make(map[string]history.HistoryEntry, size)

	for i := 0; i < size; i++ {
		gid := fmt.Sprintf("gid-%d", i)
		path := fmt.Sprintf("D:/Downloads/%s.bin", gid)
		task := rpc.Task{
			GID:    gid,
			Dir:    "D:/Downloads",
			Status: "complete",
			Files:  []rpc.File{{Path: path}},
		}

		gids = append(gids, gid)
		historyMap[gid] = history.HistoryEntry{GID: gid, Dir: task.Dir, Path: path}

		switch i % 3 {
		case 0:
			task.Status = "active"
			active = append(active, task)
		case 1:
			task.Status = "waiting"
			waiting = append(waiting, task)
		default:
			stopped = append(stopped, task)
		}
	}

	return gids, active, waiting, stopped, historyMap
}

func legacyRemovalLookup(gid string, active, waiting, stopped []rpc.Task, historyMap map[string]history.HistoryEntry) removalTarget {
	for _, task := range active {
		if task.GID == gid {
			if target, ok := removalTargetFromTask(task); ok {
				return target
			}
			break
		}
	}

	for _, task := range waiting {
		if task.GID == gid {
			if target, ok := removalTargetFromTask(task); ok {
				return target
			}
			break
		}
	}

	for _, task := range stopped {
		if task.GID == gid {
			if target, ok := removalTargetFromTask(task); ok {
				return target
			}
			break
		}
	}

	if entry, ok := historyMap[gid]; ok {
		if target, ok := removalTargetFromHistory(entry); ok {
			return target
		}
	}

	return removalTarget{}
}

func buildRemovalTargetLookup(active, waiting, stopped []rpc.Task, historyMap map[string]history.HistoryEntry) map[string]removalTarget {
	lookup := make(map[string]removalTarget, len(active)+len(waiting)+len(stopped)+len(historyMap))

	for _, task := range active {
		if target, ok := removalTargetFromTask(task); ok {
			lookup[task.GID] = target
		}
	}
	for _, task := range waiting {
		if _, exists := lookup[task.GID]; exists {
			continue
		}
		if target, ok := removalTargetFromTask(task); ok {
			lookup[task.GID] = target
		}
	}
	for _, task := range stopped {
		if _, exists := lookup[task.GID]; exists {
			continue
		}
		if target, ok := removalTargetFromTask(task); ok {
			lookup[task.GID] = target
		}
	}
	for gid, entry := range historyMap {
		if _, exists := lookup[gid]; exists {
			continue
		}
		if target, ok := removalTargetFromHistory(entry); ok {
			lookup[gid] = target
		}
	}

	return lookup
}
