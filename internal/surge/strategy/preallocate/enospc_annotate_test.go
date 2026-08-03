package preallocate

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"goaria-v3/internal/surge/types"
)

func TestAnnotateInsufficientDiskSpace_PassthroughShape(t *testing.T) {
	// Preallocate annotates via types.AnnotateInsufficientDiskSpace before
	// return; verify the helper shape callers rely on (sentinel + PathError).
	raw := &os.PathError{Op: "truncate", Path: "x.bin", Err: platformDiskFullErrno()}
	got := types.AnnotateInsufficientDiskSpace(raw)
	if !types.IsInsufficientDiskSpace(got) {
		t.Fatalf("annotate missed disk-full: %v", got)
	}
	wrapped := fmt.Errorf("failed to preallocate file: %w", got)
	if !errors.Is(wrapped, types.ErrInsufficientDiskSpace) {
		t.Fatalf("caller %%w wrap lost sentinel: %v", wrapped)
	}
}
