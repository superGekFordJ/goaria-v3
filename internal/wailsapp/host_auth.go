//go:build extractor

package wailsapp

import (
	"bytes"
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
	"runtime"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/extractor"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

var _ extractor.AuthWebViewDriver = (*appHostAuthDriver)(nil)

func HostAuthCallbackMiddleware(appService *App) func(http.Handler) http.Handler {
	return appService.hostAuthCallbackMiddleware
}

func HostAuthRawMessageHandler(window application.Window, message string, origin *application.OriginInfo) {
	appHostAuthRawMessageHandler(window, message, origin)
}

type appHostAuthDriver struct {
	app     *App
	factory hostAuthSessionWindowFactory

	mu       sync.Mutex
	inflight *appHostAuthSession
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
	if factory == nil {
		factory = &wailsHostAuthSessionWindowFactory{app: app}
	}

	return &appHostAuthDriver{app: app, factory: factory}
}

func (d *appHostAuthDriver) OpenAuthSession(ctx context.Context, request extractor.WebViewAuthRequest, sink extractor.AuthWebViewSink) (extractor.AuthWebViewSession, error) {
	appHostAuthRecordDiagnostic("driver", "open_attempted")
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.app == nil || d.factory == nil {
		appHostAuthRecordDiagnostic("driver", "unavailable")
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	if err := d.app.hostAuthSessionAvailable(); err != nil {
		appHostAuthRecordDiagnostic("driver", "unavailable")
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		appHostAuthRecordDiagnostic("driver", "unavailable")
		return nil, errors.New(appHostAuthUnavailableMessage)
	}

	callbackPath, callbackToken, err := d.app.registerHostAuthCallbackSession(request)
	if err != nil {
		appHostAuthRecordDiagnostic("driver", "unavailable")
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	cleanup := sync.OnceFunc(func() {
		d.app.unregisterHostAuthCallback(callbackPath)
	})

	session := &appHostAuthSession{driver: d, sink: sink, kind: request.Kind, cleanup: cleanup, request: request}
	if !d.app.hostAuthCallbackRegistry().bind(callbackPath, session) {
		cleanup()
		appHostAuthRecordDiagnostic("driver", "unavailable")
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	if err := d.setInflight(session); err != nil {
		cleanup()
		appHostAuthRecordDiagnostic("driver", "inflight_rejected")
		return nil, err
	}

	window, err := d.factory.OpenHostAuthSession(ctx, appHostAuthSessionRequestFromWebView(request, callbackPath, appHostAuthCallbackURL(callbackPath), callbackToken), appHostAuthSessionCallbacks{
		Success: func(payload appHostAuthSessionPayload) { _ = session.succeed(payload) },
		Cancel:  func() { _ = session.cancel() },
		Error:   func(err error) { _ = session.fail(err) },
	})
	if err != nil {
		session.release()
		appHostAuthRecordDiagnostic("driver", "unavailable")
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	if window == nil {
		session.release()
		appHostAuthRecordDiagnostic("driver", "unavailable")
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	session.setWindow(window)
	appHostAuthRecordDiagnostic("driver", "opened")

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
			appHostAuthRecordDiagnostic("session", "error")
			appHostAuthRecordDiagnostic("parser", appHostAuthParserRejectCategory(err))
			if s.sink.OnError != nil {
				s.sink.OnError(err)
			}
			won = true
			return
		}

		s.release()
		appHostAuthRecordDiagnostic("parser", "accepted")
		appHostAuthRecordDiagnostic("session", "success")
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
		appHostAuthRecordDiagnostic("session", "cancel")
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
		appHostAuthRecordDiagnostic("session", "error")
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
			appHostAuthRecordDiagnostic("callback_route", "late_or_expired")
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

	return a.hostAuthCallbacks.(*appHostAuthCallbackRegistry)
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
		appHostAuthRecordDiagnostic("callback_route", "late_or_expired")
		http.Error(w, "expired", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodOptions {
		h.handlePreflight(w, r)
		return
	}
	appHostAuthRecordDiagnostic("callback_route", "hit")
	if r.Method != http.MethodPost {
		appHostAuthRecordDiagnostic("callback_route", "method_rejected")
		http.Error(w, "invalid", http.StatusMethodNotAllowed)
		return
	}
	if !h.validateOrigin(r.Header.Get("Origin")) {
		appHostAuthRecordDiagnostic("callback_route", "origin_rejected")
		http.Error(w, "invalid", http.StatusForbidden)
		return
	}
	writeAppHostAuthCORSHeaders(w, r.Header.Get("Origin"))
	if !appHostAuthContentTypeAllowed(r.Header.Get("Content-Type"), h.request.CallbackTransport.ContentTypes) {
		h.fail(w, "content_type_rejected")
		return
	}
	if !appHostAuthTokenMatches(r.Header.Get(appHostAuthSessionHeader), h.token) {
		h.fail(w, "session_rejected")
		return
	}
	raw, err := appHostAuthReadBoundedBody(r.Body, h.request.CallbackTransport.MaxBodyBytes)
	if err != nil {
		h.fail(w, "body_rejected")
		return
	}
	if h.handleDiagnosticEnvelope(w, raw) {
		return
	}
	token, err := extractor.ParseWebViewAuthCallbackPayload(h.request, raw)
	if err != nil {
		appHostAuthRecordDiagnostic("parser", appHostAuthParserRejectCategory(err))
		h.fail(w, "body_rejected")
		return
	}
	appHostAuthRecordDiagnostic("parser", "accepted")
	if !h.session.succeed(appHostAuthSessionPayload{Kind: token.Kind, Secret: token.Secret, ExpiresAt: token.ExpiresAt, RedactedDisplay: token.RedactedDisplay}) {
		appHostAuthRecordDiagnostic("callback_route", "late_or_expired")
		http.Error(w, "expired", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, "accepted")
}

func (h *appHostAuthCallbackHandler) fail(w http.ResponseWriter, routeCategory string) {
	appHostAuthRecordDiagnostic("callback_route", routeCategory)
	if !h.session.fail(errors.New(appHostAuthInvalidPayloadMessage)) {
		appHostAuthRecordDiagnostic("callback_route", "late_or_expired")
		http.Error(w, "expired", http.StatusConflict)
		return
	}
	http.Error(w, "invalid", http.StatusBadRequest)
}

func (h *appHostAuthCallbackHandler) handleDiagnosticEnvelope(w http.ResponseWriter, raw []byte) bool {
	if !appHostAuthDiagnosticsEnabled() {
		return false
	}
	var envelope struct {
		Diagnostic bool   `json:"diagnostic"`
		Stage      string `json:"stage"`
		Category   string `json:"category"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return false
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return false
	}
	if !envelope.Diagnostic {
		return false
	}
	if !appHostAuthDiagnosticAllowed(envelope.Stage, envelope.Category) {
		appHostAuthRecordDiagnostic("callback_route", "diagnostic_event_rejected")
		http.Error(w, "diagnostic_event_rejected", http.StatusUnprocessableEntity)

		return true
	}
	appHostAuthRecordDiagnostic(envelope.Stage, envelope.Category)
	appHostAuthRecordDiagnostic("callback_route", "diagnostic_event_accepted")
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, "accepted")

	return true
}

func appHostAuthParserRejectCategory(err error) string {
	if err == nil {
		return "rejected_payload"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "secret"):
		return "rejected_secret_candidate"
	case strings.Contains(message, "kind"):
		return "rejected_kind"
	case strings.Contains(message, "expiry"):
		return "rejected_expiry"
	default:
		return "rejected_payload"
	}
}

func (h *appHostAuthCallbackHandler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !h.validateOrigin(origin) {
		appHostAuthRecordDiagnostic("callback_route", "origin_rejected")
		http.Error(w, "invalid", http.StatusForbidden)
		return
	}
	if !strings.EqualFold(r.Header.Get("Access-Control-Request-Method"), http.MethodPost) {
		appHostAuthRecordDiagnostic("callback_route", "method_rejected")
		http.Error(w, "invalid", http.StatusMethodNotAllowed)
		return
	}
	if !appHostAuthCORSHeadersAllowed(r.Header.Values("Access-Control-Request-Headers")) {
		appHostAuthRecordDiagnostic("callback_route", "content_type_rejected")
		http.Error(w, "invalid", http.StatusForbidden)
		return
	}
	writeAppHostAuthCORSHeaders(w, origin)
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
	window := creator.NewWithOptions(appHostAuthSessionWindowOptions(request))
	if window == nil {
		return nil, errors.New(appHostAuthUnavailableMessage)
	}

	return setupWailsHostAuthSessionWindow(&wailsHostAuthSessionWindowWrapper{window: window}, request, callbacks), nil
}

func appHostAuthSessionWindowOptions(request appHostAuthSessionRequest) application.WebviewWindowOptions {
	// Wails v3 alpha Windows only passes WebviewWindowOptions.JS to
	// chromium.Init when HTML is non-empty. The real auth page navigation still
	// happens below via SetURL(request.LoginURL); this minimal blank document
	// exists only so the origin-gated collector bootstrap is initialized before
	// any third-party navigation without marking the page Wails-runtime-ready.
	return application.WebviewWindowOptions{
		Name:             appHostAuthSessionWindowName(request),
		Title:            "GoAria Auth Session",
		URL:              appHostAuthInitialURL,
		HTML:             appHostAuthInitialHTML,
		JS:               renderAppHostAuthCollectorJS(request),
		Width:            720,
		Height:           760,
		MinWidth:         480,
		MinHeight:        560,
		AlwaysOnTop:      true,
		Hidden:           false,
		BackgroundType:   application.BackgroundTypeSolid,
		BackgroundColour: application.NewRGBA(12, 12, 15, 255),
	}
}

func setupWailsHostAuthSessionWindow(window appHostAuthWebviewWindow, request appHostAuthSessionRequest, callbacks appHostAuthSessionCallbacks) hostAuthSessionWindow {
	unregisters := make([]func(), 0, 1+len(appHostAuthNavigationCompleteEvents()))
	unregisters = append(unregisters, window.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		if callbacks.Cancel != nil {
			callbacks.Cancel()
		}
	}))
	inject := func(_ *application.WindowEvent) {
		appHostAuthRecordDiagnostic("injection", "navigation_completed")
		window.ExecJS(renderAppHostAuthCollectorJS(request))
		appHostAuthRecordDiagnostic("injection", "execjs_dispatched")
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
	authPageOriginJSON := appHostAuthJSON(request.AuthPageOrigin)
	diagnosticsEnabled := appHostAuthDiagnosticsEnabled()
	diagnosticsEnabledJSON := appHostAuthJSON(diagnosticsEnabled)
	callbackPayload := map[string]any{
		"callback_url":         request.CallbackURL,
		"session_token":        request.SessionToken,
		"expected_kind":        string(request.Kind),
		"content_type":         contentType,
		"session_header":       appHostAuthSessionHeader,
		"auth_page_origin":     request.AuthPageOrigin,
		"diagnostic_enabled":   diagnosticsEnabled,
		"source_probe_enabled": appHostAuthSourceProbeEnabled(),
	}
	callbackJSON := appHostAuthJSON(callbackPayload)
	collectorJSON := appHostAuthJSON(request.CollectorJS)
	// __goariaPostDiag emits diagnostic events through the WebView2 native
	// chrome.webview.postMessage channel, which is delivered to the host via
	// the application-wide RawMessageHandler regardless of CORS / Mixed
	// Content / CSP restrictions on the loaded page. It runs both BEFORE the
	// origin gate (so we can confirm the script executed at all on a hostile
	// or redirected origin) and AFTER the origin gate (so we can confirm the
	// origin matched even when the subsequent fetch-based diagnostics are
	// blocked). Only the static categories "script_running" and
	// "origin_check_passed" are emitted before context is constructed; no
	// callback URL or session token is ever exposed pre-gate.
	return `(function(){"use strict";var __goariaInjDiag=` + diagnosticsEnabledJSON + `;var __goariaPostDiag=function(category){if(!__goariaInjDiag){return;}try{var w=(typeof window!=="undefined"?window:null);if(w&&w.chrome&&w.chrome.webview&&typeof w.chrome.webview.postMessage==="function"){w.chrome.webview.postMessage("goaria-auth-diag:injection:"+category);}}catch(__e){}};__goariaPostDiag("script_running");var authPageOrigin=` + authPageOriginJSON + `;if(window.location.origin!==authPageOrigin){return;}__goariaPostDiag("origin_check_passed");var context=` + callbackJSON + `;var diagnosticsEnabled=context.diagnostic_enabled===true;var sourceProbeEnabled=context.source_probe_enabled===true;void sourceProbeEnabled;var diagnostic=function(stage,category){if(!diagnosticsEnabled){return Promise.resolve();}return fetch(context.callback_url,{method:"POST",headers:{"Content-Type":context.content_type,[context.session_header]:context.session_token},body:JSON.stringify({diagnostic:true,stage:stage,category:category})}).then(function(response){return response&&response.ok;}).catch(function(){return false;});};if(diagnosticsEnabled){context.diagnostic=diagnostic;}diagnostic("injection","attempted");var marker="__goariaHostAuthCollectorExecuted";if(window[marker]){diagnostic("injection","marker_skip");return;}window[marker]=true;var collectorSource=` + collectorJSON + `;var postCapture=function(payload){diagnostic("post_capture","called");return fetch(context.callback_url,{method:"POST",headers:{"Content-Type":context.content_type,[context.session_header]:context.session_token},body:JSON.stringify(payload)}).catch(function(error){diagnostic("post_capture","rejected");throw error;});};var collector;try{diagnostic("injection","collector_eval_attempted");collector=(0,eval)(collectorSource);diagnostic("injection","collector_eval_succeeded");}catch(_){diagnostic("injection","collector_eval_failed");return;}if(typeof collector==="function"){diagnostic("injection","collector_invoked");collector(context,postCapture);}else{diagnostic("injection","collector_function_missing");}})();`
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
