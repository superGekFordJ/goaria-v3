package monitor

import (
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

const (
	downloadGroupNameDebounceDefault = 250 * time.Millisecond
	downloadGroupNameRetryDefault    = time.Second
	downloadGroupNameMaxRetries      = 3
	downloadGroupStemRuneLimit       = 80
	downloadGroupDisplayRuneLimit    = 72
)

var defaultDownloadGroupNamer = newDownloadGroupNamer()

type DownloadGroupNameJobResult struct {
	GroupKey string
	Name     string
	Status   string
	Attempts int
	Requeued bool
	Applied  int
}

type downloadGroupNamer struct {
	mu       sync.Mutex
	timers   map[string]*time.Timer
	attempts map[string]int
	wg       sync.WaitGroup

	debounceDelay time.Duration
	retryDelay    time.Duration
	maxRetries    int
}

type downloadGroupNameSnapshot struct {
	groupKey        string
	group           rpc.DownloadGroup
	foundGroup      bool
	memberGIDs      map[string]struct{}
	stems           []downloadGroupStem
	pendingMetadata bool
}

type downloadGroupStem struct {
	GID   string
	Stem  string
	Lower string
}

func newDownloadGroupNamer() *downloadGroupNamer {
	return &downloadGroupNamer{
		timers:        make(map[string]*time.Timer),
		attempts:      make(map[string]int),
		debounceDelay: downloadGroupNameDebounceDefault,
		retryDelay:    downloadGroupNameRetryDefault,
		maxRetries:    downloadGroupNameMaxRetries,
	}
}

func QueueDownloadGroupName(groupKey string) {
	defaultDownloadGroupNamer.mu.Lock()
	delay := defaultDownloadGroupNamer.debounceDelay
	defaultDownloadGroupNamer.mu.Unlock()
	defaultDownloadGroupNamer.queue(groupKey, delay)
}

func queueDownloadGroupNameRefresh(groupKey string) {
	QueueDownloadGroupName(groupKey)
}

func RunDownloadGroupNameJobForTest(groupKey string) DownloadGroupNameJobResult {
	return defaultDownloadGroupNamer.run(groupKey, false)
}

func ConfigureDownloadGroupNamerForTest(debounceDelay, retryDelay time.Duration, maxRetries int) func() {
	defaultDownloadGroupNamer.mu.Lock()
	oldDebounce := defaultDownloadGroupNamer.debounceDelay
	oldRetry := defaultDownloadGroupNamer.retryDelay
	oldMaxRetries := defaultDownloadGroupNamer.maxRetries
	if debounceDelay >= 0 {
		defaultDownloadGroupNamer.debounceDelay = debounceDelay
	}
	if retryDelay >= 0 {
		defaultDownloadGroupNamer.retryDelay = retryDelay
	}
	if maxRetries >= 0 {
		defaultDownloadGroupNamer.maxRetries = maxRetries
	}
	defaultDownloadGroupNamer.mu.Unlock()

	return func() {
		defaultDownloadGroupNamer.mu.Lock()
		defaultDownloadGroupNamer.debounceDelay = oldDebounce
		defaultDownloadGroupNamer.retryDelay = oldRetry
		defaultDownloadGroupNamer.maxRetries = oldMaxRetries
		defaultDownloadGroupNamer.mu.Unlock()
	}
}

func ResetDownloadGroupNamerForTest() {
	defaultDownloadGroupNamer.mu.Lock()
	for key, timer := range defaultDownloadGroupNamer.timers {
		if timer != nil {
			if timer.Stop() {
				defaultDownloadGroupNamer.wg.Done()
			}
		}
		delete(defaultDownloadGroupNamer.timers, key)
	}
	defaultDownloadGroupNamer.attempts = make(map[string]int)
	defaultDownloadGroupNamer.mu.Unlock()

	defaultDownloadGroupNamer.wg.Wait()
}

func PendingDownloadGroupNameJobCountForTest() int {
	defaultDownloadGroupNamer.mu.Lock()
	defer defaultDownloadGroupNamer.mu.Unlock()
	return len(defaultDownloadGroupNamer.timers)
}

func (n *downloadGroupNamer) queue(groupKey string, delay time.Duration) {
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" {
		return
	}
	if delay < 0 {
		delay = 0
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if timer := n.timers[groupKey]; timer != nil {
		if !timer.Reset(delay) {
			n.wg.Add(1)
		}
		return
	}
	n.wg.Add(1)
	n.timers[groupKey] = time.AfterFunc(delay, func() {
		defer n.wg.Done()
		n.mu.Lock()
		delete(n.timers, groupKey)
		n.mu.Unlock()
		n.markPending(groupKey)
		n.run(groupKey, true)
	})
}

func (n *downloadGroupNamer) markPending(groupKey string) {
	snapshot := collectDownloadGroupNameSnapshot(groupKey)
	if !snapshot.foundGroup {
		return
	}
	name := fallbackDownloadGroupName(snapshot.group, groupKey)
	if safeName, ok := SanitizeDownloadGroupDisplayName(snapshot.group.Name); ok {
		name = safeName
	}
	applyDownloadGroupNameResult(groupKey, name, rpc.DownloadGroupNameStatusPending)
}

func (n *downloadGroupNamer) run(groupKey string, allowRequeue bool) DownloadGroupNameJobResult {
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" {
		return DownloadGroupNameJobResult{}
	}

	snapshot := collectDownloadGroupNameSnapshot(groupKey)
	result := classifyDownloadGroupNameSnapshot(snapshot)
	result.GroupKey = groupKey

	if result.Status == rpc.DownloadGroupNameStatusPending {
		attempts := n.incrementAttempt(groupKey)
		result.Attempts = attempts
		if attempts <= n.maxRetries {
			result.Applied = applyDownloadGroupNameResult(groupKey, result.Name, result.Status)
			if allowRequeue {
				result.Requeued = true
				n.queue(groupKey, n.retryDelay)
			}
			return result
		}
		result.Name = degradedDownloadGroupName(snapshot)
		result.Status = rpc.DownloadGroupNameStatusDegraded
	}

	n.clearAttempt(groupKey)
	if result.Name != "" && rpc.IsDownloadGroupNameStatus(result.Status) {
		result.Applied = applyDownloadGroupNameResult(groupKey, result.Name, result.Status)
	}
	return result
}

func (n *downloadGroupNamer) incrementAttempt(groupKey string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.attempts[groupKey]++
	return n.attempts[groupKey]
}

func (n *downloadGroupNamer) clearAttempt(groupKey string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.attempts, groupKey)
}

func collectDownloadGroupNameSnapshot(groupKey string) downloadGroupNameSnapshot {
	groupKey = strings.TrimSpace(groupKey)
	snapshot := downloadGroupNameSnapshot{
		groupKey:   groupKey,
		memberGIDs: make(map[string]struct{}),
	}
	if groupKey == "" {
		return snapshot
	}

	tasks, metadata := downloadGroupNamerCacheSnapshot()
	stored := ListStoredTaskGroups()
	historyEntries := history.GetAll()

	tasksByGID := make(map[string]rpc.Task, len(tasks))
	for _, task := range tasks {
		if task.GID == "" {
			continue
		}
		tasksByGID[task.GID] = task
		if task.DownloadGroup != nil && task.DownloadGroup.ID == groupKey {
			snapshot.recordGroup(*task.DownloadGroup)
			snapshot.memberGIDs[task.GID] = struct{}{}
		}
	}
	for gid, meta := range metadata {
		if meta.DownloadGroup != nil && meta.DownloadGroup.ID == groupKey {
			snapshot.recordGroup(*meta.DownloadGroup)
			snapshot.memberGIDs[gid] = struct{}{}
		}
	}
	for gid, group := range stored {
		if group.ID == groupKey {
			snapshot.recordGroup(group)
			snapshot.memberGIDs[gid] = struct{}{}
		}
	}
	historyByGID := make(map[string]history.HistoryEntry, len(historyEntries))
	for _, entry := range historyEntries {
		if entry.GID == "" {
			continue
		}
		historyByGID[entry.GID] = entry
		if entry.DownloadGroup != nil && entry.DownloadGroup.ID == groupKey {
			snapshot.recordGroup(*entry.DownloadGroup)
			snapshot.memberGIDs[entry.GID] = struct{}{}
		}
	}

	gids := make([]string, 0, len(snapshot.memberGIDs))
	for gid := range snapshot.memberGIDs {
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	for _, gid := range gids {
		stem, inspected := bestDownloadGroupMemberStem(gid, tasksByGID[gid], metadata[gid], historyByGID[gid])
		if stem != "" {
			snapshot.stems = append(snapshot.stems, downloadGroupStem{GID: gid, Stem: stem, Lower: strings.ToLower(stem)})
		}
		if !inspected {
			snapshot.pendingMetadata = true
		}
	}

	sort.SliceStable(snapshot.stems, func(i, j int) bool {
		if snapshot.stems[i].Lower != snapshot.stems[j].Lower {
			return snapshot.stems[i].Lower < snapshot.stems[j].Lower
		}
		if snapshot.stems[i].Stem != snapshot.stems[j].Stem {
			return snapshot.stems[i].Stem < snapshot.stems[j].Stem
		}
		return snapshot.stems[i].GID < snapshot.stems[j].GID
	})

	return snapshot
}

func (s *downloadGroupNameSnapshot) recordGroup(group rpc.DownloadGroup) {
	if strings.TrimSpace(group.ID) == "" {
		return
	}
	if !s.foundGroup || betterNamerGroup(group, s.group) {
		s.group = group
		s.foundGroup = true
	}
}

func betterNamerGroup(candidate, current rpc.DownloadGroup) bool {
	if candidate.NameStatus == rpc.DownloadGroupNameStatusStable && current.NameStatus != rpc.DownloadGroupNameStatusStable {
		return true
	}
	if candidate.NameStatus == rpc.DownloadGroupNameStatusPending && current.NameStatus == "" {
		return true
	}
	if candidate.ItemCount > current.ItemCount {
		return true
	}
	if strings.TrimSpace(current.Name) == "" && strings.TrimSpace(candidate.Name) != "" {
		return true
	}
	return current.CreatedAt == 0 && candidate.CreatedAt != 0
}

func downloadGroupNamerCacheSnapshot() ([]rpc.Task, map[string]TaskMetadata) {
	if Cache == nil {
		return nil, nil
	}
	active := Cache.GetActive()
	waiting := Cache.GetWaiting()
	stopped := Cache.GetStopped()
	tasks := make([]rpc.Task, 0, len(active)+len(waiting)+len(stopped))
	for _, task := range active {
		tasks = append(tasks, copyDownloadGroupTaskForNamer(task))
	}
	for _, task := range waiting {
		tasks = append(tasks, copyDownloadGroupTaskForNamer(task))
	}
	for _, task := range stopped {
		tasks = append(tasks, copyDownloadGroupTaskForNamer(task))
	}

	Cache.mu.RLock()
	metadata := make(map[string]TaskMetadata, len(Cache.metadata))
	for gid, meta := range Cache.metadata {
		if meta == nil {
			continue
		}
		copied := *meta
		if len(meta.Files) > 0 {
			copied.Files = append([]string(nil), meta.Files...)
		}
		copied.DownloadGroup = copyDownloadGroup(meta.DownloadGroup)
		metadata[gid] = copied
	}
	Cache.mu.RUnlock()
	return tasks, metadata
}

func copyDownloadGroupTaskForNamer(task rpc.Task) rpc.Task {
	copied := task
	copied.DownloadGroup = copyDownloadGroup(task.DownloadGroup)
	if len(task.Files) > 0 {
		copied.Files = make([]rpc.File, len(task.Files))
		for i, file := range task.Files {
			copied.Files[i] = file
			if len(file.Uris) > 0 {
				copied.Files[i].Uris = append([]rpc.Uri(nil), file.Uris...)
			}
		}
	}
	return copied
}

func bestDownloadGroupMemberStem(gid string, task rpc.Task, meta TaskMetadata, entry history.HistoryEntry) (string, bool) {
	if stem, inspected := bestStemFromTaskFiles(task.Files); stem != "" || inspected {
		return stem, true
	}
	if stem, inspected := bestStemFromMetadataFiles(meta.Files); stem != "" || inspected {
		return stem, true
	}
	if strings.TrimSpace(entry.Path) != "" {
		if stem, ok := sanitizeDownloadGroupBasenameStem(entry.Path); ok {
			return stem, true
		}
		return "", true
	}
	if basenameLikeDownloadGroupTitle(task.Title) {
		if stem, ok := sanitizeDownloadGroupBasenameStem(task.Title); ok {
			return stem, true
		}
		return "", true
	}
	return "", strings.TrimSpace(gid) == ""
}

func bestStemFromTaskFiles(files []rpc.File) (string, bool) {
	inspected := false
	for _, file := range files {
		if strings.TrimSpace(file.Path) == "" {
			continue
		}
		inspected = true
		if stem, ok := sanitizeDownloadGroupBasenameStem(file.Path); ok {
			return stem, true
		}
	}
	return "", inspected
}

func bestStemFromMetadataFiles(files []string) (string, bool) {
	inspected := false
	for _, file := range files {
		if strings.TrimSpace(file) == "" {
			continue
		}
		inspected = true
		if stem, ok := sanitizeDownloadGroupBasenameStem(file); ok {
			return stem, true
		}
	}
	return "", inspected
}

func basenameLikeDownloadGroupTitle(title string) bool {
	title = strings.TrimSpace(title)
	if title == "" || strings.ContainsAny(title, `/\?#&`) || hasDownloadGroupURLMarker(title) {
		return false
	}
	return true
}

func classifyDownloadGroupNameSnapshot(snapshot downloadGroupNameSnapshot) DownloadGroupNameJobResult {
	if !snapshot.foundGroup {
		name := fallbackDownloadGroupName(rpc.DownloadGroup{}, snapshot.groupKey)
		return DownloadGroupNameJobResult{Name: name, Status: rpc.DownloadGroupNameStatusDegraded}
	}

	if len(snapshot.stems) < 2 {
		if snapshot.pendingMetadata {
			return DownloadGroupNameJobResult{Name: degradedDownloadGroupName(snapshot), Status: rpc.DownloadGroupNameStatusPending}
		}
		return DownloadGroupNameJobResult{Name: fallbackDownloadGroupName(snapshot.group, snapshot.groupKey), Status: rpc.DownloadGroupNameStatusFallback}
	}

	if name, ok := longestCommonPrefixDownloadGroupName(snapshot.stems); ok {
		return DownloadGroupNameJobResult{Name: name, Status: rpc.DownloadGroupNameStatusStable}
	}
	if name, ok := longestCommonSubstringDownloadGroupName(snapshot.stems); ok {
		return DownloadGroupNameJobResult{Name: name, Status: rpc.DownloadGroupNameStatusStable}
	}
	return DownloadGroupNameJobResult{Name: fallbackDownloadGroupName(snapshot.group, snapshot.groupKey), Status: rpc.DownloadGroupNameStatusFallback}
}

func longestCommonPrefixDownloadGroupName(stems []downloadGroupStem) (string, bool) {
	if len(stems) == 0 {
		return "", false
	}
	firstRunes := []rune(stems[0].Stem)
	commonLen := len(firstRunes)
	for _, stem := range stems[1:] {
		candidateRunes := []rune(stem.Lower)
		firstLowerRunes := []rune(strings.ToLower(string(firstRunes)))
		limit := commonLen
		if len(candidateRunes) < limit {
			limit = len(candidateRunes)
		}
		index := 0
		for index < limit && firstLowerRunes[index] == candidateRunes[index] {
			index++
		}
		commonLen = index
		if commonLen == 0 {
			break
		}
	}
	if commonLen <= 0 || commonLen > len(firstRunes) {
		return "", false
	}
	candidate := trimDownloadGroupNameCandidate(string(firstRunes[:commonLen]))
	if !strongDownloadGroupPrefix(candidate) {
		return "", false
	}
	return candidate, true
}

func longestCommonSubstringDownloadGroupName(stems []downloadGroupStem) (string, bool) {
	if len(stems) == 0 {
		return "", false
	}
	shortest := stems[0]
	for _, stem := range stems[1:] {
		if len([]rune(stem.Stem)) < len([]rune(shortest.Stem)) {
			shortest = stem
		}
	}
	base := []rune(shortest.Stem)
	baseLower := []rune(strings.ToLower(shortest.Stem))
	best := ""
	bestLower := ""
	bestStart := 0

	for start := 0; start < len(baseLower); start++ {
		for end := start + 1; end <= len(baseLower); end++ {
			candidateLower := string(baseLower[start:end])
			if !downloadGroupSubstringInAll(candidateLower, stems) {
				continue
			}
			candidate := trimDownloadGroupNameCandidate(string(base[start:end]))
			if !strongDownloadGroupSubstring(candidate) {
				continue
			}
			candidateLower = strings.ToLower(candidate)
			candidateLen := len([]rune(candidate))
			bestLen := len([]rune(best))
			if candidateLen > bestLen ||
				(candidateLen == bestLen && start < bestStart) ||
				(candidateLen == bestLen && start == bestStart && candidateLower < bestLower) {
				best = candidate
				bestLower = candidateLower
				bestStart = start
			}
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

func downloadGroupSubstringInAll(candidateLower string, stems []downloadGroupStem) bool {
	for _, stem := range stems {
		if !strings.Contains(stem.Lower, candidateLower) {
			return false
		}
	}
	return true
}

func strongDownloadGroupPrefix(candidate string) bool {
	if _, ok := SanitizeDownloadGroupDisplayName(candidate); !ok {
		return false
	}
	return (len([]rune(candidate)) >= 4 && hasDownloadGroupLetterOrNumber(candidate)) || downloadGroupReadableTokenCount(candidate) >= 2
}

func strongDownloadGroupSubstring(candidate string) bool {
	if _, ok := SanitizeDownloadGroupDisplayName(candidate); !ok {
		return false
	}
	return len([]rune(candidate)) >= 6 || downloadGroupReadableTokenCount(candidate) >= 2
}

func trimDownloadGroupNameCandidate(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	candidate = strings.Trim(candidate, "._-–—()[]{}<>:;,+*/|\\\"'`~!@#$%^&=")
	candidate = strings.Join(strings.Fields(candidate), " ")
	candidate = trimTrailingShortNumericToken(candidate)
	if safe, ok := SanitizeDownloadGroupDisplayName(candidate); ok {
		return safe
	}
	return ""
}

func trimTrailingShortNumericToken(candidate string) string {
	tokens := strings.Fields(candidate)
	if len(tokens) < 2 {
		return candidate
	}
	last := tokens[len(tokens)-1]
	if len([]rune(last)) > 3 {
		return candidate
	}
	for _, r := range last {
		if !unicode.IsDigit(r) {
			return candidate
		}
	}
	return strings.Join(tokens[:len(tokens)-1], " ")
}

func degradedDownloadGroupName(snapshot downloadGroupNameSnapshot) string {
	if safeName, ok := SanitizeDownloadGroupDisplayName(snapshot.group.Name); ok {
		return safeName
	}
	return fallbackDownloadGroupName(snapshot.group, snapshot.groupKey)
}

func fallbackDownloadGroupName(group rpc.DownloadGroup, groupKey string) string {
	label := downloadGroupKindLabel(group.Kind)
	if group.CreatedAt > 0 {
		return label + " " + time.Unix(group.CreatedAt, 0).UTC().Format("2006-01-02 15-04-05")
	}
	if suffix := opaqueDownloadGroupKeySuffixForNamer(groupKey); suffix != "" {
		return label + " " + suffix
	}
	return label
}

func downloadGroupKindLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case "collection":
		return "Collection"
	case "batch":
		return "Batch"
	default:
		return "Download group"
	}
}

func opaqueDownloadGroupKeySuffixForNamer(groupKey string) string {
	groupKey = strings.TrimSpace(groupKey)
	if groupKey == "" {
		return ""
	}
	runes := []rune(groupKey)
	if len(runes) <= 8 {
		return groupKey
	}
	return string(runes[len(runes)-8:])
}

func applyDownloadGroupNameResult(groupKey, name, status string) int {
	if strings.TrimSpace(groupKey) == "" || strings.TrimSpace(name) == "" || !rpc.IsDownloadGroupNameStatus(status) {
		return 0
	}
	changed := UpdateStoredTaskGroupName(groupKey, name, status)
	if Cache != nil {
		changed += Cache.UpdateTaskGroupName(groupKey, name, status)
	}
	if tracker := State.GetTracker(); tracker != nil {
		changed += tracker.UpdateTaskGroupName(groupKey, name, status)
	}
	changed += history.UpdateDownloadGroupName(groupKey, name, status)
	return changed
}

func sanitizeDownloadGroupBasenameStem(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || hasDownloadGroupURLMarker(raw) || strings.ContainsAny(raw, "?#&") || containsEncodedURLMarker(raw) {
		return "", false
	}
	base := stripDownloadGroupDirComponents(raw)
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == ".." || isPureDownloadGroupExtension(base) {
		return "", false
	}
	stem := dropDownloadGroupExtension(base)
	stem = strings.TrimSpace(stem)
	if stem == "" || stem == "." || stem == ".." {
		return "", false
	}
	if isUnsafeDownloadGroupRawIdentifier(stem) {
		return "", false
	}
	tokens := safeDownloadGroupStemTokens(stem)
	if len(tokens) == 0 {
		return "", false
	}
	candidate := strings.Join(tokens, " ")
	candidate = capDownloadGroupRunes(candidate, downloadGroupStemRuneLimit)
	if _, ok := SanitizeDownloadGroupDisplayName(candidate); !ok {
		return "", false
	}
	return candidate, true
}

func isUnsafeDownloadGroupRawIdentifier(stem string) bool {
	for _, field := range strings.Fields(stem) {
		field = strings.Trim(field, "._-–—()[]{}<>:;,+*/|\\\"'`~!@#$%^&=")
		if field == "" {
			continue
		}
		lower := strings.ToLower(field)
		if isDownloadGroupUUIDLike(lower) || isDownloadGroupOpaqueIdentifier(lower) {
			return true
		}
	}
	return false
}

func stripDownloadGroupDirComponents(path string) string {
	path = strings.TrimSpace(path)
	if index := strings.LastIndexAny(path, `/\`); index >= 0 {
		return path[index+1:]
	}
	return path
}

func isPureDownloadGroupExtension(base string) bool {
	if !strings.HasPrefix(base, ".") {
		return false
	}
	return strings.Count(base, ".") == 1
}

func dropDownloadGroupExtension(base string) string {
	runes := []rune(base)
	lastDot := -1
	for i, r := range runes {
		if r == '.' {
			lastDot = i
		}
	}
	if lastDot > 0 && lastDot < len(runes)-1 {
		stem := strings.TrimSpace(string(runes[:lastDot]))
		if stem != "" {
			return stem
		}
	}
	return base
}

func safeDownloadGroupStemTokens(stem string) []string {
	tokens := tokenizeDownloadGroupReadable(stem)
	if len(tokens) == 0 {
		return nil
	}
	if downloadGroupStemContainsSecretMarker(tokens) {
		return nil
	}
	filtered := make([]string, 0, len(tokens))
	skipNext := false
	for _, token := range tokens {
		lower := strings.ToLower(token)
		if skipNext {
			skipNext = false
			continue
		}
		if isDownloadGroupSecretMarker(lower) {
			skipNext = true
			continue
		}
		if isDownloadGroupOpaqueIdentifier(lower) {
			continue
		}
		filtered = append(filtered, token)
	}
	return filtered
}

func downloadGroupStemContainsSecretMarker(tokens []string) bool {
	for _, token := range tokens {
		if tokenContainsDownloadGroupSecretMarker(strings.ToLower(token)) {
			return true
		}
	}
	return false
}

func tokenizeDownloadGroupReadable(input string) []string {
	tokens := make([]string, 0)
	var builder strings.Builder
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		tokens = append(tokens, builder.String())
		builder.Reset()
	}
	for _, r := range input {
		switch {
		case isDownloadGroupInvisible(r) || r == '/' || r == '\\':
			flush()
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			builder.WriteRune(r)
		case unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r):
			flush()
		default:
			flush()
		}
	}
	flush()
	return tokens
}

func SanitizeDownloadGroupDisplayName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || hasDownloadGroupURLMarker(name) || strings.ContainsAny(name, "?#&") || containsEncodedURLMarker(name) {
		return "", false
	}
	var builder strings.Builder
	for _, r := range name {
		if isDownloadGroupInvisible(r) || r == '/' || r == '\\' {
			builder.WriteRune(' ')
			continue
		}
		builder.WriteRune(r)
	}
	candidate := strings.Join(strings.Fields(builder.String()), " ")
	candidate = strings.TrimRight(strings.TrimSpace(candidate), ". ")
	if candidate == "" || candidate == "." || candidate == ".." || !hasDownloadGroupLetterOrNumber(candidate) {
		return "", false
	}
	if isWindowsReservedDownloadGroupName(candidate) {
		return "", false
	}
	for _, token := range tokenizeDownloadGroupReadable(candidate) {
		lower := strings.ToLower(token)
		if tokenContainsDownloadGroupSecretMarker(lower) || isDownloadGroupOpaqueIdentifier(lower) {
			return "", false
		}
	}
	candidate = capDownloadGroupRunes(candidate, downloadGroupDisplayRuneLimit)
	candidate = strings.TrimRight(strings.TrimSpace(candidate), ". ")
	return candidate, candidate != "" && hasDownloadGroupLetterOrNumber(candidate)
}

func hasDownloadGroupURLMarker(input string) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	return strings.Contains(lower, "://") ||
		strings.HasPrefix(lower, "http:") ||
		strings.HasPrefix(lower, "https:") ||
		strings.HasPrefix(lower, "ftp:") ||
		strings.HasPrefix(lower, "magnet:") ||
		strings.HasPrefix(lower, "http ") ||
		strings.HasPrefix(lower, "https ") ||
		strings.HasPrefix(lower, "ftp ") ||
		strings.HasPrefix(lower, "magnet ")
}

func containsEncodedURLMarker(input string) bool {
	lower := strings.ToLower(input)
	return strings.Contains(lower, "%3f") || strings.Contains(lower, "%23")
}

func isDownloadGroupInvisible(r rune) bool {
	return r == 0 || unicode.IsControl(r) ||
		r == '\u00ad' || r == '\ufeff' ||
		(r >= '\u200b' && r <= '\u200f') ||
		(r >= '\u202a' && r <= '\u202e') ||
		(r >= '\u2060' && r <= '\u206f')
}

func isDownloadGroupSecretMarker(token string) bool {
	if token == "" {
		return false
	}
	exact := map[string]struct{}{
		"token": {}, "secret": {}, "bearer": {}, "cookie": {}, "auth": {}, "account": {},
		"password": {}, "passwd": {}, "apikey": {}, "api": {}, "accesskey": {}, "session": {},
		"signature": {}, "sig": {}, "key": {},
	}
	if _, ok := exact[token]; ok {
		return true
	}
	markers := []string{"token", "secret", "bearer", "cookie", "auth", "account", "password", "passwd", "apikey", "accesskey", "session", "signature"}
	for _, marker := range markers {
		if strings.Contains(token, marker) {
			return true
		}
	}
	return false
}

func tokenContainsDownloadGroupSecretMarker(token string) bool {
	if token == "" {
		return false
	}
	markers := []string{"token", "secret", "bearer", "cookie", "auth", "account", "password", "passwd", "apikey", "api_key", "accesskey", "session", "signature", "sig", "key"}
	for _, marker := range markers {
		if strings.Contains(token, marker) {
			return true
		}
	}
	return false
}

func isDownloadGroupOpaqueIdentifier(token string) bool {
	token = strings.Trim(token, "-_=+")
	if len([]rune(token)) < 16 {
		return false
	}
	if isDownloadGroupUUIDLike(token) || isDownloadGroupLongHex(token) {
		return true
	}
	if len([]rune(token)) >= 16 && hasASCIIAlpha(token) && hasASCIIDigit(token) && isDownloadGroupBase64Like(token) {
		return true
	}
	return false
}

func isDownloadGroupUUIDLike(token string) bool {
	parts := strings.Split(token, "-")
	if len(parts) != 5 {
		return false
	}
	wants := []int{8, 4, 4, 4, 12}
	for i, part := range parts {
		if len(part) != wants[i] || !isDownloadGroupHex(part) {
			return false
		}
	}
	return true
}

func isDownloadGroupLongHex(token string) bool {
	return len(token) >= 16 && isDownloadGroupHex(token)
}

func isDownloadGroupHex(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func isDownloadGroupBase64Like(token string) bool {
	for _, r := range token {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '_' && r != '-' && r != '+' && r != '/' && r != '=' {
			return false
		}
	}
	return true
}

func hasASCIIAlpha(token string) bool {
	for _, r := range token {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func hasASCIIDigit(token string) bool {
	for _, r := range token {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func hasDownloadGroupLetterOrNumber(input string) bool {
	for _, r := range input {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func downloadGroupReadableTokenCount(input string) int {
	count := 0
	for _, token := range tokenizeDownloadGroupReadable(input) {
		if hasDownloadGroupLetterOrNumber(token) {
			count++
		}
	}
	return count
}

func isWindowsReservedDownloadGroupName(name string) bool {
	tokens := tokenizeDownloadGroupReadable(name)
	if len(tokens) == 0 {
		return false
	}
	base := strings.ToUpper(tokens[0])
	reserved := map[string]struct{}{
		"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
		"COM1": {}, "COM2": {}, "COM3": {}, "COM4": {}, "COM5": {}, "COM6": {}, "COM7": {}, "COM8": {}, "COM9": {},
		"LPT1": {}, "LPT2": {}, "LPT3": {}, "LPT4": {}, "LPT5": {}, "LPT6": {}, "LPT7": {}, "LPT8": {}, "LPT9": {},
	}
	_, ok := reserved[base]
	return ok && len(tokens) == 1
}

func capDownloadGroupRunes(input string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(input)
	if len(runes) <= limit {
		return strings.TrimSpace(input)
	}
	return strings.TrimSpace(string(runes[:limit]))
}
