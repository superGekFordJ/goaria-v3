package extractor

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAuthMaterializerMaterializesBearerAndCookiePassThrough(t *testing.T) {
	materializer := NewDefaultAuthMaterializer()

	bearer, err := materializer.MaterializeAuth(ResolvedAuthSecret{
		Kind:            AuthSecretKindBearer,
		HeaderName:      "Authorization",
		HeaderValue:     "Bearer raw-bearer-token",
		RedactedDisplay: "safe bearer",
	})
	if err != nil {
		t.Fatalf("MaterializeAuth(bearer) error = %v", err)
	}
	if bearer.HeaderName != "Authorization" || bearer.HeaderValue() != "Bearer raw-bearer-token" || bearer.Kind != AuthSecretKindBearer {
		t.Fatal("bearer material did not preserve the expected safe metadata and header value")
	}
	assertStringSetContains(t, bearer.SensitiveValues(), "Bearer raw-bearer-token", "raw-bearer-token")

	cookie, err := materializer.MaterializeAuth(ResolvedAuthSecret{
		Kind:        AuthSecretKindCookie,
		HeaderName:  "Cookie",
		HeaderValue: "sid=cookie-secret; refresh=second-secret",
	})
	if err != nil {
		t.Fatalf("MaterializeAuth(cookie) error = %v", err)
	}
	if cookie.HeaderName != "Cookie" || cookie.HeaderValue() != "sid=cookie-secret; refresh=second-secret" || cookie.Kind != AuthSecretKindCookie {
		t.Fatal("cookie material did not preserve the expected safe metadata and header value")
	}
	assertStringSetContains(t, cookie.SensitiveValues(), "sid=cookie-secret; refresh=second-secret", "sid=cookie-secret", "cookie-secret", "refresh=second-secret", "second-secret")
}

func TestAuthMaterializerInfersLegacyKindFromSupportedHeader(t *testing.T) {
	materializer := NewDefaultAuthMaterializer()

	for _, tt := range []struct {
		name       string
		headerName string
		value      string
		wantKind   AuthSecretKind
	}{
		{name: "authorization", headerName: "Authorization", value: "Bearer legacy-token", wantKind: AuthSecretKindBearer},
		{name: "cookie", headerName: "Cookie", value: "sid=legacy-cookie", wantKind: AuthSecretKindCookie},
	} {
		t.Run(tt.name, func(t *testing.T) {
			material, err := materializer.MaterializeAuth(ResolvedAuthSecret{HeaderName: tt.headerName, HeaderValue: tt.value})
			if err != nil {
				t.Fatalf("MaterializeAuth() error = %v", err)
			}
			if material.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", material.Kind, tt.wantKind)
			}
		})
	}

	_, err := materializer.MaterializeAuth(ResolvedAuthSecret{HeaderName: "X-Api-Key", HeaderValue: "legacy-secret"})
	if err == nil {
		t.Fatal("MaterializeAuth(unsupported legacy header) error = nil, want error")
	}
	if strings.Contains(err.Error(), "legacy-secret") {
		t.Fatalf("MaterializeAuth() leaked raw secret: %v", err)
	}
}

func TestAuthMaterializerRejectsUnsupportedHeadersKindsAndMismatches(t *testing.T) {
	materializer := NewDefaultAuthMaterializer()

	for _, headerName := range []string{"X-Api-Key", "Proxy-Authorization", "Set-Cookie", "Host", "X-Anything"} {
		t.Run("header "+headerName, func(t *testing.T) {
			_, err := materializer.MaterializeAuth(ResolvedAuthSecret{Kind: AuthSecretKindBearer, HeaderName: headerName, HeaderValue: "raw-secret-value"})
			if err == nil {
				t.Fatal("MaterializeAuth() error = nil, want error")
			}
			if strings.Contains(err.Error(), "raw-secret-value") {
				t.Fatalf("MaterializeAuth() leaked raw secret: %v", err)
			}
		})
	}

	for _, kind := range []AuthSecretKind{AuthSecretKind("basic"), AuthSecretKind("api-key")} {
		t.Run("kind "+string(kind), func(t *testing.T) {
			_, err := materializer.MaterializeAuth(ResolvedAuthSecret{Kind: kind, HeaderName: "Authorization", HeaderValue: "raw-secret-value"})
			if err == nil {
				t.Fatal("MaterializeAuth() error = nil, want error")
			}
			if strings.Contains(err.Error(), "raw-secret-value") {
				t.Fatalf("MaterializeAuth() leaked raw secret: %v", err)
			}
		})
	}

	for _, tt := range []struct {
		name       string
		kind       AuthSecretKind
		headerName string
		value      string
	}{
		{name: "bearer with cookie", kind: AuthSecretKindBearer, headerName: "Cookie", value: "sid=raw-cookie-secret"},
		{name: "cookie with authorization", kind: AuthSecretKindCookie, headerName: "Authorization", value: "Bearer raw-token-value"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := materializer.MaterializeAuth(ResolvedAuthSecret{Kind: tt.kind, HeaderName: tt.headerName, HeaderValue: tt.value})
			if err == nil {
				t.Fatal("MaterializeAuth() error = nil, want error")
			}
			assertNoForbiddenSubstrings(t, err.Error(), tt.value, "raw-cookie-secret", "raw-token-value")
		})
	}
}

func TestAuthMaterializerRejectsMissingOrCRLFMaterial(t *testing.T) {
	materializer := NewDefaultAuthMaterializer()

	for _, tt := range []struct {
		name       string
		headerName string
		value      string
	}{
		{name: "empty header name", headerName: "", value: "Bearer raw-token-value"},
		{name: "empty value", headerName: "Authorization", value: ""},
		{name: "whitespace value", headerName: "Authorization", value: "   \t"},
		{name: "crlf header name", headerName: "Authorization\r\nX-Injected", value: "Bearer raw-token-value"},
		{name: "crlf header value", headerName: "Authorization", value: "Bearer raw-token-value\r\nX-Injected: bad"},
		{name: "crlf cookie value", headerName: "Cookie", value: "sid=raw-cookie-secret\nother=value"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := materializer.MaterializeAuth(ResolvedAuthSecret{HeaderName: tt.headerName, HeaderValue: tt.value})
			if err == nil {
				t.Fatal("MaterializeAuth() error = nil, want error")
			}
			assertNoForbiddenSubstrings(t, err.Error(), "raw-token-value", "Bearer raw-token-value", "sid=raw-cookie-secret", "raw-cookie-secret")
		})
	}
}

func TestAuthMaterializerSanitizesRedactedDisplayAndDebugStrings(t *testing.T) {
	materializer := NewDefaultAuthMaterializer()

	material, err := materializer.MaterializeAuth(ResolvedAuthSecret{
		Kind:            AuthSecretKindCookie,
		HeaderName:      "Cookie",
		HeaderValue:     "sid=cookie-secret; refresh=second-secret",
		RedactedDisplay: "cookie sid=cookie-secret has second-secret",
	})
	if err != nil {
		t.Fatalf("MaterializeAuth() error = %v", err)
	}
	assertNoForbiddenSubstrings(t, material.RedactedDisplay, "sid=cookie-secret", "cookie-secret", "refresh=second-secret", "second-secret")

	formatted := fmt.Sprintf("%v %#v %+v", material, material, material)
	assertNoForbiddenSubstrings(t, formatted, "sid=cookie-secret; refresh=second-secret", "sid=cookie-secret", "cookie-secret", "refresh=second-secret", "second-secret")
}

func TestMaterializedAuthSecretApplyToOverwritesAndRemovesStaleAuthHeaders(t *testing.T) {
	materializer := NewDefaultAuthMaterializer()

	bearer, err := materializer.MaterializeAuth(ResolvedAuthSecret{Kind: AuthSecretKindBearer, HeaderName: "Authorization", HeaderValue: "Bearer fresh-token"})
	if err != nil {
		t.Fatalf("MaterializeAuth(bearer) error = %v", err)
	}
	headers := http.Header{}
	headers.Add("Authorization", "Bearer stale-token")
	headers.Add("Cookie", "sid=stale-cookie")
	headers.Set("Accept", "application/json")
	bearer.ApplyTo(headers)
	if got := headers.Values("Authorization"); len(got) != 1 || got[0] != "Bearer fresh-token" {
		t.Fatal("Authorization values did not contain exactly the fresh bearer material")
	}
	if got := headers.Get("Cookie"); got != "" {
		t.Fatal("Cookie header was not removed")
	}
	if got := headers.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want preserved", got)
	}

	cookie, err := materializer.MaterializeAuth(ResolvedAuthSecret{Kind: AuthSecretKindCookie, HeaderName: "Cookie", HeaderValue: "sid=fresh-cookie"})
	if err != nil {
		t.Fatalf("MaterializeAuth(cookie) error = %v", err)
	}
	headers = http.Header{}
	headers.Add("Authorization", "Bearer stale-token")
	headers.Add("Cookie", "sid=stale-cookie")
	headers.Set("Accept", "application/json")
	cookie.ApplyTo(headers)
	if got := headers.Get("Authorization"); got != "" {
		t.Fatal("Authorization header was not removed")
	}
	if got := headers.Values("Cookie"); len(got) != 1 || got[0] != "sid=fresh-cookie" {
		t.Fatal("Cookie values did not contain exactly the fresh cookie material")
	}
	if got := headers.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want preserved", got)
	}

	bearer.ApplyTo(nil)
}

func TestAuthMaterializerNormalizesRawBearerSecret(t *testing.T) {
	materializer := NewDefaultAuthMaterializer()

	for _, input := range []string{"Bearer stripped-token", "stripped-token"} {
		t.Run(input, func(t *testing.T) {
			material, err := materializer.MaterializeAuth(ResolvedAuthSecret{Kind: AuthSecretKindBearer, HeaderName: "Authorization", HeaderValue: input})
			if err != nil {
				t.Fatalf("MaterializeAuth() error = %v", err)
			}
			if got := material.HeaderValue(); got != "Bearer stripped-token" {
				t.Fatal("HeaderValue() did not normalize the bearer material")
			}
			assertStringSetContains(t, material.SensitiveValues(), "Bearer stripped-token", "stripped-token")
			formatted := fmt.Sprintf("%v %#v %+v", material, material, material)
			assertNoForbiddenSubstrings(t, formatted, "Bearer stripped-token", "stripped-token")
		})
	}
}

func assertStringSetContains(t *testing.T, values []string, want ...string) {
	t.Helper()
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	for _, value := range want {
		if _, ok := set[value]; !ok {
			t.Fatal("sensitive values were missing an expected redaction form")
		}
	}
}

func assertNoForbiddenSubstrings(t *testing.T, value string, forbidden ...string) {
	t.Helper()
	for _, substring := range forbidden {
		if substring != "" && strings.Contains(value, substring) {
			t.Fatal("value leaked a forbidden sensitive substring")
		}
	}
}
