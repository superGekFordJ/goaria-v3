package extractor_test

import (
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

func TestHostCallFixturePackVerifiesAndRunsThroughDispatcher(t *testing.T) {
	assets, pack := verifiedHostCallFixturePack(t)
	resolver := fixtureHostPolicyResolver(pack)
	registry, rejections := extractor.NewRegistryWithHostPolicyResolver([]extractor.EmbeddedPack{{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}}, policyForFixture(assets.PublicKey), resolver)
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	if packs := registry.Packs(); len(packs) != 1 || packs[0].Manifest.PackID != pack.Manifest.PackID {
		t.Fatalf("registry packs = %#v", packs)
	}

	brokerTransport := &fixtureHTTPTransport{body: `{"ok":true,"item":"fixture-item"}`}
	runner := extractor.NewRunnerWithConfig(extractor.RunnerConfig{
		HTTPBroker:         extractor.NewHTTPBroker(extractor.HTTPBrokerConfig{Transport: brokerTransport, HostPolicyResolver: resolver}),
		HostPolicyResolver: resolver,
	})
	dispatcher := extractor.NewAddTaskDispatcher(extractor.AddTaskDispatcherConfig{
		Registry: registry,
		Runner:   runner,
	})

	resolution, err := dispatcher.Resolve(context.Background(), packbuilder.HostCallFixtureShareURL)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !resolution.Matched || resolution.PackID != packbuilder.HostCallFixturePackID {
		t.Fatalf("resolution = %#v, want matched hostcall fixture", resolution)
	}
	if brokerTransport.Count() != 1 {
		t.Fatalf("fake broker transport calls = %d, want 1", brokerTransport.Count())
	}
	if got := brokerTransport.LastURL(); got != packbuilder.HostCallFixtureAPIURL {
		t.Fatalf("fake broker URL = %q, want %q", got, packbuilder.HostCallFixtureAPIURL)
	}
	if len(resolution.Items) != 1 {
		t.Fatalf("resolution items = %d, want 1", len(resolution.Items))
	}
	item := resolution.Items[0]
	if item.URL != packbuilder.HostCallFixtureItemURL || item.Filename != packbuilder.HostCallFixtureFilename {
		t.Fatalf("item = %#v", item)
	}
	if item.AuthProfileRef != "" || item.HeaderProfileRef != "" {
		t.Fatalf("fixture item returned auth/header refs unexpectedly: %#v", item)
	}
	if item.MimeType != "application/octet-stream" {
		t.Fatalf("item mime = %q, want application/octet-stream", item.MimeType)
	}
	joinedMetadata := strings.ToLower(strings.Join(mapValues(item.Metadata), " "))
	for _, forbidden := range []string{"authorization", "cookie", "token", "secret", "raw"} {
		if strings.Contains(joinedMetadata, forbidden) {
			t.Fatalf("metadata leaked forbidden marker %q: %#v", forbidden, item.Metadata)
		}
	}
}

func TestHostCallFixtureDeniedPolicyDoesNotCallBroker(t *testing.T) {
	assets, pack := verifiedHostCallFixturePack(t)
	registry, rejections := extractor.NewRegistryWithHostPolicyResolver([]extractor.EmbeddedPack{{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}}, policyForFixture(assets.PublicKey), fixtureHostPolicyResolver(pack))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	transport := &fixtureHTTPTransport{body: `{"ok":true}`}
	dispatcher := extractor.NewAddTaskDispatcher(extractor.AddTaskDispatcherConfig{
		Registry: registry,
		Runner: extractor.NewRunnerWithConfig(extractor.RunnerConfig{
			HTTPBroker:         extractor.NewHTTPBroker(extractor.HTTPBrokerConfig{Transport: transport}),
			HostPolicyResolver: fixtureHostPolicyResolver(pack),
		}),
	})

	resolution, err := dispatcher.Resolve(context.Background(), packbuilder.HostCallFixtureShareURL)
	if err != nil {
		t.Fatalf("Resolve() error = %v, want guest fallback output with denied host response", err)
	}
	if !resolution.Matched {
		t.Fatalf("resolution.Matched = false, want fixture match despite denied host call")
	}
	if transport.Count() != 0 {
		t.Fatalf("fake broker transport calls = %d, want 0", transport.Count())
	}
	if len(resolution.Items) != 1 || resolution.Items[0].URL != packbuilder.HostCallFixtureItemURL {
		t.Fatalf("resolution items = %#v", resolution.Items)
	}
}

func TestHostCallFixtureSupplyChainLockVerifies(t *testing.T) {
	outDir := t.TempDir()
	lockPath := filepath.Join(outDir, "hostcall_fixture.lock.json")
	result, err := packbuilder.WriteHostCallFixture(outDir, lockPath)
	if err != nil {
		t.Fatalf("WriteHostCallFixture() error = %v", err)
	}
	if result.Lock.Packs[0].AssetPath != packbuilder.HostCallFixtureAssetName {
		t.Fatalf("asset_path = %q", result.Lock.Packs[0].AssetPath)
	}
	if result.Lock.Packs[0].AssetSHA256 != result.Assets.AssetSHA256 {
		t.Fatalf("lock asset hash = %s, want %s", result.Lock.Packs[0].AssetSHA256, result.Assets.AssetSHA256)
	}
	verified, err := extractor.VerifyEmbeddedPack(extractor.EmbeddedPack{
		ManifestJSON: result.Assets.ManifestJSON,
		Payload:      result.Assets.Payload,
		Signature:    result.Assets.Signature,
	}, policyForFixture(result.Assets.PublicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	if verified.Manifest.PackID != packbuilder.HostCallFixturePackID {
		t.Fatalf("verified PackID = %q", verified.Manifest.PackID)
	}
}

func verifiedHostCallFixturePack(t *testing.T) (packbuilder.SignedPackAssets, extractor.VerifiedPack) {
	t.Helper()
	assets, err := packbuilder.BuildSignedHostCallFixture()
	if err != nil {
		t.Fatalf("BuildSignedHostCallFixture() error = %v", err)
	}
	pack, err := extractor.VerifyEmbeddedPack(extractor.EmbeddedPack{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}, policyForFixture(assets.PublicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}

	return assets, pack
}

func fixtureHostPolicyResolver(pack extractor.VerifiedPack) extractor.HostPolicyResolver {
	return fixturePolicyResolver{policy: extractor.ResolvedHostPolicy{
		PolicyID:            "hpr-fixture001",
		PolicyVersion:       "fixture-1",
		PolicySHA256:        strings.Repeat("a", 64),
		PackIdentity:        pack.Identity,
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
			Methods:          []string{http.MethodGet},
			TimeoutMillis:    1000,
			MaxResponseBytes: 4096,
		}},
	}}
}

type fixturePolicyResolver struct {
	policy extractor.ResolvedHostPolicy
}

func (r fixturePolicyResolver) ResolveHostPolicy(context.Context, extractor.HostPolicyRequest) (extractor.ResolvedHostPolicy, error) {
	return r.policy, nil
}

func policyForFixture(publicKey ed25519.PublicKey) extractor.TrustPolicy {
	policy := extractor.DefaultTrustPolicy()
	policy.TrustedPublicKeys = []ed25519.PublicKey{publicKey}

	return policy
}

type fixtureHTTPTransport struct {
	mu    sync.Mutex
	body  string
	err   error
	calls int
	last  string
}

func (t *fixtureHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.calls++
	t.last = req.URL.String()
	t.mu.Unlock()
	if t.err != nil {
		return nil, t.err
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": {"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(t.body)),
	}, nil
}

func (t *fixtureHTTPTransport) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.calls
}

func (t *fixtureHTTPTransport) LastURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.last
}

func mapValues(input map[string]string) []string {
	values := make([]string, 0, len(input))
	for key, value := range input {
		values = append(values, key, value)
	}

	return values
}
