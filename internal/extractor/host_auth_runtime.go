package extractor

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type HostAuthRuntimeConfig struct {
	Bundle             *PrivateAuthRuntimeBundle
	Store              AuthProfileStore
	Coordinator        *WebViewAuthCoordinator
	Materializer       AuthMaterializer
	HostPolicyResolver HostPolicyResolver
	Now                func() time.Time
}

type HostAuthRuntime struct {
	bundle             *PrivateAuthRuntimeBundle
	store              AuthProfileStore
	coordinator        *WebViewAuthCoordinator
	materializer       AuthMaterializer
	hostPolicyResolver HostPolicyResolver
	now                func() time.Time

	identityIndex map[VerifiedPackIdentity]PrivateAuthRuntimePack
	packIDIndex   map[string][]VerifiedPackIdentity
	profiles      map[VerifiedPackIdentity]map[AuthProfileID]PrivateAuthRuntimeProfile
	storeRefs     map[VerifiedPackIdentity]map[AuthProfileID]struct{}
	materialRefs  map[VerifiedPackIdentity]map[AuthProfileID]struct{}
	provisionRefs map[VerifiedPackIdentity]map[AuthProfileID]struct{}
}

type HostAuthRuntimeRequest struct {
	PackIdentity VerifiedPackIdentity
	Manifest     Manifest
	SourceURL    string
	TargetURL    string
	ProfileRef   AuthProfileID
}

type HostAuthRuntimeProfileStatus string

const (
	HostAuthRuntimeProfileAvailable   HostAuthRuntimeProfileStatus = "available"
	HostAuthRuntimeProfileMissing     HostAuthRuntimeProfileStatus = "missing"
	HostAuthRuntimeProfileExpired     HostAuthRuntimeProfileStatus = "expired"
	HostAuthRuntimeProfileUnavailable HostAuthRuntimeProfileStatus = "unavailable"
)

type HostAuthRuntimeResult struct {
	Matched         bool
	Required        bool
	Available       bool
	Refreshable     bool
	Provisioned     bool
	RefreshSkipped  bool
	PackID          string
	ProfileStatuses []HostAuthRuntimeProfileResult
	Message         string
}

type HostAuthRuntimeProfileResult struct {
	ProfileRef      AuthProfileID
	Kind            AuthSecretKind
	Status          HostAuthRuntimeProfileStatus
	Refreshable     bool
	RedactedDisplay string
	Snapshot        AuthProfileSnapshot
}

type hostAuthRuntimeBindOptions struct {
	strictProfileBinding bool
	requireTarget        bool
}

type hostAuthRuntimeBoundRequest struct {
	request      HostAuthRuntimeRequest
	pack         PrivateAuthRuntimePack
	selectedRefs []AuthProfileID
	targetHost   string
}

type hostAuthRuntimeProfileSnapshotClass string

const (
	hostAuthRuntimeProfileSnapshotAvailable           hostAuthRuntimeProfileSnapshotClass = "available"
	hostAuthRuntimeProfileSnapshotMissing             hostAuthRuntimeProfileSnapshotClass = "missing"
	hostAuthRuntimeProfileSnapshotExpired             hostAuthRuntimeProfileSnapshotClass = "expired"
	hostAuthRuntimeProfileSnapshotStaleKindMismatch   hostAuthRuntimeProfileSnapshotClass = "stale_kind_mismatch"
	hostAuthRuntimeProfileSnapshotStaleEmptyDomains   hostAuthRuntimeProfileSnapshotClass = "stale_empty_domains"
	hostAuthRuntimeProfileSnapshotStaleDomainMismatch hostAuthRuntimeProfileSnapshotClass = "stale_domain_mismatch"
)

func NewHostAuthRuntime(config HostAuthRuntimeConfig) *HostAuthRuntime {
	materializer := config.Materializer
	if materializer == nil {
		materializer = NewDefaultAuthMaterializer()
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}

	runtime := &HostAuthRuntime{
		bundle:             config.Bundle,
		store:              config.Store,
		coordinator:        config.Coordinator,
		materializer:       materializer,
		hostPolicyResolver: config.HostPolicyResolver,
		now:                now,
		identityIndex:      make(map[VerifiedPackIdentity]PrivateAuthRuntimePack),
		packIDIndex:        make(map[string][]VerifiedPackIdentity),
		profiles:           make(map[VerifiedPackIdentity]map[AuthProfileID]PrivateAuthRuntimeProfile),
		storeRefs:          make(map[VerifiedPackIdentity]map[AuthProfileID]struct{}),
		materialRefs:       make(map[VerifiedPackIdentity]map[AuthProfileID]struct{}),
		provisionRefs:      make(map[VerifiedPackIdentity]map[AuthProfileID]struct{}),
	}
	if config.Bundle == nil || config.Bundle.PackCount() == 0 {
		return runtime
	}

	for _, identity := range config.Bundle.PackIdentities() {
		pack, ok := config.Bundle.PackRuntime(identity)
		if !ok {
			continue
		}
		runtime.identityIndex[identity] = clonePrivateAuthRuntimePack(pack)
		runtime.packIDIndex[identity.PackID] = append(runtime.packIDIndex[identity.PackID], identity)
		runtime.profiles[identity] = hostAuthRuntimeProfileMap(pack.Profiles)
		runtime.storeRefs[identity] = hostAuthRuntimeProfileRefSet(pack.StoreBinding.ProfileRefs)
		runtime.materialRefs[identity] = hostAuthRuntimeProfileRefSet(pack.Materialization.ProfileRefs)
		runtime.provisionRefs[identity] = hostAuthRuntimeProfileRefSet(pack.Provisioning.ProfileRefs)
	}

	return runtime
}

func (r *HostAuthRuntime) Preflight(ctx context.Context, request HostAuthRuntimeRequest) (HostAuthRuntimeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	bound, result, done, err := r.bindRequest(request, hostAuthRuntimeBindOptions{})
	if done || err != nil {
		return cloneHostAuthRuntimeResult(result), err
	}

	result = HostAuthRuntimeResult{
		Matched:   true,
		Required:  hostAuthRuntimePreflightRequired(bound.pack),
		PackID:    bound.pack.PackIdentity.PackID,
		Available: true,
	}
	if r.store == nil {
		result.Available = !result.Required && bound.pack.Preflight.Mode == "optional"
		result.ProfileStatuses = hostAuthRuntimeUnavailableStatuses(bound)
		result.Message = hostAuthRuntimeProfileUnavailableMessage
		if bound.pack.Preflight.Mode == "optional" {
			result.Required = false
			result.Available = true
			result.Refreshable = false
			result.Message = ""
		}
		return cloneHostAuthRuntimeResult(result), nil
	}

	snapshots, err := r.store.AuthProfileSnapshots(ctx, bound.pack.PackIdentity.PackID)
	if err != nil {
		return HostAuthRuntimeResult{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	snapshotByProfile := make(map[AuthProfileID]AuthProfileSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		snapshotByProfile[snapshot.ProfileID] = cloneAuthProfileSnapshot(snapshot)
	}

	profiles := r.profiles[bound.pack.PackIdentity]
	statuses := make([]HostAuthRuntimeProfileResult, 0, len(bound.selectedRefs))
	allAvailable := true
	anyRefreshable := false
	for _, ref := range bound.selectedRefs {
		profile := profiles[ref]
		statusResult := HostAuthRuntimeProfileResult{
			ProfileRef: ref,
			Kind:       profile.Kind,
			Status:     HostAuthRuntimeProfileMissing,
		}
		class := hostAuthRuntimeProfileSnapshotMissing
		if snapshot, ok := snapshotByProfile[ref]; ok {
			statusResult.Snapshot = cloneAuthProfileSnapshot(snapshot)
			statusResult.RedactedDisplay = snapshot.RedactedDisplay
			class = hostAuthRuntimeClassifySnapshot(snapshot, profile, bound.targetHost, r.effectiveNow())
			statusResult.Status = class.status()
		}
		statusResult.Refreshable = r.profileRefreshable(ctx, bound, ref, class)
		if bound.pack.Preflight.Mode == "optional" {
			statusResult.Refreshable = false
		}
		if statusResult.Status != HostAuthRuntimeProfileAvailable {
			allAvailable = false
		}
		if statusResult.Refreshable {
			anyRefreshable = true
		}
		statuses = append(statuses, statusResult)
	}

	result.ProfileStatuses = statuses
	if bound.pack.Preflight.Mode == "optional" {
		result.Required = false
		result.Available = true
		result.Refreshable = false
	} else {
		result.Available = allAvailable
		result.Refreshable = anyRefreshable
	}
	if !result.Available {
		result.Message = hostAuthRuntimeProfileUnavailableMessage
	}

	return cloneHostAuthRuntimeResult(result), nil
}

func (r *HostAuthRuntime) ResolveAuthProfile(ctx context.Context, packID string, profileID AuthProfileID, rawURL string) (ResolvedAuthSecret, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validatePackID(packID); err != nil {
		return ResolvedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	if err := validateAuthProfileID(profileID); err != nil {
		return ResolvedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	if err := validateHostAuthRuntimeHTTPSURL(rawURL); err != nil {
		return ResolvedAuthSecret{}, err
	}
	if r == nil || len(r.identityIndex) == 0 || r.store == nil {
		return ResolvedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	identities := r.packIDIndex[packID]
	if len(identities) != 1 {
		return ResolvedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	identity := identities[0]
	profile, ok := r.boundMaterializedProfile(identity, profileID)
	if !ok {
		return ResolvedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}

	resolved, err := r.store.ResolveAuthProfile(ctx, packID, profileID, rawURL)
	if err != nil {
		return ResolvedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	material, err := r.effectiveMaterializer().MaterializeAuth(resolved)
	if err != nil || material.Kind != profile.Kind {
		return ResolvedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}

	return resolved, nil
}

func (r *HostAuthRuntime) MaterializeAuthProfile(ctx context.Context, request HostAuthRuntimeRequest) (MaterializedAuthSecret, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	bound, _, done, err := r.bindRequest(request, hostAuthRuntimeBindOptions{strictProfileBinding: true, requireTarget: true})
	if err != nil {
		return MaterializedAuthSecret{}, err
	}
	if done || !hostAuthRuntimeRequestMatched(bound) {
		return MaterializedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	ref, err := hostAuthRuntimeSingleSelectedRef(bound.selectedRefs)
	if err != nil {
		return MaterializedAuthSecret{}, err
	}
	if r.store == nil {
		return MaterializedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	profile := r.profiles[bound.pack.PackIdentity][ref]
	resolved, err := r.store.ResolveAuthProfile(ctx, bound.pack.PackIdentity.PackID, ref, request.TargetURL)
	if err != nil {
		return MaterializedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}
	material, err := r.effectiveMaterializer().MaterializeAuth(resolved)
	if err != nil || material.Kind != profile.Kind {
		return MaterializedAuthSecret{}, errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}

	return material, nil
}

func (r HostAuthRuntimeResult) String() string {
	return fmt.Sprintf("HostAuthRuntimeResult{matched:%t required:%t available:%t refreshable:%t provisioned:%t refresh_skipped:%t pack_id:%q message:%q profile_statuses:%v}", r.Matched, r.Required, r.Available, r.Refreshable, r.Provisioned, r.RefreshSkipped, r.PackID, r.Message, r.ProfileStatuses)
}

func (r HostAuthRuntimeResult) GoString() string {
	return r.String()
}

func (r HostAuthRuntimeProfileResult) String() string {
	return fmt.Sprintf("HostAuthRuntimeProfileResult{profile_ref:%q kind:%q status:%q refreshable:%t redacted_display:%q snapshot:%s}", r.ProfileRef, r.Kind, r.Status, r.Refreshable, r.RedactedDisplay, r.Snapshot.String())
}

func (r HostAuthRuntimeProfileResult) GoString() string {
	return r.String()
}

func (r *HostAuthRuntime) bindRequest(request HostAuthRuntimeRequest, opts hostAuthRuntimeBindOptions) (hostAuthRuntimeBoundRequest, HostAuthRuntimeResult, bool, error) {
	if r == nil || len(r.identityIndex) == 0 {
		return hostAuthRuntimeBoundRequest{}, hostAuthRuntimeNoRuntimeResult(), true, nil
	}
	pack, ok := r.identityIndex[request.PackIdentity]
	if !ok {
		return hostAuthRuntimeBoundRequest{}, hostAuthRuntimeNoRuntimeResult(), true, nil
	}
	if request.Manifest.PackID != request.PackIdentity.PackID || request.Manifest.PackVersion != request.PackIdentity.PackVersion {
		return hostAuthRuntimeBoundRequest{}, hostAuthRuntimeNoRuntimeResult(), true, nil
	}
	if request.SourceURL != "" {
		sourceHost, ok := hostAuthRuntimeSourceHost(request.SourceURL)
		if !ok {
			return hostAuthRuntimeBoundRequest{}, hostAuthRuntimeNoRuntimeResult(), true, nil
		}
		if !isAliasManifest(request.Manifest) && len(request.Manifest.Domains) > 0 && !manifestMatchesHost(request.Manifest, sourceHost) {
			return hostAuthRuntimeBoundRequest{}, hostAuthRuntimeNoRuntimeResult(), true, nil
		}
	}

	targetHost := ""
	if request.TargetURL != "" {
		parsed, host, err := parseSafeHTTPURL(request.TargetURL)
		if err != nil || parsed.Scheme != "https" {
			return hostAuthRuntimeBoundRequest{}, HostAuthRuntimeResult{}, true, errors.New("auth target URL is invalid")
		}
		targetHost = host
	} else if opts.requireTarget {
		return hostAuthRuntimeBoundRequest{}, HostAuthRuntimeResult{}, true, errors.New("auth target URL is required")
	}

	selectedRefs, ok, err := r.selectProfileRefs(request.PackIdentity, request.ProfileRef)
	if err != nil {
		return hostAuthRuntimeBoundRequest{}, HostAuthRuntimeResult{}, true, err
	}
	bound := hostAuthRuntimeBoundRequest{
		request:      request,
		pack:         clonePrivateAuthRuntimePack(pack),
		selectedRefs: cloneAuthProfileIDSlice(selectedRefs),
		targetHost:   targetHost,
	}
	if !ok {
		if opts.strictProfileBinding {
			return bound, r.unavailableResult(bound), true, errors.New(hostAuthRuntimeProfileUnavailableMessage)
		}
		return bound, r.unavailableResult(bound), true, nil
	}
	for _, ref := range selectedRefs {
		profile := r.profiles[request.PackIdentity][ref]
		if err := validateAuthSecretKind(profile.Kind); err != nil {
			return bound, r.unavailableResult(bound), true, errors.New(hostAuthRuntimeProfileUnavailableMessage)
		}
	}

	return bound, HostAuthRuntimeResult{}, false, nil
}

func (r *HostAuthRuntime) selectProfileRefs(identity VerifiedPackIdentity, requested AuthProfileID) ([]AuthProfileID, bool, error) {
	if requested != "" {
		if err := validateAuthProfileID(requested); err != nil {
			return nil, false, errors.New(hostAuthRuntimeProfileUnavailableMessage)
		}
		if _, ok := r.boundMaterializedProfile(identity, requested); !ok {
			return []AuthProfileID{requested}, false, nil
		}

		return []AuthProfileID{requested}, true, nil
	}

	pack := r.identityIndex[identity]
	refs := cloneAuthProfileIDSlice(pack.Materialization.ProfileRefs)
	if len(refs) == 0 {
		return nil, false, nil
	}
	for _, ref := range refs {
		if _, ok := r.boundMaterializedProfile(identity, ref); !ok {
			return refs, false, nil
		}
	}

	return refs, true, nil
}

func (r *HostAuthRuntime) boundMaterializedProfile(identity VerifiedPackIdentity, profileID AuthProfileID) (PrivateAuthRuntimeProfile, bool) {
	if r == nil {
		return PrivateAuthRuntimeProfile{}, false
	}
	profile, ok := r.profiles[identity][profileID]
	if !ok {
		return PrivateAuthRuntimeProfile{}, false
	}
	if _, ok := r.storeRefs[identity][profileID]; !ok {
		return PrivateAuthRuntimeProfile{}, false
	}
	if _, ok := r.materialRefs[identity][profileID]; !ok {
		return PrivateAuthRuntimeProfile{}, false
	}

	return clonePrivateAuthRuntimeProfile(profile), true
}

func (r *HostAuthRuntime) unavailableResult(bound hostAuthRuntimeBoundRequest) HostAuthRuntimeResult {
	result := HostAuthRuntimeResult{
		Matched:   true,
		Required:  hostAuthRuntimePreflightRequired(bound.pack),
		Available: false,
		PackID:    bound.pack.PackIdentity.PackID,
		Message:   hostAuthRuntimeProfileUnavailableMessage,

		ProfileStatuses: hostAuthRuntimeUnavailableStatuses(bound),
	}

	return cloneHostAuthRuntimeResult(result)
}

func hostAuthRuntimeUnavailableStatuses(bound hostAuthRuntimeBoundRequest) []HostAuthRuntimeProfileResult {
	if len(bound.selectedRefs) == 0 {
		return nil
	}
	profiles := hostAuthRuntimeProfileMap(bound.pack.Profiles)
	statuses := make([]HostAuthRuntimeProfileResult, 0, len(bound.selectedRefs))
	for _, ref := range bound.selectedRefs {
		statuses = append(statuses, HostAuthRuntimeProfileResult{
			ProfileRef: ref,
			Kind:       profiles[ref].Kind,
			Status:     HostAuthRuntimeProfileUnavailable,
		})
	}

	return statuses
}

func hostAuthRuntimeClassifySnapshot(snapshot AuthProfileSnapshot, profile PrivateAuthRuntimeProfile, targetHost string, now time.Time) hostAuthRuntimeProfileSnapshotClass {
	if !snapshot.HasSecret {
		return hostAuthRuntimeProfileSnapshotMissing
	}
	if snapshot.Kind != profile.Kind {
		return hostAuthRuntimeProfileSnapshotStaleKindMismatch
	}
	if snapshot.ExpiresAt != nil && now.After(*snapshot.ExpiresAt) {
		return hostAuthRuntimeProfileSnapshotExpired
	}
	if targetHost == "" {
		return hostAuthRuntimeProfileSnapshotAvailable
	}
	if len(snapshot.AllowedDomains) == 0 {
		return hostAuthRuntimeProfileSnapshotStaleEmptyDomains
	}
	if targetHost != "" && !domainRulesMatchHost(snapshot.AllowedDomains, targetHost) {
		return hostAuthRuntimeProfileSnapshotStaleDomainMismatch
	}

	return hostAuthRuntimeProfileSnapshotAvailable
}

func (c hostAuthRuntimeProfileSnapshotClass) status() HostAuthRuntimeProfileStatus {
	switch c {
	case hostAuthRuntimeProfileSnapshotAvailable:
		return HostAuthRuntimeProfileAvailable
	case hostAuthRuntimeProfileSnapshotMissing:
		return HostAuthRuntimeProfileMissing
	case hostAuthRuntimeProfileSnapshotExpired:
		return HostAuthRuntimeProfileExpired
	default:
		return HostAuthRuntimeProfileUnavailable
	}
}

func hostAuthRuntimeNoRuntimeResult() HostAuthRuntimeResult {
	return HostAuthRuntimeResult{Available: true}
}

func hostAuthRuntimePreflightRequired(pack PrivateAuthRuntimePack) bool {
	return pack.Preflight.Mode != "optional"
}

func hostAuthRuntimeSourceHost(rawURL string) (string, bool) {
	_, host, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return "", false
	}

	return host, true
}

func hostAuthRuntimeSingleSelectedRef(refs []AuthProfileID) (AuthProfileID, error) {
	if len(refs) != 1 {
		return "", errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}

	return refs[0], nil
}

func hostAuthRuntimeRequestMatched(bound hostAuthRuntimeBoundRequest) bool {
	return bound.pack.PackIdentity.PackID != ""
}

func validateHostAuthRuntimeHTTPSURL(rawURL string) error {
	parsed, _, err := parseSafeHTTPURL(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}

	return nil
}

func (r *HostAuthRuntime) effectiveMaterializer() AuthMaterializer {
	if r == nil || r.materializer == nil {
		return NewDefaultAuthMaterializer()
	}

	return r.materializer
}

func (r *HostAuthRuntime) effectiveNow() time.Time {
	if r == nil || r.now == nil {
		return time.Now()
	}

	return r.now()
}

func hostAuthRuntimeProfileMap(profiles []PrivateAuthRuntimeProfile) map[AuthProfileID]PrivateAuthRuntimeProfile {
	output := make(map[AuthProfileID]PrivateAuthRuntimeProfile, len(profiles))
	for _, profile := range profiles {
		output[profile.ProfileRef] = clonePrivateAuthRuntimeProfile(profile)
	}

	return output
}

func hostAuthRuntimeProfileRefSet(refs []AuthProfileID) map[AuthProfileID]struct{} {
	set := make(map[AuthProfileID]struct{}, len(refs))
	for _, ref := range refs {
		set[ref] = struct{}{}
	}

	return set
}

func cloneHostAuthRuntimeResult(result HostAuthRuntimeResult) HostAuthRuntimeResult {
	if result.ProfileStatuses != nil {
		statuses := make([]HostAuthRuntimeProfileResult, len(result.ProfileStatuses))
		for i, status := range result.ProfileStatuses {
			statuses[i] = cloneHostAuthRuntimeProfileResult(status)
		}
		result.ProfileStatuses = statuses
	}

	return result
}

func cloneHostAuthRuntimeProfileResult(result HostAuthRuntimeProfileResult) HostAuthRuntimeProfileResult {
	result.Snapshot = cloneAuthProfileSnapshot(result.Snapshot)

	return result
}

func cloneAuthProfileSnapshot(snapshot AuthProfileSnapshot) AuthProfileSnapshot {
	snapshot.AllowedDomains = cloneDomainRules(snapshot.AllowedDomains)
	snapshot.ExpiresAt = cloneTime(snapshot.ExpiresAt)

	return snapshot
}

func (r *HostAuthRuntime) SupportsPackIdentity(identity VerifiedPackIdentity) bool {
	if r == nil {
		return false
	}
	pack, ok := r.identityIndex[identity]
	if !ok {
		return false
	}
	return len(pack.StoreBinding.ProfileRefs) > 0 || len(pack.Materialization.ProfileRefs) > 0 || len(pack.Profiles) > 0
}
