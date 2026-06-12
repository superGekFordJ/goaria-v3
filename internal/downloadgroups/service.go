package downloadgroups

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

// --- Folder Planning and Folder Creation (from app_tasks_groups.go) ---

const (
	DownloadGroupKindCollection = "collection"
	DownloadGroupKindBatch      = "batch"
	DownloadGroupKindGeneric    = "download_group"

	DownloadGroupFolderMaxRunes = 100
)

type DownloadGroupPlan struct {
	mu        sync.Mutex
	group     rpc.DownloadGroup
	baseDir   string
	created   bool
	succeeded int
}

func NewDownloadGroupPlan(kind string, itemCount int, now time.Time) (*DownloadGroupPlan, error) {
	if itemCount <= 0 {
		return nil, errors.New("could not prepare download group folder")
	}
	if config.Current == nil {
		return nil, errors.New("could not prepare download group folder")
	}
	if kind != DownloadGroupKindCollection && kind != DownloadGroupKindBatch && kind != DownloadGroupKindGeneric {
		kind = DownloadGroupKindGeneric
	}

	baseDir, err := ResolveDownloadGroupBaseDir(config.Current.DownloadDir)
	if err != nil {
		return nil, err
	}

	suffix := OpaqueDownloadGroupSuffix()
	timestamp := now.Format("2006-01-02 15-04-05")
	folderName, err := SafeDownloadGroupFolderName(kind, timestamp, "dg-"+suffix)
	if err != nil {
		return nil, err
	}
	dir, err := ResolveDownloadGroupDir(baseDir, folderName)
	if err != nil {
		return nil, err
	}

	label := DownloadGroupLabel(kind)
	return &DownloadGroupPlan{
		baseDir: baseDir,
		group: rpc.DownloadGroup{
			ID:         fmt.Sprintf("dg-%d-%s", now.Unix(), suffix),
			Kind:       kind,
			Name:       fmt.Sprintf("%s %s", label, timestamp),
			NameStatus: rpc.DownloadGroupNameStatusFallback,
			FolderName: folderName,
			Dir:        dir,
			ItemCount:  itemCount,
			CreatedAt:  now.Unix(),
		},
	}, nil
}

func SafeDownloadGroupFolderName(kind, timestamp, suffix string) (string, error) {
	label := DownloadGroupLabel(kind)
	name := strings.TrimSpace(strings.Join([]string{label, timestamp, suffix}, " "))
	name = SanitizeDownloadGroupFolderName(name)
	if name == "" {
		return "", errors.New("could not prepare download group folder")
	}
	return name, nil
}

func ResolveDownloadGroupBaseDir(baseDir string) (string, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return "", errors.New("could not prepare download group folder")
	}
	cleanBase := filepath.Clean(filepath.FromSlash(baseDir))
	absBase, err := filepath.Abs(cleanBase)
	if err != nil || strings.TrimSpace(absBase) == "" {
		return "", errors.New("could not prepare download group folder")
	}
	return absBase, nil
}

func ResolveDownloadGroupDir(baseDir, folderName string) (string, error) {
	absBase, err := ResolveDownloadGroupBaseDir(baseDir)
	if err != nil {
		return "", err
	}
	folderName = SanitizeDownloadGroupFolderName(folderName)
	if folderName == "" || filepath.IsAbs(folderName) {
		return "", errors.New("could not prepare download group folder")
	}
	absGroup, err := filepath.Abs(filepath.Join(absBase, folderName))
	if err != nil {
		return "", errors.New("could not prepare download group folder")
	}
	if !DownloadGroupPathContained(absBase, absGroup) {
		return "", errors.New("could not prepare download group folder")
	}
	return absGroup, nil
}

func EnsureDownloadGroupDir(baseDir string, group *rpc.DownloadGroup) error {
	if group == nil {
		return nil
	}
	baseDir, err := ResolveDownloadGroupBaseDir(baseDir)
	if err != nil {
		return err
	}
	baseFolderName := SanitizeDownloadGroupFolderName(group.FolderName)
	if baseFolderName == "" {
		return errors.New("could not prepare download group folder")
	}

	for i := 1; i <= 99; i++ {
		folderName := baseFolderName
		if i > 1 {
			suffix := fmt.Sprintf("-%02d", i)
			baseRunes := []rune(baseFolderName)
			maxBaseRunes := DownloadGroupFolderMaxRunes - len([]rune(suffix))
			if maxBaseRunes < 1 {
				return errors.New("could not prepare download group folder")
			}
			if len(baseRunes) > maxBaseRunes {
				folderName = strings.TrimRight(string(baseRunes[:maxBaseRunes]), ". ")
			} else {
				folderName = baseFolderName
			}
			folderName += suffix
		}
		dir, err := ResolveDownloadGroupDir(baseDir, folderName)
		if err != nil {
			return err
		}
		if _, statErr := os.Stat(dir); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return errors.New("could not prepare download group folder")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return errors.New("could not prepare download group folder")
		}
		group.FolderName = folderName
		group.Dir = dir
		return nil
	}

	return errors.New("could not prepare download group folder")
}

func (p *DownloadGroupPlan) EnsureDir() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.created {
		return nil
	}
	if err := EnsureDownloadGroupDir(p.baseDir, &p.group); err != nil {
		return err
	}
	p.created = true
	return nil
}

func (p *DownloadGroupPlan) RecordSuccess() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.succeeded++
}

func (p *DownloadGroupPlan) CleanupIfUnused() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.created || p.succeeded > 0 || p.group.Dir == "" {
		return
	}
	_ = os.Remove(p.group.Dir)
	p.created = false
}

func (p *DownloadGroupPlan) GroupCopy() rpc.DownloadGroup {
	if p == nil {
		return rpc.DownloadGroup{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.group
}

func CopyDownloadGroup(group *rpc.DownloadGroup) *rpc.DownloadGroup {
	if group == nil {
		return nil
	}
	copy := *group
	return &copy
}

func DownloadGroupLabel(kind string) string {
	switch kind {
	case DownloadGroupKindCollection:
		return "Collection"
	case DownloadGroupKindBatch:
		return "Batch"
	default:
		return "Download group"
	}
}

func SanitizeDownloadGroupFolderName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) || strings.ContainsRune("<>:\"/\\|?*", r) {
			continue
		}
		builder.WriteRune(r)
	}
	cleaned := strings.TrimRight(strings.TrimSpace(builder.String()), ". ")
	if cleaned == "" {
		return ""
	}
	runes := []rune(cleaned)
	if len(runes) > DownloadGroupFolderMaxRunes {
		cleaned = strings.TrimRight(string(runes[:DownloadGroupFolderMaxRunes]), ". ")
	}
	return strings.TrimSpace(cleaned)
}

func DownloadGroupPathContained(absBase, absGroup string) bool {
	rel, err := filepath.Rel(absBase, absGroup)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func OpaqueDownloadGroupSuffix() string {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(buf)
}

// --- Read operations (from app_tasks_group_read.go) ---

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
		DownloadGroup:   CopyDownloadGroup(&group),
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
		case member.source == "active" || status == DownloadGroupStatusActive:
			counts.Active++
			statusBuckets[DownloadGroupStatusActive] = struct{}{}
		case status == DownloadGroupStatusPaused:
			counts.Paused++
			statusBuckets[DownloadGroupStatusPaused] = struct{}{}
		case member.source == "waiting" || status == DownloadGroupStatusWaiting:
			counts.Waiting++
			statusBuckets[DownloadGroupStatusWaiting] = struct{}{}
		case status == DownloadGroupStatusError:
			counts.Error++
			statusBuckets[DownloadGroupStatusError] = struct{}{}
		case status == DownloadGroupStatusComplete:
			counts.Complete++
			statusBuckets[DownloadGroupStatusComplete] = struct{}{}
		}
		if member.historyOnly {
			counts.HistoryOnly++
			statusBuckets[DownloadGroupWarningHistoryOnly] = struct{}{}
		}
		if !taskHasUsableDownloadGroupMetadata(task) {
			missingMetadata++
		}
		total += parseDownloadGroupByteString(task.TotalLength)
		completed += parseDownloadGroupByteString(task.CompletedLength)
		if status == DownloadGroupStatusActive {
			speed += parseDownloadGroupByteString(task.DownloadSpeed)
		}
		if member.completedAt > updatedAt {
			updatedAt = member.completedAt
		}
	}

	return counts, total, completed, speed, missingMetadata, statusBuckets, updatedAt
}

func buildDownloadGroupWarnings(counts DownloadGroupMemberCounts, missingMetadata int, staleStoreMembers int, statusBucketCount int, namePending bool, nameDegraded bool) []DownloadGroupWarning {
	warnings := make([]DownloadGroupWarning, 0, 6)
	if statusBucketCount > 1 {
		warnings = append(warnings, DownloadGroupWarning{Code: DownloadGroupWarningMixedStatus, Severity: "info", Count: statusBucketCount})
	}
	if counts.Error > 0 && counts.Error < counts.Resolved {
		warnings = append(warnings, DownloadGroupWarning{Code: DownloadGroupWarningPartialError, Severity: "error", Count: counts.Error})
	}
	if counts.Missing > 0 {
		warnings = append(warnings, DownloadGroupWarning{Code: DownloadGroupWarningMissingMembers, Severity: "warning", Count: counts.Missing})
	}
	if missingMetadata > 0 {
		warnings = append(warnings, DownloadGroupWarning{Code: DownloadGroupWarningMissingMetadata, Severity: "warning", Count: missingMetadata})
	}
	if counts.Resolved > 0 && counts.HistoryOnly == counts.Resolved {
		warnings = append(warnings, DownloadGroupWarning{Code: DownloadGroupWarningHistoryOnly, Severity: "info", Count: counts.HistoryOnly})
	}
	if staleStoreMembers > 0 {
		warnings = append(warnings, DownloadGroupWarning{Code: DownloadGroupWarningStaleGroup, Severity: "warning", Count: staleStoreMembers})
	}
	if namePending {
		warnings = append(warnings, DownloadGroupWarning{Code: DownloadGroupWarningNamePending, Severity: "info"})
	}
	if nameDegraded {
		warnings = append(warnings, DownloadGroupWarning{Code: DownloadGroupWarningNameDegraded, Severity: "warning"})
	}
	return warnings
}

func downloadGroupDisplayNameForCard(group rpc.DownloadGroup, fallbackName string) (string, string, bool, bool) {
	if fallbackName == "" {
		fallbackName = DownloadGroupLabel(DownloadGroupKindGeneric)
	}
	safeStoredName, storedNameSafe := monitor.SanitizeDownloadGroupDisplayName(group.Name)
	status := strings.TrimSpace(group.NameStatus)
	switch status {
	case DownloadGroupNameStatusStable:
		if storedNameSafe {
			return safeStoredName, DownloadGroupNameStatusStable, false, false
		}
		return fallbackName, DownloadGroupNameStatusDegraded, false, true
	case DownloadGroupNameStatusPending:
		if storedNameSafe {
			return safeStoredName, DownloadGroupNameStatusPending, true, false
		}
		return fallbackName, DownloadGroupNameStatusPending, true, false
	case DownloadGroupNameStatusFallback, "":
		if storedNameSafe {
			return safeStoredName, DownloadGroupNameStatusFallback, false, false
		}
		return fallbackName, DownloadGroupNameStatusFallback, false, false
	case DownloadGroupNameStatusDegraded:
		if storedNameSafe {
			return safeStoredName, DownloadGroupNameStatusDegraded, false, true
		}
		return fallbackName, DownloadGroupNameStatusDegraded, false, true
	default:
		return fallbackName, DownloadGroupNameStatusDegraded, false, true
	}
}

func downloadGroupAggregateStatus(counts DownloadGroupMemberCounts) string {
	switch {
	case counts.Resolved == 0:
		return DownloadGroupStatusUnknown
	case counts.Active > 0:
		return DownloadGroupStatusActive
	case counts.Paused > 0:
		return DownloadGroupStatusPaused
	case counts.Waiting > 0:
		return DownloadGroupStatusWaiting
	case counts.Error > 0:
		return DownloadGroupStatusError
	case counts.Complete > 0:
		return DownloadGroupStatusComplete
	default:
		return DownloadGroupStatusUnknown
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
	label := DownloadGroupLabel(downloadGroupKindOrDefault(group.Kind))
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
		return DownloadGroupKindGeneric
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
	warning := DownloadGroupWarning{Code: DownloadGroupWarningGroupNotFound, Severity: "warning"}
	card := DownloadGroupCard{
		GroupKey:   groupKey,
		NameStatus: DownloadGroupNameStatusDegraded,
		Status:     DownloadGroupStatusUnknown,
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
		case DownloadGroupWarningMissingMembers, DownloadGroupWarningMissingMetadata, DownloadGroupWarningStaleGroup, DownloadGroupWarningNameDegraded, DownloadGroupWarningGroupNotFound:
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
	cloned.DownloadGroup = CopyDownloadGroup(task.DownloadGroup)
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
		task.DownloadGroup = CopyDownloadGroup(entry.DownloadGroup)
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
		DownloadGroup:   CopyDownloadGroup(entry.DownloadGroup),
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

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

// --- Write operations (from app_tasks_group_ops.go) ---

var OpenFolderLauncher func(dir string) error = func(dir string) error {
	return errors.New("open folder launcher not initialized")
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

	// Remove tasks via the provided main delegate
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

	if resolution.card.DownloadGroup != nil && resolution.card.DownloadGroup.Dir != "" {
		dir := filepath.Clean(resolution.card.DownloadGroup.Dir)
		if config.Current != nil && isSafeDownloadGroupFolderPathHint(dir) {
			absBase, err1 := filepath.Abs(config.Current.DownloadDir)
			absGroup, err2 := filepath.Abs(dir)
			if err1 == nil && err2 == nil && DownloadGroupPathContained(absBase, absGroup) {
				if deleteFiles {
					_ = os.RemoveAll(dir)
				} else if isDirEmpty(dir) {
					_ = os.Remove(dir)
				}
			}
		}
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
		multiByGID[item.GID] = item
	}
	successCode := DownloadGroupOperationCodePaused
	if action == DownloadGroupOperationActionResume {
		successCode = DownloadGroupOperationCodeResumed
	}
	for _, target := range actionable {
		item, ok := multiByGID[target.gid]
		if ok && item.OK {
			result.addItem(DownloadGroupOperationItemResult{GID: target.gid, Status: DownloadGroupOperationItemSucceeded, Code: successCode})
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
		return DownloadGroupOperationCodeStaleMember, true
	}
	if target.historyOnly {
		return DownloadGroupOperationCodeHistoryOnly, true
	}
	if target.source == "stopped" {
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
	if target.source == "active" {
		return DownloadGroupOperationCodeAlreadyActive, true
	}
	if target.source == "waiting" && target.status == DownloadGroupStatusPaused {
		return "", false
	}
	if target.source == "waiting" {
		return DownloadGroupOperationCodeNotPaused, true
	}
	return DownloadGroupOperationCodeTerminalState, true
}

func callDownloadGroupPauseResumeRPC(action string, gids []string) ([]rpc.MultiCallItemResult, error) {
	if action == DownloadGroupOperationActionResume {
		return rpc.UnpauseMultiResults(gids)
	}
	return rpc.PauseMultiResults(gids)
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
