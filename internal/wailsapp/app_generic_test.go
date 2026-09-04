//go:build !extractor

package wailsapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goaria-v3/internal/extension"
)

func TestGenericNewAppNotNil(t *testing.T) {
	app := NewApp(Options{})
	if app == nil {
		t.Fatal("NewApp returned nil")
	}
}

func TestGenericConfigureEmbeddedExtractorDispatcherNoop(t *testing.T) {
	app := NewApp(Options{})
	if err := ConfigureEmbeddedExtractorDispatcher(app); err != nil {
		t.Fatalf("ConfigureEmbeddedExtractorDispatcher returned error: %v", err)
	}
}

func TestGenericConfigureExtensionLinkageAttachesDirect(t *testing.T) {
	app := NewApp(Options{})
	ConfigureExtensionLinkage(app, nil)
	ConfigureExtensionLinkage(nil, nil)

	store := extension.NewSecretStore()
	store.SetSecret("generic-fixture-secret")
	srv := extension.NewServer(nil, nil, store)
	ConfigureExtensionLinkage(app, srv)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ack := dialGenericExtensionAuth(t, srv)
	if !hasGenericCapability(ack.Capabilities, extension.CapDownloadBatch) {
		t.Fatalf("generic configure must grant download.batch, got %v", ack.Capabilities)
	}
	if hasGenericCapability(ack.Capabilities, extension.CapExtractorResolve) ||
		hasGenericCapability(ack.Capabilities, extension.CapExtractorBatch) {
		t.Fatalf("generic configure must omit extractor caps, got %v", ack.Capabilities)
	}
}

func dialGenericExtensionAuth(t *testing.T, srv *extension.Server) extension.AuthAck {
	t.Helper()
	conn := dialGenericExtension(t, srv.GetStatus().WSPort)
	defer conn.Close()
	writeGenericJSON(t, conn, extension.AuthMessage{
		Type:   extension.MsgTypeAuth,
		Secret: "generic-fixture-secret",
	})
	var ack extension.AuthAck
	readGenericJSON(t, conn, &ack)
	return ack
}

func TestGenericHostAuthCallbackMiddlewarePassthrough(t *testing.T) {
	app := NewApp(Options{})
	middleware := HostAuthCallbackMiddleware(app)
	if middleware == nil {
		t.Fatal("HostAuthCallbackMiddleware returned nil")
	}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	rec := httptest.NewRecorder()
	middleware(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Fatal("middleware did not pass through to next handler")
	}
}

func TestGenericHostAuthRawMessageHandlerNoPanic(t *testing.T) {
	HostAuthRawMessageHandler(nil, "test", nil)
}

func TestGenericTaskServiceNilAdapter(t *testing.T) {
	app := NewApp(Options{})
	svc := app.taskService()
	if svc == nil {
		t.Fatal("taskService returned nil")
	}
	if svc.Adapter != nil {
		t.Fatalf("expected nil Adapter in generic variant, got %v", svc.Adapter)
	}
}

func TestGenericExtractorStateEmptyAndUnavailable(t *testing.T) {
	app := NewApp(Options{})
	state := app.GetExtractorState()

	if state.Available {
		t.Fatalf("expected available: false in generic build, got %v", state.Available)
	}
	if state.Sources == nil || len(state.Sources) != 0 {
		t.Fatalf("expected non-nil empty sources array, got %#v", state.Sources)
	}
	if state.RecoveryErrors == nil || len(state.RecoveryErrors) != 0 {
		t.Fatalf("expected non-nil empty recovery_errors array, got %#v", state.RecoveryErrors)
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	jsonStr := string(data)
	if !strings.Contains(jsonStr, `"available":false`) {
		t.Fatalf("expected available:false in JSON, got %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"sources":[]`) {
		t.Fatalf("expected sources:[] in JSON, got %s", jsonStr)
	}
	if !strings.Contains(jsonStr, `"recovery_errors":[]`) {
		t.Fatalf("expected recovery_errors:[] in JSON, got %s", jsonStr)
	}
}

func TestGenericExtractorMutationsReturnUnavailable(t *testing.T) {
	app := NewApp(Options{})

	mutations := []struct {
		name string
		call func() ExtractorOperationResult
	}{
		{
			name: "LoadExtractorPackFile",
			call: func() ExtractorOperationResult {
				return app.LoadExtractorPackFile()
			},
		},
		{
			name: "LoadExtractorPackDirectory",
			call: func() ExtractorOperationResult {
				return app.LoadExtractorPackDirectory()
			},
		},
		{
			name: "LoadExtractorPackURL",
			call: func() ExtractorOperationResult {
				return app.LoadExtractorPackURL("https://example.com/lock.json")
			},
		},
		{
			name: "ReloadExtractorSource",
			call: func() ExtractorOperationResult {
				return app.ReloadExtractorSource("opaque-source-1")
			},
		},
		{
			name: "RemoveExtractorSource",
			call: func() ExtractorOperationResult {
				return app.RemoveExtractorSource("opaque-source-1")
			},
		},
	}

	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.call()
			if res.Success {
				t.Fatalf("expected success: false in generic, got %v", res.Success)
			}
			if res.Cancelled {
				t.Fatalf("expected cancelled: false in generic, got %v", res.Cancelled)
			}
			if res.ErrorCode != ExtractorErrorCodeUnavailable {
				t.Fatalf("expected error_code: unavailable, got %q", res.ErrorCode)
			}
			if res.State.Available {
				t.Fatalf("expected state.available: false, got %v", res.State.Available)
			}
			if res.State.Sources == nil || len(res.State.Sources) != 0 {
				t.Fatalf("expected non-nil empty state.sources, got %#v", res.State.Sources)
			}
			if res.State.RecoveryErrors == nil || len(res.State.RecoveryErrors) != 0 {
				t.Fatalf("expected non-nil empty state.recovery_errors, got %#v", res.State.RecoveryErrors)
			}

			data, err := json.Marshal(res)
			if err != nil {
				t.Fatalf("marshal result: %v", err)
			}
			jsonStr := string(data)
			if !strings.Contains(jsonStr, `"success":false`) {
				t.Fatalf("expected success:false in JSON, got %s", jsonStr)
			}
			if !strings.Contains(jsonStr, `"cancelled":false`) {
				t.Fatalf("expected cancelled:false in JSON, got %s", jsonStr)
			}
			if !strings.Contains(jsonStr, `"error_code":"unavailable"`) {
				t.Fatalf("expected error_code:unavailable in JSON, got %s", jsonStr)
			}
			if !strings.Contains(jsonStr, `"state":{`) {
				t.Fatalf("expected state object in JSON, got %s", jsonStr)
			}
		})
	}
}

func TestExtractorDTOJSONFieldNames(t *testing.T) {
	source := ExtractorSource{
		SourceID:          "src-1",
		Kind:              ExtractorSourceKindLocalZip,
		DisplayName:       "Test Pack",
		PackID:            "pack.test",
		PackVersion:       "1.0.0",
		SignerFingerprint: "0123456789abcdef",
		Status:            ExtractorSourceStatusReady,
		ErrorCode:         "err_code",
	}
	sourceData, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	sourceJSON := string(sourceData)
	for _, key := range []string{
		`"source_id":`,
		`"kind":`,
		`"display_name":`,
		`"pack_id":`,
		`"pack_version":`,
		`"signer_fingerprint":`,
		`"status":`,
		`"error_code":`,
	} {
		if !strings.Contains(sourceJSON, key) {
			t.Fatalf("expected key %s in source JSON, got %s", key, sourceJSON)
		}
	}
}
