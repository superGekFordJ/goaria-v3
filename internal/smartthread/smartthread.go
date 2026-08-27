package smartthread

import (
	"math"

	"goaria-v3/internal/config"
	"goaria-v3/internal/speedstats"
)

const (
	defaultMinThreadLife = 5 // 默认最小生存时间 5 秒
)

// ThreadParams 计算结果
type ThreadParams struct {
	Split           int   // -x 参数：线程数
	MinSize         int64 // -k 参数：最小切分大小 (bytes)
	IsExploration   bool  // 是否触发了探索模式
	TargetBandwidth int64 // 预计占用带宽，供批次账本 Reserve
	NSat            int   // BBR saturation concurrency (for IsKeepAlive detection)
}

// Calculate 根据 BBR 带宽感知公式计算最优线程参数。
// p.FileSize: 文件大小 (bytes)，0 表示未知（HEAD 失败）；Resume 时传剩余大小。
// p.MaxConnections: 用户硬上限 W_max。
// p.Scope/p.Domain: 用于按 scope 分池查询 speedstats 和探索判定。
// p.ReservedBandwidth: 同 scope 活跃任务实时速度之和 + 本批次已预留。
func Calculate(p CalcParams) ThreadParams {
	maxConn := p.MaxConnections
	if maxConn <= 0 {
		maxConn = 8
	}

	// 未知大小：保守策略，跳过 BDP/带宽约束
	if p.FileSize <= 0 {
		isExploration := !speedstats.HasDomainScopeEnvRecord(p.Domain, p.Scope, p.EnvKey)
		split := maxConn
		if isExploration {
			limit := explorationLimit(maxConn)
			if split > limit {
				split = limit
			}
		}
		return ThreadParams{
			Split:           split,
			MinSize:         0,
			IsExploration:   isExploration,
			TargetBandwidth: 0,
			NSat:            maxConn,
		}
	}

	// 获取 T_min
	tMin := config.Get().MinThreadLife
	if tMin <= 0 {
		tMin = defaultMinThreadLife
	}

	// --- 数据采集 ---
	// Tier 1: Domain-specific p75 (preferred — no cross-CDN pollution)
	vThreadAvg, threadAvgOK := speedstats.GetRecentPeakByDomain(p.Domain, p.Scope, p.EnvKey)

	// Tier 2: Fallback to scope-wide p75 with 0.5x conservative penalty
	// for unknown domains (avoids over-allocation from polluted scope data)
	if !threadAvgOK {
		vThreadAvg, threadAvgOK = speedstats.GetRecentPeakByScope(p.Scope, p.EnvKey)
		if threadAvgOK {
			vThreadAvg /= 2
		}
	}

	vSinglePeak, singlePeakOK := speedstats.GetDomainPeak(p.Domain, p.Scope, p.EnvKey)
	vGlobalPeak, globalPeakOK := speedstats.GetGlobalPeak(p.Scope, p.EnvKey)

	// --- 冷启动 / 缺数据降级 ---
	// V_thread_avg 或 V_global_peak 无 → 退化为 legacy 启发式
	if !threadAvgOK || !globalPeakOK {
		return calculateLegacy(p.FileSize, maxConn, p.Domain, p.Scope, p.EnvKey, tMin)
	}

	// 钳 V_thread_avg 到下限
	if vThreadAvg < minThreadEfficiency {
		vThreadAvg = minThreadEfficiency
	}

	// --- 全局带宽感知 ---
	vAvailable := max(vGlobalPeak-p.ReservedBandwidth, 0)

	// 物理天花板（前向碰撞 + 对称扣减）：内置默认启用，
	// 降级时退回 vLogicalAvailable（原逻辑天花板），只收紧不放宽。
	vAvailable = applyPhysicalCeiling(vAvailable, p)

	// V_target = min(domain remaining, V_available) when domain peak known.
	// Domain-only exhaustion uses floor=1 (global V_available still healthy);
	// congestionFloor=2 applies only when global V_available is congested.
	var vTarget int64
	if singlePeakOK {
		vDomainAvailable := max(vSinglePeak-p.ReservedDomainBandwidth, 0)
		vTarget = min64(vDomainAvailable, vAvailable)
	} else {
		vTarget = vAvailable
	}

	// --- 并发数计算 ---
	// N_sat = ceil(V_target / V_thread_avg) + gamma
	var nSat64 int64
	if vThreadAvg > 0 {
		nSat64 = ceilDivPositive(vTarget, vThreadAvg) + int64(gamma)
	}

	// N_tmin = ceil(fileSize / (V_thread_avg * T_min))
	nTmin64 := ceilDivPositive(p.FileSize, saturatingMulPositive(vThreadAvg, int64(tMin)))

	// floor: 拥塞时 1~2，其它 1
	floor := 1
	if vAvailable <= 0 || (globalPeakOK && vAvailable < vGlobalPeak/10) {
		floor = congestionFloor
	}

	nFinal64 := min(nSat64, nTmin64)
	nFinal64 = clampToConn(nFinal64, floor, maxConn)

	// --- 探索标记（重新引入冷启动保守防护） ---
	// 初见新域名时缺乏 BBR 历史指纹，为获取纯净的单线程效率（V_thread_avg）样本，
	// 强制限制初始线程数：最高不超过 min(max(W_max/4, 4), 8)。
	isExploration := !speedstats.HasDomainScopeEnvRecord(p.Domain, p.Scope, p.EnvKey)
	if isExploration {
		limit := explorationLimit(maxConn)
		if nFinal64 > int64(limit) {
			nFinal64 = int64(limit)
		}
	}
	nFinal := int(nFinal64)
	nSat := intFromNonNegative(nSat64)

	// --- 初始切分（蓝图 §2.1） ---
	// MinChunk = clamp(V_thread_avg * T_target_chunk, 1MB, 1GB)
	minChunk := min(max(saturatingMulPositive(vThreadAvg, tTargetChunk), minChunkSize), maxChunkSize)
	// MinChunk = min(MinChunk, fileSize / N_final)
	if nFinal > 0 {
		perWorker := p.FileSize / int64(nFinal)
		if perWorker < minChunk {
			minChunk = max(perWorker, minChunkSize)
		}
	}

	targetBandwidth := saturatingMulPositive(vThreadAvg, int64(nFinal))

	return ThreadParams{
		Split:           nFinal,
		MinSize:         minChunk,
		IsExploration:   isExploration,
		TargetBandwidth: targetBandwidth,
		NSat:            nSat,
	}
}

// calculateLegacy is the fallback when BBR data is unavailable (cold start).
// Uses the old N_tmin-only heuristic without bandwidth floor.
func calculateLegacy(fileSize int64, maxConnections int, domain, scope, envKey string, tMin int) ThreadParams {
	vSingleEst := max(
		// 默认 2MB/s
		int64(2*1024*1024), minThreadEfficiency)

	nLimit := ceilDivPositive(fileSize, saturatingMulPositive(vSingleEst, int64(tMin)))
	nFinal64 := clampToConn(nLimit, 1, maxConnections)

	// --- 探索标记（重新引入冷启动保守防护） ---
	isExploration := !speedstats.HasDomainScopeEnvRecord(domain, scope, envKey)
	if isExploration {
		limit := explorationLimit(maxConnections)
		if nFinal64 > int64(limit) {
			nFinal64 = int64(limit)
		}
	}
	nFinal := int(nFinal64)

	var minSize int64
	if nFinal > 0 {
		minSize = int64(float64(fileSize) / float64(nFinal) * 0.99)
		if minSize < minChunkSize {
			minSize = minChunkSize
		} else if minSize > maxChunkSize {
			minSize = maxChunkSize
		}
	}

	return ThreadParams{
		Split:           nFinal,
		MinSize:         minSize,
		IsExploration:   isExploration,
		TargetBandwidth: vSingleEst * int64(nFinal),
		NSat:            intFromNonNegative(nLimit),
	}
}

func ceilDiv(a, b int64) int64 {
	return ceilDivPositive(a, b)
}

func explorationLimit(maxConnections int) int {
	return min(max(maxConnections/4, 4), 8)
}

func ceilDivPositive(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	return 1 + (a-1)/b
}

func saturatingMulPositive(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

func clampToConn(n int64, floor, maxConn int) int64 {
	out := max(n, int64(floor))
	if maxConn > 0 && out > int64(maxConn) {
		out = int64(maxConn)
	}
	if out < 1 && floor >= 1 {
		out = 1
	}
	if out < 0 {
		out = 0
	}
	return out
}

func intFromNonNegative(n int64) int {
	if n <= 0 {
		return 0
	}
	if n > math.MaxInt {
		return math.MaxInt
	}
	return int(n)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// limitKey constructs the aggregation key for N_max domain-level operations.
// Format: scope + "|" + domain. Callers must guard against empty scope/domain
// before calling — this function is pure and does not filter empty values.
func limitKey(scope, domain string) string {
	return scope + "|" + domain
}

// approvedScopeKey is the tick-local approvedDelta key for scope headroom.
// Format: scope + "|" + envKey (never bare concat).
func approvedScopeKey(scope, envKey string) string {
	return scope + "|" + envKey
}

// approvedDomainKey is the tick-local approvedDelta / domainMacroBps key for
// bandwidth gates. Format: scope + "|" + domain + "|" + envKey — env-aware.
func approvedDomainKey(scope, domain, envKey string) string {
	return scope + "|" + domain + "|" + envKey
}

// approvedNMaxKey is the tick-local approvedDelta key for N_max pending counts.
// Env-blind like limitKey (scope|domain). Prefixed so it cannot collide with
// approvedScopeKey when envKey equals a domain string.
func approvedNMaxKey(scope, domain string) string {
	return "nmax|" + scope + "|" + domain
}

// ClampToServerLimit applies the per-domain N_max server limit to Calculate's
// output. If the domain has a non-expired N_max and params.Split would push
// the domain total above N_max, Split is clamped to max(1, nMax - existingDomainWorkers),
// MinSize is adjusted so per-worker size stays valid, and TargetBandwidth is
// scaled proportionally. Calculate itself stays a pure function with no Store
// dependency.
func ClampToServerLimit(params ThreadParams, fileSize int64, scope, domain string, existingDomainWorkers int, store *ServerLimitStore) ThreadParams {
	if store == nil {
		return params
	}
	if scope == "" || domain == "" {
		return params
	}
	lk := limitKey(scope, domain)
	nMax, hasLimit := store.GetNMax(lk)
	if !hasLimit || nMax <= 0 {
		return params
	}

	allowed := max(nMax-existingDomainWorkers, 1)
	if params.Split <= allowed {
		return params
	}

	oldSplit := params.Split
	params.Split = allowed

	if fileSize > 0 && allowed > 0 {
		perWorker := fileSize / int64(allowed)
		if perWorker < params.MinSize {
			params.MinSize = max(perWorker, minChunkSize)
		}
	}

	if oldSplit > 0 && params.TargetBandwidth > 0 {
		params.TargetBandwidth = params.TargetBandwidth * int64(allowed) / int64(oldSplit)
	}

	return params
}
