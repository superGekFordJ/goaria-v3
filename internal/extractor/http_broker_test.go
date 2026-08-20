package extractor

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPBrokerAllowsGETAndHEADToManifestDomains(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			var seenMethod string
			broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seenMethod = req.Method
				return textResponse(200, "ok"), nil
			}), nil)

			resp, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:   "xpk-fixture01",
				Manifest: httpBrokerManifest(),
				Method:   method,
				URL:      "https://api.fixture.invalid/path",
			})
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if seenMethod != method {
				t.Fatalf("transport method = %q, want %q", seenMethod, method)
			}
			if resp.StatusCode != 200 || resp.FinalURL != "https://api.fixture.invalid/path" {
				t.Fatalf("Fetch() response = %#v", resp)
			}
		})
	}
}

func TestHTTPBrokerRejectsUnsupportedMethodsByDefault(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, "BREW"} {
		t.Run(method, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)

			_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:   "xpk-fixture01",
				Manifest: httpBrokerManifest(),
				Method:   method,
				URL:      "https://fixture.invalid/d/abc",
			})
			if err == nil {
				t.Fatal("Fetch() error = nil, want error")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport invoked %d times, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerRejectsForbiddenPackHeaders(t *testing.T) {
	for _, header := range []string{
		"Authorization",
		"Cookie",
		"Set-Cookie",
		"Host",
		"Content-Length",
		"Transfer-Encoding",
		"Connection",
		"Proxy-Authorization",
		"X-Api-Key",
	} {
		t.Run(header, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)

			_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:   "xpk-fixture01",
				Manifest: httpBrokerManifest(),
				Method:   http.MethodGet,
				URL:      "https://fixture.invalid/d/abc",
				Headers: map[string]string{
					header: "raw-secret-value",
				},
			})
			if err == nil {
				t.Fatal("Fetch() error = nil, want error")
			}
			if strings.Contains(err.Error(), "raw-secret-value") {
				t.Fatalf("Fetch() leaked forbidden header value: %v", err)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport invoked %d times, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerAllowsSafeHeadersWithLimits(t *testing.T) {
	seen := make(http.Header)
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:   "xpk-fixture01",
		Manifest: httpBrokerManifest(),
		Method:   http.MethodGet,
		URL:      "https://fixture.invalid/d/abc",
		Headers: map[string]string{
			"Accept":          "application/json",
			"Accept-Language": "en-US",
			"Content-Type":    "application/json",
			"Referer":         "https://fixture.invalid/",
			"User-Agent":      "GoAria-Test",
		},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	for name, want := range map[string]string{
		"Accept":          "application/json",
		"Accept-Language": "en-US",
		"Content-Type":    "application/json",
		"Referer":         "https://fixture.invalid/",
		"User-Agent":      "GoAria-Test",
	} {
		if got := seen.Get(name); got != want {
			t.Fatalf("header %s = %q, want %q", name, got, want)
		}
	}

	for _, tt := range []struct {
		name  string
		value string
	}{
		{name: "crlf", value: "good\r\nInjected: bad"},
		{name: "too long", value: strings.Repeat("a", 257)},
	} {
		t.Run("reject "+tt.name, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)

			_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:   "xpk-fixture01",
				Manifest: httpBrokerManifest(),
				Method:   http.MethodGet,
				URL:      "https://fixture.invalid/d/abc",
				Headers:  map[string]string{"Accept": tt.value},
			})
			if err == nil {
				t.Fatal("Fetch() error = nil, want error")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport invoked %d times, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerRejectsUnsafeURLsBeforeTransport(t *testing.T) {
	unsafeURLs := []string{
		"https://evil.test/path",
		"://bad-url",
		"https://user:pass@fixture.invalid/path",
		"ftp://fixture.invalid/path",
		"https://127.0.0.1/path",
		"https://[::1]/path",
	}

	for _, rawURL := range unsafeURLs {
		t.Run(rawURL, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)

			_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:   "xpk-fixture01",
				Manifest: httpBrokerManifest(),
				Method:   http.MethodGet,
				URL:      rawURL,
			})
			if err == nil {
				t.Fatal("Fetch() error = nil, want error")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport invoked %d times, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerRedirectPolicy(t *testing.T) {
	t.Run("allows same allowlist redirect", func(t *testing.T) {
		calls := 0
		broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return redirectResponse("https://api.fixture.invalid/next"), nil
			}
			if req.URL.String() != "https://api.fixture.invalid/next" {
				t.Fatalf("redirect request URL = %q", req.URL.String())
			}
			return textResponse(200, "done"), nil
		}), nil)

		resp, err := broker.Fetch(context.Background(), HTTPFetchRequest{
			PackID:   "xpk-fixture01",
			Manifest: httpBrokerManifest(),
			Method:   http.MethodGet,
			URL:      "https://fixture.invalid/start",
		})
		if err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		if calls != 2 || resp.FinalURL != "https://api.fixture.invalid/next" {
			t.Fatalf("calls=%d finalURL=%q, want 2 and redirect target", calls, resp.FinalURL)
		}
	})

	for _, location := range []string{"https://evil.test/next", "ftp://fixture.invalid/next"} {
		t.Run("reject "+location, func(t *testing.T) {
			calls := 0
			broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return redirectResponse(location), nil
			}), nil)

			_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:   "xpk-fixture01",
				Manifest: httpBrokerManifest(),
				Method:   http.MethodGet,
				URL:      "https://fixture.invalid/start",
			})
			if err == nil {
				t.Fatal("Fetch() error = nil, want error")
			}
			if calls != 1 {
				t.Fatalf("transport calls = %d, want only initial request", calls)
			}
		})
	}

	t.Run("rejects redirect loops over limit", func(t *testing.T) {
		calls := 0
		broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return redirectResponse("https://fixture.invalid/loop"), nil
		}), nil)

		_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
			PackID:   "xpk-fixture01",
			Manifest: httpBrokerManifest(),
			Method:   http.MethodGet,
			URL:      "https://fixture.invalid/start",
		})
		if err == nil {
			t.Fatal("Fetch() error = nil, want error")
		}
		if calls != testHTTPPolicy().RedirectLimit+1 {
			t.Fatalf("transport calls = %d, want limit+1", calls)
		}
	})
}

func TestHTTPBrokerAliasPolicyAllowsFetchAndRedirect(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}
	calls := 0
	broker := NewHTTPBroker(HTTPBrokerConfig{
		Policy:             testHTTPPolicy(),
		HostPolicyResolver: resolver,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return redirectResponse("https://files.alpha.test/final"), nil
			}
			if got := req.URL.String(); got != "https://files.alpha.test/final" {
				t.Fatalf("redirect target = %q", got)
			}
			return textResponse(http.StatusOK, "ok"), nil
		}),
	})

	resp, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:       pack.Manifest.PackID,
		Manifest:     pack.Manifest,
		PackIdentity: pack.Identity,
		Method:       http.MethodGet,
		URL:          "https://api.alpha.test/start",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if resp.FinalURL != "https://files.alpha.test/final" || calls != 2 {
		t.Fatalf("response final=%q calls=%d, want same-policy redirect", resp.FinalURL, calls)
	}
}

func TestHTTPBrokerAliasPolicyDenialsBeforeTransport(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	tests := []struct {
		name     string
		policy   ResolvedHostPolicy
		identity VerifiedPackIdentity
		url      string
	}{
		{name: "no resolver", identity: pack.Identity, url: "https://api.alpha.test/start"},
		{name: "identity mismatch", policy: syntheticHostPolicy(VerifiedPackIdentity{PackID: pack.Manifest.PackID, PackVersion: pack.Manifest.PackVersion}), identity: pack.Identity, url: "https://api.alpha.test/start"},
		{name: "ingress denied", policy: syntheticHostPolicy(pack.Identity), identity: pack.Identity, url: "https://share.alpha.test/start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &recordingTransport{}
			var resolver HostPolicyResolver
			if tt.policy.PolicyID != "" {
				resolver = &fakeHostPolicyResolver{policy: tt.policy}
			}
			broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
			_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:       pack.Manifest.PackID,
				Manifest:     pack.Manifest,
				PackIdentity: tt.identity,
				Method:       http.MethodGet,
				URL:          tt.url,
			})
			if err == nil {
				t.Fatal("Fetch() error = nil, want fail closed")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerAliasRedirectToDeniedPolicyStopsAfterFirstResponse(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	calls := 0
	broker := NewHTTPBroker(HTTPBrokerConfig{
		Policy:             testHTTPPolicy(),
		HostPolicyResolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)},
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return redirectResponse("https://other.alpha.test/final"), nil
		}),
	})

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:       pack.Manifest.PackID,
		Manifest:     pack.Manifest,
		PackIdentity: pack.Identity,
		Method:       http.MethodGet,
		URL:          "https://api.alpha.test/start",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want redirect denial")
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want initial response only", calls)
	}
}

func TestHTTPBrokerAuthInjectionUsesMaterializer(t *testing.T) {
	const resolverSecret = "resolver-token-value"
	const materializedSecret = "materialized-token-value"
	materializer := &recordingAuthMaterializer{material: MaterializedAuthSecret{
		HeaderName:      "Authorization",
		Kind:            AuthSecretKindBearer,
		RedactedDisplay: "safe bearer",
		headerValue:     "Bearer " + materializedSecret,
		sensitiveValues: []string{"Bearer " + materializedSecret, materializedSecret, "Bearer " + resolverSecret, resolverSecret},
	}}
	manifest := httpBrokerManifest()
	manifest.PackID = "xpk-alpha001"
	manifest.Domains = []DomainRule{{Host: "share.alpha.test"}}
	broker := NewHTTPBroker(HTTPBrokerConfig{
		Policy: testHTTPPolicy(),
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer "+materializedSecret {
				t.Fatal("Authorization values did not contain exactly the materialized bearer")
			}
			if got := req.Header.Get("Cookie"); got != "" {
				t.Fatal("Cookie header was not removed before transport")
			}

			return textResponse(http.StatusOK, "ok"), nil
		}),
		AuthResolver: fakeAuthResolver{secret: ResolvedAuthSecret{
			Kind:        AuthSecretKindBearer,
			HeaderName:  "Authorization",
			HeaderValue: "Bearer " + resolverSecret,
		}},
		AuthMaterializer: materializer,
	})

	resp, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        "xpk-alpha001",
		Manifest:      manifest,
		Method:        http.MethodGet,
		URL:           "https://share.alpha.test/d/abc",
		AuthProfileID: "default",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if materializer.Count() != 1 {
		t.Fatalf("materializer calls = %d, want 1", materializer.Count())
	}
	last := materializer.LastSecret()
	if last.HeaderValue != "Bearer "+resolverSecret || last.Kind != AuthSecretKindBearer {
		t.Fatal("materializer did not receive the resolver material")
	}
	responseText := resp.FinalURL + string(resp.Body) + resp.Headers.Get("Content-Type")
	assertNoForbiddenSubstrings(t, responseText, resolverSecret, "Bearer "+resolverSecret, materializedSecret, "Bearer "+materializedSecret)
}

func TestHTTPBrokerAliasRedirectRechecksAuthScopeBeforeSecondRequest(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	policy := syntheticHostPolicy(pack.Identity)
	policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: "alpha-secret", Domains: []DomainRule{{Host: "api.alpha.test"}}}}
	resolver := &recordingAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer alpha-secret"}}
	calls := 0
	broker := NewHTTPBroker(HTTPBrokerConfig{
		Policy:             testHTTPPolicy(),
		AuthResolver:       resolver,
		HostPolicyResolver: &fakeHostPolicyResolver{policy: policy},
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return redirectResponse("https://files.alpha.test/final"), nil
		}),
	})

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        pack.Manifest.PackID,
		Manifest:      pack.Manifest,
		PackIdentity:  pack.Identity,
		Method:        http.MethodGet,
		URL:           "https://api.alpha.test/start",
		AuthProfileID: "alpha-secret",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want redirected auth-scope denial")
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want only initial request", calls)
	}
	if resolver.Count() != 1 {
		t.Fatalf("auth resolver calls = %d, want only initial request auth resolution", resolver.Count())
	}
}

func TestHTTPBrokerAliasAuthScopeBeforeResolver(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	transport := &recordingTransport{}
	resolver := &recordingAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer alpha-secret"}}
	policy := syntheticHostPolicy(pack.Identity)
	policy.AuthProfiles = []HostPolicyAuthProfileScope{{ProfileID: "other-secret", Domains: []DomainRule{{Host: "api.alpha.test"}}}}
	broker := NewHTTPBroker(HTTPBrokerConfig{
		Policy:             testHTTPPolicy(),
		Transport:          transport,
		AuthResolver:       resolver,
		HostPolicyResolver: &fakeHostPolicyResolver{policy: policy},
	})

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        pack.Manifest.PackID,
		Manifest:      pack.Manifest,
		PackIdentity:  pack.Identity,
		Method:        http.MethodGet,
		URL:           "https://api.alpha.test/start",
		AuthProfileID: "alpha-secret",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want auth scope denial")
	}
	if resolver.Count() != 0 || transport.Count() != 0 {
		t.Fatalf("resolver calls=%d transport calls=%d, want 0/0", resolver.Count(), transport.Count())
	}
}

func TestHTTPBrokerAliasAuthAllowsStoreDomainCheckAfterPolicy(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	resolver := &recordingAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer alpha-secret"}}
	broker := NewHTTPBroker(HTTPBrokerConfig{
		Policy:             testHTTPPolicy(),
		AuthResolver:       resolver,
		HostPolicyResolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)},
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer alpha-secret" {
				t.Fatalf("Authorization = %q, want injected", got)
			}
			return textResponse(http.StatusOK, "ok"), nil
		}),
	})

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        pack.Manifest.PackID,
		Manifest:      pack.Manifest,
		PackIdentity:  pack.Identity,
		Method:        http.MethodGet,
		URL:           "https://api.alpha.test/start",
		AuthProfileID: "alpha-secret",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if resolver.Count() != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.Count())
	}
}

func TestHTTPBrokerBodyCapRedactsSecretValues(t *testing.T) {
	secret := "raw-bearer-secret"
	resolver := fakeAuthResolver{secret: ResolvedAuthSecret{
		HeaderName:      "Authorization",
		HeaderValue:     "Bearer " + secret,
		RedactedDisplay: "Bearer ra…et",
	}}
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("Authorization header = %q, want injected secret", got)
		}
		return textResponse(200, "too-large"), nil
	}), resolver)

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:           "xpk-fixture01",
		Manifest:         httpBrokerManifest(),
		Method:           http.MethodGet,
		URL:              "https://fixture.invalid/d/abc?token=query-secret",
		AuthProfileID:    "default",
		MaxResponseBytes: 3,
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want body cap error")
	}
	for _, forbidden := range []string{secret, "query-secret", "Bearer " + secret} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Fetch() error leaked %q: %v", forbidden, err)
		}
	}
}

func TestHTTPBrokerTimeoutCancelsSlowTransport(t *testing.T) {
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}), nil)

	start := time.Now()
	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:   "xpk-fixture01",
		Manifest: httpBrokerManifest(),
		Method:   http.MethodGet,
		URL:      "https://fixture.invalid/slow",
		Timeout:  5 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want timeout error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("Fetch() timeout took too long")
	}
}

func TestHTTPBrokerAuthProfileInjectionIsHostOnly(t *testing.T) {
	secret := "raw-token-value"
	resolver := fakeAuthResolver{secret: ResolvedAuthSecret{
		HeaderName:      "Authorization",
		HeaderValue:     "Bearer " + secret,
		RedactedDisplay: "Bearer ra…ue",
	}}
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("Authorization header = %q, want host-injected token", got)
		}
		resp := textResponse(200, "ok")
		resp.Header.Set("Set-Cookie", "session=server-cookie")
		return resp, nil
	}), resolver)

	resp, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        "xpk-fixture01",
		Manifest:      httpBrokerManifest(),
		Method:        http.MethodGet,
		URL:           "https://fixture.invalid/d/abc",
		AuthProfileID: "default",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if resp.Headers.Get("Authorization") != "" || resp.Headers.Get("Set-Cookie") != "" {
		t.Fatalf("response headers exposed secret material: %#v", resp.Headers)
	}
	if strings.Contains(string(resp.Body), secret) {
		t.Fatalf("response body unexpectedly contains secret: %q", resp.Body)
	}
}

func TestHTTPBrokerRejectsAuthenticatedBodySecretReflection(t *testing.T) {
	for _, tt := range []struct {
		name      string
		secret    ResolvedAuthSecret
		body      string
		forbidden []string
	}{
		{
			name:      "bearer header value",
			secret:    ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer raw-bearer-token"},
			body:      "echo Bearer raw-bearer-token",
			forbidden: []string{"raw-bearer-token", "Bearer raw-bearer-token"},
		},
		{
			name:      "bearer token without prefix",
			secret:    ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer raw-bearer-token"},
			body:      "echo raw-bearer-token",
			forbidden: []string{"raw-bearer-token"},
		},
		{
			name:      "cookie component value",
			secret:    ResolvedAuthSecret{HeaderName: "Cookie", HeaderValue: "sid=cookie-secret; refresh=second-secret"},
			body:      "echo second-secret",
			forbidden: []string{"sid=cookie-secret", "cookie-secret", "refresh=second-secret", "second-secret"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return textResponse(200, tt.body), nil
			}), fakeAuthResolver{secret: tt.secret})

			_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:        "xpk-fixture01",
				Manifest:      httpBrokerManifest(),
				Method:        http.MethodGet,
				URL:           "https://fixture.invalid/d/abc",
				AuthProfileID: "default",
			})
			if err == nil {
				t.Fatal("Fetch() error = nil, want reflected secret error")
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("Fetch() error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestHTTPBrokerRejectsAuthenticatedSafeHeaderSecretReflection(t *testing.T) {
	for _, tt := range []struct {
		name   string
		secret ResolvedAuthSecret
		header string
		value  string
	}{
		{
			name:   "bearer token in etag",
			secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer header-token"},
			header: "Etag",
			value:  `"header-token"`,
		},
		{
			name:   "cookie component in last modified",
			secret: ResolvedAuthSecret{HeaderName: "Cookie", HeaderValue: "sid=cookie-secret; refresh=second-secret"},
			header: "Last-Modified",
			value:  "second-secret",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
				resp := textResponse(200, "ok")
				resp.Header.Set(tt.header, tt.value)
				return resp, nil
			}), fakeAuthResolver{secret: tt.secret})

			_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
				PackID:        "xpk-fixture01",
				Manifest:      httpBrokerManifest(),
				Method:        http.MethodGet,
				URL:           "https://fixture.invalid/d/abc",
				AuthProfileID: "default",
			})
			if err == nil {
				t.Fatal("Fetch() error = nil, want reflected header secret error")
			}
			if strings.Contains(err.Error(), tt.value) {
				t.Fatalf("Fetch() error leaked reflected header value: %v", err)
			}
		})
	}
}

func TestHTTPBrokerAuthProfileRequiresCapabilityBeforeResolverOrTransport(t *testing.T) {
	manifest := httpBrokerManifest()
	manifest.Capabilities = []Capability{CapabilityHTTPFetch}
	transport := &recordingTransport{}
	resolver := &recordingAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer token"}}
	broker := testHTTPBroker(transport, resolver)

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        "xpk-fixture01",
		Manifest:      manifest,
		Method:        http.MethodGet,
		URL:           "https://fixture.invalid/d/abc",
		AuthProfileID: "default",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want missing auth capability error")
	}
	if resolver.Count() != 0 {
		t.Fatalf("resolver invoked %d times, want 0", resolver.Count())
	}
	if transport.Count() != 0 {
		t.Fatalf("transport invoked %d times, want 0", transport.Count())
	}
}

func TestHTTPBrokerRejectsAuthInjectionOverPlainHTTP(t *testing.T) {
	transport := &recordingTransport{}
	resolver := &recordingAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer token"}}
	broker := testHTTPBroker(transport, resolver)
	manifest := httpBrokerManifest()
	manifest.Domains = []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}}

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        "xpk-fixture01",
		Manifest:      manifest,
		Method:        http.MethodGet,
		URL:           "http://fixture.invalid/d/abc",
		AuthProfileID: "default",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want HTTPS requirement error")
	}
	if resolver.Count() != 0 {
		t.Fatalf("resolver invoked %d times, want 0", resolver.Count())
	}
	if transport.Count() != 0 {
		t.Fatalf("transport invoked %d times, want 0", transport.Count())
	}
}

func TestHTTPBrokerRejectsAuthenticatedRedirectDowngrade(t *testing.T) {
	secret := "raw-token"
	resolver := &recordingAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer " + secret}}
	calls := 0
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return redirectResponse("http://fixture.invalid/downgrade"), nil
	}), resolver)

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        "xpk-fixture01",
		Manifest:      httpBrokerManifest(),
		Method:        http.MethodGet,
		URL:           "https://fixture.invalid/start",
		AuthProfileID: "default",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want redirect downgrade error")
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want initial request only", calls)
	}
	if resolver.Count() != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.Count())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Fetch() leaked secret: %v", err)
	}
}

func TestHTTPBrokerRejectsUnsafeResolvedIPsAndBypassesProxy(t *testing.T) {
	resolver := fakeIPResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	dialed := false
	transport := newPrivateIPGuardedTransport(resolver, func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://fixture.invalid/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}

	_, err = transport.DialContext(req.Context(), "tcp", "fixture.invalid:443")
	if err == nil {
		t.Fatal("DialContext() error = nil, want unsafe IP error")
	}
	if dialed {
		t.Fatal("underlying dialer was invoked for unsafe IP")
	}
	if transport.Proxy != nil {
		t.Fatal("secure default transport should bypass environment proxies")
	}

	for _, ip := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.1.1", "0.0.0.0", "224.0.0.1", "::1", "fc00::1", "fe80::1"} {
		t.Run(ip, func(t *testing.T) {
			addr, ok := netip.ParseAddr(ip)
			if ok != nil {
				t.Fatalf("ParseAddr(%s) error = %v", ip, ok)
			}
			if isAllowedPublicIP(addr) {
				t.Fatalf("isAllowedPublicIP(%s) = true, want false", ip)
			}
		})
	}
}

func TestHTTPBrokerPublicIPAllowPolicySpecialUseRanges(t *testing.T) {
	denied := []string{
		"0.0.0.1",
		"10.0.0.1",
		"100.64.0.1",
		"100.127.255.254",
		"127.0.0.1",
		"169.254.1.1",
		"172.16.0.1",
		"192.0.0.1",
		"192.0.2.1",
		"192.168.1.1",
		"198.18.0.1",
		"198.19.255.254",
		"198.51.100.1",
		"203.0.113.1",
		"224.0.0.1",
		"240.0.0.1",
		"255.255.255.255",
		"::",
		"::1",
		"::ffff:192.168.1.1",
		"64:ff9b::1",
		"64:ff9b:1::1",
		"100::1",
		"2001::1",
		"2001:2::1",
		"2001:db8::1",
		"2002::1",
		"fc00::1",
		"fe80::1",
		"ff00::1",
	}
	for _, ip := range denied {
		t.Run("deny "+ip, func(t *testing.T) {
			addr, err := netip.ParseAddr(ip)
			if err != nil {
				t.Fatalf("ParseAddr(%s) error = %v", ip, err)
			}
			if isAllowedPublicIP(addr) {
				t.Fatalf("isAllowedPublicIP(%s) = true, want false", ip)
			}
		})
	}

	allowed := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, ip := range allowed {
		t.Run("allow "+ip, func(t *testing.T) {
			addr, err := netip.ParseAddr(ip)
			if err != nil {
				t.Fatalf("ParseAddr(%s) error = %v", ip, err)
			}
			if !isAllowedPublicIP(addr) {
				t.Fatalf("isAllowedPublicIP(%s) = false, want true", ip)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type recordingTransport struct {
	mu    sync.Mutex
	calls int
}

func (t *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls++

	return nil, errors.New("unexpected transport call")
}

func (t *recordingTransport) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.calls
}

type fakeAuthResolver struct {
	secret ResolvedAuthSecret
	err    error
}

type recordingAuthResolver struct {
	mu     sync.Mutex
	secret ResolvedAuthSecret
	err    error
	calls  int
}

type recordingAuthMaterializer struct {
	mu       sync.Mutex
	material MaterializedAuthSecret
	err      error
	secret   ResolvedAuthSecret
	calls    int
}

func (r *recordingAuthResolver) ResolveAuthProfile(context.Context, string, AuthProfileID, string) (ResolvedAuthSecret, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return ResolvedAuthSecret{}, r.err
	}

	return r.secret, nil
}

func (r *recordingAuthResolver) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls
}

func (m *recordingAuthMaterializer) MaterializeAuth(secret ResolvedAuthSecret) (MaterializedAuthSecret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.secret = secret
	if m.err != nil {
		return MaterializedAuthSecret{}, m.err
	}

	return m.material, nil
}

func (m *recordingAuthMaterializer) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.calls
}

func (m *recordingAuthMaterializer) LastSecret() ResolvedAuthSecret {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.secret
}

type fakeIPResolver struct {
	ips []net.IPAddr
	err error
}

func (r fakeIPResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	if r.err != nil {
		return nil, r.err
	}

	return r.ips, nil
}

func (r fakeAuthResolver) ResolveAuthProfile(context.Context, string, AuthProfileID, string) (ResolvedAuthSecret, error) {
	if r.err != nil {
		return ResolvedAuthSecret{}, r.err
	}

	return r.secret, nil
}

func testHTTPBroker(transport http.RoundTripper, resolver AuthProfileResolver) *HTTPBroker {
	return NewHTTPBroker(HTTPBrokerConfig{
		Policy:       testHTTPPolicy(),
		Transport:    transport,
		AuthResolver: resolver,
	})
}

func testHTTPPolicy() HTTPBrokerPolicy {
	policy := DefaultHTTPBrokerPolicy()
	policy.DefaultTimeout = 50 * time.Millisecond
	policy.MaxTimeout = 100 * time.Millisecond
	policy.MaxResponseBytes = 1024
	policy.MaxHeaderValueBytes = 256

	return policy
}

func httpBrokerManifest() Manifest {
	manifest := validCapabilityManifest()
	manifest.ResourceLimits.TimeoutMillis = 100
	manifest.ResourceLimits.MaxResponseBytes = 1024

	return manifest
}

func textResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header: http.Header{
			"Content-Type": []string{"text/plain"},
			"X-Secret":     []string{"must-not-return"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func redirectResponse(location string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusFound,
		Header: http.Header{
			"Location": []string{location},
		},
		Body: io.NopCloser(strings.NewReader("")),
	}
}
