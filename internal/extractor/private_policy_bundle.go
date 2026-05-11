package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"unicode"
)

const (
	privatePolicyBundleInvalidError      = "private host policy bundle is invalid"
	privateHostPolicyResolutionDenied    = "host policy resolution denied"
	privatePolicyBundlePathEnv           = "GOARIA_EXTRACTOR_PRIVATE_POLICY_BUNDLE"
	privatePolicyBundleExpectedSHA256Env = "GOARIA_EXTRACTOR_PRIVATE_POLICY_SHA256"
)

var (
	embeddedPrivatePolicyBundleJSON   []byte
	embeddedPrivatePolicyBundleSHA256 string
)

type PrivatePolicyBundleLoadOptions struct {
	ExpectedPolicyPrivateSHA256     string
	ExpectedPolicyPublicFingerprint string
}

type privatePolicyBundleEnvelopeDTO struct {
	SchemaVersion           int             `json:"schema_version"`
	BundleID                string          `json:"bundle_id"`
	BundleVersion           string          `json:"bundle_version"`
	PolicyPrivateSHA256     string          `json:"policy_private_sha256"`
	PolicyPublicFingerprint string          `json:"policy_public_fingerprint,omitempty"`
	Policy                  json.RawMessage `json:"policy"`
}

type privatePolicyBundlePolicyDTO struct {
	Packs []privatePolicyBundlePackDTO `json:"packs"`
}

type privatePolicyBundlePackDTO struct {
	VerifiedPackIdentity privatePolicyBundleIdentityDTO         `json:"verified_pack_identity"`
	DomainPolicyRefs     []string                               `json:"domain_policy_refs"`
	BrokerPolicyRefs     []string                               `json:"broker_policy_refs"`
	AllowedCapabilities  []Capability                           `json:"allowed_capabilities"`
	IngressDomainRules   []DomainRule                           `json:"ingress_domain_rules"`
	BrokerDomainRules    []DomainRule                           `json:"broker_domain_rules"`
	BrokerEndpoints      []privatePolicyBundleBrokerEndpointDTO `json:"broker_endpoints"`
}

type privatePolicyBundleIdentityDTO struct {
	PackID          string `json:"pack_id"`
	PackVersion     string `json:"pack_version"`
	AssetSHA256     string `json:"asset_sha256"`
	ManifestSHA256  string `json:"manifest_sha256"`
	PayloadSHA256   string `json:"payload_sha256"`
	SignatureSHA256 string `json:"signature_sha256"`
	PublicKeySHA256 string `json:"public_key_sha256"`
}

type privatePolicyBundleBrokerEndpointDTO struct {
	BrokerPolicyRef string   `json:"broker_policy_ref"`
	EndpointRef     string   `json:"endpoint_ref"`
	URLTemplate     string   `json:"url_template"`
	AuthProfileRefs []string `json:"auth_profile_refs"`
}

type privatePolicyBundleResolver struct {
	policies map[VerifiedPackIdentity]ResolvedHostPolicy
}

func NewPrivatePolicyBundleResolver(raw []byte, opts PrivatePolicyBundleLoadOptions) (HostPolicyResolver, error) {
	resolver, err := newPrivatePolicyBundleResolver(raw, opts)
	if err != nil {
		return nil, errors.New(privatePolicyBundleInvalidError)
	}

	return resolver, nil
}

func LoadPrivatePolicyBundleResolverFromFile(path string, opts PrivatePolicyBundleLoadOptions) (HostPolicyResolver, error) {
	if path == "" {
		return nil, errors.New(privatePolicyBundleInvalidError)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New(privatePolicyBundleInvalidError)
	}

	return NewPrivatePolicyBundleResolver(raw, opts)
}

func LoadPrivatePolicyBundleResolverFromRuntimeSources() (HostPolicyResolver, error) {
	envPath := os.Getenv(privatePolicyBundlePathEnv)
	envExpectedSHA256 := os.Getenv(privatePolicyBundleExpectedSHA256Env)
	hasEnvSource := envPath != ""
	hasEmbeddedSource := len(embeddedPrivatePolicyBundleJSON) > 0

	switch {
	case !hasEnvSource && !hasEmbeddedSource:
		return nil, nil
	case hasEnvSource && hasEmbeddedSource:
		return nil, errors.New(privatePolicyBundleInvalidError)
	case hasEnvSource:
		return LoadPrivatePolicyBundleResolverFromFile(envPath, PrivatePolicyBundleLoadOptions{
			ExpectedPolicyPrivateSHA256: envExpectedSHA256,
		})
	default:
		return NewPrivatePolicyBundleResolver(embeddedPrivatePolicyBundleJSON, PrivatePolicyBundleLoadOptions{
			ExpectedPolicyPrivateSHA256: embeddedPrivatePolicyBundleSHA256,
		})
	}
}

func newPrivatePolicyBundleResolver(raw []byte, opts PrivatePolicyBundleLoadOptions) (*privatePolicyBundleResolver, error) {
	raw = cloneBytes(raw)
	if len(raw) == 0 {
		return nil, errors.New("empty bundle")
	}

	var envelope privatePolicyBundleEnvelopeDTO
	if err := decodePrivatePolicyBundleJSON(raw, &envelope); err != nil {
		return nil, err
	}
	if err := validatePrivatePolicyBundleEnvelope(envelope, opts); err != nil {
		return nil, err
	}

	var policy privatePolicyBundlePolicyDTO
	if err := decodePrivatePolicyBundleJSON(envelope.Policy, &policy); err != nil {
		return nil, err
	}
	if len(policy.Packs) == 0 {
		return nil, errors.New("private policy bundle has no packs")
	}

	policies := make(map[VerifiedPackIdentity]ResolvedHostPolicy, len(policy.Packs))
	for _, pack := range policy.Packs {
		identity := pack.VerifiedPackIdentity.verifiedPackIdentity()
		if err := validatePrivatePolicyBundleIdentity(identity); err != nil {
			return nil, err
		}
		if _, ok := policies[identity]; ok {
			return nil, errors.New("private policy bundle contains duplicate pack identity")
		}

		policies[identity] = cloneResolvedHostPolicy(ResolvedHostPolicy{
			PolicyID:            envelope.BundleID,
			PolicyVersion:       envelope.BundleVersion,
			PolicySHA256:        envelope.PolicyPrivateSHA256,
			PackIdentity:        identity,
			DomainPolicyRefs:    cloneStringSlice(pack.DomainPolicyRefs),
			BrokerPolicyRefs:    cloneStringSlice(pack.BrokerPolicyRefs),
			AllowedCapabilities: append([]Capability(nil), pack.AllowedCapabilities...),
			IngressDomains:      cloneDomainRules(pack.IngressDomainRules),
			BrokerDomains:       cloneDomainRules(pack.BrokerDomainRules),
			BrokerEndpoints:     privatePolicyBundleBrokerEndpoints(pack.BrokerEndpoints),
		})
	}

	return &privatePolicyBundleResolver{policies: policies}, nil
}

func (r *privatePolicyBundleResolver) ResolveHostPolicy(ctx context.Context, request HostPolicyRequest) (ResolvedHostPolicy, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ResolvedHostPolicy{}, errors.New(privateHostPolicyResolutionDenied)
	}
	if r == nil || len(r.policies) == 0 || !isAliasManifest(request.Manifest) || !hasPrivatePolicyBundleIdentity(request.PackIdentity) {
		return ResolvedHostPolicy{}, errors.New(privateHostPolicyResolutionDenied)
	}

	policy, ok := r.policies[request.PackIdentity]
	if !ok {
		return ResolvedHostPolicy{}, errors.New(privateHostPolicyResolutionDenied)
	}
	if err := validateResolvedHostPolicy(request.PackIdentity, request.Manifest, policy); err != nil {
		return ResolvedHostPolicy{}, errors.New(privateHostPolicyResolutionDenied)
	}
	if err := ctx.Err(); err != nil {
		return ResolvedHostPolicy{}, errors.New(privateHostPolicyResolutionDenied)
	}

	return cloneResolvedHostPolicy(policy), nil
}

func decodePrivatePolicyBundleJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("private policy bundle contains trailing JSON data")
	}

	return nil
}

func validatePrivatePolicyBundleEnvelope(envelope privatePolicyBundleEnvelopeDTO, opts PrivatePolicyBundleLoadOptions) error {
	if envelope.SchemaVersion != 1 {
		return errors.New("private policy bundle schema version is unsupported")
	}
	if err := validateOpaquePolicyRef("bundle_id", envelope.BundleID); err != nil {
		return err
	}
	if err := validatePrivatePolicyBundleVersion(envelope.BundleVersion); err != nil {
		return err
	}
	if len(envelope.Policy) == 0 {
		return errors.New("private policy bundle policy is empty")
	}
	if err := validateSHA256Hex("policy_private_sha256", envelope.PolicyPrivateSHA256); err != nil {
		return err
	}
	if sha256Hex(envelope.Policy) != envelope.PolicyPrivateSHA256 {
		return errors.New("private policy bundle policy hash mismatch")
	}
	if err := validateExpectedPrivatePolicySHA256(opts.ExpectedPolicyPrivateSHA256, envelope.PolicyPrivateSHA256); err != nil {
		return err
	}
	if envelope.PolicyPublicFingerprint != "" {
		if err := validateSHA256Hex("policy_public_fingerprint", envelope.PolicyPublicFingerprint); err != nil {
			return err
		}
	}
	if err := validateExpectedPolicyPublicFingerprint(opts.ExpectedPolicyPublicFingerprint, envelope.PolicyPublicFingerprint); err != nil {
		return err
	}

	return nil
}

func validateExpectedPrivatePolicySHA256(expected string, actual string) error {
	if expected == "" {
		return nil
	}
	if strings.TrimSpace(expected) != expected {
		return errors.New("expected private policy sha256 is invalid")
	}
	if err := validateSHA256Hex("expected_policy_private_sha256", expected); err != nil {
		return err
	}
	if expected != actual {
		return errors.New("expected private policy sha256 does not match")
	}

	return nil
}

func validateExpectedPolicyPublicFingerprint(expected string, actual string) error {
	if expected == "" {
		return nil
	}
	if strings.TrimSpace(expected) != expected {
		return errors.New("expected public policy fingerprint is invalid")
	}
	if err := validateSHA256Hex("expected_policy_public_fingerprint", expected); err != nil {
		return err
	}
	if expected != actual {
		return errors.New("expected public policy fingerprint does not match")
	}

	return nil
}

func validatePrivatePolicyBundleVersion(version string) error {
	if version == "" || strings.TrimSpace(version) != version {
		return errors.New("private policy bundle version must be non-empty and trimmed")
	}
	for _, r := range version {
		if r == '/' || r == '\\' || unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("private policy bundle version is invalid")
		}
	}

	return nil
}

func validatePrivatePolicyBundleIdentity(identity VerifiedPackIdentity) error {
	if err := validatePackID(identity.PackID); err != nil {
		return err
	}
	if err := validatePackVersion(identity.PackVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"asset_sha256":      identity.AssetSHA256,
		"manifest_sha256":   identity.ManifestSHA256,
		"payload_sha256":    identity.PayloadSHA256,
		"signature_sha256":  identity.SignatureSHA256,
		"public_key_sha256": identity.PublicKeySHA256,
	} {
		if err := validateSHA256Hex(field, value); err != nil {
			return err
		}
	}

	return nil
}

func hasPrivatePolicyBundleIdentity(identity VerifiedPackIdentity) bool {
	return identity.AssetSHA256 != "" && hasVerifiedPackIdentity(identity)
}

func (dto privatePolicyBundleIdentityDTO) verifiedPackIdentity() VerifiedPackIdentity {
	return VerifiedPackIdentity(dto)
}

func privatePolicyBundleBrokerEndpoints(endpoints []privatePolicyBundleBrokerEndpointDTO) []HostPolicyBrokerEndpoint {
	if endpoints == nil {
		return nil
	}
	converted := make([]HostPolicyBrokerEndpoint, len(endpoints))
	for i, endpoint := range endpoints {
		converted[i] = HostPolicyBrokerEndpoint{
			BrokerPolicyRef: endpoint.BrokerPolicyRef,
			EndpointRef:     endpoint.EndpointRef,
			URLTemplate:     endpoint.URLTemplate,
			AuthProfileRefs: cloneStringSlice(endpoint.AuthProfileRefs),
		}
	}

	return converted
}
