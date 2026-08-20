package extractor

import (
	"context"
	"errors"
	"testing"
)

type fakeHostPolicyResolver struct {
	policy      ResolvedHostPolicy
	err         error
	lastRequest HostPolicyRequest
	calls       int
}

func (r *fakeHostPolicyResolver) ResolveHostPolicy(_ context.Context, request HostPolicyRequest) (ResolvedHostPolicy, error) {
	r.calls++
	r.lastRequest = request
	if r.err != nil {
		return ResolvedHostPolicy{}, r.err
	}

	return cloneResolvedHostPolicy(r.policy), nil
}

func TestHostOwnedPolicyValidatesBindingAndDefensiveCopies(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}

	policy, err := resolveAliasHostPolicy(context.Background(), resolver, pack.Identity, pack.Manifest)
	if err != nil {
		t.Fatalf("resolveAliasHostPolicy() error = %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	if resolver.lastRequest.PackIdentity != pack.Identity {
		t.Fatalf("resolver identity = %#v, want verified identity", resolver.lastRequest.PackIdentity)
	}
	resolver.lastRequest.Manifest.DomainPolicyRefs[0] = "dpr-mutated"
	if pack.Manifest.DomainPolicyRefs[0] != "dpr-alpha001" {
		t.Fatalf("manifest passed to resolver was not defensive-copied")
	}

	policy.DomainPolicyRefs[0] = "dpr-mutated"
	policy.IngressDomains[0].Host = "mutated.test"
	fresh, err := resolveAliasHostPolicy(context.Background(), resolver, pack.Identity, pack.Manifest)
	if err != nil {
		t.Fatalf("resolveAliasHostPolicy() second error = %v", err)
	}
	if fresh.DomainPolicyRefs[0] != "dpr-alpha001" || fresh.IngressDomains[0].Host != "share.alpha.test" {
		t.Fatalf("resolved policy was not defensive-copied: %#v", fresh)
	}
}

func TestHostOwnedPolicyRejectsMismatchesAndMalformedDomains(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	base := syntheticHostPolicy(pack.Identity)
	tests := []struct {
		name   string
		mutate func(*ResolvedHostPolicy)
	}{
		{name: "resolver error", mutate: func(policy *ResolvedHostPolicy) {}},
		{name: "identity mismatch", mutate: func(policy *ResolvedHostPolicy) { policy.PackIdentity.ManifestSHA256 = hashString('9') }},
		{name: "domain ref mismatch", mutate: func(policy *ResolvedHostPolicy) { policy.DomainPolicyRefs = []string{"dpr-bravo001"} }},
		{name: "broker ref mismatch", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerPolicyRefs = []string{"bpr-bravo001"} }},
		{name: "capability mismatch", mutate: func(policy *ResolvedHostPolicy) { policy.AllowedCapabilities = []Capability{CapabilityParseWASM} }},
		{name: "wildcard ingress", mutate: func(policy *ResolvedHostPolicy) { policy.IngressDomains = []DomainRule{{Host: "*.alpha.test"}} }},
		{name: "missing output domains", mutate: func(policy *ResolvedHostPolicy) { policy.OutputDomains = nil }},
		{name: "invalid output domain", mutate: func(policy *ResolvedHostPolicy) {
			policy.OutputDomains = []HostPolicyOutputRule{{Host: "files.alpha.test/path", PathPrefixes: []string{"/downloads/"}}}
		}},
		{name: "missing output path prefixes", mutate: func(policy *ResolvedHostPolicy) {
			policy.OutputDomains = []HostPolicyOutputRule{{Host: "files.alpha.test"}}
		}},
		{name: "unsafe output path prefix", mutate: func(policy *ResolvedHostPolicy) {
			policy.OutputDomains = []HostPolicyOutputRule{{Host: "files.alpha.test", PathPrefixes: []string{"downloads"}}}
		}},
		{name: "invalid broker", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerDomains = []DomainRule{{Host: "api.alpha.test/path"}} }},
		{name: "invalid auth scope", mutate: func(policy *ResolvedHostPolicy) {
			policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: "Default", Domains: []DomainRule{{Host: "api.alpha.test"}}}}
		}},
		{name: "duplicate endpoint", mutate: func(policy *ResolvedHostPolicy) {
			policy.Endpoints = append(policy.Endpoints, policy.Endpoints[0])
		}},
		{name: "endpoint broker ref mismatch", mutate: func(policy *ResolvedHostPolicy) {
			policy.Endpoints[0].BrokerPolicyRef = "bpr-bravo001"
		}},
		{name: "endpoint unsafe template host", mutate: func(policy *ResolvedHostPolicy) {
			policy.Endpoints[0].URLTemplate = "https://other.alpha.test/files/{id}"
		}},
		{name: "endpoint sensitive placeholder", mutate: func(policy *ResolvedHostPolicy) {
			policy.Endpoints[0].URLTemplate = "https://api.alpha.test/files/{token}"
		}},
		{name: "endpoint malformed method", mutate: func(policy *ResolvedHostPolicy) {
			policy.Endpoints[0].Methods = []string{"GET", "GET"}
		}},
		{name: "endpoint duplicate auth refs", mutate: func(policy *ResolvedHostPolicy) {
			policy.Endpoints[0].AuthProfileRefs = []AuthProfileID{"alpha-secret", "alpha-secret"}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := cloneResolvedHostPolicy(base)
			resolverErr := error(nil)
			if tt.name == "resolver error" {
				resolverErr = errors.New("private policy details should not leak")
			} else {
				tt.mutate(&policy)
			}
			resolver := &fakeHostPolicyResolver{policy: policy, err: resolverErr}

			if _, err := resolveAliasHostPolicy(context.Background(), resolver, pack.Identity, pack.Manifest); err == nil {
				t.Fatal("resolveAliasHostPolicy() error = nil, want fail closed")
			}
		})
	}
}

func TestHostOwnedPolicyOutputURLValidation(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	policy := syntheticHostPolicy(pack.Identity)

	allowed := []string{
		"https://files.alpha.test/downloads/file.bin",
		"https://cdn.files.alpha.test/downloads/file.bin",
	}
	for _, rawURL := range allowed {
		t.Run("allow "+rawURL, func(t *testing.T) {
			if err := policyAllowsOutputURL(policy, rawURL); err != nil {
				t.Fatalf("policyAllowsOutputURL(%q) error = %v", rawURL, err)
			}
		})
	}

	denied := []string{
		"https://api.alpha.test/files/file.bin",
		"https://share.alpha.test/downloads/file.bin",
		"https://unproven.alpha.test/downloads/file.bin",
		"https://files.alpha.test/private/file.bin",
		"https://files.alpha.test/downloads/file.bin?token=secret",
		"http://files.alpha.test/downloads/file.bin",
	}
	for _, rawURL := range denied {
		t.Run("deny "+rawURL, func(t *testing.T) {
			if err := policyAllowsOutputURL(policy, rawURL); err == nil {
				t.Fatalf("policyAllowsOutputURL(%q) error = nil, want fail closed", rawURL)
			}
		})
	}
}

func TestHostOwnedPolicyOutputURLValidationPathBoundSubdomains(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	policy := syntheticHostPolicy(pack.Identity)
	policy.OutputDomains = []HostPolicyOutputRule{{
		Host:              "fixture.invalid",
		IncludeSubdomains: true,
		PathPrefixes:      []string{"/files/", "/download/"},
	}}

	allowed := []string{
		"https://assets.fixture.invalid/download/synthetic-file.bin",
		"https://download.fixture.invalid/files/synthetic-file.bin",
		"https://download.fixture.invalid/download/synthetic-file.bin",
	}
	for _, rawURL := range allowed {
		t.Run("allow "+rawURL, func(t *testing.T) {
			if err := policyAllowsOutputURL(policy, rawURL); err != nil {
				t.Fatalf("policyAllowsOutputURL(%q) error = %v", rawURL, err)
			}
		})
	}

	denied := []string{
		"https://assets.fixture.invalid/other/synthetic-file.bin",
		"https://assets.fixture.invalid/download/synthetic-file.bin?token=synthetic",
		"https://assets.fixture.invalid/download/synthetic-file.bin#fragment",
		"https://example.invalid/download/synthetic-file.bin",
		"https://assets.fixture.invalid/synthetic-file.bin",
		"https://fixture.invalid/",
	}
	for _, rawURL := range denied {
		t.Run("deny "+rawURL, func(t *testing.T) {
			if err := policyAllowsOutputURL(policy, rawURL); err == nil {
				t.Fatalf("policyAllowsOutputURL(%q) error = nil, want fail closed", rawURL)
			}
		})
	}
}

func TestHostOwnedPolicyEndpointExpansionValidatesParams(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	policy := syntheticHostPolicy(pack.Identity)
	endpoint, ok := resolveHostPolicyEndpoint(policy, "bpr-alpha001", "ep-alpha001")
	if !ok {
		t.Fatal("resolveHostPolicyEndpoint() ok = false")
	}

	expanded, err := expandHostPolicyEndpointURL(policy, endpoint, map[string]string{"id": "fixture-item"})
	if err != nil {
		t.Fatalf("expandHostPolicyEndpointURL() error = %v", err)
	}
	if expanded != "https://api.alpha.test/files/fixture-item" {
		t.Fatalf("expanded URL = %q", expanded)
	}
	if _, _, _, err := validateHostPolicyEndpointRequest(endpoint, "GET", "alpha-secret", pack.Manifest, DefaultHTTPBrokerPolicy()); err != nil {
		t.Fatalf("validateHostPolicyEndpointRequest() error = %v", err)
	}

	tests := []struct {
		name   string
		params map[string]string
	}{
		{name: "missing required", params: nil},
		{name: "unknown param", params: map[string]string{"id": "fixture-item", "extra": "value"}},
		{name: "url value", params: map[string]string{"id": "https://evil.test/path"}},
		{name: "path separator", params: map[string]string{"id": "a/b"}},
		{name: "percent escape", params: map[string]string{"id": "a%2fb"}},
		{name: "control", params: map[string]string{"id": "a\nb"}},
		{name: "sensitive key", params: map[string]string{"token": "value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := expandHostPolicyEndpointURL(policy, endpoint, tt.params); err == nil {
				t.Fatal("expandHostPolicyEndpointURL() error = nil, want fail closed")
			}
		})
	}
	if _, _, _, err := validateHostPolicyEndpointRequest(endpoint, "POST", "alpha-secret", pack.Manifest, DefaultHTTPBrokerPolicy()); err == nil {
		t.Fatal("validateHostPolicyEndpointRequest() POST error = nil")
	}
	if _, _, _, err := validateHostPolicyEndpointRequest(endpoint, "GET", "other-secret", pack.Manifest, DefaultHTTPBrokerPolicy()); err == nil {
		t.Fatal("validateHostPolicyEndpointRequest() auth ref error = nil")
	}
}

func syntheticAliasVerifiedPack() VerifiedPack {
	manifest := validAliasTestManifest()
	identity := VerifiedPackIdentity{
		PackID:          manifest.PackID,
		PackVersion:     manifest.PackVersion,
		AssetSHA256:     hashString('a'),
		ManifestSHA256:  hashString('b'),
		PayloadSHA256:   hashString('c'),
		SignatureSHA256: hashString('d'),
		PublicKeySHA256: hashString('e'),
	}

	return VerifiedPack{Manifest: manifest, Payload: []byte("alias payload"), Identity: identity}
}

func syntheticHostPolicy(identity VerifiedPackIdentity) ResolvedHostPolicy {
	return ResolvedHostPolicy{
		PolicyID:            "hpr-alpha001",
		PolicyVersion:       "opaque-1",
		PolicySHA256:        hashString('f'),
		PackIdentity:        identity,
		DomainPolicyRefs:    []string{"dpr-alpha001"},
		BrokerPolicyRefs:    []string{"bpr-alpha001"},
		AllowedCapabilities: []Capability{CapabilityParseWASM, CapabilityHTTPFetch, CapabilityAuthProfile},
		IngressDomains:      []DomainRule{{Host: "share.alpha.test"}},
		BrokerDomains:       []DomainRule{{Host: "api.alpha.test"}, {Host: "files.alpha.test"}},
		OutputDomains: []HostPolicyOutputRule{{
			Host:              "files.alpha.test",
			IncludeSubdomains: true,
			PathPrefixes:      []string{"/downloads/"},
		}},
		AuthProfiles: []HostPolicyAuthProfileScope{{
			ProfileID: "alpha-secret",
			Domains:   []DomainRule{{Host: "api.alpha.test"}},
		}},
		Endpoints: []HostPolicyEndpoint{{
			BrokerPolicyRef:  "bpr-alpha001",
			EndpointRef:      "ep-alpha001",
			URLTemplate:      "https://api.alpha.test/files/{id}",
			Methods:          []string{"GET", "HEAD"},
			AuthProfileRefs:  []AuthProfileID{"alpha-secret"},
			TimeoutMillis:    100,
			MaxResponseBytes: 512,
		}},
	}
}

func hashString(ch byte) string {
	bytes := make([]byte, 64)
	for i := range bytes {
		bytes[i] = ch
	}

	return string(bytes)
}
