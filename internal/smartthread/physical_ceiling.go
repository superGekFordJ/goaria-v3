package smartthread

import "goaria-v3/internal/speedstats"

// applyPhysicalCeiling computes a physical-NIC-aware bandwidth ceiling and
// returns min(vLogicalAvailable, vPhysicalAvailable). Degrades to
// vLogicalAvailable when any injection is missing, no active MACs, envKey is
// empty, collision fails, or all 4 buckets have no historical peak.
//
// Known limitations (declared per design doc):
//   - TUN all-proxy + multi-NIC: ComputeEnvKeyForDownload falls back to
//     primary iface MAC, may group wrong. OS routing black-box limit.
//   - Historical peak != physical limit: max peak is an observed bottleneck
//     surrogate, not the hardware NIC rate.
//   - Resume with stale envKey: collision may fail → degrade to logical-only.
func applyPhysicalCeiling(vLogicalAvailable int64, p CalcParams) int64 {
	if p.Ledger == nil || p.ActiveMACsFunc == nil || p.ComputeEnvKeyFunc == nil {
		return vLogicalAvailable // degrade: injection missing (e.g. Resume path)
	}
	if p.EnvKey == "" {
		return vLogicalAvailable // degrade: empty envKey cannot collide
	}
	macs := p.ActiveMACsFunc()
	if len(macs) == 0 {
		return vLogicalAvailable // degrade: no active physical interfaces
	}

	// Forward collision: find the physical NIC whose hash matches p.EnvKey.
	// "0"=direct, "1"=proxy (monitor routeCode constants, unexported here).
	var matchedMAC string
	for _, mac := range macs {
		hDirect := p.ComputeEnvKeyFunc("0", mac)
		hProxy := p.ComputeEnvKeyFunc("1", mac)
		if p.EnvKey == hDirect || p.EnvKey == hProxy {
			matchedMAC = mac
			break
		}
	}
	if matchedMAC == "" {
		return vLogicalAvailable // degrade: collision failed (stale envKey / unknown NIC)
	}

	// Query 4 buckets (wan/lan × direct/proxy) on the matched physical NIC.
	hDirect := p.ComputeEnvKeyFunc("0", matchedMAC)
	hProxy := p.ComputeEnvKeyFunc("1", matchedMAC)
	v1, ok1 := speedstats.GetGlobalPeak("wan", hDirect)
	v2, ok2 := speedstats.GetGlobalPeak("wan", hProxy)
	v3, ok3 := speedstats.GetGlobalPeak("lan", hDirect)
	v4, ok4 := speedstats.GetGlobalPeak("lan", hProxy)

	// Zero-ceiling guard: no historical data on any bucket → do not apply.
	if !ok1 && !ok2 && !ok3 && !ok4 {
		return vLogicalAvailable // degrade: no historical peak on this NIC
	}

	// Physical peak = max of all ok=true buckets.
	physicalPeak := int64(0)
	for _, pair := range []struct {
		v  int64
		ok bool
	}{{v1, ok1}, {v2, ok2}, {v3, ok3}, {v4, ok4}} {
		if pair.ok && pair.v > physicalPeak {
			physicalPeak = pair.v
		}
	}

	// Symmetric deduction: both WAN and LAN subtract total reserved (direct+proxy).
	wanReserved := p.Ledger.Reserved("wan", hDirect) + p.Ledger.Reserved("wan", hProxy)
	lanReserved := p.Ledger.Reserved("lan", hDirect) + p.Ledger.Reserved("lan", hProxy)
	totalReserved := wanReserved + lanReserved

	vPhysicalAvailable := physicalPeak - totalReserved
	if vPhysicalAvailable < 0 {
		vPhysicalAvailable = 0
	}

	return min64(vLogicalAvailable, vPhysicalAvailable)
}
