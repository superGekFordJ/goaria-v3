// FORK-PATCH: Shared preallocation package used by both single and
// concurrent downloaders. Platform-specific implementations live in
// preallocate_windows.go, preallocate_linux.go, and preallocate_other.go.

package preallocate

import "os"

// Preallocate attempts to physically allocate disk space for the file.
// On Windows it uses SetFileValidData with SeManageVolumePrivilege elevation
// (falling back to chunked zero-fill). On Linux it uses fallocate with a
// Truncate fallback. Other platforms use Truncate only.
func Preallocate(file *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return preallocate(file, size)
}
