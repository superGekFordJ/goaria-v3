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

type hostAuthRuntimeProfileSnapshotClass string

const (
	hostAuthRuntimeProfileSnapshotAvailable           hostAuthRuntimeProfileSnapshotClass = "available"
	hostAuthRuntimeProfileSnapshotMissing             hostAuthRuntimeProfileSnapshotClass = "missing"
	hostAuthRuntimeProfileSnapshotExpired             hostAuthRuntimeProfileSnapshotClass = "expired"
	hostAuthRuntimeProfileSnapshotStaleKindMismatch   hostAuthRuntimeProfileSnapshotClass = "stale_kind_mismatch"
	hostAuthRuntimeProfileSnapshotStaleEmptyDomains   hostAuthRuntimeProfileSnapshotClass = "stale_empty_domains"
	hostAuthRuntimeProfileSnapshotStaleDomainMismatch hostAuthRuntimeProfileSnapshotClass = "stale_domain_mismatch"
)

type hostAuthRuntimeProvisionPlan struct {
	profile             PrivateAuthRuntimeProfile
	request             WebViewAuthRequest
	storeAllowedDomains []DomainRule
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

	bound, result, done, err := r.bindRequest(request, hostAuthRuntimeBindOptions{strictProfileBinding: true})
	if err != nil {
		return result, err
	}
	if done {
		return cloneHostAuthRuntimeResult(result), nil
	}

	return r.provisionAndPreflight(ctx, bound, refs)
}

func (r *HostAuthRuntime) RefreshOnRecoverablePreflightFailure(ctx context.Context, request HostAuthRuntimeRequest, guard *HostAuthRuntimeBatchGuard) (HostAuthRuntimeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	preflight, err := r.Preflight(ctx, request)
	if err != nil || !preflight.Matched || !preflight.Required || preflight.Available {
		return preflight, err
	}
	if !preflight.Refreshable || !hostAuthRuntimeNonAvailableProfilesRefreshable(preflight) {
		preflight.Message = hostAuthRuntimeProfileUnavailableMessage
		return cloneHostAuthRuntimeResult(preflight), nil
	}

	bound, result, done, err := r.bindRequest(request, hostAuthRuntimeBindOptions{strictProfileBinding: true})
	if err != nil {
		return result, err
	}
	if done {
		return cloneHostAuthRuntimeResult(result), nil
	}
	refs := hostAuthRuntimeRefreshableRefs(preflight)
	return r.clearProvisionAndPreflight(ctx, bound, refs, guard)
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
	plans, err := r.provisionPlans(ctx, bound)
	if err != nil {
		result = r.provisionUnavailableResult(bound)
		return result, err
	}

	for _, plan := range plans {
		webViewResult, err := r.startProvisioningWebView(ctx, plan)
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

func (r *HostAuthRuntime) startProvisioningWebView(ctx context.Context, plan hostAuthRuntimeProvisionPlan) (WebViewAuthResult, error) {
	return r.startProvisioningWebViewWithStoreDomains(ctx, plan.request, plan.storeAllowedDomains)
}

func (r *HostAuthRuntime) startProvisioningWebViewWithStoreDomains(ctx context.Context, request WebViewAuthRequest, storeDomains []DomainRule) (WebViewAuthResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	storeRequest := request
	if len(storeDomains) > 0 {
		storeRequest.AllowedDomains = cloneDomainRules(storeDomains)
	}
	ctx, cancel := context.WithTimeout(ctx, effectiveWebViewTimeout(request))
	defer cancel()

	terminal := newAuthTerminal()
	sink := AuthWebViewSink{
		OnSuccess: func(token AuthWebViewToken) {
			terminal.complete(webViewTerminalEvent{status: WebViewAuthStatusSuccess, token: token})
		},
		OnCancel: func() {
			terminal.complete(webViewTerminalEvent{status: WebViewAuthStatusCanceled})
		},
		OnError: func(err error) {
			terminal.complete(webViewTerminalEvent{status: WebViewAuthStatusError, err: err})
		},
	}
	session, err := r.coordinator.driver.OpenAuthSession(ctx, request, sink)
	if err != nil {
		return WebViewAuthResult{}, redactedError(fmt.Errorf("open auth webview for %s: %w", request.LoginURL, err))
	}
	defer func() { _ = session.Close() }()

	return r.coordinator.handleTerminalEvent(storeRequest, terminal.wait(ctx))
}

func (r *HostAuthRuntime) validateAliasWebViewAuthRequest(ctx context.Context, bound hostAuthRuntimeBoundRequest, profile PrivateAuthRuntimeProfile, request WebViewAuthRequest) (WebViewAuthRequest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.hostPolicyResolver == nil || bound.pack.PackIdentity == (VerifiedPackIdentity{}) {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	if request.Manifest.PackID != bound.pack.PackIdentity.PackID || request.Manifest.PackVersion != bound.pack.PackIdentity.PackVersion || request.PackID != bound.pack.PackIdentity.PackID {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	if profile.ProfileRef != request.ProfileID {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	if _, ok := r.provisionRefs[bound.pack.PackIdentity][request.ProfileID]; !ok {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	if _, ok := r.boundMaterializedProfile(bound.pack.PackIdentity, request.ProfileID); !ok {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}

	policy, err := resolveAliasHostPolicy(ctx, r.hostPolicyResolver, bound.pack.PackIdentity, request.Manifest)
	if err != nil {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	if !ManifestHasCapability(request.Manifest, CapabilityAuthProfile) || !policyAllowsCapability(policy, CapabilityAuthProfile) {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:             request.PackID,
		Manifest:           request.Manifest,
		Capability:         CapabilityAuthProfile,
		PackIdentity:       bound.pack.PackIdentity,
		HostPolicyResolver: r.hostPolicyResolver,
	}, request.LoginURL); err != nil {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}

	validatedRequest, err := validateWebViewAuthRequestBase(request)
	if err != nil {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	_, loginHost, err := parseSafeHTTPURL(validatedRequest.LoginURL)
	if err != nil {
		return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
	}
	for _, rule := range validatedRequest.AllowedDomains {
		if !hostAuthRuntimeLoginAllowedDomainMatchesHost(rule, loginHost) {
			return WebViewAuthRequest{}, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
	}

	return validatedRequest, nil
}

func hostAuthRuntimeLoginAllowedDomainMatchesHost(rule DomainRule, loginHost string) bool {
	return strings.EqualFold(rule.Host, loginHost)
}

func targetHostInProfileScope(policy ResolvedHostPolicy, profileID AuthProfileID, targetHost string) bool {
	if targetHost == "" {
		return true
	}

	return policyAuthProfileMatchesHost(policy, profileID, targetHost)
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
	refs := hostAuthRuntimeAvailableProfileRefs(preflight)
	if len(refs) == 0 && preflight.Available {
		refs = cloneAuthProfileIDSlice(bound.selectedRefs)
	}
	if len(refs) == 0 {
		preflight.Message = hostAuthRuntimeProfileUnavailableMessage
		return cloneHostAuthRuntimeResult(preflight), nil
	}

	return r.clearProvisionAndPreflight(ctx, bound, refs, guard)
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

func (r *HostAuthRuntime) clearProvisionAndPreflight(ctx context.Context, bound hostAuthRuntimeBoundRequest, refs []AuthProfileID, guard *HostAuthRuntimeBatchGuard) (HostAuthRuntimeResult, error) {
	if len(refs) == 0 {
		return r.unavailableResult(bound), nil
	}
	refreshBound := bound
	refreshBound.selectedRefs = cloneAuthProfileIDSlice(refs)
	plans, err := r.provisionPlans(ctx, refreshBound)
	if err != nil {
		return r.provisionUnavailableResult(refreshBound), err
	}
	if guard != nil && !guard.mark(hostAuthRuntimeGuardKey(refreshBound)) {
		result := r.unavailableResult(refreshBound)
		result.RefreshSkipped = true
		result.Message = "auth refresh skipped"
		return cloneHostAuthRuntimeResult(result), nil
	}
	if r.store == nil {
		return r.unavailableResult(refreshBound), nil
	}
	for _, ref := range refs {
		if err := r.store.ClearAuthProfile(ctx, refreshBound.pack.PackIdentity.PackID, ref); err != nil {
			return r.unavailableResult(refreshBound), errors.New(hostAuthRuntimeProfileUnavailableMessage)
		}
	}

	return r.runProvisionPlansAndPreflight(ctx, refreshBound, bound.request, plans)
}

func (r *HostAuthRuntime) provisionAndPreflight(ctx context.Context, bound hostAuthRuntimeBoundRequest, refs []AuthProfileID) (HostAuthRuntimeResult, error) {
	if len(refs) == 0 {
		return r.unavailableResult(bound), nil
	}
	provisionBound := bound
	provisionBound.selectedRefs = cloneAuthProfileIDSlice(refs)
	plans, err := r.provisionPlans(ctx, provisionBound)
	if err != nil {
		return r.provisionUnavailableResult(provisionBound), err
	}

	return r.runProvisionPlansAndPreflight(ctx, provisionBound, bound.request, plans)
}

func (r *HostAuthRuntime) runProvisionPlansAndPreflight(ctx context.Context, bound hostAuthRuntimeBoundRequest, finalRequest HostAuthRuntimeRequest, plans []hostAuthRuntimeProvisionPlan) (HostAuthRuntimeResult, error) {
	for _, plan := range plans {
		webViewResult, err := r.startProvisioningWebView(ctx, plan)
		if err != nil {
			return r.provisionUnavailableResult(bound), errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
		if webViewResult.Status != WebViewAuthStatusSuccess {
			return r.provisionUnavailableResult(bound), nil
		}
	}

	result, err := r.Preflight(ctx, finalRequest)
	if err != nil {
		return result, err
	}
	result.Provisioned = true
	if result.Message == "" {
		result.Message = "auth profile available"
	}

	return cloneHostAuthRuntimeResult(result), nil
}

func (r *HostAuthRuntime) provisionPlans(ctx context.Context, bound hostAuthRuntimeBoundRequest) ([]hostAuthRuntimeProvisionPlan, error) {
	if err := r.validateProvisionable(bound); err != nil {
		return nil, err
	}
	policy, err := r.resolveAliasProvisionTargetPolicy(ctx, bound)
	if err != nil {
		return nil, err
	}

	return r.webViewProvisionPlans(ctx, bound, policy)
}

func (r *HostAuthRuntime) resolveAliasProvisionTargetPolicy(ctx context.Context, bound hostAuthRuntimeBoundRequest) (*ResolvedHostPolicy, error) {
	if isAliasManifest(bound.request.Manifest) {
		if r.hostPolicyResolver == nil {
			return nil, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
		aliasPolicy, err := resolveAliasHostPolicy(ctx, r.hostPolicyResolver, bound.pack.PackIdentity, bound.request.Manifest)
		if err != nil {
			return nil, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
		if bound.targetHost != "" {
			for _, ref := range bound.selectedRefs {
				if !targetHostInProfileScope(aliasPolicy, ref, bound.targetHost) {
					return nil, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
				}
			}
		}

		return &aliasPolicy, nil
	}

	return nil, nil
}

func (r *HostAuthRuntime) webViewProvisionPlans(ctx context.Context, bound hostAuthRuntimeBoundRequest, aliasPolicy *ResolvedHostPolicy) ([]hostAuthRuntimeProvisionPlan, error) {
	plans := make([]hostAuthRuntimeProvisionPlan, 0, len(bound.selectedRefs))
	for _, ref := range bound.selectedRefs {
		profile := r.profiles[bound.pack.PackIdentity][ref]
		storeAllowedDomains := cloneDomainRules(profile.Login.AllowedDomains)
		if aliasPolicy != nil {
			storeAllowedDomains = hostAuthRuntimeAuthProfileScopeDomains(*aliasPolicy, ref)
			if len(storeAllowedDomains) == 0 {
				return nil, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
			}
		}
		webViewRequest := WebViewAuthRequest{
			PackID:            bound.pack.PackIdentity.PackID,
			Manifest:          cloneManifest(bound.request.Manifest),
			ProfileID:         ref,
			LoginURL:          profile.Login.URL,
			AllowedDomains:    cloneDomainRules(profile.Login.AllowedDomains),
			Timeout:           time.Duration(profile.Login.TimeoutMillis) * time.Millisecond,
			Kind:              profile.Kind,
			CallbackTransport: webViewAuthCallbackTransportFromRuntime(profile.Login.CallbackTransport),
			CollectorJS:       profile.Login.CollectorJS,
			Capture:           webViewAuthCaptureFromRuntime(profile.Login.Capture, bound.pack.Normalization),
		}
		if isAliasManifest(webViewRequest.Manifest) {
			validatedRequest, err := r.validateAliasWebViewAuthRequest(ctx, bound, profile, webViewRequest)
			if err != nil {
				return nil, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
			}
			webViewRequest = validatedRequest
		} else {
			validatedRequest, err := validateWebViewAuthRequest(webViewRequest)
			if err != nil {
				return nil, errors.New(hostAuthRuntimeProvisionUnavailableMessage)
			}
			webViewRequest = validatedRequest
		}
		plans = append(plans, hostAuthRuntimeProvisionPlan{profile: profile, request: webViewRequest, storeAllowedDomains: storeAllowedDomains})
	}

	return plans, nil
}

func hostAuthRuntimeAuthProfileScopeDomains(policy ResolvedHostPolicy, profileID AuthProfileID) []DomainRule {
	for _, scope := range policy.AuthProfiles {
		if scope.ProfileID == profileID {
			return cloneDomainRules(scope.Domains)
		}
	}

	return nil
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

func (r *HostAuthRuntime) profileRefreshable(ctx context.Context, bound hostAuthRuntimeBoundRequest, ref AuthProfileID, class hostAuthRuntimeProfileSnapshotClass) bool {
	if r == nil || bound.pack.Provisioning.Mode != "webview" {
		return false
	}
	if _, ok := r.provisionRefs[bound.pack.PackIdentity][ref]; !ok {
		return false
	}
	allowed := false
	switch class {
	case hostAuthRuntimeProfileSnapshotMissing:
		allowed = bound.pack.Preflight.Missing == "refresh"
	case hostAuthRuntimeProfileSnapshotExpired:
		allowed = bound.pack.Preflight.Expired == "refresh"
	case hostAuthRuntimeProfileSnapshotStaleKindMismatch, hostAuthRuntimeProfileSnapshotStaleEmptyDomains, hostAuthRuntimeProfileSnapshotStaleDomainMismatch:
		allowed = true
	default:
		return false
	}
	if !allowed {
		return false
	}
	refreshBound := bound
	refreshBound.selectedRefs = []AuthProfileID{ref}
	_, err := r.provisionPlans(ctx, refreshBound)

	return err == nil
}

func (r *HostAuthRuntime) validateProvisionable(bound hostAuthRuntimeBoundRequest) error {
	if r == nil || r.coordinator == nil || r.coordinator.store == nil || r.coordinator.driver == nil || bound.pack.Provisioning.Mode != "webview" {
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
		if err := validateWebViewAuthCallbackTransport(webViewAuthCallbackTransportFromRuntime(profile.Login.CallbackTransport)); err != nil {
			return errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
		if err := validateWebViewAuthCollectorJS(profile.Login.CollectorJS); err != nil {
			return errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
		if err := validateWebViewAuthCaptureContract(webViewAuthCaptureFromRuntime(profile.Login.Capture, bound.pack.Normalization)); err != nil {
			return errors.New(hostAuthRuntimeProvisionUnavailableMessage)
		}
	}

	return nil
}

func webViewAuthCallbackTransportFromRuntime(transport PrivateAuthRuntimeCallbackTransport) WebViewAuthCallbackTransport {
	return WebViewAuthCallbackTransport{
		Mode:         transport.Mode,
		ContentTypes: cloneStringSlice(transport.ContentTypes),
		MaxBodyBytes: transport.MaxBodyBytes,
	}
}

func webViewAuthCaptureFromRuntime(capture PrivateAuthRuntimeCaptureContract, normalization PrivateAuthRuntimeNormalizationPolicy) WebViewAuthCaptureContract {
	return WebViewAuthCaptureContract{
		Format:               capture.Format,
		SecretCandidates:     cloneStringSlice(capture.SecretCandidates),
		KindField:            capture.KindField,
		ExpiresAtField:       capture.ExpiresAtField,
		RedactedDisplayField: capture.RedactedDisplayField,
		TrimSpace:            normalization.TrimSpace,
		RejectCRLF:           normalization.RejectCRLF,
	}
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

func hostAuthRuntimeNonAvailableProfilesRefreshable(result HostAuthRuntimeResult) bool {
	foundNonAvailable := false
	for _, status := range result.ProfileStatuses {
		if status.Status == HostAuthRuntimeProfileAvailable {
			continue
		}
		foundNonAvailable = true
		if !status.Refreshable {
			return false
		}
	}

	return foundNonAvailable
}

func hostAuthRuntimeRefreshableRefs(result HostAuthRuntimeResult) []AuthProfileID {
	refs := make([]AuthProfileID, 0, len(result.ProfileStatuses))
	for _, status := range result.ProfileStatuses {
		if status.Status != HostAuthRuntimeProfileAvailable && status.Refreshable {
			refs = append(refs, status.ProfileRef)
		}
	}

	return refs
}

func hostAuthRuntimeAvailableProfileRefs(result HostAuthRuntimeResult) []AuthProfileID {
	refs := make([]AuthProfileID, 0, len(result.ProfileStatuses))
	for _, status := range result.ProfileStatuses {
		if status.Status == HostAuthRuntimeProfileAvailable {
			refs = append(refs, status.ProfileRef)
		}
	}

	return refs
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
