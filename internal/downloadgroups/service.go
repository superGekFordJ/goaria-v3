package downloadgroups

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

// --- Folder Planning and Folder Creation (from app_tasks_groups.go) ---

// --- Read operations (from app_tasks_group_read.go) ---

func GetDownloadGroups() DownloadGroupListEnvelope {
	snapshot := buildDownloadGroupReadSnapshot()
	degraded := false
	for _, card := range snapshot.cards {
		if card.Degraded {
			degraded = true
			break
		}
	}

	return DownloadGroupListEnvelope{
		Groups:    snapshot.cards,
		UpdatedAt: snapshot.updatedAt,
		Degraded:  degraded,
	}
}

func GetDownloadGroupDetail(groupKey string) DownloadGroupDetailEnvelope {
	key := strings.TrimSpace(groupKey)
	snapshot := buildDownloadGroupReadSnapshot()
	card, ok := snapshot.cardsByKey[key]
	if key == "" || !ok {
		return downloadGroupNotFoundEnvelope(key, snapshot.updatedAt)
	}

	bucket := snapshot.buckets[key]
	tasks := DownloadGroupTaskLists{
		Active:  filterDownloadGroupTasks(snapshot.active, bucket, "active"),
		Waiting: filterDownloadGroupTasks(snapshot.waiting, bucket, "waiting"),
		Stopped: filterDownloadGroupTasks(snapshot.stopped, bucket, "stopped"),
	}
	warnings := cloneDownloadGroupWarnings(card.Warnings)

	return DownloadGroupDetailEnvelope{
		GroupKey:  key,
		Found:     true,
		Group:     card,
		Tasks:     tasks,
		UpdatedAt: snapshot.updatedAt,
		Degraded:  card.Degraded || hasDegradedDownloadGroupWarning(warnings),
		Warnings:  warnings,
	}
}

// --- Write operations (from app_tasks_group_ops.go) ---

var OpenFolderLauncher func(dir string) error = func(dir string) error {
	return errors.New("open folder launcher not initialized")
}

var PauseMultiResults func(gids []string) ([]rpc.MultiCallItemResult, error) = rpc.PauseMultiResults

var ResumeMultiResults func(gids []string) ([]rpc.MultiCallItemResult, error) = rpc.UnpauseMultiResults

type downloadGroupOperationTarget struct {
	gid          string
	task         rpc.Task
	source       string
	status       string
	historyOnly  bool
	stale        bool
	pendingStart bool
}

// isMixedGroup reports whether the targets contain both sg_ and ar_ GIDs.
func isMixedGroup(targets []downloadGroupOperationTarget) bool {
	hasSg, hasAr := false, false
	for _, t := range targets {
		if monitor.IsSgGid(t.gid) {
			hasSg = true
		} else {
			hasAr = true
		}
	}
	return hasSg && hasAr
}

type downloadGroupOperationResolution struct {
	groupKey  string
	found     bool
	card      DownloadGroupCard
	bucket    *downloadGroupBucket
	targets   []downloadGroupOperationTarget
	warnings  []DownloadGroupWarning
	updatedAt int64
}

func PauseDownloadGroup(groupKey string) DownloadGroupOperationResult {
	return pauseResumeDownloadGroup(groupKey, DownloadGroupOperationActionPause)
}

func ResumeDownloadGroup(groupKey string) DownloadGroupOperationResult {
	return pauseResumeDownloadGroup(groupKey, DownloadGroupOperationActionResume)
}

func RemoveDownloadGroup(groupKey string, deleteFiles bool, removeTasks func(gids []string, deleteFile bool)) DownloadGroupOperationResult {
	resolution := resolveDownloadGroupOperation(groupKey)
	result := newDownloadGroupOperationResult(DownloadGroupOperationActionRemove, resolution.groupKey, resolution.updatedAt)
	if !resolution.found {
		result.addWarning(DownloadGroupWarning{Code: DownloadGroupOperationCodeGroupNotFound, Severity: "warning"})
		result.Refresh = downloadGroupRefreshHint(false, true, false, DownloadGroupOperationCodeGroupNotFound)
		result.finalizeOperationResult()
		return result
	}
	result.Found = true
	result.Warnings = append(result.Warnings, resolution.warnings...)

	uniqueGIDs := make([]string, 0, len(resolution.targets))
	seen := make(map[string]struct{}, len(resolution.targets))
	for _, target := range resolution.targets {
		if strings.TrimSpace(target.gid) == "" {
			continue
		}
		if _, ok := seen[target.gid]; ok {
			continue
		}
		seen[target.gid] = struct{}{}
		uniqueGIDs = append(uniqueGIDs, target.gid)
	}
	if len(uniqueGIDs) == 0 {
		result.addWarning(DownloadGroupWarning{Code: DownloadGroupOperationCodeNoActionableMembers, Severity: "info"})
		result.Refresh = downloadGroupRefreshHint(true, true, true, DownloadGroupOperationCodeNoActionableMembers)
		result.finalizeOperationResult()
		return result
	}

	// Remove tasks via the provided main delegate. The production delegate
	// (tasks.Service.BatchRemove -> cleanupRemovedTask) synchronously removes
	// each task from Cache and emits a remove delta, so no extra Cache/delta
	// work is needed here for sg_ GIDs; the later Surge event-path remove is
	// an idempotent no-op.
	removeTasks(uniqueGIDs, deleteFiles)

	for _, target := range resolution.targets {
		if _, ok := seen[target.gid]; !ok {
			continue
		}
		if strings.TrimSpace(target.gid) == "" {
			continue
		}
		code := DownloadGroupOperationCodeRemoved
		if target.stale {
			code = DownloadGroupOperationCodeRemovedStale
		} else if target.historyOnly || target.source == "stopped" {
			code = DownloadGroupOperationCodeRemoveAccepted
		}
		result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: DownloadGroupOperationItemSucceeded, Code: code})
		delete(seen, target.gid)
	}
	result.markAttempted()
	result.Refresh = downloadGroupRefreshHint(true, true, true, DownloadGroupOperationActionRemove)
	result.finalizeOperationResult()

	if deleteFiles && resolution.card.DownloadGroup != nil && resolution.card.DownloadGroup.Dir != "" {
		dir := filepath.Clean(resolution.card.DownloadGroup.Dir)
		go func(dir string) {
			time.Sleep(1500 * time.Millisecond)
			if !isSafeDownloadGroupFolderPathHint(dir) {
				return
			}
			absBase, err1 := filepath.Abs(config.Get().DownloadDir)
			absGroup, err2 := filepath.Abs(dir)
			if err1 == nil && err2 == nil && DownloadGroupPathContained(absBase, absGroup) {
				if isDirEmpty(dir) {
					_ = os.Remove(dir)
				}
			}
		}(dir)
	}

	return result
}

func OpenDownloadGroupFolder(groupKey string) DownloadGroupOperationResult {
	resolution := resolveDownloadGroupOperation(groupKey)
	result := newDownloadGroupOperationResult(DownloadGroupOperationActionOpenFolder, resolution.groupKey, resolution.updatedAt)
	if !resolution.found {
		result.addWarning(DownloadGroupWarning{Code: DownloadGroupOperationCodeGroupNotFound, Severity: "warning"})
		result.Refresh = downloadGroupRefreshHint(false, true, false, DownloadGroupOperationCodeGroupNotFound)
		result.finalizeOperationResult()
		return result
	}
	result.Found = true
	result.Warnings = append(result.Warnings, resolution.warnings...)

	dir := ""
	if resolution.card.DownloadGroup != nil {
		dir = strings.TrimSpace(resolution.card.DownloadGroup.Dir)
	}
	if dir == "" {
		result.addItem(DownloadGroupOperationItemResult{Status: DownloadGroupOperationItemFailed, Code: DownloadGroupOperationCodeFolderUnavailable, Message: downloadGroupOperationMessage(DownloadGroupOperationCodeFolderUnavailable)})
		result.Refresh = downloadGroupRefreshHint(false, true, true, DownloadGroupOperationCodeFolderUnavailable)
		result.finalizeOperationResult()
		return result
	}
	if !isSafeDownloadGroupFolderPathHint(dir) {
		result.addItem(DownloadGroupOperationItemResult{Status: DownloadGroupOperationItemFailed, Code: DownloadGroupOperationCodeFolderUnsafe, Message: downloadGroupOperationMessage(DownloadGroupOperationCodeFolderUnsafe)})
		result.Refresh = downloadGroupRefreshHint(false, true, true, DownloadGroupOperationCodeFolderUnsafe)
		result.finalizeOperationResult()
		return result
	}
	launchTarget, ok := resolveExactGroupFolderLaunchTarget(dir)
	if !ok {
		result.addItem(DownloadGroupOperationItemResult{Status: DownloadGroupOperationItemFailed, Code: DownloadGroupOperationCodeFolderUnavailable, Message: downloadGroupOperationMessage(DownloadGroupOperationCodeFolderUnavailable)})
		result.Refresh = downloadGroupRefreshHint(false, true, true, DownloadGroupOperationCodeFolderUnavailable)
		result.finalizeOperationResult()
		return result
	}
	result.markAttempted()
	if err := OpenFolderLauncher(launchTarget); err != nil {
		result.addItem(DownloadGroupOperationItemResult{Status: DownloadGroupOperationItemFailed, Code: DownloadGroupOperationCodeOpenFailed, Message: downloadGroupOperationMessage(DownloadGroupOperationCodeOpenFailed)})
		result.Refresh = downloadGroupRefreshHint(false, true, true, DownloadGroupOperationCodeOpenFailed)
		result.finalizeOperationResult()
		return result
	}

	result.addItem(DownloadGroupOperationItemResult{Status: DownloadGroupOperationItemSucceeded, Code: DownloadGroupOperationCodeOpened})
	result.Refresh = downloadGroupRefreshHint(false, false, false, "")
	result.finalizeOperationResult()
	return result
}

func pauseResumeDownloadGroup(groupKey string, action string) DownloadGroupOperationResult {
	resolution := resolveDownloadGroupOperation(groupKey)
	result := newDownloadGroupOperationResult(action, resolution.groupKey, resolution.updatedAt)
	if !resolution.found {
		result.addWarning(DownloadGroupWarning{Code: DownloadGroupOperationCodeGroupNotFound, Severity: "warning"})
		result.Refresh = downloadGroupRefreshHint(false, true, false, DownloadGroupOperationCodeGroupNotFound)
		result.finalizeOperationResult()
		return result
	}
	result.Found = true
	result.Warnings = append(result.Warnings, resolution.warnings...)

	actionable := make([]downloadGroupOperationTarget, 0)
	for _, target := range resolution.targets {
		if code, ok := downloadGroupPauseResumeSkipCode(action, target); ok {
			result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: DownloadGroupOperationItemSkipped, Code: code})
			continue
		}
		actionable = append(actionable, target)
	}

	if len(actionable) == 0 {
		result.addWarning(DownloadGroupWarning{Code: DownloadGroupOperationCodeNoActionableMembers, Severity: "info"})
		result.Refresh = downloadGroupRefreshHint(true, true, true, DownloadGroupOperationCodeNoActionableMembers)
		result.finalizeOperationResult()
		return result
	}

	gids := make([]string, len(actionable))
	for i, target := range actionable {
		gids[i] = target.gid
	}
	result.markAttempted()
	multiResults, err := callDownloadGroupPauseResumeRPC(action, gids)
	if err != nil {
		for _, target := range actionable {
			result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: DownloadGroupOperationItemFailed, Code: DownloadGroupOperationCodeRPCError, Message: downloadGroupOperationMessage(DownloadGroupOperationCodeRPCError)})
		}
		result.Refresh = downloadGroupRefreshHint(true, true, true, DownloadGroupOperationCodeRPCError)
		result.finalizeOperationResult()
		return result
	}

	multiByGID := make(map[string]rpc.MultiCallItemResult, len(multiResults))
	for _, item := range multiResults {
		if previous, exists := multiByGID[item.GID]; !exists || !previous.OK {
			multiByGID[item.GID] = item
		}
	}
	mon := monitor.State.GetMonitor()
	successCode := DownloadGroupOperationCodePaused
	if action == DownloadGroupOperationActionResume {
		successCode = DownloadGroupOperationCodeResumed
	}
	for _, target := range actionable {
		item, ok := multiByGID[target.gid]
		if ok && item.OK {
			result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: DownloadGroupOperationItemSucceeded, Code: successCode})
			if action == DownloadGroupOperationActionPause {
				mon.ApplyPause(target.gid)
			} else {
				mon.ApplyResume(target.gid)
			}
			continue
		}
		result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: DownloadGroupOperationItemFailed, Code: DownloadGroupOperationCodeRPCError, Message: downloadGroupOperationMessage(DownloadGroupOperationCodeRPCError)})
	}
	result.Refresh = downloadGroupRefreshHint(true, true, true, action)
	result.finalizeOperationResult()
	return result
}

func newDownloadGroupOperationResult(action, groupKey string, updatedAt int64) DownloadGroupOperationResult {
	if updatedAt == 0 {
		updatedAt = time.Now().Unix()
	}
	return DownloadGroupOperationResult{
		GroupKey:  groupKey,
		Action:    action,
		OK:        true,
		Noop:      true,
		Items:     []DownloadGroupOperationItemResult{},
		UpdatedAt: updatedAt,
	}
}

func (r *DownloadGroupOperationResult) addItem(item DownloadGroupOperationItemResult) {
	r.Items = append(r.Items, item)
}

func (r *DownloadGroupOperationResult) addWarning(warning DownloadGroupWarning) {
	if warning.Code == "" {
		return
	}
	r.Warnings = append(r.Warnings, warning)
}

func (r *DownloadGroupOperationResult) markAttempted() {
	r.attempted = true
}

func (r *DownloadGroupOperationResult) finalizeOperationResult() {
	r.TotalTargets = 0
	r.Succeeded = 0
	r.Skipped = 0
	r.Failed = 0
	for _, item := range r.Items {
		switch item.Status {
		case DownloadGroupOperationItemSucceeded:
			r.Succeeded++
			r.TotalTargets++
		case DownloadGroupOperationItemSkipped:
			r.Skipped++
		case DownloadGroupOperationItemFailed:
			r.Failed++
			r.TotalTargets++
		}
	}
	r.OK = r.Failed == 0
	r.Noop = !r.attempted
	if len(r.Items) == 0 {
		r.Items = nil
	}
	if len(r.Warnings) == 0 {
		r.Warnings = nil
	}
}

func resolveDownloadGroupOperation(groupKey string) downloadGroupOperationResolution {
	key := strings.TrimSpace(groupKey)
	snapshot := buildDownloadGroupReadSnapshot()
	resolution := downloadGroupOperationResolution{groupKey: safeDownloadGroupOperationResultKey(key), updatedAt: snapshot.updatedAt}
	card, ok := snapshot.cardsByKey[key]
	if key == "" || !ok {
		return resolution
	}
	bucket := snapshot.buckets[key]
	resolution.groupKey = key
	resolution.found = true
	resolution.card = card
	resolution.bucket = bucket
	resolution.warnings = cloneDownloadGroupWarnings(card.Warnings)
	resolution.targets = buildDownloadGroupOperationTargets(snapshot, bucket)
	return resolution
}

func buildDownloadGroupOperationTargets(snapshot downloadGroupReadSnapshot, bucket *downloadGroupBucket) []downloadGroupOperationTarget {
	if bucket == nil {
		return nil
	}
	targets := make([]downloadGroupOperationTarget, 0, len(bucket.members)+len(bucket.staleStoreGID))
	appendMembers := func(source string, tasks []rpc.Task) {
		for _, task := range tasks {
			member, ok := bucket.members[task.GID]
			if !ok || member.source != source {
				continue
			}
			status := strings.TrimSpace(member.task.Status)
			targets = append(targets, downloadGroupOperationTarget{
				gid:          member.task.GID,
				task:         cloneDownloadGroupTask(member.task),
				source:       member.source,
				status:       status,
				historyOnly:  member.historyOnly,
				pendingStart: source == "stopped" && monitor.Cache.IsPendingStart(task.GID),
			})
		}
	}
	appendMembers("active", snapshot.active)
	appendMembers("waiting", snapshot.waiting)
	appendMembers("stopped", snapshot.stopped)
	stale := make([]string, 0, len(bucket.staleStoreGID))
	for gid := range bucket.staleStoreGID {
		stale = append(stale, gid)
	}
	sort.Strings(stale)
	for _, gid := range stale {
		if monitor.Cache.IsPendingStart(gid) {
			targets = append(targets, downloadGroupOperationTarget{
				gid:          gid,
				source:       "waiting",
				status:       "waiting",
				stale:        true,
				pendingStart: true,
			})
			continue
		}
		targets = append(targets, downloadGroupOperationTarget{gid: gid, source: "stale", status: "stale", stale: true})
	}
	return targets
}

func downloadGroupPauseResumeSkipCode(action string, target downloadGroupOperationTarget) (string, bool) {
	if target.pendingStart {
		if action == DownloadGroupOperationActionPause {
			return "", false
		}
		return DownloadGroupOperationCodeTerminalState, true
	}
	if target.stale {
		return DownloadGroupOperationCodeStaleMember, true
	}
	if target.historyOnly {
		return DownloadGroupOperationCodeHistoryOnly, true
	}
	if target.source == "stopped" {
		if action == DownloadGroupOperationActionResume && target.status == DownloadGroupStatusError {
			return "", false
		}
		return DownloadGroupOperationCodeTerminalState, true
	}
	if action == DownloadGroupOperationActionPause {
		if target.status == DownloadGroupStatusPaused {
			return DownloadGroupOperationCodeAlreadyPaused, true
		}
		if target.source == "active" || target.source == "waiting" {
			return "", false
		}
		return DownloadGroupOperationCodeTerminalState, true
	}
	if target.status == DownloadGroupStatusPaused {
		return "", false
	}
	if target.source == "active" {
		return DownloadGroupOperationCodeAlreadyActive, true
	}
	if target.source == "waiting" {
		return DownloadGroupOperationCodeNotPaused, true
	}
	return DownloadGroupOperationCodeTerminalState, true
}

func callDownloadGroupPauseResumeRPC(action string, gids []string) ([]rpc.MultiCallItemResult, error) {
	if action == DownloadGroupOperationActionResume {
		return ResumeMultiResults(gids)
	}
	return PauseMultiResults(gids)
}

func downloadGroupRefreshHint(tasks, groups, detail bool, reason string) DownloadGroupOperationRefreshHint {
	return DownloadGroupOperationRefreshHint{Tasks: tasks, Groups: groups, Detail: detail, Reason: reason}
}

func downloadGroupOperationMessage(code string) string {
	switch code {
	case DownloadGroupOperationCodeFolderUnavailable:
		return "folder unavailable"
	case DownloadGroupOperationCodeFolderUnsafe:
		return "folder path is unsafe"
	case DownloadGroupOperationCodeOpenFailed, DownloadGroupOperationCodeRPCError:
		return "operation failed"
	default:
		return ""
	}
}

func safeDownloadGroupOperationResultKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	lower := strings.ToLower(key)
	if strings.Contains(lower, "://") || strings.ContainsAny(key, "/\\?#&=:") || downloadGroupSecretLikeSegment(key) {
		return ""
	}
	return key
}

func resolveExactGroupFolderLaunchTarget(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	cleaned := filepath.Clean(filepath.FromSlash(path))
	if cleaned == "" {
		return "", false
	}
	if st, err := os.Stat(cleaned); err == nil && st.IsDir() {
		return cleaned, true
	}
	return "", false
}

func isDirEmpty(name string) bool {
	entries, err := os.ReadDir(name)
	if err != nil {
		return false
	}
	return len(entries) == 0
}
