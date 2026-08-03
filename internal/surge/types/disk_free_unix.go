//go:build unix

package types

import "syscall"

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
