//go:build extractor

package wailsapp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

func TestLoadEmbeddedExtractorHostPolicyResolverNoRuntimeSource(t *testing.T) {
	if extractor.HasEmbeddedReleasePacks() {
		t.Skip("embedded release packs are generated for full-pack validation")
	}
	t.Setenv("GOARIA_EXTRACTOR_PRIVATE_POLICY_BUNDLE", "")
	t.Setenv("GOARIA_EXTRACTOR_PRIVATE_POLICY_SHA256", "")

	resolver, err := loadEmbeddedExtractorHostPolicyResolver()
	if err != nil {
		t.Fatalf("loadEmbeddedExtractorHostPolicyResolver() error = %v", err)
	}
	if resolver != nil {
		t.Fatal("resolver != nil for no runtime source")
	}
}

func TestLoadEmbeddedExtractorHostPolicyResolverInvalidEnvPathRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "synthetic-policy.json")
	t.Setenv("GOARIA_EXTRACTOR_PRIVATE_POLICY_BUNDLE", path)
	t.Setenv("GOARIA_EXTRACTOR_PRIVATE_POLICY_SHA256", strings.Repeat("0", 64))

	resolver, err := loadEmbeddedExtractorHostPolicyResolver()
	if err == nil {
		t.Fatal("loadEmbeddedExtractorHostPolicyResolver() error = nil")
	}
	if resolver != nil {
		t.Fatal("resolver != nil for invalid runtime source")
	}
	if !strings.Contains(err.Error(), "private host policy runtime source is invalid") || strings.Contains(err.Error(), path) {
		t.Fatalf("error is not generic/redacted: %q", err.Error())
	}
}

func TestEmbeddedExtractorStartupGeneratedSourceProof(t *testing.T) {
	t.Setenv("GOARIA_EXTRACTOR_PRIVATE_POLICY_BUNDLE", "")
	t.Setenv("GOARIA_EXTRACTOR_PRIVATE_POLICY_SHA256", "")
	t.Setenv("GOARIA_EXTRACTOR_PRIVATE_AUTH_RUNTIME_BUNDLE", "")
	t.Setenv("GOARIA_EXTRACTOR_PRIVATE_AUTH_RUNTIME_SHA256", "")

	hasPacks := extractor.HasEmbeddedReleasePacks()
	policySource := extractor.PrivatePolicyBundleRuntimeSourceState()
	authRuntimeSource := extractor.PrivateAuthRuntimeBundleRuntimeSourceState()
	if !hasPacks && policySource != extractor.PrivateBundleSourceStateEmbedded && authRuntimeSource != extractor.PrivateBundleSourceStateEmbedded {
		t.Skip("generated full-pack outputs are not present in source tree")
	}
	if !hasPacks {
		t.Fatal("generated source proof expected embedded packs to be present")
	}
	if extractor.EmbeddedReleasePackCount() == 0 {
		t.Fatal("embedded release pack count = 0, want generated source packs")
	}
	if policySource != extractor.PrivateBundleSourceStateEmbedded {
		t.Fatalf("policy runtime source = %q, want embedded", policySource)
	}
	if authRuntimeSource != extractor.PrivateBundleSourceStateEmbedded {
		t.Fatalf("auth runtime source = %q, want embedded", authRuntimeSource)
	}

	resolver, err := loadEmbeddedExtractorHostPolicyResolver()
	if err != nil {
		t.Fatalf("loadEmbeddedExtractorHostPolicyResolver() error = %v", err)
	}
	if resolver == nil {
		t.Fatal("resolver = nil, want embedded policy resolver")
	}
	bundle, err := extractor.LoadPrivateAuthRuntimeBundleFromRuntimeSources()
	if err != nil {
		t.Fatalf("LoadPrivateAuthRuntimeBundleFromRuntimeSources() error = %v", err)
	}
	if bundle == nil || bundle.PackCount() == 0 {
		t.Fatalf("bundle = %#v, want embedded auth runtime bundle", bundle)
	}

	app := newWindowedAuthApp(t)
	deps := defaultEmbeddedExtractorConfigDeps(app)
	storePath := filepath.Join(t.TempDir(), "auth.json")
	deps.defaultAuthProfileStorePath = func() (string, error) {
		return storePath, nil
	}
	err = configureEmbeddedExtractorDispatcherWithDeps(app, deps)
	if err != nil {
		t.Fatalf("configureEmbeddedExtractorDispatcherWithDeps() error = %v", err)
	}
	if app.authProfileStoreForTest() == nil {
		t.Fatal("App store = nil, want configured shared store")
	}
	if app.hostAuthRuntimeForTest() == nil {
		t.Fatal("App HostAuthRuntime = nil, want configured runtime")
	}
	if app.authWebViewDriverForTest() == nil {
		t.Fatal("App auth driver = nil, want configured driver")
	}
	if app.extractorAdapterForTest() == nil {
		t.Fatal("App extractor adapter = nil, want configured adapter")
	}
}

type fakeSetupHostPolicyResolver struct {
	expectedIdentity extractor.VerifiedPackIdentity
}

func (r fakeSetupHostPolicyResolver) ResolveHostPolicy(_ context.Context, req extractor.HostPolicyRequest) (extractor.ResolvedHostPolicy, error) {
	identity := req.PackIdentity
	if r.expectedIdentity != (extractor.VerifiedPackIdentity{}) {
		identity = r.expectedIdentity
	}
	return extractor.ResolvedHostPolicy{
		PolicyID:            "hpr-fixture001",
		PolicyVersion:       "fixture-1",
		PolicySHA256:        strings.Repeat("a", 64),
		PackIdentity:        identity,
		DomainPolicyRefs:    []string{packbuilder.HostCallFixtureDomainPolicyRef},
		BrokerPolicyRefs:    []string{packbuilder.HostCallFixtureBrokerPolicyRef},
		AllowedCapabilities: []extractor.Capability{extractor.CapabilityParseWASM, extractor.CapabilityHTTPFetch},
		IngressDomains:      []extractor.DomainRule{{Host: "share.fixture.invalid"}},
		BrokerDomains:       []extractor.DomainRule{{Host: "api.fixture.invalid"}, {Host: "download.fixture.invalid"}},
		OutputDomains: []extractor.HostPolicyOutputRule{{
			Host:         "download.fixture.invalid",
			PathPrefixes: []string{"/"},
		}},
		Endpoints: []extractor.HostPolicyEndpoint{{
			BrokerPolicyRef:  packbuilder.HostCallFixtureBrokerPolicyRef,
			EndpointRef:      packbuilder.HostCallFixtureEndpointRef,
			URLTemplate:      packbuilder.HostCallFixtureAPIBaseURL + "/{id}",
			Methods:          []string{"GET"},
			TimeoutMillis:    1000,
			MaxResponseBytes: 4096,
		}},
	}, nil
}

func TestConfigureEmbeddedExtractorZeroPackStartupAvailableRevisionOne(t *testing.T) {
	app := NewApp(Options{})
	dataRoot := t.TempDir()

	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.hasEmbeddedReleasePacks = func() bool { return false }
	deps.embeddedReleaseRequired = func() bool { return false }
	deps.dataRoot = func() (string, error) { return dataRoot, nil }

	if err := configureEmbeddedExtractorDispatcherWithDeps(app, deps); err != nil {
		t.Fatalf("zero-pack configure error: %v", err)
	}

	state := app.GetExtractorState()
	if !state.Available {
		t.Fatal("expected available: true")
	}
	if state.Sources == nil || len(state.Sources) != 0 {
		t.Fatalf("expected non-nil empty sources, got %#v", state.Sources)
	}
	if state.RecoveryErrors == nil || len(state.RecoveryErrors) != 0 {
		t.Fatalf("expected non-nil empty recovery errors, got %#v", state.RecoveryErrors)
	}
	if app.extractorAdapterForTest() != nil {
		t.Fatal("expected nil adapter for zero-pack")
	}
	rt := app.taggedRuntime()
	if rt == nil || rt.manager == nil {
		t.Fatal("expected tagged runtime manager to be non-nil")
	}
	if rt.manager.CurrentSnapshot().Revision() != 1 {
		t.Fatalf("expected revision 1, got %d", rt.manager.CurrentSnapshot().Revision())
	}
}

func TestConfigureEmbeddedExtractorInjectedDataRoot(t *testing.T) {
	deps := defaultEmbeddedExtractorConfigDeps(nil)
	root, err := deps.dataRoot()
	if err != nil {
		t.Fatalf("dataRoot error: %v", err)
	}
	expected := filepath.Join(filepath.Dir(config.GetConfigPath()), "extractor")
	if root != expected {
		t.Fatalf("dataRoot = %q, want %q", root, expected)
	}
}

func TestConfigureEmbeddedExtractorRecoversUserSource(t *testing.T) {
	dataRoot := t.TempDir()
	packDir := filepath.Join(t.TempDir(), "source-pack")
	lockOut := filepath.Join(packDir, packbuilder.HostCallFixturePackID+".lock.json")

	writeRes, err := packbuilder.WriteHostCallFixture(packDir, lockOut)
	if err != nil {
		t.Fatalf("WriteLocalDirectoryPack: %v", err)
	}

	policyResolver := fakeSetupHostPolicyResolver{}
	mgr, err := extractor.NewExtractorRuntimeManager(context.Background(), extractor.ExtractorRuntimeManagerConfig{
		DataRoot:           dataRoot,
		HostPolicyResolver: policyResolver,
	})
	if err != nil {
		t.Fatalf("NewExtractorRuntimeManager: %v", err)
	}
	_, err = mgr.LoadSource(context.Background(), extractor.RuntimeSourceSpec{
		Kind:    extractor.RuntimeSourceKindLocalDirectory,
		Locator: writeRes.OutDir,
	})
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}

	_ = os.RemoveAll(packDir)

	app := NewApp(Options{})
	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.hasEmbeddedReleasePacks = func() bool { return false }
	deps.embeddedReleaseRequired = func() bool { return false }
	deps.dataRoot = func() (string, error) { return dataRoot, nil }
	deps.loadHostPolicyResolver = func() (extractor.HostPolicyResolver, error) {
		return policyResolver, nil
	}

	if err := configureEmbeddedExtractorDispatcherWithDeps(app, deps); err != nil {
		t.Fatalf("configure error: %v", err)
	}

	state := app.GetExtractorState()
	if !state.Available {
		t.Fatal("expected state available: true")
	}
	if len(state.Sources) != 1 {
		t.Fatalf("expected 1 recovered source, got %d", len(state.Sources))
	}
	if state.Sources[0].Status != ExtractorSourceStatusReady {
		t.Fatalf("expected recovered source to be ready, got %s (err: %s)", state.Sources[0].Status, state.Sources[0].ErrorCode)
	}
	if app.extractorAdapterForTest() == nil {
		t.Fatal("expected non-nil extractor adapter after recovering healthy pack")
	}
}

func TestConfigureEmbeddedExtractorValidConfiguredPolicyLoadsWithZeroEmbeddedPacks(t *testing.T) {
	app := NewApp(Options{})
	dataRoot := t.TempDir()

	policyResolver := fakeSetupHostPolicyResolver{}
	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.hasEmbeddedReleasePacks = func() bool { return false }
	deps.embeddedReleaseRequired = func() bool { return false }
	deps.dataRoot = func() (string, error) { return dataRoot, nil }
	deps.privatePolicyRuntimeSourceState = func() extractor.PrivateBundleSourceState {
		return extractor.PrivateBundleSourceStateEnv
	}
	deps.loadHostPolicyResolver = func() (extractor.HostPolicyResolver, error) {
		return policyResolver, nil
	}

	if err := configureEmbeddedExtractorDispatcherWithDeps(app, deps); err != nil {
		t.Fatalf("configure error: %v", err)
	}
	rt := app.taggedRuntime()
	if rt == nil || rt.manager == nil {
		t.Fatal("expected non-nil runtime manager")
	}
}

func TestConfigureEmbeddedExtractorMissingOptionalPolicyAuthPermitsStartup(t *testing.T) {
	app := NewApp(Options{})
	dataRoot := t.TempDir()

	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.hasEmbeddedReleasePacks = func() bool { return false }
	deps.embeddedReleaseRequired = func() bool { return false }
	deps.dataRoot = func() (string, error) { return dataRoot, nil }
	deps.privatePolicyRuntimeSourceState = func() extractor.PrivateBundleSourceState {
		return extractor.PrivateBundleSourceStateNone
	}
	deps.privateAuthRuntimeRuntimeSourceState = func() extractor.PrivateBundleSourceState {
		return extractor.PrivateBundleSourceStateNone
	}

	if err := configureEmbeddedExtractorDispatcherWithDeps(app, deps); err != nil {
		t.Fatalf("zero pack configure without optional policy/auth error: %v", err)
	}
	if !app.GetExtractorState().Available {
		t.Fatal("expected available: true")
	}
}

func TestConfigureEmbeddedExtractorMalformedConfiguredPolicyFailsRedacted(t *testing.T) {
	app := NewApp(Options{})
	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.dataRoot = func() (string, error) { return t.TempDir(), nil }
	deps.privatePolicyRuntimeSourceState = func() extractor.PrivateBundleSourceState {
		return extractor.PrivateBundleSourceStateEnv
	}
	deps.loadHostPolicyResolver = func() (extractor.HostPolicyResolver, error) {
		return nil, errors.New("sensitive path /secret/policy.json with key=private-key")
	}

	err := configureEmbeddedExtractorDispatcherWithDeps(app, deps)
	if err == nil {
		t.Fatal("expected failure for malformed configured policy")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "/secret/policy.json") || strings.Contains(errStr, "private-key") {
		t.Fatalf("expected redacted error, got %q", errStr)
	}
}

func TestConfigureEmbeddedExtractorMalformedConfiguredAuthFailsRedacted(t *testing.T) {
	app := NewApp(Options{})
	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.dataRoot = func() (string, error) { return t.TempDir(), nil }
	deps.loadAuthRuntimeBundle = func() (*extractor.PrivateAuthRuntimeBundle, error) {
		return nil, errors.New("sensitive database connection string: postgres://user:password@internal")
	}

	err := configureEmbeddedExtractorDispatcherWithDeps(app, deps)
	if err == nil {
		t.Fatal("expected failure for malformed configured auth")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "postgres") || strings.Contains(errStr, "password") {
		t.Fatalf("expected redacted error, got %q", errStr)
	}
}

func TestConfigureEmbeddedExtractorRequiredMissingOrRejectedPackFails(t *testing.T) {
	app := NewApp(Options{})
	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.dataRoot = func() (string, error) { return t.TempDir(), nil }
	deps.hasEmbeddedReleasePacks = func() bool { return false }
	deps.embeddedReleaseRequired = func() bool { return true }
	deps.acceptedEmbeddedPacks = func(config extractor.EmbeddedReleaseDispatcherConfig) ([]extractor.EmbeddedPack, error) {
		return extractor.AcceptedEmbeddedPacks([]extractor.EmbeddedPack{}, nil, config)
	}

	err := configureEmbeddedExtractorDispatcherWithDeps(app, deps)
	if err == nil {
		t.Fatal("expected failure when required release packs are missing")
	}
}

func TestConfigureEmbeddedExtractorOptionalRejectedPacksSkipped(t *testing.T) {
	app := NewApp(Options{})
	dataRoot := t.TempDir()

	validPackDir := filepath.Join(t.TempDir(), "valid-pack")
	lockOut := filepath.Join(validPackDir, packbuilder.HostCallFixturePackID+".lock.json")
	writeRes, err := packbuilder.WriteHostCallFixture(validPackDir, lockOut)
	if err != nil {
		t.Fatalf("WriteHostCallFixture: %v", err)
	}

	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.hasEmbeddedReleasePacks = func() bool { return true }
	deps.embeddedReleaseRequired = func() bool { return false }
	deps.dataRoot = func() (string, error) { return dataRoot, nil }
	deps.acceptedEmbeddedPacks = func(config extractor.EmbeddedReleaseDispatcherConfig) ([]extractor.EmbeddedPack, error) {
		return []extractor.EmbeddedPack{
			{
				ManifestJSON: writeRes.Assets.ManifestJSON,
				Payload:      writeRes.Assets.Payload,
				Signature:    writeRes.Assets.Signature,
				AssetSHA256:  writeRes.Assets.AssetSHA256,
			},
		}, nil
	}
	deps.embeddedReleaseTrustedPublicKeys = func() []ed25519.PublicKey {
		return []ed25519.PublicKey{writeRes.Assets.PublicKey}
	}

	if err := configureEmbeddedExtractorDispatcherWithDeps(app, deps); err != nil {
		t.Fatalf("configure error: %v", err)
	}

	if app.extractorAdapterForTest() == nil {
		t.Fatal("expected accepted pack to be active in adapter")
	}
}

func TestConfigureEmbeddedExtractorIndividualUnavailableUserRowDoesNotFailStartup(t *testing.T) {
	dataRoot := t.TempDir()
	cleanLocator := filepath.Join(dataRoot, "missing-dir")

	sourcesJSON := fmt.Sprintf(`{
		"schema_version": 1,
		"sources": [
			{
				"source_id": "0123456789abcdef0123456789abcdef",
				"kind": "local_directory",
				"locator": %q,
				"pack_id": "xpk.test.missing",
				"pack_version": "1.0.0",
				"signer_fingerprint": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				"cache_generation": "fedcba9876543210fedcba9876543210"
			}
		]
	}`, cleanLocator)
	if err := os.WriteFile(filepath.Join(dataRoot, "sources.json"), []byte(sourcesJSON), 0o644); err != nil {
		t.Fatalf("write sources.json: %v", err)
	}

	app := NewApp(Options{})
	deps := defaultEmbeddedExtractorConfigDeps(app)
	deps.hasEmbeddedReleasePacks = func() bool { return false }
	deps.embeddedReleaseRequired = func() bool { return false }
	deps.dataRoot = func() (string, error) { return dataRoot, nil }

	if err := configureEmbeddedExtractorDispatcherWithDeps(app, deps); err != nil {
		t.Fatalf("startup should not fail with damaged user cache: %v", err)
	}

	state := app.GetExtractorState()
	if !state.Available {
		t.Fatal("expected available: true")
	}
	if len(state.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(state.Sources))
	}
	if state.Sources[0].Status != ExtractorSourceStatusUnavailable {
		t.Fatalf("expected source status unavailable, got %s", state.Sources[0].Status)
	}
	if app.extractorAdapterForTest() != nil {
		t.Fatal("expected nil adapter when only unavailable sources exist")
	}
}
