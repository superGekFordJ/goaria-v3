package monitor

import (
	"path/filepath"
	"strings"
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

	engine rpc.DownloadEngine

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
	GID           string
	Title         string   // 文件名
	Dir           string   // 下载目录
	TotalLength   int64    // 总大小
	Files         []string // 文件路径列表
	SourceURL     string   // 来源 URL
	DownloadGroup *rpc.DownloadGroup
	FetchedAt     time.Time
}

func metadataPathValid(path string) bool {
	return strings.TrimSpace(path) != ""
}

func metadataHasValidPath(meta *TaskMetadata) bool {
	if meta == nil {
		return false
	}
	for _, path := range meta.Files {
		if metadataPathValid(path) {
			return true
		}
	}
	return false
}

func copyDownloadGroup(group *rpc.DownloadGroup) *rpc.DownloadGroup {
	if group == nil {
		return nil
	}
	copy := *group
	return &copy
}

func copyTaskMetadata(meta *TaskMetadata) *TaskMetadata {
	if meta == nil {
		return nil
	}
	copy := *meta
	if len(meta.Files) > 0 {
		copy.Files = append([]string(nil), meta.Files...)
	}
	copy.DownloadGroup = copyDownloadGroup(meta.DownloadGroup)
	return &copy
}

func copyTaskSlice(tasks []rpc.Task) []rpc.Task {
	if len(tasks) == 0 {
		return []rpc.Task{}
	}
	copy := make([]rpc.Task, len(tasks))
	for i := range tasks {
		copy[i] = copyTask(tasks[i])
	}
	return copy
}

func copyTask(task rpc.Task) rpc.Task {
	copy := task
	copy.DownloadGroup = copyDownloadGroup(task.DownloadGroup)
	if len(task.Files) > 0 {
		copy.Files = make([]rpc.File, len(task.Files))
		for i, file := range task.Files {
			copy.Files[i] = file
			if len(file.Uris) > 0 {
				copy.Files[i].Uris = append([]rpc.Uri(nil), file.Uris...)
			}
		}
	}
	return copy
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

	c.active = copyTaskSlice(active)
	c.waiting = copyTaskSlice(waiting)
	c.stopped = copyTaskSlice(stopped)
	c.lastUpdate = time.Now()
}

// ensureMetadata 确保任务元数据已缓存（预取）
// 注意：仅当任务包含有效的文件信息时才缓存，避免 Lite 任务污染缓存
func (c *TaskCache) ensureMetadata(task rpc.Task) string {
	meta := c.metadata[task.GID]
	metadataWasValid := metadataHasValidPath(meta)
	groupKey := ""
	if task.DownloadGroup != nil {
		if meta == nil {
			meta = &TaskMetadata{GID: task.GID, FetchedAt: time.Now()}
			c.metadata[task.GID] = meta
		}
		meta.DownloadGroup = copyDownloadGroup(task.DownloadGroup)
		groupKey = strings.TrimSpace(task.DownloadGroup.ID)
		if meta.FetchedAt.IsZero() {
			meta.FetchedAt = time.Now()
		}
	} else if meta != nil && meta.DownloadGroup != nil {
		groupKey = strings.TrimSpace(meta.DownloadGroup.ID)
	}
	if metadataWasValid {
		return ""
	}

	// 关键检查：如果 Files 为空或只有空路径，说明是 Lite 任务或元数据不完整
	// 此时不应缓存，避免污染 metadata cache
	if len(task.Files) == 0 {
		return ""
	}
	validFiles := make([]rpc.File, 0, len(task.Files))
	for _, f := range task.Files {
		if metadataPathValid(f.Path) {
			validFiles = append(validFiles, f)
		}
	}
	if len(validFiles) == 0 {
		return ""
	}

	group := copyDownloadGroup(task.DownloadGroup)
	if group == nil && meta != nil {
		group = copyDownloadGroup(meta.DownloadGroup)
	}
	meta = &TaskMetadata{
		GID:           task.GID,
		Dir:           task.Dir,
		TotalLength:   parseInt64(task.TotalLength),
		DownloadGroup: group,
		FetchedAt:     time.Now(),
	}

	meta.Title = filepath.Base(validFiles[0].Path)
	for _, f := range validFiles {
		meta.Files = append(meta.Files, f.Path)
	}
	if len(validFiles[0].Uris) > 0 {
		meta.SourceURL = validFiles[0].Uris[0].Uri
	}

	c.metadata[task.GID] = meta
	if groupKey == "" && meta.DownloadGroup != nil {
		groupKey = strings.TrimSpace(meta.DownloadGroup.ID)
	}
	return groupKey
}

// GetActive 获取活跃任务（从缓存）
func (c *TaskCache) GetActive() []rpc.Task {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copyTaskSlice(c.active)
}

// GetWaiting 获取等待任务（从缓存）
func (c *TaskCache) GetWaiting() []rpc.Task {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copyTaskSlice(c.waiting)
}

// GetStopped 获取已停止任务（从缓存）
func (c *TaskCache) GetStopped() []rpc.Task {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copyTaskSlice(c.stopped)
}

// GetMetadata 获取任务元数据（用于 UI 展示）
func (c *TaskCache) GetMetadata(gid string) *TaskMetadata {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copyTaskMetadata(c.metadata[gid])
}

// EnrichTasks 批量丰富任务信息（使用缓存的元数据）
// 优化：一次性获取锁，避免循环中重复获取锁
func (c *TaskCache) EnrichTasks(tasks []rpc.Task) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for i := range tasks {
		meta := c.metadata[tasks[i].GID]
		if meta != nil && meta.DownloadGroup != nil {
			tasks[i].DownloadGroup = copyDownloadGroup(meta.DownloadGroup)
		} else if tasks[i].DownloadGroup == nil {
			tasks[i].DownloadGroup = GetStoredTaskGroup(tasks[i].GID)
		}
		if metadataHasValidPath(meta) {
			tasks[i].Title = meta.Title
			// 构造一个包含首个文件信息的 Files 列表，满足前端和 Tracker 的基本需求
			if len(meta.Files) > 0 {
				path := ""
				for _, candidate := range meta.Files {
					if metadataPathValid(candidate) {
						path = candidate
						break
					}
				}
				if path == "" {
					continue
				}
				tasks[i].Files = []rpc.File{
					{
						Path: path,
						Uris: []rpc.Uri{{Uri: meta.SourceURL}},
					},
				}
			}
		}
	}
}

func (c *TaskCache) SetTaskGroup(gid string, group rpc.DownloadGroup) {
	gid = strings.TrimSpace(gid)
	if gid == "" || group.ID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	meta := c.metadata[gid]
	if meta == nil {
		meta = &TaskMetadata{GID: gid, FetchedAt: time.Now()}
		c.metadata[gid] = meta
	}
	meta.DownloadGroup = copyDownloadGroup(&group)
}

func (c *TaskCache) RegisterTaskGroup(gid string, group rpc.DownloadGroup) {
	c.SetTaskGroup(gid, group)
	RegisterTaskGroup(gid, group)
}

func (c *TaskCache) GetTaskGroup(gid string) *rpc.DownloadGroup {
	c.mu.RLock()
	defer c.mu.RUnlock()
	meta := c.metadata[gid]
	if meta == nil || meta.DownloadGroup == nil {
		return nil
	}
	return copyDownloadGroup(meta.DownloadGroup)
}

func (c *TaskCache) UpdateTaskGroupName(groupKey, name, status string) int {
	groupKey = strings.TrimSpace(groupKey)
	name = strings.TrimSpace(name)
	status = strings.TrimSpace(status)
	if groupKey == "" || name == "" || !rpc.IsDownloadGroupNameStatus(status) {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	changed := 0
	updateGroup := func(group *rpc.DownloadGroup) (*rpc.DownloadGroup, bool) {
		if group == nil || group.ID != groupKey {
			return group, false
		}
		if group.Name == name && group.NameStatus == status {
			return group, false
		}
		updated := *group
		updated.Name = name
		updated.NameStatus = status
		return &updated, true
	}

	for _, meta := range c.metadata {
		if meta == nil {
			continue
		}
		if updated, ok := updateGroup(meta.DownloadGroup); ok {
			meta.DownloadGroup = updated
			changed++
		}
	}
	for i := range c.active {
		if updated, ok := updateGroup(c.active[i].DownloadGroup); ok {
			c.active[i].DownloadGroup = updated
			changed++
		}
	}
	for i := range c.waiting {
		if updated, ok := updateGroup(c.waiting[i].DownloadGroup); ok {
			c.waiting[i].DownloadGroup = updated
			changed++
		}
	}
	for i := range c.stopped {
		if updated, ok := updateGroup(c.stopped[i].DownloadGroup); ok {
			c.stopped[i].DownloadGroup = updated
			changed++
		}
	}

	return changed
}

// HasValidMetadata 检查任务是否有有效的元数据（包含文件信息）
// 用于检测被污染的缓存条目并触发重试
func (c *TaskCache) HasValidMetadata(gid string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return metadataHasValidPath(c.metadata[gid])
}

// InvalidateMetadata 删除指定任务的元数据（用于强制重新获取）
func (c *TaskCache) InvalidateMetadata(gid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.metadata, gid)
	RemoveTaskGroup(gid)
}

// RemoveTask 从缓存的活跃、等待和停止列表中删除指定任务并清理元数据
func (c *TaskCache) RemoveTask(gid string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	filter := func(tasks []rpc.Task) []rpc.Task {
		res := make([]rpc.Task, 0, len(tasks))
		for _, t := range tasks {
			if t.GID != gid {
				res = append(res, t)
			}
		}
		return res
	}

	c.active = filter(c.active)
	c.waiting = filter(c.waiting)
	c.stopped = filter(c.stopped)
	delete(c.metadata, gid)
	RemoveTaskGroup(gid)
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

	tasks, err := c.engine.TellStatusMulti(uniqueGids, nil)
	if err != nil || len(tasks) == 0 {
		return
	}

	c.mu.Lock()
	queuedGroupKeys := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if groupKey := c.ensureMetadata(task); groupKey != "" {
			queuedGroupKeys = append(queuedGroupKeys, groupKey)
		}
	}
	c.mu.Unlock()
	for _, groupKey := range queuedGroupKeys {
		queueDownloadGroupNameRefresh(groupKey)
	}
}

// PrefetchMetadata 强制预取指定任务的元数据
// 用于任务添加后立即获取完整信息
func (c *TaskCache) PrefetchMetadata(gid string) {
	// 从 engine 获取单个任务的完整信息
	task, err := c.engine.TellStatus(gid, nil)
	if err != nil {
		return
	}

	c.mu.Lock()
	groupKey := c.ensureMetadata(task)
	c.mu.Unlock()
	if groupKey != "" {
		queueDownloadGroupNameRefresh(groupKey)
	}
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
