//go:build extractor

package wailsapp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
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
	app, _, _, _ := newTestAppWithManager(t)

	// A private query URL
	secretURL := "https://example.invalid:8443/dist/my-pack.lock.json?secret_token=super-secret-12345&internal=true"

	res := app.LoadExtractorPackURL(secretURL)
	// Even though the network request fails (or host is invalid), check error envelope and privacy
	if res.Success {
		t.Fatal("expected failure on unresolvable domain")
	}
	if res.Cancelled {
		t.Fatal("expected cancelled: false")
	}
	if res.ErrorCode == "" {
		t.Fatal("expected non-empty error code")
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)
	if strings.Contains(jsonStr, "super-secret-12345") || strings.Contains(jsonStr, "example.invalid") {
		t.Fatalf("sensitive URL or query leaked into result JSON: %s", jsonStr)
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
	app, _, picker, _ := newTestAppWithManager(t)

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

	// 3. Failed URL load
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
