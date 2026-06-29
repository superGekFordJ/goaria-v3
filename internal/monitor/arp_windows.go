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
		if mac := parseMACFromArpOutputWindows(string(out)); mac != "" {
			return mac
		}
	}

	out, err = exec.Command("arp", "-a").Output()
	if err == nil {
		if mac := parseMACFromArpOutputWindows(string(out)); mac != "" {
			return mac
		}
	}

	return ""
}

// parseMACFromArpOutputWindows extracts the first MAC address from Windows arp output.
// Windows uses dash-separated MACs (aa-bb-cc-dd-ee-ff).
func parseMACFromArpOutputWindows(output string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
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
