package store

import (
	"bytes"
	"encoding/gob"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// legacyDetailState mirrors the pre-projection detail wire shape where State
// was *types.DownloadRecord. gob matches by field name, so encoding this type
// produces the same byte layout old builds wrote.
type legacyDetailState struct {
	Version int
	State   *types.DownloadRecord
}

func TestSaveLoadState_HeadersAndWhitelistRoundTrip(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	url := "https://auth.example.com/secret.zip"
	destPath := filepath.Join(tmpDir, "secret.zip")
	id := "hdr-roundtrip-id"
	headers := map[string]string{
		"Cookie":        "session=xyz123; theme=dark",
		"Authorization": "Bearer tok-abc",
		"Referer":       "https://auth.example.com/page",
		"User-Agent":    "MyBrowser/9.9",
	}
	mirrors := []string{url, "https://mirror.example.com/secret.zip"}

	state := &types.DownloadRecord{
		ID:                   id,
		URL:                  url,
		DestPath:             destPath,
		Filename:             "secret.zip",
		TotalSize:            1000,
		Downloaded:           400,
		Elapsed:              int64(2 * time.Second),
		Tasks:                []types.Task{{Offset: 400, Length: 600}},
		ChunkBitmap:          []byte{0b00000001},
		ActualChunkSize:      500,
		Mirrors:              mirrors,
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		Headers:              headers,
		// Whitelist-excluded runtime fields: an unregistered non-nil
		// ProgressState would previously fail the whole gob encode.
		ProgressState: &struct{ N int }{N: 1},
		Runtime:       types.DefaultRuntimeConfig(),
		SupportsRange: true,
		IsResume:      true,
		OutputPath:    "SENTINEL_NOT_PERSISTED",
	}
	if err := SaveStateWithOptions(url, destPath, state, SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions failed: %v", err)
	}
	if err := AddToMasterList(types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "secret.zip", Status: "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList failed: %v", err)
	}

	loaded, err := LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadState returned nil")
	}
	if !maps.Equal(loaded.Headers, headers) {
		t.Errorf("Headers = %v, want %v", loaded.Headers, headers)
	}
	if loaded.Elapsed != state.Elapsed {
		t.Errorf("Elapsed = %d, want %d", loaded.Elapsed, state.Elapsed)
	}
	if len(loaded.Tasks) != 1 || loaded.Tasks[0].Offset != 400 || loaded.Tasks[0].Length != 600 {
		t.Errorf("Tasks = %+v, want one task 400/600", loaded.Tasks)
	}
	if len(loaded.Mirrors) != 2 || loaded.Mirrors[1] != mirrors[1] {
		t.Errorf("Mirrors = %v, want %v", loaded.Mirrors, mirrors)
	}
	if loaded.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Errorf("RangeAcquisitionMode = %q, want range_supported", loaded.RangeAcquisitionMode)
	}
	// Runtime-only fields must be zero after the projection round-trip.
	if loaded.ProgressState != nil {
		t.Errorf("ProgressState = %v, want nil", loaded.ProgressState)
	}
	if loaded.Runtime != nil {
		t.Errorf("Runtime = %v, want nil", loaded.Runtime)
	}
	if loaded.SupportsRange {
		t.Error("SupportsRange persisted, want false")
	}
	if loaded.IsResume {
		t.Error("IsResume persisted, want false")
	}
	if loaded.OutputPath != "" {
		t.Errorf("OutputPath = %q, want empty", loaded.OutputPath)
	}

	// Inspect raw gob bytes: credentials in, sentinel fields out.
	raw, err := os.ReadFile(getDetailPath(tmpDir, id))
	if err != nil {
		t.Fatalf("failed to read detail gob: %v", err)
	}
	if !bytes.Contains(raw, []byte("session=xyz123")) {
		t.Error("detail gob missing persisted Cookie value")
	}
	if !bytes.Contains(raw, []byte("Bearer tok-abc")) {
		t.Error("detail gob missing persisted Authorization value")
	}
	if bytes.Contains(raw, []byte("SENTINEL_NOT_PERSISTED")) {
		t.Error("detail gob leaked whitelist-excluded OutputPath sentinel")
	}
}

func TestLoadState_LegacyDetailFixture(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	url := "https://legacy.example.com/old.zip"
	destPath := filepath.Join(tmpDir, "old.zip")
	id := "legacy-detail-id"

	if err := AddToMasterList(types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "old.zip", Status: "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList failed: %v", err)
	}

	// Hand-encode the pre-projection shape: State was *types.DownloadRecord.
	// gob matches by field name, so this is byte-compatible with old files.
	legacy := legacyDetailState{
		Version: 1,
		State: &types.DownloadRecord{
			ID:           id,
			URL:          url,
			DestPath:     destPath,
			Filename:     "old.zip",
			TotalSize:    2048,
			Downloaded:   512,
			Tasks:        []types.Task{{Offset: 512, Length: 1536}},
			Mirrors:      []string{url},
			FileHash:     "md5:deadbeef",
			OutputPath:   "/should/not/come/back",
			IsResume:     true,
			Elapsed:      int64(3 * time.Second),
			MinChunkSize: 256,
		},
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(legacy); err != nil {
		t.Fatalf("failed to encode legacy fixture: %v", err)
	}
	detailPath := getDetailPath(tmpDir, id)
	if err := os.MkdirAll(filepath.Dir(detailPath), 0o755); err != nil {
		t.Fatalf("failed to create details dir: %v", err)
	}
	if err := os.WriteFile(detailPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write legacy fixture: %v", err)
	}

	loaded, err := LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState failed on legacy detail: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadState returned nil for legacy detail")
	}
	if loaded.Downloaded != 512 || loaded.TotalSize != 2048 {
		t.Errorf("progress = %d/%d, want 512/2048", loaded.Downloaded, loaded.TotalSize)
	}
	if loaded.FileHash != "md5:deadbeef" {
		t.Errorf("FileHash = %q, want md5:deadbeef", loaded.FileHash)
	}
	if loaded.Headers != nil {
		t.Errorf("legacy detail should decode with nil Headers, got %v", loaded.Headers)
	}
	// Fields outside the whitelist in the old file are dropped on decode.
	if loaded.OutputPath != "" || loaded.IsResume {
		t.Error("legacy-only runtime fields should not survive projection decode")
	}

	// LoadStates must decode the same file.
	states, err := LoadStates([]string{id})
	if err != nil {
		t.Fatalf("LoadStates failed: %v", err)
	}
	if st := states[id]; st == nil || st.Downloaded != 512 {
		t.Errorf("LoadStates[%s] = %+v, want Downloaded=512", id, st)
	}
}

func TestValidateIntegrity_RemovesOrphanDetails(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	detailsDir := filepath.Join(tmpDir, "details")
	writeDetail := func(id string) string {
		t.Helper()
		if err := os.MkdirAll(detailsDir, 0o755); err != nil {
			t.Fatalf("mkdir details: %v", err)
		}
		p := filepath.Join(detailsDir, id+".gob")
		if err := atomicWrite(p, DetailState{
			Version: 2,
			State: &persistedDetailRecord{
				ID: id, URL: "https://x/" + id, DestPath: filepath.Join(tmpDir, id),
				Headers: map[string]string{"Cookie": "s=1"},
			},
		}); err != nil {
			t.Fatalf("write detail %s: %v", id, err)
		}
		return p
	}

	// Orphan: detail with no master row.
	orphanPath := writeDetail("orphan-detail")
	// Completed: detail whose row is finished — credentials must not linger.
	completedPath := writeDetail("completed-detail")
	// Alive paused entry: detail must be kept.
	pausedDest := filepath.Join(tmpDir, "alive.bin")
	if err := AddToMasterList(types.DownloadRecord{
		ID: "alive-id", URL: "https://x/alive.bin", DestPath: pausedDest,
		Filename: "alive.bin", Status: "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList alive: %v", err)
	}
	if err := os.WriteFile(pausedDest+types.IncompleteSuffix, []byte("part"), 0o644); err != nil {
		t.Fatalf("write .surge: %v", err)
	}
	alivePath := writeDetail("alive-id")
	if err := AddToMasterList(types.DownloadRecord{
		ID: "completed-detail", URL: "https://x/done.bin", DestPath: filepath.Join(tmpDir, "done.bin"),
		Filename: "done.bin", Status: "completed", CompletedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("AddToMasterList completed: %v", err)
	}
	// Non-.gob / temp files must be ignored.
	tmpFile := filepath.Join(detailsDir, ".tmp-12345")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	notGob := filepath.Join(detailsDir, "stray.txt")
	if err := os.WriteFile(notGob, []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	if _, err := ValidateIntegrity(); err != nil {
		t.Fatalf("ValidateIntegrity failed: %v", err)
	}

	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Errorf("orphan detail should be removed, stat err: %v", err)
	}
	if _, err := os.Stat(completedPath); !os.IsNotExist(err) {
		t.Errorf("completed detail should be removed, stat err: %v", err)
	}
	if _, err := os.Stat(alivePath); err != nil {
		t.Errorf("paused detail should be kept, stat err: %v", err)
	}
	if _, err := os.Stat(tmpFile); err != nil {
		t.Error(".tmp- file should be left alone")
	}
	if _, err := os.Stat(notGob); err != nil {
		t.Error("non-.gob file should be left alone")
	}
}

func TestInvalidateResumeState(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	// Missing detail: nil-safe.
	if err := InvalidateResumeState("no-such-id"); err != nil {
		t.Fatalf("InvalidateResumeState on missing detail: %v", err)
	}

	url := "https://auth.example.com/f.bin"
	destPath := filepath.Join(tmpDir, "f.bin")
	id := "invalidate-id"
	if err := AddToMasterList(types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "f.bin", Status: "paused",
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
	}); err != nil {
		t.Fatalf("AddToMasterList failed: %v", err)
	}
	detailPath := getDetailPath(tmpDir, id)
	if err := os.MkdirAll(filepath.Dir(detailPath), 0o755); err != nil {
		t.Fatalf("mkdir details: %v", err)
	}
	if err := atomicWrite(detailPath, DetailState{
		Version: 2,
		State: &persistedDetailRecord{
			ID: id, URL: url, DestPath: destPath, Filename: "f.bin",
			Status: "paused", TotalSize: 1000, Downloaded: 250,
			Tasks:                []types.Task{{Offset: 250, Length: 750}},
			ChunkBitmap:          []byte{0x01},
			ActualChunkSize:      500,
			FileHash:             "md5:abc",
			RangeAcquisitionMode: types.RangeAcquireRangeSupported,
			Mirrors:              []string{url, "https://m.example.com/f.bin"},
			Headers:              map[string]string{"Cookie": "session=keepme"},
			Workers:              4,
			MinChunkSize:         128,
		},
	}); err != nil {
		t.Fatalf("seed detail: %v", err)
	}

	if err := InvalidateResumeState(id); err != nil {
		t.Fatalf("InvalidateResumeState failed: %v", err)
	}

	loaded, err := LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("detail should survive invalidation")
	}
	if len(loaded.Tasks) != 0 {
		t.Errorf("Tasks = %v, want cleared", loaded.Tasks)
	}
	if len(loaded.ChunkBitmap) != 0 {
		t.Errorf("ChunkBitmap = %v, want cleared", loaded.ChunkBitmap)
	}
	if loaded.ActualChunkSize != 0 {
		t.Errorf("ActualChunkSize = %d, want 0", loaded.ActualChunkSize)
	}
	if loaded.FileHash != "" {
		t.Errorf("FileHash = %q, want empty", loaded.FileHash)
	}
	if loaded.RangeAcquisitionMode != "" {
		t.Errorf("RangeAcquisitionMode = %q, want cleared", loaded.RangeAcquisitionMode)
	}
	// Credentials and mirrors must survive.
	if got := loaded.Headers["Cookie"]; got != "session=keepme" {
		t.Errorf("Cookie header = %q, want session=keepme", got)
	}
	if len(loaded.Mirrors) != 2 {
		t.Errorf("Mirrors = %v, want preserved", loaded.Mirrors)
	}
	if loaded.Workers != 4 || loaded.MinChunkSize != 128 {
		t.Errorf("config fields changed: workers=%d minChunk=%d", loaded.Workers, loaded.MinChunkSize)
	}
}

func TestAddToMasterList_ScrubsSecretsAndRuntime(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	entry := types.DownloadRecord{
		ID:       "scrub-id",
		URL:      "https://auth.example.com/x.zip",
		DestPath: filepath.Join(tmpDir, "x.zip"),
		Filename: "x.zip",
		Status:   "queued",
		Headers:  map[string]string{"Cookie": "session=topsecret", "Authorization": "Bearer s3cr3t"},
		// An unregistered ProgressState would previously fail the master gob
		// write entirely; scrubbing makes the write succeed.
		ProgressState:      &struct{ N int }{N: 7},
		Runtime:            types.DefaultRuntimeConfig(),
		IsResume:           true,
		IsExplicitCategory: true,
		SupportsRange:      true,
		OutputPath:         "/sentinel/out",
		Mirrors:            []string{"https://m.example.com/x.zip"},
		RateLimit:          4096,
		Workers:            8,
	}
	if err := AddToMasterList(entry); err != nil {
		t.Fatalf("AddToMasterList failed: %v", err)
	}

	loaded, err := GetDownload("scrub-id")
	if err != nil || loaded == nil {
		t.Fatalf("GetDownload failed: %v (nil=%v)", err, loaded == nil)
	}
	if loaded.Headers != nil {
		t.Errorf("master Headers = %v, want nil", loaded.Headers)
	}
	if loaded.ProgressState != nil || loaded.ProgressCh != nil || loaded.Runtime != nil || loaded.Limiter != nil {
		t.Error("runtime fields leaked into master record")
	}
	if loaded.IsResume || loaded.IsExplicitCategory || loaded.SupportsRange {
		t.Error("runtime bool fields leaked into master record")
	}
	// OutputPath is a legitimately persisted master-side field, not a
	// secret or runtime handle — the scrub must leave it intact.
	if loaded.OutputPath != "/sentinel/out" {
		t.Errorf("OutputPath = %q, want persisted master field", loaded.OutputPath)
	}
	// Non-secret configuration must be retained.
	if loaded.RateLimit != 4096 || loaded.Workers != 8 || len(loaded.Mirrors) != 1 {
		t.Errorf("config lost: rate=%d workers=%d mirrors=%v", loaded.RateLimit, loaded.Workers, loaded.Mirrors)
	}

	raw, err := os.ReadFile(getMasterPath())
	if err != nil {
		t.Fatalf("read master.gob: %v", err)
	}
	if bytes.Contains(raw, []byte("topsecret")) || bytes.Contains(raw, []byte("s3cr3t")) {
		t.Error("master.gob contains credential material")
	}
}
