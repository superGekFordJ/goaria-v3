//go:build linux

package monitor

import (
	"context"
	"net"
	"os/exec"
	"time"
)

// arpLookup resolves the MAC address of the given gateway IP on Linux,
// strictly scoped to the specified network interface.
func arpLookup(iface net.Interface, gw net.IP) string {
	if gw == nil {
		return ""
	}
	gwIPStr := gw.String()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Scoped "ip neigh show to <gwIP> dev <iface>"
	cmd := exec.CommandContext(ctx, "ip", "neigh", "show", "to", gwIPStr, "dev", iface.Name)
	if out, err := cmd.Output(); err == nil {
		if mac := parseLinuxNeighOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	// 2. Scoped "arp -n -i <iface> <gwIP>"
	cmdARP := exec.CommandContext(ctx, "arp", "-n", "-i", iface.Name, gwIPStr)
	if out, err := cmdARP.Output(); err == nil {
		if mac := parseLinuxARPOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	// 3. Fallback: general "ip neigh show dev <iface>"
	cmdNeighDev := exec.CommandContext(ctx, "ip", "neigh", "show", "dev", iface.Name)
	if out, err := cmdNeighDev.Output(); err == nil {
		if mac := parseLinuxNeighOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	// 4. Fallback: "arp -n" table
	cmdFull := exec.CommandContext(ctx, "arp", "-n")
	if out, err := cmdFull.Output(); err == nil {
		if mac := parseLinuxARPOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	return ""
}
