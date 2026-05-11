package extractor

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyEmbeddedPackAcceptsValidFixture(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	payload := []byte("fixture wasm bytes")
	manifestJSON := mustManifestJSON(t, payload, nil)
	signature := ed25519.Sign(privateKey, manifestJSON)
	assetSHA := strings.Repeat("a", 64)
	policy := policyWithKeys(publicKey)

	verified, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    signature,
		AssetSHA256:  assetSHA,
	}, policy)
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	if verified.Manifest.PackID != "fixturepack" {
		t.Fatalf("verified Manifest.PackID = %q, want fixturepack", verified.Manifest.PackID)
	}
	if string(verified.Payload) != string(payload) {
		t.Fatalf("verified payload = %q, want %q", verified.Payload, payload)
	}
	if verified.Identity.PackID != verified.Manifest.PackID || verified.Identity.PackVersion != verified.Manifest.PackVersion {
		t.Fatalf("verified identity pack fields = %#v, manifest = %#v", verified.Identity, verified.Manifest)
	}
	if verified.Identity.AssetSHA256 != assetSHA {
		t.Fatalf("verified asset sha = %q, want %q", verified.Identity.AssetSHA256, assetSHA)
	}
	if verified.Identity.ManifestSHA256 != sha256Hex(manifestJSON) || verified.Identity.PayloadSHA256 != sha256Hex(payload) || verified.Identity.SignatureSHA256 != sha256Hex(signature) || verified.Identity.PublicKeySHA256 != sha256Hex(publicKey) {
		t.Fatalf("verified identity hashes = %#v", verified.Identity)
	}
}

func TestVerifyEmbeddedPackAcceptsSignedAliasManifestAndDefensiveRefs(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(11)
	payload := []byte("alias fixture wasm bytes")
	manifestJSON := mustAliasManifestJSON(t, payload, nil)
	signature := ed25519.Sign(privateKey, manifestJSON)
	verified, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    signature,
		AssetSHA256:  strings.Repeat("b", 64),
	}, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	if verified.Manifest.PackID != "xpk-alpha001" || verified.Identity.PackID != "xpk-alpha001" {
		t.Fatalf("verified alias identity = %#v manifest=%#v", verified.Identity, verified.Manifest)
	}
	if len(verified.Manifest.DomainPolicyRefs) != 1 || verified.Manifest.DomainPolicyRefs[0] != "dpr-alpha001" {
		t.Fatalf("verified domain refs = %#v", verified.Manifest.DomainPolicyRefs)
	}

	cloned := cloneVerifiedPack(verified)
	cloned.Manifest.DomainPolicyRefs[0] = "dpr-mutated"
	cloned.Manifest.BrokerPolicyRefs[0] = "bpr-mutated"
	cloned.Payload[0] = 'X'
	if verified.Manifest.DomainPolicyRefs[0] != "dpr-alpha001" || verified.Manifest.BrokerPolicyRefs[0] != "bpr-alpha001" || string(verified.Payload) != string(payload) {
		t.Fatalf("verified alias pack was mutated through clone: %#v", verified.Manifest)
	}
}

func TestVerifyEmbeddedPackRejectsInvalidAssetSHA(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(12)
	payload := []byte("fixture wasm bytes")
	manifestJSON := mustManifestJSON(t, payload, nil)

	if _, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
		AssetSHA256:  strings.ToUpper(strings.Repeat("a", 64)),
	}, policyWithKeys(publicKey)); err == nil {
		t.Fatal("VerifyEmbeddedPack() error = nil, want invalid asset hash error")
	}
}

func TestVerifyEmbeddedPackRejectsUnsignedPack(t *testing.T) {
	publicKey, _ := deterministicKeyPair(1)
	payload := []byte("fixture wasm bytes")
	manifestJSON := mustManifestJSON(t, payload, nil)

	if _, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
	}, policyWithKeys(publicKey)); err == nil {
		t.Fatal("VerifyEmbeddedPack() error = nil, want error")
	}
}

func TestVerifyEmbeddedPackRejectsTamperedPayload(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	payload := []byte("fixture wasm bytes")
	manifestJSON := mustManifestJSON(t, payload, nil)
	signature := ed25519.Sign(privateKey, manifestJSON)

	if _, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      []byte("modified fixture wasm bytes"),
		Signature:    signature,
	}, policyWithKeys(publicKey)); err == nil {
		t.Fatal("VerifyEmbeddedPack() error = nil, want error")
	}
}

func TestVerifyEmbeddedPackRejectsTamperedManifest(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	payload := []byte("fixture wasm bytes")
	manifestJSON := mustManifestJSON(t, payload, nil)
	signature := ed25519.Sign(privateKey, manifestJSON)
	tamperedManifest := append([]byte(nil), manifestJSON...)
	tamperedManifest[len(tamperedManifest)-2] ^= 1

	if _, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: tamperedManifest,
		Payload:      payload,
		Signature:    signature,
	}, policyWithKeys(publicKey)); err == nil {
		t.Fatal("VerifyEmbeddedPack() error = nil, want error")
	}
}

func TestVerifyEmbeddedPackRejectsTamperedSignedAliasRefs(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(13)
	payload := []byte("alias fixture wasm bytes")
	manifestJSON := mustAliasManifestJSON(t, payload, nil)
	signature := ed25519.Sign(privateKey, manifestJSON)
	tamperedManifest := mustAliasManifestJSON(t, payload, func(values map[string]any) {
		values["domain_policy_refs"] = []string{"dpr-alpha002"}
	})

	if _, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: tamperedManifest,
		Payload:      payload,
		Signature:    signature,
	}, policyWithKeys(publicKey)); err == nil {
		t.Fatal("VerifyEmbeddedPack() error = nil, want alias ref tamper failure")
	}
}

func TestVerifyEmbeddedPackRejectsWrongKey(t *testing.T) {
	_, privateKey := deterministicKeyPair(1)
	wrongPublicKey, _ := deterministicKeyPair(2)
	payload := []byte("fixture wasm bytes")
	manifestJSON := mustManifestJSON(t, payload, nil)
	signature := ed25519.Sign(privateKey, manifestJSON)

	if _, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    signature,
	}, policyWithKeys(wrongPublicKey)); err == nil {
		t.Fatal("VerifyEmbeddedPack() error = nil, want error")
	}
}

func TestVerifyEmbeddedPackRejectsInvalidJSONOrUnknownFields(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	payload := []byte("fixture wasm bytes")
	validManifestJSON := mustManifestJSON(t, payload, nil)

	tests := []struct {
		name         string
		manifestJSON []byte
	}{
		{
			name:         "malformed json",
			manifestJSON: []byte(`{"pack_id":"fixturepack"`),
		},
		{
			name: "unknown field",
			manifestJSON: mustManifestJSON(t, payload, func(values map[string]any) {
				values["unexpected"] = true
			}),
		},
		{
			name:         "trailing json",
			manifestJSON: append(append([]byte(nil), validManifestJSON...), []byte(` {}`)...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signature := ed25519.Sign(privateKey, tt.manifestJSON)

			if _, err := VerifyEmbeddedPack(EmbeddedPack{
				ManifestJSON: tt.manifestJSON,
				Payload:      payload,
				Signature:    signature,
			}, policyWithKeys(publicKey)); err == nil {
				t.Fatal("VerifyEmbeddedPack() error = nil, want error")
			}
		})
	}
}

func TestVerifyEmbeddedPackReturnsDefensivePayloadCopy(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(1)
	payload := []byte("fixture wasm bytes")
	manifestJSON := mustManifestJSON(t, payload, nil)
	verified, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
	}, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}

	payload[0] = 'X'
	if string(verified.Payload) != "fixture wasm bytes" {
		t.Fatalf("verified payload mutated to %q", verified.Payload)
	}
}

func TestVerifyEmbeddedPackAcceptsAnyTrustedKey(t *testing.T) {
	trustedPublicKey, privateKey := deterministicKeyPair(1)
	otherPublicKey, _ := deterministicKeyPair(2)
	payload := []byte("fixture wasm bytes")
	manifestJSON := mustManifestJSON(t, payload, nil)
	signature := ed25519.Sign(privateKey, manifestJSON)

	verified, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    signature,
	}, policyWithKeys(otherPublicKey, trustedPublicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	if verified.Manifest.PackID != "fixturepack" {
		t.Fatalf("verified Manifest.PackID = %q, want fixturepack", verified.Manifest.PackID)
	}
}

func deterministicKeyPair(seedByte byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = seedByte
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)

	return publicKey, privateKey
}

func policyWithKeys(keys ...ed25519.PublicKey) TrustPolicy {
	policy := DefaultTrustPolicy()
	policy.TrustedPublicKeys = keys

	return policy
}

func mustManifestJSON(t *testing.T, payload []byte, mutate func(map[string]any)) []byte {
	t.Helper()

	hash := sha256.Sum256(payload)
	values := map[string]any{
		"pack_id":      "fixturepack",
		"pack_version": "1.0.0",
		"abi_version":  CurrentABIVersion,
		"capabilities": []string{
			string(CapabilityParseWASM),
			string(CapabilityHTTPFetch),
		},
		"domains": []map[string]any{
			{
				"host":               "fixture.invalid",
				"include_subdomains": true,
			},
		},
		"resource_limits": map[string]any{
			"timeout_millis":     1_000,
			"max_memory_pages":   64,
			"max_host_calls":     16,
			"max_response_bytes": 1 << 20,
			"max_output_items":   100,
			"max_output_bytes":   1 << 16,
		},
		"payload_sha256": hex.EncodeToString(hash[:]),
	}
	if mutate != nil {
		mutate(values)
	}

	manifestJSON, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	return manifestJSON
}

func mustAliasManifestJSON(t *testing.T, payload []byte, mutate func(map[string]any)) []byte {
	t.Helper()

	return mustManifestJSON(t, payload, func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
		if mutate != nil {
			mutate(values)
		}
	})
}
