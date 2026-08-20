package extractor

import (
	"context"
	"fmt"
	"sync"

	"goaria-v3/internal/tasks"
)

type addTaskDispatcherInterface interface {
	Resolve(ctx context.Context, rawURL string) (AddTaskResolution, error)
	BuildAria2Headers(ctx context.Context, item ResolvedAddItem) ([]string, error)
	AuthRuntimeRequestsForSource(ctx context.Context, rawURL string) ([]HostAuthRuntimeRequest, error)
}

type TasksAdapter struct {
	dispatcher addTaskDispatcherInterface
	runtime    *HostAuthRuntime

	mu             sync.Mutex
	nextRef        int64
	sourceRequests map[string]HostAuthRuntimeRequest
	resolvedItems  map[string]ResolvedAddItem
}

func NewTasksAdapter(dispatcher addTaskDispatcherInterface, runtime *HostAuthRuntime) *TasksAdapter {
	return &TasksAdapter{
		dispatcher:     dispatcher,
		runtime:        runtime,
		sourceRequests: make(map[string]HostAuthRuntimeRequest),
		resolvedItems:  make(map[string]ResolvedAddItem),
	}
}

func (a *TasksAdapter) Resolve(ctx context.Context, rawURL string) (tasks.Resolution, error) {
	resolution, err := a.dispatcher.Resolve(ctx, rawURL)
	if err != nil {
		if IsGenericAuthResolutionError(err) {
			return tasks.Resolution{}, &tasks.GenericAuthResolutionError{}
		}
		return tasks.Resolution{}, err
	}

	status := tasks.ResolutionStatusUnmatched
	if resolution.Matched {
		status = tasks.ResolutionStatusMatched
	}
	items := make([]tasks.ResolvedItem, 0, len(resolution.Items))
	for _, item := range resolution.Items {
		items = append(items, a.toNeutralItem(item))
	}

	return tasks.Resolution{
		Status:    status,
		SourceURL: resolution.SourceURL,
		Items:     items,
	}, nil
}

func (a *TasksAdapter) Mint(item ResolvedAddItem) tasks.ResolvedItem {
	return a.toNeutralItem(CloneResolvedAddItem(item))
}

func (a *TasksAdapter) Release(ref string) {
	if a == nil || ref == "" {
		return
	}
	a.mu.Lock()
	delete(a.resolvedItems, ref)
	a.mu.Unlock()
}

func (a *TasksAdapter) toNeutralItem(item ResolvedAddItem) tasks.ResolvedItem {
	a.mu.Lock()
	a.nextRef++
	ref := fmt.Sprintf("r-%d", a.nextRef)
	a.resolvedItems[ref] = item
	a.mu.Unlock()

	return tasks.ResolvedItem{
		Ref:              ref,
		ID:               item.ID,
		SourceURL:        item.SourceURL,
		URL:              item.URL,
		Filename:         item.Filename,
		SizeBytes:        item.SizeBytes,
		AuthProfileRef:   item.AuthProfileRef,
		HeaderProfileRef: item.HeaderProfileRef,
		PackID:           item.PackManifest.PackID,
		PackVersion:      item.PackIdentity.PackVersion,
		AssetSHA256:      item.PackIdentity.AssetSHA256,
		ManifestSHA256:   item.PackIdentity.ManifestSHA256,
		PayloadSHA256:    item.PackIdentity.PayloadSHA256,
		SignatureSHA256:  item.PackIdentity.SignatureSHA256,
		PublicKeySHA256:  item.PackIdentity.PublicKeySHA256,
	}
}

func (a *TasksAdapter) BuildHeaders(ctx context.Context, item tasks.ResolvedItem) ([]string, error) {
	a.mu.Lock()
	fullItem, ok := a.resolvedItems[item.Ref]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("resolved item not found for ref %q", item.Ref)
	}
	headers, err := a.dispatcher.BuildAria2Headers(ctx, fullItem)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), headers...), nil
}

func (a *TasksAdapter) AuthRequestsForSource(ctx context.Context, rawURL string) ([]tasks.AuthRequest, error) {
	requests, err := a.dispatcher.AuthRuntimeRequestsForSource(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	result := make([]tasks.AuthRequest, 0, len(requests))
	for _, req := range requests {
		result = append(result, a.toNeutralRequest(req))
	}
	return result, nil
}

func (a *TasksAdapter) toNeutralRequest(req HostAuthRuntimeRequest) tasks.AuthRequest {
	a.mu.Lock()
	a.nextRef++
	ref := fmt.Sprintf("r-%d", a.nextRef)
	a.sourceRequests[ref] = req
	a.mu.Unlock()

	return tasks.AuthRequest{
		Ref:             ref,
		PackID:          req.PackIdentity.PackID,
		PackVersion:     req.PackIdentity.PackVersion,
		AssetSHA256:     req.PackIdentity.AssetSHA256,
		ManifestSHA256:  req.PackIdentity.ManifestSHA256,
		PayloadSHA256:   req.PackIdentity.PayloadSHA256,
		SignatureSHA256: req.PackIdentity.SignatureSHA256,
		PublicKeySHA256: req.PackIdentity.PublicKeySHA256,
		SourceURL:       req.SourceURL,
		TargetURL:       req.TargetURL,
		ProfileRef:      string(req.ProfileRef),
	}
}

func (a *TasksAdapter) Preflight(ctx context.Context, request tasks.AuthRequest) (tasks.PreflightResult, error) {
	if a.runtime == nil {
		return tasks.PreflightResult{Available: true, NoRuntime: true}, nil
	}
	extractorReq := a.toExtractorRequest(request)
	result, err := a.runtime.Preflight(ctx, extractorReq)
	if err != nil {
		return tasks.PreflightResult{}, err
	}
	return tasks.PreflightResult{
		Matched:     result.Matched,
		Required:    result.Required,
		Available:   result.Available && a.allProfilesAvailable(result),
		Refreshable: result.Refreshable,
	}, nil
}

func (a *TasksAdapter) RefreshOnRecoverablePreflightFailure(ctx context.Context, request tasks.AuthRequest, guard tasks.RefreshGuard) (tasks.RefreshResult, error) {
	if a.runtime == nil {
		return tasks.RefreshResult{}, nil
	}
	extractorReq := a.toExtractorRequest(request)
	innerGuard := a.unwrapGuard(guard)
	result, err := a.runtime.RefreshOnRecoverablePreflightFailure(ctx, extractorReq, innerGuard)
	if err != nil {
		return tasks.RefreshResult{}, err
	}
	return tasks.RefreshResult{
		Provisioned: result.Provisioned,
		Available:   result.Available && a.allProfilesAvailable(result),
	}, nil
}

func (a *TasksAdapter) RefreshOnGenericFailure(ctx context.Context, request tasks.AuthRequest, guard tasks.RefreshGuard) (tasks.RefreshResult, error) {
	if a.runtime == nil {
		return tasks.RefreshResult{}, nil
	}
	extractorReq := a.toExtractorRequest(request)
	innerGuard := a.unwrapGuard(guard)
	result, err := a.runtime.RefreshOnGenericFailure(ctx, extractorReq, innerGuard)
	if err != nil {
		return tasks.RefreshResult{}, err
	}
	return tasks.RefreshResult{
		Provisioned: result.Provisioned,
		Available:   result.Available && a.allProfilesAvailable(result),
	}, nil
}

func (a *TasksAdapter) ValidateItemAuthPolicy(item tasks.ResolvedItem) error {
	a.mu.Lock()
	fullItem, ok := a.resolvedItems[item.Ref]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("resolved item not found for ref %q", item.Ref)
	}
	return validateResolvedAddItemAuthPolicy(fullItem)
}

func (a *TasksAdapter) NewRefreshGuard() tasks.RefreshGuard {
	return &tasksRefreshGuard{inner: NewHostAuthRuntimeBatchGuard()}
}

func (a *TasksAdapter) RedactError(err error) string {
	if err == nil {
		return ""
	}
	return RedactSensitive(err.Error())
}

func (a *TasksAdapter) toExtractorRequest(req tasks.AuthRequest) HostAuthRuntimeRequest {
	a.mu.Lock()
	stored, ok := a.sourceRequests[req.Ref]
	a.mu.Unlock()
	if ok {
		return cloneHostAuthRuntimeRequest(stored)
	}

	a.mu.Lock()
	item, itemOK := a.resolvedItems[req.Ref]
	a.mu.Unlock()

	identity := VerifiedPackIdentity{
		PackID:          req.PackID,
		PackVersion:     req.PackVersion,
		AssetSHA256:     req.AssetSHA256,
		ManifestSHA256:  req.ManifestSHA256,
		PayloadSHA256:   req.PayloadSHA256,
		SignatureSHA256: req.SignatureSHA256,
		PublicKeySHA256: req.PublicKeySHA256,
	}
	manifest := Manifest{PackID: req.PackID}
	if itemOK {
		manifest = cloneManifest(item.PackManifest)
		identity = item.PackIdentity
	}
	return HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     manifest,
		SourceURL:    req.SourceURL,
		TargetURL:    req.TargetURL,
		ProfileRef:   AuthProfileID(req.ProfileRef),
	}
}

func (a *TasksAdapter) unwrapGuard(guard tasks.RefreshGuard) *HostAuthRuntimeBatchGuard {
	if guard == nil {
		return nil
	}
	g, ok := guard.(*tasksRefreshGuard)
	if !ok {
		return nil
	}
	return g.inner
}

func (a *TasksAdapter) allProfilesAvailable(result HostAuthRuntimeResult) bool {
	if len(result.ProfileStatuses) == 0 {
		return result.Available
	}
	for _, status := range result.ProfileStatuses {
		if status.Status != HostAuthRuntimeProfileAvailable {
			return false
		}
	}
	return true
}

type tasksRefreshGuard struct {
	inner *HostAuthRuntimeBatchGuard
}

func (g *tasksRefreshGuard) MarkRefreshed(key string) bool {
	if g == nil || g.inner == nil {
		return true
	}
	return g.inner.mark(key)
}

func cloneHostAuthRuntimeRequest(req HostAuthRuntimeRequest) HostAuthRuntimeRequest {
	return HostAuthRuntimeRequest{
		PackIdentity: req.PackIdentity,
		Manifest:     cloneManifest(req.Manifest),
		SourceURL:    req.SourceURL,
		TargetURL:    req.TargetURL,
		ProfileRef:   req.ProfileRef,
	}
}
