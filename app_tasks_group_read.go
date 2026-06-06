package main

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

const (
	downloadGroupNameStatusStable   = rpc.DownloadGroupNameStatusStable
	downloadGroupNameStatusPending  = rpc.DownloadGroupNameStatusPending
	downloadGroupNameStatusFallback = rpc.DownloadGroupNameStatusFallback
	downloadGroupNameStatusDegraded = rpc.DownloadGroupNameStatusDegraded

	downloadGroupStatusUnknown  = "unknown"
	downloadGroupStatusActive   = "active"
	downloadGroupStatusPaused   = "paused"
	downloadGroupStatusWaiting  = "waiting"
	downloadGroupStatusError    = "error"
	downloadGroupStatusComplete = "complete"

	downloadGroupWarningMixedStatus     = "mixed_status"
	downloadGroupWarningPartialError    = "partial_error"
	downloadGroupWarningMissingMembers  = "missing_members"
	downloadGroupWarningMissingMetadata = "missing_metadata"
	downloadGroupWarningHistoryOnly     = "history_only"
	downloadGroupWarningStaleGroup      = "stale_group"
	downloadGroupWarningNamePending     = "name_pending"
	downloadGroupWarningNameDegraded    = "name_degraded"
	downloadGroupWarningGroupNotFound   = "group_not_found"
)

type DownloadGroupListEnvelope struct {
	Groups    []DownloadGroupCard    `json:"groups"`
	UpdatedAt int64                  `json:"updated_at"`
	Degraded  bool                   `json:"degraded"`
	Warnings  []DownloadGroupWarning `json:"warnings,omitempty"`
}

type DownloadGroupDetailEnvelope struct {
	GroupKey  string                 `json:"group_key"`
	Found     bool                   `json:"found"`
	Group     DownloadGroupCard      `json:"group"`
	Tasks     DownloadGroupTaskLists `json:"tasks"`
	UpdatedAt int64                  `json:"updated_at"`
	Degraded  bool                   `json:"degraded"`
	Warnings  []DownloadGroupWarning `json:"warnings,omitempty"`
}

type DownloadGroupTaskLists struct {
	Active  []rpc.Task `json:"active"`
	Waiting []rpc.Task `json:"waiting"`
	Stopped []rpc.Task `json:"stopped"`
}

type DownloadGroupCard struct {
	GroupKey        string                    `json:"group_key"`
	DownloadGroup   *rpc.DownloadGroup        `json:"download_group,omitempty"`
	Kind            string                    `json:"kind"`
	DisplayName     string                    `json:"display_name"`
	FallbackName    string                    `json:"fallback_name"`
	NameStatus      string                    `json:"name_status"`
	Status          string                    `json:"status"`
	Degraded        bool                      `json:"degraded"`
	Warnings        []DownloadGroupWarning    `json:"warnings,omitempty"`
	Counts          DownloadGroupMemberCounts `json:"counts"`
	TotalLength     string                    `json:"total_length"`
	CompletedLength string                    `json:"completed_length"`
	DownloadSpeed   string                    `json:"download_speed"`
	Progress        float64                   `json:"progress"`
	CreatedAt       int64                     `json:"created_at"`
	UpdatedAt       int64                     `json:"updated_at"`
	FolderLabel     string                    `json:"folder_label,omitempty"`
	FolderPathHint  string                    `json:"folder_path_hint,omitempty"`
	HasFolder       bool                      `json:"has_folder"`
}

type DownloadGroupMemberCounts struct {
	Expected    int `json:"expected"`
	Resolved    int `json:"resolved"`
	Missing     int `json:"missing"`
	Active      int `json:"active"`
	Waiting     int `json:"waiting"`
	Paused      int `json:"paused"`
	Complete    int `json:"complete"`
	Error       int `json:"error"`
	HistoryOnly int `json:"history_only"`
}

type DownloadGroupWarning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Count    int    `json:"count,omitempty"`
}

type downloadGroupReadSnapshot struct {
	updatedAt  int64
	active     []rpc.Task
	waiting    []rpc.Task
	stopped    []rpc.Task
	buckets    map[string]*downloadGroupBucket
	cards      []DownloadGroupCard
	cardsByKey map[string]DownloadGroupCard
}

type downloadGroupBucket struct {
	groupKey      string
	group         rpc.DownloadGroup
	hasGroup      bool
	itemCount     int
	members       map[string]downloadGroupMember
	storeGroups   map[string]rpc.DownloadGroup
	staleStoreGID map[string]struct{}
}

type downloadGroupMember struct {
	task        rpc.Task
	source      string
	priority    int
	historyOnly bool
	completedAt int64
}

func (a *App) GetDownloadGroups() DownloadGroupListEnvelope {
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

func (a *App) GetDownloadGroupDetail(groupKey string) DownloadGroupDetailEnvelope {
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

func buildDownloadGroupReadSnapshot() downloadGroupReadSnapshot {
	active := cloneDownloadGroupTasks(monitor.Cache.GetActive())
	waiting := cloneDownloadGroupTasks(monitor.Cache.GetWaiting())
	monitor.HydrateTaskGroups(active)
	monitor.HydrateTaskGroups(waiting)

	stoppedCache := cloneDownloadGroupTasks(monitor.Cache.GetStopped())
	originalStoppedGIDs := make(map[string]struct{}, len(stoppedCache))
	for _, task := range stoppedCache {
		if task.GID != "" {
			originalStoppedGIDs[task.GID] = struct{}{}
		}
	}
	historyEntries := history.GetAll()
	historyByGID := make(map[string]history.HistoryEntry, len(historyEntries))
	for _, entry := range historyEntries {
		if entry.GID != "" {
			historyByGID[entry.GID] = entry
		}
	}
	stopped := cloneDownloadGroupTasks(stoppedTasksWithHistory(stoppedCache))

	bestPriority := make(map[string]int)
	markDownloadGroupBestPriority(bestPriority, active, 3)
	markDownloadGroupBestPriority(bestPriority, waiting, 2)
	markDownloadGroupBestPriority(bestPriority, stopped, 1)

	buckets := make(map[string]*downloadGroupBucket)
	addDownloadGroupTasksToBuckets(buckets, bestPriority, active, "active", 3, nil, nil)
	addDownloadGroupTasksToBuckets(buckets, bestPriority, waiting, "waiting", 2, nil, nil)
	addDownloadGroupTasksToBuckets(buckets, bestPriority, stopped, "stopped", 1, originalStoppedGIDs, historyByGID)
	addStoredDownloadGroupsToBuckets(buckets, monitor.ListStoredTaskGroups())

	cards := make([]DownloadGroupCard, 0, len(buckets))
	for _, bucket := range buckets {
		bucket.finalizeStaleStoreMembers()
		if !bucket.shouldIncludeCard() {
			continue
		}
		cards = append(cards, bucket.buildCard())
	}
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].UpdatedAt != cards[j].UpdatedAt {
			return cards[i].UpdatedAt > cards[j].UpdatedAt
		}
		if cards[i].CreatedAt != cards[j].CreatedAt {
			return cards[i].CreatedAt > cards[j].CreatedAt
		}
		return cards[i].GroupKey < cards[j].GroupKey
	})
	cardsByKey := make(map[string]DownloadGroupCard, len(cards))
	for _, card := range cards {
		cardsByKey[card.GroupKey] = card
	}

	return downloadGroupReadSnapshot{
		updatedAt:  time.Now().Unix(),
		active:     active,
		waiting:    waiting,
		stopped:    stopped,
		buckets:    buckets,
		cards:      cards,
		cardsByKey: cardsByKey,
	}
}

func markDownloadGroupBestPriority(best map[string]int, tasks []rpc.Task, priority int) {
	for _, task := range tasks {
		if strings.TrimSpace(task.GID) == "" {
			continue
		}
		if current, ok := best[task.GID]; !ok || priority > current {
			best[task.GID] = priority
		}
	}
}

func addDownloadGroupTasksToBuckets(buckets map[string]*downloadGroupBucket, bestPriority map[string]int, tasks []rpc.Task, source string, priority int, originalStoppedGIDs map[string]struct{}, historyByGID map[string]history.HistoryEntry) {
	for _, task := range tasks {
		if strings.TrimSpace(task.GID) == "" || bestPriority[task.GID] != priority {
			continue
		}
		if task.DownloadGroup == nil || strings.TrimSpace(task.DownloadGroup.ID) == "" {
			continue
		}
		historyOnly := false
		completedAt := int64(0)
		if source == "stopped" && historyByGID != nil {
			if entry, ok := historyByGID[task.GID]; ok {
				completedAt = entry.CompletedAt
				_, wasStopped := originalStoppedGIDs[task.GID]
				historyOnly = !wasStopped
			}
		}

		bucket := getDownloadGroupBucket(buckets, *task.DownloadGroup)
		bucket.addMember(task.GID, downloadGroupMember{
			task:        cloneDownloadGroupTask(task),
			source:      source,
			priority:    priority,
			historyOnly: historyOnly,
			completedAt: completedAt,
		})
	}
}

func addStoredDownloadGroupsToBuckets(buckets map[string]*downloadGroupBucket, stored map[string]rpc.DownloadGroup) {
	gids := make([]string, 0, len(stored))
	for gid := range stored {
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	for _, gid := range gids {
		group := stored[gid]
		if strings.TrimSpace(gid) == "" || strings.TrimSpace(group.ID) == "" {
			continue
		}
		bucket := getDownloadGroupBucket(buckets, group)
		bucket.storeGroups[gid] = group
	}
}

func getDownloadGroupBucket(buckets map[string]*downloadGroupBucket, group rpc.DownloadGroup) *downloadGroupBucket {
	key := group.ID
	bucket := buckets[key]
	if bucket == nil {
		bucket = &downloadGroupBucket{
			groupKey:      key,
			members:       make(map[string]downloadGroupMember),
			storeGroups:   make(map[string]rpc.DownloadGroup),
			staleStoreGID: make(map[string]struct{}),
		}
		buckets[key] = bucket
	}
	bucket.recordGroup(group)
	return bucket
}

func (b *downloadGroupBucket) recordGroup(group rpc.DownloadGroup) {
	if strings.TrimSpace(group.ID) == "" {
		return
	}
	if group.ItemCount > b.itemCount {
		b.itemCount = group.ItemCount
	}
	if !b.hasGroup || betterDownloadGroupRepresentative(group, b.group) {
		b.group = group
		b.hasGroup = true
	}
}

func betterDownloadGroupRepresentative(candidate, current rpc.DownloadGroup) bool {
	if current.NameStatus == rpc.DownloadGroupNameStatusPending && candidate.NameStatus != "" && candidate.NameStatus != rpc.DownloadGroupNameStatusPending {
		return true
	}
	if candidate.NameStatus == rpc.DownloadGroupNameStatusStable && current.NameStatus != rpc.DownloadGroupNameStatusStable {
		return true
	}
	if candidate.ItemCount > current.ItemCount {
		return true
	}
	if strings.TrimSpace(current.Name) == "" && strings.TrimSpace(candidate.Name) != "" {
		return true
	}
	if strings.TrimSpace(current.FolderName) == "" && strings.TrimSpace(candidate.FolderName) != "" {
		return true
	}
	if strings.TrimSpace(current.Dir) == "" && strings.TrimSpace(candidate.Dir) != "" {
		return true
	}
	return current.CreatedAt == 0 && candidate.CreatedAt != 0
}

func (b *downloadGroupBucket) addMember(gid string, member downloadGroupMember) {
	if strings.TrimSpace(gid) == "" {
		return
	}
	if existing, ok := b.members[gid]; ok && existing.priority >= member.priority {
		return
	}
	b.members[gid] = member
	if member.task.DownloadGroup != nil {
		b.recordGroup(*member.task.DownloadGroup)
	}
}

func (b *downloadGroupBucket) finalizeStaleStoreMembers() {
	for gid := range b.storeGroups {
		if _, ok := b.members[gid]; !ok {
			b.staleStoreGID[gid] = struct{}{}
		}
	}
}

func (b *downloadGroupBucket) shouldIncludeCard() bool {
	if !b.hasGroup || strings.TrimSpace(b.groupKey) == "" {
		return false
	}
	return maxInt(b.itemCount, len(b.members), len(b.staleStoreGID)) >= 2
}

func (b *downloadGroupBucket) buildCard() DownloadGroupCard {
	counts, total, completed, speed, missingMetadata, statusBuckets, updatedAt := b.memberStats()
	counts.Expected = maxInt(b.itemCount, counts.Resolved, len(b.staleStoreGID))
	counts.Missing = counts.Expected - counts.Resolved
	if counts.Missing < 0 {
		counts.Missing = 0
	}

	group := b.group
	if group.ItemCount < b.itemCount {
		group.ItemCount = b.itemCount
	}
	fallbackName := downloadGroupFallbackName(group, b.groupKey)
	displayName, nameStatus, namePending, nameDegraded := downloadGroupDisplayNameForCard(group, fallbackName)
	if fallbackName == "" {
		fallbackName = displayName
	}

	warnings := buildDownloadGroupWarnings(counts, missingMetadata, len(b.staleStoreGID), len(statusBuckets), namePending, nameDegraded)
	createdAt := group.CreatedAt
	if updatedAt < createdAt {
		updatedAt = createdAt
	}

	folderLabel := downloadGroupFolderLabel(group)
	folderPathHint := ""
	if isSafeDownloadGroupFolderPathHint(group.Dir) {
		folderPathHint = strings.TrimSpace(group.Dir)
	}

	return DownloadGroupCard{
		GroupKey:        b.groupKey,
		DownloadGroup:   copyDownloadGroup(&group),
		Kind:            downloadGroupKindOrDefault(group.Kind),
		DisplayName:     displayName,
		FallbackName:    fallbackName,
		NameStatus:      nameStatus,
		Status:          downloadGroupAggregateStatus(counts),
		Degraded:        hasDegradedDownloadGroupWarning(warnings),
		Warnings:        warnings,
		Counts:          counts,
		TotalLength:     strconv.FormatUint(total, 10),
		CompletedLength: strconv.FormatUint(completed, 10),
		DownloadSpeed:   strconv.FormatUint(speed, 10),
		Progress:        downloadGroupProgress(completed, total),
		CreatedAt:       createdAt,
		UpdatedAt:       updatedAt,
		FolderLabel:     folderLabel,
		FolderPathHint:  folderPathHint,
		HasFolder:       strings.TrimSpace(group.FolderName) != "" || strings.TrimSpace(group.Dir) != "",
	}
}

func (b *downloadGroupBucket) memberStats() (DownloadGroupMemberCounts, uint64, uint64, uint64, int, map[string]struct{}, int64) {
	counts := DownloadGroupMemberCounts{Resolved: len(b.members)}
	var total uint64
	var completed uint64
	var speed uint64
	missingMetadata := 0
	statusBuckets := make(map[string]struct{})
	updatedAt := b.group.CreatedAt

	gids := make([]string, 0, len(b.members))
	for gid := range b.members {
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	for _, gid := range gids {
		member := b.members[gid]
		task := member.task
		status := strings.TrimSpace(task.Status)
		switch {
		case member.source == "active" || status == downloadGroupStatusActive:
			counts.Active++
			statusBuckets[downloadGroupStatusActive] = struct{}{}
		case status == downloadGroupStatusPaused:
			counts.Paused++
			statusBuckets[downloadGroupStatusPaused] = struct{}{}
		case member.source == "waiting" || status == downloadGroupStatusWaiting:
			counts.Waiting++
			statusBuckets[downloadGroupStatusWaiting] = struct{}{}
		case status == downloadGroupStatusError:
			counts.Error++
			statusBuckets[downloadGroupStatusError] = struct{}{}
		case status == downloadGroupStatusComplete:
			counts.Complete++
			statusBuckets[downloadGroupStatusComplete] = struct{}{}
		}
		if member.historyOnly {
			counts.HistoryOnly++
			statusBuckets[downloadGroupWarningHistoryOnly] = struct{}{}
		}
		if !taskHasUsableDownloadGroupMetadata(task) {
			missingMetadata++
		}
		total += parseDownloadGroupByteString(task.TotalLength)
		completed += parseDownloadGroupByteString(task.CompletedLength)
		speed += parseDownloadGroupByteString(task.DownloadSpeed)
		if member.completedAt > updatedAt {
			updatedAt = member.completedAt
		}
	}

	return counts, total, completed, speed, missingMetadata, statusBuckets, updatedAt
}

func buildDownloadGroupWarnings(counts DownloadGroupMemberCounts, missingMetadata int, staleStoreMembers int, statusBucketCount int, namePending bool, nameDegraded bool) []DownloadGroupWarning {
	warnings := make([]DownloadGroupWarning, 0, 6)
	if statusBucketCount > 1 {
		warnings = append(warnings, DownloadGroupWarning{Code: downloadGroupWarningMixedStatus, Severity: "info", Count: statusBucketCount})
	}
	if counts.Error > 0 && counts.Error < counts.Resolved {
		warnings = append(warnings, DownloadGroupWarning{Code: downloadGroupWarningPartialError, Severity: "error", Count: counts.Error})
	}
	if counts.Missing > 0 {
		warnings = append(warnings, DownloadGroupWarning{Code: downloadGroupWarningMissingMembers, Severity: "warning", Count: counts.Missing})
	}
	if missingMetadata > 0 {
		warnings = append(warnings, DownloadGroupWarning{Code: downloadGroupWarningMissingMetadata, Severity: "warning", Count: missingMetadata})
	}
	if counts.Resolved > 0 && counts.HistoryOnly == counts.Resolved {
		warnings = append(warnings, DownloadGroupWarning{Code: downloadGroupWarningHistoryOnly, Severity: "info", Count: counts.HistoryOnly})
	}
	if staleStoreMembers > 0 {
		warnings = append(warnings, DownloadGroupWarning{Code: downloadGroupWarningStaleGroup, Severity: "warning", Count: staleStoreMembers})
	}
	if namePending {
		warnings = append(warnings, DownloadGroupWarning{Code: downloadGroupWarningNamePending, Severity: "info"})
	}
	if nameDegraded {
		warnings = append(warnings, DownloadGroupWarning{Code: downloadGroupWarningNameDegraded, Severity: "warning"})
	}
	return warnings
}

func downloadGroupDisplayNameForCard(group rpc.DownloadGroup, fallbackName string) (string, string, bool, bool) {
	if fallbackName == "" {
		fallbackName = downloadGroupLabel(downloadGroupKindGeneric)
	}
	safeStoredName, storedNameSafe := monitor.SanitizeDownloadGroupDisplayName(group.Name)
	status := strings.TrimSpace(group.NameStatus)
	switch status {
	case downloadGroupNameStatusStable:
		if storedNameSafe {
			return safeStoredName, downloadGroupNameStatusStable, false, false
		}
		return fallbackName, downloadGroupNameStatusDegraded, false, true
	case downloadGroupNameStatusPending:
		if storedNameSafe {
			return safeStoredName, downloadGroupNameStatusPending, true, false
		}
		return fallbackName, downloadGroupNameStatusPending, true, false
	case downloadGroupNameStatusFallback, "":
		if storedNameSafe {
			return safeStoredName, downloadGroupNameStatusFallback, false, false
		}
		return fallbackName, downloadGroupNameStatusFallback, false, false
	case downloadGroupNameStatusDegraded:
		if storedNameSafe {
			return safeStoredName, downloadGroupNameStatusDegraded, false, true
		}
		return fallbackName, downloadGroupNameStatusDegraded, false, true
	default:
		return fallbackName, downloadGroupNameStatusDegraded, false, true
	}
}

func downloadGroupAggregateStatus(counts DownloadGroupMemberCounts) string {
	switch {
	case counts.Resolved == 0:
		return downloadGroupStatusUnknown
	case counts.Active > 0:
		return downloadGroupStatusActive
	case counts.Paused > 0:
		return downloadGroupStatusPaused
	case counts.Waiting > 0:
		return downloadGroupStatusWaiting
	case counts.Error > 0:
		return downloadGroupStatusError
	case counts.Complete > 0:
		return downloadGroupStatusComplete
	default:
		return downloadGroupStatusUnknown
	}
}

func downloadGroupProgress(completed, total uint64) float64 {
	if total == 0 {
		return 0
	}
	progress := float64(completed) / float64(total)
	if progress < 0 {
		return 0
	}
	if progress > 1 {
		return 1
	}
	return progress
}

func downloadGroupFallbackName(group rpc.DownloadGroup, groupKey string) string {
	label := downloadGroupLabel(downloadGroupKindOrDefault(group.Kind))
	if group.CreatedAt > 0 {
		return label + " " + time.Unix(group.CreatedAt, 0).UTC().Format("2006-01-02 15-04-05")
	}
	suffix := opaqueDownloadGroupKeySuffix(groupKey)
	if suffix != "" {
		return label + " " + suffix
	}
	return label
}

func downloadGroupKindOrDefault(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return downloadGroupKindGeneric
	}
	return kind
}

func opaqueDownloadGroupKeySuffix(groupKey string) string {
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" {
		return ""
	}
	if len(groupKey) <= 8 {
		return groupKey
	}
	return groupKey[len(groupKey)-8:]
}

func downloadGroupFolderLabel(group rpc.DownloadGroup) string {
	if folderName := strings.TrimSpace(group.FolderName); folderName != "" {
		return folderName
	}
	if isSafeDownloadGroupFolderPathHint(group.Dir) {
		if base := downloadGroupPathBase(group.Dir); base != "" && !downloadGroupSecretLikeSegment(base) {
			return base
		}
	}
	return ""
}

func isSafeDownloadGroupFolderPathHint(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	lower := strings.ToLower(path)
	if strings.Contains(lower, "://") || strings.HasPrefix(lower, "http:") || strings.HasPrefix(lower, "https:") || strings.HasPrefix(lower, "ftp:") || strings.HasPrefix(lower, "sftp:") || strings.HasPrefix(lower, "magnet:") {
		return false
	}
	if strings.ContainsAny(path, "?#&") {
		return false
	}
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if downloadGroupSecretLikeSegment(segment) {
			return false
		}
	}
	return true
}

func downloadGroupSecretLikeSegment(segment string) bool {
	segment = strings.ToLower(strings.TrimSpace(segment))
	if segment == "" {
		return false
	}
	markers := []string{"token", "secret", "bearer", "cookie", "auth", "account", "password", "key"}
	for _, marker := range markers {
		if strings.Contains(segment, marker) {
			return true
		}
	}
	return false
}

func downloadGroupPathBase(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/\\")
	if path == "" {
		return ""
	}
	index := strings.LastIndexAny(path, "/\\")
	if index >= 0 {
		return strings.TrimSpace(path[index+1:])
	}
	return path
}

func taskHasUsableDownloadGroupMetadata(task rpc.Task) bool {
	if strings.TrimSpace(task.Dir) != "" {
		return true
	}
	for _, file := range task.Files {
		if strings.TrimSpace(file.Path) != "" {
			return true
		}
	}
	return false
}

func parseDownloadGroupByteString(value string) uint64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func filterDownloadGroupTasks(tasks []rpc.Task, bucket *downloadGroupBucket, source string) []rpc.Task {
	if bucket == nil || len(tasks) == 0 {
		return []rpc.Task{}
	}
	result := make([]rpc.Task, 0)
	for _, task := range tasks {
		member, ok := bucket.members[task.GID]
		if !ok || member.source != source {
			continue
		}
		result = append(result, cloneDownloadGroupTask(task))
	}
	return result
}

func downloadGroupNotFoundEnvelope(groupKey string, updatedAt int64) DownloadGroupDetailEnvelope {
	warning := DownloadGroupWarning{Code: downloadGroupWarningGroupNotFound, Severity: "warning"}
	card := DownloadGroupCard{
		GroupKey:   groupKey,
		NameStatus: downloadGroupNameStatusDegraded,
		Status:     downloadGroupStatusUnknown,
		Degraded:   true,
		Warnings:   []DownloadGroupWarning{warning},
	}
	return DownloadGroupDetailEnvelope{
		GroupKey:  groupKey,
		Found:     false,
		Group:     card,
		Tasks:     DownloadGroupTaskLists{Active: []rpc.Task{}, Waiting: []rpc.Task{}, Stopped: []rpc.Task{}},
		UpdatedAt: updatedAt,
		Degraded:  true,
		Warnings:  []DownloadGroupWarning{warning},
	}
}

func hasDegradedDownloadGroupWarning(warnings []DownloadGroupWarning) bool {
	for _, warning := range warnings {
		switch warning.Code {
		case downloadGroupWarningMissingMembers, downloadGroupWarningMissingMetadata, downloadGroupWarningStaleGroup, downloadGroupWarningNameDegraded, downloadGroupWarningGroupNotFound:
			return true
		}
	}
	return false
}

func cloneDownloadGroupWarnings(warnings []DownloadGroupWarning) []DownloadGroupWarning {
	if len(warnings) == 0 {
		return nil
	}
	cloned := make([]DownloadGroupWarning, len(warnings))
	copy(cloned, warnings)
	return cloned
}

func cloneDownloadGroupTasks(tasks []rpc.Task) []rpc.Task {
	if len(tasks) == 0 {
		return []rpc.Task{}
	}
	cloned := make([]rpc.Task, len(tasks))
	for i := range tasks {
		cloned[i] = cloneDownloadGroupTask(tasks[i])
	}
	return cloned
}

func cloneDownloadGroupTask(task rpc.Task) rpc.Task {
	cloned := task
	cloned.DownloadGroup = copyDownloadGroup(task.DownloadGroup)
	if len(task.Files) > 0 {
		cloned.Files = make([]rpc.File, len(task.Files))
		for i, file := range task.Files {
			cloned.Files[i] = file
			if len(file.Uris) > 0 {
				cloned.Files[i].Uris = make([]rpc.Uri, len(file.Uris))
				copy(cloned.Files[i].Uris, file.Uris)
			}
		}
	}
	return cloned
}

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
