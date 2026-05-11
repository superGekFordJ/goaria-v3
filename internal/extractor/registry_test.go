package extractor

import (
	"crypto/ed25519"
	"testing"
)

func TestRegistryNoPackFallback(t *testing.T) {
	registry, rejections := NewRegistry(nil, DefaultTrustPolicy())
	if registry == nil {
		t.Fatal("NewRegistry() registry = nil, want usable registry")
	}
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %d, want 0", len(rejections))
	}
	if packs := registry.Packs(); len(packs) != 0 {
		t.Fatalf("registry.Packs() = %d packs, want 0", len(packs))
	}
	if matches := registry.FindByURL("https://fixture.invalid/d/abc"); len(matches) != 0 {
		t.Fatalf("registry.FindByURL() = %d matches, want 0", len(matches))
	}
}

func TestRegistryLoadsOnlyVerifiedPacks(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	validPack := signedTestPack(t, privateKey, []byte("valid payload"), nil)
	invalidPack := signedTestPack(t, privateKey, []byte("invalid payload"), nil)
	invalidPack.Payload = []byte("tampered payload")

	registry, rejections := NewRegistry([]EmbeddedPack{validPack, invalidPack}, policyWithKeys(publicKey))
	if registry == nil {
		t.Fatal("NewRegistry() registry = nil, want usable registry")
	}
	if packs := registry.Packs(); len(packs) != 1 {
		t.Fatalf("registry.Packs() = %d packs, want 1", len(packs))
	}
	if len(rejections) != 1 {
		t.Fatalf("NewRegistry() rejections = %d, want 1", len(rejections))
	}
	if rejections[0].PackID == "" || rejections[0].Reason == "" {
		t.Fatalf("rejection should include best-effort PackID and Reason: %#v", rejections[0])
	}
}

func TestRegistryFindByURLUsesDomainAllowlist(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	pack := signedTestPack(t, privateKey, []byte("payload"), func(values map[string]any) {
		values["domains"] = []map[string]any{
			{
				"host":               "fixture.invalid",
				"include_subdomains": true,
			},
		}
	})
	registry, rejections := NewRegistry([]EmbeddedPack{pack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v, want none", rejections)
	}

	matchingURLs := []string{
		"https://fixture.invalid/d/abc",
		"https://api.fixture.invalid/path",
	}
	for _, rawURL := range matchingURLs {
		t.Run("match "+rawURL, func(t *testing.T) {
			if matches := registry.FindByURL(rawURL); len(matches) != 1 {
				t.Fatalf("registry.FindByURL(%q) = %d matches, want 1", rawURL, len(matches))
			}
		})
	}

	nonMatchingURLs := []string{
		"https://evilfixture.invalid/d/abc",
		"https://fixture.invalid.evil.test/",
		"://bad-url",
		"ftp://fixture.invalid/d/abc",
		"https:///missing-host",
		"https://user:fixture.invalid@evil.test/d/abc",
		"https://fixture.invalid./d/abc",
		"https://fixture.invalid:bad/d/abc",
	}
	for _, rawURL := range nonMatchingURLs {
		t.Run("no match "+rawURL, func(t *testing.T) {
			if matches := registry.FindByURL(rawURL); len(matches) != 0 {
				t.Fatalf("registry.FindByURL(%q) = %d matches, want 0", rawURL, len(matches))
			}
		})
	}
}

func TestRegistryExactDomainDoesNotMatchSubdomainWhenDisabled(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	pack := signedTestPack(t, privateKey, []byte("payload"), func(values map[string]any) {
		values["domains"] = []map[string]any{
			{"host": "fixture.invalid"},
		}
	})
	registry, rejections := NewRegistry([]EmbeddedPack{pack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v, want none", rejections)
	}

	if matches := registry.FindByURL("https://fixture.invalid/d/abc"); len(matches) != 1 {
		t.Fatalf("exact domain matches = %d, want 1", len(matches))
	}
	if matches := registry.FindByURL("https://api.fixture.invalid/d/abc"); len(matches) != 0 {
		t.Fatalf("subdomain matches = %d, want 0", len(matches))
	}
}

func TestRegistryLoadsAliasPacksButNoResolverYieldsNoMatch(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(21)
	pack := signedAliasTestPack(t, privateKey, []byte("alias payload"), nil)
	registry, rejections := NewRegistry([]EmbeddedPack{pack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v, want none", rejections)
	}
	if packs := registry.Packs(); len(packs) != 1 || !isAliasManifest(packs[0].Manifest) {
		t.Fatalf("registry packs = %#v, want one alias pack", packs)
	}
	if matches := registry.FindByURL("https://share.alpha.test/path"); len(matches) != 0 {
		t.Fatalf("registry.FindByURL() = %d matches, want no resolver no-match", len(matches))
	}
}

func TestRegistryFindByURLUsesResolverBackedAliasIngress(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(22)
	pack := signedAliasTestPack(t, privateKey, []byte("alias payload"), nil)
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	resolver := &fakeHostPolicyResolver{policy: validResolvedHostPolicy(verified.Identity, verified.Manifest)}
	registry, rejections := NewRegistryWithHostPolicyResolver([]EmbeddedPack{pack}, policyWithKeys(publicKey), resolver)
	if len(rejections) != 0 {
		t.Fatalf("NewRegistryWithHostPolicyResolver() rejections = %#v, want none", rejections)
	}

	matches := registry.FindByURL("https://share.alpha.test/path")
	if len(matches) != 1 {
		t.Fatalf("registry.FindByURL() = %d matches, want resolver-backed alias match", len(matches))
	}
	if matches[0].Identity != verified.Identity || matches[0].Manifest.DomainPolicyRefs[0] != "dpr-alpha001" {
		t.Fatalf("matched pack identity/refs = %#v %#v", matches[0].Identity, matches[0].Manifest.DomainPolicyRefs)
	}
	if noMatches := registry.FindByURL("https://outside.alpha.test/path"); len(noMatches) != 0 {
		t.Fatalf("registry.FindByURL(outside) = %d matches, want 0", len(noMatches))
	}

	matches[0].Manifest.DomainPolicyRefs[0] = "dpr-mutated"
	matches[0].Manifest.BrokerPolicyRefs[0] = "bpr-mutated"
	matches[0].Payload[0] = 'X'
	fresh := registry.FindByURL("https://share.alpha.test/path")
	if fresh[0].Manifest.DomainPolicyRefs[0] != "dpr-alpha001" || fresh[0].Manifest.BrokerPolicyRefs[0] != "bpr-alpha001" || string(fresh[0].Payload) != "alias payload" {
		t.Fatalf("registry alias match was not defensively copied: %#v", fresh[0])
	}
}

func TestRegistryAliasResolverMismatchYieldsNoMatch(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(23)
	pack := signedAliasTestPack(t, privateKey, []byte("alias payload"), nil)
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	mismatchedPolicy := validResolvedHostPolicy(verified.Identity, verified.Manifest)
	mismatchedPolicy.DomainPolicyRefs = []string{"dpr-alpha002"}
	registry, rejections := NewRegistryWithHostPolicyResolver([]EmbeddedPack{pack}, policyWithKeys(publicKey), &fakeHostPolicyResolver{policy: mismatchedPolicy})
	if len(rejections) != 0 {
		t.Fatalf("NewRegistryWithHostPolicyResolver() rejections = %#v, want none", rejections)
	}

	if matches := registry.FindByURL("https://share.alpha.test/path"); len(matches) != 0 {
		t.Fatalf("registry.FindByURL() = %d matches, want resolver mismatch no-match", len(matches))
	}
}

func TestRegistryReturnsDefensiveCopies(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	pack := signedTestPack(t, privateKey, []byte("payload"), nil)
	registry, rejections := NewRegistry([]EmbeddedPack{pack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v, want none", rejections)
	}

	packs := registry.Packs()
	if len(packs) != 1 {
		t.Fatalf("registry.Packs() = %d packs, want 1", len(packs))
	}
	packs[0].Payload[0] = 'X'
	packs[0].Manifest.Capabilities[0] = Capability("cap.changed")
	packs[0].Manifest.Domains[0].Host = "changed.example"

	freshPacks := registry.Packs()
	if string(freshPacks[0].Payload) != "payload" {
		t.Fatalf("registry payload mutated to %q", freshPacks[0].Payload)
	}
	if freshPacks[0].Manifest.Capabilities[0] != CapabilityParseWASM {
		t.Fatalf("registry capabilities mutated to %#v", freshPacks[0].Manifest.Capabilities)
	}
	if freshPacks[0].Manifest.Domains[0].Host != "fixture.invalid" {
		t.Fatalf("registry domains mutated to %#v", freshPacks[0].Manifest.Domains)
	}

	matches := registry.FindByURL("https://fixture.invalid/d/abc")
	if len(matches) != 1 {
		t.Fatalf("registry.FindByURL() = %d matches, want 1", len(matches))
	}
	matches[0].Payload[0] = 'Y'
	matches[0].Manifest.Domains[0].Host = "mutated.example"

	freshMatches := registry.FindByURL("https://fixture.invalid/d/abc")
	if string(freshMatches[0].Payload) != "payload" {
		t.Fatalf("registry match payload mutated to %q", freshMatches[0].Payload)
	}
	if freshMatches[0].Manifest.Domains[0].Host != "fixture.invalid" {
		t.Fatalf("registry match domains mutated to %#v", freshMatches[0].Manifest.Domains)
	}
}

func signedTestPack(t *testing.T, privateKey ed25519.PrivateKey, payload []byte, mutate func(map[string]any)) EmbeddedPack {
	t.Helper()

	manifestJSON := mustManifestJSON(t, payload, mutate)

	return EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
	}
}

func signedAliasTestPack(t *testing.T, privateKey ed25519.PrivateKey, payload []byte, mutate func(map[string]any)) EmbeddedPack {
	t.Helper()

	manifestJSON := mustAliasManifestJSON(t, payload, mutate)

	return EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
	}
}
