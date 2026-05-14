package extractor

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

type fakeHostPolicyResolver struct {
	policy      ResolvedHostPolicy
	err         error
	lastRequest HostPolicyRequest
}

func (r *fakeHostPolicyResolver) ResolveHostPolicy(ctx context.Context, request HostPolicyRequest) (ResolvedHostPolicy, error) {
	r.lastRequest = request
	if r.err != nil {
		return ResolvedHostPolicy{}, r.err
	}

	return cloneResolvedHostPolicy(r.policy), nil
}

func TestHostPolicyResolverAcceptsMatchingPolicy(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	resolver := &fakeHostPolicyResolver{policy: validResolvedHostPolicy(identity, manifest)}

	policy, err := resolveAliasHostPolicy(context.Background(), resolver, identity, manifest)
	if err != nil {
		t.Fatalf("resolveAliasHostPolicy() error = %v", err)
	}
	if !policyIngressMatchesHost(policy, "share.alpha.test") || !policyIngressMatchesHost(policy, "files.alpha.test") {
		t.Fatalf("policy did not match expected synthetic ingress domains: %#v", policy.IngressDomains)
	}
	if policyIngressMatchesHost(policy, "outside.alpha.test") {
		t.Fatalf("policy matched unexpected host")
	}

	resolver.lastRequest.Manifest.DomainPolicyRefs[0] = "dpr-mutated"
	resolver.lastRequest.Manifest.BrokerPolicyRefs[0] = "bpr-mutated"
	if manifest.DomainPolicyRefs[0] != "dpr-alpha001" || manifest.BrokerPolicyRefs[0] != "bpr-alpha001" {
		t.Fatalf("resolver request did not receive defensive manifest copy")
	}
	policy.DomainPolicyRefs[0] = "dpr-mutated"
	policy.BrokerPolicyRefs[0] = "bpr-mutated"
	policy.AllowedCapabilities[0] = Capability("cap.changed")
	policy.IngressDomains[0].Host = "mutated.alpha.test"
	policy.OutputDomains[0].PathPrefixes[0] = "/mutated/"
	policy.AuthProfiles[0].Domains[0].Host = "mutated.alpha.test"
	policy.BrokerEndpoints[0].Methods[0] = "POST"
	policy.BrokerEndpoints[0].AuthProfileRefs[0] = "apr-mutated"
	fresh, err := resolveAliasHostPolicy(context.Background(), resolver, identity, manifest)
	if err != nil {
		t.Fatalf("resolveAliasHostPolicy() fresh error = %v", err)
	}
	if fresh.DomainPolicyRefs[0] != "dpr-alpha001" || fresh.BrokerPolicyRefs[0] != "bpr-alpha001" || fresh.IngressDomains[0].Host != "share.alpha.test" || fresh.OutputDomains[0].PathPrefixes[0] != "/downloads/" || fresh.AuthProfiles[0].Domains[0].Host != "api.alpha.test" || fresh.BrokerEndpoints[0].Methods[0] != "GET" || fresh.BrokerEndpoints[0].AuthProfileRefs[0] != "apr-alpha001" {
		t.Fatalf("resolved host policy was not defensively copied: %#v", fresh)
	}
}

func TestHostPolicyResolverRejectsInvalidPolicies(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	basePolicy := validResolvedHostPolicy(identity, manifest)

	tests := []struct {
		name   string
		mutate func(*ResolvedHostPolicy)
	}{
		{name: "mismatched identity", mutate: func(policy *ResolvedHostPolicy) { policy.PackIdentity.PackID = "xpk-alpha002" }},
		{name: "mismatched domain refs", mutate: func(policy *ResolvedHostPolicy) { policy.DomainPolicyRefs = []string{"dpr-alpha002"} }},
		{name: "mismatched broker refs", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerPolicyRefs = []string{"bpr-alpha002"} }},
		{name: "invalid policy id", mutate: func(policy *ResolvedHostPolicy) { policy.PolicyID = "policy.alpha" }},
		{name: "invalid policy version", mutate: func(policy *ResolvedHostPolicy) { policy.PolicyVersion = " v1" }},
		{name: "invalid policy hash", mutate: func(policy *ResolvedHostPolicy) { policy.PolicySHA256 = strings.Repeat("A", 64) }},
		{name: "undeclared capability", mutate: func(policy *ResolvedHostPolicy) {
			policy.AllowedCapabilities = []Capability{CapabilityParseWASM, Capability("cap.unlisted")}
		}},
		{name: "invalid ingress domains", mutate: func(policy *ResolvedHostPolicy) { policy.IngressDomains = []DomainRule{{Host: "*.alpha.test"}} }},
		{name: "invalid broker domains", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerDomains = []DomainRule{{Host: "api.alpha.test:443"}} }},
		{name: "invalid output domain", mutate: func(policy *ResolvedHostPolicy) { policy.OutputDomains[0].Host = "files.alpha.test/path" }},
		{name: "invalid output path prefix", mutate: func(policy *ResolvedHostPolicy) { policy.OutputDomains[0].PathPrefixes = []string{"downloads"} }},
		{name: "invalid auth profile", mutate: func(policy *ResolvedHostPolicy) { policy.AuthProfiles[0].ProfileID = "Invalid" }},
		{name: "invalid auth profile domain", mutate: func(policy *ResolvedHostPolicy) {
			policy.AuthProfiles[0].Domains = []DomainRule{{Host: "api.alpha.test/path"}}
		}},
		{name: "missing broker domains", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerDomains = nil }},
		{name: "missing broker endpoints", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerEndpoints = nil }},
		{name: "malformed endpoint ref", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerEndpoints[0].EndpointRef = "endpoint.alpha" }},
		{name: "duplicate endpoint", mutate: func(policy *ResolvedHostPolicy) {
			policy.BrokerEndpoints = append(policy.BrokerEndpoints, policy.BrokerEndpoints[0])
		}},
		{name: "undeclared endpoint broker ref", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerEndpoints[0].BrokerPolicyRef = "bpr-alpha002" }},
		{name: "invalid endpoint template", mutate: func(policy *ResolvedHostPolicy) {
			policy.BrokerEndpoints[0].URLTemplate = "https://user:pass@api.alpha.test/resource/{id}"
		}},
		{name: "invalid endpoint auth profile ref", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerEndpoints[0].AuthProfileRefs = []string{"Invalid"} }},
		{name: "undeclared endpoint auth profile ref", mutate: func(policy *ResolvedHostPolicy) { policy.BrokerEndpoints[0].AuthProfileRefs = []string{"apr-alpha002"} }},
		{name: "duplicate endpoint auth profile ref", mutate: func(policy *ResolvedHostPolicy) {
			policy.BrokerEndpoints[0].AuthProfileRefs = []string{"apr-alpha001", "apr-alpha001"}
		}},
		{name: "duplicate endpoint method", mutate: func(policy *ResolvedHostPolicy) {
			policy.BrokerEndpoints[0].Methods = []string{"GET", "GET"}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := cloneResolvedHostPolicy(basePolicy)
			tt.mutate(&policy)

			if _, err := resolveAliasHostPolicy(context.Background(), &fakeHostPolicyResolver{policy: policy}, identity, manifest); err == nil {
				t.Fatal("resolveAliasHostPolicy() error = nil, want error")
			}
		})
	}
}

func TestHostPolicyBrokerEndpointExpansion(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	policy := validResolvedHostPolicy(identity, manifest)
	endpoint, ok := findBrokerEndpoint(policy, "bpr-alpha001", "epr-alpha001")
	if !ok {
		t.Fatal("findBrokerEndpoint() ok = false")
	}

	expanded, err := expandBrokerEndpointURL(policy, endpoint, map[string]string{"id": "item 001"})
	if err != nil {
		t.Fatalf("expandBrokerEndpointURL() error = %v", err)
	}
	if expanded != "https://api.alpha.test/resource/item%20001" {
		t.Fatalf("expanded URL = %q, want escaped synthetic URL", expanded)
	}
	if !endpointAllowsAuthProfile(endpoint, "apr-alpha001") || endpointAllowsAuthProfile(endpoint, "apr-alpha002") {
		t.Fatalf("endpoint auth scope mismatch: %#v", endpoint.AuthProfileRefs)
	}
	if !policyAuthProfileMatchesHost(policy, "apr-alpha001", "api.alpha.test") || policyAuthProfileMatchesHost(policy, "apr-alpha001", "files.alpha.test") {
		t.Fatalf("policy auth scope mismatch: %#v", policy.AuthProfiles)
	}
	if err := policyAllowsOutputURL(policy, "https://files.alpha.test/downloads/item.bin"); err != nil {
		t.Fatalf("policyAllowsOutputURL() error = %v", err)
	}
	if err := policyAllowsOutputURL(policy, "https://files.alpha.test/private/item.bin"); err == nil {
		t.Fatal("policyAllowsOutputURL() error = nil for disallowed output")
	}
	if !policyAllowsCapability(policy, CapabilityHTTPFetch) || policyAllowsCapability(policy, Capability("cap.missing")) {
		t.Fatalf("policyAllowsCapability mismatch: %#v", policy.AllowedCapabilities)
	}

	for _, tt := range []struct {
		name   string
		params map[string]string
	}{
		{name: "missing param", params: map[string]string{}},
		{name: "extra param", params: map[string]string{"id": "item", "extra": "value"}},
		{name: "secret-shaped key", params: map[string]string{"token": "value"}},
		{name: "url-like value", params: map[string]string{"id": "https://api.alpha.test/value"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := expandBrokerEndpointURL(policy, endpoint, tt.params); err == nil {
				t.Fatal("expandBrokerEndpointURL() error = nil, want error")
			}
		})
	}

	badPolicy := cloneResolvedHostPolicy(policy)
	badPolicy.BrokerDomains = []DomainRule{{Host: "cdn.alpha.test"}}
	if _, err := expandBrokerEndpointURL(badPolicy, endpoint, map[string]string{"id": "item"}); err == nil {
		t.Fatal("expandBrokerEndpointURL() error = nil for broker-domain mismatch")
	}
}

func TestHostPolicyBrokerEndpointExpansionRejectsStructuralParamInjection(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	policy := validResolvedHostPolicy(identity, manifest)
	endpoint, ok := findBrokerEndpoint(policy, "bpr-alpha001", "epr-alpha001")
	if !ok {
		t.Fatal("findBrokerEndpoint() ok = false")
	}

	for _, tt := range []struct {
		name  string
		value string
	}{
		{name: "dot segment", value: "."},
		{name: "dot dot segment", value: ".."},
		{name: "slash", value: "item/child"},
		{name: "backslash", value: `item\child`},
		{name: "reserved delimiter", value: "item?next=value"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := expandBrokerEndpointURL(policy, endpoint, map[string]string{"id": tt.value}); err == nil {
				t.Fatal("expandBrokerEndpointURL() error = nil, want structural param denial")
			}
		})
	}
}

func TestHostPolicyBrokerEndpointExpansionAllowsSafePathAndQueryParams(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	policy := validResolvedHostPolicy(identity, manifest)
	endpoint := HostPolicyBrokerEndpoint{
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "epr-alpha001",
		URLTemplate:     "https://api.alpha.test/resource/{id}?filter={filter}&fixed=alpha",
	}

	expanded, err := expandBrokerEndpointURL(policy, endpoint, map[string]string{
		"id":     "item-001",
		"filter": "type alpha",
	})
	if err != nil {
		t.Fatalf("expandBrokerEndpointURL() error = %v", err)
	}
	if expanded != "https://api.alpha.test/resource/item-001?filter=type+alpha&fixed=alpha" {
		t.Fatalf("expanded URL = %q, want safe path/query expansion", expanded)
	}
}

func TestHostPolicyBrokerEndpointExpansionRejectsQueryDelimiterInjection(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	policy := validResolvedHostPolicy(identity, manifest)
	endpoint := HostPolicyBrokerEndpoint{
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "epr-alpha001",
		URLTemplate:     "https://api.alpha.test/resource/{id}?filter={filter}",
	}
	expanded, err := expandBrokerEndpointURL(policy, endpoint, map[string]string{
		"id":     "item-001",
		"filter": "alpha&extra=beta",
	})
	if err != nil {
		t.Fatalf("expandBrokerEndpointURL() error = %v", err)
	}
	if expanded != "https://api.alpha.test/resource/item-001?filter=alpha%26extra%3Dbeta" {
		t.Fatalf("expanded URL = %q, want escaped query delimiter value", expanded)
	}
	parsed, err := url.Parse(expanded)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Query().Get("extra") != "" || parsed.Query().Get("filter") != "alpha&extra=beta" {
		t.Fatalf("query injection was not confined: raw_query=%q parsed=%#v", parsed.RawQuery, parsed.Query())
	}
}

func TestHostPolicyBrokerEndpointValidationRejectsUnsafePlaceholderLocations(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	manifest.Capabilities = append(manifest.Capabilities, CapabilityAuthProfile)
	identity := syntheticVerifiedPackIdentity(manifest)
	basePolicy := validResolvedHostPolicy(identity, manifest)

	for _, tt := range []struct {
		name     string
		template string
	}{
		{name: "scheme", template: "{scheme}://api.alpha.test/resource"},
		{name: "host", template: "https://{host}.alpha.test/resource"},
		{name: "fragment", template: "https://api.alpha.test/resource/{id}#{frag}"},
		{name: "partial path segment", template: "https://api.alpha.test/resource/id-{id}"},
		{name: "partial query value", template: "https://api.alpha.test/resource/{id}?filter=a-{filter}"},
		{name: "query key", template: "https://api.alpha.test/resource/{id}?{key}=value"},
		{name: "token query key", template: "https://api.alpha.test/resource/{id}?token={filter}"},
		{name: "signature query key", template: "https://api.alpha.test/resource/{id}?signature={filter}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := cloneResolvedHostPolicy(basePolicy)
			policy.BrokerEndpoints[0].URLTemplate = tt.template
			if _, err := resolveAliasHostPolicy(context.Background(), &fakeHostPolicyResolver{policy: policy}, identity, manifest); err == nil {
				t.Fatal("resolveAliasHostPolicy() error = nil, want invalid template denial")
			}
		})
	}
}

func TestHostPolicyResolverFailsClosedForResolverErrors(t *testing.T) {
	manifest := validAliasTestManifest(nil)
	identity := syntheticVerifiedPackIdentity(manifest)

	if _, err := resolveAliasHostPolicy(context.Background(), nil, identity, manifest); err == nil {
		t.Fatal("resolveAliasHostPolicy() nil resolver error = nil")
	}
	_, err := resolveAliasHostPolicy(context.Background(), &fakeHostPolicyResolver{err: errors.New("private-route-secret")}, identity, manifest)
	if err == nil {
		t.Fatal("resolveAliasHostPolicy() resolver error = nil")
	}
	if strings.Contains(err.Error(), "private-route-secret") {
		t.Fatalf("resolver error leaked private details: %q", err.Error())
	}

	legacy := validTestManifest()
	if _, err := resolveAliasHostPolicy(context.Background(), &fakeHostPolicyResolver{}, syntheticVerifiedPackIdentity(legacy), legacy); err == nil {
		t.Fatal("resolveAliasHostPolicy() legacy manifest error = nil")
	}
}

func validResolvedHostPolicy(identity VerifiedPackIdentity, manifest Manifest) ResolvedHostPolicy {
	capabilities := append([]Capability(nil), manifest.Capabilities...)
	brokerRefs := cloneStringSlice(manifest.BrokerPolicyRefs)
	if len(brokerRefs) == 0 {
		brokerRefs = []string{"bpr-alpha001"}
		manifest.BrokerPolicyRefs = cloneStringSlice(brokerRefs)
	}
	domainRefs := cloneStringSlice(manifest.DomainPolicyRefs)
	if len(domainRefs) == 0 {
		domainRefs = []string{"dpr-alpha001"}
		manifest.DomainPolicyRefs = cloneStringSlice(domainRefs)
	}
	var authProfiles []HostPolicyAuthProfileScope
	var endpointAuthRefs []string
	if manifestHasCapability(manifest, CapabilityAuthProfile) {
		authProfiles = []HostPolicyAuthProfileScope{{ProfileID: "apr-alpha001", Domains: []DomainRule{{Host: "api.alpha.test"}}}}
		endpointAuthRefs = []string{"apr-alpha001"}
	}
	return ResolvedHostPolicy{
		PolicyID:            "pol-alpha001",
		PolicyVersion:       "2026.05.11-alpha",
		PolicySHA256:        strings.Repeat("c", 64),
		PackIdentity:        identity,
		DomainPolicyRefs:    domainRefs,
		BrokerPolicyRefs:    brokerRefs,
		AllowedCapabilities: capabilities,
		IngressDomains: []DomainRule{
			{Host: "share.alpha.test"},
			{Host: "files.alpha.test", IncludeSubdomains: true},
		},
		BrokerDomains: []DomainRule{{Host: "api.alpha.test"}},
		OutputDomains: []HostPolicyOutputRule{{Host: "files.alpha.test", IncludeSubdomains: true, PathPrefixes: []string{"/downloads/"}}},
		AuthProfiles:  authProfiles,
		BrokerEndpoints: []HostPolicyBrokerEndpoint{{
			BrokerPolicyRef:  "bpr-alpha001",
			EndpointRef:      "epr-alpha001",
			URLTemplate:      "https://api.alpha.test/resource/{id}",
			Methods:          []string{"GET", "HEAD"},
			AuthProfileRefs:  endpointAuthRefs,
			TimeoutMillis:    100,
			MaxResponseBytes: 512,
		}},
	}
}

func syntheticVerifiedPackIdentity(manifest Manifest) VerifiedPackIdentity {
	return VerifiedPackIdentity{
		PackID:          manifest.PackID,
		PackVersion:     manifest.PackVersion,
		AssetSHA256:     strings.Repeat("d", 64),
		ManifestSHA256:  strings.Repeat("e", 64),
		PayloadSHA256:   manifest.PayloadSHA256,
		SignatureSHA256: strings.Repeat("f", 64),
		PublicKeySHA256: strings.Repeat("0", 64),
	}
}
