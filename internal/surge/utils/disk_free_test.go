package utils

import (
	"testing"
)

func TestHasSufficientDiskSpace(t *testing.T) {
	const buf = DiskSpaceSafetyBuffer

	tests := []struct {
		name      string
		fileSize  int64
		freeBytes int64
		want      bool
	}{
		{name: "zero size always ok", fileSize: 0, freeBytes: 0, want: true},
		{name: "negative size not applicable", fileSize: -1, freeBytes: 0, want: true},
		{name: "free exactly buffer rejects positive", fileSize: 1, freeBytes: buf, want: false},
		{name: "free below buffer rejects positive", fileSize: 1, freeBytes: buf - 1, want: false},
		{name: "free zero rejects positive", fileSize: 1024, freeBytes: 0, want: false},
		{name: "fileSize one over headroom rejects", fileSize: buf + 1, freeBytes: 2 * buf, want: false},
		{name: "fileSize equals headroom allows", fileSize: buf, freeBytes: 2 * buf, want: true},
		{name: "fileSize under headroom allows", fileSize: 1, freeBytes: buf + 1, want: true},
		{name: "ample free allows large file", fileSize: 10 * 1024 * 1024 * 1024, freeBytes: 20 * 1024 * 1024 * 1024, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasSufficientDiskSpace(tt.fileSize, tt.freeBytes)
			if got != tt.want {
				t.Fatalf("HasSufficientDiskSpace(%d, %d) = %v, want %v", tt.fileSize, tt.freeBytes, got, tt.want)
			}
		})
	}
}

func TestFreeDiskBytes_TempDirSmoke(t *testing.T) {
	dir := t.TempDir()
	free, err := FreeDiskBytes(dir)
	if err != nil {
		t.Fatalf("FreeDiskBytes(%q): %v", dir, err)
	}
	if free < 0 {
		t.Fatalf("FreeDiskBytes returned negative: %d", free)
	}
}

func TestFreeDiskBytes_NonexistentAncestor(t *testing.T) {
	dir := t.TempDir()
	nested := dir + "/does/not/exist/yet"
	free, err := FreeDiskBytes(nested)
	if err != nil {
		t.Fatalf("FreeDiskBytes(nonexistent under temp): %v", err)
	}
	if free < 0 {
		t.Fatalf("FreeDiskBytes returned negative: %d", free)
	}
}
