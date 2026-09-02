package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type Registry struct {
	packs              []VerifiedPack
	hostPolicyResolver HostPolicyResolver
}

type PackRejection struct {
	PackID string
	Reason string
}

func NewRegistry(embedded []EmbeddedPack, policy TrustPolicy) (*Registry, []PackRejection) {
	return NewRegistryWithHostPolicyResolver(embedded, policy, nil)
}

func NewRegistryWithHostPolicyResolver(embedded []EmbeddedPack, policy TrustPolicy, resolver HostPolicyResolver) (*Registry, []PackRejection) {
	registry := &Registry{hostPolicyResolver: resolver}
	if len(embedded) == 0 {
		return registry, nil
	}

	rejections := make([]PackRejection, 0)
	for i, pack := range embedded {
		verified, err := VerifyEmbeddedPack(pack, policy)
		if err != nil {
			rejections = append(rejections, PackRejection{
				PackID: bestEffortPackID(pack.ManifestJSON, i),
				Reason: err.Error(),
			})
			continue
		}

		registry.packs = append(registry.packs, cloneVerifiedPack(verified))
	}

	return registry, rejections
}

func NewRegistryFromVerifiedPacks(packs []VerifiedPack, resolver HostPolicyResolver) (*Registry, error) {
	registry := &Registry{hostPolicyResolver: resolver}
	if len(packs) == 0 {
		return registry, nil
	}

	seenIDs := make(map[string]struct{}, len(packs))
	for _, pack := range packs {
		if err := validatePackID(pack.Manifest.PackID); err != nil {
			return nil, fmt.Errorf("invalid pack id %q: %w", pack.Manifest.PackID, err)
		}
		if err := validatePackVersion(pack.Manifest.PackVersion); err != nil {
			return nil, fmt.Errorf("invalid pack version %q: %w", pack.Manifest.PackVersion, err)
		}
		if _, exists := seenIDs[pack.Manifest.PackID]; exists {
			return nil, fmt.Errorf("duplicate pack id: %s", pack.Manifest.PackID)
		}
		seenIDs[pack.Manifest.PackID] = struct{}{}

		if pack.Manifest.PackID != pack.Identity.PackID {
			return nil, errors.New("manifest pack_id does not match identity")
		}
		if pack.Manifest.PackVersion != pack.Identity.PackVersion {
			return nil, errors.New("manifest pack_version does not match identity")
		}
		if pack.Manifest.PayloadSHA256 != pack.Identity.PayloadSHA256 {
			return nil, errors.New("manifest payload_sha256 does not match identity")
		}

		if err := ValidateManifest(pack.Manifest, DefaultTrustPolicy()); err != nil {
			return nil, fmt.Errorf("validate manifest %q: %w", pack.Manifest.PackID, err)
		}

		if err := validateLowerHexSHA256Field("manifest_sha256", pack.Identity.ManifestSHA256); err != nil {
			return nil, err
		}
		if err := validateLowerHexSHA256Field("payload_sha256", pack.Identity.PayloadSHA256); err != nil {
			return nil, err
		}
		if err := validateLowerHexSHA256Field("signature_sha256", pack.Identity.SignatureSHA256); err != nil {
			return nil, err
		}
		if err := validateLowerHexSHA256Field("public_key_sha256", pack.Identity.PublicKeySHA256); err != nil {
			return nil, err
		}

		if pack.Identity.AssetSHA256 != "" {
			if err := validateLowerHexSHA256Field("asset_sha256", pack.Identity.AssetSHA256); err != nil {
				return nil, err
			}
		}

		actualPayloadSHA256 := sha256HexString(pack.Payload)
		if actualPayloadSHA256 != pack.Identity.PayloadSHA256 {
			return nil, errors.New("payload sha256 does not match identity")
		}

		registry.packs = append(registry.packs, cloneVerifiedPack(pack))
	}

	return registry, nil
}

func (r *Registry) Packs() []VerifiedPack {
	if r == nil || len(r.packs) == 0 {
		return nil
	}

	packs := make([]VerifiedPack, len(r.packs))
	for i, pack := range r.packs {
		packs[i] = cloneVerifiedPack(pack)
	}

	return packs
}

func (r *Registry) FindByURL(rawURL string) []VerifiedPack {
	return r.FindByURLWithContext(context.Background(), rawURL)
}

func (r *Registry) FindByURLWithContext(ctx context.Context, rawURL string) []VerifiedPack {
	if r == nil || len(r.packs) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	host, ok := ParseHTTPURLHost(rawURL)
	if !ok {
		return nil
	}

	matches := make([]VerifiedPack, 0)
	for _, pack := range r.packs {
		if registryPackMatchesHost(ctx, r.hostPolicyResolver, pack, host) {
			matches = append(matches, cloneVerifiedPack(pack))
		}
	}

	return matches
}

func registryPackMatchesHost(ctx context.Context, resolver HostPolicyResolver, pack VerifiedPack, host string) bool {
	if isAliasManifest(pack.Manifest) {
		policy, err := resolveAliasHostPolicy(ctx, resolver, pack.Identity, pack.Manifest)
		if err != nil {
			return false
		}

		return policyIngressMatchesHost(policy, host)
	}

	return manifestMatchesHost(pack.Manifest, host)
}

func ParseHTTPURLHost(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	if parsed.User != nil {
		return "", false
	}
	if strings.Contains(parsed.Host, ":") && parsed.Port() == "" {
		return "", false
	}

	host := parsed.Hostname()
	if host == "" {
		return "", false
	}
	if host != strings.TrimSpace(host) || strings.HasSuffix(host, ".") {
		return "", false
	}
	if strings.Contains(host, "%") {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil {
		return "", false
	}

	host = strings.ToLower(host)
	if validateDomainRule(DomainRule{Host: host}) != nil {
		return "", false
	}

	return host, true
}

func manifestMatchesHost(manifest Manifest, host string) bool {
	for _, rule := range manifest.Domains {
		if matchesDomainRule(host, rule) {
			return true
		}
	}

	return false
}

func matchesDomainRule(host string, rule DomainRule) bool {
	ruleHost := strings.ToLower(rule.Host)
	if validateDomainRule(DomainRule{Host: ruleHost}) != nil {
		return false
	}
	if host == ruleHost {
		return true
	}

	return rule.IncludeSubdomains && strings.HasSuffix(host, "."+ruleHost)
}

func bestEffortPackID(manifestJSON []byte, index int) string {
	var manifest struct {
		PackID string `json:"pack_id"`
	}
	if err := json.Unmarshal(manifestJSON, &manifest); err == nil && validatePackID(manifest.PackID) == nil {
		return manifest.PackID
	}

	return fmt.Sprintf("embedded[%d]", index)
}
