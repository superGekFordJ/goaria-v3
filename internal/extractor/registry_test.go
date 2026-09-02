package extractor

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
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

func TestRegistryFindByURLDoesNotTreatAliasRefsAsDomains(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	pack := signedTestPack(t, privateKey, []byte("opaque payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["pack_version"] = "opaque-1"
		values["capabilities"] = []string{
			string(CapabilityParseWASM),
			string(CapabilityHTTPFetch),
			string(CapabilityAuthProfile),
		}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
	})
	registry, rejections := NewRegistry([]EmbeddedPack{pack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v, want none", rejections)
	}
	if packs := registry.Packs(); len(packs) != 1 {
		t.Fatalf("registry.Packs() = %d packs, want 1", len(packs))
	}

	for _, rawURL := range []string{
		"https://dpr-alpha001.invalid/path",
		"https://bpr-alpha001.invalid/path",
		"https://opaque.invalid/path",
	} {
		t.Run(rawURL, func(t *testing.T) {
			if matches := registry.FindByURL(rawURL); len(matches) != 0 {
				t.Fatalf("registry.FindByURL(%q) = %d matches, want 0", rawURL, len(matches))
			}
		})
	}
}

func TestRegistryFindByURLWithContextMatchesAliasIngressPolicy(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}
	registry := &Registry{packs: []VerifiedPack{pack}, hostPolicyResolver: resolver}

	if matches := registry.FindByURLWithContext(context.Background(), "https://share.alpha.test/item"); len(matches) != 1 {
		t.Fatalf("FindByURLWithContext() matches = %d, want 1", len(matches))
	}
	if matches := registry.FindByURLWithContext(context.Background(), "https://api.alpha.test/item"); len(matches) != 0 {
		t.Fatalf("FindByURLWithContext() broker-only matches = %d, want 0", len(matches))
	}
	if resolver.calls == 0 {
		t.Fatal("resolver was not called for alias manifest")
	}
}

func TestRegistryFindByURLWithContextAliasPolicyFailuresNoMatch(t *testing.T) {
	pack := syntheticAliasVerifiedPack()

	if matches := (&Registry{packs: []VerifiedPack{pack}}).FindByURLWithContext(context.Background(), "https://share.alpha.test/item"); len(matches) != 0 {
		t.Fatalf("FindByURLWithContext() without resolver = %d matches, want 0", len(matches))
	}
	policy := syntheticHostPolicy(pack.Identity)
	policy.PackIdentity.PublicKeySHA256 = hashString('9')
	registry := &Registry{packs: []VerifiedPack{pack}, hostPolicyResolver: &fakeHostPolicyResolver{policy: policy}}
	if matches := registry.FindByURLWithContext(context.Background(), "https://share.alpha.test/item"); len(matches) != 0 {
		t.Fatalf("FindByURLWithContext() mismatch = %d matches, want 0", len(matches))
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

func TestRegistryReturnsDefensiveCopiesForAliasRefs(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	pack := signedTestPack(t, privateKey, []byte("opaque payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["pack_version"] = "opaque-1"
		values["capabilities"] = []string{
			string(CapabilityParseWASM),
			string(CapabilityHTTPFetch),
			string(CapabilityAuthProfile),
		}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
	})
	registry, rejections := NewRegistry([]EmbeddedPack{pack}, policyWithKeys(publicKey))
	if len(rejections) != 0 {
		t.Fatalf("NewRegistry() rejections = %#v, want none", rejections)
	}

	packs := registry.Packs()
	if len(packs) != 1 {
		t.Fatalf("registry.Packs() = %d packs, want 1", len(packs))
	}
	packs[0].Manifest.DomainPolicyRefs[0] = "dpr-bravo001"
	packs[0].Manifest.BrokerPolicyRefs[0] = "bpr-bravo001"

	freshPacks := registry.Packs()
	if freshPacks[0].Manifest.DomainPolicyRefs[0] != "dpr-alpha001" {
		t.Fatalf("registry domain refs mutated to %#v", freshPacks[0].Manifest.DomainPolicyRefs)
	}
	if freshPacks[0].Manifest.BrokerPolicyRefs[0] != "bpr-alpha001" {
		t.Fatalf("registry broker refs mutated to %#v", freshPacks[0].Manifest.BrokerPolicyRefs)
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

func newTestVerifiedPackForRegistry(t *testing.T, packID, packVersion string, payload []byte, mutateManifest func(*Manifest)) VerifiedPack {
	t.Helper()
	payloadHash := sha256HexString(payload)
	manifest := Manifest{
		PackID:         packID,
		PackVersion:    packVersion,
		ABIVersion:     CurrentABIVersion,
		PayloadSHA256:  payloadHash,
		Capabilities:   []Capability{CapabilityParseWASM},
		Domains:        []DomainRule{{Host: "fixture.invalid"}},
		ResourceLimits: DefaultTrustPolicy().MaxResourceLimits,
	}
	if mutateManifest != nil {
		mutateManifest(&manifest)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestHash := sha256HexString(manifestJSON)
	return VerifiedPack{
		Manifest: manifest,
		Payload:  cloneBytes(payload),
		Identity: VerifiedPackIdentity{
			PackID:          manifest.PackID,
			PackVersion:     manifest.PackVersion,
			AssetSHA256:     sha256HexString([]byte("asset-zip")),
			ManifestSHA256:  manifestHash,
			PayloadSHA256:   payloadHash,
			SignatureSHA256: sha256HexString([]byte("sig")),
			PublicKeySHA256: sha256HexString([]byte("pubkey")),
		},
	}
}

func TestRegistryFromVerifiedPacks(t *testing.T) {
	t.Run("empty input returns empty registry", func(t *testing.T) {
		reg, err := NewRegistryFromVerifiedPacks(nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reg == nil {
			t.Fatal("expected non-nil registry")
		}
		if packs := reg.Packs(); len(packs) != 0 {
			t.Fatalf("expected 0 packs, got %d", len(packs))
		}
	})

	t.Run("preserves order of multiple packs", func(t *testing.T) {
		p1 := newTestVerifiedPackForRegistry(t, "xpk-order1", "1.0.0", []byte("payload 1"), nil)
		p2 := newTestVerifiedPackForRegistry(t, "xpk-order2", "1.0.0", []byte("payload 2"), nil)
		p3 := newTestVerifiedPackForRegistry(t, "xpk-order3", "1.0.0", []byte("payload 3"), nil)

		reg, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p1, p2, p3}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		packs := reg.Packs()
		if len(packs) != 3 {
			t.Fatalf("expected 3 packs, got %d", len(packs))
		}
		if packs[0].Identity.PackID != "xpk-order1" || packs[1].Identity.PackID != "xpk-order2" || packs[2].Identity.PackID != "xpk-order3" {
			t.Fatalf("order not preserved: %s, %s, %s", packs[0].Identity.PackID, packs[1].Identity.PackID, packs[2].Identity.PackID)
		}
	})

	t.Run("rejects duplicate pack IDs", func(t *testing.T) {
		p1 := newTestVerifiedPackForRegistry(t, "xpk-dup", "1.0.0", []byte("payload 1"), nil)
		p2 := newTestVerifiedPackForRegistry(t, "xpk-dup", "1.0.1", []byte("payload 2"), nil)

		_, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p1, p2}, nil)
		if err == nil {
			t.Fatal("expected duplicate pack id error, got nil")
		}
	})

	t.Run("rejects manifest identity mismatch", func(t *testing.T) {
		p := newTestVerifiedPackForRegistry(t, "xpk-mismatch", "1.0.0", []byte("payload"), nil)
		p.Identity.PackID = "xpk-other"
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err == nil {
			t.Fatal("expected pack id mismatch error")
		}

		p = newTestVerifiedPackForRegistry(t, "xpk-mismatch", "1.0.0", []byte("payload"), nil)
		p.Identity.PackVersion = "2.0.0"
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err == nil {
			t.Fatal("expected pack version mismatch error")
		}

		p = newTestVerifiedPackForRegistry(t, "xpk-mismatch", "1.0.0", []byte("payload"), nil)
		p.Identity.PayloadSHA256 = sha256HexString([]byte("other"))
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err == nil {
			t.Fatal("expected payload sha256 mismatch error")
		}
	})

	t.Run("rejects payload bytes mismatch", func(t *testing.T) {
		p := newTestVerifiedPackForRegistry(t, "xpk-tampered", "1.0.0", []byte("original payload"), nil)
		p.Payload = []byte("tampered payload")
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err == nil {
			t.Fatal("expected actual payload mismatch error")
		}
	})

	t.Run("rejects malformed identity hashes", func(t *testing.T) {
		p := newTestVerifiedPackForRegistry(t, "xpk-badhash", "1.0.0", []byte("payload"), nil)
		p.Identity.ManifestSHA256 = strings.ToUpper(p.Identity.ManifestSHA256)
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err == nil {
			t.Fatal("expected error on uppercase manifest sha256")
		}

		p = newTestVerifiedPackForRegistry(t, "xpk-badhash", "1.0.0", []byte("payload"), nil)
		p.Identity.SignatureSHA256 = "invalid-len"
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err == nil {
			t.Fatal("expected error on invalid signature sha256 length")
		}

		p = newTestVerifiedPackForRegistry(t, "xpk-badhash", "1.0.0", []byte("payload"), nil)
		p.Identity.PublicKeySHA256 = "short"
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err == nil {
			t.Fatal("expected error on invalid public key sha256")
		}
	})

	t.Run("allows empty AssetSHA256 but rejects invalid non-empty", func(t *testing.T) {
		p := newTestVerifiedPackForRegistry(t, "xpk-emptyasset", "1.0.0", []byte("payload"), nil)
		p.Identity.AssetSHA256 = ""
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err != nil {
			t.Fatalf("expected empty asset sha256 allowed, got: %v", err)
		}

		p.Identity.AssetSHA256 = "not-a-valid-sha256"
		if _, err := NewRegistryFromVerifiedPacks([]VerifiedPack{p}, nil); err == nil {
			t.Fatal("expected invalid asset sha256 error")
		}
	})

	t.Run("defensive copy protects registry from external mutation", func(t *testing.T) {
		payload := []byte("defensive payload")
		p := newTestVerifiedPackForRegistry(t, "xpk-defensive", "1.0.0", payload, nil)
		inputSlice := []VerifiedPack{p}

		reg, err := NewRegistryFromVerifiedPacks(inputSlice, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Mutate input
		inputSlice[0].Payload[0] = 'X'
		inputSlice[0].Manifest.Domains[0].Host = "tampered.invalid"

		packs := reg.Packs()
		if string(packs[0].Payload) != "defensive payload" {
			t.Fatalf("registry payload mutated via input: %q", packs[0].Payload)
		}
		if packs[0].Manifest.Domains[0].Host != "fixture.invalid" {
			t.Fatalf("registry domain mutated via input: %q", packs[0].Manifest.Domains[0].Host)
		}

		// Mutate output
		packs[0].Payload[0] = 'Y'
		packs[0].Manifest.Domains[0].Host = "mutated.invalid"

		packsAgain := reg.Packs()
		if string(packsAgain[0].Payload) != "defensive payload" {
			t.Fatalf("registry payload mutated via output: %q", packsAgain[0].Payload)
		}
		if packsAgain[0].Manifest.Domains[0].Host != "fixture.invalid" {
			t.Fatalf("registry domain mutated via output: %q", packsAgain[0].Manifest.Domains[0].Host)
		}
	})
}
