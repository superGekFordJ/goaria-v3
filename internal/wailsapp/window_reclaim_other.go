//go:build !windows

package wailsapp

// trimProcessWorkingSet is a no-op on non-Windows platforms.
func trimProcessWorkingSet() {}
