//go:build !windows

package hls

import (
	"errors"
	"syscall"
)

// isTransientSessionIO marks errno values a freshly prepared session may
// retry (interrupted calls, temporarily unavailable resources). The owning
// instance never retries in place — it latches the fault.
func isTransientSessionIO(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.EINTR || errno == syscall.EAGAIN
}
