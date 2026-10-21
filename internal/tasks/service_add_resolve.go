// service_add_resolve.go handles raw URL resolution into download candidates and download groups via extractor adapters or direct fallback.

package tasks

import (
	"context"
	"time"

	"goaria-v3/internal/downloadgroups"
)

func (s *Service) resolveAddCandidates(ctx context.Context, normalizedURL string, authState *addTaskAuthBatchState) ([]addTaskCandidate, error) {
	if s == nil || s.Adapter == nil {
		return []addTaskCandidate{directAddTaskCandidate(normalizedURL)}, nil
	}

	sourcePlans, err := s.preflightSourceAuth(ctx, normalizedURL, authState)
	if err != nil {
		return nil, err
	}

	resolution, err := s.Adapter.Resolve(ctx, normalizedURL)
	if err != nil {
		if !isGenericAuthResolutionError(err) || !canRefreshAddTaskSourceAuth(sourcePlans) {
			return nil, err
		}
		refreshed, refreshErr := s.refreshSourceAuthAfterGenericFailure(ctx, authState, sourcePlans)
		if refreshErr != nil {
			return nil, refreshErr
		}
		if !refreshed {
			return nil, err
		}
		resolution, err = s.Adapter.Resolve(ctx, normalizedURL)
		if err != nil {
			return nil, err
		}
	}
	candidates, err := addCandidatesFromResolution(normalizedURL, resolution)
	if err != nil {
		// Items already carry minted neutral refs; conversion failure
		// must drop their adapter claims so no holder is stranded.
		s.releaseResolutionClaims(resolution)
		return nil, err
	}
	if resolution.Status != ResolutionStatusMatched {
		// An unmatched resolution never becomes candidates; drop any
		// refs the adapter still minted for its items.
		s.releaseResolutionClaims(resolution)
	}

	return candidates, nil
}

// releaseResolutionClaims drops the adapter-side refs minted for a
// resolution that could not be converted into candidates.
func (s *Service) releaseResolutionClaims(resolution Resolution) {
	if s == nil || s.Adapter == nil {
		return
	}
	for _, item := range resolution.Items {
		if item.Ref != "" {
			s.Adapter.Release(item.Ref)
		}
	}
}

func addCandidatesFromResolution(normalizedURL string, resolution Resolution) ([]addTaskCandidate, error) {
	if resolution.Status != ResolutionStatusMatched {
		return []addTaskCandidate{directAddTaskCandidate(normalizedURL)}, nil
	}

	var group *downloadgroups.DownloadGroupPlan
	if len(resolution.Items) >= 2 {
		var err error
		group, err = downloadgroups.NewDownloadGroupPlan(downloadgroups.DownloadGroupKindCollection, len(resolution.Items), time.Now())
		if err != nil {
			return nil, err
		}
	}
	candidates := make([]addTaskCandidate, 0, len(resolution.Items))
	for _, item := range resolution.Items {
		candidate := extractorAddTaskCandidate(item)
		candidate.downloadGroup = group
		candidates = append(candidates, candidate)
	}

	return candidates, nil
}

func directAddTaskCandidate(normalizedURL string) addTaskCandidate {
	return addTaskCandidate{
		sourceURL:  normalizedURL,
		url:        normalizedURL,
		displayKey: normalizedURL,
	}
}

func extractorAddTaskCandidate(item ResolvedItem) addTaskCandidate {
	displayKey := item.URL
	if displayKey == "" && item.ID != "" {
		displayKey = item.SourceURL + "#" + item.ID
	}

	return addTaskCandidate{
		sourceURL:  item.SourceURL,
		url:        item.URL,
		out:        item.Filename,
		sizeBytes:  item.SizeBytes,
		extracted:  true,
		protected:  item.AuthProfileRef != "" || item.HeaderProfileRef != "" || item.DownloadAuthRef != "",
		displayKey: displayKey,
		item:       item,
	}
}

func candidateDisplayKey(candidate addTaskCandidate) string {
	if candidate.displayKey != "" {
		return candidate.displayKey
	}
	if candidate.url != "" {
		return candidate.url
	}
	return candidate.sourceURL
}
