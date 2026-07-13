//go:build windows

// FORK-PATCH: Windows-specific tests for the sparse and zero-fill fallback paths.

package preallocate

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestPreallocateZeroFill_SeeksFromStart verifies that preallocateZeroFill
// (tertiary fallback, used only when FSCTL_SET_SPARSE is unsupported) writes
// from offset 0 even when the file pointer is left at EOF (as happens after
// a Truncate that precedes a failed SetFileValidData). Without the leading
// Seek(0,0) the file would grow to 2x the requested size.
func TestPreallocateZeroFill_SeeksFromStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zerofill.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	const size = int64(2048)
	// Simulate the post-Truncate pointer position (at EOF) that a failed
	// SetFileValidData would leave behind.
	if _, err := file.Seek(size, 0); err != nil {
		t.Fatal(err)
	}

	if err := preallocateZeroFill(file, size); err != nil {
		t.Fatalf("preallocateZeroFill failed: %v", err)
	}

	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Fatalf("file size = %d, want %d (zero-fill must start from offset 0)", info.Size(), size)
	}
}

// TestPreallocateSparse_GrowsAndSeeksFromStart verifies the sparse fallback:
// the file grows to the requested size, unallocated regions read as zeros,
// and the file pointer is at offset 0 after the call (so the downloader
// writes from the beginning).
func TestPreallocateSparse_GrowsAndSeeksFromStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sparse.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	const size = int64(2048)
	if err := preallocateSparse(file, size); err != nil {
		// FSCTL_SET_SPARSE fails on FAT32/exFAT/network shares. Skip instead
		// of failing so the suite passes on non-NTFS temp directories.
		if errno, ok := err.(windows.Errno); ok && (errno == windows.ERROR_NOT_SUPPORTED || errno == windows.ERROR_INVALID_FUNCTION) {
			t.Skipf("preallocateSparse unsupported on this filesystem (FSCTL_SET_SPARSE: %v)", err)
		}
		t.Fatalf("preallocateSparse failed: %v", err)
	}

	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Fatalf("file size = %d, want %d", info.Size(), size)
	}

	// Seek position must be 0 so the downloader writes from the beginning.
	pos, err := file.Seek(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pos != 0 {
		t.Fatalf("file position = %d, want 0 (sparse fallback must seek to start)", pos)
	}

	// Unallocated region (sparse hole) must read as zeros.
	buf := make([]byte, size)
	n, err := file.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("ReadAt failed: %v", err)
	}
	if int64(n) != size {
		t.Fatalf("read %d bytes, want %d", n, size)
	}
	for i, b := range buf {
		if b != 0 {
			t.Fatalf("byte at offset %d = %d, want 0 (sparse hole must read as zero)", i, b)
		}
	}
}
