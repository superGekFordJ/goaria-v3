//go:build windows

package hls

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"goaria-v3/internal/surge/types"

	"golang.org/x/sys/windows"
)

// transientErrno returns this platform's "retry in a fresh session" errno.
func transientErrno() error { return windows.ERROR_SHARING_VIOLATION }

// diskFullErrno returns this platform's real disk-full errno.
func diskFullErrno() error { return windows.ERROR_DISK_FULL }

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
		return nil, &os.PathError{Op: "open", Path: a.path, Err: diskFullErrno()}
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
}

func TestSpillArena_WindowsQuotaErrnosAnnotated(t *testing.T) {
	a, _, _, ops := newArenaFixture(t, "id-9")
	for _, errno := range []syscall.Errno{windows.ERROR_DISK_FULL, windows.ERROR_DISK_QUOTA_EXCEEDED, windows.ERROR_HANDLE_DISK_FULL} {
		ops.open = func(name string, flag int, perm os.FileMode) (*os.File, error) {
			return nil, &os.PathError{Op: "open", Path: a.path, Err: errno}
		}
		if _, err := a.append(1, []byte("x")); !errors.Is(err, types.ErrInsufficientDiskSpace) {
			t.Fatalf("errno %v not annotated: %v", errno, err)
		}
	}
}

func TestSpillArena_LockViolationRetryable(t *testing.T) {
	a, _, _, ops := newArenaFixture(t, "id-9")
	ops.open = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: a.path, Err: windows.ERROR_LOCK_VIOLATION}
	}
	_, err := a.append(1, []byte("x"))
	if se, ok := errors.AsType[*storageError](err); !ok || !se.Retryable {
		t.Fatalf("lock violation: %v", err)
	}
}
