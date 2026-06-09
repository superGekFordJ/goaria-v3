package tasks

import (
	"context"
	"sync"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/rpc"
)

type BatchAddResult struct {
	Succeeded  []string            `json:"succeeded"`
	Duplicates []string            `json:"duplicates"`
	Errors     map[string]string   `json:"errors"`
	Groups     []rpc.DownloadGroup `json:"groups,omitempty"`
}

type ExtractorAddTaskDispatcher interface {
	Resolve(ctx context.Context, rawURL string) (extractor.AddTaskResolution, error)
	BuildAria2Headers(ctx context.Context, item extractor.ResolvedAddItem) ([]string, error)
}

type ExtractorAuthRuntimeSourcePlanner interface {
	AuthRuntimeRequestsForSource(ctx context.Context, rawURL string) ([]extractor.HostAuthRuntimeRequest, error)
}

type addTaskCandidate struct {
	sourceURL     string
	url           string
	out           string
	sizeBytes     int64
	extracted     bool
	protected     bool
	displayKey    string
	item          extractor.ResolvedAddItem
	downloadGroup *downloadgroups.DownloadGroupPlan
}

type addTaskSummary struct {
	succeeded  []string
	duplicates []string
	errors     map[string]string
	groups     []rpc.DownloadGroup
	groupIDs   map[string]struct{}
}

type addTaskAuthBatchState struct {
	refreshGuard *extractor.HostAuthRuntimeBatchGuard

	mu        sync.Mutex
	refreshed map[string]struct{}
	stale     map[string]struct{}
}

type addTaskAuthSourcePlan struct {
	request                       extractor.HostAuthRuntimeRequest
	key                           string
	locallyAvailableBeforeResolve bool
}
