package rpc

import (
	"fmt"
	"testing"

	"goaria-v3/internal/surge/types"
)

func TestErrorCodeForSurgeStatus_DiskSpace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		st   types.DownloadStatus
		want string
	}{
		{
			name: "bare sentinel",
			st:   types.DownloadStatus{Status: "error", Error: types.ErrInsufficientDiskSpace.Error()},
			want: "9",
		},
		{
			name: "wrapped sentinel text",
			st:   types.DownloadStatus{Status: "error", Error: "write error: insufficient disk space"},
			want: "9",
		},
		{
			name: "generic error",
			st:   types.DownloadStatus{Status: "error", Error: "connection reset"},
			want: "1",
		},
		{
			name: "error status empty message",
			st:   types.DownloadStatus{Status: "error"},
			want: "1",
		},
		{
			name: "active no error",
			st:   types.DownloadStatus{Status: "downloading"},
			want: "",
		},
		{
			name: "message without error status still maps",
			st:   types.DownloadStatus{Error: types.ErrInsufficientDiskSpace.Error()},
			want: "9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := errorCodeForSurgeStatus(tt.st); got != tt.want {
				t.Fatalf("errorCodeForSurgeStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConvertTask_ErrorCodeDiskSpace(t *testing.T) {
	t.Parallel()

	task := convertTask(types.DownloadStatus{
		ID:       "sg_disk",
		Filename: "big.bin",
		DestPath: `/tmp/big.bin`,
		Status:   "error",
		Error:    fmt.Sprintf("annotate: %s", types.ErrInsufficientDiskSpace.Error()),
	})
	if task.ErrorCode != "9" {
		t.Fatalf("ErrorCode = %q, want 9", task.ErrorCode)
	}
	if task.ErrorMessage == "" || !isInsufficientDiskSpaceMessage(task.ErrorMessage) {
		t.Fatalf("ErrorMessage = %q, want disk sentinel", task.ErrorMessage)
	}
	if task.Status != "error" {
		t.Fatalf("Status = %q, want error", task.Status)
	}
}

func TestConvertTask_GenericErrorRemainsOne(t *testing.T) {
	t.Parallel()

	task := convertTask(types.DownloadStatus{
		ID:       "sg_net",
		Filename: "file.bin",
		DestPath: `/tmp/file.bin`,
		Status:   "error",
		Error:    "network timeout",
	})
	if task.ErrorCode != "1" {
		t.Fatalf("ErrorCode = %q, want 1", task.ErrorCode)
	}
}

func TestBuildDownloadList_ForwardsMasterCacheError(t *testing.T) {
	t.Parallel()

	engine := &SurgeEngine{}
	engine.SetMasterCacheForTesting([]types.DownloadRecord{{
		ID:       "disk-stopped",
		URL:      "http://example.com/big.bin",
		Filename: "big.bin",
		DestPath: `/tmp/big.bin`,
		Status:   "error",
		Error:    types.ErrInsufficientDiskSpace.Error(),
		TotalSize: 100,
		Downloaded: 40,
	}})

	list := engine.buildDownloadList()
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if list[0].Error != types.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("Error = %q, want sentinel", list[0].Error)
	}

	task := convertTask(list[0])
	if task.ErrorCode != "9" {
		t.Fatalf("ErrorCode = %q, want 9", task.ErrorCode)
	}
}
