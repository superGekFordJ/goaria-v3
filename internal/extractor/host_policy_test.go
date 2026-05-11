package extractor

import (
	"context"
	"errors"
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
	fresh, err := resolveAliasHostPolicy(context.Background(), resolver, identity, manifest)
	if err != nil {
		t.Fatalf("resolveAliasHostPolicy() fresh error = %v", err)
	}
	if fresh.DomainPolicyRefs[0] != "dpr-alpha001" || fresh.BrokerPolicyRefs[0] != "bpr-alpha001" || fresh.IngressDomains[0].Host != "share.alpha.test" {
		t.Fatalf("resolved host policy was not defensively copied: %#v", fresh)
	}
}

func TestHostPolicyResolverRejectsInvalidPolicies(t *testing.T) {
	manifest := validAliasTestManifest(nil)
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
	return ResolvedHostPolicy{
		PolicyID:            "pol-alpha001",
		PolicyVersion:       "2026.05.11-alpha",
		PolicySHA256:        strings.Repeat("c", 64),
		PackIdentity:        identity,
		DomainPolicyRefs:    cloneStringSlice(manifest.DomainPolicyRefs),
		BrokerPolicyRefs:    cloneStringSlice(manifest.BrokerPolicyRefs),
		AllowedCapabilities: append([]Capability(nil), manifest.Capabilities...),
		IngressDomains: []DomainRule{
			{Host: "share.alpha.test"},
			{Host: "files.alpha.test", IncludeSubdomains: true},
		},
		BrokerDomains: []DomainRule{{Host: "api.alpha.test"}},
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
