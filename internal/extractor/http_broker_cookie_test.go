package extractor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPBrokerAttachesDomainCookieWithoutAuthProfile(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
	}})

	resp, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := seen.Get("Cookie"); got != "sid=browser-sid" {
		t.Fatalf("Cookie = %q, want sid=browser-sid", got)
	}
	if LastHTTPFetchStatus(ctx) != 200 {
		t.Fatalf("last status = %d, want 200", LastHTTPFetchStatus(ctx))
	}
}

func TestHTTPBrokerHostOnlyCookieDoesNotAttachToSubdomain(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: "alpha.test", Path: "/", Secure: true, HostOnly: true,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, ok := seen["Cookie"]; ok {
		t.Fatalf("Cookie header present: %q", seen.Get("Cookie"))
	}
}

func TestHTTPBrokerRedirectRefiltersCookies(t *testing.T) {
	var hops []string
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hops = append(hops, req.URL.Host+"|"+req.Header.Get("Cookie"))
		if req.URL.Host == "api.alpha.test" {
			return redirectResponse("https://files.other.test/item"), nil
		}
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(hops) != 2 {
		t.Fatalf("hops = %#v, want 2", hops)
	}
	if hops[0] != "api.alpha.test|sid=browser-sid" {
		t.Fatalf("first hop = %q", hops[0])
	}
	if hops[1] != "files.other.test|" {
		t.Fatalf("redirect hop leaked cookie: %q", hops[1])
	}
	if LastHTTPFetchStatus(ctx) != 200 {
		t.Fatalf("last status = %d, want final 200 not redirect", LastHTTPFetchStatus(ctx))
	}
}

func TestHTTPBrokerProfilePathDoesNotAttachContextCookies(t *testing.T) {
	var seen http.Header
	resolver := fakeAuthResolver{secret: ResolvedAuthSecret{
		HeaderName:  "Cookie",
		HeaderValue: "session=profile-token",
		Kind:        AuthSecretKindCookie,
	}}
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), resolver)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:        "xpk-alpha001",
		Manifest:      alphaCookieManifest(),
		Method:        http.MethodGet,
		URL:           "https://api.alpha.test/x",
		AuthProfileID: "alpha-secret",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := seen.Get("Cookie"); got != "session=profile-token" {
		t.Fatalf("Cookie = %q, want profile header not ctx sid", got)
	}
	if strings.Contains(seen.Get("Cookie"), "sid=") {
		t.Fatalf("ctx sid leaked onto profile fetch: %q", seen.Get("Cookie"))
	}
}

func TestHTTPBrokerEmptyCookieMatchOmitsHeader(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: "files.alpha.test", Path: "/", HostOnly: true,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, ok := seen["Cookie"]; ok {
		t.Fatalf("empty match still sent Cookie: %q", seen.Get("Cookie"))
	}
}

func TestHTTPBrokerInjectAuthFailureDoesNotAttachContextCookies(t *testing.T) {
	transport := &recordingTransport{}
	resolver := fakeAuthResolver{err: errors.New("injectAuth failed")}
	broker := testHTTPBroker(transport, resolver)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:        "xpk-alpha001",
		Manifest:      alphaCookieManifest(),
		Method:        http.MethodGet,
		URL:           "https://api.alpha.test/x",
		AuthProfileID: "alpha-secret",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want injectAuth failure")
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestHTTPBrokerCookieValueIsRedactedFromFetchError(t *testing.T) {
	const secret = "browser-sid-secret"
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed carrying " + secret)
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: secret, Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Fetch() leaked cookie value: %v", err)
	}
	if LastHTTPFetchStatus(ctx) != 0 {
		t.Fatalf("network error must not record last status, got %d", LastHTTPFetchStatus(ctx))
	}
}

func TestRunnerExtractResetsLastHTTPFetchStatus(t *testing.T) {
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return textResponse(401, "denied"), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "session", Value: "browser-session", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
	}})
	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if LastHTTPFetchStatus(ctx) != 401 {
		t.Fatalf("last status = %d, want 401", LastHTTPFetchStatus(ctx))
	}

	_, extractErr := NewRunner().Extract(ctx, VerifiedPack{}, ExtractInput{})
	if extractErr == nil {
		t.Fatal("Extract() error = nil, want validation error")
	}
	if LastHTTPFetchStatus(ctx) != 0 {
		t.Fatalf("Extract must reset last status, got %d", LastHTTPFetchStatus(ctx))
	}
}

func TestHTTPBrokerBackgroundContextDoesNotAttachCookies(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, ok := seen["Cookie"]; ok {
		t.Fatalf("background ctx attached Cookie: %q", seen.Get("Cookie"))
	}
}

func TestHTTPBrokerCookiePathRejectsSecretReflection(t *testing.T) {
	const secret = "browser-sid-secret"
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return textResponse(200, `{"token":"`+secret+`"}`), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: secret, Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want reflected secret rejection")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Fetch() leaked cookie value: %v", err)
	}
}

func TestHTTPBrokerShortCookieValuesDoNotTripReflection(t *testing.T) {
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return textResponse(200, `{"ok":true,"consent":1,"consent=1":true}`), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{
		{Name: "lang", Value: "en", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false},
		{Name: "consent", Value: "1", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false},
	})

	resp, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestHTTPBrokerProfilePathRejectsShortSecretReflection(t *testing.T) {
	const secret = "abcd"
	resolver := fakeAuthResolver{secret: ResolvedAuthSecret{
		HeaderName:  "Cookie",
		HeaderValue: "session=" + secret,
		Kind:        AuthSecretKindCookie,
	}}
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return textResponse(200, `{"echo":"`+secret+`"}`), nil
	}), resolver)

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:        "xpk-alpha001",
		Manifest:      alphaCookieManifest(),
		Method:        http.MethodGet,
		URL:           "https://api.alpha.test/x",
		AuthProfileID: "alpha-secret",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want short profile secret reflection rejection")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Fetch() leaked profile secret: %v", err)
	}
}

func TestHTTPBrokerCookiePathRequiresHTTPS(t *testing.T) {
	transport := &recordingTransport{}
	broker := testHTTPBroker(transport, nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", HostOnly: false,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "http://api.alpha.test/x",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want HTTPS requirement")
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestHTTPBrokerCookiePathDeniesHTTPSToHTTPRedirect(t *testing.T) {
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme == "https" {
			return redirectResponse("http://api.alpha.test/x"), nil
		}
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", HostOnly: false,
	}})

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "https://api.alpha.test/x",
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want HTTPS→HTTP redirect denial")
	}
}

func TestHTTPBrokerUnrelatedHTTPHopAllowedWithCtxCookies(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", HostOnly: false,
	}})

	resp, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      "http://cdn.other.test/file",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if _, ok := seen["Cookie"]; ok {
		t.Fatalf("unrelated hop attached Cookie: %q", seen.Get("Cookie"))
	}
}

func alphaCookieManifest() Manifest {
	return Manifest{
		PackID:       "xpk-alpha001",
		PackVersion:  "opaque-1",
		ABIVersion:   CurrentABIVersion,
		Capabilities: []Capability{CapabilityHTTPFetch, CapabilityAuthProfile},
		Domains: []DomainRule{
			{Host: "alpha.test", IncludeSubdomains: true},
			{Host: "other.test", IncludeSubdomains: true},
		},
		ResourceLimits: ResourceLimits{
			TimeoutMillis:    100,
			MaxMemoryPages:   64,
			MaxHostCalls:     16,
			MaxResponseBytes: 1024,
			MaxOutputItems:   100,
			MaxOutputBytes:   1 << 16,
		},
		PayloadSHA256: strings.Repeat("0123456789abcdef", 4),
	}
}
