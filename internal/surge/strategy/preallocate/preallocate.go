// FORK-PATCH: Shared preallocation package used by both single and
// concurrent downloaders. Platform-specific implementations live in
// preallocate_windows.go, preallocate_linux.go, and preallocate_other.go.

package preallocate

import (
	"os"

	"goaria-v3/internal/surge/types"
)

// Preallocate attempts to physically allocate disk space for the file.
// On Windows it uses SetFileValidData with SeManageVolumePrivilege elevation
// (falling back to sparse file + Truncate to eliminate non-admin startup
// latency, then to chunked zero-fill on unsupported filesystems). On Linux it
// uses fallocate with a Truncate fallback. Other platforms use Truncate only.
func Preallocate(file *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return types.AnnotateInsufficientDiskSpace(preallocate(file, size))
}
