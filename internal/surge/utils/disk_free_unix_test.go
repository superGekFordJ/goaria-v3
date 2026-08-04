//go:build unix

package utils

import (
	"os"
	"syscall"
	"testing"
)

func TestIsOSDiskFull_ENOSPC_Bare(t *testing.T) {
	if !IsOSDiskFull(syscall.ENOSPC) {
		t.Fatal("bare ENOSPC should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_ENOSPC_PathError(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}
	if !IsOSDiskFull(raw) {
		t.Fatal("PathError-wrapped ENOSPC should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_EDQUOT(t *testing.T) {
	if !IsOSDiskFull(syscall.EDQUOT) {
		t.Fatal("bare EDQUOT should match IsOSDiskFull")
	}
	raw := &os.PathError{Op: "write", Path: "x", Err: syscall.EDQUOT}
	if !IsOSDiskFull(raw) {
		t.Fatal("PathError-wrapped EDQUOT should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_NonDisk_EPERM(t *testing.T) {
	if IsOSDiskFull(syscall.EPERM) {
		t.Fatal("EPERM should not match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_Nil(t *testing.T) {
	if IsOSDiskFull(nil) {
		t.Fatal("nil should not match IsOSDiskFull")
	}
}
