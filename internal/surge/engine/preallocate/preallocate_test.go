// FORK-PATCH: Tests for the shared preallocate package.

package preallocate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreallocate_SizeGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guard.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	for _, size := range []int64{0, -1} {
		if err := Preallocate(file, size); err != nil {
			t.Fatalf("Preallocate(%d) returned error: %v", size, err)
		}
	}

	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("file size = %d, want 0 (size guard must not grow file)", info.Size())
	}
}

func TestPreallocate_GrowsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grow.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	const size = int64(1024)
	if err := Preallocate(file, size); err != nil {
		t.Fatalf("Preallocate failed: %v", err)
	}

	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Fatalf("file size = %d, want %d", info.Size(), size)
	}
}
