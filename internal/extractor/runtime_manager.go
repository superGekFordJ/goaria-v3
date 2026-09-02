package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
)

type RuntimeSourceKind string

const (
	RuntimeSourceKindLocalZip       RuntimeSourceKind = "local_zip"
	RuntimeSourceKindLocalDirectory RuntimeSourceKind = "local_directory"
	RuntimeSourceKindRemoteLock     RuntimeSourceKind = "remote_lock"
)

type RuntimeSourceStatus string

const (
	RuntimeSourceStatusReady       RuntimeSourceStatus = "ready"
	RuntimeSourceStatusUnavailable RuntimeSourceStatus = "unavailable"
)

type RuntimeSourceSpec struct {
	Kind    RuntimeSourceKind
	Locator string
}

type RuntimeSourceState struct {
	SourceID          string              `json:"source_id"`
	Kind              RuntimeSourceKind   `json:"kind"`
	DisplayName       string              `json:"display_name"`
	PackID            string              `json:"pack_id"`
	PackVersion       string              `json:"pack_version"`
	SignerFingerprint string              `json:"signer_fingerprint"`
	Status            RuntimeSourceStatus `json:"status"`
	ErrorCode         string              `json:"error_code,omitempty"`
}

type RuntimeManagerErrorCode string

const (
	RuntimeManagerErrorInvalidSourceKind      RuntimeManagerErrorCode = "invalid_source_kind"
	RuntimeManagerErrorInvalidSourceID        RuntimeManagerErrorCode = "invalid_source_id"
	RuntimeManagerErrorInvalidSourceSpec      RuntimeManagerErrorCode = "invalid_source_spec"
	RuntimeManagerErrorSourceLimitReached     RuntimeManagerErrorCode = "source_limit_reached"
	RuntimeManagerErrorPackIDConflict         RuntimeManagerErrorCode = "pack_id_conflict"
	RuntimeManagerErrorSignerChanged          RuntimeManagerErrorCode = "signer_changed"
	RuntimeManagerErrorPackIdentityChanged    RuntimeManagerErrorCode = "pack_identity_changed"
	RuntimeManagerErrorPolicyUnavailable      RuntimeManagerErrorCode = "policy_unavailable"
	RuntimeManagerErrorAuthRuntimeUnavailable RuntimeManagerErrorCode = "auth_runtime_unavailable"
	RuntimeManagerErrorPersistFailed          RuntimeManagerErrorCode = "persist_failed"
	RuntimeManagerErrorConcurrentChange       RuntimeManagerErrorCode = "concurrent_change"
	RuntimeManagerErrorStateInvalid           RuntimeManagerErrorCode = "state_invalid"
	RuntimeManagerErrorCancelled              RuntimeManagerErrorCode = "cancelled"
)

type RuntimeManagerError struct {
	Code RuntimeManagerErrorCode
	err  error
}

func (e *RuntimeManagerError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("runtime manager error: %s", e.Code)
}

func (e *RuntimeManagerError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newRuntimeManagerError(code RuntimeManagerErrorCode, cause error) error {
	return &RuntimeManagerError{Code: code, err: cause}
}

type RuntimeSnapshot struct {
	revision       uint64
	registry       *Registry
	dispatcher     *AddTaskDispatcher
	tasksAdapter   *TasksAdapter
	ingressDigests *IngressDigestSource
}

func (s *RuntimeSnapshot) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}

func (s *RuntimeSnapshot) Registry() *Registry {
	if s == nil {
		return nil
	}
	return s.registry
}

func (s *RuntimeSnapshot) Dispatcher() *AddTaskDispatcher {
	if s == nil {
		return nil
	}
	return s.dispatcher
}

func (s *RuntimeSnapshot) TasksAdapter() *TasksAdapter {
	if s == nil {
		return nil
	}
	return s.tasksAdapter
}

func (s *RuntimeSnapshot) IngressDigests() *IngressDigestSource {
	if s == nil {
		return nil
	}
	return s.ingressDigests
}

type ExtractorRuntimeManagerConfig struct {
	DataRoot           string
	EmbeddedPacks      []EmbeddedPack
	TrustPolicy        TrustPolicy
	HostPolicyResolver HostPolicyResolver
	AuthResolver       AuthProfileResolver
	HeaderResolver     HeaderProfileResolver
	HostAuthRuntime    *HostAuthRuntime
}

type internalSourceRecord struct {
	sourceID          string
	kind              RuntimeSourceKind
	locator           string
	packID            string
	packVersion       string
	signerFingerprint string
	cacheGeneration   string
	status            RuntimeSourceStatus
	errorCode         string
	pack              *VerifiedPack
}

func (r *internalSourceRecord) toSafeState() RuntimeSourceState {
	return RuntimeSourceState{
		SourceID:          r.sourceID,
		Kind:              r.kind,
		DisplayName:       safeDisplayName(r.kind, r.locator),
		PackID:            r.packID,
		PackVersion:       r.packVersion,
		SignerFingerprint: r.signerFingerprint,
		Status:            r.status,
		ErrorCode:         r.errorCode,
	}
}

func safeDisplayName(kind RuntimeSourceKind, locator string) string {
	switch kind {
	case RuntimeSourceKindLocalZip, RuntimeSourceKindLocalDirectory:
		base := filepath.Base(locator)
		clean := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || !unicode.IsPrint(r) {
				return -1
			}
			return r
		}, base)
		clean = strings.TrimSpace(clean)
		if clean == "" || clean == "." || clean == "/" || clean == "\\" {
			return "local"
		}
		if len(clean) > 64 {
			clean = clean[:64]
		}
		return clean
	case RuntimeSourceKindRemoteLock:
		u, err := url.Parse(locator)
		if err != nil || u.Hostname() == "" {
			return "remote"
		}
		return strings.ToLower(u.Hostname())
	default:
		return "unknown"
	}
}

type managerState struct {
	snapshot       *RuntimeSnapshot
	sources        []internalSourceRecord
	recoveryErrors []string
	stateWritable  bool
}

type ExtractorRuntimeManager struct {
	writeMu       sync.Mutex
	current       atomic.Pointer[managerState]
	config        ExtractorRuntimeManagerConfig
	embeddedPacks []VerifiedPack
	store         *runtimeStore

	testLoaderOverride func(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error)
	testPreCommitHook  func()
}

func NewExtractorRuntimeManager(ctx context.Context, config ExtractorRuntimeManagerConfig) (*ExtractorRuntimeManager, error) {
	if config.DataRoot == "" {
		return nil, errors.New("data root is empty")
	}
	if !filepath.IsAbs(config.DataRoot) {
		return nil, errors.New("data root must be an absolute path")
	}
	if filepath.Clean(config.DataRoot) != config.DataRoot {
		return nil, errors.New("data root must be a clean path")
	}

	if config.AuthResolver == nil && config.HostAuthRuntime != nil {
		config.AuthResolver = config.HostAuthRuntime
	}
	if len(config.TrustPolicy.TrustedPublicKeys) == 0 {
		config.TrustPolicy = DefaultTrustPolicy()
	}

	var verifiedEmbedded []VerifiedPack
	seenEmbeddedIDs := make(map[string]struct{}, len(config.EmbeddedPacks))
	for _, ep := range config.EmbeddedPacks {
		vp, err := VerifyEmbeddedPack(ep, config.TrustPolicy)
		if err != nil {
			return nil, fmt.Errorf("verify embedded pack: %w", err)
		}
		if _, exists := seenEmbeddedIDs[vp.Identity.PackID]; exists {
			return nil, fmt.Errorf("duplicate embedded pack id: %s", vp.Identity.PackID)
		}
		seenEmbeddedIDs[vp.Identity.PackID] = struct{}{}
		verifiedEmbedded = append(verifiedEmbedded, cloneVerifiedPack(vp))
	}

	store := newRuntimeStore(config.DataRoot)
	rawSources, indexExists, err := store.readIndex()

	var recoveryErrors []string
	stateWritable := true
	var sourceRecords []internalSourceRecord

	if err != nil {
		stateWritable = false
		recoveryErrors = []string{string(RuntimeManagerErrorStateInvalid)}
	} else if indexExists {
		for _, raw := range rawSources {
			record := internalSourceRecord{
				sourceID:          raw.SourceID,
				kind:              raw.Kind,
				locator:           raw.Locator,
				packID:            raw.PackID,
				packVersion:       raw.PackVersion,
				signerFingerprint: raw.SignerFingerprint,
				cacheGeneration:   raw.CacheGeneration,
				status:            RuntimeSourceStatusUnavailable,
			}

			if _, collides := seenEmbeddedIDs[raw.PackID]; collides {
				record.errorCode = string(RuntimeManagerErrorPackIDConflict)
				sourceRecords = append(sourceRecords, record)
				continue
			}

			candidate, err := store.readCachedCandidate(ctx, raw.PackID, raw.CacheGeneration, raw.Kind)
			if err != nil {
				if loadErr, ok := errors.AsType[*RuntimePackLoadError](err); ok {
					record.errorCode = string(loadErr.Code)
				} else {
					record.errorCode = string(RuntimePackLoadErrorSourceUnreadable)
				}
				sourceRecords = append(sourceRecords, record)
				continue
			}

			vp := candidate.VerifiedPack
			if vp.Manifest.PackID != raw.PackID || vp.Manifest.PackVersion != raw.PackVersion {
				record.errorCode = string(RuntimeManagerErrorPackIdentityChanged)
				sourceRecords = append(sourceRecords, record)
				continue
			}
			if vp.Identity.PublicKeySHA256 != raw.SignerFingerprint {
				record.errorCode = string(RuntimeManagerErrorSignerChanged)
				sourceRecords = append(sourceRecords, record)
				continue
			}

			if errCode, ok := checkRuntimeDependencies(ctx, vp, config); !ok {
				record.errorCode = errCode
				sourceRecords = append(sourceRecords, record)
				continue
			}

			record.status = RuntimeSourceStatusReady
			record.errorCode = ""
			clonedPack := cloneVerifiedPack(vp)
			record.pack = &clonedPack
			sourceRecords = append(sourceRecords, record)
		}
	}

	manager := &ExtractorRuntimeManager{
		config:        config,
		embeddedPacks: verifiedEmbedded,
		store:         store,
	}

	orderedPacks := manager.collectPacks(sourceRecords)
	initialSnapshot, err := buildSnapshot(1, orderedPacks, config)
	if err != nil {
		return nil, fmt.Errorf("build initial snapshot: %w", err)
	}

	initialState := &managerState{
		snapshot:       initialSnapshot,
		sources:        sourceRecords,
		recoveryErrors: recoveryErrors,
		stateWritable:  stateWritable,
	}
	manager.current.Store(initialState)

	return manager, nil
}

func checkRuntimeDependencies(ctx context.Context, pack VerifiedPack, config ExtractorRuntimeManagerConfig) (string, bool) {
	if isAliasManifest(pack.Manifest) {
		if config.HostPolicyResolver == nil {
			return string(RuntimeManagerErrorPolicyUnavailable), false
		}
		if _, err := resolveAliasHostPolicy(ctx, config.HostPolicyResolver, pack.Identity, pack.Manifest); err != nil {
			return string(RuntimeManagerErrorPolicyUnavailable), false
		}
	}

	if ManifestHasCapability(pack.Manifest, CapabilityAuthProfile) {
		supported := false
		if config.HostAuthRuntime != nil && config.HostAuthRuntime.SupportsPackIdentity(pack.Identity) {
			supported = true
		} else if aware, ok := config.AuthResolver.(interface {
			SupportsPackIdentity(VerifiedPackIdentity) bool
		}); ok && aware.SupportsPackIdentity(pack.Identity) {
			supported = true
		}
		if !supported {
			return string(RuntimeManagerErrorAuthRuntimeUnavailable), false
		}
	}

	return "", true
}

func buildSnapshot(revision uint64, packs []VerifiedPack, config ExtractorRuntimeManagerConfig) (*RuntimeSnapshot, error) {
	reg, err := NewRegistryFromVerifiedPacks(packs, config.HostPolicyResolver)
	if err != nil {
		return nil, fmt.Errorf("build registry: %w", err)
	}

	if len(packs) == 0 {
		return &RuntimeSnapshot{
			revision: revision,
			registry: reg,
		}, nil
	}

	authResolver := config.AuthResolver
	if authResolver == nil && config.HostAuthRuntime != nil {
		authResolver = config.HostAuthRuntime
	}

	runner := NewRunnerWithConfig(RunnerConfig{
		HTTPBroker: NewHTTPBroker(HTTPBrokerConfig{
			AuthResolver:       authResolver,
			HostPolicyResolver: config.HostPolicyResolver,
		}),
		AuthResolver:       authResolver,
		HostPolicyResolver: config.HostPolicyResolver,
	})

	dispatcher := NewAddTaskDispatcher(AddTaskDispatcherConfig{
		Registry:       reg,
		Runner:         runner,
		AuthResolver:   authResolver,
		HeaderResolver: config.HeaderResolver,
	})

	tasksAdapter := NewTasksAdapter(dispatcher, config.HostAuthRuntime)
	ingressDigests := NewIngressDigestSource(reg)

	return &RuntimeSnapshot{
		revision:       revision,
		registry:       reg,
		dispatcher:     dispatcher,
		tasksAdapter:   tasksAdapter,
		ingressDigests: ingressDigests,
	}, nil
}

func (m *ExtractorRuntimeManager) collectPacks(userSources []internalSourceRecord) []VerifiedPack {
	orderedPacks := make([]VerifiedPack, 0, len(m.embeddedPacks)+len(userSources))
	for _, ep := range m.embeddedPacks {
		orderedPacks = append(orderedPacks, cloneVerifiedPack(ep))
	}
	for _, sr := range userSources {
		if sr.status == RuntimeSourceStatusReady && sr.pack != nil {
			orderedPacks = append(orderedPacks, cloneVerifiedPack(*sr.pack))
		}
	}
	return orderedPacks
}

func (m *ExtractorRuntimeManager) loadCandidate(ctx context.Context, spec RuntimeSourceSpec) (RuntimePackCandidate, error) {
	if m.testLoaderOverride != nil {
		return m.testLoaderOverride(ctx, spec)
	}

	switch spec.Kind {
	case RuntimeSourceKindLocalZip:
		return LoadLocalPackZip(ctx, spec.Locator)
	case RuntimeSourceKindLocalDirectory:
		return LoadLocalPackDirectory(ctx, spec.Locator)
	case RuntimeSourceKindRemoteLock:
		return LoadRemotePackLock(ctx, spec.Locator)
	default:
		return RuntimePackCandidate{}, newRuntimeManagerError(RuntimeManagerErrorInvalidSourceKind, fmt.Errorf("unknown source kind: %s", spec.Kind))
	}
}

func (m *ExtractorRuntimeManager) CurrentSnapshot() *RuntimeSnapshot {
	return m.current.Load().snapshot
}

func (m *ExtractorRuntimeManager) ListSources() []RuntimeSourceState {
	curr := m.current.Load()
	if len(curr.sources) == 0 {
		return []RuntimeSourceState{}
	}
	res := make([]RuntimeSourceState, len(curr.sources))
	for i, s := range curr.sources {
		res[i] = s.toSafeState()
	}
	return res
}

func (m *ExtractorRuntimeManager) RecoveryErrors() []string {
	curr := m.current.Load()
	if len(curr.recoveryErrors) == 0 {
		return []string{}
	}
	res := make([]string, len(curr.recoveryErrors))
	copy(res, curr.recoveryErrors)
	return res
}

func (m *ExtractorRuntimeManager) LoadSource(ctx context.Context, spec RuntimeSourceSpec) (RuntimeSourceState, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	curr := m.current.Load()
	if !curr.stateWritable {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorStateInvalid, nil)
	}
	if len(curr.sources) >= maxSourceCount {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorSourceLimitReached, nil)
	}

	switch spec.Kind {
	case RuntimeSourceKindLocalZip, RuntimeSourceKindLocalDirectory, RuntimeSourceKindRemoteLock:
	default:
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorInvalidSourceKind, fmt.Errorf("unknown source kind: %s", spec.Kind))
	}

	if err := validateDurableLocator(spec.Kind, spec.Locator); err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorInvalidSourceSpec, err)
	}

	if err := ctx.Err(); err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorCancelled, err)
	}

	candidate, err := m.loadCandidate(ctx, spec)
	if err != nil {
		if ctx.Err() != nil {
			return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorCancelled, ctx.Err())
		}
		return RuntimeSourceState{}, err
	}

	if err := ctx.Err(); err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorCancelled, err)
	}

	if m.testPreCommitHook != nil {
		m.testPreCommitHook()
	}

	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	curr = m.current.Load()
	if !curr.stateWritable {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorStateInvalid, nil)
	}
	if len(curr.sources) >= maxSourceCount {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorSourceLimitReached, nil)
	}

	candidatePackID := candidate.VerifiedPack.Manifest.PackID
	for _, ep := range m.embeddedPacks {
		if ep.Identity.PackID == candidatePackID {
			return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPackIDConflict, fmt.Errorf("pack id %s conflicts with embedded", candidatePackID))
		}
	}
	for _, sr := range curr.sources {
		if sr.packID == candidatePackID {
			return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPackIDConflict, fmt.Errorf("pack id %s conflicts with user source %s", candidatePackID, sr.sourceID))
		}
	}

	if errCode, ok := checkRuntimeDependencies(ctx, candidate.VerifiedPack, m.config); !ok {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorCode(errCode), nil)
	}

	sourceID, err := generateRandomHex(16)
	if err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}
	cacheGen, err := generateRandomHex(16)
	if err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	newSources := make([]internalSourceRecord, len(curr.sources), len(curr.sources)+1)
	copy(newSources, curr.sources)

	newPack := cloneVerifiedPack(candidate.VerifiedPack)
	newRecord := internalSourceRecord{
		sourceID:          sourceID,
		kind:              spec.Kind,
		locator:           spec.Locator,
		packID:            candidatePackID,
		packVersion:       candidate.VerifiedPack.Manifest.PackVersion,
		signerFingerprint: candidate.VerifiedPack.Identity.PublicKeySHA256,
		cacheGeneration:   cacheGen,
		status:            RuntimeSourceStatusReady,
		pack:              &newPack,
	}
	newSources = append(newSources, newRecord)

	nextSnapshotPacks := m.collectPacks(newSources)
	nextSnapshot, err := buildSnapshot(curr.snapshot.revision+1, nextSnapshotPacks, m.config)
	if err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	_, err = m.store.writeCandidateToStaging(cacheGen, candidate)
	if err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	err = m.store.finalizeCandidateGeneration(candidatePackID, cacheGen)
	if err != nil {
		_ = os.RemoveAll(filepath.Join(m.store.stagingDir(), cacheGen))
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	indexRows := make([]runtimeIndexSource, len(newSources))
	for i, s := range newSources {
		indexRows[i] = runtimeIndexSource{
			SourceID:          s.sourceID,
			Kind:              s.kind,
			Locator:           s.locator,
			PackID:            s.packID,
			PackVersion:       s.packVersion,
			SignerFingerprint: s.signerFingerprint,
			CacheGeneration:   s.cacheGeneration,
		}
	}

	if err := m.store.replaceIndex(indexRows); err != nil {
		_ = m.store.deleteGeneration(candidatePackID, cacheGen)
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	nextState := &managerState{
		snapshot:       nextSnapshot,
		sources:        newSources,
		recoveryErrors: curr.recoveryErrors,
		stateWritable:  true,
	}
	m.current.Store(nextState)

	return newRecord.toSafeState(), nil
}

func (m *ExtractorRuntimeManager) ReloadSource(ctx context.Context, sourceID string) (RuntimeSourceState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateLowerHex32Field("source_id", sourceID); err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorInvalidSourceID, err)
	}

	curr := m.current.Load()
	if !curr.stateWritable {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorStateInvalid, nil)
	}

	var captured internalSourceRecord
	found := false
	for _, s := range curr.sources {
		if s.sourceID == sourceID {
			captured = s
			found = true
			break
		}
	}
	if !found {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorInvalidSourceID, fmt.Errorf("source %s not found", sourceID))
	}

	if err := ctx.Err(); err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorCancelled, err)
	}

	candidate, err := m.loadCandidate(ctx, RuntimeSourceSpec{Kind: captured.kind, Locator: captured.locator})
	if err != nil {
		if ctx.Err() != nil {
			return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorCancelled, ctx.Err())
		}
		return RuntimeSourceState{}, err
	}

	if err := ctx.Err(); err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorCancelled, err)
	}

	if m.testPreCommitHook != nil {
		m.testPreCommitHook()
	}

	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	curr = m.current.Load()
	if !curr.stateWritable {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorStateInvalid, nil)
	}

	targetIdx := -1
	for i, s := range curr.sources {
		if s.sourceID == sourceID {
			targetIdx = i
			break
		}
	}
	if targetIdx == -1 {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorConcurrentChange, errors.New("source removed during reload"))
	}
	currentRec := curr.sources[targetIdx]
	if currentRec.cacheGeneration != captured.cacheGeneration || currentRec.packID != captured.packID ||
		currentRec.signerFingerprint != captured.signerFingerprint || currentRec.locator != captured.locator {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorConcurrentChange, errors.New("source changed concurrently"))
	}

	if candidate.VerifiedPack.Manifest.PackID != captured.packID {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPackIdentityChanged, errors.New("pack id changed in reload"))
	}
	if candidate.VerifiedPack.Identity.PublicKeySHA256 != captured.signerFingerprint {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorSignerChanged, errors.New("signer changed in reload"))
	}

	if errCode, ok := checkRuntimeDependencies(ctx, candidate.VerifiedPack, m.config); !ok {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorCode(errCode), nil)
	}

	newGen, err := generateRandomHex(16)
	if err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	newSources := make([]internalSourceRecord, len(curr.sources))
	copy(newSources, curr.sources)

	newPack := cloneVerifiedPack(candidate.VerifiedPack)
	updatedRecord := internalSourceRecord{
		sourceID:          captured.sourceID,
		kind:              captured.kind,
		locator:           captured.locator,
		packID:            captured.packID,
		packVersion:       candidate.VerifiedPack.Manifest.PackVersion,
		signerFingerprint: captured.signerFingerprint,
		cacheGeneration:   newGen,
		status:            RuntimeSourceStatusReady,
		pack:              &newPack,
	}
	newSources[targetIdx] = updatedRecord

	nextSnapshotPacks := m.collectPacks(newSources)
	nextSnapshot, err := buildSnapshot(curr.snapshot.revision+1, nextSnapshotPacks, m.config)
	if err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	_, err = m.store.writeCandidateToStaging(newGen, candidate)
	if err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	err = m.store.finalizeCandidateGeneration(captured.packID, newGen)
	if err != nil {
		_ = os.RemoveAll(filepath.Join(m.store.stagingDir(), newGen))
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	indexRows := make([]runtimeIndexSource, len(newSources))
	for i, s := range newSources {
		indexRows[i] = runtimeIndexSource{
			SourceID:          s.sourceID,
			Kind:              s.kind,
			Locator:           s.locator,
			PackID:            s.packID,
			PackVersion:       s.packVersion,
			SignerFingerprint: s.signerFingerprint,
			CacheGeneration:   s.cacheGeneration,
		}
	}

	if err := m.store.replaceIndex(indexRows); err != nil {
		_ = m.store.deleteGeneration(captured.packID, newGen)
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	nextState := &managerState{
		snapshot:       nextSnapshot,
		sources:        newSources,
		recoveryErrors: curr.recoveryErrors,
		stateWritable:  true,
	}
	m.current.Store(nextState)

	// Best-effort post-publication cleanup of exact superseded generation
	go func(pID, oldGen string) {
		_ = m.store.deleteGeneration(pID, oldGen)
	}(captured.packID, captured.cacheGeneration)

	return updatedRecord.toSafeState(), nil
}

func (m *ExtractorRuntimeManager) RemoveSource(ctx context.Context, sourceID string) (RuntimeSourceState, error) {
	if err := validateLowerHex32Field("source_id", sourceID); err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorInvalidSourceID, err)
	}

	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	curr := m.current.Load()
	if !curr.stateWritable {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorStateInvalid, nil)
	}

	targetIdx := -1
	for i, s := range curr.sources {
		if s.sourceID == sourceID {
			targetIdx = i
			break
		}
	}
	if targetIdx == -1 {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorInvalidSourceID, fmt.Errorf("source %s not found", sourceID))
	}

	targetRec := curr.sources[targetIdx]

	newSources := make([]internalSourceRecord, 0, len(curr.sources)-1)
	for i, s := range curr.sources {
		if i != targetIdx {
			newSources = append(newSources, s)
		}
	}

	nextSnapshotPacks := m.collectPacks(newSources)
	nextSnapshot, err := buildSnapshot(curr.snapshot.revision+1, nextSnapshotPacks, m.config)
	if err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	indexRows := make([]runtimeIndexSource, len(newSources))
	for i, s := range newSources {
		indexRows[i] = runtimeIndexSource{
			SourceID:          s.sourceID,
			Kind:              s.kind,
			Locator:           s.locator,
			PackID:            s.packID,
			PackVersion:       s.packVersion,
			SignerFingerprint: s.signerFingerprint,
			CacheGeneration:   s.cacheGeneration,
		}
	}

	if err := m.store.replaceIndex(indexRows); err != nil {
		return RuntimeSourceState{}, newRuntimeManagerError(RuntimeManagerErrorPersistFailed, err)
	}

	nextState := &managerState{
		snapshot:       nextSnapshot,
		sources:        newSources,
		recoveryErrors: curr.recoveryErrors,
		stateWritable:  true,
	}
	m.current.Store(nextState)

	// Post-cleanup
	go func(pID, oldGen string) {
		_ = m.store.deleteGeneration(pID, oldGen)
	}(targetRec.packID, targetRec.cacheGeneration)

	return targetRec.toSafeState(), nil
}
