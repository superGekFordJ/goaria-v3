package main

import (
	"strings"
	"testing"

	"goaria-v3/internal/rpc"
)

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
		allTasks := append(active, append(waiting, stopped...)...)

		for _, t := range allTasks {
			for _, f := range t.Files {
				for _, u := range f.Uris {
					if strings.TrimSpace(u.Uri) == normalizedUrl {
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
		if check(active) || check(waiting) || check(stopped) {
			// found
		}
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
