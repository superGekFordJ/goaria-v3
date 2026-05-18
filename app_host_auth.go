package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/extractor"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const (
	appHostAuthUnavailableMessage    = "auth webview session unavailable"
	appHostAuthInProgressMessage     = "auth webview session already in progress"
	appHostAuthInvalidPayloadMessage = "auth webview callback payload invalid"
	appHostAuthCallbackErrorMessage  = "auth webview session failed"
	appHostAuthCallbackPrefix        = "/_goaria/auth/callback/"
	appHostAuthSessionHeader         = "X-Goaria-Auth-Session"
	appHostAuthInitialURL            = "/"
	appHostAuthCORSAllowedHeaders    = "content-type, x-goaria-auth-session"

	appHostAuthDiagnosticsEnv    = "GOARIA_WEBVIEW_AUTH_DIAGNOSTICS"
	appHostAuthDiagnosticsOutEnv = "GOARIA_WEBVIEW_AUTH_DIAGNOSTICS_OUT"

	appHostAuthDiagnosticSessionOpened             = "session_opened"
	appHostAuthDiagnosticSessionUnavailable        = "session_unavailable"
	appHostAuthDiagnosticSessionBusy               = "session_busy"
	appHostAuthDiagnosticWindowOpenFailed          = "window_open_failed"
	appHostAuthDiagnosticPreflightAccepted         = "preflight_accepted"
	appHostAuthDiagnosticPreflightOriginRejected   = "preflight_origin_rejected"
	appHostAuthDiagnosticPreflightMethodRejected   = "preflight_method_rejected"
	appHostAuthDiagnosticPreflightHeaderRejected   = "preflight_header_rejected"
	appHostAuthDiagnosticPostOriginRejected        = "post_origin_rejected"
	appHostAuthDiagnosticPostMethodRejected        = "post_method_rejected"
	appHostAuthDiagnosticPostContentTypeRejected   = "post_content_type_rejected"
	appHostAuthDiagnosticPostSessionHeaderRejected = "post_session_header_rejected"
	appHostAuthDiagnosticPostBodyRejected          = "post_body_rejected"
	appHostAuthDiagnosticPostPayloadRejected       = "post_payload_rejected"
	appHostAuthDiagnosticPostAccepted              = "post_accepted"
	appHostAuthDiagnosticPostExpired               = "post_expired"
	appHostAuthDiagnosticTerminalSuccess           = "terminal_success"
	appHostAuthDiagnosticTerminalCancel            = "terminal_cancel"
	appHostAuthDiagnosticTerminalError             = "terminal_error"
	appHostAuthDiagnosticSessionClosed             = "session_closed"
)

var _ extractor.AuthWebViewDriver = (*appHostAuthDriver)(nil)

var appHostAuthDiagnosticCategories = map[string]struct{}{
	appHostAuthDiagnosticSessionOpened:             {},
	appHostAuthDiagnosticSessionUnavailable:        {},
	appHostAuthDiagnosticSessionBusy:               {},
	appHostAuthDiagnosticWindowOpenFailed:          {},
	appHostAuthDiagnosticPreflightAccepted:         {},
	appHostAuthDiagnosticPreflightOriginRejected:   {},
	appHostAuthDiagnosticPreflightMethodRejected:   {},
	appHostAuthDiagnosticPreflightHeaderRejected:   {},
	appHostAuthDiagnosticPostOriginRejected:        {},
	appHostAuthDiagnosticPostMethodRejected:        {},
	appHostAuthDiagnosticPostContentTypeRejected:   {},
	appHostAuthDiagnosticPostSessionHeaderRejected: {},
	appHostAuthDiagnosticPostBodyRejected:          {},
	appHostAuthDiagnosticPostPayloadRejected:       {},
	appHostAuthDiagnosticPostAccepted:              {},
	appHostAuthDiagnosticPostExpired:               {},
	appHostAuthDiagnosticTerminalSuccess:           {},
	appHostAuthDiagnosticTerminalCancel:            {},
	appHostAuthDiagnosticTerminalError:             {},
	appHostAuthDiagnosticSessionClosed:             {},
}

type appHostAuthDriver struct {
	app         *App
	factory     hostAuthSessionWindowFactory
	diagnostics appHostAuthDiagnosticObserver

	mu       sync.Mutex
	inflight *appHostAuthSession
}

type appHostAuthDiagnosticObserver interface {
	observeAppHostAuthDiagnostic(appHostAuthDiagnosticEvent)
}

type appHostAuthDiagnosticEvent struct {
	Category string `json:"category"`
}

type appHostAuthJSONLDiagnosticObserver struct {
	mu     sync.Mutex
	writer io.Writer
	path   string
}

type hostAuthSessionWindowFactory interface {
	OpenHostAuthSession(ctx context.Context, request appHostAuthSessionRequest, callbacks appHostAuthSessionCallbacks) (hostAuthSessionWindow, error)
}

type hostAuthSessionWindow interface {
	Close() error
}

type appHostAuthSessionCallbacks struct {
	Success func(appHostAuthSessionPayload)
	Cancel  func()
	Error   func(error)
}

type appHostAuthSessionRequest struct {
	PackID            string
	ProfileID         extractor.AuthProfileID
	Kind              extractor.AuthSecretKind
	LoginURL          string
	AllowedDomains    []extractor.DomainRule
	Timeout           time.Duration
	CallbackURL       string
	CallbackPath      string
	SessionToken      string
	AuthPageOrigin    string
	CallbackTransport extractor.WebViewAuthCallbackTransport
	CollectorJS       string
	Capture           extractor.WebViewAuthCaptureContract
	webViewRequest    extractor.WebViewAuthRequest
}

type appHostAuthSessionPayload struct {
	Kind            extractor.AuthSecretKind
	Secret          string
	ExpiresAt       *time.Time
	RedactedDisplay string
}

type appHostAuthSession struct {
	driver  *appHostAuthDriver
	sink    extractor.AuthWebViewSink
	kind    extractor.AuthSecretKind
	cleanup func()
	request extractor.WebViewAuthRequest

	mu     sync.Mutex
	window hostAuthSessionWindow

	terminalOnce sync.Once
	releaseOnce  sync.Once
	closeOnce    sync.Once
}

type wailsHostAuthSessionWindowFactory struct {
	app *App
}

type wailsHostAuthSessionWindow struct {
	mu         sync.Mutex
	window     appHostAuthWebviewWindow
	unregister func()
}

type wailsHostAuthSessionWindowWrapper struct {
	window *application.WebviewWindow
}

type appHostAuthCallbackRegistry struct {
	mu       sync.Mutex
	handlers map[string]*appHostAuthCallbackHandler
}

type appHostAuthCallbackHandler struct {
	path     string
	registry *appHostAuthCallbackRegistry
	request  extractor.WebViewAuthRequest
	token    string
	session  *appHostAuthSession
}

type appHostAuthWebviewWindow interface {
	OnWindowEvent(eventType events.WindowEventType, callback func(event *application.WindowEvent)) func()
	RegisterHook(eventType events.WindowEventType, callback func(event *application.WindowEvent)) func()
	SetURL(s string) application.Window
	ExecJS(js string)
}

type appHostAuthWindowCreator interface {
	NewWithOptions(options application.WebviewWindowOptions) *application.WebviewWindow
}

func newAppHostAuthDriver(app *App) *appHostAuthDriver {
	return newAppHostAuthDriverWithFactory(app, nil)
}

func newAppHostAuthDriverWithFactory(app *App, factory hostAuthSessionWindowFactory) *appHostAuthDriver {
	return newAppHostAuthDriverWithFactoryAndDiagnostics(app, factory, newAppHostAuthDiagnosticObserverFromEnv())
}

func newAppHostAuthDriverWithFactoryAndDiagnostics(app *App, factory hostAuthSessionWindowFactory, observer appHostAuthDiagnosticObserver) *appHostAuthDriver {
	if factory == nil {
		factory = &wailsHostAuthSessionWindowFactory{app: app}
	}

	return &appHostAuthDriver{app: app, factory: factory, diagnostics: observer}
}

func newAppHostAuthDiagnosticObserverFromEnv() appHostAuthDiagnosticObserver {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(appHostAuthDiagnosticsEnv))) {
	case "1", "true", "category", "categories":
	default:
		return nil
	}
	if outPath := strings.TrimSpace(os.Getenv(appHostAuthDiagnosticsOutEnv)); outPath != "" {
		file, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil
		}
		_ = file.Close()

		return &appHostAuthJSONLDiagnosticObserver{path: outPath}
	}

	return &appHostAuthJSONLDiagnosticObserver{writer: os.Stderr}
}

func (o *appHostAuthJSONLDiagnosticObserver) observeAppHostAuthDiagnostic(event appHostAuthDiagnosticEvent) {
	if o == nil || o.writer == nil && o.path == "" || !appHostAuthDiagnosticCategoryAllowed(event.Category) {
		return
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return
	}
	raw = append(raw, '\n')

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.path != "" {
		file, err := os.OpenFile(o.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		defer file.Close()
		_, _ = file.Write(raw)
		return
	}
	_, _ = o.writer.Write(raw)
}

func (d *appHostAuthDriver) observe(category string) {
	if d == nil || d.diagnostics == nil || !appHostAuthDiagnosticCategoryAllowed(category) {
		return
	}
	defer func() {
		_ = recover()
	}()
	d.diagnostics.observeAppHostAuthDiagnostic(appHostAuthDiagnosticEvent{Category: category})
}

func (h *appHostAuthCallbackHandler) observe(category string) {
	if h == nil || h.session == nil || h.session.driver == nil {
		return
	}
	h.session.driver.observe(category)
}

func appHostAuthDiagnosticCategoryAllowed(category string) bool {
	_, ok := appHostAuthDiagnosticCategories[category]

	return ok
}

func (d *appHostAuthDriver) OpenAuthSession(ctx context.Context, request extractor.WebViewAuthRequest, sink extractor.AuthWebViewSink) (extractor.AuthWebViewSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.app == nil || d.factory == nil {
		if d != nil {
			d.observe(appHostAuthDiagnosticSessionUnavailable)
		}
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	if err := d.app.hostAuthSessionAvailable(); err != nil {
		d.observe(appHostAuthDiagnosticSessionUnavailable)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		d.observe(appHostAuthDiagnosticSessionUnavailable)
		return nil, errors.New(appHostAuthUnavailableMessage)
	}

	callbackPath, callbackToken, err := d.app.registerHostAuthCallbackSession(request)
	if err != nil {
		d.observe(appHostAuthDiagnosticSessionUnavailable)
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	cleanup := sync.OnceFunc(func() {
		d.app.unregisterHostAuthCallback(callbackPath)
	})

	session := &appHostAuthSession{driver: d, sink: sink, kind: request.Kind, cleanup: cleanup, request: request}
	if !d.app.hostAuthCallbackRegistry().bind(callbackPath, session) {
		cleanup()
		d.observe(appHostAuthDiagnosticSessionUnavailable)
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	if err := d.setInflight(session); err != nil {
		cleanup()
		if err.Error() == appHostAuthInProgressMessage {
			d.observe(appHostAuthDiagnosticSessionBusy)
		} else {
			d.observe(appHostAuthDiagnosticSessionUnavailable)
		}
		return nil, err
	}

	window, err := d.factory.OpenHostAuthSession(ctx, appHostAuthSessionRequestFromWebView(request, callbackPath, appHostAuthCallbackURL(callbackPath), callbackToken), appHostAuthSessionCallbacks{
		Success: func(payload appHostAuthSessionPayload) { _ = session.succeed(payload) },
		Cancel:  func() { _ = session.cancel() },
		Error:   func(err error) { _ = session.fail(err) },
	})
	if err != nil {
		session.release()
		d.observe(appHostAuthDiagnosticWindowOpenFailed)
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	if window == nil {
		session.release()
		d.observe(appHostAuthDiagnosticWindowOpenFailed)
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	session.setWindow(window)
	d.observe(appHostAuthDiagnosticSessionOpened)

	return session, nil
}

func (d *appHostAuthDriver) setInflight(session *appHostAuthSession) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inflight != nil {
		return errors.New(appHostAuthInProgressMessage)
	}
	d.inflight = session

	return nil
}

func (d *appHostAuthDriver) clearInflight(session *appHostAuthSession) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inflight == session {
		d.inflight = nil
	}
}

func (s *appHostAuthSession) setWindow(window hostAuthSessionWindow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.window = window
}

func (s *appHostAuthSession) succeed(payload appHostAuthSessionPayload) bool {
	won := false
	s.terminalOnce.Do(func() {
		token, err := appHostAuthTokenFromPayload(s.kind, payload)
		if err != nil {
			s.release()
			if s.driver != nil {
				s.driver.observe(appHostAuthDiagnosticTerminalError)
			}
			if s.sink.OnError != nil {
				s.sink.OnError(err)
			}
			won = true
			return
		}

		s.release()
		if s.driver != nil {
			s.driver.observe(appHostAuthDiagnosticTerminalSuccess)
		}
		if s.sink.OnSuccess != nil {
			s.sink.OnSuccess(token)
		}
		won = true
	})

	return won
}

func (s *appHostAuthSession) cancel() bool {
	won := false
	s.terminalOnce.Do(func() {
		s.release()
		if s.driver != nil {
			s.driver.observe(appHostAuthDiagnosticTerminalCancel)
		}
		if s.sink.OnCancel != nil {
			s.sink.OnCancel()
		}
		won = true
	})

	return won
}

func (s *appHostAuthSession) fail(err error) bool {
	won := false
	s.terminalOnce.Do(func() {
		s.release()
		if s.driver != nil {
			s.driver.observe(appHostAuthDiagnosticTerminalError)
		}
		if s.sink.OnError != nil {
			s.sink.OnError(appHostAuthSanitizedCallbackError(err))
		}
		won = true
	})

	return won
}

func (s *appHostAuthSession) release() {
	if s == nil {
		return
	}
	s.releaseOnce.Do(func() {
		if s.cleanup != nil {
			s.cleanup()
		}
		if s.driver != nil {
			s.driver.clearInflight(s)
		}
	})
}

func (s *appHostAuthSession) Close() error {
	if s == nil {
		return nil
	}
	s.terminalOnce.Do(func() {})
	s.release()

	var closeErr error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		window := s.window
		s.mu.Unlock()
		if window != nil {
			closeErr = window.Close()
		}
		if s.driver != nil {
			s.driver.observe(appHostAuthDiagnosticSessionClosed)
		}
	})

	return closeErr
}

func appHostAuthSessionRequestFromWebView(request extractor.WebViewAuthRequest, callbackPath string, callbackURL string, sessionToken string) appHostAuthSessionRequest {
	return appHostAuthSessionRequest{
		PackID:            request.PackID,
		ProfileID:         request.ProfileID,
		Kind:              request.Kind,
		LoginURL:          request.LoginURL,
		AllowedDomains:    cloneAppHostAuthDomainRules(request.AllowedDomains),
		Timeout:           request.Timeout,
		CallbackURL:       callbackURL,
		CallbackPath:      callbackPath,
		SessionToken:      sessionToken,
		AuthPageOrigin:    appHostAuthOriginFromURL(request.LoginURL),
		CallbackTransport: cloneAppHostAuthCallbackTransport(request.CallbackTransport),
		CollectorJS:       request.CollectorJS,
		Capture:           cloneAppHostAuthCapture(request.Capture),
		webViewRequest:    cloneAppHostAuthWebViewRequest(request),
	}
}

func appHostAuthTokenFromPayload(expected extractor.AuthSecretKind, payload appHostAuthSessionPayload) (extractor.AuthWebViewToken, error) {
	if payload.Kind != extractor.AuthSecretKindBearer && payload.Kind != extractor.AuthSecretKindCookie {
		return extractor.AuthWebViewToken{}, errors.New(appHostAuthInvalidPayloadMessage)
	}
	if payload.Kind != expected {
		return extractor.AuthWebViewToken{}, errors.New(appHostAuthInvalidPayloadMessage)
	}
	secret := strings.TrimSpace(payload.Secret)
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return extractor.AuthWebViewToken{}, errors.New(appHostAuthInvalidPayloadMessage)
	}

	return extractor.AuthWebViewToken{
		Kind:            payload.Kind,
		Secret:          secret,
		ExpiresAt:       cloneAppHostAuthTime(payload.ExpiresAt),
		RedactedDisplay: appHostAuthSafeDisplay(payload.Kind, secret, payload.RedactedDisplay),
	}, nil
}

func appHostAuthSafeDisplay(kind extractor.AuthSecretKind, secret string, display string) string {
	display = strings.TrimSpace(display)
	if display == "" || appHostAuthDisplayLeaksSecret(kind, secret, display) {
		return ""
	}

	return extractor.RedactSensitive(display, appHostAuthSensitiveForms(kind, secret)...)
}

func appHostAuthDisplayLeaksSecret(kind extractor.AuthSecretKind, secret string, display string) bool {
	redacted := extractor.RedactSensitive(display, appHostAuthSensitiveForms(kind, secret)...)

	return redacted != display
}

func appHostAuthSensitiveForms(kind extractor.AuthSecretKind, secret string) []string {
	forms := []string{secret}
	if kind == extractor.AuthSecretKindBearer {
		forms = append(forms, "Bearer "+secret)
	}
	if kind == extractor.AuthSecretKindCookie {
		for _, part := range strings.Split(secret, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			forms = append(forms, part)
			if _, value, ok := strings.Cut(part, "="); ok {
				forms = append(forms, strings.TrimSpace(value))
			}
		}
	}

	return compactAppHostAuthStrings(forms)
}

func appHostAuthSanitizedCallbackError(err error) error {
	if err == nil {
		return errors.New(appHostAuthCallbackErrorMessage)
	}
	redacted := strings.TrimSpace(extractor.RedactSensitive(err.Error()))
	if redacted == "" || redacted != err.Error() {
		return errors.New(appHostAuthCallbackErrorMessage)
	}

	return fmt.Errorf("%s: %s", appHostAuthCallbackErrorMessage, redacted)
}

func (a *App) hostAuthSessionAvailable() error {
	_, _, err := a.hostAuthSessionWindowContext()

	return err
}

func (a *App) hostAuthSessionWindowContext() (*application.App, *application.WebviewWindow, error) {
	if a == nil {
		return nil, nil, errors.New(appHostAuthUnavailableMessage)
	}
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	if a.app == nil || a.window == nil {
		return nil, nil, errors.New(appHostAuthUnavailableMessage)
	}

	return a.app, a.window, nil
}

func (a *App) hostAuthCallbackMiddleware(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil || r == nil || !strings.HasPrefix(r.URL.Path, appHostAuthCallbackPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		handler := a.hostAuthCallbackRegistry().lookup(r.URL.Path)
		if handler == nil {
			http.Error(w, "expired", http.StatusGone)
			return
		}
		handler.serveHTTP(w, r)
	})
}

func (a *App) registerHostAuthCallbackSession(request extractor.WebViewAuthRequest) (string, string, error) {
	if a == nil {
		return "", "", errors.New(appHostAuthUnavailableMessage)
	}
	path, err := newAppHostAuthCallbackPath()
	if err != nil {
		return "", "", err
	}
	token, err := newAppHostAuthSessionToken()
	if err != nil {
		return "", "", err
	}
	registry := a.hostAuthCallbackRegistry()
	registry.register(path, &appHostAuthCallbackHandler{path: path, registry: registry, request: cloneAppHostAuthWebViewRequest(request), token: token})

	return path, token, nil
}

func (a *App) unregisterHostAuthCallback(path string) {
	if a == nil || path == "" {
		return
	}
	a.hostAuthCallbackRegistry().unregister(path)
}

func (a *App) hostAuthCallbackRegistry() *appHostAuthCallbackRegistry {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if a.hostAuthCallbacks == nil {
		a.hostAuthCallbacks = &appHostAuthCallbackRegistry{handlers: make(map[string]*appHostAuthCallbackHandler)}
	}

	return a.hostAuthCallbacks
}

func (r *appHostAuthCallbackRegistry) register(path string, handler *appHostAuthCallbackHandler) {
	if r == nil || path == "" || handler == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handlers == nil {
		r.handlers = make(map[string]*appHostAuthCallbackHandler)
	}
	r.handlers[path] = handler
}

func (r *appHostAuthCallbackRegistry) bind(path string, session *appHostAuthSession) bool {
	if r == nil || path == "" || session == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	handler := r.handlers[path]
	if handler == nil {
		return false
	}
	handler.session = session

	return true
}

func (r *appHostAuthCallbackRegistry) lookup(path string) *appHostAuthCallbackHandler {
	if r == nil || path == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.handlers[path]
}

func (r *appHostAuthCallbackRegistry) unregister(path string) {
	if r == nil || path == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.handlers, path)
}

func (h *appHostAuthCallbackHandler) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.session == nil {
		http.Error(w, "expired", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodOptions {
		h.handlePreflight(w, r)
		return
	}
	if r.Method != http.MethodPost {
		h.observe(appHostAuthDiagnosticPostMethodRejected)
		http.Error(w, "invalid", http.StatusMethodNotAllowed)
		return
	}
	if !h.validateOrigin(r.Header.Get("Origin")) {
		h.observe(appHostAuthDiagnosticPostOriginRejected)
		http.Error(w, "invalid", http.StatusForbidden)
		return
	}
	writeAppHostAuthCORSHeaders(w, r.Header.Get("Origin"))
	if !appHostAuthContentTypeAllowed(r.Header.Get("Content-Type"), h.request.CallbackTransport.ContentTypes) {
		h.fail(w, appHostAuthDiagnosticPostContentTypeRejected)
		return
	}
	if !appHostAuthTokenMatches(r.Header.Get(appHostAuthSessionHeader), h.token) {
		h.fail(w, appHostAuthDiagnosticPostSessionHeaderRejected)
		return
	}
	raw, err := appHostAuthReadBoundedBody(r.Body, h.request.CallbackTransport.MaxBodyBytes)
	if err != nil {
		h.fail(w, appHostAuthDiagnosticPostBodyRejected)
		return
	}
	token, err := extractor.ParseWebViewAuthCallbackPayload(h.request, raw)
	if err != nil {
		h.fail(w, appHostAuthDiagnosticPostPayloadRejected)
		return
	}
	if !h.session.succeed(appHostAuthSessionPayload{Kind: token.Kind, Secret: token.Secret, ExpiresAt: token.ExpiresAt, RedactedDisplay: token.RedactedDisplay}) {
		h.observe(appHostAuthDiagnosticPostExpired)
		http.Error(w, "expired", http.StatusConflict)
		return
	}
	h.observe(appHostAuthDiagnosticPostAccepted)
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, "accepted")
}

func (h *appHostAuthCallbackHandler) fail(w http.ResponseWriter, category string) {
	if !h.session.fail(errors.New(appHostAuthInvalidPayloadMessage)) {
		h.observe(appHostAuthDiagnosticPostExpired)
		http.Error(w, "expired", http.StatusConflict)
		return
	}
	h.observe(category)
	http.Error(w, "invalid", http.StatusBadRequest)
}

func (h *appHostAuthCallbackHandler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !h.validateOrigin(origin) {
		h.observe(appHostAuthDiagnosticPreflightOriginRejected)
		http.Error(w, "invalid", http.StatusForbidden)
		return
	}
	if !strings.EqualFold(r.Header.Get("Access-Control-Request-Method"), http.MethodPost) {
		h.observe(appHostAuthDiagnosticPreflightMethodRejected)
		http.Error(w, "invalid", http.StatusMethodNotAllowed)
		return
	}
	if !appHostAuthCORSHeadersAllowed(r.Header.Values("Access-Control-Request-Headers")) {
		h.observe(appHostAuthDiagnosticPreflightHeaderRejected)
		http.Error(w, "invalid", http.StatusForbidden)
		return
	}
	writeAppHostAuthCORSHeaders(w, origin)
	h.observe(appHostAuthDiagnosticPreflightAccepted)
	w.WriteHeader(http.StatusNoContent)
}

func (h *appHostAuthCallbackHandler) validateOrigin(origin string) bool {
	expected := appHostAuthOriginFromURL(h.request.LoginURL)

	return origin != "" && expected != "" && origin == expected
}

func writeAppHostAuthCORSHeaders(w http.ResponseWriter, origin string) {
	header := w.Header()
	header.Add("Vary", "Origin")
	header.Add("Vary", "Access-Control-Request-Method")
	header.Add("Vary", "Access-Control-Request-Headers")
	header.Set("Access-Control-Allow-Origin", origin)
	header.Set("Access-Control-Allow-Methods", http.MethodPost)
	header.Set("Access-Control-Allow-Headers", appHostAuthCORSAllowedHeaders)
}

func appHostAuthCORSHeadersAllowed(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			header := strings.ToLower(strings.TrimSpace(part))
			if header == "" {
				return false
			}
			switch header {
			case "content-type", strings.ToLower(appHostAuthSessionHeader):
				seen[header] = struct{}{}
			default:
				return false
			}
		}
	}
	_, hasContentType := seen["content-type"]
	_, hasSessionHeader := seen[strings.ToLower(appHostAuthSessionHeader)]

	return hasContentType && hasSessionHeader
}

func appHostAuthContentTypeAllowed(header string, allowed []string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil || mediaType == "" {
		return false
	}
	for _, value := range allowed {
		if mediaType == value {
			return true
		}
	}

	return false
}

func appHostAuthTokenMatches(got string, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func appHostAuthReadBoundedBody(body io.Reader, maxBytes int64) ([]byte, error) {
	if body == nil || maxBytes <= 0 {
		return nil, errors.New(appHostAuthInvalidPayloadMessage)
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, errors.New(appHostAuthInvalidPayloadMessage)
	}
	if int64(len(raw)) > maxBytes {
		return nil, errors.New(appHostAuthInvalidPayloadMessage)
	}

	return raw, nil
}

func newAppHostAuthCallbackPath() (string, error) {
	random, err := randomAppHostAuthHex(24)
	if err != nil {
		return "", err
	}

	return appHostAuthCallbackPrefix + random, nil
}

func newAppHostAuthSessionToken() (string, error) {
	return randomAppHostAuthHex(32)
}

func randomAppHostAuthHex(bytesLen int) (string, error) {
	buffer := make([]byte, bytesLen)
	if _, err := rand.Read(buffer); err != nil {
		return "", errors.New(appHostAuthUnavailableMessage)
	}

	return hex.EncodeToString(buffer), nil
}

func appHostAuthCallbackURL(path string) string {
	switch runtime.GOOS {
	case "windows":
		return "http://wails.localhost" + path
	case "darwin", "linux":
		return "wails://localhost" + path
	default:
		return path
	}
}

func appHostAuthOriginFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	return parsed.Scheme + "://" + parsed.Host
}

func (f *wailsHostAuthSessionWindowFactory) OpenHostAuthSession(_ context.Context, request appHostAuthSessionRequest, callbacks appHostAuthSessionCallbacks) (hostAuthSessionWindow, error) {
	app, mainWindow, err := f.app.hostAuthSessionWindowContext()
	if err != nil {
		return nil, err
	}
	if app.Window == nil || mainWindow == nil {
		return nil, errors.New(appHostAuthUnavailableMessage)
	}

	window, err := openWailsHostAuthSessionWindow(app.Window, request, callbacks)
	if err != nil {
		return nil, err
	}

	return window, nil
}

func openWailsHostAuthSessionWindow(creator appHostAuthWindowCreator, request appHostAuthSessionRequest, callbacks appHostAuthSessionCallbacks) (hostAuthSessionWindow, error) {
	if creator == nil {
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	window := creator.NewWithOptions(application.WebviewWindowOptions{
		Name:             appHostAuthSessionWindowName(request),
		Title:            "GoAria Auth Session",
		URL:              appHostAuthInitialURL,
		Width:            720,
		Height:           760,
		MinWidth:         480,
		MinHeight:        560,
		AlwaysOnTop:      true,
		Hidden:           false,
		BackgroundType:   application.BackgroundTypeSolid,
		BackgroundColour: application.NewRGBA(12, 12, 15, 255),
	})
	if window == nil {
		return nil, errors.New(appHostAuthUnavailableMessage)
	}

	return setupWailsHostAuthSessionWindow(&wailsHostAuthSessionWindowWrapper{window: window}, request, callbacks), nil
}

func setupWailsHostAuthSessionWindow(window appHostAuthWebviewWindow, request appHostAuthSessionRequest, callbacks appHostAuthSessionCallbacks) hostAuthSessionWindow {
	unregisters := make([]func(), 0, 1+len(appHostAuthNavigationCompleteEvents()))
	unregisters = append(unregisters, window.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		if callbacks.Cancel != nil {
			callbacks.Cancel()
		}
	}))
	inject := func(_ *application.WindowEvent) {
		window.ExecJS(renderAppHostAuthCollectorJS(request))
	}
	for _, eventType := range appHostAuthNavigationCompleteEvents() {
		unregisters = append(unregisters, window.RegisterHook(eventType, inject))
	}
	window.SetURL(request.LoginURL)

	unregister := func() {
		for _, fn := range unregisters {
			if fn != nil {
				fn()
			}
		}
	}

	return &wailsHostAuthSessionWindow{window: window, unregister: unregister}
}

func (w *wailsHostAuthSessionWindow) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	window := w.window
	unregister := w.unregister
	w.unregister = nil
	w.window = nil
	w.mu.Unlock()

	if unregister != nil {
		unregister()
	}
	appHostAuthWindowCloseOnly(window)

	return nil
}

func appHostAuthWindowCloseOnly(window appHostAuthWebviewWindow) {
	if closeable, ok := window.(interface{ Close() }); ok {
		closeable.Close()
	}
}

func (w *wailsHostAuthSessionWindowWrapper) OnWindowEvent(eventType events.WindowEventType, callback func(event *application.WindowEvent)) func() {
	return w.window.OnWindowEvent(eventType, callback)
}

func (w *wailsHostAuthSessionWindowWrapper) RegisterHook(eventType events.WindowEventType, callback func(event *application.WindowEvent)) func() {
	return w.window.RegisterHook(eventType, callback)
}

func (w *wailsHostAuthSessionWindowWrapper) SetURL(s string) application.Window {
	return w.window.SetURL(s)
}

func (w *wailsHostAuthSessionWindowWrapper) ExecJS(js string) {
	w.window.ExecJS(js)
}

func (w *wailsHostAuthSessionWindowWrapper) Close() {
	w.window.Close()
}

func appHostAuthNavigationCompleteEvents() []events.WindowEventType {
	switch runtime.GOOS {
	case "windows":
		return []events.WindowEventType{events.Windows.WebViewNavigationCompleted}
	case "darwin":
		return []events.WindowEventType{events.Mac.WebViewDidFinishNavigation}
	case "linux":
		return []events.WindowEventType{events.Linux.WindowLoadFinished}
	default:
		return []events.WindowEventType{events.Common.WindowRuntimeReady}
	}
}

func renderAppHostAuthCollectorJS(request appHostAuthSessionRequest) string {
	contentType := appHostAuthCallbackContentType(request.CallbackTransport)
	contextPayload := map[string]string{
		"callback_url":     request.CallbackURL,
		"session_token":    request.SessionToken,
		"expected_kind":    string(request.Kind),
		"content_type":     contentType,
		"session_header":   appHostAuthSessionHeader,
		"auth_page_origin": request.AuthPageOrigin,
	}
	contextJSON := appHostAuthJSON(contextPayload)
	collectorJSON := appHostAuthJSON(request.CollectorJS)
	return `(function(){"use strict";var context=` + contextJSON + `;if(window.location.origin!==context.auth_page_origin){return;}var marker="__goariaHostAuthCollectorExecuted";if(window[marker]){return;}window[marker]=true;var collectorSource=` + collectorJSON + `;var postCapture=function(payload){return fetch(context.callback_url,{method:"POST",headers:{"Content-Type":context.content_type,[context.session_header]:context.session_token},body:JSON.stringify(payload)});};var collector=(0,eval)(collectorSource);if(typeof collector==="function"){collector(context,postCapture);}})();`
}

func appHostAuthJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "null"
	}

	return string(raw)
}

func appHostAuthCallbackContentType(transport extractor.WebViewAuthCallbackTransport) string {
	if len(transport.ContentTypes) > 0 && transport.ContentTypes[0] != "" {
		return transport.ContentTypes[0]
	}

	return "application/json"
}

func appHostAuthSessionWindowName(request appHostAuthSessionRequest) string {
	profileID := strings.ReplaceAll(string(request.ProfileID), "-", "_")
	if profileID == "" {
		profileID = "default"
	}

	return fmt.Sprintf("host_auth_%d_%s", time.Now().UnixNano(), profileID)
}

func cloneAppHostAuthDomainRules(rules []extractor.DomainRule) []extractor.DomainRule {
	if rules == nil {
		return nil
	}
	cloned := make([]extractor.DomainRule, len(rules))
	copy(cloned, rules)

	return cloned
}

func cloneAppHostAuthCallbackTransport(transport extractor.WebViewAuthCallbackTransport) extractor.WebViewAuthCallbackTransport {
	transport.ContentTypes = append([]string(nil), transport.ContentTypes...)

	return transport
}

func cloneAppHostAuthCapture(capture extractor.WebViewAuthCaptureContract) extractor.WebViewAuthCaptureContract {
	capture.SecretCandidates = append([]string(nil), capture.SecretCandidates...)

	return capture
}

func cloneAppHostAuthWebViewRequest(request extractor.WebViewAuthRequest) extractor.WebViewAuthRequest {
	request.AllowedDomains = cloneAppHostAuthDomainRules(request.AllowedDomains)
	request.CallbackTransport = cloneAppHostAuthCallbackTransport(request.CallbackTransport)
	request.Capture = cloneAppHostAuthCapture(request.Capture)

	return request
}

func cloneAppHostAuthTime(input *time.Time) *time.Time {
	if input == nil {
		return nil
	}
	cloned := *input

	return &cloned
}

func compactAppHostAuthStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	compact := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		compact = append(compact, value)
	}

	return compact
}
