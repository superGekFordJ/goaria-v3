package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	webViewAuthCallbackTransportMode   = privateAuthRuntimeCallbackTransportMode
	webViewAuthCallbackContentTypeJSON = privateAuthRuntimeCallbackContentTypeJSON
	webViewAuthCaptureFormatJSON       = privateAuthRuntimeCaptureFormatJSON
)

type AuthWebViewDriver interface {
	OpenAuthSession(ctx context.Context, request WebViewAuthRequest, sink AuthWebViewSink) (AuthWebViewSession, error)
}

type AuthWebViewSession interface {
	Close() error
}

type AuthWebViewSink struct {
	OnSuccess func(AuthWebViewToken)
	OnCancel  func()
	OnError   func(error)
}

type AuthWebViewToken struct {
	Kind            AuthSecretKind
	Secret          string
	ExpiresAt       *time.Time
	RedactedDisplay string
}

type WebViewAuthRequest struct {
	PackID            string
	Manifest          Manifest
	ProfileID         AuthProfileID
	LoginURL          string
	AllowedDomains    []DomainRule
	Timeout           time.Duration
	Kind              AuthSecretKind
	CallbackTransport WebViewAuthCallbackTransport
	CollectorJS       string
	Capture           WebViewAuthCaptureContract
}

type WebViewAuthCallbackTransport struct {
	Mode         string
	ContentTypes []string
	MaxBodyBytes int64
}

type WebViewAuthCaptureContract struct {
	Format               string
	SecretCandidates     []string
	KindField            string
	ExpiresAtField       string
	RedactedDisplayField string
	TrimSpace            bool
	RejectCRLF           bool
}

type WebViewAuthStatus string

const (
	WebViewAuthStatusSuccess  WebViewAuthStatus = "success"
	WebViewAuthStatusCanceled WebViewAuthStatus = "canceled"
	WebViewAuthStatusTimeout  WebViewAuthStatus = "timeout"
	WebViewAuthStatusError    WebViewAuthStatus = "error"
)

type WebViewAuthResult struct {
	Status   WebViewAuthStatus   `json:"status"`
	PackID   string              `json:"pack_id"`
	Profile  AuthProfileID       `json:"profile_id"`
	Snapshot AuthProfileSnapshot `json:"snapshot,omitzero"`
	Message  string              `json:"message,omitempty"`
}

func (r WebViewAuthResult) String() string {
	return fmt.Sprintf("WebViewAuthResult{status:%q pack_id:%q profile_id:%q message:%q snapshot:%s}", r.Status, r.PackID, r.Profile, r.Message, r.Snapshot.String())
}

func (r WebViewAuthResult) GoString() string {
	return r.String()
}

type WebViewAuthCoordinator struct {
	store      AuthProfileStore
	driver     AuthWebViewDriver
	observerMu sync.RWMutex
	observer   WebViewAuthObserver
}

type WebViewAuthObserver interface {
	RecordWebViewAuthEvent(stage string, category string)
}

func NewWebViewAuthCoordinator(store AuthProfileStore, driver AuthWebViewDriver) *WebViewAuthCoordinator {
	return &WebViewAuthCoordinator{store: store, driver: driver}
}

func (c *WebViewAuthCoordinator) SetObserver(observer WebViewAuthObserver) {
	if c == nil {
		return
	}
	c.observerMu.Lock()
	defer c.observerMu.Unlock()
	c.observer = observer
}

func (c *WebViewAuthCoordinator) recordEvent(stage string, category string) {
	if c == nil {
		return
	}
	c.observerMu.RLock()
	observer := c.observer
	c.observerMu.RUnlock()
	if observer != nil {
		observer.RecordWebViewAuthEvent(stage, category)
	}
}

func (c *WebViewAuthCoordinator) Start(ctx context.Context, request WebViewAuthRequest) (WebViewAuthResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return WebViewAuthResult{}, errors.New("webview auth coordinator is nil")
	}
	if c.store == nil {
		return WebViewAuthResult{}, errors.New("webview auth coordinator requires an auth profile store")
	}
	if c.driver == nil {
		return WebViewAuthResult{}, errors.New("webview auth coordinator requires a driver")
	}

	validatedRequest, err := validateWebViewAuthRequest(request)
	if err != nil {
		return WebViewAuthResult{}, err
	}

	return c.startValidated(ctx, validatedRequest)
}

func (c *WebViewAuthCoordinator) startValidated(ctx context.Context, validatedRequest WebViewAuthRequest) (WebViewAuthResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return WebViewAuthResult{}, errors.New("webview auth coordinator is nil")
	}
	if c.store == nil {
		return WebViewAuthResult{}, errors.New("webview auth coordinator requires an auth profile store")
	}
	if c.driver == nil {
		return WebViewAuthResult{}, errors.New("webview auth coordinator requires a driver")
	}

	ctx, cancel := context.WithTimeout(ctx, effectiveWebViewTimeout(validatedRequest))
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

	session, err := c.driver.OpenAuthSession(ctx, validatedRequest, sink)
	if err != nil {
		return WebViewAuthResult{}, redactedError(fmt.Errorf("open auth webview for %s: %w", validatedRequest.LoginURL, err))
	}
	defer func() { _ = session.Close() }()

	event := terminal.wait(ctx)
	return c.handleTerminalEvent(validatedRequest, event)
}

func validateWebViewAuthRequest(request WebViewAuthRequest) (WebViewAuthRequest, error) {
	validated, err := validateWebViewAuthRequestBase(request)
	if err != nil {
		return WebViewAuthRequest{}, err
	}
	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:     validated.PackID,
		Manifest:   validated.Manifest,
		Capability: CapabilityAuthProfile,
	}, validated.LoginURL); err != nil {
		return WebViewAuthRequest{}, err
	}
	if err := validateWebViewAllowedDomainsManifestScope(validated); err != nil {
		return WebViewAuthRequest{}, err
	}

	return validated, nil
}

func validateWebViewAuthRequestBase(request WebViewAuthRequest) (WebViewAuthRequest, error) {
	if err := validateAuthProfileID(request.ProfileID); err != nil {
		return WebViewAuthRequest{}, redactedError(err)
	}
	if err := validateAuthSecretKind(request.Kind); err != nil {
		return WebViewAuthRequest{}, redactedError(err)
	}
	if err := validateWebViewAuthCallbackTransport(request.CallbackTransport); err != nil {
		return WebViewAuthRequest{}, err
	}
	if err := validateWebViewAuthCollectorJS(request.CollectorJS); err != nil {
		return WebViewAuthRequest{}, err
	}
	if err := validateWebViewAuthCaptureContract(request.Capture); err != nil {
		return WebViewAuthRequest{}, err
	}
	request.CallbackTransport.ContentTypes = cloneStringSlice(request.CallbackTransport.ContentTypes)
	loginRule, err := loginOriginDomainRule(request.LoginURL)
	if err != nil {
		return WebViewAuthRequest{}, err
	}
	if len(request.AllowedDomains) == 0 {
		request.AllowedDomains = []DomainRule{loginRule}
	}
	if err := validateDomainRules(request.AllowedDomains); err != nil {
		return WebViewAuthRequest{}, redactedError(err)
	}
	if err := validateWebViewAllowedDomainsLoginScope(request, loginRule); err != nil {
		return WebViewAuthRequest{}, err
	}
	request.AllowedDomains = cloneDomainRules(request.AllowedDomains)
	request.Capture.SecretCandidates = cloneStringSlice(request.Capture.SecretCandidates)

	return request, nil
}

func validateWebViewAuthCallbackTransport(transport WebViewAuthCallbackTransport) error {
	if transport.Mode != webViewAuthCallbackTransportMode {
		return errors.New("auth webview callback transport is invalid")
	}
	if transport.MaxBodyBytes <= 0 || transport.MaxBodyBytes > privateAuthRuntimeMaxCallbackBodyBytes {
		return errors.New("auth webview callback body limit is invalid")
	}
	if len(transport.ContentTypes) == 0 {
		return errors.New("auth webview callback content types are required")
	}
	seen := make(map[string]struct{}, len(transport.ContentTypes))
	for _, contentType := range transport.ContentTypes {
		if contentType != webViewAuthCallbackContentTypeJSON {
			return errors.New("auth webview callback content type is invalid")
		}
		if _, ok := seen[contentType]; ok {
			return errors.New("auth webview callback content types are invalid")
		}
		seen[contentType] = struct{}{}
	}

	return nil
}

func validateWebViewAuthCollectorJS(source string) error {
	if strings.TrimSpace(source) == "" {
		return errors.New("auth webview collector source is required")
	}
	if len([]byte(source)) > privateAuthRuntimeMaxCollectorJSBytes || strings.ContainsRune(source, '\x00') {
		return errors.New("auth webview collector source is invalid")
	}

	return nil
}

func cloneWebViewAuthCaptureContract(capture WebViewAuthCaptureContract) WebViewAuthCaptureContract {
	capture.SecretCandidates = cloneStringSlice(capture.SecretCandidates)

	return capture
}

func validateWebViewAuthCaptureContract(capture WebViewAuthCaptureContract) error {
	if capture.Format != webViewAuthCaptureFormatJSON {
		return errors.New("auth webview capture format is invalid")
	}
	if len(capture.SecretCandidates) == 0 {
		return errors.New("auth webview capture candidates are required")
	}
	seen := make(map[string]struct{}, len(capture.SecretCandidates))
	for _, candidate := range capture.SecretCandidates {
		if err := validatePrivateAuthRuntimeCaptureFieldPath(candidate); err != nil {
			return errors.New("auth webview capture field path is invalid")
		}
		if _, ok := seen[candidate]; ok {
			return errors.New("auth webview capture candidates are invalid")
		}
		seen[candidate] = struct{}{}
	}
	for _, optional := range []string{capture.KindField, capture.ExpiresAtField, capture.RedactedDisplayField} {
		if optional == "" {
			continue
		}
		if err := validatePrivateAuthRuntimeCaptureFieldPath(optional); err != nil {
			return errors.New("auth webview capture field path is invalid")
		}
	}

	return nil
}

func ParseWebViewAuthCallbackPayload(request WebViewAuthRequest, raw []byte) (AuthWebViewToken, error) {
	validated, err := validateWebViewAuthRequestBase(request)
	if err != nil {
		return AuthWebViewToken{}, errors.New("auth webview callback request is invalid")
	}
	if len(raw) == 0 || int64(len(raw)) > validated.CallbackTransport.MaxBodyBytes {
		return AuthWebViewToken{}, errors.New("auth webview callback payload is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return AuthWebViewToken{}, errors.New("auth webview callback payload is invalid")
	}
	if len(payload) == 0 {
		return AuthWebViewToken{}, errors.New("auth webview callback payload is invalid")
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err == nil {
		return AuthWebViewToken{}, errors.New("auth webview callback payload is invalid")
	} else if err != io.EOF {
		return AuthWebViewToken{}, errors.New("auth webview callback payload is invalid")
	}

	secret, ok, err := firstWebViewCaptureSecret(payload, validated.Capture)
	if err != nil || !ok {
		return AuthWebViewToken{}, errors.New("auth webview callback secret is invalid")
	}

	kind := validated.Kind
	if validated.Capture.KindField != "" {
		if value, exists := webViewCaptureValueAtPath(payload, validated.Capture.KindField); exists {
			rawKind, ok := value.(string)
			if !ok || rawKind == "" {
				return AuthWebViewToken{}, errors.New("auth webview callback kind is invalid")
			}
			kind = AuthSecretKind(rawKind)
		}
	}
	if kind != validated.Kind || validateAuthSecretKind(kind) != nil {
		return AuthWebViewToken{}, errors.New("auth webview callback kind is invalid")
	}

	var expiresAt *time.Time
	if validated.Capture.ExpiresAtField != "" {
		if value, exists := webViewCaptureValueAtPath(payload, validated.Capture.ExpiresAtField); exists {
			rawExpires, ok := value.(string)
			if !ok {
				return AuthWebViewToken{}, errors.New("auth webview callback expiry is invalid")
			}
			if rawExpires == "" {
				return AuthWebViewToken{}, errors.New("auth webview callback expiry is invalid")
			}
			parsed, err := time.Parse(time.RFC3339, rawExpires)
			if err != nil {
				return AuthWebViewToken{}, errors.New("auth webview callback expiry is invalid")
			}
			expiresAt = &parsed
		}
	}

	redactedDisplay := ""
	if validated.Capture.RedactedDisplayField != "" {
		if display, ok := webViewCaptureStringAtPath(payload, validated.Capture.RedactedDisplayField); ok {
			redactedDisplay = display
		}
	}

	return AuthWebViewToken{Kind: kind, Secret: secret, ExpiresAt: expiresAt, RedactedDisplay: redactedDisplay}, nil
}

func webViewCaptureStringAtPath(payload map[string]any, path string) (string, bool) {
	current, ok := webViewCaptureValueAtPath(payload, path)
	if !ok {
		return "", false
	}
	value, ok := current.(string)

	return value, ok
}

func firstWebViewCaptureSecret(payload map[string]any, capture WebViewAuthCaptureContract) (string, bool, error) {
	for _, path := range capture.SecretCandidates {
		secret, ok := webViewCaptureStringAtPath(payload, path)
		if !ok {
			continue
		}
		if capture.TrimSpace {
			secret = strings.TrimSpace(secret)
		}
		if secret == "" {
			continue
		}
		if capture.RejectCRLF && strings.ContainsAny(secret, "\r\n") {
			return "", false, errors.New("auth webview callback secret is invalid")
		}

		return secret, true, nil
	}

	return "", false, nil
}

func webViewCaptureValueAtPath(payload map[string]any, path string) (any, bool) {
	current := any(payload)
	for part := range strings.SplitSeq(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = object[part]
		if !ok {
			return "", false
		}
	}

	return current, true
}

func (c *WebViewAuthCoordinator) handleTerminalEvent(request WebViewAuthRequest, event webViewTerminalEvent) (WebViewAuthResult, error) {
	switch event.status {
	case WebViewAuthStatusSuccess:
		return c.handleSuccess(request, event.token)
	case WebViewAuthStatusCanceled:
		return WebViewAuthResult{
			Status:  WebViewAuthStatusCanceled,
			PackID:  request.PackID,
			Profile: request.ProfileID,
			Message: "authentication canceled",
		}, nil
	case WebViewAuthStatusTimeout:
		c.recordEvent("session", "timeout")
		return WebViewAuthResult{
			Status:  WebViewAuthStatusTimeout,
			PackID:  request.PackID,
			Profile: request.ProfileID,
			Message: "authentication timed out",
		}, nil
	case WebViewAuthStatusError:
		return WebViewAuthResult{}, redactedError(fmt.Errorf("auth webview failed: %w", event.err))
	default:
		return WebViewAuthResult{}, redactErrorf("auth webview ended with unknown status %q", event.status)
	}
}

func (c *WebViewAuthCoordinator) handleSuccess(request WebViewAuthRequest, token AuthWebViewToken) (WebViewAuthResult, error) {
	if token.Kind == "" {
		token.Kind = request.Kind
	}
	if err := validateAuthSecretKind(token.Kind); err != nil {
		return WebViewAuthResult{}, redactedError(err, token.Secret)
	}
	if token.Kind != request.Kind {
		return WebViewAuthResult{}, redactedError(fmt.Errorf("captured token kind %q does not match requested kind %q", token.Kind, request.Kind), token.Secret)
	}
	c.recordEvent("store", "set_attempted")
	snapshot, err := c.store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:          request.PackID,
		ProfileID:       request.ProfileID,
		Kind:            token.Kind,
		Secret:          token.Secret,
		AllowedDomains:  request.AllowedDomains,
		ExpiresAt:       token.ExpiresAt,
		RedactedDisplay: token.RedactedDisplay,
	})
	if err != nil {
		c.recordEvent("store", "set_failed")
		return WebViewAuthResult{}, redactedError(fmt.Errorf("store captured auth profile %q: %w", request.ProfileID, err), token.Secret)
	}
	c.recordEvent("store", "set_succeeded")

	return WebViewAuthResult{
		Status:   WebViewAuthStatusSuccess,
		PackID:   request.PackID,
		Profile:  request.ProfileID,
		Snapshot: snapshot,
		Message:  "authentication captured",
	}, nil
}

func effectiveWebViewTimeout(request WebViewAuthRequest) time.Duration {
	// Manifest.ResourceLimits.TimeoutMillis is the WASM sandbox execution
	// budget (used by the extractor runner) and must not constrain
	// interactive WebView login sessions, which are human-paced and can
	// legitimately wait minutes for the user to complete auth.
	if request.Timeout > 0 {
		return request.Timeout
	}

	return 30 * time.Second
}

func loginOriginDomainRule(rawURL string) (DomainRule, error) {
	parsed, host, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return DomainRule{}, err
	}
	if parsed.Scheme != "https" {
		return DomainRule{}, redactErrorf("auth login url %s must use https", rawURL)
	}

	return DomainRule{Host: host}, nil
}

func validateWebViewAllowedDomainsLoginScope(request WebViewAuthRequest, loginRule DomainRule) error {
	loginHost := loginRule.Host
	for _, rule := range request.AllowedDomains {
		ruleHost := rule.Host
		if !matchesDomainRule(ruleHost, loginRule) {
			return redactErrorf("auth profile domain %q is outside login origin %q", ruleHost, loginHost)
		}
		if rule.IncludeSubdomains && ruleHost == loginHost {
			return redactErrorf("auth profile domain %q may not expand beyond login origin", ruleHost)
		}
	}

	return nil
}

func validateWebViewAllowedDomainsManifestScope(request WebViewAuthRequest) error {
	for _, rule := range request.AllowedDomains {
		ruleHost := rule.Host
		if !manifestMatchesHost(request.Manifest, ruleHost) {
			return redactErrorf("auth profile domain %q is outside manifest allowlist", ruleHost)
		}
	}

	return nil
}

type webViewTerminalEvent struct {
	status WebViewAuthStatus
	token  AuthWebViewToken
	err    error
}

type authTerminal struct {
	once sync.Once
	done chan webViewTerminalEvent
}

func newAuthTerminal() *authTerminal {
	return &authTerminal{done: make(chan webViewTerminalEvent, 1)}
}

func (t *authTerminal) complete(event webViewTerminalEvent) {
	t.once.Do(func() {
		t.done <- event
	})
}

func (t *authTerminal) wait(ctx context.Context) webViewTerminalEvent {
	select {
	case event := <-t.done:
		return event
	case <-ctx.Done():
		timeoutEvent := webViewTerminalEvent{status: WebViewAuthStatusTimeout, err: ctx.Err()}
		t.complete(timeoutEvent)
		return <-t.done
	}
}
