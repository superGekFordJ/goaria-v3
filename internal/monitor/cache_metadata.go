// Task metadata prefetch cache, download group associations, RPC prefetch, and orphan metadata eviction.

package monitor

import (
	"path/filepath"
	"slices"
	"strings"
	"time"

	"goaria-v3/internal/rpc"
)

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
	return slices.ContainsFunc(meta.Files, metadataPathValid)
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
			fillBlank(&tasks[i].Title, meta.Title)
			fillBlank(&tasks[i].Dir, meta.Dir)
			files := make([]rpc.File, 0, len(meta.Files))
			for _, path := range meta.Files {
				files = append(files, rpc.File{Path: path})
			}
			if len(files) > 0 && metadataPathValid(meta.SourceURL) {
				files[0].Uris = []rpc.Uri{{Uri: meta.SourceURL}}
			}
			if len(tasks[i].Files) == 0 {
				tasks[i].Files = files
			} else {
				tasks[i].Files = mergeTaskFiles(tasks[i].Files, files)
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

func (c *TaskCache) markPendingStart(gid string) {
	gid = strings.TrimSpace(gid)
	if gid == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingStartGids == nil {
		c.pendingStartGids = make(map[string]time.Time)
	}
	c.pendingStartGids[gid] = time.Now()
}

func (c *TaskCache) IsPendingStart(gid string) bool {
	gid = strings.TrimSpace(gid)
	if gid == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pendingStartGids == nil {
		return false
	}
	_, ok := c.pendingStartGids[gid]
	return ok
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

	changed := 0

	c.mu.Lock()
	for _, meta := range c.metadata {
		if meta == nil {
			continue
		}
		if updated, ok := updateGroup(meta.DownloadGroup); ok {
			meta.DownloadGroup = updated
			changed++
		}
	}
	c.mu.Unlock()

	c.sgMu.Lock()
	for i := range c.sgActive {
		if updated, ok := updateGroup(c.sgActive[i].DownloadGroup); ok {
			c.sgActive[i].DownloadGroup = updated
			changed++
		}
	}
	for i := range c.sgWaiting {
		if updated, ok := updateGroup(c.sgWaiting[i].DownloadGroup); ok {
			c.sgWaiting[i].DownloadGroup = updated
			changed++
		}
	}
	for i := range c.sgStopped {
		if updated, ok := updateGroup(c.sgStopped[i].DownloadGroup); ok {
			c.sgStopped[i].DownloadGroup = updated
			changed++
		}
	}
	c.sgMu.Unlock()

	c.arMu.Lock()
	for i := range c.arActive {
		if updated, ok := updateGroup(c.arActive[i].DownloadGroup); ok {
			c.arActive[i].DownloadGroup = updated
			changed++
		}
	}
	for i := range c.arWaiting {
		if updated, ok := updateGroup(c.arWaiting[i].DownloadGroup); ok {
			c.arWaiting[i].DownloadGroup = updated
			changed++
		}
	}
	for i := range c.arStopped {
		if updated, ok := updateGroup(c.arStopped[i].DownloadGroup); ok {
			c.arStopped[i].DownloadGroup = updated
			changed++
		}
	}
	c.arMu.Unlock()

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
	if c.pendingStartGids != nil {
		delete(c.pendingStartGids, gid)
	}
	RemoveTaskGroup(gid)
}

func (c *TaskCache) PrefetchMetadataMulti(gids []string) {
	if len(gids) == 0 || c.engine == nil {
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
	if c.engine == nil {
		return
	}
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
	c.enrichSgEntry(gid, task)
}

// enrichSgEntry writes fetched metadata fields (Dir/Files/Title/DownloadGroup)
// from a full TellStatus result back into the matching sg slice entry, so that
// cache getters return enriched sg tasks without per-call EnrichTasks calls.
// Event-managed fields (Status/progress) are preserved.
func (c *TaskCache) enrichSgEntry(gid string, full rpc.Task) {
	if enginePrefix(gid) != "sg" {
		return
	}
	c.sgMu.Lock()
	defer c.sgMu.Unlock()
	update := func(slice []rpc.Task) bool {
		for i := range slice {
			if slice[i].GID != gid {
				continue
			}
			if metadataPathValid(full.Dir) {
				slice[i].Dir = full.Dir
			}
			completeFiles := len(full.Files) > 0 && len(full.Files) >= len(slice[i].Files)
			for _, file := range full.Files {
				completeFiles = completeFiles && metadataPathValid(file.Path) && len(file.Uris) > 0
				for _, uri := range file.Uris {
					completeFiles = completeFiles && metadataPathValid(uri.Uri)
				}
			}
			if completeFiles {
				slice[i].Files = copyTask(full).Files
			} else {
				slice[i].Files = mergeTaskFiles(slice[i].Files, full.Files)
			}
			if metadataPathValid(full.Title) {
				slice[i].Title = full.Title
			}
			if full.DownloadGroup != nil && metadataPathValid(full.DownloadGroup.ID) {
				group := copyDownloadGroup(full.DownloadGroup)
				fillDownloadGroup(group, slice[i].DownloadGroup)
				slice[i].DownloadGroup = group
			}
			return true
		}
		return false
	}
	if update(c.sgActive) || update(c.sgWaiting) || update(c.sgStopped) {
		return
	}
}

// CleanupMetadata evicts orphaned metadata entries for Aria2 tasks no longer
// in any engine list. It protects pendingStartGids entries, recently-fetched
// metadata (FetchedAt grace), and skips Surge (sg_) GIDs which have their own
// reconcile path. It does not touch the durable group store. Returns the count
// of evicted entries.
func (c *TaskCache) CleanupMetadata(activeGids map[string]bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	evicted := 0
	for gid, meta := range c.metadata {
		if IsSgGid(gid) {
			continue
		}
		if activeGids[gid] {
			continue
		}
		if c.pendingStartGids != nil {
			if _, ok := c.pendingStartGids[gid]; ok {
				continue
			}
		}
		if meta != nil && now.Sub(meta.FetchedAt) < metadataCleanupGrace {
			continue
		}
		delete(c.metadata, gid)
		evicted++
	}
	return evicted
}
