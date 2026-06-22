package speedstats

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
)

const (
	ScopeWAN = "wan"
	ScopeLAN = "lan"
)

// ScopeClassifier classifies download scope (wan/lan) by IP or host.
// Host→scope results are cached to avoid repeated DNS lookups.
type ScopeClassifier struct {
	mu    sync.RWMutex
	cache map[string]string
}

// NewScopeClassifier creates a new ScopeClassifier.
func NewScopeClassifier() *ScopeClassifier {
	return &ScopeClassifier{
		cache: make(map[string]string),
	}
}

// ClassifyByIP classifies a raw IP string directly.
func (c *ScopeClassifier) ClassifyByIP(ipStr string) string {
	addr, err := netip.ParseAddr(ipStr)
	if err != nil {
		return ScopeWAN
	}
	return classifyAddr(addr)
}

// ClassifyByHost classifies by hostname, using DNS lookup with caching.
func (c *ScopeClassifier) ClassifyByHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ScopeWAN
	}

	c.mu.RLock()
	if scope, ok := c.cache[host]; ok {
		c.mu.RUnlock()
		return scope
	}
	c.mu.RUnlock()

	scope := ScopeWAN
	if ips, err := net.LookupIP(host); err == nil && len(ips) > 0 {
		for _, ip := range ips {
			if addr, err := netip.ParseAddr(ip.String()); err == nil {
				scope = classifyAddr(addr)
				break
			}
		}
	}

	c.mu.Lock()
	c.cache[host] = scope
	c.mu.Unlock()
	return scope
}

// ClassifyByURL classifies by URL, returning scope and domain.
// IP-literal URLs are classified directly; hostnames go through ClassifyByHost.
func (c *ScopeClassifier) ClassifyByURL(rawURL string) (scope, domain string) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return ScopeWAN, ""
	}

	host := parsed.Hostname()
	domain = host

	if addr, err := netip.ParseAddr(host); err == nil {
		return classifyAddr(addr), domain
	}

	return c.ClassifyByHost(host), domain
}

// ClassifyByURLAndIP classifies using a pre-resolved remote IP (e.g. from TCP connection).
// The domain is extracted from the URL; scope is determined from the IP.
// The host→scope mapping is cached for future ClassifyByHost calls.
func (c *ScopeClassifier) ClassifyByURLAndIP(rawURL string, remoteIP string) (scope, domain string) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return ScopeWAN, ""
	}

	host := strings.ToLower(parsed.Hostname())
	domain = host

	scope = ScopeWAN
	if addr, err := netip.ParseAddr(remoteIP); err == nil {
		scope = classifyAddr(addr)
	}
	// 当 remoteIP 无效时保守默认 wan 并缓存，后续 ClassifyByHost 命中缓存跳过 DNS。
	// 这是有意为之的保守策略：wan 是安全默认值，误判 lan→wan 不会导致过度并发。

	if host != "" {
		c.mu.Lock()
		c.cache[host] = scope
		c.mu.Unlock()
	}

	return scope, domain
}

// classifyAddr determines wan/lan for a parsed IP address.
// Order: loopback → lan, link-local → lan, CGNAT 100.64/10 → wan, private → lan, else → wan.
func classifyAddr(ip netip.Addr) string {
	if ip.IsLoopback() {
		return ScopeLAN
	}
	if ip.IsLinkLocalUnicast() {
		return ScopeLAN
	}

	// CGNAT 100.64.0.0/10 → wan (IsPrivate does not cover CGNAT)
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	if cgnat.Contains(ip) {
		return ScopeWAN
	}

	if ip.IsPrivate() {
		return ScopeLAN
	}

	return ScopeWAN
}
