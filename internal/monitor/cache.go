package monitor

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/history"
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

// retainedRow holds only the fields retainMetadata consumes. At 64 bytes it
// stays under the map inline-value threshold, avoiding per-entry heap boxing
// that a full rpc.Task (176 bytes) would incur.
type retainedRow struct {
	Title         string
	Dir           string
	Files         []rpc.File
	DownloadGroup *rpc.DownloadGroup
}

func retainedRowOf(task rpc.Task) retainedRow {
	return retainedRow{Title: task.Title, Dir: task.Dir, Files: task.Files, DownloadGroup: task.DownloadGroup}
}

func (r retainedRow) task() rpc.Task {
	return rpc.Task{Title: r.Title, Dir: r.Dir, Files: r.Files, DownloadGroup: r.DownloadGroup}
}

// fillFrom merges src through fillTaskMetadata on a projected task so the
// merge rules stay identical; r's Files/DownloadGroup may alias src rows
// because fillTaskMetadata copies before mutating.
func (r *retainedRow) fillFrom(src rpc.Task) {
	t := r.task()
	fillTaskMetadata(&t, src)
	*r = retainedRowOf(t)
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
	old := make(map[string]retainedRow, len(c.arActive)+len(c.arWaiting)+len(c.arStopped))
	for _, list := range [][]rpc.Task{c.arActive, c.arWaiting, c.arStopped} {
		for _, task := range list {
			row, exists := old[task.GID]
			if !exists {
				row = retainedRowOf(task)
			} else {
				row.fillFrom(task)
			}
			old[task.GID] = row
		}
	}
	retainMetadata := func(tasks, previous []rpc.Task) []rpc.Task {
		// sameList keeps same-list rows authoritative for metadata when a GID
		// has corrupt twins across lists; old folds all three lists in
		// active/waiting/stopped order and only fills what remains missing.
		sameList := make(map[string]retainedRow, len(previous))
		for _, task := range previous {
			row, exists := sameList[task.GID]
			if !exists {
				row = retainedRowOf(task)
			} else {
				row.fillFrom(task)
			}
			sameList[task.GID] = row
		}
		rows := copyTaskSlice(tasks)
		for i := range rows {
			previous := sameList[rows[i].GID]
			previous.fillFrom(old[rows[i].GID].task())
			// mergeTaskFiles cleanses dst on every later merge, so only the
			// valid-path count is needed to pick the merge direction here.
			validCount := 0
			for _, file := range rows[i].Files {
				if metadataPathValid(file.Path) {
					validCount++
				}
			}
			if validCount < len(previous.Files) {
				rows[i].Files = mergeTaskFiles(previous.Files, rows[i].Files)
			}
			fillTaskMetadata(&rows[i], previous.task())
		}
		return rows
	}
	c.arActive = retainMetadata(arActive, c.arActive)
	c.arWaiting = retainMetadata(arWaiting, c.arWaiting)
	c.arStopped = retainMetadata(arStopped, c.arStopped)

	var resumedFromStopped []rpc.Task
	for _, task := range c.arActive {
		if _, wasStopped := prevStopped[task.GID]; wasStopped {
			resumedFromStopped = append(resumedFromStopped, task)
		}
	}
	for _, task := range c.arWaiting {
		if _, wasStopped := prevStopped[task.GID]; wasStopped {
			resumedFromStopped = append(resumedFromStopped, task)
		}
	}
	c.arMu.Unlock()

	for _, task := range resumedFromStopped {
		var tracker *TaskTracker
		if mon := State.GetMonitor(); mon != nil {
			tracker = mon.tracker
		}
		tracker.RunUnderLifecycle(task.GID, func() {
			current, list, ok := c.GetTask(task.GID)
			_, historied := history.Get(task.GID)
			if ok && list != "stopped" && current.Status == task.Status &&
				(historied || tracker.TerminalAcceptedInCurrentGeneration(task.GID)) {
				retireHistoryAndReopenToStatusLocked(tracker, task.GID, current.Status)
			}
		})
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
	mu := &c.arMu
	active, waiting, stopped := &c.arActive, &c.arWaiting, &c.arStopped
	if enginePrefix(gid) == "sg" {
		mu = &c.sgMu
		active, waiting, stopped = &c.sgActive, &c.sgWaiting, &c.sgStopped
	}
	mu.Lock()
	defer mu.Unlock()
	if !containsTaskGID(*active, gid) && !containsTaskGID(*waiting, gid) {
		return
	}
	moveTaskBetweenLists(active, waiting, stopped, gid, status, "stopped", false)
	stampStoppedTerminal(*stopped, gid, status, errorCode, errorMessage, 0)
}

// stampStoppedTerminal fills terminal-only fields on a row that just landed
// in stopped. Complete commits the canonical total as total=completed and
// zeroes speed; error keeps its checkpoint and stamps code/message only.
func stampStoppedTerminal(stopped []rpc.Task, gid, status, errorCode, errorMessage string, canonicalTotal int64) {
	for i := range stopped {
		if stopped[i].GID != gid {
			continue
		}
		t := &stopped[i]
		if errorCode != "" {
			t.ErrorCode = errorCode
		}
		if errorMessage != "" {
			t.ErrorMessage = errorMessage
		}
		if status == "complete" && canonicalTotal > 0 {
			totalStr := strconv.FormatInt(canonicalTotal, 10)
			t.TotalLength = totalStr
			t.CompletedLength = totalStr
			t.DownloadSpeed = "0"
		}
		return
	}
}

// MoveTaskToStoppedTerminal is the accepted-terminal counterpart of
// moveTaskToStopped: the move and the terminal field commit share one critical
// section so a waiting-source fast-complete cannot lose its canonical total.
func (c *TaskCache) MoveTaskToStoppedTerminal(gid, status, errorCode, errorMessage string, canonicalTotal int64) {
	mu := &c.arMu
	active, waiting, stopped := &c.arActive, &c.arWaiting, &c.arStopped
	if enginePrefix(gid) == "sg" {
		mu = &c.sgMu
		active, waiting, stopped = &c.sgActive, &c.sgWaiting, &c.sgStopped
	}
	mu.Lock()
	defer mu.Unlock()
	if !containsTaskGID(*active, gid) && !containsTaskGID(*waiting, gid) {
		return
	}
	moveTaskBetweenLists(active, waiting, stopped, gid, status, "stopped", false)
	stampStoppedTerminal(*stopped, gid, status, errorCode, errorMessage, canonicalTotal)
}

// MoveTaskToWaiting moves a task into the waiting list from any of the three
// engine slices (active / waiting / stopped), setting its status. Returns the
// source list name, or "" when the GID was not found. When the source is
// stopped, ErrorCode/ErrorMessage are cleared.
func (c *TaskCache) MoveTaskToWaiting(gid, status string) string {
	return c.moveTaskToWaiting(gid, status, false)
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

// RestoreAriaHistoryTaskToWaiting stages a history-only Aria2 row into the
// stopped list and moves it to waiting inside a single arMu critical
// section, so readers can only ever observe absent or waiting — never the
// intermediate append-to-stopped state. Returns the logical source:
// "stopped" for the staged restore (or a real stopped row), the live list
// name when a full row already exists, or "" for empty/foreign GIDs. The
// history snapshot only seeds a row when none exists; an existing live row
// is folded and moved with its metadata intact.
func (c *TaskCache) RestoreAriaHistoryTaskToWaiting(task rpc.Task, status string) string {
	gid := task.GID
	if gid == "" || enginePrefix(gid) == "sg" {
		return ""
	}
	c.arMu.Lock()
	defer c.arMu.Unlock()
	if !containsTaskGID(c.arActive, gid) && !containsTaskGID(c.arWaiting, gid) && !containsTaskGID(c.arStopped, gid) {
		c.arStopped = append(c.arStopped, copyTask(task))
	}
	return moveTaskBetweenLists(&c.arActive, &c.arWaiting, &c.arStopped, gid, status, "waiting", false)
}

// MoveTaskToActive moves a task into the active list from any of the three
// engine slices (active / waiting / stopped), setting its status. Returns the
// source list name, or "" when the GID was not found. When the source is
// stopped, ErrorCode/ErrorMessage are cleared.
func (c *TaskCache) MoveTaskToActive(gid, status string) string {
	return c.moveTaskToActive(gid, status, false)
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

	row, found := foldTaskRows(gid, *dest, *active, *waiting, *stopped)
	if !found || (refuseStopped && !containsTaskGID(*active, gid) && !containsTaskGID(*waiting, gid)) {
		return ""
	}
	if updateTaskInPlace(dest, gid, status) {
		row.Status, row.DownloadSpeed = status, "0"
		storeFoldedTask(dest, row)
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
		_, ok := detachTask(src, gid)
		if !ok {
			continue
		}
		task := row
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

func detachTask(list *[]rpc.Task, gid string) (task rpc.Task, found bool) {
	kept := (*list)[:0]
	for _, row := range *list {
		if row.GID == gid {
			if !found {
				task, found = row, true
			}
		} else {
			kept = append(kept, row)
		}
	}
	clear((*list)[len(kept):])
	*list = kept
	return
}

// foldTaskRows keeps the first row's facts and fills only missing sibling data.
func foldTaskRows(gid string, lists ...[]rpc.Task) (row rpc.Task, found bool) {
	for _, list := range lists {
		for _, task := range list {
			if task.GID != gid {
				continue
			}
			if !found {
				row, found = copyTask(task), true
			} else {
				fillTaskMetadata(&row, task)
				if parseInt64(row.TotalLength) <= 0 && parseInt64(task.TotalLength) > 0 {
					row.TotalLength = task.TotalLength
				}
				if parseInt64(row.CompletedLength) <= 0 && parseInt64(task.CompletedLength) > 0 {
					row.CompletedLength = task.CompletedLength
				}
			}
		}
	}
	return
}

func storeFoldedTask(list *[]rpc.Task, task rpc.Task) {
	kept, stored := (*list)[:0], false
	for _, row := range *list {
		if row.GID != task.GID {
			kept = append(kept, row)
		} else if !stored {
			kept, stored = append(kept, task), true
		}
	}
	clear((*list)[len(kept):])
	if !stored {
		kept = append(kept, task)
	}
	*list = kept
}

// AddSgTask inserts or updates a Surge task in the specified sg slice.
// If the GID already exists in the target slice, fields are shallow-merged
// (non-empty values in the new task overwrite the old). If the GID exists in
// a different sg slice, it is moved to the target slice. list is "active",
// "waiting", or "stopped".
func (c *TaskCache) AddSgTask(task rpc.Task, list string) {
	c.sgMu.Lock()
	defer c.sgMu.Unlock()
	c.addSgTaskLocked(task, list)
}

func (c *TaskCache) addSgTaskLocked(task rpc.Task, list string) {
	target := listPtrByName(&c.sgActive, &c.sgWaiting, &c.sgStopped, list)
	if target == nil {
		return
	}
	inTarget := containsTaskGID(*target, task.GID)
	row, found := foldTaskRows(task.GID, *target, c.sgActive, c.sgWaiting, c.sgStopped)
	if !found {
		row = copyTask(task)
	} else {
		mergeTaskFields(&row, task)
	}
	if !inTarget {
		row.ErrorCode, row.ErrorMessage = task.ErrorCode, task.ErrorMessage
	}
	storeFoldedTask(target, row)
	sweepGIDFromOtherLists(&c.sgActive, &c.sgWaiting, &c.sgStopped, task.GID, list)
}

// mergeTaskFields accepts sparse metadata and reliable positive checkpoints.
func mergeTaskFields(dst *rpc.Task, src rpc.Task) {
	if src.Status != "" {
		dst.Status = src.Status
	}
	mergeLength := func(dst *string, value string) {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil && (n > 0 || n == 0 && parseInt64(*dst) <= 0) {
			*dst = value
		}
	}
	mergeLength(&dst.TotalLength, src.TotalLength)
	mergeLength(&dst.CompletedLength, src.CompletedLength)
	if src.DownloadSpeed != "" {
		dst.DownloadSpeed = src.DownloadSpeed
	}
	if metadataPathValid(src.Title) {
		dst.Title = src.Title
	}
	if metadataPathValid(src.Dir) {
		dst.Dir = src.Dir
	}
	dst.Files = mergeTaskFiles(dst.Files, src.Files)
	if src.DownloadGroup != nil && metadataPathValid(src.DownloadGroup.ID) {
		group := copyDownloadGroup(src.DownloadGroup)
		fillDownloadGroup(group, dst.DownloadGroup)
		dst.DownloadGroup = group
	}
}

func fillBlank(dst *string, src string) {
	if !metadataPathValid(*dst) && metadataPathValid(src) {
		*dst = src
	}
}

func fillDownloadGroup(dst, src *rpc.DownloadGroup) {
	if dst == nil || src == nil || dst.ID != src.ID {
		return
	}
	fillBlank(&dst.Kind, src.Kind)
	fillBlank(&dst.Name, src.Name)
	fillBlank(&dst.NameStatus, src.NameStatus)
	fillBlank(&dst.FolderName, src.FolderName)
	fillBlank(&dst.Dir, src.Dir)
	if dst.ItemCount == 0 {
		dst.ItemCount = src.ItemCount
	}
	if dst.CreatedAt == 0 {
		dst.CreatedAt = src.CreatedAt
	}
}

// fillTaskMetadata never takes status, errors or measured progress from siblings.
func fillTaskMetadata(dst *rpc.Task, src rpc.Task) {
	fillBlank(&dst.Title, src.Title)
	fillBlank(&dst.Dir, src.Dir)
	dst.Files = mergeTaskFiles(dst.Files, src.Files)
	if src.DownloadGroup != nil && metadataPathValid(src.DownloadGroup.ID) {
		if dst.DownloadGroup == nil || !metadataPathValid(dst.DownloadGroup.ID) {
			dst.DownloadGroup = copyDownloadGroup(src.DownloadGroup)
		} else {
			dst.DownloadGroup = copyDownloadGroup(dst.DownloadGroup)
			fillDownloadGroup(dst.DownloadGroup, src.DownloadGroup)
		}
	}
}

func mergeTaskFiles(dst, src []rpc.File) []rpc.File {
	files := copyTask(rpc.Task{Files: dst}).Files
	files = slices.DeleteFunc(files, func(old rpc.File) bool { return !metadataPathValid(old.Path) })
	for i := range files {
		files[i].Uris = slices.DeleteFunc(files[i].Uris, func(old rpc.Uri) bool { return !metadataPathValid(old.Uri) })
	}
	for _, file := range src {
		if !metadataPathValid(file.Path) {
			continue
		}
		i := slices.IndexFunc(files, func(old rpc.File) bool { return old.Path == file.Path })
		if i < 0 {
			files = slices.DeleteFunc(files, func(old rpc.File) bool { return !metadataPathValid(old.Path) })
			files = append(files, rpc.File{Path: file.Path})
			i = len(files) - 1
		}
		for _, uri := range file.Uris {
			if !metadataPathValid(uri.Uri) {
				continue
			}
			j := slices.IndexFunc(files[i].Uris, func(old rpc.Uri) bool { return old.Uri == uri.Uri })
			if j < 0 {
				files[i].Uris = slices.DeleteFunc(files[i].Uris, func(old rpc.Uri) bool { return !metadataPathValid(old.Uri) })
				files[i].Uris = append(files[i].Uris, uri)
			} else {
				fillBlank(&files[i].Uris[j].Status, uri.Status)
			}
		}
	}
	return files
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

// GetTask returns a copy of gid and the list holding it ("active",
// "waiting" or "stopped"). Only the owning engine's lock is taken.
func (c *TaskCache) GetTask(gid string) (task rpc.Task, list string, ok bool) {
	if gid == "" {
		return rpc.Task{}, "", false
	}
	mu := &c.arMu
	lists := [3]*[]rpc.Task{&c.arActive, &c.arWaiting, &c.arStopped}
	if enginePrefix(gid) == "sg" {
		mu = &c.sgMu
		lists = [3]*[]rpc.Task{&c.sgActive, &c.sgWaiting, &c.sgStopped}
	}
	names := [3]string{"active", "waiting", "stopped"}

	mu.RLock()
	defer mu.RUnlock()
	for i, l := range lists {
		for j := range *l {
			if (*l)[j].GID == gid {
				return copyTask((*l)[j]), names[i], true
			}
		}
	}
	return rpc.Task{}, "", false
}

func containsTaskGID(tasks []rpc.Task, gid string) bool {
	for i := range tasks {
		if tasks[i].GID == gid {
			return true
		}
	}
	return false
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

// GetLastUpdate 获取最后更新时间
func (c *TaskCache) GetLastUpdate() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastUpdate
}
