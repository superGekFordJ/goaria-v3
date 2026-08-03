//go:build unix

package types

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func platformDiskFullErrno() error { return syscall.ENOSPC }

func platformNonDiskErrno() error { return syscall.EPERM }

func TestIsInsufficientDiskSpace_EDQUOT(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "x.bin", Err: syscall.EDQUOT}
	if !IsInsufficientDiskSpace(raw) {
		t.Fatal("raw EDQUOT PathError should match")
	}
	annotated := AnnotateInsufficientDiskSpace(raw)
	if !errors.Is(annotated, ErrInsufficientDiskSpace) {
		t.Fatalf("EDQUOT annotate missing sentinel: %v", annotated)
	}
	wrapped := fmt.Errorf("copy error: %w", annotated)
	if !IsInsufficientDiskSpace(wrapped) {
		t.Fatalf("wrapped EDQUOT not detected: %v", wrapped)
	}
}

func TestIsInsufficientDiskSpace_ENOSPC_Errno(t *testing.T) {
	if !IsInsufficientDiskSpace(syscall.ENOSPC) {
		t.Fatal("bare ENOSPC errno should match")
	}
}
