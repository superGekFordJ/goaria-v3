package smartthread

import "goaria-v3/internal/speedstats"

// physicalHeadroom reports the matched-NIC physical bandwidth headroom: the
// maximum historical peak across the four scope/route buckets on the NIC that
// owns p.EnvKey, minus the symmetric total reserved against that NIC.
// known=false when any injection is missing, envKey is empty, there are no
// active MACs, the forward collision fails, or no bucket has history — the
// caller must treat it as an unknown dimension, never a zero budget.
func physicalHeadroom(p CalcParams) (available int64, known bool) {
	if p.Ledger == nil || p.ActiveMACsFunc == nil || p.ComputeEnvKeyFunc == nil {
		return 0, false // unknown: injection missing (e.g. Resume path)
	}
	if p.EnvKey == "" {
		return 0, false // unknown: empty envKey cannot collide
	}
	macs := p.ActiveMACsFunc()
	if len(macs) == 0 {
		return 0, false // unknown: no active physical interfaces
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
		return 0, false // unknown: collision failed (stale envKey / unknown NIC)
	}

	// Query 4 buckets (wan/lan × direct/proxy) on the matched physical NIC.
	v1, ok1 := speedstats.GetGlobalPeak("wan", matchedDirect)
	v2, ok2 := speedstats.GetGlobalPeak("wan", matchedProxy)
	v3, ok3 := speedstats.GetGlobalPeak("lan", matchedDirect)
	v4, ok4 := speedstats.GetGlobalPeak("lan", matchedProxy)

	// No historical data on any bucket → dimension stays unknown.
	if !ok1 && !ok2 && !ok3 && !ok4 {
		return 0, false
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

	// Symmetric deduction: both WAN and LAN subtract total reserved
	// (direct+proxy), accumulated saturating.
	wanReserved := saturatingAdd64(p.Ledger.Reserved("wan", matchedDirect), p.Ledger.Reserved("wan", matchedProxy))
	lanReserved := saturatingAdd64(p.Ledger.Reserved("lan", matchedDirect), p.Ledger.Reserved("lan", matchedProxy))
	totalReserved := saturatingAdd64(wanReserved, lanReserved)

	return max(physicalPeak-totalReserved, 0), true
}

// applyPhysicalCeiling computes a physical-NIC-aware bandwidth ceiling and
// returns min(vLogicalAvailable, vPhysicalAvailable). Degrades to
// vLogicalAvailable when the physical dimension is unknown (missing
// injection, empty envKey, no MACs, collision failure, or no bucket
// history); only tightens, never widens.
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
	vPhysicalAvailable, known := physicalHeadroom(p)
	if !known {
		return vLogicalAvailable
	}
	return min64(vLogicalAvailable, vPhysicalAvailable)
}
