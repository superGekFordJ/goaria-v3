//go:build windows

package types

import (
	"testing"

	"golang.org/x/sys/windows"
)

func platformDiskFullErrno() error { return windows.ERROR_DISK_FULL }

func platformNonDiskErrno() error { return windows.ERROR_ACCESS_DENIED }

func TestIsInsufficientDiskSpace_ERROR_DISK_FULL(t *testing.T) {
	if !IsInsufficientDiskSpace(windows.ERROR_DISK_FULL) {
		t.Fatal("bare ERROR_DISK_FULL should match")
	}
}

func TestIsInsufficientDiskSpace_ERROR_DISK_QUOTA_EXCEEDED(t *testing.T) {
	if !IsInsufficientDiskSpace(windows.ERROR_DISK_QUOTA_EXCEEDED) {
		t.Fatal("bare ERROR_DISK_QUOTA_EXCEEDED should match (EDQUOT parity)")
	}
}
