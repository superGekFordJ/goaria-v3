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
}

type recordingAuthProfileMaterializer struct {
	material MaterializedAuthSecret
	err      error
	requests []HostAuthRuntimeRequest
}

func (r fakeAuthProfileResolver) ResolveAuthProfile(ctx context.Context, packID string, profileID AuthProfileID, rawURL string) (ResolvedAuthSecret, error) {
	if r.err != nil {
		return ResolvedAuthSecret{}, r.err
	}
	return r.secret, nil
}

func (r *recordingAuthProfileMaterializer) ResolveAuthProfile(ctx context.Context, packID string, profileID AuthProfileID, rawURL string) (ResolvedAuthSecret, error) {
	return ResolvedAuthSecret{}, errors.New("compat resolver should not be used")
}

func (r *recordingAuthProfileMaterializer) MaterializeAuthProfile(ctx context.Context, request HostAuthRuntimeRequest) (MaterializedAuthSecret, error) {
	r.requests = append(r.requests, request)
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

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
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

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Matched {
		t.Fatal("Resolve() matched = false, want true")
	}
	if resolution.PackID != "fixturepack" {
		t.Fatalf("Resolve() PackID = %q, want fixturepack", resolution.PackID)
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
	if item.AuthProfileRef != "fixturepack-default" || item.HeaderProfileRef != "fixturepack-download" {
		t.Fatalf("item refs = auth:%q header:%q", item.AuthProfileRef, item.HeaderProfileRef)
	}
	if item.PackIdentity.PackID != resolution.PackID || item.PackIdentity.PackVersion != item.Manifest.PackVersion {
		t.Fatalf("item identity/manifest not propagated: identity=%#v manifest=%#v", item.PackIdentity, item.Manifest)
	}
	if item.Manifest.PackID != resolution.PackID || len(item.Manifest.Domains) == 0 || item.Manifest.Domains[0].Host != "fixture.invalid" {
		t.Fatalf("item manifest = %#v, want verified pack manifest", item.Manifest)
	}
}

func TestAddTaskDispatcherAuthRuntimeRequestsForSourceUsesVerifiedMatches(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(86)
	pack := signedTestPack(t, privateKey, []byte("auth planning payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
	})
	registry, rejections := NewRegistry([]EmbeddedPack{pack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{Registry: registry})

	requests, err := dispatcher.AuthRuntimeRequestsForSource(context.Background(), "https://fixture.invalid/d/abc")
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
	if request.SourceURL != "https://fixture.invalid/d/abc" || request.TargetURL != "" || request.ProfileRef != "" {
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

			resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
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

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
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

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
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

func TestAddTaskDispatcherAliasOutputPolicyDeniesBeforeResolvedItem(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	policy := validResolvedHostPolicy(identity, manifest)
	pack := VerifiedPack{Manifest: manifest, Identity: identity}
	rawURL := "https://files.alpha.test/downloads%2fitem.bin?token=raw-query-secret"

	items, err := resolvedItemsFromExtractOutput("https://share.alpha.test/source", pack, &policy, ExtractOutput{Items: []ExtractedItemRef{{
		ID:             "item-1",
		URL:            rawURL,
		Filename:       "file.bin",
		AuthProfileRef: "apr-alpha001",
	}}})
	if err == nil {
		t.Fatalf("resolvedItemsFromExtractOutput() error = nil, items=%#v", items)
	}
	if len(items) != 0 {
		t.Fatalf("resolvedItemsFromExtractOutput() returned items on denied output: %#v", items)
	}
	assertNoForbiddenSubstrings(t, err.Error(), "raw-query-secret")
}

func TestAddTaskDispatcherResolvedItemsCarryDefensiveAliasPolicyCopies(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	policy := validResolvedHostPolicy(identity, manifest)
	pack := VerifiedPack{Manifest: manifest, Identity: identity}
	metadata := map[string]string{"label": "fixture-item"}

	items, err := resolvedItemsFromExtractOutput("https://share.alpha.test/source", pack, &policy, ExtractOutput{Items: []ExtractedItemRef{{
		ID:               "item-1",
		URL:              "https://files.alpha.test/downloads/item.bin",
		Filename:         "file.bin",
		SizeBytes:        123,
		AuthProfileRef:   "apr-alpha001",
		HeaderProfileRef: "hpr-alpha001",
		Metadata:         metadata,
	}}})
	if err != nil {
		t.Fatalf("resolvedItemsFromExtractOutput() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("resolvedItemsFromExtractOutput() items = %d, want 1", len(items))
	}
	item := items[0]
	if item.SourceURL != "https://share.alpha.test/source" || item.PackID != manifest.PackID || item.PackIdentity != identity || item.URL != "https://files.alpha.test/downloads/item.bin" || item.AuthProfileRef != "apr-alpha001" || item.HeaderProfileRef != "hpr-alpha001" || item.Metadata["label"] != "fixture-item" {
		t.Fatalf("resolved item did not preserve expected fields: %#v", item)
	}
	if item.Manifest.PackID != manifest.PackID || item.Manifest.DomainPolicyRefs[0] != "dpr-alpha001" || item.HostPolicy == nil || item.HostPolicy.OutputDomains[0].PathPrefixes[0] != "/downloads/" {
		t.Fatalf("resolved item missing manifest/host policy copies: %#v", item)
	}

	metadata["label"] = "mutated"
	originalPackDomainRef := pack.Manifest.DomainPolicyRefs[0]
	pack.Manifest.DomainPolicyRefs[0] = "dpr-source-mutated"
	policy.OutputDomains[0].PathPrefixes[0] = "/mutated/"
	item.Metadata["label"] = "item-mutated"
	item.Manifest.DomainPolicyRefs[0] = "item-mutated"
	item.HostPolicy.OutputDomains[0].PathPrefixes[0] = "/item-mutated/"
	if metadata["label"] != "mutated" || items[0].Manifest.DomainPolicyRefs[0] != "item-mutated" || items[0].HostPolicy.OutputDomains[0].PathPrefixes[0] != "/item-mutated/" {
		t.Fatalf("resolved item copy mutation expectation failed: item=%#v source=%#v", items[0], metadata)
	}
	if originalPackDomainRef != "dpr-alpha001" || pack.Manifest.DomainPolicyRefs[0] != "dpr-source-mutated" || items[0].Manifest.DomainPolicyRefs[0] == pack.Manifest.DomainPolicyRefs[0] || policy.OutputDomains[0].PathPrefixes[0] == items[0].HostPolicy.OutputDomains[0].PathPrefixes[0] {
		t.Fatalf("resolved item did not remain independent from source mutations: source=%#v policy=%#v item=%#v", pack, policy, items[0])
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

func TestAddTaskValidateResolvedItemAuthPolicyAliasScope(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	policy := validResolvedHostPolicy(identity, manifest)
	policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: "apr-alpha001", Domains: []DomainRule{{Host: "files.alpha.test", IncludeSubdomains: true}}}}

	allowed := ResolvedAddItem{
		PackIdentity:   identity,
		Manifest:       manifest,
		HostPolicy:     &policy,
		URL:            "https://cdn.files.alpha.test/downloads/item.bin",
		AuthProfileRef: "apr-alpha001",
	}
	if err := ValidateResolvedAddItemAuthPolicy(allowed); err != nil {
		t.Fatalf("ValidateResolvedAddItemAuthPolicy() allowed error = %v", err)
	}

	denied := allowed
	denied.URL = "https://api.alpha.test/downloads/item.bin?token=target-secret"
	if err := ValidateResolvedAddItemAuthPolicy(denied); err == nil {
		t.Fatal("ValidateResolvedAddItemAuthPolicy() denial error = nil")
	} else {
		assertNoForbiddenSubstrings(t, err.Error(), "target-secret", denied.URL)
	}

	missingPolicy := allowed
	missingPolicy.HostPolicy = nil
	if err := ValidateResolvedAddItemAuthPolicy(missingPolicy); err == nil {
		t.Fatal("ValidateResolvedAddItemAuthPolicy() missing alias host policy error = nil")
	}

	invalidRef := allowed
	invalidRef.AuthProfileRef = "Invalid"
	if err := ValidateResolvedAddItemAuthPolicy(invalidRef); err == nil {
		t.Fatal("ValidateResolvedAddItemAuthPolicy() invalid auth ref error = nil")
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
		values["pack_id"] = "fixturepack-empty"
	})
	resolvingPack := signedTestPack(t, privateKey, buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[{"url":"https://download.fixture.invalid/fallback.bin","filename":"fallback.bin"}]}`,
		memoryMinPages: 1,
	}), func(values map[string]any) {
		values["pack_id"] = "fixturepack-resolver"
	})
	registry, rejections := NewRegistry([]EmbeddedPack{emptyPack, resolvingPack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{Registry: registry, Runner: NewRunner()})

	resolution, err := dispatcher.Resolve(context.Background(), "https://fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Matched || resolution.PackID != "fixturepack-resolver" || len(resolution.Items) != 1 {
		t.Fatalf("resolution = %#v, want later resolving pack item", resolution)
	}
	if resolution.Items[0].URL != "https://download.fixture.invalid/fallback.bin" {
		t.Fatalf("resolved URL = %q", resolution.Items[0].URL)
	}
}

func TestAddTaskAria2HeaderExpansionValidatesBoundsDedupesAndRedacts(t *testing.T) {
	t.Run("dedupes", func(t *testing.T) {
		dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
			HeaderResolver: fakeHeaderProfileResolver{headers: map[string][]string{
				"download": {"User-Agent: GoAria-Test", "User-Agent: GoAria-Test"},
			}},
		})

		headers, err := dispatcher.BuildAria2Headers(context.Background(), ResolvedAddItem{
			PackID:           "fixturepack",
			URL:              "https://download.fixture.invalid/file.bin",
			HeaderProfileRef: "download",
		})
		if err != nil {
			t.Fatalf("BuildAria2Headers() error = %v", err)
		}
		if len(headers) != 1 || headers[0] != "User-Agent: GoAria-Test" {
			t.Fatalf("headers = %#v, want deduped single header", headers)
		}
	})

	t.Run("header count", func(t *testing.T) {
		headers := make([]string, maxAria2Headers+1)
		for i := range headers {
			headers[i] = "X-Test-" + strings.Repeat("A", i+1) + ": value"
		}
		dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
			HeaderResolver: fakeHeaderProfileResolver{headers: map[string][]string{"download": headers}},
		})
		if _, err := dispatcher.BuildAria2Headers(context.Background(), ResolvedAddItem{
			PackID:           "fixturepack",
			URL:              "https://download.fixture.invalid/file.bin",
			HeaderProfileRef: "download",
		}); err == nil {
			t.Fatal("BuildAria2Headers() header count error = nil")
		}
	})

	t.Run("invalid materialized auth", func(t *testing.T) {
		identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
		manifest := hostAuthRuntimeManifest(identity)
		materializer := &recordingAuthProfileMaterializer{material: MaterializedAuthSecret{HeaderName: "Authorization", Kind: AuthSecretKindBearer}}
		materializer.material.headerValue = "Bearer materialized-secret\r\nInjected: value"
		dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{AuthResolver: materializer})

		_, err := dispatcher.BuildAria2Headers(context.Background(), ResolvedAddItem{
			SourceURL:      "https://fixture.invalid/source?token=source-secret",
			PackID:         identity.PackID,
			PackIdentity:   identity,
			Manifest:       manifest,
			URL:            "https://fixture.invalid/file.bin?token=query-secret",
			AuthProfileRef: "apr-alpha001",
		})
		if err == nil {
			t.Fatal("BuildAria2Headers() invalid materialized auth error = nil")
		}
		assertNoForbiddenSubstrings(t, err.Error(), "materialized-secret", "query-secret", "source-secret", "Authorization")
		if len(materializer.requests) != 1 {
			t.Fatalf("MaterializeAuthProfile() calls = %d, want 1", len(materializer.requests))
		}
		req := materializer.requests[0]
		if req.PackIdentity != identity || req.Manifest.PackID != manifest.PackID || req.SourceURL != "https://fixture.invalid/source?token=source-secret" || req.TargetURL != "https://fixture.invalid/file.bin?token=query-secret" || req.ProfileRef != "apr-alpha001" {
			t.Fatalf("materializer request = %#v, want exact item-bound request", req)
		}
	})
}

func TestAddTaskAria2HeaderExpansionUsesHostResolversOnly(t *testing.T) {
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		AuthResolver: fakeAuthProfileResolver{secret: ResolvedAuthSecret{
			HeaderName:  "Authorization",
			HeaderValue: "Bearer test-secret",
		}},
		HeaderResolver: fakeHeaderProfileResolver{headers: map[string][]string{
			"download": {"User-Agent: GoAria-Test", "Authorization: Bearer test-secret"},
		}},
	})
	item := ResolvedAddItem{
		PackID:           "fixturepack",
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
		AuthResolver: fakeAuthProfileResolver{err: errors.New("raw-token-123 failed")},
	})
	_, err = failing.BuildAria2Headers(context.Background(), ResolvedAddItem{
		PackID:         "fixturepack",
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
	store := newTempAuthProfileStore(t)
	setHostAuthProfile(t, store, identity.PackID, "apr-alpha001", AuthSecretKindBearer, "identity-aware-token", []DomainRule{{Host: "fixture.invalid"}}, nil)
	runtime := NewHostAuthRuntime(HostAuthRuntimeConfig{
		Bundle: newHostAuthRuntimeBundle(t, identity, nil, nil),
		Store:  store,
	})
	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{AuthResolver: runtime})

	headers, err := dispatcher.BuildAria2Headers(context.Background(), ResolvedAddItem{
		SourceURL:      "https://fixture.invalid/source",
		PackID:         identity.PackID,
		PackIdentity:   identity,
		Manifest:       manifest,
		URL:            "https://fixture.invalid/file.bin?token=query-secret",
		AuthProfileRef: "apr-alpha001",
	})
	if err != nil {
		t.Fatalf("BuildAria2Headers() error = %v", err)
	}
	if strings.Join(headers, "\n") != "Authorization: Bearer identity-aware-token" {
		t.Fatalf("headers = %#v, want identity-aware bearer header", headers)
	}

	badIdentity := identity
	badIdentity.PackVersion = "opaque-2"
	_, err = dispatcher.BuildAria2Headers(context.Background(), ResolvedAddItem{
		SourceURL:      "https://fixture.invalid/source",
		PackID:         identity.PackID,
		PackIdentity:   badIdentity,
		Manifest:       manifest,
		URL:            "https://fixture.invalid/file.bin?token=query-secret",
		AuthProfileRef: "apr-alpha001",
	})
	if err == nil {
		t.Fatal("BuildAria2Headers() mismatched identity error = nil, want fail-closed")
	}
	assertNoForbiddenSubstrings(t, err.Error(), "identity-aware-token", "query-secret", "Authorization")
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
	_ AuthProfileResolver   = fakeAuthProfileResolver{}
	_ HeaderProfileResolver = fakeHeaderProfileResolver{}
	_ AuthProfileResolver   = (*recordingAuthProfileMaterializer)(nil)
	_                       = ed25519.PublicKey{}
)
