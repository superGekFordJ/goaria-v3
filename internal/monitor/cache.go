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
// 4. 按引擎前缀分离存储：sgMu/arMu 各保护对应引擎切片，mu 仅保护共享字段
type TaskCache struct {
	sgMu sync.RWMutex
	arMu sync.RWMutex
	mu   sync.RWMutex

	// Surge 引擎专用切片
	sgActive  []rpc.Task
	sgWaiting []rpc.Task
	sgStopped []rpc.Task

	// aria2c 引擎专用切片
	arActive  []rpc.Task
	arWaiting []rpc.Task
	arStopped []rpc.Task

	engine rpc.DownloadEngine

	// 元数据缓存（用于暂停任务的文件名等信息）
	metadata map[string]*TaskMetadata

	// pendingStartGids tracks GIDs registered with a download group but
	// not yet seen in a cache tick. Covers the post-GID pre-cache window.
	pendingStartGids map[string]time.Time

	// 最后更新时间
	lastUpdate time.Time
}

// enginePrefix 按 GID 前缀路由到对应引擎，无前缀默认 ar（兼容纯 aria2c 模式）
func enginePrefix(gid string) string {
	if strings.HasPrefix(gid, "sg_") {
		return "sg"
	}
	return "ar"
}

// IsSgGid reports whether the GID belongs to the Surge engine.
func IsSgGid(gid string) bool {
	return enginePrefix(gid) == "sg"
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
	metadata:         make(map[string]*TaskMetadata),
	pendingStartGids: make(map[string]time.Time),
}

// NewTaskCacheForTest returns a TaskCache with all internal maps initialized,
// for use by cross-package test setups that cannot access unexported fields.
func NewTaskCacheForTest() *TaskCache {
	return &TaskCache{
		metadata:         make(map[string]*TaskMetadata),
		pendingStartGids: make(map[string]time.Time),
	}
}

// UpdateFromAria2 从引擎更新缓存（批量获取）。
// 仅替换 aria2 (ar_) 切片；Surge (sg_) 切片由事件驱动路径维护
// （AddSgTask/MoveTaskTo*/RemoveTask/PatchTaskProgress）。
// 共享字段（pendingStartGids、lastUpdate）在 mu 下更新。
// GIDs that leave arStopped for arActive/arWaiting trigger history retirement.
func (c *TaskCache) UpdateFromAria2(active, waiting, stopped []rpc.Task) {
	_, arActive := splitByPrefix(active)
	_, arWaiting := splitByPrefix(waiting)
	_, arStopped := splitByPrefix(stopped)

	c.arMu.Lock()
	prevStopped := make(map[string]struct{}, len(c.arStopped))
	for i := range c.arStopped {
		if gid := c.arStopped[i].GID; gid != "" {
			prevStopped[gid] = struct{}{}
		}
	}
	c.arActive = copyTaskSlice(arActive)
	c.arWaiting = copyTaskSlice(arWaiting)
	c.arStopped = copyTaskSlice(arStopped)

	var resumedFromStopped []string
	for _, task := range c.arActive {
		if _, wasStopped := prevStopped[task.GID]; wasStopped {
			resumedFromStopped = append(resumedFromStopped, task.GID)
		}
	}
	for _, task := range c.arWaiting {
		if _, wasStopped := prevStopped[task.GID]; wasStopped {
			resumedFromStopped = append(resumedFromStopped, task.GID)
		}
	}
	c.arMu.Unlock()

	for _, gid := range resumedFromStopped {
		RetireHistoryIfResumedFromStopped(gid, "stopped")
	}

	c.mu.Lock()
	c.lastUpdate = time.Now()
	if c.pendingStartGids != nil {
		for _, task := range active {
			delete(c.pendingStartGids, task.GID)
		}
		for _, task := range waiting {
			delete(c.pendingStartGids, task.GID)
		}
		for _, task := range stopped {
			delete(c.pendingStartGids, task.GID)
		}
		now := time.Now()
		for gid, markedAt := range c.pendingStartGids {
			if now.Sub(markedAt) > 30*time.Second {
				delete(c.pendingStartGids, gid)
			}
		}
	}
	c.mu.Unlock()
}

// splitByPrefix 按 GID 前缀将切片拆分为 sg/ar 两组
func splitByPrefix(tasks []rpc.Task) (sg, ar []rpc.Task) {
	for _, task := range tasks {
		if enginePrefix(task.GID) == "sg" {
			sg = append(sg, task)
		} else {
			ar = append(ar, task)
		}
	}
	return sg, ar
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

// PatchTaskProgress updates a single active task's progress fields in cache.
// Called from handleSurgeEvent progress push path to keep cache fresh between
// 5s ticks, so backend-side reads (e.g. download group aggregation) see
// current speed/completed/total values.
func (c *TaskCache) PatchTaskProgress(gid, completedLength, downloadSpeed, totalLength string) {
	if enginePrefix(gid) == "sg" {
		c.sgMu.Lock()
		defer c.sgMu.Unlock()
		for i := range c.sgActive {
			if c.sgActive[i].GID == gid {
				c.sgActive[i].CompletedLength = completedLength
				c.sgActive[i].DownloadSpeed = downloadSpeed
				c.sgActive[i].TotalLength = totalLength
				return
			}
		}
		return
	}
	c.arMu.Lock()
	defer c.arMu.Unlock()
	for i := range c.arActive {
		if c.arActive[i].GID == gid {
			c.arActive[i].CompletedLength = completedLength
			c.arActive[i].DownloadSpeed = downloadSpeed
			c.arActive[i].TotalLength = totalLength
			return
		}
	}
}

// MoveTaskToStopped moves a task from active or waiting to the stopped list,
// setting its status. Called from handleSurgeEvent for complete/error events
// so that GetStopped() returns the task immediately, before the next tick
// populates it from the engine.
func (c *TaskCache) MoveTaskToStopped(gid, status string) {
	c.moveTaskToStopped(gid, status, "", "")
}

// MoveTaskToStoppedWithError is MoveTaskToStopped plus immediate ErrorCode /
// ErrorMessage stamping for EventError paths (avoids empty codes until TellStopped).
func (c *TaskCache) MoveTaskToStoppedWithError(gid, status, errorCode, errorMessage string) {
	c.moveTaskToStopped(gid, status, errorCode, errorMessage)
}

func (c *TaskCache) moveTaskToStopped(gid, status, errorCode, errorMessage string) {
	if enginePrefix(gid) == "sg" {
		c.sgMu.Lock()
		defer c.sgMu.Unlock()
		for i := range c.sgActive {
			if c.sgActive[i].GID == gid {
				task := c.sgActive[i]
				task.Status = status
				task.DownloadSpeed = "0"
				if errorCode != "" {
					task.ErrorCode = errorCode
				}
				if errorMessage != "" {
					task.ErrorMessage = errorMessage
				}
				c.sgStopped = append(c.sgStopped, task)
				c.sgActive = append(c.sgActive[:i], c.sgActive[i+1:]...)
				return
			}
		}
		for i := range c.sgWaiting {
			if c.sgWaiting[i].GID == gid {
				task := c.sgWaiting[i]
				task.Status = status
				task.DownloadSpeed = "0"
				if errorCode != "" {
					task.ErrorCode = errorCode
				}
				if errorMessage != "" {
					task.ErrorMessage = errorMessage
				}
				c.sgStopped = append(c.sgStopped, task)
				c.sgWaiting = append(c.sgWaiting[:i], c.sgWaiting[i+1:]...)
				return
			}
		}
		return
	}
	c.arMu.Lock()
	defer c.arMu.Unlock()
	for i := range c.arActive {
		if c.arActive[i].GID == gid {
			task := c.arActive[i]
			task.Status = status
			task.DownloadSpeed = "0"
			if errorCode != "" {
				task.ErrorCode = errorCode
			}
			if errorMessage != "" {
				task.ErrorMessage = errorMessage
			}
			c.arStopped = append(c.arStopped, task)
			c.arActive = append(c.arActive[:i], c.arActive[i+1:]...)
			return
		}
	}
	for i := range c.arWaiting {
		if c.arWaiting[i].GID == gid {
			task := c.arWaiting[i]
			task.Status = status
			task.DownloadSpeed = "0"
			if errorCode != "" {
				task.ErrorCode = errorCode
			}
			if errorMessage != "" {
				task.ErrorMessage = errorMessage
			}
			c.arStopped = append(c.arStopped, task)
			c.arWaiting = append(c.arWaiting[:i], c.arWaiting[i+1:]...)
			return
		}
	}
}

// MoveTaskToWaiting moves a task into the waiting list from any of the three
// engine slices (active / waiting / stopped), setting its status. Returns the
// source list name, or "" when the GID was not found. When the source is
// stopped, ErrorCode/ErrorMessage are cleared.
func (c *TaskCache) MoveTaskToWaiting(gid, status string) string {
	return c.moveTaskToWaiting(gid, status, false)
}

// MoveTaskToWaitingFromLive moves only from active/waiting. If the GID is
// solely in stopped, returns "" without mutating (refuses cleared-error TOCTOU).
func (c *TaskCache) MoveTaskToWaitingFromLive(gid, status string) string {
	return c.moveTaskToWaiting(gid, status, true)
}

func (c *TaskCache) moveTaskToWaiting(gid, status string, refuseStopped bool) string {
	if enginePrefix(gid) == "sg" {
		c.sgMu.Lock()
		defer c.sgMu.Unlock()
		return moveTaskBetweenLists(&c.sgActive, &c.sgWaiting, &c.sgStopped, gid, status, "waiting", refuseStopped)
	}
	c.arMu.Lock()
	defer c.arMu.Unlock()
	return moveTaskBetweenLists(&c.arActive, &c.arWaiting, &c.arStopped, gid, status, "waiting", refuseStopped)
}

// MoveTaskToActive moves a task into the active list from any of the three
// engine slices (active / waiting / stopped), setting its status. Returns the
// source list name, or "" when the GID was not found. When the source is
// stopped, ErrorCode/ErrorMessage are cleared.
func (c *TaskCache) MoveTaskToActive(gid, status string) string {
	return c.moveTaskToActive(gid, status, false)
}

// MoveTaskToActiveFromLive moves only from active/waiting. If the GID is
// solely in stopped, returns "" without mutating (refuses cleared-error TOCTOU).
func (c *TaskCache) MoveTaskToActiveFromLive(gid, status string) string {
	return c.moveTaskToActive(gid, status, true)
}

func (c *TaskCache) moveTaskToActive(gid, status string, refuseStopped bool) string {
	if enginePrefix(gid) == "sg" {
		c.sgMu.Lock()
		defer c.sgMu.Unlock()
		return moveTaskBetweenLists(&c.sgActive, &c.sgWaiting, &c.sgStopped, gid, status, "active", refuseStopped)
	}
	c.arMu.Lock()
	defer c.arMu.Unlock()
	return moveTaskBetweenLists(&c.arActive, &c.arWaiting, &c.arStopped, gid, status, "active", refuseStopped)
}

// moveTaskBetweenLists relocates gid into the destination slice among the three
// list pointers (caller must hold the matching engine mutex). Returns the
// source list name ("active"/"waiting"/"stopped"), the destination name when
// already there, or "" when not found. When refuseStopped is true, a stopped
// source is skipped so ErrorCode/ErrorMessage are never cleared.
func moveTaskBetweenLists(active, waiting, stopped *[]rpc.Task, gid, status, destName string, refuseStopped bool) string {
	dest := listPtrByName(active, waiting, stopped, destName)
	if dest == nil {
		return ""
	}

	if updateTaskInPlace(dest, gid, status) {
		// Sibling copies are discarded (not merged): cache dest is already the
		// live engine-facing row; frontend richest-merge is the UI safety net.
		sweepGIDFromOtherLists(active, waiting, stopped, gid, destName)
		return destName
	}

	for _, srcName := range []string{"active", "waiting", "stopped"} {
		if srcName == destName {
			continue
		}
		if refuseStopped && srcName == "stopped" {
			continue
		}
		src := listPtrByName(active, waiting, stopped, srcName)
		if src == nil {
			continue
		}
		task, ok := detachTask(src, gid)
		if !ok {
			continue
		}
		task.Status = status
		task.DownloadSpeed = "0"
		if srcName == "stopped" {
			task.ErrorCode = ""
			task.ErrorMessage = ""
		}
		*dest = append(*dest, task)
		// Drop any remaining corrupt twins in other non-dest lists.
		sweepGIDFromOtherLists(active, waiting, stopped, gid, destName)
		return srcName
	}
	return ""
}

// sweepGIDFromOtherLists detaches gid from every list except destName.
func sweepGIDFromOtherLists(active, waiting, stopped *[]rpc.Task, gid, destName string) {
	for _, srcName := range []string{"active", "waiting", "stopped"} {
		if srcName == destName {
			continue
		}
		if src := listPtrByName(active, waiting, stopped, srcName); src != nil {
			_, _ = detachTask(src, gid)
		}
	}
}

func listPtrByName(active, waiting, stopped *[]rpc.Task, name string) *[]rpc.Task {
	switch name {
	case "active":
		return active
	case "waiting":
		return waiting
	case "stopped":
		return stopped
	default:
		return nil
	}
}

func updateTaskInPlace(list *[]rpc.Task, gid, status string) bool {
	for i := range *list {
		if (*list)[i].GID == gid {
			(*list)[i].Status = status
			(*list)[i].DownloadSpeed = "0"
			return true
		}
	}
	return false
}

func detachTask(list *[]rpc.Task, gid string) (rpc.Task, bool) {
	for i := range *list {
		if (*list)[i].GID == gid {
			task := (*list)[i]
			*list = append((*list)[:i], (*list)[i+1:]...)
			return task, true
		}
	}
	return rpc.Task{}, false
}

// AddSgTask inserts or updates a Surge task in the specified sg slice.
// If the GID already exists in the target slice, fields are shallow-merged
// (non-empty values in the new task overwrite the old). If the GID exists in
// a different sg slice, it is moved to the target slice. list is "active",
// "waiting", or "stopped".
func (c *TaskCache) AddSgTask(task rpc.Task, list string) {
	c.sgMu.Lock()
	defer c.sgMu.Unlock()

	var target *[]rpc.Task
	removeFrom := func(slice []rpc.Task, gid string) []rpc.Task {
		for i := range slice {
			if slice[i].GID == gid {
				return append(slice[:i], slice[i+1:]...)
			}
		}
		return slice
	}

	switch list {
	case "active":
		target = &c.sgActive
		c.sgWaiting = removeFrom(c.sgWaiting, task.GID)
		c.sgStopped = removeFrom(c.sgStopped, task.GID)
	case "waiting":
		target = &c.sgWaiting
		c.sgActive = removeFrom(c.sgActive, task.GID)
		c.sgStopped = removeFrom(c.sgStopped, task.GID)
	case "stopped":
		target = &c.sgStopped
		c.sgActive = removeFrom(c.sgActive, task.GID)
		c.sgWaiting = removeFrom(c.sgWaiting, task.GID)
	default:
		return
	}

	for i := range *target {
		if (*target)[i].GID == task.GID {
			mergeTaskFields(&(*target)[i], task)
			return
		}
	}
	*target = append(*target, copyTask(task))
}

// mergeTaskFields shallow-merges non-empty fields from src into dst.
func mergeTaskFields(dst *rpc.Task, src rpc.Task) {
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.TotalLength != "" {
		dst.TotalLength = src.TotalLength
	}
	if src.CompletedLength != "" {
		dst.CompletedLength = src.CompletedLength
	}
	if src.DownloadSpeed != "" {
		dst.DownloadSpeed = src.DownloadSpeed
	}
	if src.Dir != "" {
		dst.Dir = src.Dir
	}
	if len(src.Files) > 0 {
		dst.Files = src.Files
	}
	if src.DownloadGroup != nil {
		dst.DownloadGroup = copyDownloadGroup(src.DownloadGroup)
	}
}

// GetActive 获取活跃任务（合并 sg+ar 切片，防御性拷贝）
func (c *TaskCache) GetActive() []rpc.Task {
	c.sgMu.RLock()
	sg := copyTaskSlice(c.sgActive)
	c.sgMu.RUnlock()
	c.arMu.RLock()
	ar := copyTaskSlice(c.arActive)
	c.arMu.RUnlock()
	return append(sg, ar...)
}

// GetWaiting 获取等待任务（合并 sg+ar 切片，防御性拷贝）
func (c *TaskCache) GetWaiting() []rpc.Task {
	c.sgMu.RLock()
	sg := copyTaskSlice(c.sgWaiting)
	c.sgMu.RUnlock()
	c.arMu.RLock()
	ar := copyTaskSlice(c.arWaiting)
	c.arMu.RUnlock()
	return append(sg, ar...)
}

// GetStopped 获取已停止任务（合并 sg+ar 切片，防御性拷贝）
func (c *TaskCache) GetStopped() []rpc.Task {
	c.sgMu.RLock()
	sg := copyTaskSlice(c.sgStopped)
	c.sgMu.RUnlock()
	c.arMu.RLock()
	ar := copyTaskSlice(c.arStopped)
	c.arMu.RUnlock()
	return append(sg, ar...)
}

// GetTaskLists returns a per-engine coherent snapshot of active/waiting/stopped.
// Each engine's three lists are copied under one mutex; sg then ar are merged.
// Does not nest sgMu/arMu.
func (c *TaskCache) GetTaskLists() (active, waiting, stopped []rpc.Task) {
	c.sgMu.RLock()
	sgActive := copyTaskSlice(c.sgActive)
	sgWaiting := copyTaskSlice(c.sgWaiting)
	sgStopped := copyTaskSlice(c.sgStopped)
	c.sgMu.RUnlock()

	c.arMu.RLock()
	arActive := copyTaskSlice(c.arActive)
	arWaiting := copyTaskSlice(c.arWaiting)
	arStopped := copyTaskSlice(c.arStopped)
	c.arMu.RUnlock()

	return append(sgActive, arActive...), append(sgWaiting, arWaiting...), append(sgStopped, arStopped...)
}

// GetLiveTaskLists returns a per-engine coherent snapshot of active+waiting only
// (no stopped deep-copy). Used by high-frequency GetActiveTasks.
func (c *TaskCache) GetLiveTaskLists() (active, waiting []rpc.Task) {
	c.sgMu.RLock()
	sgActive := copyTaskSlice(c.sgActive)
	sgWaiting := copyTaskSlice(c.sgWaiting)
	c.sgMu.RUnlock()

	c.arMu.RLock()
	arActive := copyTaskSlice(c.arActive)
	arWaiting := copyTaskSlice(c.arWaiting)
	c.arMu.RUnlock()

	return append(sgActive, arActive...), append(sgWaiting, arWaiting...)
}

// IsInStopped reports whether gid is present in the stopped list for its engine.
func (c *TaskCache) IsInStopped(gid string) bool {
	if gid == "" {
		return false
	}
	if enginePrefix(gid) == "sg" {
		c.sgMu.RLock()
		defer c.sgMu.RUnlock()
		return containsTaskGID(c.sgStopped, gid)
	}
	c.arMu.RLock()
	defer c.arMu.RUnlock()
	return containsTaskGID(c.arStopped, gid)
}

func containsTaskGID(tasks []rpc.Task, gid string) bool {
	for i := range tasks {
		if tasks[i].GID == gid {
			return true
		}
	}
	return false
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

// RemoveTask 从缓存的活跃、等待和停止列表中删除指定任务并清理元数据
func (c *TaskCache) RemoveTask(gid string) {
	filter := func(tasks []rpc.Task) []rpc.Task {
		res := make([]rpc.Task, 0, len(tasks))
		for _, t := range tasks {
			if t.GID != gid {
				res = append(res, t)
			}
		}
		return res
	}

	if enginePrefix(gid) == "sg" {
		c.sgMu.Lock()
		c.sgActive = filter(c.sgActive)
		c.sgWaiting = filter(c.sgWaiting)
		c.sgStopped = filter(c.sgStopped)
		c.sgMu.Unlock()
	} else {
		c.arMu.Lock()
		c.arActive = filter(c.arActive)
		c.arWaiting = filter(c.arWaiting)
		c.arStopped = filter(c.arStopped)
		c.arMu.Unlock()
	}

	c.mu.Lock()
	delete(c.metadata, gid)
	if c.pendingStartGids != nil {
		delete(c.pendingStartGids, gid)
	}
	c.mu.Unlock()
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
			if full.Dir != "" {
				slice[i].Dir = full.Dir
			}
			if len(full.Files) > 0 {
				slice[i].Files = full.Files
			}
			if full.Title != "" {
				slice[i].Title = full.Title
			}
			if full.DownloadGroup != nil {
				slice[i].DownloadGroup = copyDownloadGroup(full.DownloadGroup)
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

// GetLastUpdate 获取最后更新时间
func (c *TaskCache) GetLastUpdate() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastUpdate
}
