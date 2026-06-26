package smartthread

import "time"

const (
	// gamma is the safety margin added to the saturation concurrency.
	gamma = 1

	// tTargetChunk is the target time to download a single chunk (seconds).
	tTargetChunk = 2

	// congestionFloor is the minimum thread count when bandwidth is congested.
	congestionFloor = 2

	// minThreadEfficiency is the lower bound for V_thread_avg (100KB/s).
	minThreadEfficiency = 100 * 1024

	// minChunkSize is the minimum chunk size (1MB).
	minChunkSize = 1024 * 1024

	// maxChunkSize is the maximum chunk size (1GB).
	maxChunkSize = 1024 * 1024 * 1024

	// Convergence tick constants
	convergenceInterval    = 5 * time.Second
	bandwidthReleaseCycles = 1 // bandwidth release degraded filter (deterministic event)
	connErrorThreshold     = 3 // consecutive conn errors before N_max fuse triggers

	// Peak-efficiency active-probing convergence tunables
	peakRaiseBand         = 1.05 // noise gate: only bump peak when raw beats it by >5%
	efficiencyGuardBand   = 0.85 // D3: adopt higher-N only if single-thread eff within 15% of bestEff
	peakSpeedGuardBand    = 0.90 // D3: adopting fewer peakWorkers requires current speed ≥ 90% of peak
	marginalDropThreshold = 0.50 // D4: DropRatio ≤ 0.5 → plateau (success); > 0.5 → linear zone (knee crossed)
	probeIntervalCycles   = 3    // COLD cadence between probes (momentum off); ~15s @ 5s
	peakSustainCycles     = 2    // consecutive stable raw samples before ratchet records
	frozenCooldownCycles  = 12   // ~60s @ 5s interval; must be < IdleConnTimeout(90s)/interval
	probeFloorWorkers     = 2    // hard lower bound for probing (aligns with congestionFloor)
)

// CalcParams holds the inputs for BBR-aware thread calculation.
type CalcParams struct {
	FileSize          int64  // 新任务=总大小；Resume=剩余大小(total-downloaded)
	MaxConnections    int    // 用户硬上限 W_max（来自 config.MaxConnections）
	Scope             string // "wan"/"lan"
	Domain            string // 用于 GetDomainPeak/GetRTprop（配合 Scope 做 domain+scope 联合过滤）
	ReservedBandwidth int64  // 同 scope 活跃任务实时速度之和 + 本批次已预留
}
