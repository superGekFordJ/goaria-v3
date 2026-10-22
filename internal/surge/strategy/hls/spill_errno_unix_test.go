//go:build !windows

package hls

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"goaria-v3/internal/surge/types"
)

// transientErrno returns this platform's "retry in a fresh session" errno.
func transientErrno() error { return syscall.EAGAIN }

// diskFullErrno returns this platform's real disk-full errno.
func diskFullErrno() error { return syscall.ENOSPC }

func TestSpillArena_TransientClassifiedRetryable(t *testing.T) {
	a, _, _, ops := newArenaFixture(t, "id-9")
	boom := &os.PathError{Op: "open", Path: a.path, Err: transientErrno()}
	ops.open = func(name string, flag int, perm os.FileMode) (*os.File, error) { return nil, boom }
	_, err := a.append(1, []byte("x"))
	se, ok := errors.AsType[*storageError](err)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	if !se.Retryable {
		t.Fatalf("transient errno not retryable: %+v", se)
	}
}

func TestSpillArena_ENOSPCNotRetryable(t *testing.T) {
	a, _, _, ops := newArenaFixture(t, "id-9")
	ops.open = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: a.path, Err: syscall.ENOSPC}
	}
	_, err := a.append(1, []byte("x"))
	se, ok := errors.AsType[*storageError](err)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	if se.Retryable {
		t.Fatal("ENOSPC marked retryable")
	}
	if !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("not annotated: %v", err)
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("errno lost: %v", err)
	}
}

func TestSpillArena_EDQUOTAnnotated(t *testing.T) {
	a, _, _, ops := newArenaFixture(t, "id-9")
	ops.open = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: a.path, Err: syscall.EDQUOT}
	}
	if _, err := a.append(1, []byte("x")); !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("EDQUOT not annotated: %v", err)
	}
}
