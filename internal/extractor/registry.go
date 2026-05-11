package extractor

import (
	"context"
	"encoding/json"
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

	host, ok := parseHTTPURLHost(rawURL)
	if !ok {
		return nil
	}

	matches := make([]VerifiedPack, 0)
	for _, pack := range r.packs {
		if manifestMatchesHost(pack.Manifest, host) {
			matches = append(matches, cloneVerifiedPack(pack))
			continue
		}
		if !isAliasManifest(pack.Manifest) {
			continue
		}
		policy, err := resolveAliasHostPolicy(ctx, r.hostPolicyResolver, pack.Identity, pack.Manifest)
		if err != nil {
			continue
		}
		if policyIngressMatchesHost(policy, host) {
			matches = append(matches, cloneVerifiedPack(pack))
		}
	}

	return matches
}

func parseHTTPURLHost(rawURL string) (string, bool) {
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
