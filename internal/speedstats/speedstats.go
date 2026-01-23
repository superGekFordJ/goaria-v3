package speedstats

import (
	"encoding/json"
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
	Timestamp   int64 `json:"timestamp"`    // Unix 时间戳
	PeakSpeed   int64 `json:"peak_speed"`   // 峰值速度 (bytes/s)
	ThreadCount int   `json:"thread_count"` // 使用的线程数
	FileSize    int64 `json:"file_size"`    // 文件大小 (bytes)
}

var (
	records []SpeedRecord
	mu      sync.RWMutex
)

// getStatsPath returns the path to speed_stats.json
func getStatsPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".goaria")
	_ = os.MkdirAll(dir, 0755)
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
	mu.Lock()
	defer mu.Unlock()

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(getStatsPath(), data, 0644)
}

// AddRecord 添加一条下载峰值记录
// 仅当 fileSize > 50MB 时才记录
func AddRecord(peakSpeed int64, threadCount int, fileSize int64) {
	if fileSize < minFileSize {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	record := SpeedRecord{
		Timestamp:   time.Now().Unix(),
		PeakSpeed:   peakSpeed,
		ThreadCount: threadCount,
		FileSize:    fileSize,
	}

	records = append(records, record)

	// 超出限制时删除最旧的
	if len(records) > maxRecords {
		records = records[len(records)-maxRecords:]
	}

	go saveAsync()
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

	// 计算当前窗口内的最大效率作为标杆
	var maxWindowEff int64
	for _, v := range vValues {
		if v > maxWindowEff {
			maxWindowEff = v
		}
	}

	// 1. 计算中位数级别
	sort.Slice(vValues, func(i, j int) bool {
		return vValues[i] < vValues[j]
	})
	medianV := vValues[len(vValues)/2]

	// 2. 进化机制：如果最近出现了极高效率的“探测成功”样本，即使是少数，也应作为标杆
	// 我们取（中位数）和（当前窗口高点 * 0.9）的较大者
	// 限制在当前窗口 (maxWindowEff) 而非历史最大值，保证了环境切换时的快速衰减
	finalV := medianV
	if maxWindowEff*9/10 > finalV {
		finalV = maxWindowEff * 9 / 10
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
		go saveAsync()
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

// saveAsync saves stats in background
func saveAsync() {
	mu.RLock()
	data, err := json.MarshalIndent(records, "", "  ")
	mu.RUnlock()

	if err == nil {
		_ = os.WriteFile(getStatsPath(), data, 0644)
	}
}
