//go:build windows

package hls

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// isTransientSessionIO marks errno values a freshly prepared session may
// retry (sharing/lock violations from AV indexers or another handle). The
// owning instance never retries in place — it latches the fault.
func isTransientSessionIO(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == windows.ERROR_SHARING_VIOLATION || errno == windows.ERROR_LOCK_VIOLATION
}
