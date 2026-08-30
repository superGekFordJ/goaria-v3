//go:build !windows && !linux && !darwin

package monitor

import "net"

// discoverGatewayIP is a no-op fallback on unsupported platforms,
// allowing caller to fall back to heuristic subnet gateway estimation.
func discoverGatewayIP(_ net.Interface) net.IP {
	return nil
}
