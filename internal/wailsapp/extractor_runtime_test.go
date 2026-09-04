//go:build extractor

package wailsapp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

type mockFilePicker struct {
	filePath   string
	fileCancel bool
	fileErr    error
	dirPath    string
	dirCancel  bool
	dirErr     error
	fileCalled bool
	dirCalled  bool
}

func (m *mockFilePicker) pickFile() (string, bool, error) {
	m.fileCalled = true
	return m.filePath, m.fileCancel, m.fileErr
}

func (m *mockFilePicker) pickDirectory() (string, bool, error) {
	m.dirCalled = true
	return m.dirPath, m.dirCancel, m.dirErr
}

type spyExtractorSourceManager struct {
	extractorSourceManager
	loadSourceFn func(ctx context.Context, spec extractor.RuntimeSourceSpec) (extractor.RuntimeSourceState, error)
	lastSpec     extractor.RuntimeSourceSpec
}

func (s *spyExtractorSourceManager) LoadSource(ctx context.Context, spec extractor.RuntimeSourceSpec) (extractor.RuntimeSourceState, error) {
	s.lastSpec = spec
	if s.loadSourceFn != nil {
		return s.loadSourceFn(ctx, spec)
	}
	return s.extractorSourceManager.LoadSource(ctx, spec)
}

func newTestAppWithManager(t *testing.T) (*App, *extractor.ExtractorRuntimeManager, *mockFilePicker, string) {
	t.Helper()
	dataRoot := t.TempDir()
	policyResolver := fakeSetupHostPolicyResolver{}
	mgr, err := extractor.NewExtractorRuntimeManager(context.Background(), extractor.ExtractorRuntimeManagerConfig{
		DataRoot:           dataRoot,
		HostPolicyResolver: policyResolver,
	})
	if err != nil {
		t.Fatalf("NewExtractorRuntimeManager: %v", err)
	}

	app := NewApp(Options{})
	picker := &mockFilePicker{}
	runtime := &taggedExtractorRuntime{
		manager: mgr,
		picker:  picker,
	}
	app.setExtractorRuntime(runtime)

	return app, mgr, picker, dataRoot
}

func createTestDirectoryPack(t *testing.T) string {
	t.Helper()
	packDir := filepath.Join(t.TempDir(), "test-pack")
	lockOut := filepath.Join(packDir, packbuilder.HostCallFixturePackID+".lock.json")
	writeRes, err := packbuilder.WriteHostCallFixture(packDir, lockOut)
	if err != nil {
		t.Fatalf("WriteHostCallFixture: %v", err)
	}
	return writeRes.OutDir
}

func TestExtractorStateCanonicalProjectionAndOrdering(t *testing.T) {
	app, _, _, _ := newTestAppWithManager(t)
	state := app.GetExtractorState()

	if !state.Available {
		t.Fatal("expected available: true in tagged build with manager")
	}
	if state.Sources == nil || len(state.Sources) != 0 {
		t.Fatalf("expected non-nil empty sources, got %#v", state.Sources)
	}
	if state.RecoveryErrors == nil || len(state.RecoveryErrors) != 0 {
		t.Fatalf("expected non-nil empty recovery errors, got %#v", state.RecoveryErrors)
	}
}

func TestExtractorState_UninitializedReturnsUnavailable(t *testing.T) {
	app := &App{}
	state := app.GetExtractorState()
	if state.Available {
		t.Fatal("expected available: false when app has no manager configured")
	}
	if state.Sources == nil || len(state.Sources) != 0 {
		t.Fatalf("expected non-nil empty sources, got %#v", state.Sources)
	}
	if state.RecoveryErrors == nil || len(state.RecoveryErrors) != 0 {
		t.Fatalf("expected non-nil empty recovery errors, got %#v", state.RecoveryErrors)
	}
}

func TestAppExtractorRuntime_ConcurrentAccess(t *testing.T) {
	app := &App{}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = app.GetExtractorState()
				_ = app.taskService()
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				app.setExtractorRuntime(nil)
			}
		}()
	}
	wg.Wait()
}

func TestLoadExtractorPackDirectorySuccessAndPrivacy(t *testing.T) {
	app, _, picker, _ := newTestAppWithManager(t)
	packDir := createTestDirectoryPack(t)
	picker.dirPath = packDir

	res := app.LoadExtractorPackDirectory()
	if !res.Success {
		t.Fatalf("expected success, got error_code: %q", res.ErrorCode)
	}
	if res.Cancelled {
		t.Fatal("expected cancelled: false")
	}
	if res.ErrorCode != "" {
		t.Fatalf("expected empty error_code, got %q", res.ErrorCode)
	}
	if len(res.State.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(res.State.Sources))
	}
	src := res.State.Sources[0]
	if src.Kind != ExtractorSourceKindLocalDirectory {
		t.Fatalf("expected local_directory, got %s", src.Kind)
	}
	if src.PackID != packbuilder.HostCallFixturePackID {
		t.Fatalf("expected pack id %s, got %s", packbuilder.HostCallFixturePackID, src.PackID)
	}
	if src.Status != ExtractorSourceStatusReady {
		t.Fatalf("expected status ready, got %s", src.Status)
	}

	// Privacy assertion: ensure local path does not cross into JSON
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, packDir) {
		t.Fatalf("local path %q leaked into result JSON: %s", packDir, jsonStr)
	}
}

func TestLoadExtractorPackDirectoryCancel(t *testing.T) {
	app, _, picker, _ := newTestAppWithManager(t)
	picker.dirCancel = true

	res := app.LoadExtractorPackDirectory()
	if res.Success {
		t.Fatal("expected success: false on cancel")
	}
	if !res.Cancelled {
		t.Fatal("expected cancelled: true")
	}
	if res.ErrorCode != "" {
		t.Fatalf("expected empty error_code on cancel, got %q", res.ErrorCode)
	}
	if len(res.State.Sources) != 0 {
		t.Fatalf("expected 0 sources, got %d", len(res.State.Sources))
	}
}

func TestLoadExtractorPackDirectoryPickerErrorRedacted(t *testing.T) {
	app, _, picker, _ := newTestAppWithManager(t)
	picker.dirErr = errors.New("OS native dialog error: access denied to C:\\Secret\\Path")

	res := app.LoadExtractorPackDirectory()
	if res.Success || res.Cancelled {
		t.Fatalf("expected failure, got success=%v, cancelled=%v", res.Success, res.Cancelled)
	}
	if res.ErrorCode != "source_unreadable" {
		t.Fatalf("expected source_unreadable, got %q", res.ErrorCode)
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, "C:\\Secret\\Path") || strings.Contains(jsonStr, "access denied") {
		t.Fatalf("raw dialog error leaked into result JSON: %s", jsonStr)
	}
}

func TestLoadExtractorPackFileCancel(t *testing.T) {
	app, _, picker, _ := newTestAppWithManager(t)
	picker.fileCancel = true

	res := app.LoadExtractorPackFile()
	if res.Success || !res.Cancelled {
		t.Fatalf("expected cancelled: true, got success=%v, cancelled=%v", res.Success, res.Cancelled)
	}
	if res.ErrorCode != "" {
		t.Fatalf("expected empty error_code, got %q", res.ErrorCode)
	}
}

func TestLoadExtractorPackURLPassThroughQueryWithoutReflection(t *testing.T) {
	app, mgr, _, _ := newTestAppWithManager(t)
	spy := &spyExtractorSourceManager{
		extractorSourceManager: mgr,
		loadSourceFn: func(ctx context.Context, spec extractor.RuntimeSourceSpec) (extractor.RuntimeSourceState, error) {
			return extractor.RuntimeSourceState{}, &extractor.RuntimePackLoadError{
				Code: extractor.RuntimePackLoadErrorRemoteFailed,
			}
		},
	}
	app.taggedRuntime().manager = spy

	// A private query URL
	secretURL := "https://example.invalid:8443/dist/my-pack.lock.json?secret_token=super-secret-12345&internal=true"

	res := app.LoadExtractorPackURL(secretURL)

	// 1. Assert full Locator was passed through to Manager intact without stripping query
	if spy.lastSpec.Kind != extractor.RuntimeSourceKindRemoteLock {
		t.Fatalf("expected remote_lock kind, got %s", spy.lastSpec.Kind)
	}
	if spy.lastSpec.Locator != secretURL {
		t.Fatalf("expected exact locator pass-through %q, got %q", secretURL, spy.lastSpec.Locator)
	}

	// 2. Check error envelope
	if res.Success {
		t.Fatal("expected failure on simulated error")
	}
	if res.Cancelled {
		t.Fatal("expected cancelled: false")
	}
	if res.ErrorCode != string(extractor.RuntimePackLoadErrorRemoteFailed) {
		t.Fatalf("expected error code %s, got %s", extractor.RuntimePackLoadErrorRemoteFailed, res.ErrorCode)
	}

	// 3. Privacy: sensitive URL, query, or raw error not leaked
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, "super-secret-12345") || strings.Contains(jsonStr, "example.invalid") || strings.Contains(jsonStr, "hermetic simulated") {
		t.Fatalf("sensitive URL, query, or raw error leaked into result JSON: %s", jsonStr)
	}
}

func TestReloadAndRemoveExtractorSource(t *testing.T) {
	app, _, picker, _ := newTestAppWithManager(t)
	packDir := createTestDirectoryPack(t)
	picker.dirPath = packDir

	loadRes := app.LoadExtractorPackDirectory()
	if !loadRes.Success || len(loadRes.State.Sources) != 1 {
		t.Fatalf("load failed: %#v", loadRes)
	}
	sourceID := loadRes.State.Sources[0].SourceID

	// Test Reload on valid source
	reloadRes := app.ReloadExtractorSource(sourceID)
	if !reloadRes.Success {
		t.Fatalf("reload failed: %q", reloadRes.ErrorCode)
	}
	if len(reloadRes.State.Sources) != 1 {
		t.Fatalf("expected 1 source after reload, got %d", len(reloadRes.State.Sources))
	}

	// Test Reload on non-existent source
	badReloadRes := app.ReloadExtractorSource("non-existent-source-id")
	if badReloadRes.Success {
		t.Fatal("expected reload of invalid id to fail")
	}
	if badReloadRes.ErrorCode != "invalid_source_id" {
		t.Fatalf("expected invalid_source_id, got %q", badReloadRes.ErrorCode)
	}
	if len(badReloadRes.State.Sources) != 1 {
		t.Fatalf("expected preserved source row on reload failure, got %d", len(badReloadRes.State.Sources))
	}

	// Test Remove on non-existent source
	badRemoveRes := app.RemoveExtractorSource("non-existent-source-id")
	if badRemoveRes.Success {
		t.Fatal("expected remove of invalid id to fail")
	}
	if badRemoveRes.ErrorCode != "invalid_source_id" {
		t.Fatalf("expected invalid_source_id, got %q", badRemoveRes.ErrorCode)
	}
	if len(badRemoveRes.State.Sources) != 1 {
		t.Fatalf("expected preserved source row on remove failure, got %d", len(badRemoveRes.State.Sources))
	}

	// Test Remove on valid source
	removeRes := app.RemoveExtractorSource(sourceID)
	if !removeRes.Success {
		t.Fatalf("remove failed: %q", removeRes.ErrorCode)
	}
	if len(removeRes.State.Sources) != 0 {
		t.Fatalf("expected 0 sources after remove, got %d", len(removeRes.State.Sources))
	}
}

func TestMapExtractorErrorKnownAndFallback(t *testing.T) {
	tests := []struct {
		err          error
		expectedCode string
		cancelled    bool
	}{
		{
			err:          nil,
			expectedCode: "",
			cancelled:    false,
		},
		{
			err:          context.Canceled,
			expectedCode: ExtractorErrorCodeCancelled,
			cancelled:    true,
		},
		{
			err:          errors.New("arbitrary internal unexpected error"),
			expectedCode: ExtractorErrorCodeUnavailable,
			cancelled:    false,
		},
	}

	for _, tt := range tests {
		code, cancelled := mapExtractorError(tt.err)
		if code != tt.expectedCode {
			t.Errorf("for %v, expected code %q, got %q", tt.err, tt.expectedCode, code)
		}
		if cancelled != tt.cancelled {
			t.Errorf("for %v, expected cancelled %v, got %v", tt.err, tt.cancelled, cancelled)
		}
	}
}

func TestWailsMutationFailureDoesNotInvokeReplacement(t *testing.T) {
	app, mgr, picker, _ := newTestAppWithManager(t)

	store := extension.NewSecretStore()
	secret := store.GenerateSecret()
	store.SetSecret(secret)

	mockRes := &testReadyResolver{}
	srv := extension.NewServer(nil, nil, store)
	srv.SetLinkage(extension.Linkage{Resolver: mockRes})
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	app.extensionServer = srv

	// 1. Picker cancel
	picker.fileCancel = true
	cancelRes := app.LoadExtractorPackFile()
	if cancelRes.Success || !cancelRes.Cancelled {
		t.Fatalf("expected cancel, got %#v", cancelRes)
	}

	// 2. Failed Reload
	reloadRes := app.ReloadExtractorSource("non-existent-source-id")
	if reloadRes.Success {
		t.Fatal("expected failure on invalid source reload")
	}

	// 3. Failed URL load (hermetic)
	spy := &spyExtractorSourceManager{
		extractorSourceManager: mgr,
		loadSourceFn: func(ctx context.Context, spec extractor.RuntimeSourceSpec) (extractor.RuntimeSourceState, error) {
			return extractor.RuntimeSourceState{}, &extractor.RuntimePackLoadError{
				Code: extractor.RuntimePackLoadErrorRemoteFailed,
			}
		},
	}
	app.taggedRuntime().manager = spy

	urlRes := app.LoadExtractorPackURL("https://invalid.test/not-found.lock.json")
	if urlRes.Success {
		t.Fatal("expected failure on invalid URL load")
	}

	// Prove server linkage was not modified or replaced
	ack := dialAuthAck(t, srv.GetStatus().WSPort, secret)
	if !hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("expected resolver cap preserved after mutation failures, got %v", ack.Capabilities)
	}
}

func TestTaggedExtractorRuntime_TypedNilSafety(t *testing.T) {
	var typedNilMgr *extractor.ExtractorRuntimeManager
	rt := newTaggedExtractorRuntime(typedNilMgr)
	if rt.manager != nil {
		t.Fatal("expected manager interface to be normalized to nil for typed nil pointer")
	}
	if rt.hasManager() {
		t.Fatal("expected hasManager to return false")
	}
	if rt.currentTasksAdapter() != nil {
		t.Fatal("expected nil tasks adapter")
	}

	app := NewApp(Options{})
	app.setExtractorRuntime(rt)

	state := app.GetExtractorState()
	if state.Available {
		t.Fatal("expected Available == false")
	}

	res := app.LoadExtractorPackFile()
	if res.Success || res.ErrorCode != ExtractorErrorCodeUnavailable {
		t.Fatalf("expected unavailable result, got %#v", res)
	}

	res = app.LoadExtractorPackDirectory()
	if res.Success || res.ErrorCode != ExtractorErrorCodeUnavailable {
		t.Fatalf("expected unavailable result, got %#v", res)
	}

	res = app.LoadExtractorPackURL("https://example.com")
	if res.Success || res.ErrorCode != ExtractorErrorCodeUnavailable {
		t.Fatalf("expected unavailable result, got %#v", res)
	}

	res = app.ReloadExtractorSource("s1")
	if res.Success || res.ErrorCode != ExtractorErrorCodeUnavailable {
		t.Fatalf("expected unavailable result, got %#v", res)
	}

	res = app.RemoveExtractorSource("s1")
	if res.Success || res.ErrorCode != ExtractorErrorCodeUnavailable {
		t.Fatalf("expected unavailable result, got %#v", res)
	}
}
