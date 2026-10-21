package tasks

import (
	"context"
	"errors"
	"net"
	neturl "net/url"
	"path"
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
	"goaria-v3/internal/surge/strategy/hls"
	surgetypes "goaria-v3/internal/surge/types"
)

// Compile-time check: *Service satisfies the extension.TaskAdder interface.
var _ extension.TaskAdder = (*Service)(nil)

// scopeClassifier is a package-level scope classifier with host caching.
var scopeClassifier = speedstats.NewScopeClassifier()

// ErrHLSEngineUnsupported rejects an HLS add when the active engine cannot
// run the Surge strategy; it is a terminal refusal, never a fallback.
var ErrHLSEngineUnsupported = errors.New("hls download requires surge engine")

// Injectable seam for tests: the manifest preflight is the only network
// touch inside the HLS add path.
var hlsPreflightManifest = hls.PreflightHLSManifest

// collectActiveTaskInfos builds a TrackedTaskInfo slice from the current tracker state
// for BandwidthLedger pre-scan seeding.
func collectActiveTaskInfos() []smartthread.TrackedTaskInfo {
	return BuildOccupancyTaskInfos()
}

// BuildOccupancyTaskInfos builds unified-seed inputs for NewBandwidthLedger.
// Delegates to the monitor collector so AddURI/BatchAddURI, Resume and pickup
// share one eligibility/engine-selection implementation.
func BuildOccupancyTaskInfos() []smartthread.TrackedTaskInfo {
	return monitor.OccupancyTaskInfos()
}

// ExistingDomainWorkersFromTelemetry returns the total occupancy worker count
// for all tracked tasks matching the given scope+domain. Used by
// ClampToServerLimit to account for existing domain concurrency before
// launching a new task. Cold fallback: max(len(snapshots), ThreadCount).
func ExistingDomainWorkersFromTelemetry(scope, domain string) int {
	return ExistingDomainWorkersFromTelemetryExcluding(scope, domain, "")
}

// kickAdmissionIfSurge notifies the resident admission controller through the
// monitor's single callback slot after a Surge add/enqueue succeeds. Service
// holds no controller pointer; nil monitor is a no-op.
func kickAdmissionIfSurge(gid string) {
	if !strings.HasPrefix(gid, "sg_") {
		return
	}
	if mon := monitor.State.GetMonitor(); mon != nil {
		mon.KickAdmission()
	}
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

// ensureRefererHeader adds a Referer header from the download page URL if no Referer line is present.
// A Referrer line does not count: engines send the header name as given.
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
	if s.Adapter != nil && candidate.item.Ref != "" {
		// Release the neutral ref claim once submission settles — covers
		// success, failure, and the duplicate-skip below.
		defer s.Adapter.Release(candidate.item.Ref)
	}
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

func isDuplicateAddCandidate(candidate addTaskCandidate, existingURLs map[string]bool, historyDuplicates map[string]bool, candidateSeen map[string]bool) bool {
	if existingURLs[candidate.url] || candidateSeen[candidate.url] {
		return true
	}
	if historyDuplicates != nil && historyDuplicates[candidate.url] {
		return true
	}

	return history.ContainsSource(candidate.url)
}

// isHLSManifestURL reports whether rawURL is an HTTP(S) URL whose path ends
// in .m3u8 (case-insensitive, query/fragment ignored). It deliberately does
// not match .m3u or non-HTTP schemes.
func isHLSManifestURL(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false
	}
	u, err := neturl.Parse(trimmed)
	if err != nil || u == nil || u.Host == "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	p := u.EscapedPath()
	if p == "" {
		p = u.Path
	}
	if unescaped, uerr := neturl.PathUnescape(p); uerr == nil {
		p = unescaped
	}
	return strings.EqualFold(path.Ext(p), ".m3u8")
}

// normalizeHLSCandidate folds HLS intent into the candidate once: an explicit
// StrategyHLS always wins, otherwise a .m3u8 url/finalURL is detected. HLS has
// no byte-range semantics, so sizes are zeroed and the HEAD probe is skipped.
// Idempotent — repeated calls re-assert the same fields.
func normalizeHLSCandidate(candidate *addTaskCandidate) {
	if candidate == nil || candidate.extracted {
		return
	}
	if candidate.strategy == surgetypes.StrategyHLS ||
		isHLSManifestURL(candidate.url) ||
		isHLSManifestURL(candidate.finalURL) {
		candidate.strategy = surgetypes.StrategyHLS
		candidate.sizeBytes = 0
		candidate.externalSizeBytes = 0
		candidate.skipHeadProbe = true
		candidate.hlsResume = candidate.hlsResume.Clone()
	}
}

// supportsHLSStrategy reports whether the engine can actually run the Surge
// HLS strategy. A wrapped-but-inactive Surge engine counts as unsupported.
func supportsHLSStrategy(engine rpc.DownloadEngine) bool {
	switch eng := engine.(type) {
	case *rpc.HybridEngine:
		return eng != nil && eng.IsSurgeActive()
	case *rpc.SurgeEngine:
		return eng != nil && eng.IsSurgeActive()
	default:
		return false
	}
}

// surgeRuntimeConfigForPreflight borrows the engine's effective runtime config
// so the manifest preflight shares proxy/DNS policy with the download itself.
// The GoAria user agent always wins when configured.
func (s *Service) surgeRuntimeConfigForPreflight() *surgetypes.RuntimeConfig {
	var runCfg *surgetypes.RuntimeConfig
	if s != nil {
		switch eng := s.Engine.(type) {
		case *rpc.SurgeEngine:
			runCfg = eng.RuntimeConfig()
		case *rpc.HybridEngine:
			if se, ok := eng.SurgeEngineRef(); ok && se != nil {
				runCfg = se.RuntimeConfig()
			}
		}
	}
	if runCfg == nil {
		runCfg = surgetypes.DefaultRuntimeConfig()
	}
	if cfg := config.Get(); cfg != nil {
		if ua := strings.TrimSpace(cfg.UserAgent); ua != "" {
			cp := *runCfg
			cp.UserAgent = ua
			runCfg = &cp
		}
	}
	return runCfg
}

// headerLinesToMap converts "Name: value" lines into the map form the HLS
// preflight consumes; malformed lines are dropped, matching the engine side.
func headerLinesToMap(lines []string) map[string]string {
	if len(lines) == 0 {
		return nil
	}
	m := make(map[string]string, len(lines))
	for _, line := range lines {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" {
			m[k] = v
		}
	}
	return m
}

// candidateClassificationURL picks the URL used for scope/domain/envKey
// classification. For HLS the segment host — not the manifest host — is the
// real traffic destination, so SegmentRefURL leads the fallback chain.
func candidateClassificationURL(candidate addTaskCandidate) string {
	if candidate.strategy == surgetypes.StrategyHLS {
		if candidate.hlsResume != nil {
			if ref := strings.TrimSpace(candidate.hlsResume.SegmentRefURL); ref != "" {
				return ref
			}
		}
		if final := strings.TrimSpace(candidate.finalURL); final != "" {
			return final
		}
		if u := strings.TrimSpace(candidate.url); u != "" {
			return u
		}
		return strings.TrimSpace(candidate.sourceURL)
	}
	if final := strings.TrimSpace(candidate.finalURL); final != "" {
		return final
	}
	return strings.TrimSpace(candidate.url)
}

func shouldSkipEngineProbe(candidate addTaskCandidate, sanitizedOut string) bool {
	return candidate.strategy != surgetypes.StrategyHLS &&
		!candidate.extracted && !candidate.protected && sanitizedOut != "" && candidate.sizeBytes > 0
}

func buildCandidateAddURIOptions(dir, out string, headers []string, split int, minSplitSize int64, beforeSave rpc.AddURIHook, candidate addTaskCandidate) rpc.AddURIOptions {
	opts := rpc.AddURIOptions{
		Dir:          dir,
		Out:          out,
		Headers:      headers,
		Split:        split,
		MinSplitSize: minSplitSize,
		BeforeSave:   beforeSave,
	}
	if candidate.strategy == surgetypes.StrategyHLS {
		// HLS never enters the size-based engine probe or payload-first
		// fallback; the resume checkpoint carries the plan.
		opts.Strategy = surgetypes.StrategyHLS
		opts.FileSize = 0
		opts.SkipServerProbe = true
		opts.RangeAcquisitionMode = surgetypes.RangeAcquireProbeAtEnqueue
		opts.HLSResume = candidate.hlsResume.Clone()
		opts.IsExplicitCategory = candidate.downloadGroup != nil
		return opts
	}
	if shouldSkipEngineProbe(candidate, out) {
		opts.FileSize = candidate.sizeBytes
		opts.RangeAcquisitionMode = surgetypes.RangeAcquirePayloadFirstUnknown
		opts.SkipServerProbe = true
	}
	return opts
}

func (s *Service) addTaskCandidate(ctx context.Context, candidate addTaskCandidate, authState *addTaskAuthBatchState, ledger *smartthread.BandwidthLedger) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalizeHLSCandidate(&candidate)
	isHLS := candidate.strategy == surgetypes.StrategyHLS
	if isHLS {
		// Capability gate first: no manifest preflight, no group directory,
		// and above all no Aria2 preparation or ledger reservation.
		if !supportsHLSStrategy(s.Engine) {
			return "", ErrHLSEngineUnsupported
		}
		// An explicit output name keeps the same safety contract as the
		// byte-range path — traversal/separator/drive errors are hard fails.
		if trimmedOut := strings.TrimSpace(candidate.out); trimmedOut != "" {
			if _, err := SafeAria2OutFilename(trimmedOut); err != nil && !isRecoverableOutFilenameError(err) {
				return "", err
			}
		}
	}

	rawURL := candidate.url
	if rawURL == "" {
		rawURL = candidate.sourceURL
	}
	var out string
	var err error
	if !isHLS {
		out, err = resolveAria2OutFilename(candidate.out, rawURL)
		if err != nil {
			return "", err
		}
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

	var preflightTTFBMs int64
	if isHLS {
		if candidate.hlsResume == nil {
			preflightRes, pErr := hlsPreflightManifest(ctx, nil, rawURL,
				headerLinesToMap(headers), s.surgeRuntimeConfigForPreflight())
			switch {
			case pErr == nil && preflightRes != nil && preflightRes.ResumeState != nil:
				candidate.hlsResume = preflightRes.ResumeState
				preflightTTFBMs = preflightRes.TTFBMs
			case pErr == nil:
				candidate.hlsResume = hls.NewFallbackHLSResumeState(rawURL, candidate.finalURL)
			default:
				// Caller-cancelled context wins outright; permanent HLS gates
				// fail fast; everything else degrades to the fallback state.
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if hls.ShouldFailFastPreflight(pErr) {
					return "", pErr
				}
				candidate.hlsResume = hls.NewFallbackHLSResumeState(rawURL, candidate.finalURL)
			}
		}
		// Naming prefers the URL that actually looks like a manifest — a
		// redirected finalURL wins over a generic launcher page path.
		namingURL := rawURL
		if !isHLSManifestURL(rawURL) && isHLSManifestURL(candidate.finalURL) {
			namingURL = candidate.finalURL
		}
		var containerFormat string
		if candidate.hlsResume != nil {
			containerFormat = candidate.hlsResume.ContainerFormat
		}
		out, err = SafeAria2OutFilename(hls.ResolveHLSOutputFilename(namingURL,
			candidate.out, candidate.pageTitle, containerFormat, time.Now()))
		if err != nil {
			return "", err
		}
	}

	dir := config.Get().DownloadDir
	if candidate.downloadGroup != nil {
		if err := candidate.downloadGroup.EnsureDir(); err != nil {
			return "", err
		}
		group := candidate.downloadGroup.GroupCopy()
		dir = group.Dir
	}
	if candidate.downloadGroup != nil && !candidate.callerOwnsGroupCleanup {
		defer candidate.downloadGroup.CleanupIfUnused()
	}

	var gid string

	if config.Get().SmartThreadMode {
		fileSize := candidate.sizeBytes
		var ttfbMs int64
		var remoteIP string

		var scope, domain, envKey string
		if isHLS {
			// HLS classifies on the segment host (the real traffic
			// destination), never the manifest host.
			fileSize = 0
			ttfbMs = preflightTTFBMs
			classifyURL := candidateClassificationURL(candidate)
			remoteIP = resolveHostIP(classifyURL)
			scope, domain = scopeClassifier.ClassifyByURLAndIP(classifyURL, remoteIP)
			envKey = monitor.ComputeEnvKeyForDownload(classifyURL, remoteIP)
		} else {
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

			envKey = monitor.ComputeEnvKeyForDownload(candidate.url, remoteIP)
		}

		maxConn, _ := strconv.Atoi(config.Get().MaxConnections)
		if maxConn <= 0 {
			maxConn = 8
		}

		// Surge admission authorizes real slots through scheduler.Admit; the
		// add request itself only carries the manual ceiling and a safe chunk
		// floor — no Calculate/Clamp/ledger claims here.
		surgeOpts := buildCandidateAddURIOptions(
			dir, out, headers, maxConn, 0, registerGroup, candidate,
		)

		var prepared bool
		var params smartthread.ThreadParams
		// The Aria2 budget is prepared lazily — exactly once, and only when
		// the engine actually routes to the daemon. A failed daemon add rolls
		// back only this batch ledger reserve.
		prepareAria2 := func() rpc.AddURIOptions {
			prepared = true
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
			return buildCandidateAddURIOptions(
				dir, out, headers, params.Split, params.MinSize, registerGroup, candidate,
			)
		}

		var err error
		switch eng := s.Engine.(type) {
		case *rpc.HybridEngine:
			gid, err = eng.AddUriWithAria2Preparation(candidate.url, surgeOpts, prepareAria2)
		case *rpc.SurgeEngine:
			gid, err = eng.AddUri(candidate.url, surgeOpts)
		default:
			if isHLS {
				// Defense in depth: the top-level gate already rejected
				// unsupported engines — never let an unrecognized engine
				// reach prepareAria2 for HLS.
				return "", ErrHLSEngineUnsupported
			}
			gid, err = s.Engine.AddUri(candidate.url, prepareAria2())
		}
		if err != nil {
			if prepared {
				ledger.Release(scope, envKey, params.TargetBandwidth)
				ledger.ReleaseByDomain(scope, domain, params.TargetBandwidth)
				ledger.ReleaseWorkers(scope, domain, params.Split)
			}
			return "", err
		}

		if gid != "" {
			tracker := monitor.State.GetTracker()
			if tracker != nil {
				tracker.MarkAdded(gid)
			}
			if strings.HasPrefix(gid, "ar_") && params.Split > 0 {
				// Real daemon budget: record the actual Aria2 plan. sg_ rows
				// carry no add-time claims — admission reservations own them.
				if tracker != nil {
					tracker.SetThreadInfo(gid, params.Split, params.IsExploration)
					// Set IsKeepAlive when initial split < nSat
					tracker.SetKeepAlive(gid, params.Split < params.NSat)
					tracker.SetMinChunk(gid, params.MinSize)
					tracker.SetTargetBandwidth(gid, params.TargetBandwidth)
				}
			}
			// Last metadata settle: scope/env write completes admission
			// readiness before the controller evaluates this opportunity.
			if tracker != nil {
				tracker.SetScopeAndEnv(gid, scope, ttfbMs, domain, envKey)
			}
			kickAdmissionIfSurge(gid)
		}
	} else {
		if isHLS && !supportsHLSStrategy(s.Engine) {
			return "", ErrHLSEngineUnsupported
		}
		maxConn, _ := strconv.Atoi(config.Get().MaxConnections)
		if maxConn <= 0 {
			maxConn = 8
		}
		var err error
		gid, err = s.Engine.AddUri(candidate.url, buildCandidateAddURIOptions(
			dir, out, headers, maxConn, 0, registerGroup, candidate,
		))
		if err != nil {
			return "", err
		}
		if gid != "" {
			tracker := monitor.State.GetTracker()
			if tracker != nil {
				tracker.MarkAdded(gid)
				if strings.HasPrefix(gid, "ar_") {
					tracker.SetThreadInfo(gid, maxConn, false)
				}
			}
			var scope, domain, envKey string
			if isHLS {
				classifyURL := candidateClassificationURL(candidate)
				finalIP := resolveHostIP(classifyURL)
				scope, domain = scopeClassifier.ClassifyByURLAndIP(classifyURL, finalIP)
				envKey = monitor.ComputeEnvKeyForDownload(classifyURL, finalIP)
			} else {
				classificationURL := candidate.finalURL
				if classificationURL == "" {
					classificationURL = candidate.url
				}
				finalIP := resolveHostIP(classificationURL)
				scope, domain = scopeClassifier.ClassifyByURLAndIP(candidate.url, finalIP)
				envKey = monitor.ComputeEnvKeyForDownload(candidate.url, finalIP)
			}
			if tracker != nil {
				tracker.SetScopeAndEnv(gid, scope, 0, domain, envKey)
			}
			kickAdmissionIfSurge(gid)
		}
	}

	if candidate.downloadGroup != nil {
		candidate.downloadGroup.RecordSuccess()
	}

	return gid, nil
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

// resolveHostIPTimeout bounds the caller-side wait of the add-path DNS
// lookup. The deadline only limits this caller: it does not claim to kill
// any resolver internals the OS may keep running.
const resolveHostIPTimeout = 2 * time.Second

// resolveHostIP extracts the host from url and does a DNS lookup.
// Returns "" on failure (caller falls back to proxy envKey).
func resolveHostIP(rawurl string) string {
	return resolveHostIPWithLookup(rawurl, resolveHostIPTimeout, net.DefaultResolver.LookupHost)
}

// resolveHostIPWithLookup is the seam behind resolveHostIP: the same
// URL/host/literal branches with an injectable context-aware lookup so the
// deadline behavior can be exercised without real DNS.
func resolveHostIPWithLookup(rawurl string, timeout time.Duration, lookup func(context.Context, string) ([]string, error)) string {
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := lookup(ctx, host)
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

// canonicalHLSPermanentError unwraps a (possibly annotated) error to its bare
// HLS sentinel so manifest URLs, query parameters, or credentials embedded in
// the original message can never reach the frontend or logs.
func canonicalHLSPermanentError(err error) (error, bool) {
	switch {
	case errors.Is(err, surgetypes.ErrHLSLiveNotSupported):
		return surgetypes.ErrHLSLiveNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSDRMNotSupported):
		return surgetypes.ErrHLSDRMNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSDemuxedAudioNotSupported):
		return surgetypes.ErrHLSDemuxedAudioNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSDiscontinuityMapNotSupported):
		return surgetypes.ErrHLSDiscontinuityMapNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSIFramesOnlyNotSupported):
		return surgetypes.ErrHLSIFramesOnlyNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSInvalidManifest):
		return surgetypes.ErrHLSInvalidManifest, true
	case errors.Is(err, surgetypes.ErrHLSInvalidSegmentResponse):
		return surgetypes.ErrHLSInvalidSegmentResponse, true
	case errors.Is(err, surgetypes.ErrHLSNotImplemented):
		return surgetypes.ErrHLSNotImplemented, true
	case errors.Is(err, ErrHLSEngineUnsupported):
		return ErrHLSEngineUnsupported, true
	}
	return nil, false
}

func (s *Service) redactAddTaskError(err error) string {
	if err == nil {
		return ""
	}
	if surgetypes.IsInsufficientDiskSpace(err) {
		return surgetypes.ErrInsufficientDiskSpace.Error()
	}
	if sentinel, ok := canonicalHLSPermanentError(err); ok {
		return sentinel.Error()
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
