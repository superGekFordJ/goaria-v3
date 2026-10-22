//go:build linux

package hls

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func tempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "rc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestIsRangeCopyUnsupported_ErrnoTable(t *testing.T) {
	for _, errno := range []syscall.Errno{
		unix.EXDEV, unix.ENOSYS, unix.EOPNOTSUPP, unix.EINVAL,
	} {
		if !isRangeCopyUnsupported(errno) {
			t.Fatalf("errno %v should mark range copy unsupported", errno)
		}
		wrapped := &os.PathError{Op: "copy_file_range", Err: errno}
		if !isRangeCopyUnsupported(wrapped) {
			t.Fatalf("wrapped errno %v should mark range copy unsupported", errno)
		}
	}
	for _, errno := range []syscall.Errno{unix.EIO, unix.EPERM, unix.EBADF} {
		if isRangeCopyUnsupported(errno) {
			t.Fatalf("errno %v should be a hard error, not unsupported", errno)
		}
	}
	if isRangeCopyUnsupported(errors.New("plain")) {
		t.Fatal("non-errno error must not be treated as unsupported")
	}
}

func TestRangeCopyLoop_ShortCopyProgresses(t *testing.T) {
	dst := tempFile(t)
	src := tempFile(t)
	var calls int
	var next int64
	cpy := func(rfd int, roff *int64, wfd int, woff *int64, n int) (int, error) {
		calls++
		if *roff != next || *woff != 100+next {
			t.Fatalf("call %d offsets roff=%d woff=%d want %d", calls, *roff, *woff, next)
		}
		if calls < 4 {
			next++
			return 1, nil // drip-feed one byte at a time
		}
		next += int64(n)
		return n, nil // drain the rest
	}
	total, unsupported, err := rangeCopyLoop(dst, src, 100, 0, 10, cpy)
	if err != nil || unsupported || total != 10 || calls != 4 {
		t.Fatalf("total=%d unsupported=%v calls=%d err=%v", total, unsupported, calls, err)
	}
}

func TestRangeCopyLoop_ZeroProgressIsEOF(t *testing.T) {
	dst := tempFile(t)
	src := tempFile(t)
	cpy := func(rfd int, roff *int64, wfd int, woff *int64, n int) (int, error) {
		return 0, nil // source short: kernel reports EOF as (0, nil)
	}
	total, _, err := rangeCopyLoop(dst, src, 0, 0, 4, cpy)
	if total != 0 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("total=%d err=%v", total, err)
	}
}

func TestRangeCopyLoop_PartialThenUnsupported(t *testing.T) {
	dst := tempFile(t)
	src := tempFile(t)
	var calls int
	cpy := func(rfd int, roff *int64, wfd int, woff *int64, n int) (int, error) {
		calls++
		if calls == 1 {
			return 3, nil
		}
		return 0, unix.EXDEV
	}
	total, unsupported, err := rangeCopyLoop(dst, src, 0, 0, 8, cpy)
	if total != 3 || !unsupported || err != nil {
		t.Fatalf("total=%d unsupported=%v err=%v", total, unsupported, err)
	}
}

func TestRangeCopyLoop_HardErrorPropagates(t *testing.T) {
	dst := tempFile(t)
	src := tempFile(t)
	cpy := func(rfd int, roff *int64, wfd int, woff *int64, n int) (int, error) {
		return 0, unix.EIO
	}
	total, unsupported, err := rangeCopyLoop(dst, src, 0, 0, 4, cpy)
	if total != 0 || unsupported || !errors.Is(err, unix.EIO) {
		t.Fatalf("total=%d unsupported=%v err=%v", total, unsupported, err)
	}
}

// Real-syscall coverage: exercises actual copy_file_range against kernel.
func TestPlatformRangeCopy_RealFiles(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src")
	dstPath := filepath.Join(dir, "dst")
	payload := []byte("0123456789abcdef")
	if err := os.WriteFile(srcPath, append([]byte("XX"), payload...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstPath, make([]byte, 64), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(dstPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	total, unsupported, err := platformRangeCopy(dst, src, 10, 2, int64(len(payload)))
	if err != nil {
		t.Fatalf("copy_file_range: %v", err)
	}
	if unsupported {
		t.Skip("copy_file_range unsupported on this kernel/fs")
	}
	if total != int64(len(payload)) {
		t.Fatalf("copied %d bytes", total)
	}
	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[10:10+len(payload)]) != string(payload) {
		t.Fatalf("dst payload %q", got[10:10+len(payload)])
	}
}
