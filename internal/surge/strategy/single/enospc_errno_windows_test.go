//go:build windows

package single

import "golang.org/x/sys/windows"

func platformDiskFullErrno() error { return windows.ERROR_DISK_FULL }
