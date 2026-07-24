package orchestrator

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

func TestEngineHooks_DefaultNil(t *testing.T) {
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()

	hooks := mgr.GetEngineHooks()
	if hooks.RecomputeResumeParams != nil {
		t.Fatal("expected nil RecomputeResumeParams by default")
	}

	if err := mgr.Resume("missing"); !errors.Is(err, types.ErrEngineNotInit) {
		t.Fatalf("expected ErrEngineNotInit, got %v", err)
	}
}

func TestEngineHooks_SetGetRMW(t *testing.T) {
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()

	var called atomic.Int32
	mgr.SetEngineHooks(EngineHooks{
		RecomputeResumeParams: func(cfg *types.DownloadRecord) {
			called.Add(1)
			cfg.Workers = 9
		},
	})

	got := mgr.GetEngineHooks()
	if got.RecomputeResumeParams == nil {
		t.Fatal("expected RecomputeResumeParams after Set")
	}
	cfg := &types.DownloadRecord{}
	got.RecomputeResumeParams(cfg)
	if called.Load() != 1 || cfg.Workers != 9 {
		t.Fatalf("RMW hook: called=%d workers=%d", called.Load(), cfg.Workers)
	}
}

func TestEngineHooks_ResumeHotCallsBeforeAdd(t *testing.T) {
	state := progress.New("hot-id", 1000)
	state.Pause()

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"hot-id": {
			ID:            "hot-id",
			Filename:      "hot.bin",
			ProgressState: state,
			URL:           "http://example.com/hot",
			DestPath:      filepath.Join(t.TempDir(), "hot.bin"),
		},
	})

	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	var callOrder []string
	mgr.SetEngineHooks(EngineHooks{
		RecomputeResumeParams: func(cfg *types.DownloadRecord) {
			callOrder = append(callOrder, "recompute:"+cfg.ID)
			cfg.Workers = 4
		},
	})

	if err := mgr.Resume("hot-id"); err != nil {
		t.Fatalf("Resume hot failed: %v", err)
	}
	if len(callOrder) != 1 || callOrder[0] != "recompute:hot-id" {
		t.Fatalf("expected recompute before Add, got %v", callOrder)
	}
}

func TestEngineHooks_ResumeColdAndBatch(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "surge.db")
	store.Configure(dbPath)
	t.Cleanup(func() { store.CloseDB() })

	coldID := "cold-id"
	entry := types.DownloadRecord{
		ID:       coldID,
		URL:      "http://example.com/cold",
		DestPath: filepath.Join(tmpDir, "cold.bin"),
		Filename: "cold.bin",
		Status:   "paused",
		Workers:  2,
	}
	if err := store.AddToMasterList(entry); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	progressCh := make(chan types.DownloadEvent, 16)
	pool := scheduler.New(progressCh, 2)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	var coldCalls atomic.Int32
	mgr.SetEngineHooks(EngineHooks{
		RecomputeResumeParams: func(cfg *types.DownloadRecord) {
			coldCalls.Add(1)
			cfg.Workers = 8
		},
	})

	if err := mgr.Resume(coldID); err != nil {
		t.Fatalf("Resume cold failed: %v", err)
	}
	if coldCalls.Load() != 1 {
		t.Fatalf("cold Resume: expected 1 recompute call, got %d", coldCalls.Load())
	}

	batchID := "batch-cold"
	if err := store.AddToMasterList(types.DownloadRecord{
		ID:       batchID,
		URL:      "http://example.com/batch",
		DestPath: filepath.Join(tmpDir, "batch.bin"),
		Filename: "batch.bin",
		Status:   "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList batch: %v", err)
	}

	errs := mgr.ResumeBatch([]string{batchID})
	if len(errs) != 1 || errs[0] != nil {
		t.Fatalf("ResumeBatch: %v", errs)
	}
	if coldCalls.Load() != 2 {
		t.Fatalf("batch cold: expected 2 total recompute calls, got %d", coldCalls.Load())
	}
}

func TestEngineHooks_ResumeBatchHot(t *testing.T) {
	state := progress.New("batch-hot", 1000)
	state.Pause()

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"batch-hot": {
			ID:            "batch-hot",
			Filename:      "bh.bin",
			ProgressState: state,
			URL:           "http://example.com/bh",
			DestPath:      filepath.Join(t.TempDir(), "bh.bin"),
		},
	})

	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	var calls atomic.Int32
	mgr.SetEngineHooks(EngineHooks{
		RecomputeResumeParams: func(cfg *types.DownloadRecord) {
			calls.Add(1)
		},
	})

	errs := mgr.ResumeBatch([]string{"batch-hot"})
	if len(errs) != 1 || errs[0] != nil {
		t.Fatalf("ResumeBatch hot: %v", errs)
	}
	if calls.Load() != 1 {
		t.Fatalf("batch hot: expected 1 recompute call, got %d", calls.Load())
	}
}
