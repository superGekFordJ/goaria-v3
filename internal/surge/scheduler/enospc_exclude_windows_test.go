package scheduler

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDiskFullErrnos_ExcludedFromRetryAndFallback(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "bare_ERROR_DISK_FULL", err: windows.ERROR_DISK_FULL},
		{name: "bare_ERROR_DISK_QUOTA_EXCEEDED", err: windows.ERROR_DISK_QUOTA_EXCEEDED},
		{name: "bare_ERROR_HANDLE_DISK_FULL", err: windows.ERROR_HANDLE_DISK_FULL},
		{
			name: "PathError_ERROR_HANDLE_DISK_FULL",
			err:  &os.PathError{Op: "write", Path: "x", Err: windows.ERROR_HANDLE_DISK_FULL},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if shouldRetryFailedDownload(tt.err, false, 0) {
				t.Fatalf("shouldRetryFailedDownload(%v) = true, want false", tt.err)
			}
			if shouldFallbackToSingle(tt.err, 0, "") {
				t.Fatalf("shouldFallbackToSingle(%v) = true, want false", tt.err)
			}
		})
	}
}
