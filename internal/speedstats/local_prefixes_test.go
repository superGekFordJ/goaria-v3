package speedstats

import (
	"net/netip"
	"testing"
)

func stubConnectedGUAPrefixes(t *testing.T, prefixes []netip.Prefix) {
	t.Helper()
	prev := listConnectedGUAPrefixes
	listConnectedGUAPrefixes = func() []netip.Prefix {
		return prefixes
	}
	t.Cleanup(func() {
		listConnectedGUAPrefixes = prev
	})
}

func TestNormalizeConnectedGUAPrefix(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"2001:db8:1::1/128", "2001:db8:1::/64", true},
		{"2001:db8:1::/64", "2001:db8:1::/64", true},
		{"2001:db8:1::/56", "2001:db8:1::/56", true},
		{"2001:db8::/48", "2001:db8::/48", true},
		{"2001:db8::/32", "", false},
		{"2001:db8::/0", "", false},
		{"fe80::1/64", "", false},
		{"fd00::1/64", "", false},
		{"::1/128", "", false},
		{"192.168.1.1/24", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := normalizeConnectedGUAPrefix(netip.MustParsePrefix(tt.in))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got %s)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.String() != tt.want {
				t.Fatalf("normalized = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNormalizeConnectedGUAPrefix_Widened128ContainsNeighbor(t *testing.T) {
	n, ok := normalizeConnectedGUAPrefix(netip.MustParsePrefix("2001:db8:1::1/128"))
	if !ok {
		t.Fatal("expected /128 GUA to normalize")
	}
	remote := netip.MustParseAddr("2001:db8:1::abcd")
	if !n.Contains(remote) {
		t.Fatalf("%s should contain %s", n, remote)
	}
	other := netip.MustParseAddr("2001:db8:2::1")
	if n.Contains(other) {
		t.Fatalf("%s must not contain %s", n, other)
	}
}
