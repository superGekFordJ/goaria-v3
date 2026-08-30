//go:build windows

package monitor

import (
	"context"
	"net"
	"os/exec"
	"syscall"
	"time"
)

// arpLookup resolves the MAC address of the given gateway IP on Windows.
// It scopes the ARP query to the specific interface IPv4 address using "-N <ifaceIP>".
func arpLookup(iface net.Interface, gw net.IP) string {
	if gw == nil {
		return ""
	}
	gwIPStr := gw.String()

	addrs, err := iface.Addrs()
	var ifaceIPStr string
	if err == nil {
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP != nil {
				if ip4 := ipNet.IP.To4(); ip4 != nil {
					ifaceIPStr = ip4.String()
					break
				}
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Scoped query: "arp -a <gwIP> -N <ifaceIP>"
	if ifaceIPStr != "" {
		cmdScoped := exec.CommandContext(ctx, "arp", "-a", gwIPStr, "-N", ifaceIPStr)
		cmdScoped.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if out, err := cmdScoped.Output(); err == nil {
			if mac := parseWindowsARPOutput(string(out), gwIPStr, ifaceIPStr); mac != "" {
				return mac
			}
		}

		// 2. Scoped interface table: "arp -a -N <ifaceIP>"
		cmdTable := exec.CommandContext(ctx, "arp", "-a", "-N", ifaceIPStr)
		cmdTable.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if out, err := cmdTable.Output(); err == nil {
			if mac := parseWindowsARPOutput(string(out), gwIPStr, ifaceIPStr); mac != "" {
				return mac
			}
		}
	}

	// 3. Fallback: "arp -a <gwIP>"
	cmdFallback := exec.CommandContext(ctx, "arp", "-a", gwIPStr)
	cmdFallback.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmdFallback.Output(); err == nil {
		if mac := parseWindowsARPOutput(string(out), gwIPStr, ifaceIPStr); mac != "" {
			return mac
		}
	}

	// 4. Fallback: "arp -a" full table
	cmdFull := exec.CommandContext(ctx, "arp", "-a")
	cmdFull.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmdFull.Output(); err == nil {
		if mac := parseWindowsARPOutput(string(out), gwIPStr, ifaceIPStr); mac != "" {
			return mac
		}
	}

	return ""
}
