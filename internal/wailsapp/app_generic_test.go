//go:build !extractor

package wailsapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
