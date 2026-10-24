package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func TestNew(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	if pool == nil {
		t.Fatal("Expected non-nil Scheduler")
	}

	if pool.taskCond == nil {
		t.Error("Expected taskCond to be initialized")
	}

	if pool.progressCh != ch {
		t.Error("Expected progressCh to be set correctly")
	}

	if pool.downloads == nil {
		t.Error("Expected downloads map to be initialized")
	}

	if pool.maxDownloads != 3 {
		t.Errorf("Expected maxDownloads=3, got %d", pool.maxDownloads)
	}
}

func TestNewScheduler_MaxDownloadsValidation(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)

	tests := []struct {
		name         string
		maxDownloads int
		wantMax      int
	}{
		{"zero defaults to 3", 0, 3},
		{"negative defaults to 3", -1, 3},
		{"valid value 1", 1, 1},
		{"valid value 5", 5, 5},
		{"valid value 10", 10, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := New(ch, tt.maxDownloads)
			if pool.maxDownloads != tt.wantMax {
				t.Errorf("maxDownloads = %d, want %d", pool.maxDownloads, tt.wantMax)
			}
		})
	}
}

func TestNewScheduler_NilChannel(t *testing.T) {
	pool := New(nil, 3)

	if pool == nil {
		t.Fatal("Expected non-nil Scheduler even with nil channel")
	}

	if pool.progressCh != nil {
		t.Error("Expected progressCh to be nil")
	}
}

func TestScheduler_Add_QueuesToChannel(t *testing.T) {
	pool := NewSchedulerForTesting(nil)
	t.Cleanup(pool.GracefulShutdown)

	cfg := types.DownloadRecord{
		ID:            "test-id",
		URL:           "http://example.com/file.zip",
		ProgressState: progress.New("test-id", 1000),
	}

	// Add should not wait for a worker to consume the queued task.
	done := make(chan struct{}, 1)
	go func() {
		pool.Add(cfg)
		done <- struct{}{}
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("Add() blocked unexpectedly")
	}

	pool.mu.RLock()
	_, queued := pool.queued[cfg.ID]
	pool.mu.RUnlock()
	if !queued {
		t.Fatal("Add() did not retain the queued task")
	}
}

func TestScheduler_Pause_NonExistentDownload(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// Should not panic when pausing non-existent download
	pool.Pause("non-existent-id")

	// No message should be sent
	select {
	case <-ch:
		t.Error("Should not send message for non-existent download")
	default:
		// Expected - no message
	}
}

func TestScheduler_Pause_ActiveDownload(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// Create a progress state
	state := progress.New("test-id", 1000)
	state.Bytes.Downloaded.Store(500)
	state.Bytes.VerifiedProgress.Store(700)

	// Manually add an active download
	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: state,
		},
	}
	pool.mu.Unlock()

	pool.Pause("test-id")

	// Check that state is paused
	if !state.IsPaused() {
		t.Error("Expected state to be marked as paused")
	}
}

func TestScheduler_Pause_NilState(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)
	canceled := make(chan struct{}, 1)

	// Add download with nil state
	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: nil,
		},
		cancel: func() {
			select {
			case canceled <- struct{}{}:
			default:
			}
		},
	}
	pool.mu.Unlock()

	// Should not panic with nil state
	pool.Pause("test-id")

	select {
	case <-canceled:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Error("Expected pause to cancel worker context")
	}
}

func TestScheduler_Pause_QueuedDownload_RemovesItBeforeStart(t *testing.T) {
	state := progress.New("queued-id", 1000)
	state.Bytes.VerifiedProgress.Store(321)
	state.SetRateLimit(2048, true)
	pool := &Scheduler{
		downloads:        make(map[string]*activeDownload),
		queued:           make(map[string]*queuedTask),
		downloadLimiters: make(map[string]*transport.RateLimiter),
	}
	pool.taskCond = sync.NewCond(&pool.mu)
	pool.queued["queued-id"] = &queuedTask{cfg: types.DownloadRecord{
		ID:            "queued-id",
		ProgressState: state,
	}}
	pool.queueOrder = []string{"queued-id"}
	pool.wg.Add(1)

	result := pool.Pause("queued-id")
	if !result.Found || result.QueuedConfig == nil {
		t.Fatalf("Pause() = %+v, want queued download to be paused", result)
	}
	if result.QueuedConfig.Downloaded != 321 || result.QueuedConfig.RateLimit != 2048 || !result.QueuedConfig.RateLimitSet {
		t.Fatalf("queued metadata = %+v, want downloaded=321 rate=2048 explicit=true", result.QueuedConfig)
	}

	pool.mu.RLock()
	_, queued := pool.queued["queued-id"]
	queueOrder := append([]string(nil), pool.queueOrder...)
	pool.mu.RUnlock()
	if queued || len(queueOrder) != 0 {
		t.Fatalf("queued state remains after pause: queued=%v queueOrder=%v", queued, queueOrder)
	}

	done := make(chan struct{})
	go func() {
		pool.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Pause did not balance the queued task wait group")
	}
}

func TestScheduler_PauseAll_NoDownloads(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// Should not panic with no downloads
	pool.PauseAll()

	// No messages should be sent
	select {
	case <-ch:
		t.Error("Should not send message when no downloads exist")
	default:
		// Expected
	}
}

func TestScheduler_PauseAll_MultipleDownloads(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// Add multiple active downloads
	states := make([]*progress.DownloadProgress, 3)
	for i := range 3 {
		id := string(rune('a' + i))
		states[i] = progress.New(id, 1000)
		pool.mu.Lock()
		pool.downloads[id] = &activeDownload{
			config: types.DownloadRecord{
				ID:            id,
				ProgressState: states[i],
			},
		}
		pool.mu.Unlock()
	}

	pool.PauseAll()

	// All should be paused
	for i, state := range states {
		if !state.IsPaused() {
			t.Errorf("Download %d should be paused", i)
		}
	}
}

func TestScheduler_PauseAll_SkipsAlreadyPaused(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// Add one paused and one active download
	activeState := progress.New("active", 1000)
	pausedState := progress.New("paused", 1000)
	pausedState.Paused.Store(true)

	pool.mu.Lock()
	pool.downloads["active"] = &activeDownload{
		config: types.DownloadRecord{ID: "active", ProgressState: activeState},
	}
	pool.downloads["paused"] = &activeDownload{
		config: types.DownloadRecord{ID: "paused", ProgressState: pausedState},
	}
	pool.mu.Unlock()

	pool.PauseAll()
}

func TestScheduler_PauseAll_SkipsCompletedDownloads(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// Add one completed and one active download
	activeState := progress.New("active", 1000)
	doneState := progress.New("done", 1000)
	doneState.Done.Store(true)

	pool.mu.Lock()
	pool.downloads["active"] = &activeDownload{
		config: types.DownloadRecord{ID: "active", ProgressState: activeState},
	}
	pool.downloads["done"] = &activeDownload{
		config: types.DownloadRecord{ID: "done", ProgressState: doneState},
	}
	pool.mu.Unlock()

	pool.PauseAll()
}

func TestScheduler_Cancel_NonExistentDownload(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// Should not panic
	pool.Cancel("non-existent-id")
}

func TestScheduler_Cancel_RemovesFromMap(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	state := progress.New("test-id", 1000)

	done := make(chan struct{})
	pool.mu.Lock()
	ad := &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: state,
		},
		done:   done,
		cancel: func() { close(done) },
	}
	ad.running.Store(true)
	pool.downloads["test-id"] = ad
	pool.mu.Unlock()
	pool.mu.Lock()
	pool.downloadLimiters["test-id"] = pool.globalLimiter
	pool.mu.Unlock()

	result := pool.Cancel("test-id")

	if !result.Found {
		t.Error("Expected CancelResult.Found to be true")
	}

	// Pool must NOT emit any event - that's the caller's responsibility
	select {
	case msg := <-ch:
		t.Errorf("Pool should not emit events on cancel, got %T", msg)
	default:
		// expected
	}

	pool.mu.RLock()
	_, exists := pool.downloads["test-id"]
	pool.mu.RUnlock()

	if exists {
		t.Error("Expected download to be removed from map after cancel")
	}

	pool.mu.Lock()
	_, limiterExists := pool.downloadLimiters["test-id"]
	pool.mu.Unlock()
	if limiterExists {
		t.Error("Expected download limiter to be removed after cancel")
	}
}

func TestScheduler_Cancel_CallsCancelFunc(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	ctx, cancel := context.WithCancel(context.Background())
	state := progress.New("test-id", 1000)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: state,
		},
		cancel: cancel,
	}
	pool.mu.Unlock()

	result := pool.Cancel("test-id")
	if !result.Found {
		t.Error("Expected CancelResult.Found")
	}

	// No event should be emitted by pool
	select {
	case msg := <-ch:
		t.Errorf("Pool should not emit events on cancel, got %T", msg)
	default:
		// expected
	}

	// Context should be canceled
	select {
	case <-ctx.Done():
		// Expected
	default:
		t.Error("Expected context to be canceled")
	}
}

func TestScheduler_Cancel_DoesNotMarkDone(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	state := progress.New("test-id", 1000)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: state,
		},
	}
	pool.mu.Unlock()

	result := pool.Cancel("test-id")
	if !result.Found {
		t.Error("Expected CancelResult.Found")
	}

	if state.Done.Load() {
		t.Error("Expected state.Done to remain false after cancel")
	}
}

func TestScheduler_Cancel_DoesNotRemoveIncompleteFile(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "cancel.bin")
	incompletePath := destPath + types.IncompleteSuffix
	if err := os.WriteFile(incompletePath, []byte("partial"), 0o644); err != nil {
		t.Fatalf("failed to create .surge file: %v", err)
	}

	state := progress.New("test-id", 1000)
	state.SetDestPath(destPath)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: state,
		},
	}
	pool.mu.Unlock()

	result := pool.Cancel("test-id")
	if !result.Found {
		t.Fatal("expected cancel to find download")
	}

	if _, err := os.Stat(incompletePath); err != nil {
		t.Fatalf("expected .surge file to remain for centralized delete cleanup, stat err: %v", err)
	}
}

func TestScheduler_Cancel_QueuedDownload_RemovesFromQueueAndReturnsResult(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := &Scheduler{
		progressCh: ch,
		downloads:  make(map[string]*activeDownload),
		queued: map[string]*queuedTask{
			"queued-id": {
				cfg: types.DownloadRecord{
					ID:       "queued-id",
					Filename: "queued.bin",
				},
			},
		},
	}
	pool.taskCond = sync.NewCond(&pool.mu)
	pool.wg.Add(1) // match the queued task

	result := pool.Cancel("queued-id")

	pool.mu.RLock()
	_, exists := pool.queued["queued-id"]
	pool.mu.RUnlock()
	if exists {
		t.Fatal("expected queued download to be removed from queue")
	}

	if !result.Found {
		t.Fatal("expected CancelResult.Found for queued cancel")
	}
	if !result.WasQueued {
		t.Fatal("expected CancelResult.WasQueued for queued cancel")
	}
	if result.Filename != "queued.bin" {
		t.Fatalf("result.Filename = %q, want queued.bin", result.Filename)
	}

	// Pool must NOT emit events - caller handles that
	select {
	case msg := <-ch:
		t.Fatalf("pool should not emit events on cancel, got %T", msg)
	default:
		// expected
	}
}

func TestScheduler_QueuedCfgSnapshot_GetStatusAndCancel(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	const id = "snap-id"
	pool := &Scheduler{
		progressCh: ch,
		downloads:  make(map[string]*activeDownload),
		queued: map[string]*queuedTask{
			id: {
				cfg: types.DownloadRecord{
					ID:           id,
					URL:          "https://example.com/snap.bin",
					Filename:     "snap.bin",
					DestPath:     "/tmp/snap.bin",
					RateLimit:    12345,
					RateLimitSet: true,
				},
			},
		},
	}
	pool.taskCond = sync.NewCond(&pool.mu)
	pool.wg.Add(1)

	status := pool.GetStatus(id)
	if status == nil {
		t.Fatal("expected queued status")
	}
	if status.Status != "queued" {
		t.Fatalf("status.Status = %q, want queued", status.Status)
	}
	if status.URL != "https://example.com/snap.bin" {
		t.Fatalf("status.URL = %q", status.URL)
	}
	if status.Filename != "snap.bin" {
		t.Fatalf("status.Filename = %q, want snap.bin", status.Filename)
	}
	if status.DestPath != "/tmp/snap.bin" {
		t.Fatalf("status.DestPath = %q, want /tmp/snap.bin", status.DestPath)
	}
	if status.RateLimit != 12345 {
		t.Fatalf("status.RateLimit = %d, want 12345", status.RateLimit)
	}
	if !status.RateLimitSet {
		t.Fatal("expected RateLimitSet")
	}

	result := pool.Cancel(id)
	if !result.Found || !result.WasQueued {
		t.Fatalf("Cancel result Found=%v WasQueued=%v", result.Found, result.WasQueued)
	}
	if result.Filename != "snap.bin" {
		t.Fatalf("Cancel Filename = %q, want snap.bin", result.Filename)
	}
	if result.DestPath != "/tmp/snap.bin" {
		t.Fatalf("Cancel DestPath = %q, want /tmp/snap.bin", result.DestPath)
	}
	if got := pool.GetStatus(id); got != nil {
		t.Fatal("expected nil GetStatus after queued cancel")
	}
}

// Resume orchestration (hot/cold path, DB hydration, event emission) was promoted to
// LifecycleManager so the pool remains a pure executor with no knowledge of persistence
// or types. Tests for pool-level extraction live below; LifecycleManager integration
// tests live in internal/processing/manager_test.go (see TestLifecycleManager_Cancel_NotFound).

func TestScheduler_GracefulShutdown_PausesAll(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	state := progress.New("test-id", 1000)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: state,
		},
	}
	pool.mu.Unlock()

	// GracefulShutdown should call PauseAll
	// PauseAll will set IsPausing() = true
	// GracefulShutdown waits for IsPausing() = false
	// We verify that PauseAll was called by checking state.IsPausing()
	// Then we clear it to unblock shutdown

	done := make(chan bool)
	stopCheck := make(chan struct{})
	defer close(stopCheck)
	go func() {
		// Wait for PauseAll to be called (Pausing=true)
		for {
			select {
			case <-stopCheck:
				return
			default:
				if state.IsPausing() {
					state.SetPausing(false)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	go func() {
		pool.GracefulShutdown()
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(500 * time.Millisecond):
		t.Error("GracefulShutdown took too long")
	}

	if !state.IsPaused() {
		t.Error("Expected state to be paused after GracefulShutdown")
	}
}

func TestScheduler_GracefulShutdown_WaitsPastSoftTimeout(t *testing.T) {
	utils.CloseDebug()
	synctest.Test(t, func(t *testing.T) {
		ch := make(chan types.DownloadEvent, 10)
		pool := New(ch, 1)

		ps := progress.New("wait-test-id", 1000)
		pool.mu.Lock()
		ad := &activeDownload{
			config: types.DownloadRecord{
				ID:            "wait-test-id",
				ProgressState: ps,
			},
		}
		ad.running.Store(true)
		pool.downloads["wait-test-id"] = ad
		pool.mu.Unlock()

		origSoftTimeout := gracefulShutdownPauseSoftTimeout
		origPollInterval := gracefulShutdownPausePollInterval
		origHardTimeout := gracefulShutdownPauseHardTimeout
		gracefulShutdownPauseSoftTimeout = 30 * time.Millisecond
		gracefulShutdownPausePollInterval = 5 * time.Millisecond
		gracefulShutdownPauseHardTimeout = 5 * time.Second
		defer func() {
			gracefulShutdownPauseSoftTimeout = origSoftTimeout
			gracefulShutdownPausePollInterval = origPollInterval
			gracefulShutdownPauseHardTimeout = origHardTimeout
		}()

		done := make(chan struct{})
		go func() {
			pool.GracefulShutdown()
			close(done)
		}()
		synctest.Wait()
		if !ps.IsPausing() {
			t.Fatal("expected graceful shutdown to set pausing=true")
		}

		time.Sleep(gracefulShutdownPauseSoftTimeout + 20*time.Millisecond)
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("GracefulShutdown returned before pausing was cleared")
		default:
		}

		ps.SetPausing(false)
		time.Sleep(gracefulShutdownPausePollInterval)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("GracefulShutdown did not return after pausing was cleared")
		}
	})
}

func TestScheduler_GracefulShutdown_ClearsStalePausingWithoutWorker(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 1)

	ps := progress.New("stale-pausing-id", 1000)
	ps.Pause()
	ps.SetPausing(true)

	pool.mu.Lock()
	pool.downloads["stale-pausing-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "stale-pausing-id",
			ProgressState: ps,
		},
	}
	pool.mu.Unlock()

	origPollInterval := gracefulShutdownPausePollInterval
	origHardTimeout := gracefulShutdownPauseHardTimeout
	gracefulShutdownPausePollInterval = 5 * time.Millisecond
	gracefulShutdownPauseHardTimeout = 2 * time.Second
	defer func() {
		gracefulShutdownPausePollInterval = origPollInterval
		gracefulShutdownPauseHardTimeout = origHardTimeout
	}()

	done := make(chan struct{})
	go func() {
		pool.GracefulShutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("GracefulShutdown should not block on stale pausing state")
	}

	if ps.IsPausing() {
		t.Fatal("expected stale pausing flag to be cleared during shutdown")
	}
}

func TestScheduler_ConcurrentPauseCancel(t *testing.T) {
	ch := make(chan types.DownloadEvent, 100)
	pool := New(ch, 3)

	// Add multiple downloads
	for i := range 10 {
		id := string(rune('a' + i))
		state := progress.New(id, 1000)
		pool.mu.Lock()
		pool.downloads[id] = &activeDownload{
			config: types.DownloadRecord{ID: id, ProgressState: state},
		}
		pool.mu.Unlock()
	}

	// Concurrently pause and cancel
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		id := string(rune('a' + i))
		go func(id string) {
			defer wg.Done()
			pool.Pause(id)
			pool.Cancel(id)
		}(id)
	}

	wg.Wait()

	// All should be removed from map
	pool.mu.RLock()
	remaining := len(pool.downloads)
	pool.mu.RUnlock()

	if remaining != 0 {
		t.Errorf("Expected 0 remaining downloads, got %d", remaining)
	}
}

func TestScheduler_HasDownload(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// 1. Test Active Download
	activeURL := "http://example.com/active.zip"
	pool.mu.Lock()
	pool.downloads["active"] = &activeDownload{
		config: types.DownloadRecord{
			ID:  "active",
			URL: activeURL,
		},
	}
	pool.mu.Unlock()

	if !pool.HasDownload(activeURL) {
		t.Error("Expected HasDownload to return true for active download")
	}

	// 2. Test Non-Existent Download
	if pool.HasDownload("http://example.com/missing.zip") {
		t.Error("Expected HasDownload to return false for missing download")
	}

	// For now, this unit test covers the memory-check part of HasDownload which was the critical logic add.
}

// --- ExtractPausedConfig Tests (replaces old pool.Resume tests) ---

func TestScheduler_ExtractPausedConfig_NonExistent(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	// Should return nil for non-existent download
	if cfg := pool.ExtractPausedConfig("non-existent-id"); cfg != nil {
		t.Errorf("Expected nil for non-existent download, got %+v", cfg)
	}
}

func TestScheduler_ExtractPausedConfig_WhilePausing(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	state := progress.New("test-id", 1000)
	state.Paused.Store(true)
	state.SetPausing(true)

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: state,
		},
	}
	pool.mu.Unlock()

	// Should return nil - still pausing (not safe to extract)
	if cfg := pool.ExtractPausedConfig("test-id"); cfg != nil {
		t.Fatal("Expected nil while still pausing")
	}

	// Download must still be in pool
	pool.mu.RLock()
	_, exists := pool.downloads["test-id"]
	pool.mu.RUnlock()
	if !exists {
		t.Error("Expected download to remain in pool while pausing")
	}
}

func TestScheduler_ExtractPausedConfig_NotPaused(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	state := progress.New("test-id", 1000)
	// NOT paused

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			ProgressState: state,
		},
	}
	pool.mu.Unlock()

	if cfg := pool.ExtractPausedConfig("test-id"); cfg != nil {
		t.Fatal("Expected nil for non-paused download")
	}
}

func TestScheduler_ExtractPausedConfig_Success(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	state := progress.New("test-id", 1000)
	state.Paused.Store(true)
	state.SetDestPath("/tmp/final.bin")
	state.SetFilename("final.bin")
	staleLimiter := transport.NewMultiLimiter(pool.globalLimiter, transport.NewRateLimiter(1024, rateLimiterBurst(1024)))

	pool.mu.Lock()
	pool.downloads["test-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "test-id",
			URL:           "http://example.com/file.zip",
			Filename:      "stale.bin",
			ProgressState: state,
			Limiter:       staleLimiter,
		},
	}
	pool.mu.Unlock()
	pool.mu.Lock()
	pool.downloadLimiters["test-id"] = pool.globalLimiter
	pool.mu.Unlock()

	cfg := pool.ExtractPausedConfig("test-id")
	if cfg == nil {
		t.Fatal("Expected config to be returned")
	}

	// State sync: filename and destpath must come from live state
	if cfg.Filename != "final.bin" {
		t.Errorf("Filename = %q, want final.bin", cfg.Filename)
	}
	if cfg.DestPath != "/tmp/final.bin" {
		t.Errorf("DestPath = %q, want /tmp/final.bin", cfg.DestPath)
	}
	if cfg.Limiter != nil {
		t.Error("Expected limiter to be cleared so resume installs a fresh tracked limiter")
	}

	// Download must be removed from pool
	pool.mu.RLock()
	_, exists := pool.downloads["test-id"]
	pool.mu.RUnlock()
	if exists {
		t.Error("Expected download to be removed from pool after extract")
	}

	pool.mu.Lock()
	_, limiterExists := pool.downloadLimiters["test-id"]
	pool.mu.Unlock()
	if limiterExists {
		t.Error("Expected download limiter to be removed from pool after extract")
	}

	// Pause state should be cleared
	if state.IsPaused() {
		t.Error("Expected pause state to be cleared after extract")
	}

	// No events emitted by pool
	select {
	case msg := <-ch:
		t.Errorf("Pool should not emit events on ExtractPausedConfig, got %T", msg)
	default:
		// expected
	}
}

func TestScheduler_PauseResume_Idempotency(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	state := progress.New("idempotent-test", 1000)

	pool.mu.Lock()
	pool.downloads["idempotent-test"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "idempotent-test",
			ProgressState: state,
		},
	}
	pool.mu.Unlock()

	// 1. First Pause
	pool.Pause("idempotent-test")

	// Should be Pausing
	if !state.IsPausing() {
		t.Error("Expected state to be Pausing after first Pause")
	}

	// 2. Second Pause (Idempotent)
	pool.Pause("idempotent-test")

	// Manually transition to Paused (simulating worker finish)
	state.SetPausing(false)
	state.Pause()

	// 3. ExtractPausedConfig (replaces Resume)
	cfg := pool.ExtractPausedConfig("idempotent-test")
	if cfg == nil {
		t.Fatal("Expected config to be extracted after true pause")
	}
	if state.IsPaused() {
		t.Error("Expected state to be cleared after extract")
	}

	// 4. Second ExtractPausedConfig (idempotent - already extracted)
	if cfg2 := pool.ExtractPausedConfig("idempotent-test"); cfg2 != nil {
		t.Error("Expected nil on second extract (already removed from pool)")
	}
}

func TestScheduler_GetStatus_IncludesDestPath(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 1)

	destPath := "/tmp/status-dest.bin"
	st := progress.New("status-id", 1024)
	st.SetDestPath(destPath)

	pool.mu.Lock()
	pool.downloads["status-id"] = &activeDownload{
		config: types.DownloadRecord{
			ID:            "status-id",
			URL:           "https://example.com/file.bin",
			ProgressState: st,
		},
	}
	pool.mu.Unlock()

	got := pool.GetStatus("status-id")
	if got == nil {
		t.Fatal("expected status, got nil")
	}
	if got.DestPath != destPath {
		t.Fatalf("dest_path = %q, want %q", got.DestPath, destPath)
	}
}

func TestScheduler_UpdateURL(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	pool := New(ch, 3)

	activeState := progress.New("active-id", 1000)
	pool.mu.Lock()
	ad := &activeDownload{
		config: types.DownloadRecord{
			ID:            "active-id",
			URL:           "http://example.com/old.zip",
			ProgressState: activeState,
		},
	}
	ad.running.Store(true)
	pool.downloads["active-id"] = ad
	pool.mu.Unlock()

	// 1. Try updating a running download - should fail
	err := pool.UpdateURL("active-id", "http://example.com/new.zip")
	if err == nil {
		t.Error("Expected error when updating URL for active download")
	}

	// 2. Try updating a paused download - pool only updates in-memory (no DB)
	activeState.Paused.Store(true)
	ad.running.Store(false)

	err = pool.UpdateURL("active-id", "http://example.com/new.zip")
	if err != nil {
		t.Errorf("Expected no error for paused download, got %v", err)
	}

	// Verify in-memory URL was updated
	pool.mu.RLock()
	gotURL := pool.downloads["active-id"].config.URL
	pool.mu.RUnlock()
	if gotURL != "http://example.com/new.zip" {
		t.Errorf("in-memory URL not updated: got %q", gotURL)
	}

	// 3. Try updating a queued download - should fail
	pool.mu.Lock()
	pool.queued["queued-id"] = &queuedTask{cfg: types.DownloadRecord{ID: "queued-id"}}
	pool.mu.Unlock()

	err = pool.UpdateURL("queued-id", "http://example.com/new.zip")
	if err == nil || err.Error() != "cannot update URL for a queued download, please cancel or wait for it to start" {
		t.Errorf("Expected queued error, got %v", err)
	}
}

// Note: UpdateURL DB persistence is now tested in internal/processing tests
// since LifecycleManager.UpdateURL() is responsible for calling store.UpdateURL().

// --- GracefulShutdown: queued download discard tests ---

// TestScheduler_GracefulShutdown_ClearsQueuedMap verifies that GracefulShutdown
// removes all entries from p.queued so that idle workers skip any items they
// drain from taskChan after shutdown has started.
func TestScheduler_GracefulShutdown_ClearsQueuedMap(t *testing.T) {
	ch := make(chan types.DownloadEvent, 10)
	// Use a pool with no workers so nothing auto-starts.
	pool := &Scheduler{
		progressCh:   ch,
		progressDone: make(chan struct{}),
		downloads:    make(map[string]*activeDownload),
		queued:       make(map[string]*queuedTask),
		maxDownloads: 0,
	}
	pool.taskCond = sync.NewCond(&pool.mu)

	// Seed the queued map directly (simulating Add() without a live worker).
	pool.mu.Lock()
	for i := range 5 {
		id := fmt.Sprintf("queued-%d", i)
		pool.queued[id] = &queuedTask{cfg: types.DownloadRecord{ID: id, URL: "http://example.com/file.zip"}}
		pool.wg.Add(1)
	}
	pool.mu.Unlock()

	done := make(chan struct{})
	go func() {
		pool.GracefulShutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("GracefulShutdown hung with no active downloads")
	}

	pool.mu.RLock()
	remaining := len(pool.queued)
	pool.mu.RUnlock()

	if remaining != 0 {
		t.Errorf("expected queued map to be empty after GracefulShutdown, got %d entries", remaining)
	}
}

// TestScheduler_GracefulShutdown_WorkerSkipsQueuedAfterShutdown confirms the
// worker-side guard: a worker that pulls a cfg from taskChan after shutdown has
// cleared p.queued will skip the item without starting a download.
func TestScheduler_GracefulShutdown_WorkerSkipsQueuedAfterShutdown(t *testing.T) {
	ch := make(chan types.DownloadEvent, 50)
	// Single worker, small taskChan.
	pool := New(ch, 1)

	// Add via public API to ensure correct internal state (WaitGroup, queued map).
	id := "skip-me"
	pool.Add(types.DownloadRecord{ID: id})

	// Shutdown should drain the queue and close taskChan.
	done := make(chan struct{})
	go func() {
		pool.GracefulShutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GracefulShutdown timed out \u2014 worker may have started a download it should have skipped")
	}

	// The queued map must be empty.
	pool.mu.RLock()
	_, stillQueued := pool.queued[id]
	pool.mu.RUnlock()
	if stillQueued {
		t.Error("expected queued map to be cleared after GracefulShutdown")
	}
}

func TestWorker_SkipsRetriesOnPermanentError(t *testing.T) {
	ch := make(chan types.DownloadEvent, 100)
	pool := New(ch, 1)

	// A server that returns a 403 Forbidden, which should trigger ErrPermanentHTTP
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	id := "test-permanent-error"
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{},
	})

	// Since max retries is 3, if it were to retry, it would take multiple attempts.
	// But because 403 is a permanent error, it should fail immediately and emit exactly ONE EventError.
	timeout := time.After(3 * time.Second)
	errorCount := 0

	for {
		select {
		case msg := <-ch:
			if msg.Type == types.EventError {
				errorCount++
				if !types.IsPermanentHTTPError(msg.Err) {
					t.Errorf("expected error to be permanent HTTP error, got: %v", msg.Err)
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for worker to finish")
		}

		pool.mu.RLock()
		_, inActive := pool.downloads[id]
		_, inQueue := pool.queued[id]
		pool.mu.RUnlock()

		if !inActive && !inQueue && errorCount > 0 {
			break // Worker finished processing the download
		}
	}

	if errorCount != 1 {
		t.Errorf("expected exactly 1 EventError for permanent failure, got %d", errorCount)
	}
}

func TestWorker_Cancel_DoesNotEmitEventError(t *testing.T) {
	ch := make(chan types.DownloadEvent, 100)
	pool := New(ch, 1)
	t.Cleanup(func() { pool.GracefulShutdown() })

	entered := make(chan struct{})
	var enterOnce sync.Once
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "1048576")
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Length", "1048576")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		// Hold the body open until the client context is canceled.
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	id := "test-cancel-no-error"
	state := progress.New(id, 0)
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		OutputPath:    tmpDir,
		Filename:      "cancel.bin",
		DestPath:      filepath.Join(tmpDir, "cancel.bin"),
		ProgressState: state,
		ProgressCh:    ch,
		Runtime:       types.DefaultRuntimeConfig(),
	})

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for download to hit the server")
	}

	result := pool.Cancel(id)
	if !result.Found {
		t.Fatal("expected Cancel to find active download")
	}

	deadline := time.After(5 * time.Second)
	for {
		pool.mu.RLock()
		_, inActive := pool.downloads[id]
		_, inQueue := pool.queued[id]
		pool.mu.RUnlock()
		if !inActive && !inQueue {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for canceled download cleanup")
		case <-time.After(20 * time.Millisecond):
		}
	}

	// Drain any buffered events; cancel must not surface as EventError.
	drainDeadline := time.After(200 * time.Millisecond)
	for {
		select {
		case msg := <-ch:
			if msg.Type == types.EventError {
				t.Fatalf("cancel must not emit EventError, got: %+v err=%v", msg, msg.Err)
			}
		case <-drainDeadline:
			goto drained
		}
	}
drained:

	if state.Done.Load() {
		t.Error("expected Done to remain false after cancel")
	}
	if err := state.GetError(); err != nil {
		t.Errorf("expected no SetError after cancel, got %v", err)
	}
}

func TestScheduler_ControlAPIs_DelegateAndZeroValues(t *testing.T) {
	state := progress.New("dl-1", 1000)
	state.SetScaleWorkersFn(func(delta int) int { return 10 + delta })
	state.SetKillWorkerFn(func(workerID int) bool { return workerID == 7 })
	state.SetSetSlowThresholdFn(func(v float64) {})
	state.SetDrainWorkerFn(func(workerID int) bool { return workerID == 3 })
	state.SetWorkerStats([]types.WorkerSnapshot{{WorkerID: 1, RetryCount: 2}})

	pool := NewSchedulerForTesting(map[string]types.DownloadRecord{
		"dl-1":   {ID: "dl-1", ProgressState: state},
		"dl-nil": {ID: "dl-nil"}, // no ProgressState
	})

	stats := pool.GetWorkerStats("dl-1")
	if len(stats) != 1 || stats[0].WorkerID != 1 {
		t.Fatalf("GetWorkerStats: got %#v", stats)
	}
	if got := pool.ScaleWorkers("dl-1", 2); got != 12 {
		t.Errorf("ScaleWorkers: got %d want 12", got)
	}
	if !pool.KillWorker("dl-1", 7) {
		t.Error("KillWorker: expected true for worker 7")
	}
	if pool.KillWorker("dl-1", 1) {
		t.Error("KillWorker: expected false for worker 1")
	}
	pool.SetSlowWorkerThreshold("dl-1", 0.5) // must not panic
	if !pool.DrainWorker("dl-1", 3) {
		t.Error("DrainWorker: expected true for worker 3")
	}

	// Missing id / nil ProgressState → safe zero values
	if pool.GetWorkerStats("missing") != nil {
		t.Error("missing id: GetWorkerStats should be nil")
	}
	if pool.ScaleWorkers("missing", 1) != 0 {
		t.Error("missing id: ScaleWorkers should be 0")
	}
	if pool.KillWorker("missing", 1) {
		t.Error("missing id: KillWorker should be false")
	}
	if pool.DrainWorker("missing", 1) {
		t.Error("missing id: DrainWorker should be false")
	}
	pool.SetSlowWorkerThreshold("missing", 1) // must not panic
	if pool.GetWorkerStats("dl-nil") != nil {
		t.Error("nil ProgressState: GetWorkerStats should be nil")
	}
	if pool.ScaleWorkers("dl-nil", 1) != 0 {
		t.Error("nil ProgressState: ScaleWorkers should be 0")
	}
}

func TestScheduler_PauseAtVerified_EndgameMatrix(t *testing.T) {
	tests := []struct {
		name         string
		done         bool
		totalSize    int64
		liveTotal    int64
		vp           int64
		wantReturn   bool
		wantCanceled bool
		wantPaused   bool
		wantPausing  bool
	}{
		{
			name:         "Done is true: no-op guard",
			done:         true,
			totalSize:    1000,
			liveTotal:    1000,
			vp:           500,
			wantReturn:   true,
			wantCanceled: false,
			wantPaused:   false,
			wantPausing:  false,
		},
		{
			name:         "Live total known and VP >= total: no-op guard",
			done:         false,
			totalSize:    1000,
			liveTotal:    1000,
			vp:           1000,
			wantReturn:   true,
			wantCanceled: false,
			wantPaused:   false,
			wantPausing:  false,
		},
		{
			name:         "VP < total: normal pause proceeds",
			done:         false,
			totalSize:    1000,
			liveTotal:    1000,
			vp:           900,
			wantReturn:   true,
			wantCanceled: true,
			wantPaused:   true,
			wantPausing:  true,
		},
		{
			name:         "Total unknown: arbitrary VP must not be treated as complete",
			done:         false,
			totalSize:    0,
			liveTotal:    0,
			vp:           5000,
			wantReturn:   true,
			wantCanceled: true,
			wantPaused:   true,
			wantPausing:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			progState := progress.New("guard-test", tc.totalSize)
			if tc.done {
				progState.Done.Store(true)
			}
			if tc.liveTotal > 0 {
				progState.Bytes.TotalSize.Store(tc.liveTotal)
			}
			if tc.vp > 0 {
				progState.Bytes.VerifiedProgress.Store(tc.vp)
			}

			canceled := false
			pool := NewSchedulerForTesting(map[string]types.DownloadRecord{
				"guard-test": {
					ID:            "guard-test",
					TotalSize:     tc.totalSize,
					ProgressState: progState,
				},
			})
			pool.downloads["guard-test"].cancel = func() {
				canceled = true
			}

			res := pool.Pause("guard-test")
			if res.Found != tc.wantReturn {
				t.Errorf("Pause() = %v, want %v", res.Found, tc.wantReturn)
			}
			if canceled != tc.wantCanceled {
				t.Errorf("canceled = %v, want %v", canceled, tc.wantCanceled)
			}
			if progState.IsPaused() != tc.wantPaused {
				t.Errorf("IsPaused() = %v, want %v", progState.IsPaused(), tc.wantPaused)
			}
			if progState.IsPausing() != tc.wantPausing {
				t.Errorf("IsPausing() = %v, want %v", progState.IsPausing(), tc.wantPausing)
			}
		})
	}
}

func TestScheduler_WorkerEndgameSuccessClearsPause(t *testing.T) {
	ch := make(chan types.DownloadEvent, 16)
	pool := New(ch, 1)
	t.Cleanup(func() { pool.GracefulShutdown() })

	body := []byte("worker endgame success")
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer server.Close()

	id := "test-endgame-success"
	state := progress.New(id, int64(len(body)))
	// Simulate late Pause arriving right as physical download completes
	state.SetPausing(true)
	state.Paused.Store(true)

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "endgame.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	pool.Add(types.DownloadRecord{
		ID:                   id,
		URL:                  server.URL,
		OutputPath:           tmpDir,
		Filename:             "endgame.bin",
		DestPath:             destPath,
		ProgressState:        state,
		ProgressCh:           ch,
		Runtime:              types.DefaultRuntimeConfig(),
		TotalSize:            int64(len(body)),
		SupportsRange:        false,
		RangeAcquisitionMode: types.RangeAcquireRangeUnsupported,
	})

	// Deterministic wait for worker to complete via WaitGroup
	done := make(chan struct{})
	go func() {
		pool.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for worker completion")
	}

	if !state.Done.Load() {
		t.Error("expected state.Done to be true on successful endgame completion")
	}
	if state.IsPaused() {
		t.Error("expected stale Paused flag to be cleared on successful endgame completion")
	}
	if state.IsPausing() {
		t.Error("expected Pausing flag to be cleared on successful endgame completion")
	}

	// Verify EventComplete received and download removed from active pool
	pool.mu.RLock()
	_, inDownloads := pool.downloads[id]
	pool.mu.RUnlock()
	if inDownloads {
		t.Error("completed download must be removed from pool.downloads")
	}

	var completeReceived bool
	for len(ch) > 0 {
		msg := <-ch
		if msg.Type == types.EventError {
			t.Fatalf("unexpected EventError: %+v", msg)
		}
		if msg.Type == types.EventPaused {
			t.Fatalf("unexpected EventPaused when physical download completed: %+v", msg)
		}
		if msg.Type == types.EventComplete {
			completeReceived = true
		}
	}
	if !completeReceived {
		t.Error("expected EventComplete to be emitted")
	}
}

func TestScheduler_WorkerRealErrorWithPauseFlagDoesNotSwallowError(t *testing.T) {
	ch := make(chan types.DownloadEvent, 16)
	pool := New(ch, 1)
	t.Cleanup(func() { pool.GracefulShutdown() })

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	id := "test-error-not-swallowed"
	state := progress.New(id, 0)
	// Simulate isPaused == true when worker finishes with a real error
	state.Paused.Store(true)

	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		ProgressState: state,
		ProgressCh:    ch,
		Runtime:       types.DefaultRuntimeConfig(),
	})

	// Deterministic wait for worker to complete via WaitGroup
	done := make(chan struct{})
	go func() {
		pool.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for worker completion")
	}

	// Drain events: must receive EventError, must NOT receive EventPaused
	var errorReceived bool
	for len(ch) > 0 {
		msg := <-ch
		if msg.Type == types.EventPaused {
			t.Fatalf("real error must not be swallowed into EventPaused: %+v", msg)
		}
		if msg.Type == types.EventError {
			errorReceived = true
		}
	}
	if !errorReceived {
		t.Error("expected EventError to be emitted for real error despite isPaused=true")
	}

	pool.mu.RLock()
	_, inDownloads := pool.downloads[id]
	pool.mu.RUnlock()
	if inDownloads {
		t.Error("failed download must not be retained in pool.downloads")
	}
}

// Retry eligibility gate — real timers required (NOT synctest: the fake
// clock fires the armed AfterFunc instantly and voids the gate check).
func TestSchedulerRetryNotRunnableBeforeRetryAt(t *testing.T) {
	ch := make(chan types.DownloadEvent, 100)
	pool := &Scheduler{
		progressCh:       ch,
		progressDone:     make(chan struct{}),
		downloads:        make(map[string]*activeDownload),
		queued:           make(map[string]*queuedTask),
		maxDownloads:     1,
		globalLimiter:    transport.NewRateLimiter(0, 0),
		downloadLimiters: make(map[string]*transport.RateLimiter),
	}
	pool.taskCond = sync.NewCond(&pool.mu)
	// Armed retry timer callbacks are real goroutines; disarm + wg settle via
	// shutdown (runs last — cleanups are LIFO).
	t.Cleanup(func() { pool.GracefulShutdown() })

	pool.mu.Lock()
	id := "delayed-task"
	retryDelay := 150 * time.Millisecond
	qt := &queuedTask{
		cfg: types.DownloadRecord{
			ID:  id,
			URL: "http://example.com/delayed.bin",
		},
		retries:  1,
		inFlight: false,
		retryAt:  time.Now().Add(retryDelay),
	}
	pool.queued[id] = qt
	pool.queueOrder = append(pool.queueOrder, id)
	pool.wg.Add(1)
	pool.mu.Unlock()

	// Scrub fallback for failure paths: if the task was never dequeued (or was
	// picked up and left inFlight), GracefulShutdown's sweep would skip its
	// wg.Done and hang — remove it here first (runs before shutdown, LIFO).
	// A synthetic waitForTask claim also leaves a physical busy slot that the
	// drain waits on, so release it alongside the queue entry.
	t.Cleanup(func() {
		pool.mu.Lock()
		if q, ok := pool.queued[id]; ok {
			if wake := pool.releaseBusyLocked(AdmissionKey{ID: id, Token: q.token}); wake != nil {
				defer wake()
			}
			delete(pool.queued, id)
			pool.wg.Done()
		}
		pool.mu.Unlock()
	})

	gotID := make(chan *queuedTask, 1)
	go func() {
		qt, _ := pool.waitForTask()
		gotID <- qt
	}()

	// At 40ms (before retryAt), task should NOT be popped yet
	select {
	case res := <-gotID:
		t.Fatalf("task %v was popped before retryAt elapsed", res)
	case <-time.After(40 * time.Millisecond):
		// Expected: still waiting
	}

	// Wait for retryAt to pass (after 250ms total)
	select {
	case res := <-gotID:
		if res == nil || res.cfg.ID != id {
			t.Fatalf("got task %v, want %s", res, id)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for delayed task to become runnable after retryAt")
	}

	pool.mu.Lock()
	if q, ok := pool.queued[id]; ok {
		pool.releaseBusyLocked(AdmissionKey{ID: id, Token: q.token})
		q.inFlight = false
		delete(pool.queued, id)
		pool.wg.Done()
	}
	pool.mu.Unlock()
}

// A task waiting out its retry backoff must not hold the worker slot: with a
// single worker, a queued task is still picked up while the failed task's
// retryAt is pending (pre-patch the worker blocked on time.After instead).
func TestWorker_RetryBackoffDoesNotHoldWorkerSlot(t *testing.T) {
	ch := make(chan types.DownloadEvent, 100)
	pool := New(ch, 1)
	t.Cleanup(func() { pool.GracefulShutdown() })

	// Always-503 without Retry-After: non-permanent, so the worker requeues
	// with retryAt ≈ now+1s instead of reporting a terminal error.
	serverA := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer serverA.Close()

	serverB := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer serverB.Close()

	pool.Add(types.DownloadRecord{
		ID:            "retrying-task",
		URL:           serverA.URL,
		ProgressState: progress.New("retrying-task", 0),
		Runtime:       &types.RuntimeConfig{},
	})

	tmpDir := t.TempDir()
	bFinal := filepath.Join(tmpDir, "b.bin")
	if err := os.WriteFile(bFinal+types.IncompleteSuffix, nil, 0o644); err != nil {
		t.Fatalf("failed to pre-create incomplete file: %v", err)
	}
	pool.Add(types.DownloadRecord{
		ID:            "queued-task",
		URL:           serverB.URL,
		OutputPath:    tmpDir,
		Filename:      "b.bin",
		ProgressState: progress.New("queued-task", 0),
		Runtime:       &types.RuntimeConfig{},
	})

	// A's first retry waits 1s; B's EventStarted must arrive well before that.
	// Require A's EventQueued first so the test only passes if A actually
	// re-entered the queue (a terminal error would free the slot trivially).
	var sawRequeue bool
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case msg := <-ch:
			if msg.Type == types.EventQueued && msg.DownloadID == "retrying-task" {
				sawRequeue = true
			}
			if msg.Type == types.EventStarted && msg.DownloadID == "queued-task" {
				if !sawRequeue {
					t.Fatal("queued task started before the failed task was requeued")
				}
				return
			}
		case <-deadline:
			t.Fatal("queued task did not start while the failed task sat in retry backoff")
		}
	}
}

// A task parked in retry backoff arms the wake timer; GracefulShutdown must
// disarm it and return promptly instead of waiting for retryAt.
func TestSchedulerGracefulShutdownDisarmsRetryTimer(t *testing.T) {
	pool := &Scheduler{
		progressCh:       make(chan types.DownloadEvent, 8),
		progressDone:     make(chan struct{}),
		downloads:        make(map[string]*activeDownload),
		queued:           make(map[string]*queuedTask),
		maxDownloads:     1,
		globalLimiter:    transport.NewRateLimiter(0, 0),
		downloadLimiters: make(map[string]*transport.RateLimiter),
	}
	pool.taskCond = sync.NewCond(&pool.mu)

	pool.mu.Lock()
	id := "pending-retry"
	pool.queued[id] = &queuedTask{
		cfg:     types.DownloadRecord{ID: id, URL: "http://example.com/pending.bin"},
		retries: 1,
		retryAt: time.Now().Add(time.Hour),
	}
	pool.queueOrder = append(pool.queueOrder, id)
	pool.wg.Add(1)
	pool.mu.Unlock()

	waited := make(chan *queuedTask, 1)
	go func() {
		qt, _ := pool.waitForTask()
		waited <- qt
	}()

	// Wait until the scan has actually armed the timer.
	armDeadline := time.Now().Add(2 * time.Second)
	for {
		pool.mu.Lock()
		armed := pool.retryTimer != nil
		pool.mu.Unlock()
		if armed {
			break
		}
		if time.Now().After(armDeadline) {
			t.Fatal("retry timer was never armed")
		}
		time.Sleep(5 * time.Millisecond)
	}

	done := make(chan struct{})
	go func() {
		pool.GracefulShutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GracefulShutdown hung with an armed retry timer")
	}

	if got := <-waited; got != nil {
		t.Fatalf("waitForTask returned %v after shutdown, want nil", got)
	}
}

// Pickup ABA: a worker that claimed qt1 but has not registered it yet must not
// adopt a same-ID replacement (qt2) queued in between; it settles qt1's wait
// group and lets a fresh pickup own qt2.
func TestScheduler_PickupABA_PauseAddSameID(t *testing.T) {
	ch := make(chan types.DownloadEvent, 16)
	pool := New(ch, 1)

	oldGate := pool.workerClaimedGate.Load()
	// LIFO: shutdown runs before the restore so no worker reads the gate
	// concurrently with the write.
	t.Cleanup(func() { pool.workerClaimedGate.Store(oldGate) })
	t.Cleanup(pool.GracefulShutdown)

	claimed := make(chan struct{})
	release := make(chan struct{})
	gateFn := func() {
		pool.workerClaimedGate.Store(nil)
		close(claimed)
		<-release
	}
	pool.workerClaimedGate.Store(&gateFn)
	var releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWorker()

	serverB := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer serverB.Close()

	const id = "aba-pickup"
	tmpDir := t.TempDir()
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           "http://example.com/stale.bin",
		OutputPath:    tmpDir,
		Filename:      "aba.bin",
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{},
	})

	select {
	case <-claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never claimed the queued task")
	}

	// The worker is parked between claim and registration with qt1 in-flight.
	result := pool.Pause(id)
	if !result.Found || result.QueuedConfig == nil {
		t.Fatalf("Pause() = %+v, want queued removal of the claimed task", result)
	}
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           serverB.URL,
		OutputPath:    tmpDir,
		Filename:      "aba2.bin",
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{},
	})
	pool.mu.RLock()
	qt2 := pool.queued[id]
	pool.mu.RUnlock()
	if qt2 == nil {
		t.Fatal("same-ID replacement was not queued")
	}

	releaseWorker()

	// The stale claim must not start or clobber qt2; a fresh pickup starts the
	// replacement URL.
	startDeadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == types.EventStarted && ev.DownloadID == id {
				if ev.URL != serverB.URL {
					t.Fatalf("started URL = %q, want replacement %q", ev.URL, serverB.URL)
				}
				goto started
			}
		case <-startDeadline:
			t.Fatal("replacement task never started")
		}
	}
started:

	done := make(chan struct{})
	go func() {
		pool.GracefulShutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("GracefulShutdown hung; claimed task wait group was not settled")
	}
}

// Retry-publish ABA: a worker that requeued qt1 in-flight and is parked in the
// EventQueued send must not overwrite a same-ID replacement (qt2) on re-lock;
// it settles qt1's wait group instead.
func TestScheduler_RetryPublishABA_PauseAddSameID(t *testing.T) {
	ch := make(chan types.DownloadEvent, 16)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	serverA := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer serverA.Close()
	serverB := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer serverB.Close()

	// Unbuffered: every send on the task's progress channel parks the worker
	// until the test receives it.
	gateCh := make(chan types.DownloadEvent)

	const id = "aba-retry"
	tmpDir := t.TempDir()
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           serverA.URL,
		OutputPath:    tmpDir,
		Filename:      "retry.bin",
		ProgressState: progress.New(id, 0),
		ProgressCh:    gateCh,
		Runtime:       &types.RuntimeConfig{},
	})

	// Admit EventStarted; the worker then fails (503), requeues qt1 in-flight,
	// and parks in the EventQueued send.
	select {
	case ev := <-gateCh:
		if ev.Type != types.EventStarted || ev.DownloadID != id {
			t.Fatalf("first send = %+v, want EventStarted for %s", ev, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker never sent EventStarted")
	}

	requeueDeadline := time.Now().Add(10 * time.Second)
	for {
		pool.mu.RLock()
		qt, ok := pool.queued[id]
		requeued := ok && qt.inFlight && !qt.retryAt.IsZero()
		pool.mu.RUnlock()
		if requeued {
			break
		}
		if time.Now().After(requeueDeadline) {
			t.Fatal("worker never requeued the failed download")
		}
		time.Sleep(5 * time.Millisecond)
	}

	result := pool.Pause(id)
	if !result.Found || result.QueuedConfig == nil {
		t.Fatalf("Pause() = %+v, want queued removal of the in-flight task", result)
	}
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           serverB.URL,
		OutputPath:    tmpDir,
		Filename:      "retry2.bin",
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{},
	})
	pool.mu.RLock()
	qt2 := pool.queued[id]
	pool.mu.RUnlock()
	if qt2 == nil {
		t.Fatal("same-ID replacement was not queued")
	}

	// Release the held EventQueued send; on re-lock the worker must see qt2,
	// not overwrite it with its stale qt1.
	select {
	case ev := <-gateCh:
		if ev.Type != types.EventQueued || ev.DownloadID != id {
			t.Fatalf("held send = %+v, want EventQueued for %s", ev, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker was not parked on the EventQueued send")
	}

	// qt2 survives intact: a fresh pickup starts the replacement URL.
	startDeadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == types.EventStarted && ev.DownloadID == id {
				if ev.URL != serverB.URL {
					t.Fatalf("started URL = %q, want replacement %q", ev.URL, serverB.URL)
				}
				goto started
			}
		case <-startDeadline:
			t.Fatal("replacement task never started")
		}
	}
started:

	done := make(chan struct{})
	go func() {
		pool.GracefulShutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("GracefulShutdown hung; requeued task wait group was not settled")
	}
}

// ==================== Managed admission (opportunity tokens) ====================

// newManagedPool builds a managed scheduler or fails the test.
func newManagedPool(t *testing.T, ch chan types.DownloadEvent, capacity int) *Scheduler {
	t.Helper()
	pool, err := NewManaged(ch, capacity)
	if err != nil {
		t.Fatalf("NewManaged(%d) returned error: %v", capacity, err)
	}
	t.Cleanup(pool.GracefulShutdown)
	return pool
}

// admissionKeyFor returns the candidate key for id from a fresh snapshot.
func admissionKeyFor(t *testing.T, pool *Scheduler, id string) AdmissionKey {
	t.Helper()
	for _, cand := range pool.AdmissionSnapshot().Candidates {
		if cand.Key.ID == id {
			return cand.Key
		}
	}
	t.Fatalf("snapshot has no candidate for %s", id)
	return AdmissionKey{}
}

// A managed pool never self-picks a queued task; only an accepted Admit of the
// snapshot key dispatches it. Legacy New() pools keep self-picking.
func TestSchedulerAdmission_ManagedWaitsForAdmit(t *testing.T) {
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	managedCh := make(chan types.DownloadEvent, 16)
	managed := newManagedPool(t, managedCh, 1)

	const id = "managed-gated"
	if !managed.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 4},
	}) {
		t.Fatal("managed Add was refused")
	}

	// No Admit yet: the worker must not self-pick.
	select {
	case ev := <-managedCh:
		t.Fatalf("managed pool dispatched without Admit: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}

	snap := managed.AdmissionSnapshot()
	if !snap.Managed || snap.Capacity != 1 || snap.CommittedSlots != 0 {
		t.Fatalf("pre-admit snapshot = %+v", snap)
	}
	if len(snap.Candidates) != 1 || !snap.Candidates[0].RuntimeReady || snap.Candidates[0].Workers != 4 {
		t.Fatalf("candidate = %+v", snap.Candidates)
	}
	key := snap.Candidates[0].Key
	if key.Token == 0 {
		t.Fatal("candidate token is zero")
	}

	if res := managed.Admit(key, 2, 0); res != AdmitAccepted {
		t.Fatalf("Admit = %v, want Accepted", res)
	}
	// Duplicate admit of the same live opportunity must not re-account.
	if res := managed.Admit(key, 2, 0); res != AdmitAlreadyAdmitted {
		t.Fatalf("repeat Admit = %v, want AlreadyAdmitted", res)
	}
	if snap := managed.AdmissionSnapshot(); snap.CommittedSlots != 1 {
		t.Fatalf("committed after Admit = %d, want 1", snap.CommittedSlots)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-managedCh:
			if ev.Type == types.EventStarted && ev.DownloadID == id {
				return
			}
		case <-deadline:
			t.Fatal("admitted task never started")
		}
	}
}

// Legacy pools ignore the admission gate entirely: Admit is Unavailable and
// queued tasks self-pick as before.
func TestSchedulerAdmission_LegacyUnchanged(t *testing.T) {
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	ch := make(chan types.DownloadEvent, 16)
	pool := New(ch, 1)
	t.Cleanup(pool.GracefulShutdown)

	const id = "legacy-selfpick"
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 2},
	}) {
		t.Fatal("Add was refused")
	}
	key := admissionKeyFor(t, pool, id)
	if res := pool.Admit(key, 1, 0); res != AdmitUnavailable {
		t.Fatalf("legacy Admit = %v, want Unavailable", res)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == types.EventStarted && ev.DownloadID == id {
				return
			}
		case <-deadline:
			t.Fatal("legacy queued task never self-picked")
		}
	}
}

// Stale tokens, zero/unknown keys, and keys orphaned by a same-ID re-add must
// never alias the live opportunity; each new queueing round mints a fresh
// token.
func TestSchedulerAdmission_StaleKeysAndSameID(t *testing.T) {
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	ch := make(chan types.DownloadEvent, 16)
	pool := newManagedPool(t, ch, 1)

	const id = "sameid-admit"
	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "sameid.bin")
	add := func() bool {
		if err := os.WriteFile(destPath+types.IncompleteSuffix, nil, 0o644); err != nil {
			t.Fatalf("pre-create incomplete file: %v", err)
		}
		return pool.Add(types.DownloadRecord{
			ID:            id,
			URL:           server.URL,
			OutputPath:    tmpDir,
			Filename:      "sameid.bin",
			DestPath:      destPath,
			ProgressState: progress.New(id, 0),
			Runtime:       &types.RuntimeConfig{Workers: 2},
		})
	}
	if !add() {
		t.Fatal("first Add was refused")
	}
	key1 := admissionKeyFor(t, pool, id)

	if res := pool.Admit(AdmissionKey{ID: id, Token: 0}, 1, 0); res != AdmitStale {
		t.Fatalf("zero-token Admit = %v, want Stale", res)
	}
	if res := pool.Admit(AdmissionKey{ID: "ghost", Token: key1.Token}, 1, 0); res != AdmitStale {
		t.Fatalf("unknown-ID Admit = %v, want Stale", res)
	}
	if res := pool.Admit(AdmissionKey{ID: id, Token: key1.Token + 999}, 1, 0); res != AdmitStale {
		t.Fatalf("future-token Admit = %v, want Stale", res)
	}

	// Pause removes the queued opportunity; a same-ID re-add mints a new token.
	if res := pool.Pause(id); !res.Found || res.QueuedConfig == nil {
		t.Fatal("Pause of queued task failed")
	}
	if !add() {
		t.Fatal("same-ID re-add was refused")
	}
	key2 := admissionKeyFor(t, pool, id)
	if key2.Token == key1.Token {
		t.Fatal("token was reused across queueing rounds")
	}
	if res := pool.Admit(key1, 1, 0); res != AdmitStale {
		t.Fatalf("stale-key Admit = %v, want Stale", res)
	}
	if res := pool.Admit(key2, 1, 0); res != AdmitAccepted {
		t.Fatalf("fresh-key Admit = %v, want Accepted", res)
	}

	// Wait for the worker to claim and finish so the busy entry leaves; the
	// retired claim key must not count as authorization afterwards.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == types.EventComplete && ev.DownloadID == id {
				goto completed
			}
		case <-deadline:
			t.Fatal("admitted task never completed")
		}
	}
completed:
	settleDeadline := time.Now().Add(5 * time.Second)
	for {
		snap := pool.AdmissionSnapshot()
		if len(snap.Busy) == 0 && snap.CommittedSlots == 0 {
			break
		}
		if time.Now().After(settleDeadline) {
			t.Fatalf("busy did not settle: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if res := pool.Admit(key2, 1, 0); res != AdmitStale {
		t.Fatalf("post-completion Admit = %v, want Stale", res)
	}
}

// Admit parameter contract: tightening only lowers Workers/MinChunkSize and
// leaves every other runtime field byte-identical; invalid tighten requests
// are Unavailable and mutate nothing.
func TestSchedulerAdmission_TightenOnlyShrinks(t *testing.T) {
	ch := make(chan types.DownloadEvent, 16)
	pool := newManagedPool(t, ch, 1)

	const id = "tighten-only"
	rt := &types.RuntimeConfig{
		Workers:            8,
		MinChunkSize:       4096,
		ProxyURL:           "http://127.0.0.1:9",
		CustomDNS:          "8.8.8.8",
		SequentialDownload: true,
		WorkerBufferSize:   12345,
	}
	if !pool.Add(types.DownloadRecord{
		ID:      id,
		URL:     "http://example.com/tighten.bin",
		Runtime: rt,
	}) {
		t.Fatal("Add was refused")
	}
	key := admissionKeyFor(t, pool, id)

	// Managed Add materializes a runtime ceiling copy (SequentialDownload
	// clamps Workers to 1); rejected Admits must not replace or mutate it.
	pool.mu.RLock()
	baseline := pool.queued[id].cfg.Runtime
	pool.mu.RUnlock()
	if baseline == rt {
		t.Fatal("managed Add kept the caller's runtime pointer")
	}
	if baseline.Workers != 1 || baseline.MinChunkSize != rt.MinChunkSize {
		t.Fatalf("materialized ceiling = %+v, want workers 1 minchunk %d", baseline, rt.MinChunkSize)
	}

	if res := pool.Admit(key, 0, 0); res != AdmitUnavailable {
		t.Fatalf("workers=0 Admit = %v, want Unavailable", res)
	}
	if res := pool.Admit(key, 9, 0); res != AdmitUnavailable {
		t.Fatalf("workers>upper Admit = %v, want Unavailable", res)
	}
	if res := pool.Admit(key, 1, 0); res != AdmitUnavailable {
		t.Fatalf("minChunk=0 with positive upper Admit = %v, want Unavailable", res)
	}
	if res := pool.Admit(key, 1, 8192); res != AdmitUnavailable {
		t.Fatalf("minChunk>upper Admit = %v, want Unavailable", res)
	}
	pool.mu.RLock()
	sameRuntime := pool.queued[id].cfg.Runtime == baseline
	pool.mu.RUnlock()
	if !sameRuntime {
		t.Fatal("rejected Admit replaced the materialized runtime")
	}

	if res := pool.Admit(key, 1, 2048); res != AdmitAccepted {
		t.Fatalf("valid Admit = %v, want Accepted", res)
	}
	pool.mu.RLock()
	got := pool.queued[id].cfg.Runtime
	pool.mu.RUnlock()
	if got == rt || got == baseline {
		t.Fatal("admitted runtime aliases a pre-existing config")
	}
	if got.Workers != 1 || got.MinChunkSize != 2048 {
		t.Fatalf("tightened fields = workers %d minchunk %d, want 1/2048", got.Workers, got.MinChunkSize)
	}
	if got.ProxyURL != rt.ProxyURL || got.CustomDNS != rt.CustomDNS ||
		got.SequentialDownload != rt.SequentialDownload || got.WorkerBufferSize != rt.WorkerBufferSize {
		t.Fatalf("Admit rewrote unrelated runtime fields: %+v", got)
	}
}

// A failed run requeues the same queuedTask under a fresh token with
// admitted=false; the stale key stays Stale, the new key is Unavailable while
// the backoff is pending, and the wait group is settled on removal.
func TestSchedulerAdmission_Retry(t *testing.T) {
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ch := make(chan types.DownloadEvent, 16)
	pool := newManagedPool(t, ch, 1)

	const id = "admit-retry"
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 2},
	}) {
		t.Fatal("Add was refused")
	}
	key1 := admissionKeyFor(t, pool, id)
	if res := pool.Admit(key1, 1, 0); res != AdmitAccepted {
		t.Fatalf("Admit = %v, want Accepted", res)
	}

	// Wait for the retry requeue publication.
	requeueDeadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == types.EventQueued && ev.DownloadID == id {
				goto requeued
			}
		case <-requeueDeadline:
			t.Fatal("failed task was never requeued")
		}
	}
requeued:

	var key2 AdmissionKey
	findDeadline := time.Now().Add(2 * time.Second)
	for {
		snap := pool.AdmissionSnapshot()
		found := false
		for _, cand := range snap.Candidates {
			if cand.Key.ID == id {
				found = true
				key2 = cand.Key
				if cand.Admitted {
					t.Fatal("requeued opportunity kept the old admission")
				}
				if cand.Key.Token == key1.Token {
					t.Fatal("requeue reused the finished opportunity token")
				}
			}
		}
		if found {
			break
		}
		if time.Now().After(findDeadline) {
			t.Fatal("requeued task missing from snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if res := pool.Admit(key1, 1, 0); res != AdmitStale {
		t.Fatalf("pre-retry key Admit = %v, want Stale", res)
	}
	// Backoff (1s for the first retry) still pending: not yet claimable.
	if res := pool.Admit(key2, 1, 0); res != AdmitUnavailable {
		t.Fatalf("in-backoff Admit = %v, want Unavailable", res)
	}

	// Wait out the backoff, then admit the new opportunity.
	pool.mu.RLock()
	wait := time.Until(pool.queued[id].retryAt)
	pool.mu.RUnlock()
	if wait > 0 {
		time.Sleep(wait + 20*time.Millisecond)
	}
	if res := pool.Admit(key2, 1, 0); res != AdmitAccepted {
		t.Fatalf("post-backoff Admit = %v, want Accepted", res)
	}
	// The retried run will fail again; Pause drops the queued round so the
	// wait group settles via the cleanup path.
	reDeadline := time.Now().Add(10 * time.Second)
	for {
		pool.mu.RLock()
		_, stillQueued := pool.queued[id]
		pool.mu.RUnlock()
		if stillQueued {
			if res := pool.Pause(id); res.Found {
				return
			}
			// Claimed between the check and the Pause; keep polling for the
			// next requeue round.
		}
		if time.Now().After(reDeadline) {
			t.Fatal("requeued opportunity vanished without settlement")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Capacity ceiling: 64 concurrent Admit calls against a 32-slot managed pool
// may accept at most 32, and a claimed busy slot is counted exactly once.
func TestSchedulerAdmission_Capacity(t *testing.T) {
	release := make(chan struct{})
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	ch := make(chan types.DownloadEvent, 128)
	pool := newManagedPool(t, ch, 32)

	const total = 64
	keys := make([]AdmissionKey, total)
	for i := range total {
		id := fmt.Sprintf("cap-%03d", i)
		if !pool.Add(types.DownloadRecord{
			ID:            id,
			URL:           server.URL,
			ProgressState: progress.New(id, 0),
			Runtime:       &types.RuntimeConfig{Workers: 2},
		}) {
			t.Fatalf("Add %s refused", id)
		}
		keys[i] = admissionKeyFor(t, pool, id)
	}

	var admitWg sync.WaitGroup
	var accepted, already, unavail, stale atomic.Int64
	for _, key := range keys {
		admitWg.Add(1)
		go func(k AdmissionKey) {
			defer admitWg.Done()
			switch pool.Admit(k, 1, 0) {
			case AdmitAccepted:
				accepted.Add(1)
			case AdmitAlreadyAdmitted:
				already.Add(1)
			case AdmitUnavailable:
				unavail.Add(1)
			default:
				stale.Add(1)
			}
		}(key)
	}
	admitWg.Wait()

	if got := accepted.Load(); got != 32 {
		t.Fatalf("accepted admits = %d, want exactly 32 (oversell)", got)
	}
	if stale.Load() != 0 || already.Load() != 0 {
		t.Fatalf("unexpected admit results: stale=%d already=%d", stale.Load(), already.Load())
	}
	if unavail.Load() != total-32 {
		t.Fatalf("unavailable admits = %d, want %d", unavail.Load(), total-32)
	}

	// Claims may still be settling; once all admitted opportunities become
	// busy, the claim must be counted exactly once — CommittedSlots stays 32.
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap := pool.AdmissionSnapshot()
		if snap.CommittedSlots > 32 {
			t.Fatalf("CommittedSlots = %d exceeds capacity 32", snap.CommittedSlots)
		}
		admittedQueued := 0
		for _, cand := range snap.Candidates {
			if cand.Admitted && !cand.InFlight {
				admittedQueued++
			}
		}
		if snap.CommittedSlots != len(snap.Busy)+admittedQueued {
			t.Fatalf("invariant broken: committed=%d busy=%d admitted=%d",
				snap.CommittedSlots, len(snap.Busy), admittedQueued)
		}
		if len(snap.Busy) == 32 {
			if snap.CommittedSlots != 32 {
				t.Fatalf("busy counted twice: committed=%d busy=%d", snap.CommittedSlots, len(snap.Busy))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d/32 claims became busy", len(snap.Busy))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The wake hook fires after the release is visible: inside the callback a
// re-entrant AdmissionSnapshot must already show the slot freed.
func TestSchedulerAdmission_Wake(t *testing.T) {
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1")
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()

	ch := make(chan types.DownloadEvent, 16)
	pool := newManagedPool(t, ch, 1)

	wakeSeen := make(chan AdmissionSnapshot, 4)
	pool.SetAdmissionWake(func() {
		// Re-entrant snapshot inside the notification: must not deadlock and
		// must already observe the freed slot.
		wakeSeen <- pool.AdmissionSnapshot()
	})

	const id = "wake-order"
	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "wake.bin")
	if err := os.WriteFile(destPath+types.IncompleteSuffix, nil, 0o644); err != nil {
		t.Fatalf("pre-create incomplete file: %v", err)
	}
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		OutputPath:    tmpDir,
		Filename:      "wake.bin",
		DestPath:      destPath,
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 2},
	}) {
		t.Fatal("Add was refused")
	}
	key := admissionKeyFor(t, pool, id)
	if res := pool.Admit(key, 1, 0); res != AdmitAccepted {
		t.Fatalf("Admit = %v, want Accepted", res)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == types.EventComplete && ev.DownloadID == id {
				goto completed
			}
		case snap := <-wakeSeen:
			// A stray wake before completion must already show post-release state.
			if len(snap.Busy) > 0 {
				t.Fatalf("wake fired while busy still held: %+v", snap.Busy)
			}
		case <-deadline:
			t.Fatal("download never completed")
		}
	}
completed:
	select {
	case snap := <-wakeSeen:
		if len(snap.Busy) != 0 || snap.CommittedSlots != 0 {
			t.Fatalf("wake snapshot still sees claim: %+v", snap)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("release wake never fired")
	}
}

// Snapshots carry the complete FIFO queue (no truncation) and are pure
// values: mutating the returned slices must not leak into pool state.
func TestSchedulerAdmission_Snapshot(t *testing.T) {
	ch := make(chan types.DownloadEvent, 16)
	pool := newManagedPool(t, ch, 4)

	const total = 1100
	for i := range total {
		id := fmt.Sprintf("snap-%04d", i)
		cfg := types.DownloadRecord{ID: id, URL: "http://example.com/" + id, TotalSize: int64(i + 1)}
		if i == 0 {
			cfg.Runtime = &types.RuntimeConfig{Workers: 7, MinChunkSize: 1024}
			cfg.Downloaded = 42
		}
		if !pool.Add(cfg) {
			t.Fatalf("Add %s refused", id)
		}
	}

	snap := pool.AdmissionSnapshot()
	if !snap.Managed || snap.Capacity != 4 || snap.CommittedSlots != 0 || len(snap.Busy) != 0 {
		t.Fatalf("snapshot header = %+v", snap)
	}
	if len(snap.Candidates) != total {
		t.Fatalf("candidates truncated: %d/%d", len(snap.Candidates), total)
	}
	for i, cand := range snap.Candidates {
		want := fmt.Sprintf("snap-%04d", i)
		if cand.Key.ID != want || cand.Key.Token != uint64(i+1) {
			t.Fatalf("candidate[%d] = %+v, want id %s token %d", i, cand.Key, want, i+1)
		}
	}
	first := snap.Candidates[0]
	if !first.RuntimeReady || first.Workers != 7 || first.MinChunkSize != 1024 ||
		first.TotalSize != 1 || first.Downloaded != 42 {
		t.Fatalf("candidate hints = %+v", first)
	}

	// Mutating the returned values must not alter pool state or later snapshots.
	snap.Candidates[0].Admitted = true
	snap.Candidates[0].Key.Token = 99999
	snap.Busy = append(snap.Busy, BusySlot{Key: AdmissionKey{ID: "fake", Token: 1}})
	again := pool.AdmissionSnapshot()
	if len(again.Candidates) != total || len(again.Busy) != 0 {
		t.Fatalf("mutating snapshot leaked into pool: %+v", again)
	}
	if again.Candidates[0].Admitted || again.Candidates[0].Key.Token != 1 {
		t.Fatalf("mutated candidate persisted: %+v", again.Candidates[0])
	}
}

// Stopping rejects new admits and drains commitments on shutdown.
func TestSchedulerAdmission_StoppingRejects(t *testing.T) {
	ch := make(chan types.DownloadEvent, 16)
	pool := newManagedPool(t, ch, 2)

	const id = "stop-admit"
	if !pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           "http://example.com/stop.bin",
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 2},
	}) {
		t.Fatal("Add was refused")
	}
	key := admissionKeyFor(t, pool, id)
	pool.GracefulShutdown()
	if res := pool.Admit(key, 1, 0); res != AdmitUnavailable {
		t.Fatalf("post-shutdown Admit = %v, want Unavailable", res)
	}
	snap := pool.AdmissionSnapshot()
	if !snap.Stopping || len(snap.Candidates) != 0 || len(snap.Busy) != 0 || snap.CommittedSlots != 0 {
		t.Fatalf("post-shutdown snapshot = %+v", snap)
	}
}

// FORK-PATCH: admission telemetry — candidates and busy claims must expose
// the strategy discriminator plus speedstats eligibility, including during
// the claim-before-register window where busySlot.ad is still nil.
func TestSchedulerAdmission_Snapshot_StrategyAndRecordEligible(t *testing.T) {
	ch := make(chan types.DownloadEvent, 32)
	pool := New(ch, 1)

	oldClaimed := pool.workerClaimedGate.Load()
	t.Cleanup(func() { pool.workerClaimedGate.Store(oldClaimed) })
	t.Cleanup(pool.GracefulShutdown)

	claimed := make(chan struct{})
	releaseClaim := make(chan struct{})
	claimGate := func() {
		pool.workerClaimedGate.Store(nil)
		close(claimed)
		<-releaseClaim
	}
	pool.workerClaimedGate.Store(&claimGate)

	done := make(chan struct{})
	releaseDone := make(chan struct{})
	doneGate := func() {
		pool.workerDoneGate.Store(nil)
		close(done)
		<-releaseDone
	}
	oldDone := pool.workerDoneGate.Load()
	t.Cleanup(func() { pool.workerDoneGate.Store(oldDone) })
	pool.workerDoneGate.Store(&doneGate)

	var releaseClaimOnce, releaseDoneOnce sync.Once
	defer releaseClaimOnce.Do(func() { close(releaseClaim) })
	defer releaseDoneOnce.Do(func() { close(releaseDone) })

	hlsResumeEligible := &types.HLSResumeState{
		ManifestURL: "http://hls.example.com/eligible.m3u8", TotalSegments: 1,
		CompletedBitmap: []byte{0x00}, RecordEligible: true,
		Segments: make([]types.HLSSegmentState, 1),
	}
	hlsResumeIneligible := &types.HLSResumeState{
		ManifestURL: "http://hls.example.com/ineligible.m3u8", TotalSegments: 1,
		CompletedBitmap: []byte{0x00}, RecordEligible: false,
		Segments: make([]types.HLSSegmentState, 1),
	}

	// Holder occupies the single worker slot while candidates queue behind
	// it. A literal loopback URL keeps RunDownload local — the refused port
	// fails the manifest fetch immediately without any DNS resolve.
	if !pool.Add(types.DownloadRecord{
		ID: "holder", URL: "http://127.0.0.1:1/holder.m3u8",
		OutputPath: t.TempDir(), Filename: "holder.ts",
		ProgressState: progress.New("holder", 0), Runtime: &types.RuntimeConfig{},
		Strategy: types.StrategyHLS, HLSResume: hlsResumeEligible,
	}) {
		t.Fatal("holder Add refused")
	}
	select {
	case <-claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never claimed the holder")
	}

	type candWant struct {
		strategy types.DownloadStrategy
		eligible bool
	}
	add := func(id string, cfg types.DownloadRecord) {
		cfg.ID = id
		cfg.OutputPath = t.TempDir()
		cfg.Filename = id + ".bin"
		if cfg.ProgressState == nil {
			cfg.ProgressState = progress.New(id, 0)
		}
		if cfg.Runtime == nil {
			cfg.Runtime = &types.RuntimeConfig{}
		}
		if !pool.Add(cfg) {
			t.Fatalf("Add %s refused", id)
		}
	}
	add("at-threshold", types.DownloadRecord{TotalSize: types.RecordEligibleMinFileSize})
	add("above-threshold", types.DownloadRecord{TotalSize: types.RecordEligibleMinFileSize + 1})
	add("zero-size", types.DownloadRecord{TotalSize: 0})
	add("hls-nil-resume", types.DownloadRecord{Strategy: types.StrategyHLS})
	add("hls-eligible", types.DownloadRecord{Strategy: types.StrategyHLS, HLSResume: hlsResumeEligible})
	add("hls-ineligible", types.DownloadRecord{Strategy: types.StrategyHLS, HLSResume: hlsResumeIneligible})

	want := map[string]candWant{
		"holder":          {types.StrategyHLS, true},
		"at-threshold":    {"", false}, // strictly greater-than boundary
		"above-threshold": {"", true},
		"zero-size":       {"", false},
		"hls-nil-resume":  {types.StrategyHLS, true},
		"hls-eligible":    {types.StrategyHLS, true},
		"hls-ineligible":  {types.StrategyHLS, false},
	}
	snap := pool.AdmissionSnapshot()
	got := make(map[string]candWant, len(snap.Candidates))
	for _, c := range snap.Candidates {
		got[c.Key.ID] = candWant{c.Strategy, c.RecordEligible}
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Fatalf("candidate %s missing from snapshot", id)
		}
		if g != w {
			t.Errorf("candidate %s = %+v, want %+v", id, g, w)
		}
	}

	// Claim-before-register window: slot.ad is nil, so Busy must report the
	// claim-minted cache, not config lookups.
	if len(snap.Busy) != 1 {
		t.Fatalf("busy slots = %d, want 1", len(snap.Busy))
	}
	bs := snap.Busy[0]
	if bs.Key.ID != "holder" {
		t.Fatalf("busy key = %+v, want holder", bs.Key)
	}
	if bs.Running {
		t.Fatal("busy Running=true before registration")
	}
	if bs.Strategy != types.StrategyHLS || !bs.RecordEligible {
		t.Fatalf("claim-window busy metadata = (%q, %v), want (hls, true)", bs.Strategy, bs.RecordEligible)
	}

	// After registration the live config drives the same metadata.
	releaseClaimOnce.Do(func() { close(releaseClaim) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker never reached post-RunDownload window")
	}
	snap2 := pool.AdmissionSnapshot()
	if len(snap2.Busy) != 1 {
		t.Fatalf("busy slots after register = %d, want 1", len(snap2.Busy))
	}
	bs2 := snap2.Busy[0]
	if bs2.Strategy != types.StrategyHLS || !bs2.RecordEligible {
		t.Fatalf("registered busy metadata = (%q, %v), want (hls, true)", bs2.Strategy, bs2.RecordEligible)
	}
	releaseDoneOnce.Do(func() { close(releaseDone) })
}
