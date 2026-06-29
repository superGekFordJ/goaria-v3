//go:build !windows

package monitor

import (
	"net"
	"os/exec"
	"strings"
)

// arpLookup resolves the MAC address of the given gateway IP via OS command.
// Returns empty string on failure. No CGO dependency.
func arpLookup(ifaceName string, gw net.IP) string {
	if gw == nil {
		return ""
	}
	ipStr := gw.String()

	// Try "arp -n <ip>" first (macOS/BSD), then "ip neigh" (Linux)
	out, err := exec.Command("arp", "-n", ipStr).Output()
	if err == nil {
		if mac := parseMACFromArpOutput(string(out), ipStr); mac != "" {
			return mac
		}
	}

	out, err = exec.Command("ip", "neigh", "show", ipStr).Output()
	if err == nil {
		if mac := parseMACFromArpOutput(string(out), ipStr); mac != "" {
			return mac
		}
	}

	// Fallback: "arp -a" full table — filter by target IP to avoid returning
	// an unrelated host's MAC.
	out, err = exec.Command("arp", "-a").Output()
	if err == nil {
		if mac := parseMACFromArpOutput(string(out), ipStr); mac != "" {
			return mac
		}
	}

	return ""
}

// parseMACFromArpOutput extracts the MAC address from the line containing ipStr.
// Only the line that mentions the target IP is considered; if no line matches,
// empty string is returned (never fall back to an arbitrary host's MAC).
func parseMACFromArpOutput(output string, ipStr string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if !strings.Contains(line, ipStr) {
			continue
		}
		fields := strings.Fields(line)
		for _, f := range fields {
			if isMACField(f) {
				return f
			}
		}
	}
	return ""
}

// isMACField checks if a string looks like a MAC address.
func isMACField(s string) bool {
	s = strings.ToLower(s)
	if len(s) != 17 {
		return false
	}
	for i, c := range s {
		switch i % 3 {
		case 2:
			if c != ':' && c != '-' {
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
