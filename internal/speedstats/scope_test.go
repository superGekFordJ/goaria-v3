package speedstats

import (
	"net/netip"
	"testing"
)

func TestClassifyByIP_PrivateLAN(t *testing.T) {
	c := NewScopeClassifier()
	tests := []struct {
		ip   string
		want string
	}{
		{"10.0.0.1", ScopeLAN},
		{"10.255.255.255", ScopeLAN},
		{"172.16.0.1", ScopeLAN},
		{"172.31.255.255", ScopeLAN},
		{"192.168.1.1", ScopeLAN},
		{"192.168.0.0", ScopeLAN},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			if got := c.ClassifyByIP(tt.ip); got != tt.want {
				t.Errorf("ClassifyByIP(%s) = %s, want %s", tt.ip, got, tt.want)
			}
		})
	}
}

func TestClassifyByIP_PublicWAN(t *testing.T) {
	c := NewScopeClassifier()
	tests := []struct {
		ip   string
		want string
	}{
		{"8.8.8.8", ScopeWAN},
		{"1.1.1.1", ScopeWAN},
		{"203.0.113.1", ScopeWAN},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			if got := c.ClassifyByIP(tt.ip); got != tt.want {
				t.Errorf("ClassifyByIP(%s) = %s, want %s", tt.ip, got, tt.want)
			}
		})
	}
}

func TestClassifyByIP_CGNAT(t *testing.T) {
	c := NewScopeClassifier()
	tests := []struct {
		ip   string
		want string
	}{
		{"100.64.0.1", ScopeWAN},
		{"100.127.255.255", ScopeWAN},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			if got := c.ClassifyByIP(tt.ip); got != tt.want {
				t.Errorf("ClassifyByIP(%s) = %s, want %s", tt.ip, got, tt.want)
			}
		})
	}
}

func TestClassifyByIP_Loopback(t *testing.T) {
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("127.0.0.1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(127.0.0.1) = %s, want lan", got)
	}
	if got := c.ClassifyByIP("127.255.255.255"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(127.255.255.255) = %s, want lan", got)
	}
}

func TestClassifyByIP_LinkLocal(t *testing.T) {
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("169.254.1.1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(169.254.1.1) = %s, want lan", got)
	}
}

func TestClassifyByIP_IPv6LinkLocal(t *testing.T) {
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("fe80::1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(fe80::1) = %s, want lan", got)
	}
}

func TestClassifyByIP_IPv6DocumentationGUA(t *testing.T) {
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("2001:db8::1"); got != ScopeWAN {
		t.Errorf("ClassifyByIP(2001:db8::1) = %s, want wan", got)
	}
}

func TestClassifyByIP_IPv6ULA(t *testing.T) {
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("fd00::1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(fd00::1) = %s, want lan", got)
	}
	if got := c.ClassifyByIP("fc00::1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(fc00::1) = %s, want lan", got)
	}
}

func TestClassifyByIP_IPv6Loopback(t *testing.T) {
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("::1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(::1) = %s, want lan", got)
	}
}

func TestClassifyByIP_InvalidIP(t *testing.T) {
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("not-an-ip"); got != ScopeWAN {
		t.Errorf("ClassifyByIP(invalid) = %s, want wan (default)", got)
	}
}

func TestClassifyByURL_IPLiteral(t *testing.T) {
	c := NewScopeClassifier()
	scope, domain := c.ClassifyByURL("http://192.168.1.1/file.zip")
	if scope != ScopeLAN {
		t.Errorf("scope = %s, want lan", scope)
	}
	if domain != "192.168.1.1" {
		t.Errorf("domain = %s, want 192.168.1.1", domain)
	}

	scope, _ = c.ClassifyByURL("http://8.8.8.8/file.zip")
	if scope != ScopeWAN {
		t.Errorf("scope = %s, want wan", scope)
	}
}

func TestClassifyByURL_InvalidURL(t *testing.T) {
	c := NewScopeClassifier()
	scope, domain := c.ClassifyByURL("://invalid")
	if scope != ScopeWAN {
		t.Errorf("scope = %s, want wan (default)", scope)
	}
	if domain != "" {
		t.Errorf("domain = %s, want empty", domain)
	}
}

func TestClassifyByURL_EmptyHost(t *testing.T) {
	c := NewScopeClassifier()
	scope, _ := c.ClassifyByURL("http:///file.zip")
	if scope != ScopeWAN {
		t.Errorf("scope = %s, want wan (default)", scope)
	}
}

func TestClassifyByURLAndIP_IPv6LinkLocalAndULACachesLAN(t *testing.T) {
	c := NewScopeClassifier()
	scope, domain := c.ClassifyByURLAndIP("http://nas.local/file.bin", "fe80::1")
	if scope != ScopeLAN {
		t.Errorf("ClassifyByURLAndIP fe80::1 scope = %s, want lan", scope)
	}
	if domain != "nas.local" {
		t.Errorf("domain = %s, want nas.local", domain)
	}
	c.mu.RLock()
	cached := c.cache["nas.local"]
	c.mu.RUnlock()
	if cached != ScopeLAN {
		t.Errorf("cache[nas.local] after fe80::1 = %s, want lan", cached)
	}

	c = NewScopeClassifier()
	scope, domain = c.ClassifyByURLAndIP("http://nas.local/file.bin", "fd00::1")
	if scope != ScopeLAN {
		t.Errorf("ClassifyByURLAndIP fd00::1 scope = %s, want lan", scope)
	}
	if domain != "nas.local" {
		t.Errorf("domain = %s, want nas.local", domain)
	}
	c.mu.RLock()
	cached = c.cache["nas.local"]
	c.mu.RUnlock()
	if cached != ScopeLAN {
		t.Errorf("cache[nas.local] after fd00::1 = %s, want lan", cached)
	}
}

func TestClassifyByURLAndIP(t *testing.T) {
	c := NewScopeClassifier()
	scope, domain := c.ClassifyByURLAndIP("http://example.com/file.zip", "10.0.0.5")
	if scope != ScopeLAN {
		t.Errorf("scope = %s, want lan", scope)
	}
	if domain != "example.com" {
		t.Errorf("domain = %s, want example.com", domain)
	}

	// Verify cache was populated
	c.mu.RLock()
	cached := c.cache["example.com"]
	c.mu.RUnlock()
	if cached != ScopeLAN {
		t.Errorf("cache[example.com] = %s, want lan", cached)
	}

	// Subsequent ClassifyByHost should use cache (no DNS needed)
	if got := c.ClassifyByHost("example.com"); got != ScopeLAN {
		t.Errorf("ClassifyByHost(example.com) from cache = %s, want lan", got)
	}
}

func TestClassifyByURLAndIP_PublicIP(t *testing.T) {
	c := NewScopeClassifier()
	scope, _ := c.ClassifyByURLAndIP("http://example.com/file.zip", "93.184.216.34")
	if scope != ScopeWAN {
		t.Errorf("scope = %s, want wan", scope)
	}
}

func TestClassifyByURLAndIP_CGNAT(t *testing.T) {
	c := NewScopeClassifier()
	scope, _ := c.ClassifyByURLAndIP("http://my-tailnet/file.zip", "100.64.1.1")
	if scope != ScopeWAN {
		t.Errorf("scope = %s, want wan (CGNAT)", scope)
	}
}

func TestClassifyByURLAndIP_InvalidURL(t *testing.T) {
	c := NewScopeClassifier()
	scope, domain := c.ClassifyByURLAndIP("://invalid", "10.0.0.1")
	if scope != ScopeWAN {
		t.Errorf("scope = %s, want wan (default)", scope)
	}
	if domain != "" {
		t.Errorf("domain = %s, want empty", domain)
	}
}

func TestClassifyByHost_CacheHit(t *testing.T) {
	c := NewScopeClassifier()
	// Manually populate cache
	c.mu.Lock()
	c.cache["cached-host.local"] = ScopeLAN
	c.mu.Unlock()

	if got := c.ClassifyByHost("cached-host.local"); got != ScopeLAN {
		t.Errorf("ClassifyByHost from cache = %s, want lan", got)
	}
}

func TestClassifyByHost_Empty(t *testing.T) {
	c := NewScopeClassifier()
	if got := c.ClassifyByHost(""); got != ScopeWAN {
		t.Errorf("ClassifyByHost(empty) = %s, want wan", got)
	}
}

func TestClassifyByIP_ConnectedGUA(t *testing.T) {
	stubConnectedGUAPrefixes(t, []netip.Prefix{
		netip.MustParsePrefix("2001:db8:1::1/128"),
	})
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("2001:db8:1::abcd"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(2001:db8:1::abcd) = %s, want lan", got)
	}
	if got := c.ClassifyByIP("2001:db8:2::1"); got != ScopeWAN {
		t.Errorf("ClassifyByIP(2001:db8:2::1) = %s, want wan", got)
	}
}

func TestClassifyByIP_EmptyConnectedGUA(t *testing.T) {
	stubConnectedGUAPrefixes(t, nil)
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("2001:db8::1"); got != ScopeWAN {
		t.Errorf("ClassifyByIP(2001:db8::1) empty list = %s, want wan", got)
	}
	if got := c.ClassifyByIP("fe80::1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(fe80::1) empty list = %s, want lan", got)
	}
	if got := c.ClassifyByIP("fd00::1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(fd00::1) empty list = %s, want lan", got)
	}
	if got := c.ClassifyByIP("::1"); got != ScopeLAN {
		t.Errorf("ClassifyByIP(::1) empty list = %s, want lan", got)
	}
}

func TestClassifyByIP_IPv4MappedWithConnectedGUA(t *testing.T) {
	stubConnectedGUAPrefixes(t, []netip.Prefix{
		netip.MustParsePrefix("2001:db8:1::/64"),
	})
	c := NewScopeClassifier()
	if got := c.ClassifyByIP("::ffff:10.0.0.1"); got != ScopeLAN {
		t.Errorf("mapped RFC1918 = %s, want lan", got)
	}
	if got := c.ClassifyByIP("::ffff:100.64.0.1"); got != ScopeWAN {
		t.Errorf("mapped CGNAT = %s, want wan", got)
	}
	if got := c.ClassifyByIP("::ffff:8.8.8.8"); got != ScopeWAN {
		t.Errorf("mapped public IPv4 = %s, want wan", got)
	}
}
