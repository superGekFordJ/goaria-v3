//go:build unix

package utils

import (
	"errors"
	"syscall"
)

func freeDiskBytesAt(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Bavail is free blocks available to an unprivileged writer.
	// Clamp like Windows GetDiskFreeSpaceEx so a pathological Statfs report
	// cannot wrap negative and fail-closed at enqueue.
	return clampFreeBytesProduct(uint64(st.Bavail), uint64(st.Bsize)), nil
}

// IsOSDiskFull reports whether err unwraps to a disk-full / quota errno
// (ENOSPC or EDQUOT). Covers bare errno and *os.PathError via errors.As.
func IsOSDiskFull(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.ENOSPC || errno == syscall.EDQUOT
}
