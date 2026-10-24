package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/types"
)

// seedHLSMaster registers a master entry and returns it.
func seedHLSMaster(t *testing.T, rec types.DownloadRecord) types.DownloadRecord {
	t.Helper()
	if err := AddToMasterList(rec); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}
	return rec
}

func loadDetailRecord(t *testing.T, id string) *types.DownloadRecord {
	t.Helper()
	states, err := LoadStates([]string{id})
	if err != nil {
		t.Fatalf("LoadStates: %v", err)
	}
	rec := states[id]
	if rec == nil {
		t.Fatalf("no detail state for %s", id)
	}
	return rec
}

func detailExists(t *testing.T, id string) bool {
	t.Helper()
	states, _ := LoadStates([]string{id})
	return states[id] != nil
}

func TestUpdateURL_HLSUnfinishedRebasesDetail(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	const id = "hls-unfinished"
	oldURL := "https://a.example.com/media.m3u8"
	newURL := "https://b.example.com/v2/media.m3u8"
	dest := filepath.Join(tmpDir, "video.ts")

	seedHLSMaster(t, types.DownloadRecord{
		ID: id, URL: oldURL, URLHash: URLHash(oldURL), DestPath: dest,
		Filename: "video.ts", Status: "paused", Strategy: types.StrategyHLS,
		Workers: 4, MinChunkSize: 1 << 16, RateLimit: 1024, RateLimitSet: true,
	})
	resume := &types.HLSResumeState{
		ManifestURL:      oldURL,
		MediaPlaylistURL: oldURL,
		TotalSegments:    3,
		CompletedBitmap:  []byte{0x01},
		MergedSegments:   1,
		MergedBytes:      128,
		VerifiedBytes:    128,
		Segments:         []types.HLSSegmentState{{Index: 0, URI: "s0.ts"}},
	}
	rec := &types.DownloadRecord{
		ID: id, URL: oldURL, DestPath: dest, Filename: "video.ts",
		Status: "paused", Strategy: types.StrategyHLS,
		Downloaded: 128, TotalSize: 0,
		HLSResume:       resume,
		Tasks:           []types.Task{{Offset: 0, Length: 127}},
		ChunkBitmap:     []byte{0xff},
		ActualChunkSize: 64,
		FileHash:        "md5:deadbeef",
		Workers:         4,
	}
	if err := SaveState(oldURL, dest, rec); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	if err := UpdateURL(id, newURL); err != nil {
		t.Fatalf("UpdateURL: %v", err)
	}

	// Master entry: URL/hash swapped, progress counters zeroed.
	master, err := GetDownload(id)
	if err != nil || master == nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if master.URL != newURL || master.URLHash != URLHash(newURL) {
		t.Fatalf("master URL/URLHash = %q/%q", master.URL, master.URLHash)
	}
	if master.Downloaded != 0 || master.TotalSize != 0 {
		t.Fatalf("master progress not zeroed: downloaded=%d total=%d", master.Downloaded, master.TotalSize)
	}

	// Detail: URL/hash swapped, progress/byte-range payload cleared,
	// checkpoint collapsed to a minimal anchor-only seed.
	detail := loadDetailRecord(t, id)
	if detail.URL != newURL || detail.URLHash != URLHash(newURL) {
		t.Fatalf("detail URL/URLHash = %q/%q", detail.URL, detail.URLHash)
	}
	if detail.Strategy != types.StrategyHLS {
		t.Fatalf("detail strategy = %q", detail.Strategy)
	}
	if detail.Downloaded != 0 || detail.TotalSize != 0 {
		t.Fatalf("detail progress not zeroed: downloaded=%d total=%d", detail.Downloaded, detail.TotalSize)
	}
	if detail.FileHash != "" || len(detail.Tasks) != 0 || len(detail.ChunkBitmap) != 0 || detail.ActualChunkSize != 0 {
		t.Fatalf("byte-range payload survived rebase: hash=%q tasks=%d bitmap=%d",
			detail.FileHash, len(detail.Tasks), len(detail.ChunkBitmap))
	}
	hr := detail.HLSResume
	if hr == nil || hr.ManifestURL != newURL || !hr.RecordEligible ||
		hr.MediaPlaylistURL != "" || hr.TotalSegments != 0 || hr.MergedBytes != 0 || len(hr.Segments) != 0 {
		t.Fatalf("detail HLSResume not rebased: %+v", hr)
	}
	// Identity/rate-limit metadata must survive.
	if detail.Workers != 4 || detail.Filename != "video.ts" || detail.DestPath != dest {
		t.Fatalf("identity fields lost: %+v", detail)
	}
	// The pre-update in-memory resume object must be untouched.
	if resume.ManifestURL != oldURL || resume.MergedSegments != 1 {
		t.Fatal("UpdateURL mutated the old checkpoint object")
	}
}

func TestUpdateURL_HLSCompletedOnlyTouchesHistory(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	const id = "hls-done"
	oldURL := "https://a.example.com/media.m3u8"
	newURL := "https://b.example.com/final.m3u8"
	dest := filepath.Join(tmpDir, "done.ts")

	seedHLSMaster(t, types.DownloadRecord{
		ID: id, URL: oldURL, URLHash: URLHash(oldURL), DestPath: dest,
		Filename: "done.ts", Status: "completed", Strategy: types.StrategyHLS,
		Downloaded: 512, TotalSize: 512,
	})
	if err := SaveState(oldURL, dest, &types.DownloadRecord{
		ID: id, URL: oldURL, DestPath: dest, Filename: "done.ts",
		Status: "completed", Strategy: types.StrategyHLS,
		Downloaded: 512, TotalSize: 512,
		HLSResume: &types.HLSResumeState{
			ManifestURL: oldURL, TotalSegments: 2, MergedSegments: 2,
			MergedBytes: 512, VerifiedBytes: 512,
		},
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	if err := UpdateURL(id, newURL); err != nil {
		t.Fatalf("UpdateURL: %v", err)
	}
	master, err := GetDownload(id)
	if err != nil || master == nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if master.URL != newURL || master.URLHash != URLHash(newURL) {
		t.Fatalf("master URL not updated: %q", master.URL)
	}
	// A completed row keeps its historical counters.
	if master.Downloaded != 512 || master.TotalSize != 512 {
		t.Fatalf("completed master counters clobbered: %+v", master)
	}
	detail := loadDetailRecord(t, id)
	if detail.URL != oldURL || detail.HLSResume == nil || detail.HLSResume.MergedBytes != 512 {
		t.Fatalf("completed detail touched: %+v", detail)
	}
}

func TestUpdateURL_NonHLSTakesLegacyPath(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	const id = "plain"
	oldURL := "https://a.example.com/file.bin"
	newURL := "https://b.example.com/file.bin"
	dest := filepath.Join(tmpDir, "file.bin")

	seedHLSMaster(t, types.DownloadRecord{
		ID: id, URL: oldURL, URLHash: URLHash(oldURL), DestPath: dest,
		Filename: "file.bin", Status: "paused",
		Downloaded: 100, TotalSize: 200,
	})
	if err := UpdateURL(id, newURL); err != nil {
		t.Fatalf("UpdateURL: %v", err)
	}
	master, err := GetDownload(id)
	if err != nil || master == nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if master.URL != newURL || master.URLHash != URLHash(newURL) {
		t.Fatalf("master URL not updated: %q", master.URL)
	}
	if master.Downloaded != 100 || master.TotalSize != 200 {
		t.Fatalf("non-HLS counters clobbered: %+v", master)
	}
}

func TestUpdateURL_HLSMissingDetailMintsFromIdentity(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	const id = "hls-no-detail"
	oldURL := "https://a.example.com/media.m3u8"
	newURL := "https://b.example.com/m.m3u8"
	dest := filepath.Join(tmpDir, "v.ts")

	seedHLSMaster(t, types.DownloadRecord{
		ID: id, URL: oldURL, URLHash: URLHash(oldURL), DestPath: dest,
		Filename: "v.ts", Status: "paused", Strategy: types.StrategyHLS,
		Workers: 2, MinChunkSize: 1 << 15,
	})
	if detailExists(t, id) {
		t.Fatal("detail unexpectedly exists")
	}
	if err := UpdateURL(id, newURL); err != nil {
		t.Fatalf("UpdateURL: %v", err)
	}
	detail := loadDetailRecord(t, id)
	if detail.URL != newURL || detail.Strategy != types.StrategyHLS ||
		detail.Filename != "v.ts" || detail.DestPath != dest || detail.Workers != 2 {
		t.Fatalf("minted detail wrong: %+v", detail)
	}
	// No prior checkpoint → nil stays nil; there is nothing to rebase.
	if detail.HLSResume != nil {
		t.Fatalf("minted detail has checkpoint: %+v", detail.HLSResume)
	}
}

func TestUpdateURL_NotFound(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	if err := UpdateURL("missing-id", "https://x/y"); !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("UpdateURL(missing) = %v, want ErrNotFound", err)
	}
}

func TestUpdateURL_HLSCorruptDetailFails(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	const id = "hls-corrupt"
	oldURL := "https://a.example.com/media.m3u8"
	dest := filepath.Join(tmpDir, "v.ts")

	seedHLSMaster(t, types.DownloadRecord{
		ID: id, URL: oldURL, URLHash: URLHash(oldURL), DestPath: dest,
		Filename: "v.ts", Status: "paused", Strategy: types.StrategyHLS,
	})
	detailPath := getDetailPath(baseDir, id)
	if err := os.MkdirAll(filepath.Dir(detailPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(detailPath, []byte("not-a-gob"), 0o644); err != nil {
		t.Fatalf("write corrupt detail: %v", err)
	}
	if err := UpdateURL(id, "https://b.example.com/m.m3u8"); err == nil {
		t.Fatal("UpdateURL succeeded over a corrupt detail — expected failure")
	}
	// Master must not have been updated on detail failure.
	master, err := GetDownload(id)
	if err != nil || master == nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if master.URL != oldURL {
		t.Fatalf("master URL moved despite failure: %q", master.URL)
	}
}
