package extractor

import (
	"context"
	"errors"
	"strings"
)

type HostPolicyResolver interface {
	ResolveHostPolicy(ctx context.Context, request HostPolicyRequest) (ResolvedHostPolicy, error)
}

type HostPolicyRequest struct {
	PackIdentity VerifiedPackIdentity
	Manifest     Manifest
}

type ResolvedHostPolicy struct {
	PolicyID            string
	PolicyVersion       string
	PolicySHA256        string
	PackIdentity        VerifiedPackIdentity
	DomainPolicyRefs    []string
	BrokerPolicyRefs    []string
	AllowedCapabilities []Capability
	IngressDomains      []DomainRule
	BrokerDomains       []DomainRule
}

func isAliasManifest(manifest Manifest) bool {
	return len(manifest.DomainPolicyRefs) > 0 && len(manifest.Domains) == 0
}

func resolveAliasHostPolicy(ctx context.Context, resolver HostPolicyResolver, identity VerifiedPackIdentity, manifest Manifest) (ResolvedHostPolicy, error) {
	if !isAliasManifest(manifest) {
		return ResolvedHostPolicy{}, errors.New("host policy requires alias manifest")
	}
	if resolver == nil {
		return ResolvedHostPolicy{}, errors.New("host policy resolver is not configured")
	}
	if !hasVerifiedPackIdentity(identity) {
		return ResolvedHostPolicy{}, errors.New("verified pack identity is incomplete")
	}

	request := HostPolicyRequest{
		PackIdentity: identity,
		Manifest:     cloneManifest(manifest),
	}
	policy, err := resolver.ResolveHostPolicy(ctx, request)
	if err != nil {
		return ResolvedHostPolicy{}, errors.New("host policy resolver failed")
	}
	if err := validateResolvedHostPolicy(identity, manifest, policy); err != nil {
		return ResolvedHostPolicy{}, err
	}

	return cloneResolvedHostPolicy(policy), nil
}

func validateResolvedHostPolicy(identity VerifiedPackIdentity, manifest Manifest, policy ResolvedHostPolicy) error {
	if err := validateOpaquePolicyRef("policy_id", policy.PolicyID); err != nil {
		return err
	}
	if policy.PolicyVersion == "" || strings.TrimSpace(policy.PolicyVersion) != policy.PolicyVersion {
		return errors.New("host policy version must be non-empty and trimmed")
	}
	if err := validateSHA256Hex("policy_sha256", policy.PolicySHA256); err != nil {
		return err
	}
	if !hasVerifiedPackIdentity(identity) || policy.PackIdentity != identity {
		return errors.New("host policy identity does not match verified pack")
	}
	if identity.PackID != manifest.PackID || identity.PackVersion != manifest.PackVersion {
		return errors.New("verified pack identity does not match manifest")
	}
	if !samePolicyRefSet(manifest.DomainPolicyRefs, policy.DomainPolicyRefs) {
		return errors.New("host policy domain refs do not match manifest")
	}
	if !samePolicyRefSet(manifest.BrokerPolicyRefs, policy.BrokerPolicyRefs) {
		return errors.New("host policy broker refs do not match manifest")
	}
	if err := validateCapabilities(policy.AllowedCapabilities, capabilitiesAllowedByManifest(manifest)); err != nil {
		return errors.New("host policy capabilities are not allowed by manifest")
	}
	if err := validateDomainRules(policy.IngressDomains); err != nil {
		return errors.New("host policy ingress domains are invalid")
	}
	if len(policy.BrokerDomains) > 0 {
		if err := validateDomainRules(policy.BrokerDomains); err != nil {
			return errors.New("host policy broker domains are invalid")
		}
	}

	return nil
}

func policyIngressMatchesHost(policy ResolvedHostPolicy, host string) bool {
	for _, rule := range policy.IngressDomains {
		if matchesDomainRule(host, rule) {
			return true
		}
	}

	return false
}

func hasVerifiedPackIdentity(identity VerifiedPackIdentity) bool {
	return identity.PackID != "" &&
		identity.PackVersion != "" &&
		identity.ManifestSHA256 != "" &&
		identity.PayloadSHA256 != "" &&
		identity.SignatureSHA256 != "" &&
		identity.PublicKeySHA256 != ""
}

func samePolicyRefSet(manifestRefs []string, policyRefs []string) bool {
	if len(manifestRefs) != len(policyRefs) {
		return false
	}
	if err := validateOpaquePolicyRefs("domain_policy_refs", policyRefs); err != nil {
		return false
	}

	seen := make(map[string]struct{}, len(manifestRefs))
	for _, ref := range manifestRefs {
		seen[ref] = struct{}{}
	}
	for _, ref := range policyRefs {
		if _, ok := seen[ref]; !ok {
			return false
		}
	}

	return true
}

func capabilitiesAllowedByManifest(manifest Manifest) map[Capability]struct{} {
	allowed := make(map[Capability]struct{}, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		allowed[capability] = struct{}{}
	}

	return allowed
}

func cloneResolvedHostPolicy(policy ResolvedHostPolicy) ResolvedHostPolicy {
	policy.DomainPolicyRefs = cloneStringSlice(policy.DomainPolicyRefs)
	policy.BrokerPolicyRefs = cloneStringSlice(policy.BrokerPolicyRefs)
	policy.AllowedCapabilities = append([]Capability(nil), policy.AllowedCapabilities...)
	policy.IngressDomains = cloneDomainRules(policy.IngressDomains)
	policy.BrokerDomains = cloneDomainRules(policy.BrokerDomains)

	return policy
}
