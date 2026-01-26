package smartthread

import (
	"hash/fnv"

	"goaria-v3/internal/config"
	"goaria-v3/internal/speedstats"
)

const (
	defaultSingleSpeed   = 2 * 1024 * 1024 // 默认单线程速度 2MB/s
	defaultMinThreadLife = 5               // 默认最小生存时间 5 秒
)

// ThreadParams 计算结果
type ThreadParams struct {
	Split         int   // -x 参数：线程数
	MinSize       int64 // -k 参数：最小切分大小 (bytes)
	IsExploration bool  // 是否触发了探索模式
}

// Calculate 根据文件大小计算最优线程参数
// fileSize: 文件大小 (bytes)，0 表示未知
// maxConnections: 全局最大线程数上限 (用户设置)
// url: 用于确定性探索的 URL
// 返回计算后的线程参数
func Calculate(fileSize int64, maxConnections int, url string) ThreadParams {
	// 获取 T_min
	tMin := config.Current.MinThreadLife
	if tMin <= 0 {
		tMin = defaultMinThreadLife
	}

	// 获取历史峰值数据 (单线程效率评估)
	vSingleEst, ok := speedstats.GetRecentPeak()
	if !ok {
		// 无历史数据，使用默认值
		vSingleEst = defaultSingleSpeed
	}

	// 确保估算速度至少为 100KB/s，避免除零或过小值
	if vSingleEst < 100*1024 {
		vSingleEst = 100 * 1024
	}

	var nFinal int
	var isExploration bool

	if fileSize <= 0 {
		// 文件大小未知，使用保守策略（最大线程数）
		nFinal = maxConnections
	} else {
		// N_limit = ceil(fileSize / (V_single_est * T_min))
		nLimit := (fileSize + (vSingleEst * int64(tMin)) - 1) / (vSingleEst * int64(tMin))

		// N = max(1, min(N_limit, maxConnections))
		nFinal = int(nLimit)
		if nFinal < 1 {
			nFinal = 1
		}
		if nFinal > maxConnections {
			nFinal = maxConnections
		}

		// 探索机制 (Exploration)
		// 10% 的概率尝试使用一半的线程，看看是否能维持同样的效率或更高效率
		// 卫语句：如果计算出的线程数已经是 1，则无需探索
		if nFinal > 1 && ShouldExplore(url) {
			nFinal = (nFinal + 1) / 2
			isExploration = true
			if nFinal < 1 {
				nFinal = 1
			}
		}
	}

	// 计算最小切分大小 k = floor(fileSize / N * 0.99)
	var minSize int64
	if fileSize > 0 && nFinal > 0 {
		minSize = int64(float64(fileSize) / float64(nFinal) * 0.99)
		// 确保 minSize 至少为 1MB
		if minSize < 1024*1024 {
			minSize = 1024 * 1024
		}
	}

	return ThreadParams{
		Split:         nFinal,
		MinSize:       minSize,
		IsExploration: isExploration,
	}
}

// ShouldExplore 确定性判定是否进行低并发探索
func ShouldExplore(url string) bool {
	h := fnv.New32a()
	h.Write([]byte(url))
	// 25% 概率触发探索 (平均每 4 个任务 1 个探索，确保 10 个任务的窗口内有足够的验证机会)
	return h.Sum32()%4 == 0
}
