package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func newArtifactTestManager(t *testing.T) *LifecycleManager {
	t.Helper()
	testutil.SetupStateDB(t)
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{})
	mgr := NewLifecycleManager(pool, nil, config.DefaultSettings())
	t.Cleanup(mgr.Shutdown)
	return mgr
}

// A trusted size, filename, and explicit range decision keep enqueue on the
// probe-free path so the request never reaches the network.
func artifactTestRequest(path, filename string) *DownloadRequest {
	return &DownloadRequest{
		URL:                "https://example.com/" + filename,
		Path:               path,
		Filename:           filename,
		FileSize:           4096,
		SupportsRange:      new(true),
		IsExplicitCategory: true,
	}
}

func TestEnqueueArtifactPolicy_RejectsHLSExpectedSHA256(t *testing.T) {
	mgr := newArtifactTestManager(t)
	req := artifactTestRequest(t.TempDir(), "media.m3u8")
	req.Strategy = types.StrategyHLS
	req.FileSize = 0
	req.SupportsRange = nil
	req.Artifact = types.ArtifactPolicy{ExpectedSHA256: strings.Repeat("a", 64)}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := mgr.EnqueueWithID(ctx, req, "hls-digest"); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("HLS digest request must fail with a sha256 error, got %v", err)
	}
}

func TestEnqueueArtifactPolicy_FailOnExistingDestination(t *testing.T) {
	mgr := newArtifactTestManager(t)
	destDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(destDir, "artifact.bin"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := artifactTestRequest(destDir, "artifact.bin")
	req.Artifact = types.ArtifactPolicy{OnExists: "fail"}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := mgr.Enqueue(ctx, req); !errors.Is(err, types.ErrDestinationExists) {
		t.Fatalf("existing destination with on_exists=fail: %v", err)
	}
}

func TestEnqueueArtifactPolicy_EmptyOnExistsKeepsUniqueFilename(t *testing.T) {
	mgr := newArtifactTestManager(t)
	destDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(destDir, "artifact.bin"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := artifactTestRequest(destDir, "artifact.bin")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, finalName, err := mgr.Enqueue(ctx, req)
	if err != nil {
		t.Fatalf("empty on_exists must keep unique naming: %v", err)
	}
	if finalName != "artifact(1).bin" {
		t.Fatalf("finalName = %q, want %q", finalName, "artifact(1).bin")
	}
}

func TestEnqueueArtifactPolicy_DoesNotMutateCaller(t *testing.T) {
	mgr := newArtifactTestManager(t)
	req := artifactTestRequest(t.TempDir(), "artifact.bin")
	req.Artifact = types.ArtifactPolicy{OnExists: "fail", ExpectedSHA256: strings.Repeat("A", 64)}
	before := req.Artifact

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := mgr.Enqueue(ctx, req); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if req.Artifact != before {
		t.Fatalf("caller artifact mutated by enqueue: before %+v after %+v", before, req.Artifact)
	}
}

// The detail gob whitelist must carry Artifact and ErrorCode in both
// directions — a struct-only addition would still compile while silently
// dropping both fields on every SaveState.
func TestArtifactPolicyDetailGob_RoundTrip(t *testing.T) {
	root := testutil.SetupStateDB(t)
	url := "https://example.com/blob"
	destPath := filepath.Join(root, "blob")
	wantArtifact := types.ArtifactPolicy{
		OnExists:       "fail",
		ExpectedSHA256: strings.Repeat("a", 64),
		SHA256:         strings.Repeat("b", 64),
		Verification:   "verified",
	}
	record := types.DownloadRecord{
		Artifact:  wantArtifact,
		ErrorCode: "integrity_mismatch",
		ID:        "gob-roundtrip",
		URL:       url,
		DestPath:  destPath,
		Filename:  "blob",
		Status:    "paused",
		TotalSize: 4096,
	}
	// LoadState locates the detail file through the master row.
	testutil.SeedMasterList(t, record)
	if err := store.SaveStateWithOptions(url, destPath, &record, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}
	got, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got == nil {
		t.Fatal("LoadState returned nil record")
	}
	if got.Artifact != wantArtifact {
		t.Fatalf("artifact policy lost in gob round trip: got %+v want %+v", got.Artifact, wantArtifact)
	}
	if got.ErrorCode != "integrity_mismatch" {
		t.Fatalf("error code lost in gob round trip: got %q", got.ErrorCode)
	}
}
