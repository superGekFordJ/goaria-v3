//go:build windows

package monitor

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// discoverGatewayIP resolves the real default gateway IP for the given interface on Windows.
// It prioritizes the native IP Helper API (GetAdaptersAddresses) for microsecond-level,
// in-memory query without sub-processes, and falls back to "route print" if needed.
func discoverGatewayIP(iface net.Interface) net.IP {
	flags := uint32(windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_DNS_SERVER)
	var size uint32 = 15000
	var b []byte
	for {
		b = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&b[0])), &size)
		if err == nil {
			break
		}
		if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			continue
		}
		return discoverGatewayIPFromRoutePrint(iface)
	}

	addr := (*windows.IpAdapterAddresses)(unsafe.Pointer(&b[0]))
	var fallbackIP net.IP
	for ; addr != nil; addr = addr.Next {
		// Match by IPv4 or IPv6 interface index, or adapter GUID name
		adapterGUID := windows.BytePtrToString((*byte)(unsafe.Pointer(addr.AdapterName)))
		if addr.IfIndex != uint32(iface.Index) && addr.Ipv6IfIndex != uint32(iface.Index) && adapterGUID != iface.Name {
			continue
		}

		for gw := addr.FirstGatewayAddress; gw != nil; gw = gw.Next {
			if gw.Address.Sockaddr == nil {
				continue
			}
			var ip net.IP
			switch gw.Address.Sockaddr.Addr.Family {
			case windows.AF_INET:
				raw4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(gw.Address.Sockaddr))
				ip = net.IPv4(raw4.Addr[0], raw4.Addr[1], raw4.Addr[2], raw4.Addr[3])
			case windows.AF_INET6:
				raw6 := (*windows.RawSockaddrInet6)(unsafe.Pointer(gw.Address.Sockaddr))
				ip = make(net.IP, len(raw6.Addr))
				copy(ip, raw6.Addr[:])
			}
			if ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() {
				if ip.To4() != nil {
					return ip // IPv4 gateway preferred
				}
				if fallbackIP == nil {
					fallbackIP = ip
				}
			}
		}
	}

	if fallbackIP != nil {
		return fallbackIP
	}

	return discoverGatewayIPFromRoutePrint(iface)
}

// discoverGatewayIPFromRoutePrint runs "route print 0.0.0.0" as a fallback.
func discoverGatewayIPFromRoutePrint(iface net.Interface) net.IP {
	addrs, err := iface.Addrs()
	if err != nil || len(addrs) == 0 {
		return nil
	}

	var ifaceIPs []net.IP
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP != nil {
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				ifaceIPs = append(ifaceIPs, ip4)
			}
		}
	}

	if len(ifaceIPs) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "route", "print", "0.0.0.0")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	return parseWindowsRoutePrintOutput(string(out), ifaceIPs)
}
