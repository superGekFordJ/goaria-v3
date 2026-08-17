package speedstats

import (
	"net"
	"net/netip"
)

const (
	connectedGUAPrefixMinBits   = 48
	connectedGUAPrefixWidenBits = 64
)

// listConnectedGUAPrefixes returns this host's connected IPv6 GUA prefixes.
// Tests replace it and restore via t.Cleanup; nil/empty means GUA stays wan.
var listConnectedGUAPrefixes = listConnectedGUAPrefixesFromSystem

func isIPv6GUA(ip netip.Addr) bool {
	return ip.Is6() && !ip.Is4In6() && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

func normalizeConnectedGUAPrefix(p netip.Prefix) (netip.Prefix, bool) {
	if !p.IsValid() {
		return netip.Prefix{}, false
	}
	addr := p.Addr().Unmap()
	if !addr.Is6() || addr.Is4In6() {
		return netip.Prefix{}, false
	}
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() {
		return netip.Prefix{}, false
	}
	bits := p.Bits()
	if bits < connectedGUAPrefixMinBits {
		return netip.Prefix{}, false
	}
	wantBits := bits
	if wantBits > connectedGUAPrefixWidenBits {
		wantBits = connectedGUAPrefixWidenBits
	}
	out, err := addr.Prefix(wantBits)
	if err != nil {
		return netip.Prefix{}, false
	}
	return out.Masked(), true
}

func listConnectedGUAPrefixesFromSystem() []netip.Prefix {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet == nil || ipNet.IP == nil {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipNet.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if !addr.Is6() {
				continue
			}
			ones, bits := ipNet.Mask.Size()
			if bits == 0 {
				continue
			}
			p, err := addr.Prefix(ones)
			if err != nil {
				continue
			}
			n, ok := normalizeConnectedGUAPrefix(p)
			if !ok {
				continue
			}
			out = append(out, n)
		}
	}
	return out
}

func connectedGUAContains(ip netip.Addr) bool {
	for _, p := range listConnectedGUAPrefixes() {
		n, ok := normalizeConnectedGUAPrefix(p)
		if ok && n.Contains(ip) {
			return true
		}
	}
	return false
}
