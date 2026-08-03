package single

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"goaria-v3/internal/surge/types"
)

func TestAnnotateCopyError_InsufficientDisk(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "out.bin", Err: platformDiskFullErrno()}
	wrapped := fmt.Errorf("copy error: %w", types.AnnotateInsufficientDiskSpace(raw))
	if !types.IsInsufficientDiskSpace(wrapped) {
		t.Fatalf("annotated copy error not detected: %v", wrapped)
	}
	if !errors.Is(wrapped, types.ErrInsufficientDiskSpace) {
		t.Fatalf("missing sentinel unwrap: %v", wrapped)
	}
}

func TestAnnotatePreallocateError_InsufficientDisk(t *testing.T) {
	raw := &os.PathError{Op: "truncate", Path: "out.bin", Err: platformDiskFullErrno()}
	wrapped := fmt.Errorf("failed to preallocate file: %w", types.AnnotateInsufficientDiskSpace(raw))
	if !types.IsInsufficientDiskSpace(wrapped) {
		t.Fatalf("annotated preallocate error not detected: %v", wrapped)
	}
}
