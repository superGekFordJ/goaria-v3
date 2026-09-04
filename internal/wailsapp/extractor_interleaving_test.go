//go:build extractor

package wailsapp

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

type barrierPicker struct {
	mu           sync.Mutex
	dirPath      string
	beforePick   func()
	afterPick    func()
	pickStarted  chan struct{}
	releasePick  chan struct{}
	pickFinished chan struct{}
}

func (b *barrierPicker) pickFile() (string, bool, error) {
	return "", false, nil
}

func (b *barrierPicker) pickDirectory() (string, bool, error) {
	if b.beforePick != nil {
		b.beforePick()
	}
	if b.pickStarted != nil {
		select {
		case b.pickStarted <- struct{}{}:
		default:
		}
	}
	if b.releasePick != nil {
		<-b.releasePick
	}
	if b.afterPick != nil {
		b.afterPick()
	}
	if b.pickFinished != nil {
		select {
		case b.pickFinished <- struct{}{}:
		default:
		}
	}
	b.mu.Lock()
	p := b.dirPath
	b.mu.Unlock()
	return p, false, nil
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

	engine := &recordingCommitEngine{failURLs: map[string]struct{}{}}
	app := NewApp(Options{DownloadEngine: engine})

	bp := &barrierPicker{
		pickStarted:  make(chan struct{}, 1),
		releasePick:  make(chan struct{}),
		pickFinished: make(chan struct{}, 1),
	}
	runtime := &taggedExtractorRuntime{
		manager: mgr,
		picker:  bp,
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

	packDir := filepath.Join(t.TempDir(), "fixture-pack")
	lockOut := filepath.Join(packDir, packbuilder.HostCallFixturePackID+".lock.json")
	if _, err := packbuilder.WriteHostCallFixture(packDir, lockOut); err != nil {
		t.Fatalf("WriteHostCallFixture: %v", err)
	}
	bp.dirPath = packDir

	// Launch Mutation 1 in background
	var mut1Done atomic.Bool
	var mut1Res ExtractorOperationResult
	go func() {
		mut1Res = app.LoadExtractorPackDirectory()
		mut1Done.Store(true)
	}()

	// Wait until Mutation 1 enters picker inside mutationMu
	select {
	case <-bp.pickStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for mutation 1 to enter picker")
	}

	// While Mutation 1 is holding mutationMu:
	// 1. A concurrent Mutation 2 MUST wait on mutationMu and not complete yet
	var mut2Started atomic.Bool
	var mut2Done atomic.Bool
	go func() {
		mut2Started.Store(true)
		_ = app.ReloadExtractorSource("non-existent-source-id")
		mut2Done.Store(true)
	}()

	time.Sleep(50 * time.Millisecond)
	if mut2Done.Load() {
		t.Fatal("mutation 2 completed while mutation 1 held mutationMu")
	}

	// 2. A concurrent normal AddUri must NOT block on mutationMu and must return immediately
	addDone := make(chan struct{})
	var addRes string
	go func() {
		addRes = app.AddUri("https://example.com/normal-download.zip")
		close(addDone)
	}()

	select {
	case <-addDone:
		// Normal add completed immediately without waiting on mutationMu!
		if addRes == "" {
			t.Fatal("expected non-empty AddUri result")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("normal AddUri was blocked by ongoing Wails mutation")
	}

	// Now unblock Mutation 1
	close(bp.releasePick)

	// Wait for Mutation 1 and Mutation 2 to complete
	deadline := time.Now().Add(5 * time.Second)
	for (!mut1Done.Load() || !mut2Done.Load()) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !mut1Done.Load() || !mut2Done.Load() {
		t.Fatal("mutations did not complete after release")
	}

	if !mut1Res.Success {
		t.Fatalf("mutation 1 failed: %q", mut1Res.ErrorCode)
	}

	// Final linkage in srv must match final Manager snapshot (1 pack)
	snap := mgr.CurrentSnapshot()
	if snap == nil || snap.TasksAdapter() == nil {
		t.Fatal("expected non-nil snap and tasks adapter")
	}

	ack := dialAuthAck(t, srv.GetStatus().WSPort, secret)
	if !hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("final server state missing extractor.resolve: %v", ack.Capabilities)
	}
}
