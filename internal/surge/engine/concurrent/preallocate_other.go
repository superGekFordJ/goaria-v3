//go:build !linux && !windows

package concurrent

import "os"

// FORK-PATCH: Unified preallocation for concurrent downloader.
// Non-Linux/non-Windows uses Truncate (logical allocation only).

func preallocateFile(file *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return file.Truncate(size)
}
