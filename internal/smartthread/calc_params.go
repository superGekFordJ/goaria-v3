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
	convergenceInterval      = 5 * time.Second
	throughputFloorRatio     = 0.5
	throughputStableRatio    = 0.8
	scaleDownStableCycles    = 3
	scaleUpStableCycles      = 3
)

// CalcParams holds the inputs for BBR-aware thread calculation.
type CalcParams struct {
	FileSize          int64  // 新任务=总大小；Resume=剩余大小(total-downloaded)
	MaxConnections    int    // 用户硬上限 W_max（来自 config.MaxConnections）
	Scope             string // "wan"/"lan"
	Domain            string // 用于 GetDomainPeak/GetRTprop
	ReservedBandwidth int64  // 同 scope 活跃任务实时速度之和 + 本批次已预留
}
