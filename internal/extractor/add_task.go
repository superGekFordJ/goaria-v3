package extractor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	maxAria2HeaderLineBytes = 8 * 1024
	maxAria2Headers         = 64
	emptyExtractOutputError = "could not resolve this link; authentication may be required or the link is unsupported"
)

var (
	ErrResolveTimeout        = errors.New("extractor resolve timed out")
	ErrGenericAuthResolution = errors.New(emptyExtractOutputError)
)

type HeaderProfileResolver interface {
	ResolveHeaderProfile(ctx context.Context, packID string, profileRef string, rawURL string) ([]string, error)
}

type authProfileMaterializer interface {
	MaterializeAuthProfile(ctx context.Context, request HostAuthRuntimeRequest) (MaterializedAuthSecret, error)
}

type AddTaskDispatcherConfig struct {
	Registry       *Registry
	Runner         *Runner
	AuthResolver   AuthProfileResolver
	HeaderResolver HeaderProfileResolver
	// DownloadAuth must be the registry instance shared with the paired
	// Runner's host imports so refs minted there bind and materialize here.
	DownloadAuth *DownloadAuthRegistry
}

type AddTaskDispatcher struct {
	registry       *Registry
	runner         *Runner
	authResolver   AuthProfileResolver
	headerResolver HeaderProfileResolver
	downloadAuth   *DownloadAuthRegistry
}

type AddTaskResolution struct {
	Matched   bool
	SourceURL string
	PackID    string
	Items     []ResolvedAddItem
}

type ResolvedAddItem struct {
	SourceURL        string
	PackID           string
	PackManifest     Manifest
	PackIdentity     VerifiedPackIdentity
	HostPolicy       *ResolvedHostPolicy
	ID               string
	URL              string
	Filename         string
	SizeBytes        int64
	AuthProfileRef   string
	HeaderProfileRef string
	DownloadAuthRef  string
	MimeType         string
	Metadata         map[string]string
}

func NewAddTaskDispatcher(config AddTaskDispatcherConfig) *AddTaskDispatcher {
	return &AddTaskDispatcher{
		registry:       config.Registry,
		runner:         config.Runner,
		authResolver:   config.AuthResolver,
		headerResolver: config.HeaderResolver,
		downloadAuth:   config.DownloadAuth,
	}
}

func (d *AddTaskDispatcher) Registry() *Registry {
	if d == nil {
		return nil
	}
	return d.registry
}

// HeaderContextCapable reports whether the resolve chain has a live broker,
// the precondition for advertising the header-context capability.
func (d *AddTaskDispatcher) HeaderContextCapable() bool {
	return d != nil && d.runner != nil && d.runner.HTTPBroker() != nil
}

func (d *AddTaskDispatcher) AuthRuntimeRequestsForSource(ctx context.Context, rawURL string) ([]HostAuthRuntimeRequest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, redactedError(err)
	}
	if d == nil || d.registry == nil {
		return nil, nil
	}

	matches := d.registry.FindByURLWithContext(ctx, rawURL)
	if err := ctx.Err(); err != nil {
		return nil, redactedError(err)
	}
	if len(matches) == 0 {
		return nil, nil
	}

	requests := make([]HostAuthRuntimeRequest, 0, len(matches))
	for _, pack := range matches {
		requests = append(requests, HostAuthRuntimeRequest{
			PackIdentity: pack.Identity,
			Manifest:     cloneManifest(pack.Manifest),
			SourceURL:    rawURL,
		})
	}

	return requests, nil
}

func (d *AddTaskDispatcher) Resolve(ctx context.Context, rawURL string) (AddTaskResolution, error) {
	resolution := AddTaskResolution{SourceURL: rawURL}
	if d == nil || d.registry == nil {
		return resolution, nil
	}

	candidates := d.registry.FindByURLWithContext(ctx, rawURL)
	if len(candidates) == 0 {
		return resolution, nil
	}
	if d.runner == nil {
		return AddTaskResolution{}, redactErrorf("extractor runner is not configured")
	}

	errorsByPack := make([]string, 0)
	positiveMatch := false
	emptyMatchedOutput := false
	for _, pack := range candidates {
		packID := pack.Manifest.PackID
		match, err := d.runner.Match(ctx, pack, MatchInput{URL: rawURL})
		if err != nil {
			if isResolveTimeout(ctx, err) {
				return AddTaskResolution{}, ErrResolveTimeout
			}
			errorsByPack = append(errorsByPack, safePackError(packID, err))
			continue
		}
		if !match.Matched {
			continue
		}

		positiveMatch = true
		extracted, err := d.runner.Extract(ctx, pack, ExtractInput{URL: rawURL})
		if err != nil {
			if isResolveTimeout(ctx, err) {
				return AddTaskResolution{}, ErrResolveTimeout
			}
			errorsByPack = append(errorsByPack, safePackError(packID, err))
			continue
		}
		// Backstop for the window before boundItemsFromExtractOutput's own
		// defer registers: a panic in alias policy resolution must still
		// purge this invocation's pending entries. Ending an already-ended
		// invocation is a no-op, so this is inert on every normal path.
		defer d.endDownloadAuthInvocation(extracted.InvocationID(), false)

		var hostPolicy *ResolvedHostPolicy
		if isAliasManifest(pack.Manifest) {
			policy, err := resolveAliasHostPolicy(ctx, d.registry.hostPolicyResolver, pack.Identity, pack.Manifest)
			if err != nil {
				d.endDownloadAuthInvocation(extracted.InvocationID(), false)
				errorsByPack = append(errorsByPack, safePackError(packID, err))
				continue
			}
			hostPolicy = &policy
		}
		items, err := d.boundItemsFromExtractOutput(rawURL, pack, hostPolicy, extracted)
		if err != nil {
			return AddTaskResolution{}, redactedError(fmt.Errorf("extractor pack %q returned invalid add item: %w", packID, err))
		}
		if len(items) == 0 {
			emptyMatchedOutput = true
			continue
		}

		return AddTaskResolution{
			Matched:   true,
			SourceURL: rawURL,
			PackID:    packID,
			Items:     items,
		}, nil
	}

	if emptyMatchedOutput {
		return AddTaskResolution{}, ErrGenericAuthResolution
	}

	if positiveMatch || len(errorsByPack) == len(candidates) {
		return AddTaskResolution{}, redactErrorf("extractor dispatch failed: %s", strings.Join(errorsByPack, "; "))
	}

	return resolution, nil
}

// boundItemsFromExtractOutput converts pack output into resolved items and
// ends the extract invocation exactly once: committed output retains bound
// download-auth entries and purges unreferenced ones; any binding failure
// purges the whole invocation.
func (d *AddTaskDispatcher) boundItemsFromExtractOutput(sourceURL string, pack VerifiedPack, hostPolicy *ResolvedHostPolicy, output ExtractOutput) ([]ResolvedAddItem, error) {
	committed := false
	defer func() {
		d.endDownloadAuthInvocation(output.InvocationID(), committed)
	}()

	items, err := d.resolvedItemsFromExtractOutput(sourceURL, pack, hostPolicy, output)
	if err != nil {
		return nil, err
	}
	committed = true

	return items, nil
}

func (d *AddTaskDispatcher) endDownloadAuthInvocation(invocation uint64, committed bool) {
	if d == nil || d.downloadAuth == nil {
		return
	}
	d.downloadAuth.EndInvocation(invocation, committed)
}

// ClaimDownloadAuth records an outstanding holder for a bound ref. Empty ref
// is a no-op; a missing registry fails closed.
func (d *AddTaskDispatcher) ClaimDownloadAuth(ref string, holderKey string) error {
	if ref == "" {
		return nil
	}
	if d == nil || d.downloadAuth == nil {
		return errors.New("download auth registry is not configured")
	}

	return d.downloadAuth.Claim(ref, holderKey)
}

func (d *AddTaskDispatcher) ReleaseDownloadAuth(ref string, holderKey string) {
	if d == nil || d.downloadAuth == nil {
		return
	}
	d.downloadAuth.Release(ref, holderKey)
}

// ValidateDownloadAuthBinding applies the materialization admission checks
// for an item's ref without revealing the token.
func (d *AddTaskDispatcher) ValidateDownloadAuthBinding(item ResolvedAddItem) error {
	if d == nil || d.downloadAuth == nil {
		return errors.New("download auth registry is not configured")
	}
	if err := credentialedItemRequiresHTTPS(item); err != nil {
		return err
	}
	host, ok := ParseHTTPURLHost(item.URL)
	if !ok {
		return errors.New("item url has an unsafe or unsupported host")
	}

	return d.downloadAuth.ValidateRef(item.PackIdentity, item.DownloadAuthRef, host)
}

// InvalidateDownloadAuth drops every registry entry and zeroes the secrets.
func (d *AddTaskDispatcher) InvalidateDownloadAuth() {
	if d == nil || d.downloadAuth == nil {
		return
	}
	d.downloadAuth.Invalidate()
}

func IsGenericAuthResolutionError(err error) bool {
	return errors.Is(err, ErrGenericAuthResolution)
}

func isResolveTimeout(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	return ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
}

func (d *AddTaskDispatcher) BuildAria2Headers(ctx context.Context, item ResolvedAddItem) ([]string, error) {
	if d == nil {
		d = &AddTaskDispatcher{}
	}

	knownSecrets := querySecretValues(item.URL)
	// Credential materialization is https-only: a bearer or cookie header
	// must never be written onto a plaintext transport.
	if err := credentialedItemRequiresHTTPS(item); err != nil {
		return nil, err
	}
	headers := make([]string, 0, 2)
	if item.AuthProfileRef != "" {
		if err := validateResolvedAddItemAuthPolicy(item); err != nil {
			return nil, err
		}
		if d.authResolver == nil {
			return nil, redactErrorf("auth profile resolver is not configured for ref %q", item.AuthProfileRef)
		}
		if materializer, ok := d.authResolver.(authProfileMaterializer); ok && item.PackIdentity.PackID != "" && item.PackManifest.PackID != "" {
			material, err := materializer.MaterializeAuthProfile(ctx, HostAuthRuntimeRequest{
				PackIdentity: item.PackIdentity,
				Manifest:     cloneManifest(item.PackManifest),
				SourceURL:    item.SourceURL,
				TargetURL:    item.URL,
				ProfileRef:   AuthProfileID(item.AuthProfileRef),
			})
			if err != nil {
				return nil, redactedError(fmt.Errorf("resolve auth profile %q: %w", item.AuthProfileRef, err), knownSecrets...)
			}
			line := material.HeaderName + ": " + material.HeaderValue()
			if err := ValidateAria2HeaderLine(line); err != nil {
				return nil, redactedError(fmt.Errorf("resolve auth profile %q: %w", item.AuthProfileRef, err), appendNonEmptySecrets(knownSecrets, material.HeaderValue())...)
			}
			headers = append(headers, line)
			knownSecrets = appendNonEmptySecrets(knownSecrets, material.HeaderValue())
		} else {
			secret, err := d.authResolver.ResolveAuthProfile(ctx, item.PackID, AuthProfileID(item.AuthProfileRef), item.URL)
			if err != nil {
				return nil, redactedError(fmt.Errorf("resolve auth profile %q: %w", item.AuthProfileRef, err), knownSecrets...)
			}
			line := secret.HeaderName + ": " + secret.HeaderValue
			if err := ValidateAria2HeaderLine(line); err != nil {
				return nil, redactedError(fmt.Errorf("resolve auth profile %q: %w", item.AuthProfileRef, err), appendNonEmptySecrets(knownSecrets, secret.HeaderValue)...)
			}
			headers = append(headers, line)
			knownSecrets = appendNonEmptySecrets(knownSecrets, secret.HeaderValue)
		}
	}

	if item.DownloadAuthRef != "" {
		if d.downloadAuth == nil {
			return nil, redactErrorf("download auth registry is not configured")
		}
		host, ok := ParseHTTPURLHost(item.URL)
		if !ok {
			return nil, redactErrorf("item url has an unsafe or unsupported host")
		}
		token, err := d.downloadAuth.Materialize(item.PackIdentity, item.DownloadAuthRef, host)
		if err != nil {
			return nil, redactedError(fmt.Errorf("materialize download auth: %w", err), knownSecrets...)
		}
		line := "Authorization: Bearer " + token
		if err := ValidateAria2HeaderLine(line); err != nil {
			return nil, redactedError(fmt.Errorf("materialize download auth: %w", err), appendNonEmptySecrets(knownSecrets, token, "Bearer "+token)...)
		}
		headers = append(headers, line)
		knownSecrets = appendNonEmptySecrets(knownSecrets, token, "Bearer "+token)
	}

	if item.HeaderProfileRef != "" {
		if d.headerResolver == nil {
			return nil, redactErrorf("header profile resolver is not configured for ref %q", item.HeaderProfileRef)
		}

		resolved, err := d.headerResolver.ResolveHeaderProfile(ctx, item.PackID, item.HeaderProfileRef, item.URL)
		if err != nil {
			return nil, redactedError(fmt.Errorf("resolve header profile %q: %w", item.HeaderProfileRef, err), knownSecrets...)
		}
		if len(resolved) > maxAria2Headers {
			return nil, redactErrorf("resolve header profile %q: header count exceeds %d", item.HeaderProfileRef, maxAria2Headers)
		}
		for _, line := range resolved {
			if err := ValidateAria2HeaderLine(line); err != nil {
				return nil, redactedError(fmt.Errorf("resolve header profile %q: %w", item.HeaderProfileRef, err), knownSecrets...)
			}
			headers = append(headers, line)
		}
	}

	if len(headers) > maxAria2Headers {
		return nil, redactErrorf("aria2 header count exceeds %d", maxAria2Headers)
	}

	return dedupeStringsPreserveOrder(headers), nil
}

func ValidateResolvedAddItemAuthPolicy(item ResolvedAddItem) error {
	return validateResolvedAddItemAuthPolicy(item)
}

// credentialedItemRequiresHTTPS fails closed when an item carrying any
// credential ref does not target https. Materialized Authorization and
// cookie headers must never be written onto a plaintext transport; this
// check backs both the admission and materialization paths.
func credentialedItemRequiresHTTPS(item ResolvedAddItem) error {
	if item.DownloadAuthRef == "" && item.AuthProfileRef == "" && item.HeaderProfileRef == "" {
		return nil
	}
	parsed, err := url.Parse(item.URL)
	if err != nil || parsed.Scheme != "https" {
		return redactErrorf("credentialed item url must use https")
	}

	return nil
}

func validateResolvedAddItemAuthPolicy(item ResolvedAddItem) error {
	if err := credentialedItemRequiresHTTPS(item); err != nil {
		return err
	}
	if item.HostPolicy == nil {
		if isAliasManifest(item.PackManifest) {
			return redactErrorf("alias host policy is required for auth profile expansion")
		}

		return nil
	}
	profileID := AuthProfileID(item.AuthProfileRef)
	if err := validateAuthProfileID(profileID); err != nil {
		return redactedError(err)
	}
	_, host, err := parseSafeHTTPURL(item.URL)
	if err != nil {
		return err
	}
	if !policyAuthProfileMatchesHost(*item.HostPolicy, profileID, host) {
		return redactErrorf("auth profile is not allowed by alias host policy")
	}

	return nil
}

func (d *AddTaskDispatcher) resolvedItemsFromExtractOutput(sourceURL string, pack VerifiedPack, hostPolicy *ResolvedHostPolicy, output ExtractOutput) ([]ResolvedAddItem, error) {
	items := make([]ResolvedAddItem, 0, len(output.Items))
	for i, ref := range output.Items {
		if err := validateABIURL(ref.URL, "item url"); err != nil {
			return nil, fmt.Errorf("item %d url: %w", i, err)
		}
		if hostPolicy != nil {
			if err := policyAllowsOutputURL(*hostPolicy, ref.URL); err != nil {
				return nil, fmt.Errorf("item %d url: %w", i, err)
			}
		}
		if ref.DownloadAuthRef != "" || ref.AuthProfileRef != "" || ref.HeaderProfileRef != "" {
			if parsed, err := url.Parse(ref.URL); err != nil || parsed.Scheme != "https" {
				return nil, fmt.Errorf("item %d url must use https for credentialed downloads", i)
			}
			// Legacy manifests scope pack-issued credentials to the declared
			// domains; alias manifests already passed the resolved host
			// output policy above. Uncredentialed items keep domain freedom.
			if !isAliasManifest(pack.Manifest) {
				host, ok := ParseHTTPURLHost(ref.URL)
				if !ok || !manifestMatchesHost(pack.Manifest, host) {
					return nil, fmt.Errorf("item %d url host is outside the pack's declared domains for credentialed downloads", i)
				}
			}
		}
		if ref.DownloadAuthRef != "" {
			if err := validateDownloadAuthRef(ref.DownloadAuthRef); err != nil {
				return nil, fmt.Errorf("item %d download_auth_ref: %w", i, err)
			}
			if !manifestHasCapability(pack.Manifest, CapabilityDownloadAuth) {
				return nil, fmt.Errorf("item %d download_auth_ref requires capability %s", i, CapabilityDownloadAuth)
			}
			if ref.AuthProfileRef != "" || ref.HeaderProfileRef != "" {
				return nil, fmt.Errorf("item %d download_auth_ref must not combine with auth_profile_ref or header_profile_ref", i)
			}
			host, ok := ParseHTTPURLHost(ref.URL)
			if !ok {
				return nil, fmt.Errorf("item %d url has an unsafe or unsupported host", i)
			}
			if err := d.bindDownloadAuth(output.InvocationID(), ref.DownloadAuthRef, pack.Identity, host); err != nil {
				return nil, fmt.Errorf("item %d download_auth_ref: %w", i, err)
			}
		}

		filename := ""
		if ref.Filename != "" {
			safe, err := SafeAria2OutFilename(ref.Filename)
			if err != nil {
				return nil, fmt.Errorf("item %d filename: %w", i, err)
			}
			filename = safe
		}

		metadata := make(map[string]string, len(ref.Metadata))
		maps.Copy(metadata, ref.Metadata)

		items = append(items, ResolvedAddItem{
			SourceURL:        sourceURL,
			PackID:           pack.Manifest.PackID,
			PackManifest:     cloneManifest(pack.Manifest),
			PackIdentity:     pack.Identity,
			HostPolicy:       cloneResolvedHostPolicyPtr(hostPolicy),
			ID:               ref.ID,
			URL:              ref.URL,
			Filename:         filename,
			SizeBytes:        ref.SizeBytes,
			AuthProfileRef:   ref.AuthProfileRef,
			HeaderProfileRef: ref.HeaderProfileRef,
			DownloadAuthRef:  ref.DownloadAuthRef,
			MimeType:         ref.MimeType,
			Metadata:         metadata,
		})
	}

	return items, nil
}

func (d *AddTaskDispatcher) bindDownloadAuth(invocation uint64, ref string, pack VerifiedPackIdentity, host string) error {
	if d == nil || d.downloadAuth == nil {
		return errors.New("download auth registry is not configured")
	}

	return d.downloadAuth.Bind(invocation, ref, pack, host)
}

func CloneResolvedAddItem(item ResolvedAddItem) ResolvedAddItem {
	out := item
	out.PackManifest = cloneManifest(item.PackManifest)
	out.HostPolicy = cloneResolvedHostPolicyPtr(item.HostPolicy)
	if item.Metadata != nil {
		out.Metadata = make(map[string]string, len(item.Metadata))
		maps.Copy(out.Metadata, item.Metadata)
	}

	return out
}

func ValidateLeaseOutputURL(item ResolvedAddItem) error {
	if err := validateABIURL(item.URL, "item url"); err != nil {
		return err
	}
	parsed, err := url.Parse(item.URL)
	if err != nil || parsed.Scheme != "https" {
		return errors.New("item url must use https")
	}
	if isAliasManifest(item.PackManifest) && item.HostPolicy == nil {
		return errors.New("alias pack requires host policy")
	}
	if item.HostPolicy != nil {
		return policyAllowsOutputURL(*item.HostPolicy, item.URL)
	}
	if item.DownloadAuthRef != "" || item.AuthProfileRef != "" || item.HeaderProfileRef != "" {
		// Mirror the extract-admission scope for legacy manifests: a
		// pack-issued credential may only target the declared domains.
		host, ok := ParseHTTPURLHost(item.URL)
		if !ok || !manifestMatchesHost(item.PackManifest, host) {
			return errors.New("credentialed item url host is outside the pack's declared domains")
		}
	}

	return nil
}

func cloneResolvedHostPolicyPtr(policy *ResolvedHostPolicy) *ResolvedHostPolicy {
	if policy == nil {
		return nil
	}
	cloned := cloneResolvedHostPolicy(*policy)

	return &cloned
}

func SafeAria2OutFilename(filename string) (string, error) {
	trimmed := strings.TrimSpace(filename)
	if trimmed == "" {
		return "", errors.New("filename must be non-empty after trim")
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return "", errors.New("filename must not contain CR/LF")
	}
	if strings.ContainsAny(trimmed, `/\\`) {
		return "", errors.New("filename must not contain path separators")
	}
	if trimmed == "." || trimmed == ".." || strings.Contains(trimmed, "..") {
		return "", errors.New("filename must not contain dot-dot path traversal")
	}
	if filepath.IsAbs(trimmed) || (runtime.GOOS != "windows" && isWindowsAbsPath(trimmed)) {
		return "", errors.New("filename must not be an absolute path")
	}
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		return "", errors.New("filename must not contain a drive prefix")
	}
	if filepath.Base(trimmed) != trimmed {
		return "", errors.New("filename must be a base name")
	}

	return trimmed, nil
}

func isWindowsAbsPath(value string) bool {
	if len(value) >= 3 && isASCIILetter(value[0]) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}

	return strings.HasPrefix(value, `\\`)
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func ValidateAria2HeaderLine(line string) error {
	if line == "" || strings.TrimSpace(line) != line {
		return errors.New("header line must be non-empty and trimmed")
	}
	if len(line) > maxAria2HeaderLineBytes {
		return fmt.Errorf("header line exceeds %d bytes", maxAria2HeaderLineBytes)
	}
	if strings.ContainsAny(line, "\r\n") {
		return errors.New("header line must not contain CR/LF")
	}
	name, value, ok := strings.Cut(line, ":")
	if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
		return errors.New("header line must be in name: value form")
	}
	if strings.TrimSpace(name) != name {
		return errors.New("header name must be trimmed")
	}
	for _, r := range name {
		if r <= 0x20 || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
			return errors.New("header name contains invalid characters")
		}
	}

	return nil
}

func dedupeStringsPreserveOrder(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

func safePackError(packID string, err error) string {
	return redactedError(fmt.Errorf("pack %q: %w", packID, err)).Error()
}

func querySecretValues(rawURL string) []string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	secrets := make([]string, 0)
	for key, values := range parsed.Query() {
		if _, ok := tokenLikeQueryKeys[strings.ToLower(key)]; !ok {
			continue
		}
		for _, value := range values {
			if value != "" {
				secrets = append(secrets, value)
			}
		}
	}

	return secrets
}
