package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/extractor"
)

const addTaskGenericAuthResolutionError = "could not resolve this link; authentication may be required or the link is unsupported"

type authRuntimeTaskDispatcher struct {
	mu            sync.Mutex
	events        *authRuntimeTaskEventLog
	plans         map[string][]extractor.HostAuthRuntimeRequest
	resolutions   map[string][]extractor.AddTaskResolution
	resolveErrors map[string][]error
	headers       map[string][]string
	resolveCounts map[string]int
}

type authRuntimeTaskDriver struct {
	mu       sync.Mutex
	events   *authRuntimeTaskEventLog
	openErr  error
	status   extractor.WebViewAuthStatus
	tokens   []extractor.AuthWebViewToken
	requests []extractor.WebViewAuthRequest
}

type authRuntimeTaskEventLog struct {
	mu     sync.Mutex
	events []string
}

type authRuntimeTaskSession struct{}

func TestAddUri_AuthRuntimePreflightsSourceBeforeExtractorResolve(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/preflight"
	targetURL := "https://fixture.invalid/file-preflight.bin"
	events := &authRuntimeTaskEventLog{}
	dispatcher := &authRuntimeTaskDispatcher{
		events: events,
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			sourceURL: {authRuntimeTaskSourceRequest(identity, manifest, sourceURL)},
		},
		resolutions: map[string][]extractor.AddTaskResolution{
			sourceURL: {authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)},
		},
	}
	driver := &authRuntimeTaskDriver{events: events}
	app, recorder, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)

	result := app.AddUri(sourceURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{targetURL}) {
		t.Fatalf("add URIs = %#v, want %#v", got, []string{targetURL})
	}
	if driver.openCount() != 1 {
		t.Fatalf("auth sessions = %d, want 1", driver.openCount())
	}
	if got := events.snapshot(); !reflect.DeepEqual(got[:3], []string{"plan:" + sourceURL, "auth:apr-alpha001", "resolve:" + sourceURL}) {
		t.Fatalf("event order = %#v, want source auth before resolve", got)
	}
	if _, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", targetURL); err != nil {
		t.Fatalf("ResolveAuthProfile() after source preflight error = %v", err)
	}
}

func TestAddUri_AuthRuntimeSourceMissingExpiredOpenWebViewBeforeResolve(t *testing.T) {
	for _, tt := range []struct {
		name string
		seed func(t *testing.T, store extractor.AuthProfileStore, packID string, targetURL string)
	}{
		{name: "missing profile"},
		{name: "expired profile", seed: func(t *testing.T, store extractor.AuthProfileStore, packID string, targetURL string) {
			past := time.Now().Add(-time.Hour)
			setAuthRuntimeTaskProfileWithOptions(t, store, packID, extractor.AuthSecretKindBearer, "expired-source-token", []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}}, &past)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bundle := syntheticRootPrivateAuthRuntimeBundle(t)
			identity := authRuntimeTaskIdentity(t, bundle)
			manifest := authRuntimeTaskManifest(identity)
			sourceURL := "https://fixture.invalid/d/source-refresh-" + strings.ReplaceAll(tt.name, " ", "-")
			targetURL := "https://fixture.invalid/file-source-refresh.bin"
			events := &authRuntimeTaskEventLog{}
			dispatcher := &authRuntimeTaskDispatcher{
				events: events,
				plans: map[string][]extractor.HostAuthRuntimeRequest{
					sourceURL: {authRuntimeTaskSourceRequest(identity, manifest, sourceURL)},
				},
				resolutions: map[string][]extractor.AddTaskResolution{
					sourceURL: {authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)},
				},
			}
			driver := &authRuntimeTaskDriver{events: events}
			app, recorder, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
			if tt.seed != nil {
				tt.seed(t, store, identity.PackID, targetURL)
			}

			result := app.AddUri(sourceURL)

			if result != "success" {
				t.Fatalf("AddUri() = %q, want success", result)
			}
			if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{targetURL}) {
				t.Fatalf("add URIs = %#v, want %#v", got, []string{targetURL})
			}
			if driver.openCount() != 1 {
				t.Fatalf("auth sessions = %d, want one source preflight refresh", driver.openCount())
			}
			assertAuthRuntimeTaskEventPrefix(t, events.snapshot(), []string{"plan:" + sourceURL, "auth:apr-alpha001", "resolve:" + sourceURL})
			if _, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", targetURL); err != nil {
				t.Fatalf("ResolveAuthProfile() after source refresh error = %v", err)
			}
		})
	}
}

func TestAddUri_AuthRuntimeTargetPreflightBeforeHeaders(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/target-preflight"
	targetURL := "https://fixture.invalid/file-target.bin"
	events := &authRuntimeTaskEventLog{}
	dispatcher := &authRuntimeTaskDispatcher{
		events: events,
		resolutions: map[string][]extractor.AddTaskResolution{
			sourceURL: {authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)},
		},
	}
	driver := &authRuntimeTaskDriver{events: events}
	app, _, _ := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)

	result := app.AddUri(sourceURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if driver.openCount() != 1 {
		t.Fatalf("auth sessions = %d, want target preflight provisioning", driver.openCount())
	}
	if got := events.snapshot(); !reflect.DeepEqual(got[:4], []string{"plan:" + sourceURL, "resolve:" + sourceURL, "auth:apr-alpha001", "build:" + targetURL}) {
		t.Fatalf("event order = %#v, want target auth before headers", got)
	}
}

func TestAddUri_AuthRuntimeAliasStoredAuthReuseSkipsWebViewWhenLoginAndTargetHostsDiffer(t *testing.T) {
	bundle := syntheticRootAliasAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskAliasManifest(identity)
	sourceURL := "https://share.alpha.test/d/alias-stored"
	targetURL := "https://files.alpha.test/files/alias-stored.bin"
	events := &authRuntimeTaskEventLog{}
	resolution := authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)
	resolution.Items[0].HostPolicy = authRuntimeTaskAliasHostPolicy(identity)
	dispatcher := &authRuntimeTaskDispatcher{
		events: events,
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			sourceURL: {authRuntimeTaskSourceRequest(identity, manifest, sourceURL)},
		},
		resolutions: map[string][]extractor.AddTaskResolution{
			sourceURL: {resolution},
		},
	}
	driver := &authRuntimeTaskDriver{events: events}
	app, recorder, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
	setAuthRuntimeTaskAliasProfile(t, store, identity.PackID, extractor.AuthSecretKindBearer, "stored-alias-task-token", []extractor.DomainRule{{Host: "files.alpha.test"}}, nil)

	result := app.AddUri(sourceURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, []string{"plan:" + sourceURL, "resolve:" + sourceURL, "build:" + targetURL}) {
		t.Fatalf("event order = %#v, want resolve/build without auth open", got)
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{targetURL}) {
		t.Fatalf("add URIs = %#v, want %#v", got, []string{targetURL})
	}
	if got := recorder.count("aria2.addUri"); got != 1 {
		t.Fatalf("aria2.addUri count = %d, want 1", got)
	}
	if driver.openCount() != 0 {
		t.Fatalf("auth sessions = %d, want 0 for stored alias auth reuse", driver.openCount())
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", targetURL)
	if err != nil {
		t.Fatalf("ResolveAuthProfile(target) error = %v", err)
	}
	if resolved.HeaderValue != "Bearer stored-alias-task-token" {
		t.Fatalf("ResolveAuthProfile(target) HeaderValue = %q", resolved.HeaderValue)
	}
	if _, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", "https://api.alpha.test/login"); err == nil {
		t.Fatal("ResolveAuthProfile(login host) error = nil, want target-scoped stored auth only")
	}
	assertAuthRuntimeTaskEventAbsent(t, events.snapshot(), "auth:apr-alpha001")
	assertRootNoSecretText(t, fmt.Sprintf("%#v %#v", result, events.snapshot()), "stored-alias-task-token", "Bearer stored-alias-task-token")
}

func TestAddUri_AuthRuntimeTargetStaleUnavailableClearsOpensWebViewAndBuildsHeaders(t *testing.T) {
	for _, tt := range []struct {
		name string
		seed func(t *testing.T, store extractor.AuthProfileStore, packID string)
	}{
		{name: "target domain mismatch", seed: func(t *testing.T, store extractor.AuthProfileStore, packID string) {
			setAuthRuntimeTaskProfileWithOptions(t, store, packID, extractor.AuthSecretKindBearer, "target-domain-token", []extractor.DomainRule{{Host: "other.fixture.invalid"}}, nil)
		}},
		{name: "target kind mismatch", seed: func(t *testing.T, store extractor.AuthProfileStore, packID string) {
			setAuthRuntimeTaskProfileWithOptions(t, store, packID, extractor.AuthSecretKindCookie, "sid=target-kind-token", []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}}, nil)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bundle := syntheticRootPrivateAuthRuntimeBundle(t)
			identity := authRuntimeTaskIdentity(t, bundle)
			manifest := authRuntimeTaskManifest(identity)
			sourceURL := "https://fixture.invalid/d/target-unavailable-" + strings.ReplaceAll(tt.name, " ", "-")
			targetURL := "https://fixture.invalid/file-target-unavailable.bin"
			events := &authRuntimeTaskEventLog{}
			dispatcher := &authRuntimeTaskDispatcher{
				events: events,
				resolutions: map[string][]extractor.AddTaskResolution{
					sourceURL: {authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)},
				},
			}
			driver := &authRuntimeTaskDriver{events: events}
			app, recorder, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
			tt.seed(t, store, identity.PackID)

			result := app.AddUri(sourceURL)

			if result != "success" {
				t.Fatalf("AddUri() = %q, want success", result)
			}
			if driver.openCount() != 1 {
				t.Fatalf("auth sessions = %d, want one target stale refresh", driver.openCount())
			}
			if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{targetURL}) {
				t.Fatalf("add URIs = %#v, want %#v", got, []string{targetURL})
			}
			if dispatcher.resolveCount(sourceURL) != 1 {
				t.Fatalf("Resolve() count = %d, want 1", dispatcher.resolveCount(sourceURL))
			}
			gotEvents := events.snapshot()
			wantPrefix := []string{"plan:" + sourceURL, "resolve:" + sourceURL, "auth:apr-alpha001", "build:" + targetURL}
			assertAuthRuntimeTaskEventPrefix(t, gotEvents, wantPrefix)
			if _, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", targetURL); err != nil {
				t.Fatalf("ResolveAuthProfile() after target stale refresh error = %v", err)
			}
		})
	}
}

func TestAddUri_AuthRuntimeProvisioningUnavailableFailsBeforeResolve(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/unavailable"
	events := &authRuntimeTaskEventLog{}
	dispatcher := &authRuntimeTaskDispatcher{
		events: events,
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			sourceURL: {authRuntimeTaskSourceRequest(identity, manifest, sourceURL)},
		},
	}
	driver := &authRuntimeTaskDriver{events: events, openErr: errors.New("Authorization: Bearer raw-secret token=raw-token")}
	app, recorder, _ := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)

	result := app.AddUri(sourceURL)

	if result != "auth profile unavailable" {
		t.Fatalf("AddUri() = %q, want generic auth unavailable", result)
	}
	if dispatcher.resolveCount(sourceURL) != 0 {
		t.Fatalf("Resolve() count = %d, want 0", dispatcher.resolveCount(sourceURL))
	}
	if got := recorder.count("aria2.addUri"); got != 0 {
		t.Fatalf("aria2.addUri count = %d, want 0", got)
	}
	assertRootNoSecretText(t, result, "raw-secret", "raw-token", "Authorization")
}

func TestAddUri_AuthRuntimeTargetPolicyDeniedDoesNotOpenWebViewOrBuildHeaders(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/policy-denied"
	targetURL := "https://fixture.invalid/file-policy-denied.bin"
	events := &authRuntimeTaskEventLog{}
	policy := extractor.ResolvedHostPolicy{
		AuthProfiles: []extractor.HostPolicyAuthProfileScope{{
			ProfileID: "apr-alpha001",
			Domains:   []extractor.DomainRule{{Host: "other.fixture.invalid"}},
		}},
	}
	resolution := authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)
	resolution.Items[0].HostPolicy = &policy
	dispatcher := &authRuntimeTaskDispatcher{
		events: events,
		resolutions: map[string][]extractor.AddTaskResolution{
			sourceURL: {resolution},
		},
	}
	driver := &authRuntimeTaskDriver{events: events}
	app, recorder, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
	setAuthRuntimeTaskProfileWithOptions(t, store, identity.PackID, extractor.AuthSecretKindCookie, "sid=policy-denied-stale", []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}}, nil)

	result := app.AddUri(sourceURL)

	if result != "auth profile unavailable" {
		t.Fatalf("AddUri() = %q, want generic auth unavailable", result)
	}
	if driver.openCount() != 0 {
		t.Fatalf("auth sessions = %d, want policy denial before WebView", driver.openCount())
	}
	if got := recorder.count("aria2.addUri"); got != 0 {
		t.Fatalf("aria2.addUri count = %d, want 0", got)
	}
	gotEvents := events.snapshot()
	if !reflect.DeepEqual(gotEvents, []string{"plan:" + sourceURL, "resolve:" + sourceURL}) {
		t.Fatalf("event order = %#v, want policy denial before auth/build", gotEvents)
	}
	assertAuthRuntimeTaskEventAbsent(t, gotEvents, "auth:apr-alpha001", "build:"+targetURL)
}

func TestAddUri_AuthRuntimeTargetPolicyDeniedDoesNotSubmitWithoutRuntime(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/policy-denied-no-runtime"
	targetURL := "https://fixture.invalid/file-policy-denied-no-runtime.bin"
	policy := extractor.ResolvedHostPolicy{
		AuthProfiles: []extractor.HostPolicyAuthProfileScope{{
			ProfileID: "apr-alpha001",
			Domains:   []extractor.DomainRule{{Host: "other.fixture.invalid"}},
		}},
	}
	resolution := authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)
	resolution.Items[0].HostPolicy = &policy
	dispatcher := &authRuntimeTaskDispatcher{
		resolutions: map[string][]extractor.AddTaskResolution{
			sourceURL: {resolution},
		},
	}
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)

	result := app.AddUri(sourceURL)

	if result != "auth profile unavailable" {
		t.Fatalf("AddUri() = %q, want generic auth unavailable", result)
	}
	if got := recorder.count("aria2.addUri"); got != 0 {
		t.Fatalf("aria2.addUri count = %d, want 0", got)
	}
	if got := dispatcher.resolveCount(sourceURL); got != 1 {
		t.Fatalf("Resolve() count = %d, want 1", got)
	}
}

func TestAddUri_AuthRuntimeTargetRefreshCancelDoesNotLoopOrSubmit(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/target-cancel"
	targetURL := "https://fixture.invalid/file-target-cancel.bin"
	events := &authRuntimeTaskEventLog{}
	dispatcher := &authRuntimeTaskDispatcher{
		events: events,
		resolutions: map[string][]extractor.AddTaskResolution{
			sourceURL: {authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)},
		},
	}
	driver := &authRuntimeTaskDriver{events: events, status: extractor.WebViewAuthStatusCanceled}
	app, recorder, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
	setAuthRuntimeTaskProfileWithOptions(t, store, identity.PackID, extractor.AuthSecretKindCookie, "sid=cancel-stale-secret", []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}}, nil)

	result := app.AddUri(sourceURL)

	if result != "auth profile unavailable" {
		t.Fatalf("AddUri() = %q, want generic auth unavailable", result)
	}
	if driver.openCount() != 1 {
		t.Fatalf("auth sessions = %d, want one bounded refresh attempt", driver.openCount())
	}
	if got := dispatcher.resolveCount(sourceURL); got != 1 {
		t.Fatalf("Resolve() count = %d, want no target resolve retry", got)
	}
	if got := recorder.count("aria2.addUri"); got != 0 {
		t.Fatalf("aria2.addUri count = %d, want 0", got)
	}
	gotEvents := events.snapshot()
	assertAuthRuntimeTaskEventPrefix(t, gotEvents, []string{"plan:" + sourceURL, "resolve:" + sourceURL, "auth:apr-alpha001"})
	assertAuthRuntimeTaskEventAbsent(t, gotEvents, "build:"+targetURL)
	assertRootNoSecretText(t, result, "cancel-stale-secret", "callback-secret", "Authorization")
}

func TestAddUri_AuthRuntimeGenericEmptyOutputRefreshesOnceAndRetries(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/refresh"
	targetURL := "https://fixture.invalid/file-refresh.bin"
	events := &authRuntimeTaskEventLog{}
	dispatcher := &authRuntimeTaskDispatcher{
		events: events,
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			sourceURL: {authRuntimeTaskSourceRequest(identity, manifest, sourceURL)},
		},
		resolveErrors: map[string][]error{
			sourceURL: {errors.New(addTaskGenericAuthResolutionError), nil},
		},
		resolutions: map[string][]extractor.AddTaskResolution{
			sourceURL: {authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)},
		},
	}
	driver := &authRuntimeTaskDriver{events: events}
	app, _, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
	setAuthRuntimeTaskProfile(t, store, identity.PackID, "stale-token-secret", targetURL)

	result := app.AddUri(sourceURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if dispatcher.resolveCount(sourceURL) != 2 {
		t.Fatalf("Resolve() count = %d, want 2", dispatcher.resolveCount(sourceURL))
	}
	if driver.openCount() != 1 {
		t.Fatalf("auth sessions = %d, want one refresh", driver.openCount())
	}
}

func TestAddUri_AuthRuntimeStaleUnavailableSourceProfileClearsOpensWebViewBeforeResolve(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/stale-source"
	targetURL := "https://fixture.invalid/file-stale-source.bin"
	events := &authRuntimeTaskEventLog{}
	dispatcher := &authRuntimeTaskDispatcher{
		events: events,
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			sourceURL: {authRuntimeTaskSourceRequest(identity, manifest, sourceURL)},
		},
		resolutions: map[string][]extractor.AddTaskResolution{
			sourceURL: {authRuntimeTaskResolution(sourceURL, targetURL, identity, manifest, true)},
		},
	}
	driver := &authRuntimeTaskDriver{events: events}
	app, recorder, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
	setAuthRuntimeTaskProfileWithOptions(t, store, identity.PackID, extractor.AuthSecretKindCookie, "sid=stale-source-token", []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}}, nil)

	result := app.AddUri(sourceURL)

	if result != "success" {
		t.Fatalf("AddUri() = %q, want success", result)
	}
	if dispatcher.resolveCount(sourceURL) != 1 {
		t.Fatalf("Resolve() count = %d, want source resolve once after refresh", dispatcher.resolveCount(sourceURL))
	}
	if driver.openCount() != 1 {
		t.Fatalf("auth sessions = %d, want stale unavailable source WebView", driver.openCount())
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{targetURL}) {
		t.Fatalf("add URIs = %#v, want %#v", got, []string{targetURL})
	}
	gotEvents := events.snapshot()
	assertAuthRuntimeTaskEventPrefix(t, gotEvents, []string{"plan:" + sourceURL, "auth:apr-alpha001", "resolve:" + sourceURL})
	resolved, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", targetURL)
	if err != nil {
		t.Fatalf("ResolveAuthProfile() after source stale refresh error = %v", err)
	}
	if resolved.Kind != extractor.AuthSecretKindBearer {
		t.Fatalf("resolved.Kind = %q, want bearer", resolved.Kind)
	}
}

func TestAddUri_AuthRuntimeGenericEmptyOutputWithoutLocalSourceAuthDoesNotRetry(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	sourceURL := "https://fixture.invalid/d/no-local-auth"
	dispatcher := &authRuntimeTaskDispatcher{
		resolveErrors: map[string][]error{
			sourceURL: {errors.New(addTaskGenericAuthResolutionError)},
		},
	}
	driver := &authRuntimeTaskDriver{}
	app, recorder, _ := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)

	result := app.AddUri(sourceURL)

	if !strings.Contains(result, "could not resolve this link") {
		t.Fatalf("AddUri() = %q, want generic resolver failure", result)
	}
	if dispatcher.resolveCount(sourceURL) != 1 {
		t.Fatalf("Resolve() count = %d, want 1", dispatcher.resolveCount(sourceURL))
	}
	if driver.openCount() != 0 {
		t.Fatalf("auth sessions = %d, want no refresh", driver.openCount())
	}
	if got := recorder.count("aria2.addUri"); got != 0 {
		t.Fatalf("aria2.addUri count = %d, want 0", got)
	}
}

func TestAddUri_AuthRuntimeDoesNotRefreshHardExtractorErrors(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/hard-error"
	targetURL := "https://fixture.invalid/file-hard.bin"
	dispatcher := &authRuntimeTaskDispatcher{
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			sourceURL: {authRuntimeTaskSourceRequest(identity, manifest, sourceURL)},
		},
		resolveErrors: map[string][]error{
			sourceURL: {errors.New("extractor pack returned invalid add item: item 0 url")},
		},
	}
	driver := &authRuntimeTaskDriver{}
	app, _, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
	setAuthRuntimeTaskProfile(t, store, identity.PackID, "stale-token-secret", targetURL)

	result := app.AddUri(sourceURL)

	if !strings.Contains(result, "invalid add item") {
		t.Fatalf("AddUri() = %q, want hard extractor error", result)
	}
	if dispatcher.resolveCount(sourceURL) != 1 {
		t.Fatalf("Resolve() count = %d, want 1", dispatcher.resolveCount(sourceURL))
	}
	if driver.openCount() != 0 {
		t.Fatalf("auth sessions = %d, want 0", driver.openCount())
	}
}

func TestBatchAddUri_AuthRuntimeProvisionsOncePerRuntimeEntry(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sources := []string{"https://fixture.invalid/d/one", "https://fixture.invalid/d/two"}
	targets := []string{"https://fixture.invalid/file-one.bin", "https://fixture.invalid/file-two.bin"}
	dispatcher := &authRuntimeTaskDispatcher{plans: make(map[string][]extractor.HostAuthRuntimeRequest), resolutions: make(map[string][]extractor.AddTaskResolution)}
	for i, source := range sources {
		dispatcher.plans[source] = []extractor.HostAuthRuntimeRequest{authRuntimeTaskSourceRequest(identity, manifest, source)}
		dispatcher.resolutions[source] = []extractor.AddTaskResolution{authRuntimeTaskResolution(source, targets[i], identity, manifest, true)}
	}
	driver := &authRuntimeTaskDriver{}
	app, _, _ := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)

	result := app.BatchAddUri(sources)

	assertBatchAddStrings(t, "succeeded", result.Succeeded, targets)
	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{})
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %#v", result.Errors)
	}
	if driver.openCount() != 1 {
		t.Fatalf("auth sessions = %d, want one shared provisioning", driver.openCount())
	}
}

func TestBatchAddUri_AuthRuntimeRefreshesOncePerRuntimeEntry(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	firstSource := "https://fixture.invalid/d/refresh-one"
	secondSource := "https://fixture.invalid/d/refresh-two"
	firstTarget := "https://fixture.invalid/file-refresh-one.bin"
	dispatcher := &authRuntimeTaskDispatcher{
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			firstSource:  {authRuntimeTaskSourceRequest(identity, manifest, firstSource)},
			secondSource: {authRuntimeTaskSourceRequest(identity, manifest, secondSource)},
		},
		resolveErrors: map[string][]error{
			firstSource:  {errors.New(addTaskGenericAuthResolutionError), nil},
			secondSource: {errors.New(addTaskGenericAuthResolutionError)},
		},
		resolutions: map[string][]extractor.AddTaskResolution{
			firstSource: {authRuntimeTaskResolution(firstSource, firstTarget, identity, manifest, true)},
		},
	}
	driver := &authRuntimeTaskDriver{}
	app, _, store := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)
	setAuthRuntimeTaskProfile(t, store, identity.PackID, "stale-token-secret", firstTarget)

	result := app.BatchAddUri([]string{firstSource, secondSource})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{firstTarget})
	if _, ok := result.Errors[secondSource]; !ok {
		t.Fatalf("expected second source generic auth error, got %#v", result.Errors)
	}
	if driver.openCount() != 1 {
		t.Fatalf("auth sessions = %d, want one shared refresh", driver.openCount())
	}
	if dispatcher.resolveCount(firstSource) != 2 || dispatcher.resolveCount(secondSource) != 1 {
		t.Fatalf("resolve counts first/second = %d/%d, want 2/1", dispatcher.resolveCount(firstSource), dispatcher.resolveCount(secondSource))
	}
}

func TestBatchAddUri_AuthRuntimePartialFailureDoesNotBlockNonAuthCandidates(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	authSource := "https://fixture.invalid/d/fail-auth"
	directURL := "https://example.test/public.bin"
	dispatcher := &authRuntimeTaskDispatcher{
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			authSource: {authRuntimeTaskSourceRequest(identity, manifest, authSource)},
		},
	}
	driver := &authRuntimeTaskDriver{openErr: errors.New("Cookie: sid=raw-cookie token=raw-token")}
	app, recorder, _ := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)

	result := app.BatchAddUri([]string{authSource, directURL})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{directURL})
	if _, ok := result.Errors[authSource]; !ok {
		t.Fatalf("expected auth source error, got %#v", result.Errors)
	}
	assertRootNoSecretText(t, result.Errors[authSource], "raw-cookie", "raw-token", "Cookie")
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{directURL}) {
		t.Fatalf("add URIs = %#v, want direct non-auth success", got)
	}
}

func TestBatchAddUri_NonAuthenticatedCandidatesUnaffectedByAuthRuntime(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	shareURL := "https://fixture.invalid/d/non-auth"
	targetURL := "https://fixture.invalid/non-auth.bin"
	dispatcher := &authRuntimeTaskDispatcher{
		resolutions: map[string][]extractor.AddTaskResolution{
			shareURL: {authRuntimeTaskResolution(shareURL, targetURL, identity, manifest, false)},
		},
	}
	driver := &authRuntimeTaskDriver{}
	app, recorder, _ := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)

	result := app.BatchAddUri([]string{shareURL, targetURL})

	assertBatchAddStrings(t, "succeeded", result.Succeeded, []string{targetURL})
	assertBatchAddStrings(t, "duplicates", result.Duplicates, []string{targetURL})
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %#v", result.Errors)
	}
	if driver.openCount() != 0 {
		t.Fatalf("auth sessions = %d, want 0", driver.openCount())
	}
	if got := recorder.addURIsSnapshot(); !reflect.DeepEqual(got, []string{targetURL}) {
		t.Fatalf("add URIs = %#v, want unchanged non-auth add", got)
	}
}

func TestAddUri_AuthRuntimeErrorsAreRedacted(t *testing.T) {
	bundle := syntheticRootPrivateAuthRuntimeBundle(t)
	identity := authRuntimeTaskIdentity(t, bundle)
	manifest := authRuntimeTaskManifest(identity)
	sourceURL := "https://fixture.invalid/d/redact?token=query-secret"
	directURL := "https://example.test/redact-public.bin"
	dispatcher := &authRuntimeTaskDispatcher{
		plans: map[string][]extractor.HostAuthRuntimeRequest{
			sourceURL: {authRuntimeTaskSourceRequest(identity, manifest, sourceURL)},
		},
	}
	driver := &authRuntimeTaskDriver{openErr: errors.New("Authorization: Bearer raw-secret Cookie: sid=raw-cookie token=raw-token C:/private/path")}
	app, _, _ := setupAuthRuntimeTaskApp(t, dispatcher, bundle, driver)

	result := app.AddUri(sourceURL)
	assertRootNoSecretText(t, result, "raw-secret", "raw-cookie", "raw-token", "query-secret", "Authorization", "Cookie", "C:/private/path")

	batch := app.BatchAddUri([]string{sourceURL, directURL})
	if _, ok := batch.Errors[sourceURL]; !ok {
		t.Fatalf("expected auth source error, got %#v", batch.Errors)
	}
	assertRootNoSecretText(t, batch.Errors[sourceURL], "raw-secret", "raw-cookie", "raw-token", "query-secret", "Authorization", "Cookie", "C:/private/path")
}

func setupAuthRuntimeTaskApp(t *testing.T, dispatcher extractorAddTaskDispatcher, bundle *extractor.PrivateAuthRuntimeBundle, driver *authRuntimeTaskDriver) (*App, *extractorRPCRecorder, *extractor.FileAuthProfileStore) {
	t.Helper()
	store := newRootTempAuthProfileStore(t)
	coordinator := extractor.NewWebViewAuthCoordinator(store, driver)
	runtime := extractor.NewHostAuthRuntime(extractor.HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: coordinator})
	app, recorder := setupAppTaskExtractorTest(t, batchAddRPCSnapshots{}, dispatcher)
	app.setHostAuthState(store, runtime, driver)

	return app, recorder, store
}

func authRuntimeTaskIdentity(t *testing.T, bundle *extractor.PrivateAuthRuntimeBundle) extractor.VerifiedPackIdentity {
	t.Helper()
	identities := bundle.PackIdentities()
	if len(identities) != 1 {
		t.Fatalf("bundle identities = %#v, want one", identities)
	}

	return identities[0]
}

func authRuntimeTaskManifest(identity extractor.VerifiedPackIdentity) extractor.Manifest {
	return extractor.Manifest{
		PackID:       identity.PackID,
		PackVersion:  identity.PackVersion,
		ABIVersion:   extractor.CurrentABIVersion,
		Capabilities: []extractor.Capability{extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile},
		Domains:      []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
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

func authRuntimeTaskSourceRequest(identity extractor.VerifiedPackIdentity, manifest extractor.Manifest, sourceURL string) extractor.HostAuthRuntimeRequest {
	return extractor.HostAuthRuntimeRequest{PackIdentity: identity, Manifest: manifest, SourceURL: sourceURL}
}

func authRuntimeTaskResolution(sourceURL string, targetURL string, identity extractor.VerifiedPackIdentity, manifest extractor.Manifest, protected bool) extractor.AddTaskResolution {
	item := extractor.ResolvedAddItem{
		SourceURL:    sourceURL,
		PackID:       identity.PackID,
		PackIdentity: identity,
		Manifest:     manifest,
		ID:           "item-1",
		URL:          targetURL,
		Filename:     "file.bin",
		SizeBytes:    1024,
	}
	if protected {
		item.AuthProfileRef = "apr-alpha001"
	}

	return extractor.AddTaskResolution{Matched: true, SourceURL: sourceURL, PackID: identity.PackID, Items: []extractor.ResolvedAddItem{item}}
}

func setAuthRuntimeTaskProfile(t *testing.T, store extractor.AuthProfileStore, packID string, secret string, targetURL string) {
	t.Helper()
	setAuthRuntimeTaskProfileWithOptions(t, store, packID, extractor.AuthSecretKindBearer, secret, []extractor.DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}}, nil)
	if _, err := store.ResolveAuthProfile(context.Background(), packID, "apr-alpha001", targetURL); err != nil {
		t.Fatalf("ResolveAuthProfile() seeded profile error = %v", err)
	}
}

func setAuthRuntimeTaskProfileWithOptions(t *testing.T, store extractor.AuthProfileStore, packID string, kind extractor.AuthSecretKind, secret string, domains []extractor.DomainRule, expiresAt *time.Time) {
	t.Helper()
	_, err := store.SetAuthProfile(context.Background(), extractor.AuthProfileUpdate{
		PackID:         packID,
		ProfileID:      "apr-alpha001",
		Kind:           kind,
		Secret:         secret,
		AllowedDomains: domains,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}
}

func syntheticRootAliasAuthRuntimeBundle(t *testing.T) *extractor.PrivateAuthRuntimeBundle {
	t.Helper()
	runtimeRaw := []byte(`{"packs":[{"verified_pack_identity":{"pack_id":"xpk-alpha001","pack_version":"opaque-1","asset_sha256":"` + strings.Repeat("1", 64) + `","manifest_sha256":"` + strings.Repeat("2", 64) + `","payload_sha256":"` + strings.Repeat("3", 64) + `","signature_sha256":"` + strings.Repeat("4", 64) + `","public_key_sha256":"` + strings.Repeat("5", 64) + `"},"store_binding":{"scope":"pack","profile_refs":["apr-alpha001"]},"profiles":[{"profile_ref":"apr-alpha001","kind":"bearer","login":{"url":"https://api.alpha.test/login","allowed_domains":[{"host":"api.alpha.test"}],"timeout_millis":30000,"callback_transport":{"mode":"local_post","content_types":["application/json"],"max_body_bytes":16384},"collector_js":"(() => { return function(ctx, postCapture) { return ctx && postCapture; }; })();","capture":{"format":"json","secret_candidates":["secret","capture.secret"],"kind_field":"kind","expires_at_field":"expires_at","redacted_display_field":"redacted_display"}}}],"preflight":{"mode":"required","missing":"refresh","expired":"refresh"},"provisioning":{"mode":"webview","profile_refs":["apr-alpha001"]},"materialization":{"profile_refs":["apr-alpha001"]},"normalization":{"reject_crlf":true,"trim_space":true}}]}`)
	envelope := []byte(`{"schema_version":1,"bundle_id":"arb-alpha001","bundle_version":"opaque-1","auth_runtime_private_sha256":"` + sha256HexForAppHostAuthTest(runtimeRaw) + `","runtime":` + string(runtimeRaw) + `}`)
	bundle, err := extractor.NewPrivateAuthRuntimeBundle(envelope, extractor.PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}

	return bundle
}

func authRuntimeTaskAliasManifest(identity extractor.VerifiedPackIdentity) extractor.Manifest {
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

func authRuntimeTaskAliasHostPolicy(identity extractor.VerifiedPackIdentity) *extractor.ResolvedHostPolicy {
	policy := extractor.ResolvedHostPolicy{
		PolicyID:            "pol-alpha001",
		PolicyVersion:       "2026.05.15-alpha",
		PolicySHA256:        strings.Repeat("c", 64),
		PackIdentity:        identity,
		DomainPolicyRefs:    []string{"dpr-alpha001"},
		BrokerPolicyRefs:    []string{"bpr-alpha001"},
		AllowedCapabilities: []extractor.Capability{extractor.CapabilityHTTPFetch, extractor.CapabilityAuthProfile},
		IngressDomains:      []extractor.DomainRule{{Host: "share.alpha.test"}},
		BrokerDomains:       []extractor.DomainRule{{Host: "api.alpha.test"}},
		OutputDomains:       []extractor.HostPolicyOutputRule{{Host: "files.alpha.test", PathPrefixes: []string{"/files/"}}},
		AuthProfiles: []extractor.HostPolicyAuthProfileScope{{
			ProfileID: "apr-alpha001",
			Domains:   []extractor.DomainRule{{Host: "files.alpha.test"}},
		}},
		BrokerEndpoints: []extractor.HostPolicyBrokerEndpoint{{
			BrokerPolicyRef: "bpr-alpha001",
			EndpointRef:     "epr-alpha001",
			URLTemplate:     "https://api.alpha.test/resource/{id}",
			Methods:         []string{"GET"},
			AuthProfileRefs: []string{"apr-alpha001"},
		}},
	}

	return &policy
}

func setAuthRuntimeTaskAliasProfile(t *testing.T, store extractor.AuthProfileStore, packID string, kind extractor.AuthSecretKind, secret string, domains []extractor.DomainRule, expiresAt *time.Time) {
	t.Helper()
	_, err := store.SetAuthProfile(context.Background(), extractor.AuthProfileUpdate{
		PackID:         packID,
		ProfileID:      "apr-alpha001",
		Kind:           kind,
		Secret:         secret,
		AllowedDomains: domains,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}
}

func (d *authRuntimeTaskDispatcher) AuthRuntimeRequestsForSource(ctx context.Context, rawURL string) ([]extractor.HostAuthRuntimeRequest, error) {
	d.record("plan:" + rawURL)
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]extractor.HostAuthRuntimeRequest(nil), d.plans[rawURL]...), nil
}

func (d *authRuntimeTaskDispatcher) Resolve(ctx context.Context, rawURL string) (extractor.AddTaskResolution, error) {
	d.record("resolve:" + rawURL)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.resolveCounts == nil {
		d.resolveCounts = make(map[string]int)
	}
	d.resolveCounts[rawURL]++
	resolveHadScript := len(d.resolveErrors[rawURL]) > 0
	if resolveHadScript {
		err := d.resolveErrors[rawURL][0]
		d.resolveErrors[rawURL] = d.resolveErrors[rawURL][1:]
		if err != nil {
			return extractor.AddTaskResolution{}, err
		}
	}
	if len(d.resolutions[rawURL]) == 0 {
		return extractor.AddTaskResolution{SourceURL: rawURL}, nil
	}
	resolution := d.resolutions[rawURL][0]
	if !resolveHadScript || len(d.resolutions[rawURL]) > 1 {
		d.resolutions[rawURL] = d.resolutions[rawURL][1:]
	}

	return resolution, nil
}

func (d *authRuntimeTaskDispatcher) BuildAria2Headers(ctx context.Context, item extractor.ResolvedAddItem) ([]string, error) {
	d.record("build:" + item.URL)
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.headers[item.URL]...), nil
}

func (d *authRuntimeTaskDispatcher) resolveCount(rawURL string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.resolveCounts[rawURL]
}

func (d *authRuntimeTaskDispatcher) record(event string) {
	if d.events != nil {
		d.events.add(event)
	}
}

func (d *authRuntimeTaskDriver) OpenAuthSession(ctx context.Context, request extractor.WebViewAuthRequest, sink extractor.AuthWebViewSink) (extractor.AuthWebViewSession, error) {
	d.mu.Lock()
	if d.openErr != nil {
		err := d.openErr
		d.mu.Unlock()
		return nil, err
	}
	d.requests = append(d.requests, request)
	index := len(d.requests) - 1
	token := extractor.AuthWebViewToken{Kind: request.Kind, Secret: fmt.Sprintf("captured-token-%d", index+1), RedactedDisplay: "captured bearer"}
	if index < len(d.tokens) {
		token = d.tokens[index]
	}
	status := d.status
	if status == "" {
		status = extractor.WebViewAuthStatusSuccess
	}
	events := d.events
	d.mu.Unlock()
	if events != nil {
		events.add("auth:" + string(request.ProfileID))
	}
	switch status {
	case extractor.WebViewAuthStatusSuccess:
		if sink.OnSuccess != nil {
			sink.OnSuccess(token)
		}
	case extractor.WebViewAuthStatusCanceled:
		if sink.OnCancel != nil {
			sink.OnCancel()
		}
	case extractor.WebViewAuthStatusError:
		if sink.OnError != nil {
			sink.OnError(errors.New("auth webview failed with Authorization: Bearer callback-secret"))
		}
	case extractor.WebViewAuthStatusTimeout:
	default:
		if sink.OnSuccess != nil {
			sink.OnSuccess(token)
		}
	}

	return authRuntimeTaskSession{}, nil
}

func (d *authRuntimeTaskDriver) openCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return len(d.requests)
}

func (authRuntimeTaskSession) Close() error { return nil }

func (l *authRuntimeTaskEventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *authRuntimeTaskEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.events...)
}

func assertAuthRuntimeTaskEventPrefix(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) < len(want) || !reflect.DeepEqual(got[:len(want)], want) {
		t.Fatalf("event order = %#v, want prefix %#v", got, want)
	}
}

func assertAuthRuntimeTaskEventAbsent(t *testing.T, got []string, forbidden ...string) {
	t.Helper()
	for _, event := range got {
		for _, deny := range forbidden {
			if event == deny {
				t.Fatalf("event order = %#v, should not contain %q", got, deny)
			}
		}
	}
}

var (
	_ extractorAddTaskDispatcher        = (*authRuntimeTaskDispatcher)(nil)
	_ extractorAuthRuntimeSourcePlanner = (*authRuntimeTaskDispatcher)(nil)
	_ extractor.AuthWebViewDriver       = (*authRuntimeTaskDriver)(nil)
	_ extractor.AuthWebViewSession      = authRuntimeTaskSession{}
)
