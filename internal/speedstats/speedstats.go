package speedstats

import (
	"bytes"
	"encoding/gob"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

func init() {
	gob.Register(SpeedRecord{})
}

const (
	maxRecords  = 100              // 最多保留记录数
	MinFileSize = 50 * 1024 * 1024 // 仅记录 >50MB 的下载
	recentDays  = 3                // GetRecentPeak 只看最近 3 天
)

// SpeedRecord 记录单次大文件下载的峰值数据
type SpeedRecord struct {
	Timestamp     int64  `json:"timestamp"`      // Unix 时间戳
	PeakSpeed     int64  `json:"peak_speed"`     // 峰值速度 (bytes/s)
	ThreadCount   int    `json:"thread_count"`   // 达成该速度时使用的线程数
	FileSize      int64  `json:"file_size"`      // 文件大小
	IsExploration bool   `json:"is_exploration"` // 是否为探索任务
	TTFBMs        int64  `json:"ttfb_ms"`        // 首字节时间 (ms)
	Domain        string `json:"domain"`         // 下载域名
	Scope         string `json:"scope"`          // wan/lan 分类
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

// getStatsPath returns the path to speed_stats.gob
func getStatsPath() string {
	if statsPath != "" {
		return statsPath
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".goaria")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "speed_stats.gob")
}

// Load reads speed stats from disk
func Load() {
	mu.Lock()
	defer mu.Unlock()

	records = []SpeedRecord{}
	data, err := os.ReadFile(getStatsPath())
	if err == nil {
		_ = gob.NewDecoder(bytes.NewReader(data)).Decode(&records)
	}
}

// Save writes speed stats to disk
func Save() error {
	mu.RLock()
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(records)
	mu.RUnlock()

	if err != nil {
		return err
	}
	return writeToDisk(buf.Bytes())
}

func writeToDisk(data []byte) error {
	saveFileMu.Lock()
	defer saveFileMu.Unlock()
	return os.WriteFile(getStatsPath(), data, 0o644)
}

// AddRecord 添加一条新的速度记录（向后兼容，委托 AddRecordV2）
func AddRecord(peakSpeed int64, threadCount int, fileSize int64, isExploration bool) {
	AddRecordV2(peakSpeed, threadCount, fileSize, isExploration, 0, "", "")
}

// AddRecordV2 添加一条新的速度记录（含 TTFB/domain/scope）
func AddRecordV2(peakSpeed int64, threadCount int, fileSize int64, isExploration bool, ttfbMs int64, domain string, scope string) {
	if peakSpeed <= 0 || threadCount <= 0 {
		return
	}
	if scope == "" {
		scope = "wan"
	}

	mu.Lock()
	defer mu.Unlock()

	records = append(records, SpeedRecord{
		Timestamp:     time.Now().Unix(),
		PeakSpeed:     peakSpeed,
		ThreadCount:   threadCount,
		FileSize:      fileSize,
		IsExploration: isExploration,
		TTFBMs:        ttfbMs,
		Domain:        domain,
		Scope:         scope,
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
	return GetRecentPeakByScope("")
}

// GetRecentPeakByScope 与 GetRecentPeak 逻辑相同但加 scope 过滤
// scope 为空时不过滤（等价于 GetRecentPeak）
func GetRecentPeakByScope(scope string) (vSingleEst int64, ok bool) {
	mu.RLock()
	defer mu.RUnlock()

	if len(records) == 0 {
		return 0, false
	}

	cutoff := time.Now().Add(-time.Duration(recentDays) * 24 * time.Hour).Unix()

	// 找出最近 3 天的记录并计算单线程效率 V
	var vValues []int64

	for i := range records {
		if scope != "" && records[i].Scope != scope {
			continue
		}
		if records[i].Timestamp >= cutoff && records[i].ThreadCount > 0 {
			eff := records[i].PeakSpeed / int64(records[i].ThreadCount)
			vValues = append(vValues, eff)
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
	// 不再盲目信任所有高分，只有当"探索任务"显著超越基准时，才采纳为新标杆
	// 验证阈值：探索效率 >= 基准 * 1.5
	var verifiedBenchmark int64

	// 重新遍历所有记录寻找成功的探索任务（按 scope 过滤）
	// records 上限 100 条，全量遍历开销可接受
	for i := range records {
		if scope != "" && records[i].Scope != scope {
			continue
		}
		r := records[i]
		if r.Timestamp < cutoff {
			continue
		}
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

// GetGlobalPeak 返回最近 recentDays 内指定 scope 的总峰值速度 (bytes/s)
// scope 为空时不过滤
func GetGlobalPeak(scope string) (int64, bool) {
	mu.RLock()
	defer mu.RUnlock()

	cutoff := time.Now().Add(-time.Duration(recentDays) * 24 * time.Hour).Unix()
	var peak int64
	found := false
	for i := range records {
		if records[i].Timestamp < cutoff {
			continue
		}
		if scope != "" && records[i].Scope != scope {
			continue
		}
		if records[i].PeakSpeed > peak {
			peak = records[i].PeakSpeed
			found = true
		}
	}
	return peak, found
}

// GetDomainPeak 返回指定 domain 的历史最高峰值速度 (bytes/s)
func GetDomainPeak(domain string) (int64, bool) {
	mu.RLock()
	defer mu.RUnlock()

	var peak int64
	found := false
	for i := range records {
		if records[i].Domain != domain {
			continue
		}
		if records[i].PeakSpeed > peak {
			peak = records[i].PeakSpeed
			found = true
		}
	}
	return peak, found
}

// GetRTprop 返回指定 domain 的 TTFB 最小值 (ms)
// 跳过 TTFBMs=0 的记录；无 domain 匹配时返回全局 TTFB 最小值
func GetRTprop(domain string) (int64, bool) {
	mu.RLock()
	defer mu.RUnlock()

	var minTTFB int64
	found := false

	// 先尝试 domain 匹配（空 domain 跳过，直接走全局回退）
	if domain != "" {
		for i := range records {
			if records[i].Domain != domain || records[i].TTFBMs <= 0 {
				continue
			}
			if !found || records[i].TTFBMs < minTTFB {
				minTTFB = records[i].TTFBMs
				found = true
			}
		}
	}
	if found {
		return minTTFB, true
	}

	// 回退到全局 TTFB 最小值
	for i := range records {
		if records[i].TTFBMs <= 0 {
			continue
		}
		if !found || records[i].TTFBMs < minTTFB {
			minTTFB = records[i].TTFBMs
			found = true
		}
	}
	return minTTFB, found
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
