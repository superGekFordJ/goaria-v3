//go:build windows

package utils

import (
	"errors"
	"math"
	"syscall"

	"golang.org/x/sys/windows"
)

func freeDiskBytesAt(path string) (int64, error) {
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeBytesAvailable, &totalNumberOfBytes, &totalNumberOfFreeBytes); err != nil {
		return 0, err
	}
	// freeBytesAvailable is the caller's usable free space (quota-aware).
	if freeBytesAvailable > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(freeBytesAvailable), nil
}

// IsOSDiskFull reports whether err unwraps to a Windows disk-full / quota errno.
// ERROR_DISK_FULL ≈ ENOSPC; ERROR_DISK_QUOTA_EXCEEDED ≈ EDQUOT;
// ERROR_HANDLE_DISK_FULL = handle-scoped disk full (often wrapped in *os.PathError).
func IsOSDiskFull(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == windows.ERROR_DISK_FULL ||
		errno == windows.ERROR_DISK_QUOTA_EXCEEDED ||
		errno == windows.ERROR_HANDLE_DISK_FULL
}
