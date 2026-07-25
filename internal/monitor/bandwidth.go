package monitor

import "strings"

// ActiveBandwidthByScope returns the sum of Cache DownloadSpeed for all active
// tasks whose tracked scope and envKey match. This is the UI-era Cache sum
// (Surge: 150ms EMA). Ledger / V_available occupancy should use
// MacroBandwidthByScope instead.
//
// Tasks without a tracked scope are skipped (conservative:宁可少计也不阻塞,
// avoids hot-path DNS re-classification).
func ActiveBandwidthByScope(scope, envKey string) int64 {
	tr := State.GetTracker()
	if tr == nil || Cache == nil {
		return 0
	}
	var sum int64
	for _, t := range Cache.GetActive() {
		s, _, e, ok := tr.GetScopeAndEnv(t.GID)
		if !ok || s != scope || e != envKey {
			continue
		}
		sum += parseInt64(t.DownloadSpeed)
	}
	return sum
}

// MacroBandwidthByScope returns macro-band occupancy for a scope+envKey bucket.
//
// Per-gid exclusive contribution:
//   - Surge (sg_): ConvergenceTicker lastRawBps when macroReady; otherwise
//     Cache DownloadSpeed cold pad. Once ready, never re-reads Cache EMA for
//     that state lifetime (including lastRawBps==0).
//   - Aria2 / non-sg_: Cache DownloadSpeed (tick band).
//
// Match/skip rules are identical to ActiveBandwidthByScope. Nil monitor /
// convergence treats all Surge tasks as cold.
func MacroBandwidthByScope(scope, envKey string) int64 {
	tr := State.GetTracker()
	if tr == nil || Cache == nil {
		return 0
	}
	mon := State.GetMonitor()
	var sum int64
	for _, t := range Cache.GetActive() {
		s, _, e, ok := tr.GetScopeAndEnv(t.GID)
		if !ok || s != scope || e != envKey {
			continue
		}
		if strings.HasPrefix(t.GID, "sg_") {
			if mon != nil && mon.convergence != nil {
				bps, ready := mon.convergence.LastRawBps(t.GID)
				if ready {
					sum += bps
					continue
				}
			}
			sum += parseInt64(t.DownloadSpeed)
			continue
		}
		sum += parseInt64(t.DownloadSpeed)
	}
	return sum
}
