package monitor

import (
	"encoding/hex"
	"net"
	"strconv"
	"strings"
)

// parseLinuxProcNetRoute extracts the default gateway for the specified interface from /proc/net/route content.
// /proc/net/route stores IPv4 gateway in little-endian 8-hex-character format.
func parseLinuxProcNetRoute(content string, targetIface string) net.IP {
	i := 0
	for line := range strings.SplitSeq(content, "\n") {
		if i == 0 {
			i++
			continue // skip header line
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		iface := fields[0]
		destination := fields[1]
		gatewayHex := fields[2]
		flagsHex := fields[3]

		if iface != targetIface || destination != "00000000" {
			continue // only matching interface and default destination (0.0.0.0)
		}

		flags, err := strconv.ParseUint(flagsHex, 16, 32)
		if err != nil || flags&0x2 == 0 { // RTF_GATEWAY flag is 0x0002
			continue
		}

		b, err := hex.DecodeString(gatewayHex)
		if err != nil || len(b) != 4 {
			continue
		}

		// Gateway in /proc/net/route is stored in little-endian order
		gwIP := net.IPv4(b[3], b[2], b[1], b[0])
		if !gwIP.IsUnspecified() && !gwIP.IsLoopback() {
			return gwIP
		}
	}
	return nil
}

// parseLinuxIPRouteOutput parses output from "ip route show default dev <iface>".
// If the output contains multipath "nexthop" specifications, it delegates to
// parseLinuxGeneralIPRouteOutput to correctly pair "via" and "dev".
// Example: "default via 192.168.1.1 proto dhcp metric 100"
func parseLinuxIPRouteOutput(output string, targetIface string) net.IP {
	if strings.Contains(output, "nexthop") {
		return parseLinuxGeneralIPRouteOutput(output, targetIface)
	}

	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "via" && i+1 < len(fields) {
				if ip := net.ParseIP(fields[i+1]); ip != nil && !ip.IsUnspecified() {
					return ip
				}
			}
		}
	}
	return nil
}

// parseLinuxGeneralIPRouteOutput parses output from "ip route show default", correctly
// pairing "via" and "dev" within the same nexthop segment in multipath routes.
// Example multipath: "default nexthop via 192.168.1.1 dev eth0 weight 1 nexthop via 10.0.0.1 dev eth1 weight 1"
// Example single: "default via 192.168.1.1 dev eth0 proto dhcp metric 100"
func parseLinuxGeneralIPRouteOutput(output string, targetIface string) net.IP {
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// If line contains multiple nexthops, split by "nexthop"
		var segments []string
		if strings.Contains(trimmed, "nexthop") {
			for p := range strings.SplitSeq(trimmed, "nexthop") {
				if strings.TrimSpace(p) != "" {
					segments = append(segments, p)
				}
			}
		} else {
			segments = []string{trimmed}
		}

		for _, seg := range segments {
			fields := strings.Fields(seg)
			var segVia net.IP
			var segDev string
			for i, field := range fields {
				if field == "via" && i+1 < len(fields) {
					segVia = net.ParseIP(fields[i+1])
				}
				if field == "dev" && i+1 < len(fields) {
					segDev = fields[i+1]
				}
			}
			if segDev == targetIface && segVia != nil && !segVia.IsUnspecified() {
				return segVia
			}
		}
	}
	return nil
}

// parseDarwinRouteOutput parses "route -n get default" command output.
// Example:
//
//	   route to: default
//	destination: default
//	       mask: default
//	    gateway: 192.168.1.1
//	  interface: en0
func parseDarwinRouteOutput(output string, targetIface string) net.IP {
	var gwStr string
	var ifaceStr string
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, "gateway:"); ok {
			gwStr = strings.TrimSpace(after)
		} else if after, ok := strings.CutPrefix(trimmed, "interface:"); ok {
			ifaceStr = strings.TrimSpace(after)
		}
	}

	if (ifaceStr == targetIface || targetIface == "") && gwStr != "" {
		if ip := net.ParseIP(gwStr); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
			return ip
		}
	}
	return nil
}

// parseDarwinNetstatOutput parses "netstat -rn -f inet" output.
// Example: "default            192.168.1.1        UGScg          en0"
func parseDarwinNetstatOutput(output string, targetIface string) net.IP {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "default" {
			gwIP := net.ParseIP(fields[1])
			ifaceField := fields[len(fields)-1]
			if ifaceField == targetIface && gwIP != nil && !gwIP.IsUnspecified() {
				return gwIP
			}
		}
	}
	return nil
}

// parseDarwinARPOutput parses macOS ARP output, handling formats such as:
// "? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]"
// "gateway (192.168.1.254) at 0:11:22:33:44:55 on en0 [ethernet]"
func parseDarwinARPOutput(output string, targetIP string, targetIface string) string {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// Check IP match (handles bare IP or parentheses like "(192.168.1.1)")
		ipMatched := false
		for _, f := range fields {
			cleaned := strings.Trim(f, "()")
			if cleaned == targetIP {
				ipMatched = true
				break
			}
		}
		if !ipMatched {
			continue
		}

		// Check interface match if targetIface is specified
		if targetIface != "" {
			ifaceMatched := false
			for i, f := range fields {
				if f == "on" && i+1 < len(fields) && fields[i+1] == targetIface {
					ifaceMatched = true
					break
				}
			}
			if !ifaceMatched {
				continue
			}
		}

		// Extract MAC following "at"
		for i, f := range fields {
			if f == "at" && i+1 < len(fields) {
				macCandidate := fields[i+1]
				if macCandidate != "(incomplete)" && isMACField(macCandidate) {
					return macCandidate
				}
			}
		}
	}
	return ""
}

// parseLinuxNeighOutput parses "ip neigh show" output.
// Example: "192.168.1.1 dev eth0 lladdr aa:bb:cc:dd:ee:ff REACHABLE"
// Example: "192.168.1.254 dev eth0 lladdr 00:11:22:33:44:55 STALE"
func parseLinuxNeighOutput(output string, targetIP string, targetIface string) string {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		// First field is typically the IP
		if fields[0] != targetIP {
			continue
		}

		// Verify interface if targetIface is specified
		if targetIface != "" {
			devMatched := false
			for i, f := range fields {
				if f == "dev" && i+1 < len(fields) && fields[i+1] == targetIface {
					devMatched = true
					break
				}
			}
			if !devMatched {
				continue
			}
		}

		// Look for lladdr
		for i, f := range fields {
			if f == "lladdr" && i+1 < len(fields) {
				macCandidate := fields[i+1]
				if isMACField(macCandidate) {
					return macCandidate
				}
			}
		}
	}
	return ""
}

// parseLinuxARPOutput parses traditional Linux "arp -n" output.
// Example: "192.168.1.1  ether  aa:bb:cc:dd:ee:ff  C  eth0"
func parseLinuxARPOutput(output string, targetIP string, targetIface string) string {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if fields[0] != targetIP {
			continue
		}
		if targetIface != "" && fields[len(fields)-1] != targetIface {
			continue
		}
		for _, f := range fields {
			if isMACField(f) {
				return f
			}
		}
	}
	return ""
}

// parseWindowsRoutePrintOutput parses the "route print 0.0.0.0" table and finds the gateway
// matching one of this interface's IPv4 addresses.
func parseWindowsRoutePrintOutput(output string, ifaceIPs []net.IP) net.IP {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		// Active Routes format: Destination Netmask Gateway Interface Metric
		// e.g. 0.0.0.0 0.0.0.0 192.168.1.1 192.168.1.100 25
		if len(fields) < 4 || fields[0] != "0.0.0.0" || fields[1] != "0.0.0.0" {
			continue
		}

		gwCandidate := net.ParseIP(fields[2])
		ifaceCandidate := net.ParseIP(fields[3])
		if gwCandidate == nil || ifaceCandidate == nil || gwCandidate.IsUnspecified() {
			continue
		}

		for _, ifaceIP := range ifaceIPs {
			if ifaceIP.Equal(ifaceCandidate) {
				return gwCandidate
			}
		}
	}
	return nil
}

// parseWindowsARPOutput parses Windows "arp -a" output, strictly scoping to the target
// interface IPv4 section in a locale-independent manner.
//
// Formats across locales:
//
//	Interface: 192.168.1.100 --- 0xa  (English)
//	接口: 192.168.1.100 --- 0xa        (Chinese)
//	  Internet Address      Physical Address      Type
//	  192.168.1.1           aa-bb-cc-dd-ee-ff     dynamic
func parseWindowsARPOutput(output string, targetIP string, targetIfaceIP string) string {
	currentIfaceMatched := (targetIfaceIP == "")
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		// Section headers on all Windows locales contain "---" and the interface IPv4 address
		if strings.Contains(trimmed, "---") {
			if targetIfaceIP != "" {
				currentIfaceMatched = false
			}
			for f := range strings.FieldsSeq(trimmed) {
				cleaned := strings.Trim(f, ":")
				if ip := net.ParseIP(cleaned); ip != nil && ip.To4() != nil {
					if targetIfaceIP != "" {
						currentIfaceMatched = (ip.String() == targetIfaceIP)
					}
					break
				}
			}
			continue
		}

		if !currentIfaceMatched {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[0] == targetIP {
			macCandidate := fields[1]
			if isMACField(macCandidate) {
				return macCandidate
			}
		}
	}
	return ""
}

// isMACField checks if a string matches standard MAC address formatting (colon or dash separated).
func isMACField(s string) bool {
	s = strings.ToLower(s)
	// Standard MAC strings are either 17 chars (00:11:22:33:44:55) or variable length if single digits (0:11:22:33:44:55)
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ':' || r == '-'
	})
	if len(parts) != 6 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 2 {
			return false
		}
		for _, c := range p {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}
