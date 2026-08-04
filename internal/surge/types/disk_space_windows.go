//go:build windows

package types

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

func isDiskFull(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	// ERROR_DISK_FULL ≈ ENOSPC; ERROR_DISK_QUOTA_EXCEEDED ≈ EDQUOT;
	// ERROR_HANDLE_DISK_FULL = handle-scoped disk full (often wrapped in *os.PathError).
	return errno == windows.ERROR_DISK_FULL ||
		errno == windows.ERROR_DISK_QUOTA_EXCEEDED ||
		errno == windows.ERROR_HANDLE_DISK_FULL
}
