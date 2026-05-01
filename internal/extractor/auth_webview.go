package extractor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
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
	PackID         string
	Manifest       Manifest
	ProfileID      AuthProfileID
	LoginURL       string
	AllowedDomains []DomainRule
	Timeout        time.Duration
	Kind           AuthSecretKind
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
	Snapshot AuthProfileSnapshot `json:"snapshot,omitempty"`
	Message  string              `json:"message,omitempty"`
}

func (r WebViewAuthResult) String() string {
	return fmt.Sprintf("WebViewAuthResult{status:%q pack_id:%q profile_id:%q message:%q snapshot:%s}", r.Status, r.PackID, r.Profile, r.Message, r.Snapshot.String())
}

func (r WebViewAuthResult) GoString() string {
	return r.String()
}

type WebViewAuthCoordinator struct {
	store  AuthProfileStore
	driver AuthWebViewDriver
}

func NewWebViewAuthCoordinator(store AuthProfileStore, driver AuthWebViewDriver) *WebViewAuthCoordinator {
	return &WebViewAuthCoordinator{store: store, driver: driver}
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
	if err := ValidateCapabilityURL(CapabilityContext{
		PackID:     request.PackID,
		Manifest:   request.Manifest,
		Capability: CapabilityAuthProfile,
	}, request.LoginURL); err != nil {
		return WebViewAuthRequest{}, err
	}
	if err := validateAuthProfileID(request.ProfileID); err != nil {
		return WebViewAuthRequest{}, redactedError(err)
	}
	if err := validateAuthSecretKind(request.Kind); err != nil {
		return WebViewAuthRequest{}, redactedError(err)
	}
	if len(request.AllowedDomains) == 0 {
		loginRule, err := loginOriginDomainRule(request.LoginURL)
		if err != nil {
			return WebViewAuthRequest{}, err
		}
		request.AllowedDomains = []DomainRule{loginRule}
	}
	if err := validateDomainRules(request.AllowedDomains); err != nil {
		return WebViewAuthRequest{}, redactedError(err)
	}
	if err := validateWebViewAllowedDomainsScope(request); err != nil {
		return WebViewAuthRequest{}, err
	}

	return request, nil
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
	if token.Kind != request.Kind {
		return WebViewAuthResult{}, redactedError(fmt.Errorf("captured token kind %q does not match requested kind %q", token.Kind, request.Kind), token.Secret)
	}

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
		return WebViewAuthResult{}, redactedError(fmt.Errorf("store captured auth profile %q: %w", request.ProfileID, err), token.Secret)
	}

	return WebViewAuthResult{
		Status:   WebViewAuthStatusSuccess,
		PackID:   request.PackID,
		Profile:  request.ProfileID,
		Snapshot: snapshot,
		Message:  "authentication captured",
	}, nil
}

func effectiveWebViewTimeout(request WebViewAuthRequest) time.Duration {
	manifestTimeout := time.Duration(request.Manifest.ResourceLimits.TimeoutMillis) * time.Millisecond
	if request.Timeout > 0 && manifestTimeout > 0 && request.Timeout < manifestTimeout {
		return request.Timeout
	}
	if manifestTimeout > 0 {
		return manifestTimeout
	}
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

func validateWebViewAllowedDomainsScope(request WebViewAuthRequest) error {
	loginRule, err := loginOriginDomainRule(request.LoginURL)
	if err != nil {
		return err
	}
	loginHost := loginRule.Host
	for _, rule := range request.AllowedDomains {
		ruleHost := rule.Host
		if !matchesDomainRule(ruleHost, loginRule) {
			return redactErrorf("auth profile domain %q is outside login origin %q", ruleHost, loginHost)
		}
		if rule.IncludeSubdomains && ruleHost == loginHost {
			return redactErrorf("auth profile domain %q may not expand beyond login origin", ruleHost)
		}
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
