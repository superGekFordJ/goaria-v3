//go:build windows

package wailsapp

import (
	"syscall"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procSetProcessWorkingSetSize = kernel32.NewProc("SetProcessWorkingSetSize")
)

// trimProcessWorkingSet requests Windows to immediately trim unreferenced pages
// from the process working set into the standby list.
func trimProcessWorkingSet() {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return
	}
	_, _, _ = procSetProcessWorkingSetSize.Call(uintptr(h), ^uintptr(0), ^uintptr(0))
}
