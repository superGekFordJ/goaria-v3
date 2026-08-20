package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHostImportHTTPFetchAllowsBrokeredGET(t *testing.T) {
	transport := &hostImportRecordingTransport{
		statusCode: http.StatusAccepted,
		body:       "fixture body",
	}
	bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, nil), nil, 4)

	raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
		Method: "GET",
		URL:    "https://api.fixture.invalid/path",
		Headers: map[string]string{
			"Accept":     "application/json",
			"User-Agent": "GoAria-Test",
		},
	}))
	var response HostHTTPFetchResponse
	decodeHostImportTestResponse(t, raw, &response)

	if !response.OK {
		t.Fatalf("executeHTTPFetch() OK = false, response = %#v", response)
	}
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d, want %d", response.StatusCode, http.StatusAccepted)
	}
	if response.FinalURL != "https://api.fixture.invalid/path" {
		t.Fatalf("FinalURL = %q, want safe final URL", response.FinalURL)
	}
	if response.Headers["Content-Type"][0] != "text/plain" {
		t.Fatalf("Headers = %#v, want safe content-type", response.Headers)
	}
	if _, ok := response.Headers["X-Secret"]; ok {
		t.Fatalf("Headers leaked unsafe response header: %#v", response.Headers)
	}
	if response.BodyBase64 != base64.StdEncoding.EncodeToString([]byte("fixture body")) {
		t.Fatalf("BodyBase64 = %q, want base64 fixture body", response.BodyBase64)
	}
	if transport.Count() != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.Count())
	}
	request := transport.LastRequest()
	if request.Header.Get("Accept") != "application/json" || request.Header.Get("User-Agent") != "GoAria-Test" {
		t.Fatalf("transport request headers = %#v, want safe guest headers", request.Header)
	}
}

func TestHostImportHTTPFetchRejectsDeniedPolicyBeforeTransport(t *testing.T) {
	tests := []struct {
		name     string
		manifest Manifest
		request  []byte
	}{
		{
			name: "missing http fetch capability",
			manifest: func() Manifest {
				manifest := hostImportManifest()
				manifest.Capabilities = []Capability{CapabilityAuthProfile}

				return manifest
			}(),
			request: mustHostImportJSON(t, HostHTTPFetchRequest{URL: "https://api.fixture.invalid/path"}),
		},
		{
			name:     "denied suffix trick domain",
			manifest: hostImportManifest(),
			request:  mustHostImportJSON(t, HostHTTPFetchRequest{URL: "https://fixture.invalid.evil.test/path?token=query-secret"}),
		},
		{
			name:     "forbidden method",
			manifest: hostImportManifest(),
			request:  mustHostImportJSON(t, HostHTTPFetchRequest{Method: "POST", URL: "https://api.fixture.invalid/path"}),
		},
		{
			name:     "forbidden authorization header",
			manifest: hostImportManifest(),
			request: mustHostImportJSON(t, HostHTTPFetchRequest{
				URL:     "https://api.fixture.invalid/path",
				Headers: map[string]string{"Authorization": "Bearer raw-token-value"},
			}),
		},
		{
			name:     "forbidden cookie header",
			manifest: hostImportManifest(),
			request: mustHostImportJSON(t, HostHTTPFetchRequest{
				URL:     "https://api.fixture.invalid/path",
				Headers: map[string]string{"Cookie": "sid=raw-cookie"},
			}),
		},
		{
			name:     "forbidden api key header",
			manifest: hostImportManifest(),
			request: mustHostImportJSON(t, HostHTTPFetchRequest{
				URL:     "https://api.fixture.invalid/path",
				Headers: map[string]string{"X-Api-Key": "raw-api-key"},
			}),
		},
		{
			name:     "malformed userinfo URL",
			manifest: hostImportManifest(),
			request:  mustHostImportJSON(t, HostHTTPFetchRequest{URL: "https://user:pass@fixture.invalid/path"}),
		},
		{
			name:     "unknown JSON field",
			manifest: hostImportManifest(),
			request:  []byte(`{"url":"https://api.fixture.invalid/path","unexpected":true}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &hostImportRecordingTransport{err: errors.New("unexpected transport call")}
			bridge := newTestHostImportBridge(t, tt.manifest, testHTTPBroker(transport, nil), nil, 4)
			raw := bridge.executeHTTPFetch(context.Background(), tt.request)
			var response HostHTTPFetchResponse
			decodeHostImportTestResponse(t, raw, &response)

			if response.OK {
				t.Fatalf("executeHTTPFetch() OK = true, want false: %#v", response)
			}
			if strings.Contains(string(raw), "raw-token-value") || strings.Contains(string(raw), "raw-cookie") || strings.Contains(string(raw), "raw-api-key") || strings.Contains(string(raw), "query-secret") {
				t.Fatalf("executeHTTPFetch() leaked secret text in %s", raw)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchEnforcesTimeoutAndBodyCap(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		transport := &hostImportRecordingTransport{
			roundTrip: func(req *http.Request) (*http.Response, error) {
				<-req.Context().Done()

				return nil, req.Context().Err()
			},
		}
		bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, nil), nil, 4)

		raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
			URL:           "https://api.fixture.invalid/path",
			TimeoutMillis: 1,
		}))
		var response HostHTTPFetchResponse
		decodeHostImportTestResponse(t, raw, &response)

		if response.OK {
			t.Fatalf("executeHTTPFetch() OK = true, want timeout failure: %#v", response)
		}
		if transport.Count() != 1 {
			t.Fatalf("transport calls = %d, want 1", transport.Count())
		}
	})

	t.Run("body cap", func(t *testing.T) {
		transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: strings.Repeat("x", 32)}
		bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, nil), nil, 4)

		raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
			URL:              "https://api.fixture.invalid/path",
			MaxResponseBytes: 4,
		}))
		var response HostHTTPFetchResponse
		decodeHostImportTestResponse(t, raw, &response)

		if response.OK {
			t.Fatalf("executeHTTPFetch() OK = true, want body-cap failure: %#v", response)
		}
		if response.BodyBase64 != "" {
			t.Fatalf("BodyBase64 = %q, want empty on body-cap failure", response.BodyBase64)
		}
	})
}

func TestHostImportHTTPFetchInjectsAuthWithoutReturningSecret(t *testing.T) {
	const rawToken = "raw-token-value"
	transport := &hostImportRecordingTransport{
		statusCode: http.StatusOK,
		body:       "authenticated ok",
	}
	resolver := hostImportAuthResolver{secret: ResolvedAuthSecret{
		HeaderName:      "Authorization",
		HeaderValue:     "Bearer " + rawToken,
		Kind:            AuthSecretKindBearer,
		RedactedDisplay: "ra…ue",
	}}
	bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, resolver), resolver, 4)

	raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
		URL:            "https://api.fixture.invalid/path?token=query-secret",
		AuthProfileRef: "default",
	}))
	var response HostHTTPFetchResponse
	decodeHostImportTestResponse(t, raw, &response)

	if !response.OK {
		t.Fatalf("executeHTTPFetch() OK = false, response = %#v", response)
	}
	if got := transport.LastRequest().Header.Get("Authorization"); got != "Bearer "+rawToken {
		t.Fatalf("Authorization header = %q, want injected bearer", got)
	}
	responseText := string(raw)
	for _, forbidden := range []string{rawToken, "Bearer " + rawToken, "query-secret"} {
		if strings.Contains(responseText, forbidden) {
			t.Fatalf("executeHTTPFetch() leaked %q in %s", forbidden, responseText)
		}
	}
}

func TestHostImportAliasRawURLRejectedBeforePolicyOrTransport(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "alias ok"}
	resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}
	broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
	bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, resolver, 4)

	raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{URL: "https://api.alpha.test/path"}))
	var response HostHTTPFetchResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK {
		t.Fatalf("executeHTTPFetch() = %#v, want raw-url denial", response)
	}
	if resolver.calls != 0 || transport.Count() != 0 {
		t.Fatalf("resolver calls=%d transport calls=%d, want 0/0", resolver.calls, transport.Count())
	}
}

func TestHostImportAliasRefModeHTTPFetchExpandsEndpointWithoutFinalURL(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "alias ok"}
	resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}
	broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
	bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, resolver, 4)

	raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "ep-alpha001",
		Params:          map[string]string{"id": "fixture-item"},
		Headers:         map[string]string{"Accept": "application/json"},
	}))
	var response HostHTTPFetchResponse
	decodeHostImportTestResponse(t, raw, &response)
	if !response.OK {
		t.Fatalf("executeHTTPFetch() = %#v, want ok", response)
	}
	if response.FinalURL != "" {
		t.Fatalf("FinalURL = %q, want empty for ref-mode", response.FinalURL)
	}
	if transport.Count() != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.Count())
	}
	request := transport.LastRequest()
	if got := request.URL.String(); got != "https://api.alpha.test/files/fixture-item" {
		t.Fatalf("transport URL = %q, want expanded endpoint", got)
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept header = %q", got)
	}
}

func TestHostImportAliasDenialsAndBudgetStopPrivilegedWork(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "alias ok"}
	broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport})
	bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, nil, 4)

	request := HostHTTPFetchRequest{BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-alpha001", Params: map[string]string{"id": "fixture-item"}}
	raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, request))
	var response HostHTTPFetchResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK {
		t.Fatalf("executeHTTPFetch() OK = true, want no-resolver denial")
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}

	exhausted := newTestHostImportBridgeForPack(t, pack, NewHTTPBroker(HTTPBrokerConfig{
		Policy:             testHTTPPolicy(),
		Transport:          transport,
		HostPolicyResolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)},
	}), nil, nil, 1)
	requestBytes := mustHostImportJSON(t, request)
	decodeHostImportTestResponse(t, exhausted.executeHTTPFetch(context.Background(), requestBytes), &response)
	if !response.OK {
		t.Fatalf("first alias fetch = %#v, want ok", response)
	}
	decodeHostImportTestResponse(t, exhausted.executeHTTPFetch(context.Background(), requestBytes), &response)
	if response.OK {
		t.Fatalf("second alias fetch OK = true, want budget denial")
	}
}

func TestHostImportAliasRefModeHTTPFetchFailsClosedBeforePrivilegedWork(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	tests := []struct {
		name               string
		request            HostHTTPFetchRequest
		policy             ResolvedHostPolicy
		wantPolicyCalls    int
		wantTransportCalls int
	}{
		{name: "mixed raw and ref", request: HostHTTPFetchRequest{URL: "https://api.alpha.test/files/fixture-item", BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-alpha001"}},
		{name: "missing endpoint ref", request: HostHTTPFetchRequest{BrokerPolicyRef: "bpr-alpha001"}},
		{name: "invalid params", request: HostHTTPFetchRequest{BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-alpha001", Params: map[string]string{"id": "a/b"}}},
		{name: "unsupported endpoint method", request: HostHTTPFetchRequest{Method: "POST", BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-alpha001", Params: map[string]string{"id": "fixture-item"}}, policy: syntheticHostPolicy(pack.Identity), wantPolicyCalls: 1},
		{name: "unknown endpoint", request: HostHTTPFetchRequest{BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-missing001", Params: map[string]string{"id": "fixture-item"}}, policy: syntheticHostPolicy(pack.Identity), wantPolicyCalls: 1},
		{name: "disallowed auth profile", request: HostHTTPFetchRequest{BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-alpha001", Params: map[string]string{"id": "fixture-item"}, AuthProfileRef: "other-secret"}, policy: syntheticHostPolicy(pack.Identity), wantPolicyCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "unexpected"}
			var resolver HostPolicyResolver
			var fake *fakeHostPolicyResolver
			if tt.policy.PolicyID != "" {
				fake = &fakeHostPolicyResolver{policy: tt.policy}
				resolver = fake
			}
			broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
			bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, resolver, 4)
			raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, tt.request))
			var response HostHTTPFetchResponse
			decodeHostImportTestResponse(t, raw, &response)
			if response.OK {
				t.Fatalf("executeHTTPFetch() = %#v, want fail closed", response)
			}
			policyCalls := 0
			if fake != nil {
				policyCalls = fake.calls
			}
			if policyCalls != tt.wantPolicyCalls || transport.Count() != tt.wantTransportCalls {
				t.Fatalf("policy calls=%d transport calls=%d, want %d/%d", policyCalls, transport.Count(), tt.wantPolicyCalls, tt.wantTransportCalls)
			}
		})
	}
}

func TestHostImportAliasRefModeHTTPFetchBrokerErrorsAreGeneric(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	request := HostHTTPFetchRequest{
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "ep-alpha001",
		Params:          map[string]string{"id": "fixture-item"},
	}
	privateURL := "https://api.alpha.test/files/fixture-item"
	tests := []struct {
		name      string
		transport http.RoundTripper
	}{
		{
			name: "transport error with expanded endpoint",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed for " + privateURL + " with private path")
			}),
		},
		{
			name: "redirect denial with private location",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return redirectResponse("https://other.alpha.test/private/redirect-location"), nil
			}),
		},
		{
			name: "redirect limit with expanded endpoint",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return redirectResponse(privateURL), nil
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}
			broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: tt.transport, HostPolicyResolver: resolver})
			bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, resolver, 4)

			raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, request))
			var response HostHTTPFetchResponse
			decodeHostImportTestResponse(t, raw, &response)
			if response.OK || response.ErrorCode != "fetch_failed" || response.Message != "fetch failed" {
				t.Fatalf("executeHTTPFetch() = %#v, want generic fetch failure", response)
			}
			for _, forbidden := range []string{"api.alpha.test", "files/fixture-item", "other.alpha.test", "private/redirect-location", privateURL} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("ref-mode broker error leaked %q in %s", forbidden, raw)
				}
			}
		})
	}
}

func TestHostImportAliasAuthProfileStatusPolicyBeforeResolver(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	authResolver := &recordingHostImportAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer alpha-secret", Kind: AuthSecretKindBearer}}
	bridge := newTestHostImportBridgeForPack(t, pack, nil, authResolver, &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}, 4)

	raw := bridge.executeAuthProfileStatus(context.Background(), mustHostImportJSON(t, HostAuthProfileStatusRequest{
		AuthProfileRef:  "alpha-secret",
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "ep-alpha001",
		Params:          map[string]string{"id": "fixture-item"},
	}))
	var response HostAuthProfileStatusResponse
	decodeHostImportTestResponse(t, raw, &response)
	if !response.OK || !response.Available {
		t.Fatalf("executeAuthProfileStatus() = %#v, want available", response)
	}
	if authResolver.Count() != 1 {
		t.Fatalf("auth resolver calls = %d, want 1", authResolver.Count())
	}

	deniedPolicy := syntheticHostPolicy(pack.Identity)
	deniedPolicy.AuthProfiles = nil
	deniedResolver := &recordingHostImportAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer should-not-resolve"}}
	denied := newTestHostImportBridgeForPack(t, pack, nil, deniedResolver, &fakeHostPolicyResolver{policy: deniedPolicy}, 4)
	raw = denied.executeAuthProfileStatus(context.Background(), mustHostImportJSON(t, HostAuthProfileStatusRequest{
		AuthProfileRef:  "alpha-secret",
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "ep-alpha001",
		Params:          map[string]string{"id": "fixture-item"},
	}))
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK || deniedResolver.Count() != 0 {
		t.Fatalf("denied auth status response=%#v resolver calls=%d, want denial before resolver", response, deniedResolver.Count())
	}
}

func TestHostImportAuthProfileStatusIgnoresBrowserCookies(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	ctx := WithBrowserCookies(context.Background(), []SessionCookie{{
		Name: "sid", Value: "browser-sid", Domain: ".alpha.test", Path: "/", Secure: true, HostOnly: false,
	}})
	bridge := newTestHostImportBridgeForPack(t, pack, nil, nil, &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}, 4)

	raw := bridge.executeAuthProfileStatus(ctx, mustHostImportJSON(t, HostAuthProfileStatusRequest{
		AuthProfileRef:  "alpha-secret",
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "ep-alpha001",
		Params:          map[string]string{"id": "fixture-item"},
	}))
	var response HostAuthProfileStatusResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.Available {
		t.Fatalf("executeAuthProfileStatus() = %#v, cookies must not make an empty store available", response)
	}
}

func TestHostImportAliasRawURLAuthProfileStatusRejectedBeforePolicyOrResolver(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	fakePolicy := &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}
	authResolver := &recordingHostImportAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Authorization", HeaderValue: "Bearer alpha-secret"}}
	bridge := newTestHostImportBridgeForPack(t, pack, nil, authResolver, fakePolicy, 4)

	raw := bridge.executeAuthProfileStatus(context.Background(), mustHostImportJSON(t, HostAuthProfileStatusRequest{
		AuthProfileRef: "alpha-secret",
		URL:            "https://api.alpha.test/path",
	}))
	var response HostAuthProfileStatusResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK || fakePolicy.calls != 0 || authResolver.Count() != 0 {
		t.Fatalf("response=%#v policy calls=%d resolver calls=%d, want raw-url denial before work", response, fakePolicy.calls, authResolver.Count())
	}
}

func TestHostImportHTTPFetchAuthResolverErrorIsGeneric(t *testing.T) {
	const rawSecret = "raw-http-fetch-auth-secret"
	transport := &hostImportRecordingTransport{err: errors.New("transport must not be called")}
	resolver := hostImportAuthResolver{
		err: errors.New("resolver failed with Authorization: Bearer " + rawSecret + "; Cookie: sid=raw-cookie-secret"),
	}
	bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, resolver), resolver, 4)

	raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
		URL:            "https://api.fixture.invalid/path",
		AuthProfileRef: "default",
	}))
	var response HostHTTPFetchResponse
	decodeHostImportTestResponse(t, raw, &response)

	if response.OK {
		t.Fatalf("executeHTTPFetch() OK = true, want auth failure: %#v", response)
	}
	if response.ErrorCode != "authenticated_fetch_failed" || response.Message != "authenticated fetch failed" {
		t.Fatalf("executeHTTPFetch() = %#v, want generic authenticated fetch failure", response)
	}
	responseText := string(raw)
	for _, forbidden := range []string{rawSecret, "raw-cookie-secret", "Authorization", "Cookie", "resolver failed"} {
		if strings.Contains(responseText, forbidden) {
			t.Fatalf("executeHTTPFetch() leaked %q in %s", forbidden, responseText)
		}
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0 when auth resolver fails", transport.Count())
	}
}

func TestHostImportAuthProfileStatusReturnsOnlyRedactedMetadata(t *testing.T) {
	const rawToken = "raw-status-token"
	resolver := hostImportAuthResolver{secret: ResolvedAuthSecret{
		HeaderName:      "Authorization",
		HeaderValue:     "Bearer " + rawToken,
		Kind:            AuthSecretKindBearer,
		RedactedDisplay: "ra…en",
	}}
	bridge := newTestHostImportBridge(t, hostImportManifest(), nil, resolver, 4)

	raw := bridge.executeAuthProfileStatus(context.Background(), mustHostImportJSON(t, HostAuthProfileStatusRequest{
		AuthProfileRef: "default",
		URL:            "https://api.fixture.invalid/path",
	}))
	var response HostAuthProfileStatusResponse
	decodeHostImportTestResponse(t, raw, &response)

	if !response.OK || !response.Available {
		t.Fatalf("executeAuthProfileStatus() = %#v, want available ok", response)
	}
	if response.Kind != AuthSecretKindBearer || response.RedactedDisplay != "ra…en" {
		t.Fatalf("safe metadata = %#v, want bearer redacted display", response)
	}
	responseText := string(raw)
	for _, forbidden := range []string{"Authorization", rawToken, "Bearer " + rawToken} {
		if strings.Contains(responseText, forbidden) {
			t.Fatalf("auth_profile_status leaked %q in %s", forbidden, responseText)
		}
	}
}

func TestHostImportSeededFileStoreStatusAndHTTPFetchAreRedacted(t *testing.T) {
	t.Run("bearer status", func(t *testing.T) {
		store := newTempAuthProfileStore(t)
		secret := "seeded-hostimport-bearer-secret"
		seedHostImportLegacyProfile(t, store, AuthSecretKindBearer, secret, nil, "Bearer "+secret)
		bridge := newTestHostImportBridge(t, hostImportManifest(), nil, store, 4)

		raw := bridge.executeAuthProfileStatus(context.Background(), mustHostImportJSON(t, HostAuthProfileStatusRequest{
			AuthProfileRef: string(hostImportLegacyProfileID),
			URL:            "https://api.fixture.invalid/contents/example",
		}))
		var response HostAuthProfileStatusResponse
		decodeHostImportTestResponse(t, raw, &response)

		if !response.OK || !response.Available || response.Kind != AuthSecretKindBearer {
			t.Fatalf("executeAuthProfileStatus() = %#v, want available bearer", response)
		}
		for _, forbidden := range []string{secret, "Bearer " + secret, "Authorization"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("executeAuthProfileStatus() leaked %q in %s", forbidden, raw)
			}
		}
	})

	for _, tt := range []struct {
		name       string
		kind       AuthSecretKind
		secret     string
		url        string
		assertions func(t *testing.T, req *http.Request, raw string)
	}{
		{
			name:   "bearer fetch",
			kind:   AuthSecretKindBearer,
			secret: "seeded-fetch-bearer-secret",
			url:    "https://api.fixture.invalid/contents/example",
			assertions: func(t *testing.T, req *http.Request, raw string) {
				t.Helper()
				if got := req.Header.Get("Authorization"); got != "Bearer seeded-fetch-bearer-secret" {
					t.Fatalf("Authorization header = %q, want derived bearer", got)
				}
				for _, forbidden := range []string{"seeded-fetch-bearer-secret", "Bearer seeded-fetch-bearer-secret", "Authorization"} {
					if strings.Contains(raw, forbidden) {
						t.Fatalf("executeHTTPFetch() leaked %q in %s", forbidden, raw)
					}
				}
			},
		},
		{
			name:   "cookie fetch",
			kind:   AuthSecretKindCookie,
			secret: "session_id=seeded-cookie-secret",
			url:    "https://api.fixture.invalid/contents/example",
			assertions: func(t *testing.T, req *http.Request, raw string) {
				t.Helper()
				if got := req.Header.Get("Cookie"); got != "session_id=seeded-cookie-secret" {
					t.Fatalf("Cookie header = %q, want cookie", got)
				}
				for _, forbidden := range []string{"seeded-cookie-secret", "session_id=seeded-cookie-secret", "Cookie"} {
					if strings.Contains(raw, forbidden) {
						t.Fatalf("executeHTTPFetch() leaked %q in %s", forbidden, raw)
					}
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTempAuthProfileStore(t)
			seedHostImportLegacyProfile(t, store, tt.kind, tt.secret, nil, "operator profile")
			transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "authenticated ok"}
			bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, store), store, 4)

			raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
				URL:            tt.url,
				AuthProfileRef: string(hostImportLegacyProfileID),
			}))
			var response HostHTTPFetchResponse
			decodeHostImportTestResponse(t, raw, &response)

			if !response.OK {
				t.Fatalf("executeHTTPFetch() = %#v, want ok", response)
			}
			tt.assertions(t, transport.LastRequest(), string(raw))
		})
	}
}

func TestHostImportSeededFileStoreAuthErrorsAreGeneric(t *testing.T) {
	t.Run("expired status", func(t *testing.T) {
		store := newTempAuthProfileStore(t)
		secret := "expired-hostimport-secret"
		expiresAt := time.Now().Add(-time.Minute)
		seedHostImportLegacyProfile(t, store, AuthSecretKindBearer, secret, &expiresAt, "expired")
		bridge := newTestHostImportBridge(t, hostImportManifest(), nil, store, 4)

		raw := bridge.executeAuthProfileStatus(context.Background(), mustHostImportJSON(t, HostAuthProfileStatusRequest{
			AuthProfileRef: string(hostImportLegacyProfileID),
			URL:            "https://api.fixture.invalid/contents/example",
		}))
		var response HostAuthProfileStatusResponse
		decodeHostImportTestResponse(t, raw, &response)

		if response.OK || response.ErrorCode != "auth_unavailable" || response.Message != "auth profile unavailable" {
			t.Fatalf("executeAuthProfileStatus() = %#v, want generic unavailable", response)
		}
		if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "Authorization") {
			t.Fatalf("executeAuthProfileStatus() leaked secret details in %s", raw)
		}
	})

	t.Run("denied fetch", func(t *testing.T) {
		store := newTempAuthProfileStore(t)
		secret := "denied-hostimport-secret"
		if _, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
			PackID:         "xpk-fixture01",
			ProfileID:      hostImportLegacyProfileID,
			Kind:           AuthSecretKindBearer,
			Secret:         secret,
			AllowedDomains: []DomainRule{{Host: "other.fixture.invalid"}},
		}); err != nil {
			t.Fatalf("SetAuthProfile() error = %v", err)
		}
		transport := &hostImportRecordingTransport{err: errors.New("transport must not be called")}
		bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, store), store, 4)

		raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
			URL:            "https://api.fixture.invalid/contents/example?token=query-secret",
			AuthProfileRef: string(hostImportLegacyProfileID),
		}))
		var response HostHTTPFetchResponse
		decodeHostImportTestResponse(t, raw, &response)

		if response.OK || response.ErrorCode != "authenticated_fetch_failed" || response.Message != "authenticated fetch failed" {
			t.Fatalf("executeHTTPFetch() = %#v, want generic authenticated fetch failure", response)
		}
		for _, forbidden := range []string{secret, "query-secret", "Authorization", "Cookie"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("executeHTTPFetch() leaked %q in %s", forbidden, raw)
			}
		}
		if transport.Count() != 0 {
			t.Fatalf("transport calls = %d, want 0", transport.Count())
		}
	})
}

func TestHostImportAuthProfileStatusRejectsDeniedCapabilityDomainOrResolver(t *testing.T) {
	tests := []struct {
		name          string
		manifest      Manifest
		resolver      AuthProfileResolver
		request       []byte
		wantCalls     int
		forbiddenText string
	}{
		{
			name: "missing auth capability",
			manifest: func() Manifest {
				manifest := hostImportManifest()
				manifest.Capabilities = []Capability{CapabilityHTTPFetch}

				return manifest
			}(),
			resolver:  &recordingHostImportAuthResolver{},
			request:   mustHostImportJSON(t, HostAuthProfileStatusRequest{AuthProfileRef: "default", URL: "https://api.fixture.invalid/path"}),
			wantCalls: 0,
		},
		{
			name:      "denied URL domain",
			manifest:  hostImportManifest(),
			resolver:  &recordingHostImportAuthResolver{},
			request:   mustHostImportJSON(t, HostAuthProfileStatusRequest{AuthProfileRef: "default", URL: "https://evil.test/path"}),
			wantCalls: 0,
		},
		{
			name:      "empty profile ref",
			manifest:  hostImportManifest(),
			resolver:  &recordingHostImportAuthResolver{},
			request:   mustHostImportJSON(t, HostAuthProfileStatusRequest{URL: "https://api.fixture.invalid/path"}),
			wantCalls: 0,
		},
		{
			name:      "invalid profile ref",
			manifest:  hostImportManifest(),
			resolver:  &recordingHostImportAuthResolver{},
			request:   mustHostImportJSON(t, HostAuthProfileStatusRequest{AuthProfileRef: "Invalid", URL: "https://api.fixture.invalid/path"}),
			wantCalls: 0,
		},
		{
			name:      "nil resolver",
			manifest:  hostImportManifest(),
			resolver:  nil,
			request:   mustHostImportJSON(t, HostAuthProfileStatusRequest{AuthProfileRef: "default", URL: "https://api.fixture.invalid/path"}),
			wantCalls: 0,
		},
		{
			name:     "resolver error redacted",
			manifest: hostImportManifest(),
			resolver: &recordingHostImportAuthResolver{
				err: errors.New("Authorization: Bearer raw-status-secret failed; Cookie: sid=raw-cookie-secret"),
			},
			request:       mustHostImportJSON(t, HostAuthProfileStatusRequest{AuthProfileRef: "default", URL: "https://api.fixture.invalid/path"}),
			wantCalls:     1,
			forbiddenText: "raw-status-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bridge := newTestHostImportBridge(t, tt.manifest, nil, tt.resolver, 4)
			raw := bridge.executeAuthProfileStatus(context.Background(), tt.request)
			var response HostAuthProfileStatusResponse
			decodeHostImportTestResponse(t, raw, &response)

			if response.OK {
				t.Fatalf("executeAuthProfileStatus() OK = true, want false: %#v", response)
			}
			if tt.forbiddenText != "" && strings.Contains(string(raw), tt.forbiddenText) {
				t.Fatalf("executeAuthProfileStatus() leaked %q in %s", tt.forbiddenText, raw)
			}
			if tt.name == "resolver error redacted" {
				if response.Message != "auth profile unavailable" {
					t.Fatalf("resolver error message = %q, want generic message", response.Message)
				}
				for _, forbidden := range []string{"Authorization", "Cookie", "raw-cookie-secret"} {
					if strings.Contains(string(raw), forbidden) {
						t.Fatalf("executeAuthProfileStatus() leaked %q in %s", forbidden, raw)
					}
				}
			}
			if recorder, ok := tt.resolver.(*recordingHostImportAuthResolver); ok && recorder.Count() != tt.wantCalls {
				t.Fatalf("resolver calls = %d, want %d", recorder.Count(), tt.wantCalls)
			}
		})
	}
}

func TestHostImportBudgetExhaustionStopsPrivilegedWork(t *testing.T) {
	transport := &hostImportRecordingTransport{statusCode: http.StatusOK, body: "ok"}
	bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, nil), nil, 1)
	request := mustHostImportJSON(t, HostHTTPFetchRequest{URL: "https://api.fixture.invalid/path"})

	var first HostHTTPFetchResponse
	decodeHostImportTestResponse(t, bridge.executeHTTPFetch(context.Background(), request), &first)
	if !first.OK {
		t.Fatalf("first executeHTTPFetch() = %#v, want ok", first)
	}

	var second HostHTTPFetchResponse
	rawSecond := bridge.executeHTTPFetch(context.Background(), request)
	decodeHostImportTestResponse(t, rawSecond, &second)
	if second.OK {
		t.Fatalf("second executeHTTPFetch() OK = true, want budget failure")
	}
	if !strings.Contains(strings.ToLower(string(rawSecond)), "budget") {
		t.Fatalf("second response = %s, want budget message/code", rawSecond)
	}
	if transport.Count() != 1 {
		t.Fatalf("transport calls = %d, want only first call", transport.Count())
	}
}

func TestHostImportStrictDecodeRejectsTrailingOrUnknownFields(t *testing.T) {
	tests := []struct {
		name    string
		execute func(*hostImportBridge, []byte) []byte
		request []byte
	}{
		{
			name: "http malformed JSON",
			execute: func(bridge *hostImportBridge, request []byte) []byte {
				return bridge.executeHTTPFetch(context.Background(), request)
			},
			request: []byte(`{"url":"https://api.fixture.invalid/path"`),
		},
		{
			name: "http trailing JSON",
			execute: func(bridge *hostImportBridge, request []byte) []byte {
				return bridge.executeHTTPFetch(context.Background(), request)
			},
			request: []byte(`{"url":"https://api.fixture.invalid/path"} {}`),
		},
		{
			name: "http unknown field",
			execute: func(bridge *hostImportBridge, request []byte) []byte {
				return bridge.executeHTTPFetch(context.Background(), request)
			},
			request: []byte(`{"url":"https://api.fixture.invalid/path","unknown":1}`),
		},
		{
			name: "auth trailing JSON",
			execute: func(bridge *hostImportBridge, request []byte) []byte {
				return bridge.executeAuthProfileStatus(context.Background(), request)
			},
			request: []byte(`{"auth_profile_ref":"default","url":"https://api.fixture.invalid/path"} {}`),
		},
		{
			name: "auth unknown field",
			execute: func(bridge *hostImportBridge, request []byte) []byte {
				return bridge.executeAuthProfileStatus(context.Background(), request)
			},
			request: []byte(`{"auth_profile_ref":"default","url":"https://api.fixture.invalid/path","unknown":1}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &hostImportRecordingTransport{err: errors.New("unexpected transport call")}
			resolver := &recordingHostImportAuthResolver{}
			bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, nil), resolver, 4)
			raw := tt.execute(bridge, tt.request)

			if !strings.Contains(string(raw), `"ok":false`) {
				t.Fatalf("response = %s, want ok:false", raw)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
			if resolver.Count() != 0 {
				t.Fatalf("resolver calls = %d, want 0", resolver.Count())
			}
		})
	}
}

type hostImportRecordingTransport struct {
	mu         sync.Mutex
	statusCode int
	body       string
	err        error
	roundTrip  func(*http.Request) (*http.Response, error)
	calls      int
	last       *http.Request
}

func (t *hostImportRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.calls++
	t.last = req.Clone(req.Context())
	t.last.Header = req.Header.Clone()
	t.mu.Unlock()

	if t.roundTrip != nil {
		return t.roundTrip(req)
	}
	if t.err != nil {
		return nil, t.err
	}
	if t.statusCode == 0 {
		return textResponse(http.StatusOK, ""), nil
	}

	return textResponse(t.statusCode, t.body), nil
}

func (t *hostImportRecordingTransport) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.calls
}

func (t *hostImportRecordingTransport) LastRequest() *http.Request {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.last
}

type hostImportAuthResolver struct {
	secret ResolvedAuthSecret
	err    error
}

func (r hostImportAuthResolver) ResolveAuthProfile(context.Context, string, AuthProfileID, string) (ResolvedAuthSecret, error) {
	if r.err != nil {
		return ResolvedAuthSecret{}, r.err
	}

	return r.secret, nil
}

type recordingHostImportAuthResolver struct {
	mu     sync.Mutex
	secret ResolvedAuthSecret
	err    error
	calls  int
}

func (r *recordingHostImportAuthResolver) ResolveAuthProfile(context.Context, string, AuthProfileID, string) (ResolvedAuthSecret, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return ResolvedAuthSecret{}, r.err
	}

	return r.secret, nil
}

func (r *recordingHostImportAuthResolver) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.calls
}

func newTestHostImportBridge(t *testing.T, manifest Manifest, broker *HTTPBroker, resolver AuthProfileResolver, maxHostCalls uint32) *hostImportBridge {
	t.Helper()
	return newTestHostImportBridgeForPack(t, VerifiedPack{Manifest: manifest}, broker, resolver, nil, maxHostCalls)
}

func newTestHostImportBridgeForPack(t *testing.T, pack VerifiedPack, broker *HTTPBroker, resolver AuthProfileResolver, hostPolicy HostPolicyResolver, maxHostCalls uint32) *hostImportBridge {
	t.Helper()
	budget, err := NewHostCallBudget(maxHostCalls)
	if err != nil {
		t.Fatalf("NewHostCallBudget() error = %v", err)
	}

	return newHostImportBridge(pack, budget, HostImportConfig{
		HTTPBroker:         broker,
		AuthResolver:       resolver,
		HostPolicyResolver: hostPolicy,
	})
}

func hostImportManifest() Manifest {
	manifest := validCapabilityManifest()
	manifest.Capabilities = []Capability{CapabilityHTTPFetch, CapabilityAuthProfile}
	manifest.ResourceLimits.TimeoutMillis = 100
	manifest.ResourceLimits.MaxResponseBytes = 1024
	manifest.ResourceLimits.MaxHostCalls = 8

	return manifest
}

const hostImportLegacyProfileID AuthProfileID = "default"

func seedHostImportLegacyProfile(t *testing.T, store AuthProfileStore, kind AuthSecretKind, secret string, expiresAt *time.Time, redactedDisplay string) {
	t.Helper()
	_, err := store.SetAuthProfile(context.Background(), AuthProfileUpdate{
		PackID:          "xpk-fixture01",
		ProfileID:       hostImportLegacyProfileID,
		Kind:            kind,
		Secret:          secret,
		AllowedDomains:  []DomainRule{{Host: "fixture.invalid", IncludeSubdomains: true}},
		ExpiresAt:       expiresAt,
		RedactedDisplay: redactedDisplay,
	})
	if err != nil {
		t.Fatalf("SetAuthProfile() error = %v", err)
	}
}

func mustHostImportJSON(t *testing.T, value any) []byte {
	t.Helper()
	bytes, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	return bytes
}

func decodeHostImportTestResponse(t *testing.T, raw []byte, output any) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("host import response is empty")
	}
	if err := json.Unmarshal(raw, output); err != nil {
		t.Fatalf("json.Unmarshal(%s) error = %v", raw, err)
	}
}
