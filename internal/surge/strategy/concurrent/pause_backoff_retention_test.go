package concurrent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// Pause during FailOnNth→retry sleep must not invent phantom progress.
// ActiveTask stays registered across WaitingOnLimiter backoff (#566).
func TestConcurrentDownloader_PauseDuringRetryBackoff(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * utils.KiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithFailOnNthRequest(1),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "pause_backoff_test.bin")
	state := progress.New("pause-backoff-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		MaxTaskRetries:            5,
		MinChunkSize:              64 * utils.KiB,
		DialHedgeCount:            0, // deterministic FailOnNth → worker backoff
	}

	progressCh := make(chan types.DownloadEvent, 10)
	downloader := NewConcurrentDownloader("pause-backoff-id", progressCh, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if f, err := os.Create(destPath + ".surge"); err == nil {
		_ = f.Close()
	}

	// Pause mid single-mirror backoff (WaitingOnLimiter), not on a fixed sleep.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			downloader.activeMu.Lock()
			waiting := false
			for _, at := range downloader.activeTasks {
				if at != nil && at.WaitingOnLimiter.Load() {
					waiting = true
					break
				}
			}
			downloader.activeMu.Unlock()
			if waiting {
				state.Pause()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Error("timed out waiting for WaitingOnLimiter before pause")
		state.Pause()
	}()

	err := downloader.Download(ctx, server.URL(), nil, nil, destPath, fileSize)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("Expected ErrPaused, got: %v", err)
	}

	var pausedEvent *types.DownloadEvent
	close(progressCh)
	for event := range progressCh {
		if event.Type == types.EventPaused {
			pausedEvent = &event
			break
		}
	}

	if pausedEvent == nil {
		t.Fatalf("Expected EventPaused on progress channel")
	}

	savedState := pausedEvent.State
	if savedState == nil {
		t.Fatalf("Expected state in paused event")
	}

	if savedState.Downloaded != 0 {
		t.Errorf("Expected 0 bytes downloaded in saved state, but got %d (progress jump bug!)", savedState.Downloaded)
	}

	var remainingBytes int64
	for _, task := range savedState.Tasks {
		remainingBytes += task.Length
	}
	// Mid-backoff with no successful writes: remaining coverage must still span
	// the whole file. Downloaded==0 alone misses the phantom-complete case
	// where Tasks were dropped and Downloaded was recomputed high.
	if remainingBytes != fileSize {
		t.Errorf("remaining task coverage = %d (Tasks=%d), want fileSize=%d",
			remainingBytes, len(savedState.Tasks), fileSize)
	}
	if savedState.Downloaded+remainingBytes != fileSize {
		t.Errorf("Downloaded(%d)+remaining(%d) = %d, want fileSize=%d",
			savedState.Downloaded, remainingBytes, savedState.Downloaded+remainingBytes, fileSize)
	}
}
