package extractor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHostAuthRuntimePreflightMatchesSourceURLAndBundleIdentity(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	baseStore := newTempAuthProfileStore(t)
	store := &countingAuthProfileStore{AuthProfileStore: baseStore}
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "captured-token"})
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:      bundle,
		Store:       store,
		Coordinator: NewWebViewAuthCoordinator(store, driver),
	})
	request := hostAuthRuntimeRequest(identity)

	result, err := runtime.Preflight(context.Background(), request)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if !result.Matched || !result.Required || result.Available || !result.Refreshable {
		t.Fatalf("Preflight() result = %#v, want missing refreshable match", result)
	}
	if len(result.ProfileStatuses) != 1 || result.ProfileStatuses[0].Status != HostAuthRuntimeProfileMissing || !result.ProfileStatuses[0].Refreshable {
		t.Fatalf("ProfileStatuses = %#v", result.ProfileStatuses)
	}
	if store.SnapshotCalls() != 1 {
		t.Fatalf("SnapshotCalls() = %d, want 1", store.SnapshotCalls())
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions during preflight", driver.OpenCount())
	}

	denied := request
	denied.SourceURL = "https://denied.example.test/path"
	deniedResult, err := runtime.Preflight(context.Background(), denied)
	if err != nil {
		t.Fatalf("Preflight(denied source) error = %v", err)
	}
	if deniedResult.Matched || !deniedResult.Available || deniedResult.Required {
		t.Fatalf("Preflight(denied source) = %#v, want unmatched default-safe result", deniedResult)
	}
	if store.SnapshotCalls() != 1 {
		t.Fatalf("denied source consulted store; calls = %d", store.SnapshotCalls())
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions for denied preflight", driver.OpenCount())
	}
}

func TestHostAuthRuntimePreflightStatusesAvailableMissingExpiredUnavailable(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	profiles := []hostAuthRuntimeProfileFixture{
		{ProfileRef: "apr-bearer001", Kind: AuthSecretKindBearer},
		{ProfileRef: "apr-cookie001", Kind: AuthSecretKindCookie},
		{ProfileRef: "apr-missing001", Kind: AuthSecretKindBearer},
		{ProfileRef: "apr-expired001", Kind: AuthSecretKindBearer},
		{ProfileRef: "apr-kind001", Kind: AuthSecretKindBearer},
		{ProfileRef: "apr-domain001", Kind: AuthSecretKindBearer},
	}
	bundle := newHostAuthRuntimeBundle(t, identity, profiles, nil)
	store := newTempAuthProfileStore(t)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	setHostAuthProfile(t, store, identity.PackID, "apr-bearer001", AuthSecretKindBearer, "bearer-status-token", []DomainRule{{Host: "fixture.invalid"}}, &future)
	setHostAuthProfile(t, store, identity.PackID, "apr-cookie001", AuthSecretKindCookie, "sid=cookie-status-secret", []DomainRule{{Host: "fixture.invalid"}}, &future)
	setHostAuthProfile(t, store, identity.PackID, "apr-expired001", AuthSecretKindBearer, "expired-status-token", []DomainRule{{Host: "fixture.invalid"}}, &past)
	setHostAuthProfile(t, store, identity.PackID, "apr-kind001", AuthSecretKindCookie, "sid=kind-status-secret", []DomainRule{{Host: "fixture.invalid"}}, &future)
	setHostAuthProfile(t, store, identity.PackID, "apr-domain001", AuthSecretKindBearer, "domain-status-token", []DomainRule{{Host: "example.test"}}, &future)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "captured-status-token"})
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:      bundle,
		Store:       store,
		Coordinator: NewWebViewAuthCoordinator(store, driver),
		Now:         func() time.Time { return now },
	})

	result, err := runtime.Preflight(context.Background(), hostAuthRuntimeRequest(identity))
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if result.Available || !result.Refreshable {
		t.Fatalf("Preflight() = %#v, want unavailable but refreshable due missing/expired", result)
	}
	statuses := hostAuthRuntimeStatusMap(result)
	want := map[AuthProfileID]HostAuthRuntimeProfileStatus{
		"apr-bearer001":  HostAuthRuntimeProfileAvailable,
		"apr-cookie001":  HostAuthRuntimeProfileAvailable,
		"apr-missing001": HostAuthRuntimeProfileMissing,
		"apr-expired001": HostAuthRuntimeProfileExpired,
		"apr-kind001":    HostAuthRuntimeProfileUnavailable,
		"apr-domain001":  HostAuthRuntimeProfileUnavailable,
	}
	for ref, wantStatus := range want {
		if statuses[ref].Status != wantStatus {
			t.Fatalf("status[%s] = %q, want %q (all: %#v)", ref, statuses[ref].Status, wantStatus, result.ProfileStatuses)
		}
	}
	if !statuses["apr-missing001"].Refreshable || !statuses["apr-expired001"].Refreshable {
		t.Fatalf("missing/expired profiles should be refreshable: %#v", result.ProfileStatuses)
	}
	if !statuses["apr-kind001"].Refreshable || !statuses["apr-domain001"].Refreshable {
		t.Fatalf("recoverable stale unavailable profiles should be refreshable: %#v", result.ProfileStatuses)
	}
}

func TestHostAuthRuntimeStoredProfileIdentityMismatchIsInvisible(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, "xpk-alpha002", "apr-alpha001", AuthSecretKindBearer, "wrong-pack-token", []DomainRule{{Host: "fixture.invalid"}}, nil)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha002", AuthSecretKindBearer, "wrong-profile-token", []DomainRule{{Host: "fixture.invalid"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store})
	request := hostAuthRuntimeRequest(identity)
	request.ProfileRef = "apr-alpha001"

	result, err := runtime.Preflight(context.Background(), request)
	if err != nil {
		t.Fatalf("store_identity_mismatch: Preflight() error = %v", err)
	}
	if !result.Matched || result.Available || len(result.ProfileStatuses) != 1 || result.ProfileStatuses[0].Status != HostAuthRuntimeProfileMissing {
		t.Fatalf("store_identity_mismatch: result = %#v", result)
	}
	_, err = runtime.MaterializeAuthProfile(context.Background(), request)
	if err == nil {
		t.Fatal("store_identity_mismatch: MaterializeAuthProfile() error = nil")
	}
	formatted := fmt.Sprintf("%#v %v", result, err)
	assertNoForbiddenSubstrings(t, formatted, "wrong-pack-token", "wrong-profile-token")
}

func TestHostAuthRuntimeEnforcesStoreBindingAndMaterializationRefs(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{
		{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer},
		{ProfileRef: "apr-storeonly001", Kind: AuthSecretKindBearer},
	}, func(pack map[string]any) {
		pack["store_binding"].(map[string]any)["profile_refs"] = []string{"apr-alpha001", "apr-storeonly001"}
		pack["materialization"].(map[string]any)["profile_refs"] = []string{"apr-alpha001"}
		pack["provisioning"].(map[string]any)["profile_refs"] = []string{"apr-alpha001"}
	})
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "bound-token-secret", []DomainRule{{Host: "fixture.invalid"}}, nil)
	setHostAuthProfile(t, store, identity.PackID, "apr-storeonly001", AuthSecretKindBearer, "store-only-token-secret", []DomainRule{{Host: "fixture.invalid"}}, nil)
	setHostAuthProfile(t, store, identity.PackID, "apr-outside001", AuthSecretKindBearer, "outside-token-secret", []DomainRule{{Host: "fixture.invalid"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store})

	storeOnly := hostAuthRuntimeRequest(identity)
	storeOnly.ProfileRef = "apr-storeonly001"
	result, err := runtime.Preflight(context.Background(), storeOnly)
	if err != nil {
		t.Fatalf("Preflight(store-only) error = %v", err)
	}
	if !result.Matched || result.Available || len(result.ProfileStatuses) != 1 || result.ProfileStatuses[0].Status != HostAuthRuntimeProfileUnavailable {
		t.Fatalf("Preflight(store-only) = %#v, want fail-closed unavailable", result)
	}
	if _, err := runtime.ResolveAuthProfile(context.Background(), identity.PackID, "apr-storeonly001", storeOnly.TargetURL); err == nil {
		t.Fatal("ResolveAuthProfile(store-only) error = nil, want fail-closed")
	}
	if _, err := runtime.MaterializeAuthProfile(context.Background(), storeOnly); err == nil {
		t.Fatal("MaterializeAuthProfile(store-only) error = nil, want fail-closed")
	}

	outside := hostAuthRuntimeRequest(identity)
	outside.ProfileRef = "apr-outside001"
	result, err = runtime.Preflight(context.Background(), outside)
	if err != nil {
		t.Fatalf("Preflight(outside) error = %v", err)
	}
	if result.Available || result.ProfileStatuses[0].Status != HostAuthRuntimeProfileUnavailable {
		t.Fatalf("Preflight(outside) = %#v, want unavailable", result)
	}
	if _, err := runtime.ResolveAuthProfile(context.Background(), identity.PackID, "apr-outside001", outside.TargetURL); err == nil {
		t.Fatal("ResolveAuthProfile(outside) error = nil, want fail-closed")
	}
	if _, err := runtime.MaterializeAuthProfile(context.Background(), outside); err == nil {
		t.Fatal("MaterializeAuthProfile(outside) error = nil, want fail-closed")
	}
}

func TestHostAuthRuntimeResolvesOpaqueDirectStorage(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{
		{ProfileRef: "apr-bearer001", Kind: AuthSecretKindBearer},
		{ProfileRef: "apr-cookie001", Kind: AuthSecretKindCookie},
	}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-bearer001", AuthSecretKindBearer, "direct-bearer-secret", []DomainRule{{Host: "fixture.invalid"}}, nil)
	setHostAuthProfile(t, store, identity.PackID, "apr-cookie001", AuthSecretKindCookie, "sid=direct-cookie-secret; refresh=second-cookie-secret", []DomainRule{{Host: "fixture.invalid"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store})

	bearer, err := runtime.ResolveAuthProfile(context.Background(), identity.PackID, "apr-bearer001", "https://fixture.invalid/file")
	if err != nil {
		t.Fatalf("ResolveAuthProfile(bearer) error = %v", err)
	}
	if bearer.HeaderName != "Authorization" || bearer.HeaderValue != "Bearer direct-bearer-secret" || bearer.Kind != AuthSecretKindBearer {
		t.Fatalf("bearer resolved = %#v", bearer)
	}
	cookie, err := runtime.ResolveAuthProfile(context.Background(), identity.PackID, "apr-cookie001", "https://fixture.invalid/file")
	if err != nil {
		t.Fatalf("ResolveAuthProfile(cookie) error = %v", err)
	}
	if cookie.HeaderName != "Cookie" || cookie.HeaderValue != "sid=direct-cookie-secret; refresh=second-cookie-secret" || cookie.Kind != AuthSecretKindCookie {
		t.Fatalf("cookie resolved = %#v", cookie)
	}

	bearerReq := hostAuthRuntimeRequest(identity)
	bearerReq.ProfileRef = "apr-bearer001"
	material, err := runtime.MaterializeAuthProfile(context.Background(), bearerReq)
	if err != nil {
		t.Fatalf("MaterializeAuthProfile(bearer) error = %v", err)
	}
	formatted := fmt.Sprintf("%v %#v %+v", material, material, material)
	assertNoForbiddenSubstrings(t, formatted, "direct-bearer-secret", "Bearer direct-bearer-secret")

	cookieReq := hostAuthRuntimeRequest(identity)
	cookieReq.ProfileRef = "apr-cookie001"
	cookieMaterial, err := runtime.MaterializeAuthProfile(context.Background(), cookieReq)
	if err != nil {
		t.Fatalf("MaterializeAuthProfile(cookie) error = %v", err)
	}
	formatted = fmt.Sprintf("%v %#v %+v", cookieMaterial, cookieMaterial, cookieMaterial)
	assertNoForbiddenSubstrings(t, formatted, "sid=direct-cookie-secret", "direct-cookie-secret", "refresh=second-cookie-secret", "second-cookie-secret")

	result, err := runtime.Preflight(context.Background(), hostAuthRuntimeRequest(identity))
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	formatted = fmt.Sprintf("%v %#v %+v", result, result, result)
	assertNoForbiddenSubstrings(t, formatted, "direct-bearer-secret", "direct-cookie-secret", "second-cookie-secret", "Authorization: Bearer direct-bearer-secret", "Cookie: sid=direct-cookie-secret")
}

func TestHostAuthRuntimeEnsureProvisionsViaWebViewCoordinator(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, TimeoutMillis: 45000}}, nil)
	store := newTempAuthProfileStore(t)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "captured-runtime-token"})
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:      bundle,
		Store:       store,
		Coordinator: NewWebViewAuthCoordinator(store, driver),
	})

	result, err := runtime.Ensure(context.Background(), hostAuthRuntimeRequest(identity))
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if !result.Provisioned || !result.Available {
		t.Fatalf("Ensure() = %#v, want provisioned available", result)
	}
	requests := driver.Requests()
	if len(requests) != 1 {
		t.Fatalf("driver requests = %d, want 1", len(requests))
	}
	opened := requests[0]
	if opened.PackID != identity.PackID || opened.ProfileID != "apr-alpha001" || opened.Kind != AuthSecretKindBearer || opened.LoginURL != "https://fixture.invalid/login" {
		t.Fatalf("opened request = %#v", opened)
	}
	if len(opened.AllowedDomains) != 1 || opened.AllowedDomains[0].Host != "fixture.invalid" || opened.Timeout != 45*time.Second {
		t.Fatalf("opened request domain/timeout = %#v", opened)
	}
	if opened.CallbackTransport.Mode != "local_post" || opened.CallbackTransport.MaxBodyBytes != 16384 || len(opened.CallbackTransport.ContentTypes) != 1 || opened.CollectorJS == "" {
		t.Fatalf("opened request callback metadata = %#v collector=%q", opened.CallbackTransport, opened.CollectorJS)
	}
	if opened.Capture.Format != "json" || len(opened.Capture.SecretCandidates) != 2 || !opened.Capture.TrimSpace || !opened.Capture.RejectCRLF {
		t.Fatalf("opened request capture metadata = %#v", opened.Capture)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", "https://fixture.invalid/file")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() after Ensure error = %v", err)
	}
	if resolved.HeaderValue != "Bearer captured-runtime-token" {
		t.Fatalf("resolved HeaderValue = %q, want captured token", resolved.HeaderValue)
	}
}

func TestHostAuthRuntimeRecoverableUnavailableClearsBeforeProvision(t *testing.T) {
	for _, tt := range []struct {
		name       string
		kind       AuthSecretKind
		secret     string
		domains    []DomainRule
		wantStatus HostAuthRuntimeProfileStatus
	}{
		{
			name:       "wrong kind",
			kind:       AuthSecretKindCookie,
			secret:     "sid=stale-kind-secret",
			domains:    []DomainRule{{Host: "fixture.invalid"}},
			wantStatus: HostAuthRuntimeProfileUnavailable,
		},
		{
			name:       "domain mismatch",
			kind:       AuthSecretKindBearer,
			secret:     "stale-domain-secret",
			domains:    []DomainRule{{Host: "api.fixture.invalid"}},
			wantStatus: HostAuthRuntimeProfileUnavailable,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
			bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
			store := newTempAuthProfileStore(t)
			setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", tt.kind, tt.secret, tt.domains, nil)
			driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "fresh-recoverable-secret", RedactedDisplay: "fresh bearer"})
			runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
				Bundle:      bundle,
				Store:       store,
				Coordinator: NewWebViewAuthCoordinator(store, driver),
			})
			request := hostAuthRuntimeRequest(identity)
			request.ProfileRef = "apr-alpha001"

			preflight, err := runtime.Preflight(context.Background(), request)
			if err != nil {
				t.Fatalf("Preflight() error = %v", err)
			}
			if preflight.Available || !preflight.Refreshable || len(preflight.ProfileStatuses) != 1 || preflight.ProfileStatuses[0].Status != tt.wantStatus || !preflight.ProfileStatuses[0].Refreshable {
				t.Fatalf("Preflight() = %#v, want refreshable stale unavailable", preflight)
			}

			result, err := runtime.RefreshOnRecoverablePreflightFailure(context.Background(), request, NewHostAuthRuntimeBatchGuard())
			if err != nil {
				t.Fatalf("RefreshOnRecoverablePreflightFailure() error = %v", err)
			}
			if !result.Provisioned || !result.Available || driver.OpenCount() != 1 {
				t.Fatalf("refresh result=%#v opens=%d, want single provisioned refresh", result, driver.OpenCount())
			}
			post, err := runtime.Preflight(context.Background(), request)
			if err != nil || !post.Available {
				t.Fatalf("post Preflight() = %#v err=%v, want available", post, err)
			}
			material, err := runtime.MaterializeAuthProfile(context.Background(), request)
			if err != nil {
				t.Fatalf("MaterializeAuthProfile() error = %v", err)
			}
			if material.Kind != AuthSecretKindBearer || material.HeaderName != "Authorization" || material.RedactedDisplay == "" {
				t.Fatalf("materialized public-safe shape = %#v", material)
			}
			resolved, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", request.TargetURL)
			if err != nil {
				t.Fatalf("ResolveAuthProfile() after refresh error = %v", err)
			}
			if resolved.Kind != AuthSecretKindBearer || resolved.HeaderValue != "Bearer fresh-recoverable-secret" {
				t.Fatalf("resolved = %#v, want fresh bearer", resolved)
			}
			formatted := fmt.Sprintf("%#v %#v %#v", preflight, result, material)
			assertNoForbiddenSubstrings(t, formatted, tt.secret, "fresh-recoverable-secret", "Bearer fresh-recoverable-secret")
		})
	}
}

func TestHostAuthRuntimeAliasProvisioningPolicyDeniedBeforeDriverOpenClassifiesBoundary(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	manifest := hostAuthRuntimeAliasManifest(identity)
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://api.alpha.test/login"}}, nil)
	store := newTempAuthProfileStore(t)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "must-not-store"})
	resolver := &hostAuthRuntimePolicyResolver{policy: hostAuthRuntimeAliasPolicy(identity, func(policy *ResolvedHostPolicy) {
		policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: "apr-alpha001", Domains: []DomainRule{{Host: "files.alpha.test"}}}}
	})}
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:             bundle,
		Store:              store,
		Coordinator:        NewWebViewAuthCoordinator(store, driver),
		HostPolicyResolver: resolver,
	})
	request := HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://api.alpha.test/files/fixture-item",
		ProfileRef:   "apr-alpha001",
	}

	preflight, err := runtime.Preflight(context.Background(), request)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if preflight.Available || preflight.Refreshable || len(preflight.ProfileStatuses) != 1 || preflight.ProfileStatuses[0].Refreshable {
		t.Fatalf("Preflight() = %#v, want policy-denied non-refreshable unavailable", preflight)
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions during preflight, want 0", driver.OpenCount())
	}

	result, err := runtime.Provision(context.Background(), request)
	if err == nil {
		t.Fatalf("Provision() error = nil, result=%#v", result)
	}
	if result.Message != hostAuthRuntimeProvisionUnavailableMessage || result.Available || result.Provisioned {
		t.Fatalf("Provision() = %#v, want provisioning unavailable fail closed", result)
	}
	if resolver.calls == 0 {
		t.Fatal("resolver calls = 0, want alias policy gate reached")
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions, want policy deny before driver", driver.OpenCount())
	}
	assertNoStoredAuthProfile(t, store, identity.PackID, "apr-alpha001")
	formatted := fmt.Sprintf("%#v %v", result, err)
	assertNoForbiddenSubstrings(t, formatted, "must-not-store", "https://api.alpha.test/login", "Authorization", "Bearer", "Cookie")
}

func TestHostAuthRuntimeProvisionCancelTimeoutOrUnavailableIsGeneric(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")

	t.Run("cancel", func(t *testing.T) {
		bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
		store := newTempAuthProfileStore(t)
		driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "cancel-secret-token"})
		driver.status = WebViewAuthStatusCanceled
		runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: NewWebViewAuthCoordinator(store, driver)})
		result, err := runtime.Provision(context.Background(), hostAuthRuntimeRequest(identity))
		if err != nil {
			t.Fatalf("Provision(cancel) error = %v", err)
		}
		if result.Available || result.Provisioned || strings.Contains(fmt.Sprintf("%#v", result), "cancel-secret-token") {
			t.Fatalf("Provision(cancel) result = %#v", result)
		}
		assertNoStoredAuthProfile(t, store, identity.PackID, "apr-alpha001")
	})

	t.Run("timeout", func(t *testing.T) {
		bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, TimeoutMillis: 1}}, nil)
		store := newTempAuthProfileStore(t)
		driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "timeout-secret-token"})
		driver.status = WebViewAuthStatusTimeout
		runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: NewWebViewAuthCoordinator(store, driver)})
		result, err := runtime.Provision(context.Background(), hostAuthRuntimeRequest(identity))
		if err != nil {
			t.Fatalf("Provision(timeout) error = %v", err)
		}
		if result.Available || result.Provisioned || strings.Contains(fmt.Sprintf("%#v", result), "timeout-secret-token") {
			t.Fatalf("Provision(timeout) result = %#v", result)
		}
		assertNoStoredAuthProfile(t, store, identity.PackID, "apr-alpha001")
	})

	t.Run("error", func(t *testing.T) {
		bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
		store := newTempAuthProfileStore(t)
		driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "error-secret-token"})
		driver.status = WebViewAuthStatusError
		driver.err = errors.New("driver failed with Authorization: Bearer error-secret-token")
		runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: NewWebViewAuthCoordinator(store, driver)})
		result, err := runtime.Provision(context.Background(), hostAuthRuntimeRequest(identity))
		if err == nil {
			t.Fatalf("Provision(error) error = nil, result = %#v", result)
		}
		assertNoForbiddenSubstrings(t, err.Error(), "error-secret-token", "Authorization: Bearer error-secret-token", "https://fixture.invalid/login")
		assertNoStoredAuthProfile(t, store, identity.PackID, "apr-alpha001")
	})

	t.Run("nil coordinator", func(t *testing.T) {
		bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
		runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: newTempAuthProfileStore(t)})
		result, err := runtime.Provision(context.Background(), hostAuthRuntimeRequest(identity))
		if err == nil || strings.Contains(err.Error(), "https://fixture.invalid/login") || strings.Contains(fmt.Sprintf("%#v", result), "https://fixture.invalid/login") {
			t.Fatalf("Provision(nil coordinator) result=%#v error=%v", result, err)
		}
	})

	t.Run("provisioning none", func(t *testing.T) {
		bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, func(pack map[string]any) {
			pack["preflight"].(map[string]any)["missing"] = "fail"
			pack["preflight"].(map[string]any)["expired"] = "fail"
			pack["provisioning"].(map[string]any)["mode"] = "none"
			pack["provisioning"].(map[string]any)["profile_refs"] = []string{}
		})
		store := newTempAuthProfileStore(t)
		driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "none-secret-token"})
		runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: NewWebViewAuthCoordinator(store, driver)})
		result, err := runtime.Provision(context.Background(), hostAuthRuntimeRequest(identity))
		if err == nil || driver.OpenCount() != 0 || strings.Contains(fmt.Sprintf("%#v %v", result, err), "none-secret-token") {
			t.Fatalf("Provision(none) result=%#v error=%v opens=%d", result, err, driver.OpenCount())
		}
	})
}

func TestHostAuthRuntimeCallbackMetadataMissingFailsClosed(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: identity, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://fixture.invalid/login"}}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
		delete(packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any), "collector_js")
	})
	_, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{})
	if err == nil {
		t.Fatal("NewPrivateAuthRuntimeBundle() error = nil, want fail-closed missing callback metadata")
	}
	assertGenericPrivateAuthRuntimeBundleError(t, err)
}

func TestHostAuthRuntimeClearAndRefreshOnGenericFailureOnce(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "stale-refresh-token", []DomainRule{{Host: "fixture.invalid"}}, nil)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "fresh-refresh-token"})
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: NewWebViewAuthCoordinator(store, driver)})
	guard := NewHostAuthRuntimeBatchGuard()
	request := hostAuthRuntimeRequest(identity)

	result, err := runtime.RefreshOnGenericFailure(context.Background(), request, guard)
	if err != nil {
		t.Fatalf("RefreshOnGenericFailure() error = %v", err)
	}
	if !result.Provisioned || !result.Available || driver.OpenCount() != 1 {
		t.Fatalf("RefreshOnGenericFailure() result=%#v opens=%d", result, driver.OpenCount())
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", request.TargetURL)
	if err != nil {
		t.Fatalf("ResolveAuthProfile() after refresh error = %v", err)
	}
	if resolved.HeaderValue != "Bearer fresh-refresh-token" {
		t.Fatalf("HeaderValue = %q, want fresh token", resolved.HeaderValue)
	}

	result, err = runtime.RefreshOnGenericFailure(context.Background(), request, guard)
	if err != nil {
		t.Fatalf("RefreshOnGenericFailure(repeat) error = %v", err)
	}
	if !result.RefreshSkipped || driver.OpenCount() != 1 {
		t.Fatalf("repeat result=%#v opens=%d, want skipped without new session", result, driver.OpenCount())
	}
}

func TestHostAuthRuntimeBatchGuardAvoidsDoubleRefresh(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "batch-stale-token", []DomainRule{{Host: "fixture.invalid"}}, nil)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "batch-fresh-token"})
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: NewWebViewAuthCoordinator(store, driver)})
	guard := NewHostAuthRuntimeBatchGuard()
	request := hostAuthRuntimeRequest(identity)

	first, err := runtime.RefreshOnGenericFailure(context.Background(), request, guard)
	if err != nil {
		t.Fatalf("first RefreshOnGenericFailure() error = %v", err)
	}
	second, err := runtime.RefreshOnGenericFailure(context.Background(), request, guard)
	if err != nil {
		t.Fatalf("second RefreshOnGenericFailure() error = %v", err)
	}
	if !first.Provisioned || !second.RefreshSkipped || driver.OpenCount() != 1 {
		t.Fatalf("guard did not prevent duplicate refresh: first=%#v second=%#v opens=%d", first, second, driver.OpenCount())
	}

	distinct := request
	distinct.TargetURL = "https://fixture.invalid/other-file"
	third, err := runtime.RefreshOnGenericFailure(context.Background(), distinct, guard)
	if err != nil {
		t.Fatalf("distinct RefreshOnGenericFailure() error = %v", err)
	}
	if !third.Provisioned || driver.OpenCount() != 2 {
		t.Fatalf("distinct target did not refresh independently: result=%#v opens=%d", third, driver.OpenCount())
	}
}

func TestHostAuthRuntimeCompatibilityResolverFailsClosedOnAmbiguousPackID(t *testing.T) {
	identityOne := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	identityTwo := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-2", "2")
	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{
		{Identity: identityOne, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://fixture.invalid/login"},
		{Identity: identityTwo, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://fixture.invalid/login"},
	}, nil)
	bundle, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identityOne.PackID, "apr-alpha001", AuthSecretKindBearer, "ambiguous-token-secret", []DomainRule{{Host: "fixture.invalid"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store})

	resolved, err := runtime.ResolveAuthProfile(context.Background(), identityOne.PackID, "apr-alpha001", "https://fixture.invalid/file")
	if err == nil {
		t.Fatalf("ResolveAuthProfile() error = nil, resolved = %#v", resolved)
	}
	assertNoForbiddenSubstrings(t, err.Error(), "ambiguous-token-secret", "Bearer ambiguous-token-secret")
}

func TestHostAuthRuntimeResultsAreGenericAndRedacted(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://fixture.invalid/login?token=login-secret-value"}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "redaction-bearer-token", []DomainRule{{Host: "fixture.invalid"}}, nil)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "redaction-captured-token"})
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: NewWebViewAuthCoordinator(store, driver)})

	result, err := runtime.Preflight(context.Background(), hostAuthRuntimeRequest(identity))
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	formatted := fmt.Sprintf("%v %#v %+v", result, result, result)
	assertNoForbiddenSubstrings(t, formatted, "redaction-bearer-token", "Bearer redaction-bearer-token", "login-secret-value", "https://fixture.invalid/login?token=login-secret-value", "provider")

	badTarget := hostAuthRuntimeRequest(identity)
	badTarget.ProfileRef = "apr-alpha001"
	badTarget.TargetURL = "https://denied.example.test/file?token=target-secret-value"
	_, err = runtime.MaterializeAuthProfile(context.Background(), badTarget)
	if err == nil {
		t.Fatal("MaterializeAuthProfile(denied target) error = nil, want error")
	}
	assertNoForbiddenSubstrings(t, fmt.Sprintf("%v %#v", err, err), "redaction-bearer-token", "target-secret-value", "login-secret-value", "https://fixture.invalid/login?token=login-secret-value")
}

type hostAuthRuntimeProfileFixture struct {
	ProfileRef    AuthProfileID
	Kind          AuthSecretKind
	LoginURL      string
	TimeoutMillis int
}

func newHostAuthRuntimeBundle(t *testing.T, identity VerifiedPackIdentity, profiles []hostAuthRuntimeProfileFixture, mutatePack func(map[string]any)) *PrivateAuthRuntimeBundle {
	t.Helper()
	if len(profiles) == 0 {
		profiles = []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}
	}
	first := profiles[0]
	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: identity, ProfileRef: string(first.ProfileRef), Kind: first.Kind, LoginURL: first.effectiveLoginURL()}}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
		refs := make([]string, 0, len(profiles))
		profileMaps := make([]map[string]any, 0, len(profiles))
		for _, profile := range profiles {
			ref := string(profile.ProfileRef)
			if ref == "" {
				ref = "apr-alpha001"
			}
			kind := profile.Kind
			if kind == "" {
				kind = AuthSecretKindBearer
			}
			profileMap := privateAuthRuntimeProfileMap(ref, kind, profile.effectiveLoginURL())
			if profile.TimeoutMillis > 0 {
				profileMap["login"].(map[string]any)["timeout_millis"] = profile.TimeoutMillis
			}
			refs = append(refs, ref)
			profileMaps = append(profileMaps, profileMap)
		}
		pack := packs[0]
		pack["profiles"] = profileMaps
		pack["store_binding"].(map[string]any)["profile_refs"] = refs
		pack["materialization"].(map[string]any)["profile_refs"] = refs
		pack["provisioning"].(map[string]any)["profile_refs"] = refs
		if mutatePack != nil {
			mutatePack(pack)
		}
	})
	bundle, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}

	return bundle
}

func (f hostAuthRuntimeProfileFixture) effectiveLoginURL() string {
	if f.LoginURL != "" {
		return f.LoginURL
	}

	return "https://fixture.invalid/login"
}

func hostAuthRuntimeRequest(identity VerifiedPackIdentity) HostAuthRuntimeRequest {
	return HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     hostAuthRuntimeManifest(identity),
		SourceURL:    "https://fixture.invalid/source",
		TargetURL:    "https://fixture.invalid/file",
	}
}

func hostAuthRuntimeManifest(identity VerifiedPackIdentity) Manifest {
	return Manifest{
		PackID:      identity.PackID,
		PackVersion: identity.PackVersion,
		Capabilities: []Capability{
			CapabilityAuthProfile,
		},
		Domains: []DomainRule{{Host: "fixture.invalid"}},
		ResourceLimits: ResourceLimits{
			TimeoutMillis: 60000,
		},
	}
}

func hostAuthRuntimeAliasManifest(identity VerifiedPackIdentity) Manifest {
	return Manifest{
		PackID:           identity.PackID,
		PackVersion:      identity.PackVersion,
		Capabilities:     []Capability{CapabilityHTTPFetch, CapabilityAuthProfile},
		DomainPolicyRefs: []string{"dpr-alpha001"},
		BrokerPolicyRefs: []string{"bpr-alpha001"},
		ResourceLimits: ResourceLimits{
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

func hostAuthRuntimeAliasPolicy(identity VerifiedPackIdentity, mutate func(*ResolvedHostPolicy)) ResolvedHostPolicy {
	policy := ResolvedHostPolicy{
		PolicyID:            "pol-alpha001",
		PolicyVersion:       "2026.05.15-alpha",
		PolicySHA256:        strings.Repeat("c", 64),
		PackIdentity:        identity,
		DomainPolicyRefs:    []string{"dpr-alpha001"},
		BrokerPolicyRefs:    []string{"bpr-alpha001"},
		AllowedCapabilities: []Capability{CapabilityHTTPFetch, CapabilityAuthProfile},
		IngressDomains:      []DomainRule{{Host: "share.alpha.test"}},
		BrokerDomains:       []DomainRule{{Host: "api.alpha.test"}},
		OutputDomains:       []HostPolicyOutputRule{{Host: "api.alpha.test", PathPrefixes: []string{"/"}}},
		AuthProfiles:        []HostPolicyAuthProfileScope{{ProfileID: "apr-alpha001", Domains: []DomainRule{{Host: "api.alpha.test"}}}},
		BrokerEndpoints: []HostPolicyBrokerEndpoint{{
			BrokerPolicyRef: "bpr-alpha001",
			EndpointRef:     "epr-alpha001",
			URLTemplate:     "https://api.alpha.test/resource/{id}",
			Methods:         []string{"GET"},
			AuthProfileRefs: []string{"apr-alpha001"},
		}},
	}
	if mutate != nil {
		mutate(&policy)
	}

	return policy
}

type hostAuthRuntimePolicyResolver struct {
	policy ResolvedHostPolicy
	err    error
	calls  int
}

func (r *hostAuthRuntimePolicyResolver) ResolveHostPolicy(context.Context, HostPolicyRequest) (ResolvedHostPolicy, error) {
	r.calls++
	if r.err != nil {
		return ResolvedHostPolicy{}, r.err
	}

	return cloneResolvedHostPolicy(r.policy), nil
}

func setHostAuthProfile(t *testing.T, store AuthProfileStore, packID string, profileID AuthProfileID, kind AuthSecretKind, secret string, domains []DomainRule, expiresAt *time.Time) {
	t.Helper()
	_, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         packID,
		ProfileID:      profileID,
		Kind:           kind,
		Secret:         secret,
		AllowedDomains: domains,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		t.Fatalf("SetAuthProfile(%s) error = %v", profileID, err)
	}
}

func hostAuthRuntimeStatusMap(result HostAuthRuntimeResult) map[AuthProfileID]HostAuthRuntimeProfileResult {
	statuses := make(map[AuthProfileID]HostAuthRuntimeProfileResult, len(result.ProfileStatuses))
	for _, status := range result.ProfileStatuses {
		statuses[status.ProfileRef] = status
	}

	return statuses
}

func assertNoStoredAuthProfile(t *testing.T, store AuthProfileStore, packID string, profileID AuthProfileID) {
	t.Helper()
	_, err := store.ResolveAuthProfile(context.Background(), packID, profileID, "https://fixture.invalid/file")
	if err == nil {
		t.Fatalf("ResolveAuthProfile(%s) error = nil, want no stored auth", profileID)
	}
}

type countingAuthProfileStore struct {
	AuthProfileStore

	mu            sync.Mutex
	snapshotCalls int
}

func (s *countingAuthProfileStore) AuthProfileSnapshots(ctx context.Context, packID string) ([]AuthProfileSnapshot, error) {
	s.mu.Lock()
	s.snapshotCalls++
	s.mu.Unlock()

	return s.AuthProfileStore.AuthProfileSnapshots(ctx, packID)
}

func (s *countingAuthProfileStore) SnapshotCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.snapshotCalls
}

type hostAuthRecordingDriver struct {
	mu       sync.Mutex
	token    AuthWebViewToken
	status   WebViewAuthStatus
	err      error
	requests []WebViewAuthRequest
}

func newHostAuthRecordingDriver(token AuthWebViewToken) *hostAuthRecordingDriver {
	return &hostAuthRecordingDriver{token: token}
}

func (d *hostAuthRecordingDriver) OpenAuthSession(_ context.Context, request WebViewAuthRequest, sink AuthWebViewSink) (AuthWebViewSession, error) {
	d.mu.Lock()
	status := d.status
	if status == "" {
		status = WebViewAuthStatusSuccess
	}
	token := d.token
	if token.Kind == "" {
		token.Kind = request.Kind
	}
	driverErr := d.err
	d.requests = append(d.requests, cloneWebViewAuthRequestForTest(request))
	d.mu.Unlock()

	switch status {
	case WebViewAuthStatusSuccess:
		sink.OnSuccess(token)
	case WebViewAuthStatusCanceled:
		sink.OnCancel()
	case WebViewAuthStatusError:
		if driverErr == nil {
			driverErr = errors.New("generic auth webview error")
		}
		sink.OnError(driverErr)
	case WebViewAuthStatusTimeout:
	}

	return hostAuthRecordingSession{}, nil
}

func (d *hostAuthRecordingDriver) OpenCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return len(d.requests)
}

func (d *hostAuthRecordingDriver) Requests() []WebViewAuthRequest {
	d.mu.Lock()
	defer d.mu.Unlock()

	requests := make([]WebViewAuthRequest, len(d.requests))
	for i, request := range d.requests {
		requests[i] = cloneWebViewAuthRequestForTest(request)
	}

	return requests
}

type hostAuthRecordingSession struct{}

func (hostAuthRecordingSession) Close() error { return nil }

func cloneWebViewAuthRequestForTest(request WebViewAuthRequest) WebViewAuthRequest {
	request.Manifest = cloneManifest(request.Manifest)
	request.AllowedDomains = cloneDomainRules(request.AllowedDomains)
	request.CallbackTransport.ContentTypes = cloneStringSlice(request.CallbackTransport.ContentTypes)
	request.Capture.SecretCandidates = cloneStringSlice(request.Capture.SecretCandidates)

	return request
}
