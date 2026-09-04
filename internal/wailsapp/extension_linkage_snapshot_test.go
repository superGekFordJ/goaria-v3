//go:build extractor

package wailsapp

import (
	"context"
	"path/filepath"
	"testing"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
	"goaria-v3/internal/tasks"
)

func TestLinkageConstructionFromSnapshot_ZeroPack(t *testing.T) {
	dataRoot := t.TempDir()
	mgr, err := extractor.NewExtractorRuntimeManager(context.Background(), extractor.ExtractorRuntimeManagerConfig{
		DataRoot:           dataRoot,
		HostPolicyResolver: fakeSetupHostPolicyResolver{},
	})
	if err != nil {
		t.Fatalf("NewExtractorRuntimeManager: %v", err)
	}

	zeroSnap := mgr.CurrentSnapshot()
	engine := &recordingCommitEngine{failURLs: map[string]struct{}{}}
	linkage := buildExtensionLinkageFromSnapshot(zeroSnap, engine)

	if linkage.Resolver != nil {
		t.Errorf("expected nil Resolver for zero-pack, got %#v", linkage.Resolver)
	}
	if linkage.Digests != nil {
		t.Errorf("expected nil Digests for zero-pack, got %#v", linkage.Digests)
	}
	if linkage.Committer != nil {
		t.Errorf("expected nil Committer for zero-pack, got %#v", linkage.Committer)
	}

	app := NewApp(Options{DownloadEngine: engine})
	runtime := &taggedExtractorRuntime{manager: mgr}
	app.setExtractorRuntime(runtime)

	store := extension.NewSecretStore()
	store.SetSecret("link-secret")
	srv := extension.NewServer(nil, nil, store)
	ConfigureExtensionLinkage(app, srv)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ack := dialAuthAck(t, srv.GetStatus().WSPort, "link-secret")
	if hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("zero-pack must not advertise extractor.resolve, got %v", ack.Capabilities)
	}
	if hasCap(ack.Capabilities, extension.CapExtractorBatch) {
		t.Fatalf("zero-pack must not advertise extractor.batch, got %v", ack.Capabilities)
	}
	if !hasCap(ack.Capabilities, extension.CapDownloadBatch) {
		t.Fatalf("zero-pack must still advertise download.batch via DirectCommitter, got %v", ack.Capabilities)
	}
	if ack.Match != nil {
		t.Fatal("zero-pack must omit match digest")
	}
}

func TestLinkageConstructionFromSnapshot_WithPacks(t *testing.T) {
	dataRoot := t.TempDir()
	packDir := filepath.Join(t.TempDir(), "fixture-pack")
	lockOut := filepath.Join(packDir, packbuilder.HostCallFixturePackID+".lock.json")
	if _, err := packbuilder.WriteHostCallFixture(packDir, lockOut); err != nil {
		t.Fatalf("WriteHostCallFixture: %v", err)
	}

	mgr, err := extractor.NewExtractorRuntimeManager(context.Background(), extractor.ExtractorRuntimeManagerConfig{
		DataRoot:           dataRoot,
		HostPolicyResolver: fakeSetupHostPolicyResolver{},
	})
	if err != nil {
		t.Fatalf("NewExtractorRuntimeManager: %v", err)
	}

	_, err = mgr.LoadSource(context.Background(), extractor.RuntimeSourceSpec{
		Kind:    extractor.RuntimeSourceKindLocalDirectory,
		Locator: packDir,
	})
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}

	snap := mgr.CurrentSnapshot()
	engine := &recordingCommitEngine{failURLs: map[string]struct{}{}}
	linkage := buildExtensionLinkageFromSnapshot(snap, engine)

	if linkage.Resolver == nil || !linkage.Resolver.Ready() {
		t.Fatal("expected ready Resolver")
	}
	if linkage.Digests == nil || !linkage.Digests.Ready() {
		t.Fatal("expected ready Digests")
	}
	if linkage.Committer == nil || !linkage.Committer.Ready() {
		t.Fatal("expected ready Committer")
	}

	app := NewApp(Options{DownloadEngine: engine})
	runtime := &taggedExtractorRuntime{manager: mgr}
	app.setExtractorRuntime(runtime)

	store := extension.NewSecretStore()
	store.SetSecret("link-secret")
	srv := extension.NewServer(nil, nil, store)
	ConfigureExtensionLinkage(app, srv)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ack := dialAuthAck(t, srv.GetStatus().WSPort, "link-secret")
	if !hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("ready pack must advertise extractor.resolve, got %v", ack.Capabilities)
	}
	if !hasCap(ack.Capabilities, extension.CapExtractorBatch) {
		t.Fatalf("ready pack must advertise extractor.batch, got %v", ack.Capabilities)
	}
	if !hasCap(ack.Capabilities, extension.CapDownloadBatch) {
		t.Fatalf("ready pack must advertise download.batch, got %v", ack.Capabilities)
	}
	if ack.Match == nil {
		t.Fatal("ready pack must include match digest")
	}
}

func TestExtensionCommitInvokesFixedOldTaskServiceAcrossSnapshotSwitch(t *testing.T) {
	dataRoot := t.TempDir()
	packDir := filepath.Join(t.TempDir(), "fixture-pack")
	lockOut := filepath.Join(packDir, packbuilder.HostCallFixturePackID+".lock.json")
	if _, err := packbuilder.WriteHostCallFixture(packDir, lockOut); err != nil {
		t.Fatalf("WriteHostCallFixture: %v", err)
	}

	mgr, err := extractor.NewExtractorRuntimeManager(context.Background(), extractor.ExtractorRuntimeManagerConfig{
		DataRoot:           dataRoot,
		HostPolicyResolver: fakeSetupHostPolicyResolver{},
	})
	if err != nil {
		t.Fatalf("NewExtractorRuntimeManager: %v", err)
	}

	srcState, err := mgr.LoadSource(context.Background(), extractor.RuntimeSourceSpec{
		Kind:    extractor.RuntimeSourceKindLocalDirectory,
		Locator: packDir,
	})
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}

	snap1 := mgr.CurrentSnapshot()
	engine := &recordingCommitEngine{failURLs: map[string]struct{}{}}
	app := NewApp(Options{DownloadEngine: engine})
	runtime := &taggedExtractorRuntime{manager: mgr}
	app.setExtractorRuntime(runtime)

	linkage1 := buildExtensionLinkageFromSnapshot(snap1, engine)
	batchAdapter, ok := linkage1.Committer.(*extensionBatchAdapter)
	if !ok {
		t.Fatalf("expected *extensionBatchAdapter, got %T", linkage1.Committer)
	}

	fixedService, ok := batchAdapter.service.(*tasks.Service)
	if !ok {
		t.Fatalf("expected *tasks.Service, got %T", batchAdapter.service)
	}
	if fixedService.Adapter != snap1.TasksAdapter() {
		t.Fatalf("expected service adapter to match snap1 adapter")
	}

	// Now mutate manager to create snap2
	_, err = mgr.ReloadSource(context.Background(), srcState.SourceID)
	if err != nil {
		t.Fatalf("ReloadSource: %v", err)
	}
	snap2 := mgr.CurrentSnapshot()
	if snap2.Revision() == snap1.Revision() {
		t.Fatalf("expected snap2 revision > snap1 revision")
	}

	// Verify that app.taskService().Adapter now points to snap2
	currentService := app.taskService()
	if currentService.Adapter != snap2.TasksAdapter() {
		t.Fatalf("expected app.taskService() adapter to be snap2 adapter")
	}

	// But batchAdapter.service must still point to snap1 adapter!
	if fixedService.Adapter != snap1.TasksAdapter() {
		t.Fatalf("expected batchAdapter to maintain fixed snap1 adapter across switch")
	}
}
