//go:build linux

package concurrent

import (
	"os"
	"syscall"
)

// FORK-PATCH: Unified preallocation for concurrent downloader.
// Linux uses fallocate for physical allocation, falls back to Truncate.

func preallocateFile(file *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	if err := syscall.Fallocate(int(file.Fd()), 0, 0, size); err == nil {
		return nil
	}
	return file.Truncate(size)
}
