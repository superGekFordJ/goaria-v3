//go:build windows

package preallocate

import "golang.org/x/sys/windows"

func platformDiskFullErrno() error { return windows.ERROR_DISK_FULL }
