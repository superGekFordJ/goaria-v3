package tasks

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
)

// scopeClassifier is a package-level scope classifier with host caching.
var scopeClassifier = speedstats.NewScopeClassifier()

func (s *Service) AddUri(url string) string {
	normalizedUrl := strings.TrimSpace(url)
	active, _ := s.Engine.TellActive()
	waiting, _ := s.Engine.TellWaiting(0, 1000)
	stopped, _ := s.Engine.TellStopped(0, 1000)
	existingURLs := collectExistingTaskSourceURLs(active, waiting, stopped)

	if existingURLs[normalizedUrl] {
		return "duplicate"
	}

	if history.ContainsSource(normalizedUrl) {
		return "duplicate"
	}

	summary := addTaskSummary{errors: make(map[string]string)}
	candidateSeen := make(map[string]bool)
	authState := newAddTaskAuthBatchState()
	s.addNormalizedInput(context.Background(), normalizedUrl, existingURLs, nil, candidateSeen, authState, &summary)

	if len(summary.succeeded) == 0 {
		if len(summary.errors) > 0 {
			return firstErrorString(summary.errors)
		}
		if len(summary.duplicates) > 0 {
			return "duplicate"
		}
		return "success"
	}
	if len(summary.errors) > 0 {
		return "partial success: " + firstErrorString(summary.errors)
	}

	return "success"
}

func (s *Service) BatchAddUri(urls []string) BatchAddResult {
	result := BatchAddResult{
		Succeeded:  []string{},
		Duplicates: []string{},
		Errors:     make(map[string]string),
	}

	if len(urls) > 100 {
		urls = urls[:100]
	}

	active, _ := s.Engine.TellActive()
	waiting, _ := s.Engine.TellWaiting(0, 1000)
	stopped, _ := s.Engine.TellStopped(0, 1000)

	existingUrls := collectExistingTaskSourceURLs(active, waiting, stopped)

	normalizedSources := make([]string, 0, len(urls))
	for _, rawUrl := range urls {
		normalized := strings.TrimSpace(rawUrl)
		if normalized == "" {
			continue
		}
		normalizedSources = append(normalizedSources, normalized)
	}
	historyDuplicates := history.ContainsSources(normalizedSources)

	seenRaw := make(map[string]bool)
	seenCandidates := make(map[string]bool)
	pendingCandidates := make([]addTaskCandidate, 0, len(urls))
	batchThresholdSeen := make(map[string]struct{}, len(urls))
	batchGroupCount := 0
	summary := addTaskSummary{errors: result.Errors}
	authState := newAddTaskAuthBatchState()

	for _, rawUrl := range urls {
		normalized := strings.TrimSpace(rawUrl)
		if normalized == "" {
			continue
		}

		if seenRaw[normalized] {
			result.Duplicates = append(result.Duplicates, normalized)
			continue
		}
		seenRaw[normalized] = true

		if existingUrls[normalized] || historyDuplicates[normalized] {
			result.Duplicates = append(result.Duplicates, normalized)
			continue
		}

		candidates, err := s.resolveAddCandidates(context.Background(), normalized, authState)
		if err != nil {
			result.Errors[normalized] = redactAddTaskError(err)
			continue
		}

		pendingCandidates = append(pendingCandidates, candidates...)
		for _, candidate := range candidates {
			if candidate.downloadGroup != nil {
				continue
			}
			if existingUrls[candidate.url] || historyDuplicates[candidate.url] || history.ContainsSource(candidate.url) {
				continue
			}
			if _, exists := batchThresholdSeen[candidate.url]; exists {
				continue
			}
			batchThresholdSeen[candidate.url] = struct{}{}
			batchGroupCount++
		}
	}

	if batchGroupCount >= 5 {
		batchGroup, err := downloadgroups.NewDownloadGroupPlan(downloadgroups.DownloadGroupKindBatch, batchGroupCount, time.Now())
		if err != nil {
			result.Errors["batch"] = redactAddTaskError(err)
			return result
		}
		for i := range pendingCandidates {
			if pendingCandidates[i].downloadGroup == nil {
				pendingCandidates[i].downloadGroup = batchGroup
			}
		}
	}

	ledger := smartthread.NewBandwidthLedger()
	for _, candidate := range pendingCandidates {
		s.submitAddCandidate(context.Background(), candidate, existingUrls, historyDuplicates, seenCandidates, authState, &summary, ledger)
	}
	result.Succeeded = append(result.Succeeded, summary.succeeded...)
	result.Duplicates = append(result.Duplicates, summary.duplicates...)
	result.Groups = append(result.Groups, summary.groups...)

	return result
}

func (s *Service) addNormalizedInput(ctx context.Context, normalizedURL string, existingURLs map[string]bool, historyDuplicates map[string]bool, candidateSeen map[string]bool, authState *addTaskAuthBatchState, summary *addTaskSummary) {
	candidates, err := s.resolveAddCandidates(ctx, normalizedURL, authState)
	if err != nil {
		summary.errors[normalizedURL] = redactAddTaskError(err)
		return
	}

	ledger := smartthread.NewBandwidthLedger()
	for _, candidate := range candidates {
		s.submitAddCandidate(ctx, candidate, existingURLs, historyDuplicates, candidateSeen, authState, summary, ledger)
	}
}

func (s *Service) submitAddCandidate(ctx context.Context, candidate addTaskCandidate, existingURLs map[string]bool, historyDuplicates map[string]bool, candidateSeen map[string]bool, authState *addTaskAuthBatchState, summary *addTaskSummary, ledger *smartthread.BandwidthLedger) {
	displayKey := candidateDisplayKey(candidate)
	if isDuplicateAddCandidate(candidate, existingURLs, historyDuplicates, candidateSeen) {
		summary.duplicates = append(summary.duplicates, displayKey)
		return
	}

	if _, err := s.addTaskCandidate(ctx, candidate, authState, ledger); err != nil {
		summary.errors[displayKey] = redactAddTaskError(err)
		return
	}

	summary.succeeded = append(summary.succeeded, displayKey)
	existingURLs[candidate.url] = true
	candidateSeen[candidate.url] = true
	if candidate.downloadGroup != nil {
		summary.addGroup(candidate.downloadGroup.GroupCopy())
	}
}

func (s *addTaskSummary) addGroup(group rpc.DownloadGroup) {
	if group.ID == "" {
		return
	}
	if s.groupIDs == nil {
		s.groupIDs = make(map[string]struct{})
	}
	if _, exists := s.groupIDs[group.ID]; exists {
		return
	}
	s.groupIDs[group.ID] = struct{}{}
	s.groups = append(s.groups, group)
}

func (s *Service) resolveAddCandidates(ctx context.Context, normalizedURL string, authState *addTaskAuthBatchState) ([]addTaskCandidate, error) {
	if s == nil || s.Dispatcher == nil {
		return []addTaskCandidate{directAddTaskCandidate(normalizedURL)}, nil
	}

	runtime := s.Runtime
	sourcePlans, err := s.preflightSourceAuth(ctx, normalizedURL, runtime, authState)
	if err != nil {
		return nil, err
	}

	resolution, err := s.Dispatcher.Resolve(ctx, normalizedURL)
	if err != nil {
		if !extractor.IsGenericAuthResolutionError(err) || !canRefreshAddTaskSourceAuth(sourcePlans) {
			return nil, err
		}
		refreshed, refreshErr := s.refreshSourceAuthAfterGenericFailure(ctx, runtime, authState, sourcePlans)
		if refreshErr != nil {
			return nil, refreshErr
		}
		if !refreshed {
			return nil, err
		}
		resolution, err = s.Dispatcher.Resolve(ctx, normalizedURL)
		if err != nil {
			return nil, err
		}
	}
	return addCandidatesFromResolution(normalizedURL, resolution)
}

func addCandidatesFromResolution(normalizedURL string, resolution extractor.AddTaskResolution) ([]addTaskCandidate, error) {
	if !resolution.Matched {
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

func extractorAddTaskCandidate(item extractor.ResolvedAddItem) addTaskCandidate {
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
		protected:  item.AuthProfileRef != "" || item.HeaderProfileRef != "",
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

func isDuplicateAddCandidate(candidate addTaskCandidate, existingURLs map[string]bool, historyDuplicates map[string]bool, candidateSeen map[string]bool) bool {
	if existingURLs[candidate.url] || candidateSeen[candidate.url] {
		return true
	}
	if historyDuplicates != nil && historyDuplicates[candidate.url] {
		return true
	}

	return history.ContainsSource(candidate.url)
}

func (s *Service) addTaskCandidate(ctx context.Context, candidate addTaskCandidate, authState *addTaskAuthBatchState, ledger *smartthread.BandwidthLedger) (string, error) {
	out := ""
	if candidate.out != "" {
		safeOut, err := extractor.SafeAria2OutFilename(candidate.out)
		if err != nil {
			return "", err
		}
		out = safeOut
	}

	if err := s.preflightCandidateAuth(ctx, candidate, authState); err != nil {
		return "", err
	}

	headers, err := s.buildCandidateHeaders(ctx, candidate)
	if err != nil {
		return "", err
	}
	registerGroup := func(gid string) error {
		if candidate.downloadGroup == nil || gid == "" {
			return nil
		}
		group := candidate.downloadGroup.GroupCopy()
		monitor.Cache.RegisterTaskGroup(gid, group)
		if tracker := monitor.State.GetTracker(); tracker != nil {
			tracker.SetTaskGroup(gid, group)
		}
		return nil
	}

	dir := config.Current.DownloadDir
	if candidate.downloadGroup != nil {
		if err := candidate.downloadGroup.EnsureDir(); err != nil {
			return "", err
		}
		group := candidate.downloadGroup.GroupCopy()
		dir = group.Dir
	}
	if candidate.downloadGroup != nil {
		defer candidate.downloadGroup.CleanupIfUnused()
	}

	var gid string

	if config.Current.SmartThreadMode {
		fileSize := candidate.sizeBytes
		var ttfbMs int64
		var remoteIP string

		if !candidate.extracted && !candidate.protected && len(headers) == 0 {
			probe := rpc.HeadProbe(candidate.url, 3*time.Second)
			fileSize = probe.ContentLength
			ttfbMs = probe.TTFBMs
			remoteIP = probe.RemoteIP
		}

		var scope, domain string
		if remoteIP != "" {
			scope, domain = scopeClassifier.ClassifyByURLAndIP(candidate.url, remoteIP)
		} else {
			scope, domain = scopeClassifier.ClassifyByURL(candidate.url)
		}

		maxConn, _ := strconv.Atoi(config.Current.MaxConnections)
		if maxConn <= 0 {
			maxConn = 8
		}

		params := smartthread.Calculate(smartthread.CalcParams{
			FileSize:          fileSize,
			MaxConnections:    maxConn,
			Scope:             scope,
			Domain:            domain,
			ReservedBandwidth: ledger.Reserved(scope),
		})
		ledger.Reserve(scope, params.TargetBandwidth)
		var err error
		gid, err = s.Engine.AddUri(candidate.url, rpc.AddURIOptions{
			Dir:          dir,
			Out:          out,
			Headers:      headers,
			Split:        params.Split,
			MinSplitSize: params.MinSize,
			BeforeSave:   registerGroup,
		})
		if err != nil {
			return "", err
		}

		if gid != "" {
			if params.Split > 0 {
				if tracker := monitor.State.GetTracker(); tracker != nil {
					tracker.SetThreadInfo(gid, params.Split, params.IsExploration)
				}
			}
			if tracker := monitor.State.GetTracker(); tracker != nil {
				tracker.SetScope(gid, scope, ttfbMs, domain)
			}
		}
	} else {
		maxConn, _ := strconv.Atoi(config.Current.MaxConnections)
		if maxConn <= 0 {
			maxConn = 8
		}
		var err error
		gid, err = s.Engine.AddUri(candidate.url, rpc.AddURIOptions{
			Dir:        dir,
			Out:        out,
			Headers:    headers,
			Split:      maxConn,
			BeforeSave: registerGroup,
		})
		if err != nil {
			return "", err
		}
		if gid != "" {
			if tracker := monitor.State.GetTracker(); tracker != nil {
				tracker.SetThreadInfo(gid, maxConn, false)
			}
			scope, domain := scopeClassifier.ClassifyByURL(candidate.url)
			if tracker := monitor.State.GetTracker(); tracker != nil {
				tracker.SetScope(gid, scope, 0, domain)
			}
		}
	}

	if candidate.downloadGroup != nil {
		candidate.downloadGroup.RecordSuccess()
	}

	return gid, nil
}

func (s *Service) buildCandidateHeaders(ctx context.Context, candidate addTaskCandidate) ([]string, error) {
	if !candidate.extracted || s == nil || s.Dispatcher == nil {
		return nil, nil
	}

	return s.Dispatcher.BuildAria2Headers(ctx, candidate.item)
}

func newAddTaskAuthBatchState() *addTaskAuthBatchState {
	return &addTaskAuthBatchState{
		refreshGuard: extractor.NewHostAuthRuntimeBatchGuard(),
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

func (s *Service) preflightSourceAuth(ctx context.Context, normalizedURL string, runtime *extractor.HostAuthRuntime, authState *addTaskAuthBatchState) ([]addTaskAuthSourcePlan, error) {
	if s == nil || s.Dispatcher == nil || runtime == nil {
		return nil, nil
	}
	planner, ok := s.Dispatcher.(ExtractorAuthRuntimeSourcePlanner)
	if !ok {
		return nil, nil
	}

	requests, err := planner.AuthRuntimeRequestsForSource(ctx, normalizedURL)
	if err != nil {
		return nil, addTaskAuthUnavailableError()
	}
	if len(requests) == 0 {
		return nil, nil
	}

	plans := make([]addTaskAuthSourcePlan, 0, len(requests))
	for _, request := range requests {
		key := addTaskAuthRuntimeKey(request)
		preflight, err := runtime.Preflight(ctx, request)
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
		var guard *extractor.HostAuthRuntimeBatchGuard
		if authState != nil {
			guard = authState.refreshGuard
		}
		refreshed, err := runtime.RefreshOnRecoverablePreflightFailure(ctx, request, guard)
		if err != nil || !refreshed.Available || !addTaskAuthProfilesAvailable(refreshed) {
			return nil, addTaskAuthUnavailableError()
		}
		postRefresh, err := runtime.Preflight(ctx, request)
		if err != nil || !postRefresh.Available || !addTaskAuthProfilesAvailable(postRefresh) {
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

func (s *Service) refreshSourceAuthAfterGenericFailure(ctx context.Context, runtime *extractor.HostAuthRuntime, authState *addTaskAuthBatchState, plans []addTaskAuthSourcePlan) (bool, error) {
	if runtime == nil {
		return false, nil
	}
	for _, plan := range plans {
		if !plan.locallyAvailableBeforeResolve {
			continue
		}
		if authState != nil && !authState.markRefreshed(plan.key) {
			continue
		}
		var guard *extractor.HostAuthRuntimeBatchGuard
		if authState != nil {
			guard = authState.refreshGuard
		}
		result, err := runtime.RefreshOnGenericFailure(ctx, plan.request, guard)
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
	if !candidate.extracted || candidate.item.AuthProfileRef == "" {
		return nil
	}
	if candidate.item.PackIdentity.PackID == "" || candidate.item.Manifest.PackID == "" {
		return addTaskAuthUnavailableError()
	}
	if err := extractor.ValidateResolvedAddItemAuthPolicy(candidate.item); err != nil {
		return addTaskAuthUnavailableError()
	}
	runtime := s.Runtime
	if runtime == nil {
		return nil
	}

	request := extractor.HostAuthRuntimeRequest{
		PackIdentity: candidate.item.PackIdentity,
		Manifest:     candidate.item.Manifest,
		SourceURL:    candidate.item.SourceURL,
		TargetURL:    candidate.item.URL,
		ProfileRef:   extractor.AuthProfileID(candidate.item.AuthProfileRef),
	}
	preflight, err := runtime.Preflight(ctx, request)
	if err != nil {
		return addTaskAuthUnavailableError()
	}
	if !preflight.Matched {
		return addTaskAuthUnavailableError()
	}
	if preflight.Available && addTaskAuthProfilesAvailable(preflight) {
		return nil
	}
	if !preflight.Refreshable {
		return addTaskAuthUnavailableError()
	}
	key := addTaskAuthRuntimePreflightKey(request)
	if authState != nil && !authState.markStaleRefresh(key) {
		return addTaskAuthUnavailableError()
	}
	var guard *extractor.HostAuthRuntimeBatchGuard
	if authState != nil {
		guard = authState.refreshGuard
	}
	refreshed, err := runtime.RefreshOnRecoverablePreflightFailure(ctx, request, guard)
	if err != nil || !refreshed.Available || !addTaskAuthProfilesAvailable(refreshed) {
		return addTaskAuthUnavailableError()
	}
	ensured, err := runtime.Preflight(ctx, request)
	if err != nil || !ensured.Available || !addTaskAuthProfilesAvailable(ensured) {
		return addTaskAuthUnavailableError()
	}

	return nil
}

func addTaskAuthProfilesAvailable(result extractor.HostAuthRuntimeResult) bool {
	if len(result.ProfileStatuses) == 0 {
		return result.Available
	}
	for _, status := range result.ProfileStatuses {
		if status.Status != extractor.HostAuthRuntimeProfileAvailable {
			return false
		}
	}

	return true
}

func addTaskAuthRuntimeKey(request extractor.HostAuthRuntimeRequest) string {
	profileRef := string(request.ProfileRef)
	if profileRef == "" {
		profileRef = "*"
	}
	parts := []string{
		request.PackIdentity.PackID,
		request.PackIdentity.PackVersion,
		request.PackIdentity.AssetSHA256,
		request.PackIdentity.ManifestSHA256,
		request.PackIdentity.PayloadSHA256,
		request.PackIdentity.SignatureSHA256,
		request.PackIdentity.PublicKeySHA256,
		profileRef,
	}

	return strings.Join(parts, "\x00")
}

func addTaskAuthRuntimePreflightKey(request extractor.HostAuthRuntimeRequest) string {
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

func firstErrorString(errors map[string]string) string {
	for _, err := range errors {
		return err
	}

	return ""
}

func redactAddTaskError(err error) string {
	if err == nil {
		return ""
	}

	return redactAssignmentValues(extractor.RedactSensitive(err.Error()))
}

func redactAssignmentValues(input string) string {
	markers := []string{"token=", "secret=", "auth=", "key="}
	var builder strings.Builder
	lower := strings.ToLower(input)
	for offset := 0; offset < len(input); {
		match := -1
		marker := ""
		for _, candidate := range markers {
			if idx := strings.Index(lower[offset:], candidate); idx >= 0 && (match < 0 || idx < match) {
				match = idx
				marker = candidate
			}
		}
		if match < 0 {
			builder.WriteString(input[offset:])
			break
		}

		start := offset + match
		valueStart := start + len(marker)
		valueEnd := valueStart
		for valueEnd < len(input) && !strings.ContainsRune(" \t\r\n&;,'\"`,)", rune(input[valueEnd])) {
			valueEnd++
		}

		builder.WriteString(input[offset:valueStart])
		if valueEnd > valueStart {
			builder.WriteString("[REDACTED]")
		}
		offset = valueEnd
	}

	return builder.String()
}

func collectTaskSourceURLs(existingURLs map[string]bool, tasks []rpc.Task) {
	for _, task := range tasks {
		for _, file := range task.Files {
			for _, uri := range file.Uris {
				existingURLs[strings.TrimSpace(uri.Uri)] = true
			}
		}
	}
}

func collectExistingTaskSourceURLs(active, waiting, stopped []rpc.Task) map[string]bool {
	existingURLs := make(map[string]bool)
	collectTaskSourceURLs(existingURLs, active)
	collectTaskSourceURLs(existingURLs, waiting)
	collectTaskSourceURLs(existingURLs, stopped)

	return existingURLs
}
