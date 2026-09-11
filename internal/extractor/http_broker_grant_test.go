package extractor

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	grantBrokerTarget = "https://api.alpha.test/x"
	grantSecretA      = "fixture-grant-secret-a"
	grantSecretB      = "fixture-x-token-b"
)

func liveBrokerGrant(target, method string, headers ...BrowserHeader) BrowserHeaderGrant {
	now := time.Now()
	return BrowserHeaderGrant{
		SourceOrigin:     "https://share.fixture.invalid",
		TargetURL:        target,
		Method:           method,
		CapturedAtUnixMs: now.Add(-5 * time.Second).UnixMilli(),
		ExpiresAtUnixMs:  now.Add(30 * time.Second).UnixMilli(),
		Headers:          headers,
	}
}

func grantBrokerContext(grant BrowserHeaderGrant) BrowserRequestContext {
	return BrowserRequestContext{
		Cookies: []SessionCookie{{
			Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
		}},
		UserAgent:      "fixture-ua",
		AcceptLanguage: "en-US",
		RefererOrigin:  "https://share.fixture.invalid",
		Grants:         []BrowserHeaderGrant{grant},
	}
}

func grantFetchRequest() HTTPFetchRequest {
	return HTTPFetchRequest{
		PackID:   "xpk-alpha001",
		Manifest: alphaCookieManifest(),
		Method:   http.MethodGet,
		URL:      grantBrokerTarget,
	}
}

func TestHTTPBrokerGrantHitInjectsScopedHeaders(t *testing.T) {
	var seen []http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.Header.Clone())
		return textResponse(200, "ok"), nil
	}), nil)
	bc := grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "authorization", Value: "Bearer " + grantSecretA},
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	))
	ctx := WithBrowserContext(t.Context(), bc)

	resp, err := broker.Fetch(ctx, grantFetchRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(seen) != 1 {
		t.Fatalf("transport calls = %d, want 1", len(seen))
	}
	got := seen[0]
	if got.Get("Authorization") != "Bearer "+grantSecretA {
		t.Fatalf("Authorization = %q, want grant value", got.Get("Authorization"))
	}
	if got.Get("X-Fixture-Token") != grantSecretB {
		t.Fatalf("X-Fixture-Token = %q, want grant value", got.Get("X-Fixture-Token"))
	}
	if _, ok := got["Cookie"]; ok {
		t.Fatalf("grant hop must not carry browser Cookie: %q", got.Get("Cookie"))
	}
}

func TestHTTPBrokerGrantAppliesTypedFields(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	)))
	req := grantFetchRequest()
	req.Headers = map[string]string{"User-Agent": "pack-ua"}

	_, err := broker.Fetch(ctx, req)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if seen.Get("User-Agent") != "fixture-ua" {
		t.Fatalf("User-Agent = %q, want browser typed field overriding pack header", seen.Get("User-Agent"))
	}
	if seen.Get("Accept-Language") != "en-US" {
		t.Fatalf("Accept-Language = %q, want en-US", seen.Get("Accept-Language"))
	}
	if seen.Get("Referer") != "https://share.fixture.invalid/" {
		t.Fatalf("Referer = %q, want source origin with trailing slash", seen.Get("Referer"))
	}
}

func TestHTTPBrokerGrantTypedFieldsAbsentKeepPackValues(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	bc := BrowserRequestContext{Grants: []BrowserHeaderGrant{liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	)}}
	ctx := WithBrowserContext(t.Context(), bc)
	req := grantFetchRequest()
	req.Headers = map[string]string{"User-Agent": "pack-ua"}

	_, err := broker.Fetch(ctx, req)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if seen.Get("User-Agent") != "pack-ua" {
		t.Fatalf("User-Agent = %q, want pack value preserved", seen.Get("User-Agent"))
	}
	if _, ok := seen["Referer"]; ok {
		t.Fatalf("Referer must not be invented: %q", seen.Get("Referer"))
	}
}

func TestHTTPBrokerGrantMethodMismatchLeavesBasicPath(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "HEAD",
		BrowserHeader{Name: "authorization", Value: "Bearer " + grantSecretA},
	)))
	req := grantFetchRequest()

	_, err := broker.Fetch(ctx, req)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, ok := seen["Authorization"]; ok {
		t.Fatalf("GET request must not inject HEAD grant: %q", seen.Get("Authorization"))
	}
	if seen.Get("Cookie") != "sid=browser-sid" {
		t.Fatalf("non-matching grant must leave cookie path intact, Cookie = %q", seen.Get("Cookie"))
	}
}

func TestHTTPBrokerGrantExpiredLeavesBasicPath(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "authorization", Value: "Bearer " + grantSecretA},
	)))
	expired := browserContextFromContext(ctx)
	expired.Grants[0].ExpiresAtUnixMs = time.Now().Add(-time.Second).UnixMilli()
	ctx = WithBrowserContext(t.Context(), expired)

	_, err := broker.Fetch(ctx, grantFetchRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if _, ok := seen["Authorization"]; ok {
		t.Fatalf("expired grant must not inject: %q", seen.Get("Authorization"))
	}
	if seen.Get("Cookie") != "sid=browser-sid" {
		t.Fatalf("expired grant must leave cookie path intact, Cookie = %q", seen.Get("Cookie"))
	}
}

func TestHTTPBrokerGrantSuppressesCookiesOnlyOnHit(t *testing.T) {
	var seen []http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.Header.Clone())
		return textResponse(200, "ok"), nil
	}), nil)
	// Grant targets a different URL: request URL keeps the plain cookie path.
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant("https://api.alpha.test/other", "GET",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	)))

	_, err := broker.Fetch(ctx, grantFetchRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("transport calls = %d, want 1", len(seen))
	}
	if seen[0].Get("Cookie") != "sid=browser-sid" {
		t.Fatalf("non-hit hop must keep cookie path, Cookie = %q", seen[0].Get("Cookie"))
	}
	if seen[0].Get("X-Fixture-Token") != "" {
		t.Fatalf("non-hit hop must not inject grant headers: %q", seen[0].Get("X-Fixture-Token"))
	}
}

func TestHTTPBrokerGrantRejectsAuthProfileCombination(t *testing.T) {
	transport := &recordingTransport{}
	broker := testHTTPBroker(transport, fakeAuthResolver{secret: ResolvedAuthSecret{
		HeaderName: "Authorization", HeaderValue: "Bearer profile-secret", Kind: AuthSecretKindBearer,
	}})
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	)))
	req := grantFetchRequest()
	req.AuthProfileID = "alpha-secret"

	_, err := broker.Fetch(ctx, req)
	if err == nil {
		t.Fatal("Fetch() error = nil, want grant+auth_profile rejection")
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0 (reject before transport)", transport.Count())
	}
}

func TestHTTPBrokerGrantRedirectFailsClosed(t *testing.T) {
	calls := 0
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return redirectResponse("https://api.alpha.test/next"), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	)))

	_, err := broker.Fetch(ctx, grantFetchRequest())
	if err == nil {
		t.Fatal("Fetch() error = nil, want grant-scoped redirect rejection")
	}
	if err.Error() != "grant-scoped request must not redirect" {
		t.Fatalf("error = %q, want static grant redirect rejection", err.Error())
	}
	if calls != 1 {
		t.Fatalf("transport calls = %d, want exactly 1 (fail closed before any next hop)", calls)
	}
}

func TestHTTPBrokerGrantRedirectDoesNotLeakToNextHop(t *testing.T) {
	var hops []http.Header
	calls := 0
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		hops = append(hops, req.Header.Clone())
		if calls == 1 {
			return redirectResponse(grantBrokerTarget), nil
		}
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "authorization", Value: "Bearer " + grantSecretA},
	)))

	// First hop is NOT the grant target; redirect lands on the grant target.
	req := grantFetchRequest()
	req.URL = "https://api.alpha.test/start"
	_, err := broker.Fetch(ctx, req)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(hops) != 2 {
		t.Fatalf("hops = %d, want 2", len(hops))
	}
	if hops[0].Get("Authorization") != "" {
		t.Fatal("non-scoped first hop must not carry grant headers")
	}
	if hops[0].Get("Cookie") != "sid=browser-sid" {
		t.Fatalf("non-scoped first hop keeps cookie path, Cookie = %q", hops[0].Get("Cookie"))
	}
	if hops[1].Get("Authorization") != "Bearer "+grantSecretA {
		t.Fatalf("scoped second hop must inject grant, Authorization = %q", hops[1].Get("Authorization"))
	}
	if _, ok := hops[1]["Cookie"]; ok {
		t.Fatalf("scoped second hop must suppress Cookie: %q", hops[1].Get("Cookie"))
	}
}

func TestHTTPBrokerGrantValueRedactedFromTransportError(t *testing.T) {
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed carrying " + grantSecretA)
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "authorization", Value: "Bearer " + grantSecretA},
	)))

	_, err := broker.Fetch(ctx, grantFetchRequest())
	if err == nil {
		t.Fatal("Fetch() error = nil, want transport error")
	}
	if strings.Contains(err.Error(), grantSecretA) {
		t.Fatalf("Fetch() leaked grant value: %v", err)
	}
}

func TestHTTPBrokerGrantCredentialsPartRedactedFromError(t *testing.T) {
	const credentials = "fixture-credentials-part"
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed carrying " + credentials)
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "authorization", Value: "Bearer " + credentials},
	)))

	_, err := broker.Fetch(ctx, grantFetchRequest())
	if err == nil {
		t.Fatal("Fetch() error = nil, want transport error")
	}
	if strings.Contains(err.Error(), credentials) {
		t.Fatalf("Fetch() leaked authorization credentials part: %v", err)
	}
}

func TestHTTPBrokerGrantReflectionRejectsEchoedValue(t *testing.T) {
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return textResponse(200, `{"echo":"`+grantSecretB+`"}`), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	)))

	_, err := broker.Fetch(ctx, grantFetchRequest())
	if err == nil {
		t.Fatal("Fetch() error = nil, want reflected secret rejection")
	}
	if strings.Contains(err.Error(), grantSecretB) {
		t.Fatalf("Fetch() leaked grant value: %v", err)
	}
}

func TestHTTPBrokerGrantReflectionRejectsEchoedSafeHeader(t *testing.T) {
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		resp := textResponse(200, "ok")
		// Content-Type is on the safe response-header allowlist.
		resp.Header.Set("Content-Type", "application/json; token=Bearer "+grantSecretA)
		return resp, nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "authorization", Value: "Bearer " + grantSecretA},
	)))

	_, err := broker.Fetch(ctx, grantFetchRequest())
	if err == nil {
		t.Fatal("Fetch() error = nil, want reflected secret rejection on safe response header")
	}
	if strings.Contains(err.Error(), grantSecretA) {
		t.Fatalf("Fetch() leaked grant value: %v", err)
	}
}

func TestHTTPBrokerGrantShortValueSkipsReflection(t *testing.T) {
	const short = "x1y2z3" // 6 bytes < 8
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return textResponse(200, `{"note":"`+short+`"}`), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-short", Value: short},
	)))

	resp, err := broker.Fetch(ctx, grantFetchRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v, short grant values must not trip reflection", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
