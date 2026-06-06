package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

const (
	downloadGroupOperationActionPause      = "pause"
	downloadGroupOperationActionResume     = "resume"
	downloadGroupOperationActionRemove     = "remove"
	downloadGroupOperationActionOpenFolder = "open_folder"

	downloadGroupOperationItemSucceeded = "succeeded"
	downloadGroupOperationItemSkipped   = "skipped"
	downloadGroupOperationItemFailed    = "failed"

	downloadGroupOperationCodeGroupNotFound       = "group_not_found"
	downloadGroupOperationCodeEmptyGroup          = "empty_group"
	downloadGroupOperationCodeNoActionableMembers = "no_actionable_members"
	downloadGroupOperationCodeStaleMember         = "stale_member"
	downloadGroupOperationCodeMissingMember       = "missing_member"
	downloadGroupOperationCodePartialFailure      = "partial_failure"
	downloadGroupOperationCodeRPCError            = "rpc_error"
	downloadGroupOperationCodePaused              = "paused"
	downloadGroupOperationCodeAlreadyPaused       = "already_paused"
	downloadGroupOperationCodeTerminalState       = "terminal_state"
	downloadGroupOperationCodeHistoryOnly         = "history_only"
	downloadGroupOperationCodeResumed             = "resumed"
	downloadGroupOperationCodeAlreadyActive       = "already_active"
	downloadGroupOperationCodeNotPaused           = "not_paused"
	downloadGroupOperationCodeRemoved             = "removed"
	downloadGroupOperationCodeRemovedStale        = "removed_stale_metadata"
	downloadGroupOperationCodeRemoveAccepted      = "remove_accepted"
	downloadGroupOperationCodeOpened              = "opened"
	downloadGroupOperationCodeFolderUnavailable   = "folder_unavailable"
	downloadGroupOperationCodeFolderUnsafe        = "folder_unsafe"
	downloadGroupOperationCodeOpenFailed          = "open_failed"
)

type DownloadGroupOperationResult struct {
	GroupKey     string                             `json:"group_key"`
	Action       string                             `json:"action"`
	OK           bool                               `json:"ok"`
	Found        bool                               `json:"found"`
	Noop         bool                               `json:"noop"`
	TotalTargets int                                `json:"total_targets"`
	Succeeded    int                                `json:"succeeded"`
	Skipped      int                                `json:"skipped"`
	Failed       int                                `json:"failed"`
	Items        []DownloadGroupOperationItemResult `json:"items,omitempty"`
	Warnings     []DownloadGroupWarning             `json:"warnings,omitempty"`
	Refresh      DownloadGroupOperationRefreshHint  `json:"refresh"`
	UpdatedAt    int64                              `json:"updated_at"`

	attempted bool
}

type DownloadGroupOperationItemResult struct {
	GID     string `json:"gid,omitempty"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type DownloadGroupOperationRefreshHint struct {
	Tasks  bool   `json:"tasks"`
	Groups bool   `json:"groups"`
	Detail bool   `json:"detail"`
	Reason string `json:"reason,omitempty"`
}

type downloadGroupOperationTarget struct {
	gid         string
	task        rpc.Task
	source      string
	status      string
	historyOnly bool
	stale       bool
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

func (a *App) PauseDownloadGroup(groupKey string) DownloadGroupOperationResult {
	return a.pauseResumeDownloadGroup(groupKey, downloadGroupOperationActionPause)
}

func (a *App) ResumeDownloadGroup(groupKey string) DownloadGroupOperationResult {
	return a.pauseResumeDownloadGroup(groupKey, downloadGroupOperationActionResume)
}

func (a *App) RemoveDownloadGroup(groupKey string, deleteFiles bool) DownloadGroupOperationResult {
	resolution := resolveDownloadGroupOperation(groupKey)
	result := newDownloadGroupOperationResult(downloadGroupOperationActionRemove, resolution.groupKey, resolution.updatedAt)
	if !resolution.found {
		result.addWarning(DownloadGroupWarning{Code: downloadGroupOperationCodeGroupNotFound, Severity: "warning"})
		result.Refresh = downloadGroupRefreshHint(false, true, false, downloadGroupOperationCodeGroupNotFound)
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
		result.addWarning(DownloadGroupWarning{Code: downloadGroupOperationCodeNoActionableMembers, Severity: "info"})
		result.Refresh = downloadGroupRefreshHint(true, true, true, downloadGroupOperationCodeNoActionableMembers)
		result.finalizeOperationResult()
		return result
	}

	targets := resolveRemovalTargetsBatch(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		rpc.Remove(gid)
	}
	history.RemoveMany(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		a.cleanupRemovedTask(gid, targets[gid], deleteFiles)
	}

	for _, target := range resolution.targets {
		if _, ok := seen[target.gid]; !ok {
			continue
		}
		if strings.TrimSpace(target.gid) == "" {
			continue
		}
		code := downloadGroupOperationCodeRemoved
		if target.stale {
			code = downloadGroupOperationCodeRemovedStale
		} else if target.historyOnly || target.source == "stopped" {
			code = downloadGroupOperationCodeRemoveAccepted
		}
		result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: downloadGroupOperationItemSucceeded, Code: code})
		delete(seen, target.gid)
	}
	result.markAttempted()
	result.Refresh = downloadGroupRefreshHint(true, true, true, downloadGroupOperationActionRemove)
	result.finalizeOperationResult()
	return result
}

func (a *App) OpenDownloadGroupFolder(groupKey string) DownloadGroupOperationResult {
	resolution := resolveDownloadGroupOperation(groupKey)
	result := newDownloadGroupOperationResult(downloadGroupOperationActionOpenFolder, resolution.groupKey, resolution.updatedAt)
	if !resolution.found {
		result.addWarning(DownloadGroupWarning{Code: downloadGroupOperationCodeGroupNotFound, Severity: "warning"})
		result.Refresh = downloadGroupRefreshHint(false, true, false, downloadGroupOperationCodeGroupNotFound)
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
		result.addItem(DownloadGroupOperationItemResult{Status: downloadGroupOperationItemFailed, Code: downloadGroupOperationCodeFolderUnavailable, Message: downloadGroupOperationMessage(downloadGroupOperationCodeFolderUnavailable)})
		result.Refresh = downloadGroupRefreshHint(false, true, true, downloadGroupOperationCodeFolderUnavailable)
		result.finalizeOperationResult()
		return result
	}
	if !isSafeDownloadGroupFolderPathHint(dir) {
		result.addItem(DownloadGroupOperationItemResult{Status: downloadGroupOperationItemFailed, Code: downloadGroupOperationCodeFolderUnsafe, Message: downloadGroupOperationMessage(downloadGroupOperationCodeFolderUnsafe)})
		result.Refresh = downloadGroupRefreshHint(false, true, true, downloadGroupOperationCodeFolderUnsafe)
		result.finalizeOperationResult()
		return result
	}
	launchTarget, ok := resolveExactGroupFolderLaunchTarget(dir)
	if !ok {
		result.addItem(DownloadGroupOperationItemResult{Status: downloadGroupOperationItemFailed, Code: downloadGroupOperationCodeFolderUnavailable, Message: downloadGroupOperationMessage(downloadGroupOperationCodeFolderUnavailable)})
		result.Refresh = downloadGroupRefreshHint(false, true, true, downloadGroupOperationCodeFolderUnavailable)
		result.finalizeOperationResult()
		return result
	}
	result.markAttempted()
	if err := openFolderLauncher(launchTarget); err != nil {
		result.addItem(DownloadGroupOperationItemResult{Status: downloadGroupOperationItemFailed, Code: downloadGroupOperationCodeOpenFailed, Message: downloadGroupOperationMessage(downloadGroupOperationCodeOpenFailed)})
		result.Refresh = downloadGroupRefreshHint(false, true, true, downloadGroupOperationCodeOpenFailed)
		result.finalizeOperationResult()
		return result
	}

	result.addItem(DownloadGroupOperationItemResult{Status: downloadGroupOperationItemSucceeded, Code: downloadGroupOperationCodeOpened})
	result.Refresh = downloadGroupRefreshHint(false, false, false, "")
	result.finalizeOperationResult()
	return result
}

func (a *App) pauseResumeDownloadGroup(groupKey string, action string) DownloadGroupOperationResult {
	resolution := resolveDownloadGroupOperation(groupKey)
	result := newDownloadGroupOperationResult(action, resolution.groupKey, resolution.updatedAt)
	if !resolution.found {
		result.addWarning(DownloadGroupWarning{Code: downloadGroupOperationCodeGroupNotFound, Severity: "warning"})
		result.Refresh = downloadGroupRefreshHint(false, true, false, downloadGroupOperationCodeGroupNotFound)
		result.finalizeOperationResult()
		return result
	}
	result.Found = true
	result.Warnings = append(result.Warnings, resolution.warnings...)

	actionable := make([]downloadGroupOperationTarget, 0)
	for _, target := range resolution.targets {
		if code, ok := downloadGroupPauseResumeSkipCode(action, target); ok {
			result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: downloadGroupOperationItemSkipped, Code: code})
			continue
		}
		actionable = append(actionable, target)
	}

	if len(actionable) == 0 {
		result.addWarning(DownloadGroupWarning{Code: downloadGroupOperationCodeNoActionableMembers, Severity: "info"})
		result.Refresh = downloadGroupRefreshHint(true, true, true, downloadGroupOperationCodeNoActionableMembers)
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
			result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: downloadGroupOperationItemFailed, Code: downloadGroupOperationCodeRPCError, Message: downloadGroupOperationMessage(downloadGroupOperationCodeRPCError)})
		}
		result.Refresh = downloadGroupRefreshHint(true, true, true, downloadGroupOperationCodeRPCError)
		result.finalizeOperationResult()
		return result
	}

	multiByGID := make(map[string]rpc.MultiCallItemResult, len(multiResults))
	for _, item := range multiResults {
		multiByGID[item.GID] = item
	}
	successCode := downloadGroupOperationCodePaused
	if action == downloadGroupOperationActionResume {
		successCode = downloadGroupOperationCodeResumed
	}
	for _, target := range actionable {
		item, ok := multiByGID[target.gid]
		if ok && item.OK {
			result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: downloadGroupOperationItemSucceeded, Code: successCode})
			continue
		}
		result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: downloadGroupOperationItemFailed, Code: downloadGroupOperationCodeRPCError, Message: downloadGroupOperationMessage(downloadGroupOperationCodeRPCError)})
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
		case downloadGroupOperationItemSucceeded:
			r.Succeeded++
			r.TotalTargets++
		case downloadGroupOperationItemSkipped:
			r.Skipped++
		case downloadGroupOperationItemFailed:
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
				gid:         member.task.GID,
				task:        cloneDownloadGroupTask(member.task),
				source:      member.source,
				status:      status,
				historyOnly: member.historyOnly,
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
		targets = append(targets, downloadGroupOperationTarget{gid: gid, source: "stale", status: "stale", stale: true})
	}
	return targets
}

func downloadGroupPauseResumeSkipCode(action string, target downloadGroupOperationTarget) (string, bool) {
	if target.stale {
		return downloadGroupOperationCodeStaleMember, true
	}
	if target.historyOnly {
		return downloadGroupOperationCodeHistoryOnly, true
	}
	if target.source == "stopped" {
		return downloadGroupOperationCodeTerminalState, true
	}
	if action == downloadGroupOperationActionPause {
		if target.status == downloadGroupStatusPaused {
			return downloadGroupOperationCodeAlreadyPaused, true
		}
		if target.source == "active" || target.source == "waiting" {
			return "", false
		}
		return downloadGroupOperationCodeTerminalState, true
	}
	if target.source == "active" {
		return downloadGroupOperationCodeAlreadyActive, true
	}
	if target.source == "waiting" && target.status == downloadGroupStatusPaused {
		return "", false
	}
	if target.source == "waiting" {
		return downloadGroupOperationCodeNotPaused, true
	}
	return downloadGroupOperationCodeTerminalState, true
}

func callDownloadGroupPauseResumeRPC(action string, gids []string) ([]rpc.MultiCallItemResult, error) {
	if action == downloadGroupOperationActionResume {
		return rpc.UnpauseMultiResults(gids)
	}
	return rpc.PauseMultiResults(gids)
}

func downloadGroupRefreshHint(tasks, groups, detail bool, reason string) DownloadGroupOperationRefreshHint {
	return DownloadGroupOperationRefreshHint{Tasks: tasks, Groups: groups, Detail: detail, Reason: reason}
}

func downloadGroupOperationMessage(code string) string {
	switch code {
	case downloadGroupOperationCodeFolderUnavailable:
		return "folder unavailable"
	case downloadGroupOperationCodeFolderUnsafe:
		return "folder path is unsafe"
	case downloadGroupOperationCodeOpenFailed, downloadGroupOperationCodeRPCError:
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

func resolveExactGroupFolderLaunchTarget(path string) (openFolderLaunchTarget, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return openFolderLaunchTarget{}, false
	}
	cleaned := filepath.Clean(filepath.FromSlash(path))
	if cleaned == "" {
		return openFolderLaunchTarget{}, false
	}
	if st, err := os.Stat(cleaned); err == nil && st.IsDir() {
		return openFolderLaunchTarget{OpenDir: cleaned}, true
	}
	return openFolderLaunchTarget{}, false
}
