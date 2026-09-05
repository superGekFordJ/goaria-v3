//go:build windows

package wailsapp

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func BenchmarkWindowReclaim_TrimWorkingSet(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		runtime.GC()
		debug.FreeOSMemory()
		trimProcessWorkingSet()
	}
}
