package tasks

import (
	"context"
	"errors"
	"strings"
	"time"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/history"
	"goaria-v3/internal/smartthread"
)

type PreparedDirectAddItem struct {
	ClientItemID  string
	URL           string
	FinalURL      string
	Headers       []string
	FileSize      int64
	HasFileSize   bool
	SkipHeadProbe bool
	Filename      string
	DownloadPage  string
}

type PreparedDirectAddRequest struct {
	Items       []PreparedDirectAddItem
	CreateGroup bool
	FolderName  string
}

func (s *Service) AddPreparedDirectItems(ctx context.Context, req PreparedDirectAddRequest) (PreparedAddResult, error) {
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
	if s == nil || s.Engine == nil {
		return result, errors.New("download engine unavailable")
	}

	seenKeys := make(map[string]struct{}, len(req.Items))
	ownerByURL := make(map[string]string, len(req.Items))
	owners := make([]PreparedDirectAddItem, 0, len(req.Items))
	intraDuplicates := make([]string, 0)
	for _, item := range req.Items {
		key := strings.TrimSpace(item.ClientItemID)
		url := strings.TrimSpace(item.URL)
		if key == "" || url == "" {
			return result, ErrInvalidPreparedAdd
		}
		if _, dup := seenKeys[key]; dup {
			return result, ErrInvalidPreparedAdd
		}
		seenKeys[key] = struct{}{}
		if _, exists := ownerByURL[url]; exists {
			intraDuplicates = append(intraDuplicates, key)
			continue
		}
		ownerByURL[url] = key
		item.ClientItemID = key
		item.URL = url
		owners = append(owners, item)
	}

	active, _ := s.Engine.TellActive()
	waiting, _ := s.Engine.TellWaiting(0, 1000)
	stopped, _ := s.Engine.TellStopped(0, 1000)
	existingURLs := collectExistingTaskSourceURLs(active, waiting, stopped)

	ownerSources := make([]string, 0, len(owners))
	for _, owner := range owners {
		ownerSources = append(ownerSources, owner.URL)
	}
	historyDuplicates := history.ContainsSources(ownerSources)

	preflightDuplicates := make([]string, 0)
	eligible := make([]PreparedDirectAddItem, 0, len(owners))
	for _, owner := range owners {
		if existingURLs[owner.URL] || historyDuplicates[owner.URL] {
			preflightDuplicates = append(preflightDuplicates, owner.ClientItemID)
			continue
		}
		eligible = append(eligible, owner)
	}

	result.Duplicates = append(result.Duplicates, intraDuplicates...)
	result.Duplicates = append(result.Duplicates, preflightDuplicates...)

	var group *downloadgroups.DownloadGroupPlan
	if req.CreateGroup && len(eligible) >= 2 {
		plan, err := downloadgroups.NewDownloadGroupPlanWithFolderName(
			downloadgroups.DownloadGroupKindCollection,
			len(eligible),
			time.Now(),
			req.FolderName,
		)
		if err != nil {
			return result, err
		}
		group = plan
		defer group.CleanupIfUnused()
	}

	candidates := make([]addTaskCandidate, 0, len(eligible))
	for _, item := range eligible {
		candidate := directAddTaskCandidate(item.URL)
		candidate.displayKey = item.ClientItemID
		candidate.finalURL = item.FinalURL
		headers := append([]string{}, item.Headers...)
		if item.DownloadPage != "" {
			headers = ensureRefererHeader(headers, item.DownloadPage)
		}
		candidate.externalHeaders = headers
		candidate.skipHeadProbe = item.SkipHeadProbe
		if item.HasFileSize {
			candidate.externalSizeBytes = item.FileSize
			if item.FileSize > 0 {
				candidate.sizeBytes = item.FileSize
			}
		}
		if item.Filename != "" {
			candidate.out = item.Filename
		}
		if group != nil {
			candidate.downloadGroup = group
			candidate.callerOwnsGroupCleanup = true
		}
		candidates = append(candidates, candidate)
	}

	summary := addTaskSummary{errors: result.Errors}
	batchState := &addCandidateBatchState{
		existingUrls:  existingURLs,
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
