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
	denied.SourceURL = "https://denied.alpha.test/path"
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
	setHostAuthProfile(t, store, identity.PackID, "apr-bearer001", AuthSecretKindBearer, "bearer-status-token", []DomainRule{{Host: "files.alpha.test"}}, &future)
	setHostAuthProfile(t, store, identity.PackID, "apr-cookie001", AuthSecretKindCookie, "sid=cookie-status-secret", []DomainRule{{Host: "files.alpha.test"}}, &future)
	setHostAuthProfile(t, store, identity.PackID, "apr-expired001", AuthSecretKindBearer, "expired-status-token", []DomainRule{{Host: "files.alpha.test"}}, &past)
	setHostAuthProfile(t, store, identity.PackID, "apr-kind001", AuthSecretKindCookie, "sid=kind-status-secret", []DomainRule{{Host: "files.alpha.test"}}, &future)
	setHostAuthProfile(t, store, identity.PackID, "apr-domain001", AuthSecretKindBearer, "domain-status-token", []DomainRule{{Host: "api.alpha.test"}}, &future)
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
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "bound-token-secret", []DomainRule{{Host: "files.alpha.test"}}, nil)
	setHostAuthProfile(t, store, identity.PackID, "apr-storeonly001", AuthSecretKindBearer, "store-only-token-secret", []DomainRule{{Host: "files.alpha.test"}}, nil)
	setHostAuthProfile(t, store, identity.PackID, "apr-outside001", AuthSecretKindBearer, "outside-token-secret", []DomainRule{{Host: "files.alpha.test"}}, nil)
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

func TestHostAuthRuntimeStoredProfileIdentityMismatchIsInvisible(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, "xpk-alpha002", "apr-alpha001", AuthSecretKindBearer, "wrong-pack-token", []DomainRule{{Host: "files.alpha.test"}}, nil)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha002", AuthSecretKindBearer, "wrong-profile-token", []DomainRule{{Host: "files.alpha.test"}}, nil)
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

func TestHostAuthRuntimeDomainScopeMismatchPreflightAndMaterialization(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "domain-scope-token", []DomainRule{{Host: "share.alpha.test"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store})
	request := hostAuthRuntimeRequest(identity)
	request.ProfileRef = "apr-alpha001"
	request.TargetURL = "https://files.alpha.test/file?token=target-query-secret"

	result, err := runtime.Preflight(context.Background(), request)
	if err != nil {
		t.Fatalf("domain_scope_mismatch: Preflight() error = %v", err)
	}
	if result.Available || len(result.ProfileStatuses) != 1 || result.ProfileStatuses[0].Status != HostAuthRuntimeProfileUnavailable || result.ProfileStatuses[0].Refreshable {
		t.Fatalf("domain_scope_mismatch: result = %#v", result)
	}
	_, err = runtime.MaterializeAuthProfile(context.Background(), request)
	if err == nil {
		t.Fatal("domain_scope_mismatch: MaterializeAuthProfile() error = nil")
	}
	assertNoForbiddenSubstrings(t, fmt.Sprintf("%#v %v", result, err), "domain-scope-token", "target-query-secret", "Authorization: Bearer domain-scope-token")
}

func TestHostAuthRuntimeWrongSecretKindIsMaterializationMismatch(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindCookie, "sid=wrong-kind-token", []DomainRule{{Host: "files.alpha.test"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store})
	request := hostAuthRuntimeRequest(identity)
	request.ProfileRef = "apr-alpha001"

	result, err := runtime.Preflight(context.Background(), request)
	if err != nil {
		t.Fatalf("materialization_store_resolve: Preflight() error = %v", err)
	}
	if result.Available || len(result.ProfileStatuses) != 1 || result.ProfileStatuses[0].Status != HostAuthRuntimeProfileUnavailable || result.ProfileStatuses[0].Refreshable {
		t.Fatalf("materialization_store_resolve: result = %#v", result)
	}
	_, err = runtime.MaterializeAuthProfile(context.Background(), request)
	if err == nil {
		t.Fatal("materialization_store_resolve: MaterializeAuthProfile() error = nil")
	}
	assertNoForbiddenSubstrings(t, fmt.Sprintf("%#v %v", result, err), "wrong-kind-token", "Cookie", "sid=wrong-kind-token")
}

func TestHostAuthRuntimeResolvesOpaqueDirectStorage(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{
		{ProfileRef: "apr-bearer001", Kind: AuthSecretKindBearer},
		{ProfileRef: "apr-cookie001", Kind: AuthSecretKindCookie},
	}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-bearer001", AuthSecretKindBearer, "direct-bearer-secret", []DomainRule{{Host: "files.alpha.test"}}, nil)
	setHostAuthProfile(t, store, identity.PackID, "apr-cookie001", AuthSecretKindCookie, "sid=direct-cookie-secret; refresh=second-cookie-secret", []DomainRule{{Host: "files.alpha.test"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store})

	bearer, err := runtime.ResolveAuthProfile(context.Background(), identity.PackID, "apr-bearer001", "https://files.alpha.test/file")
	if err != nil {
		t.Fatalf("ResolveAuthProfile(bearer) error = %v", err)
	}
	if bearer.HeaderName != "Authorization" || bearer.HeaderValue != "Bearer direct-bearer-secret" || bearer.Kind != AuthSecretKindBearer {
		t.Fatalf("bearer resolved = %#v", bearer)
	}
	cookie, err := runtime.ResolveAuthProfile(context.Background(), identity.PackID, "apr-cookie001", "https://files.alpha.test/file")
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
	if opened.PackID != identity.PackID || opened.ProfileID != "apr-alpha001" || opened.Kind != AuthSecretKindBearer || opened.LoginURL != "https://files.alpha.test/login" {
		t.Fatalf("opened request = %#v", opened)
	}
	if len(opened.AllowedDomains) != 1 || opened.AllowedDomains[0].Host != "files.alpha.test" || opened.Timeout != 45*time.Second {
		t.Fatalf("opened request domain/timeout = %#v", opened)
	}
	if opened.CallbackTransport.Mode != "local_post" || opened.CallbackTransport.MaxBodyBytes != 16384 || len(opened.CallbackTransport.ContentTypes) != 1 || opened.CollectorJS == "" {
		t.Fatalf("opened request callback metadata = %#v collector=%q", opened.CallbackTransport, opened.CollectorJS)
	}
	if opened.Capture.Format != "json" || len(opened.Capture.SecretCandidates) != 2 || !opened.Capture.TrimSpace || !opened.Capture.RejectCRLF {
		t.Fatalf("opened request capture metadata = %#v", opened.Capture)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), identity.PackID, "apr-alpha001", "https://files.alpha.test/file")
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
			domains:    []DomainRule{{Host: "files.alpha.test"}},
			wantStatus: HostAuthRuntimeProfileUnavailable,
		},
		{
			name:       "domain mismatch",
			kind:       AuthSecretKindBearer,
			secret:     "stale-domain-secret",
			domains:    []DomainRule{{Host: "api.alpha.test"}},
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

func TestHostAuthRuntimeCallbackStoreMaterializableIfProvisioningRuns(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	store := newTempAuthProfileStore(t)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "materializable-token", RedactedDisplay: "captured bearer"})
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:      bundle,
		Store:       store,
		Coordinator: NewWebViewAuthCoordinator(store, driver),
	})
	request := hostAuthRuntimeRequest(identity)
	request.ProfileRef = "apr-alpha001"

	provisioned, err := runtime.Provision(context.Background(), request)
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if !provisioned.Provisioned || !provisioned.Available || driver.OpenCount() != 1 {
		t.Fatalf("Provision() = %#v opens=%d, want single successful callback", provisioned, driver.OpenCount())
	}
	material, err := runtime.MaterializeAuthProfile(context.Background(), request)
	if err != nil {
		t.Fatalf("MaterializeAuthProfile() error = %v", err)
	}
	if material.Kind != AuthSecretKindBearer || material.HeaderName != "Authorization" || material.RedactedDisplay == "" {
		t.Fatalf("materialized public-safe shape = %#v", material)
	}
	formatted := fmt.Sprintf("%#v", material)
	assertNoForbiddenSubstrings(t, formatted, "materializable-token", "Bearer materializable-token")
}

func TestHostAuthRuntimeAliasWebViewProvisionSucceedsWithHostPolicyScope(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	identity := pack.Identity
	profileRef := AuthProfileID("alpha-secret")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: profileRef, Kind: AuthSecretKindBearer, LoginURL: "https://api.alpha.test/login"}}, nil)
	store := newTempAuthProfileStore(t)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "alias-captured-token"})
	resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(identity)}
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:             bundle,
		Store:              store,
		Coordinator:        NewWebViewAuthCoordinator(store, driver),
		HostPolicyResolver: resolver,
	})
	request := HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     pack.Manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://api.alpha.test/files/fixture-item",
		ProfileRef:   profileRef,
	}

	result, err := runtime.Provision(context.Background(), request)
	if err != nil {
		t.Fatalf("Provision(alias) error = %v", err)
	}
	if !result.Provisioned || !result.Available {
		t.Fatalf("Provision(alias) = %#v, want provisioned available", result)
	}
	if driver.OpenCount() != 1 || resolver.calls == 0 {
		t.Fatalf("opens=%d resolver calls=%d, want driver/resolver used", driver.OpenCount(), resolver.calls)
	}
	opened := driver.Requests()[0]
	if opened.Manifest.PackID != identity.PackID || len(opened.Manifest.Domains) != 0 || opened.ProfileID != profileRef || opened.LoginURL != "https://api.alpha.test/login" {
		t.Fatalf("opened request = %#v", opened)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), identity.PackID, profileRef, "https://api.alpha.test/files/fixture-item")
	if err != nil {
		t.Fatalf("ResolveAuthProfile(alias) error = %v", err)
	}
	if resolved.HeaderValue != "Bearer alias-captured-token" {
		t.Fatalf("HeaderValue = %q, want alias captured token", resolved.HeaderValue)
	}
}

func TestHostAuthRuntimeAliasWebViewProvisionFailsClosedBeforeDriverOpen(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	identity := pack.Identity
	profileRef := AuthProfileID("alpha-secret")
	baseRequest := HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     pack.Manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://api.alpha.test/files/fixture-item",
		ProfileRef:   profileRef,
	}

	tests := []struct {
		name          string
		resolver      HostPolicyResolver
		request       HostAuthRuntimeRequest
		mutateBundle  func(map[string]any)
		wantUnmatched bool
	}{
		{name: "nil resolver", resolver: nil},
		{name: "resolver error", resolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(identity), err: errors.New("private policy detail")}},
		{name: "mismatched policy identity", resolver: &fakeHostPolicyResolver{policy: mutateHostRuntimePolicy(syntheticHostPolicy(identity), func(policy *ResolvedHostPolicy) {
			policy.PackIdentity.AssetSHA256 = hashString('9')
		})}},
		{name: "policy missing auth capability", resolver: &fakeHostPolicyResolver{policy: mutateHostRuntimePolicy(syntheticHostPolicy(identity), func(policy *ResolvedHostPolicy) {
			policy.AllowedCapabilities = []Capability{CapabilityParseWASM, CapabilityHTTPFetch}
		})}},
		{name: "login outside broker policy", resolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(identity)}, mutateBundle: func(pack map[string]any) {
			pack["profiles"].([]map[string]any)[0]["login"].(map[string]any)["url"] = "https://denied.alpha.test/login"
			pack["profiles"].([]map[string]any)[0]["login"].(map[string]any)["allowed_domains"] = []map[string]any{{"host": "denied.alpha.test"}}
		}},
		{name: "allowed domain outside login url host", resolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(identity)}, mutateBundle: func(pack map[string]any) {
			profiles := pack["profiles"].([]map[string]any)
			profiles[0]["login"].(map[string]any)["allowed_domains"] = []map[string]any{{"host": "files.alpha.test"}}
		}},
		{name: "wrong request identity unmatched", resolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(identity)}, request: mutateHostRuntimeRequest(baseRequest, func(request *HostAuthRuntimeRequest) {
			request.PackIdentity.AssetSHA256 = hashString('8')
		}), wantUnmatched: true},
		{name: "missing auth capability", resolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(identity)}, request: mutateHostRuntimeRequest(baseRequest, func(request *HostAuthRuntimeRequest) {
			request.Manifest.Capabilities = []Capability{CapabilityParseWASM, CapabilityHTTPFetch}
		})},
		{name: "profile outside host policy scope", resolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(identity)}, request: mutateHostRuntimeRequest(baseRequest, func(request *HostAuthRuntimeRequest) {
			request.ProfileRef = "apr-other001"
		}), mutateBundle: func(pack map[string]any) {
			profile := privateAuthRuntimeProfileMap("apr-other001", AuthSecretKindBearer, "https://api.alpha.test/login")
			profiles := pack["profiles"].([]map[string]any)
			pack["profiles"] = append(profiles, profile)
			pack["store_binding"].(map[string]any)["profile_refs"] = []string{"alpha-secret", "apr-other001"}
			pack["materialization"].(map[string]any)["profile_refs"] = []string{"alpha-secret", "apr-other001"}
			pack["provisioning"].(map[string]any)["profile_refs"] = []string{"alpha-secret", "apr-other001"}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := tt.request
			if request.PackIdentity == (VerifiedPackIdentity{}) {
				request = baseRequest
			}
			bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: profileRef, Kind: AuthSecretKindBearer, LoginURL: "https://api.alpha.test/login"}}, tt.mutateBundle)
			store := newTempAuthProfileStore(t)
			driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "must-not-store"})
			runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
				Bundle:             bundle,
				Store:              store,
				Coordinator:        NewWebViewAuthCoordinator(store, driver),
				HostPolicyResolver: tt.resolver,
			})

			result, err := runtime.Provision(context.Background(), request)
			if tt.wantUnmatched {
				if err != nil || result.Matched || !result.Available {
					t.Fatalf("Provision(unmatched) result=%#v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatalf("Provision() error = nil, result=%#v", result)
			}
			if driver.OpenCount() != 0 {
				t.Fatalf("driver opened %d sessions, want 0", driver.OpenCount())
			}
			if _, resolveErr := store.ResolveAuthProfile(context.Background(), identity.PackID, request.ProfileRef, "https://api.alpha.test/files/fixture-item"); resolveErr == nil {
				t.Fatal("ResolveAuthProfile() error = nil, want no stored profile")
			}
			assertNoForbiddenSubstrings(t, fmt.Sprintf("%#v %v", result, err), "must-not-store", "private policy detail", "https://api.alpha.test/login")
		})
	}
}

func TestHostAuthRuntimeAliasWebViewProvisionAllowsLoginDomainOutsideTargetAuthProfileScope(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	identity := pack.Identity
	profileRef := AuthProfileID("alpha-secret")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: profileRef, Kind: AuthSecretKindBearer, LoginURL: "https://api.alpha.test/login"}}, nil)
	store := newTempAuthProfileStore(t)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "alias-login-scope-token", RedactedDisplay: "captured bearer"})
	resolver := &fakeHostPolicyResolver{policy: mutateHostRuntimePolicy(syntheticHostPolicy(identity), func(policy *ResolvedHostPolicy) {
		policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: profileRef, Domains: []DomainRule{{Host: "share.alpha.test"}}}}
	})}
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:             bundle,
		Store:              store,
		Coordinator:        NewWebViewAuthCoordinator(store, driver),
		HostPolicyResolver: resolver,
	})
	request := HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     pack.Manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://share.alpha.test/files/fixture-item",
		ProfileRef:   profileRef,
	}

	result, err := runtime.Provision(context.Background(), request)
	if err != nil {
		t.Fatalf("Provision(login-scope decoupled) error = %v", err)
	}
	if !result.Provisioned || !result.Available {
		t.Fatalf("Provision(login-scope decoupled) = %#v, want provisioned", result)
	}
	if driver.OpenCount() != 1 || resolver.calls == 0 {
		t.Fatalf("opens=%d resolver calls=%d, want one driver open and resolver used", driver.OpenCount(), resolver.calls)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), identity.PackID, profileRef, request.TargetURL)
	if err != nil {
		t.Fatalf("ResolveAuthProfile() after provision error = %v", err)
	}
	if resolved.HeaderValue != "Bearer alias-login-scope-token" {
		t.Fatalf("resolved header value mismatch")
	}
	formatted := fmt.Sprintf("%#v", result)
	assertNoForbiddenSubstrings(t, formatted, "alias-login-scope-token", "Bearer alias-login-scope-token", "Cookie", "Authorization")
}

func TestHostAuthRuntimeAliasWebViewProvisionTargetOutsideAuthProfileScopeStillDeniesBeforeDriverOpen(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	identity := pack.Identity
	profileRef := AuthProfileID("alpha-secret")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: profileRef, Kind: AuthSecretKindBearer, LoginURL: "https://api.alpha.test/login"}}, nil)
	store := newTempAuthProfileStore(t)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "must-not-store"})
	resolver := &fakeHostPolicyResolver{policy: mutateHostRuntimePolicy(syntheticHostPolicy(identity), func(policy *ResolvedHostPolicy) {
		policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: profileRef, Domains: []DomainRule{{Host: "files.alpha.test"}}}}
	})}
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:             bundle,
		Store:              store,
		Coordinator:        NewWebViewAuthCoordinator(store, driver),
		HostPolicyResolver: resolver,
	})
	request := HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     pack.Manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://api.alpha.test/files/fixture-item",
		ProfileRef:   profileRef,
	}

	preflight, err := runtime.Preflight(context.Background(), request)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if preflight.Available || preflight.Refreshable || len(preflight.ProfileStatuses) != 1 || preflight.ProfileStatuses[0].Refreshable || preflight.ProfileStatuses[0].Status != HostAuthRuntimeProfileMissing {
		t.Fatalf("Preflight() = %#v, want target-scope denied non-refreshable missing", preflight)
	}
	result, err := runtime.Provision(context.Background(), request)
	if err == nil {
		t.Fatalf("Provision(target-scope deny) error = nil, result=%#v", result)
	}
	if result.Message != hostAuthRuntimeProvisionUnavailableMessage || result.Available || result.Provisioned {
		t.Fatalf("Provision(target-scope deny) = %#v, want provisioning unavailable", result)
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions, want target-scope deny before driver", driver.OpenCount())
	}
	assertNoStoredAuthProfile(t, store, identity.PackID, profileRef)
	formatted := fmt.Sprintf("%#v %v", result, err)
	assertNoForbiddenSubstrings(t, formatted, "must-not-store", "Authorization", "Bearer", "Cookie")
}

func TestHostAuthRuntimeAliasWebViewProvisionRejectsLoginAllowedDomainOutsideLoginURLHost(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	identity := pack.Identity
	profileRef := AuthProfileID("alpha-secret")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: profileRef, Kind: AuthSecretKindBearer, LoginURL: "https://api.alpha.test/login"}}, func(pack map[string]any) {
		profiles := pack["profiles"].([]map[string]any)
		profiles[0]["login"].(map[string]any)["allowed_domains"] = []map[string]any{{"host": "files.alpha.test"}}
	})
	store := newTempAuthProfileStore(t)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "must-not-store"})
	resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(identity)}
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:             bundle,
		Store:              store,
		Coordinator:        NewWebViewAuthCoordinator(store, driver),
		HostPolicyResolver: resolver,
	})
	request := HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     pack.Manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://api.alpha.test/files/fixture-item",
		ProfileRef:   profileRef,
	}

	result, err := runtime.Provision(context.Background(), request)
	if err == nil {
		t.Fatalf("Provision(login-scope deny) error = nil, result=%#v", result)
	}
	if result.Message != hostAuthRuntimeProvisionUnavailableMessage || result.Available || result.Provisioned {
		t.Fatalf("Provision(login-scope deny) = %#v, want provisioning unavailable", result)
	}
	if resolver.calls == 0 {
		t.Fatal("resolver calls = 0, want alias policy gate reached")
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions, want login-scope deny before driver", driver.OpenCount())
	}
	assertNoStoredAuthProfile(t, store, identity.PackID, profileRef)
	formatted := fmt.Sprintf("%#v %v", result, err)
	assertNoForbiddenSubstrings(t, formatted, "must-not-store", "https://api.alpha.test/login", "Authorization", "Bearer", "Cookie")
}

func TestHostAuthRuntimeAliasProvisioningPolicyDeniedBeforeDriverOpenClassifiesBoundary(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	identity := pack.Identity
	profileRef := AuthProfileID("alpha-secret")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: profileRef, Kind: AuthSecretKindBearer, LoginURL: "https://api.alpha.test/login"}}, nil)
	store := newTempAuthProfileStore(t)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "must-not-store"})
	resolver := &fakeHostPolicyResolver{policy: mutateHostRuntimePolicy(syntheticHostPolicy(identity), func(policy *ResolvedHostPolicy) {
		policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: profileRef, Domains: []DomainRule{{Host: "files.alpha.test"}}}}
	})}
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle:             bundle,
		Store:              store,
		Coordinator:        NewWebViewAuthCoordinator(store, driver),
		HostPolicyResolver: resolver,
	})
	request := HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     pack.Manifest,
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://api.alpha.test/files/fixture-item",
		ProfileRef:   profileRef,
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
	assertNoStoredAuthProfile(t, store, identity.PackID, profileRef)
	formatted := fmt.Sprintf("%#v %v", result, err)
	assertNoForbiddenSubstrings(t, formatted, "must-not-store", "https://api.alpha.test/login", "Authorization", "Bearer", "Cookie")
}

func TestHostAuthRuntimeProvisionMissingCallbackContractFailsBeforeDriverOpen(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	pack, ok := bundle.PackRuntime(identity)
	if !ok {
		t.Fatal("PackRuntime() ok = false")
	}
	pack.Profiles[0].Login.CollectorJS = ""
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Store: newTempAuthProfileStore(t), Coordinator: NewWebViewAuthCoordinator(newTempAuthProfileStore(t), newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "must-not-open"}))})
	runtime.identityIndex[identity] = pack
	runtime.packIDIndex[identity.PackID] = []VerifiedPackIdentity{identity}
	runtime.profiles[identity] = hostAuthRuntimeProfileMap(pack.Profiles)
	runtime.storeRefs[identity] = hostAuthRuntimeProfileRefSet(pack.StoreBinding.ProfileRefs)
	runtime.materialRefs[identity] = hostAuthRuntimeProfileRefSet(pack.Materialization.ProfileRefs)
	runtime.provisionRefs[identity] = hostAuthRuntimeProfileRefSet(pack.Provisioning.ProfileRefs)
	driver := runtime.coordinator.driver.(*hostAuthRecordingDriver)

	result, err := runtime.Provision(context.Background(), hostAuthRuntimeRequest(identity))
	if err == nil {
		t.Fatalf("Provision() error = nil, result=%#v", result)
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions, want 0", driver.OpenCount())
	}
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
		assertNoForbiddenSubstrings(t, err.Error(), "error-secret-token", "Authorization: Bearer error-secret-token", "https://files.alpha.test/login")
		assertNoStoredAuthProfile(t, store, identity.PackID, "apr-alpha001")
	})

	t.Run("nil coordinator", func(t *testing.T) {
		bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
		runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: newTempAuthProfileStore(t)})
		result, err := runtime.Provision(context.Background(), hostAuthRuntimeRequest(identity))
		if err == nil || strings.Contains(err.Error(), "https://files.alpha.test/login") || strings.Contains(fmt.Sprintf("%#v", result), "https://files.alpha.test/login") {
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

func TestHostAuthRuntimeClearAndRefreshOnGenericFailureOnce(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "stale-refresh-token", []DomainRule{{Host: "files.alpha.test"}}, nil)
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
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "batch-stale-token", []DomainRule{{Host: "files.alpha.test"}}, nil)
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
	distinct.TargetURL = "https://files.alpha.test/other-file"
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
		{Identity: identityOne, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"},
		{Identity: identityTwo, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"},
	}, nil)
	bundle, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identityOne.PackID, "apr-alpha001", AuthSecretKindBearer, "ambiguous-token-secret", []DomainRule{{Host: "files.alpha.test"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store})

	resolved, err := runtime.ResolveAuthProfile(context.Background(), identityOne.PackID, "apr-alpha001", "https://files.alpha.test/file")
	if err == nil {
		t.Fatalf("ResolveAuthProfile() error = nil, resolved = %#v", resolved)
	}
	assertNoForbiddenSubstrings(t, err.Error(), "ambiguous-token-secret", "Bearer ambiguous-token-secret")
}

func TestHostAuthRuntimeResultsAreGenericAndRedacted(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	bundle := newHostAuthRuntimeBundle(t, identity, []hostAuthRuntimeProfileFixture{{ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://files.alpha.test/login?token=login-secret-value"}}, nil)
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "redaction-bearer-token", []DomainRule{{Host: "files.alpha.test"}}, nil)
	driver := newHostAuthRecordingDriver(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "redaction-captured-token"})
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{Bundle: bundle, Store: store, Coordinator: NewWebViewAuthCoordinator(store, driver)})

	result, err := runtime.Preflight(context.Background(), hostAuthRuntimeRequest(identity))
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	formatted := fmt.Sprintf("%v %#v %+v", result, result, result)
	assertNoForbiddenSubstrings(t, formatted, "redaction-bearer-token", "Bearer redaction-bearer-token", "login-secret-value", "https://files.alpha.test/login?token=login-secret-value")

	badTarget := hostAuthRuntimeRequest(identity)
	badTarget.ProfileRef = "apr-alpha001"
	badTarget.TargetURL = "https://denied.alpha.test/file?token=target-secret-value"
	_, err = runtime.MaterializeAuthProfile(context.Background(), badTarget)
	if err == nil {
		t.Fatal("MaterializeAuthProfile(denied target) error = nil, want error")
	}
	assertNoForbiddenSubstrings(t, fmt.Sprintf("%v %#v", err, err), "redaction-bearer-token", "target-secret-value", "login-secret-value", "https://files.alpha.test/login?token=login-secret-value")
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

	return "https://files.alpha.test/login"
}

func hostAuthRuntimeRequest(identity VerifiedPackIdentity) HostAuthRuntimeRequest {
	return HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     hostAuthRuntimeManifest(identity),
		SourceURL:    "https://share.alpha.test/source",
		TargetURL:    "https://files.alpha.test/file",
	}
}

func hostAuthRuntimeManifest(identity VerifiedPackIdentity) Manifest {
	return Manifest{
		PackID:      identity.PackID,
		PackVersion: identity.PackVersion,
		Capabilities: []Capability{
			CapabilityAuthProfile,
		},
		Domains: []DomainRule{{Host: "share.alpha.test"}, {Host: "files.alpha.test"}},
		ResourceLimits: ResourceLimits{
			TimeoutMillis: 60000,
		},
	}
}

func mutateHostRuntimePolicy(policy ResolvedHostPolicy, mutate func(*ResolvedHostPolicy)) ResolvedHostPolicy {
	mutate(&policy)

	return policy
}

func mutateHostRuntimeRequest(request HostAuthRuntimeRequest, mutate func(*HostAuthRuntimeRequest)) HostAuthRuntimeRequest {
	mutate(&request)

	return request
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
	_, err := store.ResolveAuthProfile(context.Background(), packID, profileID, "https://files.alpha.test/file")
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
	d.requests = append(d.requests, cloneWebViewAuthRequestForHostAuthTest(request))
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
		requests[i] = cloneWebViewAuthRequestForHostAuthTest(request)
	}

	return requests
}

type hostAuthRecordingSession struct{}

func (hostAuthRecordingSession) Close() error { return nil }

func cloneWebViewAuthRequestForHostAuthTest(request WebViewAuthRequest) WebViewAuthRequest {
	request.Manifest = cloneManifest(request.Manifest)
	request.AllowedDomains = cloneDomainRules(request.AllowedDomains)
	request.Capture = cloneWebViewAuthCaptureContract(request.Capture)

	return request
}
