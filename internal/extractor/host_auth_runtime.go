package extractor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	hostAuthRuntimeProfileUnavailableMessage   = "auth profile unavailable"
	hostAuthRuntimeProvisionUnavailableMessage = "auth provisioning unavailable"
)

type HostAuthRuntimeConfig struct {
	Bundle       *PrivateAuthRuntimeBundle
	Store        AuthProfileStore
	Coordinator  *WebViewAuthCoordinator
	Materializer AuthMaterializer
	Now          func() time.Time
}

type HostAuthRuntime struct {
	bundle       *PrivateAuthRuntimeBundle
	store        AuthProfileStore
	coordinator  *WebViewAuthCoordinator
	materializer AuthMaterializer
	now          func() time.Time

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

type HostAuthRuntimeBatchGuard struct {
	mu        sync.Mutex
	refreshed map[string]struct{}
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
		bundle:        config.Bundle,
		store:         config.Store,
		coordinator:   config.Coordinator,
		materializer:  materializer,
		now:           now,
		identityIndex: make(map[VerifiedPackIdentity]PrivateAuthRuntimePack),
		packIDIndex:   make(map[string][]VerifiedPackIdentity),
		profiles:      make(map[VerifiedPackIdentity]map[AuthProfileID]PrivateAuthRuntimeProfile),
		storeRefs:     make(map[VerifiedPackIdentity]map[AuthProfileID]struct{}),
		materialRefs:  make(map[VerifiedPackIdentity]map[AuthProfileID]struct{}),
		provisionRefs: make(map[VerifiedPackIdentity]map[AuthProfileID]struct{}),
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

func NewHostAuthRuntimeBatchGuard() *HostAuthRuntimeBatchGuard {
	return &HostAuthRuntimeBatchGuard{refreshed: make(map[string]struct{})}
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
		if snapshot, ok := snapshotByProfile[ref]; ok {
			statusResult.Snapshot = cloneAuthProfileSnapshot(snapshot)
			statusResult.RedactedDisplay = snapshot.RedactedDisplay
			statusResult.Status = hostAuthRuntimeSnapshotStatus(snapshot, profile, bound.targetHost, r.effectiveNow())
		}
		statusResult.Refreshable = r.profileRefreshable(bound.pack, ref, statusResult.Status)
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

func (r *HostAuthRuntime) Ensure(ctx context.Context, request HostAuthRuntimeRequest) (HostAuthRuntimeResult, error) {
	preflight, err := r.Preflight(ctx, request)
	if err != nil || !preflight.Matched || !preflight.Required || preflight.Available {
		return preflight, err
	}

	refs := make([]AuthProfileID, 0, len(preflight.ProfileStatuses))
	for _, status := range preflight.ProfileStatuses {
		if status.Refreshable {
			refs = append(refs, status.ProfileRef)
		}
	}
	if len(refs) == 0 {
		preflight.Message = hostAuthRuntimeProfileUnavailableMessage
		return cloneHostAuthRuntimeResult(preflight), nil
	}

	provisioned := false
	for _, ref := range refs {
		nextRequest := request
		nextRequest.ProfileRef = ref
		result, err := r.Provision(ctx, nextRequest)
		if err != nil {
			return result, err
		}
		if !result.Provisioned || !result.Available {
			return result, nil
		}
		provisioned = true
	}

	result, err := r.Preflight(ctx, request)
	if err != nil {
		return result, err
	}
	result.Provisioned = provisioned
	if provisioned && result.Message == "" {
		result.Message = "auth profile available"
	}

	return cloneHostAuthRuntimeResult(result), nil
}

func (r *HostAuthRuntime) Provision(ctx context.Context, request HostAuthRuntimeRequest) (HostAuthRuntimeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	bound, result, done, err := r.bindRequest(request, hostAuthRuntimeBindOptions{strictProfileBinding: true})
	if err != nil {
		return result, err
	}
	if done {
		return cloneHostAuthRuntimeResult(result), nil
	}
	if err := r.validateProvisionable(bound); err != nil {
		result = r.provisionUnavailableResult(bound)
		return result, err
	}

	for _, ref := range bound.selectedRefs {
		profile := r.profiles[bound.pack.PackIdentity][ref]
		webViewResult, err := r.coordinator.Start(ctx, WebViewAuthRequest{
			PackID:         bound.pack.PackIdentity.PackID,
			Manifest:       cloneManifest(request.Manifest),
			ProfileID:      ref,
			LoginURL:       profile.Login.URL,
			AllowedDomains: cloneDomainRules(profile.Login.AllowedDomains),
			Timeout:        time.Duration(profile.Login.TimeoutMillis) * time.Millisecond,
			Kind:           profile.Kind,
		})
		if err != nil {
			return r.provisionUnavailableResult(bound), errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
		if webViewResult.Status != WebViewAuthStatusSuccess {
			return r.provisionUnavailableResult(bound), nil
		}
	}

	result, err = r.Preflight(ctx, request)
	if err != nil {
		return result, err
	}
	result.Provisioned = true
	if result.Message == "" {
		result.Message = "auth profile available"
	}

	return cloneHostAuthRuntimeResult(result), nil
}

func (r *HostAuthRuntime) Clear(ctx context.Context, request HostAuthRuntimeRequest) (HostAuthRuntimeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	bound, result, done, err := r.bindRequest(request, hostAuthRuntimeBindOptions{strictProfileBinding: true})
	if err != nil {
		return result, err
	}
	if done {
		return cloneHostAuthRuntimeResult(result), nil
	}
	if r.store == nil {
		return r.unavailableResult(bound), errors.New(hostAuthRuntimeProfileUnavailableMessage)
	}

	for _, ref := range bound.selectedRefs {
		if err := r.store.ClearAuthProfile(ctx, bound.pack.PackIdentity.PackID, ref); err != nil {
			return r.unavailableResult(bound), errors.New(hostAuthRuntimeProfileUnavailableMessage)
		}
	}

	result, err = r.Preflight(ctx, request)
	if err != nil {
		return result, err
	}
	result.Message = "auth profile cleared"

	return cloneHostAuthRuntimeResult(result), nil
}

func (r *HostAuthRuntime) RefreshOnGenericFailure(ctx context.Context, request HostAuthRuntimeRequest, guard *HostAuthRuntimeBatchGuard) (HostAuthRuntimeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	preflight, err := r.Preflight(ctx, request)
	if err != nil || !preflight.Matched || !preflight.Required {
		return preflight, err
	}

	bound, result, done, err := r.bindRequest(request, hostAuthRuntimeBindOptions{strictProfileBinding: true})
	if err != nil {
		return result, err
	}
	if done {
		return cloneHostAuthRuntimeResult(result), nil
	}
	if !r.isProvisionable(bound) {
		preflight.Message = hostAuthRuntimeProvisionUnavailableMessage
		return cloneHostAuthRuntimeResult(preflight), nil
	}
	if guard != nil && !guard.mark(hostAuthRuntimeGuardKey(bound)) {
		preflight.RefreshSkipped = true
		preflight.Message = "auth refresh skipped"
		return cloneHostAuthRuntimeResult(preflight), nil
	}
	if r.store == nil {
		preflight.Message = hostAuthRuntimeProfileUnavailableMessage
		return cloneHostAuthRuntimeResult(preflight), nil
	}
	for _, ref := range bound.selectedRefs {
		if err := r.store.ClearAuthProfile(ctx, bound.pack.PackIdentity.PackID, ref); err != nil {
			return r.unavailableResult(bound), errors.New(hostAuthRuntimeProfileUnavailableMessage)
		}
	}

	return r.Provision(ctx, request)
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

func (g *HostAuthRuntimeBatchGuard) mark(key string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refreshed == nil {
		g.refreshed = make(map[string]struct{})
	}
	if _, ok := g.refreshed[key]; ok {
		return false
	}
	g.refreshed[key] = struct{}{}

	return true
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

func (r *HostAuthRuntime) profileRefreshable(pack PrivateAuthRuntimePack, ref AuthProfileID, status HostAuthRuntimeProfileStatus) bool {
	if r == nil || r.coordinator == nil || pack.Provisioning.Mode != "webview" {
		return false
	}
	if _, ok := r.provisionRefs[pack.PackIdentity][ref]; !ok {
		return false
	}
	switch status {
	case HostAuthRuntimeProfileMissing:
		return pack.Preflight.Missing == "refresh"
	case HostAuthRuntimeProfileExpired:
		return pack.Preflight.Expired == "refresh"
	default:
		return false
	}
}

func (r *HostAuthRuntime) isProvisionable(bound hostAuthRuntimeBoundRequest) bool {
	return r.validateProvisionable(bound) == nil
}

func (r *HostAuthRuntime) validateProvisionable(bound hostAuthRuntimeBoundRequest) error {
	if r == nil || r.coordinator == nil || bound.pack.Provisioning.Mode != "webview" {
		return errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	for _, ref := range bound.selectedRefs {
		profile, ok := r.boundMaterializedProfile(bound.pack.PackIdentity, ref)
		if !ok {
			return errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
		if _, ok := r.provisionRefs[bound.pack.PackIdentity][ref]; !ok {
			return errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
		if profile.Login.URL == "" {
			return errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
	}

	return nil
}

func (r *HostAuthRuntime) unavailableResult(bound hostAuthRuntimeBoundRequest) HostAuthRuntimeResult {
	result := HostAuthRuntimeResult{
		Matched:   true,
		Required:  hostAuthRuntimePreflightRequired(bound.pack),
		Available: false,
		PackID:    bound.pack.PackIdentity.PackID,
		Message:   hostAuthRuntimeProfileUnavailableMessage,
	}
	result.ProfileStatuses = hostAuthRuntimeUnavailableStatuses(bound)

	return cloneHostAuthRuntimeResult(result)
}

func (r *HostAuthRuntime) provisionUnavailableResult(bound hostAuthRuntimeBoundRequest) HostAuthRuntimeResult {
	result := r.unavailableResult(bound)
	result.Message = hostAuthRuntimeProvisionUnavailableMessage

	return result
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

func hostAuthRuntimeSnapshotStatus(snapshot AuthProfileSnapshot, profile PrivateAuthRuntimeProfile, targetHost string, now time.Time) HostAuthRuntimeProfileStatus {
	if !snapshot.HasSecret {
		return HostAuthRuntimeProfileMissing
	}
	if snapshot.Kind != profile.Kind {
		return HostAuthRuntimeProfileUnavailable
	}
	if snapshot.ExpiresAt != nil && now.After(*snapshot.ExpiresAt) {
		return HostAuthRuntimeProfileExpired
	}
	if targetHost == "" {
		return HostAuthRuntimeProfileAvailable
	}
	if len(snapshot.AllowedDomains) == 0 {
		return HostAuthRuntimeProfileUnavailable
	}
	if targetHost != "" && !domainRulesMatchHost(snapshot.AllowedDomains, targetHost) {
		return HostAuthRuntimeProfileUnavailable
	}

	return HostAuthRuntimeProfileAvailable
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

func hostAuthRuntimeGuardKey(bound hostAuthRuntimeBoundRequest) string {
	parts := []string{
		bound.pack.PackIdentity.PackID,
		bound.pack.PackIdentity.PackVersion,
		bound.pack.PackIdentity.AssetSHA256,
		bound.pack.PackIdentity.ManifestSHA256,
		bound.pack.PackIdentity.PayloadSHA256,
		bound.pack.PackIdentity.SignatureSHA256,
		bound.pack.PackIdentity.PublicKeySHA256,
	}
	for _, ref := range bound.selectedRefs {
		parts = append(parts, string(ref))
	}
	parts = append(parts, hostAuthRuntimeHashString(bound.request.SourceURL), hostAuthRuntimeHashString(bound.request.TargetURL))

	return strings.Join(parts, "\x00")
}

func hostAuthRuntimeHashString(value string) string {
	hash := sha256.Sum256([]byte(value))

	return hex.EncodeToString(hash[:])
}
