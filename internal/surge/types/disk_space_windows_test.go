//go:build windows

package types

import (
	"errors"
	"fmt"
	"os"
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

func TestIsInsufficientDiskSpace_ERROR_HANDLE_DISK_FULL(t *testing.T) {
	if !IsInsufficientDiskSpace(windows.ERROR_HANDLE_DISK_FULL) {
		t.Fatal("bare ERROR_HANDLE_DISK_FULL should match")
	}
}

func TestIsInsufficientDiskSpace_HANDLE_PathError(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "x", Err: windows.ERROR_HANDLE_DISK_FULL}
	if !IsInsufficientDiskSpace(raw) {
		t.Fatal("PathError-wrapped ERROR_HANDLE_DISK_FULL should match without annotate")
	}
}

func TestAnnotateInsufficientDiskSpace_HANDLE_PathError(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "x", Err: windows.ERROR_HANDLE_DISK_FULL}
	annotated := AnnotateInsufficientDiskSpace(raw)
	if !errors.Is(annotated, ErrInsufficientDiskSpace) {
		t.Fatalf("HANDLE annotate missing sentinel: %v", annotated)
	}
	if _, ok := errors.AsType[*os.PathError](annotated); !ok {
		t.Fatal("HANDLE annotate lost PathError via unwrap")
	}
	wrapped := fmt.Errorf("write error: %w", annotated)
	if !IsInsufficientDiskSpace(wrapped) {
		t.Fatalf("double-wrapped HANDLE annotate not detected: %v", wrapped)
	}
}
