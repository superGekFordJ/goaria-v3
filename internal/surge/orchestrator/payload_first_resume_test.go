package orchestrator

import (
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func TestEventStarted_PreservesPayloadFirstMode(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "pf.bin")
	url := "http://example.com/pf.bin"
	id := "pf-started"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:                   id,
		URL:                  url,
		URLHash:              store.URLHash(url),
		DestPath:             destPath,
		Filename:             "pf.bin",
		Status:               "queued",
		TotalSize:            1024,
		RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown,
		SkipServerProbe:      true,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	ch <- types.DownloadEvent{
		Type:       types.EventStarted,
		DownloadID: id,
		URL:        url,
		DestPath:   destPath,
		Filename:   "pf.bin",
		Total:      1024,
	}
	close(ch)
	<-done

	got, err := store.GetDownload(id)
	if err != nil || got == nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if got.RangeAcquisitionMode != types.RangeAcquirePayloadFirstUnknown {
		t.Fatalf("mode = %q, want payload_first_unknown", got.RangeAcquisitionMode)
	}
	if !got.SkipServerProbe {
		t.Fatal("SkipServerProbe wiped by EventStarted")
	}
	if got.Status != "downloading" {
		t.Fatalf("status = %q, want downloading", got.Status)
	}
}

func TestResume_PayloadFirst_HotNoTasksStaysPayloadFirst(t *testing.T) {
	cfg := &types.DownloadRecord{
		URL:                  "http://example.com/hot.bin",
		DestPath:             filepath.Join(t.TempDir(), "missing-on-purpose.bin"),
		RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown,
		SkipServerProbe:      true,
		SupportsRange:        false,
	}
	hydrateConfigFromDisk(cfg)
	if cfg.RangeAcquisitionMode != types.RangeAcquirePayloadFirstUnknown {
		t.Fatalf("mode = %q, want payload_first_unknown", cfg.RangeAcquisitionMode)
	}
	if !cfg.SkipServerProbe {
		t.Fatal("SkipServerProbe lost")
	}
}

func TestResume_PayloadFirst_LegacyGob(t *testing.T) {
	settings := config.DefaultSettings()
	withTasks := buildResumeConfig("id-tasks", t.TempDir(), nil, &types.DownloadRecord{
		URL:       "http://example.com/a.bin",
		DestPath:  filepath.Join(t.TempDir(), "a.bin"),
		Filename:  "a.bin",
		TotalSize: 100,
		Tasks:     []types.Task{{Offset: 50, Length: 50}},
	}, settings)
	if withTasks.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("tasks snapshot mode = %q, want range_supported", withTasks.RangeAcquisitionMode)
	}
	if !withTasks.SupportsRange {
		t.Fatal("legacy tasks snapshot must be concurrent")
	}

	noSnap := buildResumeConfig("id-none", t.TempDir(), &types.DownloadRecord{
		URL:       "http://example.com/b.bin",
		DestPath:  filepath.Join(t.TempDir(), "b.bin"),
		Filename:  "b.bin",
		TotalSize: 100,
		Status:    "paused",
	}, nil, settings)
	if noSnap.RangeAcquisitionMode != types.RangeAcquireProbeAtEnqueue {
		t.Fatalf("no snapshot mode = %q, want empty (old sequential)", noSnap.RangeAcquisitionMode)
	}
	if noSnap.SupportsRange {
		t.Fatal("no snapshot must not invent concurrent")
	}
}

func TestResume_PayloadFirst_HonorsSavedMode(t *testing.T) {
	settings := config.DefaultSettings()
	cfg := buildResumeConfig("id-saved", t.TempDir(), &types.DownloadRecord{
		URL:                  "http://example.com/c.bin",
		DestPath:             filepath.Join(t.TempDir(), "c.bin"),
		Filename:             "c.bin",
		TotalSize:            100,
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		SkipServerProbe:      true,
	}, &types.DownloadRecord{
		URL:                  "http://example.com/c.bin",
		DestPath:             filepath.Join(t.TempDir(), "c.bin"),
		Filename:             "c.bin",
		TotalSize:            100,
		Downloaded:           0,
		Tasks:                []types.Task{{Offset: 0, Length: 100}},
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		SkipServerProbe:      true,
	}, settings)
	if cfg.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("mode = %q", cfg.RangeAcquisitionMode)
	}
	if !cfg.SkipServerProbe {
		t.Fatal("skip-origin must survive cold resume")
	}
	if !cfg.SupportsRange {
		t.Fatal("RangeSupported must launch concurrent")
	}
}

func TestHydrate_SkipServerProbeORsTrue(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "or.bin")
	url := "http://example.com/or.bin"
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:                   "or-id",
		URL:                  url,
		URLHash:              store.URLHash(url),
		DestPath:             destPath,
		Filename:             "or.bin",
		Status:               "paused",
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		SkipServerProbe:      false,
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID:                   "or-id",
		URL:                  url,
		DestPath:             destPath,
		Filename:             "or.bin",
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		SkipServerProbe:      false,
		Tasks:                []types.Task{{Offset: 0, Length: 100}},
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatal(err)
	}
	cfg := &types.DownloadRecord{
		URL:             url,
		DestPath:        destPath,
		SkipServerProbe: true,
	}
	hydrateConfigFromDisk(cfg)
	if !cfg.SkipServerProbe {
		t.Fatal("hydrate must OR SkipServerProbe, not overwrite true with false")
	}
	if cfg.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("mode = %q", cfg.RangeAcquisitionMode)
	}
}

func TestEventQueued_SkipsInsertWhenAbsent(t *testing.T) {
	_ = testutil.SetupStateDB(t)
	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()
	ch <- types.DownloadEvent{
		Type:       types.EventQueued,
		DownloadID: "missing-queued",
		URL:        "http://example.com/missing.bin",
		Filename:   "missing.bin",
	}
	close(ch)
	<-done
	got, err := store.GetDownload("missing-queued")
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if got != nil {
		t.Fatalf("EventQueued inserted no-mode row: %+v", got)
	}
}
