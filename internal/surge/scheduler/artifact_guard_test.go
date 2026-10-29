package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// A published artifact whose working name still links the same inode must be
// treated as completed — reopening that working name would corrupt the
// published bytes.
func TestRunDownload_PublishedWorkingLinkReturnsCompleted(t *testing.T) {
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "artifact.bin")
	if err := os.WriteFile(finalPath, []byte("published bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(finalPath, finalPath+types.IncompleteSuffix); err != nil {
		t.Fatalf("hardlink fixture requires a same-volume filesystem: %v", err)
	}

	cfg := types.DownloadRecord{
		IsResume:   true,
		DestPath:   finalPath,
		Filename:   "artifact.bin",
		OutputPath: dir,
	}
	if err := RunDownload(context.Background(), &cfg); !errors.Is(err, types.ErrCompleted) {
		t.Fatalf("retained working link must not be resumable: %v", err)
	}
}

// During artifact verification the polled status stays "downloading" but the
// reported speed is gated to zero — a positive byte counter plus a positive
// session clock proves the zero comes from the gate, not an idle fixture.
func TestGetStatus_VerifyingKeepsDownloadingWithZeroSpeed(t *testing.T) {
	const id = "verifying-status"
	prog := progress.New(id, 4096)
	prog.Bytes.VerifiedProgress.Store(1024)
	prog.Session.SetStartTimeForTest(time.Now().Add(-time.Second))
	prog.Verifying.Store(true)

	pool := NewSchedulerForTesting(map[string]types.DownloadRecord{
		id: {
			ID:            id,
			URL:           "https://example.com/blob.bin",
			Filename:      "blob.bin",
			DestPath:      "blob.bin",
			TotalSize:     4096,
			ProgressState: prog,
		},
	})
	defer pool.GracefulShutdown()

	status := pool.GetStatus(id)
	if status == nil {
		t.Fatal("GetStatus returned nil for an active record")
	}
	if status.Status != "downloading" {
		t.Fatalf("verifying download must keep status downloading, got %q", status.Status)
	}
	if status.Speed != 0 {
		t.Fatalf("verifying download must report zero speed, got %v", status.Speed)
	}
}
