//go:build !extractor

package wailsapp

import (
	"net/http"
	"net/http/httptest"
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
