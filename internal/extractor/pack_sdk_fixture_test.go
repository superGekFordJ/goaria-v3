package extractor_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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
	registry, rejections := extractor.NewRegistry([]extractor.EmbeddedPack{{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}}, policyForFixture(assets.PublicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	if packs := registry.Packs(); len(packs) != 1 || packs[0].Manifest.PackID != pack.Manifest.PackID {
		t.Fatalf("registry packs = %#v", packs)
	}

	brokerTransport := &fixtureHTTPTransport{body: `{"ok":true,"item":"fixture-item"}`}
	runner := extractor.NewRunnerWithConfig(extractor.RunnerConfig{
		HTTPBroker: extractor.NewHTTPBroker(extractor.HTTPBrokerConfig{Transport: brokerTransport}),
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
	joinedMetadata := strings.ToLower(strings.Join(mapValues(item.Metadata), " "))
	for _, forbidden := range []string{"authorization", "cookie", "token", "secret", "raw"} {
		if strings.Contains(joinedMetadata, forbidden) {
			t.Fatalf("metadata leaked forbidden marker %q: %#v", forbidden, item.Metadata)
		}
	}
}

func TestHostCallFixtureDeniedPolicyDoesNotCallBroker(t *testing.T) {
	assets, err := packbuilder.BuildSignedHostCallFixture()
	if err != nil {
		t.Fatalf("BuildSignedHostCallFixture() error = %v", err)
	}
	manifest := assets.Manifest
	manifest.Capabilities = []extractor.Capability{extractor.CapabilityParseWASM}
	manifest.PayloadSHA256 = assets.PayloadSHA256
	manifestJSON := mustJSON(t, manifest)
	_, privateKey := packbuilder.DeterministicFixtureKeyPair()
	assets.ManifestJSON = manifestJSON
	assets.Signature = ed25519.Sign(privateKey, manifestJSON)

	registry, rejections := extractor.NewRegistry([]extractor.EmbeddedPack{{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}}, policyForFixture(assets.PublicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	transport := &fixtureHTTPTransport{body: `{"ok":true}`}
	dispatcher := extractor.NewAddTaskDispatcher(extractor.AddTaskDispatcherConfig{
		Registry: registry,
		Runner: extractor.NewRunnerWithConfig(extractor.RunnerConfig{
			HTTPBroker: extractor.NewHTTPBroker(extractor.HTTPBrokerConfig{Transport: transport}),
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

func TestHostCallFixtureDeniedDomainDoesNotCallBroker(t *testing.T) {
	assets, err := packbuilder.BuildSignedHostCallFixture()
	if err != nil {
		t.Fatalf("BuildSignedHostCallFixture() error = %v", err)
	}
	manifest := assets.Manifest
	manifest.Domains = []extractor.DomainRule{{Host: "example.invalid", IncludeSubdomains: true}}
	manifest.PayloadSHA256 = assets.PayloadSHA256
	manifestJSON := mustJSON(t, manifest)
	_, privateKey := packbuilder.DeterministicFixtureKeyPair()
	assets.ManifestJSON = manifestJSON
	assets.Signature = ed25519.Sign(privateKey, manifestJSON)

	registry, rejections := extractor.NewRegistry([]extractor.EmbeddedPack{{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}}, policyForFixture(assets.PublicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v", rejections)
	}
	transport := &fixtureHTTPTransport{err: errors.New("transport must not be called")}
	dispatcher := extractor.NewAddTaskDispatcher(extractor.AddTaskDispatcherConfig{
		Registry: registry,
		Runner: extractor.NewRunnerWithConfig(extractor.RunnerConfig{
			HTTPBroker: extractor.NewHTTPBroker(extractor.HTTPBrokerConfig{Transport: transport}),
		}),
	})

	resolution, err := dispatcher.Resolve(context.Background(), packbuilder.HostCallFixtureShareURL)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolution.Matched || len(resolution.Items) != 0 {
		t.Fatalf("resolution = %#v, want no-match direct fallback for denied registry domain", resolution)
	}
	if transport.Count() != 0 {
		t.Fatalf("fake broker transport calls = %d, want 0", transport.Count())
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

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	return raw
}
