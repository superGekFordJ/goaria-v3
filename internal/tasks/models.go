package tasks

import (
	"sync"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/rpc"
	surgetypes "goaria-v3/internal/surge/types"
)

type BatchAddResult struct {
	Succeeded  []string            `json:"succeeded"`
	Duplicates []string            `json:"duplicates"`
	Errors     map[string]string   `json:"errors"`
	Groups     []rpc.DownloadGroup `json:"groups,omitempty"`
}

// TaskDetailEnvelope is the per-gid result of GetTaskDetails.
type TaskDetailEnvelope struct {
	Found  bool        `json:"found"`
	Detail *TaskDetail `json:"detail,omitempty"`
}

// TaskDetail carries on-demand facts absent from the delta hot path. Zero
// fields mean unknown and are omitted; the UI hides them.
type TaskDetail struct {
	GID         string   `json:"gid"`
	AddedAt     int64    `json:"added_at,omitempty"`     // unix seconds
	CompletedAt int64    `json:"completed_at,omitempty"` // unix seconds
	TimeTakenMs int64    `json:"time_taken_ms,omitempty"`
	AvgSpeed    int64    `json:"avg_speed,omitempty"`  // B/s, terminal only
	PeakSpeed   int64    `json:"peak_speed,omitempty"` // B/s
	URIs        []string `json:"uris,omitempty"`       // primary first, deduped
	// ChunkStates is the unpacked per-chunk status map (0 pending / 1
	// downloading / 2 completed), sg_ only, len == ChunkCount when present.
	ChunkStates []int `json:"chunk_states,omitempty"`
	ChunkCount  int   `json:"chunk_count,omitempty"` // total engine chunks
	ChunkSize   int64 `json:"chunk_size,omitempty"`  // bytes per chunk
	// ChunkProgress is the per-chunk downloaded byte count, sg_ only;
	// normally len == ChunkCount when present, consumers index defensively.
	ChunkProgress []int64 `json:"chunk_progress,omitempty"`
}

type addTaskCandidate struct {
	sourceURL string
	url       string
	out       string
	sizeBytes int64
	extracted bool
	// protected marks credential-bearing items; extracted items already
	// skip probes unconditionally, so this is belt-and-suspenders should a
	// future probe path ever include them.
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
	// strategy is the download strategy discriminator; empty means the legacy
	// byte-range path. normalizeHLSCandidate sets StrategyHLS for explicit
	// flags or .m3u8 URLs on non-extracted candidates.
	strategy surgetypes.DownloadStrategy
	// pageTitle feeds HLS output naming (third priority after explicit out
	// and the manifest stem).
	pageTitle string
	// hlsResume carries the caller-owned resume checkpoint; cloned at every
	// handoff boundary so the engine never aliases the input slices.
	hlsResume *surgetypes.HLSResumeState
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
