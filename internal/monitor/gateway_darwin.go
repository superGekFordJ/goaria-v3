//go:build darwin

package monitor

import (
	"context"
	"net"
	"os/exec"
	"time"
)

// discoverGatewayIP resolves the real default gateway IP for the given interface on macOS.
// It uses "route -n get -ifscope <iface> default" and falls back to "netstat -rn -f inet".
func discoverGatewayIP(iface net.Interface) net.IP {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try scoped route query first (macOS route syntax: route -n get -ifscope <iface> <destination>)
	cmd := exec.CommandContext(ctx, "route", "-n", "get", "-ifscope", iface.Name, "default")
	if out, err := cmd.Output(); err == nil {
		if ip := parseDarwinRouteOutput(string(out), iface.Name); ip != nil {
			return ip
		}
	}

	// Try generic route query
	cmdGen := exec.CommandContext(ctx, "route", "-n", "get", "default")
	if out, err := cmdGen.Output(); err == nil {
		if ip := parseDarwinRouteOutput(string(out), iface.Name); ip != nil {
			return ip
		}
	}

	// Fallback to netstat -rn -f inet
	cmdNetstat := exec.CommandContext(ctx, "netstat", "-rn", "-f", "inet")
	if out, err := cmdNetstat.Output(); err == nil {
		if ip := parseDarwinNetstatOutput(string(out), iface.Name); ip != nil {
			return ip
		}
	}

	return nil
}
