package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// Enqueue must seed a minimal detail gob when the request carries headers or
// mirrors, so credentials survive a kill before the first worker snapshot.
func TestEnqueue_WritesMinimalDetail(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	ts, _ := newRangeProbeServer(t)
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{})
	mgr := NewLifecycleManager(pool, nil, nil)
	defer mgr.Shutdown()
	destDir := t.TempDir()

	req := &DownloadRequest{
		URL:           ts.URL + "/auth.bin",
		Filename:      "auth.bin",
		Path:          destDir,
		FileSize:      1024,
		SupportsRange: new(true),
		Headers: map[string]string{
			"Cookie":        "session=enq-1",
			"Authorization": "Bearer enq-tok",
		},
		Mirrors:      []string{ts.URL + "/auth.bin", ts.URL + "/auth-mirror.bin"},
		Workers:      4,
		MinChunkSize: 256,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id, finalName, err := mgr.Enqueue(ctx, req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	destPath := filepath.Join(destDir, finalName)

	saved, err := store.LoadState(req.URL, destPath)
	if err != nil {
		t.Fatalf("queued detail must exist for header-bearing requests: %v", err)
	}
	if saved == nil {
		t.Fatal("LoadState returned nil")
	}
	if saved.Headers["Cookie"] != "session=enq-1" || saved.Headers["Authorization"] != "Bearer enq-tok" {
		t.Fatalf("detail Headers = %v, want Cookie+Authorization", saved.Headers)
	}
	if len(saved.Mirrors) != 2 {
		t.Fatalf("detail Mirrors = %v, want 2", saved.Mirrors)
	}
	if saved.ID != id {
		t.Fatalf("detail ID = %q, want %q", saved.ID, id)
	}

	// The detail seed's master write-back must be a no-op: the queued row
	// keeps exactly what AddToMasterList persisted.
	master, err := store.GetDownload(id)
	if err != nil || master == nil {
		t.Fatalf("GetDownload: %v (nil=%v)", err, master == nil)
	}
	if master.TotalSize != 1024 || master.Workers != 4 || master.MinChunkSize != 256 {
		t.Fatalf("master row rewritten by detail seed: size=%d workers=%d minChunk=%d",
			master.TotalSize, master.Workers, master.MinChunkSize)
	}
	if master.Status != "queued" {
		t.Fatalf("master status = %q, want queued", master.Status)
	}

	// No headers and no mirrors → no detail file solely from enqueue.
	req2 := &DownloadRequest{
		URL:           ts.URL + "/plain.bin",
		Filename:      "plain.bin",
		Path:          destDir,
		FileSize:      1024,
		SupportsRange: new(true),
	}
	id2, finalName2, err := mgr.Enqueue(ctx, req2)
	if err != nil {
		t.Fatalf("Enqueue plain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "details", id2+".gob")); !os.IsNotExist(err) {
		t.Fatalf("detail file must not exist for headerless enqueue, stat err=%v", err)
	}
	if _, err := store.LoadState(req2.URL, filepath.Join(destDir, finalName2)); err == nil {
		t.Fatal("LoadState must fail when no detail was seeded")
	}
}

// buildResumeConfig must restore persisted headers and follow the mirror
// fallback chain detail → master entry → primary URL.
func TestBuildResumeConfig_RestoresHeadersAndMirrors(t *testing.T) {
	settings := config.DefaultSettings()

	entry := &types.DownloadRecord{
		URL:      "http://primary.example.com/f.bin",
		DestPath: filepath.Join(t.TempDir(), "f.bin"),
		Filename: "f.bin",
		Status:   "paused",
		Mirrors:  []string{"http://primary.example.com/f.bin", "http://entry-mirror.example.com/f.bin"},
	}
	saved := &types.DownloadRecord{
		URL:      entry.URL,
		DestPath: entry.DestPath,
		Filename: "f.bin",
		Headers:  map[string]string{"Cookie": "session=res"},
		Mirrors:  []string{"http://primary.example.com/f.bin", "http://saved-mirror.example.com/f.bin"},
		Tasks:    []types.Task{{Offset: 0, Length: 100}},
	}
	cfg := buildResumeConfig("id-1", t.TempDir(), entry, saved, settings)
	if cfg.Headers["Cookie"] != "session=res" {
		t.Fatalf("cfg.Headers = %v, want persisted Cookie", cfg.Headers)
	}
	if len(cfg.Mirrors) != 2 || cfg.Mirrors[1] != "http://saved-mirror.example.com/f.bin" {
		t.Fatalf("cfg.Mirrors = %v, want saved-state mirrors", cfg.Mirrors)
	}

	// Detail has no mirrors → entry mirrors.
	saved2 := &types.DownloadRecord{
		URL: entry.URL, DestPath: entry.DestPath, Filename: "f.bin",
		Headers: map[string]string{"Cookie": "session=res"},
	}
	cfg = buildResumeConfig("id-2", t.TempDir(), entry, saved2, settings)
	if len(cfg.Mirrors) != 2 || cfg.Mirrors[1] != "http://entry-mirror.example.com/f.bin" {
		t.Fatalf("cfg.Mirrors = %v, want entry mirrors", cfg.Mirrors)
	}
	if cfg.Headers["Cookie"] != "session=res" {
		t.Fatal("headers must restore even without saved mirrors")
	}

	// Neither has mirrors → primary URL fallback.
	entryBare := &types.DownloadRecord{
		URL: "http://bare.example.com/b.bin", DestPath: filepath.Join(t.TempDir(), "b.bin"),
		Filename: "b.bin", Status: "paused",
	}
	cfg = buildResumeConfig("id-3", t.TempDir(), entryBare, nil, settings)
	if len(cfg.Mirrors) != 1 || cfg.Mirrors[0] != "http://bare.example.com/b.bin" {
		t.Fatalf("cfg.Mirrors = %v, want [primary URL]", cfg.Mirrors)
	}
	if cfg.Headers != nil {
		t.Fatalf("no saved state → Headers must be nil, got %v", cfg.Headers)
	}
}

// ResumeBatch cold path must restore persisted headers from detail state.
func TestResumeBatch_Cold_RestoresHeaders(t *testing.T) {
	testutil.SetupStateDB(t)
	url := "http://batch.example.com/cold.bin"
	destPath := filepath.Join(t.TempDir(), "cold.bin")
	id := "batch-cold-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url), DestPath: destPath,
		Filename: "cold.bin", Status: "paused", TotalSize: 1000,
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "cold.bin",
		TotalSize: 1000, Downloaded: 500,
		Tasks:   []types.Task{{Offset: 500, Length: 500}},
		Mirrors: []string{url},
		Headers: map[string]string{"Cookie": "session=batch"},
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{})
	mgr := NewLifecycleManager(pool, nil, nil)
	defer mgr.Shutdown()

	errs := mgr.ResumeBatch([]string{id})
	if len(errs) != 1 || errs[0] != nil {
		t.Fatalf("ResumeBatch errs = %v", errs)
	}
	rec, ok := recordByID(pool, id)
	if !ok {
		t.Fatal("expected re-queued record")
	}
	if rec.Headers["Cookie"] != "session=batch" {
		t.Fatalf("resumed record Headers = %v, want persisted Cookie", rec.Headers)
	}
	if len(rec.Mirrors) == 0 {
		t.Fatal("resumed record must carry mirrors")
	}
}

// Queued-never-started: a queued entry with only the enqueue-seeded detail
// (no tasks) must still restore headers on cold resume.
func TestResume_Cold_QueuedNeverStarted_RestoresHeaders(t *testing.T) {
	testutil.SetupStateDB(t)
	url := "http://queued.example.com/never.bin"
	destPath := filepath.Join(t.TempDir(), "never.bin")
	id := "queued-cold-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url), DestPath: destPath,
		Filename: "never.bin", Status: "queued", TotalSize: 2048,
	})
	// Enqueue-seeded detail: headers+mirrors, no tasks, no progress.
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "never.bin",
		Status: "queued", TotalSize: 2048,
		Mirrors: []string{url, "http://queued-mirror.example.com/never.bin"},
		Headers: map[string]string{"Authorization": "Bearer queued-tok"},
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{})
	mgr := NewLifecycleManager(pool, nil, nil)
	defer mgr.Shutdown()

	if err := mgr.Resume(id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	rec, ok := recordByID(pool, id)
	if !ok {
		t.Fatal("expected re-queued record")
	}
	if rec.Headers["Authorization"] != "Bearer queued-tok" {
		t.Fatalf("resumed Headers = %v, want persisted Authorization", rec.Headers)
	}
	if len(rec.Mirrors) != 2 {
		t.Fatalf("resumed Mirrors = %v, want detail mirrors", rec.Mirrors)
	}
}

// End-to-end: a paused concurrent download resumed cold (process restart
// simulated by loading only from store) must send persisted headers on the
// wire.
func TestColdResume_Concurrent_HeadersReachServer(t *testing.T) {
	testutil.SetupStateDB(t)

	fileSize := int64(64 * utils.KiB)
	half := fileSize / 2
	data := make([]byte, fileSize)

	var sawCookie atomic.Value
	var sawRange atomic.Bool

	srv := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := r.Header.Get("Cookie"); c != "" {
			sawCookie.Store(c)
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")
		rng := r.Header.Get("Range")
		if r.Method == http.MethodHead || rng == "" {
			w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
			if r.Method != http.MethodHead {
				_, _ = w.Write(data)
			}
			return
		}
		sawRange.Store(true)
		spec := strings.TrimPrefix(rng, "bytes=")
		parts := strings.SplitN(spec, "-", 2)
		start, _ := strconv.ParseInt(parts[0], 10, 64)
		end := fileSize - 1
		if len(parts) > 1 && parts[1] != "" {
			if e, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				end = e
			}
		}
		if end >= fileSize {
			end = fileSize - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
	defer srv.Close()

	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "cold-conc.bin")
	url := srv.URL + "/cold-conc.bin"
	id := "cold-conc-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url), DestPath: destPath,
		Filename: "cold-conc.bin", Status: "paused", TotalSize: fileSize,
		Downloaded:           half,
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "cold-conc.bin",
		TotalSize: fileSize, Downloaded: half,
		Tasks:                []types.Task{{Offset: half, Length: fileSize - half}},
		Mirrors:              []string{url},
		Headers:              map[string]string{"Cookie": "session=restored"},
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		Workers:              1,
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}
	// The partial file must exist for a concurrent resume.
	if err := os.WriteFile(destPath+types.IncompleteSuffix, data[:half], 0o644); err != nil {
		t.Fatalf("write .surge: %v", err)
	}

	entry, err := store.GetDownload(id)
	if err != nil || entry == nil {
		t.Fatalf("GetDownload: %v", err)
	}
	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("LoadState: %v", err)
	}
	cfg := buildResumeConfig(id, destDir, entry, saved, config.DefaultSettings())
	cfg.Runtime.Workers = 1
	cfg.Runtime.MinChunkSize = fileSize // keep the resumed task whole

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := scheduler.RunDownload(ctx, &cfg); err != nil {
		t.Fatalf("RunDownload: %v", err)
	}

	got, _ := sawCookie.Load().(string)
	if got != "session=restored" {
		t.Fatalf("server saw Cookie = %q, want session=restored", got)
	}
	if !sawRange.Load() {
		t.Fatal("expected a ranged request on resume")
	}
}

// End-to-end for the single-threaded path: mode range_unsupported → plain GET
// must still carry the persisted headers.
func TestColdResume_Single_HeadersReachServer(t *testing.T) {
	testutil.SetupStateDB(t)

	fileSize := int64(32 * utils.KiB)
	data := make([]byte, fileSize)
	var sawCookie atomic.Value

	srv := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := r.Header.Get("Cookie"); c != "" {
			sawCookie.Store(c)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	}))
	defer srv.Close()

	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "cold-single.bin")
	url := srv.URL + "/cold-single.bin"
	id := "cold-single-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url), DestPath: destPath,
		Filename: "cold-single.bin", Status: "paused", TotalSize: fileSize,
		RangeAcquisitionMode: types.RangeAcquireRangeUnsupported,
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "cold-single.bin",
		TotalSize: fileSize,
		Mirrors:   []string{url},
		Headers:   map[string]string{"Cookie": "session=single"},
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}
	if err := os.WriteFile(destPath+types.IncompleteSuffix, nil, 0o644); err != nil {
		t.Fatalf("write .surge: %v", err)
	}

	entry, _ := store.GetDownload(id)
	saved, _ := store.LoadState(url, destPath)
	cfg := buildResumeConfig(id, destDir, entry, saved, config.DefaultSettings())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := scheduler.RunDownload(ctx, &cfg); err != nil {
		t.Fatalf("RunDownload: %v", err)
	}

	got, _ := sawCookie.Load().(string)
	if got != "session=single" {
		t.Fatalf("server saw Cookie = %q, want session=single", got)
	}
}

// hydrateConfigFromDisk must backfill empty runtime headers from the
// persisted detail while leaving a non-empty runtime map authoritative.
func TestHydrate_HeadersFromDiskFallback(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "hyd.bin")
	url := "http://example.com/hyd.bin"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: "hyd-id", URL: url, URLHash: store.URLHash(url),
		DestPath: destPath, Filename: "hyd.bin", Status: "paused",
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: "hyd-id", URL: url, DestPath: destPath, Filename: "hyd.bin",
		Headers: map[string]string{"Cookie": "session=disk"},
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}

	cfg := &types.DownloadRecord{URL: url, DestPath: destPath}
	hydrateConfigFromDisk(cfg)
	if cfg.Headers["Cookie"] != "session=disk" {
		t.Fatalf("empty cfg.Headers = %v, want disk-persisted Cookie", cfg.Headers)
	}

	cfg = &types.DownloadRecord{
		URL:      url,
		DestPath: destPath,
		Headers:  map[string]string{"Cookie": "session=live"},
	}
	hydrateConfigFromDisk(cfg)
	if cfg.Headers["Cookie"] != "session=live" {
		t.Fatalf("non-empty cfg.Headers overwritten: %v", cfg.Headers)
	}
}

// EventPaused must persist the snapshot's headers into the detail gob.
func TestEventPaused_PersistsStateHeaders(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "pause.bin")
	url := "http://example.com/pause.bin"
	id := "pause-hdr-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url),
		DestPath: destPath, Filename: "pause.bin",
		Status: "downloading", TotalSize: 1024,
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
		Type: types.EventPaused, DownloadID: id,
		URL: url, DestPath: destPath, Filename: "pause.bin",
		State: &types.DownloadRecord{
			URL: url, DestPath: destPath, Filename: "pause.bin",
			TotalSize: 1024, Downloaded: 512,
			Tasks:   []types.Task{{Offset: 512, Length: 512}},
			Mirrors: []string{url},
			Headers: map[string]string{"Cookie": "session=pause"},
		},
	}
	close(ch)
	<-done

	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("LoadState: %v (nil=%v)", err, saved == nil)
	}
	if saved.Headers["Cookie"] != "session=pause" {
		t.Fatalf("detail Headers = %v, want persisted pause Cookie", saved.Headers)
	}
}

// EventPaused without a snapshot rewrites the detail in place; persisted
// headers must survive that merge.
func TestEventPaused_NilStateKeepsDetailHeaders(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "fallback.bin")
	url := "http://example.com/fallback.bin"
	id := "pause-nil-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url),
		DestPath: destPath, Filename: "fallback.bin",
		Status: "downloading", TotalSize: 1000, Downloaded: 100,
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "fallback.bin",
		Status: "downloading", TotalSize: 1000, Downloaded: 100,
		Tasks:   []types.Task{{Offset: 100, Length: 900}},
		Mirrors: []string{url},
		Headers: map[string]string{"Cookie": "session=keep"},
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	ch <- types.DownloadEvent{
		Type: types.EventPaused, DownloadID: id, Downloaded: 600,
	}
	close(ch)
	<-done

	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("LoadState: %v (nil=%v)", err, saved == nil)
	}
	if saved.Headers["Cookie"] != "session=keep" {
		t.Fatalf("detail Headers = %v, want surviving Cookie", saved.Headers)
	}
	if saved.Downloaded != 600 {
		t.Fatalf("detail Downloaded = %d, want 600 after fallback merge", saved.Downloaded)
	}
}

// EventComplete must delete the detail gob entirely so persisted
// credentials do not linger past completion.
func TestEventComplete_RemovesDetail(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "done.bin")
	url := "http://example.com/done.bin"
	id := "complete-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url),
		DestPath: destPath, Filename: "done.bin",
		Status: "downloading", TotalSize: 4,
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "done.bin",
		Status: "downloading", TotalSize: 4, Downloaded: 4,
		Mirrors: []string{url},
		Headers: map[string]string{"Cookie": "session=done"},
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}
	// Working file so finalizeCompletedFile can promote it.
	if err := os.WriteFile(destPath+types.IncompleteSuffix, []byte("data"), 0o644); err != nil {
		t.Fatalf("write .surge: %v", err)
	}
	detailPath := filepath.Join(tmpDir, "details", id+".gob")

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	ch <- types.DownloadEvent{
		Type: types.EventComplete, DownloadID: id,
		Filename: "done.bin", Total: 4, Elapsed: time.Second,
	}
	close(ch)
	<-done

	if _, err := os.Stat(detailPath); !os.IsNotExist(err) {
		t.Fatalf("detail gob must be deleted on complete, stat err=%v", err)
	}
	if _, err := os.Stat(destPath); err != nil {
		t.Fatalf("completed file missing after finalize: %v", err)
	}
}

// An HLS enqueue without headers or mirrors must still seed the detail gob —
// the resume checkpoint is authoritative detail data — while master.gob keeps
// the strategy discriminator but never the payload.
func TestEnqueue_HLSWithoutHeaders_SeedsDetailAndSurvivesColdResume(t *testing.T) {
	testutil.SetupStateDB(t)
	ts, _ := newRangeProbeServer(t)
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{})
	mgr := NewLifecycleManager(pool, nil, nil)
	defer mgr.Shutdown()
	destDir := t.TempDir()

	url := ts.URL + "/index.m3u8"
	resume := &types.HLSResumeState{
		ManifestURL:         url,
		MediaPlaylistURL:    url,
		ManifestFingerprint: "fp-seed",
		TotalSegments:       4,
		CompletedBitmap:     []byte{0x01},
		VerifiedBytes:       128,
		Segments:            make([]types.HLSSegmentState, 4),
	}
	req := &DownloadRequest{
		URL:       url,
		Filename:  "video.ts",
		Path:      destDir,
		Strategy:  types.StrategyHLS,
		HLSResume: resume,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id, finalName, err := mgr.Enqueue(ctx, req)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	destPath := filepath.Join(destDir, finalName)

	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("queued HLS detail must exist: %v (nil=%v)", err, saved == nil)
	}
	if saved.Strategy != types.StrategyHLS {
		t.Fatalf("detail Strategy = %q, want %q", saved.Strategy, types.StrategyHLS)
	}
	if saved.HLSResume == nil || saved.HLSResume.TotalSegments != 4 || saved.HLSResume.VerifiedBytes != 128 {
		t.Fatalf("detail HLSResume = %+v", saved.HLSResume)
	}
	master, err := store.GetDownload(id)
	if err != nil || master == nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if master.Strategy != types.StrategyHLS {
		t.Fatalf("master Strategy = %q, want %q", master.Strategy, types.StrategyHLS)
	}
	if master.HLSResume != nil {
		t.Fatalf("master HLSResume = %+v, want nil (payload stays in detail)", master.HLSResume)
	}
}

// buildResumeConfig resolves strategy detail-first then master, clones the
// checkpoint, and forces probe/range normalization for HLS.
func TestBuildResumeConfig_HLSStrategyPrecedenceAndClone(t *testing.T) {
	settings := config.DefaultSettings()
	destDir := t.TempDir()
	destPath := filepath.Join(destDir, "v.ts")
	url := "http://hls.example.com/index.m3u8"

	entry := &types.DownloadRecord{
		URL: url, DestPath: destPath, Filename: "v.ts",
		Status: "paused", Strategy: types.StrategyHLS,
	}
	saved := &types.DownloadRecord{
		URL: url, DestPath: destPath, Filename: "v.ts",
		Status: "paused", Strategy: types.StrategyHLS,
		HLSResume: &types.HLSResumeState{
			ManifestURL: url, TotalSegments: 2,
			CompletedBitmap: []byte{0x03}, VerifiedBytes: 10,
			Segments: []types.HLSSegmentState{{Index: 0}, {Index: 1}},
		},
	}

	cfg := buildResumeConfig("hls-1", destDir, entry, saved, settings)
	if cfg.Strategy != types.StrategyHLS {
		t.Fatalf("cfg.Strategy = %q, want %q", cfg.Strategy, types.StrategyHLS)
	}
	if cfg.RangeAcquisitionMode != types.RangeAcquireProbeAtEnqueue {
		t.Fatalf("mode = %q, want probe_at_enqueue", cfg.RangeAcquisitionMode)
	}
	if !cfg.SkipServerProbe {
		t.Fatal("SkipServerProbe = false, want true for HLS")
	}
	if cfg.SupportsRange {
		t.Fatal("SupportsRange = true, want false for HLS")
	}
	if cfg.HLSResume == nil || cfg.HLSResume.TotalSegments != 2 {
		t.Fatalf("cfg.HLSResume = %+v", cfg.HLSResume)
	}

	// The resumed cfg must own a physically isolated checkpoint.
	saved.HLSResume.Segments[0].URI = "mutated"
	if cfg.HLSResume.Segments[0].URI != "" {
		t.Fatal("cfg.HLSResume aliases the persisted checkpoint slices")
	}

	// Master row alone (detail missing) still restores the strategy.
	cfg2 := buildResumeConfig("hls-2", destDir, entry, nil, settings)
	if cfg2.Strategy != types.StrategyHLS || !cfg2.SkipServerProbe || cfg2.SupportsRange {
		t.Fatalf("entry-only resume lost HLS normalization: strategy=%q skip=%v range=%v",
			cfg2.Strategy, cfg2.SkipServerProbe, cfg2.SupportsRange)
	}
	if cfg2.HLSResume != nil {
		t.Fatal("entry-only resume must not fabricate an HLS checkpoint")
	}
}

// Cold Resume / ResumeBatch must deliver a cfg that already carries the
// restored strategy + checkpoint into the host hook.
func TestResume_ColdHLS_RestoresStrategyIntoHookAndQueue(t *testing.T) {
	testutil.SetupStateDB(t)
	url := "http://hls.example.com/list.m3u8"
	destPath := filepath.Join(t.TempDir(), "v.ts")
	id := "hls-cold-resume"

	resume := &types.HLSResumeState{
		ManifestURL: url, TotalSegments: 5,
		CompletedBitmap: []byte{0x1F}, VerifiedBytes: 900,
		Segments: make([]types.HLSSegmentState, 5),
	}
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url), DestPath: destPath,
		Filename: "v.ts", Status: "paused", Strategy: types.StrategyHLS,
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "v.ts",
		Status: "paused", Downloaded: 900,
		Strategy: types.StrategyHLS, HLSResume: resume,
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{})
	mgr := NewLifecycleManager(pool, nil, nil)
	defer mgr.Shutdown()

	var hookCalls atomic.Int32
	var sawStrategy atomic.Value
	var sawResume atomic.Bool
	mgr.SetEngineHooks(EngineHooks{
		RecomputeResumeParams: func(cfg *types.DownloadRecord) {
			hookCalls.Add(1)
			sawStrategy.Store(cfg.Strategy)
			sawResume.Store(cfg.HLSResume != nil && cfg.HLSResume.TotalSegments == 5)
		},
	})

	if err := mgr.Resume(id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if hookCalls.Load() != 1 {
		t.Fatalf("hook calls = %d, want 1", hookCalls.Load())
	}
	if got, _ := sawStrategy.Load().(types.DownloadStrategy); got != types.StrategyHLS {
		t.Fatalf("hook saw Strategy = %q, want %q", got, types.StrategyHLS)
	}
	if !sawResume.Load() {
		t.Fatal("hook saw nil/incorrect HLSResume")
	}

	rec, ok := recordByID(pool, id)
	if !ok {
		t.Fatal("expected re-queued record")
	}
	if rec.Strategy != types.StrategyHLS || !rec.SkipServerProbe || rec.SupportsRange ||
		rec.RangeAcquisitionMode != types.RangeAcquireProbeAtEnqueue {
		t.Fatalf("resumed record lost HLS normalization: %+v", rec)
	}
	if rec.HLSResume == nil || rec.HLSResume.VerifiedBytes != 900 {
		t.Fatalf("resumed HLSResume = %+v", rec.HLSResume)
	}
	// Mutating the persisted source must not corrupt the queued copy.
	resume.VerifiedBytes = 1
	if rec.HLSResume.VerifiedBytes != 900 {
		t.Fatal("queued HLSResume aliases the persisted checkpoint")
	}
}

func TestResumeBatch_ColdHLS_RestoresStrategy(t *testing.T) {
	testutil.SetupStateDB(t)
	url := "http://hls.example.com/batch.m3u8"
	destPath := filepath.Join(t.TempDir(), "b.ts")
	id := "hls-batch-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url), DestPath: destPath,
		Filename: "b.ts", Status: "paused", Strategy: types.StrategyHLS,
	})
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "b.ts",
		Status: "paused", Strategy: types.StrategyHLS,
		HLSResume: &types.HLSResumeState{
			ManifestURL: url, TotalSegments: 2,
			CompletedBitmap: []byte{0x03}, Segments: make([]types.HLSSegmentState, 2),
		},
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{})
	mgr := NewLifecycleManager(pool, nil, nil)
	defer mgr.Shutdown()

	var hookCalls atomic.Int32
	mgr.SetEngineHooks(EngineHooks{
		RecomputeResumeParams: func(cfg *types.DownloadRecord) {
			if cfg.Strategy != types.StrategyHLS || cfg.HLSResume == nil {
				t.Errorf("batch hook saw strategy=%q resume=%v", cfg.Strategy, cfg.HLSResume != nil)
			}
			hookCalls.Add(1)
		},
	})

	errs := mgr.ResumeBatch([]string{id})
	if len(errs) != 1 || errs[0] != nil {
		t.Fatalf("ResumeBatch errs = %v", errs)
	}
	if hookCalls.Load() != 1 {
		t.Fatalf("hook calls = %d, want 1", hookCalls.Load())
	}
	rec, ok := recordByID(pool, id)
	if !ok || rec.Strategy != types.StrategyHLS || rec.HLSResume == nil {
		t.Fatalf("batch-resumed record: found=%v strategy=%q resume=%v", ok, rec.Strategy, rec.HLSResume != nil)
	}
}
