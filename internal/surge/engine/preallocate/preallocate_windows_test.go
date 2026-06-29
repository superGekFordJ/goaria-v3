//go:build windows

// FORK-PATCH: Windows-specific test for the zero-fill fallback path.

package preallocate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPreallocateZeroFill_SeeksFromStart verifies that preallocateZeroFill
// writes from offset 0 even when the file pointer is left at EOF (as happens
// after a Truncate that precedes a failed SetFileValidData). Without the
// leading Seek(0,0) the file would grow to 2x the requested size.
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
