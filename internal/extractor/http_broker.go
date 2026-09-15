package extractor

import (
	"bytes"
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
	minSecretReflectionBytes   = 8
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
			http.MethodPost: {},
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
	PackID       string
	Manifest     Manifest
	PackIdentity VerifiedPackIdentity
	Method       string
	URL          string
	Headers      map[string]string
	// Body must not be mutated by the caller for the duration of Fetch.
	Body             []byte
	AuthProfileID    AuthProfileID
	Timeout          time.Duration
	MaxResponseBytes int64
	// OmitBrowserContext replaces the request-scoped browser context with an
	// empty one for this fetch: no grant match, no cookie attach, no typed
	// UA/Accept-Language/Referer fields.
	OmitBrowserContext bool
}

type HTTPFetchResponse struct {
	StatusCode int
	FinalURL   string
	Headers    http.Header
	Body       []byte
}

type HTTPBrokerConfig struct {
	Policy             HTTPBrokerPolicy
	Transport          http.RoundTripper
	AuthResolver       AuthProfileResolver
	AuthMaterializer   AuthMaterializer
	HostPolicyResolver HostPolicyResolver
}

type HTTPBroker struct {
	policy             HTTPBrokerPolicy
	transport          http.RoundTripper
	authResolver       AuthProfileResolver
	authMaterializer   AuthMaterializer
	hostPolicyResolver HostPolicyResolver
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
		policy:             policy,
		transport:          transport,
		authResolver:       config.AuthResolver,
		authMaterializer:   materializer,
		hostPolicyResolver: config.HostPolicyResolver,
	}
}

// Fetch is the single entry point for pack-originated fetches. The
// host-import bridge is the sole intended caller: alias endpoint-method and
// auth_profile_ref scoping are enforced there, upstream of this API.
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

func (b *HTTPBroker) fetch(ctx context.Context, request HTTPFetchRequest, knownSecrets *[]string) (HTTPFetchResponse, error) {
	policy, err := b.resolveRequestHostPolicy(ctx, request)
	if err != nil {
		return HTTPFetchResponse{}, err
	}
	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:             request.PackID,
		Manifest:           request.Manifest,
		Capability:         CapabilityHTTPFetch,
		PackIdentity:       request.PackIdentity,
		HostPolicyResolver: b.hostPolicyResolver,
	}, request.URL); err != nil {
		return HTTPFetchResponse{}, err
	}
	if request.AuthProfileID != "" {
		if err := ValidateCapabilityURL(CapabilityContext{
			PackID:             request.PackID,
			Manifest:           request.Manifest,
			Capability:         CapabilityAuthProfile,
			PackIdentity:       request.PackIdentity,
			HostPolicyResolver: b.hostPolicyResolver,
		}, request.URL); err != nil {
			return HTTPFetchResponse{}, err
		}
		if policy != nil {
			_, host, err := parseSafeHTTPURL(request.URL)
			if err != nil {
				return HTTPFetchResponse{}, err
			}
			if !policyAuthProfileMatchesHost(*policy, request.AuthProfileID, host) {
				return HTTPFetchResponse{}, redactErrorf("auth profile is not allowed by alias host policy")
			}
		}
	}

	method, err := b.validateMethod(request.Method)
	if err != nil {
		return HTTPFetchResponse{}, err
	}
	extendedCapable := ManifestHasCapability(request.Manifest, CapabilityHTTPFetchExtended) &&
		(policy == nil || policyAllowsCapability(*policy, CapabilityHTTPFetchExtended))
	validatedHeaders, err := b.validatePackHeaders(request.Headers, extendedCapable)
	if err != nil {
		return HTTPFetchResponse{}, err
	}
	wantsExtended := method == http.MethodPost || len(request.Body) > 0 || headersContainPrivilegedName(validatedHeaders)
	if wantsExtended && method != http.MethodGet && method != http.MethodHead && method != http.MethodPost {
		// A wider host-configured method vocabulary must not smuggle
		// extended features onto verbs outside the frozen channel set.
		return HTTPFetchResponse{}, errors.New("extended fetch features require GET, HEAD, or POST")
	}
	if wantsExtended && !extendedCapable {
		return HTTPFetchResponse{}, errors.New("extended fetch features require the extended fetch capability")
	}
	if wantsExtended && request.AuthProfileID != "" {
		return HTTPFetchResponse{}, errors.New("extended fetch request must not use an auth profile")
	}
	if len(request.Body) > 0 {
		if method != http.MethodPost {
			return HTTPFetchResponse{}, errors.New("request body requires the POST method")
		}
		if len(request.Body) > maxExtendedFetchBodyBytes {
			return HTTPFetchResponse{}, fmt.Errorf("request body exceeds the %d byte cap", maxExtendedFetchBodyBytes)
		}
		if !isExtendedBodyContentTypeAllowed(validatedHeaders.Get("Content-Type")) {
			return HTTPFetchResponse{}, errors.New("request body requires a single application/json or application/x-www-form-urlencoded content type")
		}
	}
	if request.OmitBrowserContext && request.AuthProfileID != "" {
		return HTTPFetchResponse{}, errors.New("omit_browser_context forbids auth_profile_ref")
	}
	if request.OmitBrowserContext {
		// Overwrite the context value so every reader — grant matching,
		// cookie attach, typed fields — sees an empty browser context.
		ctx = WithBrowserContext(ctx, BrowserRequestContext{})
	}
	browserCtx := browserContextFromContext(ctx)
	ctx, cancel := context.WithTimeout(ctx, b.effectiveTimeout(request))
	defer cancel()

	currentURL := request.URL
	var cookieReflection []string
	for redirects := 0; ; redirects++ {
		parsed, err := b.allowedFetchURL(ctx, request, policy, currentURL)
		if err != nil {
			return HTTPFetchResponse{}, err
		}
		grant, grantTarget := browserGrantMatch(browserCtx.Grants, method, currentURL, time.Now())
		grantScopedHop := grant != nil
		hopAttachesCookies := request.AuthProfileID == "" && !wantsExtended && !grantScopedHop && len(cookiesMatchingRequest(browserCtx.Cookies, currentURL)) > 0
		if hopAttachesCookies && parsed.Scheme != "https" {
			return HTTPFetchResponse{}, errors.New("cookie-authenticated request requires HTTPS")
		}
		if wantsExtended && parsed.Scheme != "https" {
			return HTTPFetchResponse{}, errors.New("extended fetch request requires HTTPS")
		}
		wireTarget := parsed.String()
		if grantScopedHop {
			// Send the canonical form of the policy-checked URL, not the
			// request spelling (host case, default port, bare query marker,
			// fragment) and not the stored grant field.
			wireTarget = grantTarget
		}
		var requestBody io.Reader
		if len(request.Body) > 0 {
			requestBody = bytes.NewReader(request.Body)
		}
		httpRequest, err := http.NewRequestWithContext(ctx, method, wireTarget, requestBody)
		if err != nil {
			return HTTPFetchResponse{}, fmt.Errorf("construct request: %w", err)
		}
		for name, values := range validatedHeaders {
			for _, value := range values {
				httpRequest.Header.Add(name, value)
			}
		}

		var grantReflection []string
		var extendedReflection []string
		switch {
		case grantScopedHop:
			// A scoped browser grant hop never mixes with host auth profiles,
			// browser cookies, or pack-owned extended features (body/privileged
			// headers); the extended guard is checked first, the pack-header
			// collision scan below is unreachable depth for such requests.
			if wantsExtended {
				return HTTPFetchResponse{}, errors.New("grant-scoped request must not use extended fetch features")
			}
			if request.AuthProfileID != "" {
				return HTTPFetchResponse{}, errors.New("grant-scoped request must not use an auth profile")
			}
			for _, header := range grant.Headers {
				if _, collision := validatedHeaders[http.CanonicalHeaderKey(header.Name)]; collision {
					return HTTPFetchResponse{}, errors.New("grant-scoped header collides with pack header")
				}
			}
			httpRequest.Header.Del("Cookie")
			if browserCtx.UserAgent != "" {
				httpRequest.Header.Set("User-Agent", browserCtx.UserAgent)
			}
			if browserCtx.AcceptLanguage != "" {
				httpRequest.Header.Set("Accept-Language", browserCtx.AcceptLanguage)
			}
			if browserCtx.RefererOrigin != "" {
				httpRequest.Header.Set("Referer", browserCtx.RefererOrigin+"/")
			}
			for _, header := range grant.Headers {
				httpRequest.Header.Set(header.Name, header.Value)
				*knownSecrets = appendNonEmptySecrets(*knownSecrets, header.Value)
				if len(header.Value) >= minSecretReflectionBytes {
					grantReflection = append(grantReflection, header.Value)
				}
				if header.Name == "authorization" {
					if _, credentials, ok := strings.Cut(header.Value, " "); ok {
						// Trim so a non-canonical spacing variant still keys
						// on the credential form an endpoint would echo.
						credentials = strings.TrimSpace(credentials)
						*knownSecrets = appendNonEmptySecrets(*knownSecrets, credentials)
						if len(credentials) >= minSecretReflectionBytes {
							grantReflection = append(grantReflection, credentials)
						}
					}
				}
			}
		case wantsExtended:
			// Extended requests never attach ambient credentials: browser
			// cookies are suppressed (hopAttachesCookies already excludes this
			// hop) and pack-owned privileged values register as secrets.
			httpRequest.Header.Del("Cookie")
			for name, values := range validatedHeaders {
				if !packHeaderNeedsExtended(name) {
					continue
				}
				for _, value := range values {
					*knownSecrets = appendNonEmptySecrets(*knownSecrets, value)
					if len(value) >= minSecretReflectionBytes {
						extendedReflection = append(extendedReflection, value)
					}
					if name == "Authorization" {
						if _, credentials, ok := strings.Cut(value, " "); ok {
							credentials = strings.TrimSpace(credentials)
							*knownSecrets = appendNonEmptySecrets(*knownSecrets, credentials)
							if len(credentials) >= minSecretReflectionBytes {
								extendedReflection = append(extendedReflection, credentials)
							}
						}
					}
				}
			}
		case request.AuthProfileID != "":
			if err := validateAliasAuthProfileScopeForURL(policy, request.AuthProfileID, currentURL); err != nil {
				return HTTPFetchResponse{}, err
			}
			if err := b.injectAuth(ctx, httpRequest, request, currentURL, knownSecrets); err != nil {
				return HTTPFetchResponse{}, err
			}
		default:
			cookieReflection = append(cookieReflection, attachBrowserCookies(ctx, httpRequest, currentURL, knownSecrets)...)
		}

		response, err := b.transport.RoundTrip(httpRequest)
		if err != nil {
			return HTTPFetchResponse{}, fmt.Errorf("http fetch failed for %s: %w", currentURL, err)
		}
		if response == nil {
			return HTTPFetchResponse{}, fmt.Errorf("http fetch failed for %s: empty response", currentURL)
		}

		if isRedirectStatus(response.StatusCode) {
			if response.Body != nil {
				_ = response.Body.Close()
			}
			switch {
			case grantScopedHop:
				return HTTPFetchResponse{}, errors.New("grant-scoped request must not redirect")
			case wantsExtended:
				// Single hop only: a pack-owned credential must not follow an
				// origin-controlled redirect target.
				return HTTPFetchResponse{}, errors.New("extended fetch request must not redirect")
			}
			location := response.Header.Get("Location")
			if redirects >= b.policy.RedirectLimit {
				return HTTPFetchResponse{}, fmt.Errorf("redirect limit exceeded for %s", currentURL)
			}
			nextURL, err := resolveRedirectURL(parsed, location)
			if err != nil {
				return HTTPFetchResponse{}, err
			}
			parsedNext, err := b.allowedFetchURL(ctx, request, policy, nextURL)
			if err != nil {
				return HTTPFetchResponse{}, fmt.Errorf("redirect denied: %w", err)
			}
			if request.AuthProfileID != "" && parsedNext.Scheme != "https" {
				return HTTPFetchResponse{}, fmt.Errorf("authenticated redirect to non-HTTPS url denied: %s", nextURL)
			}
			nextAttachesCookies := request.AuthProfileID == "" && len(cookiesMatchingRequest(browserCtx.Cookies, nextURL)) > 0
			if nextAttachesCookies && parsedNext.Scheme != "https" {
				return HTTPFetchResponse{}, fmt.Errorf("cookie-authenticated redirect to non-HTTPS url denied: %s", nextURL)
			}
			currentURL = nextURL
			continue
		}

		body, err := readCappedBody(response.Body, b.effectiveBodyCap(request))
		if err != nil {
			return HTTPFetchResponse{}, err
		}
		safeHeaders := b.safeResponseHeaders(response.Header)
		reflectionSecrets := cookieReflection
		switch {
		case grantScopedHop:
			// Union, not replacement: earlier hops may have attached cookie
			// secrets this response could replay (debug/history endpoints).
			reflectionSecrets = append(append([]string(nil), cookieReflection...), grantReflection...)
		case wantsExtended:
			reflectionSecrets = extendedReflection
		case request.AuthProfileID != "":
			reflectionSecrets = *knownSecrets
		}
		if len(reflectionSecrets) > 0 && len(body) > 0 {
			// Reflection can only be checked on bytes we can read. The
			// transport only transparently decodes the gzip encoding it
			// requested itself; any other declared encoding — or an
			// undecoded body from a custom RoundTripper — hides secret
			// echoes inside compressed bytes. Fail closed.
			enc := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
			if enc != "" && enc != "identity" && !response.Uncompressed {
				return HTTPFetchResponse{}, errors.New("opaque content encoding on secret-carrying response")
			}
		}
		if err := rejectSecretReflection(body, safeHeaders, reflectionSecrets); err != nil {
			return HTTPFetchResponse{}, err
		}

		recordLastHTTPFetchStatus(ctx, response.StatusCode)

		return HTTPFetchResponse{
			StatusCode: response.StatusCode,
			FinalURL:   RedactSensitive(currentURL, *knownSecrets...),
			Headers:    safeHeaders,
			Body:       body,
		}, nil
	}
}

func attachBrowserCookies(ctx context.Context, httpRequest *http.Request, currentURL string, knownSecrets *[]string) []string {
	if httpRequest == nil {
		return nil
	}
	matched := cookiesMatchingRequest(browserCookiesFromContext(ctx), currentURL)
	header := serializeCookieHeader(matched)
	if header == "" {
		return nil
	}
	httpRequest.Header.Set("Cookie", header)
	var reflection []string
	for _, cookie := range matched {
		if knownSecrets != nil {
			*knownSecrets = appendNonEmptySecrets(*knownSecrets, cookie.Value, cookie.Name+"="+cookie.Value)
		}
		if len(cookie.Value) >= minSecretReflectionBytes {
			reflection = append(reflection, cookie.Value)
		}
	}

	return reflection
}

func validateAliasAuthProfileScopeForURL(policy *ResolvedHostPolicy, profileID AuthProfileID, rawURL string) error {
	if policy == nil {
		return nil
	}
	_, host, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return err
	}
	if !policyAuthProfileMatchesHost(*policy, profileID, host) {
		return redactErrorf("auth profile is not allowed by alias host policy")
	}

	return nil
}

func (b *HTTPBroker) resolveRequestHostPolicy(ctx context.Context, request HTTPFetchRequest) (*ResolvedHostPolicy, error) {
	if !isAliasManifest(request.Manifest) {
		return nil, nil
	}
	policy, err := resolveAliasHostPolicy(ctx, b.hostPolicyResolver, request.PackIdentity, request.Manifest)
	if err != nil {
		return nil, redactErrorf("alias host policy denied request")
	}

	return &policy, nil
}

func (b *HTTPBroker) allowedFetchURL(ctx context.Context, request HTTPFetchRequest, policy *ResolvedHostPolicy, rawURL string) (*url.URL, error) {
	if policy == nil {
		return allowedHTTPURLForManifest(request.Manifest, rawURL)
	}
	parsed, host, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !policyAllowsCapability(*policy, CapabilityHTTPFetch) {
		return nil, redactErrorf("alias host policy does not allow http fetch")
	}
	if !policyBrokerMatchesHost(*policy, host) {
		return nil, redactErrorf("url is not allowed by alias broker policy")
	}

	return parsed, nil
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

func (b *HTTPBroker) validatePackHeaders(headers map[string]string, extendedCapable bool) (http.Header, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	if len(headers) > b.policy.MaxHeaderCount {
		return nil, errors.New("too many request headers")
	}

	validated := make(http.Header, len(headers))
	seen := make(map[string]struct{}, len(headers))
	for name, value := range headers {
		trimmed := strings.TrimSpace(name)
		canonical := http.CanonicalHeaderKey(trimmed)
		if canonical == "" || !isHTTPToken(trimmed) {
			return nil, errors.New("invalid request header name")
		}
		if _, dup := seen[canonical]; dup {
			return nil, errors.New("request header names must be unique after canonicalization")
		}
		seen[canonical] = struct{}{}
		if packHeaderNeedsExtended(canonical) {
			// Privileged names bypass the name-level secret heuristic: business
			// x-* token headers are the legitimate use of the extended channel.
			if isDeniedExtendedPackHeaderName(strings.ToLower(canonical)) {
				return nil, fmt.Errorf("request header %q is not allowed", canonical)
			}
			if !extendedCapable {
				return nil, fmt.Errorf("request header %q requires the extended fetch capability", canonical)
			}
			if len(value) > b.policy.MaxHeaderValueBytes {
				return nil, fmt.Errorf("request header %q value is too large", canonical)
			}
			if stringContainsControl(value) {
				return nil, fmt.Errorf("request header %q value contains control bytes", canonical)
			}
			if canonical == "Authorization" && !validatePackOwnedAuthorizationValue(value) {
				return nil, errors.New("request header \"Authorization\" value must be a scheme followed by credentials")
			}
			validated.Set(canonical, value)
			continue
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

func headersContainPrivilegedName(headers http.Header) bool {
	for name := range headers {
		if packHeaderNeedsExtended(name) {
			return true
		}
	}

	return false
}

// packHeaderNeedsExtended reports whether a canonicalized pack header name is
// part of the extended channel's privileged set: Authorization or any x-*
// business header (the deny list narrows that set afterwards).
func packHeaderNeedsExtended(canonical string) bool {
	return canonical == "Authorization" || strings.HasPrefix(canonical, "X-")
}

func isDeniedExtendedPackHeaderName(lower string) bool {
	if _, denied := deniedBrowserGrantHeaderExact[lower]; denied {
		return true
	}
	for _, prefix := range deniedBrowserGrantHeaderPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	return false
}

// validatePackOwnedAuthorizationValue requires "<scheme><SP><credentials>"
// form. Unlike browser grants, ambient schemes are allowed: the value is a
// pack-owned secret, never browser-negotiated state. Edge-trimmed credentials
// keep the extracted credential form exact for redaction/reflection keys, and
// consecutive spaces are rejected like on the grant side so a whitespace-
// normalizing endpoint cannot echo a credential form we did not register.
func validatePackOwnedAuthorizationValue(value string) bool {
	scheme, credentials, ok := strings.Cut(value, " ")
	if !ok || scheme == "" || !isHTTPToken(scheme) || credentials == "" || strings.Contains(value, "  ") {
		return false
	}

	return credentials == strings.TrimSpace(credentials)
}

func (b *HTTPBroker) injectAuth(ctx context.Context, httpRequest *http.Request, request HTTPFetchRequest, targetURL string, knownSecrets *[]string) error {
	if httpRequest.URL.Scheme != "https" {
		return fmt.Errorf("auth profile %q requires an HTTPS request target", request.AuthProfileID)
	}
	if b.authResolver == nil {
		return fmt.Errorf("auth profile %q requested but no auth resolver is configured", request.AuthProfileID)
	}
	resolved, err := b.authResolver.ResolveAuthProfile(ctx, request.PackID, request.AuthProfileID, targetURL)
	if err != nil {
		return err
	}
	if resolved.HeaderName == "" || resolved.HeaderValue == "" {
		return fmt.Errorf("auth profile %q resolved without a usable secret", request.AuthProfileID)
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
		return "", errors.New("redirect response missing Location")
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
		return nil, errors.New("response body cap must be positive")
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
