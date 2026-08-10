//go:build extractor

package wailsapp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	pendingScripts  []string
}

type recordingAuthProfileStore struct {
	extractor.AuthProfileStore

	mu       sync.Mutex
	setCalls int
	last     extractor.AuthProfileSnapshot
	lastErr  error
}

type hostAuthCallbackResponse struct {
	status int
	body   string
}

type appHostAuthRuntimeOutcome struct {
	result extractor.HostAuthRuntimeResult
	err    error
}

func TestAppHostAuthCallbackDiagnosticStoreWriteMatrix(t *testing.T) {
	t.Run("success writes exactly once and late duplicate does not write", func(t *testing.T) {
		store := newRecordingRootAuthProfileStore(t)
		app, _, request, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)

		preflightStatus := preflightHostAuthCallback(t, app, request, request.AuthPageOrigin, http.MethodPost, "content-type, x-goaria-auth-session")
		if preflightStatus != http.StatusNoContent {
			t.Fatalf("preflight status = %d, want no content", preflightStatus)
		}
		response := postHostAuthCallbackResponse(t, app, request, `{"kind":"bearer","secret":"diagnostic-captured-secret","redacted_display":"synthetic captured auth"}`, request.SessionToken, "application/json")
		if response.status != http.StatusAccepted {
			t.Fatalf("callback status = %d, want accepted", response.status)
		}
		outcome := receiveAppHostAuthOutcome(t, resultCh)
		if outcome.err != nil {
			t.Fatalf("coordinator.Start() error = %v", outcome.err)
		}
		if outcome.result.Status != extractor.WebViewAuthStatusSuccess {
			t.Fatalf("result status = %q, want success", outcome.result.Status)
		}
		if calls := store.SetCalls(); calls != 1 {
			t.Fatalf("store set calls = %d, want 1", calls)
		}
		if snapshot := store.LastSnapshot(); !snapshot.HasSecret || snapshot.ProfileID != request.ProfileID {
			t.Fatalf("store snapshot was not written for expected profile")
		}
		if _, err := store.ResolveAuthProfile(context.Background(), request.PackID, request.ProfileID, "https://fixture.invalid/item"); err != nil {
			t.Fatalf("ResolveAuthProfile() error = %v", err)
		}
		duplicate := postHostAuthCallbackResponse(t, app, request, `{"kind":"bearer","secret":"diagnostic-late-secret"}`, request.SessionToken, "application/json")
		if duplicate.status != http.StatusGone {
			t.Fatalf("duplicate callback status = %d, want gone", duplicate.status)
		}
		if calls := store.SetCalls(); calls != 1 {
			t.Fatalf("store set calls after duplicate = %d, want 1", calls)
		}
	})

	t.Run("no callback cancel leaves store empty", func(t *testing.T) {
		store := newRecordingRootAuthProfileStore(t)
		_, factory, _, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)

		factory.callback(0).Cancel()
		outcome := receiveAppHostAuthOutcome(t, resultCh)
		if outcome.err != nil || outcome.result.Status != extractor.WebViewAuthStatusCanceled {
			t.Fatalf("no callback cancel outcome = %#v err=%v", outcome.result, outcome.err)
		}
		if calls := store.SetCalls(); calls != 0 {
			t.Fatalf("store set calls = %d, want 0", calls)
		}
		assertNoRootAuthProfile(t, store)
	})

	t.Run("no callback timeout leaves store empty", func(t *testing.T) {
		store := newRecordingRootAuthProfileStore(t)
		_, _, _, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, 15*time.Millisecond)

		outcome := receiveAppHostAuthOutcome(t, resultCh)
		if outcome.err != nil || outcome.result.Status != extractor.WebViewAuthStatusTimeout {
			t.Fatalf("no callback timeout outcome = %#v err=%v", outcome.result, outcome.err)
		}
		if calls := store.SetCalls(); calls != 0 {
			t.Fatalf("store set calls = %d, want 0", calls)
		}
		assertNoRootAuthProfile(t, store)
	})

	t.Run("transport rejects write no store profile", func(t *testing.T) {
		for _, tt := range []struct {
			name        string
			method      string
			body        string
			token       string
			contentType string
			origin      string
			wantStatus  int
			terminal    bool
		}{
			{name: "wrong origin", method: http.MethodPost, body: `{"kind":"bearer","secret":"diagnostic-transport-secret"}`, token: "request", contentType: "application/json", origin: "https://other.alpha.test", wantStatus: http.StatusForbidden},
			{name: "wrong method", method: http.MethodGet, body: `{"kind":"bearer","secret":"diagnostic-transport-secret"}`, token: "request", contentType: "application/json", origin: "request", wantStatus: http.StatusMethodNotAllowed},
			{name: "missing content type", method: http.MethodPost, body: `{"kind":"bearer","secret":"diagnostic-transport-secret"}`, token: "request", origin: "request", wantStatus: http.StatusBadRequest, terminal: true},
			{name: "wrong content type", method: http.MethodPost, body: `{"kind":"bearer","secret":"diagnostic-transport-secret"}`, token: "request", contentType: "text/plain", origin: "request", wantStatus: http.StatusBadRequest, terminal: true},
			{name: "missing session header", method: http.MethodPost, body: `{"kind":"bearer","secret":"diagnostic-transport-secret"}`, contentType: "application/json", origin: "request", wantStatus: http.StatusBadRequest, terminal: true},
			{name: "wrong session header", method: http.MethodPost, body: `{"kind":"bearer","secret":"diagnostic-transport-secret"}`, token: "wrong", contentType: "application/json", origin: "request", wantStatus: http.StatusBadRequest, terminal: true},
			{name: "oversized body", method: http.MethodPost, body: `{"secret":"` + strings.Repeat("x", 17000) + `"}`, token: "request", contentType: "application/json", origin: "request", wantStatus: http.StatusBadRequest, terminal: true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				store := newRecordingRootAuthProfileStore(t)
				app, factory, request, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)
				token := tt.token
				if token == "request" {
					token = request.SessionToken
				}
				origin := tt.origin
				if origin == "request" {
					origin = request.AuthPageOrigin
				}

				response := callHostAuthCallbackResponse(t, app, request, tt.method, tt.body, token, tt.contentType, origin)
				if response.status != tt.wantStatus {
					t.Fatalf("transport status = %d, want %d", response.status, tt.wantStatus)
				}
				if calls := store.SetCalls(); calls != 0 {
					t.Fatalf("store set calls after reject = %d, want 0", calls)
				}

				if tt.terminal {
					outcome := receiveAppHostAuthOutcome(t, resultCh)
					if outcome.err == nil {
						t.Fatalf("terminal transport reject returned nil error")
					}
					late := postHostAuthCallbackResponse(t, app, request, `{"kind":"bearer","secret":"diagnostic-late-secret"}`, request.SessionToken, "application/json")
					if late.status != http.StatusGone {
						t.Fatalf("late callback status = %d, want gone", late.status)
					}
				} else {
					select {
					case outcome := <-resultCh:
						t.Fatalf("non-terminal transport reject completed session: %#v", outcome)
					case <-time.After(20 * time.Millisecond):
					}
					factory.callback(0).Cancel()
					_ = receiveAppHostAuthOutcome(t, resultCh)
				}
				assertNoRootAuthProfile(t, store)
			})
		}
	})

	t.Run("parser rejects through route write no store profile", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			body      string
			forbidden string
		}{
			{name: "capture secret candidate mismatch", body: `{"kind":"bearer","capture":{}}`},
			{name: "capture kind field mismatch", body: `{"kind":"cookie","secret":"diagnostic-parser-secret"}`, forbidden: "diagnostic-parser-secret"},
			{name: "capture secret crlf", body: `{"kind":"bearer","secret":"bad\nsecret"}`, forbidden: "bad"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				store := newRecordingRootAuthProfileStore(t)
				app, _, request, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)

				response := postHostAuthCallbackResponse(t, app, request, tt.body, request.SessionToken, "application/json")
				if response.status != http.StatusBadRequest {
					t.Fatalf("parser reject status = %d, want bad request", response.status)
				}
				if tt.forbidden != "" {
					assertRootNoSecretText(t, response.body, tt.forbidden)
				}
				outcome := receiveAppHostAuthOutcome(t, resultCh)
				if outcome.err == nil {
					t.Fatalf("parser reject returned nil error")
				}
				if calls := store.SetCalls(); calls != 0 {
					t.Fatalf("store set calls after parser reject = %d, want 0", calls)
				}
				late := postHostAuthCallbackResponse(t, app, request, `{"kind":"bearer","secret":"diagnostic-late-secret"}`, request.SessionToken, "application/json")
				if late.status != http.StatusGone {
					t.Fatalf("late callback status = %d, want gone", late.status)
				}
				assertNoRootAuthProfile(t, store)
			})
		}
	})
}

func TestAppHostAuthDiagnosticRouteIsNonTerminalAndNoStore(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	t.Setenv(appHostAuthDiagnosticLogEnv, filepath.Join(t.TempDir(), "diagnostic.jsonl"))
	store := newRecordingRootAuthProfileStore(t)
	app, factory, request, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)

	response := postHostAuthCallbackResponse(t, app, request, `{"diagnostic":true,"stage":"collector_source","category":"bounded_not_found"}`, request.SessionToken, "application/json")
	if response.status != http.StatusAccepted {
		t.Fatalf("diagnostic callback status = %d, want accepted", response.status)
	}
	if calls := store.SetCalls(); calls != 0 {
		t.Fatalf("store set calls after diagnostic envelope = %d, want 0", calls)
	}
	select {
	case outcome := <-resultCh:
		t.Fatalf("diagnostic envelope completed auth session: %#v", outcome)
	case <-time.After(20 * time.Millisecond):
	}

	status := postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"diagnostic-route-secret"}`, request.SessionToken, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("valid callback after diagnostic status = %d, want accepted", status)
	}
	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err != nil || outcome.result.Status != extractor.WebViewAuthStatusSuccess {
		t.Fatalf("valid callback after diagnostic outcome = %#v err=%v", outcome.result, outcome.err)
	}
	if calls := store.SetCalls(); calls != 1 {
		t.Fatalf("store set calls after valid callback = %d, want 1", calls)
	}
	if factory.openCount() != 1 {
		t.Fatalf("factory open count = %d, want 1", factory.openCount())
	}
}

func TestAppHostAuthSourceProbeDiagnosticCategoriesAreWhitelisted(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	t.Setenv(appHostAuthSourceProbeEnv, "1")
	logPath := filepath.Join(t.TempDir(), "diagnostic.jsonl")
	t.Setenv(appHostAuthDiagnosticLogEnv, logPath)
	store := newRecordingRootAuthProfileStore(t)
	app, factory, request, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)

	accepted := []string{
		"network_hooks_installed",
		"network_hooks_absent",
		"network_fetch_seen",
		"network_xhr_seen",
		"storage_checked",
		"storage_present",
		"cookie_absent",
		"request_header_source_checked",
		"request_header_source_matched",
		"request_header_source_unmatched",
		"request_header_value_absent",
		"request_header_present",
		"response_source_checked",
		"response_source_matched",
		"response_source_unmatched",
		"response_json_invalid",
		"candidate_shape_parser_compatible",
		"post_capture_suppressed",
		"completed",
	}
	for _, category := range accepted {
		response := postHostAuthCallbackResponse(t, app, request, `{"diagnostic":true,"stage":"collector_probe","category":"`+category+`"}`, request.SessionToken, "application/json")
		if response.status != http.StatusAccepted {
			t.Fatalf("probe diagnostic callback status = %d, want accepted", response.status)
		}
	}
	response := postHostAuthCallbackResponse(t, app, request, `{"diagnostic":true,"stage":"collector_probe","category":"raw-token-marker"}`, request.SessionToken, "application/json")
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown probe diagnostic callback status = %d, want unprocessable", response.status)
	}
	if calls := store.SetCalls(); calls != 0 {
		t.Fatalf("store set calls after probe diagnostic envelopes = %d, want 0", calls)
	}
	select {
	case outcome := <-resultCh:
		t.Fatalf("probe diagnostic envelope completed auth session: %#v", outcome)
	case <-time.After(20 * time.Millisecond):
	}

	text := string(mustReadAppHostAuthTestFile(t, logPath))
	for _, category := range accepted {
		if !strings.Contains(text, `"stage":"collector_probe","category":"`+category+`"`) {
			t.Fatalf("probe diagnostic log missing whitelisted category")
		}
	}
	for _, forbidden := range []string{"raw-token-marker", request.SessionToken, request.CallbackPath, "diagnostic-route-secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("probe diagnostic log leaks forbidden value")
		}
	}
	if !strings.Contains(text, `"stage":"callback_route","category":"diagnostic_event_rejected"`) {
		t.Fatalf("probe diagnostic log missing rejected category")
	}

	factory.callback(0).Cancel()
	_ = receiveAppHostAuthOutcome(t, resultCh)
}

func TestAppHostAuthDiagnosticEnvelopeRejectsUnknownCategoryNonTerminalNoStore(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	logPath := filepath.Join(t.TempDir(), "diagnostic.jsonl")
	t.Setenv(appHostAuthDiagnosticLogEnv, logPath)
	store := newRecordingRootAuthProfileStore(t)
	app, _, request, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)

	response := postHostAuthCallbackResponse(t, app, request, `{"diagnostic":true,"stage":"collector_probe","category":"raw-token-marker"}`, request.SessionToken, "application/json")
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("unknown diagnostic status = %d, want unprocessable", response.status)
	}
	for _, forbidden := range []string{"raw-token-marker", request.SessionToken, request.CallbackPath} {
		assertRootNoSecretText(t, response.body, forbidden)
	}
	if calls := store.SetCalls(); calls != 0 {
		t.Fatalf("store set calls after rejected diagnostic envelope = %d, want 0", calls)
	}
	select {
	case outcome := <-resultCh:
		t.Fatalf("rejected diagnostic envelope completed auth session: %#v", outcome)
	case <-time.After(20 * time.Millisecond):
	}

	status := postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"diagnostic-route-secret"}`, request.SessionToken, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("valid callback after rejected diagnostic status = %d, want accepted", status)
	}
	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err != nil || outcome.result.Status != extractor.WebViewAuthStatusSuccess {
		t.Fatalf("valid callback after rejected diagnostic outcome = %#v err=%v", outcome.result, outcome.err)
	}
	if calls := store.SetCalls(); calls != 1 {
		t.Fatalf("store set calls after valid callback = %d, want 1", calls)
	}

	text := string(mustReadAppHostAuthTestFile(t, logPath))
	if !strings.Contains(text, `"stage":"callback_route","category":"diagnostic_event_rejected"`) {
		t.Fatalf("diagnostic log missing rejected category")
	}
	for _, forbidden := range []string{"raw-token-marker", request.SessionToken, request.CallbackPath, "diagnostic-route-secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("rejected diagnostic log leaks forbidden value")
		}
	}
}

func TestAppHostAuthDiagnosticRejectPathsCategoryOnly(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	logPath := filepath.Join(t.TempDir(), "diagnostic.jsonl")
	t.Setenv(appHostAuthDiagnosticLogEnv, logPath)
	store := newRecordingRootAuthProfileStore(t)
	app, _, request, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)

	response := postHostAuthCallbackResponse(t, app, request, `{"kind":"cookie","secret":"raw-diagnostic-secret"}`, request.SessionToken, "application/json")
	if response.status != http.StatusBadRequest {
		t.Fatalf("parser reject status = %d, want bad request", response.status)
	}
	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err == nil {
		t.Fatalf("parser reject outcome error = nil")
	}
	if calls := store.SetCalls(); calls != 0 {
		t.Fatalf("store set calls after parser reject = %d, want 0", calls)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read diagnostic log: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"stage":"callback_route"`, `"category":"hit"`, `"stage":"parser"`, `"category":"rejected_kind"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("diagnostic log missing category marker")
		}
	}
	for _, forbidden := range []string{"raw-diagnostic-secret", "callback_url", "session_token", "fixture.invalid", request.SessionToken, request.CallbackPath} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("diagnostic log leaks forbidden value")
		}
	}
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
			} else {
				factory.callback(0).Cancel()
				_ = receiveAppHostAuthOutcome(t, resultCh)
			}
		})
	}
}

func TestAppHostAuthCallbackCleanupAfterCancelAndTimeout(t *testing.T) {
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

func TestAppHostAuthCollectorInjectionHookOrder(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "0")
	window := newFakeHostAuthWebviewWindow()
	request := appHostAuthWebViewRequest(time.Second)
	sessionRequest := appHostAuthSessionRequestFromWebView(request, "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	session := setupWailsHostAuthSessionWindow(window, sessionRequest, appHostAuthSessionCallbacks{})
	defer func() { _ = session.Close() }()

	eventsLog := window.eventLog()
	wantPrefix := []string{"listen", "hook", "seturl:https://fixture.invalid/login"}
	if len(eventsLog) < len(wantPrefix) || !reflect.DeepEqual(eventsLog[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("hook order = %#v, want prefix %#v", eventsLog, wantPrefix)
	}
	window.navigateOrigin("about:blank")
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	if events := window.eventLog(); strings.Contains(strings.Join(events, "\n"), "wails:runtime:ready") {
		t.Fatalf("auth injection must not force Wails runtime-ready: %#v", events)
	}
	neutralScripts := window.scripts()
	if len(neutralScripts) != 0 {
		t.Fatalf("neutral load executed script count = %d, want queued fallback only", len(neutralScripts))
	}
	pending := window.pendingScriptsSnapshot()
	if len(pending) != 1 {
		t.Fatalf("neutral load pending fallback script count = %d, want 1", len(pending))
	}
	assertAppHostAuthCollectorScriptJSSideGated(t, pending[0], sessionRequest.AuthPageOrigin)
	if runs := window.collectorRunCount(); runs != 0 {
		t.Fatalf("neutral load collector runs = %d, want 0", runs)
	}

	window.navigateOrigin(sessionRequest.AuthPageOrigin)
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	authScripts := window.scripts()
	if len(authScripts) != 0 {
		t.Fatalf("auth load executed script count = %d, want queued fallback only", len(authScripts))
	}
	pending = window.pendingScriptsSnapshot()
	if len(pending) != 2 {
		t.Fatalf("auth load pending fallback script count = %d, want 2", len(pending))
	}
	assertAppHostAuthCollectorScriptJSSideGated(t, pending[1], sessionRequest.AuthPageOrigin)
	if !strings.Contains(pending[1], "synthetic-session-token") || !strings.Contains(pending[1], "callback_url") || !strings.Contains(pending[1], "auth_page_origin") || !strings.Contains(pending[1], appHostAuthSessionHeader) || !strings.Contains(pending[1], "session_header") {
		t.Fatalf("collector script did not expose required public-safe context keys")
	}
	if runs := window.collectorRunCount(); runs != 0 {
		t.Fatalf("auth load collector runs via ExecJS = %d, want queued fallback only", runs)
	}

	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	if runs := window.collectorRunCount(); runs != 0 {
		t.Fatalf("same-origin repeated navigation collector runs via ExecJS = %d, want queued fallback only", runs)
	}
	if len(window.pendingScriptsSnapshot()) != 3 {
		t.Fatalf("same-origin repeated navigation pending fallback injections = %d, want 3", len(window.pendingScriptsSnapshot()))
	}
}

func TestAppHostAuthCollectorInjectionDiagnosticsCategories(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	t.Setenv(appHostAuthDiagnosticLogEnv, filepath.Join(t.TempDir(), "diagnostic.jsonl"))
	window := newFakeHostAuthWebviewWindow()
	request := appHostAuthWebViewRequest(time.Second)
	sessionRequest := appHostAuthSessionRequestFromWebView(request, "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	session := setupWailsHostAuthSessionWindow(window, sessionRequest, appHostAuthSessionCallbacks{})
	defer func() { _ = session.Close() }()

	window.navigateOrigin("about:blank")
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	window.navigateOrigin(sessionRequest.AuthPageOrigin)
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	scripts := window.pendingScriptsSnapshot()
	if len(scripts) != 3 {
		t.Fatalf("diagnostic script count = %d, want 3", len(scripts))
	}
	originGate := strings.Index(scripts[0], "if(window.location.origin!==authPageOrigin){return;}")
	if originGate < 0 {
		t.Fatalf("wrong-origin diagnostic script missing early origin gate")
	}
	for _, marker := range []string{`diagnostic("injection","origin_mismatch")`, `fetch(context.callback_url`} {
		if strings.Contains(scripts[0][:originGate], marker) {
			t.Fatalf("wrong-origin diagnostic script can invoke callback transport before origin gate")
		}
	}
	for _, marker := range []string{"callback_url", "session_token", "synthetic-session-token", sessionRequest.CallbackURL} {
		idx := strings.Index(scripts[0], marker)
		if idx >= 0 && idx < originGate {
			t.Fatalf("wrong-origin diagnostic script exposes callback/session material before origin gate")
		}
	}
	for _, want := range []string{`diagnostic("injection","attempted")`, `diagnostic("injection","marker_skip")`, `diagnostic("injection","collector_eval_attempted")`, `diagnostic("injection","collector_eval_succeeded")`, `diagnostic("injection","collector_eval_failed")`, `diagnostic("injection","collector_function_missing")`, `diagnostic("injection","collector_invoked")`, `diagnostic("post_capture","called")`, `diagnostic("post_capture","rejected")`} {
		if !strings.Contains(scripts[1], want) {
			t.Fatalf("collector diagnostic script missing category marker")
		}
	}
	if strings.Contains(scripts[1], `"resolved"`) {
		t.Fatalf("collector diagnostic script should not claim post_capture resolved")
	}
	for _, forbidden := range []string{"raw-token", "Authorization", "Cookie"} {
		if strings.Contains(strings.Join(scripts, "\n"), forbidden) {
			t.Fatalf("collector diagnostic script leaked forbidden marker")
		}
	}
}

func TestAppHostAuthCollectorContextIncludesSourceProbeGate(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	t.Setenv(appHostAuthSourceProbeEnv, "1")
	t.Setenv(appHostAuthDiagnosticLogEnv, filepath.Join(t.TempDir(), "diagnostic.jsonl"))
	request := appHostAuthWebViewRequest(time.Second)
	sessionRequest := appHostAuthSessionRequestFromWebView(request, "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	probeScript := renderAppHostAuthCollectorJS(sessionRequest)
	for _, want := range []string{`"source_probe_enabled":true`, `context.source_probe_enabled===true`} {
		if !strings.Contains(probeScript, want) {
			t.Fatalf("source probe script missing env-gated context marker")
		}
	}

	t.Setenv(appHostAuthSourceProbeEnv, "0")
	disabledScript := renderAppHostAuthCollectorJS(sessionRequest)
	if !strings.Contains(disabledScript, `"source_probe_enabled":false`) {
		t.Fatalf("source probe script missing disabled context marker")
	}
	for _, forbidden := range []string{"raw-token", "Authorization: Bearer", "Cookie:"} {
		if strings.Contains(probeScript+disabledScript, forbidden) {
			t.Fatalf("source probe script leaked forbidden marker")
		}
	}
}

func TestAppHostAuthCollectorInjectionUsesOptionsJSWithoutRuntimeReady(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	logPath := filepath.Join(t.TempDir(), "diagnostic.jsonl")
	t.Setenv(appHostAuthDiagnosticLogEnv, logPath)
	request := appHostAuthWebViewRequest(time.Second)
	sessionRequest := appHostAuthSessionRequestFromWebView(request, "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	options := appHostAuthSessionWindowOptions(sessionRequest)
	if options.HTML == "" {
		t.Fatal("auth window options HTML must be non-empty so Wails Windows initializes options JS")
	}
	if !strings.Contains(options.HTML, "<!doctype html>") || strings.Contains(options.HTML, "synthetic-session-token") || strings.Contains(options.HTML, sessionRequest.CallbackURL) {
		t.Fatalf("auth window HTML bootstrap is not minimal/safe")
	}
	if options.URL != appHostAuthInitialURL {
		t.Fatalf("auth window initial URL = %q, want neutral initial URL", options.URL)
	}
	window := newFakeHostAuthWebviewWindow()
	session := setupWailsHostAuthSessionWindow(window, sessionRequest, appHostAuthSessionCallbacks{})
	defer func() { _ = session.Close() }()
	initialScript := options.JS
	if initialScript == "" {
		t.Fatal("auth window options JS is empty")
	}
	// Mirrors Wails Windows v3 alpha behavior: options.JS reaches
	// chromium.Init only when options.HTML is non-empty; login navigation still
	// comes from setupWailsHostAuthSessionWindow.SetURL below.
	assertAppHostAuthCollectorScriptJSSideGated(t, initialScript, sessionRequest.AuthPageOrigin)
	originGate := strings.Index(initialScript, "if(window.location.origin!==authPageOrigin){return;}")
	if originGate < 0 {
		t.Fatalf("options JS missing early origin gate")
	}
	for _, marker := range []string{"callback_url", "session_token", "synthetic-session-token", sessionRequest.CallbackURL} {
		idx := strings.Index(initialScript, marker)
		if idx >= 0 && idx < originGate {
			t.Fatalf("options JS exposes callback/session material before origin gate")
		}
	}

	window.navigateOrigin(sessionRequest.AuthPageOrigin)
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	if urls := window.urlsSnapshot(); len(urls) != 1 || urls[0] != sessionRequest.LoginURL {
		t.Fatalf("auth window login navigation URLs = %#v, want SetURL login navigation", urls)
	}

	if pending := window.pendingScriptCount(); pending != 1 {
		t.Fatalf("pending fallback scripts after navigation = %d, want 1", pending)
	}
	events := window.eventLog()
	if strings.Contains(strings.Join(events, "\n"), "wails:runtime:ready") {
		t.Fatalf("auth window injection sent forbidden runtime-ready event: %#v", events)
	}
	if len(events) < 2 || events[len(events)-1] != "execjs:queued" {
		t.Fatalf("navigation fallback ExecJS was not queued best-effort: %#v", events)
	}
	text := string(mustReadAppHostAuthTestFile(t, logPath))
	for _, want := range []string{`"stage":"injection","category":"navigation_completed"`, `"stage":"injection","category":"execjs_dispatched"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("diagnostic log missing injection fallback category")
		}
	}
	if strings.Contains(text, "runtime_ready_dispatched") {
		t.Fatalf("diagnostic log recorded forbidden runtime-ready category")
	}
}

func TestAppHostAuthCollectorInjectionDoesNotFlushPendingEventJS(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "0")
	window := newFakeHostAuthWebviewWindow()
	window.ExecJS("window.__nonCollectorEventPayload='must-remain-pending';")
	request := appHostAuthWebViewRequest(time.Second)
	sessionRequest := appHostAuthSessionRequestFromWebView(request, "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	session := setupWailsHostAuthSessionWindow(window, sessionRequest, appHostAuthSessionCallbacks{})
	defer func() { _ = session.Close() }()

	window.navigateOrigin(sessionRequest.AuthPageOrigin)
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])

	if scripts := window.scripts(); len(scripts) != 0 {
		t.Fatalf("pending event JS was flushed/executed by auth injection: %d", len(scripts))
	}
	pending := window.pendingScriptsSnapshot()
	if len(pending) != 2 || !strings.Contains(pending[0], "must-remain-pending") || !strings.Contains(pending[1], "collectorSource") {
		t.Fatalf("pending JS queue not preserved with fallback collector: %#v", pending)
	}
	if events := strings.Join(window.eventLog(), "\n"); strings.Contains(events, "wails:runtime:ready") {
		t.Fatalf("auth injection must not flush pending event JS via runtime-ready: %s", events)
	}
}

func TestAppHostAuthRawMessageHandlerAcceptsOnlyPreContextInjectionProof(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "1")
	logPath := filepath.Join(t.TempDir(), "diagnostic.jsonl")
	t.Setenv(appHostAuthDiagnosticLogEnv, logPath)

	appHostAuthRawMessageHandler(nil, "ignored-untrusted-message", nil)
	appHostAuthRawMessageHandler(nil, "goaria-auth-diag:injection:script_running", &application.OriginInfo{Origin: "https://synthetic.alpha.test"})
	appHostAuthRawMessageHandler(nil, "goaria-auth-diag:injection:origin_check_passed", &application.OriginInfo{Origin: "https://synthetic.alpha.test"})
	appHostAuthRawMessageHandler(nil, "goaria-auth-diag:collector_probe:post_capture_suppressed", &application.OriginInfo{Origin: "https://synthetic.alpha.test"})
	appHostAuthRawMessageHandler(nil, "goaria-auth-diag:parser:accepted", &application.OriginInfo{Origin: "https://synthetic.alpha.test"})
	appHostAuthRawMessageHandler(nil, "goaria-auth-diag:store:set_succeeded", &application.OriginInfo{Origin: "https://synthetic.alpha.test"})
	appHostAuthRawMessageHandler(nil, "goaria-auth-diag:session:success", &application.OriginInfo{Origin: "https://synthetic.alpha.test"})
	appHostAuthRawMessageHandler(nil, "goaria-auth-diag:callback_route:diagnostic_event_accepted", &application.OriginInfo{Origin: "https://synthetic.alpha.test"})

	text := string(mustReadAppHostAuthTestFile(t, logPath))
	if count := strings.Count(text, `"stage":"raw_message","category":"handler_invoked"`); count != 2 {
		t.Fatalf("raw message invocation count = %d, want only accepted pre-context categories", count)
	}
	for _, want := range []string{`"stage":"raw_message","category":"handler_invoked"`, `"stage":"injection","category":"script_running"`, `"stage":"injection","category":"origin_check_passed"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("raw message diagnostic missing category")
		}
	}
	for _, forbidden := range []string{"ignored-untrusted-message", "synthetic.alpha.test", `"stage":"collector_probe"`, `"stage":"parser"`, `"stage":"store"`, `"stage":"session"`, `"stage":"callback_route"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("raw message diagnostic leaked forbidden value")
		}
	}
}

func TestAppHostAuthCollectorDiagnosticsDisabledNoCallbackTraffic(t *testing.T) {
	t.Setenv(appHostAuthDiagnosticEnv, "0")
	store := newRecordingRootAuthProfileStore(t)
	app, _, request, resultCh := startAppHostAuthCallbackDiagnosticSession(t, store, time.Second)
	window := newFakeHostAuthWebviewWindow()
	session := setupWailsHostAuthSessionWindow(window, request, appHostAuthSessionCallbacks{})
	defer func() { _ = session.Close() }()

	window.navigateOrigin(request.AuthPageOrigin)
	window.fireHook(appHostAuthNavigationCompleteEvents()[0])
	scripts := window.pendingScriptsSnapshot()
	if len(scripts) != 1 {
		t.Fatalf("disabled diagnostic script count = %d, want 1", len(scripts))
	}
	for _, want := range []string{"diagnosticsEnabled=context.diagnostic_enabled===true", "if(diagnosticsEnabled){context.diagnostic=diagnostic;}"} {
		if !strings.Contains(scripts[0], want) {
			t.Fatalf("disabled diagnostic script missing immutable env gate marker")
		}
	}
	if strings.Contains(scripts[0], "context.diagnostic=diagnostic;diagnostic") {
		t.Fatalf("disabled diagnostic script installs helper before checking env gate")
	}
	select {
	case outcome := <-resultCh:
		t.Fatalf("disabled diagnostics unexpectedly completed session: %#v", outcome)
	case <-time.After(20 * time.Millisecond):
	}
	if calls := store.SetCalls(); calls != 0 {
		t.Fatalf("store set calls before valid auth callback = %d, want 0", calls)
	}

	status := postHostAuthCallback(t, app, request, `{"kind":"bearer","secret":"disabled-diagnostic-valid-secret"}`, request.SessionToken, "application/json")
	if status != http.StatusAccepted {
		t.Fatalf("valid callback status = %d, want accepted", status)
	}
	outcome := receiveAppHostAuthOutcome(t, resultCh)
	if outcome.err != nil || outcome.result.Status != extractor.WebViewAuthStatusSuccess {
		t.Fatalf("valid callback outcome = %#v err=%v", outcome.result, outcome.err)
	}
	if calls := store.SetCalls(); calls != 1 {
		t.Fatalf("store set calls after valid callback = %d, want 1", calls)
	}
}

func TestAppHostAuthRuntimeProvisioningThroughActualCallbackStoresMaterializableProfile(t *testing.T) {
	t.Run("valid callback stores materializable profile", func(t *testing.T) {
		store := newRecordingRootAuthProfileStore(t)
		app := newWindowedAuthApp(t)
		factory := &fakeHostAuthSessionWindowFactory{}
		driver := newAppHostAuthDriverWithFactory(app, factory)
		runtime := extractor.NewHostAuthRuntime(extractor.HostAuthRuntimeConfig{
			Bundle:      syntheticRootPrivateAuthRuntimeBundle(t),
			Store:       store,
			Coordinator: extractor.NewWebViewAuthCoordinator(store, driver),
		})
		request := appHostAuthRuntimeProvisionRequest()
		resultCh := make(chan appHostAuthRuntimeOutcome, 1)

		go func() {
			result, err := runtime.Provision(context.Background(), request)
			resultCh <- appHostAuthRuntimeOutcome{result: result, err: err}
		}()
		factory.waitForOpen(t)
		opened := factory.request(0)
		preflightStatus := preflightHostAuthCallback(t, app, opened, opened.AuthPageOrigin, http.MethodPost, "content-type, x-goaria-auth-session")
		if preflightStatus != http.StatusNoContent {
			t.Fatalf("preflight status = %d, want no content", preflightStatus)
		}
		status := postHostAuthCallback(t, app, opened, `{"kind":"bearer","secret":"runtime-callback-secret","redacted_display":"synthetic captured auth"}`, opened.SessionToken, "application/json")
		if status != http.StatusAccepted {
			t.Fatalf("callback status = %d, want accepted", status)
		}

		outcome := receiveAppHostAuthRuntimeOutcome(t, resultCh)
		if outcome.err != nil {
			t.Fatalf("runtime.Provision() error = %v", outcome.err)
		}
		if !outcome.result.Provisioned || !outcome.result.Available {
			t.Fatalf("runtime.Provision() = %#v, want provisioned available", outcome.result)
		}
		if factory.openCount() != 1 {
			t.Fatalf("driver open count = %d, want 1", factory.openCount())
		}
		if calls := store.SetCalls(); calls != 1 {
			t.Fatalf("store set calls = %d, want 1", calls)
		}
		if _, err := store.ResolveAuthProfile(context.Background(), request.PackIdentity.PackID, request.ProfileRef, request.TargetURL); err != nil {
			t.Fatalf("ResolveAuthProfile() error = %v", err)
		}
		material, err := runtime.MaterializeAuthProfile(context.Background(), request)
		if err != nil {
			t.Fatalf("MaterializeAuthProfile() error = %v", err)
		}
		if material.Kind != extractor.AuthSecretKindBearer || material.HeaderName != "Authorization" || material.HeaderValue() == "" {
			t.Fatalf("materialized auth public-safe shape invalid")
		}
		late := postHostAuthCallback(t, app, opened, `{"kind":"bearer","secret":"runtime-late-secret"}`, opened.SessionToken, "application/json")
		if late != http.StatusGone {
			t.Fatalf("late callback status = %d, want gone", late)
		}
		if calls := store.SetCalls(); calls != 1 {
			t.Fatalf("store set calls after duplicate = %d, want 1", calls)
		}
	})

	t.Run("invalid callback reaches route but parser rejects", func(t *testing.T) {
		store := newRecordingRootAuthProfileStore(t)
		app := newWindowedAuthApp(t)
		factory := &fakeHostAuthSessionWindowFactory{}
		driver := newAppHostAuthDriverWithFactory(app, factory)
		runtime := extractor.NewHostAuthRuntime(extractor.HostAuthRuntimeConfig{
			Bundle:      syntheticRootPrivateAuthRuntimeBundle(t),
			Store:       store,
			Coordinator: extractor.NewWebViewAuthCoordinator(store, driver),
		})
		request := appHostAuthRuntimeProvisionRequest()
		resultCh := make(chan appHostAuthRuntimeOutcome, 1)

		go func() {
			result, err := runtime.Provision(context.Background(), request)
			resultCh <- appHostAuthRuntimeOutcome{result: result, err: err}
		}()
		factory.waitForOpen(t)
		opened := factory.request(0)
		response := postHostAuthCallbackResponse(t, app, opened, `{"kind":"cookie","secret":"runtime-invalid-secret"}`, opened.SessionToken, "application/json")
		if response.status != http.StatusBadRequest {
			t.Fatalf("invalid callback status = %d, want bad request", response.status)
		}
		assertRootNoSecretText(t, response.body, "runtime-invalid-secret")

		outcome := receiveAppHostAuthRuntimeOutcome(t, resultCh)
		if outcome.err == nil || outcome.result.Available || outcome.result.Provisioned {
			t.Fatalf("runtime.Provision(invalid) = %#v err=%v, want unavailable error", outcome.result, outcome.err)
		}
		if calls := store.SetCalls(); calls != 0 {
			t.Fatalf("store set calls after invalid callback = %d, want 0", calls)
		}
		assertNoRootAuthProfile(t, store)
		if _, err := runtime.MaterializeAuthProfile(context.Background(), request); err == nil {
			t.Fatal("MaterializeAuthProfile() error = nil, want unavailable")
		}
	})
}

func assertAppHostAuthCollectorScriptJSSideGated(t *testing.T, script string, origin string) {
	t.Helper()
	for _, want := range []string{"window.location.origin!==authPageOrigin", "if(window[marker]){", "window[marker]=true", "collector=(0,eval)(collectorSource)", origin} {
		if !strings.Contains(script, want) {
			t.Fatalf("collector script missing required public-safe marker %q", want)
		}
	}
}

func postHostAuthCallback(t *testing.T, app *App, request appHostAuthSessionRequest, body string, token string, contentType string) int {
	t.Helper()
	return callHostAuthCallback(t, app, request, http.MethodPost, body, token, contentType, request.AuthPageOrigin)
}

func callHostAuthCallback(t *testing.T, app *App, request appHostAuthSessionRequest, method string, body string, token string, contentType string, origin string) int {
	t.Helper()
	return callHostAuthCallbackResponse(t, app, request, method, body, token, contentType, origin).status
}

func postHostAuthCallbackResponse(t *testing.T, app *App, request appHostAuthSessionRequest, body string, token string, contentType string) hostAuthCallbackResponse {
	t.Helper()
	return callHostAuthCallbackResponse(t, app, request, http.MethodPost, body, token, contentType, request.AuthPageOrigin)
}

func callHostAuthCallbackResponse(t *testing.T, app *App, request appHostAuthSessionRequest, method string, body string, token string, contentType string, origin string) hostAuthCallbackResponse {
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
	return hostAuthCallbackResponse{status: recorder.Code, body: recorder.Body.String()}
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

func newRecordingRootAuthProfileStore(t *testing.T) *recordingAuthProfileStore {
	t.Helper()
	return &recordingAuthProfileStore{AuthProfileStore: newRootTempAuthProfileStore(t)}
}

func (s *recordingAuthProfileStore) SetAuthProfile(ctx context.Context, update extractor.AuthProfileUpdate) (extractor.AuthProfileSnapshot, error) {
	snapshot, err := s.AuthProfileStore.SetAuthProfile(ctx, update)
	s.mu.Lock()
	s.setCalls++
	if err == nil {
		s.last = snapshot
	}
	s.lastErr = err
	s.mu.Unlock()

	return snapshot, err
}

func (s *recordingAuthProfileStore) SetCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.setCalls
}

func (s *recordingAuthProfileStore) LastSnapshot() extractor.AuthProfileSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.last
}

func startAppHostAuthCallbackDiagnosticSession(t *testing.T, store extractor.AuthProfileStore, timeout time.Duration) (*App, *fakeHostAuthSessionWindowFactory, appHostAuthSessionRequest, <-chan appHostAuthOutcome) {
	t.Helper()
	factory := &fakeHostAuthSessionWindowFactory{}
	app := newWindowedAuthApp(t)
	coordinator := extractor.NewWebViewAuthCoordinator(store, newAppHostAuthDriverWithFactory(app, factory))
	resultCh := make(chan appHostAuthOutcome, 1)
	go func() {
		result, err := coordinator.Start(context.Background(), appHostAuthWebViewRequest(timeout))
		resultCh <- appHostAuthOutcome{result: result, err: err}
	}()
	factory.waitForOpen(t)

	return app, factory, factory.request(0), resultCh
}

func appHostAuthRuntimeProvisionRequest() extractor.HostAuthRuntimeRequest {
	identity := appHostAuthAliasIdentity()
	manifest := appHostAuthWebViewRequest(time.Second).Manifest

	return extractor.HostAuthRuntimeRequest{
		PackIdentity: identity,
		Manifest:     manifest,
		SourceURL:    "https://fixture.invalid/source",
		TargetURL:    "https://fixture.invalid/item",
		ProfileRef:   "apr-alpha001",
	}
}

func receiveAppHostAuthRuntimeOutcome(t *testing.T, ch <-chan appHostAuthRuntimeOutcome) appHostAuthRuntimeOutcome {
	t.Helper()
	select {
	case outcome := <-ch:
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for host auth runtime outcome")
	}

	return appHostAuthRuntimeOutcome{}
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
	w.events = append(w.events, "execjs:queued")
	w.pendingScripts = append(w.pendingScripts, script)
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

func (w *fakeHostAuthWebviewWindow) urlsSnapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]string(nil), w.urls...)
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

func (w *fakeHostAuthWebviewWindow) pendingScriptCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return len(w.pendingScripts)
}

func (w *fakeHostAuthWebviewWindow) pendingScriptsSnapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]string(nil), w.pendingScripts...)
}

func Example_renderAppHostAuthCollectorJS_publicSafe() {
	request := appHostAuthSessionRequestFromWebView(appHostAuthWebViewRequest(time.Second), "/_goaria/auth/callback/synthetic", "http://wails.localhost/_goaria/auth/callback/synthetic", "synthetic-session-token")
	js := renderAppHostAuthCollectorJS(request)
	fmt.Println(strings.Contains(js, "callback_url"), strings.Contains(js, "synthetic-session-token"))
	// Output: true true
}
