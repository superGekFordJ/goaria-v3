package extractor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func extendedBrokerManifest() Manifest {
	manifest := httpBrokerManifest()
	manifest.Capabilities = append(manifest.Capabilities, CapabilityHTTPFetchExtended)

	return manifest
}

func extendedFetchRequest() HTTPFetchRequest {
	return HTTPFetchRequest{
		PackID:   "xpk-fixture01",
		Manifest: extendedBrokerManifest(),
		Method:   http.MethodPost,
		URL:      "https://api.fixture.invalid/submit",
	}
}

func TestHTTPBrokerExtendedGETAndHEADMatchBasicBehavior(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			var seenMethod string
			var seen http.Header
			broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seenMethod = req.Method
				seen = req.Header.Clone()
				return textResponse(200, "ok"), nil
			}), nil)
			req := extendedFetchRequest()
			req.Method = method
			req.Headers = map[string]string{
				"Accept":     "application/json",
				"User-Agent": "GoAria-Test",
			}

			resp, err := broker.Fetch(context.Background(), req)
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if seenMethod != method {
				t.Fatalf("transport method = %q, want %q", seenMethod, method)
			}
			if seen.Get("Accept") != "application/json" {
				t.Fatalf("Accept header = %q", seen.Get("Accept"))
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
		})
	}
}

func TestHTTPBrokerExtendedPOSTCarriesBodyAndContentType(t *testing.T) {
	var seenMethod, seenCT string
	var seenBody []byte
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seenMethod = req.Method
		seenCT = req.Header.Get("Content-Type")
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("io.ReadAll(request body) error = %v", err)
		}
		seenBody = body
		return textResponse(200, "ok"), nil
	}), nil)
	req := extendedFetchRequest()
	req.Body = []byte(`{"k":"v"}`)
	req.Headers = map[string]string{"Content-Type": "application/json"}

	if _, err := broker.Fetch(context.Background(), req); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if seenMethod != http.MethodPost {
		t.Fatalf("transport method = %q, want POST", seenMethod)
	}
	if string(seenBody) != `{"k":"v"}` {
		t.Fatalf("transport body = %q", seenBody)
	}
	if seenCT != "application/json" {
		t.Fatalf("Content-Type = %q", seenCT)
	}
}

func TestHTTPBrokerRejectsExtendedFeaturesWithoutCapability(t *testing.T) {
	tests := []struct {
		name    string
		request HTTPFetchRequest
	}{
		{name: "post", request: HTTPFetchRequest{Method: http.MethodPost}},
		{name: "body", request: HTTPFetchRequest{Body: []byte("x")}},
		{name: "authorization header", request: HTTPFetchRequest{Headers: map[string]string{"Authorization": "Bearer x"}}},
		{name: "x- business header", request: HTTPFetchRequest{Headers: map[string]string{"X-Foo": "x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)
			request := tt.request
			request.PackID = "xpk-fixture01"
			request.Manifest = httpBrokerManifest()
			request.URL = "https://api.fixture.invalid/submit"
			if request.Method == "" {
				request.Method = http.MethodGet
			}

			_, err := broker.Fetch(context.Background(), request)
			if err == nil {
				t.Fatal("Fetch() error = nil, want missing-extended rejection")
			}
			if !strings.Contains(err.Error(), "extended") {
				t.Fatalf("Fetch() error = %q, want missing extended capability", err.Error())
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerRejectsMethodsOutsideVocabularyWithExtended(t *testing.T) {
	for _, method := range []string{"OPTIONS", http.MethodPut, "PATCH", http.MethodDelete, "CONNECT", "TRACE", "BREW"} {
		t.Run(method, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)
			req := extendedFetchRequest()
			req.Method = method

			_, err := broker.Fetch(context.Background(), req)
			if err == nil {
				t.Fatal("Fetch() error = nil, want method rejection")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerExtendedNormalizesPostSpelling(t *testing.T) {
	for _, method := range []string{" post ", "pOsT"} {
		t.Run(method, func(t *testing.T) {
			var seenMethod string
			broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seenMethod = req.Method
				return textResponse(200, "ok"), nil
			}), nil)
			req := extendedFetchRequest()
			req.Method = method

			if _, err := broker.Fetch(context.Background(), req); err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if seenMethod != http.MethodPost {
				t.Fatalf("transport method = %q, want POST", seenMethod)
			}
		})
	}
}

func TestHTTPBrokerExtendedAuthorizationReachesWireAndStaysSecret(t *testing.T) {
	const sentinel = "fixture-auth-value"
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	req := extendedFetchRequest()
	req.Method = http.MethodGet
	req.Headers = map[string]string{"Authorization": "Bearer " + sentinel}

	if _, err := broker.Fetch(context.Background(), req); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := seen.Get("Authorization"); got != "Bearer "+sentinel {
		t.Fatalf("Authorization header = %q", got)
	}

	// A transport error echoing the pack-owned value must be redacted, since
	// the extended hop registers it into the known-secrets set.
	leaking := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed carrying " + sentinel)
	}), nil)
	if _, err := leaking.Fetch(context.Background(), req); err == nil {
		t.Fatal("Fetch() error = nil, want transport error")
	} else if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("Fetch() leaked pack-owned authorization value: %v", err)
	}
}

func TestHTTPBrokerExtendedBusinessXHeadersReachWire(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	req := extendedFetchRequest()
	req.Method = http.MethodGet
	req.Headers = map[string]string{
		"X-Website-Token": "fixture-website-token",
		"X-Api-Key":       "fixture-api-key",
		"X-Foo":           "fixture-foo",
	}

	if _, err := broker.Fetch(context.Background(), req); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	for name, want := range map[string]string{
		"X-Website-Token": "fixture-website-token",
		"X-Api-Key":       "fixture-api-key",
		"X-Foo":           "fixture-foo",
	} {
		if got := seen.Get(name); got != want {
			t.Fatalf("header %s = %q, want %q", name, got, want)
		}
	}
}

func TestHTTPBrokerExtendedRejectsDeniedXHeaderNames(t *testing.T) {
	for _, name := range []string{
		"X-Real-Ip", "X-Forwarded-For", "X-Forwarded-Host", "X-Goaria-Debug",
		"X-Http-Method-Override", "X-Method-Override", "X-Original-Url",
		"X-Proxy-Url", "X-Override-Foo", "X-Rewrite-Url", "X-Host", "X-Client-Ip",
	} {
		t.Run(name, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)
			req := extendedFetchRequest()
			req.Method = http.MethodGet
			req.Headers = map[string]string{name: "fixture-value"}

			_, err := broker.Fetch(context.Background(), req)
			if err == nil {
				t.Fatal("Fetch() error = nil, want denied-name rejection")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerExtendedStillRejectsAmbientAndHopHeaders(t *testing.T) {
	for _, name := range []string{
		"Cookie", "Set-Cookie", "Host", "Content-Length", "Transfer-Encoding",
		"Connection", "Proxy-Authorization", "Origin", "Sec-Fetch-Site", "Keep-Alive",
		"TE", "Trailer", "Upgrade", "Proxy-Authenticate", "Foo-Bar", "Expect",
	} {
		t.Run(name, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)
			req := extendedFetchRequest()
			req.Method = http.MethodGet
			req.Headers = map[string]string{name: "fixture-value"}

			_, err := broker.Fetch(context.Background(), req)
			if err == nil {
				t.Fatal("Fetch() error = nil, want rejection")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerExtendedRejectsMalformedAuthorizationValues(t *testing.T) {
	for _, value := range []string{
		"bare-token", "Bearer", "Bearer ", " Bearer x", "Bearer  x", "Bearer x ",
		"Bearer x\ty", "Bearer x\x7f", "\tBearer x",
	} {
		t.Run(value, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)
			req := extendedFetchRequest()
			req.Method = http.MethodGet
			req.Headers = map[string]string{"Authorization": value}

			_, err := broker.Fetch(context.Background(), req)
			if err == nil {
				t.Fatal("Fetch() error = nil, want malformed authorization rejection")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerExtendedAllowsNonAmbientAuthorizationSchemes(t *testing.T) {
	for _, value := range []string{"Bearer tok", "Basic dXNlcjpwYXNz", "ApiKey abc123", "Digest xyz"} {
		t.Run(value, func(t *testing.T) {
			var seen http.Header
			broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seen = req.Header.Clone()
				return textResponse(200, "ok"), nil
			}), nil)
			req := extendedFetchRequest()
			req.Method = http.MethodGet
			req.Headers = map[string]string{"Authorization": value}

			if _, err := broker.Fetch(context.Background(), req); err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if seen.Get("Authorization") != value {
				t.Fatalf("Authorization = %q, want %q", seen.Get("Authorization"), value)
			}
		})
	}
}

func TestHTTPBrokerExtendedRejectsAmbiguousOrMalformedHeaderNames(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "canonical case duplicate", headers: map[string]string{"authorization": "Bearer a", "Authorization": "Bearer b"}},
		{name: "canonical x- duplicate", headers: map[string]string{"x-foo": "a", "X-Foo": "b"}},
		{name: "name with space", headers: map[string]string{"Bad Name": "v"}},
		{name: "name with at sign", headers: map[string]string{"X@Y": "v"}},
		{name: "name with colon", headers: map[string]string{"X:Y": "v"}},
		{name: "non ascii name", headers: map[string]string{"X-\u00e9": "v"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &recordingTransport{}
			broker := testHTTPBroker(transport, nil)
			req := extendedFetchRequest()
			req.Method = http.MethodGet
			req.Headers = tt.headers

			_, err := broker.Fetch(context.Background(), req)
			if err == nil {
				t.Fatal("Fetch() error = nil, want rejection")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerExtendedKeepsHeaderLimits(t *testing.T) {
	t.Run("header count", func(t *testing.T) {
		transport := &recordingTransport{}
		broker := testHTTPBroker(transport, nil)
		req := extendedFetchRequest()
		req.Method = http.MethodGet
		req.Headers = map[string]string{"Accept": "a"}
		for i := range 16 {
			req.Headers["X-Fill-"+strings.Repeat("x", i+1)] = "v"
		}

		if _, err := broker.Fetch(context.Background(), req); err == nil {
			t.Fatal("Fetch() error = nil, want header count rejection")
		}
		if transport.Count() != 0 {
			t.Fatalf("transport calls = %d, want 0", transport.Count())
		}
	})

	t.Run("header value length", func(t *testing.T) {
		transport := &recordingTransport{}
		broker := testHTTPBroker(transport, nil)
		req := extendedFetchRequest()
		req.Method = http.MethodGet
		req.Headers = map[string]string{"X-Foo": strings.Repeat("a", 257)}

		if _, err := broker.Fetch(context.Background(), req); err == nil {
			t.Fatal("Fetch() error = nil, want header value length rejection")
		}
		if transport.Count() != 0 {
			t.Fatalf("transport calls = %d, want 0", transport.Count())
		}
	})
}

func TestHTTPBrokerExtendedSuppressesBrowserCookies(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "fixture-browser-sid", Domain: ".fixture.invalid", Path: "/", Secure: true, HostOnly: false,
	}})
	req := extendedFetchRequest()
	req.Method = http.MethodGet
	req.Headers = map[string]string{"X-Foo": "fixture-foo"}

	if _, err := broker.Fetch(ctx, req); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := seen.Get("Cookie"); got != "" {
		t.Fatalf("extended request carried browser Cookie %q", got)
	}
	if _, ok := seen["Cookie"]; ok {
		t.Fatal("extended request must not carry a Cookie header")
	}
}

func TestHTTPBrokerExtendedRejectsMatchingGrant(t *testing.T) {
	// A matching grant for an extended request can only be constructed for
	// GET/HEAD on the wire; the broker must still fail closed even when a
	// same-method scope collides with pack-owned extended features.
	transport := &recordingTransport{}
	broker := testHTTPBroker(transport, nil)
	ctx := WithBrowserContext(context.Background(), grantBrokerContext(liveBrokerGrant("https://api.fixture.invalid/submit", "POST",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	)))
	req := extendedFetchRequest()
	req.Body = []byte(`{"k":"v"}`)
	req.Headers = map[string]string{"Content-Type": "application/json"}

	_, err := broker.Fetch(ctx, req)
	if err == nil {
		t.Fatal("Fetch() error = nil, want grant/extended mutex rejection")
	}
	if err.Error() != "grant-scoped request must not use extended fetch features" {
		t.Fatalf("error = %q, want static grant/extended mutex rejection", err.Error())
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestHTTPBrokerExtendedRejectsAuthProfileCombination(t *testing.T) {
	transport := &recordingTransport{}
	broker := testHTTPBroker(transport, fakeAuthResolver{secret: ResolvedAuthSecret{
		HeaderName: "Authorization", HeaderValue: "Bearer profile-secret", Kind: AuthSecretKindBearer,
	}})
	for _, tt := range []struct {
		name   string
		mutate func(*HTTPFetchRequest)
	}{
		{name: "post", mutate: func(r *HTTPFetchRequest) {}},
		{name: "privileged header on get", mutate: func(r *HTTPFetchRequest) {
			r.Method = http.MethodGet
			r.Headers = map[string]string{"X-Foo": "fixture-foo"}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := extendedFetchRequest()
			req.AuthProfileID = "default"
			tt.mutate(&req)

			_, err := broker.Fetch(context.Background(), req)
			if err == nil {
				t.Fatal("Fetch() error = nil, want extended/auth-profile mutex rejection")
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHTTPBrokerExtendedFailsClosedOnAnyRedirect(t *testing.T) {
	for _, statusCode := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			calls := 0
			broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				resp := redirectResponse("https://api.fixture.invalid/next")
				resp.StatusCode = statusCode
				return resp, nil
			}), nil)
			req := extendedFetchRequest()
			req.Body = []byte(`{"k":"v"}`)
			req.Headers = map[string]string{"Content-Type": "application/json"}

			_, err := broker.Fetch(context.Background(), req)
			if err == nil {
				t.Fatal("Fetch() error = nil, want redirect rejection")
			}
			if err.Error() != "extended fetch request must not redirect" {
				t.Fatalf("error = %q, want static redirect rejection", err.Error())
			}
			if calls != 1 {
				t.Fatalf("transport calls = %d, want exactly 1 (single hop)", calls)
			}
		})
	}

	t.Run("privileged get does not follow redirects", func(t *testing.T) {
		calls := 0
		broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return redirectResponse("https://api.fixture.invalid/next"), nil
		}), nil)
		req := extendedFetchRequest()
		req.Method = http.MethodGet
		req.Headers = map[string]string{"X-Foo": "fixture-foo"}

		_, err := broker.Fetch(context.Background(), req)
		if err == nil {
			t.Fatal("Fetch() error = nil, want redirect rejection")
		}
		if calls != 1 {
			t.Fatalf("transport calls = %d, want exactly 1", calls)
		}
	})
}

func TestHTTPBrokerExtendedRequiresHTTPS(t *testing.T) {
	transport := &recordingTransport{}
	broker := testHTTPBroker(transport, nil)
	req := extendedFetchRequest()
	req.URL = "http://api.fixture.invalid/submit"
	req.Body = []byte(`{"k":"v"}`)
	req.Headers = map[string]string{"Content-Type": "application/json"}

	_, err := broker.Fetch(context.Background(), req)
	if err == nil {
		t.Fatal("Fetch() error = nil, want https rejection")
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("error = %q, want HTTPS requirement", err.Error())
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestHTTPBrokerExtendedBodyRules(t *testing.T) {
	t.Run("body without post", func(t *testing.T) {
		transport := &recordingTransport{}
		broker := testHTTPBroker(transport, nil)
		req := extendedFetchRequest()
		req.Method = http.MethodGet
		req.Body = []byte("x")
		req.Headers = map[string]string{"Content-Type": "application/json"}

		if _, err := broker.Fetch(context.Background(), req); err == nil {
			t.Fatal("Fetch() error = nil, want body/POST rejection")
		}
		if transport.Count() != 0 {
			t.Fatalf("transport calls = %d, want 0", transport.Count())
		}
	})

	t.Run("body over cap", func(t *testing.T) {
		transport := &recordingTransport{}
		broker := testHTTPBroker(transport, nil)
		req := extendedFetchRequest()
		req.Body = make([]byte, maxExtendedFetchBodyBytes+1)
		req.Headers = map[string]string{"Content-Type": "application/json"}

		if _, err := broker.Fetch(context.Background(), req); err == nil {
			t.Fatal("Fetch() error = nil, want body cap rejection")
		}
		if transport.Count() != 0 {
			t.Fatalf("transport calls = %d, want 0", transport.Count())
		}
	})

	t.Run("body missing content type", func(t *testing.T) {
		transport := &recordingTransport{}
		broker := testHTTPBroker(transport, nil)
		req := extendedFetchRequest()
		req.Body = []byte("x")

		if _, err := broker.Fetch(context.Background(), req); err == nil {
			t.Fatal("Fetch() error = nil, want content type rejection")
		}
		if transport.Count() != 0 {
			t.Fatalf("transport calls = %d, want 0", transport.Count())
		}
	})

	t.Run("body with disallowed content type", func(t *testing.T) {
		transport := &recordingTransport{}
		broker := testHTTPBroker(transport, nil)
		req := extendedFetchRequest()
		req.Body = []byte("x")
		req.Headers = map[string]string{"Content-Type": "text/plain"}

		if _, err := broker.Fetch(context.Background(), req); err == nil {
			t.Fatal("Fetch() error = nil, want content type rejection")
		}
		if transport.Count() != 0 {
			t.Fatalf("transport calls = %d, want 0", transport.Count())
		}
	})

	t.Run("empty body post sends no explicit body", func(t *testing.T) {
		var seenBody any
		var seenLength int64
		broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenBody = req.Body
			seenLength = req.ContentLength
			return textResponse(200, "ok"), nil
		}), nil)
		req := extendedFetchRequest()

		if _, err := broker.Fetch(context.Background(), req); err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		if seenBody != nil {
			t.Fatalf("request body = %#v, want nil for empty extended POST", seenBody)
		}
		if seenLength != 0 {
			t.Fatalf("ContentLength = %d, want transport-default 0", seenLength)
		}
	})
}

func TestHTTPBrokerExtendedRejectsReflectedSecrets(t *testing.T) {
	const sentinel = "fixture-extended-secret"
	base := func() HTTPFetchRequest {
		req := extendedFetchRequest()
		req.Method = http.MethodGet
		req.Headers = map[string]string{"Authorization": "Bearer " + sentinel}
		return req
	}

	t.Run("response body echo", func(t *testing.T) {
		broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return textResponse(200, `{"echo":"`+sentinel+`"}`), nil
		}), nil)

		_, err := broker.Fetch(context.Background(), base())
		if err == nil {
			t.Fatal("Fetch() error = nil, want reflected secret rejection")
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("Fetch() leaked pack-owned value: %v", err)
		}
	})

	t.Run("credentials segment echo", func(t *testing.T) {
		broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return textResponse(200, `{"echo":"`+sentinel+`"}`), nil
		}), nil)
		req := extendedFetchRequest()
		req.Method = http.MethodGet
		// Credentials must be extracted with the same trim rule used for
		// secret registration; an echoed bare credential still trips.
		req.Headers = map[string]string{"Authorization": "ApiKey " + sentinel}

		_, err := broker.Fetch(context.Background(), req)
		if err == nil {
			t.Fatal("Fetch() error = nil, want reflected secret rejection")
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("Fetch() leaked credentials segment: %v", err)
		}
	})

	t.Run("safe response header echo", func(t *testing.T) {
		broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
			resp := textResponse(200, "ok")
			resp.Header.Set("Content-Type", "application/json; note="+sentinel)
			return resp, nil
		}), nil)

		_, err := broker.Fetch(context.Background(), base())
		if err == nil {
			t.Fatal("Fetch() error = nil, want reflected secret rejection")
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("Fetch() leaked pack-owned value: %v", err)
		}
	})

	t.Run("opaque content encoding fails closed", func(t *testing.T) {
		broker := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/plain"}, "Content-Encoding": []string{"br"}},
				Body:       io.NopCloser(strings.NewReader("raw compressed bytes")),
			}, nil
		}), nil)

		_, err := broker.Fetch(context.Background(), base())
		if err == nil {
			t.Fatal("Fetch() error = nil, want opaque encoding rejection")
		}
		if !strings.Contains(err.Error(), "opaque content encoding") {
			t.Fatalf("error = %q, want opaque encoding rejection", err.Error())
		}
	})
}

func TestValidatePackHeadersExtendedMatrix(t *testing.T) {
	broker := NewHTTPBroker(HTTPBrokerConfig{Policy: DefaultHTTPBrokerPolicy()})

	tests := []struct {
		name            string
		headers         map[string]string
		extendedCapable bool
		wantErr         bool
	}{
		{name: "x- business header", headers: map[string]string{"X-Foo": "v"}, extendedCapable: true},
		{name: "lowercase x- name", headers: map[string]string{"x-foo": "v"}, extendedCapable: true},
		{name: "bare x- name", headers: map[string]string{"X-": "v"}, extendedCapable: true},
		{name: "empty x- value", headers: map[string]string{"X-Foo": ""}, extendedCapable: true},
		{name: "authorization scheme", headers: map[string]string{"Authorization": "Bearer x"}, extendedCapable: true},
		{name: "ambient scheme allowed for pack-owned", headers: map[string]string{"Authorization": "Basic abc"}, extendedCapable: true},
		{name: "denied x- name", headers: map[string]string{"X-Real-Ip": "v"}, extendedCapable: true, wantErr: true},
		{name: "denied x- prefix", headers: map[string]string{"X-Forwarded-For": "v"}, extendedCapable: true, wantErr: true},
		{name: "x- without capability", headers: map[string]string{"X-Foo": "v"}, wantErr: true},
		{name: "authorization without capability", headers: map[string]string{"Authorization": "Bearer x"}, wantErr: true},
		{name: "safe header without capability", headers: map[string]string{"Accept": "a"}},
		{name: "safe header with capability", headers: map[string]string{"Accept": "a"}, extendedCapable: true},
		{name: "canonical duplicate", headers: map[string]string{"x-foo": "a", "X-Foo": "b"}, extendedCapable: true, wantErr: true},
		{name: "non token name", headers: map[string]string{"Bad Name": "v"}, extendedCapable: true, wantErr: true},
		{name: "non token at sign", headers: map[string]string{"x@y": "v"}, extendedCapable: true, wantErr: true},
		{name: "control byte in x- value", headers: map[string]string{"X-Foo": "a\tb"}, extendedCapable: true, wantErr: true},
		{name: "value at max", headers: map[string]string{"X-Foo": strings.Repeat("a", 1024)}, extendedCapable: true},
		{name: "value over max", headers: map[string]string{"X-Foo": strings.Repeat("a", 1025)}, extendedCapable: true, wantErr: true},
		{name: "authorization missing credentials", headers: map[string]string{"Authorization": "Bearer"}, extendedCapable: true, wantErr: true},
		{name: "authorization double space", headers: map[string]string{"Authorization": "Bearer  x"}, extendedCapable: true, wantErr: true},
		{name: "authorization trailing space", headers: map[string]string{"Authorization": "Bearer x "}, extendedCapable: true, wantErr: true},
		{name: "authorization leading space", headers: map[string]string{"Authorization": " Bearer x"}, extendedCapable: true, wantErr: true},
		{name: "forbidden cookie stays rejected", headers: map[string]string{"Cookie": "a=b"}, extendedCapable: true, wantErr: true},
		{name: "forbidden host stays rejected", headers: map[string]string{"Host": "v"}, extendedCapable: true, wantErr: true},
		{name: "origin stays rejected", headers: map[string]string{"Origin": "v"}, extendedCapable: true, wantErr: true},
		{name: "sec- prefix stays rejected", headers: map[string]string{"Sec-Fetch-Site": "v"}, extendedCapable: true, wantErr: true},
		{name: "non x- custom stays rejected", headers: map[string]string{"Foo-Bar": "v"}, extendedCapable: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := broker.validatePackHeaders(tt.headers, tt.extendedCapable)
			if tt.wantErr && err == nil {
				t.Fatal("validatePackHeaders() error = nil, want rejection")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validatePackHeaders() error = %v, want success", err)
			}
		})
	}
}
