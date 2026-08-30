//go:build linux

package monitor

import (
	"context"
	"net"
	"os"
	"os/exec"
	"time"
)

// discoverGatewayIP resolves the real default gateway IP for the given interface on Linux.
// It reads /proc/net/route directly (pure Go in-memory kernel read, microsecond-level),
// and falls back to "ip route" command if necessary.
func discoverGatewayIP(iface net.Interface) net.IP {
	if data, err := os.ReadFile("/proc/net/route"); err == nil {
		if ip := parseLinuxProcNetRoute(string(data), iface.Name); ip != nil {
			return ip
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Fallback to "ip route show default dev <iface>"
	cmd := exec.CommandContext(ctx, "ip", "route", "show", "default", "dev", iface.Name)
	if out, err := cmd.Output(); err == nil {
		if ip := parseLinuxIPRouteOutput(string(out), iface.Name); ip != nil {
			return ip
		}
	}

	// Fallback to general "ip route show default" (with multipath & metric support)
	cmdGen := exec.CommandContext(ctx, "ip", "route", "show", "default")
	if out, err := cmdGen.Output(); err == nil {
		if ip := parseLinuxGeneralIPRouteOutput(string(out), iface.Name); ip != nil {
			return ip
		}
	}

	return nil
}
