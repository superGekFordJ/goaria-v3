package extractor

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPrivatePolicyBundleLoadsAndResolvesSyntheticPolicy(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	raw, privateSHA := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)

	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: privateSHA})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}
	resolved, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: pack.Identity, Manifest: pack.Manifest})
	if err != nil {
		t.Fatalf("ResolveHostPolicy() error = %v", err)
	}
	if resolved.PackIdentity != pack.Identity {
		t.Fatalf("resolved identity = %#v, want %#v", resolved.PackIdentity, pack.Identity)
	}
	if resolved.PolicyID != "hpb-alpha001" || resolved.PolicyVersion != "opaque-1" || resolved.PolicySHA256 != privateSHA {
		t.Fatalf("resolved policy identity = %#v", resolved)
	}
	if !policyIngressMatchesHost(resolved, "share.alpha.test") {
		t.Fatalf("resolved policy does not match synthetic ingress")
	}
}

func TestPrivatePolicyBundleResolverReturnsDefensiveCopies(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	raw, _ := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)
	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}

	resolved, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: pack.Identity, Manifest: pack.Manifest})
	if err != nil {
		t.Fatalf("ResolveHostPolicy() error = %v", err)
	}
	resolved.DomainPolicyRefs[0] = "dpr-mutated"
	resolved.IngressDomains[0].Host = "mutated.alpha.test"
	resolved.OutputDomains[0].PathPrefixes[0] = "/mutated/"
	resolved.AuthProfiles[0].Domains[0].Host = "mutated.alpha.test"
	resolved.Endpoints[0].Methods[0] = "POST"

	fresh, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: pack.Identity, Manifest: pack.Manifest})
	if err != nil {
		t.Fatalf("ResolveHostPolicy() second error = %v", err)
	}
	if fresh.DomainPolicyRefs[0] != "dpr-alpha001" || fresh.IngressDomains[0].Host != "share.alpha.test" || fresh.OutputDomains[0].PathPrefixes[0] != "/downloads/" || fresh.AuthProfiles[0].Domains[0].Host != "api.alpha.test" || fresh.Endpoints[0].Methods[0] != "GET" {
		t.Fatalf("resolved policy was not defensive-copied: %#v", fresh)
	}
}

func TestPrivatePolicyBundleResolutionFailsClosedForMismatches(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	raw, _ := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)
	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}

	missingIdentity := pack.Identity
	missingIdentity.AssetSHA256 = hashString('9')
	if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: missingIdentity, Manifest: pack.Manifest}); err == nil {
		t.Fatal("ResolveHostPolicy() missing identity error = nil")
	}

	manifestRefMismatch := pack.Manifest
	manifestRefMismatch.DomainPolicyRefs = []string{"dpr-bravo001"}
	if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: pack.Identity, Manifest: manifestRefMismatch}); err == nil {
		t.Fatal("ResolveHostPolicy() manifest ref mismatch error = nil")
	}

	manifestCapabilityMismatch := pack.Manifest
	manifestCapabilityMismatch.Capabilities = []Capability{CapabilityParseWASM, CapabilityHTTPFetch}
	if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: pack.Identity, Manifest: manifestCapabilityMismatch}); err == nil {
		t.Fatal("ResolveHostPolicy() capability mismatch error = nil")
	}

	legacyManifest := validTestManifest()
	if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: pack.Identity, Manifest: legacyManifest}); err == nil {
		t.Fatal("ResolveHostPolicy() legacy manifest error = nil")
	}
}

func TestPrivatePolicyBundleResolutionBindsEveryIdentityHashField(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	raw, _ := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)
	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*VerifiedPackIdentity)
	}{
		{name: "asset_sha256", mutate: func(identity *VerifiedPackIdentity) { identity.AssetSHA256 = hashString('1') }},
		{name: "manifest_sha256", mutate: func(identity *VerifiedPackIdentity) { identity.ManifestSHA256 = hashString('2') }},
		{name: "payload_sha256", mutate: func(identity *VerifiedPackIdentity) { identity.PayloadSHA256 = hashString('3') }},
		{name: "signature_sha256", mutate: func(identity *VerifiedPackIdentity) { identity.SignatureSHA256 = hashString('4') }},
		{name: "public_key_sha256", mutate: func(identity *VerifiedPackIdentity) { identity.PublicKeySHA256 = hashString('5') }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := pack.Identity
			tt.mutate(&identity)
			if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: identity, Manifest: pack.Manifest}); err == nil {
				t.Fatal("ResolveHostPolicy() error = nil, want exact identity hash binding denial")
			}
		})
	}
}

func TestPrivatePolicyBundleEndpointResourceCapsValidateAtLoadAndLookup(t *testing.T) {
	pack := syntheticAliasVerifiedPack()

	loadTooLarge := mustMutatedPrivatePolicyBundleJSON(t, pack.Identity, func(envelope map[string]any) {
		entry := mustPrivatePolicyBundlePackMap(pack.Identity)
		entry["endpoints"].([]map[string]any)[0]["max_response_bytes"] = DefaultTrustPolicy().MaxResourceLimits.MaxResponseBytes + 1
		envelope["policy"] = map[string]any{"packs": []any{entry}}
	})
	if _, err := NewPrivatePolicyBundleResolver(loadTooLarge, PrivatePolicyBundleLoadOptions{}); err == nil {
		t.Fatal("NewPrivatePolicyBundleResolver() error = nil, want host max resource cap denial")
	}

	raw, _ := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)
	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}
	manifest := pack.Manifest
	manifest.ResourceLimits.MaxResponseBytes = 128
	if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: pack.Identity, Manifest: manifest}); err == nil {
		t.Fatal("ResolveHostPolicy() error = nil, want signed manifest resource cap denial")
	}
}

func TestPrivatePolicyBundleRejectsMalformedEnvelope(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	validRaw, validSHA := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)

	tests := []struct {
		name    string
		raw     []byte
		opts    PrivatePolicyBundleLoadOptions
		mutate  func(map[string]any)
		wantErr string
	}{
		{name: "malformed json", raw: []byte(`{`), wantErr: "private host policy bundle is invalid"},
		{name: "unknown envelope field", mutate: func(envelope map[string]any) { envelope["extra"] = true }},
		{name: "trailing json", raw: append(append([]byte(nil), validRaw...), []byte(` {}`)...)},
		{name: "unsupported schema", mutate: func(envelope map[string]any) { envelope["schema_version"] = 2 }},
		{name: "malformed bundle id", mutate: func(envelope map[string]any) { envelope["bundle_id"] = "HPB_ALPHA001" }},
		{name: "malformed bundle version", mutate: func(envelope map[string]any) { envelope["bundle_version"] = "opaque 1" }},
		{name: "malformed private hash", mutate: func(envelope map[string]any) { envelope["policy_private_sha256"] = strings.ToUpper(validSHA) }},
		{name: "wrong computed hash", mutate: func(envelope map[string]any) { envelope["policy_private_sha256"] = hashString('9') }},
		{name: "expected private hash mismatch", opts: PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: hashString('8')}},
		{name: "malformed public fingerprint", mutate: func(envelope map[string]any) { envelope["policy_public_fingerprint"] = "abc" }},
		{name: "expected public fingerprint mismatch", opts: PrivatePolicyBundleLoadOptions{ExpectedPolicyPublicFingerprint: hashString('7')}},
		{name: "empty packs", mutate: func(envelope map[string]any) { envelope["policy"] = map[string]any{"packs": []any{}} }},
		{name: "unknown policy field", mutate: func(envelope map[string]any) {
			envelope["policy"] = map[string]any{"packs": []any{mustPrivatePolicyBundlePackMap(pack.Identity)}, "extra": true}
		}},
		{name: "unknown pack field", mutate: func(envelope map[string]any) {
			entry := mustPrivatePolicyBundlePackMap(pack.Identity)
			entry["extra"] = true
			envelope["policy"] = map[string]any{"packs": []any{entry}}
		}},
		{name: "duplicate identities", mutate: func(envelope map[string]any) {
			entry := mustPrivatePolicyBundlePackMap(pack.Identity)
			envelope["policy"] = map[string]any{"packs": []any{entry, entry}}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.raw
			if raw == nil {
				raw = mustMutatedPrivatePolicyBundleJSON(t, pack.Identity, tt.mutate)
			}
			_, err := NewPrivatePolicyBundleResolver(raw, tt.opts)
			if err == nil {
				t.Fatal("NewPrivatePolicyBundleResolver() error = nil")
			}
			if !strings.Contains(err.Error(), "private host policy bundle is invalid") {
				t.Fatalf("error = %q, want generic bundle error", err.Error())
			}
		})
	}
}

func TestPrivatePolicyBundleRejectsInvalidPackEntries(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing asset hash", mutate: func(entry map[string]any) { entry["verified_pack_identity"].(map[string]any)["asset_sha256"] = "" }},
		{name: "public key hash mismatch shape", mutate: func(entry map[string]any) {
			entry["verified_pack_identity"].(map[string]any)["public_key_sha256"] = "ABC"
		}},
		{name: "malformed domain ref", mutate: func(entry map[string]any) { entry["domain_policy_refs"] = []string{"dpr_bravo001"} }},
		{name: "duplicate domain refs", mutate: func(entry map[string]any) { entry["domain_policy_refs"] = []string{"dpr-alpha001", "dpr-alpha001"} }},
		{name: "unknown capability", mutate: func(entry map[string]any) {
			entry["allowed_capabilities"] = []string{string(CapabilityParseWASM), "cap.unknown"}
		}},
		{name: "invalid ingress domain", mutate: func(entry map[string]any) {
			entry["ingress_domain_rules"] = []map[string]any{{"host": "share.alpha.test/path"}}
		}},
		{name: "invalid broker domain", mutate: func(entry map[string]any) {
			entry["broker_domain_rules"] = []map[string]any{{"host": "api.alpha.test/path"}}
		}},
		{name: "invalid output domain", mutate: func(entry map[string]any) {
			entry["output_domain_rules"] = []map[string]any{{"host": "files.alpha.test/path", "path_prefixes": []string{"/downloads/"}}}
		}},
		{name: "invalid output path prefix", mutate: func(entry map[string]any) {
			entry["output_domain_rules"] = []map[string]any{{"host": "files.alpha.test", "path_prefixes": []string{"downloads"}}}
		}},
		{name: "invalid auth profile id", mutate: func(entry map[string]any) {
			entry["auth_profile_scopes"] = []map[string]any{{"profile_id": "Alpha", "domain_rules": []map[string]any{{"host": "api.alpha.test"}}}}
		}},
		{name: "invalid auth scope domain", mutate: func(entry map[string]any) {
			entry["auth_profile_scopes"] = []map[string]any{{"profile_id": "alpha-secret", "domain_rules": []map[string]any{{"host": "api.alpha.test/path"}}}}
		}},
		{name: "invalid endpoint ref", mutate: func(entry map[string]any) { entry["endpoints"].([]map[string]any)[0]["endpoint_ref"] = "ep_alpha001" }},
		{name: "endpoint broker ref mismatch", mutate: func(entry map[string]any) {
			entry["endpoints"].([]map[string]any)[0]["broker_policy_ref"] = "bpr-bravo001"
		}},
		{name: "invalid endpoint template", mutate: func(entry map[string]any) {
			entry["endpoints"].([]map[string]any)[0]["url_template"] = "https://other.alpha.test/files/{id}"
		}},
		{name: "invalid endpoint method", mutate: func(entry map[string]any) {
			entry["endpoints"].([]map[string]any)[0]["methods"] = []string{"GET", "GET"}
		}},
		{name: "undeclared endpoint auth ref", mutate: func(entry map[string]any) {
			entry["endpoints"].([]map[string]any)[0]["auth_profile_refs"] = []string{"other-secret"}
		}},
		{name: "duplicate endpoint refs", mutate: func(entry map[string]any) {
			endpoint := entry["endpoints"].([]map[string]any)[0]
			entry["endpoints"] = []map[string]any{endpoint, endpoint}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := mustMutatedPrivatePolicyBundleJSON(t, pack.Identity, func(envelope map[string]any) {
				entry := mustPrivatePolicyBundlePackMap(pack.Identity)
				tt.mutate(entry)
				envelope["policy"] = map[string]any{"packs": []any{entry}}
			})
			if _, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{}); err == nil {
				t.Fatal("NewPrivatePolicyBundleResolver() error = nil")
			}
		})
	}
}

func TestPrivatePolicyBundleErrorsAreRedacted(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	raw := mustMutatedPrivatePolicyBundleJSON(t, pack.Identity, func(envelope map[string]any) {
		entry := mustPrivatePolicyBundlePackMap(pack.Identity)
		entry["endpoints"].([]map[string]any)[0]["url_template"] = "https://secret.alpha.test/files/{id}"
		envelope["policy"] = map[string]any{"packs": []any{entry}}
	})
	_, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: hashString('1')})
	if err == nil {
		t.Fatal("NewPrivatePolicyBundleResolver() error = nil")
	}
	for _, forbidden := range []string{"secret.alpha.test", "policy_private_sha256", string(raw), hashString('1')} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error %q leaked %q", err.Error(), forbidden)
		}
	}
}

func TestPrivatePolicyRuntimeSourceHelper(t *testing.T) {
	t.Setenv(privatePolicyBundlePathEnv, "")
	t.Setenv(privatePolicyBundleSHA256Env, "")
	withEmbeddedPrivatePolicyBundleState(t, nil, "", "")

	resolver, err := LoadPrivatePolicyBundleResolverFromRuntimeSources()
	if err != nil {
		t.Fatalf("LoadPrivatePolicyBundleResolverFromRuntimeSources() no source error = %v", err)
	}
	if resolver != nil {
		t.Fatal("resolver != nil for no runtime source")
	}

	pack := syntheticAliasVerifiedPack()
	raw, privateSHA := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)
	path := tempPrivatePolicyBundleFile(t, raw)
	t.Setenv(privatePolicyBundlePathEnv, path)
	t.Setenv(privatePolicyBundleSHA256Env, privateSHA)
	resolver, err = LoadPrivatePolicyBundleResolverFromRuntimeSources()
	if err != nil {
		t.Fatalf("LoadPrivatePolicyBundleResolverFromRuntimeSources() env source error = %v", err)
	}
	if resolver == nil {
		t.Fatal("resolver = nil for env source")
	}

	withEmbeddedPrivatePolicyBundleState(t, raw, privateSHA, hashString('0'))
	_, err = LoadPrivatePolicyBundleResolverFromRuntimeSources()
	if err == nil {
		t.Fatal("LoadPrivatePolicyBundleResolverFromRuntimeSources() ambiguous error = nil")
	}
	if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), string(raw)) || !strings.Contains(err.Error(), "private host policy runtime source is invalid") {
		t.Fatalf("ambiguous error is not redacted/generic: %q", err.Error())
	}
}

func TestPrivatePolicyRuntimeSourceEmbeddedBytes(t *testing.T) {
	t.Setenv(privatePolicyBundlePathEnv, "")
	t.Setenv(privatePolicyBundleSHA256Env, "")
	pack := syntheticAliasVerifiedPack()
	raw, privateSHA := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)
	withEmbeddedPrivatePolicyBundleState(t, raw, privateSHA, hashString('0'))

	resolver, err := LoadPrivatePolicyBundleResolverFromRuntimeSources()
	if err != nil {
		t.Fatalf("LoadPrivatePolicyBundleResolverFromRuntimeSources() embedded error = %v", err)
	}
	if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: pack.Identity, Manifest: pack.Manifest}); err != nil {
		t.Fatalf("embedded resolver ResolveHostPolicy() error = %v", err)
	}
}

func TestPrivatePolicyRuntimeSourceState(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	raw, privateSHA := mustPrivatePolicyBundleJSON(t, pack.Identity, nil)
	path := tempPrivatePolicyBundleFile(t, raw)

	t.Run("none", func(t *testing.T) {
		t.Setenv(privatePolicyBundlePathEnv, "")
		withEmbeddedPrivatePolicyBundleState(t, nil, "", "")
		if got := PrivatePolicyBundleRuntimeSourceState(); got != RuntimeSourceStateNone {
			t.Fatalf("PrivatePolicyBundleRuntimeSourceState() = %q, want none", got)
		}
	})

	t.Run("env", func(t *testing.T) {
		t.Setenv(privatePolicyBundlePathEnv, path)
		t.Setenv(privatePolicyBundleSHA256Env, privateSHA)
		withEmbeddedPrivatePolicyBundleState(t, nil, "", "")
		if got := PrivatePolicyBundleRuntimeSourceState(); got != RuntimeSourceStateEnv {
			t.Fatalf("PrivatePolicyBundleRuntimeSourceState() = %q, want env", got)
		}
	})

	t.Run("embedded", func(t *testing.T) {
		t.Setenv(privatePolicyBundlePathEnv, "")
		withEmbeddedPrivatePolicyBundleState(t, raw, privateSHA, hashString('0'))
		if got := PrivatePolicyBundleRuntimeSourceState(); got != RuntimeSourceStateEmbedded {
			t.Fatalf("PrivatePolicyBundleRuntimeSourceState() = %q, want embedded", got)
		}
	})

	t.Run("ambiguous", func(t *testing.T) {
		t.Setenv(privatePolicyBundlePathEnv, path)
		t.Setenv(privatePolicyBundleSHA256Env, privateSHA)
		withEmbeddedPrivatePolicyBundleState(t, raw, privateSHA, hashString('0'))
		if got := PrivatePolicyBundleRuntimeSourceState(); got != RuntimeSourceStateAmbiguous {
			t.Fatalf("PrivatePolicyBundleRuntimeSourceState() = %q, want ambiguous", got)
		}
	})
}

func mustPrivatePolicyBundleJSON(t *testing.T, identity VerifiedPackIdentity, mutate func(map[string]any)) ([]byte, string) {
	t.Helper()
	raw := mustMutatedPrivatePolicyBundleJSON(t, identity, mutate)
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("json.Unmarshal(envelope) error = %v", err)
	}

	return raw, envelope["policy_private_sha256"].(string)
}

func mustMutatedPrivatePolicyBundleJSON(t *testing.T, identity VerifiedPackIdentity, mutate func(map[string]any)) []byte {
	t.Helper()
	policy := map[string]any{"packs": []any{mustPrivatePolicyBundlePackMap(identity)}}
	envelope := map[string]any{
		"schema_version":            1,
		"bundle_id":                 "hpb-alpha001",
		"bundle_version":            "opaque-1",
		"policy_private_sha256":     hashRawJSONValue(t, policy),
		"policy_public_fingerprint": hashString('0'),
		"policy":                    policy,
	}
	if mutate != nil {
		oldHash := envelope["policy_private_sha256"]
		mutate(envelope)
		if policyValue, ok := envelope["policy"]; ok && envelope["policy_private_sha256"] == oldHash {
			envelope["policy_private_sha256"] = hashRawJSONValue(t, policyValue)
		}
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("json.Marshal(envelope) error = %v", err)
	}

	return raw
}

func mustPrivatePolicyBundlePackMap(identity VerifiedPackIdentity) map[string]any {
	return map[string]any{
		"verified_pack_identity": map[string]any{
			"pack_id":           identity.PackID,
			"pack_version":      identity.PackVersion,
			"asset_sha256":      identity.AssetSHA256,
			"manifest_sha256":   identity.ManifestSHA256,
			"payload_sha256":    identity.PayloadSHA256,
			"signature_sha256":  identity.SignatureSHA256,
			"public_key_sha256": identity.PublicKeySHA256,
		},
		"domain_policy_refs":   []string{"dpr-alpha001"},
		"broker_policy_refs":   []string{"bpr-alpha001"},
		"allowed_capabilities": []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)},
		"ingress_domain_rules": []map[string]any{{"host": "share.alpha.test", "include_subdomains": false}},
		"broker_domain_rules":  []map[string]any{{"host": "api.alpha.test", "include_subdomains": false}, {"host": "files.alpha.test", "include_subdomains": false}},
		"output_domain_rules":  []map[string]any{{"host": "files.alpha.test", "include_subdomains": true, "path_prefixes": []string{"/downloads/"}}},
		"auth_profile_scopes":  []map[string]any{{"profile_id": "alpha-secret", "domain_rules": []map[string]any{{"host": "api.alpha.test"}}}},
		"endpoints": []map[string]any{{
			"broker_policy_ref":  "bpr-alpha001",
			"endpoint_ref":       "ep-alpha001",
			"url_template":       "https://api.alpha.test/files/{id}",
			"methods":            []string{"GET", "HEAD"},
			"auth_profile_refs":  []string{"alpha-secret"},
			"timeout_millis":     100,
			"max_response_bytes": 512,
		}},
	}
}

func hashRawJSONValue(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(value) error = %v", err)
	}

	return sha256HexString(raw)
}

func tempPrivatePolicyBundleFile(t *testing.T, raw []byte) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "policy-*.json")
	if err != nil {
		t.Fatalf("os.CreateTemp() error = %v", err)
	}
	if _, err := file.Write(raw); err != nil {
		t.Fatalf("file.Write() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("file.Close() error = %v", err)
	}

	return file.Name()
}

func withEmbeddedPrivatePolicyBundleState(t *testing.T, raw []byte, privateSHA string, publicFingerprint string) {
	t.Helper()
	oldRaw := embeddedPrivatePolicyBundleJSON
	oldSHA := embeddedPrivatePolicyBundleSHA256
	oldFingerprint := embeddedPrivatePolicyBundlePublicFingerprint
	embeddedPrivatePolicyBundleJSON = cloneBytes(raw)
	embeddedPrivatePolicyBundleSHA256 = privateSHA
	embeddedPrivatePolicyBundlePublicFingerprint = publicFingerprint
	t.Cleanup(func() {
		embeddedPrivatePolicyBundleJSON = oldRaw
		embeddedPrivatePolicyBundleSHA256 = oldSHA
		embeddedPrivatePolicyBundlePublicFingerprint = oldFingerprint
	})
}
