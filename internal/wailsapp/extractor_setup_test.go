//go:build extractor

package wailsapp

import (
	"path/filepath"
	"strings"
	"testing"

	"goaria-v3/internal/extractor"
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
	if !hasPacks && policySource != extractor.RuntimeSourceStateEmbedded && authRuntimeSource != extractor.RuntimeSourceStateEmbedded {
		t.Skip("generated full-pack outputs are not present in source tree")
	}
	if !hasPacks {
		t.Fatal("generated source proof expected embedded packs to be present")
	}
	if extractor.EmbeddedReleasePackCount() == 0 {
		t.Fatal("embedded release pack count = 0, want generated source packs")
	}
	if policySource != extractor.RuntimeSourceStateEmbedded {
		t.Fatalf("policy runtime source = %q, want embedded", policySource)
	}
	if authRuntimeSource != extractor.RuntimeSourceStateEmbedded {
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
	if app.extractorAdapter == nil {
		t.Fatal("App extractor adapter = nil, want configured adapter")
	}
}
