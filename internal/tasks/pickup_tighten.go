package tasks

import (
	"strconv"

	"goaria-v3/internal/config"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// ApplyPickupTighten is the host body for Scheduler TightenOnPickup.
// Tighten-only: may lower Runtime.Workers when domain occupancy / N_max
// requires it; never raises Workers or MinChunkSize; never HeadProbes.
// Self-excludes the promoting GID from occupancy seed and domain workers.
func ApplyPickupTighten(cfg *types.DownloadRecord) {
	if cfg == nil || cfg.Runtime == nil {
		return
	}
	if !config.Get().SmartThreadMode {
		return
	}

	tracker := monitor.State.GetTracker()
	if tracker == nil {
		return
	}

	gid := "sg_" + cfg.ID
	scope, domain, envKey, ok := tracker.GetScopeAndEnv(gid)
	if !ok {
		return
	}

	remaining := cfg.TotalSize
	downloaded := cfg.Downloaded
	if cp := progress.CfgProgress(cfg); cp != nil {
		d, total, _, _, _, _ := cp.GetProgress()
		downloaded = d
		// Prefer progress total when cfg.TotalSize is still unknown.
		if remaining <= 0 && total > 0 {
			remaining = total
		}
	}
	if remaining > 0 && downloaded < remaining {
		remaining -= downloaded
	}
	if remaining <= 0 {
		return
	}

	maxConn, _ := strconv.Atoi(config.Get().MaxConnections)
	if maxConn <= 0 {
		maxConn = 8
	}

	infos := BuildOccupancyTaskInfosExcluding(gid)
	ledger := smartthread.NewBandwidthLedger(infos)
	existingWorkers := ExistingDomainWorkersFromTelemetryExcluding(scope, domain, gid)

	// Scope reserved from occupancy ledger (includes waiting TargetBandwidth
	// claims). Domain reserved already used the same ledger. Live Macro alone
	// would miss waiters — promote is occupancy-aware, not Resume-Macro parity.
	params := smartthread.Calculate(smartthread.CalcParams{
		FileSize:                remaining,
		MaxConnections:          maxConn,
		Scope:                   scope,
		Domain:                  domain,
		EnvKey:                  envKey,
		ReservedBandwidth:       ledger.Reserved(scope, envKey),
		ReservedDomainBandwidth: ledger.ReservedByDomain(scope, domain),
	})
	params = smartthread.ClampToServerLimit(params, remaining, scope, domain,
		existingWorkers, smartthread.GetDefaultServerLimits())

	if params.Split <= 0 || params.Split >= cfg.Runtime.Workers {
		return
	}

	cfg.Runtime.Workers = params.Split
	if params.MinSize > 0 && cfg.Runtime.MinChunkSize > 0 && params.MinSize < cfg.Runtime.MinChunkSize {
		cfg.Runtime.MinChunkSize = params.MinSize
	}
	tracker.SetThreadInfo(gid, params.Split, params.IsExploration)
	tracker.SetTargetBandwidth(gid, params.TargetBandwidth)
}

// BuildOccupancyTaskInfosExcluding is BuildOccupancyTaskInfos with one GID
// filtered out so a promoting waiter does not double-count its own claim.
func BuildOccupancyTaskInfosExcluding(excludeGID string) []smartthread.TrackedTaskInfo {
	infos := BuildOccupancyTaskInfos()
	if excludeGID == "" || len(infos) == 0 {
		return infos
	}
	out := make([]smartthread.TrackedTaskInfo, 0, len(infos))
	for _, info := range infos {
		if info.GID == excludeGID {
			continue
		}
		out = append(out, info)
	}
	return out
}

// ExistingDomainWorkersFromTelemetryExcluding matches
// ExistingDomainWorkersFromTelemetry but skips excludeGID so the waiter's
// own ThreadCount is not treated as peer occupancy at promote.
func ExistingDomainWorkersFromTelemetryExcluding(scope, domain, excludeGID string) int {
	if scope == "" || domain == "" {
		return 0
	}
	tr := monitor.State.GetTracker()
	if tr == nil {
		return 0
	}
	occupied := tr.GetOccupancyTrackedTasks()
	mon := monitor.State.GetMonitor()
	var telemetry *monitor.TelemetryCache
	if mon != nil {
		telemetry = mon.GetTelemetry()
	}
	total := 0
	for _, t := range occupied {
		if excludeGID != "" && t.GID == excludeGID {
			continue
		}
		if t.Scope != scope || t.Domain != domain {
			continue
		}
		snapCount := 0
		if telemetry != nil {
			snapCount = len(telemetry.Get(t.GID))
		}
		n := snapCount
		if t.ThreadCount > n {
			n = t.ThreadCount
		}
		total += n
	}
	return total
}
