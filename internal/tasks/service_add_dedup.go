package tasks

import (
	"context"
	"strings"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	surgetypes "goaria-v3/internal/surge/types"
)

// submitAddCandidate admits one final candidate. With a nil Deduper it keeps
// the legacy per-batch URL serialization; with a shared Deduper it first
// claims the engine-bound candidate URL so concurrent calls for the same
// target coalesce onto one real engine add.
func (s *Service) submitAddCandidate(ctx context.Context, candidate addTaskCandidate, batchState *addCandidateBatchState, historyDuplicates map[string]bool, authState *addTaskAuthBatchState, ledger *smartthread.BandwidthLedger) {
	if s.Adapter != nil && candidate.item.Ref != "" {
		// Release the neutral ref claim once submission settles — covers
		// success, failure, and the duplicate-skip below.
		defer s.Adapter.Release(candidate.item.Ref)
	}
	displayKey := candidateDisplayKey(candidate)

	if s.Deduper == nil {
		unlock := batchState.lockForUrl(candidate.url)
		defer unlock()

		if batchState.checkAndMarkDuplicate(candidate, historyDuplicates) {
			batchState.recordDuplicate(displayKey)
			return
		}

		if _, err := s.addTaskCandidate(ctx, candidate, authState, ledger); err != nil {
			batchState.unmarkSeen(candidate.url)
			batchState.recordError(displayKey, s.redactAddTaskError(err))
			return
		}

		batchState.recordSuccess(displayKey, candidate.downloadGroup)
		return
	}

	claim, res, owner := s.Deduper.acquire(ctx, candidate.url)
	if !owner {
		// Loser consumes the token's real outcome; it never touches the
		// engine, provisional marks it does not own, or group accounting.
		if res.available {
			batchState.markConfirmedSeen(candidate.url)
			batchState.recordDuplicate(displayKey)
			return
		}
		batchState.recordError(displayKey, s.redactAddTaskError(res.err))
		return
	}

	settled := false
	marked := false
	defer func() {
		if settled {
			return
		}
		// Unwind before a result was determined (panic, forgotten return):
		// drop only the marks this owner made, then settle as aborted so
		// attached waiters get a real failure and the key frees for retry.
		if marked {
			batchState.unmarkSeen(candidate.url)
		}
		s.Deduper.finish(claim, addClaimResult{err: errAddClaimAborted})
	}()

	if batchState.checkAndMarkDuplicate(candidate, historyDuplicates) {
		batchState.recordDuplicate(displayKey)
		s.Deduper.finish(claim, addClaimResult{available: true})
		settled = true
		return
	}
	marked = true

	gid, err := s.addTaskCandidate(ctx, candidate, authState, ledger)
	if err != nil {
		safeErr := s.canonicalizeExtensionAddError(err)
		batchState.unmarkSeen(candidate.url)
		marked = false
		batchState.recordError(displayKey, s.redactAddTaskError(err))
		s.Deduper.finish(claim, addClaimResult{err: safeErr})
		settled = true
		return
	}

	batchState.recordSuccess(displayKey, candidate.downloadGroup)
	s.Deduper.finish(claim, addClaimResult{gid: gid, available: true})
	settled = true
}

// submitExtensionCandidate builds and admits the extension single-item
// candidate, returning the engine gid or the canonicalized failure.
func (s *Service) submitExtensionCandidate(req extension.DownloadRequest, normalizedUrl string, headers []string) (string, error) {
	candidate := directAddTaskCandidate(normalizedUrl)
	candidate.externalHeaders = headers
	candidate.externalSizeBytes = req.FileSize
	candidate.skipHeadProbe = req.SkipHeadProbe
	candidate.externalDedupKey = req.DedupKey
	candidate.finalURL = req.FinalURL
	candidate.pageTitle = req.PageTitle
	// An explicit hls handoff pins the strategy even when neither url nor
	// finalURL carries a manifest suffix; URL-based detection stays the
	// fallback for plain download requests.
	if req.Type == extension.MsgTypeHLS {
		candidate.strategy = surgetypes.StrategyHLS
	}
	if req.Filename != "" {
		candidate.out = req.Filename
	}
	if req.FileSize > 0 {
		candidate.sizeBytes = req.FileSize
	}

	authState := s.newAddTaskAuthBatchState()
	ledger := smartthread.NewBandwidthLedger(collectActiveTaskInfos())

	gid, err := s.addTaskCandidate(context.Background(), candidate, authState, ledger)
	if err != nil {
		return "", s.canonicalizeExtensionAddError(err)
	}
	return gid, nil
}

// firstGIDForTaskSource returns the first non-empty GID whose task carries
// the dedup target as a source URI, searching the already-fetched snapshots
// in the same order their URLs were collected. No extra engine query.
func firstGIDForTaskSource(active, waiting, stopped []rpc.Task, target string) string {
	for _, list := range [][]rpc.Task{active, waiting, stopped} {
		for _, task := range list {
			if task.GID == "" {
				continue
			}
			for _, file := range task.Files {
				for _, uri := range file.Uris {
					if strings.TrimSpace(uri.Uri) == target {
						return task.GID
					}
				}
			}
		}
	}
	return ""
}
