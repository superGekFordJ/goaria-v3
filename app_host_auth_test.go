package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"goaria-v3/internal/config"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeHostAuthSessionWindowFactory struct {
	mu        sync.Mutex
	requests  []appHostAuthSessionRequest
	callbacks []appHostAuthSessionCallbacks
	openErr   error
	openNil   bool
	windows   []*fakeHostAuthSessionWindow
}

type fakeHostAuthSessionWindow struct {
	mu         sync.Mutex
	closeCount int
	closeErr   error
}

type recordingAuthWebViewSink struct {
	mu       sync.Mutex
	success  []extractor.AuthWebViewToken
	cancel   int
	errors   []error
	terminal chan struct{}
	once     sync.Once
}

type recordingAppHostAuthDiagnosticObserver struct {
	mu     sync.Mutex
	events []appHostAuthDiagnosticEvent
}

type appHostAuthOutcome struct {
	result extractor.WebViewAuthResult
	err    error
}

type fakeExtractorDispatcher struct{}

type fakeHostPolicyResolverForAppAuth struct{}

type fakeNoopAuthWebViewDriver struct{}

type appHostAuthAutoSuccessDriver struct {
	mu    sync.Mutex
	opens int
}

type appHostAuthAliasResolver struct {
	mu       sync.Mutex
	identity extractor.VerifiedPackIdentity
	calls    int
}

var rootWailsTestAppMu sync.Mutex

func TestAppHostAuthDriverAllowsOneInflightSession(t *testing.T) {
	app := NewApp()
	app.SetApp(newRootWailsTestApp(t))
	app.SetWindow(&application.WebviewWindow{})
	factory := &fakeHostAuthSessionWindowFactory{}
	driver := newAppHostAuthDriverWithFactory(app, factory)

	first, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), newRecordingAuthWebViewSink().sink())
	if err != nil {
		t.Fatalf("OpenAuthSession() first error = %v", err)
	}
	if factory.openCount() != 1 {
		t.Fatalf("factory open count = %d, want 1", factory.openCount())
	}

	second, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), newRecordingAuthWebViewSink().sink())
	if err == nil {
		t.Fatalf("OpenAuthSession() second error = nil, session=%#v", second)
	}
	if err.Error() != appHostAuthInProgressMessage {
		t.Fatalf("second error = %q, want generic in-progress", err.Error())
	}
	if factory.openCount() != 1 {
		t.Fatalf("factory open count after second = %d, want 1", factory.openCount())
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first.Close() error = %v", err)
	}
	third, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), newRecordingAuthWebViewSink().sink())
	if err != nil {
		t.Fatalf("OpenAuthSession() after close error = %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("third.Close() error = %v", err)
	}
	if factory.openCount() != 2 {
		t.Fatalf("factory open count after reopen = %d, want 2", factory.openCount())
	}
}

func TestAppHostAuthDriverReportsCallbackSuccessCancelTimeoutInvalidPayload(t *testing.T) {
	t.Run("success stores through coordinator", func(t *testing.T) {
		store := newRootTempAuthProfileStore(t)
		factory := &fakeHostAuthSessionWindowFactory{}
		driver := newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory)
		coordinator := extractor.NewWebViewAuthCoordinator(store, driver)
		resultCh := make(chan appHostAuthOutcome, 1)

		go func() {
			result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
			resultCh <- appHostAuthOutcome{result: result, err: err}
		}()
		factory.waitForOpen(t)
		factory.callback(0).Success(appHostAuthSessionPayload{Kind: extractor.AuthSecretKindBearer, Secret: "captured-token-secret", RedactedDisplay: "captured bearer"})

		outcome := receiveAppHostAuthOutcome(t, resultCh)
		if outcome.err != nil {
			t.Fatalf("coordinator.Start() error = %v", outcome.err)
		}
		if outcome.result.Status != extractor.WebViewAuthStatusSuccess || !outcome.result.Snapshot.HasSecret {
			t.Fatalf("coordinator.Start() result = %#v", outcome.result)
		}
		resolved, err := store.ResolveAuthProfile(context.Background(), "xpk-alpha001", "apr-alpha001", "https://fixture.invalid/item")
		if err != nil {
			t.Fatalf("ResolveAuthProfile() error = %v", err)
		}
		if resolved.HeaderValue != "Bearer captured-token-secret" {
			t.Fatalf("HeaderValue = %q, want captured bearer", resolved.HeaderValue)
		}
	})

	t.Run("cancel stores no profile", func(t *testing.T) {
		store := newRootTempAuthProfileStore(t)
		factory := &fakeHostAuthSessionWindowFactory{}
		coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory))
		resultCh := make(chan appHostAuthOutcome, 1)

		go func() {
			result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
			resultCh <- appHostAuthOutcome{result: result, err: err}
		}()
		factory.waitForOpen(t)
		factory.callback(0).Cancel()

		outcome := receiveAppHostAuthOutcome(t, resultCh)
		if outcome.err != nil {
			t.Fatalf("coordinator.Start() cancel error = %v", outcome.err)
		}
		if outcome.result.Status != extractor.WebViewAuthStatusCanceled {
			t.Fatalf("cancel status = %q, want canceled", outcome.result.Status)
		}
		assertNoRootAuthProfile(t, store)
	})

	t.Run("error is generic", func(t *testing.T) {
		factory := &fakeHostAuthSessionWindowFactory{}
		driver := newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory)
		recorder := newRecordingAuthWebViewSink()
		_, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), recorder.sink())
		if err != nil {
			t.Fatalf("OpenAuthSession() error = %v", err)
		}
		factory.callback(0).Error(errors.New("Authorization: Bearer callback-secret token=raw-token"))
		recorder.wait(t)

		got := recorder.errorString()
		if !strings.Contains(got, appHostAuthCallbackErrorMessage) || strings.Contains(got, "callback-secret") || strings.Contains(got, "raw-token") || strings.Contains(got, "Authorization") {
			t.Fatalf("callback error was not sanitized: %q", got)
		}
	})

	t.Run("timeout stores no profile", func(t *testing.T) {
		store := newRootTempAuthProfileStore(t)
		factory := &fakeHostAuthSessionWindowFactory{}
		coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory))
		result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(10*time.Millisecond))
		if err != nil {
			t.Fatalf("coordinator.Start() timeout error = %v", err)
		}
		if result.Status != extractor.WebViewAuthStatusTimeout {
			t.Fatalf("timeout status = %q, want timeout", result.Status)
		}
		assertNoRootAuthProfile(t, store)
	})

	t.Run("invalid payloads fail closed", func(t *testing.T) {
		cases := []struct {
			name    string
			payload appHostAuthSessionPayload
		}{
			{name: "unsupported kind", payload: appHostAuthSessionPayload{Kind: extractor.AuthSecretKind("basic"), Secret: "captured-token-secret"}},
			{name: "kind mismatch", payload: appHostAuthSessionPayload{Kind: extractor.AuthSecretKindCookie, Secret: "a=b"}},
			{name: "empty secret", payload: appHostAuthSessionPayload{Kind: extractor.AuthSecretKindBearer, Secret: ""}},
			{name: "crlf secret", payload: appHostAuthSessionPayload{Kind: extractor.AuthSecretKindBearer, Secret: "bad\r\nsecret"}},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				store := newRootTempAuthProfileStore(t)
				factory := &fakeHostAuthSessionWindowFactory{}
				coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory))
				resultCh := make(chan appHostAuthOutcome, 1)

				go func() {
					result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
					resultCh <- appHostAuthOutcome{result: result, err: err}
				}()
				factory.waitForOpen(t)
				factory.callback(0).Success(tt.payload)

				outcome := receiveAppHostAuthOutcome(t, resultCh)
				if outcome.err == nil {
					t.Fatalf("coordinator.Start() error = nil, result=%#v", outcome.result)
				}
				if strings.Contains(outcome.err.Error(), tt.payload.Secret) && tt.payload.Secret != "" {
					t.Fatalf("invalid payload error leaked secret: %v", outcome.err)
				}
				assertNoRootAuthProfile(t, store)
			})
		}
	})
}

func TestAppHostAuthDiagnosticDisabledByDefaultPreservesSuccessStore(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticsEnv, "")
	t.Setenv(appHostAuthDiagnosticsOutEnv, "")
	store := newRootTempAuthProfileStore(t)
	factory := &fakeHostAuthSessionWindowFactory{}
	driver := newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory)
	if driver.diagnostics != nil {
		t.Fatalf("default diagnostics = %#v, want nil", driver.diagnostics)
	}
	coordinator := extractor.NewWebViewAuthCoordinator(store, driver)
	resultCh := make(chan appHostAuthOutcome, 1)

	go func() {
		result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
		resultCh <- appHostAuthOutcome{result: result, err: err}
	}()
	factory.waitForOpen(t)
	factory.callback(0).Success(appHostAuthSessionPayload{Kind: extractor.AuthSecretKindBearer, Secret: "captured-token-secret", RedactedDisplay: "captured bearer"})

	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err != nil {
		t.Fatalf("coordinator.Start() error = %v", outcome.err)
	}
	if outcome.result.Status != extractor.WebViewAuthStatusSuccess || !outcome.result.Snapshot.HasSecret {
		t.Fatalf("coordinator.Start() result = %#v", outcome.result)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), "xpk-alpha001", "apr-alpha001", "https://fixture.invalid/item")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderValue != "Bearer captured-token-secret" {
		t.Fatalf("HeaderValue = %q, want captured bearer", resolved.HeaderValue)
	}
}

func TestAppHostAuthDiagnosticDropsUnknownCategoryAndEncodesCategoryOnly(t *testing.T) {
	diagnostics := &recordingAppHostAuthDiagnosticObserver{}
	driver := &appHostAuthDriver{diagnostics: diagnostics}
	driver.observe(appHostAuthDiagnosticPostAccepted)
	driver.observe("not_allowed")

	events := diagnostics.eventsSnapshot()
	if len(events) != 1 || events[0].Category != appHostAuthDiagnosticPostAccepted {
		t.Fatalf("diagnostic events = %#v, want only allowed category", events)
	}
	assertAppHostAuthDiagnosticEventsCategoryOnly(t, events)

	var output bytes.Buffer
	jsonl := &appHostAuthJSONLDiagnosticObserver{writer: &output}
	jsonl.observeAppHostAuthDiagnostic(appHostAuthDiagnosticEvent{Category: "not_allowed"})
	jsonl.observeAppHostAuthDiagnostic(appHostAuthDiagnosticEvent{Category: appHostAuthDiagnosticSessionClosed})

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("JSONL lines = %#v, want only one allowed diagnostic", lines)
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("diagnostic line is not JSON: %v", err)
	}
	if len(decoded) != 1 || decoded["category"] != appHostAuthDiagnosticSessionClosed {
		t.Fatalf("diagnostic event = %#v, want category-only close event", decoded)
	}
	assertRootNoSecretText(t, output.String(), "not_allowed", "fixture.invalid", "raw-token", "Authorization", "Cookie")
}

func TestAppHostAuthDiagnosticTerminalCategoriesOnceAndCategoryOnly(t *testing.T) {
	cases := []struct {
		name      string
		trigger   func(appHostAuthSessionCallbacks)
		want      string
		wantError bool
	}{
		{
			name: "success",
			trigger: func(callbacks appHostAuthSessionCallbacks) {
				callbacks.Success(appHostAuthSessionPayload{Kind: extractor.AuthSecretKindBearer, Secret: "captured-token-secret", RedactedDisplay: "captured bearer"})
				callbacks.Cancel()
				callbacks.Error(errors.New("Authorization: Bearer raw-token"))
			},
			want: appHostAuthDiagnosticTerminalSuccess,
		},
		{
			name: "cancel",
			trigger: func(callbacks appHostAuthSessionCallbacks) {
				callbacks.Cancel()
				callbacks.Success(appHostAuthSessionPayload{Kind: extractor.AuthSecretKindBearer, Secret: "late-token"})
				callbacks.Error(errors.New("Cookie: sid=raw-cookie"))
			},
			want: appHostAuthDiagnosticTerminalCancel,
		},
		{
			name: "error",
			trigger: func(callbacks appHostAuthSessionCallbacks) {
				callbacks.Error(errors.New("Authorization: Bearer raw-token"))
				callbacks.Cancel()
				callbacks.Success(appHostAuthSessionPayload{Kind: extractor.AuthSecretKindBearer, Secret: "late-token"})
			},
			want:      appHostAuthDiagnosticTerminalError,
			wantError: true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			factory := &fakeHostAuthSessionWindowFactory{}
			diagnostics := &recordingAppHostAuthDiagnosticObserver{}
			driver := newAppHostAuthDriverWithFactoryAndDiagnostics(newWindowedAuthApp(t), factory, diagnostics)
			recorder := newRecordingAuthWebViewSink()
			session, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), recorder.sink())
			if err != nil {
				t.Fatalf("OpenAuthSession() error = %v", err)
			}
			callbacks := factory.callback(0)
			tt.trigger(callbacks)
			recorder.wait(t)
			if err := session.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}

			categories := diagnostics.categories()
			if got := countString(categories, tt.want); got != 1 {
				t.Fatalf("category %q count = %d, events=%#v", tt.want, got, categories)
			}
			for _, other := range []string{appHostAuthDiagnosticTerminalSuccess, appHostAuthDiagnosticTerminalCancel, appHostAuthDiagnosticTerminalError} {
				if other == tt.want {
					continue
				}
				if got := countString(categories, other); got != 0 {
					t.Fatalf("unexpected terminal category %q count = %d, events=%#v", other, got, categories)
				}
			}
			assertAppHostAuthDiagnosticEventsCategoryOnly(t, diagnostics.eventsSnapshot())
			encoded := diagnostics.encodedEvents(t)
			assertRootNoSecretText(t, encoded, "captured-token-secret", "late-token", "raw-token", "raw-cookie", "Authorization", "Cookie", "fixture.invalid", "apr-alpha001", "xpk-alpha001")
			if tt.wantError && !strings.Contains(recorder.errorString(), appHostAuthCallbackErrorMessage) {
				t.Fatalf("error callback = %q, want sanitized callback error", recorder.errorString())
			}
		})
	}
}

func TestAppHostAuthDiagnosticEnvOutputJSONLCategoryOnly(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "diagnostics.jsonl")
	t.Setenv(appHostAuthDiagnosticsEnv, "categories")
	t.Setenv(appHostAuthDiagnosticsOutEnv, outPath)
	factory := &fakeHostAuthSessionWindowFactory{}
	driver := newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory)
	recorder := newRecordingAuthWebViewSink()
	session, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), recorder.sink())
	if err != nil {
		t.Fatalf("OpenAuthSession() error = %v", err)
	}
	factory.callback(0).Cancel()
	recorder.wait(t)
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		t.Fatal("diagnostic output is empty")
	}
	for _, line := range lines {
		var event map[string]string
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("diagnostic line is not JSON: %v", err)
		}
		if len(event) != 1 || event["category"] == "" {
			t.Fatalf("diagnostic event = %#v, want category-only", event)
		}
		if !appHostAuthDiagnosticCategoryAllowed(event["category"]) {
			t.Fatalf("category %q is not whitelisted", event["category"])
		}
		assertRootNoSecretText(t, line, "fixture.invalid", "apr-alpha001", "xpk-alpha001", "raw-token", "Cookie", "Authorization")
	}
	if !stringSliceContains(diagnosticCategoriesFromJSONLLines(t, lines), appHostAuthDiagnosticSessionClosed) {
		t.Fatalf("diagnostic output missing close category: %q", string(raw))
	}
}

func TestAppHostAuthDriverBuildsGenericSessionRequest(t *testing.T) {
	factory := &fakeHostAuthSessionWindowFactory{}
	driver := newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory)
	request := appHostAuthWebViewRequest(2 * time.Second)
	request.AllowedDomains = []extractor.DomainRule{{Host: "fixture.invalid"}}

	session, err := driver.OpenAuthSession(context.Background(), request, newRecordingAuthWebViewSink().sink())
	if err != nil {
		t.Fatalf("OpenAuthSession() error = %v", err)
	}
	defer func() { _ = session.Close() }()

	got := factory.request(0)
	if got.PackID != request.PackID || got.ProfileID != request.ProfileID || got.Kind != request.Kind || got.LoginURL != request.LoginURL || got.Timeout != request.Timeout {
		t.Fatalf("generic request = %#v, want fields from %#v", got, request)
	}
	if len(got.AllowedDomains) != 1 || got.AllowedDomains[0].Host != "fixture.invalid" {
		t.Fatalf("AllowedDomains = %#v, want fixture.invalid", got.AllowedDomains)
	}
	formatted := fmt.Sprintf("%#v", got)
	for _, forbidden := range []string{"provider", "cookieName", "login step"} {
		if strings.Contains(strings.ToLower(formatted), strings.ToLower(forbidden)) {
			t.Fatalf("generic request contains forbidden marker %q: %s", forbidden, formatted)
		}
	}
	if got.CallbackPath == "" || got.CallbackURL == "" || got.SessionToken == "" || got.CollectorJS == "" || got.CallbackTransport.Mode != "local_post" || len(got.Capture.SecretCandidates) == 0 {
		t.Fatalf("generic callback request metadata missing: %#v", got)
	}
}

func TestAppHostAuthDriverHeadlessOrNoWindowFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name string
		app  *App
	}{
		{name: "nil app", app: nil},
		{name: "wails app nil", app: NewApp()},
		{name: "window nil", app: appWithWailsOnly(t)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			factory := &fakeHostAuthSessionWindowFactory{}
			driver := newAppHostAuthDriverWithFactory(tt.app, factory)
			session, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), newRecordingAuthWebViewSink().sink())
			if err == nil {
				t.Fatalf("OpenAuthSession() error = nil, session=%#v", session)
			}
			if err.Error() != appHostAuthUnavailableMessage {
				t.Fatalf("error = %q, want unavailable", err.Error())
			}
			if factory.openCount() != 0 {
				t.Fatalf("factory opened %d sessions, want 0", factory.openCount())
			}
		})
	}
}

func TestAppHostAuthDriverRedactsWindowAndCallbackErrors(t *testing.T) {
	t.Run("window errors", func(t *testing.T) {
		factory := &fakeHostAuthSessionWindowFactory{openErr: errors.New("open failed token=raw-token Authorization: Bearer raw-secret https://fixture.invalid/login?token=query-secret")}
		driver := newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory)
		_, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), newRecordingAuthWebViewSink().sink())
		if err == nil {
			t.Fatal("OpenAuthSession() error = nil, want window failure")
		}
		assertRootNoSecretText(t, err.Error(), "raw-token", "raw-secret", "query-secret", "Authorization")
	})

	t.Run("callback errors", func(t *testing.T) {
		factory := &fakeHostAuthSessionWindowFactory{}
		driver := newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory)
		recorder := newRecordingAuthWebViewSink()
		_, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), recorder.sink())
		if err != nil {
			t.Fatalf("OpenAuthSession() error = %v", err)
		}
		factory.callback(0).Error(errors.New("Cookie: sid=raw-cookie; token=raw-token https://fixture.invalid/login?token=query-secret"))
		recorder.wait(t)
		assertRootNoSecretText(t, recorder.errorString(), "raw-cookie", "raw-token", "query-secret", "Cookie")
	})
}

func TestConfigureEmbeddedExtractorDispatcherWiresHostAuthRuntimeState(t *testing.T) {
	app := NewApp()
	store := newRootTempAuthProfileStore(t)
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	driver := fakeNoopAuthWebViewDriver{}
	policyResolver := fakeHostPolicyResolverForAppAuth{}
	var captured extractor.EmbeddedReleaseDispatcherConfig

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:     func() bool { return true },
		embeddedReleaseRequired:     func() bool { return false },
		loadHostPolicyResolver:      func() (extractor.HostPolicyResolver, error) { return policyResolver, nil },
		loadAuthRuntimeBundle:       func() (*extractor.PrivateAuthRuntimeBundle, error) { return bundle, nil },
		defaultAuthProfileStorePath: func() (string, error) { return filepath.Join(t.TempDir(), "auth.json"), nil },
		newFileAuthProfileStore:     func(string) (extractor.AuthProfileStore, error) { return store, nil },
		newAuthWebViewDriver:        func(*App) extractor.AuthWebViewDriver { return driver },
		newEmbeddedReleaseAddTaskDispatcher: func(config extractor.EmbeddedReleaseDispatcherConfig) (extractorAddTaskDispatcher, error) {
			captured = config
			return fakeExtractorDispatcher{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if app.authProfileStoreForTest() != store {
		t.Fatalf("App store = %#v, want shared store", app.authProfileStoreForTest())
	}
	runtime := app.hostAuthRuntimeForTest()
	if runtime == nil {
		t.Fatal("App HostAuthRuntime = nil")
	}
	if app.authWebViewDriverForTest() == nil {
		t.Fatal("App auth driver = nil")
	}
	if captured.AuthResolver != runtime {
		t.Fatalf("captured AuthResolver = %#v, want App runtime %#v", captured.AuthResolver, runtime)
	}
	if captured.AuthRuntimeBundle != bundle {
		t.Fatalf("captured AuthRuntimeBundle = %#v, want loaded bundle %#v", captured.AuthRuntimeBundle, bundle)
	}
	if captured.HostPolicyResolver != policyResolver {
		t.Fatalf("captured HostPolicyResolver = %#v, want shared resolver", captured.HostPolicyResolver)
	}
	if app.extractorDispatcher == nil {
		t.Fatal("App extractor dispatcher = nil")
	}
}

func TestConfigureEmbeddedExtractorDispatcherFallsBackToSharedStoreWithoutRuntimeBundle(t *testing.T) {
	app := NewApp()
	store := newRootTempAuthProfileStore(t)
	var captured extractor.EmbeddedReleaseDispatcherConfig

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:     func() bool { return true },
		embeddedReleaseRequired:     func() bool { return false },
		loadHostPolicyResolver:      func() (extractor.HostPolicyResolver, error) { return nil, nil },
		loadAuthRuntimeBundle:       func() (*extractor.PrivateAuthRuntimeBundle, error) { return nil, nil },
		defaultAuthProfileStorePath: func() (string, error) { return filepath.Join(t.TempDir(), "auth.json"), nil },
		newFileAuthProfileStore:     func(string) (extractor.AuthProfileStore, error) { return store, nil },
		newEmbeddedReleaseAddTaskDispatcher: func(config extractor.EmbeddedReleaseDispatcherConfig) (extractorAddTaskDispatcher, error) {
			captured = config
			return fakeExtractorDispatcher{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if app.authProfileStoreForTest() != store {
		t.Fatal("App did not retain shared fallback store")
	}
	if app.hostAuthRuntimeForTest() != nil {
		t.Fatalf("App runtime = %#v, want nil", app.hostAuthRuntimeForTest())
	}
	if captured.AuthResolver != store {
		t.Fatalf("captured AuthResolver = %#v, want fallback store", captured.AuthResolver)
	}
}

func TestConfigureEmbeddedExtractorDispatcherNoPackNoRuntimeIsNoop(t *testing.T) {
	app := NewApp()
	storeCreated := false
	dispatcherCreated := false

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks: func() bool { return false },
		embeddedReleaseRequired: func() bool { return false },
		loadAuthRuntimeBundle:   func() (*extractor.PrivateAuthRuntimeBundle, error) { return nil, nil },
		newFileAuthProfileStore: func(string) (extractor.AuthProfileStore, error) {
			storeCreated = true
			return newRootTempAuthProfileStore(t), nil
		},
		newEmbeddedReleaseAddTaskDispatcher: func(extractor.EmbeddedReleaseDispatcherConfig) (extractorAddTaskDispatcher, error) {
			dispatcherCreated = true
			return fakeExtractorDispatcher{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if storeCreated || dispatcherCreated || app.extractorDispatcher != nil || app.authProfileStoreForTest() != nil || app.hostAuthRuntimeForTest() != nil {
		t.Fatalf("no-op path created state: store=%t dispatcher=%t app=%#v", storeCreated, dispatcherCreated, app)
	}
}

func TestConfigureEmbeddedExtractorDispatcherSanitizesLoaderAndStoreErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		deps embeddedExtractorConfigDeps
	}{
		{
			name: "loader error",
			deps: embeddedExtractorConfigDeps{
				hasEmbeddedReleasePacks: func() bool { return false },
				embeddedReleaseRequired: func() bool { return false },
				loadAuthRuntimeBundle: func() (*extractor.PrivateAuthRuntimeBundle, error) {
					return nil, errors.New("load failed token=raw-token Authorization: Bearer raw-secret")
				},
			},
		},
		{
			name: "store path error",
			deps: embeddedExtractorConfigDeps{
				hasEmbeddedReleasePacks:     func() bool { return true },
				embeddedReleaseRequired:     func() bool { return false },
				loadHostPolicyResolver:      func() (extractor.HostPolicyResolver, error) { return nil, nil },
				loadAuthRuntimeBundle:       func() (*extractor.PrivateAuthRuntimeBundle, error) { return nil, nil },
				defaultAuthProfileStorePath: func() (string, error) { return "", errors.New("C:/private/path?token=raw-token") },
			},
		},
		{
			name: "store error",
			deps: embeddedExtractorConfigDeps{
				hasEmbeddedReleasePacks:     func() bool { return true },
				embeddedReleaseRequired:     func() bool { return false },
				loadHostPolicyResolver:      func() (extractor.HostPolicyResolver, error) { return nil, nil },
				loadAuthRuntimeBundle:       func() (*extractor.PrivateAuthRuntimeBundle, error) { return nil, nil },
				defaultAuthProfileStorePath: func() (string, error) { return filepath.Join(t.TempDir(), "auth.json"), nil },
				newFileAuthProfileStore:     func(string) (extractor.AuthProfileStore, error) { return nil, errors.New("Cookie: sid=raw-cookie") },
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := configureEmbeddedExtractorDispatcherWithDeps(NewApp(), tt.deps)
			if err == nil {
				t.Fatal("configure helper error = nil, want sanitized failure")
			}
			assertRootNoSecretText(t, err.Error(), "raw-token", "raw-secret", "raw-cookie", "Authorization", "Cookie")
		})
	}
}

func TestConfigureEmbeddedExtractorDispatcherSharesHostPolicyResolver(t *testing.T) {
	app := NewApp()
	store := newRootTempAuthProfileStore(t)
	policyResolver := fakeHostPolicyResolverForAppAuth{}
	var captured extractor.EmbeddedReleaseDispatcherConfig

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:     func() bool { return true },
		embeddedReleaseRequired:     func() bool { return true },
		loadHostPolicyResolver:      func() (extractor.HostPolicyResolver, error) { return policyResolver, nil },
		loadAuthRuntimeBundle:       func() (*extractor.PrivateAuthRuntimeBundle, error) { return nil, nil },
		defaultAuthProfileStorePath: func() (string, error) { return filepath.Join(t.TempDir(), "auth.json"), nil },
		newFileAuthProfileStore:     func(string) (extractor.AuthProfileStore, error) { return store, nil },
		newEmbeddedReleaseAddTaskDispatcher: func(config extractor.EmbeddedReleaseDispatcherConfig) (extractorAddTaskDispatcher, error) {
			captured = config
			return fakeExtractorDispatcher{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if captured.HostPolicyResolver != policyResolver {
		t.Fatalf("captured HostPolicyResolver = %#v, want policy resolver", captured.HostPolicyResolver)
	}
}

func TestConfigureEmbeddedExtractorDispatcherPassesHostPolicyResolverToRuntime(t *testing.T) {
	app := NewApp()
	store := newRootTempAuthProfileStore(t)
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	driver := &appHostAuthAutoSuccessDriver{}
	resolver := &appHostAuthAliasResolver{identity: identity}
	manifest := appHostAuthAliasManifest(identity)
	runtime := extractor.NewHostAuthRuntime(extractor.HostAuthRuntimeConfig{
		Bundle:             bundle,
		Store:              store,
		Coordinator:        extractor.NewWebViewAuthCoordinator(store, driver),
		HostPolicyResolver: resolver,
	})
	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:     func() bool { return true },
		embeddedReleaseRequired:     func() bool { return false },
		loadHostPolicyResolver:      func() (extractor.HostPolicyResolver, error) { return resolver, nil },
		loadAuthRuntimeBundle:       func() (*extractor.PrivateAuthRuntimeBundle, error) { return bundle, nil },
		defaultAuthProfileStorePath: func() (string, error) { return filepath.Join(t.TempDir(), "auth.json"), nil },
		newFileAuthProfileStore:     func(string) (extractor.AuthProfileStore, error) { return store, nil },
		newAuthWebViewDriver:        func(*App) extractor.AuthWebViewDriver { return driver },
		newEmbeddedReleaseAddTaskDispatcher: func(extractor.EmbeddedReleaseDispatcherConfig) (extractorAddTaskDispatcher, error) {
			return fakeExtractorDispatcher{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if app.hostAuthRuntimeForTest() == nil {
		t.Fatal("App HostAuthRuntime = nil")
	}
	result, err := runtime.Provision(context.Background(), extractor.HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://fixture.invalid/file",
		ProfileRef:   "apr-alpha001",
	})
	if err != nil {
		t.Fatalf("runtime.Provision(alias) error = %v", err)
	}
	if !result.Provisioned || !result.Available {
		t.Fatalf("runtime.Provision(alias) = %#v, want provisioned available", result)
	}
	if resolver.callCount() == 0 || driver.openCount() == 0 {
		t.Fatalf("resolver calls=%d driver opens=%d, want both used", resolver.callCount(), driver.openCount())
	}
}

func (f *fakeHostAuthSessionWindowFactory) OpenHostAuthSession(_ context.Context, request appHostAuthSessionRequest, callbacks appHostAuthSessionCallbacks) (hostAuthSessionWindow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.requests = append(f.requests, request)
	f.callbacks = append(f.callbacks, callbacks)
	if f.openNil {
		return nil, nil
	}
	window := &fakeHostAuthSessionWindow{}
	f.windows = append(f.windows, window)

	return window, nil
}

func (f *fakeHostAuthSessionWindowFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.requests)
}

func (f *fakeHostAuthSessionWindowFactory) waitForOpen(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f.openCount() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("factory did not open auth session")
}

func (f *fakeHostAuthSessionWindowFactory) callback(index int) appHostAuthSessionCallbacks {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.callbacks[index]
}

func (f *fakeHostAuthSessionWindowFactory) request(index int) appHostAuthSessionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.requests[index]
}

func (w *fakeHostAuthSessionWindow) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeCount++

	return w.closeErr
}

func newRecordingAuthWebViewSink() *recordingAuthWebViewSink {
	return &recordingAuthWebViewSink{terminal: make(chan struct{})}
}

func (s *recordingAuthWebViewSink) sink() extractor.AuthWebViewSink {
	return extractor.AuthWebViewSink{
		OnSuccess: func(token extractor.AuthWebViewToken) {
			s.mu.Lock()
			s.success = append(s.success, token)
			s.mu.Unlock()
			s.signal()
		},
		OnCancel: func() {
			s.mu.Lock()
			s.cancel++
			s.mu.Unlock()
			s.signal()
		},
		OnError: func(err error) {
			s.mu.Lock()
			s.errors = append(s.errors, err)
			s.mu.Unlock()
			s.signal()
		},
	}
}

func (s *recordingAuthWebViewSink) signal() {
	s.once.Do(func() { close(s.terminal) })
}

func (s *recordingAuthWebViewSink) wait(t *testing.T) {
	t.Helper()
	select {
	case <-s.terminal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for sink terminal callback")
	}
}

func (s *recordingAuthWebViewSink) errorString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.errors) == 0 || s.errors[0] == nil {
		return ""
	}

	return s.errors[0].Error()
}

func newWindowedAuthApp(t *testing.T) *App {
	t.Helper()
	app := appWithWailsOnly(t)
	app.SetWindow(&application.WebviewWindow{})

	return app
}

func appWithWailsOnly(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	app.SetApp(newRootWailsTestApp(t))

	return app
}

func newRootWailsTestApp(t *testing.T) *application.App {
	t.Helper()
	rootWailsTestAppMu.Lock()
	t.Cleanup(rootWailsTestAppMu.Unlock)

	return application.New(application.Options{Transport: noopWailsTransport{}})
}

func appHostAuthWebViewRequest(timeout time.Duration) extractor.WebViewAuthRequest {
	manifest := extractor.Manifest{
		PackID:       "xpk-alpha001",
		PackVersion:  "opaque-1",
		ABIVersion:   extractor.CurrentABIVersion,
		Capabilities: []extractor.Capability{extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile},
		Domains:      []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
		ResourceLimits: extractor.ResourceLimits{
			TimeoutMillis:    int(timeout / time.Millisecond),
			MaxMemoryPages:   64,
			MaxHostCalls:     16,
			MaxResponseBytes: 1 << 20,
			MaxOutputItems:   16,
			MaxOutputBytes:   1 << 16,
		},
		PayloadSHA256: strings.Repeat("a", 64),
	}

	return extractor.WebViewAuthRequest{
		PackID:         "xpk-alpha001",
		Manifest:       manifest,
		ProfileID:      "apr-alpha001",
		LoginURL:       "https://fixture.invalid/login",
		AllowedDomains: []extractor.DomainRule{{Host: "fixture.invalid"}},
		Timeout:        timeout,
		Kind:           extractor.AuthSecretKindBearer,
		CallbackTransport: extractor.WebViewAuthCallbackTransport{
			Mode:         "local_post",
			ContentTypes: []string{"application/json"},
			MaxBodyBytes: 16384,
		},
		CollectorJS: "(() => { return function(ctx, postCapture) { return ctx && postCapture; }; })();",
		Capture: extractor.WebViewAuthCaptureContract{
			Format:               "json",
			SecretCandidates:     []string{"secret", "capture.secret"},
			KindField:            "kind",
			ExpiresAtField:       "expires_at",
			RedactedDisplayField: "redacted_display",
			TrimSpace:            true,
			RejectCRLF:           true,
		},
	}
}

func newRootTempAuthProfileStore(t *testing.T) *extractor.FileAuthProfileStore {
	t.Helper()
	store, err := extractor.NewFileAuthProfileStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatalf("NewFileAuthProfileStore() error = %v", err)
	}

	return store
}

func assertNoRootAuthProfile(t *testing.T, store extractor.AuthProfileStore) {
	t.Helper()
	if _, err := store.ResolveAuthProfile(context.Background(), "xpk-alpha001", "apr-alpha001", "https://fixture.invalid/item"); err == nil {
		t.Fatal("ResolveAuthProfile() error = nil, want no stored auth profile")
	}
}

func receiveAppHostAuthOutcome(t *testing.T, ch <-chan appHostAuthOutcome) appHostAuthOutcome {
	t.Helper()
	select {
	case outcome := <-ch:
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for auth outcome")
	}

	return appHostAuthOutcome{}
}

func assertRootNoSecretText(t *testing.T, text string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("text leaked %q: %s", value, text)
		}
	}
}

func (o *recordingAppHostAuthDiagnosticObserver) observeAppHostAuthDiagnostic(event appHostAuthDiagnosticEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
}

func (o *recordingAppHostAuthDiagnosticObserver) eventsSnapshot() []appHostAuthDiagnosticEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]appHostAuthDiagnosticEvent(nil), o.events...)
}

func (o *recordingAppHostAuthDiagnosticObserver) categories() []string {
	events := o.eventsSnapshot()
	categories := make([]string, 0, len(events))
	for _, event := range events {
		categories = append(categories, event.Category)
	}

	return categories
}

func (o *recordingAppHostAuthDiagnosticObserver) encodedEvents(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(o.eventsSnapshot())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	return string(raw)
}

func assertAppHostAuthDiagnosticEventsCategoryOnly(t *testing.T, events []appHostAuthDiagnosticEvent) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("diagnostic events are empty")
	}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		var decoded map[string]string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if len(decoded) != 1 || decoded["category"] == "" {
			t.Fatalf("diagnostic event = %#v, encoded=%s", event, string(raw))
		}
		if !appHostAuthDiagnosticCategoryAllowed(decoded["category"]) {
			t.Fatalf("category %q is not whitelisted", decoded["category"])
		}
	}
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}

	return count
}

func stringSliceContains(values []string, want string) bool {
	return countString(values, want) > 0
}

func diagnosticCategoriesFromJSONLLines(t *testing.T, lines []string) []string {
	t.Helper()
	categories := make([]string, 0, len(lines))
	for _, line := range lines {
		var event map[string]string
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		categories = append(categories, event["category"])
	}

	return categories
}

func syntheticRootPrivateAuthRuntimeBundle(t *testing.T) *extractor.PrivateAuthRuntimeBundle {
	t.Helper()
	runtimeRaw := []byte(`{"packs":[{"verified_pack_identity":{"pack_id":"xpk-alpha001","pack_version":"opaque-1","asset_sha256":"` + strings.Repeat("1", 64) + `","manifest_sha256":"` + strings.Repeat("2", 64) + `","payload_sha256":"` + strings.Repeat("3", 64) + `","signature_sha256":"` + strings.Repeat("4", 64) + `","public_key_sha256":"` + strings.Repeat("5", 64) + `"},"store_binding":{"scope":"pack","profile_refs":["apr-alpha001"]},"profiles":[{"profile_ref":"apr-alpha001","kind":"bearer","login":{"url":"https://fixture.invalid/login","allowed_domains":[{"host":"fixture.invalid"}],"timeout_millis":30000,"callback_transport":{"mode":"local_post","content_types":["application/json"],"max_body_bytes":16384},"collector_js":"(() => { return function(ctx, postCapture) { return ctx && postCapture; }; })();","capture":{"format":"json","secret_candidates":["secret","capture.secret"],"kind_field":"kind","expires_at_field":"expires_at","redacted_display_field":"redacted_display"}}}],"preflight":{"mode":"required","missing":"refresh","expired":"refresh"},"provisioning":{"mode":"webview","profile_refs":["apr-alpha001"]},"materialization":{"profile_refs":["apr-alpha001"]},"normalization":{"reject_crlf":true,"trim_space":true}}]}`)
	envelope := []byte(`{"schema_version":1,"bundle_id":"arb-alpha001","bundle_version":"opaque-1","auth_runtime_private_sha256":"` + sha256HexForAppHostAuthTest(runtimeRaw) + `","runtime":` + string(runtimeRaw) + `}`)
	bundle, err := extractor.NewPrivateAuthRuntimeBundle(envelope, extractor.PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}

	return bundle
}

func appHostAuthAliasManifest(identity extractor.VerifiedPackIdentity) extractor.Manifest {
	return extractor.Manifest{
		PackID:           identity.PackID,
		PackVersion:      identity.PackVersion,
		Capabilities:     []extractor.Capability{extractor.CapabilityParseWASM, extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile},
		DomainPolicyRefs: []string{"dpr-alpha001"},
		BrokerPolicyRefs: []string{"bpr-alpha001"},
		ResourceLimits: extractor.ResourceLimits{
			TimeoutMillis:    60000,
			MaxMemoryPages:   64,
			MaxHostCalls:     16,
			MaxResponseBytes: 1 << 20,
			MaxOutputItems:   16,
			MaxOutputBytes:   1 << 16,
		},
		PayloadSHA256: identity.PayloadSHA256,
	}
}

func sha256HexForAppHostAuthTest(raw []byte) string {
	hash := sha256.Sum256(raw)

	return fmt.Sprintf("%x", hash[:])
}

func (fakeExtractorDispatcher) Resolve(context.Context, string) (extractor.AddTaskResolution, error) {
	return extractor.AddTaskResolution{}, nil
}

func (fakeExtractorDispatcher) BuildAria2Headers(context.Context, extractor.ResolvedAddItem) ([]string, error) {
	return nil, nil
}

func (fakeHostPolicyResolverForAppAuth) ResolveHostPolicy(context.Context, extractor.HostPolicyRequest) (extractor.ResolvedHostPolicy, error) {
	return extractor.ResolvedHostPolicy{}, errors.New("host policy denied")
}

func (fakeNoopAuthWebViewDriver) OpenAuthSession(context.Context, extractor.WebViewAuthRequest, extractor.AuthWebViewSink) (extractor.AuthWebViewSession, error) {
	return nil, errors.New("not used")
}

func (d *appHostAuthAutoSuccessDriver) OpenAuthSession(_ context.Context, request extractor.WebViewAuthRequest, sink extractor.AuthWebViewSink) (extractor.AuthWebViewSession, error) {
	d.mu.Lock()
	d.opens++
	d.mu.Unlock()
	if sink.OnSuccess != nil {
		sink.OnSuccess(extractor.AuthWebViewToken{Kind: request.Kind, Secret: "alias-captured-token", RedactedDisplay: "captured bearer"})
	}

	return appHostAuthAutoSuccessSession{}, nil
}

func (d *appHostAuthAutoSuccessDriver) openCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.opens
}

func (r *appHostAuthAliasResolver) ResolveHostPolicy(context.Context, extractor.HostPolicyRequest) (extractor.ResolvedHostPolicy, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	identity := r.identity

	return extractor.ResolvedHostPolicy{
		PolicyID:            "pol-alpha001",
		PolicyVersion:       "2026.05.15-alpha",
		PolicySHA256:        strings.Repeat("c", 64),
		PackIdentity:        identity,
		DomainPolicyRefs:    []string{"dpr-alpha001"},
		BrokerPolicyRefs:    []string{"bpr-alpha001"},
		AllowedCapabilities: []extractor.Capability{extractor.CapabilityParseWASM, extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile},
		IngressDomains:      []extractor.DomainRule{{Host: "share.alpha.test"}},
		BrokerDomains:       []extractor.DomainRule{{Host: "fixture.invalid"}},
		OutputDomains:       []extractor.HostPolicyOutputRule{{Host: "fixture.invalid", PathPrefixes: []string{"/"}}},
		AuthProfiles: []extractor.HostPolicyAuthProfileScope{{
			ProfileID: "apr-alpha001",
			Domains:   []extractor.DomainRule{{Host: "fixture.invalid"}},
		}},
		BrokerEndpoints: []extractor.HostPolicyBrokerEndpoint{{
			BrokerPolicyRef: "bpr-alpha001",
			EndpointRef:     "epr-alpha001",
			URLTemplate:     "https://fixture.invalid/resource/{id}",
			Methods:         []string{"GET"},
			AuthProfileRefs: []string{"apr-alpha001"},
		}},
	}, nil
}

func (r *appHostAuthAliasResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls
}

type appHostAuthAutoSuccessSession struct{}

func (appHostAuthAutoSuccessSession) Close() error { return nil }

type noopWailsTransport struct{}

func (noopWailsTransport) Start(context.Context, *application.MessageProcessor) error { return nil }
func (noopWailsTransport) JSClient() []byte                                           { return nil }
func (noopWailsTransport) Stop() error                                                { return nil }

func authRuntimeTaskIdentity(t *testing.T, bundle *extractor.PrivateAuthRuntimeBundle) extractor.VerifiedPackIdentity {
	t.Helper()
	identities := bundle.PackIdentities()
	if len(identities) != 1 {
		t.Fatalf("bundle identities = %#v, want one", identities)
	}

	return identities[0]
}

func setupAppTaskHistoryTest(t *testing.T) *App {
	t.Helper()

	originalCache := monitor.Cache
	originalSaveEnabled := history.SaveEnabled
	originalConfig := config.Current

	monitor.ResetDownloadGroupNamerForTest()
	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	monitor.Cache = &monitor.TaskCache{}
	history.DisableSaveForTest()
	history.Clear()
	config.Current = &config.AppConfig{ShowHistory: true}

	t.Cleanup(func() {
		monitor.ResetDownloadGroupNamerForTest()
		history.Clear()
		monitor.ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		monitor.Cache = originalCache
		config.Current = originalConfig
	})

	return NewApp()
}

func appHistoryTestDownloadGroup(id string) *rpc.DownloadGroup {
	return &rpc.DownloadGroup{
		ID:         id,
		Kind:       "batch",
		Name:       "Batch 2026-05-07 15-04-05",
		FolderName: "Batch 2026-05-07 15-04-05 " + id,
		Dir:        filepath.Join("history", id),
		ItemCount:  5,
		CreatedAt:  1778166245,
	}
}

func snapshotRPCResponse(result any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      "goaria",
		"result":  result,
	}
}

func mustFindTaskByGID(t *testing.T, tasks []rpc.Task, gid string) rpc.Task {
	t.Helper()

	for _, task := range tasks {
		if task.GID == gid {
			return task
		}
	}

	t.Fatalf("expected task %q in stopped slice", gid)
	return rpc.Task{}
}

func countTasksByGID(tasks []rpc.Task, gid string) int {
	count := 0
	for _, task := range tasks {
		if task.GID == gid {
			count++
		}
	}
	return count
}

func assertTaskPathAndSource(t *testing.T, task rpc.Task, wantPath string, wantSource string) {
	t.Helper()

	if len(task.Files) == 0 {
		t.Fatalf("expected task %q to include files", task.GID)
	}
	if task.Files[0].Path != wantPath {
		t.Fatalf("expected task %q path %q, got %q", task.GID, wantPath, task.Files[0].Path)
	}
	if len(task.Files[0].Uris) == 0 {
		t.Fatalf("expected task %q to include source uris", task.GID)
	}
	if task.Files[0].Uris[0].Uri != wantSource {
		t.Fatalf("expected task %q source %q, got %q", task.GID, wantSource, task.Files[0].Uris[0].Uri)
	}
}

func TestGetFullSnapshot_UsesFreshRPCStateAndHydratesDownloadGroupsForWindowRestore(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	activeGroup := appHistoryTestDownloadGroup("dg-snapshot-fresh-active")
	waitingGroup := appHistoryTestDownloadGroup("dg-snapshot-fresh-waiting")
	stoppedGroup := appHistoryTestDownloadGroup("dg-snapshot-fresh-stopped")

	monitor.RegisterTaskGroup("gid-fresh-active", *activeGroup)
	monitor.RegisterTaskGroup("gid-fresh-waiting", *waitingGroup)
	monitor.Cache.UpdateFromAria2(
		[]rpc.Task{{
			GID:             "gid-stale-active",
			Status:          "active",
			TotalLength:     "999",
			CompletedLength: "111",
			DownloadSpeed:   "333",
			Dir:             activeGroup.Dir,
		}},
		[]rpc.Task{{
			GID:             "gid-stale-waiting",
			Status:          "waiting",
			TotalLength:     "888",
			CompletedLength: "222",
			DownloadSpeed:   "444",
			Dir:             waitingGroup.Dir,
		}},
		[]rpc.Task{{
			GID:             "gid-fresh-stopped",
			Status:          "complete",
			TotalLength:     "0",
			CompletedLength: "0",
		}},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "aria2.tellActive":
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]map[string]any{{
				"gid":             "gid-fresh-active",
				"status":          "active",
				"totalLength":     "100",
				"completedLength": "25",
				"downloadSpeed":   "555",
				"errorCode":       "",
				"errorMessage":    "",
				"dir":             activeGroup.Dir,
				"files": []map[string]any{{
					"path": filepath.ToSlash(filepath.Join(activeGroup.Dir, "active.bin")),
					"uris": []map[string]any{{"uri": "https://example.com/fresh-active.bin"}},
				}},
			}}))
		case "aria2.tellWaiting":
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]map[string]any{{
				"gid":             "gid-fresh-waiting",
				"status":          "waiting",
				"totalLength":     "200",
				"completedLength": "40",
				"downloadSpeed":   "0",
				"errorCode":       "",
				"errorMessage":    "",
				"dir":             waitingGroup.Dir,
				"files": []map[string]any{{
					"path": filepath.ToSlash(filepath.Join(waitingGroup.Dir, "waiting.bin")),
					"uris": []map[string]any{{"uri": "https://example.com/fresh-waiting.bin"}},
				}},
			}}))
		case "aria2.tellStopped":
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]map[string]any{{
				"gid":             "gid-fresh-stopped",
				"status":          "complete",
				"totalLength":     "0",
				"completedLength": "0",
				"downloadSpeed":   "0",
				"errorCode":       "",
				"errorMessage":    "",
				"dir":             stoppedGroup.Dir,
				"files":           []map[string]any{},
			}}))
		default:
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]any{}))
		}
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	rpc.Init(parts[len(parts)-1], "secret")

	history.Add(history.HistoryEntry{
		GID:             "gid-fresh-stopped",
		Dir:             stoppedGroup.Dir,
		Path:            filepath.Join(stoppedGroup.Dir, "stopped.bin"),
		Source:          "https://example.com/snapshot-stopped.bin",
		TotalLength:     "300",
		CompletedLength: "300",
		DownloadGroup:   stoppedGroup,
	})

	snapshot := app.GetFullSnapshot()

	active := mustFindTaskByGID(t, snapshot.Tasks.Active, "gid-fresh-active")
	if active.DownloadGroup == nil || active.DownloadGroup.ID != activeGroup.ID {
		t.Fatalf("expected active snapshot task to hydrate group, got %#v", active.DownloadGroup)
	}
	if active.CompletedLength != "25" || active.DownloadSpeed != "555" {
		t.Fatalf("expected active snapshot to prefer fresh RPC values, got completed=%q speed=%q", active.CompletedLength, active.DownloadSpeed)
	}
	if countTasksByGID(snapshot.Tasks.Active, "gid-stale-active") != 0 {
		t.Fatalf("expected stale active cache task excluded from snapshot, got %#v", snapshot.Tasks.Active)
	}

	waiting := mustFindTaskByGID(t, snapshot.Tasks.Waiting, "gid-fresh-waiting")
	if waiting.DownloadGroup == nil || waiting.DownloadGroup.ID != waitingGroup.ID {
		t.Fatalf("expected waiting snapshot task to hydrate group, got %#v", waiting.DownloadGroup)
	}
	if waiting.CompletedLength != "40" {
		t.Fatalf("expected waiting snapshot to prefer fresh RPC values, got completed=%q", waiting.CompletedLength)
	}
	if countTasksByGID(snapshot.Tasks.Waiting, "gid-stale-waiting") != 0 {
		t.Fatalf("expected stale waiting cache task excluded from snapshot, got %#v", snapshot.Tasks.Waiting)
	}

	stopped := mustFindTaskByGID(t, snapshot.Tasks.Stopped, "gid-fresh-stopped")
	if stopped.DownloadGroup == nil || stopped.DownloadGroup.ID != stoppedGroup.ID {
		t.Fatalf("expected stopped snapshot task to backfill group, got %#v", stopped.DownloadGroup)
	}
	assertTaskPathAndSource(t, stopped, filepath.Join(stoppedGroup.Dir, "stopped.bin"), "https://example.com/snapshot-stopped.bin")
}
