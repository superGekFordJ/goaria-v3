//go:build !windows && !linux && !darwin

package monitor

import (
	"context"
	"net"
	"os/exec"
	"time"
)

// arpLookup resolves the MAC address of the given gateway IP via generic OS command fallback.
func arpLookup(iface net.Interface, gw net.IP) string {
	if gw == nil {
		return ""
	}
	gwIPStr := gw.String()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try "arp -n" first
	out, err := exec.CommandContext(ctx, "arp", "-n", gwIPStr).Output()
	if err == nil {
		if mac := parseDarwinARPOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
		if mac := parseLinuxARPOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	// Try "ip neigh show"
	out, err = exec.CommandContext(ctx, "ip", "neigh", "show", gwIPStr).Output()
	if err == nil {
		if mac := parseLinuxNeighOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	return ""
}
