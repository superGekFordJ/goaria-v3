//go:build unix

package utils

import (
	"errors"
	"math"
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

// clampFreeBytesProduct multiplies available blocks by block size without
// wrapping to a negative int64 (clamps to MaxInt64 on overflow).
func clampFreeBytesProduct(blocks, blockSize uint64) int64 {
	if blocks == 0 || blockSize == 0 {
		return 0
	}
	if blocks > math.MaxInt64/blockSize {
		return math.MaxInt64
	}
	return int64(blocks * blockSize)
}
