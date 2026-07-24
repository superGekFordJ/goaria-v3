//go:build linux

// FORK-PATCH: Linux fallocate for physical allocation with Truncate fallback.

package preallocate

import (
	"os"
	"syscall"
)

func preallocate(file *os.File, size int64) error {
	if err := syscall.Fallocate(int(file.Fd()), 0, 0, size); err == nil {
		return nil
	}
	return file.Truncate(size)
}
