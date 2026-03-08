package monitor

import (
	"path/filepath"
	"sync"
	"time"

	"goaria-v3/internal/rpc"
)

// TaskCache 任务状态缓存
// 设计原则：
// 1. 批量：一次 Aria2 轮询获取所有任务，缓存完整状态
// 2. 去重：相同 GID 的任务只保留最新状态
// 3. 预取：任务添加/暂停时立即获取完整元数据
type TaskCache struct {
	mu sync.RWMutex

	// 完整任务缓存（包含元数据）
	active  []rpc.Task
	waiting []rpc.Task
	stopped []rpc.Task

	// 元数据缓存（用于暂停任务的文件名等信息）
	metadata map[string]*TaskMetadata

	// 最后更新时间
	lastUpdate time.Time
}

// TaskMetadata 任务元数据（预取缓存）
type TaskMetadata struct {
	GID         string
	Title       string   // 文件名
	Dir         string   // 下载目录
	TotalLength int64    // 总大小
	Files       []string // 文件路径列表
	SourceURL   string   // 来源 URL
	FetchedAt   time.Time
}

// Cache 全局缓存实例
var Cache = &TaskCache{
	metadata: make(map[string]*TaskMetadata),
}

// UpdateFromAria2 从 Aria2 更新缓存（批量获取）
// 注意：此方法仅更新任务列表，不再自动调用 ensureMetadata
// 元数据预取由 Monitor.tick() 显式处理，避免 Lite 任务污染缓存
func (c *TaskCache) UpdateFromAria2(active, waiting, stopped []rpc.Task) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.active = active
	c.waiting = waiting
	c.stopped = stopped
	c.lastUpdate = time.Now()
}

// ensureMetadata 确保任务元数据已缓存（预取）
// 注意：仅当任务包含有效的文件信息时才缓存，避免 Lite 任务污染缓存
func (c *TaskCache) ensureMetadata(task rpc.Task) {
	if _, exists := c.metadata[task.GID]; exists {
		return
	}

	// 关键检查：如果 Files 为空，说明是 Lite 任务或元数据不完整
	// 此时不应缓存，避免污染 metadata cache
	if len(task.Files) == 0 {
		return
	}

	meta := &TaskMetadata{
		GID:         task.GID,
		Dir:         task.Dir,
		TotalLength: parseInt64(task.TotalLength),
		FetchedAt:   time.Now(),
	}

	meta.Title = filepath.Base(task.Files[0].Path)
	for _, f := range task.Files {
		meta.Files = append(meta.Files, f.Path)
	}
	if len(task.Files[0].Uris) > 0 {
		meta.SourceURL = task.Files[0].Uris[0].Uri
	}

	c.metadata[task.GID] = meta
}

// GetActive 获取活跃任务（从缓存）
func (c *TaskCache) GetActive() []rpc.Task {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.active
}

// GetWaiting 获取等待任务（从缓存）
func (c *TaskCache) GetWaiting() []rpc.Task {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.waiting
}

// GetStopped 获取已停止任务（从缓存）
func (c *TaskCache) GetStopped() []rpc.Task {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stopped
}

// GetMetadata 获取任务元数据（用于 UI 展示）
func (c *TaskCache) GetMetadata(gid string) *TaskMetadata {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.metadata[gid]
}

// EnrichTasks 批量丰富任务信息（使用缓存的元数据）
// 优化：一次性获取锁，避免循环中重复获取锁
func (c *TaskCache) EnrichTasks(tasks []rpc.Task) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for i := range tasks {
		meta := c.metadata[tasks[i].GID]
		if meta != nil {
			tasks[i].Title = meta.Title
			// 构造一个包含首个文件信息的 Files 列表，满足前端和 Tracker 的基本需求
			if len(meta.Files) > 0 {
				tasks[i].Files = []rpc.File{
					{
						Path: meta.Files[0],
						Uris: []rpc.Uri{{Uri: meta.SourceURL}},
					},
				}
			}
		}
	}
}

// HasValidMetadata 检查任务是否有有效的元数据（包含文件信息）
// 用于检测被污染的缓存条目并触发重试
func (c *TaskCache) HasValidMetadata(gid string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	meta := c.metadata[gid]
	return meta != nil && len(meta.Files) > 0
}

// InvalidateMetadata 删除指定任务的元数据（用于强制重新获取）
func (c *TaskCache) InvalidateMetadata(gid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.metadata, gid)
}

func (c *TaskCache) PrefetchMetadataMulti(gids []string) {
	if len(gids) == 0 {
		return
	}

	seen := make(map[string]struct{}, len(gids))
	uniqueGids := make([]string, 0, len(gids))
	for _, gid := range gids {
		if gid == "" {
			continue
		}
		if _, exists := seen[gid]; exists {
			continue
		}
		seen[gid] = struct{}{}
		uniqueGids = append(uniqueGids, gid)
	}

	tasks, err := rpc.TellStatusMulti(uniqueGids)
	if err != nil || len(tasks) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for _, task := range tasks {
		if task != nil {
			c.ensureMetadata(*task)
		}
	}
}

// PrefetchMetadata 强制预取指定任务的元数据
// 用于任务添加后立即获取完整信息
func (c *TaskCache) PrefetchMetadata(gid string) {
	// 从 Aria2 获取单个任务的完整信息
	task, err := rpc.TellStatus(gid)
	if err != nil || task == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureMetadata(*task)
}

// CleanupMetadata 清理已移除任务的元数据
func (c *TaskCache) CleanupMetadata(activeGids map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for gid := range c.metadata {
		if !activeGids[gid] {
			delete(c.metadata, gid)
		}
	}
}

// GetLastUpdate 获取最后更新时间
func (c *TaskCache) GetLastUpdate() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastUpdate
}
