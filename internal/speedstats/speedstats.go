package speedstats

import (
	"bytes"
	"encoding/gob"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"
)

func init() {
	gob.Register(SpeedRecord{})
}

const (
	maxRecords  = 10000            // 最多保留记录数（网卡隔离需更多历史）
	MinFileSize = 50 * 1024 * 1024 // 仅记录 >50MB 的下载
	recentDays  = 365              // 跨季节数据，配合网卡隔离
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
	EnvKey        string `json:"env_key"`        // 网络环境指纹 (SHA-256 前 8 位 hex)
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

// Load reads speed stats from disk. Records with empty EnvKey (pre-upgrade) are silently dropped.
func Load() {
	mu.Lock()
	defer mu.Unlock()

	records = []SpeedRecord{}
	data, err := os.ReadFile(getStatsPath())
	if err == nil {
		var loaded []SpeedRecord
		_ = gob.NewDecoder(bytes.NewReader(data)).Decode(&loaded)
		for _, r := range loaded {
			if r.EnvKey == "" {
				continue // read-time scrubbing: drop pre-upgrade records
			}
			records = append(records, r)
		}
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
	AddRecordV2(peakSpeed, threadCount, fileSize, isExploration, 0, "", "", "")
}

// AddRecordV2 添加一条新的速度记录（含 TTFB/domain/scope/envKey）
func AddRecordV2(peakSpeed int64, threadCount int, fileSize int64, isExploration bool, ttfbMs int64, domain string, scope string, envKey string) {
	if peakSpeed <= 0 || threadCount <= 0 {
		return
	}
	if scope == "" {
		scope = "wan"
	}
	if envKey == "" {
		return // empty envKey is a dirty-data signal — skip recording
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
		EnvKey:        envKey,
	})

	// 超出限制时删除最旧的
	if len(records) > maxRecords {
		records = records[len(records)-maxRecords:]
	}

	saveAsync()
}

// GetRecentPeak 获取最近有效峰值（采用单线程效率 p75 + 标杆优先逻辑）
// 返回最近 recentDays 天内的单线程能力评估值 (bytes/s)
func GetRecentPeak() (vSingleEst int64, ok bool) {
	return GetRecentPeakByScope("", "")
}

// medianFallbackMinN is locked at 0: GetRecentPeak* always uses p75, never
// falls back to median for small sample counts.
const medianFallbackMinN = 0

// p75SortedAsc returns the p75 element of a non-empty ascending-sorted
// []int64 using nearest-rank index (n*3)/4.
// Small-sample contract: n<=2 identical to median index (n/2);
// n==3 or n==4 selects the maximum (intentional max-filter lean).
// Never falls back to median for any n (medianFallbackMinN locked at 0).
func p75SortedAsc(sorted []int64) int64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	return sorted[(n*3)/4]
}

// GetRecentPeakByScope returns the p75 per-thread efficiency (bytes/s) over the
// recentDays window for scope+envKey (scope "" = no scope filter; envKey never
// cross-env falls back). At most the last 100 matching samples are used.
func GetRecentPeakByScope(scope string, envKey string) (vSingleEst int64, ok bool) {
	mu.RLock()
	defer mu.RUnlock()

	if len(records) == 0 {
		return 0, false
	}

	cutoff := time.Now().Add(-time.Duration(recentDays) * 24 * time.Hour).Unix()

	// 找出最近 recentDays 天的记录并计算单线程效率 V
	var vValues []int64

	for i := range records {
		if scope != "" && records[i].Scope != scope {
			continue
		}
		if records[i].EnvKey != envKey {
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

	// 限制样本量为最近 100 条
	if len(vValues) > 100 {
		vValues = vValues[len(vValues)-100:]
	}

	// 计算 p75 基准 (Baseline)
	slices.Sort(vValues)
	return p75SortedAsc(vValues), true
}

// GetRecentPeakByDomain returns the p75 per-thread efficiency (bytes/s) over the
// recentDays window for domain+scope+envKey (empty domain → 0,false; scope "" =
// no scope filter; envKey never cross-env falls back). At most the last 100
// matching samples are used.
func GetRecentPeakByDomain(domain, scope string, envKey string) (vSingleEst int64, ok bool) {
	if domain == "" {
		return 0, false
	}

	mu.RLock()
	defer mu.RUnlock()

	if len(records) == 0 {
		return 0, false
	}

	cutoff := time.Now().Add(-time.Duration(recentDays) * 24 * time.Hour).Unix()

	var vValues []int64

	for i := range records {
		if records[i].Domain != domain {
			continue
		}
		if scope != "" && records[i].Scope != scope {
			continue
		}
		if records[i].EnvKey != envKey {
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

	if len(vValues) > 100 {
		vValues = vValues[len(vValues)-100:]
	}

	// 计算 p75 基准 (Baseline)
	slices.Sort(vValues)
	return p75SortedAsc(vValues), true
}

// GetGlobalPeak 返回最近 recentDays 内指定 scope+envKey 的总峰值速度 (bytes/s)
// scope 为空时不过滤；envKey 绝不跨 env 回退
func GetGlobalPeak(scope string, envKey string) (int64, bool) {
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
		if records[i].EnvKey != envKey {
			continue
		}
		if records[i].PeakSpeed > peak {
			peak = records[i].PeakSpeed
			found = true
		}
	}
	return peak, found
}

// GetDomainPeak 返回最近 recentDays 内指定 domain+scope 的历史最高峰值速度 (bytes/s)
// scope 为空时不过滤 scope。
// 此函数允许跨 env 回退到 scope-only：先按 domain+scope+envKey 过滤，无结果再按 domain+scope 聚合所有 env。
// 这是唯一允许跨 env 回退的函数（服务器侧属性，被 V_available 安全拦截）。
func GetDomainPeak(domain, scope string, envKey string) (int64, bool) {
	mu.RLock()
	defer mu.RUnlock()

	cutoff := time.Now().Add(-time.Duration(recentDays) * 24 * time.Hour).Unix()
	var peak int64
	found := false
	for i := range records {
		if records[i].Timestamp < cutoff {
			continue
		}
		if domain == "" || records[i].Domain != domain {
			continue
		}
		if scope != "" && records[i].Scope != scope {
			continue
		}
		if records[i].EnvKey != envKey {
			continue
		}
		if records[i].PeakSpeed > peak {
			peak = records[i].PeakSpeed
			found = true
		}
	}
	if found {
		return peak, true
	}

	// Fallback to scope-only (aggregate all envs) — only this function allows cross-env fallback
	for i := range records {
		if records[i].Timestamp < cutoff {
			continue
		}
		if domain == "" || records[i].Domain != domain {
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

// GetRTprop 返回最近 recentDays 内指定 domain+scope+envKey 的 TTFB 最小值 (ms)
// 跳过 TTFBMs=0 的记录；无 domain 匹配时返回同 scope+envKey 的 TTFB 最小值
// scope 为空时回退到全局 TTFB 最小值；envKey 绝不跨 env 回退
func GetRTprop(domain, scope string, envKey string) (int64, bool) {
	mu.RLock()
	defer mu.RUnlock()

	cutoff := time.Now().Add(-time.Duration(recentDays) * 24 * time.Hour).Unix()
	var minTTFB int64
	found := false

	// 先尝试 domain+scope+envKey 匹配（空 domain 跳过，直接走回退）
	if domain != "" {
		for i := range records {
			if records[i].Timestamp < cutoff {
				continue
			}
			if records[i].Domain != domain || records[i].TTFBMs <= 0 {
				continue
			}
			if scope != "" && records[i].Scope != scope {
				continue
			}
			if records[i].EnvKey != envKey {
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

	// 回退到同 scope+envKey 的 TTFB 最小值
	for i := range records {
		if records[i].Timestamp < cutoff {
			continue
		}
		if records[i].TTFBMs <= 0 {
			continue
		}
		if scope != "" && records[i].Scope != scope {
			continue
		}
		if records[i].EnvKey != envKey {
			continue
		}
		if !found || records[i].TTFBMs < minTTFB {
			minTTFB = records[i].TTFBMs
			found = true
		}
	}
	return minTTFB, found
}

// HasDomainScopeEnvRecord returns true if at least one record exists within
// recentDays matching the given domain, scope, and envKey. Used by the exploration
// mechanism to determine if a domain+scope+env combination is "known".
// envKey 绝不跨 env 回退（无数据→isExploration=true）。
func HasDomainScopeEnvRecord(domain, scope string, envKey string) bool {
	mu.RLock()
	defer mu.RUnlock()

	cutoff := time.Now().Add(-time.Duration(recentDays) * 24 * time.Hour).Unix()
	for i := range records {
		if records[i].Timestamp < cutoff {
			continue
		}
		if domain != "" && records[i].Domain != domain {
			continue
		}
		if scope != "" && records[i].Scope != scope {
			continue
		}
		if records[i].EnvKey != envKey {
			continue
		}
		return true
	}
	return false
}

// HasDomainScopeRecord is a backward-compatible wrapper that delegates to
// HasDomainScopeEnvRecord with an empty envKey. Pre-upgrade records with
// empty EnvKey have been scrubbed on Load, so this always returns false
// for the legacy call path. Prefer HasDomainScopeEnvRecord for new code.
func HasDomainScopeRecord(domain, scope string) bool {
	return HasDomainScopeEnvRecord(domain, scope, "")
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
