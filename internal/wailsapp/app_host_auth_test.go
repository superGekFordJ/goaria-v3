//go:build extractor

package wailsapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/tasks"

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

type (
	fakeExtractorAdapter             struct{}
	fakeHostPolicyResolverForAppAuth struct{}
	fakeNoopAuthWebViewDriver        struct{}
)

type appHostAuthAutoSuccessDriver struct {
	mu       sync.Mutex
	requests []extractor.WebViewAuthRequest
}

type appHostAuthNoopSession struct{}

type appHostAuthAliasResolver struct {
	mu       sync.Mutex
	calls    int
	identity extractor.VerifiedPackIdentity
}

var rootWailsTestAppMu sync.Mutex

func TestAppHostAuthDriverAllowsOneInflightSession(t *testing.T) {
	app := NewApp(Options{})
	app.SetApp(newRootWailsTestApp(t))
	app.SetWindow(&application.WebviewWindow{})
	factory := &fakeHostAuthSessionWindowFactory{}
	driver := newAppHostAuthDriverWithFactory(app, factory)

	first, err := driver.OpenAuthSession(context.Background(), appHostAuthWebViewRequest(time.Second), newRecordingAuthWebViewSink().sink())
	if err != nil {
		t.Fatalf("OpenAuthSession() first error = %v", err)
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
		coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(newWindowedAuthApp(t), factory))
		coordinator.SetObserver(appHostAuthDiagnosticObserver{})
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
	formatted := strings.ToLower(got.PackID + " " + string(got.ProfileID) + " " + string(got.Kind) + " " + got.LoginURL + " " + got.CallbackURL + " " + got.CollectorJS)
	for _, forbidden := range []string{"provider", "cookiename", "login step"} {
		if strings.Contains(strings.ToLower(formatted), strings.ToLower(forbidden)) {
			t.Fatalf("generic request contains forbidden marker %q: %s", forbidden, formatted)
		}
	}
}

func TestAppHostAuthCallbackMiddlewareCompletesCoordinatorSession(t *testing.T) {
	store := newRootTempAuthProfileStore(t)
	factory := &fakeHostAuthSessionWindowFactory{}
	app := newWindowedAuthApp(t)
	coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(app, factory))
	resultCh := make(chan appHostAuthOutcome, 1)

	go func() {
		result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequestWithCallback(time.Second))
		resultCh <- appHostAuthOutcome{result: result, err: err}
	}()
	factory.waitForOpen(t)
	opened := factory.request(0)
	if opened.CallbackURL == "" || !strings.Contains(opened.CallbackURL, appHostAuthCallbackPrefix) || opened.SessionToken == "" || opened.CollectorJS == "" {
		t.Fatalf("opened callback route/js not rendered: %#v", opened)
	}

	req := httptest.NewRequest(http.MethodPost, opened.CallbackPath, strings.NewReader(`{"kind":"bearer","secret":"captured-callback-token"}`))
	req.Header.Set("Origin", opened.AuthPageOrigin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(appHostAuthSessionHeader, opened.SessionToken)
	rec := httptest.NewRecorder()
	app.hostAuthCallbackMiddleware(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("callback status = %d body=%q, want 202", rec.Code, rec.Body.String())
	}

	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err != nil {
		t.Fatalf("coordinator.Start() error = %v", outcome.err)
	}
	if outcome.result.Status != extractor.WebViewAuthStatusSuccess {
		t.Fatalf("coordinator.Start() status = %q, want success", outcome.result.Status)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), "xpk-alpha001", "apr-alpha001", "https://fixture.invalid/item")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderValue != "Bearer captured-callback-token" {
		t.Fatalf("HeaderValue = %q, want captured callback token", resolved.HeaderValue)
	}

	dup := httptest.NewRecorder()
	app.hostAuthCallbackMiddleware(http.NotFoundHandler()).ServeHTTP(dup, req.Clone(context.Background()))
	if dup.Code != http.StatusGone {
		t.Fatalf("duplicate callback status = %d, want 410", dup.Code)
	}
}

func TestAppHostAuthCallbackMiddlewareInvalidPayloadFailsClosed(t *testing.T) {
	store := newRootTempAuthProfileStore(t)
	factory := &fakeHostAuthSessionWindowFactory{}
	app := newWindowedAuthApp(t)
	coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(app, factory))
	resultCh := make(chan appHostAuthOutcome, 1)

	go func() {
		result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequestWithCallback(time.Second))
		resultCh <- appHostAuthOutcome{result: result, err: err}
	}()
	factory.waitForOpen(t)
	opened := factory.request(0)
	req := httptest.NewRequest(http.MethodPost, opened.CallbackPath, strings.NewReader(`{"kind":"cookie","secret":"raw-invalid-secret"}`))
	req.Header.Set("Origin", opened.AuthPageOrigin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(appHostAuthSessionHeader, opened.SessionToken)
	rec := httptest.NewRecorder()
	app.hostAuthCallbackMiddleware(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback status = %d body=%q, want 400", rec.Code, rec.Body.String())
	}
	assertRootNoSecretText(t, rec.Body.String(), "raw-invalid-secret")

	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err == nil {
		t.Fatalf("coordinator.Start() error = nil, result=%#v", outcome.result)
	}
	assertNoRootAuthProfile(t, store)
}

func TestAppHostAuthDriverHeadlessOrNoWindowFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name string
		app  *App
	}{
		{name: "nil app", app: nil},
		{name: "wails app nil", app: NewApp(Options{})},
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
	app := NewApp(Options{})
	store := newRootTempAuthProfileStore(t)
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	driver := fakeNoopAuthWebViewDriver{}
	policyResolver := fakeHostPolicyResolverForAppAuth{}
	var captured extractor.EmbeddedReleaseDispatcherConfig

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks: func() bool { return true },
		embeddedReleaseRequired: func() bool { return false },
		loadHostPolicyResolver:  func() (extractor.HostPolicyResolver, error) { return policyResolver, nil },
		loadAuthRuntimeBundle:   func() (*extractor.PrivateAuthRuntimeBundle, error) { return bundle, nil },
		defaultAuthProfileStorePath: func() (string, error) {
			return filepath.Join(t.TempDir(), "auth.json"), nil
		},
		newFileAuthProfileStore: func(string) (extractor.AuthProfileStore, error) { return store, nil },
		newAuthWebViewDriver:    func(*App) extractor.AuthWebViewDriver { return driver },
		newEmbeddedReleaseAddTaskAdapter: func(config extractor.EmbeddedReleaseDispatcherConfig, runtime *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			captured = config
			return fakeExtractorAdapter{}, nil
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
	if app.extractorAdapter == nil {
		t.Fatal("App extractor adapter = nil")
	}
}

func TestConfigureEmbeddedExtractorDispatcherPassesHostPolicyResolverToRuntime(t *testing.T) {
	app := NewApp(Options{})
	store := newRootTempAuthProfileStore(t)
	identity := appHostAuthAliasIdentity()
	bundle := syntheticRootAliasPrivateAuthRuntimeBundle(t, identity)
	driver := &appHostAuthAutoSuccessDriver{}
	resolver := &appHostAuthAliasResolver{identity: identity}

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks: func() bool { return true },
		embeddedReleaseRequired: func() bool { return false },
		loadHostPolicyResolver:  func() (extractor.HostPolicyResolver, error) { return resolver, nil },
		loadAuthRuntimeBundle:   func() (*extractor.PrivateAuthRuntimeBundle, error) { return bundle, nil },
		defaultAuthProfileStorePath: func() (string, error) {
			return filepath.Join(t.TempDir(), "auth.json"), nil
		},
		newFileAuthProfileStore: func(string) (extractor.AuthProfileStore, error) { return store, nil },
		newAuthWebViewDriver:    func(*App) extractor.AuthWebViewDriver { return driver },
		newEmbeddedReleaseAddTaskAdapter: func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			return fakeExtractorAdapter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	runtime := app.hostAuthRuntimeForTest()
	if runtime == nil {
		t.Fatal("App HostAuthRuntime = nil")
	}
	manifest := appHostAuthAliasManifest(identity)
	result, err := runtime.Provision(context.Background(), extractor.HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://auth.alpha.test/file",
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

func TestConfigureEmbeddedExtractorDispatcherActualCallbackUsesSharedStore(t *testing.T) {
	app := newWindowedAuthApp(t)
	store := newRecordingRootAuthProfileStore(t)
	factory := &fakeHostAuthSessionWindowFactory{}
	identity := appHostAuthAliasIdentity()
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	storePath := filepath.Join(t.TempDir(), "auth.json")

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks: func() bool { return true },
		embeddedReleaseRequired: func() bool { return false },
		loadHostPolicyResolver:  func() (extractor.HostPolicyResolver, error) { return nil, nil },
		loadAuthRuntimeBundle:   func() (*extractor.PrivateAuthRuntimeBundle, error) { return bundle, nil },
		defaultAuthProfileStorePath: func() (string, error) {
			return storePath, nil
		},
		newFileAuthProfileStore: func(path string) (extractor.AuthProfileStore, error) {
			if path != storePath {
				t.Fatalf("store path mismatch")
			}
			return store, nil
		},
		newAuthWebViewDriver: func(appService *App) extractor.AuthWebViewDriver {
			return newAppHostAuthDriverWithFactory(appService, factory)
		},
		newEmbeddedReleaseAddTaskAdapter: func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			return fakeExtractorAdapter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if app.authProfileStoreForTest() != store {
		t.Fatal("configured App store is not the shared test store")
	}
	runtime := app.hostAuthRuntimeForTest()
	if runtime == nil {
		t.Fatal("App HostAuthRuntime = nil")
	}
	request := extractor.HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     appHostAuthWebViewRequest(time.Second).Manifest,
		SourceURL:    "https://fixture.invalid/source",
		TargetURL:    "https://fixture.invalid/item",
		ProfileRef:   "apr-alpha001",
	}
	resultCh := make(chan appHostAuthRuntimeOutcome, 1)

	go func() {
		result, err := runtime.Provision(context.Background(), request)
		resultCh <- appHostAuthRuntimeOutcome{result: result, err: err}
	}()
	factory.waitForOpen(t)
	opened := factory.request(0)
	status := postHostAuthCallback(t, app, opened, `{"kind":"bearer","secret":"configured-callback-secret","redacted_display":"synthetic captured auth"}`, opened.SessionToken, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("callback status = %d, want accepted", status)
	}
	outcome := receiveAppHostAuthRuntimeOutcome(t, resultCh)
	if outcome.err != nil {
		t.Fatalf("runtime.Provision() error = %v", outcome.err)
	}
	if !outcome.result.Provisioned || !outcome.result.Available {
		t.Fatalf("runtime.Provision() = %#v, want provisioned available", outcome.result)
	}
	if calls := store.SetCalls(); calls != 1 {
		t.Fatalf("store set calls = %d, want 1", calls)
	}
	if _, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", request.TargetURL); err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if _, err := runtime.MaterializeAuthProfile(context.Background(), request); err != nil {
		t.Fatalf("MaterializeAuthProfile() error = %v", err)
	}
}

func TestConfigureEmbeddedExtractorDispatcherDiagnosticStoreWrapperRecordsBuckets(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	logPath := filepath.Join(t.TempDir(), "diagnostic.jsonl")
	t.Setenv(appHostAuthDiagnosticLogEnv, logPath)
	app := newWindowedAuthApp(t)
	store := newRecordingRootAuthProfileStore(t)
	factory := &fakeHostAuthSessionWindowFactory{}
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks: func() bool { return true },
		embeddedReleaseRequired: func() bool { return false },
		loadHostPolicyResolver:  func() (extractor.HostPolicyResolver, error) { return nil, nil },
		loadAuthRuntimeBundle:   func() (*extractor.PrivateAuthRuntimeBundle, error) { return bundle, nil },
		defaultAuthProfileStorePath: func() (string, error) {
			return filepath.Join(t.TempDir(), "auth.json"), nil
		},
		newFileAuthProfileStore: func(string) (extractor.AuthProfileStore, error) { return store, nil },
		newAuthWebViewDriver: func(appService *App) extractor.AuthWebViewDriver {
			return newAppHostAuthDriverWithFactory(appService, factory)
		},
		newEmbeddedReleaseAddTaskAdapter: func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			return fakeExtractorAdapter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if app.authProfileStoreForTest() == store {
		t.Fatalf("diagnostic store wrapper was not installed")
	}
	if _, err := app.authProfileStoreForTest().AuthProfileSnapshots(context.Background(), "xpk-alpha001"); err != nil {
		t.Fatalf("snapshot bucket check error = %v", err)
	}
	runtime := app.hostAuthRuntimeForTest()
	if runtime == nil {
		t.Fatal("App HostAuthRuntime = nil")
	}
	request := extractor.HostAuthRuntimeRequest{
		PackIdentity: appHostAuthAliasIdentity(),
		Manifest:     appHostAuthWebViewRequest(time.Second).Manifest,
		SourceURL:    "https://fixture.invalid/source",
		TargetURL:    "https://fixture.invalid/item",
		ProfileRef:   "apr-alpha001",
	}
	resultCh := make(chan appHostAuthRuntimeOutcome, 1)
	go func() {
		result, err := runtime.Provision(context.Background(), request)
		resultCh <- appHostAuthRuntimeOutcome{result: result, err: err}
	}()
	factory.waitForOpen(t)
	opened := factory.request(0)
	status := postHostAuthCallback(t, app, opened, `{"kind":"bearer","secret":"diagnostic-wrapper-secret"}`, opened.SessionToken, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("callback status = %d, want accepted", status)
	}
	outcome := receiveAppHostAuthRuntimeOutcome(t, resultCh)
	if outcome.err != nil || !outcome.result.Available {
		t.Fatalf("runtime.Provision() = %#v err=%v, want available", outcome.result, outcome.err)
	}
	_, _ = app.authProfileStoreForTest().AuthProfileSnapshots(context.Background(), request.PackIdentity.PackID)
	text := string(mustReadAppHostAuthTestFile(t, logPath))
	for _, want := range []string{`"stage":"store","category":"snapshot_bucket_zero"`, `"stage":"store","category":"set_attempted"`, `"stage":"store","category":"set_succeeded"`, `"stage":"store","category":"snapshot_bucket_nonzero"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("diagnostic store log missing category marker")
		}
	}
	for _, forbidden := range []string{"diagnostic-wrapper-secret", "fixture.invalid", "apr-alpha001", request.PackIdentity.PackID} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("diagnostic store log leaks forbidden value")
		}
	}
}

func TestConfigureEmbeddedExtractorDispatcherFallsBackToSharedStoreWithoutRuntimeBundle(t *testing.T) {
	app := NewApp(Options{})
	store := newRootTempAuthProfileStore(t)
	var captured extractor.EmbeddedReleaseDispatcherConfig

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks: func() bool { return true },
		embeddedReleaseRequired: func() bool { return false },
		loadHostPolicyResolver:  func() (extractor.HostPolicyResolver, error) { return nil, nil },
		loadAuthRuntimeBundle:   func() (*extractor.PrivateAuthRuntimeBundle, error) { return nil, nil },
		defaultAuthProfileStorePath: func() (string, error) {
			return filepath.Join(t.TempDir(), "auth.json"), nil
		},
		newFileAuthProfileStore: func(string) (extractor.AuthProfileStore, error) { return store, nil },
		newEmbeddedReleaseAddTaskAdapter: func(config extractor.EmbeddedReleaseDispatcherConfig, runtime *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			captured = config
			return fakeExtractorAdapter{}, nil
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
	app := NewApp(Options{})
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
		newEmbeddedReleaseAddTaskAdapter: func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			dispatcherCreated = true
			return fakeExtractorAdapter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if storeCreated || dispatcherCreated || app.extractorAdapter != nil || app.authProfileStoreForTest() != nil || app.hostAuthRuntimeForTest() != nil {
		t.Fatalf("no-op path created state: store=%t dispatcher=%t app=%#v", storeCreated, dispatcherCreated, app)
	}
}

func TestConfigureEmbeddedExtractorDispatcherStartupNoRuntimeInputs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "startup.jsonl")
	t.Setenv(extractorStartupDiagnosticEnv, "1")
	t.Setenv(extractorStartupDiagnosticLogEnv, logPath)
	app := NewApp(Options{})
	storeCreated := false
	dispatcherCreated := false

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:              func() bool { return false },
		embeddedReleaseRequired:              func() bool { return false },
		privatePolicyRuntimeSourceState:      func() extractor.RuntimeSourceState { return extractor.RuntimeSourceStateNone },
		privateAuthRuntimeRuntimeSourceState: func() extractor.RuntimeSourceState { return extractor.RuntimeSourceStateNone },
		loadAuthRuntimeBundle:                func() (*extractor.PrivateAuthRuntimeBundle, error) { return nil, nil },
		newFileAuthProfileStore: func(string) (extractor.AuthProfileStore, error) {
			storeCreated = true
			return newRootTempAuthProfileStore(t), nil
		},
		newEmbeddedReleaseAddTaskAdapter: func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			dispatcherCreated = true
			return fakeExtractorAdapter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if storeCreated || dispatcherCreated || app.extractorAdapter != nil || app.authProfileStoreForTest() != nil || app.hostAuthRuntimeForTest() != nil {
		t.Fatalf("startup no-runtime path created state: store=%t dispatcher=%t app=%#v", storeCreated, dispatcherCreated, app)
	}
	text := string(mustReadAppHostAuthTestFile(t, logPath))
	assertStartupDiagnosticCategories(t, text,
		`"stage":"embedded_pack","category":"absent"`,
		`"stage":"embedded_release","category":"optional"`,
		`"stage":"policy_source","category":"none"`,
		`"stage":"auth_runtime_source","category":"none"`,
		`"stage":"policy_load","category":"skipped"`,
		`"stage":"auth_store","category":"skipped"`,
		`"stage":"host_auth_runtime","category":"skipped"`,
		`"stage":"driver","category":"skipped"`,
		`"stage":"dispatcher","category":"skipped"`,
		`"stage":"startup_activation","category":"no_runtime_inputs"`,
	)
	assertRootNoSecretText(t, text, "raw-token", "fixture.invalid")
}

func TestConfigureEmbeddedExtractorDispatcherStartupActivationProved(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "startup.jsonl")
	t.Setenv(extractorStartupDiagnosticEnv, "1")
	t.Setenv(extractorStartupDiagnosticLogEnv, logPath)
	app := NewApp(Options{})
	store := newRootTempAuthProfileStore(t)
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	storePath := filepath.Join(t.TempDir(), "auth.json")

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:              func() bool { return true },
		embeddedReleaseRequired:              func() bool { return false },
		privatePolicyRuntimeSourceState:      func() extractor.RuntimeSourceState { return extractor.RuntimeSourceStateEmbedded },
		privateAuthRuntimeRuntimeSourceState: func() extractor.RuntimeSourceState { return extractor.RuntimeSourceStateEmbedded },
		loadHostPolicyResolver:               func() (extractor.HostPolicyResolver, error) { return fakeHostPolicyResolverForAppAuth{}, nil },
		loadAuthRuntimeBundle:                func() (*extractor.PrivateAuthRuntimeBundle, error) { return bundle, nil },
		defaultAuthProfileStorePath: func() (string, error) {
			return storePath, nil
		},
		newFileAuthProfileStore: func(path string) (extractor.AuthProfileStore, error) {
			if path != storePath {
				t.Fatalf("store path mismatch")
			}
			return store, nil
		},
		newAuthWebViewDriver: func(*App) extractor.AuthWebViewDriver { return fakeNoopAuthWebViewDriver{} },
		newEmbeddedReleaseAddTaskAdapter: func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			return fakeExtractorAdapter{}, nil
		},
	})
	if err != nil {
		t.Fatalf("configure helper error = %v", err)
	}
	if app.authProfileStoreForTest() != store {
		t.Fatalf("App store = %#v, want shared store", app.authProfileStoreForTest())
	}
	if app.hostAuthRuntimeForTest() == nil || app.authWebViewDriverForTest() == nil || app.extractorAdapter == nil {
		t.Fatalf("startup activation proved path not fully configured: runtime=%#v driver=%#v adapter=%#v", app.hostAuthRuntimeForTest(), app.authWebViewDriverForTest(), app.extractorAdapter)
	}
	text := string(mustReadAppHostAuthTestFile(t, logPath))
	assertStartupDiagnosticCategories(t, text,
		`"stage":"embedded_pack","category":"present"`,
		`"stage":"embedded_release","category":"optional"`,
		`"stage":"policy_source","category":"embedded"`,
		`"stage":"policy_load","category":"loaded"`,
		`"stage":"auth_runtime_source","category":"embedded"`,
		`"stage":"auth_runtime_load","category":"loaded_nonzero"`,
		`"stage":"auth_store","category":"configured"`,
		`"stage":"host_auth_runtime","category":"configured"`,
		`"stage":"driver","category":"configured"`,
		`"stage":"dispatcher","category":"configured"`,
		`"stage":"startup_activation","category":"activation_proved"`,
	)
	assertRootNoSecretText(t, text, storePath, "xpk-alpha001", "apr-alpha001", "fixture.invalid")
}

func TestConfigureEmbeddedExtractorDispatcherStartupActivationMissingOrSkipped(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "startup.jsonl")
	t.Setenv(extractorStartupDiagnosticEnv, "1")
	t.Setenv(extractorStartupDiagnosticLogEnv, logPath)
	app := NewApp(Options{})
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)

	err := configureEmbeddedExtractorDispatcherWithDeps(app, embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:              func() bool { return true },
		embeddedReleaseRequired:              func() bool { return false },
		privatePolicyRuntimeSourceState:      func() extractor.RuntimeSourceState { return extractor.RuntimeSourceStateEmbedded },
		privateAuthRuntimeRuntimeSourceState: func() extractor.RuntimeSourceState { return extractor.RuntimeSourceStateEmbedded },
		loadHostPolicyResolver:               func() (extractor.HostPolicyResolver, error) { return fakeHostPolicyResolverForAppAuth{}, nil },
		loadAuthRuntimeBundle:                func() (*extractor.PrivateAuthRuntimeBundle, error) { return bundle, nil },
		defaultAuthProfileStorePath: func() (string, error) {
			return filepath.Join(t.TempDir(), "auth.json"), nil
		},
		newFileAuthProfileStore: func(string) (extractor.AuthProfileStore, error) {
			return newRootTempAuthProfileStore(t), nil
		},
		newAuthWebViewDriver: func(*App) extractor.AuthWebViewDriver { return fakeNoopAuthWebViewDriver{} },
		newEmbeddedReleaseAddTaskAdapter: func(extractor.EmbeddedReleaseDispatcherConfig, *extractor.HostAuthRuntime) (tasks.ExtractorAdapter, error) {
			return nil, errors.New("dispatcher failed Authorization: Bearer raw-secret token=raw-token")
		},
	})
	if err == nil {
		t.Fatal("configure helper error = nil, want sanitized dispatcher failure")
	}
	assertRootNoSecretText(t, err.Error(), "raw-secret", "raw-token", "Authorization")
	text := string(mustReadAppHostAuthTestFile(t, logPath))
	assertStartupDiagnosticCategories(t, text,
		`"stage":"embedded_pack","category":"present"`,
		`"stage":"policy_source","category":"embedded"`,
		`"stage":"policy_load","category":"loaded"`,
		`"stage":"auth_runtime_source","category":"embedded"`,
		`"stage":"auth_runtime_load","category":"loaded_nonzero"`,
		`"stage":"auth_store","category":"configured"`,
		`"stage":"host_auth_runtime","category":"configured"`,
		`"stage":"driver","category":"configured"`,
		`"stage":"dispatcher","category":"failed"`,
		`"stage":"startup_activation","category":"activation_missing_or_skipped"`,
	)
	assertRootNoSecretText(t, text, "raw-secret", "raw-token", "Authorization")
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
				hasEmbeddedReleasePacks: func() bool { return true },
				embeddedReleaseRequired: func() bool { return false },
				loadHostPolicyResolver:  func() (extractor.HostPolicyResolver, error) { return nil, nil },
				loadAuthRuntimeBundle:   func() (*extractor.PrivateAuthRuntimeBundle, error) { return nil, nil },
				defaultAuthProfileStorePath: func() (string, error) {
					return filepath.Join(t.TempDir(), "auth.json"), nil
				},
				newFileAuthProfileStore: func(string) (extractor.AuthProfileStore, error) { return nil, errors.New("Cookie: sid=raw-cookie") },
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := configureEmbeddedExtractorDispatcherWithDeps(NewApp(Options{}), tt.deps)
			if err == nil {
				t.Fatal("configure helper error = nil, want sanitized failure")
			}
			assertRootNoSecretText(t, err.Error(), "raw-token", "raw-secret", "raw-cookie", "Authorization", "Cookie")
		})
	}
}

func TestHostAuthPublicStableIdentifiersAreGeneric(t *testing.T) {
	request := appHostAuthSessionRequest{ProfileID: "apr-alpha001"}
	for _, value := range []string{
		appHostAuthUnavailableMessage,
		appHostAuthInProgressMessage,
		appHostAuthInvalidPayloadMessage,
		appHostAuthCallbackErrorMessage,
		appHostAuthSessionWindowName(request),
		"GoAria Auth Session",
	} {
		assertNoProviderSurfaceTerm(t, value)
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
	app := NewApp(Options{})
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

func appHostAuthWebViewRequestWithCallback(timeout time.Duration) extractor.WebViewAuthRequest {
	return appHostAuthWebViewRequest(timeout)
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

func assertStartupDiagnosticCategories(t *testing.T, text string, wanted ...string) {
	t.Helper()
	for _, marker := range wanted {
		if !strings.Contains(text, marker) {
			t.Fatalf("startup diagnostic log missing category marker %q in %s", marker, text)
		}
	}
}

func mustReadAppHostAuthTestFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	return raw
}

func syntheticRootPrivateAuthRuntimeBundle(t *testing.T) *extractor.PrivateAuthRuntimeBundle {
	t.Helper()
	runtimeRaw := []byte(`{"packs":[{"verified_pack_identity":{"pack_id":"xpk-alpha001","pack_version":"opaque-1","asset_sha256":"` + strings.Repeat("1", 64) + `","manifest_sha256":"` + strings.Repeat("2", 64) + `","payload_sha256":"` + strings.Repeat("3", 64) + `","signature_sha256":"` + strings.Repeat("4", 64) + `","public_key_sha256":"` + strings.Repeat("5", 64) + `"},"store_binding":{"scope":"pack","profile_refs":["apr-alpha001"]},"profiles":[{"profile_ref":"apr-alpha001","kind":"bearer","login":{"url":"https://fixture.invalid/login","allowed_domains":[{"host":"fixture.invalid"}],"timeout_millis":30000,` + appHostAuthRuntimeCallbackJSONForTest() + `}}],"preflight":{"mode":"required","missing":"refresh","expired":"refresh"},"provisioning":{"mode":"webview","profile_refs":["apr-alpha001"]},"materialization":{"profile_refs":["apr-alpha001"]},"normalization":{"reject_crlf":true,"trim_space":true}}]}`)
	envelope := []byte(`{"schema_version":1,"bundle_id":"arb-alpha001","bundle_version":"opaque-1","auth_runtime_private_sha256":"` + sha256HexForAppHostAuthTest(runtimeRaw) + `","runtime":` + string(runtimeRaw) + `}`)
	bundle, err := extractor.NewPrivateAuthRuntimeBundle(envelope, extractor.PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}

	return bundle
}

func syntheticRootAliasPrivateAuthRuntimeBundle(t *testing.T, identity extractor.VerifiedPackIdentity) *extractor.PrivateAuthRuntimeBundle {
	t.Helper()
	runtimeRaw := []byte(`{"packs":[{"verified_pack_identity":{"pack_id":"` + identity.PackID + `","pack_version":"` + identity.PackVersion + `","asset_sha256":"` + identity.AssetSHA256 + `","manifest_sha256":"` + identity.ManifestSHA256 + `","payload_sha256":"` + identity.PayloadSHA256 + `","signature_sha256":"` + identity.SignatureSHA256 + `","public_key_sha256":"` + identity.PublicKeySHA256 + `"},"store_binding":{"scope":"pack","profile_refs":["apr-alpha001"]},"profiles":[{"profile_ref":"apr-alpha001","kind":"bearer","login":{"url":"https://auth.alpha.test/login","allowed_domains":[{"host":"auth.alpha.test"}],"timeout_millis":30000,` + appHostAuthRuntimeCallbackJSONForTest() + `}}],"preflight":{"mode":"required","missing":"refresh","expired":"refresh"},"provisioning":{"mode":"webview","profile_refs":["apr-alpha001"]},"materialization":{"profile_refs":["apr-alpha001"]},"normalization":{"reject_crlf":true,"trim_space":true}}]}`)
	envelope := []byte(`{"schema_version":1,"bundle_id":"arb-alpha001","bundle_version":"opaque-1","auth_runtime_private_sha256":"` + sha256HexForAppHostAuthTest(runtimeRaw) + `","runtime":` + string(runtimeRaw) + `}`)
	bundle, err := extractor.NewPrivateAuthRuntimeBundle(envelope, extractor.PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle(alias) error = %v", err)
	}

	return bundle
}

func sha256HexForAppHostAuthTest(raw []byte) string {
	hash := sha256.Sum256(raw)

	return hex.EncodeToString(hash[:])
}

func appHostAuthRuntimeCallbackJSONForTest() string {
	return `"callback_transport":{"mode":"local_post","content_types":["application/json"],"max_body_bytes":16384},"collector_js":"(() => { return function(ctx, postCapture) { return ctx && postCapture; }; })();","capture":{"format":"json","secret_candidates":["secret","capture.secret"],"kind_field":"kind","expires_at_field":"expires_at","redacted_display_field":"redacted_display"}`
}

func appHostAuthAliasIdentity() extractor.VerifiedPackIdentity {
	return extractor.VerifiedPackIdentity{
		PackID:          "xpk-alpha001",
		PackVersion:     "opaque-1",
		AssetSHA256:     strings.Repeat("1", 64),
		ManifestSHA256:  strings.Repeat("2", 64),
		PayloadSHA256:   strings.Repeat("3", 64),
		SignatureSHA256: strings.Repeat("4", 64),
		PublicKeySHA256: strings.Repeat("5", 64),
	}
}

func appHostAuthAliasManifest(identity extractor.VerifiedPackIdentity) extractor.Manifest {
	return extractor.Manifest{
		PackID:           identity.PackID,
		PackVersion:      identity.PackVersion,
		ABIVersion:       extractor.CurrentABIVersion,
		Capabilities:     []extractor.Capability{extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile},
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

func (fakeExtractorAdapter) Resolve(context.Context, string) (tasks.Resolution, error) {
	return tasks.Resolution{}, nil
}

func (fakeExtractorAdapter) BuildHeaders(context.Context, tasks.ResolvedItem) ([]string, error) {
	return nil, nil
}

func (fakeExtractorAdapter) AuthRequestsForSource(context.Context, string) ([]tasks.AuthRequest, error) {
	return nil, nil
}

func (fakeExtractorAdapter) Preflight(context.Context, tasks.AuthRequest) (tasks.PreflightResult, error) {
	return tasks.PreflightResult{}, nil
}

func (fakeExtractorAdapter) RefreshOnRecoverablePreflightFailure(context.Context, tasks.AuthRequest, tasks.RefreshGuard) (tasks.RefreshResult, error) {
	return tasks.RefreshResult{}, nil
}

func (fakeExtractorAdapter) RefreshOnGenericFailure(context.Context, tasks.AuthRequest, tasks.RefreshGuard) (tasks.RefreshResult, error) {
	return tasks.RefreshResult{}, nil
}

func (fakeExtractorAdapter) ValidateItemAuthPolicy(tasks.ResolvedItem) error {
	return nil
}

func (fakeExtractorAdapter) NewRefreshGuard() tasks.RefreshGuard {
	return nil
}

func (fakeExtractorAdapter) RedactError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (fakeHostPolicyResolverForAppAuth) ResolveHostPolicy(context.Context, extractor.HostPolicyRequest) (extractor.ResolvedHostPolicy, error) {
	return extractor.ResolvedHostPolicy{}, errors.New("host policy denied")
}

func (fakeNoopAuthWebViewDriver) OpenAuthSession(context.Context, extractor.WebViewAuthRequest, extractor.AuthWebViewSink) (extractor.AuthWebViewSession, error) {
	return nil, errors.New("not used")
}

func (d *appHostAuthAutoSuccessDriver) OpenAuthSession(ctx context.Context, request extractor.WebViewAuthRequest, sink extractor.AuthWebViewSink) (extractor.AuthWebViewSession, error) {
	d.mu.Lock()
	d.requests = append(d.requests, request)
	d.mu.Unlock()
	if sink.OnSuccess != nil {
		sink.OnSuccess(extractor.AuthWebViewToken{Kind: request.Kind, Secret: "alias-runtime-token", RedactedDisplay: "captured bearer"})
	}

	return appHostAuthNoopSession{}, nil
}

func (d *appHostAuthAutoSuccessDriver) openCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return len(d.requests)
}

func (appHostAuthNoopSession) Close() error { return nil }

func (r *appHostAuthAliasResolver) ResolveHostPolicy(_ context.Context, request extractor.HostPolicyRequest) (extractor.ResolvedHostPolicy, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return extractor.ResolvedHostPolicy{
		PolicyID:            "hpr-alpha001",
		PolicyVersion:       "2026.5.14",
		PolicySHA256:        strings.Repeat("c", 64),
		PackIdentity:        request.PackIdentity,
		DomainPolicyRefs:    append([]string(nil), request.Manifest.DomainPolicyRefs...),
		BrokerPolicyRefs:    append([]string(nil), request.Manifest.BrokerPolicyRefs...),
		AllowedCapabilities: []extractor.Capability{extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile},
		IngressDomains:      []extractor.DomainRule{{Host: "share.alpha.test"}},
		BrokerDomains:       []extractor.DomainRule{{Host: "auth.alpha.test"}},
		OutputDomains:       []extractor.HostPolicyOutputRule{{Host: "auth.alpha.test", PathPrefixes: []string{"/"}}},
		AuthProfiles:        []extractor.HostPolicyAuthProfileScope{{ProfileID: "apr-alpha001", Domains: []extractor.DomainRule{{Host: "auth.alpha.test"}}}},
		Endpoints:           []extractor.HostPolicyEndpoint{{BrokerPolicyRef: "bpr-alpha001", EndpointRef: "epr-alpha001", URLTemplate: "https://auth.alpha.test/session/{id}", Methods: []string{"GET"}, AuthProfileRefs: []extractor.AuthProfileID{"apr-alpha001"}, TimeoutMillis: 3000, MaxResponseBytes: 65536}},
	}, nil
}

func (r *appHostAuthAliasResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls
}

type noopWailsTransport struct{}

func (noopWailsTransport) Start(context.Context, *application.MessageProcessor) error { return nil }
func (noopWailsTransport) JSClient() []byte                                           { return nil }
func (noopWailsTransport) Stop() error                                                { return nil }

func assertNoProviderSurfaceTerm(t *testing.T, value string) {
	t.Helper()
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"accounttoken", "x-website-token"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("public-stable value %q contains provider/private term %q", value, forbidden)
		}
	}
}
