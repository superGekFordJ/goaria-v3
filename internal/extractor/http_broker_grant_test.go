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
		t.Fatal("Authorization header mismatch")
	}
	if got.Get("X-Fixture-Token") != grantSecretB {
		t.Fatal("X-Fixture-Token header mismatch")
	}
	if _, ok := got["Cookie"]; ok {
		t.Fatal("grant hop must not carry browser Cookie")
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
		t.Fatal("GET request must not inject HEAD grant")
	}
	if seen.Get("Cookie") != "sid=browser-sid" {
		t.Fatal("non-matching grant must leave cookie path intact")
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
		t.Fatal("expired grant must not inject")
	}
	if seen.Get("Cookie") != "sid=browser-sid" {
		t.Fatal("expired grant must leave cookie path intact")
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
		t.Fatal("non-hit hop must keep cookie path")
	}
	if seen[0].Get("X-Fixture-Token") != "" {
		t.Fatal("non-hit hop must not inject grant headers")
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

func TestHTTPBrokerGrantHitUsesCanonicalTargetOnWire(t *testing.T) {
	// The wire request must carry the canonical grant target, not the raw
	// request spelling (host case, default port, bare query marker, fragment).
	for _, variant := range []string{
		"https://API.ALPHA.TEST/x",
		"https://api.alpha.test:443/x",
		"https://api.alpha.test/x?",
		"https://api.alpha.test/x#frag",
	} {
		t.Run(variant, func(t *testing.T) {
			var seenURL string
			var seen http.Header
			broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seenURL = req.URL.String()
				seen = req.Header.Clone()
				return textResponse(200, "ok"), nil
			}), nil)
			ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
				BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
			)))
			req := grantFetchRequest()
			req.URL = variant

			_, err := broker.Fetch(ctx, req)
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if seenURL != grantBrokerTarget {
				t.Fatalf("wire URL = %q, want canonical grant target %q", seenURL, grantBrokerTarget)
			}
			if seen.Get("X-Fixture-Token") != grantSecretB {
				t.Fatal("grant header must be injected on canonical match")
			}
		})
	}
}

func TestHTTPBrokerGrantHeadMethodHitInjects(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "HEAD",
		BrowserHeader{Name: "authorization", Value: "Bearer " + grantSecretA},
	)))
	req := grantFetchRequest()
	req.Method = http.MethodHead

	_, err := broker.Fetch(ctx, req)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if seen.Get("Authorization") != "Bearer "+grantSecretA {
		t.Fatal("HEAD grant must inject on HEAD request")
	}
	if _, ok := seen["Cookie"]; ok {
		t.Fatal("grant hop must suppress Cookie")
	}
}

func TestHTTPBrokerGrantRejectsPackHeaderCollision(t *testing.T) {
	transport := &recordingTransport{}
	policy := testHTTPPolicy()
	// x-fixture-flag survives validatePackHeaders (no secret-name substring, not
	// forbidden, allowlisted here) so the grant collision check is what fires.
	policy.AllowedRequestHeaders["X-Fixture-Flag"] = struct{}{}
	broker := NewHTTPBroker(HTTPBrokerConfig{Policy: policy, Transport: transport})
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-fixture-flag", Value: grantSecretB},
	)))
	req := grantFetchRequest()
	req.Headers = map[string]string{"x-fixture-flag": "pack-owned"}

	_, err := broker.Fetch(ctx, req)
	if err == nil {
		t.Fatal("Fetch() error = nil, want grant/pack header collision rejection")
	}
	if err.Error() != "grant-scoped header collides with pack header" {
		t.Fatalf("error = %q, want the collision check, not earlier header validation", err.Error())
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0 (reject before transport)", transport.Count())
	}
}

func TestHTTPBrokerGrantReflectionChecksCookieSecretsAcrossHops(t *testing.T) {
	// Hop 1 is not grant-scoped and attaches the browser cookie; it redirects
	// to the grant target. The hop-2 response echoing the earlier cookie value
	// must still be rejected — the grant hop's reflection set is a union.
	calls := 0
	broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return redirectResponse(grantBrokerTarget), nil
		}
		return textResponse(200, `{"debug":"sid=browser-sid"}`), nil
	}), nil)
	ctx := WithBrowserContext(t.Context(), grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	)))
	req := grantFetchRequest()
	req.URL = "https://api.alpha.test/start"

	_, err := broker.Fetch(ctx, req)
	if err == nil {
		t.Fatal("Fetch() error = nil, want reflected cookie secret rejection on grant hop")
	}
	if calls != 2 {
		t.Fatalf("transport calls = %d, want 2", calls)
	}
	if strings.Contains(err.Error(), "browser-sid") {
		t.Fatalf("Fetch() leaked cookie value: %v", err)
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
		t.Fatal("non-scoped first hop keeps cookie path")
	}
	if hops[1].Get("Authorization") != "Bearer "+grantSecretA {
		t.Fatal("scoped second hop must inject grant")
	}
	if _, ok := hops[1]["Cookie"]; ok {
		t.Fatal("scoped second hop must suppress Cookie")
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
