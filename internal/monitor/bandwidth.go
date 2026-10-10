package monitor

import (
	"math"
	"strings"
	"time"

	"goaria-v3/internal/smartthread"
)

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

// OccupancyTaskInfos collects the unified per-task occupancy inputs used by
// ledger seeding (Add/Batch/Resume/pickup) and scope aggregation. The tracker
// occupancy set — not the cache — is the membership authority, so waiting
// claims, paused resume holds and confirmed-add placeholders are never lost
// to a stale cache row, and a stale cache row cannot resurrect a tracked
// waiting/terminal task.
//
// Lock order: immutable tracker copies first, then one cache snapshot, then
// the Surge raw overlay — the only step that may take Convergence c.mu.
// Never call while holding tracker or cache locks.
func OccupancyTaskInfos() []smartthread.TrackedTaskInfo {
	tr := State.GetTracker()
	if tr == nil {
		return nil
	}
	occupied := tr.GetOccupancyTrackedTasks()
	if len(occupied) == 0 {
		return nil
	}
	infos := trackedTaskInfos(occupied, activeCacheSpeedByGID())
	mon := State.GetMonitor()
	if mon == nil || mon.convergence == nil {
		return infos
	}
	for i := range infos {
		if !strings.HasPrefix(infos[i].GID, "sg_") {
			continue
		}
		if bps, ready := mon.convergence.LastRawBps(infos[i].GID); ready {
			infos[i].RawBps = bps
			infos[i].MacroReady = true
		}
	}
	return infos
}

// MacroOccupancyByScope returns the unified occupancy sum for a scope+envKey
// bucket evaluated at now: every eligible task contributes exactly once via
// the shared per-task formula (Surge raw when ready, cache telemetry
// otherwise, Target floor only under a live lease or retained claim).
// Tasks without a tracked scope are skipped; scope+env matching is exact.
func MacroOccupancyByScope(scope, envKey string, now time.Time) int64 {
	infos := OccupancyTaskInfos()
	var sum int64
	for _, info := range infos {
		if info.Scope == "" || info.Scope != scope || info.EnvKey != envKey {
			continue
		}
		sum = saturatingAddOccupancy(sum, smartthread.TaskOccupancy(info, now))
	}
	return sum
}

// MacroBandwidthByScope is the legacy name for the unified scope occupancy
// query. It captures a single evaluation instant and delegates to
// MacroOccupancyByScope so every consumer shares one formula.
func MacroBandwidthByScope(scope, envKey string) int64 {
	return MacroOccupancyByScope(scope, envKey, time.Now())
}

func saturatingAddOccupancy(a, b int64) int64 {
	if b < 0 {
		return a
	}
	sum := a + b
	if sum < a {
		return math.MaxInt64
	}
	return sum
}
