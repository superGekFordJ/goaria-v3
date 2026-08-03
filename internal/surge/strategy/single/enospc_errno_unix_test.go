//go:build unix

package single

import "syscall"

func platformDiskFullErrno() error { return syscall.ENOSPC }
