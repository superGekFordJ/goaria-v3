//go:build windows

package utils

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestIsOSDiskFull_ERROR_DISK_FULL(t *testing.T) {
	if !IsOSDiskFull(windows.ERROR_DISK_FULL) {
		t.Fatal("bare ERROR_DISK_FULL should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_ERROR_DISK_QUOTA_EXCEEDED(t *testing.T) {
	if !IsOSDiskFull(windows.ERROR_DISK_QUOTA_EXCEEDED) {
		t.Fatal("bare ERROR_DISK_QUOTA_EXCEEDED should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_ERROR_HANDLE_DISK_FULL(t *testing.T) {
	if !IsOSDiskFull(windows.ERROR_HANDLE_DISK_FULL) {
		t.Fatal("bare ERROR_HANDLE_DISK_FULL should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_HANDLE_PathError(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "x", Err: windows.ERROR_HANDLE_DISK_FULL}
	if !IsOSDiskFull(raw) {
		t.Fatal("PathError-wrapped ERROR_HANDLE_DISK_FULL should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_NonDisk_ACCESS_DENIED(t *testing.T) {
	if IsOSDiskFull(windows.ERROR_ACCESS_DENIED) {
		t.Fatal("ERROR_ACCESS_DENIED should not match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_Nil(t *testing.T) {
	if IsOSDiskFull(nil) {
		t.Fatal("nil should not match IsOSDiskFull")
	}
}
