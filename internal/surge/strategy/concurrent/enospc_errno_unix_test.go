//go:build unix

package concurrent

import "syscall"

func platformDiskFullErrno() error { return syscall.ENOSPC }
