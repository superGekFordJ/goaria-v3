//go:build extractor

package wailsapp

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

type barrierSourceManager struct {
	extractorSourceManager
	beforeMut1  chan struct{}
	unblockMut1 chan struct{}
	beforeMut2  chan struct{}
	unblockMut2 chan struct{}
	reloadCount atomic.Int32
}

func (b *barrierSourceManager) ReloadSource(ctx context.Context, sourceID string) (extractor.RuntimeSourceState, error) {
	switch b.reloadCount.Add(1) {
	case 1:
		close(b.beforeMut1)
		<-b.unblockMut1
	case 2:
		close(b.beforeMut2)
		<-b.unblockMut2
	}
	return b.extractorSourceManager.ReloadSource(ctx, sourceID)
}

func TestConcurrentWailsMutationsSerializedAndNormalAddUnblocked(t *testing.T) {
	dataRoot := t.TempDir()
	policyResolver := fakeSetupHostPolicyResolver{}
	mgr, err := extractor.NewExtractorRuntimeManager(context.Background(), extractor.ExtractorRuntimeManagerConfig{
		DataRoot:           dataRoot,
		HostPolicyResolver: policyResolver,
	})
	if err != nil {
		t.Fatalf("NewExtractorRuntimeManager: %v", err)
	}

	packDir := filepath.Join(t.TempDir(), "fixture-pack")
	lockOut := filepath.Join(packDir, packbuilder.HostCallFixturePackID+".lock.json")
	if _, err := packbuilder.WriteHostCallFixture(packDir, lockOut); err != nil {
		t.Fatalf("WriteHostCallFixture: %v", err)
	}

	// Pre-load one source so both Mutation 1 and Mutation 2 can successfully reload it
	initialSrc, err := mgr.LoadSource(context.Background(), extractor.RuntimeSourceSpec{
		Kind:    extractor.RuntimeSourceKindLocalDirectory,
		Locator: packDir,
	})
	if err != nil {
		t.Fatalf("initial LoadSource: %v", err)
	}

	bm := &barrierSourceManager{
		extractorSourceManager: mgr,
		beforeMut1:             make(chan struct{}),
		unblockMut1:            make(chan struct{}),
		beforeMut2:             make(chan struct{}),
		unblockMut2:            make(chan struct{}),
	}

	engine := &recordingCommitEngine{failURLs: map[string]struct{}{}}
	app := NewApp(Options{DownloadEngine: engine})
	runtime := &taggedExtractorRuntime{
		manager: bm,
	}
	app.setExtractorRuntime(runtime)

	store := extension.NewSecretStore()
	secret := store.GenerateSecret()
	store.SetSecret(secret)
	srv := extension.NewServer(nil, nil, store)
	ConfigureExtensionLinkage(app, srv)
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()
	app.extensionServer = srv

	// 1. Launch Mutation 1 in background
	var mut1Res ExtractorOperationResult
	mut1Done := make(chan struct{})
	go func() {
		defer close(mut1Done)
		mut1Res = app.ReloadExtractorSource(initialSrc.SourceID)
	}()

	// Wait until Mutation 1 enters ReloadSource while holding mutationMu
	select {
	case <-bm.beforeMut1:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for mutation 1 to enter ReloadSource")
	}

	// 2. While Mutation 1 holds mutationMu:
	// A concurrent normal AddUri must NOT block on mutationMu and must complete immediately
	addDone := make(chan struct{})
	var addRes string
	go func() {
		defer close(addDone)
		addRes = app.AddUri("https://example.com/normal-download.zip")
	}()

	select {
	case <-addDone:
		if addRes == "" {
			t.Fatal("expected non-empty AddUri result")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("normal AddUri was blocked by ongoing Wails mutation")
	}

	// 3. While Mutation 1 still holds mutationMu, launch Mutation 2 (also a valid reload)
	var mut2Res ExtractorOperationResult
	mut2Done := make(chan struct{})
	go func() {
		defer close(mut2Done)
		mut2Res = app.ReloadExtractorSource(initialSrc.SourceID)
	}()

	// Verify Mutation 2 is blocked by mutationMu and has NOT entered ReloadSource yet
	select {
	case <-bm.beforeMut2:
		t.Fatal("mutation 2 entered ReloadSource while mutation 1 held mutationMu")
	default:
	}

	// 4. Unblock Mutation 1
	close(bm.unblockMut1)

	select {
	case <-mut1Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for mutation 1 to finish")
	}
	if !mut1Res.Success {
		t.Fatalf("mutation 1 failed: %s", mut1Res.ErrorCode)
	}

	// 5. Now Mutation 2 acquires mutationMu and enters ReloadSource
	select {
	case <-bm.beforeMut2:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for mutation 2 to enter ReloadSource")
	}

	// 6. Unblock Mutation 2
	close(bm.unblockMut2)

	select {
	case <-mut2Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for mutation 2 to finish")
	}
	if !mut2Res.Success {
		t.Fatalf("mutation 2 failed: %s", mut2Res.ErrorCode)
	}

	// 7. Verify final state and linkage:
	// Both mutations succeeded in order, final revision is 3 (initial load = 1, reload 1 = 2, reload 2 = 3)
	snap := mgr.CurrentSnapshot()
	if snap == nil || snap.TasksAdapter() == nil {
		t.Fatal("expected non-nil snap and tasks adapter")
	}
	if snap.Revision() != 4 {
		t.Fatalf("expected final revision 4, got %d", snap.Revision())
	}

	// Server linkage matches final manager snapshot
	ack := dialAuthAck(t, srv.GetStatus().WSPort, secret)
	if !hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("final server state missing extractor.resolve: %v", ack.Capabilities)
	}
}
