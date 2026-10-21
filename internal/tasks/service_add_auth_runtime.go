// service_add_auth_runtime.go manages WASM extractor pack credential preflight, token refresh, and header injection for protected candidate downloads.

package tasks

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

func isGenericAuthResolutionError(err error) bool {
	var genericErr *GenericAuthResolutionError
	return errors.As(err, &genericErr)
}

func (s *Service) buildCandidateHeaders(ctx context.Context, candidate addTaskCandidate) ([]string, error) {
	// Scan external-header conflicts before materializing: a conflicting
	// request must fail without ever touching the registry secret. Extracted
	// candidates do not populate externalHeaders today, so this is a
	// defensive invariant for future or alternate callers rather than a
	// currently reachable conflict path.
	if len(candidate.externalHeaders) > 0 && candidate.item.DownloadAuthRef != "" {
		for _, line := range candidate.externalHeaders {
			name, _, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "authorization", "cookie", "cookie2":
				return nil, errors.New("external authorization or cookie headers conflict with pack download auth")
			}
		}
	}
	extractorHeaders, err := s.buildExtractorHeaders(ctx, candidate)
	if err != nil {
		return nil, err
	}
	if len(candidate.externalHeaders) == 0 {
		return extractorHeaders, nil
	}
	if len(extractorHeaders) == 0 {
		return candidate.externalHeaders, nil
	}
	return mergeHeaders(candidate.externalHeaders, extractorHeaders), nil
}

func (s *Service) buildExtractorHeaders(ctx context.Context, candidate addTaskCandidate) ([]string, error) {
	if !candidate.extracted || s == nil || s.Adapter == nil {
		return nil, nil
	}

	return s.Adapter.BuildHeaders(ctx, candidate.item)
}

// mergeHeaders combines external (extension) headers with extractor headers,
// deduplicating by header name (case-insensitive). externalHeaders take priority.
func mergeHeaders(externalHeaders, extractorHeaders []string) []string {
	seen := make(map[string]struct{}, len(externalHeaders)+len(extractorHeaders))
	result := make([]string, 0, len(externalHeaders)+len(extractorHeaders))

	addIfNew := func(line string) {
		name, _, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		key := strings.ToLower(strings.TrimSpace(name))
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, line)
	}

	for _, h := range externalHeaders {
		addIfNew(h)
	}
	for _, h := range extractorHeaders {
		addIfNew(h)
	}
	return result
}

func (s *Service) newAddTaskAuthBatchState() *addTaskAuthBatchState {
	var guard RefreshGuard
	if s != nil && s.Adapter != nil {
		guard = s.Adapter.NewRefreshGuard()
	}
	return &addTaskAuthBatchState{
		refreshGuard: guard,
		refreshed:    make(map[string]struct{}),
		stale:        make(map[string]struct{}),
	}
}

func (s *addTaskAuthBatchState) markRefreshed(key string) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshed == nil {
		s.refreshed = make(map[string]struct{})
	}
	if _, ok := s.refreshed[key]; ok {
		return false
	}
	s.refreshed[key] = struct{}{}

	return true
}

func (s *addTaskAuthBatchState) markStaleRefresh(key string) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stale == nil {
		s.stale = make(map[string]struct{})
	}
	if _, ok := s.stale[key]; ok {
		return false
	}
	s.stale[key] = struct{}{}

	return true
}

func (s *Service) preflightSourceAuth(ctx context.Context, normalizedURL string, authState *addTaskAuthBatchState) ([]addTaskAuthSourcePlan, error) {
	if s == nil || s.Adapter == nil {
		return nil, nil
	}

	requests, err := s.Adapter.AuthRequestsForSource(ctx, normalizedURL)
	if err != nil {
		return nil, addTaskAuthUnavailableError()
	}
	if len(requests) == 0 {
		return nil, nil
	}

	plans := make([]addTaskAuthSourcePlan, 0, len(requests))
	for _, request := range requests {
		key := addTaskAuthRuntimeKey(request)
		preflight, err := s.Adapter.Preflight(ctx, request)
		if err != nil {
			return nil, addTaskAuthUnavailableError()
		}
		plan := addTaskAuthSourcePlan{
			request:                       request,
			key:                           key,
			locallyAvailableBeforeResolve: preflight.Matched && preflight.Required && preflight.Available,
		}
		plans = append(plans, plan)
		if !preflight.Matched || !preflight.Required || preflight.Available {
			continue
		}
		if !preflight.Refreshable {
			return nil, addTaskAuthUnavailableError()
		}
		if authState != nil && !authState.markStaleRefresh(key) {
			return nil, addTaskAuthUnavailableError()
		}
		var guard RefreshGuard
		if authState != nil {
			guard = authState.refreshGuard
		}
		refreshed, err := s.Adapter.RefreshOnRecoverablePreflightFailure(ctx, request, guard)
		if err != nil || !refreshed.Available {
			return nil, addTaskAuthUnavailableError()
		}
		postRefresh, err := s.Adapter.Preflight(ctx, request)
		if err != nil || !postRefresh.Available {
			return nil, addTaskAuthUnavailableError()
		}
	}

	return plans, nil
}

func canRefreshAddTaskSourceAuth(plans []addTaskAuthSourcePlan) bool {
	for _, plan := range plans {
		if plan.locallyAvailableBeforeResolve {
			return true
		}
	}

	return false
}

func (s *Service) refreshSourceAuthAfterGenericFailure(ctx context.Context, authState *addTaskAuthBatchState, plans []addTaskAuthSourcePlan) (bool, error) {
	if s == nil || s.Adapter == nil {
		return false, nil
	}
	for _, plan := range plans {
		if !plan.locallyAvailableBeforeResolve {
			continue
		}
		if authState != nil && !authState.markRefreshed(plan.key) {
			continue
		}
		var guard RefreshGuard
		if authState != nil {
			guard = authState.refreshGuard
		}
		result, err := s.Adapter.RefreshOnGenericFailure(ctx, plan.request, guard)
		if err != nil {
			return false, addTaskAuthUnavailableError()
		}
		if result.Provisioned {
			return true, nil
		}
	}

	return false, nil
}

func (s *Service) preflightCandidateAuth(ctx context.Context, candidate addTaskCandidate, authState *addTaskAuthBatchState) error {
	hasAuthRef := candidate.item.AuthProfileRef != "" || candidate.item.DownloadAuthRef != "" || candidate.item.HeaderProfileRef != ""
	if !candidate.extracted || !hasAuthRef {
		return nil
	}
	if s == nil || s.Adapter == nil {
		// An extracted candidate carrying credential refs without an
		// adapter would submit bare — fail closed instead.
		return addTaskAuthUnavailableError()
	}
	if candidate.item.DownloadAuthRef != "" {
		// Download-auth items only need the registry admission check; the
		// auth-profile preflight machinery does not apply to them.
		if err := s.Adapter.ValidateItemAuthPolicy(candidate.item); err != nil {
			return addTaskAuthUnavailableError()
		}
		return nil
	}
	if candidate.item.AuthProfileRef == "" {
		// Header-profile-only items need adapter headers but no runtime
		// profile preflight.
		return nil
	}
	if candidate.item.PackID == "" {
		return addTaskAuthUnavailableError()
	}
	if err := s.Adapter.ValidateItemAuthPolicy(candidate.item); err != nil {
		return addTaskAuthUnavailableError()
	}

	request := AuthRequest{
		Ref:             candidate.item.Ref,
		PackID:          candidate.item.PackID,
		PackVersion:     candidate.item.PackVersion,
		AssetSHA256:     candidate.item.AssetSHA256,
		ManifestSHA256:  candidate.item.ManifestSHA256,
		PayloadSHA256:   candidate.item.PayloadSHA256,
		SignatureSHA256: candidate.item.SignatureSHA256,
		PublicKeySHA256: candidate.item.PublicKeySHA256,
		SourceURL:       candidate.item.SourceURL,
		TargetURL:       candidate.item.URL,
		ProfileRef:      candidate.item.AuthProfileRef,
	}
	preflight, err := s.Adapter.Preflight(ctx, request)
	if err != nil {
		return addTaskAuthUnavailableError()
	}
	if !preflight.Matched {
		if preflight.NoRuntime {
			return nil
		}
		return addTaskAuthUnavailableError()
	}
	if preflight.Available {
		return nil
	}
	if !preflight.Refreshable {
		return addTaskAuthUnavailableError()
	}
	key := addTaskAuthRuntimePreflightKey(request)
	if authState != nil && !authState.markStaleRefresh(key) {
		return addTaskAuthUnavailableError()
	}
	var guard RefreshGuard
	if authState != nil {
		guard = authState.refreshGuard
	}
	refreshed, err := s.Adapter.RefreshOnRecoverablePreflightFailure(ctx, request, guard)
	if err != nil || !refreshed.Available {
		return addTaskAuthUnavailableError()
	}
	ensured, err := s.Adapter.Preflight(ctx, request)
	if err != nil || !ensured.Available {
		return addTaskAuthUnavailableError()
	}

	return nil
}

func addTaskAuthRuntimeKey(request AuthRequest) string {
	profileRef := request.ProfileRef
	if profileRef == "" {
		profileRef = "*"
	}
	parts := []string{
		request.PackID,
		request.PackVersion,
		request.AssetSHA256,
		request.ManifestSHA256,
		request.PayloadSHA256,
		request.SignatureSHA256,
		request.PublicKeySHA256,
		profileRef,
	}

	return strings.Join(parts, "\x00")
}

func addTaskAuthRuntimePreflightKey(request AuthRequest) string {
	return addTaskAuthRuntimeKey(request) + "\x00" + authTaskHashString(request.SourceURL) + "\x00" + authTaskHashString(request.TargetURL)
}

func authTaskHashString(value string) string {
	var hash uint64 = 14695981039346656037
	for i := 0; i < len(value); i++ {
		hash ^= uint64(value[i])
		hash *= 1099511628211
	}

	return strconv.FormatUint(hash, 16)
}

func addTaskAuthUnavailableError() error {
	return errors.New("auth profile unavailable")
}
