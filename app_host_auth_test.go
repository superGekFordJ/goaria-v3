package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/extractor"

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

type appHostAuthOutcome struct {
	result extractor.WebViewAuthResult
	err    error
}

type fakeExtractorDispatcher struct{}

type fakeHostPolicyResolverForAppAuth struct{}

type fakeNoopAuthWebViewDriver struct{}

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

type noopWailsTransport struct{}

func (noopWailsTransport) Start(context.Context, *application.MessageProcessor) error { return nil }
func (noopWailsTransport) JSClient() []byte                                           { return nil }
func (noopWailsTransport) Stop() error                                                { return nil }
