//go:build windows

package wailsapp

import (
	"runtime/debug"
	"testing"
)

func BenchmarkWindowReclaim_TrimWorkingSet(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		debug.FreeOSMemory()
		trimProcessWorkingSet()
	}
}
