package speedstats

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	maxRecords       = 100              // 最多保留记录数
	minFileSize      = 50 * 1024 * 1024 // 仅记录 >50MB 的下载
	recentDays       = 3                // GetRecentPeak 只看最近 3 天
	defaultPeakSpeed = 2 * 1024 * 1024  // 默认峰值速度 2MB/s
)

// SpeedRecord 记录单次大文件下载的峰值数据
type SpeedRecord struct {
	Timestamp     int64 `json:"timestamp"`      // Unix 时间戳
	PeakSpeed     int64 `json:"peak_speed"`     // 峰值速度 (bytes/s)
	ThreadCount   int   `json:"thread_count"`   // 达成该速度时使用的线程数
	FileSize      int64 `json:"file_size"`      // 文件大小
	IsExploration bool  `json:"is_exploration"` // 是否为探索任务
}

// 全局变量
var (
	records []SpeedRecord
	mu      sync.RWMutex

	// Async save control
	saveTimer    *time.Timer
	saveTimerMu  sync.Mutex
	saveFileMu   sync.Mutex
	statsPath    string
	saveInterval = 1 * time.Second
)

// SetStatsPath overrides the default speed stats file path.
// This is primarily used for testing to isolate test data.
func SetStatsPath(path string) {
	statsPath = path
}

// SetSaveInterval configures the write coalescing interval.
// Default is 1 second. This is primarily used for testing.
func SetSaveInterval(d time.Duration) {
	saveInterval = d
}

// getStatsPath returns the path to speed_stats.json
func getStatsPath() string {
	if statsPath != "" {
		return statsPath
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".goaria")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "speed_stats.json")
}

// Load reads speed stats from disk
func Load() {
	mu.Lock()
	defer mu.Unlock()

	records = []SpeedRecord{}
	data, err := os.ReadFile(getStatsPath())
	if err == nil {
		_ = json.Unmarshal(data, &records)
	}
}

// Save writes speed stats to disk
func Save() error {
	mu.RLock()
	data, err := json.MarshalIndent(records, "", "  ")
	mu.RUnlock()

	if err != nil {
		return err
	}
	return writeToDisk(data)
}

func writeToDisk(data []byte) error {
	saveFileMu.Lock()
	defer saveFileMu.Unlock()
	return os.WriteFile(getStatsPath(), data, 0o644)
}

// AddRecord 添加一条新的速度记录
func AddRecord(peakSpeed int64, threadCount int, fileSize int64, isExploration bool) {
	if peakSpeed <= 0 || threadCount <= 0 {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	records = append(records, SpeedRecord{
		Timestamp:     time.Now().Unix(),
		PeakSpeed:     peakSpeed,
		ThreadCount:   threadCount,
		FileSize:      fileSize,
		IsExploration: isExploration,
	})

	// 超出限制时删除最旧的
	if len(records) > maxRecords {
		records = records[len(records)-maxRecords:]
	}

	saveAsync()
}

// GetRecentPeak 获取最近有效峰值（采用单线程开发效率中位数 + 标杆优先逻辑）
// 返回最近 3 天内的单线程能力评估值 (bytes/s)
func GetRecentPeak() (vSingleEst int64, ok bool) {
	mu.RLock()
	defer mu.RUnlock()

	if len(records) == 0 {
		return 0, false
	}

	cutoff := time.Now().Add(-time.Duration(recentDays) * 24 * time.Hour).Unix()

	// 找出最近 3 天的记录并计算单线程效率 V
	var vValues []int64
	var maxRecentEff int64

	for i := range records {
		if records[i].Timestamp >= cutoff && records[i].ThreadCount > 0 {
			eff := records[i].PeakSpeed / int64(records[i].ThreadCount)
			vValues = append(vValues, eff)
			if eff > maxRecentEff {
				maxRecentEff = eff
			}
		}
	}

	if len(vValues) == 0 {
		return 0, false
	}

	// 限制样本量为最近 10 条
	if len(vValues) > 10 {
		vValues = vValues[len(vValues)-10:]
	}

	// 1. 计算中位数基准 (Baseline)
	sort.Slice(vValues, func(i, j int) bool {
		return vValues[i] < vValues[j]
	})
	medianV := vValues[len(vValues)/2]

	// 2. 科学验证机制 (Scientific Validation)
	// 不再盲目信任所有高分，只有当“探索任务”显著超越基准时，才采纳为新标杆
	// 验证阈值：探索效率 >= 基准 * 1.5
	var verifiedBenchmark int64

	// 重新遍历窗口寻找成功的探索任务
	// 注意：我们需要原始记录来检查 IsExploration，上面的 vValues 丢失了该信息
	// 这里做一个简单的优化：在收集 vValues 时其实可以保留更多信息，或者再次遍历 recentRecords
	limit := 10
	startIdx := len(records) - limit
	if startIdx < 0 {
		startIdx = 0
	}

	for i := startIdx; i < len(records); i++ {
		r := records[i]
		if r.IsExploration && r.ThreadCount > 0 {
			eff := r.PeakSpeed / int64(r.ThreadCount)
			// 只有显著超越中位数的探索才算成功
			if eff >= medianV*3/2 { // eff >= median * 1.5
				if eff > verifiedBenchmark {
					verifiedBenchmark = eff
				}
			}
		}
	}

	// 决策：如果有验证成功的标杆，使用标杆（打九折以防万一）；否则坚持中位数
	finalV := medianV
	if verifiedBenchmark > 0 {
		// 采用成功探索的值，保留 10% 安全余量
		candidate := verifiedBenchmark * 9 / 10
		if candidate > finalV {
			finalV = candidate
		}
	}

	return finalV, true
}

// CleanExpired 清理过期数据
func CleanExpired(days int) {
	mu.Lock()
	defer mu.Unlock()

	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()

	filtered := records[:0]
	for _, r := range records {
		if r.Timestamp >= cutoff {
			filtered = append(filtered, r)
		}
	}

	if len(filtered) != len(records) {
		records = filtered
		saveAsync()
	}
}

// GetAllRecords 返回所有记录（用于调试）
func GetAllRecords() []SpeedRecord {
	mu.RLock()
	defer mu.RUnlock()

	result := make([]SpeedRecord, len(records))
	copy(result, records)

	// 按时间戳排序，最新的在前
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp > result[j].Timestamp
	})

	return result
}

// saveAsync saves stats in background with coalescing
func saveAsync() {
	saveTimerMu.Lock()
	defer saveTimerMu.Unlock()

	if saveTimer != nil {
		return
	}

	saveTimer = time.AfterFunc(saveInterval, func() {
		saveTimerMu.Lock()
		saveTimer = nil
		saveTimerMu.Unlock()

		if err := Save(); err != nil {
			log.Printf("[speedstats] Failed to save: %v", err)
		}
	})
}
