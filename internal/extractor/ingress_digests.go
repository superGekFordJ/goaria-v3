package extractor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strings"
)

const (
	ingressDigestSaltBytes = 16
	ingressDigestVersion   = 1
)

// IngressDigestSource caches FindByURL ingress hosts and hashes them with a
// fresh salt on each Snapshot. It lives in extractor so hashing never crosses
// into internal/extension.
type IngressDigestSource struct {
	exact     []string
	subdomain []string
	saltRead  io.Reader
}

type IngressDigestSnapshot struct {
	Version          int
	Salt             string
	ExactDigests     []string
	SubdomainDigests []string
}

func NewIngressDigestSource(registry *Registry) *IngressDigestSource {
	src := &IngressDigestSource{saltRead: rand.Reader}
	if registry == nil || len(registry.packs) == 0 {
		return src
	}

	exact := make(map[string]struct{})
	subdomain := make(map[string]struct{})
	ctx := context.Background()
	for _, pack := range registry.packs {
		rules := collectIngressRules(ctx, registry.hostPolicyResolver, pack)
		for _, rule := range rules {
			host := strings.ToLower(strings.TrimSpace(rule.Host))
			if validateDomainRule(DomainRule{Host: host}) != nil {
				continue
			}
			// IncludeSubdomains hosts are subdomain-only; the client still
			// hashes the current host against that set so the apex matches.
			if rule.IncludeSubdomains {
				subdomain[host] = struct{}{}
				continue
			}
			exact[host] = struct{}{}
		}
	}
	src.exact = sortedKeys(exact)
	src.subdomain = sortedKeys(subdomain)
	return src
}

func NewIngressDigestSourceFromLegacyRules(domains []DomainRule) *IngressDigestSource {
	cloned := append([]DomainRule(nil), domains...)
	return NewIngressDigestSource(&Registry{
		packs: []VerifiedPack{{Manifest: Manifest{Domains: cloned}}},
	})
}

func collectIngressRules(ctx context.Context, resolver HostPolicyResolver, pack VerifiedPack) []DomainRule {
	if isAliasManifest(pack.Manifest) {
		policy, err := resolveAliasHostPolicy(ctx, resolver, pack.Identity, pack.Manifest)
		if err != nil {
			return nil
		}
		return policy.IngressDomains
	}
	return pack.Manifest.Domains
}

func (s *IngressDigestSource) Ready() bool {
	return s != nil && (len(s.exact) > 0 || len(s.subdomain) > 0)
}

func (s *IngressDigestSource) Snapshot() (IngressDigestSnapshot, bool) {
	if !s.Ready() {
		return IngressDigestSnapshot{}, false
	}
	reader := s.saltRead
	if reader == nil {
		reader = rand.Reader
	}
	salt := make([]byte, ingressDigestSaltBytes)
	if _, err := io.ReadFull(reader, salt); err != nil {
		return IngressDigestSnapshot{}, false
	}

	exact := hashHosts(salt, s.exact)
	sub := hashHosts(salt, s.subdomain)
	return IngressDigestSnapshot{
		Version:          ingressDigestVersion,
		Salt:             hex.EncodeToString(salt),
		ExactDigests:     exact,
		SubdomainDigests: sub,
	}, true
}

func hashHosts(salt []byte, hosts []string) []string {
	if len(hosts) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(hosts))
	seen := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		digest := hashIngressHost(salt, host)
		if _, ok := seen[digest]; ok {
			continue
		}
		seen[digest] = struct{}{}
		out = append(out, digest)
	}
	sort.Strings(out)
	return out
}

// hashIngressHost concatenates the raw 16-byte salt with the ASCII host.
// The salt is not hex-encoded before hashing.
func hashIngressHost(salt []byte, host string) string {
	sum := sha256.Sum256(append(append([]byte{}, salt...), host...))
	return hex.EncodeToString(sum[:])
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
