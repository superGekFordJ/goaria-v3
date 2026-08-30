//go:build darwin

package monitor

import (
	"context"
	"net"
	"os/exec"
	"time"
)

// arpLookup resolves the MAC address of the given gateway IP on macOS,
// strictly scoped to the specified network interface.
func arpLookup(iface net.Interface, gw net.IP) string {
	if gw == nil {
		return ""
	}
	gwIPStr := gw.String()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Scoped "arp -n -i <iface> <gwIP>"
	cmd := exec.CommandContext(ctx, "arp", "-n", "-i", iface.Name, gwIPStr)
	if out, err := cmd.Output(); err == nil {
		if mac := parseDarwinARPOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	// 2. Scoped interface query: "arp -n -i <iface> -a"
	cmdTable := exec.CommandContext(ctx, "arp", "-n", "-i", iface.Name, "-a")
	if out, err := cmdTable.Output(); err == nil {
		if mac := parseDarwinARPOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	// 3. Fallback: "arp -n -a" full table with interface filtering
	cmdFull := exec.CommandContext(ctx, "arp", "-n", "-a")
	if out, err := cmdFull.Output(); err == nil {
		if mac := parseDarwinARPOutput(string(out), gwIPStr, iface.Name); mac != "" {
			return mac
		}
	}

	return ""
}
