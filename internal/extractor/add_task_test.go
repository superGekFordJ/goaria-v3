package extractor

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
)

type fakeHeaderProfileResolver struct {
	headers map[string][]string
	err     error
}

func (r fakeHeaderProfileResolver) ResolveHeaderProfile(ctx context.Context, packID string, profileRef string, rawURL string) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]string(nil), r.headers[profileRef]...), nil
}

type fakeAuthProfileResolver struct {
	secret ResolvedAuthSecret
	err    error
	calls  int
}

func (r *fakeAuthProfileResolver) ResolveAuthProfile(ctx context.Context, packID string, profileID AuthProfileID, rawURL string) (ResolvedAuthSecret, error) {
	r.calls++
	if r.err != nil {
		return ResolvedAuthSecret{}, r.err
	}
	return r.secret, nil
}

type fakeIdentityAwareAuthResolver struct {
	secret      ResolvedAuthSecret
	material    MaterializedAuthSecret
	err         error
	calls       int
	legacyCalls int
	lastRequest HostAuthRuntimeRequest
}

func (r *fakeIdentityAwareAuthResolver) ResolveAuthProfile(ctx context.Context, packID string, profileID AuthProfileID, rawURL string) (ResolvedAuthSecret, error) {
	r.legacyCalls++
	if r.err != nil {
		return ResolvedAuthSecret{}, r.err
	}
	return r.secret, nil
}

func (r *fakeIdentityAwareAuthResolver) MaterializeAuthProfile(ctx context.Context, request HostAuthRuntimeRequest) (MaterializedAuthSecret, error) {
	r.calls++
	r.lastRequest = request
	if r.err != nil {
		return MaterializedAuthSecret{}, r.err
	}
	return r.material, nil
}

func TestAddTaskDispatcherNoMatchFallsBack(t *testing.T) {
	registry, _ := NewRegistry(nil, DefaultTrustPolicy())
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		Registry: registry,
		Runner:   NewRunner(),
	})

	resolution, err := dispatcher.Resolve(context.Background(), "https://example.com/file.zip")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolution.Matched {
		t.Fatalf("Resolve() matched = true, want false")
	}
	if len(resolution.Items) != 0 {
		t.Fatalf("Resolve() returned items for no-match: %#v", resolution.Items)
	}
}

func TestAddTaskDispatcherCandidateNoMatchFallsBack(t *testing.T) {
	dispatcher := newFixtureAddTaskDispatcher(t, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":false}`,
		extractJSON:    `{"items":[{"url":"https://download.fixture.invalid/unused.bin"}]}`,
		memoryMinPages: 1,
	}), nil)

	resolution, err := dispatcher.Resolve(context.Background(), "https://share.fixture.invalid/s/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolution.Matched {
		t.Fatalf("Resolve() matched = true, want false for candidate Match=false")
	}
	if len(resolution.Items) != 0 {
		t.Fatalf("Resolve() returned items for no-match candidate: %#v", resolution.Items)
	}
}

func TestAddTaskDispatcherRunsVerifiedFixturePack(t *testing.T) {
	dispatcher := newFixtureAddTaskDispatcher(t, validRunnerFixtureWASM(), nil)

	resolution, err := dispatcher.Resolve(context.Background(), "https://share.fixture.invalid/s/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Matched {
		t.Fatal("Resolve() matched = false, want true")
	}
	if resolution.PackID != "xpk-fixture01" {
		t.Fatalf("Resolve() PackID = %q, want xpk-fixture01", resolution.PackID)
	}
	if len(resolution.Items) != 1 {
		t.Fatalf("Resolve() returned %d items, want 1", len(resolution.Items))
	}
	item := resolution.Items[0]
	if item.URL != "https://download.fixture.invalid/file.bin" {
		t.Fatalf("item.URL = %q", item.URL)
	}
	if item.Filename != "file.bin" || item.SizeBytes != 123 {
		t.Fatalf("item filename/size = %q/%d", item.Filename, item.SizeBytes)
	}
	if item.AuthProfileRef != "apr-fixture01" || item.HeaderProfileRef != "hpr-fixture01" {
		t.Fatalf("item refs = auth:%q header:%q", item.AuthProfileRef, item.HeaderProfileRef)
	}
	if item.PackIdentity.PackID != resolution.PackID || item.PackIdentity.PackVersion != item.PackManifest.PackVersion {
		t.Fatalf("item identity/manifest not propagated: identity=%#v manifest=%#v", item.PackIdentity, item.PackManifest)
	}
	if item.PackManifest.PackID != resolution.PackID || len(item.PackManifest.Domains) == 0 || item.PackManifest.Domains[0].Host != "fixture.invalid" {
		t.Fatalf("item manifest = %#v, want verified pack manifest", item.PackManifest)
	}
}

func TestAddTaskDispatcherAuthRuntimeRequestsForSourceUsesVerifiedMatches(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(86)
	pack := signedTestPack(t, privateKey, []byte("auth planning payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
		values["domains"] = []map[string]any{{"host": "share.alpha.test"}}
	})
	registry, rejections := NewRegistry([]EmbeddedPack{pack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{Registry: registry})

	requests, err := dispatcher.AuthRuntimeRequestsForSource(context.Background(), "https://share.alpha.test/d/abc")
	if err != nil {
		t.Fatalf("AuthRuntimeRequestsForSource() error = %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("AuthRuntimeRequestsForSource() returned %d requests, want 1", len(requests))
	}
	verified := registry.Packs()[0]
	request := requests[0]
	if request.PackIdentity != verified.Identity {
		t.Fatalf("request identity = %#v, want %#v", request.PackIdentity, verified.Identity)
	}
	if request.Manifest.PackID != verified.Manifest.PackID || request.Manifest.PackVersion != verified.Manifest.PackVersion || !ManifestHasCapability(request.Manifest, CapabilityAuthProfile) {
		t.Fatalf("request manifest = %#v, want verified auth-capable manifest", request.Manifest)
	}
	if request.SourceURL != "https://share.alpha.test/d/abc" || request.TargetURL != "" || request.ProfileRef != "" {
		t.Fatalf("request source/target/profile = %#v", request)
	}

	noMatch, err := dispatcher.AuthRuntimeRequestsForSource(context.Background(), "https://example.test/file.bin")
	if err != nil {
		t.Fatalf("AuthRuntimeRequestsForSource() no-match error = %v", err)
	}
	if len(noMatch) != 0 {
		t.Fatalf("AuthRuntimeRequestsForSource() no-match returned %#v", noMatch)
	}
}

func TestAddTaskDispatcherRejectsInvalidExtractedItems(t *testing.T) {
	tests := []struct {
		name        string
		extractJSON string
		wantSafe    string
	}{
		{
			name:        "empty url",
			extractJSON: `{"items":[{"filename":"file.bin"}]}`,
			wantSafe:    "url",
		},
		{
			name:        "unsafe filename",
			extractJSON: `{"items":[{"url":"https://download.fixture.invalid/file.bin","filename":"../secret.txt"}]}`,
			wantSafe:    "filename",
		},
		{
			name:        "credential metadata",
			extractJSON: `{"items":[{"url":"https://download.fixture.invalid/file.bin","metadata":{"token":"secret-value"}}]}`,
			wantSafe:    "metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := newFixtureAddTaskDispatcher(t, buildRunnerFixtureWASM(wasmFixtureConfig{
				abiVersion:     CurrentABIVersion,
				matchJSON:      `{"matched":true}`,
				extractJSON:    tt.extractJSON,
				memoryMinPages: 1,
			}), nil)

			resolution, err := dispatcher.Resolve(context.Background(), "https://share.fixture.invalid/s/abc")
			if err == nil {
				t.Fatalf("Resolve() error = nil, resolution = %#v, want error", resolution)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tt.wantSafe) {
				t.Fatalf("Resolve() error = %q, want to mention %q", err.Error(), tt.wantSafe)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("Resolve() error leaked secret: %q", err.Error())
			}
		})
	}
}

func TestAddTaskDispatcherMatchedEmptyExtractOutputReturnsGenericFailure(t *testing.T) {
	dispatcher := newFixtureAddTaskDispatcher(t, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
	}), nil)

	resolution, err := dispatcher.Resolve(context.Background(), "https://share.fixture.invalid/s/abc")
	if err == nil {
		t.Fatalf("Resolve() error = nil, resolution = %#v, want generic unsupported/auth failure", resolution)
	}
	if strings.Contains(err.Error(), "extract output must contain at least one item") || strings.Contains(err.Error(), "invalid add item") {
		t.Fatalf("Resolve() leaked internal empty-output validation error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "could not resolve this link") {
		t.Fatalf("Resolve() error = %q, want generic resolver failure", err.Error())
	}
	if !IsGenericAuthResolutionError(err) {
		t.Fatalf("IsGenericAuthResolutionError(%q) = false, want true", err.Error())
	}
}

func TestAddTaskDispatcherInvalidNonEmptyItemRemainsHardFailure(t *testing.T) {
	dispatcher := newFixtureAddTaskDispatcher(t, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[{"filename":"file.bin"}]}`,
		memoryMinPages: 1,
	}), nil)

	resolution, err := dispatcher.Resolve(context.Background(), "https://share.fixture.invalid/s/abc")
	if err == nil {
		t.Fatalf("Resolve() error = nil, resolution = %#v, want hard invalid-item failure", resolution)
	}
	if !strings.Contains(err.Error(), "invalid add item") || !strings.Contains(err.Error(), "item 0 url") {
		t.Fatalf("Resolve() error = %q, want hard invalid-item failure", err.Error())
	}
	if IsGenericAuthResolutionError(err) {
		t.Fatalf("IsGenericAuthResolutionError(%q) = true, want false", err.Error())
	}
}

func TestIsGenericAuthResolutionErrorRejectsHardFailures(t *testing.T) {
	for _, err := range []error{
		errors.New("extractor pack \"fixturepack\" returned invalid add item: item 0 url: item url must use http or https"),
		errors.New("item 0 filename: filename must not contain path separators"),
		errors.New("embedded pack signature verification failed"),
		errors.New("runner failed"),
		nil,
	} {
		if IsGenericAuthResolutionError(err) {
			t.Fatalf("IsGenericAuthResolutionError(%v) = true, want false", err)
		}
	}
}

func TestAddTaskDispatcherContinuesAfterMatchedEmptyOutput(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(85)
	emptyPack := signedTestPack(t, privateKey, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
	}), func(values map[string]any) {
		values["pack_id"] = "xpk-fixture01-empty"
	})
	resolvingPack := signedTestPack(t, privateKey, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[{"url":"https://download.fixture.invalid/downloads/fallback.bin","filename":"fallback.bin"}]}`,
		memoryMinPages: 1,
	}), func(values map[string]any) {
		values["pack_id"] = "xpk-fixture01-resolver"
	})
	registry, rejections := NewRegistry([]EmbeddedPack{emptyPack, resolvingPack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{Registry: registry, Runner: NewRunner()})

	resolution, err := dispatcher.Resolve(context.Background(), "https://share.fixture.invalid/s/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Matched || resolution.PackID != "xpk-fixture01-resolver" || len(resolution.Items) != 1 {
		t.Fatalf("resolution = %#v, want later resolving pack item", resolution)
	}
	if resolution.Items[0].URL != "https://download.fixture.invalid/downloads/fallback.bin" {
		t.Fatalf("resolved URL = %q", resolution.Items[0].URL)
	}
}

func TestAddTaskDispatcherUsesContextAwareAliasRegistry(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(86)
	pack := signedTestPack(t, privateKey, validRunnerFixtureWASM(), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["pack_version"] = "opaque-1"
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
	})
	registry, rejections := NewRegistryWithHostPolicyResolver([]EmbeddedPack{pack}, policyWithKeys(publicKey), nil)
	if len(rejections) != 0 {
		t.Fatalf("NewRegistryWithHostPolicyResolver() rejections = %#v", rejections)
	}
	verified := registry.Packs()[0]
	hostPolicy := syntheticHostPolicy(verified.Identity)
	hostPolicy.OutputDomains = []HostPolicyOutputRule{{Host: "download.fixture.invalid", PathPrefixes: []string{"/"}}}
	registry.hostPolicyResolver = &fakeHostPolicyResolver{policy: hostPolicy}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{Registry: registry, Runner: NewRunnerWithConfig(RunnerConfig{HostPolicyResolver: registry.hostPolicyResolver})})

	resolution, err := dispatcher.Resolve(context.Background(), "https://share.alpha.test/item")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Matched || resolution.PackID != "xpk-alpha001" {
		t.Fatalf("resolution = %#v, want alias match", resolution)
	}
}

func TestAddTaskDispatcherAliasOutputPolicyIsSeparateFromBrokerPolicy(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(87)
	makeDispatcher := func(t *testing.T, outputURL string) *AddTaskDispatcher {
		t.Helper()
		payload := buildRunnerFixtureWASM(wasmFixtureConfig{
			abiVersion:     CurrentABIVersion,
			matchJSON:      `{"matched":true,"confidence":90,"reason":"fixture"}`,
			extractJSON:    `{"items":[{"url":"` + outputURL + `","filename":"file.bin"}]}`,
			memoryMinPages: 1,
		})
		pack := signedTestPack(t, privateKey, payload, func(values map[string]any) {
			values["pack_id"] = "xpk-alpha001"
			values["pack_version"] = "opaque-1"
			values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
			values["domains"] = []map[string]any{}
			values["domain_policy_refs"] = []string{"dpr-alpha001"}
			values["broker_policy_refs"] = []string{"bpr-alpha001"}
		})
		registry, rejections := NewRegistryWithHostPolicyResolver([]EmbeddedPack{pack}, policyWithKeys(publicKey), nil)
		if len(rejections) != 0 {
			t.Fatalf("NewRegistryWithHostPolicyResolver() rejections = %#v", rejections)
		}
		verified := registry.Packs()[0]
		policy := syntheticHostPolicy(verified.Identity)
		policy.BrokerDomains = []DomainRule{{Host: "api.alpha.test"}, {Host: "page.alpha.test"}}
		policy.OutputDomains = []HostPolicyOutputRule{{Host: "files.alpha.test", IncludeSubdomains: false, PathPrefixes: []string{"/downloads/"}}}
		registry.hostPolicyResolver = &fakeHostPolicyResolver{policy: policy}

		return NewAddTaskDispatcher(AddTaskDispatcherConfig{Registry: registry, Runner: NewRunnerWithConfig(RunnerConfig{HostPolicyResolver: registry.hostPolicyResolver})})
	}

	tests := []struct {
		name      string
		outputURL string
		wantMatch bool
	}{
		{name: "explicit output path", outputURL: "https://files.alpha.test/downloads/file.bin", wantMatch: true},
		{name: "broker api host denied", outputURL: "https://api.alpha.test/files/file.bin"},
		{name: "broker page host denied", outputURL: "https://page.alpha.test/images/file.bin"},
		{name: "provider owned non download path denied", outputURL: "https://files.alpha.test/private/file.bin"},
		{name: "unproven subdomain denied", outputURL: "https://cdn.files.alpha.test/downloads/file.bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher := makeDispatcher(t, tt.outputURL)
			resolution, err := dispatcher.Resolve(context.Background(), "https://share.alpha.test/item")
			if tt.wantMatch {
				if err != nil {
					t.Fatalf("Resolve() error = %v", err)
				}
				if !resolution.Matched || len(resolution.Items) != 1 || resolution.Items[0].URL != tt.outputURL {
					t.Fatalf("resolution = %#v, want allowed output", resolution)
				}
				return
			}
			if err == nil {
				t.Fatalf("Resolve() error = nil, resolution = %#v, want output policy denial", resolution)
			}
			if strings.Contains(err.Error(), tt.outputURL) {
				t.Fatalf("Resolve() leaked denied output URL: %q", err.Error())
			}
		})
	}
}

func TestAddTaskAria2HeaderExpansionUsesHostResolversOnly(t *testing.T) {
	authResolver := &fakeAuthProfileResolver{secret: ResolvedAuthSecret{
		HeaderName:  "Authorization",
		HeaderValue: "Bearer test-secret",
	}}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		AuthResolver: authResolver,
		HeaderResolver: fakeHeaderProfileResolver{headers: map[string][]string{
			"download": {"User-Agent: GoAria-Test", "Authorization: Bearer test-secret"},
		}},
	})
	item := ResolvedAddItem{
		PackID:           "xpk-fixture01",
		URL:              "https://download.fixture.invalid/file.bin",
		AuthProfileRef:   "default",
		HeaderProfileRef: "download",
	}

	headers, err := dispatcher.BuildAria2Headers(context.Background(), item)
	if err != nil {
		t.Fatalf("BuildAria2Headers() error = %v", err)
	}
	want := []string{"Authorization: Bearer test-secret", "User-Agent: GoAria-Test"}
	if strings.Join(headers, "\n") != strings.Join(want, "\n") {
		t.Fatalf("headers = %#v, want %#v", headers, want)
	}

	missing := NewAddTaskDispatcher(AddTaskDispatcherConfig{})
	if _, err := missing.BuildAria2Headers(context.Background(), item); err == nil {
		t.Fatal("BuildAria2Headers() with missing resolvers error = nil, want error")
	}

	failing := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		AuthResolver: &fakeAuthProfileResolver{err: errors.New("raw-token-123 failed")},
	})
	_, err = failing.BuildAria2Headers(context.Background(), ResolvedAddItem{
		PackID:         "xpk-fixture01",
		URL:            "https://download.fixture.invalid/file.bin?token=raw-token-123",
		AuthProfileRef: "default",
	})
	if err == nil {
		t.Fatal("BuildAria2Headers() missing profile error = nil, want error")
	}
	if strings.Contains(err.Error(), "raw-token-123") {
		t.Fatalf("BuildAria2Headers() leaked secret: %q", err.Error())
	}
}

func TestAddTaskAria2HeaderExpansionUsesIdentityAwareMaterializer(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	manifest := hostAuthRuntimeManifest(identity)
	resolver := &fakeIdentityAwareAuthResolver{material: MaterializedAuthSecret{
		HeaderName:      "Authorization",
		Kind:            AuthSecretKindBearer,
		RedactedDisplay: "safe bearer",
		headerValue:     "Bearer identity-aware-token",
		sensitiveValues: []string{"Bearer identity-aware-token", "identity-aware-token"},
	}}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{AuthResolver: resolver})

	headers, err := dispatcher.BuildAria2Headers(context.Background(), ResolvedAddItem{
		SourceURL:      "https://share.alpha.test/source",
		PackID:         identity.PackID,
		PackIdentity:   identity,
		PackManifest:   manifest,
		URL:            "https://files.alpha.test/file.bin?token=query-secret",
		AuthProfileRef: "apr-alpha001",
	})
	if err != nil {
		t.Fatalf("BuildAria2Headers() error = %v", err)
	}
	if strings.Join(headers, "\n") != "Authorization: Bearer identity-aware-token" {
		t.Fatalf("headers = %#v, want identity-aware bearer header", headers)
	}
	if resolver.calls != 1 || resolver.legacyCalls != 0 {
		t.Fatalf("materializer calls=%d legacy=%d, want 1/0", resolver.calls, resolver.legacyCalls)
	}
	if resolver.lastRequest.PackIdentity != identity || resolver.lastRequest.Manifest.PackID != manifest.PackID || resolver.lastRequest.SourceURL != "https://share.alpha.test/source" || resolver.lastRequest.TargetURL != "https://files.alpha.test/file.bin?token=query-secret" || resolver.lastRequest.ProfileRef != "apr-alpha001" {
		t.Fatalf("materializer request = %#v", resolver.lastRequest)
	}

	failing := &fakeIdentityAwareAuthResolver{err: errors.New("Authorization: Bearer identity-aware-token query-secret failed")}
	_, err = NewAddTaskDispatcher(AddTaskDispatcherConfig{AuthResolver: failing}).BuildAria2Headers(context.Background(), ResolvedAddItem{
		SourceURL:      "https://share.alpha.test/source",
		PackID:         identity.PackID,
		PackIdentity:   identity,
		PackManifest:   manifest,
		URL:            "https://files.alpha.test/file.bin?token=query-secret",
		AuthProfileRef: "apr-alpha001",
	})
	if err == nil {
		t.Fatal("BuildAria2Headers() failing materializer error = nil")
	}
	assertNoForbiddenSubstrings(t, err.Error(), "identity-aware-token", "query-secret")
}

func TestAddTaskOwnedSubdomainAuthMaterializationAfterOutputPolicyAcceptance(t *testing.T) {
	const (
		packID    = "xpk-fixture01"
		profileID = AuthProfileID("apr-fixture01")
		targetURL = "https://assets.fixture.invalid/download/synthetic-file.bin"
	)
	policy := ResolvedHostPolicy{
		OutputDomains: []HostPolicyOutputRule{{
			Host:              "fixture.invalid",
			IncludeSubdomains: true,
			PathPrefixes:      []string{"/files/", "/download/"},
		}},
		AuthProfiles: []HostPolicyAuthProfileScope{{
			ProfileID: profileID,
			Domains:   []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
		}},
	}
	if err := policyAllowsOutputURL(policy, targetURL); err != nil {
		t.Fatalf("policyAllowsOutputURL() error = %v", err)
	}
	store, err := NewFileAuthProfileStore(t.TempDir() + "/extractor_auth.json")
	if err != nil {
		t.Fatalf("NewFileAuthProfileStore() error = %v", err)
	}
	if _, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:         packID,
		ProfileID:      profileID,
		Kind:           AuthSecretKindBearer,
		Secret:         "synthetic-fixture-token",
		AllowedDomains: []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
	}); err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}

	headers, err := NewAddTaskDispatcher(AddTaskDispatcherConfig{AuthResolver: store}).BuildAria2Headers(context.Background(), ResolvedAddItem{
		PackID:         packID,
		HostPolicy:     &policy,
		URL:            targetURL,
		AuthProfileRef: string(profileID),
	})
	if err != nil {
		t.Fatalf("BuildAria2Headers() error = %v", err)
	}
	if strings.Join(headers, "\n") != "Authorization: Bearer synthetic-fixture-token" {
		t.Fatalf("headers = %#v, want bearer header", headers)
	}
}

func TestAddTaskOldAuthScopeDeniesOwnedSubdomainBeforeResolver(t *testing.T) {
	const (
		packID    = "xpk-fixture01"
		profileID = AuthProfileID("apr-fixture01")
		targetURL = "https://assets.fixture.invalid/download/synthetic-file.bin"
	)
	policy := ResolvedHostPolicy{
		OutputDomains: []HostPolicyOutputRule{{
			Host:              "fixture.invalid",
			IncludeSubdomains: true,
			PathPrefixes:      []string{"/files/", "/download/"},
		}},
		AuthProfiles: []HostPolicyAuthProfileScope{{
			ProfileID: profileID,
			Domains:   []DomainRule{{Host: "api.fixture.invalid"}, {Host: "download.fixture.invalid", IncludeSubdomains: true}},
		}},
	}
	if err := policyAllowsOutputURL(policy, targetURL); err != nil {
		t.Fatalf("policyAllowsOutputURL() error = %v", err)
	}
	authResolver := &fakeAuthProfileResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer synthetic-fixture-token"}}
	_, err := NewAddTaskDispatcher(AddTaskDispatcherConfig{AuthResolver: authResolver}).BuildAria2Headers(context.Background(), ResolvedAddItem{
		PackID:         packID,
		HostPolicy:     &policy,
		URL:            targetURL,
		AuthProfileRef: string(profileID),
	})
	if err == nil {
		t.Fatal("BuildAria2Headers() error = nil, want old auth-scope denial")
	}
	if authResolver.calls != 0 {
		t.Fatalf("auth resolver calls = %d, want 0", authResolver.calls)
	}
	assertNoForbiddenSubstrings(t, err.Error(), "synthetic-fixture-token", targetURL)
}

func TestAddTaskAliasFinalAuthExpansionChecksHostPolicyBeforeResolver(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	policy := syntheticHostPolicy(pack.Identity)
	policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: "alpha-secret", Domains: []DomainRule{{Host: "api.alpha.test"}}}}
	authResolver := &fakeAuthProfileResolver{secret: ResolvedAuthSecret{
		HeaderName:  "Authorization",
		HeaderValue: "Bearer alpha-secret",
	}}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		AuthResolver: authResolver,
	})
	item := ResolvedAddItem{
		PackID:         pack.Manifest.PackID,
		PackManifest:   pack.Manifest,
		PackIdentity:   pack.Identity,
		HostPolicy:     &policy,
		URL:            "https://files.alpha.test/file.bin",
		AuthProfileRef: "alpha-secret",
	}

	_, err := dispatcher.BuildAria2Headers(context.Background(), item)
	if err == nil {
		t.Fatal("BuildAria2Headers() error = nil, want alias auth-scope denial")
	}
	if authResolver.calls != 0 {
		t.Fatalf("auth resolver calls = %d, want 0", authResolver.calls)
	}
}

func TestAddTaskAliasFinalAuthExpansionRequiresHostPolicy(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	authResolver := &fakeAuthProfileResolver{secret: ResolvedAuthSecret{
		HeaderName:  "Authorization",
		HeaderValue: "Bearer alpha-secret",
	}}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{AuthResolver: authResolver})

	_, err := dispatcher.BuildAria2Headers(context.Background(), ResolvedAddItem{
		PackID:         pack.Manifest.PackID,
		PackManifest:   pack.Manifest,
		PackIdentity:   pack.Identity,
		URL:            "https://api.alpha.test/file.bin",
		AuthProfileRef: "alpha-secret",
	})
	if err == nil {
		t.Fatal("BuildAria2Headers() error = nil, want missing alias policy denial")
	}
	if authResolver.calls != 0 {
		t.Fatalf("auth resolver calls = %d, want 0", authResolver.calls)
	}
}

func newFixtureAddTaskDispatcher(t *testing.T, payload []byte, mutate func(map[string]any)) *AddTaskDispatcher {
	t.Helper()

	publicKey, privateKey := deterministicKeyPair(84)
	registry, rejections := NewRegistry([]EmbeddedPack{signedTestPack(t, privateKey, payload, mutate)}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}

	return NewAddTaskDispatcher(AddTaskDispatcherConfig{
		Registry: registry,
		Runner:   NewRunner(),
	})
}

var (
	_ AuthProfileResolver   = (*fakeAuthProfileResolver)(nil)
	_ HeaderProfileResolver = fakeHeaderProfileResolver{}
	_                       = ed25519.PublicKey{}
)
