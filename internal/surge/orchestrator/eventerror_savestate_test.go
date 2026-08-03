package orchestrator

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func TestEventError_WithState_SaveStateAndStatusError(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "error_resume.bin")
	url := "http://example.com/error_resume.bin"
	id := "error-resume"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         id,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "downloading",
		TotalSize:  1000,
		Downloaded: 100,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	go mgr.StartEventWorker(ch)

	snapshot := &types.DownloadRecord{
		URL:             url,
		ID:              id,
		DestPath:        destPath,
		TotalSize:       1000,
		Downloaded:      600,
		Tasks:           []types.Task{{Offset: 600, Length: 400}},
		Filename:        filepath.Base(destPath),
		Workers:         4,
		MinChunkSize:    64 * 1024,
		ActualChunkSize: 100,
		ChunkBitmap:     []byte{0x3f},
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		DestPath:   destPath,
		Downloaded: 600,
		Err:        errors.New("disk full"),
		State:      snapshot,
	}
	close(ch)

	deadline := time.Now().Add(3 * time.Second)
	for {
		entry, err := store.GetDownload(id)
		if err == nil && entry != nil && entry.Status == "error" {
			if entry.Downloaded != 600 {
				t.Fatalf("master Downloaded=%d, want 600", entry.Downloaded)
			}
			if entry.Error != "disk full" {
				t.Fatalf("master Error=%q, want %q", entry.Error, "disk full")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Status=error")
		}
		time.Sleep(20 * time.Millisecond)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(saved.Tasks) != 1 || saved.Tasks[0].Offset != 600 {
		t.Fatalf("saved Tasks=%+v, want [{Offset:600 Length:400}]", saved.Tasks)
	}
	if saved.Downloaded != 600 {
		t.Fatalf("saved Downloaded=%d, want 600", saved.Downloaded)
	}
}

func TestEventError_NilState_StatusErrorOnly(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "error_nil.bin")
	url := "http://example.com/error_nil.bin"
	id := "error-nil"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:        id,
		URL:       url,
		URLHash:   store.URLHash(url),
		DestPath:  destPath,
		Filename:  filepath.Base(destPath),
		Status:    "downloading",
		TotalSize: 500,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	go mgr.StartEventWorker(ch)

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		Err:        errors.New("boom"),
	}
	close(ch)

	deadline := time.Now().Add(3 * time.Second)
	for {
		entry, err := store.GetDownload(id)
		if err == nil && entry != nil && entry.Status == "error" {
			if entry.Error != "boom" {
				t.Fatalf("nil-State master Error=%q, want boom", entry.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Status=error without State")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := store.LoadState(url, destPath); err == nil {
		t.Fatal("nil-State EventError must not invent detail.gob")
	}
}

func TestEventError_WithState_ElapsedMonotonicBump(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "error_elapsed.bin")
	url := "http://example.com/error_elapsed.bin"
	id := "error-elapsed"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         id,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "downloading",
		TotalSize:  1000,
		Downloaded: 100,
		TimeTaken:  5000, // 5s already on master
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	go mgr.StartEventWorker(ch)

	snapshot := &types.DownloadRecord{
		URL:        url,
		ID:         id,
		DestPath:   destPath,
		TotalSize:  1000,
		Downloaded: 400,                // advanced vs master
		Elapsed:    int64(time.Second), // 1s — below master candidateElapsed
		Tasks:      []types.Task{{Offset: 400, Length: 600}},
		Filename:   filepath.Base(destPath),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		DestPath:   destPath,
		Downloaded: 400,
		Err:        errors.New("boom"),
		State:      snapshot,
	}
	close(ch)

	deadline := time.Now().Add(3 * time.Second)
	var entry *types.DownloadRecord
	for {
		got, err := store.GetDownload(id)
		if err == nil && got != nil && got.Status == "error" {
			entry = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Status=error")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Pause-aligned bump: Downloaded advanced and Elapsed was ≤ candidate → +1ms.
	if entry.TimeTaken < 5001 {
		t.Fatalf("TimeTaken=%d, want >=5001 (monotonic bump vs master)", entry.TimeTaken)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Elapsed < int64(5001*time.Millisecond) {
		t.Fatalf("detail Elapsed=%d, want >=5001ms monotonic", saved.Elapsed)
	}
}
