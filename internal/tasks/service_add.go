package tasks

import (
	"context"
	"errors"
	"net"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
	surgetypes "goaria-v3/internal/surge/types"
)

// Compile-time check: *Service satisfies the extension.TaskAdder interface.
var _ extension.TaskAdder = (*Service)(nil)

// scopeClassifier is a package-level scope classifier with host caching.
var scopeClassifier = speedstats.NewScopeClassifier()

// collectActiveTaskInfos builds a TrackedTaskInfo slice from the current tracker state
// for BandwidthLedger pre-scan seeding.
func collectActiveTaskInfos() []smartthread.TrackedTaskInfo {
	return BuildOccupancyTaskInfos()
}

// BuildOccupancyTaskInfos builds hybrid-seed inputs for NewBandwidthLedger.
// Shared by AddURI/BatchAddURI and Resume so seed policy cannot drift.
func BuildOccupancyTaskInfos() []smartthread.TrackedTaskInfo {
	tr := monitor.State.GetTracker()
	if tr == nil {
		return nil
	}
	occupied := tr.GetOccupancyTrackedTasks()
	if len(occupied) == 0 {
		return nil
	}

	speedByGID := map[string]int64{}
	if monitor.Cache != nil {
		for _, t := range monitor.Cache.GetActive() {
			speedByGID[t.GID] = parseDownloadSpeed(t.DownloadSpeed)
		}
	}

	mon := monitor.State.GetMonitor()
	result := make([]smartthread.TrackedTaskInfo, 0, len(occupied))
	for _, t := range occupied {
		info := smartthread.TrackedTaskInfo{
			GID:             t.GID,
			Status:          t.Status,
			Scope:           t.Scope,
			Domain:          t.Domain,
			EnvKey:          t.CurrentEnvKey,
			ThreadCount:     t.ThreadCount,
			TargetBandwidth: t.TargetBandwidth,
			AllocatedAt:     t.AllocatedAt,
		}
		cacheSpeed := speedByGID[t.GID]
		if strings.HasPrefix(t.GID, "sg_") {
			if mon != nil {
				bps, ready := mon.LastRawBps(t.GID)
				info.MacroReady = ready
				if ready {
					info.TelemetryBps = bps
				} else {
					info.TelemetryBps = cacheSpeed
				}
			} else {
				info.TelemetryBps = cacheSpeed
			}
		} else {
			info.TelemetryBps = cacheSpeed
		}
		result = append(result, info)
	}
	return result
}

func parseDownloadSpeed(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// ExistingDomainWorkersFromTelemetry returns the total occupancy worker count
// for all tracked tasks matching the given scope+domain. Used by
// ClampToServerLimit to account for existing domain concurrency before
// launching a new task. Cold fallback: max(len(snapshots), ThreadCount).
func ExistingDomainWorkersFromTelemetry(scope, domain string) int {
	return ExistingDomainWorkersFromTelemetryExcluding(scope, domain, "")
}

// AddUriFromExtension processes a download request from the browser extension.
// It goes through the directAddTaskCandidate path (extracted=false, protected=false)
// carrying extension metadata (headers/size/dedupKey) so the task benefits from the
// full smartthread/BBR/ConvergenceTicker/CDN detector pipeline.
func (s *Service) AddUriFromExtension(req extension.DownloadRequest) (string, error) {
	normalizedUrl := strings.TrimSpace(req.URL)
	if normalizedUrl == "" {
		return "", errors.New("empty url")
	}

	headers := req.Headers
	if req.DownloadPage != "" {
		headers = ensureRefererHeader(headers, req.DownloadPage)
	}

	if err := rpc.ValidateAddURIHeaders(headers); err != nil {
		return "", err
	}

	active, _ := s.Engine.TellActive()
	waiting, _ := s.Engine.TellWaiting(0, 1000)
	stopped, _ := s.Engine.TellStopped(0, 1000)
	existingURLs := collectExistingTaskSourceURLs(active, waiting, stopped)

	dedupTarget := normalizedUrl
	if req.DedupKey != "" {
		dedupTarget = req.DedupKey
	}
	if existingURLs[dedupTarget] || history.ContainsSource(dedupTarget) {
		return "", errors.New("duplicate")
	}

	candidate := directAddTaskCandidate(normalizedUrl)
	candidate.externalHeaders = headers
	candidate.externalSizeBytes = req.FileSize
	candidate.skipHeadProbe = req.SkipHeadProbe
	candidate.externalDedupKey = req.DedupKey
	candidate.finalURL = req.FinalURL
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
		return "", err
	}
	return gid, nil
}

// ensureRefererHeader adds a Referer header from the download page URL if none is present.
func ensureRefererHeader(headers []string, downloadPage string) []string {
	for _, h := range headers {
		name, _, ok := strings.Cut(h, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Referer") {
			return headers
		}
	}
	return append(headers, "Referer: "+downloadPage)
}

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
	authState := s.newAddTaskAuthBatchState()
	batchState := &addCandidateBatchState{
		existingUrls:  existingURLs,
		candidateSeen: candidateSeen,
		summary:       &summary,
	}
	s.addNormalizedInput(context.Background(), normalizedUrl, batchState, nil, authState)

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
	authState := s.newAddTaskAuthBatchState()

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
			result.Errors[normalized] = s.redactAddTaskError(err)
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
			result.Errors["batch"] = s.redactAddTaskError(err)
			return result
		}
		for i := range pendingCandidates {
			if pendingCandidates[i].downloadGroup == nil {
				pendingCandidates[i].downloadGroup = batchGroup
			}
		}
	}

	ledger := smartthread.NewBandwidthLedger(collectActiveTaskInfos())
	batchState := &addCandidateBatchState{
		existingUrls:  existingUrls,
		candidateSeen: seenCandidates,
		summary:       &summary,
	}
	submitCandidatesConcurrently(s, context.Background(), pendingCandidates, batchState, historyDuplicates, authState, ledger)
	result.Succeeded = append(result.Succeeded, summary.succeeded...)
	result.Duplicates = append(result.Duplicates, summary.duplicates...)
	result.Groups = append(result.Groups, summary.groups...)

	return result
}

func (s *Service) addNormalizedInput(ctx context.Context, normalizedURL string, batchState *addCandidateBatchState, historyDuplicates map[string]bool, authState *addTaskAuthBatchState) {
	candidates, err := s.resolveAddCandidates(ctx, normalizedURL, authState)
	if err != nil {
		batchState.recordError(normalizedURL, s.redactAddTaskError(err))
		return
	}

	ledger := smartthread.NewBandwidthLedger(collectActiveTaskInfos())
	if len(candidates) <= 1 {
		for _, candidate := range candidates {
			s.submitAddCandidate(ctx, candidate, batchState, historyDuplicates, authState, ledger)
		}
		return
	}
	submitCandidatesConcurrently(s, ctx, candidates, batchState, historyDuplicates, authState, ledger)
}

const addCandidateConcurrency = 12

func submitCandidatesConcurrently(s *Service, ctx context.Context, candidates []addTaskCandidate, batchState *addCandidateBatchState, historyDuplicates map[string]bool, authState *addTaskAuthBatchState, ledger *smartthread.BandwidthLedger) {
	if len(candidates) <= 1 {
		for _, candidate := range candidates {
			s.submitAddCandidate(ctx, candidate, batchState, historyDuplicates, authState, ledger)
		}
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, addCandidateConcurrency)
	for _, candidate := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(c addTaskCandidate) {
			defer wg.Done()
			defer func() { <-sem }()
			s.submitAddCandidate(ctx, c, batchState, historyDuplicates, authState, ledger)
		}(candidate)
	}
	wg.Wait()
}

func (s *Service) submitAddCandidate(ctx context.Context, candidate addTaskCandidate, batchState *addCandidateBatchState, historyDuplicates map[string]bool, authState *addTaskAuthBatchState, ledger *smartthread.BandwidthLedger) {
	displayKey := candidateDisplayKey(candidate)
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
}

// addGroupLocked appends a deduplicated group. Caller must hold the
// addCandidateBatchState mutex that guards all summary fields.
func (s *addTaskSummary) addGroupLocked(group rpc.DownloadGroup) {
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
	return addCandidatesFromResolution(normalizedURL, resolution)
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

func isGenericAuthResolutionError(err error) bool {
	var genericErr *GenericAuthResolutionError
	return errors.As(err, &genericErr)
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
		safeOut, err := SafeAria2OutFilename(candidate.out)
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

	dir := config.Get().DownloadDir
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

	if config.Get().SmartThreadMode {
		fileSize := candidate.sizeBytes
		var ttfbMs int64
		var remoteIP string

		if !candidate.extracted && !candidate.protected && !candidate.skipHeadProbe && candidate.externalSizeBytes == 0 {
			var probe rpc.HeadProbeResult
			if len(headers) > 0 {
				probe = rpc.HeadProbeWithHeaders(candidate.url, 3*time.Second, headers)
			} else {
				probe = rpc.HeadProbe(candidate.url, 3*time.Second)
			}
			fileSize = probe.ContentLength
			ttfbMs = probe.TTFBMs
			remoteIP = probe.RemoteIP
		}

		var scope, domain string
		if remoteIP != "" {
			scope, domain = scopeClassifier.ClassifyByURLAndIP(candidate.url, remoteIP)
		} else {
			// Extension path: skipHeadProbe=true → remoteIP="".
			// Resolve CDN IP from finalURL (post-redirect); fall back to original URL.
			classificationURL := candidate.finalURL
			if classificationURL == "" {
				classificationURL = candidate.url
			}
			finalIP := resolveHostIP(classificationURL)
			scope, domain = scopeClassifier.ClassifyByURLAndIP(candidate.url, finalIP)
			remoteIP = finalIP
		}

		envKey := monitor.ComputeEnvKeyForDownload(candidate.url, remoteIP)

		maxConn, _ := strconv.Atoi(config.Get().MaxConnections)
		if maxConn <= 0 {
			maxConn = 8
		}

		var params smartthread.ThreadParams
		ledger.WithAlloc(func() {
			params = smartthread.Calculate(smartthread.CalcParams{
				FileSize:                fileSize,
				MaxConnections:          maxConn,
				Scope:                   scope,
				Domain:                  domain,
				EnvKey:                  envKey,
				ReservedBandwidth:       ledger.Reserved(scope, envKey),
				ReservedDomainBandwidth: ledger.ReservedByDomain(scope, domain),
				Ledger:                  ledger,
				ActiveMACsFunc: func() []string {
					ne := monitor.State.GetNetEnv()
					if ne == nil {
						return nil
					}
					return ne.GetAllActiveMACs()
				},
				ComputeEnvKeyFunc: monitor.ComputeEnvKey,
			})
			params = smartthread.ClampToServerLimit(params, fileSize, scope, domain,
				ExistingDomainWorkersFromTelemetry(scope, domain)+ledger.ReservedWorkers(scope, domain),
				smartthread.GetDefaultServerLimits())
			ledger.Reserve(scope, envKey, params.TargetBandwidth)
			ledger.ReserveByDomain(scope, domain, params.TargetBandwidth)
			ledger.ReserveWorkers(scope, domain, params.Split)
		})
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
			ledger.Release(scope, envKey, params.TargetBandwidth)
			ledger.ReleaseByDomain(scope, domain, params.TargetBandwidth)
			ledger.ReleaseWorkers(scope, domain, params.Split)
			return "", err
		}

		if gid != "" {
			if params.Split > 0 {
				if tracker := monitor.State.GetTracker(); tracker != nil {
					tracker.SetThreadInfo(gid, params.Split, params.IsExploration)
					// Set IsKeepAlive when initial split < nSat
					tracker.SetKeepAlive(gid, params.Split < params.NSat)
					tracker.SetMinChunk(gid, params.MinSize)
					tracker.SetTargetBandwidth(gid, params.TargetBandwidth)
				}
			}
			if tracker := monitor.State.GetTracker(); tracker != nil {
				tracker.SetScopeAndEnv(gid, scope, ttfbMs, domain, envKey)
			}
		}
	} else {
		maxConn, _ := strconv.Atoi(config.Get().MaxConnections)
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
			classificationURL := candidate.finalURL
			if classificationURL == "" {
				classificationURL = candidate.url
			}
			finalIP := resolveHostIP(classificationURL)
			scope, domain := scopeClassifier.ClassifyByURLAndIP(candidate.url, finalIP)
			envKey := monitor.ComputeEnvKeyForDownload(candidate.url, finalIP)
			if tracker := monitor.State.GetTracker(); tracker != nil {
				tracker.SetScopeAndEnv(gid, scope, 0, domain, envKey)
			}
		}
	}

	if candidate.downloadGroup != nil {
		candidate.downloadGroup.RecordSuccess()
	}

	return gid, nil
}

func (s *Service) buildCandidateHeaders(ctx context.Context, candidate addTaskCandidate) ([]string, error) {
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
	if !candidate.extracted || candidate.item.AuthProfileRef == "" {
		return nil
	}
	if s == nil || s.Adapter == nil {
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

// resolveHostIP extracts the host from url and does a DNS lookup.
// Returns "" on failure (caller falls back to proxy envKey).
func resolveHostIP(rawurl string) string {
	u, err := neturl.Parse(rawurl)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return host
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0]
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

func firstErrorString(errors map[string]string) string {
	for _, err := range errors {
		return err
	}

	return ""
}

func (s *Service) redactAddTaskError(err error) string {
	if err == nil {
		return ""
	}
	if surgetypes.IsInsufficientDiskSpace(err) {
		return surgetypes.ErrInsufficientDiskSpace.Error()
	}
	if s != nil && s.Adapter != nil {
		return redactAssignmentValues(s.Adapter.RedactError(err))
	}
	return redactAssignmentValues(err.Error())
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
