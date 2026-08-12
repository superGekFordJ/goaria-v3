//go:build unix

package utils

import (
	"math"
	"os"
	"syscall"
	"testing"
)

func TestIsOSDiskFull_ENOSPC_Bare(t *testing.T) {
	if !IsOSDiskFull(syscall.ENOSPC) {
		t.Fatal("bare ENOSPC should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_ENOSPC_PathError(t *testing.T) {
	raw := &os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}
	if !IsOSDiskFull(raw) {
		t.Fatal("PathError-wrapped ENOSPC should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_EDQUOT(t *testing.T) {
	if !IsOSDiskFull(syscall.EDQUOT) {
		t.Fatal("bare EDQUOT should match IsOSDiskFull")
	}
	raw := &os.PathError{Op: "write", Path: "x", Err: syscall.EDQUOT}
	if !IsOSDiskFull(raw) {
		t.Fatal("PathError-wrapped EDQUOT should match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_NonDisk_EPERM(t *testing.T) {
	if IsOSDiskFull(syscall.EPERM) {
		t.Fatal("EPERM should not match IsOSDiskFull")
	}
}

func TestIsOSDiskFull_Nil(t *testing.T) {
	if IsOSDiskFull(nil) {
		t.Fatal("nil should not match IsOSDiskFull")
	}
}

func TestClampFreeBytesProduct(t *testing.T) {
	tests := []struct {
		name      string
		blocks    uint64
		blockSize uint64
		want      int64
	}{
		{name: "zero blocks", blocks: 0, blockSize: 4096, want: 0},
		{name: "zero block size", blocks: 100, blockSize: 0, want: 0},
		{name: "normal", blocks: 1000, blockSize: 4096, want: 1000 * 4096},
		{name: "exact MaxInt64", blocks: 1, blockSize: math.MaxInt64, want: math.MaxInt64},
		{name: "overflow clamps", blocks: math.MaxInt64/2 + 1, blockSize: 3, want: math.MaxInt64},
		{name: "huge inputs clamp", blocks: math.MaxUint64, blockSize: math.MaxUint64, want: math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampFreeBytesProduct(tt.blocks, tt.blockSize)
			if got != tt.want {
				t.Fatalf("clampFreeBytesProduct(%d, %d) = %d, want %d", tt.blocks, tt.blockSize, got, tt.want)
			}
			if got < 0 {
				t.Fatalf("clampFreeBytesProduct returned negative: %d", got)
			}
		})
	}
}
