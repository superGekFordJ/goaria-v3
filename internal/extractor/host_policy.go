package extractor

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var brokerEndpointParamPattern = regexp.MustCompile(`\{([a-zA-Z0-9_-]+)\}`)

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
	BrokerEndpoints     []HostPolicyBrokerEndpoint
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

type HostPolicyBrokerEndpoint struct {
	BrokerPolicyRef  string
	EndpointRef      string
	URLTemplate      string
	Methods          []string
	AuthProfileRefs  []string
	TimeoutMillis    int
	MaxResponseBytes int64
}

func isAliasManifest(manifest Manifest) bool {
	return len(manifest.DomainPolicyRefs) > 0 && len(manifest.Domains) == 0
}

func resolveAliasHostPolicy(ctx context.Context, resolver HostPolicyResolver, identity VerifiedPackIdentity, manifest Manifest) (ResolvedHostPolicy, error) {
	if !isAliasManifest(manifest) {
		return ResolvedHostPolicy{}, errors.New("host policy requires alias manifest")
	}
	if resolver == nil {
		return ResolvedHostPolicy{}, errors.New("host policy resolver is not configured")
	}
	if !hasVerifiedPackIdentity(identity) {
		return ResolvedHostPolicy{}, errors.New("verified pack identity is incomplete")
	}

	request := HostPolicyRequest{
		PackIdentity: identity,
		Manifest:     cloneManifest(manifest),
	}
	policy, err := resolver.ResolveHostPolicy(ctx, request)
	if err != nil {
		return ResolvedHostPolicy{}, errors.New("host policy resolver failed")
	}
	if err := validateResolvedHostPolicy(identity, manifest, policy); err != nil {
		return ResolvedHostPolicy{}, err
	}

	return cloneResolvedHostPolicy(policy), nil
}

func validateResolvedHostPolicy(identity VerifiedPackIdentity, manifest Manifest, policy ResolvedHostPolicy) error {
	if err := validateOpaquePolicyRef("policy_id", policy.PolicyID); err != nil {
		return err
	}
	if policy.PolicyVersion == "" || strings.TrimSpace(policy.PolicyVersion) != policy.PolicyVersion {
		return errors.New("host policy version must be non-empty and trimmed")
	}
	if err := validateSHA256Hex("policy_sha256", policy.PolicySHA256); err != nil {
		return err
	}
	if !hasVerifiedPackIdentity(identity) || policy.PackIdentity != identity {
		return errors.New("host policy identity does not match verified pack")
	}
	if identity.PackID != manifest.PackID || identity.PackVersion != manifest.PackVersion {
		return errors.New("verified pack identity does not match manifest")
	}
	if !samePolicyRefSet(manifest.DomainPolicyRefs, policy.DomainPolicyRefs) {
		return errors.New("host policy domain refs do not match manifest")
	}
	if !samePolicyRefSet(manifest.BrokerPolicyRefs, policy.BrokerPolicyRefs) {
		return errors.New("host policy broker refs do not match manifest")
	}
	if err := validateCapabilities(policy.AllowedCapabilities, capabilitiesAllowedByManifest(manifest)); err != nil {
		return errors.New("host policy capabilities are not allowed by manifest")
	}
	if err := validateDomainRules(policy.IngressDomains); err != nil {
		return errors.New("host policy ingress domains are invalid")
	}
	if len(policy.OutputDomains) == 0 {
		return errors.New("host policy output domains are required")
	}
	if err := validateHostPolicyOutputDomains(policy.OutputDomains); err != nil {
		return err
	}
	if len(policy.BrokerDomains) > 0 {
		if err := validateDomainRules(policy.BrokerDomains); err != nil {
			return errors.New("host policy broker domains are invalid")
		}
	}
	if err := validateHostPolicyAuthProfiles(policy.AuthProfiles, policy.AllowedCapabilities); err != nil {
		return err
	}
	needsBrokerEndpoints := manifestHasCapability(manifest, CapabilityHTTPFetch) || manifestHasCapability(manifest, CapabilityAuthProfile)
	if needsBrokerEndpoints {
		if len(policy.BrokerDomains) == 0 {
			return errors.New("host policy broker domains are required")
		}
		if len(policy.BrokerEndpoints) == 0 {
			return errors.New("host policy broker endpoints are required")
		}
	}
	if err := validateHostPolicyBrokerEndpoints(manifest, policy); err != nil {
		return err
	}

	return nil
}

func validateHostPolicyOutputDomains(rules []HostPolicyOutputRule) error {
	for _, rule := range rules {
		if err := validateDomainRule(DomainRule{Host: rule.Host, IncludeSubdomains: rule.IncludeSubdomains}); err != nil {
			return errors.New("host policy output domain is invalid")
		}
		if len(rule.PathPrefixes) == 0 {
			return errors.New("host policy output path prefixes are required")
		}
		seen := make(map[string]struct{}, len(rule.PathPrefixes))
		for _, prefix := range rule.PathPrefixes {
			if err := validateHostPolicyOutputPathPrefix(prefix); err != nil {
				return err
			}
			if _, ok := seen[prefix]; ok {
				return errors.New("host policy output path prefixes contain duplicates")
			}
			seen[prefix] = struct{}{}
		}
	}

	return nil
}

func validateHostPolicyOutputPathPrefix(prefix string) error {
	if prefix == "" || strings.TrimSpace(prefix) != prefix {
		return errors.New("host policy output path prefix is invalid")
	}
	if !strings.HasPrefix(prefix, "/") {
		return errors.New("host policy output path prefix is invalid")
	}
	if prefix != "/" && !strings.HasSuffix(prefix, "/") {
		return errors.New("host policy output path prefix is invalid")
	}
	if strings.ContainsAny(prefix, "\\?#%") || containsControl(prefix) {
		return errors.New("host policy output path prefix is invalid")
	}
	if strings.Contains(prefix, "//") || strings.Contains(prefix, "/./") || strings.Contains(prefix, "/../") || strings.Contains(prefix, "..") {
		return errors.New("host policy output path prefix is invalid")
	}

	return nil
}

func validateHostPolicyAuthProfiles(scopes []HostPolicyAuthProfileScope, capabilities []Capability) error {
	if len(scopes) == 0 {
		return nil
	}
	if !capabilityListContains(capabilities, CapabilityAuthProfile) {
		return errors.New("host policy auth profile scopes require auth capability")
	}
	seen := make(map[AuthProfileID]struct{}, len(scopes))
	for _, scope := range scopes {
		if err := validateAuthProfileID(scope.ProfileID); err != nil {
			return errors.New("host policy auth profile scope is invalid")
		}
		if _, ok := seen[scope.ProfileID]; ok {
			return errors.New("host policy auth profile scopes contain duplicates")
		}
		seen[scope.ProfileID] = struct{}{}
		if err := validateDomainRules(scope.Domains); err != nil {
			return errors.New("host policy auth profile domain rules are invalid")
		}
	}

	return nil
}

func policyIngressMatchesHost(policy ResolvedHostPolicy, host string) bool {
	for _, rule := range policy.IngressDomains {
		if matchesDomainRule(host, rule) {
			return true
		}
	}

	return false
}

func policyBrokerMatchesHost(policy ResolvedHostPolicy, host string) bool {
	for _, rule := range policy.BrokerDomains {
		if matchesDomainRule(host, rule) {
			return true
		}
	}

	return false
}

func validateHostPolicyBrokerEndpoints(manifest Manifest, policy ResolvedHostPolicy) error {
	if len(policy.BrokerEndpoints) == 0 {
		return nil
	}
	manifestBrokerRefs := makeStringSet(manifest.BrokerPolicyRefs)
	policyBrokerRefs := makeStringSet(policy.BrokerPolicyRefs)
	declaredAuthProfiles := make(map[string]struct{}, len(policy.AuthProfiles))
	for _, scope := range policy.AuthProfiles {
		declaredAuthProfiles[string(scope.ProfileID)] = struct{}{}
	}
	seenEndpoints := make(map[string]struct{}, len(policy.BrokerEndpoints))
	for _, endpoint := range policy.BrokerEndpoints {
		if err := validateOpaquePolicyRef("broker_policy_ref", endpoint.BrokerPolicyRef); err != nil {
			return errors.New("host policy broker endpoint ref is invalid")
		}
		if _, ok := manifestBrokerRefs[endpoint.BrokerPolicyRef]; !ok {
			return errors.New("host policy broker endpoint uses undeclared broker policy ref")
		}
		if _, ok := policyBrokerRefs[endpoint.BrokerPolicyRef]; !ok {
			return errors.New("host policy broker endpoint uses unknown broker policy ref")
		}
		if err := validateOpaquePolicyRef("endpoint_ref", endpoint.EndpointRef); err != nil {
			return errors.New("host policy endpoint ref is invalid")
		}
		key := endpoint.BrokerPolicyRef + "\x00" + endpoint.EndpointRef
		if _, ok := seenEndpoints[key]; ok {
			return errors.New("host policy broker endpoints contain duplicate refs")
		}
		seenEndpoints[key] = struct{}{}
		if err := validateBrokerEndpointURLTemplate(endpoint.URLTemplate); err != nil {
			return err
		}
		if _, err := normalizeBrokerEndpointMethods(endpoint.Methods, DefaultHTTPBrokerPolicy()); err != nil {
			return err
		}
		if err := validateBrokerEndpointAuthProfileRefs(endpoint.AuthProfileRefs); err != nil {
			return err
		}
		for _, ref := range endpoint.AuthProfileRefs {
			if !manifestHasCapability(manifest, CapabilityAuthProfile) || !policyAllowsCapability(policy, CapabilityAuthProfile) {
				return errors.New("host policy broker endpoint auth profile refs require auth capability")
			}
			if _, ok := declaredAuthProfiles[ref]; !ok {
				return errors.New("host policy broker endpoint auth profile ref is not declared")
			}
		}
		if err := validateBrokerEndpointResourceCaps(endpoint, manifest, DefaultHTTPBrokerPolicy()); err != nil {
			return err
		}
	}

	return nil
}

func normalizeBrokerEndpointMethods(methods []string, policy HTTPBrokerPolicy) ([]string, error) {
	if len(methods) == 0 {
		return nil, nil
	}
	allowed := policy.AllowedMethods
	if len(allowed) == 0 {
		allowed = DefaultHTTPBrokerPolicy().AllowedMethods
	}
	seen := make(map[string]struct{}, len(methods))
	normalized := make([]string, 0, len(methods))
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || strings.ContainsAny(method, " \t\r\n") {
			return nil, errors.New("host policy broker endpoint method is invalid")
		}
		if _, ok := seen[method]; ok {
			return nil, errors.New("host policy broker endpoint methods contain duplicates")
		}
		if _, ok := allowed[method]; !ok {
			return nil, errors.New("host policy broker endpoint method is not allowed")
		}
		seen[method] = struct{}{}
		normalized = append(normalized, method)
	}

	return normalized, nil
}

func validateBrokerEndpointResourceCaps(endpoint HostPolicyBrokerEndpoint, manifest Manifest, policy HTTPBrokerPolicy) error {
	if endpoint.TimeoutMillis < 0 {
		return errors.New("host policy broker endpoint timeout is invalid")
	}
	if endpoint.TimeoutMillis > 0 {
		brokerMaxMillis := int(policy.MaxTimeout / time.Millisecond)
		if manifest.ResourceLimits.TimeoutMillis > 0 && endpoint.TimeoutMillis > manifest.ResourceLimits.TimeoutMillis {
			return errors.New("host policy broker endpoint timeout exceeds manifest limit")
		}
		if brokerMaxMillis > 0 && endpoint.TimeoutMillis > brokerMaxMillis {
			return errors.New("host policy broker endpoint timeout exceeds broker limit")
		}
	}
	if endpoint.MaxResponseBytes < 0 {
		return errors.New("host policy broker endpoint response cap is invalid")
	}
	if endpoint.MaxResponseBytes > 0 {
		if manifest.ResourceLimits.MaxResponseBytes > 0 && endpoint.MaxResponseBytes > manifest.ResourceLimits.MaxResponseBytes {
			return errors.New("host policy broker endpoint response cap exceeds manifest limit")
		}
		if policy.MaxResponseBytes > 0 && endpoint.MaxResponseBytes > policy.MaxResponseBytes {
			return errors.New("host policy broker endpoint response cap exceeds broker limit")
		}
	}

	return nil
}

func validateBrokerEndpointURLTemplate(template string) error {
	if template == "" || strings.TrimSpace(template) != template {
		return errors.New("host policy broker endpoint template is invalid")
	}
	if len(template) > maxABIURLBytes {
		return errors.New("host policy broker endpoint template is invalid")
	}
	if strings.ContainsAny(template, "\r\n\x00") {
		return errors.New("host policy broker endpoint template is invalid")
	}
	if strings.ContainsAny(brokerEndpointParamPattern.ReplaceAllString(template, ""), "{}") {
		return errors.New("host policy broker endpoint template is invalid")
	}
	parsed, err := url.Parse(template)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("host policy broker endpoint template is invalid")
	}
	if err := validateBrokerEndpointTemplatePlaceholders(parsed); err != nil {
		return err
	}

	return nil
}

func validateBrokerEndpointAuthProfileRefs(refs []string) error {
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if err := validateAuthProfileID(AuthProfileID(ref)); err != nil {
			return errors.New("host policy broker endpoint auth profile ref is invalid")
		}
		if _, ok := seen[ref]; ok {
			return errors.New("host policy broker endpoint auth profile refs contain duplicates")
		}
		seen[ref] = struct{}{}
	}

	return nil
}

func findBrokerEndpoint(policy ResolvedHostPolicy, brokerPolicyRef string, endpointRef string) (HostPolicyBrokerEndpoint, bool) {
	var found HostPolicyBrokerEndpoint
	count := 0
	for _, endpoint := range policy.BrokerEndpoints {
		if endpoint.BrokerPolicyRef != brokerPolicyRef || endpoint.EndpointRef != endpointRef {
			continue
		}
		found = cloneHostPolicyBrokerEndpoint(endpoint)
		count++
	}

	return found, count == 1
}

func endpointAllowsAuthProfile(endpoint HostPolicyBrokerEndpoint, profileRef AuthProfileID) bool {
	for _, allowed := range endpoint.AuthProfileRefs {
		if allowed == string(profileRef) {
			return true
		}
	}

	return false
}

func policyAllowsCapability(policy ResolvedHostPolicy, capability Capability) bool {
	for _, allowed := range policy.AllowedCapabilities {
		if allowed == capability {
			return true
		}
	}

	return false
}

func expandBrokerEndpointURL(policy ResolvedHostPolicy, endpoint HostPolicyBrokerEndpoint, params map[string]string) (string, error) {
	if err := validateHostImportParams(params); err != nil {
		return "", errors.New("invalid endpoint params")
	}
	parsedTemplate, err := url.Parse(endpoint.URLTemplate)
	if err != nil {
		return "", errors.New("expanded endpoint url is invalid")
	}
	expanded, err := expandBrokerEndpointTemplate(parsedTemplate, params)
	if err != nil {
		return "", err
	}
	parsed, host, err := parseSafeHTTPURL(expanded)
	if err != nil {
		return "", errors.New("expanded endpoint url is invalid")
	}
	if !policyBrokerMatchesHost(policy, host) {
		return "", errors.New("expanded endpoint host is not allowed")
	}

	return parsed.String(), nil
}

func policyAllowsOutputURL(policy ResolvedHostPolicy, rawURL string) error {
	parsed, host, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return redactErrorf("output url is invalid")
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
	if err := validateOutputURLPath(path); err != nil {
		return err
	}
	for _, rule := range policy.OutputDomains {
		if !matchesDomainRule(host, DomainRule{Host: rule.Host, IncludeSubdomains: rule.IncludeSubdomains}) {
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

func validateOutputURLPath(escapedPath string) error {
	for _, current := range strings.Split(escapedPath, "/") {
		if current == "" {
			continue
		}
		for depth := 0; ; depth++ {
			if outputURLPathSegmentUnsafe(current) {
				return redactErrorf("output url path is invalid")
			}
			if !containsPercentEscape(current) {
				break
			}
			if depth >= 8 {
				return redactErrorf("output url path is invalid")
			}
			decoded, err := url.PathUnescape(current)
			if err != nil {
				return redactErrorf("output url path is invalid")
			}
			current = decoded
		}
	}

	return nil
}

func outputURLPathSegmentUnsafe(segment string) bool {
	return segment == "." || segment == ".." || strings.Contains(segment, "/") || strings.Contains(segment, `\`) || containsControl(segment)
}

func containsPercentEscape(value string) bool {
	for i := 0; i+2 < len(value); i++ {
		if value[i] == '%' && isASCIIHex(value[i+1]) && isASCIIHex(value[i+2]) {
			return true
		}
	}

	return false
}

func isASCIIHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func policyAuthProfileMatchesHost(policy ResolvedHostPolicy, profileID AuthProfileID, host string) bool {
	for _, scope := range policy.AuthProfiles {
		if scope.ProfileID == profileID {
			return domainRulesMatchHost(scope.Domains, host)
		}
	}

	return false
}

func validateBrokerEndpointTemplatePlaceholders(parsed *url.URL) error {
	if parsed == nil {
		return errors.New("host policy broker endpoint template is invalid")
	}
	if strings.Contains(parsed.Scheme, "{") || strings.Contains(parsed.Host, "{") || strings.Contains(parsed.Fragment, "{") || strings.Contains(parsed.RawFragment, "{") {
		return errors.New("host policy broker endpoint template is invalid")
	}
	pathPlaceholders, err := templatePathPlaceholders(parsed.Path)
	if err != nil {
		return err
	}
	queryPlaceholders, err := templateQueryPlaceholders(parsed.RawQuery)
	if err != nil {
		return err
	}
	if len(pathPlaceholders)+len(queryPlaceholders) != len(brokerEndpointParamPattern.FindAllStringSubmatch(parsed.Path+"?"+parsed.RawQuery, -1)) {
		return errors.New("host policy broker endpoint template is invalid")
	}

	return nil
}

func expandBrokerEndpointTemplate(parsed *url.URL, params map[string]string) (string, error) {
	if err := validateBrokerEndpointTemplatePlaceholders(parsed); err != nil {
		return "", errors.New("expanded endpoint url is invalid")
	}
	pathPlaceholders, err := templatePathPlaceholders(parsed.Path)
	if err != nil {
		return "", errors.New("invalid endpoint params")
	}
	queryPlaceholders, err := templateQueryPlaceholders(parsed.RawQuery)
	if err != nil {
		return "", errors.New("invalid endpoint params")
	}
	required := make(map[string]struct{}, len(pathPlaceholders)+len(queryPlaceholders))
	for _, placeholder := range pathPlaceholders {
		required[placeholder] = struct{}{}
	}
	for _, placeholder := range queryPlaceholders {
		required[placeholder] = struct{}{}
	}
	for key := range required {
		if _, ok := params[key]; !ok {
			return "", errors.New("invalid endpoint params")
		}
	}
	for key := range params {
		if _, ok := required[key]; !ok {
			return "", errors.New("invalid endpoint params")
		}
	}
	expanded := *parsed
	expanded.RawPath = ""
	expanded.Path, err = expandEndpointPath(parsed.Path, params)
	if err != nil {
		return "", err
	}
	expanded.RawQuery, err = expandEndpointRawQuery(parsed.RawQuery, params)
	if err != nil {
		return "", err
	}

	return expanded.String(), nil
}

func templatePathPlaceholders(escapedPath string) ([]string, error) {
	if escapedPath == "" {
		return nil, nil
	}
	segments := strings.Split(escapedPath, "/")
	placeholders := make([]string, 0)
	for _, segment := range segments {
		if !strings.Contains(segment, "{") && !strings.Contains(segment, "}") {
			continue
		}
		match := brokerEndpointParamPattern.FindStringSubmatch(segment)
		if len(match) != 2 || match[0] != segment {
			return nil, errors.New("host policy broker endpoint template is invalid")
		}
		placeholders = append(placeholders, match[1])
	}

	return placeholders, nil
}

func templateQueryPlaceholders(rawQuery string) ([]string, error) {
	if rawQuery == "" {
		return nil, nil
	}
	parts := strings.Split(rawQuery, "&")
	placeholders := make([]string, 0)
	for _, part := range parts {
		if part == "" {
			return nil, errors.New("host policy broker endpoint template is invalid")
		}
		key, value, hasValue := strings.Cut(part, "=")
		if !hasValue || key == "" || isTokenShapedEndpointQueryKey(key) {
			return nil, errors.New("host policy broker endpoint template is invalid")
		}
		if strings.Contains(key, "{") || strings.Contains(key, "}") {
			return nil, errors.New("host policy broker endpoint template is invalid")
		}
		if !strings.Contains(value, "{") && !strings.Contains(value, "}") {
			continue
		}
		match := brokerEndpointParamPattern.FindStringSubmatch(value)
		if len(match) != 2 || match[0] != value {
			return nil, errors.New("host policy broker endpoint template is invalid")
		}
		placeholders = append(placeholders, match[1])
	}

	return placeholders, nil
}

func expandEndpointPath(escapedPath string, params map[string]string) (string, error) {
	if escapedPath == "" {
		return "", nil
	}
	segments := strings.Split(escapedPath, "/")
	for i, segment := range segments {
		match := brokerEndpointParamPattern.FindStringSubmatch(segment)
		if len(match) == 0 || match[0] != segment {
			continue
		}
		value := params[match[1]]
		if err := validateEndpointPathParam(value); err != nil {
			return "", err
		}
		segments[i] = value
	}

	return strings.Join(segments, "/"), nil
}

func expandEndpointRawQuery(rawQuery string, params map[string]string) (string, error) {
	if rawQuery == "" {
		return "", nil
	}
	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		key, value, hasValue := strings.Cut(part, "=")
		if !hasValue {
			return "", errors.New("invalid endpoint params")
		}
		match := brokerEndpointParamPattern.FindStringSubmatch(value)
		if len(match) == 0 || match[0] != value {
			continue
		}
		if err := validateEndpointQueryParam(params[match[1]]); err != nil {
			return "", err
		}
		parts[i] = key + "=" + url.QueryEscape(params[match[1]])
	}

	return strings.Join(parts, "&"), nil
}

func validateEndpointPathParam(value string) error {
	if value == "." || value == ".." || strings.Contains(value, "/") || strings.Contains(value, `\`) || strings.ContainsAny(value, `:/?#[]@!$&'()*+,;=`) {
		return errors.New("invalid endpoint params")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return errors.New("invalid endpoint params")
		}
	}

	return nil
}

func validateEndpointQueryParam(value string) error {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return errors.New("invalid endpoint params")
		}
	}

	return nil
}

func isTokenShapedEndpointQueryKey(key string) bool {
	decoded, err := url.QueryUnescape(key)
	if err != nil {
		decoded = key
	}
	normalized := strings.ToLower(strings.TrimSpace(decoded))
	normalized = strings.NewReplacer("-", "_", ".", "_", " ", "_", ":", "_").Replace(normalized)
	if _, ok := tokenLikeQueryKeys[normalized]; ok {
		return true
	}
	sensitive := map[string]struct{}{
		"authorization": {},
		"cookie":        {},
		"set_cookie":    {},
	}
	if _, ok := sensitive[normalized]; ok {
		return true
	}
	parts := strings.FieldsFunc(normalized, func(r rune) bool { return r == '_' })
	for _, part := range parts {
		if _, ok := tokenLikeQueryKeys[part]; ok {
			return true
		}
	}

	return false
}

func capabilityListContains(capabilities []Capability, capability Capability) bool {
	for _, candidate := range capabilities {
		if candidate == capability {
			return true
		}
	}

	return false
}

func containsControl(value string) bool {
	for _, r := range value {
		if r == 0 || r < 0x20 || r == 0x7f {
			return true
		}
	}

	return false
}

func validateResolvedHostPolicyBinding(identity VerifiedPackIdentity, manifest Manifest, policy ResolvedHostPolicy) error {
	if !isAliasManifest(manifest) {
		return errors.New("host policy requires alias manifest")
	}
	if !hasVerifiedPackIdentity(identity) {
		return errors.New("verified pack identity is incomplete")
	}
	if err := validateResolvedHostPolicy(identity, manifest, policy); err != nil {
		return err
	}

	return nil
}

func hasVerifiedPackIdentity(identity VerifiedPackIdentity) bool {
	return identity.PackID != "" &&
		identity.PackVersion != "" &&
		identity.ManifestSHA256 != "" &&
		identity.PayloadSHA256 != "" &&
		identity.SignatureSHA256 != "" &&
		identity.PublicKeySHA256 != ""
}

func samePolicyRefSet(manifestRefs []string, policyRefs []string) bool {
	if len(manifestRefs) != len(policyRefs) {
		return false
	}
	if err := validateOpaquePolicyRefs("domain_policy_refs", policyRefs); err != nil {
		return false
	}

	seen := make(map[string]struct{}, len(manifestRefs))
	for _, ref := range manifestRefs {
		seen[ref] = struct{}{}
	}
	for _, ref := range policyRefs {
		if _, ok := seen[ref]; !ok {
			return false
		}
	}

	return true
}

func capabilitiesAllowedByManifest(manifest Manifest) map[Capability]struct{} {
	allowed := make(map[Capability]struct{}, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		allowed[capability] = struct{}{}
	}

	return allowed
}

func makeStringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}

	return set
}

func cloneResolvedHostPolicy(policy ResolvedHostPolicy) ResolvedHostPolicy {
	policy.DomainPolicyRefs = cloneStringSlice(policy.DomainPolicyRefs)
	policy.BrokerPolicyRefs = cloneStringSlice(policy.BrokerPolicyRefs)
	policy.AllowedCapabilities = append([]Capability(nil), policy.AllowedCapabilities...)
	policy.IngressDomains = cloneDomainRules(policy.IngressDomains)
	policy.BrokerDomains = cloneDomainRules(policy.BrokerDomains)
	policy.OutputDomains = cloneHostPolicyOutputRules(policy.OutputDomains)
	policy.AuthProfiles = cloneHostPolicyAuthProfileScopes(policy.AuthProfiles)
	policy.BrokerEndpoints = cloneHostPolicyBrokerEndpoints(policy.BrokerEndpoints)

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

func cloneHostPolicyAuthProfileScopes(scopes []HostPolicyAuthProfileScope) []HostPolicyAuthProfileScope {
	if scopes == nil {
		return nil
	}
	cloned := append([]HostPolicyAuthProfileScope(nil), scopes...)
	for i := range cloned {
		cloned[i].Domains = cloneDomainRules(cloned[i].Domains)
	}

	return cloned
}

func cloneHostPolicyBrokerEndpoints(endpoints []HostPolicyBrokerEndpoint) []HostPolicyBrokerEndpoint {
	if endpoints == nil {
		return nil
	}
	cloned := make([]HostPolicyBrokerEndpoint, len(endpoints))
	for i, endpoint := range endpoints {
		cloned[i] = cloneHostPolicyBrokerEndpoint(endpoint)
	}

	return cloned
}

func cloneHostPolicyBrokerEndpoint(endpoint HostPolicyBrokerEndpoint) HostPolicyBrokerEndpoint {
	endpoint.Methods = cloneStringSlice(endpoint.Methods)
	endpoint.AuthProfileRefs = cloneStringSlice(endpoint.AuthProfileRefs)

	return endpoint
}
