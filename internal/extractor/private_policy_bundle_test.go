package extractor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivatePolicyBundleLoadsAndResolves(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	raw := privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{Identity: identity, Manifest: manifest}}, nil)

	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}
	policy, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: identity, Manifest: manifest})
	if err != nil {
		t.Fatalf("ResolveHostPolicy() error = %v", err)
	}
	if policy.PolicyID != "hpb-alpha001" || policy.PolicyVersion != "opaque-1" || policy.PolicySHA256 != privatePolicyHash(t, raw) {
		t.Fatalf("resolved policy envelope fields mismatch: %#v", policy)
	}
	if policy.PackIdentity != identity {
		t.Fatalf("resolved identity mismatch: %#v", policy.PackIdentity)
	}
	if !policyIngressMatchesHost(policy, "share.alpha.test") {
		t.Fatalf("resolved policy did not include expected ingress: %#v", policy.IngressDomains)
	}
}

func TestPrivatePolicyBundlePreservesOutputAndAuthPolicyScopes(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	outputDomains := []HostPolicyOutputRule{{Host: "assets.alpha.test", IncludeSubdomains: true, PathPrefixes: []string{"/public/", "/"}}}
	authProfiles := []HostPolicyAuthProfileScope{{ProfileID: "apr-alpha001", Domains: []DomainRule{{Host: "api.alpha.test", IncludeSubdomains: true}}}}
	endpoints := []HostPolicyBrokerEndpoint{{
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "epr-alpha001",
		URLTemplate:     "https://api.alpha.test/resource/{id}",
		Methods:         []string{"GET"},
		AuthProfileRefs: []string{"apr-alpha001"},
	}}
	raw := privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{
		Identity:        identity,
		Manifest:        manifest,
		OutputDomains:   outputDomains,
		AuthProfiles:    authProfiles,
		BrokerEndpoints: endpoints,
	}}, nil)

	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}
	policy, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: identity, Manifest: manifest})
	if err != nil {
		t.Fatalf("ResolveHostPolicy() error = %v", err)
	}
	if len(policy.OutputDomains) != 1 || policy.OutputDomains[0].Host != "assets.alpha.test" || !policy.OutputDomains[0].IncludeSubdomains || strings.Join(policy.OutputDomains[0].PathPrefixes, ",") != "/public/,/" {
		t.Fatalf("output domains were not preserved: %#v", policy.OutputDomains)
	}
	if err := policyAllowsOutputURL(policy, "https://cdn.assets.alpha.test/public/item.bin"); err != nil {
		t.Fatalf("policyAllowsOutputURL() subdomain output error = %v", err)
	}
	if !policyAuthProfileMatchesHost(policy, "apr-alpha001", "sub.api.alpha.test") || policyAuthProfileMatchesHost(policy, "apr-alpha001", "assets.alpha.test") {
		t.Fatalf("auth profile scope mismatch: %#v", policy.AuthProfiles)
	}
	endpoint, ok := findBrokerEndpoint(policy, "bpr-alpha001", "epr-alpha001")
	if !ok || !endpointAllowsAuthProfile(endpoint, "apr-alpha001") || endpointAllowsAuthProfile(endpoint, "apr-alpha002") {
		t.Fatalf("endpoint auth refs mismatch: %#v ok=%t", endpoint, ok)
	}
}

func TestPrivatePolicyBundleReturnsDefensiveCopies(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	raw := privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{Identity: identity, Manifest: manifest}}, nil)

	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}
	for i := range raw {
		raw[i] = 0
	}
	policy, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: identity, Manifest: manifest})
	if err != nil {
		t.Fatalf("ResolveHostPolicy() error = %v", err)
	}
	policy.DomainPolicyRefs[0] = "dpr-mutated"
	policy.BrokerPolicyRefs[0] = "bpr-mutated"
	policy.AllowedCapabilities[0] = Capability("cap.changed")
	policy.IngressDomains[0].Host = "mutated.alpha.test"
	policy.OutputDomains[0].PathPrefixes[0] = "/mutated/"
	policy.AuthProfiles[0].Domains[0].Host = "mutated.alpha.test"
	policy.BrokerEndpoints[0].Methods[0] = "POST"
	policy.BrokerEndpoints[0].AuthProfileRefs[0] = "apr-mutated"

	fresh, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: identity, Manifest: manifest})
	if err != nil {
		t.Fatalf("ResolveHostPolicy() fresh error = %v", err)
	}
	if fresh.DomainPolicyRefs[0] != "dpr-alpha001" || fresh.BrokerPolicyRefs[0] != "bpr-alpha001" || fresh.AllowedCapabilities[0] != CapabilityParseWASM || fresh.IngressDomains[0].Host != "share.alpha.test" || fresh.OutputDomains[0].PathPrefixes[0] != "/downloads/" || fresh.AuthProfiles[0].Domains[0].Host != "api.alpha.test" || fresh.BrokerEndpoints[0].Methods[0] != "GET" || fresh.BrokerEndpoints[0].AuthProfileRefs[0] != "apr-alpha001" {
		t.Fatalf("resolved policy was not defensively copied: %#v", fresh)
	}
}

func TestPrivatePolicyBundleResolverDeniesMismatches(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	raw := privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{Identity: identity, Manifest: manifest}}, nil)
	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}

	tests := []struct {
		name     string
		identity VerifiedPackIdentity
		manifest Manifest
	}{
		{name: "missing bundle identity", identity: syntheticVerifiedPackIdentity(validAliasAuthTestManifest(func(m *Manifest) { m.PackID = "xpk-alpha002" })), manifest: validAliasAuthTestManifest(func(m *Manifest) { m.PackID = "xpk-alpha002" })},
		{name: "mismatched asset", identity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.AssetSHA256 = strings.Repeat("1", 64) }), manifest: manifest},
		{name: "mismatched manifest hash", identity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.ManifestSHA256 = strings.Repeat("2", 64) }), manifest: manifest},
		{name: "mismatched payload hash", identity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.PayloadSHA256 = strings.Repeat("3", 64) }), manifest: manifest},
		{name: "mismatched signature hash", identity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.SignatureSHA256 = strings.Repeat("4", 64) }), manifest: manifest},
		{name: "mismatched public key hash", identity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.PublicKeySHA256 = strings.Repeat("5", 64) }), manifest: manifest},
		{name: "mismatched domain refs", identity: identity, manifest: validAliasAuthTestManifest(func(m *Manifest) { m.DomainPolicyRefs = []string{"dpr-alpha002"} })},
		{name: "capability mismatch", identity: identity, manifest: validAliasTestManifest(func(m *Manifest) { m.Capabilities = []Capability{CapabilityParseWASM} })},
		{name: "legacy manifest", identity: syntheticVerifiedPackIdentity(validTestManifest()), manifest: validTestManifest()},
		{name: "incomplete request identity", identity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.AssetSHA256 = "" }), manifest: manifest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: tt.identity, Manifest: tt.manifest})
			if err == nil {
				t.Fatal("ResolveHostPolicy() error = nil, want denial")
			}
			if err.Error() != privateHostPolicyResolutionDenied {
				t.Fatalf("ResolveHostPolicy() error = %q, want generic denial", err.Error())
			}
		})
	}
}

func TestPrivatePolicyBundleRejectsMalformedBundles(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	basePack := privatePolicyBundlePackFixture{Identity: identity, Manifest: manifest}
	validRaw := privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, nil)
	validHash := privatePolicyHash(t, validRaw)

	tests := []struct {
		name string
		raw  []byte
		opts PrivatePolicyBundleLoadOptions
	}{
		{name: "malformed json", raw: []byte(`{`)},
		{name: "unknown envelope field", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) { bundle["unknown"] = true })},
		{name: "unknown policy field", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(_ map[string]any, policy map[string]any, _ []map[string]any) { policy["unknown"] = true })},
		{name: "unknown pack field", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) { packs[0]["unknown"] = true })},
		{name: "unknown identity field", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["verified_pack_identity"].(map[string]any)["unknown"] = true
		})},
		{name: "unknown output rule field", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["output_domain_rules"].([]map[string]any)[0]["unknown"] = true
		})},
		{name: "unknown auth scope field", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["auth_profile_scopes"].([]map[string]any)[0]["unknown"] = true
		})},
		{name: "unknown endpoint field", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["endpoints"].([]map[string]any)[0]["unknown"] = true
		})},
		{name: "trailing json", raw: append(cloneBytes(validRaw), []byte(` {}`)...)},
		{name: "unsupported schema", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) { bundle["schema_version"] = 2 })},
		{name: "malformed bundle id", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["bundle_id"] = "hpb.alpha001"
		})},
		{name: "empty bundle version", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) { bundle["bundle_version"] = "" })},
		{name: "whitespace bundle version", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["bundle_version"] = "opaque 1"
		})},
		{name: "path bundle version", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["bundle_version"] = "opaque/1"
		})},
		{name: "malformed private hash", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["policy_private_sha256"] = strings.Repeat("A", 64)
		})},
		{name: "wrong private hash", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["policy_private_sha256"] = strings.Repeat("1", 64)
		})},
		{name: "expected sha mismatch", raw: validRaw, opts: PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: strings.Repeat("2", 64)}},
		{name: "malformed expected sha", raw: validRaw, opts: PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: strings.Repeat("A", 64)}},
		{name: "expected sha whitespace", raw: validRaw, opts: PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: validHash + " "}},
		{name: "malformed public fingerprint", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["policy_public_fingerprint"] = strings.Repeat("G", 64)
		})},
		{name: "expected fingerprint mismatch", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["policy_public_fingerprint"] = strings.Repeat("3", 64)
		}), opts: PrivatePolicyBundleLoadOptions{ExpectedPolicyPublicFingerprint: strings.Repeat("4", 64)}},
		{name: "malformed expected fingerprint", raw: validRaw, opts: PrivatePolicyBundleLoadOptions{ExpectedPolicyPublicFingerprint: strings.Repeat("Z", 64)}},
		{name: "empty packs", raw: privatePolicyBundleRaw(t, nil, nil)},
		{name: "missing asset sha", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{Identity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.AssetSHA256 = "" }), Manifest: manifest}}, nil)},
		{name: "malformed manifest sha", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{Identity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.ManifestSHA256 = "bad" }), Manifest: manifest}}, nil)},
		{name: "duplicate identities", raw: privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{basePack, basePack}, nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, err := NewPrivatePolicyBundleResolver(tt.raw, tt.opts)
			if err == nil {
				t.Fatalf("NewPrivatePolicyBundleResolver() error = nil, resolver=%#v", resolver)
			}
			assertGenericPrivateBundleError(t, err)
		})
	}

	if _, err := NewPrivatePolicyBundleResolver(validRaw, PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: validHash}); err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() expected sha match error = %v", err)
	}
}

func TestPrivatePolicyBundleInvalidPoliciesFailAtLookup(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)

	tests := []struct {
		name   string
		mutate func(*privatePolicyBundlePackFixture)
	}{
		{name: "invalid ingress domain", mutate: func(f *privatePolicyBundlePackFixture) { f.IngressDomains = []DomainRule{{Host: "*.alpha.test"}} }},
		{name: "invalid broker domain", mutate: func(f *privatePolicyBundlePackFixture) { f.BrokerDomains = []DomainRule{{Host: "api.alpha.test:443"}} }},
		{name: "invalid output rule", mutate: func(f *privatePolicyBundlePackFixture) { f.OutputDomains[0].Host = "files.alpha.test/path" }},
		{name: "invalid output path prefix", mutate: func(f *privatePolicyBundlePackFixture) { f.OutputDomains[0].PathPrefixes = []string{"downloads"} }},
		{name: "invalid auth profile scope", mutate: func(f *privatePolicyBundlePackFixture) { f.AuthProfiles[0].ProfileID = "Invalid" }},
		{name: "invalid auth profile scope domain", mutate: func(f *privatePolicyBundlePackFixture) {
			f.AuthProfiles[0].Domains = []DomainRule{{Host: "api.alpha.test/path"}}
		}},
		{name: "invalid endpoint ref", mutate: func(f *privatePolicyBundlePackFixture) { f.BrokerEndpoints[0].EndpointRef = "endpoint.alpha" }},
		{name: "invalid endpoint template", mutate: func(f *privatePolicyBundlePackFixture) {
			f.BrokerEndpoints[0].URLTemplate = "https://user:pass@api.alpha.test/resource/{id}"
		}},
		{name: "invalid endpoint method", mutate: func(f *privatePolicyBundlePackFixture) { f.BrokerEndpoints[0].Methods = []string{"GET", "GET"} }},
		{name: "invalid auth profile ref", mutate: func(f *privatePolicyBundlePackFixture) { f.BrokerEndpoints[0].AuthProfileRefs = []string{"Invalid"} }},
		{name: "undeclared auth profile ref", mutate: func(f *privatePolicyBundlePackFixture) {
			f.BrokerEndpoints[0].AuthProfileRefs = []string{"apr-alpha002"}
		}},
		{name: "undeclared broker ref", mutate: func(f *privatePolicyBundlePackFixture) { f.BrokerEndpoints[0].BrokerPolicyRef = "bpr-alpha002" }},
		{name: "endpoint timeout exceeds limit", mutate: func(f *privatePolicyBundlePackFixture) {
			f.BrokerEndpoints[0].TimeoutMillis = DefaultTrustPolicy().MaxResourceLimits.TimeoutMillis + 1
		}},
		{name: "endpoint response exceeds limit", mutate: func(f *privatePolicyBundlePackFixture) {
			f.BrokerEndpoints[0].MaxResponseBytes = DefaultTrustPolicy().MaxResourceLimits.MaxResponseBytes + 1
		}},
		{name: "duplicate endpoint ref", mutate: func(f *privatePolicyBundlePackFixture) {
			f.BrokerEndpoints = append(f.BrokerEndpoints, f.BrokerEndpoints[0])
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := privatePolicyBundlePackFixture{
				Identity: identity, Manifest: manifest,
				OutputDomains: []HostPolicyOutputRule{{Host: "files.alpha.test", IncludeSubdomains: true, PathPrefixes: []string{"/downloads/"}}},
				AuthProfiles:  []HostPolicyAuthProfileScope{{ProfileID: "apr-alpha001", Domains: []DomainRule{{Host: "api.alpha.test"}}}},
				BrokerEndpoints: []HostPolicyBrokerEndpoint{{
					BrokerPolicyRef:  "bpr-alpha001",
					EndpointRef:      "epr-alpha001",
					URLTemplate:      "https://api.alpha.test/resource/{id}",
					Methods:          []string{"GET", "HEAD"},
					AuthProfileRefs:  []string{"apr-alpha001"},
					TimeoutMillis:    100,
					MaxResponseBytes: 512,
				}},
			}
			tt.mutate(&fixture)
			resolver, err := NewPrivatePolicyBundleResolver(privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{fixture}, nil), PrivatePolicyBundleLoadOptions{})
			if err != nil {
				assertGenericPrivateBundleError(t, err)
				return
			}
			_, err = resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: identity, Manifest: manifest})
			if err == nil {
				t.Fatal("ResolveHostPolicy() error = nil, want validation denial")
			}
			if err.Error() != privateHostPolicyResolutionDenied {
				t.Fatalf("ResolveHostPolicy() error = %q, want generic denial", err.Error())
			}
		})
	}
}

func TestPrivatePolicyBundleErrorsAreGeneric(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	raw := privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{Identity: identity, Manifest: manifest}}, nil)
	privateHash := privatePolicyHash(t, raw)
	rawJSON := string(raw)
	endpointTemplate := "https://api.alpha.test/resource/{id}"

	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: strings.Repeat("1", 64)})
	if err == nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = nil, resolver=%#v", resolver)
	}
	assertNoPrivateBundleLeak(t, err.Error(), privateHash, rawJSON, endpointTemplate, "api.alpha.test", "share.alpha.test")

	resolver, err = NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}
	_, err = resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: mutateIdentity(identity, func(id *VerifiedPackIdentity) { id.AssetSHA256 = strings.Repeat("1", 64) }), Manifest: manifest})
	if err == nil {
		t.Fatal("ResolveHostPolicy() error = nil, want denial")
	}
	assertNoPrivateBundleLeak(t, err.Error(), privateHash, rawJSON, endpointTemplate, "api.alpha.test", "share.alpha.test")
}

func TestPrivatePolicyRuntimeSource(t *testing.T) {
	withPrivatePolicyRuntimeEnv(t, "", "")
	withEmbeddedPrivatePolicyBundleState(t, nil, "")

	resolver, err := LoadPrivatePolicyBundleResolverFromRuntimeSources()
	if err != nil {
		t.Fatalf("LoadPrivatePolicyBundleResolverFromRuntimeSources() no source error = %v", err)
	}
	if resolver != nil {
		t.Fatal("LoadPrivatePolicyBundleResolverFromRuntimeSources() no source resolver != nil")
	}

	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	raw := privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{Identity: identity, Manifest: manifest}}, nil)
	policyHash := privatePolicyHash(t, raw)
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Run("env path loads", func(t *testing.T) {
		withPrivatePolicyRuntimeEnv(t, path, policyHash)
		withEmbeddedPrivatePolicyBundleState(t, nil, "")

		resolver, err := LoadPrivatePolicyBundleResolverFromRuntimeSources()
		if err != nil {
			t.Fatalf("LoadPrivatePolicyBundleResolverFromRuntimeSources() error = %v", err)
		}
		if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: identity, Manifest: manifest}); err != nil {
			t.Fatalf("ResolveHostPolicy() error = %v", err)
		}
	})

	t.Run("env expected sha mismatch", func(t *testing.T) {
		withPrivatePolicyRuntimeEnv(t, path, strings.Repeat("1", 64))
		withEmbeddedPrivatePolicyBundleState(t, nil, "")

		_, err := LoadPrivatePolicyBundleResolverFromRuntimeSources()
		if err == nil {
			t.Fatal("LoadPrivatePolicyBundleResolverFromRuntimeSources() error = nil, want mismatch")
		}
		assertGenericPrivateBundleError(t, err)
		assertNoPrivateBundleLeak(t, err.Error(), path, policyHash, string(raw), "api.alpha.test")
	})

	t.Run("embedded loads", func(t *testing.T) {
		withPrivatePolicyRuntimeEnv(t, "", "")
		withEmbeddedPrivatePolicyBundleState(t, raw, policyHash)

		resolver, err := LoadPrivatePolicyBundleResolverFromRuntimeSources()
		if err != nil {
			t.Fatalf("LoadPrivatePolicyBundleResolverFromRuntimeSources() embedded error = %v", err)
		}
		if _, err := resolver.ResolveHostPolicy(context.Background(), HostPolicyRequest{PackIdentity: identity, Manifest: manifest}); err != nil {
			t.Fatalf("ResolveHostPolicy() embedded policy error = %v", err)
		}
	})

	t.Run("env and embedded ambiguity", func(t *testing.T) {
		withPrivatePolicyRuntimeEnv(t, path, policyHash)
		withEmbeddedPrivatePolicyBundleState(t, raw, policyHash)

		_, err := LoadPrivatePolicyBundleResolverFromRuntimeSources()
		if err == nil {
			t.Fatal("LoadPrivatePolicyBundleResolverFromRuntimeSources() error = nil, want ambiguity denial")
		}
		assertGenericPrivateBundleError(t, err)
		assertNoPrivateBundleLeak(t, err.Error(), path, policyHash, string(raw), "api.alpha.test")
	})

	t.Run("file error is redacted", func(t *testing.T) {
		missingPath := filepath.Join(t.TempDir(), "missing-policy.json")
		withPrivatePolicyRuntimeEnv(t, missingPath, "")
		withEmbeddedPrivatePolicyBundleState(t, nil, "")

		_, err := LoadPrivatePolicyBundleResolverFromRuntimeSources()
		if err == nil {
			t.Fatal("LoadPrivatePolicyBundleResolverFromRuntimeSources() error = nil, want file denial")
		}
		assertGenericPrivateBundleError(t, err)
		assertNoPrivateBundleLeak(t, err.Error(), missingPath)
	})
}

type privatePolicyBundlePackFixture struct {
	Identity            VerifiedPackIdentity
	Manifest            Manifest
	DomainPolicyRefs    []string
	BrokerPolicyRefs    []string
	AllowedCapabilities []Capability
	IngressDomains      []DomainRule
	BrokerDomains       []DomainRule
	OutputDomains       []HostPolicyOutputRule
	AuthProfiles        []HostPolicyAuthProfileScope
	BrokerEndpoints     []HostPolicyBrokerEndpoint
}

func privatePolicyBundleRaw(t *testing.T, fixtures []privatePolicyBundlePackFixture, mutate func(map[string]any, map[string]any, []map[string]any)) []byte {
	t.Helper()

	packs := make([]map[string]any, 0, len(fixtures))
	for _, fixture := range fixtures {
		packs = append(packs, privatePolicyBundlePackMap(fixture))
	}
	policy := map[string]any{"packs": packs}
	bundle := map[string]any{
		"schema_version": 1,
		"bundle_id":      "hpb-alpha001",
		"bundle_version": "opaque-1",
	}
	if mutate != nil {
		mutate(bundle, policy, packs)
	}
	policyJSON := mustMarshalPrivatePolicyBundleJSON(t, policy)
	if _, ok := bundle["policy_private_sha256"]; !ok {
		bundle["policy_private_sha256"] = sha256Hex(policyJSON)
	}
	if _, ok := bundle["policy"]; !ok {
		bundle["policy"] = json.RawMessage(policyJSON)
	}

	return mustMarshalPrivatePolicyBundleJSON(t, bundle)
}

func privatePolicyBundlePackMap(fixture privatePolicyBundlePackFixture) map[string]any {
	manifest := fixture.Manifest
	if manifest.PackID == "" {
		manifest = validAliasTestManifest(nil)
	}
	identity := fixture.Identity
	if identity.PackID == "" {
		identity = syntheticVerifiedPackIdentity(manifest)
	}
	domainRefs := fixture.DomainPolicyRefs
	if domainRefs == nil {
		domainRefs = cloneStringSlice(manifest.DomainPolicyRefs)
	}
	brokerRefs := fixture.BrokerPolicyRefs
	if brokerRefs == nil {
		brokerRefs = cloneStringSlice(manifest.BrokerPolicyRefs)
	}
	capabilities := fixture.AllowedCapabilities
	if capabilities == nil {
		capabilities = append([]Capability(nil), manifest.Capabilities...)
	}
	ingressDomains := fixture.IngressDomains
	if ingressDomains == nil {
		ingressDomains = []DomainRule{{Host: "share.alpha.test"}, {Host: "files.alpha.test", IncludeSubdomains: true}}
	}
	brokerDomains := fixture.BrokerDomains
	if brokerDomains == nil {
		brokerDomains = []DomainRule{{Host: "api.alpha.test"}}
	}
	outputDomains := fixture.OutputDomains
	if outputDomains == nil {
		outputDomains = []HostPolicyOutputRule{{Host: "files.alpha.test", IncludeSubdomains: true, PathPrefixes: []string{"/downloads/"}}}
	}
	authProfiles := fixture.AuthProfiles
	if authProfiles == nil {
		if manifestHasCapability(manifest, CapabilityAuthProfile) {
			authProfiles = []HostPolicyAuthProfileScope{{ProfileID: "apr-alpha001", Domains: []DomainRule{{Host: "api.alpha.test"}}}}
		}
	}
	brokerEndpoints := fixture.BrokerEndpoints
	if brokerEndpoints == nil {
		authProfileRefs := []string(nil)
		if manifestHasCapability(manifest, CapabilityAuthProfile) {
			authProfileRefs = []string{"apr-alpha001"}
		}
		brokerEndpoints = []HostPolicyBrokerEndpoint{{
			BrokerPolicyRef:  "bpr-alpha001",
			EndpointRef:      "epr-alpha001",
			URLTemplate:      "https://api.alpha.test/resource/{id}",
			Methods:          []string{"GET", "HEAD"},
			AuthProfileRefs:  authProfileRefs,
			TimeoutMillis:    100,
			MaxResponseBytes: 512,
		}}
	}

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
		"domain_policy_refs":   cloneStringSlice(domainRefs),
		"broker_policy_refs":   cloneStringSlice(brokerRefs),
		"allowed_capabilities": append([]Capability(nil), capabilities...),
		"ingress_domain_rules": cloneDomainRules(ingressDomains),
		"broker_domain_rules":  cloneDomainRules(brokerDomains),
		"output_domain_rules":  privatePolicyBundleOutputRuleMaps(outputDomains),
		"auth_profile_scopes":  privatePolicyBundleAuthScopeMaps(authProfiles),
		"endpoints":            privatePolicyBundleEndpointMaps(brokerEndpoints),
	}
}

func validAliasAuthTestManifest(mutate func(*Manifest)) Manifest {
	manifest := validAliasTestManifest(nil)
	if !manifestHasCapability(manifest, CapabilityAuthProfile) {
		manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	}
	if mutate != nil {
		mutate(&manifest)
	}

	return manifest
}

func privatePolicyBundleOutputRuleMaps(rules []HostPolicyOutputRule) []map[string]any {
	if rules == nil {
		return nil
	}
	mapped := make([]map[string]any, len(rules))
	for i, rule := range rules {
		mapped[i] = map[string]any{
			"host":               rule.Host,
			"include_subdomains": rule.IncludeSubdomains,
			"path_prefixes":      cloneStringSlice(rule.PathPrefixes),
		}
	}

	return mapped
}

func privatePolicyBundleAuthScopeMaps(scopes []HostPolicyAuthProfileScope) []map[string]any {
	if scopes == nil {
		return nil
	}
	mapped := make([]map[string]any, len(scopes))
	for i, scope := range scopes {
		mapped[i] = map[string]any{
			"profile_id":   string(scope.ProfileID),
			"domain_rules": cloneDomainRules(scope.Domains),
		}
	}

	return mapped
}

func privatePolicyBundleEndpointMaps(endpoints []HostPolicyBrokerEndpoint) []map[string]any {
	if endpoints == nil {
		return nil
	}
	mapped := make([]map[string]any, len(endpoints))
	for i, endpoint := range endpoints {
		mapped[i] = map[string]any{
			"broker_policy_ref":  endpoint.BrokerPolicyRef,
			"endpoint_ref":       endpoint.EndpointRef,
			"url_template":       endpoint.URLTemplate,
			"methods":            cloneStringSlice(endpoint.Methods),
			"auth_profile_refs":  cloneStringSlice(endpoint.AuthProfileRefs),
			"timeout_millis":     endpoint.TimeoutMillis,
			"max_response_bytes": endpoint.MaxResponseBytes,
		}
	}

	return mapped
}

func mustMarshalPrivatePolicyBundleJSON(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	return raw
}

func privatePolicyHash(t *testing.T, raw []byte) string {
	t.Helper()

	var envelope struct {
		Policy json.RawMessage `json:"policy"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	return sha256Hex(envelope.Policy)
}

func mutateIdentity(identity VerifiedPackIdentity, mutate func(*VerifiedPackIdentity)) VerifiedPackIdentity {
	mutated := identity
	mutate(&mutated)

	return mutated
}

func assertGenericPrivateBundleError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("error = nil, want generic private bundle error")
	}
	if err.Error() != privatePolicyBundleInvalidError {
		t.Fatalf("error = %q, want %q", err.Error(), privatePolicyBundleInvalidError)
	}
}

func assertNoPrivateBundleLeak(t *testing.T, message string, forbidden ...string) {
	t.Helper()

	for _, value := range forbidden {
		if value == "" {
			continue
		}
		if strings.Contains(message, value) {
			t.Fatalf("error %q leaks forbidden value %q", message, value)
		}
	}
}

func withPrivatePolicyRuntimeEnv(t *testing.T, path string, expectedSHA string) {
	t.Helper()

	oldPath, hadPath := os.LookupEnv(privatePolicyBundlePathEnv)
	oldSHA, hadSHA := os.LookupEnv(privatePolicyBundleExpectedSHA256Env)
	if path == "" {
		_ = os.Unsetenv(privatePolicyBundlePathEnv)
	} else {
		_ = os.Setenv(privatePolicyBundlePathEnv, path)
	}
	if expectedSHA == "" {
		_ = os.Unsetenv(privatePolicyBundleExpectedSHA256Env)
	} else {
		_ = os.Setenv(privatePolicyBundleExpectedSHA256Env, expectedSHA)
	}
	t.Cleanup(func() {
		if hadPath {
			_ = os.Setenv(privatePolicyBundlePathEnv, oldPath)
		} else {
			_ = os.Unsetenv(privatePolicyBundlePathEnv)
		}
		if hadSHA {
			_ = os.Setenv(privatePolicyBundleExpectedSHA256Env, oldSHA)
		} else {
			_ = os.Unsetenv(privatePolicyBundleExpectedSHA256Env)
		}
	})
}

func withEmbeddedPrivatePolicyBundleState(t *testing.T, raw []byte, expectedSHA string) {
	t.Helper()

	oldRaw := embeddedPrivatePolicyBundleJSON
	oldSHA := embeddedPrivatePolicyBundleSHA256
	embeddedPrivatePolicyBundleJSON = cloneBytes(raw)
	embeddedPrivatePolicyBundleSHA256 = expectedSHA
	t.Cleanup(func() {
		embeddedPrivatePolicyBundleJSON = oldRaw
		embeddedPrivatePolicyBundleSHA256 = oldSHA
	})
}
