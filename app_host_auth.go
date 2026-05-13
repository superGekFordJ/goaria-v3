package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/extractor"

	"github.com/wailsapp/wails/v3/pkg/application"
	wailsevents "github.com/wailsapp/wails/v3/pkg/events"
)

const (
	appHostAuthUnavailableMessage    = "auth webview session unavailable"
	appHostAuthInProgressMessage     = "auth webview session already in progress"
	appHostAuthInvalidPayloadMessage = "auth webview callback payload invalid"
	appHostAuthCallbackErrorMessage  = "auth webview session failed"
)

var _ extractor.AuthWebViewDriver = (*appHostAuthDriver)(nil)

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
	PackID         string
	ProfileID      extractor.AuthProfileID
	Kind           extractor.AuthSecretKind
	LoginURL       string
	AllowedDomains []extractor.DomainRule
	Timeout        time.Duration
}

type appHostAuthSessionPayload struct {
	Kind            extractor.AuthSecretKind
	Secret          string
	ExpiresAt       *time.Time
	RedactedDisplay string
}

type appHostAuthSession struct {
	driver *appHostAuthDriver
	sink   extractor.AuthWebViewSink
	kind   extractor.AuthSecretKind

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
	window     *application.WebviewWindow
	unregister func()
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
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.app == nil || d.factory == nil {
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	if err := d.app.hostAuthSessionAvailable(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.New(appHostAuthUnavailableMessage)
	}

	session := &appHostAuthSession{driver: d, sink: sink, kind: request.Kind}
	if err := d.setInflight(session); err != nil {
		return nil, err
	}

	window, err := d.factory.OpenHostAuthSession(ctx, appHostAuthSessionRequestFromWebView(request), appHostAuthSessionCallbacks{
		Success: session.succeed,
		Cancel:  session.cancel,
		Error:   session.fail,
	})
	if err != nil {
		session.release()
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	if window == nil {
		session.release()
		return nil, errors.New(appHostAuthUnavailableMessage)
	}
	session.setWindow(window)

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

func (s *appHostAuthSession) succeed(payload appHostAuthSessionPayload) {
	s.terminalOnce.Do(func() {
		token, err := appHostAuthTokenFromPayload(s.kind, payload)
		if err != nil {
			s.release()
			if s.sink.OnError != nil {
				s.sink.OnError(err)
			}
			return
		}

		s.release()
		if s.sink.OnSuccess != nil {
			s.sink.OnSuccess(token)
		}
	})
}

func (s *appHostAuthSession) cancel() {
	s.terminalOnce.Do(func() {
		s.release()
		if s.sink.OnCancel != nil {
			s.sink.OnCancel()
		}
	})
}

func (s *appHostAuthSession) fail(err error) {
	s.terminalOnce.Do(func() {
		s.release()
		if s.sink.OnError != nil {
			s.sink.OnError(appHostAuthSanitizedCallbackError(err))
		}
	})
}

func (s *appHostAuthSession) release() {
	if s == nil {
		return
	}
	s.releaseOnce.Do(func() {
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

func appHostAuthSessionRequestFromWebView(request extractor.WebViewAuthRequest) appHostAuthSessionRequest {
	return appHostAuthSessionRequest{
		PackID:         request.PackID,
		ProfileID:      request.ProfileID,
		Kind:           request.Kind,
		LoginURL:       request.LoginURL,
		AllowedDomains: cloneAppHostAuthDomainRules(request.AllowedDomains),
		Timeout:        request.Timeout,
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

func (f *wailsHostAuthSessionWindowFactory) OpenHostAuthSession(_ context.Context, request appHostAuthSessionRequest, callbacks appHostAuthSessionCallbacks) (hostAuthSessionWindow, error) {
	app, mainWindow, err := f.app.hostAuthSessionWindowContext()
	if err != nil {
		return nil, err
	}
	if app.Window == nil || mainWindow == nil {
		return nil, errors.New(appHostAuthUnavailableMessage)
	}

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             appHostAuthSessionWindowName(request),
		Title:            "GoAria Auth Session",
		URL:              request.LoginURL,
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
	window.SetURL(request.LoginURL)

	unregister := window.OnWindowEvent(wailsevents.Common.WindowClosing, func(_ *application.WindowEvent) {
		if callbacks.Cancel != nil {
			callbacks.Cancel()
		}
	})

	return &wailsHostAuthSessionWindow{window: window, unregister: unregister}, nil
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
	if window != nil {
		window.Close()
	}

	return nil
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
