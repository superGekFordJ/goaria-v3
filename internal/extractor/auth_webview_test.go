package extractor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWebViewAuthRequiresCapabilityAndAllowedLoginURL(t *testing.T) {
	store := newTempAuthProfileStore(t)
	driver := newFakeAuthWebViewDriver()
	coordinator := NewWebViewAuthCoordinator(store, driver)
	manifest := validCapabilityManifest()
	manifest.Capabilities = []Capability{CapabilityHTTPFetch}

	_, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(func(request *WebViewAuthRequest) {
		request.Manifest = manifest
	}))
	if err == nil {
		t.Fatal("Start() error = nil, want missing capability error")
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions, want 0", driver.OpenCount())
	}

	manifest = validCapabilityManifest()
	_, err = coordinator.Start(context.Background(), webViewAuthRequestForTest(func(request *WebViewAuthRequest) {
		request.Manifest = manifest
		request.LoginURL = "https://evil.test/login"
	}))
	if err == nil {
		t.Fatal("Start() error = nil, want denied domain error")
	}
	if driver.OpenCount() != 0 {
		t.Fatalf("driver opened %d sessions, want 0", driver.OpenCount())
	}
}

func TestWebViewAuthSuccessStoresProfileAndReturnsRedactedResult(t *testing.T) {
	store := newTempAuthProfileStore(t)
	driver := newFakeAuthWebViewDriver()
	coordinator := NewWebViewAuthCoordinator(store, driver)
	secret := "captured-token-secret"

	resultCh := make(chan webViewAuthOutcome, 1)
	go func() {
		result, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(nil))
		resultCh <- webViewAuthOutcome{result: result, err: err}
	}()
	driver.WaitForOpen(t)
	driver.Succeed(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: secret})

	outcome := receiveWebViewOutcome(t, resultCh)
	if outcome.err != nil {
		t.Fatalf("Start() error = %v", outcome.err)
	}
	if outcome.result.Status != WebViewAuthStatusSuccess || !outcome.result.Snapshot.HasSecret {
		t.Fatalf("Start() result = %#v", outcome.result)
	}
	if strings.Contains(outcome.result.String(), secret) {
		t.Fatalf("result leaked secret: %#v", outcome.result)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "apr-fixture01", "https://auth.fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderValue != "Bearer "+secret {
		t.Fatalf("resolved HeaderValue = %q, want captured token", resolved.HeaderValue)
	}
	if _, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "apr-fixture01", "https://api.auth.fixture.invalid/d/abc"); err == nil {
		t.Fatal("ResolveAuthProfile() error = nil for subdomain outside login origin scope, want error")
	}
	if driver.CloseCount() != 1 {
		t.Fatalf("CloseCount() = %d, want 1", driver.CloseCount())
	}
}

func TestWebViewAuthSuccessStoreIsDefaultMaterializerCompatible(t *testing.T) {
	store := newTempAuthProfileStore(t)
	driver := newFakeAuthWebViewDriver()
	coordinator := NewWebViewAuthCoordinator(store, driver)
	secret := "materializer-compatible-token"
	packID := "xpk-webview001"
	profileID := AuthProfileID("apr-webview001")
	targetURL := "https://fixture.test/d/abc"

	resultCh := make(chan webViewAuthOutcome, 1)
	go func() {
		result, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(func(request *WebViewAuthRequest) {
			request.PackID = packID
			request.Manifest.PackID = packID
			request.Manifest.Domains = []DomainRule{{Host: "fixture.test"}}
			request.ProfileID = profileID
			request.LoginURL = "https://fixture.test/login"
			request.AllowedDomains = []DomainRule{{Host: "fixture.test"}}
		}))
		resultCh <- webViewAuthOutcome{result: result, err: err}
	}()
	driver.WaitForOpen(t)
	driver.Succeed(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: secret, RedactedDisplay: "captured bearer"})

	outcome := receiveWebViewOutcome(t, resultCh)
	if outcome.err != nil {
		t.Fatalf("Start() error = %v", outcome.err)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), packID, profileID, targetURL)
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	material, err := NewDefaultAuthMaterializer().MaterializeAuth(resolved)
	if err != nil {
		t.Fatalf("MaterializeAuth() error = %v", err)
	}
	if material.Kind != AuthSecretKindBearer || material.HeaderName != "Authorization" || material.RedactedDisplay == "" {
		t.Fatalf("materialized public-safe shape = %#v", material)
	}
	assertNoForbiddenSubstrings(t, material.String(), secret, "Bearer "+secret)
}

func TestWebViewAuthSuccessPersistsWhenCallerContextCanceledAfterSuccessWins(t *testing.T) {
	store := newTempAuthProfileStore(t)
	driver := newFakeAuthWebViewDriver()
	driver.syncCalls = true
	coordinator := NewWebViewAuthCoordinator(store, driver)
	ctx, cancel := context.WithCancel(context.Background())
	secret := "winning-token"

	resultCh := make(chan webViewAuthOutcome, 1)
	go func() {
		result, err := coordinator.Start(ctx, webViewAuthRequestForTest(nil))
		resultCh <- webViewAuthOutcome{result: result, err: err}
	}()
	driver.WaitForOpen(t)
	driver.Succeed(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: secret})
	cancel()

	outcome := receiveWebViewOutcome(t, resultCh)
	if outcome.err != nil {
		t.Fatalf("Start() error = %v", outcome.err)
	}
	if outcome.result.Status != WebViewAuthStatusSuccess {
		t.Fatalf("Start() status = %q, want success", outcome.result.Status)
	}
	resolved, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "apr-fixture01", "https://auth.fixture.invalid/d/abc")
	if err != nil {
		t.Fatalf("ResolveAuthProfile() error = %v", err)
	}
	if resolved.HeaderValue != "Bearer "+secret {
		t.Fatalf("HeaderValue = %q, want winning token", resolved.HeaderValue)
	}
}

func TestWebViewAuthAllowedDomainsDefaultToLoginOriginAndExplicitDomainsAreScoped(t *testing.T) {
	manifest := validCapabilityManifest()
	manifest.Domains = []DomainRule{
		{Host: "auth.fixture.invalid", IncludeSubdomains: true},
		{Host: "other.auth.fixture.invalid"},
	}

	for _, tt := range []struct {
		name           string
		allowedDomains []DomainRule
	}{
		{name: "different manifest domain", allowedDomains: []DomainRule{{Host: "other.auth.fixture.invalid"}}},
		{name: "expands login origin", allowedDomains: []DomainRule{{Host: "auth.fixture.invalid", IncludeSubdomains: true}}},
		{name: "outside manifest", allowedDomains: []DomainRule{{Host: "evil.test"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTempAuthProfileStore(t)
			driver := newFakeAuthWebViewDriver()
			coordinator := NewWebViewAuthCoordinator(store, driver)
			_, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(func(request *WebViewAuthRequest) {
				request.Manifest = manifest
				request.AllowedDomains = tt.allowedDomains
			}))
			if err == nil {
				t.Fatal("Start() error = nil, want scoped-domain validation error")
			}
			if driver.OpenCount() != 0 {
				t.Fatalf("driver opened %d sessions, want 0", driver.OpenCount())
			}
		})
	}

	t.Run("explicit exact login origin succeeds", func(t *testing.T) {
		store := newTempAuthProfileStore(t)
		driver := newFakeAuthWebViewDriver()
		coordinator := NewWebViewAuthCoordinator(store, driver)
		resultCh := make(chan webViewAuthOutcome, 1)
		go func() {
			result, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(func(request *WebViewAuthRequest) {
				request.Manifest = manifest
				request.AllowedDomains = []DomainRule{{Host: "auth.fixture.invalid"}}
			}))
			resultCh <- webViewAuthOutcome{result: result, err: err}
		}()
		driver.WaitForOpen(t)
		driver.Succeed(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "origin-token"})
		outcome := receiveWebViewOutcome(t, resultCh)
		if outcome.err != nil {
			t.Fatalf("Start() error = %v", outcome.err)
		}
		if outcome.result.Status != WebViewAuthStatusSuccess {
			t.Fatalf("Start() status = %q, want success", outcome.result.Status)
		}
	})
}

func TestWebViewAuthCancelClosesOnceAndDoesNotStore(t *testing.T) {
	store := newTempAuthProfileStore(t)
	driver := newFakeAuthWebViewDriver()
	coordinator := NewWebViewAuthCoordinator(store, driver)

	resultCh := make(chan webViewAuthOutcome, 1)
	go func() {
		result, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(nil))
		resultCh <- webViewAuthOutcome{result: result, err: err}
	}()
	driver.WaitForOpen(t)
	driver.Cancel()

	outcome := receiveWebViewOutcome(t, resultCh)
	driver.Succeed(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "late-token"})
	if outcome.err != nil {
		t.Fatalf("Start() error = %v", outcome.err)
	}
	if outcome.result.Status != WebViewAuthStatusCanceled {
		t.Fatalf("Start() status = %q, want canceled", outcome.result.Status)
	}
	if _, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "apr-fixture01", "https://auth.fixture.invalid/d/abc"); err == nil {
		t.Fatal("ResolveAuthProfile() error = nil, want no stored token")
	}
	if driver.CloseCount() != 1 {
		t.Fatalf("CloseCount() = %d, want 1", driver.CloseCount())
	}
}

func TestWebViewAuthTimeoutClosesOnceAndDoesNotStore(t *testing.T) {
	store := newTempAuthProfileStore(t)
	driver := newFakeAuthWebViewDriver()
	coordinator := NewWebViewAuthCoordinator(store, driver)

	result, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(func(request *WebViewAuthRequest) {
		request.Timeout = 5 * time.Millisecond
	}))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if result.Status != WebViewAuthStatusTimeout {
		t.Fatalf("Start() status = %q, want timeout", result.Status)
	}
	if _, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "apr-fixture01", "https://auth.fixture.invalid/d/abc"); err == nil {
		t.Fatal("ResolveAuthProfile() error = nil, want no stored token")
	}
	if driver.CloseCount() != 1 {
		t.Fatalf("CloseCount() = %d, want 1", driver.CloseCount())
	}
}

func TestWebViewAuthOnlyFirstTerminalResultWins(t *testing.T) {
	store := newTempAuthProfileStore(t)
	driver := newFakeAuthWebViewDriver()
	coordinator := NewWebViewAuthCoordinator(store, driver)
	secret := "first-token"

	resultCh := make(chan webViewAuthOutcome, 1)
	go func() {
		result, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(nil))
		resultCh <- webViewAuthOutcome{result: result, err: err}
	}()
	driver.WaitForOpen(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				driver.Succeed(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: secret})
				return
			}
			driver.Cancel()
		}(i)
	}
	wg.Wait()

	outcome := receiveWebViewOutcome(t, resultCh)
	if outcome.err != nil {
		t.Fatalf("Start() error = %v", outcome.err)
	}
	if outcome.result.Status != WebViewAuthStatusSuccess && outcome.result.Status != WebViewAuthStatusCanceled {
		t.Fatalf("unexpected terminal status: %#v", outcome.result)
	}
	if driver.CloseCount() != 1 {
		t.Fatalf("CloseCount() = %d, want 1", driver.CloseCount())
	}
}

func TestWebViewAuthRedactsDriverAndCaptureErrors(t *testing.T) {
	secret := "raw-error-token"
	t.Run("driver error", func(t *testing.T) {
		store := newTempAuthProfileStore(t)
		driver := newFakeAuthWebViewDriver()
		driver.openErr = errors.New("driver failed with Authorization: Bearer " + secret)
		coordinator := NewWebViewAuthCoordinator(store, driver)

		_, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(func(request *WebViewAuthRequest) {
			request.LoginURL = "https://auth.fixture.invalid/login?token=" + secret
		}))
		if err == nil {
			t.Fatal("Start() error = nil, want driver error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Start() leaked secret: %v", err)
		}
	})

	t.Run("capture error", func(t *testing.T) {
		store := newTempAuthProfileStore(t)
		driver := newFakeAuthWebViewDriver()
		coordinator := NewWebViewAuthCoordinator(store, driver)
		resultCh := make(chan webViewAuthOutcome, 1)
		go func() {
			result, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(nil))
			resultCh <- webViewAuthOutcome{result: result, err: err}
		}()
		driver.WaitForOpen(t)
		driver.Succeed(AuthWebViewToken{Kind: AuthSecretKindBearer, Secret: "bad\r\n" + secret})

		outcome := receiveWebViewOutcome(t, resultCh)
		if outcome.err == nil {
			t.Fatal("Start() error = nil, want capture validation error")
		}
		if strings.Contains(outcome.err.Error(), secret) {
			t.Fatalf("Start() leaked secret: %v", outcome.err)
		}
		if _, err := store.ResolveAuthProfile(context.Background(), "xpk-fixture01", "apr-fixture01", "https://auth.fixture.invalid/d/abc"); err == nil {
			t.Fatal("ResolveAuthProfile() error = nil, want no stored token")
		}
	})
}

func TestWebViewAuthCallbackRequestValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*WebViewAuthRequest)
	}{
		{name: "missing transport", mutate: func(request *WebViewAuthRequest) { request.CallbackTransport = WebViewAuthCallbackTransport{} }},
		{name: "unsupported mode", mutate: func(request *WebViewAuthRequest) { request.CallbackTransport.Mode = "manual" }},
		{name: "empty content types", mutate: func(request *WebViewAuthRequest) { request.CallbackTransport.ContentTypes = nil }},
		{name: "oversized body", mutate: func(request *WebViewAuthRequest) {
			request.CallbackTransport.MaxBodyBytes = privateAuthRuntimeMaxCallbackBodyBytes + 1
		}},
		{name: "missing collector", mutate: func(request *WebViewAuthRequest) { request.CollectorJS = "" }},
		{name: "nul collector", mutate: func(request *WebViewAuthRequest) { request.CollectorJS = "x\x00" }},
		{name: "bad capture format", mutate: func(request *WebViewAuthRequest) { request.Capture.Format = "text" }},
		{name: "empty candidates", mutate: func(request *WebViewAuthRequest) { request.Capture.SecretCandidates = nil }},
		{name: "bad path", mutate: func(request *WebViewAuthRequest) { request.Capture.SecretCandidates = []string{"capture[0].secret"} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTempAuthProfileStore(t)
			driver := newFakeAuthWebViewDriver()
			coordinator := NewWebViewAuthCoordinator(store, driver)
			_, err := coordinator.Start(context.Background(), webViewAuthRequestForTest(tt.mutate))
			if err == nil {
				t.Fatal("Start() error = nil, want callback metadata validation failure")
			}
			if driver.OpenCount() != 0 {
				t.Fatalf("driver opened %d sessions, want 0", driver.OpenCount())
			}
		})
	}
}

func TestWebViewAuthCallbackParser(t *testing.T) {
	base := webViewAuthRequestForTest(nil)
	expires := "2026-05-14T12:00:00Z"
	token, err := ParseWebViewAuthCallbackPayload(base, []byte(`{"kind":"bearer","secret":"  synthetic-captured-secret  ","expires_at":"`+expires+`","redacted_display":"synthetic captured auth"}`))
	if err != nil {
		t.Fatalf("ParseWebViewAuthCallbackPayload() error = %v", err)
	}
	if token.Kind != AuthSecretKindBearer || token.Secret != "synthetic-captured-secret" || token.ExpiresAt == nil || token.ExpiresAt.Format(time.RFC3339) != expires || token.RedactedDisplay != "synthetic captured auth" {
		t.Fatalf("parsed token = %#v", token)
	}

	nested, err := ParseWebViewAuthCallbackPayload(base, []byte(`{"kind":"bearer","capture":{"secret":"nested-secret"}}`))
	if err != nil {
		t.Fatalf("nested ParseWebViewAuthCallbackPayload() error = %v", err)
	}
	if nested.Secret != "nested-secret" {
		t.Fatalf("nested secret = %q", nested.Secret)
	}

	defaultKind, err := ParseWebViewAuthCallbackPayload(base, []byte(`{"secret":"default-kind-secret"}`))
	if err != nil {
		t.Fatalf("default kind ParseWebViewAuthCallbackPayload() error = %v", err)
	}
	if defaultKind.Kind != AuthSecretKindBearer {
		t.Fatalf("default kind = %q", defaultKind.Kind)
	}

	for _, tt := range []struct {
		name string
		raw  string
	}{
		{name: "malformed", raw: `{`},
		{name: "trailing", raw: `{"secret":"x"} {}`},
		{name: "array", raw: `[{"secret":"x"}]`},
		{name: "scalar", raw: `"x"`},
		{name: "empty object", raw: `{}`},
		{name: "missing secret", raw: `{"kind":"bearer"}`},
		{name: "empty secret", raw: `{"secret":"   "}`},
		{name: "crlf secret", raw: `{"secret":"bad\nsecret"}`},
		{name: "kind mismatch", raw: `{"kind":"cookie","secret":"x"}`},
		{name: "unsupported kind", raw: `{"kind":"basic","secret":"x"}`},
		{name: "bad expiry", raw: `{"kind":"bearer","secret":"x","expires_at":"not-time"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseWebViewAuthCallbackPayload(base, []byte(tt.raw))
			if err == nil {
				t.Fatal("ParseWebViewAuthCallbackPayload() error = nil, want validation failure")
			}
			assertNoForbiddenSubstrings(t, err.Error(), "bad\nsecret", "default-kind-secret", "synthetic-captured-secret")
		})
	}
}

func TestWebViewAuthCallbackParserCaptureMatrix(t *testing.T) {
	base := webViewAuthRequestForTest(nil)
	expires := "2026-05-14T12:00:00Z"
	for _, tt := range []struct {
		name        string
		raw         string
		wantKind    AuthSecretKind
		wantSecret  string
		wantExpiry  bool
		wantDisplay string
	}{
		{name: "primary candidate", raw: `{"kind":"bearer","secret":"  matrix-primary-secret  "}`, wantKind: AuthSecretKindBearer, wantSecret: "matrix-primary-secret"},
		{name: "nested candidate", raw: `{"kind":"bearer","capture":{"secret":"matrix-nested-secret"}}`, wantKind: AuthSecretKindBearer, wantSecret: "matrix-nested-secret"},
		{name: "default kind when field missing", raw: `{"secret":"matrix-default-kind-secret"}`, wantKind: AuthSecretKindBearer, wantSecret: "matrix-default-kind-secret"},
		{name: "explicit matching kind", raw: `{"kind":"bearer","secret":"matrix-kind-secret"}`, wantKind: AuthSecretKindBearer, wantSecret: "matrix-kind-secret"},
		{name: "valid expiry and redacted display", raw: `{"kind":"bearer","secret":"matrix-expiry-secret","expires_at":"` + expires + `","redacted_display":"synthetic display"}`, wantKind: AuthSecretKindBearer, wantSecret: "matrix-expiry-secret", wantExpiry: true, wantDisplay: "synthetic display"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			token, err := ParseWebViewAuthCallbackPayload(base, []byte(tt.raw))
			if err != nil {
				t.Fatalf("ParseWebViewAuthCallbackPayload() error = %v", err)
			}
			if token.Kind != tt.wantKind || token.Secret != tt.wantSecret {
				t.Fatalf("parsed token public shape = kind:%q secret-match:%t", token.Kind, token.Secret == tt.wantSecret)
			}
			if (token.ExpiresAt != nil) != tt.wantExpiry {
				t.Fatalf("expiry presence = %t, want %t", token.ExpiresAt != nil, tt.wantExpiry)
			}
			if token.ExpiresAt != nil && token.ExpiresAt.Format(time.RFC3339) != expires {
				t.Fatalf("expiry mismatch")
			}
			if token.RedactedDisplay != tt.wantDisplay {
				t.Fatalf("redacted display = %q, want %q", token.RedactedDisplay, tt.wantDisplay)
			}
		})
	}

	for _, tt := range []struct {
		name      string
		raw       string
		forbidden []string
	}{
		{name: "capture_secret_candidate_mismatch_missing", raw: `{"kind":"bearer","capture":{}}`},
		{name: "capture_secret_candidate_mismatch_non_string", raw: `{"kind":"bearer","secret":123,"capture":{"secret":false}}`},
		{name: "capture_secret_candidate_mismatch_empty_after_trim", raw: `{"kind":"bearer","secret":"   "}`},
		{name: "capture_secret_candidate_mismatch_array", raw: `{"kind":"bearer","secret":["array-secret"]}`, forbidden: []string{"array-secret"}},
		{name: "capture_secret_candidate_mismatch_scalar_parent", raw: `{"kind":"bearer","capture":"scalar"}`},
		{name: "capture_secret_candidate_mismatch_crlf", raw: `{"kind":"bearer","secret":"bad\nsecret"}`, forbidden: []string{"bad\nsecret"}},
		{name: "capture_kind_field_mismatch_wrong_kind", raw: `{"kind":"cookie","secret":"matrix-kind-mismatch-secret"}`, forbidden: []string{"matrix-kind-mismatch-secret"}},
		{name: "capture_kind_field_mismatch_unsupported_kind", raw: `{"kind":"basic","secret":"matrix-unsupported-secret"}`, forbidden: []string{"matrix-unsupported-secret"}},
		{name: "capture_kind_field_mismatch_non_string_kind", raw: `{"kind":42,"secret":"matrix-non-string-kind-secret"}`, forbidden: []string{"matrix-non-string-kind-secret"}},
		{name: "capture_kind_field_mismatch_empty_kind", raw: `{"kind":"","secret":"matrix-empty-kind-secret"}`, forbidden: []string{"matrix-empty-kind-secret"}},
		{name: "invalid_expiry_type", raw: `{"kind":"bearer","secret":"matrix-expiry-type-secret","expires_at":123}`, forbidden: []string{"matrix-expiry-type-secret"}},
		{name: "invalid_expiry_string", raw: `{"kind":"bearer","secret":"matrix-expiry-string-secret","expires_at":"not-time"}`, forbidden: []string{"matrix-expiry-string-secret"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseWebViewAuthCallbackPayload(base, []byte(tt.raw))
			if err == nil {
				t.Fatal("ParseWebViewAuthCallbackPayload() error = nil, want validation failure")
			}
			assertNoForbiddenSubstrings(t, err.Error(), tt.forbidden...)
		})
	}
}

func TestWebViewAuthCallbackParserMaterializerCompatibilityMatrix(t *testing.T) {
	for _, tt := range []struct {
		name       string
		kind       AuthSecretKind
		raw        string
		headerName string
	}{
		{name: "bearer", kind: AuthSecretKindBearer, raw: `{"kind":"bearer","secret":"matrix-materializer-bearer","redacted_display":"matrix-materializer-bearer"}`, headerName: "Authorization"},
		{name: "cookie", kind: AuthSecretKindCookie, raw: `{"kind":"cookie","secret":"sid=matrix-materializer-cookie; pref=synthetic","redacted_display":"sid=matrix-materializer-cookie"}`, headerName: "Cookie"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := webViewAuthRequestForTest(func(request *WebViewAuthRequest) {
				request.PackID = "xpk-matrix001"
				request.Manifest.PackID = "xpk-matrix001"
				request.Manifest.Domains = []DomainRule{{Host: "matrix.test"}}
				request.ProfileID = "apr-matrix001"
				request.LoginURL = "https://matrix.test/login"
				request.AllowedDomains = []DomainRule{{Host: "matrix.test"}}
				request.Kind = tt.kind
			})
			token, err := ParseWebViewAuthCallbackPayload(request, []byte(tt.raw))
			if err != nil {
				t.Fatalf("ParseWebViewAuthCallbackPayload() error = %v", err)
			}
			store := newTempAuthProfileStore(t)
			coordinator := NewWebViewAuthCoordinator(store, newFakeAuthWebViewDriver())
			result, err := coordinator.handleSuccess(request, token)
			if err != nil {
				t.Fatalf("handleSuccess() error = %v", err)
			}
			if result.Status != WebViewAuthStatusSuccess || !result.Snapshot.HasSecret {
				t.Fatalf("handleSuccess() public shape invalid")
			}
			resolved, err := store.ResolveAuthProfile(context.Background(), request.PackID, request.ProfileID, "https://matrix.test/item")
			if err != nil {
				t.Fatalf("ResolveAuthProfile() error = %v", err)
			}
			material, err := NewDefaultAuthMaterializer().MaterializeAuth(resolved)
			if err != nil {
				t.Fatalf("MaterializeAuth() error = %v", err)
			}
			if material.Kind != tt.kind || material.HeaderName != tt.headerName || material.HeaderValue() == "" {
				t.Fatalf("materialized public shape invalid")
			}
			formatted := fmt.Sprintf("%#v %#v", result, material)
			assertNoForbiddenSubstrings(t, formatted, token.Secret, material.HeaderValue())
		})
	}
}

type webViewAuthOutcome struct {
	result WebViewAuthResult
	err    error
}

type fakeAuthWebViewDriver struct {
	mu         sync.Mutex
	sink       AuthWebViewSink
	openCount  int
	closeCount int
	openErr    error
	syncCalls  bool
	opened     chan struct{}
}

func newFakeAuthWebViewDriver() *fakeAuthWebViewDriver {
	return &fakeAuthWebViewDriver{opened: make(chan struct{})}
}

func (d *fakeAuthWebViewDriver) OpenAuthSession(_ context.Context, _ WebViewAuthRequest, sink AuthWebViewSink) (AuthWebViewSession, error) {
	if d.openErr != nil {
		return nil, d.openErr
	}
	d.mu.Lock()
	d.openCount++
	d.sink = AuthWebViewSink{
		OnSuccess: func(token AuthWebViewToken) {
			if d.syncCalls {
				sink.OnSuccess(token)
				return
			}
			go sink.OnSuccess(token)
		},
		OnCancel: func() {
			if d.syncCalls {
				sink.OnCancel()
				return
			}
			go sink.OnCancel()
		},
		OnError: func(err error) {
			if d.syncCalls {
				sink.OnError(err)
				return
			}
			go sink.OnError(err)
		},
	}
	select {
	case <-d.opened:
	default:
		close(d.opened)
	}
	d.mu.Unlock()

	return fakeAuthSession{driver: d}, nil
}

func (d *fakeAuthWebViewDriver) WaitForOpen(t *testing.T) {
	t.Helper()
	select {
	case <-d.opened:
	case <-time.After(time.Second):
		t.Fatal("driver did not open")
	}
}

func (d *fakeAuthWebViewDriver) Succeed(token AuthWebViewToken) {
	d.mu.Lock()
	sink := d.sink
	d.mu.Unlock()
	if sink.OnSuccess != nil {
		sink.OnSuccess(token)
	}
}

func (d *fakeAuthWebViewDriver) Cancel() {
	d.mu.Lock()
	sink := d.sink
	d.mu.Unlock()
	if sink.OnCancel != nil {
		sink.OnCancel()
	}
}

func (d *fakeAuthWebViewDriver) OpenCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.openCount
}

func (d *fakeAuthWebViewDriver) CloseCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.closeCount
}

type fakeAuthSession struct {
	driver *fakeAuthWebViewDriver
}

func (s fakeAuthSession) Close() error {
	s.driver.mu.Lock()
	defer s.driver.mu.Unlock()
	s.driver.closeCount++

	return nil
}

func receiveWebViewOutcome(t *testing.T, resultCh <-chan webViewAuthOutcome) webViewAuthOutcome {
	t.Helper()

	select {
	case outcome := <-resultCh:
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for auth result")
	}

	return webViewAuthOutcome{}
}

func webViewAuthRequestForTest(mutate func(*WebViewAuthRequest)) WebViewAuthRequest {
	request := WebViewAuthRequest{
		PackID:         "xpk-fixture01",
		Manifest:       validCapabilityManifest(),
		ProfileID:      "apr-fixture01",
		LoginURL:       "https://auth.fixture.invalid/login",
		AllowedDomains: []DomainRule{{Host: "auth.fixture.invalid"}},
		Timeout:        time.Second,
		Kind:           AuthSecretKindBearer,
		CallbackTransport: WebViewAuthCallbackTransport{
			Mode:         "local_post",
			ContentTypes: []string{"application/json"},
			MaxBodyBytes: 16384,
		},
		CollectorJS: "(() => { return function(ctx, postCapture) { return ctx && postCapture; }; })();",
		Capture: WebViewAuthCaptureContract{
			Format:               "json",
			SecretCandidates:     []string{"secret", "capture.secret"},
			KindField:            "kind",
			ExpiresAtField:       "expires_at",
			RedactedDisplayField: "redacted_display",
			TrimSpace:            true,
			RejectCRLF:           true,
		},
	}
	if mutate != nil {
		mutate(&request)
	}

	return request
}
