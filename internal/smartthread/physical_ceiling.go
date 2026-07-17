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
	// min(0, anything)=0; skip physical work when logical is already exhausted.
	if vLogicalAvailable <= 0 {
		return 0
	}
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
	var matchedDirect, matchedProxy string
	for _, mac := range macs {
		hDirect := p.ComputeEnvKeyFunc("0", mac)
		hProxy := p.ComputeEnvKeyFunc("1", mac)
		if p.EnvKey == hDirect || p.EnvKey == hProxy {
			matchedDirect, matchedProxy = hDirect, hProxy
			break
		}
	}
	if matchedDirect == "" {
		return vLogicalAvailable // degrade: collision failed (stale envKey / unknown NIC)
	}

	// Query 4 buckets (wan/lan × direct/proxy) on the matched physical NIC.
	v1, ok1 := speedstats.GetGlobalPeak("wan", matchedDirect)
	v2, ok2 := speedstats.GetGlobalPeak("wan", matchedProxy)
	v3, ok3 := speedstats.GetGlobalPeak("lan", matchedDirect)
	v4, ok4 := speedstats.GetGlobalPeak("lan", matchedProxy)

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
	wanReserved := p.Ledger.Reserved("wan", matchedDirect) + p.Ledger.Reserved("wan", matchedProxy)
	lanReserved := p.Ledger.Reserved("lan", matchedDirect) + p.Ledger.Reserved("lan", matchedProxy)
	totalReserved := wanReserved + lanReserved

	vPhysicalAvailable := max(physicalPeak-totalReserved, 0)

	return min64(vLogicalAvailable, vPhysicalAvailable)
}
