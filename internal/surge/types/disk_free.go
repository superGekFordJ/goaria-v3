package types

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DiskSpaceSafetyBuffer is the hard-coded free-space cushion required on top of
// a known download size before enqueue may proceed (500 MiB).
const DiskSpaceSafetyBuffer int64 = 500 * 1024 * 1024

// HasSufficientDiskSpace reports whether freeBytes can hold fileSize plus the
// safety buffer. fileSize <= 0 is treated as not applicable (sufficient) so
// callers that skip unknown sizes stay consistent if they still call this.
// When freeBytes <= DiskSpaceSafetyBuffer, any positive fileSize is rejected
// (underflow-safe: available headroom is treated as zero).
func HasSufficientDiskSpace(fileSize, freeBytes int64) bool {
	if fileSize <= 0 {
		return true
	}
	if freeBytes <= DiskSpaceSafetyBuffer {
		return false
	}
	return fileSize <= freeBytes-DiskSpaceSafetyBuffer
}

// FreeDiskBytes returns available bytes on the volume that owns path
// (Abs + walk to an existing ancestor when path is not yet created).
// On Unix this is unprivileged free space (Bavail-style); on Windows it is
// the caller's free bytes from GetDiskFreeSpaceEx.
func FreeDiskBytes(path string) (int64, error) {
	if path == "" {
		return 0, fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, err
	}

	var lastErr error
	for {
		free, err := freeDiskBytesAt(abs)
		if err == nil {
			return free, nil
		}
		lastErr = err

		// Only walk parents when the path does not exist yet.
		if _, statErr := os.Stat(abs); !errors.Is(statErr, os.ErrNotExist) {
			return 0, lastErr
		}

		parent := filepath.Dir(abs)
		if parent == abs {
			return 0, lastErr
		}
		abs = parent
	}
}
