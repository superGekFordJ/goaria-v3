package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxHostPolicyEndpointParams        = 16
	maxHostPolicyEndpointParamKeyLen   = 32
	maxHostPolicyEndpointParamValueLen = 512
)

type HostPolicyResolver interface {
	ResolveHostPolicy(ctx context.Context, request HostPolicyRequest) (ResolvedHostPolicy, error)
}

type HostPolicyRequest struct {
	PackIdentity VerifiedPackIdentity
	Manifest     Manifest
}

type ResolvedHostPolicy struct {
	PolicyID            string
	PolicyVersion       string
	PolicySHA256        string
	PackIdentity        VerifiedPackIdentity
	DomainPolicyRefs    []string
	BrokerPolicyRefs    []string
	AllowedCapabilities []Capability
	IngressDomains      []DomainRule
	BrokerDomains       []DomainRule
	OutputDomains       []HostPolicyOutputRule
	AuthProfiles        []HostPolicyAuthProfileScope
	Endpoints           []HostPolicyEndpoint
}

type HostPolicyOutputRule struct {
	Host              string
	IncludeSubdomains bool
	PathPrefixes      []string
}

type HostPolicyAuthProfileScope struct {
	ProfileID AuthProfileID
	Domains   []DomainRule
}

type HostPolicyEndpoint struct {
	BrokerPolicyRef  string
	EndpointRef      string
	URLTemplate      string
	Methods          []string
	AuthProfileRefs  []AuthProfileID
	TimeoutMillis    int
	MaxResponseBytes int64
}

func isAliasManifest(manifest Manifest) bool {
	return len(manifest.DomainPolicyRefs) > 0 && len(manifest.Domains) == 0
}

func resolveAliasHostPolicy(ctx context.Context, resolver HostPolicyResolver, identity VerifiedPackIdentity, manifest Manifest) (ResolvedHostPolicy, error) {
	if !isAliasManifest(manifest) {
		return ResolvedHostPolicy{}, errors.New("host policy resolution requires an alias manifest")
	}
	if resolver == nil {
		return ResolvedHostPolicy{}, errors.New("host policy resolver is required for alias manifest")
	}
	if identity == (VerifiedPackIdentity{}) {
		return ResolvedHostPolicy{}, errors.New("verified pack identity is required for alias manifest")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resolved, err := resolver.ResolveHostPolicy(ctx, HostPolicyRequest{
		PackIdentity: identity,
		Manifest:     cloneManifest(manifest),
	})
	if err != nil {
		return ResolvedHostPolicy{}, errors.New("host policy resolution denied")
	}
	if err := validateResolvedHostPolicy(identity, manifest, resolved); err != nil {
		return ResolvedHostPolicy{}, err
	}

	return cloneResolvedHostPolicy(resolved), nil
}

func validateResolvedHostPolicy(identity VerifiedPackIdentity, manifest Manifest, policy ResolvedHostPolicy) error {
	if err := validateOpaquePolicyRef("policy_id", policy.PolicyID); err != nil {
		return errors.New("resolved host policy is invalid")
	}
	if strings.TrimSpace(policy.PolicyVersion) == "" || strings.TrimSpace(policy.PolicyVersion) != policy.PolicyVersion {
		return errors.New("resolved host policy is invalid")
	}
	if err := validateLowerHexSHA256Field("policy_sha256", policy.PolicySHA256); err != nil {
		return errors.New("resolved host policy is invalid")
	}
	if policy.PackIdentity != identity {
		return errors.New("resolved host policy does not match verified pack identity")
	}
	if policy.PackIdentity.PackID != manifest.PackID || policy.PackIdentity.PackVersion != manifest.PackVersion {
		return errors.New("resolved host policy does not match manifest identity")
	}
	if !sameStringSet(policy.DomainPolicyRefs, manifest.DomainPolicyRefs) {
		return errors.New("resolved host policy does not match manifest domain policy refs")
	}
	if !sameStringSet(policy.BrokerPolicyRefs, manifest.BrokerPolicyRefs) {
		return errors.New("resolved host policy does not match manifest broker policy refs")
	}
	if err := validateOpaquePolicyRefs("domain_policy_refs", policy.DomainPolicyRefs); err != nil {
		return errors.New("resolved host policy is invalid")
	}
	if err := validateOpaquePolicyRefs("broker_policy_refs", policy.BrokerPolicyRefs); err != nil {
		return errors.New("resolved host policy is invalid")
	}
	if err := validateHostPolicyCapabilities(manifest, policy.AllowedCapabilities); err != nil {
		return err
	}
	if len(policy.IngressDomains) == 0 {
		return errors.New("resolved host policy ingress domain rules are required")
	}
	if err := validateDomainRules(policy.IngressDomains); err != nil {
		return errors.New("resolved host policy ingress domain rules are invalid")
	}
	if len(policy.OutputDomains) == 0 {
		return errors.New("resolved host policy output domain rules are required")
	}
	if err := validateHostPolicyOutputDomains(policy.OutputDomains); err != nil {
		return err
	}
	requiresBroker := ManifestHasCapability(manifest, CapabilityHTTPFetch) || ManifestHasCapability(manifest, CapabilityAuthProfile)
	if requiresBroker {
		if len(policy.BrokerDomains) == 0 {
			return errors.New("resolved host policy broker domain rules are required")
		}
		if err := validateDomainRules(policy.BrokerDomains); err != nil {
			return errors.New("resolved host policy broker domain rules are invalid")
		}
	} else if len(policy.BrokerDomains) > 0 {
		return errors.New("resolved host policy broker domain rules require broker capabilities")
	}
	if err := validateHostPolicyAuthProfiles(policy.AuthProfiles, policy.AllowedCapabilities); err != nil {
		return err
	}
	if requiresBroker {
		if len(policy.Endpoints) == 0 {
			return errors.New("resolved host policy endpoints are required")
		}
		if err := validateHostPolicyEndpoints(manifest, policy); err != nil {
			return err
		}
	} else if len(policy.Endpoints) > 0 {
		return errors.New("resolved host policy endpoints require broker capabilities")
	}

	return nil
}

func validateHostPolicyOutputDomains(rules []HostPolicyOutputRule) error {
	for _, rule := range rules {
		domain := DomainRule{Host: rule.Host, IncludeSubdomains: rule.IncludeSubdomains}
		if err := validateDomainRule(domain); err != nil {
			return errors.New("resolved host policy output domain rule is invalid")
		}
		if len(rule.PathPrefixes) == 0 {
			return errors.New("resolved host policy output path prefixes are required")
		}
		seen := make(map[string]struct{}, len(rule.PathPrefixes))
		for _, prefix := range rule.PathPrefixes {
			if err := validateHostPolicyOutputPathPrefix(prefix); err != nil {
				return err
			}
			if _, ok := seen[prefix]; ok {
				return errors.New("resolved host policy output path prefixes contain duplicate entries")
			}
			seen[prefix] = struct{}{}
		}
	}

	return nil
}

func validateHostPolicyOutputPathPrefix(prefix string) error {
	if prefix == "" || strings.TrimSpace(prefix) != prefix {
		return errors.New("resolved host policy output path prefix must be non-empty and trimmed")
	}
	if !strings.HasPrefix(prefix, "/") {
		return errors.New("resolved host policy output path prefix must start with slash")
	}
	if prefix != "/" && !strings.HasSuffix(prefix, "/") {
		return errors.New("resolved host policy output path prefix must end with slash")
	}
	if strings.ContainsAny(prefix, "\\?#%") || stringContainsControl(prefix) {
		return errors.New("resolved host policy output path prefix contains unsafe syntax")
	}
	if strings.Contains(prefix, "//") || strings.Contains(prefix, "/./") || strings.Contains(prefix, "/../") || strings.Contains(prefix, "..") {
		return errors.New("resolved host policy output path prefix contains unsafe path segments")
	}

	return nil
}

func validateHostPolicyEndpoints(manifest Manifest, policy ResolvedHostPolicy) error {
	seen := make(map[string]map[string]struct{}, len(policy.Endpoints))
	for _, endpoint := range policy.Endpoints {
		if err := validateOpaquePolicyRef("broker_policy_ref", endpoint.BrokerPolicyRef); err != nil {
			return errors.New("resolved host policy endpoint is invalid")
		}
		if !stringSliceContains(policy.BrokerPolicyRefs, endpoint.BrokerPolicyRef) || !stringSliceContains(manifest.BrokerPolicyRefs, endpoint.BrokerPolicyRef) {
			return errors.New("resolved host policy endpoint broker ref is not declared")
		}
		if err := validateOpaquePolicyRef("endpoint_ref", endpoint.EndpointRef); err != nil {
			return errors.New("resolved host policy endpoint is invalid")
		}
		byBroker := seen[endpoint.BrokerPolicyRef]
		if byBroker == nil {
			byBroker = make(map[string]struct{})
			seen[endpoint.BrokerPolicyRef] = byBroker
		}
		if _, ok := byBroker[endpoint.EndpointRef]; ok {
			return errors.New("resolved host policy endpoint refs must be unique per broker ref")
		}
		byBroker[endpoint.EndpointRef] = struct{}{}

		if _, _, err := validateHostPolicyEndpointURLTemplate(policy, endpoint.URLTemplate); err != nil {
			return err
		}
		if _, err := normalizeHostPolicyEndpointMethodNames(endpoint.Methods); err != nil {
			return err
		}
		if err := validateHostPolicyEndpointAuthRefs(endpoint.AuthProfileRefs); err != nil {
			return err
		}
		if endpoint.TimeoutMillis < 0 {
			return errors.New("resolved host policy endpoint timeout must not be negative")
		}
		if endpoint.MaxResponseBytes < 0 {
			return errors.New("resolved host policy endpoint max response bytes must not be negative")
		}
	}

	return nil
}

func validateHostPolicyEndpointURLTemplate(policy ResolvedHostPolicy, template string) (*url.URL, map[string]struct{}, error) {
	if template == "" || strings.TrimSpace(template) != template {
		return nil, nil, errors.New("resolved host policy endpoint url template must be non-empty and trimmed")
	}
	if stringContainsControl(template) {
		return nil, nil, errors.New("resolved host policy endpoint url template contains control characters")
	}
	parsed, host, err := parseSafeHTTPURL(template)
	if err != nil {
		return nil, nil, errors.New("resolved host policy endpoint url template is invalid")
	}
	if !policyBrokerMatchesHost(policy, host) {
		return nil, nil, errors.New("resolved host policy endpoint url template host is not allowed by broker policy")
	}
	placeholders, err := hostPolicyEndpointPlaceholders(template)
	if err != nil {
		return nil, nil, err
	}
	if hostPolicyEndpointTemplateContainsPlaceholder(parsed.Scheme) || hostPolicyEndpointTemplateContainsPlaceholder(parsed.Host) {
		return nil, nil, errors.New("resolved host policy endpoint url template must use a concrete host")
	}

	return parsed, placeholders, nil
}

func normalizeHostPolicyEndpointMethods(methods []string, policy HTTPBrokerPolicy) ([]string, error) {
	normalized, err := normalizeHostPolicyEndpointMethodNames(methods)
	if err != nil || len(normalized) == 0 {
		return normalized, err
	}
	allowed := policy.AllowedMethods
	if len(allowed) == 0 {
		allowed = DefaultHTTPBrokerPolicy().AllowedMethods
	}
	for _, method := range normalized {
		if _, ok := allowed[method]; !ok {
			return nil, fmt.Errorf("resolved host policy endpoint method %q is not allowed", method)
		}
	}

	return normalized, nil
}

func normalizeHostPolicyEndpointMethodNames(methods []string) ([]string, error) {
	if len(methods) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(methods))
	normalized := make([]string, 0, len(methods))
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || strings.ContainsAny(method, " \t\r\n") {
			return nil, errors.New("resolved host policy endpoint method is invalid")
		}
		if _, ok := seen[method]; ok {
			return nil, errors.New("resolved host policy endpoint methods contain duplicate entries")
		}
		seen[method] = struct{}{}
		normalized = append(normalized, method)
	}

	return normalized, nil
}

func validateHostPolicyEndpointAuthRefs(refs []AuthProfileID) error {
	seen := make(map[AuthProfileID]struct{}, len(refs))
	for _, ref := range refs {
		if err := validateAuthProfileID(ref); err != nil {
			return errors.New("resolved host policy endpoint auth profile ref is invalid")
		}
		if _, ok := seen[ref]; ok {
			return errors.New("resolved host policy endpoint auth profile refs contain duplicate entries")
		}
		seen[ref] = struct{}{}
	}

	return nil
}

func validateHostPolicyCapabilities(manifest Manifest, capabilities []Capability) error {
	if len(capabilities) == 0 {
		return errors.New("resolved host policy allowed capabilities are required")
	}
	seen := make(map[Capability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if capability == "" {
			return errors.New("resolved host policy allowed capability is invalid")
		}
		if _, ok := seen[capability]; ok {
			return errors.New("resolved host policy allowed capabilities contain duplicate entries")
		}
		seen[capability] = struct{}{}
		if !ManifestHasCapability(manifest, capability) {
			return errors.New("resolved host policy allowed capability is not declared by manifest")
		}
	}

	return nil
}

func validateHostPolicyAuthProfiles(scopes []HostPolicyAuthProfileScope, capabilities []Capability) error {
	if len(scopes) == 0 {
		return nil
	}
	if !capabilitySliceContains(capabilities, CapabilityAuthProfile) {
		return errors.New("resolved host policy auth profile scopes require auth capability")
	}
	seen := make(map[AuthProfileID]struct{}, len(scopes))
	for _, scope := range scopes {
		if err := validateAuthProfileID(scope.ProfileID); err != nil {
			return errors.New("resolved host policy auth profile scope is invalid")
		}
		if _, ok := seen[scope.ProfileID]; ok {
			return errors.New("resolved host policy auth profile scopes contain duplicate entries")
		}
		seen[scope.ProfileID] = struct{}{}
		if len(scope.Domains) == 0 {
			return errors.New("resolved host policy auth profile domain rules are required")
		}
		if err := validateDomainRules(scope.Domains); err != nil {
			return errors.New("resolved host policy auth profile domain rules are invalid")
		}
	}

	return nil
}

func policyAllowsCapability(policy ResolvedHostPolicy, capability Capability) bool {
	return capabilitySliceContains(policy.AllowedCapabilities, capability)
}

func capabilitySliceContains(capabilities []Capability, capability Capability) bool {
	for _, candidate := range capabilities {
		if candidate == capability {
			return true
		}
	}

	return false
}

func policyIngressMatchesHost(policy ResolvedHostPolicy, host string) bool {
	return domainRulesMatchHost(policy.IngressDomains, host)
}

func policyBrokerMatchesHost(policy ResolvedHostPolicy, host string) bool {
	return domainRulesMatchHost(policy.BrokerDomains, host)
}

func policyAllowsOutputURL(policy ResolvedHostPolicy, rawURL string) error {
	parsed, host, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" {
		return redactErrorf("output url must use https")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return redactErrorf("output url must not include query or fragment")
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	for _, rule := range policy.OutputDomains {
		domain := DomainRule{Host: rule.Host, IncludeSubdomains: rule.IncludeSubdomains}
		if !matchesDomainRule(host, domain) {
			continue
		}
		for _, prefix := range rule.PathPrefixes {
			if prefix == "/" || strings.HasPrefix(path, prefix) {
				return nil
			}
		}
	}

	return redactErrorf("url is not allowed by alias output policy")
}

func policyAuthProfileMatchesHost(policy ResolvedHostPolicy, profileID AuthProfileID, host string) bool {
	for _, scope := range policy.AuthProfiles {
		if scope.ProfileID == profileID {
			return domainRulesMatchHost(scope.Domains, host)
		}
	}

	return false
}

func resolveHostPolicyEndpoint(policy ResolvedHostPolicy, brokerPolicyRef string, endpointRef string) (HostPolicyEndpoint, bool) {
	for _, endpoint := range policy.Endpoints {
		if endpoint.BrokerPolicyRef == brokerPolicyRef && endpoint.EndpointRef == endpointRef {
			return cloneHostPolicyEndpoint(endpoint), true
		}
	}

	return HostPolicyEndpoint{}, false
}

func validateHostPolicyEndpointRequest(endpoint HostPolicyEndpoint, method string, authProfileRef AuthProfileID, manifest Manifest, brokerPolicy HTTPBrokerPolicy) (string, int, int64, error) {
	normalizedMethod, err := normalizeHostImportMethod(method)
	if err != nil {
		return "", 0, 0, err
	}
	normalizedEndpointMethods, err := normalizeHostPolicyEndpointMethods(endpoint.Methods, brokerPolicy)
	if err != nil {
		return "", 0, 0, err
	}
	if len(normalizedEndpointMethods) > 0 && !stringSliceContains(normalizedEndpointMethods, normalizedMethod) {
		return "", 0, 0, errors.New("http method is not allowed by host policy endpoint")
	}
	if authProfileRef != "" && !hostPolicyEndpointAllowsAuthProfile(endpoint, authProfileRef) {
		return "", 0, 0, errors.New("auth profile is not allowed by host policy endpoint")
	}
	if err := validateHostPolicyEndpointResourceCaps(endpoint, manifest, brokerPolicy); err != nil {
		return "", 0, 0, err
	}

	return normalizedMethod, endpoint.TimeoutMillis, endpoint.MaxResponseBytes, nil
}

func validateHostPolicyEndpointResourceCaps(endpoint HostPolicyEndpoint, manifest Manifest, brokerPolicy HTTPBrokerPolicy) error {
	if endpoint.TimeoutMillis < 0 {
		return errors.New("endpoint timeout_millis must not be negative")
	}
	if endpoint.TimeoutMillis > 0 {
		brokerMaxMillis := int(brokerPolicy.MaxTimeout / time.Millisecond)
		if manifest.ResourceLimits.TimeoutMillis > 0 && endpoint.TimeoutMillis > manifest.ResourceLimits.TimeoutMillis {
			return errors.New("endpoint timeout_millis exceeds manifest resource limit")
		}
		if brokerMaxMillis > 0 && endpoint.TimeoutMillis > brokerMaxMillis {
			return errors.New("endpoint timeout_millis exceeds broker resource limit")
		}
	}
	if endpoint.MaxResponseBytes < 0 {
		return errors.New("endpoint max_response_bytes must not be negative")
	}
	if endpoint.MaxResponseBytes > 0 {
		if manifest.ResourceLimits.MaxResponseBytes > 0 && endpoint.MaxResponseBytes > manifest.ResourceLimits.MaxResponseBytes {
			return errors.New("endpoint max_response_bytes exceeds manifest resource limit")
		}
		if brokerPolicy.MaxResponseBytes > 0 && endpoint.MaxResponseBytes > brokerPolicy.MaxResponseBytes {
			return errors.New("endpoint max_response_bytes exceeds broker resource limit")
		}
	}

	return nil
}

func hostPolicyEndpointAllowsAuthProfile(endpoint HostPolicyEndpoint, authProfileRef AuthProfileID) bool {
	for _, allowed := range endpoint.AuthProfileRefs {
		if allowed == authProfileRef {
			return true
		}
	}

	return false
}

func expandHostPolicyEndpointURL(policy ResolvedHostPolicy, endpoint HostPolicyEndpoint, params map[string]string) (string, error) {
	_, placeholders, err := validateHostPolicyEndpointURLTemplate(policy, endpoint.URLTemplate)
	if err != nil {
		return "", err
	}
	validatedParams, err := validateHostPolicyEndpointParams(params)
	if err != nil {
		return "", err
	}
	for key := range validatedParams {
		if _, ok := placeholders[key]; !ok {
			return "", fmt.Errorf("unknown endpoint param %q", key)
		}
	}
	for key := range placeholders {
		if _, ok := validatedParams[key]; !ok {
			return "", fmt.Errorf("missing endpoint param %q", key)
		}
	}

	expanded := endpoint.URLTemplate
	for key, value := range validatedParams {
		expanded = strings.ReplaceAll(expanded, "{"+key+"}", url.PathEscape(value))
	}
	if hostPolicyEndpointTemplateContainsPlaceholder(expanded) {
		return "", errors.New("endpoint url contains unresolved placeholders")
	}
	_, host, err := parseSafeHTTPURL(expanded)
	if err != nil {
		return "", err
	}
	if !policyBrokerMatchesHost(policy, host) {
		return "", errors.New("expanded endpoint url is not allowed by broker policy")
	}

	return expanded, nil
}

func validateHostPolicyEndpointParams(params map[string]string) (map[string]string, error) {
	if len(params) == 0 {
		return nil, nil
	}
	if len(params) > maxHostPolicyEndpointParams {
		return nil, fmt.Errorf("params must contain at most %d entries", maxHostPolicyEndpointParams)
	}
	validated := make(map[string]string, len(params))
	for key, value := range params {
		if err := validateHostPolicyEndpointParamKey(key); err != nil {
			return nil, err
		}
		if err := validateHostPolicyEndpointParamValue(value); err != nil {
			return nil, err
		}
		if _, ok := validated[key]; ok {
			return nil, errors.New("params contain duplicate keys")
		}
		validated[key] = value
	}

	return validated, nil
}

func validateHostPolicyEndpointParamKey(key string) error {
	if len(key) < 1 || len(key) > maxHostPolicyEndpointParamKeyLen {
		return fmt.Errorf("param key length must be between 1 and %d bytes", maxHostPolicyEndpointParamKeyLen)
	}
	if !utf8.ValidString(key) || strings.TrimSpace(key) != key || stringContainsControl(key) {
		return errors.New("param key must be valid UTF-8, trimmed, and control-free")
	}
	if !isHostPolicyEndpointPlaceholderKey(key) {
		return fmt.Errorf("param key %q is invalid", key)
	}
	if isHostPolicyEndpointSensitiveKey(key) {
		return fmt.Errorf("param key %q is reserved", key)
	}

	return nil
}

func validateHostPolicyEndpointParamValue(value string) error {
	if len(value) < 1 || len(value) > maxHostPolicyEndpointParamValueLen {
		return fmt.Errorf("param value length must be between 1 and %d bytes", maxHostPolicyEndpointParamValueLen)
	}
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || stringContainsControl(value) {
		return errors.New("param value must be valid UTF-8, trimmed, and control-free")
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, "/\\?#@%&=;:") {
		return errors.New("param value contains reserved URL syntax")
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "basic ") || strings.Contains(lower, "authorization:") || strings.Contains(lower, "cookie:") {
		return errors.New("param value contains credential-looking syntax")
	}

	return nil
}

func hostPolicyEndpointPlaceholders(template string) (map[string]struct{}, error) {
	placeholders := make(map[string]struct{})
	for i := 0; i < len(template); i++ {
		switch template[i] {
		case '{':
			end := strings.IndexByte(template[i+1:], '}')
			if end < 0 {
				return nil, errors.New("endpoint url template contains an unterminated placeholder")
			}
			key := template[i+1 : i+1+end]
			if err := validateHostPolicyEndpointParamKey(key); err != nil {
				return nil, errors.New("endpoint url template contains an invalid placeholder")
			}
			placeholders[key] = struct{}{}
			i += end + 1
		case '}':
			return nil, errors.New("endpoint url template contains an unmatched placeholder terminator")
		}
		if len(placeholders) > maxHostPolicyEndpointParams {
			return nil, fmt.Errorf("endpoint url template must contain at most %d placeholders", maxHostPolicyEndpointParams)
		}
	}

	return placeholders, nil
}

func isHostPolicyEndpointPlaceholderKey(key string) bool {
	if key == "" {
		return false
	}
	if !isLowerSlugEdge(key[0]) || !isLowerSlugEdge(key[len(key)-1]) {
		return false
	}
	for i := 1; i < len(key)-1; i++ {
		if !isLowerSlugEdge(key[i]) && key[i] != '-' {
			return false
		}
	}

	return true
}

func isHostPolicyEndpointSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	if lower == "key" || lower == "api-key" || lower == "apikey" {
		return true
	}
	for _, marker := range []string{"token", "secret", "auth", "cookie", "header", "credential", "password", "passwd", "bearer", "session"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	return false
}

func stringContainsControl(value string) bool {
	for _, r := range value {
		if r == 0 || r < 0x20 || r == 0x7f {
			return true
		}
	}

	return false
}

func hostPolicyEndpointTemplateContainsPlaceholder(value string) bool {
	return strings.ContainsAny(value, "{}")
}

func normalizeHostImportMethod(method string) (string, error) {
	if method == "" {
		return http.MethodGet, nil
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || strings.ContainsAny(method, " \t\r\n") {
		return "", fmt.Errorf("unsupported http method %q", method)
	}

	return method, nil
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func cloneResolvedHostPolicy(policy ResolvedHostPolicy) ResolvedHostPolicy {
	policy.DomainPolicyRefs = cloneStringSlice(policy.DomainPolicyRefs)
	policy.BrokerPolicyRefs = cloneStringSlice(policy.BrokerPolicyRefs)
	policy.AllowedCapabilities = append([]Capability(nil), policy.AllowedCapabilities...)
	policy.IngressDomains = cloneDomainRules(policy.IngressDomains)
	policy.BrokerDomains = cloneDomainRules(policy.BrokerDomains)
	policy.OutputDomains = cloneHostPolicyOutputRules(policy.OutputDomains)
	if policy.AuthProfiles != nil {
		policy.AuthProfiles = append([]HostPolicyAuthProfileScope(nil), policy.AuthProfiles...)
		for i := range policy.AuthProfiles {
			policy.AuthProfiles[i].Domains = cloneDomainRules(policy.AuthProfiles[i].Domains)
		}
	}
	if policy.Endpoints != nil {
		policy.Endpoints = append([]HostPolicyEndpoint(nil), policy.Endpoints...)
		for i := range policy.Endpoints {
			policy.Endpoints[i] = cloneHostPolicyEndpoint(policy.Endpoints[i])
		}
	}

	return policy
}

func cloneHostPolicyOutputRules(rules []HostPolicyOutputRule) []HostPolicyOutputRule {
	if rules == nil {
		return nil
	}
	cloned := append([]HostPolicyOutputRule(nil), rules...)
	for i := range cloned {
		cloned[i].PathPrefixes = cloneStringSlice(cloned[i].PathPrefixes)
	}

	return cloned
}

func cloneHostPolicyEndpoint(endpoint HostPolicyEndpoint) HostPolicyEndpoint {
	endpoint.Methods = cloneStringSlice(endpoint.Methods)
	if endpoint.AuthProfileRefs != nil {
		endpoint.AuthProfileRefs = append([]AuthProfileID(nil), endpoint.AuthProfileRefs...)
	}

	return endpoint
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[value]; !ok {
			return false
		}
	}

	return true
}
