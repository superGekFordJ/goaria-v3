package smartthread

import (
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
			exploreLimit := maxConn / 4
			if exploreLimit < 4 {
				exploreLimit = 4
			}
			if split > exploreLimit {
				split = exploreLimit
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
	tMin := config.Current.MinThreadLife
	if tMin <= 0 {
		tMin = defaultMinThreadLife
	}

	// --- 数据采集 ---
	// Tier 1: Domain-specific median (preferred — no cross-CDN pollution)
	vThreadAvg, threadAvgOK := speedstats.GetRecentPeakByDomain(p.Domain, p.Scope, p.EnvKey)

	// Tier 2: Fallback to scope-wide median with 0.5x conservative penalty
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
	vAvailable := vGlobalPeak - p.ReservedBandwidth
	if vAvailable < 0 {
		vAvailable = 0
	}

	// V_target = min(V_single_peak, V_available)
	// V_single_peak 无（新域名）→ V_target = V_available
	var vTarget int64
	if singlePeakOK {
		vTarget = min64(vSinglePeak, vAvailable)
	} else {
		vTarget = vAvailable
	}

	// --- 并发数计算 ---
	// N_sat = ceil(V_target / V_thread_avg) + gamma
	var nSat int
	if vThreadAvg > 0 {
		nSat = int(ceilDiv(vTarget, vThreadAvg)) + gamma
	}

	// N_tmin = ceil(fileSize / (V_thread_avg * T_min))
	nTmin := int(ceilDiv(p.FileSize, vThreadAvg*int64(tMin)))

	// floor: 拥塞时 1~2，其它 1
	floor := 1
	if vAvailable <= 0 || (globalPeakOK && vAvailable < vGlobalPeak/10) {
		floor = congestionFloor
	}

	// N_final = clamp(min(N_sat, N_tmin, MaxConnections), floor, MaxConnections)
	nFinal := minInt(minInt(nSat, nTmin), maxConn)
	if nFinal < floor {
		nFinal = floor
	}
	if nFinal > maxConn {
		nFinal = maxConn
	}
	if nFinal < 1 {
		nFinal = 1
	}

	// --- 探索标记（重新引入冷启动保守防护） ---
	// 初见新域名时缺乏 BBR 历史指纹，为获取纯净的单线程效率（V_thread_avg）样本，
	// 强制限制初始线程数：最高不超过 1/4 MaxConnections，且不低于 4 线程。
	isExploration := !speedstats.HasDomainScopeEnvRecord(p.Domain, p.Scope, p.EnvKey)
	if isExploration {
		exploreLimit := maxConn / 4
		if exploreLimit < 4 {
			exploreLimit = 4
		}
		if nFinal > exploreLimit {
			nFinal = exploreLimit
		}
	}

	// --- 初始切分（蓝图 §2.1） ---
	// MinChunk = clamp(V_thread_avg * T_target_chunk, 1MB, 1GB)
	minChunk := vThreadAvg * tTargetChunk
	if minChunk < minChunkSize {
		minChunk = minChunkSize
	}
	if minChunk > maxChunkSize {
		minChunk = maxChunkSize
	}
	// MinChunk = min(MinChunk, fileSize / N_final)
	if nFinal > 0 {
		perWorker := p.FileSize / int64(nFinal)
		if perWorker < minChunk {
			minChunk = perWorker
			if minChunk < minChunkSize {
				minChunk = minChunkSize
			}
		}
	}

	targetBandwidth := vThreadAvg * int64(nFinal)

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
	vSingleEst := int64(2 * 1024 * 1024) // 默认 2MB/s
	if vSingleEst < minThreadEfficiency {
		vSingleEst = minThreadEfficiency
	}

	nLimit := ceilDiv(fileSize, vSingleEst*int64(tMin))
	nFinal := int(nLimit)
	if nFinal < 1 {
		nFinal = 1
	}
	if nFinal > maxConnections {
		nFinal = maxConnections
	}

	// --- 探索标记（重新引入冷启动保守防护） ---
	isExploration := !speedstats.HasDomainScopeEnvRecord(domain, scope, envKey)
	if isExploration {
		exploreLimit := maxConnections / 4
		if exploreLimit < 4 {
			exploreLimit = 4
		}
		if nFinal > exploreLimit {
			nFinal = exploreLimit
		}
	}

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
		NSat:            int(nLimit),
	}
}

func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func min64(a, b int64) int64 {
	if a < b {
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

// ClampToServerLimit applies the per-domain N_max server limit to Calculate's
// output. If the domain has a non-expired N_max and params.Split exceeds it,
// Split is clamped to nMax, MinSize is adjusted so per-worker size stays valid,
// and TargetBandwidth is scaled proportionally. Calculate itself stays a pure
// function with no Store dependency.
func ClampToServerLimit(params ThreadParams, fileSize int64, domain string, store *ServerLimitStore) ThreadParams {
	if store == nil {
		return params
	}
	nMax, hasLimit := store.GetNMax(domain)
	if !hasLimit || nMax <= 0 || params.Split <= nMax {
		return params
	}

	oldSplit := params.Split
	params.Split = nMax

	// When split decreases, per-worker size grows, so MinSize never needs to go
	// up. But Calculate may have raised MinSize to the minChunkSize floor above
	// the new per-worker size; lower it to keep per-worker >= MinSize, clamped
	// to minChunkSize so the engine still accepts the split.
	if fileSize > 0 && nMax > 0 {
		perWorker := fileSize / int64(nMax)
		if perWorker < params.MinSize {
			params.MinSize = perWorker
			if params.MinSize < minChunkSize {
				params.MinSize = minChunkSize
			}
		}
	}

	// Scale TargetBandwidth proportionally so the per-worker bandwidth
	// expectation (and thus ledger Reserve) stays consistent with the new split.
	if oldSplit > 0 && params.TargetBandwidth > 0 {
		params.TargetBandwidth = params.TargetBandwidth * int64(nMax) / int64(oldSplit)
	}

	return params
}
