package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/extractor"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

type fakeHostAuthWebviewWindow struct {
	mu              sync.Mutex
	events          []string
	hooks           map[events.WindowEventType][]func(*application.WindowEvent)
	listeners       map[events.WindowEventType][]func(*application.WindowEvent)
	urls            []string
	execScripts     []string
	currentOrigin   string
	collectorMarker bool
	collectorRuns   int
	closed          int
}

func TestAppHostAuthCallbackMiddlewareSuccessStoresProfile(t *testing.T) {
	store := newRootTempAuthProfileStore(t)
	factory := &fakeHostAuthSessionWindowFactory{}
	app := newWindowedAuthApp(t)
	coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(app, factory))
	resultCh := make(chan appHostAuthOutcome, 1)

	go func() {
		result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
		resultCh <- appHostAuthOutcome{result: result, err: err}
	}()
	factory.waitForOpen(t)
	request := factory.request(0)
	preflightStatus := preflightHostAuthCallback(t, app, request, request.AuthPageOrigin, http.MethodPost, "content-type, x-goaria-auth-session")
	if preflightStatus != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want no content", preflightStatus)
	}
	status := postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"middleware-captured-secret","redacted_display":"synthetic captured auth"}`, request.SessionToken, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("callback status = %d, want accepted", status)
	}

	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err != nil {
		t.Fatalf("coordinator.Start() error = %v", outcome.err)
	}
	if outcome.result.Status != extractor.WebViewAuthStatusSuccess {
		t.Fatalf("result status = %q, want success", outcome.result.Status)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), "xpk-alpha001", "apr-alpha001", "https://fixture.invalid/item")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderValue != "Bearer middleware-captured-secret" {
		t.Fatalf("HeaderValue = %q, want middleware secret", resolved.HeaderValue)
	}
	duplicateStatus := postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"late-secret"}`, request.SessionToken, "application/json")
	if duplicateStatus != http.StatusGone {
		t.Fatalf("duplicate status = %d, want expired/gone", duplicateStatus)
	}
	resolved, err = store.ResolveAuthProfile(context.Background(), "xpk-alpha001", "apr-alpha001", "https://fixture.invalid/item")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() after duplicate error = %v", err)
	}
	if resolved.HeaderValue != "Bearer middleware-captured-secret" {
		t.Fatalf("duplicate overwrote HeaderValue = %q", resolved.HeaderValue)
	}
}

func TestAppHostAuthDiagnosticCallbackCategories(t *testing.T) {
	diagnostics := &recordingAppHostAuthDiagnosticObserver{}
	store := newRootTempAuthProfileStore(t)
	factory := &fakeHostAuthSessionWindowFactory{}
	app := newWindowedAuthApp(t)
	coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactoryAndDiagnostics(app, factory, diagnostics))
	resultCh := make(chan appHostAuthOutcome, 1)

	go func() {
		result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
		resultCh <- appHostAuthOutcome{result: result, err: err}
	}()
	factory.waitForOpen(t)
	request := factory.request(0)

	if status := preflightHostAuthCallback(t, app, request, request.AuthPageOrigin, http.MethodPost, "content-type, x-goaria-auth-session"); status != http.StatusNoContent {
		t.Fatalf("accepted preflight status = %d, want no content", status)
	}
	if status := preflightHostAuthCallback(t, app, request, "https://example.test", http.MethodPost, "content-type, x-goaria-auth-session"); status != http.StatusForbidden {
		t.Fatalf("rejected preflight status = %d, want forbidden", status)
	}
	if status := callHostAuthCallback(t, app, request, http.MethodGet, `{"kind":"bearer","secret":"not-used"}`, request.SessionToken, "application/json", request.AuthPageOrigin); status != http.StatusMethodNotAllowed {
		t.Fatalf("rejected POST status = %d, want method not allowed", status)
	}
	if status := postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"middleware-captured-secret","redacted_display":"synthetic captured auth"}`, request.SessionToken, "application/json"); status != http.StatusAccepted {
		t.Fatalf("accepted POST status = %d, want accepted", status)
	}
	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err != nil || outcome.result.Status != extractor.WebViewAuthStatusSuccess {
		t.Fatalf("coordinator.Start() outcome = %#v err=%v", outcome.result, outcome.err)
	}

	categories := diagnostics.categories()
	for _, want := range []string{
		appHostAuthDiagnosticPreflightAccepted,
		appHostAuthDiagnosticPreflightOriginRejected,
		appHostAuthDiagnosticPostMethodRejected,
		appHostAuthDiagnosticPostAccepted,
		appHostAuthDiagnosticTerminalSuccess,
	} {
		if !stringSliceContains(categories, want) {
			t.Fatalf("categories missing %q: %#v", want, categories)
		}
	}
	assertAppHostAuthDiagnosticEventsCategoryOnly(t, diagnostics.eventsSnapshot())
	encoded := diagnostics.encodedEvents(t)
	assertRootNoSecretText(t, encoded, "middleware-captured-secret", "not-used", "example.test", "fixture.invalid", request.SessionToken, request.CallbackPath, "xpk-alpha001", "apr-alpha001", "Authorization", "Cookie")
}

func TestAppHostAuthDiagnosticCallbackRejectedCategoryOnly(t *testing.T) {
	cases := []struct {
		name           string
		trigger        func(t *testing.T, app *App, request appHostAuthSessionRequest) int
		wantCategory   string
		wantStatus     int
		completesAuth  bool
		forbiddenTexts []string
	}{
		{
			name: "preflight method rejected",
			trigger: func(t *testing.T, app *App, request appHostAuthSessionRequest) int {
				return preflightHostAuthCallback(t, app, request, request.AuthPageOrigin, http.MethodGet, "content-type, x-goaria-auth-session")
			},
			wantCategory:  appHostAuthDiagnosticPreflightMethodRejected,
			wantStatus:    http.StatusMethodNotAllowed,
			completesAuth: false,
		},
		{
			name: "preflight header rejected",
			trigger: func(t *testing.T, app *App, request appHostAuthSessionRequest) int {
				return preflightHostAuthCallback(t, app, request, request.AuthPageOrigin, http.MethodPost, "content-type, x-goaria-auth-session, authorization")
			},
			wantCategory:  appHostAuthDiagnosticPreflightHeaderRejected,
			wantStatus:    http.StatusForbidden,
			completesAuth: false,
			forbiddenTexts: []string{
				"authorization",
			},
		},
		{
			name: "post origin rejected",
			trigger: func(t *testing.T, app *App, request appHostAuthSessionRequest) int {
				return callHostAuthCallback(t, app, request, http.MethodPost, `{"kind":"bearer","secret":"body-secret"}`, request.SessionToken, "application/json", "https://example.test")
			},
			wantCategory:  appHostAuthDiagnosticPostOriginRejected,
			wantStatus:    http.StatusForbidden,
			completesAuth: false,
			forbiddenTexts: []string{
				"body-secret", "example.test",
			},
		},
		{
			name: "post content type rejected",
			trigger: func(t *testing.T, app *App, request appHostAuthSessionRequest) int {
				return postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"body-secret"}`, request.SessionToken, "text/plain")
			},
			wantCategory:  appHostAuthDiagnosticPostContentTypeRejected,
			wantStatus:    http.StatusBadRequest,
			completesAuth: true,
			forbiddenTexts: []string{
				"body-secret",
			},
		},
		{
			name: "post session rejected",
			trigger: func(t *testing.T, app *App, request appHostAuthSessionRequest) int {
				return postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"body-secret"}`, "synthetic-session-token", "application/json")
			},
			wantCategory:  appHostAuthDiagnosticPostSessionHeaderRejected,
			wantStatus:    http.StatusBadRequest,
			completesAuth: true,
			forbiddenTexts: []string{
				"body-secret", "synthetic-session-token",
			},
		},
		{
			name: "post body rejected",
			trigger: func(t *testing.T, app *App, request appHostAuthSessionRequest) int {
				return postHostAuthCallback(t, app, request, `{"secret":"`+strings.Repeat("x", 17000)+`"}`, request.SessionToken, "application/json")
			},
			wantCategory:  appHostAuthDiagnosticPostBodyRejected,
			wantStatus:    http.StatusBadRequest,
			completesAuth: true,
			forbiddenTexts: []string{
				strings.Repeat("x", 32),
			},
		},
		{
			name: "post payload rejected",
			trigger: func(t *testing.T, app *App, request appHostAuthSessionRequest) int {
				return postHostAuthCallback(t, app, request, `{"kind":"cookie","secret":"body-secret"}`, request.SessionToken, "application/json")
			},
			wantCategory:  appHostAuthDiagnosticPostPayloadRejected,
			wantStatus:    http.StatusBadRequest,
			completesAuth: true,
			forbiddenTexts: []string{
				"body-secret",
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			diagnostics := &recordingAppHostAuthDiagnosticObserver{}
			store := newRootTempAuthProfileStore(t)
			factory := &fakeHostAuthSessionWindowFactory{}
			app := newWindowedAuthApp(t)
			coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactoryAndDiagnostics(app, factory, diagnostics))
			resultCh := make(chan appHostAuthOutcome, 1)
			go func() {
				result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
				resultCh <- appHostAuthOutcome{result: result, err: err}
			}()
			factory.waitForOpen(t)
			request := factory.request(0)

			if status := tt.trigger(t, app, request); status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if tt.completesAuth {
				outcome := receiveAppHostAuthOutcome(t, resultCh)
				if outcome.err == nil {
					t.Fatalf("coordinator.Start() error = nil, result=%#v", outcome.result)
				}
			} else {
				factory.callback(0).Cancel()
				_ = receiveAppHostAuthOutcome(t, resultCh)
			}

			categories := diagnostics.categories()
			if !stringSliceContains(categories, tt.wantCategory) {
				t.Fatalf("categories missing %q: %#v", tt.wantCategory, categories)
			}
			assertAppHostAuthDiagnosticEventsCategoryOnly(t, diagnostics.eventsSnapshot())
			encoded := diagnostics.encodedEvents(t)
			forbidden := append([]string{request.SessionToken, request.CallbackPath, "fixture.invalid", "xpk-alpha001", "apr-alpha001", "Authorization", "Cookie"}, tt.forbiddenTexts...)
			assertRootNoSecretText(t, encoded, forbidden...)
		})
	}
}

func TestAppHostAuthCallbackMiddlewareRejectsInvalidRequests(t *testing.T) {
	for _, tt := range []struct {
		name        string
		method      string
		body        string
		token       string
		contentType string
		wantStatus  int
	}{
		{name: "wrong method", method: http.MethodGet, body: `{"secret":"x"}`, token: "", contentType: "application/json", wantStatus: http.StatusMethodNotAllowed},
		{name: "wrong origin", method: http.MethodPost, body: `{"secret":"x"}`, token: "request", contentType: "application/json", wantStatus: http.StatusForbidden},
		{name: "missing content type", method: http.MethodPost, body: `{"secret":"x"}`, token: "request", contentType: "", wantStatus: http.StatusBadRequest},
		{name: "wrong content type", method: http.MethodPost, body: `{"secret":"x"}`, token: "request", contentType: "text/plain", wantStatus: http.StatusBadRequest},
		{name: "wrong token", method: http.MethodPost, body: `{"secret":"x"}`, token: "wrong", contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "oversized", method: http.MethodPost, body: `{"secret":"` + strings.Repeat("x", 17000) + `"}`, token: "request", contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "parser invalid", method: http.MethodPost, body: `{"kind":"cookie","secret":"x"}`, token: "request", contentType: "application/json", wantStatus: http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newRootTempAuthProfileStore(t)
			factory := &fakeHostAuthSessionWindowFactory{}
			app := newWindowedAuthApp(t)
			coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(app, factory))
			resultCh := make(chan appHostAuthOutcome, 1)
			go func() {
				result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
				resultCh <- appHostAuthOutcome{result: result, err: err}
			}()
			factory.waitForOpen(t)
			request := factory.request(0)
			token := tt.token
			if token == "request" {
				token = request.SessionToken
			}
			origin := request.AuthPageOrigin
			if tt.name == "wrong origin" {
				origin = "https://example.test"
			}
			status := callHostAuthCallback(t, app, request, tt.method, tt.body, token, tt.contentType, origin)
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if tt.method != http.MethodGet && tt.name != "wrong origin" {
				outcome := receiveAppHostAuthOutcome(t, resultCh)
				if outcome.err == nil {
					t.Fatalf("coordinator.Start() error = nil, result=%#v", outcome.result)
				}
				assertNoRootAuthProfile(t, store)
				lateStatus := postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"late-secret"}`, request.SessionToken, "application/json")
				if lateStatus != http.StatusGone {
					t.Fatalf("late status = %d, want expired", lateStatus)
				}
			} else if tt.method == http.MethodGet || tt.name == "wrong origin" {
				factory.callback(0).Cancel()
				_ = receiveAppHostAuthOutcome(t, resultCh)
			}
		})
	}
}

func TestAppHostAuthCallbackCleanupAfterCancelAndTimeout(t *testing.T) {
	t.Run("cancel before post", func(t *testing.T) {
		store := newRootTempAuthProfileStore(t)
		factory := &fakeHostAuthSessionWindowFactory{}
		app := newWindowedAuthApp(t)
		coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(app, factory))
		resultCh := make(chan appHostAuthOutcome, 1)
		go func() {
			result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
			resultCh <- appHostAuthOutcome{result: result, err: err}
		}()
		factory.waitForOpen(t)
		request := factory.request(0)
		factory.callback(0).Cancel()
		outcome := receiveAppHostAuthOutcome(t, resultCh)
		if outcome.result.Status != extractor.WebViewAuthStatusCanceled {
			t.Fatalf("status = %q, want canceled", outcome.result.Status)
		}
		status := postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"late-secret"}`, request.SessionToken, "application/json")
		if status != http.StatusGone {
			t.Fatalf("late status = %d, want expired", status)
		}
		assertNoRootAuthProfile(t, store)
	})

	t.Run("timeout cleanup", func(t *testing.T) {
		store := newRootTempAuthProfileStore(t)
		factory := &fakeHostAuthSessionWindowFactory{}
		app := newWindowedAuthApp(t)
		coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(app, factory))
		request := appHostAuthWebViewRequest(5 * time.Millisecond)
		result := make(chan appHostAuthOutcome, 1)
		go func() {
			out, err := coordinator.Start(context.Background(), request)
			result <- appHostAuthOutcome{result: out, err: err}
		}()
		factory.waitForOpen(t)
		opened := factory.request(0)
		outcome := receiveAppHostAuthOutcome(t, result)
		if outcome.result.Status != extractor.WebViewAuthStatusTimeout {
			t.Fatalf("status = %q, want timeout", outcome.result.Status)
		}
		status := postHostAuthCallback(t, app, opened, `{"kind":"bearer","secret":"late-secret"}`, opened.SessionToken, "application/json")
		if status != http.StatusGone {
			t.Fatalf("late timeout status = %d, want expired", status)
		}
		assertNoRootAuthProfile(t, store)
	})
}

func TestAppHostAuthCallbackCORSPreflight(t *testing.T) {
	for _, tt := range []struct {
		name          string
		origin        string
		method        string
		requestHeader string
		wantStatus    int
	}{
		{name: "valid", origin: "request", method: http.MethodPost, requestHeader: "content-type, x-goaria-auth-session", wantStatus: http.StatusNoContent},
		{name: "wrong origin", origin: "https://example.test", method: http.MethodPost, requestHeader: "content-type, x-goaria-auth-session", wantStatus: http.StatusForbidden},
		{name: "wrong method", origin: "request", method: http.MethodGet, requestHeader: "content-type, x-goaria-auth-session", wantStatus: http.StatusMethodNotAllowed},
		{name: "wrong header", origin: "request", method: http.MethodPost, requestHeader: "content-type, x-goaria-auth-session, x-extra", wantStatus: http.StatusForbidden},
		{name: "missing header", origin: "request", method: http.MethodPost, requestHeader: "", wantStatus: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newRootTempAuthProfileStore(t)
			factory := &fakeHostAuthSessionWindowFactory{}
			app := newWindowedAuthApp(t)
			coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(app, factory))
			resultCh := make(chan appHostAuthOutcome, 1)
			go func() {
				result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(time.Second))
				resultCh <- appHostAuthOutcome{result: result, err: err}
			}()
			factory.waitForOpen(t)
			request := factory.request(0)
			origin := tt.origin
			if origin == "request" {
				origin = request.AuthPageOrigin
			}
			status := preflightHostAuthCallback(t, app, request, origin, tt.method, tt.requestHeader)
			if status != tt.wantStatus {
				t.Fatalf("preflight status = %d, want %d", status, tt.wantStatus)
			}
			select {
			case outcome := <-resultCh:
				t.Fatalf("preflight unexpectedly completed auth session: %#v", outcome)
			case <-time.After(20 * time.Millisecond):
			}
			factory.callback(0).Cancel()
			_ = receiveAppHostAuthOutcome(t, resultCh)
			assertNoRootAuthProfile(t, store)
		})
	}
}

func TestAppHostAuthSessionWindowOptionsBootstrap(t *testing.T) {
	request := appHostAuthWebViewRequest(time.Second)
	sessionRequest := appHostAuthSessionRequestFromWebView(request, "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	options := appHostAuthSessionWindowOptions(sessionRequest)

	if options.HTML == "" {
		t.Fatal("auth window HTML bootstrap is empty")
	}
	if !strings.Contains(strings.ToLower(options.HTML), "<!doctype html>") {
		t.Fatalf("auth window HTML bootstrap = %q, want minimal blank document", options.HTML)
	}
	if options.URL != appHostAuthInitialURL {
		t.Fatalf("auth window initial URL = %q, want %q", options.URL, appHostAuthInitialURL)
	}
	if options.JS == "" {
		t.Fatal("auth window bootstrap JS is empty")
	}
	assertAppHostAuthCollectorScriptJSSideGated(t, options.JS, sessionRequest.AuthPageOrigin)
	for _, want := range []string{"callback_url", "session_token", appHostAuthRawMessagePrefix + appHostAuthRawMessageStage + ":", appHostAuthRawMessageScriptRun, appHostAuthRawMessageOriginPass} {
		if !strings.Contains(options.JS, want) {
			t.Fatalf("auth window bootstrap JS missing %q", want)
		}
	}
	for _, forbidden := range []string{sessionRequest.CallbackURL, sessionRequest.CallbackPath, sessionRequest.SessionToken, sessionRequest.PackID, string(sessionRequest.ProfileID)} {
		if strings.Contains(options.HTML, forbidden) {
			t.Fatalf("auth window HTML bootstrap leaked %q: %q", forbidden, options.HTML)
		}
	}
	if options.Title != "GoAria Auth Session" || options.Name == "" || options.Width != 720 || options.Height != 760 || options.MinWidth != 480 || options.MinHeight != 560 || !options.AlwaysOnTop || options.Hidden {
		t.Fatalf("auth window options changed unexpectedly: %#v", options)
	}
}

func TestAppHostAuthCollectorInjectionHookOrder(t *testing.T) {
	window := newFakeHostAuthWebviewWindow()
	request := appHostAuthWebViewRequest(time.Second)
	sessionRequest := appHostAuthSessionRequestFromWebView(request, "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	options := appHostAuthSessionWindowOptions(sessionRequest)
	if options.HTML == "" || options.JS == "" {
		t.Fatalf("auth window bootstrap options missing initial HTML/JS: %#v", options)
	}
	session := setupWailsHostAuthSessionWindow(window, sessionRequest, appHostAuthSessionCallbacks{})
	defer func() { _ = session.Close() }()

	eventsLog := window.eventLog()
	wantPrefix := []string{"listen", "hook", "seturl:https://fixture.invalid/login"}
	if len(eventsLog) < len(wantPrefix) || !reflect.DeepEqual(eventsLog[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("hook order = %#v, want prefix %#v", eventsLog, wantPrefix)
	}
	window.navigateOrigin("about:blank")
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	neutralScripts := window.scripts()
	if len(neutralScripts) != 1 {
		t.Fatalf("neutral load script count = %d, want 1", len(neutralScripts))
	}
	assertAppHostAuthCollectorScriptJSSideGated(t, neutralScripts[0], sessionRequest.AuthPageOrigin)
	if runs := window.collectorRunCount(); runs != 0 {
		t.Fatalf("neutral load collector runs = %d, want 0", runs)
	}

	window.navigateOrigin("https://example.test")
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	offScopeScripts := window.scripts()
	if len(offScopeScripts) != 2 {
		t.Fatalf("off-scope load script count = %d, want 2", len(offScopeScripts))
	}
	assertAppHostAuthCollectorScriptJSSideGated(t, offScopeScripts[1], sessionRequest.AuthPageOrigin)
	if runs := window.collectorRunCount(); runs != 0 {
		t.Fatalf("off-scope load collector runs = %d, want 0", runs)
	}

	window.navigateOrigin(sessionRequest.AuthPageOrigin)
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	authScripts := window.scripts()
	if len(authScripts) != 3 {
		t.Fatalf("auth load script count = %d, want 3", len(authScripts))
	}
	assertAppHostAuthCollectorScriptJSSideGated(t, authScripts[2], sessionRequest.AuthPageOrigin)
	if !strings.Contains(authScripts[2], "synthetic-session-token") || !strings.Contains(authScripts[2], "callback_url") || !strings.Contains(authScripts[2], "auth_page_origin") {
		t.Fatalf("collector script not injected with context: %#v", authScripts[2])
	}
	if runs := window.collectorRunCount(); runs != 1 {
		t.Fatalf("auth load collector runs = %d, want 1", runs)
	}

	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	repeatedScripts := window.scripts()
	if len(repeatedScripts) != 4 {
		t.Fatalf("repeated hook script count = %d, want 4", len(repeatedScripts))
	}
	assertAppHostAuthCollectorScriptJSSideGated(t, repeatedScripts[3], sessionRequest.AuthPageOrigin)
	if runs := window.collectorRunCount(); runs != 1 {
		t.Fatalf("repeated auth hook collector runs = %d, want 1", runs)
	}
}

func TestAppHostAuthRawMessageHandlerAllowList(t *testing.T) {
	originalSink := appHostAuthRawMessageSink
	defer func() {
		appHostAuthRawMessageSink = originalSink
	}()

	var categories []string
	appHostAuthRawMessageSink = func(category string) {
		categories = append(categories, category)
	}

	acceptedMessages := []string{
		appHostAuthRawMessagePrefix + appHostAuthRawMessageStage + ":" + appHostAuthRawMessageScriptRun,
		appHostAuthRawMessagePrefix + appHostAuthRawMessageStage + ":" + appHostAuthRawMessageOriginPass,
	}
	rejectedMessages := []string{
		"ignored-untrusted-message",
		appHostAuthRawMessagePrefix + "collector_probe:post_capture_suppressed",
		appHostAuthRawMessagePrefix + "parser:accepted",
		appHostAuthRawMessagePrefix + "store:set_succeeded",
		appHostAuthRawMessagePrefix + appHostAuthRawMessageStage + ":unknown",
		appHostAuthRawMessagePrefix + appHostAuthRawMessageStage,
	}

	for _, message := range append(acceptedMessages, rejectedMessages...) {
		appHostAuthRawMessageHandler(nil, message, &application.OriginInfo{Origin: "https://example.test"})
	}

	if !reflect.DeepEqual(categories, []string{appHostAuthRawMessageScriptRun, appHostAuthRawMessageOriginPass}) {
		t.Fatalf("raw message categories = %#v, want only narrow bootstrap allow-list", categories)
	}
}

func assertAppHostAuthCollectorScriptJSSideGated(t *testing.T, script string, origin string) {
	t.Helper()
	originGuard := "window.location.origin!==context.auth_page_origin"
	idempotentGuard := "if(window[marker]){return;}"
	markerWrite := "window[marker]=true"
	collectorEval := "var collector=(0,eval)(collectorSource)"
	for _, want := range []string{originGuard, idempotentGuard, markerWrite, collectorEval, origin} {
		if !strings.Contains(script, want) {
			t.Fatalf("collector script missing %q: %s", want, script)
		}
	}
	if strings.Contains(script, "injection_mode") {
		t.Fatalf("collector script still uses Go-side injection mode gate: %s", script)
	}
	if strings.Index(script, originGuard) > strings.Index(script, markerWrite) {
		t.Fatalf("origin guard must run before marker write: %s", script)
	}
	if strings.Index(script, idempotentGuard) > strings.Index(script, collectorEval) {
		t.Fatalf("idempotent guard must run before collector execution: %s", script)
	}
}

func postHostAuthCallback(t *testing.T, app *App, request appHostAuthSessionRequest, body string, token string, contentType string) int {
	t.Helper()
	return callHostAuthCallback(t, app, request, http.MethodPost, body, token, contentType, request.AuthPageOrigin)
}

func callHostAuthCallback(t *testing.T, app *App, request appHostAuthSessionRequest, method string, body string, token string, contentType string, origin string) int {
	t.Helper()
	req := httptest.NewRequest(method, request.CallbackPath, strings.NewReader(body))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if token != "" {
		req.Header.Set(appHostAuthSessionHeader, token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	app.hostAuthCallbackMiddleware(http.NotFoundHandler()).ServeHTTP(recorder, req)
	return recorder.Code
}

func preflightHostAuthCallback(t *testing.T, app *App, request appHostAuthSessionRequest, origin string, method string, requestHeaders string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, request.CallbackPath, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method != "" {
		req.Header.Set("Access-Control-Request-Method", method)
	}
	if requestHeaders != "" {
		req.Header.Set("Access-Control-Request-Headers", requestHeaders)
	}
	recorder := httptest.NewRecorder()
	app.hostAuthCallbackMiddleware(http.NotFoundHandler()).ServeHTTP(recorder, req)
	return recorder.Code
}

func newFakeHostAuthWebviewWindow() *fakeHostAuthWebviewWindow {
	return &fakeHostAuthWebviewWindow{
		hooks:     make(map[events.WindowEventType][]func(*application.WindowEvent)),
		listeners: make(map[events.WindowEventType][]func(*application.WindowEvent)),
	}
}

func (w *fakeHostAuthWebviewWindow) OnWindowEvent(eventType events.WindowEventType, callback func(event *application.WindowEvent)) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, "listen")
	w.listeners[eventType] = append(w.listeners[eventType], callback)
	return func() {}
}

func (w *fakeHostAuthWebviewWindow) RegisterHook(eventType events.WindowEventType, callback func(event *application.WindowEvent)) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, "hook")
	w.hooks[eventType] = append(w.hooks[eventType], callback)
	return func() {}
}

func (w *fakeHostAuthWebviewWindow) SetURL(url string) application.Window {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, "seturl:"+url)
	w.urls = append(w.urls, url)
	return nil
}

func (w *fakeHostAuthWebviewWindow) ExecJS(script string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, "execjs")
	w.execScripts = append(w.execScripts, script)
	if w.currentOrigin == appHostAuthCollectorScriptOrigin(script) && strings.Contains(script, "if(window[marker]){return;}") && !w.collectorMarker {
		w.collectorMarker = true
		w.collectorRuns++
	}
}

func (w *fakeHostAuthWebviewWindow) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed++
}

func (w *fakeHostAuthWebviewWindow) fireHook(eventType events.WindowEventType) {
	w.mu.Lock()
	hooks := append([]func(*application.WindowEvent){}, w.hooks[eventType]...)
	w.mu.Unlock()
	for _, hook := range hooks {
		hook(application.NewWindowEvent())
	}
}

func (w *fakeHostAuthWebviewWindow) eventLog() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.events...)
}

func (w *fakeHostAuthWebviewWindow) scripts() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.execScripts...)
}

func (w *fakeHostAuthWebviewWindow) navigateOrigin(origin string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.currentOrigin = origin
	w.collectorMarker = false
}

func (w *fakeHostAuthWebviewWindow) collectorRunCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.collectorRuns
}

func appHostAuthCollectorScriptOrigin(script string) string {
	const prefix = `"auth_page_origin":"`
	start := strings.Index(script, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(script[start:], `"`)
	if end < 0 {
		return ""
	}

	return script[start : start+end]
}

func Example_renderAppHostAuthCollectorJS_publicSafe() {
	request := appHostAuthSessionRequestFromWebView(appHostAuthWebViewRequest(time.Second), "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	js := renderAppHostAuthCollectorJS(request)
	fmt.Println(strings.Contains(js, "callback_url"), strings.Contains(js, "synthetic-session-token"))
	// Output: true true
}
