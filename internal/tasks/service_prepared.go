package tasks

import (
	"context"
	"errors"
	"strings"
	"time"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
)

const maxPreparedAddItems = 128

var ErrInvalidPreparedAdd = errors.New("invalid prepared add request")

type PreparedAddItem struct {
	Item       ResolvedItem
	DisplayKey string
}

type PreparedAddRequest struct {
	Items       []PreparedAddItem
	CreateGroup bool
	FolderName  string
}

type PreparedAddResult struct {
	Succeeded  []string
	Duplicates []string
	Errors     map[string]string
	Groups     []rpc.DownloadGroup
}

func (s *Service) AddPreparedExtractorItems(ctx context.Context, req PreparedAddRequest) (PreparedAddResult, error) {
	result := PreparedAddResult{
		Succeeded:  []string{},
		Duplicates: []string{},
		Errors:     make(map[string]string),
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if len(req.Items) == 0 || len(req.Items) > maxPreparedAddItems {
		return result, ErrInvalidPreparedAdd
	}

	candidates := make([]addTaskCandidate, 0, len(req.Items))
	uniqueURLs := make(map[string]struct{}, len(req.Items))
	seenKeys := make(map[string]struct{}, len(req.Items))
	for _, item := range req.Items {
		key := strings.TrimSpace(item.DisplayKey)
		if key == "" {
			return result, ErrInvalidPreparedAdd
		}
		if _, dup := seenKeys[key]; dup {
			return result, ErrInvalidPreparedAdd
		}
		seenKeys[key] = struct{}{}
		candidate := extractorAddTaskCandidate(item.Item)
		candidate.displayKey = key
		if url := strings.TrimSpace(candidate.url); url != "" {
			uniqueURLs[url] = struct{}{}
		}
		candidates = append(candidates, candidate)
	}

	var group *downloadgroups.DownloadGroupPlan
	if req.CreateGroup && len(uniqueURLs) >= 2 {
		plan, err := downloadgroups.NewDownloadGroupPlanWithFolderName(
			downloadgroups.DownloadGroupKindCollection,
			len(uniqueURLs),
			time.Now(),
			req.FolderName,
		)
		if err != nil {
			return result, err
		}
		group = plan
		for i := range candidates {
			candidates[i].downloadGroup = group
			candidates[i].callerOwnsGroupCleanup = true
		}
		defer group.CleanupIfUnused()
	}

	active, _ := s.Engine.TellActive()
	waiting, _ := s.Engine.TellWaiting(0, 1000)
	stopped, _ := s.Engine.TellStopped(0, 1000)
	existingUrls := collectExistingTaskSourceURLs(active, waiting, stopped)

	normalizedSources := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if url := strings.TrimSpace(candidate.url); url != "" {
			normalizedSources = append(normalizedSources, url)
		}
	}
	historyDuplicates := history.ContainsSources(normalizedSources)

	summary := addTaskSummary{errors: result.Errors}
	batchState := &addCandidateBatchState{
		existingUrls:  existingUrls,
		candidateSeen: make(map[string]bool),
		summary:       &summary,
	}
	authState := s.newAddTaskAuthBatchState()
	ledger := smartthread.NewBandwidthLedger(collectActiveTaskInfos())
	submitCandidatesConcurrently(s, ctx, candidates, batchState, historyDuplicates, authState, ledger)

	result.Succeeded = append(result.Succeeded, summary.succeeded...)
	result.Duplicates = append(result.Duplicates, summary.duplicates...)
	result.Groups = append(result.Groups, summary.groups...)
	return result, nil
}
