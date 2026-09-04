//go:build extractor

package wailsapp

import (
	"context"
	"path/filepath"
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
}

func (b *barrierSourceManager) ReloadSource(ctx context.Context, sourceID string) (extractor.RuntimeSourceState, error) {
	close(b.beforeMut1)
	<-b.unblockMut1
	return b.extractorSourceManager.ReloadSource(ctx, sourceID)
}

func (b *barrierSourceManager) RemoveSource(ctx context.Context, sourceID string) (extractor.RuntimeSourceState, error) {
	close(b.beforeMut2)
	<-b.unblockMut2
	return b.extractorSourceManager.RemoveSource(ctx, sourceID)
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

	// Pre-load one source: manager revision transitions from 1 (init) to 2
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

	// 1. Launch Mutation 1 in background: ReloadSource (ready state with extractor capability)
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

	// Confirm mutual exclusion: TryLock must return false while Mutation 1 holds mutationMu
	if runtime.mutationMu.TryLock() {
		runtime.mutationMu.Unlock()
		t.Fatal("mutationMu was unexpectedly unlocked during Mutation 1 execution")
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

	// 3. While Mutation 1 still holds mutationMu, launch Mutation 2: RemoveSource (degrades to zero-pack)
	var mut2Res ExtractorOperationResult
	mut2Done := make(chan struct{})
	go func() {
		defer close(mut2Done)
		mut2Res = app.RemoveExtractorSource(initialSrc.SourceID)
	}()

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

	// 5. Now Mutation 2 acquires mutationMu and enters RemoveSource
	select {
	case <-bm.beforeMut2:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for mutation 2 to enter RemoveSource")
	}

	// Confirm mutual exclusion: TryLock must return false while Mutation 2 holds mutationMu
	if runtime.mutationMu.TryLock() {
		runtime.mutationMu.Unlock()
		t.Fatal("mutationMu was unexpectedly unlocked during Mutation 2 execution")
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
	// Lifecycle revisions: init (1) -> initial load (2) -> reload (3) -> remove (4)
	snap := mgr.CurrentSnapshot()
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if snap.Revision() != 4 {
		t.Fatalf("expected final revision 4, got %d", snap.Revision())
	}

	// Verify zero-pack state on App
	state := app.GetExtractorState()
	if !state.Available {
		t.Fatal("expected manager available: true in zero-pack state")
	}
	if len(state.Sources) != 0 {
		t.Fatalf("expected 0 sources after removal, got %d", len(state.Sources))
	}

	// Server linkage matches final manager snapshot: zero-pack means NO extractor capabilities.
	// If Mutation 1 had installed its linkage after Mutation 2, extractor.resolve would be present.
	ack := dialAuthAck(t, srv.GetStatus().WSPort, secret)
	if hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("final server state must not advertise extractor.resolve after removal (stale linkage bug): %v", ack.Capabilities)
	}
	if hasCap(ack.Capabilities, extension.CapExtractorBatch) {
		t.Fatalf("final server state must not advertise extractor.batch after removal: %v", ack.Capabilities)
	}
	if !hasCap(ack.Capabilities, extension.CapDownloadBatch) {
		t.Fatalf("final server state must preserve download.batch: %v", ack.Capabilities)
	}
}
