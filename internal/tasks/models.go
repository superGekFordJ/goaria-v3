package tasks

import (
	"sync"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/rpc"
)

type BatchAddResult struct {
	Succeeded  []string            `json:"succeeded"`
	Duplicates []string            `json:"duplicates"`
	Errors     map[string]string   `json:"errors"`
	Groups     []rpc.DownloadGroup `json:"groups,omitempty"`
}

type addTaskCandidate struct {
	sourceURL              string
	url                    string
	out                    string
	sizeBytes              int64
	extracted              bool
	protected              bool
	displayKey             string
	item                   ResolvedItem
	downloadGroup          *downloadgroups.DownloadGroupPlan
	externalHeaders        []string
	externalSizeBytes      int64
	skipHeadProbe          bool
	externalDedupKey       string
	finalURL               string
	callerOwnsGroupCleanup bool
}

type addTaskSummary struct {
	succeeded  []string
	duplicates []string
	errors     map[string]string
	groups     []rpc.DownloadGroup
	groupIDs   map[string]struct{}
}

type addTaskAuthBatchState struct {
	refreshGuard RefreshGuard

	mu        sync.Mutex
	refreshed map[string]struct{}
	stale     map[string]struct{}
}

type addTaskAuthSourcePlan struct {
	request                       AuthRequest
	key                           string
	locallyAvailableBeforeResolve bool
}

type addCandidateBatchState struct {
	mu            sync.Mutex
	existingUrls  map[string]bool
	candidateSeen map[string]bool
	summary       *addTaskSummary

	inflightMu sync.Map // url -> *sync.Mutex
}

func (b *addCandidateBatchState) lockForUrl(url string) func() {
	v, _ := b.inflightMu.LoadOrStore(url, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (b *addCandidateBatchState) checkAndMarkDuplicate(candidate addTaskCandidate, historyDuplicates map[string]bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if isDuplicateAddCandidate(candidate, b.existingUrls, historyDuplicates, b.candidateSeen) {
		return true
	}
	b.existingUrls[candidate.url] = true
	b.candidateSeen[candidate.url] = true
	return false
}

func (b *addCandidateBatchState) unmarkSeen(url string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.existingUrls, url)
	delete(b.candidateSeen, url)
}

func (b *addCandidateBatchState) recordSuccess(displayKey string, group *downloadgroups.DownloadGroupPlan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.summary.succeeded = append(b.summary.succeeded, displayKey)
	if group != nil {
		b.summary.addGroupLocked(group.GroupCopy())
	}
}

func (b *addCandidateBatchState) recordError(displayKey, errMsg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.summary.errors[displayKey] = errMsg
}

func (b *addCandidateBatchState) recordDuplicate(displayKey string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.summary.duplicates = append(b.summary.duplicates, displayKey)
}
