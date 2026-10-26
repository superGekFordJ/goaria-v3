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
	"goaria-v3/internal/surge/strategy/hls"
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

func isDuplicateAddCandidate(candidate addTaskCandidate, existingURLs map[string]bool, historyDuplicates map[string]bool, candidateSeen map[string]bool) bool {
	if existingURLs[candidate.url] || candidateSeen[candidate.url] {
		return true
	}
	if historyDuplicates != nil && historyDuplicates[candidate.url] {
		return true
	}

	return history.ContainsSource(candidate.url)
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
		// Isolation lives at the callee: SurgeEngine.AddUri clones the
		// checkpoint before first use, so this hands off by reference like
		// the other AddURIOptions fields.
		opts.HLSResume = candidate.hlsResume
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

func firstErrorString(errors map[string]string) string {
	for _, err := range errors {
		return err
	}

	return ""
}

// canonicalizeExtensionAddError normalizes an add failure crossing the
// extension boundary: handleDownload stringifies it into download_ack, so
// signed URLs or credentials wrapped inside the message must never leak.
// Known sentinels return as themselves — the wire text is identical and
// Go callers keep errors.Is; anything else collapses to a redacted string.
func (s *Service) canonicalizeExtensionAddError(err error) error {
	if err == nil {
		return nil
	}
	if surgetypes.IsInsufficientDiskSpace(err) {
		return surgetypes.ErrInsufficientDiskSpace
	}
	if sentinel, ok := canonicalHLSPermanentError(err); ok {
		return sentinel
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(s.redactAddTaskError(err))
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
