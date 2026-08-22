package types

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestIsInsufficientDiskSpace_Sentinel(t *testing.T) {
	if !IsInsufficientDiskSpace(ErrInsufficientDiskSpace) {
		t.Fatal("expected sentinel to match")
	}
	joined := errors.Join(errors.New("outer"), ErrInsufficientDiskSpace)
	if !IsInsufficientDiskSpace(joined) {
		t.Fatal("expected Join-wrapped sentinel to match")
	}
	wrapped := fmt.Errorf("write error: %w", ErrInsufficientDiskSpace)
	if !IsInsufficientDiskSpace(wrapped) {
		t.Fatal("expected fmt-wrapped sentinel to match")
	}
	if IsInsufficientDiskSpace(errors.New("other")) {
		t.Fatal("expected unrelated error to be false")
	}
	if IsInsufficientDiskSpace(nil) {
		t.Fatal("expected nil to be false")
	}
}

func TestAnnotateInsufficientDiskSpace_IdempotentAndPassthrough(t *testing.T) {
	if got := AnnotateInsufficientDiskSpace(nil); got != nil {
		t.Fatalf("nil annotate = %v, want nil", got)
	}
	got := AnnotateInsufficientDiskSpace(ErrInsufficientDiskSpace)
	if !errors.Is(got, ErrInsufficientDiskSpace) {
		t.Fatalf("idempotent annotate lost sentinel: %v", got)
	}
	got2 := AnnotateInsufficientDiskSpace(got)
	if !errors.Is(got2, ErrInsufficientDiskSpace) {
		t.Fatalf("second annotate lost sentinel: %v", got2)
	}
	other := errors.New("network reset")
	if got := AnnotateInsufficientDiskSpace(other); !errors.Is(got, other) || IsInsufficientDiskSpace(got) {
		t.Fatalf("non-disk annotate = %v", got)
	}
}

func TestAnnotateInsufficientDiskSpace_PathError(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "x.bin", Err: platformDiskFullErrno()}
	annotated := AnnotateInsufficientDiskSpace(raw)
	if !IsInsufficientDiskSpace(annotated) {
		t.Fatalf("annotated PathError not detected: %v", annotated)
	}
	if !errors.Is(annotated, ErrInsufficientDiskSpace) {
		t.Fatalf("annotated missing sentinel: %v", annotated)
	}
	if _, ok := errors.AsType[*os.PathError](annotated); !ok {
		t.Fatal("annotated lost PathError via unwrap")
	}
	// Raw PathError (no annotate) still matches via utils.IsOSDiskFull.
	if !IsInsufficientDiskSpace(raw) {
		t.Fatal("raw PathError should match IsInsufficientDiskSpace")
	}
	double := fmt.Errorf("write error: %w", annotated)
	if !IsInsufficientDiskSpace(double) {
		t.Fatalf("double-wrapped annotated not detected: %v", double)
	}
}

func TestIsInsufficientDiskSpace_NonDiskErrno(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "x.bin", Err: platformNonDiskErrno()}
	if IsInsufficientDiskSpace(raw) {
		t.Fatalf("non-disk errno matched: %v", raw)
	}
	if got := AnnotateInsufficientDiskSpace(raw); IsInsufficientDiskSpace(got) || !errors.As(got, new(*os.PathError)) {
		t.Fatalf("non-disk annotate = %v", got)
	}
}
