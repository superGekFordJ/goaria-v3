package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

const (
	privatePolicyBundlePathEnv   = "GOARIA_EXTRACTOR_PRIVATE_POLICY_BUNDLE"
	privatePolicyBundleSHA256Env = "GOARIA_EXTRACTOR_PRIVATE_POLICY_SHA256"
)

type RuntimeSourceState string

const (
	RuntimeSourceStateNone      RuntimeSourceState = "none"
	RuntimeSourceStateEnv       RuntimeSourceState = "env"
	RuntimeSourceStateEmbedded  RuntimeSourceState = "embedded"
	RuntimeSourceStateAmbiguous RuntimeSourceState = "ambiguous"
)

var (
	embeddedPrivatePolicyBundleJSON              []byte
	embeddedPrivatePolicyBundleSHA256            string
	embeddedPrivatePolicyBundlePublicFingerprint string
)

type PrivatePolicyBundleLoadOptions struct {
	ExpectedPolicyPrivateSHA256     string
	ExpectedPolicyPublicFingerprint string
}

type privatePolicyBundleEnvelope struct {
	SchemaVersion           int             `json:"schema_version"`
	BundleID                string          `json:"bundle_id"`
	BundleVersion           string          `json:"bundle_version"`
	PolicyPrivateSHA256     string          `json:"policy_private_sha256"`
	PolicyPublicFingerprint string          `json:"policy_public_fingerprint"`
	Policy                  json.RawMessage `json:"policy"`
}

type privatePolicyBundlePolicy struct {
	Packs []privatePolicyBundlePack `json:"packs"`
}

type privatePolicyBundlePack struct {
	VerifiedPackIdentity privatePolicyVerifiedPackIdentity `json:"verified_pack_identity"`
	DomainPolicyRefs     []string                          `json:"domain_policy_refs"`
	BrokerPolicyRefs     []string                          `json:"broker_policy_refs"`
	AllowedCapabilities  []Capability                      `json:"allowed_capabilities"`
	IngressDomainRules   []DomainRule                      `json:"ingress_domain_rules"`
	BrokerDomainRules    []DomainRule                      `json:"broker_domain_rules"`
	OutputDomainRules    []privatePolicyOutputRule         `json:"output_domain_rules"`
	AuthProfileScopes    []privatePolicyAuthProfileScope   `json:"auth_profile_scopes"`
	Endpoints            []privatePolicyEndpoint           `json:"endpoints"`
}

type privatePolicyVerifiedPackIdentity struct {
	PackID          string `json:"pack_id"`
	PackVersion     string `json:"pack_version"`
	AssetSHA256     string `json:"asset_sha256"`
	ManifestSHA256  string `json:"manifest_sha256"`
	PayloadSHA256   string `json:"payload_sha256"`
	SignatureSHA256 string `json:"signature_sha256"`
	PublicKeySHA256 string `json:"public_key_sha256"`
}

type privatePolicyOutputRule struct {
	Host              string   `json:"host"`
	IncludeSubdomains bool     `json:"include_subdomains"`
	PathPrefixes      []string `json:"path_prefixes"`
}

type privatePolicyAuthProfileScope struct {
	ProfileID   string       `json:"profile_id"`
	DomainRules []DomainRule `json:"domain_rules"`
}

type privatePolicyEndpoint struct {
	BrokerPolicyRef  string   `json:"broker_policy_ref"`
	EndpointRef      string   `json:"endpoint_ref"`
	URLTemplate      string   `json:"url_template"`
	Methods          []string `json:"methods"`
	AuthProfileRefs  []string `json:"auth_profile_refs"`
	TimeoutMillis    int      `json:"timeout_millis"`
	MaxResponseBytes int64    `json:"max_response_bytes"`
}

type privatePolicyBundleResolver struct {
	policiesByIdentity map[VerifiedPackIdentity]ResolvedHostPolicy
}

func NewPrivatePolicyBundleResolver(raw []byte, opts PrivatePolicyBundleLoadOptions) (HostPolicyResolver, error) {
	resolver, err := parsePrivatePolicyBundleResolver(cloneBytes(raw), opts)
	if err != nil {
		return nil, privatePolicyBundleInvalidError()
	}

	return resolver, nil
}

func LoadPrivatePolicyBundleResolverFromFile(path string, opts PrivatePolicyBundleLoadOptions) (HostPolicyResolver, error) {
	if path == "" {
		return nil, privatePolicyBundleInvalidError()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, privatePolicyBundleInvalidError()
	}

	return NewPrivatePolicyBundleResolver(raw, opts)
}

func PrivatePolicyBundleRuntimeSourceState() RuntimeSourceState {
	return classifyRuntimeSourceState(os.Getenv(privatePolicyBundlePathEnv) != "", len(embeddedPrivatePolicyBundleJSON) > 0)
}

func LoadPrivatePolicyBundleResolverFromRuntimeSources() (HostPolicyResolver, error) {
	path := os.Getenv(privatePolicyBundlePathEnv)
	expectedSHA256 := os.Getenv(privatePolicyBundleSHA256Env)
	sourceState := PrivatePolicyBundleRuntimeSourceState()

	if sourceState == RuntimeSourceStateAmbiguous {
		return nil, privatePolicyRuntimeSourceInvalidError()
	}
	if sourceState == RuntimeSourceStateNone {
		return nil, nil
	}
	if sourceState == RuntimeSourceStateEnv {
		resolver, err := LoadPrivatePolicyBundleResolverFromFile(path, PrivatePolicyBundleLoadOptions{
			ExpectedPolicyPrivateSHA256: expectedSHA256,
		})
		if err != nil {
			return nil, privatePolicyRuntimeSourceInvalidError()
		}

		return resolver, nil
	}

	resolver, err := NewPrivatePolicyBundleResolver(embeddedPrivatePolicyBundleJSON, PrivatePolicyBundleLoadOptions{
		ExpectedPolicyPrivateSHA256:     embeddedPrivatePolicyBundleSHA256,
		ExpectedPolicyPublicFingerprint: embeddedPrivatePolicyBundlePublicFingerprint,
	})
	if err != nil {
		return nil, privatePolicyRuntimeSourceInvalidError()
	}

	return resolver, nil
}

func classifyRuntimeSourceState(hasEnvSource bool, hasEmbeddedSource bool) RuntimeSourceState {
	switch {
	case hasEnvSource && hasEmbeddedSource:
		return RuntimeSourceStateAmbiguous
	case hasEnvSource:
		return RuntimeSourceStateEnv
	case hasEmbeddedSource:
		return RuntimeSourceStateEmbedded
	default:
		return RuntimeSourceStateNone
	}
}

func (r *privatePolicyBundleResolver) ResolveHostPolicy(ctx context.Context, request HostPolicyRequest) (ResolvedHostPolicy, error) {
	if r == nil || len(r.policiesByIdentity) == 0 {
		return ResolvedHostPolicy{}, hostPolicyResolutionDeniedError()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ResolvedHostPolicy{}, hostPolicyResolutionDeniedError()
	default:
	}
	if request.PackIdentity == (VerifiedPackIdentity{}) || !isAliasManifest(request.Manifest) {
		return ResolvedHostPolicy{}, hostPolicyResolutionDeniedError()
	}

	policy, ok := r.policiesByIdentity[request.PackIdentity]
	if !ok {
		return ResolvedHostPolicy{}, hostPolicyResolutionDeniedError()
	}
	if err := validateResolvedHostPolicy(request.PackIdentity, request.Manifest, policy); err != nil {
		return ResolvedHostPolicy{}, hostPolicyResolutionDeniedError()
	}
	if err := validatePrivatePolicyEndpointResourcesForManifest(policy, request.Manifest); err != nil {
		return ResolvedHostPolicy{}, hostPolicyResolutionDeniedError()
	}

	return cloneResolvedHostPolicy(policy), nil
}

func parsePrivatePolicyBundleResolver(raw []byte, opts PrivatePolicyBundleLoadOptions) (*privatePolicyBundleResolver, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty private policy bundle")
	}
	if err := validateOptionalPrivatePolicyExpectedHashes(opts); err != nil {
		return nil, err
	}

	var envelope privatePolicyBundleEnvelope
	if err := decodeStrictJSON(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.SchemaVersion != 1 {
		return nil, errors.New("unsupported private policy bundle schema")
	}
	if err := validateOpaquePolicyRef("bundle_id", envelope.BundleID); err != nil {
		return nil, err
	}
	if err := validatePrivatePolicyBundleVersion(envelope.BundleVersion); err != nil {
		return nil, err
	}
	if err := validateLowerHexSHA256Field("policy_private_sha256", envelope.PolicyPrivateSHA256); err != nil {
		return nil, err
	}
	if len(envelope.Policy) == 0 {
		return nil, errors.New("private policy bundle policy is required")
	}
	if sha256HexString(envelope.Policy) != envelope.PolicyPrivateSHA256 {
		return nil, errors.New("private policy bundle hash mismatch")
	}
	if opts.ExpectedPolicyPrivateSHA256 != "" && opts.ExpectedPolicyPrivateSHA256 != envelope.PolicyPrivateSHA256 {
		return nil, errors.New("private policy bundle expected hash mismatch")
	}
	if err := validateLowerHexSHA256Field("policy_public_fingerprint", envelope.PolicyPublicFingerprint); err != nil {
		return nil, err
	}
	if opts.ExpectedPolicyPublicFingerprint != "" && opts.ExpectedPolicyPublicFingerprint != envelope.PolicyPublicFingerprint {
		return nil, errors.New("private policy bundle expected fingerprint mismatch")
	}

	var bundlePolicy privatePolicyBundlePolicy
	if err := decodeStrictJSON(envelope.Policy, &bundlePolicy); err != nil {
		return nil, err
	}
	if len(bundlePolicy.Packs) == 0 {
		return nil, errors.New("private policy bundle must contain packs")
	}

	policies := make(map[VerifiedPackIdentity]ResolvedHostPolicy, len(bundlePolicy.Packs))
	for _, entry := range bundlePolicy.Packs {
		resolved, err := entry.toResolvedHostPolicy(envelope.BundleID, envelope.BundleVersion, envelope.PolicyPrivateSHA256)
		if err != nil {
			return nil, err
		}
		if _, ok := policies[resolved.PackIdentity]; ok {
			return nil, errors.New("private policy bundle contains duplicate pack identity")
		}
		if err := validatePrivatePolicyBundleEntry(resolved); err != nil {
			return nil, err
		}
		policies[resolved.PackIdentity] = cloneResolvedHostPolicy(resolved)
	}

	return &privatePolicyBundleResolver{policiesByIdentity: policies}, nil
}

func validateOptionalPrivatePolicyExpectedHashes(opts PrivatePolicyBundleLoadOptions) error {
	if opts.ExpectedPolicyPrivateSHA256 != "" {
		if err := validateLowerHexSHA256Field("expected_policy_private_sha256", opts.ExpectedPolicyPrivateSHA256); err != nil {
			return err
		}
	}
	if opts.ExpectedPolicyPublicFingerprint != "" {
		if err := validateLowerHexSHA256Field("expected_policy_public_fingerprint", opts.ExpectedPolicyPublicFingerprint); err != nil {
			return err
		}
	}

	return nil
}

func (entry privatePolicyBundlePack) toResolvedHostPolicy(bundleID string, bundleVersion string, policySHA256 string) (ResolvedHostPolicy, error) {
	identity, err := entry.VerifiedPackIdentity.toVerifiedPackIdentity()
	if err != nil {
		return ResolvedHostPolicy{}, err
	}

	policy := ResolvedHostPolicy{
		PolicyID:            bundleID,
		PolicyVersion:       bundleVersion,
		PolicySHA256:        policySHA256,
		PackIdentity:        identity,
		DomainPolicyRefs:    cloneStringSlice(entry.DomainPolicyRefs),
		BrokerPolicyRefs:    cloneStringSlice(entry.BrokerPolicyRefs),
		AllowedCapabilities: append([]Capability(nil), entry.AllowedCapabilities...),
		IngressDomains:      cloneDomainRules(entry.IngressDomainRules),
		BrokerDomains:       cloneDomainRules(entry.BrokerDomainRules),
		OutputDomains:       convertPrivatePolicyOutputRules(entry.OutputDomainRules),
		AuthProfiles:        convertPrivatePolicyAuthProfileScopes(entry.AuthProfileScopes),
		Endpoints:           convertPrivatePolicyEndpoints(entry.Endpoints),
	}

	return policy, nil
}

func (identity privatePolicyVerifiedPackIdentity) toVerifiedPackIdentity() (VerifiedPackIdentity, error) {
	if err := validatePackID(identity.PackID); err != nil {
		return VerifiedPackIdentity{}, err
	}
	if err := validatePackVersion(identity.PackVersion); err != nil {
		return VerifiedPackIdentity{}, err
	}
	if err := validateLowerHexSHA256Field("asset_sha256", identity.AssetSHA256); err != nil {
		return VerifiedPackIdentity{}, err
	}
	if err := validateLowerHexSHA256Field("manifest_sha256", identity.ManifestSHA256); err != nil {
		return VerifiedPackIdentity{}, err
	}
	if err := validateLowerHexSHA256Field("payload_sha256", identity.PayloadSHA256); err != nil {
		return VerifiedPackIdentity{}, err
	}
	if err := validateLowerHexSHA256Field("signature_sha256", identity.SignatureSHA256); err != nil {
		return VerifiedPackIdentity{}, err
	}
	if err := validateLowerHexSHA256Field("public_key_sha256", identity.PublicKeySHA256); err != nil {
		return VerifiedPackIdentity{}, err
	}

	return VerifiedPackIdentity(identity), nil
}

func validatePrivatePolicyBundleEntry(policy ResolvedHostPolicy) error {
	manifest, err := privatePolicySyntheticManifest(policy)
	if err != nil {
		return err
	}
	if err := validateResolvedHostPolicy(policy.PackIdentity, manifest, policy); err != nil {
		return err
	}
	if err := validatePrivatePolicyEndpointResourcesForHost(policy); err != nil {
		return err
	}
	if err := validatePrivatePolicyEndpointAuthRefsDeclared(policy); err != nil {
		return err
	}

	return nil
}

func privatePolicySyntheticManifest(policy ResolvedHostPolicy) (Manifest, error) {
	manifest := Manifest{
		PackID:           policy.PackIdentity.PackID,
		PackVersion:      policy.PackIdentity.PackVersion,
		ABIVersion:       CurrentABIVersion,
		Capabilities:     append([]Capability(nil), policy.AllowedCapabilities...),
		Domains:          []DomainRule{},
		DomainPolicyRefs: cloneStringSlice(policy.DomainPolicyRefs),
		BrokerPolicyRefs: cloneStringSlice(policy.BrokerPolicyRefs),
		ResourceLimits:   DefaultTrustPolicy().MaxResourceLimits,
		PayloadSHA256:    policy.PackIdentity.PayloadSHA256,
	}
	if err := validateCapabilities(manifest.Capabilities, DefaultTrustPolicy().AllowedCapabilities); err != nil {
		return Manifest{}, err
	}
	if err := validateDomainPolicyMode(manifest); err != nil {
		return Manifest{}, err
	}

	return manifest, nil
}

func validatePrivatePolicyEndpointAuthRefsDeclared(policy ResolvedHostPolicy) error {
	declared := make(map[AuthProfileID]struct{}, len(policy.AuthProfiles))
	for _, scope := range policy.AuthProfiles {
		declared[scope.ProfileID] = struct{}{}
	}
	for _, endpoint := range policy.Endpoints {
		for _, ref := range endpoint.AuthProfileRefs {
			if !policyAllowsCapability(policy, CapabilityAuthProfile) {
				return errors.New("private policy endpoint auth refs require auth capability")
			}
			if _, ok := declared[ref]; !ok {
				return errors.New("private policy endpoint auth ref is not declared by scope")
			}
		}
	}

	return nil
}

func validatePrivatePolicyEndpointResourcesForManifest(policy ResolvedHostPolicy, manifest Manifest) error {
	brokerPolicy := DefaultHTTPBrokerPolicy()
	for _, endpoint := range policy.Endpoints {
		if err := validateHostPolicyEndpointResourceCaps(endpoint, manifest, brokerPolicy); err != nil {
			return err
		}
	}

	return nil
}

func validatePrivatePolicyEndpointResourcesForHost(policy ResolvedHostPolicy) error {
	brokerPolicy := DefaultHTTPBrokerPolicy()
	for _, endpoint := range policy.Endpoints {
		if err := validateHostPolicyEndpointResourceCaps(endpoint, privatePolicyHostMaxManifest(), brokerPolicy); err != nil {
			return err
		}
	}

	return nil
}

func privatePolicyHostMaxManifest() Manifest {
	return Manifest{ResourceLimits: DefaultTrustPolicy().MaxResourceLimits}
}

func validatePrivatePolicyBundleVersion(version string) error {
	if version == "" || strings.TrimSpace(version) != version {
		return errors.New("private policy bundle version must be non-empty and trimmed")
	}
	for _, r := range version {
		if r == '/' || r == '\\' || r == 0 || isASCIIWhitespace(r) || r < 0x20 || r == 0x7f {
			return errors.New("private policy bundle version contains unsafe characters")
		}
	}

	return nil
}

func convertPrivatePolicyOutputRules(rules []privatePolicyOutputRule) []HostPolicyOutputRule {
	if rules == nil {
		return nil
	}
	converted := make([]HostPolicyOutputRule, len(rules))
	for i, rule := range rules {
		converted[i] = HostPolicyOutputRule{
			Host:              rule.Host,
			IncludeSubdomains: rule.IncludeSubdomains,
			PathPrefixes:      cloneStringSlice(rule.PathPrefixes),
		}
	}

	return converted
}

func convertPrivatePolicyAuthProfileScopes(scopes []privatePolicyAuthProfileScope) []HostPolicyAuthProfileScope {
	if scopes == nil {
		return nil
	}
	converted := make([]HostPolicyAuthProfileScope, len(scopes))
	for i, scope := range scopes {
		converted[i] = HostPolicyAuthProfileScope{
			ProfileID: AuthProfileID(scope.ProfileID),
			Domains:   cloneDomainRules(scope.DomainRules),
		}
	}

	return converted
}

func convertPrivatePolicyEndpoints(endpoints []privatePolicyEndpoint) []HostPolicyEndpoint {
	if endpoints == nil {
		return nil
	}
	converted := make([]HostPolicyEndpoint, len(endpoints))
	for i, endpoint := range endpoints {
		converted[i] = HostPolicyEndpoint{
			BrokerPolicyRef:  endpoint.BrokerPolicyRef,
			EndpointRef:      endpoint.EndpointRef,
			URLTemplate:      endpoint.URLTemplate,
			Methods:          cloneStringSlice(endpoint.Methods),
			AuthProfileRefs:  convertAuthProfileIDs(endpoint.AuthProfileRefs),
			TimeoutMillis:    endpoint.TimeoutMillis,
			MaxResponseBytes: endpoint.MaxResponseBytes,
		}
	}

	return converted
}

func convertAuthProfileIDs(ids []string) []AuthProfileID {
	if ids == nil {
		return nil
	}
	converted := make([]AuthProfileID, len(ids))
	for i, id := range ids {
		converted[i] = AuthProfileID(id)
	}

	return converted
}

func privatePolicyBundleInvalidError() error {
	return errors.New("private host policy bundle is invalid")
}

func privatePolicyRuntimeSourceInvalidError() error {
	return errors.New("private host policy runtime source is invalid")
}

func hostPolicyResolutionDeniedError() error {
	return errors.New("host policy resolution denied")
}
