package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
)

// --- Task Management ---

// RecordTaskSpeed 已废弃 - 后端 TaskTracker 自动采集
// 保留空实现以兼容现有前端
func (a *App) RecordTaskSpeed(gid string, speed int64, cl int64) {
	// 业务逻辑已迁移到 monitor.TaskTracker
	// 此方法保留以兼容前端，但不执行任何操作
}

// BatchAddResult holds the result of a batch add operation
type BatchAddResult struct {
	Succeeded  []string            `json:"succeeded"`
	Duplicates []string            `json:"duplicates"`
	Errors     map[string]string   `json:"errors"`
	Groups     []rpc.DownloadGroup `json:"groups,omitempty"`
}

type extractorAddTaskDispatcher interface {
	Resolve(ctx context.Context, rawURL string) (extractor.AddTaskResolution, error)
	BuildAria2Headers(ctx context.Context, item extractor.ResolvedAddItem) ([]string, error)
}

type addTaskCandidate struct {
	sourceURL     string
	url           string
	out           string
	sizeBytes     int64
	extracted     bool
	protected     bool
	displayKey    string
	item          extractor.ResolvedAddItem
	downloadGroup *downloadGroupPlan
}

type addTaskSummary struct {
	succeeded  []string
	duplicates []string
	errors     map[string]string
	groups     []rpc.DownloadGroup
	groupIDs   map[string]struct{}
}

func collectTaskSourceURLs(existingURLs map[string]bool, tasks []rpc.Task) {
	for _, task := range tasks {
		for _, file := range task.Files {
			for _, uri := range file.Uris {
				existingURLs[strings.TrimSpace(uri.Uri)] = true
			}
		}
	}
}

func collectExistingTaskSourceURLs(active, waiting, stopped []rpc.Task) map[string]bool {
	existingURLs := make(map[string]bool)
	collectTaskSourceURLs(existingURLs, active)
	collectTaskSourceURLs(existingURLs, waiting)
	collectTaskSourceURLs(existingURLs, stopped)

	return existingURLs
}

// AddUri adds a new download task
// Returns "success" on success, "duplicate" if task already exists, or error message
func (a *App) AddUri(url string) string {
	normalizedUrl := strings.TrimSpace(url)
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 1000)
	stopped, _ := rpc.TellStopped(0, 1000)
	existingURLs := collectExistingTaskSourceURLs(active, waiting, stopped)

	if existingURLs[normalizedUrl] {
		return "duplicate"
	}

	if history.ContainsSource(normalizedUrl) {
		return "duplicate"
	}

	summary := addTaskSummary{errors: make(map[string]string)}
	candidateSeen := make(map[string]bool)
	a.addNormalizedInput(context.Background(), normalizedUrl, existingURLs, nil, candidateSeen, &summary)

	if len(summary.succeeded) == 0 {
		if len(summary.errors) > 0 {
			return firstErrorString(summary.errors)
		}
		if len(summary.duplicates) > 0 {
			return "duplicate"
		}
		return "success"
	}
	if len(summary.errors) > 0 {
		return "partial success: " + firstErrorString(summary.errors)
	}

	return "success"
}

// BatchAddUri adds multiple download URLs in one batch.
// Performs O(1) Set-based deduplication with only 3 RPC calls total.
func (a *App) BatchAddUri(urls []string) BatchAddResult {
	result := BatchAddResult{
		Succeeded:  []string{},
		Duplicates: []string{},
		Errors:     make(map[string]string),
	}

	if len(urls) > 100 {
		urls = urls[:100]
	}

	// 3 RPC calls total for deduplication (not 3N)
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 1000)
	stopped, _ := rpc.TellStopped(0, 1000)

	// Build existing URL set for O(1) lookup
	existingUrls := collectExistingTaskSourceURLs(active, waiting, stopped)

	// History dedup for only the capped, normalized batch candidates.
	normalizedSources := make([]string, 0, len(urls))
	for _, rawUrl := range urls {
		normalized := strings.TrimSpace(rawUrl)
		if normalized == "" {
			continue
		}
		normalizedSources = append(normalizedSources, normalized)
	}
	historyDuplicates := history.ContainsSources(normalizedSources)

	seenRaw := make(map[string]bool)
	seenCandidates := make(map[string]bool)
	pendingCandidates := make([]addTaskCandidate, 0, len(urls))
	batchThresholdSeen := make(map[string]struct{}, len(urls))
	batchGroupCount := 0
	summary := addTaskSummary{errors: result.Errors}

	for _, rawUrl := range urls {
		normalized := strings.TrimSpace(rawUrl)
		if normalized == "" {
			continue
		}

		if seenRaw[normalized] {
			result.Duplicates = append(result.Duplicates, normalized)
			continue
		}
		seenRaw[normalized] = true

		if existingUrls[normalized] || historyDuplicates[normalized] {
			result.Duplicates = append(result.Duplicates, normalized)
			continue
		}

		candidates, err := a.resolveAddCandidates(context.Background(), normalized)
		if err != nil {
			result.Errors[normalized] = redactAddTaskError(err)
			continue
		}

		pendingCandidates = append(pendingCandidates, candidates...)
		for _, candidate := range candidates {
			if candidate.downloadGroup != nil {
				continue
			}
			if existingUrls[candidate.url] || historyDuplicates[candidate.url] || history.ContainsSource(candidate.url) {
				continue
			}
			if _, exists := batchThresholdSeen[candidate.url]; exists {
				continue
			}
			batchThresholdSeen[candidate.url] = struct{}{}
			batchGroupCount++
		}
	}

	if batchGroupCount >= 5 {
		batchGroup, err := newDownloadGroupPlan(downloadGroupKindBatch, batchGroupCount, time.Now())
		if err != nil {
			result.Errors["batch"] = redactAddTaskError(err)
			return result
		}
		for i := range pendingCandidates {
			if pendingCandidates[i].downloadGroup == nil {
				pendingCandidates[i].downloadGroup = batchGroup
			}
		}
	}

	for _, candidate := range pendingCandidates {
		a.submitAddCandidate(context.Background(), candidate, existingUrls, historyDuplicates, seenCandidates, &summary)
	}
	result.Succeeded = append(result.Succeeded, summary.succeeded...)
	result.Duplicates = append(result.Duplicates, summary.duplicates...)
	result.Groups = append(result.Groups, summary.groups...)

	return result
}

func (a *App) addNormalizedInput(ctx context.Context, normalizedURL string, existingURLs map[string]bool, historyDuplicates map[string]bool, candidateSeen map[string]bool, summary *addTaskSummary) {
	candidates, err := a.resolveAddCandidates(ctx, normalizedURL)
	if err != nil {
		summary.errors[normalizedURL] = redactAddTaskError(err)
		return
	}

	for _, candidate := range candidates {
		a.submitAddCandidate(ctx, candidate, existingURLs, historyDuplicates, candidateSeen, summary)
	}
}

func (a *App) submitAddCandidate(ctx context.Context, candidate addTaskCandidate, existingURLs map[string]bool, historyDuplicates map[string]bool, candidateSeen map[string]bool, summary *addTaskSummary) {
	displayKey := candidateDisplayKey(candidate)
	if isDuplicateAddCandidate(candidate, existingURLs, historyDuplicates, candidateSeen) {
		summary.duplicates = append(summary.duplicates, displayKey)
		return
	}

	if _, err := a.addTaskCandidate(ctx, candidate); err != nil {
		summary.errors[displayKey] = redactAddTaskError(err)
		return
	}

	summary.succeeded = append(summary.succeeded, displayKey)
	existingURLs[candidate.url] = true
	candidateSeen[candidate.url] = true
	if candidate.downloadGroup != nil {
		summary.addGroup(candidate.downloadGroup.groupCopy())
	}
}

func (s *addTaskSummary) addGroup(group rpc.DownloadGroup) {
	if group.ID == "" {
		return
	}
	if s.groupIDs == nil {
		s.groupIDs = make(map[string]struct{})
	}
	if _, exists := s.groupIDs[group.ID]; exists {
		return
	}
	s.groupIDs[group.ID] = struct{}{}
	s.groups = append(s.groups, group)
}

func (a *App) resolveAddCandidates(ctx context.Context, normalizedURL string) ([]addTaskCandidate, error) {
	if a == nil || a.extractorDispatcher == nil {
		return []addTaskCandidate{directAddTaskCandidate(normalizedURL)}, nil
	}

	resolution, err := a.extractorDispatcher.Resolve(ctx, normalizedURL)
	if err != nil {
		return nil, err
	}
	if !resolution.Matched {
		return []addTaskCandidate{directAddTaskCandidate(normalizedURL)}, nil
	}

	return addCandidatesFromResolution(normalizedURL, resolution)
}

func addCandidatesFromResolution(normalizedURL string, resolution extractor.AddTaskResolution) ([]addTaskCandidate, error) {
	if !resolution.Matched {
		return []addTaskCandidate{directAddTaskCandidate(normalizedURL)}, nil
	}

	var group *downloadGroupPlan
	if len(resolution.Items) >= 2 {
		var err error
		group, err = newDownloadGroupPlan(downloadGroupKindCollection, len(resolution.Items), time.Now())
		if err != nil {
			return nil, err
		}
	}

	candidates := make([]addTaskCandidate, 0, len(resolution.Items))
	for _, item := range resolution.Items {
		candidate := extractorAddTaskCandidate(item)
		candidate.downloadGroup = group
		candidates = append(candidates, candidate)
	}

	return candidates, nil
}

func directAddTaskCandidate(normalizedURL string) addTaskCandidate {
	return addTaskCandidate{
		sourceURL:  normalizedURL,
		url:        normalizedURL,
		displayKey: normalizedURL,
	}
}

func extractorAddTaskCandidate(item extractor.ResolvedAddItem) addTaskCandidate {
	displayKey := item.URL
	if displayKey == "" && item.ID != "" {
		displayKey = item.SourceURL + "#" + item.ID
	}

	return addTaskCandidate{
		sourceURL:  item.SourceURL,
		url:        item.URL,
		out:        item.Filename,
		sizeBytes:  item.SizeBytes,
		extracted:  true,
		protected:  item.AuthProfileRef != "" || item.HeaderProfileRef != "",
		displayKey: displayKey,
		item:       item,
	}
}

func candidateDisplayKey(candidate addTaskCandidate) string {
	if candidate.displayKey != "" {
		return candidate.displayKey
	}
	if candidate.url != "" {
		return candidate.url
	}
	return candidate.sourceURL
}

func isDuplicateAddCandidate(candidate addTaskCandidate, existingURLs map[string]bool, historyDuplicates map[string]bool, candidateSeen map[string]bool) bool {
	if existingURLs[candidate.url] || candidateSeen[candidate.url] {
		return true
	}
	if historyDuplicates != nil && historyDuplicates[candidate.url] {
		return true
	}

	return history.ContainsSource(candidate.url)
}

func (a *App) addTaskCandidate(ctx context.Context, candidate addTaskCandidate) (string, error) {
	out := ""
	if candidate.out != "" {
		safeOut, err := extractor.SafeAria2OutFilename(candidate.out)
		if err != nil {
			return "", err
		}
		out = safeOut
	}

	headers, err := a.buildCandidateHeaders(ctx, candidate)
	if err != nil {
		return "", err
	}
	registerGroup := func(gid string) error {
		if candidate.downloadGroup == nil || gid == "" {
			return nil
		}
		group := candidate.downloadGroup.groupCopy()
		monitor.Cache.RegisterTaskGroup(gid, group)
		if tracker := monitor.State.GetTracker(); tracker != nil {
			tracker.SetTaskGroup(gid, group)
		}
		return nil
	}

	dir := config.Current.DownloadDir
	if candidate.downloadGroup != nil {
		if err := candidate.downloadGroup.ensureDir(); err != nil {
			return "", err
		}
		group := candidate.downloadGroup.groupCopy()
		dir = group.Dir
	}
	if candidate.downloadGroup != nil {
		defer candidate.downloadGroup.cleanupIfUnused()
	}

	var gid string

	if config.Current.SmartThreadMode {
		fileSize := candidate.sizeBytes
		if !candidate.extracted && !candidate.protected && len(headers) == 0 {
			fileSize = rpc.HeadContentLength(candidate.url, 3*time.Second)
		}

		maxConn, _ := strconv.Atoi(config.Current.MaxConnections)
		if maxConn <= 0 {
			maxConn = 16
		}

		params := smartthread.Calculate(fileSize, maxConn, candidate.url)
		gid, err = rpc.AddUriWithAria2OptionsHook(candidate.url, rpc.AddURIOptions{
			Dir:          dir,
			Out:          out,
			Headers:      headers,
			Split:        params.Split,
			MinSplitSize: params.MinSize,
		}, registerGroup)
		if err != nil {
			return "", err
		}

		if gid != "" && params.Split > 0 {
			if tracker := monitor.State.GetTracker(); tracker != nil {
				tracker.SetThreadInfo(gid, params.Split, params.IsExploration)
			}
		}
	} else {
		gid, err = rpc.AddUriWithAria2OptionsHook(candidate.url, rpc.AddURIOptions{
			Dir:     dir,
			Out:     out,
			Headers: headers,
		}, registerGroup)
		if err != nil {
			return "", err
		}
	}

	if candidate.downloadGroup != nil {
		candidate.downloadGroup.recordSuccess()
	}

	return gid, nil
}

func (a *App) buildCandidateHeaders(ctx context.Context, candidate addTaskCandidate) ([]string, error) {
	if !candidate.extracted || a == nil || a.extractorDispatcher == nil {
		return nil, nil
	}

	return a.extractorDispatcher.BuildAria2Headers(ctx, candidate.item)
}

func firstErrorString(errors map[string]string) string {
	for _, err := range errors {
		return err
	}

	return ""
}

func redactAddTaskError(err error) string {
	if err == nil {
		return ""
	}

	return redactAssignmentValues(extractor.RedactSensitive(err.Error()))
}

func redactAssignmentValues(input string) string {
	markers := []string{"token=", "secret=", "auth=", "key="}
	var builder strings.Builder
	lower := strings.ToLower(input)
	for offset := 0; offset < len(input); {
		match := -1
		marker := ""
		for _, candidate := range markers {
			if idx := strings.Index(lower[offset:], candidate); idx >= 0 && (match < 0 || idx < match) {
				match = idx
				marker = candidate
			}
		}
		if match < 0 {
			builder.WriteString(input[offset:])
			break
		}

		start := offset + match
		valueStart := start + len(marker)
		valueEnd := valueStart
		for valueEnd < len(input) && !strings.ContainsRune(" \t\r\n&;,'\"`,)", rune(input[valueEnd])) {
			valueEnd++
		}

		builder.WriteString(input[offset:valueStart])
		if valueEnd > valueStart {
			builder.WriteString("[REDACTED]")
		}
		offset = valueEnd
	}

	return builder.String()
}

// GetActiveTasks returns only active and waiting tasks (high-frequency channel)
// This endpoint is optimized for frequent polling (every 1000ms)
// 优化：从后端 Cache 读取，避免重复调用 Aria2 RPC
func (a *App) GetActiveTasks() map[string][]rpc.Task {
	active := monitor.Cache.GetActive()
	waiting := monitor.Cache.GetWaiting()
	monitor.HydrateTaskGroups(active)
	monitor.HydrateTaskGroups(waiting)
	return map[string][]rpc.Task{
		"active":  active,
		"waiting": waiting,
	}
}

func (a *App) GetActiveProgress() []rpc.TaskProgress {
	progress, err := rpc.TellActiveProgress()
	if err != nil {
		return []rpc.TaskProgress{}
	}
	return progress
}

// GetStoppedTasks returns stopped tasks with history (low-frequency channel)
// Called on-demand when user switches to "Completed" tab or every 30s in background
// 业务逻辑（速度统计、历史写入）已迁移到 Monitor
// 优化：从后端 Cache 读取，避免重复调用 Aria2 RPC
func (a *App) GetStoppedTasks() []rpc.Task {
	if !config.Current.ShowHistory {
		return []rpc.Task{}
	}

	return stoppedTasksWithHistory(monitor.Cache.GetStopped())
}

func stoppedTasksWithHistory(stopped []rpc.Task) []rpc.Task {
	existingGIDs := make(map[string]struct{}, len(stopped))
	for i := range stopped {
		existingGIDs[stopped[i].GID] = struct{}{}
		if stopped[i].DownloadGroup == nil {
			stopped[i].DownloadGroup = monitor.Cache.GetTaskGroup(stopped[i].GID)
			if stopped[i].DownloadGroup == nil {
				stopped[i].DownloadGroup = monitor.GetStoredTaskGroup(stopped[i].GID)
			}
		}
		if entry, ok := history.Get(stopped[i].GID); ok {
			backfillStoppedTaskFromHistory(&stopped[i], entry)
		}
	}

	for _, entry := range history.GetMissingByGID(existingGIDs) {
		stopped = append(stopped, historyEntryToStoppedTask(entry))
		if entry.DownloadGroup != nil {
			monitor.RemoveTaskGroup(entry.GID)
		}
	}
	return stopped
}

func backfillStoppedTaskFromHistory(task *rpc.Task, entry history.HistoryEntry) {
	if task.DownloadGroup == nil && entry.DownloadGroup != nil {
		task.DownloadGroup = copyDownloadGroup(entry.DownloadGroup)
	}
	if entry.Path != "" && (len(task.Files) == 0 || task.Files[0].Path == "") {
		var uris []rpc.Uri
		if len(task.Files) > 0 && len(task.Files[0].Uris) > 0 {
			uris = task.Files[0].Uris
		} else {
			uris = historySourceURIs(entry.Source)
		}
		task.Files = []rpc.File{{Path: entry.Path, Uris: uris}}
	}
	if len(task.Files) > 0 && len(task.Files[0].Uris) == 0 && entry.Source != "" {
		task.Files[0].Uris = []rpc.Uri{{Uri: entry.Source}}
	}

	if task.TotalLength == "0" && isNonZeroLength(entry.TotalLength) {
		task.TotalLength = entry.TotalLength
	}
	if task.CompletedLength == "0" && isNonZeroLength(entry.CompletedLength) {
		task.CompletedLength = entry.CompletedLength
	}
}

func historyEntryToStoppedTask(entry history.HistoryEntry) rpc.Task {
	return rpc.Task{
		GID:             entry.GID,
		Status:          "complete",
		TotalLength:     entry.TotalLength,
		CompletedLength: entry.CompletedLength,
		Dir:             entry.Dir,
		Files:           []rpc.File{{Path: entry.Path, Uris: historySourceURIs(entry.Source)}},
		DownloadGroup:   copyDownloadGroup(entry.DownloadGroup),
	}
}

func historySourceURIs(source string) []rpc.Uri {
	if source == "" {
		return []rpc.Uri{}
	}
	return []rpc.Uri{{Uri: source}}
}

func isNonZeroLength(value string) bool {
	return value != "" && value != "0"
}

// GetTasks returns all tasks grouped by status
// 业务逻辑（历史写入）已迁移到 Monitor
// 优化：从后端 Cache 读取，避免重复调用 Aria2 RPC
func (a *App) GetTasks() map[string][]rpc.Task {
	active := monitor.Cache.GetActive()
	waiting := monitor.Cache.GetWaiting()
	monitor.HydrateTaskGroups(active)
	monitor.HydrateTaskGroups(waiting)
	var stopped []rpc.Task
	if config.Current.ShowHistory {
		stopped = stoppedTasksWithHistory(monitor.Cache.GetStopped())
	}
	return map[string][]rpc.Task{"active": active, "waiting": waiting, "stopped": stopped}
}

// GetTaskMetadata fetches detailed metadata for tasks with missing file paths
func (a *App) GetTaskMetadata(gids []string) map[string]rpc.Task {
	result := make(map[string]rpc.Task)
	if len(gids) == 0 {
		return result
	}

	tasks, err := rpc.TellStatusMulti(gids)
	if err == nil {
		for _, task := range tasks {
			if task != nil {
				if task.DownloadGroup == nil {
					task.DownloadGroup = monitor.Cache.GetTaskGroup(task.GID)
					if task.DownloadGroup == nil {
						task.DownloadGroup = monitor.GetStoredTaskGroup(task.GID)
					}
				}
				result[task.GID] = *task
			}
		}
	}
	return result
}

// PauseTask pauses a download task
func (a *App) PauseTask(gid string) {
	rpc.Pause(gid)
}

// ResumeTask resumes a paused task
func (a *App) ResumeTask(gid string) {
	rpc.Unpause(gid)
}

// BatchPause pauses multiple tasks
func (a *App) BatchPause(gids []string) {
	_ = rpc.PauseMulti(gids)
}

// BatchResume resumes multiple paused tasks
func (a *App) BatchResume(gids []string) {
	_ = rpc.UnpauseMulti(gids)
}

type removalTarget struct {
	path string
	dir  string
}

func removalTargetFromTask(task rpc.Task) (removalTarget, bool) {
	if len(task.Files) == 0 || task.Files[0].Path == "" {
		return removalTarget{}, false
	}
	return removalTarget{path: task.Files[0].Path, dir: task.Dir}, true
}

func removalTargetFromMetadata(meta *monitor.TaskMetadata) (removalTarget, bool) {
	if meta == nil || len(meta.Files) == 0 || meta.Files[0] == "" {
		return removalTarget{}, false
	}
	return removalTarget{path: meta.Files[0], dir: meta.Dir}, true
}

func removalTargetFromHistory(entry history.HistoryEntry) (removalTarget, bool) {
	if entry.Path == "" {
		return removalTarget{}, false
	}
	return removalTarget{path: entry.Path, dir: entry.Dir}, true
}

func normalizeRemovalGIDs(gids []string) []string {
	seen := make(map[string]struct{}, len(gids))
	unique := make([]string, 0, len(gids))
	for _, gid := range gids {
		gid = strings.TrimSpace(gid)
		if gid == "" {
			continue
		}
		if _, exists := seen[gid]; exists {
			continue
		}
		seen[gid] = struct{}{}
		unique = append(unique, gid)
	}
	return unique
}

func fillRemovalTargetsFromTasks(tasks []rpc.Task, unresolved map[string]struct{}, targets map[string]removalTarget) {
	for _, task := range tasks {
		if _, ok := unresolved[task.GID]; !ok {
			continue
		}
		target, ok := removalTargetFromTask(task)
		if !ok {
			continue
		}
		targets[task.GID] = target
		delete(unresolved, task.GID)
	}
}

func unresolvedRemovalGIDs(order []string, unresolved map[string]struct{}) []string {
	gids := make([]string, 0, len(unresolved))
	for _, gid := range order {
		if _, ok := unresolved[gid]; ok {
			gids = append(gids, gid)
		}
	}
	return gids
}

func resolveRemovalTargetsBatch(gids []string) map[string]removalTarget {
	uniqueGIDs := normalizeRemovalGIDs(gids)
	targets := make(map[string]removalTarget, len(uniqueGIDs))
	if len(uniqueGIDs) == 0 {
		return targets
	}

	unresolved := make(map[string]struct{}, len(uniqueGIDs))
	for _, gid := range uniqueGIDs {
		unresolved[gid] = struct{}{}
	}

	fillRemovalTargetsFromTasks(monitor.Cache.GetActive(), unresolved, targets)
	fillRemovalTargetsFromTasks(monitor.Cache.GetWaiting(), unresolved, targets)
	fillRemovalTargetsFromTasks(monitor.Cache.GetStopped(), unresolved, targets)

	for _, gid := range uniqueGIDs {
		if _, ok := unresolved[gid]; !ok {
			continue
		}
		target, ok := removalTargetFromMetadata(monitor.Cache.GetMetadata(gid))
		if !ok {
			continue
		}
		targets[gid] = target
		delete(unresolved, gid)
	}

	for _, gid := range uniqueGIDs {
		if _, ok := unresolved[gid]; !ok {
			continue
		}
		entry, ok := history.Get(gid)
		if !ok {
			continue
		}
		target, ok := removalTargetFromHistory(entry)
		if !ok {
			continue
		}
		targets[gid] = target
		delete(unresolved, gid)
	}

	fallbackGIDs := unresolvedRemovalGIDs(uniqueGIDs, unresolved)
	if len(fallbackGIDs) == 0 {
		return targets
	}

	tasks, err := rpc.TellStatusMulti(fallbackGIDs)
	if err != nil {
		return targets
	}

	for _, task := range tasks {
		if task == nil {
			continue
		}
		target, ok := removalTargetFromTask(*task)
		if !ok {
			continue
		}
		targets[task.GID] = target
	}

	return targets
}

func resolveRemovalTarget(gid string) removalTarget {
	return resolveRemovalTargetsBatch([]string{gid})[strings.TrimSpace(gid)]
}

func (a *App) removeTaskWithTarget(gid string, target removalTarget, deleteFile bool) {
	// 1. Remove from Aria2 memory and result list
	rpc.Remove(gid)

	// 2. Remove from history
	history.Remove(gid)

	a.cleanupRemovedTask(gid, target, deleteFile)
}

func (a *App) cleanupRemovedTask(gid string, target removalTarget, deleteFile bool) {
	// 3. Clean up from Tracker
	if tracker := monitor.State.GetTracker(); tracker != nil {
		tracker.RemoveTask(gid)
	}

	// 4. Invalidate cache and emit remove event
	// 这确保 lastStopped 缓存和元数据缓存被清理，防止幽灵任务
	if mon := monitor.State.GetMonitor(); mon != nil {
		mon.InvalidateTask(gid)
	} else {
		monitor.Cache.InvalidateMetadata(gid)
	}

	// 5. Physical cleanup
	if target.path == "" {
		return
	}

	go func(p string, dir string) {
		// Give Aria2 enough time to release file handle
		time.Sleep(1 * time.Second)

		cleanP := filepath.Clean(filepath.FromSlash(p))
		absPath := cleanP
		if !filepath.IsAbs(cleanP) {
			baseDir := dir
			if baseDir == "" {
				baseDir = config.Current.DownloadDir
			}
			absPath = filepath.Clean(filepath.Join(filepath.FromSlash(baseDir), cleanP))
		}

		// If user checked delete file
		if deleteFile {
			if fi, err := os.Stat(absPath); err == nil && fi.IsDir() {
				_ = os.RemoveAll(absPath)
			} else {
				_ = os.Remove(absPath)
			}
		}

		// Always remove .aria2 control file when task is removed from UI
		_ = os.Remove(absPath + ".aria2")

		// For some BT tasks, path might be a directory
		if strings.HasSuffix(absPath, ".torrent") {
			_ = os.Remove(absPath)
		}
	}(target.path, target.dir)
}

// BatchRemove removes multiple tasks
func (a *App) BatchRemove(gids []string, deleteFiles bool) {
	uniqueGIDs := normalizeRemovalGIDs(gids)
	if len(uniqueGIDs) == 0 {
		return
	}

	targets := resolveRemovalTargetsBatch(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		rpc.Remove(gid)
	}
	history.RemoveMany(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		a.cleanupRemovedTask(gid, targets[gid], deleteFiles)
	}
}

// RemoveTask removes a task and optionally deletes the file
func (a *App) RemoveTask(gid string, deleteFile bool) {
	gid = strings.TrimSpace(gid)
	if gid == "" {
		return
	}

	target := resolveRemovalTarget(gid)
	a.removeTaskWithTarget(gid, target, deleteFile)
}
