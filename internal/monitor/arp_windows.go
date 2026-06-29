//go:build windows

package monitor

import (
	"net"
	"os/exec"
	"strings"
)

// arpLookup resolves the MAC address of the given gateway IP on Windows.
// Uses "arp -a" via cmd; no CGO dependency.
func arpLookup(ifaceName string, gw net.IP) string {
	if gw == nil {
		return ""
	}
	ipStr := gw.String()

	out, err := exec.Command("arp", "-a", ipStr).Output()
	if err == nil {
		if mac := parseMACFromArpOutputWindows(string(out), ipStr); mac != "" {
			return mac
		}
	}

	// Fallback: "arp -a" full table — filter by target IP to avoid returning
	// an unrelated host's MAC.
	out, err = exec.Command("arp", "-a").Output()
	if err == nil {
		if mac := parseMACFromArpOutputWindows(string(out), ipStr); mac != "" {
			return mac
		}
	}

	return ""
}

// parseMACFromArpOutputWindows extracts the MAC from the line whose fields
// include ipStr exactly. If no line matches, empty string is returned
// (never fall back to an arbitrary host's MAC).
// Windows uses dash-separated MACs (aa-bb-cc-dd-ee-ff).
func parseMACFromArpOutputWindows(output string, ipStr string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		// Exact field match avoids treating 192.168.1.1 as a
		// substring of 192.168.1.10 in the full ARP table.
		ipMatched := false
		for _, f := range fields {
			if f == ipStr {
				ipMatched = true
				break
			}
		}
		if !ipMatched {
			continue
		}
		for _, f := range fields {
			if isMACFieldWindows(f) {
				return f
			}
		}
	}
	return ""
}

func isMACFieldWindows(s string) bool {
	s = strings.ToLower(s)
	if len(s) != 17 {
		return false
	}
	for i, c := range s {
		switch i % 3 {
		case 2:
			if c != '-' && c != ':' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}
