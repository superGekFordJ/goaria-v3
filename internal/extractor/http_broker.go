package extractor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	defaultRedirectLimit       = 5
	defaultHeaderCountLimit    = 16
	defaultHeaderValueMaxBytes = 1024
)

var defaultSafeRequestHeaders = map[string]struct{}{
	"Accept":          {},
	"Accept-Language": {},
	"Content-Type":    {},
	"Referer":         {},
	"User-Agent":      {},
}

var defaultSafeResponseHeaders = map[string]struct{}{
	"Content-Length": {},
	"Content-Type":   {},
	"Etag":           {},
	"Last-Modified":  {},
}

type HTTPBrokerPolicy struct {
	AllowedMethods        map[string]struct{}
	AllowedRequestHeaders map[string]struct{}
	SafeResponseHeaders   map[string]struct{}
	RedirectLimit         int
	DefaultTimeout        time.Duration
	MaxTimeout            time.Duration
	MaxResponseBytes      int64
	MaxHeaderCount        int
	MaxHeaderValueBytes   int
}

func DefaultHTTPBrokerPolicy() HTTPBrokerPolicy {
	trustPolicy := DefaultTrustPolicy()

	return HTTPBrokerPolicy{
		AllowedMethods: map[string]struct{}{
			http.MethodGet:  {},
			http.MethodHead: {},
		},
		AllowedRequestHeaders: cloneStringSet(defaultSafeRequestHeaders),
		SafeResponseHeaders:   cloneStringSet(defaultSafeResponseHeaders),
		RedirectLimit:         defaultRedirectLimit,
		DefaultTimeout:        5 * time.Second,
		MaxTimeout:            10 * time.Second,
		MaxResponseBytes:      trustPolicy.MaxResourceLimits.MaxResponseBytes,
		MaxHeaderCount:        defaultHeaderCountLimit,
		MaxHeaderValueBytes:   defaultHeaderValueMaxBytes,
	}
}

type HTTPFetchRequest struct {
	PackID           string
	Manifest         Manifest
	Method           string
	URL              string
	Headers          map[string]string
	AuthProfileID    AuthProfileID
	Timeout          time.Duration
	MaxResponseBytes int64
}

type HTTPFetchRefRequest struct {
	PackID           string
	Manifest         Manifest
	PackIdentity     VerifiedPackIdentity
	HostPolicy       ResolvedHostPolicy
	BrokerPolicyRef  string
	EndpointRef      string
	Params           map[string]string
	Method           string
	Headers          map[string]string
	AuthProfileID    AuthProfileID
	Timeout          time.Duration
	MaxResponseBytes int64
}

type httpFetchExecutionRequest struct {
	PackID              string
	Manifest            Manifest
	Method              string
	URL                 string
	Headers             map[string]string
	AuthProfileID       AuthProfileID
	Timeout             time.Duration
	MaxResponseBytes    int64
	FinalURLRedacted    bool
	AllowedURL          func(string) (*url.URL, error)
	ValidateAuthProfile func(AuthProfileID, string) error
}

type HTTPFetchResponse struct {
	StatusCode int
	FinalURL   string
	Headers    http.Header
	Body       []byte
}

type HTTPBrokerConfig struct {
	Policy           HTTPBrokerPolicy
	Transport        http.RoundTripper
	AuthResolver     AuthProfileResolver
	AuthMaterializer AuthMaterializer
}

type HTTPBroker struct {
	policy           HTTPBrokerPolicy
	transport        http.RoundTripper
	authResolver     AuthProfileResolver
	authMaterializer AuthMaterializer
}

func NewHTTPBroker(config HTTPBrokerConfig) *HTTPBroker {
	policy := normalizeHTTPBrokerPolicy(config.Policy)
	transport := config.Transport
	if transport == nil {
		transport = defaultSecureHTTPTransport()
	}
	materializer := config.AuthMaterializer
	if materializer == nil {
		materializer = NewDefaultAuthMaterializer()
	}

	return &HTTPBroker{
		policy:           policy,
		transport:        transport,
		authResolver:     config.AuthResolver,
		authMaterializer: materializer,
	}
}

func (b *HTTPBroker) Fetch(ctx context.Context, request HTTPFetchRequest) (HTTPFetchResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil {
		return HTTPFetchResponse{}, errors.New("http broker is nil")
	}

	knownSecrets := make([]string, 0, 2)
	response, err := b.fetch(ctx, request, &knownSecrets)
	if err != nil {
		return HTTPFetchResponse{}, redactedError(err, knownSecrets...)
	}

	return response, nil
}

func (b *HTTPBroker) FetchRef(ctx context.Context, request HTTPFetchRefRequest) (HTTPFetchResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil {
		return HTTPFetchResponse{}, errors.New("http broker is nil")
	}
	knownSecrets := make([]string, 0, 2)
	response, err := b.fetchRef(ctx, request, &knownSecrets)
	if err != nil {
		return HTTPFetchResponse{}, sanitizeRefModeFetchError(err, knownSecrets...)
	}

	return response, nil
}

func sanitizeRefModeFetchError(err error, knownSecrets ...string) error {
	if err == nil {
		return nil
	}
	redacted := RedactSensitive(err.Error(), knownSecrets...)
	for _, allowed := range []string{
		"unknown endpoint ref",
		"invalid endpoint params",
		"host policy denied",
		"auth profile denied",
		"request timeout is invalid",
		"response body cap must be positive",
	} {
		if redacted == allowed {
			return errors.New(allowed)
		}
	}
	if strings.HasPrefix(redacted, "http method ") || strings.HasPrefix(redacted, "unsupported http method") || strings.HasPrefix(redacted, "request header ") || strings.HasPrefix(redacted, "too many request headers") || strings.HasPrefix(redacted, "invalid request header") {
		return errors.New(redacted)
	}

	return errors.New("ref-mode fetch failed")
}

func (b *HTTPBroker) fetch(ctx context.Context, request HTTPFetchRequest, knownSecrets *[]string) (HTTPFetchResponse, error) {
	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:     request.PackID,
		Manifest:   request.Manifest,
		Capability: CapabilityHTTPFetch,
	}, request.URL); err != nil {
		return HTTPFetchResponse{}, err
	}
	if request.AuthProfileID != "" {
		if err := ValidateCapabilityURL(CapabilityContext{
			PackID:     request.PackID,
			Manifest:   request.Manifest,
			Capability: CapabilityAuthProfile,
		}, request.URL); err != nil {
			return HTTPFetchResponse{}, err
		}
		if err := validateAuthProfileID(request.AuthProfileID); err != nil {
			return HTTPFetchResponse{}, err
		}
	}

	return b.fetchExpanded(ctx, httpFetchExecutionRequest{
		PackID:           request.PackID,
		Manifest:         request.Manifest,
		Method:           request.Method,
		URL:              request.URL,
		Headers:          request.Headers,
		AuthProfileID:    request.AuthProfileID,
		Timeout:          request.Timeout,
		MaxResponseBytes: request.MaxResponseBytes,
		AllowedURL: func(rawURL string) (*url.URL, error) {
			return allowedHTTPURLForManifest(request.Manifest, rawURL)
		},
	}, knownSecrets)
}

func (b *HTTPBroker) fetchRef(ctx context.Context, request HTTPFetchRefRequest, knownSecrets *[]string) (HTTPFetchResponse, error) {
	if err := validatePackID(request.PackID); err != nil {
		return HTTPFetchResponse{}, errors.New("invalid ref-mode request")
	}
	if request.Manifest.PackID != request.PackID {
		return HTTPFetchResponse{}, errors.New("invalid ref-mode request")
	}
	if !isAliasManifest(request.Manifest) {
		return HTTPFetchResponse{}, errors.New("ref-mode fetch requires alias manifest")
	}
	if request.PackIdentity.PackID != request.PackID || request.PackIdentity != request.HostPolicy.PackIdentity {
		return HTTPFetchResponse{}, errors.New("host policy denied")
	}
	if err := validateResolvedHostPolicyBinding(request.PackIdentity, request.Manifest, request.HostPolicy); err != nil {
		return HTTPFetchResponse{}, errors.New("host policy denied")
	}
	if !manifestHasCapability(request.Manifest, CapabilityHTTPFetch) || !policyAllowsCapability(request.HostPolicy, CapabilityHTTPFetch) {
		return HTTPFetchResponse{}, errors.New("host policy denied")
	}
	if err := validateHostImportRefs(request.BrokerPolicyRef, request.EndpointRef); err != nil {
		return HTTPFetchResponse{}, errors.New("host policy denied")
	}
	endpoint, ok := findBrokerEndpoint(request.HostPolicy, request.BrokerPolicyRef, request.EndpointRef)
	if !ok {
		return HTTPFetchResponse{}, errors.New("unknown endpoint ref")
	}
	if _, err := b.validateMethod(request.Method); err != nil {
		return HTTPFetchResponse{}, err
	}
	if _, err := b.validatePackHeaders(request.Headers); err != nil {
		return HTTPFetchResponse{}, err
	}
	if request.Timeout < 0 {
		return HTTPFetchResponse{}, errors.New("request timeout is invalid")
	}
	if request.MaxResponseBytes < 0 {
		return HTTPFetchResponse{}, errors.New("response body cap must be positive")
	}
	timeout := b.effectiveTimeout(HTTPFetchRequest{Manifest: request.Manifest, Timeout: request.Timeout})
	if timeout <= 0 {
		return HTTPFetchResponse{}, errors.New("request timeout is invalid")
	}
	if bodyCap := b.effectiveBodyCap(HTTPFetchRequest{Manifest: request.Manifest, MaxResponseBytes: request.MaxResponseBytes}); bodyCap <= 0 {
		return HTTPFetchResponse{}, errors.New("response body cap must be positive")
	}
	targetURL, err := expandBrokerEndpointURL(request.HostPolicy, endpoint, request.Params)
	if err != nil {
		return HTTPFetchResponse{}, err
	}
	allowedURL := func(rawURL string) (*url.URL, error) {
		parsed, host, err := parseSafeHTTPURL(rawURL)
		if err != nil {
			return nil, errors.New("broker url denied")
		}
		if !policyBrokerMatchesHost(request.HostPolicy, host) {
			return nil, errors.New("broker url denied")
		}

		return parsed, nil
	}
	authValidator := func(profileID AuthProfileID, rawURL string) error {
		if profileID == "" {
			return nil
		}
		if !manifestHasCapability(request.Manifest, CapabilityAuthProfile) || !policyAllowsCapability(request.HostPolicy, CapabilityAuthProfile) {
			return errors.New("auth profile denied")
		}
		if err := validateAuthProfileID(profileID); err != nil {
			return errors.New("auth profile denied")
		}
		if !endpointAllowsAuthProfile(endpoint, profileID) {
			return errors.New("auth profile denied")
		}
		parsed, _, err := parseSafeHTTPURL(rawURL)
		if err != nil || parsed.Scheme != "https" {
			return errors.New("auth profile denied")
		}
		if b.authResolver == nil {
			return errors.New("auth profile denied")
		}

		return nil
	}

	return b.fetchExpanded(ctx, httpFetchExecutionRequest{
		PackID:              request.PackID,
		Manifest:            request.Manifest,
		Method:              request.Method,
		URL:                 targetURL,
		Headers:             request.Headers,
		AuthProfileID:       request.AuthProfileID,
		Timeout:             timeout,
		MaxResponseBytes:    request.MaxResponseBytes,
		FinalURLRedacted:    true,
		AllowedURL:          allowedURL,
		ValidateAuthProfile: authValidator,
	}, knownSecrets)
}

func (b *HTTPBroker) fetchExpanded(ctx context.Context, request httpFetchExecutionRequest, knownSecrets *[]string) (HTTPFetchResponse, error) {
	method, err := b.validateMethod(request.Method)
	if err != nil {
		return HTTPFetchResponse{}, err
	}
	validatedHeaders, err := b.validatePackHeaders(request.Headers)
	if err != nil {
		return HTTPFetchResponse{}, err
	}
	if request.AuthProfileID != "" {
		if request.ValidateAuthProfile != nil {
			if err := request.ValidateAuthProfile(request.AuthProfileID, request.URL); err != nil {
				return HTTPFetchResponse{}, err
			}
		}
		if b.authResolver == nil {
			return HTTPFetchResponse{}, fmt.Errorf("auth profile %q requested but no auth resolver is configured", request.AuthProfileID)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, b.effectiveTimeout(HTTPFetchRequest{Manifest: request.Manifest, Timeout: request.Timeout}))
	defer cancel()

	currentURL := request.URL
	for redirects := 0; ; redirects++ {
		parsed, err := request.AllowedURL(currentURL)
		if err != nil {
			return HTTPFetchResponse{}, err
		}
		httpRequest, err := http.NewRequestWithContext(ctx, method, parsed.String(), nil)
		if err != nil {
			return HTTPFetchResponse{}, fmt.Errorf("construct request: %w", err)
		}
		for name, values := range validatedHeaders {
			for _, value := range values {
				httpRequest.Header.Add(name, value)
			}
		}

		if request.AuthProfileID != "" {
			if request.ValidateAuthProfile != nil {
				if err := request.ValidateAuthProfile(request.AuthProfileID, currentURL); err != nil {
					return HTTPFetchResponse{}, err
				}
			}
			if err := b.injectAuth(ctx, httpRequest, HTTPFetchRequest{
				PackID:        request.PackID,
				Manifest:      request.Manifest,
				AuthProfileID: request.AuthProfileID,
			}, currentURL, knownSecrets); err != nil {
				return HTTPFetchResponse{}, err
			}
		}

		response, err := b.transport.RoundTrip(httpRequest)
		if err != nil {
			return HTTPFetchResponse{}, fmt.Errorf("http fetch failed for %s: %w", currentURL, err)
		}
		if response == nil {
			return HTTPFetchResponse{}, fmt.Errorf("http fetch failed for %s: empty response", currentURL)
		}
		if response.Body == nil {
			response.Body = io.NopCloser(strings.NewReader(""))
		}

		if isRedirectStatus(response.StatusCode) {
			if response.Body != nil {
				_ = response.Body.Close()
			}
			location := response.Header.Get("Location")
			if redirects >= b.policy.RedirectLimit {
				return HTTPFetchResponse{}, fmt.Errorf("redirect limit exceeded for %s", currentURL)
			}
			nextURL, err := resolveRedirectURL(parsed, location)
			if err != nil {
				return HTTPFetchResponse{}, err
			}
			parsedNext, err := request.AllowedURL(nextURL)
			if err != nil {
				return HTTPFetchResponse{}, fmt.Errorf("redirect denied: %w", err)
			}
			if request.AuthProfileID != "" && parsedNext.Scheme != "https" {
				return HTTPFetchResponse{}, fmt.Errorf("authenticated redirect to non-HTTPS url denied: %s", nextURL)
			}
			currentURL = nextURL
			continue
		}

		body, err := readCappedBody(response.Body, b.effectiveBodyCap(HTTPFetchRequest{Manifest: request.Manifest, MaxResponseBytes: request.MaxResponseBytes}))
		if err != nil {
			return HTTPFetchResponse{}, err
		}
		safeHeaders := b.safeResponseHeaders(response.Header)
		if request.AuthProfileID != "" {
			if err := rejectSecretReflection(body, safeHeaders, *knownSecrets); err != nil {
				return HTTPFetchResponse{}, err
			}
		}

		finalURL := RedactSensitive(currentURL, *knownSecrets...)
		if request.FinalURLRedacted {
			finalURL = ""
		}

		return HTTPFetchResponse{
			StatusCode: response.StatusCode,
			FinalURL:   finalURL,
			Headers:    safeHeaders,
			Body:       body,
		}, nil
	}
}

func (b *HTTPBroker) validateMethod(method string) (string, error) {
	if method == "" {
		method = http.MethodGet
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || strings.ContainsAny(method, " \t\r\n") {
		return "", fmt.Errorf("unsupported http method %q", method)
	}
	if _, ok := b.policy.AllowedMethods[method]; !ok {
		return "", fmt.Errorf("http method %q is not allowed", method)
	}

	return method, nil
}

func (b *HTTPBroker) validatePackHeaders(headers map[string]string) (http.Header, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	if len(headers) > b.policy.MaxHeaderCount {
		return nil, fmt.Errorf("too many request headers")
	}

	validated := make(http.Header, len(headers))
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" || canonical != http.CanonicalHeaderKey(canonical) || strings.ContainsAny(name, "\r\n:") {
			return nil, fmt.Errorf("invalid request header name")
		}
		if isSecretHeaderName(canonical) || isForbiddenPackHeader(canonical) {
			return nil, fmt.Errorf("request header %q is not allowed", canonical)
		}
		if _, ok := b.policy.AllowedRequestHeaders[canonical]; !ok {
			return nil, fmt.Errorf("request header %q is not allowed", canonical)
		}
		if len(value) > b.policy.MaxHeaderValueBytes {
			return nil, fmt.Errorf("request header %q value is too large", canonical)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("request header %q value contains CR/LF", canonical)
		}
		validated.Set(canonical, value)
	}

	return validated, nil
}

func (b *HTTPBroker) injectAuth(ctx context.Context, httpRequest *http.Request, request HTTPFetchRequest, targetURL string, knownSecrets *[]string) error {
	if httpRequest.URL.Scheme != "https" {
		return fmt.Errorf("auth profile %q requires an HTTPS request target", request.AuthProfileID)
	}
	if b.authResolver == nil {
		return fmt.Errorf("auth profile %q requested but no auth resolver is configured", request.AuthProfileID)
	}
	if err := validateAuthProfileID(request.AuthProfileID); err != nil {
		return err
	}
	resolved, err := b.authResolver.ResolveAuthProfile(ctx, request.PackID, request.AuthProfileID, targetURL)
	if err != nil {
		return err
	}
	materializer := b.authMaterializer
	if materializer == nil {
		materializer = NewDefaultAuthMaterializer()
	}
	material, err := materializer.MaterializeAuth(resolved)
	if err != nil {
		return redactedError(fmt.Errorf("auth profile %q resolved unusable auth material: %w", request.AuthProfileID, err), authSecretForms(resolved.HeaderName, resolved.HeaderValue)...)
	}
	*knownSecrets = appendNonEmptySecrets(*knownSecrets, material.SensitiveValues()...)
	material.ApplyTo(httpRequest.Header)

	return nil
}

func (b *HTTPBroker) effectiveTimeout(request HTTPFetchRequest) time.Duration {
	requestMillis := 0
	if request.Timeout > 0 {
		requestMillis = int(request.Timeout / time.Millisecond)
	}
	policyDefaultMillis := int(b.policy.DefaultTimeout / time.Millisecond)
	policyMaxMillis := int(b.policy.MaxTimeout / time.Millisecond)
	manifestMillis := request.Manifest.ResourceLimits.TimeoutMillis
	effectiveMillis := minPositiveDurationMillis(requestMillis, manifestMillis, policyMaxMillis)
	if effectiveMillis == 0 {
		effectiveMillis = policyDefaultMillis
	}
	if effectiveMillis == 0 {
		effectiveMillis = policyMaxMillis
	}

	return time.Duration(effectiveMillis) * time.Millisecond
}

func (b *HTTPBroker) effectiveBodyCap(request HTTPFetchRequest) int64 {
	effective := minPositiveInt64(request.MaxResponseBytes, request.Manifest.ResourceLimits.MaxResponseBytes, b.policy.MaxResponseBytes)
	if effective <= 0 {
		return b.policy.MaxResponseBytes
	}

	return effective
}

func (b *HTTPBroker) safeResponseHeaders(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}

	safe := make(http.Header)
	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)
		if _, ok := b.policy.SafeResponseHeaders[canonical]; !ok {
			continue
		}
		if isSecretHeaderName(canonical) {
			continue
		}
		safe[canonical] = append([]string(nil), values...)
	}

	return safe
}

func rejectSecretReflection(body []byte, headers http.Header, knownSecrets []string) error {
	forms := compactUniqueNonEmpty(knownSecrets)
	if len(forms) == 0 {
		return nil
	}
	bodyText := string(body)
	for _, secret := range forms {
		if strings.Contains(bodyText, secret) {
			return redactedError(fmt.Errorf("authenticated response body reflected secret %q", secret), forms...)
		}
	}
	for name, values := range headers {
		for _, value := range values {
			for _, secret := range forms {
				if strings.Contains(value, secret) {
					return redactedError(fmt.Errorf("authenticated response header %q reflected secret %q", name, secret), forms...)
				}
			}
		}
	}

	return nil
}

func isForbiddenPackHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Authorization", "Cookie", "Set-Cookie", "Host", "Content-Length", "Transfer-Encoding", "Connection", "Proxy-Authorization":
		return true
	default:
		return false
	}
}

func isRedirectStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func resolveRedirectURL(base *url.URL, location string) (string, error) {
	if location == "" {
		return "", fmt.Errorf("redirect response missing Location")
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("parse redirect location: %w", err)
	}
	if !parsed.IsAbs() {
		parsed = base.ResolveReference(parsed)
	}

	return parsed.String(), nil
}

func readCappedBody(body io.ReadCloser, capBytes int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	defer body.Close()
	if capBytes <= 0 {
		return nil, fmt.Errorf("response body cap must be positive")
	}

	limited := io.LimitReader(body, capBytes+1)
	bytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(bytes)) > capBytes {
		return nil, fmt.Errorf("response body exceeds %d byte cap", capBytes)
	}

	return bytes, nil
}

func normalizeHTTPBrokerPolicy(policy HTTPBrokerPolicy) HTTPBrokerPolicy {
	defaults := DefaultHTTPBrokerPolicy()
	if len(policy.AllowedMethods) == 0 {
		policy.AllowedMethods = defaults.AllowedMethods
	} else {
		policy.AllowedMethods = normalizeMethodSet(policy.AllowedMethods)
	}
	if len(policy.AllowedRequestHeaders) == 0 {
		policy.AllowedRequestHeaders = defaults.AllowedRequestHeaders
	} else {
		policy.AllowedRequestHeaders = normalizeHeaderSet(policy.AllowedRequestHeaders)
	}
	if len(policy.SafeResponseHeaders) == 0 {
		policy.SafeResponseHeaders = defaults.SafeResponseHeaders
	} else {
		policy.SafeResponseHeaders = normalizeHeaderSet(policy.SafeResponseHeaders)
	}
	if policy.RedirectLimit <= 0 {
		policy.RedirectLimit = defaults.RedirectLimit
	}
	if policy.DefaultTimeout <= 0 {
		policy.DefaultTimeout = defaults.DefaultTimeout
	}
	if policy.MaxTimeout <= 0 || policy.MaxTimeout > defaults.MaxTimeout {
		policy.MaxTimeout = defaults.MaxTimeout
	}
	if policy.DefaultTimeout > policy.MaxTimeout {
		policy.DefaultTimeout = policy.MaxTimeout
	}
	if policy.MaxResponseBytes <= 0 || policy.MaxResponseBytes > defaults.MaxResponseBytes {
		policy.MaxResponseBytes = defaults.MaxResponseBytes
	}
	if policy.MaxHeaderCount <= 0 {
		policy.MaxHeaderCount = defaults.MaxHeaderCount
	}
	if policy.MaxHeaderValueBytes <= 0 {
		policy.MaxHeaderValueBytes = defaults.MaxHeaderValueBytes
	}

	return policy
}

func cloneStringSet(input map[string]struct{}) map[string]struct{} {
	if input == nil {
		return nil
	}
	cloned := make(map[string]struct{}, len(input))
	for key := range input {
		cloned[key] = struct{}{}
	}

	return cloned
}

func normalizeHeaderSet(input map[string]struct{}) map[string]struct{} {
	output := make(map[string]struct{}, len(input))
	for key := range input {
		output[http.CanonicalHeaderKey(key)] = struct{}{}
	}

	return output
}

func normalizeMethodSet(input map[string]struct{}) map[string]struct{} {
	output := make(map[string]struct{}, len(input))
	for key := range input {
		output[strings.ToUpper(key)] = struct{}{}
	}

	return output
}

type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type netIPResolver struct{}

func (netIPResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

type dialContextFunc func(ctx context.Context, network string, address string) (net.Conn, error)

func defaultSecureHTTPTransport() http.RoundTripper {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

	return newPrivateIPGuardedTransport(netIPResolver{}, dialer.DialContext)
}

func newPrivateIPGuardedTransport(resolver ipResolver, dialer dialContextFunc) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network string, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split dial address: %w", err)
		}
		resolved, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if err := rejectNonPublicResolvedIPs(host, resolved); err != nil {
			return nil, err
		}

		return dialer(ctx, network, net.JoinHostPort(resolved[0].IP.String(), port))
	}

	return transport
}

func rejectNonPublicResolvedIPs(host string, resolved []net.IPAddr) error {
	if len(resolved) == 0 {
		return fmt.Errorf("host %q resolved to no IP addresses", host)
	}
	for _, ipAddr := range resolved {
		addr, ok := netip.AddrFromSlice(ipAddr.IP)
		if !ok || !isAllowedPublicIP(addr) {
			return fmt.Errorf("host %q resolved to non-public IP %s", host, ipAddr.IP.String())
		}
	}

	return nil
}

func isAllowedPublicIP(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	if addr.Is4In6() {
		addr = addr.Unmap()
	}
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedSpecialUsePrefixes() {
		if prefix.Contains(addr) {
			return false
		}
	}

	return true
}

func blockedSpecialUsePrefixes() []netip.Prefix {
	return []netip.Prefix{
		mustPrefix("0.0.0.0/8"),
		mustPrefix("10.0.0.0/8"),
		mustPrefix("100.64.0.0/10"),
		mustPrefix("127.0.0.0/8"),
		mustPrefix("169.254.0.0/16"),
		mustPrefix("172.16.0.0/12"),
		mustPrefix("192.0.0.0/24"),
		mustPrefix("192.0.2.0/24"),
		mustPrefix("192.168.0.0/16"),
		mustPrefix("198.18.0.0/15"),
		mustPrefix("198.51.100.0/24"),
		mustPrefix("203.0.113.0/24"),
		mustPrefix("224.0.0.0/4"),
		mustPrefix("240.0.0.0/4"),
		mustPrefix("255.255.255.255/32"),
		mustPrefix("::/128"),
		mustPrefix("::1/128"),
		mustPrefix("64:ff9b::/96"),
		mustPrefix("64:ff9b:1::/48"),
		mustPrefix("100::/64"),
		mustPrefix("2001::/23"),
		mustPrefix("2001:2::/48"),
		mustPrefix("2001:db8::/32"),
		mustPrefix("2002::/16"),
		mustPrefix("fc00::/7"),
		mustPrefix("fe80::/10"),
		mustPrefix("ff00::/8"),
	}
}

func mustPrefix(raw string) netip.Prefix {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		panic(err)
	}

	return prefix
}
