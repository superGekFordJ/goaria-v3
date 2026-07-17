package monitor

import (
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// virtualInterfacePrefixes lists interface name prefixes that indicate tunnel/overlay.
// Traffic through these is treated as proxy route.
var virtualInterfacePrefixes = []string{
	"utun", "tun", "tap", "tailscale", "zt", "docker", "veth", "br-",
	"singbox", "clash", "mihomo", "wintun", "v2ray", "wireguard", "wg",
}

// skipInterfacePrefixes lists prefixes for non-physical or virtual interfaces
// that should be excluded from gateway MAC enumeration.
var skipInterfacePrefixes = []string{
	"lo", "loopback", "utun", "tun", "tap", "tailscale", "zt", "docker", "veth", "br-",
	"singbox", "clash", "mihomo", "wintun", "v2ray", "wireguard", "wg",
}

// NetEnvCache maintains a background-refreshed map of physical interface → gateway MAC.
// The MAC is used to derive a privacy-preserving envKey for BBR history bucketing.
type NetEnvCache struct {
	mu           sync.RWMutex
	ifaceToMAC   map[string]string // interfaceName → normalized gateway MAC
	primaryIface string            // metric-lowest physical interface
	stopChan     chan struct{}
	stopOnce     sync.Once
}

// NewNetEnvCache creates an empty cache. Call Start to begin background refresh.
func NewNetEnvCache() *NetEnvCache {
	return &NetEnvCache{
		ifaceToMAC: make(map[string]string),
		stopChan:   make(chan struct{}),
	}
}

// Start launches the background refresh goroutine (15s interval).
func (n *NetEnvCache) Start() {
	go n.run()
}

// Stop signals the background goroutine to exit (idempotent).
func (n *NetEnvCache) Stop() {
	n.stopOnce.Do(func() {
		close(n.stopChan)
	})
}

func (n *NetEnvCache) run() {
	n.refresh()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-n.stopChan:
			return
		case <-ticker.C:
			n.refresh()
		}
	}
}

// refresh enumerates physical interfaces and populates the gateway MAC cache.
// alpha: primaryIface is the first enumerated physical interface, not the
// metric-lowest. Multi-homed + TUN scenarios may see primary drift.
func (n *NetEnvCache) refresh() {
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}

	newMap := make(map[string]string)
	var primaryCandidate string

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isSkipInterface(iface.Name) {
			continue
		}

		mac := gatewayMACForInterface(iface)
		if mac != "" {
			newMap[iface.Name] = mac
			if primaryCandidate == "" {
				primaryCandidate = iface.Name
			}
		} else {
			log.Printf("[netenv] gateway MAC lookup failed for iface=%s — envKey may degrade to empty-MAC bucket", iface.Name)
		}
	}

	n.mu.Lock()
	n.ifaceToMAC = newMap
	if primaryCandidate != "" {
		n.primaryIface = primaryCandidate
	}
	n.mu.Unlock()
}

// GetGatewayMAC returns the cached gateway MAC for the given interface.
func (n *NetEnvCache) GetGatewayMAC(ifaceName string) (string, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	mac, ok := n.ifaceToMAC[ifaceName]
	return mac, ok
}

// GetPrimaryIfaceMAC returns the gateway MAC of the primary (first enumerated) physical interface.
func (n *NetEnvCache) GetPrimaryIfaceMAC() (string, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.primaryIface == "" {
		return "", false
	}
	mac, ok := n.ifaceToMAC[n.primaryIface]
	return mac, ok
}

// GetAllActiveMACs returns the gateway MACs of all active physical interfaces.
// Virtual interfaces are already filtered by skipInterfacePrefixes in refresh().
func (n *NetEnvCache) GetAllActiveMACs() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	macs := make([]string, 0, len(n.ifaceToMAC))
	for _, mac := range n.ifaceToMAC {
		macs = append(macs, mac)
	}
	return macs
}

// isSkipInterface returns true for loopback/virtual/overlay interface names.
func isSkipInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range skipInterfacePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// isVirtualInterface returns true for tunnel/overlay interface names (proxy route indicator).
func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range virtualInterfacePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// gatewayMACForInterface resolves the gateway MAC for a given interface.
// Uses OS-specific ARP lookup; falls back to empty string on failure.
func gatewayMACForInterface(iface net.Interface) string {
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.To4() == nil {
			continue // IPv4 only here; IPv6 handled separately
		}
		gw := gatewayIPForSubnet(ip, ipNet.Mask)
		if gw == nil {
			continue
		}
		mac := arpLookup(iface.Name, gw)
		if mac != "" {
			return NormalizeMAC(mac)
		}
	}

	// IPv6 fallback: use gateway IPv6 /64 prefix hash as MAC surrogate
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip.To4() != nil || ip.To16() == nil {
			continue
		}
		prefix := ipv6LinkLocalPrefix(ip, ipNet.Mask)
		if prefix != "" {
			return prefix
		}
	}

	return ""
}

// gatewayIPForSubnet computes the first usable host in the subnet as a naive gateway guess.
// Real gateway discovery would parse routing tables; this is a best-effort fallback.
func gatewayIPForSubnet(ip net.IP, mask net.IPMask) net.IP {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}
	masked := ip4.Mask(mask)
	gw := make(net.IP, 4)
	copy(gw, masked)
	gw[3] |= 1 // first host in subnet
	return gw
}

// ipv6LinkLocalPrefix returns the /64 prefix hex as a MAC surrogate for pure-IPv6 networks.
func ipv6LinkLocalPrefix(ip net.IP, mask net.IPMask) string {
	if len(mask) < 2 {
		return ""
	}
	prefixLen, _ := mask.Size()
	if prefixLen < 64 {
		return ""
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return ""
	}
	return hexEncodeBytes(ip16[:8])
}

// hexEncodeBytes returns lowercase hex of the given bytes.
func hexEncodeBytes(b []byte) string {
	const hexChars = "0123456789abcdef"
	buf := make([]byte, len(b)*2)
	for i, v := range b {
		buf[i*2] = hexChars[v>>4]
		buf[i*2+1] = hexChars[v&0xf]
	}
	return string(buf)
}

// udpDialAndGetIface performs a UDP dial (no real traffic) to determine which
// local interface the OS routing table would use to reach remoteIP.
func udpDialAndGetIface(remoteIP string) (string, bool) {
	conn, err := net.Dial("udp", net.JoinHostPort(remoteIP, "80"))
	if err != nil {
		return "", false
	}
	defer conn.Close()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", false
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", false
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ipNet.Contains(localAddr.IP) {
				return iface.Name, true
			}
		}
	}
	return "", false
}

// ComputeEnvKeyForDownload derives the envKey for a download based on route and physical environment.
// routeCode is determined by whether the traffic goes through a virtual/tunnel interface.
// The url parameter is currently unused but reserved for future URL-based route判定.
func ComputeEnvKeyForDownload(url string, remoteIP string) string {
	netEnv := State.GetNetEnv()
	if netEnv == nil {
		return ComputeEnvKey(routeCodeProxy, "")
	}

	// If remoteIP is known, UDP dial to find the egress interface.
	if remoteIP != "" {
		ifaceName, ok := udpDialAndGetIface(remoteIP)
		if ok && !isVirtualInterface(ifaceName) {
			mac, macOK := netEnv.GetGatewayMAC(ifaceName)
			if macOK {
				return ComputeEnvKey(routeCodeDirect, mac)
			}
			return ComputeEnvKey(routeCodeDirect, "")
		}
		// Virtual interface or dial failed → proxy route
		mac, _ := netEnv.GetPrimaryIfaceMAC()
		return ComputeEnvKey(routeCodeProxy, mac)
	}

	// No remoteIP (e.g. resume path without head probe): best-effort.
	// Treat as proxy with primary MAC — conservative, avoids cross-env pollution.
	mac, _ := netEnv.GetPrimaryIfaceMAC()
	return ComputeEnvKey(routeCodeProxy, mac)
}
