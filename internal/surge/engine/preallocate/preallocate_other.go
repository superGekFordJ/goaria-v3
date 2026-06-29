//go:build !linux && !windows

// FORK-PATCH: Non-Linux/non-Windows fallback using Truncate (logical allocation only).

package preallocate

import "os"

func preallocate(file *os.File, size int64) error {
	return file.Truncate(size)
}
