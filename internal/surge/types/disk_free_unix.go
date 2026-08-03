//go:build unix

package types

import "syscall"

func freeDiskBytesAt(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Bavail is free blocks available to an unprivileged writer.
	return int64(st.Bavail) * int64(st.Bsize), nil
}
