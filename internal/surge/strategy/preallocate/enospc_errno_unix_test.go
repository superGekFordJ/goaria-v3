//go:build unix

package preallocate

import "syscall"

func platformDiskFullErrno() error { return syscall.ENOSPC }
