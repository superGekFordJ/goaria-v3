package extractor

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func hostImportExtendedManifest() Manifest {
	manifest := hostImportManifest()
	manifest.Capabilities = append(manifest.Capabilities, CapabilityHTTPFetchExtended)

	return manifest
}

func syntheticExtendedAliasPack() VerifiedPack {
	pack := syntheticAliasVerifiedPack()
	pack.Manifest.Capabilities = append(pack.Manifest.Capabilities, CapabilityHTTPFetchExtended)

	return pack
}

func syntheticExtendedHostPolicy(identity VerifiedPackIdentity) ResolvedHostPolicy {
	policy := syntheticHostPolicy(identity)
	policy.AllowedCapabilities = append(policy.AllowedCapabilities, CapabilityHTTPFetchExtended)
	policy.Endpoints[0].Methods = []string{"GET", "HEAD", "POST"}

	return policy
}

func executeHostHTTPFetch(t *testing.T, bridge *hostImportBridge, raw []byte) HostHTTPFetchResponse {
	t.Helper()
	var response HostHTTPFetchResponse
	decodeHostImportTestResponse(t, bridge.executeHTTPFetch(context.Background(), raw), &response)

	return response
}

func transportRequestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	if request == nil {
		t.Fatal("transport request = nil")
	}
	if request.GetBody == nil {
		if request.Body == nil {
			return nil
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("io.ReadAll(request body) error = %v", err)
		}

		return body
	}
	reader, err := request.GetBody()
	if err != nil {
		t.Fatalf("request.GetBody() error = %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("io.ReadAll(request body) error = %v", err)
	}

	return body
}

func TestHostImportHTTPFetchBodyBase64OmissionMeansNoBody(t *testing.T) {
	for _, raw := range []string{
		`{"url":"https://api.fixture.invalid/path"}`,
		`{"url":"https://api.fixture.invalid/path","body_base64":""}`,
		`{"url":"https://api.fixture.invalid/path","body_base64":null}`,
	} {
		transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
		bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, nil), nil, 4)
		response := executeHostHTTPFetch(t, bridge, []byte(raw))
		if !response.OK {
			t.Fatalf("raw %s → response = %+v, want success", raw, response)
		}
		if transport.Count() != 1 {
			t.Fatalf("transport calls = %d, want 1", transport.Count())
		}
	}
}

func TestHostImportHTTPFetchRejectsMalformedBodyBase64(t *testing.T) {
	// The \\u000a / \\u000d / \\u0009 entries carry escaped control characters
	// through JSON so the base64 layer itself has to reject the whitespace the
	// decoder would otherwise skip.
	for _, body := range []string{
		"!!!!", "e30", "e30=-_", "e3 0=", "e30=\\u000a", "e30=\\u000d\\u000a",
		"e30=\\u0009", "e30= e30=", "####",
	} {
		t.Run(body, func(t *testing.T) {
			transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
			bridge := newTestHostImportBridge(t, hostImportExtendedManifest(), testHTTPBroker(transport, nil), nil, 4)
			response := executeHostHTTPFetch(t, bridge, []byte(`{"url":"https://api.fixture.invalid/path","method":"POST","headers":{"Content-Type":"application/json"},"body_base64":"`+body+`"}`))
			if response.OK || response.ErrorCode != "invalid_request" {
				t.Fatalf("response = %+v, want invalid_request", response)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchBodyBase64Cap(t *testing.T) {
	newBridge := func(t *testing.T) (*hostImportBridge, *hostImportRecordingTransport) {
		transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}

		return newTestHostImportBridge(t, hostImportExtendedManifest(), testHTTPBroker(transport, nil), nil, 4), transport
	}

	t.Run("exactly 16KiB passes", func(t *testing.T) {
		bridge, transport := newBridge(t)
		body := base64.StdEncoding.EncodeToString(make([]byte, 16*1024))
		response := executeHostHTTPFetch(t, bridge, []byte(`{"url":"https://api.fixture.invalid/path","method":"POST","headers":{"Content-Type":"application/json"},"body_base64":"`+body+`"}`))
		if !response.OK {
			t.Fatalf("response = %+v, want success at the 16KiB boundary", response)
		}
		if transport.LastRequest() == nil || transport.LastRequest().Method != http.MethodPost {
			t.Fatalf("transport request = %+v, want POST", transport.LastRequest())
		}
	})

	t.Run("16KiB plus one byte fails", func(t *testing.T) {
		bridge, transport := newBridge(t)
		body := base64.StdEncoding.EncodeToString(make([]byte, 16*1024+1))
		response := executeHostHTTPFetch(t, bridge, []byte(`{"url":"https://api.fixture.invalid/path","method":"POST","headers":{"Content-Type":"application/json"},"body_base64":"`+body+`"}`))
		if response.OK || response.ErrorCode != "invalid_request" {
			t.Fatalf("response = %+v, want invalid_request", response)
		}
		if transport.Count() != 0 {
			t.Fatalf("transport calls = %d, want 0", transport.Count())
		}
	})
}

func TestHostImportHTTPFetchRequestStaysWithinHostImportCap(t *testing.T) {
	transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
	bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, nil), nil, 4)
	// Raw JSON over the 64KiB host-import cap must fail closed before any
	// body decode or broker work happens.
	raw := `{"url":"https://api.fixture.invalid/path","body_base64":"` + strings.Repeat("A", 70*1024) + `"}`
	response := executeHostHTTPFetch(t, bridge, []byte(raw))
	if response.OK || response.ErrorCode != "invalid_request" {
		t.Fatalf("response = %+v, want invalid_request", response)
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestHostImportHTTPFetchBodyRequiresPOST(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", ""} {
		t.Run("method="+method, func(t *testing.T) {
			transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
			bridge := newTestHostImportBridge(t, hostImportExtendedManifest(), testHTTPBroker(transport, nil), nil, 4)
			raw := `{"url":"https://api.fixture.invalid/path","headers":{"Content-Type":"application/json"},"body_base64":"e30="}`
			if method != "" {
				raw = `{"url":"https://api.fixture.invalid/path","method":"` + method + `","headers":{"Content-Type":"application/json"},"body_base64":"e30="}`
			}
			response := executeHostHTTPFetch(t, bridge, []byte(raw))
			if response.OK || response.ErrorCode != "invalid_request" {
				t.Fatalf("response = %+v, want invalid_request", response)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchBodyContentTypeRules(t *testing.T) {
	tests := []struct {
		name    string
		headers string
	}{
		{name: "missing content type", headers: ``},
		{name: "text/plain rejected", headers: `,"headers":{"Content-Type":"text/plain"}`},
		{name: "malformed content type", headers: `,"headers":{"Content-Type":"application/json; charset"}`},
		{name: "ambiguous content type keys", headers: `,"headers":{"Content-Type":"application/json","content-type":"application/json"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
			bridge := newTestHostImportBridge(t, hostImportExtendedManifest(), testHTTPBroker(transport, nil), nil, 4)
			raw := `{"url":"https://api.fixture.invalid/path","method":"POST"` + tt.headers + `,"body_base64":"e30="}`
			response := executeHostHTTPFetch(t, bridge, []byte(raw))
			if response.OK || response.ErrorCode != "invalid_request" {
				t.Fatalf("response = %+v, want invalid_request", response)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchBodyAllowedContentTypes(t *testing.T) {
	for _, contentType := range []string{
		"application/json; charset=utf-8",
		"application/x-www-form-urlencoded",
	} {
		t.Run(contentType, func(t *testing.T) {
			transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
			bridge := newTestHostImportBridge(t, hostImportExtendedManifest(), testHTTPBroker(transport, nil), nil, 4)
			raw := `{"url":"https://api.fixture.invalid/path","method":"POST","headers":{"Content-Type":"` + contentType + `"},"body_base64":"e30="}`
			response := executeHostHTTPFetch(t, bridge, []byte(raw))
			if !response.OK {
				t.Fatalf("response = %+v, want success", response)
			}
			if transport.Count() != 1 {
				t.Fatalf("transport calls = %d, want 1", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchAuthProfileMutex(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{name: "post", raw: `{"url":"https://api.fixture.invalid/path","method":"POST","auth_profile_ref":"default"}`},
		{name: "body", raw: `{"url":"https://api.fixture.invalid/path","method":"POST","auth_profile_ref":"default","headers":{"Content-Type":"application/json"},"body_base64":"e30="}`},
		{name: "privileged authorization", raw: `{"url":"https://api.fixture.invalid/path","auth_profile_ref":"default","headers":{"Authorization":"Bearer fixture-pack-auth"}}`},
		{name: "privileged x- header", raw: `{"url":"https://api.fixture.invalid/path","auth_profile_ref":"default","headers":{"X-Foo":"fixture-foo"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
			broker := testHTTPBroker(transport, hostImportAuthResolver{secret: ResolvedAuthSecret{
				HeaderName: "Authorization", HeaderValue: "Bearer profile-secret", Kind: AuthSecretKindBearer,
			}})
			bridge := newTestHostImportBridge(t, hostImportExtendedManifest(), broker, nil, 4)

			response := executeHostHTTPFetch(t, bridge, []byte(tt.raw))
			if response.OK || response.ErrorCode != "invalid_request" {
				t.Fatalf("response = %+v, want invalid_request", response)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchExtendedPOSTSuccessLegacyMode(t *testing.T) {
	transport := &hostImportRecordingTransport{statusCode: 200, body: "stored"}
	bridge := newTestHostImportBridge(t, hostImportExtendedManifest(), testHTTPBroker(transport, nil), nil, 4)

	response := executeHostHTTPFetch(t, bridge, mustHostImportJSON(t, HostHTTPFetchRequest{
		Method: "POST",
		URL:    "https://api.fixture.invalid/path",
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer fixture-pack-auth",
			"X-Foo":         "fixture-foo",
		},
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"k":"v"}`)),
	}))
	if !response.OK {
		t.Fatalf("response = %+v, want success", response)
	}
	if response.BodyBase64 != base64.StdEncoding.EncodeToString([]byte("stored")) {
		t.Fatalf("response body_base64 = %q, want transport body", response.BodyBase64)
	}
	if response.StatusCode != http.StatusOK || response.FinalURL != "https://api.fixture.invalid/path" {
		t.Fatalf("response = %+v", response)
	}
	request := transport.LastRequest()
	if request == nil {
		t.Fatal("transport request = nil")
	}
	if request.Method != http.MethodPost {
		t.Fatalf("transport method = %q, want POST", request.Method)
	}
	if request.Header.Get("Authorization") != "Bearer fixture-pack-auth" {
		t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
	}
	if request.Header.Get("X-Foo") != "fixture-foo" {
		t.Fatalf("X-Foo = %q", request.Header.Get("X-Foo"))
	}
	if body := transportRequestBody(t, request); string(body) != `{"k":"v"}` {
		t.Fatalf("transport body = %q", body)
	}
}

func TestHostImportHTTPFetchRejectsExtendedFeaturesWithoutCapability(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  string
	}{
		{name: "post", raw: `{"url":"https://api.fixture.invalid/path","method":"POST"}`},
		{name: "body", raw: `{"url":"https://api.fixture.invalid/path","method":"POST","headers":{"Content-Type":"application/json"},"body_base64":"e30="}`},
		{name: "authorization", raw: `{"url":"https://api.fixture.invalid/path","headers":{"Authorization":"Bearer x"}}`},
		{name: "x- header", raw: `{"url":"https://api.fixture.invalid/path","headers":{"X-Foo":"x"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
			bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(transport, nil), nil, 4)

			response := executeHostHTTPFetch(t, bridge, []byte(tt.raw))
			if response.OK || response.ErrorCode != "policy_denied" {
				t.Fatalf("response = %+v, want policy_denied", response)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchRefModeExtendedPOSTSuccess(t *testing.T) {
	pack := syntheticExtendedAliasPack()
	transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
	resolver := &fakeHostPolicyResolver{policy: syntheticExtendedHostPolicy(pack.Identity)}
	broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
	bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, resolver, 4)

	response := executeHostHTTPFetch(t, bridge, mustHostImportJSON(t, HostHTTPFetchRequest{
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "ep-alpha001",
		Method:          "POST",
		Params:          map[string]string{"id": "fixture-item"},
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer fixture-pack-auth",
		},
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"k":"v"}`)),
	}))
	if !response.OK {
		t.Fatalf("response = %+v, want success", response)
	}
	if transport.Count() != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.Count())
	}
	request := transport.LastRequest()
	if request.Method != http.MethodPost {
		t.Fatalf("transport method = %q, want POST", request.Method)
	}
	if got := request.URL.Path; !strings.HasSuffix(got, "/files/fixture-item") {
		t.Fatalf("transport url path = %q, want params-expanded /files/fixture-item", got)
	}
	body := transportRequestBody(t, request)
	if string(body) != `{"k":"v"}` {
		t.Fatalf("transport body = %q, want verbatim request body", body)
	}
	if strings.Contains(string(body), "fixture-item") {
		t.Fatal("transport body unexpectedly contains the expanded param value")
	}
}

func TestHostImportHTTPFetchRefModeRejectsPOSTWithoutEndpointMethod(t *testing.T) {
	pack := syntheticExtendedAliasPack()
	for _, tt := range []struct {
		name    string
		methods []string
	}{
		{name: "get head only", methods: []string{"GET", "HEAD"}},
		{name: "empty methods", methods: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy := syntheticExtendedHostPolicy(pack.Identity)
			policy.Endpoints[0].Methods = tt.methods
			resolver := &fakeHostPolicyResolver{policy: policy}
			transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
			broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
			bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, resolver, 4)

			response := executeHostHTTPFetch(t, bridge, mustHostImportJSON(t, HostHTTPFetchRequest{
				BrokerPolicyRef: "bpr-alpha001",
				EndpointRef:     "ep-alpha001",
				Method:          "POST",
				Params:          map[string]string{"id": "fixture-item"},
			}))
			if response.OK || response.ErrorCode != "policy_denied" {
				t.Fatalf("response = %+v, want policy_denied", response)
			}
			if resolver.calls != 1 {
				t.Fatalf("resolver calls = %d, want 1", resolver.calls)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchRefModeRejectsPOSTWithoutExtendedPolicyCapability(t *testing.T) {
	pack := syntheticExtendedAliasPack()
	policy := syntheticHostPolicy(pack.Identity)
	policy.Endpoints[0].Methods = []string{"GET", "HEAD", "POST"}
	resolver := &fakeHostPolicyResolver{policy: policy}
	transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
	broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
	bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, resolver, 4)

	response := executeHostHTTPFetch(t, bridge, mustHostImportJSON(t, HostHTTPFetchRequest{
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "ep-alpha001",
		Method:          "POST",
		Params:          map[string]string{"id": "fixture-item"},
	}))
	if response.OK || response.ErrorCode != "policy_denied" {
		t.Fatalf("response = %+v, want policy_denied", response)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestHostImportHTTPFetchRefModeRejectsAuthProfileWithExtendedFeatures(t *testing.T) {
	pack := syntheticExtendedAliasPack()
	for _, tt := range []struct {
		name string
		req  HostHTTPFetchRequest
	}{
		{name: "post", req: HostHTTPFetchRequest{
			BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-alpha001",
			Method: "POST", AuthProfileRef: "alpha-secret",
		}},
		{name: "body", req: HostHTTPFetchRequest{
			BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-alpha001",
			Method: "POST", AuthProfileRef: "alpha-secret",
			Headers: map[string]string{"Content-Type": "application/json"}, BodyBase64: "e30=",
		}},
		{name: "privileged authorization", req: HostHTTPFetchRequest{
			BrokerPolicyRef: "bpr-alpha001", EndpointRef: "ep-alpha001",
			AuthProfileRef: "alpha-secret",
			Headers:        map[string]string{"Authorization": "Bearer fixture-pack-auth"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeHostPolicyResolver{policy: syntheticExtendedHostPolicy(pack.Identity)}
			transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
			broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
			bridge := newTestHostImportBridgeForPack(t, pack, broker, hostImportAuthResolver{secret: ResolvedAuthSecret{
				HeaderName: "Authorization", HeaderValue: "Bearer profile-secret", Kind: AuthSecretKindBearer,
			}}, resolver, 4)

			response := executeHostHTTPFetch(t, bridge, mustHostImportJSON(t, tt.req))
			if response.OK || response.ErrorCode != "invalid_request" {
				t.Fatalf("response = %+v, want invalid_request", response)
			}
			// The credential-channel mutex fires in local shape validation
			// before mode dispatch, so the policy resolver is never consulted.
			if resolver.calls != 0 {
				t.Fatalf("resolver calls = %d, want 0", resolver.calls)
			}
			if transport.Count() != 0 {
				t.Fatalf("transport calls = %d, want 0", transport.Count())
			}
		})
	}
}

func TestHostImportHTTPFetchRejectsRawURLWithEndpointRefAndBody(t *testing.T) {
	pack := syntheticExtendedAliasPack()
	resolver := &fakeHostPolicyResolver{policy: syntheticExtendedHostPolicy(pack.Identity)}
	transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
	broker := NewHTTPBroker(HTTPBrokerConfig{Policy: testHTTPPolicy(), Transport: transport, HostPolicyResolver: resolver})
	bridge := newTestHostImportBridgeForPack(t, pack, broker, nil, resolver, 4)

	response := executeHostHTTPFetch(t, bridge, []byte(`{
		"url":"https://api.fixture.invalid/path",
		"broker_policy_ref":"bpr-alpha001",
		"endpoint_ref":"ep-alpha001",
		"method":"POST",
		"headers":{"Content-Type":"application/json"},
		"body_base64":"e30="
	}`))
	if response.OK || response.ErrorCode != "invalid_request" {
		t.Fatalf("response = %+v, want invalid_request", response)
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0", resolver.calls)
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestHostImportAuthProfileStatusRejectsBodyBase64Field(t *testing.T) {
	bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(&hostImportRecordingTransport{}, nil), nil, 4)

	raw := bridge.executeAuthProfileStatus(context.Background(), []byte(`{"auth_profile_ref":"alpha-secret","body_base64":"e30="}`))
	var response HostAuthProfileStatusResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK || response.ErrorCode != "invalid_request" {
		t.Fatalf("response = %+v, want invalid_request", response)
	}
}

func TestHostImportHTTPFetchExtendedConsumesHostCallBudget(t *testing.T) {
	transport := &hostImportRecordingTransport{statusCode: 200, body: "ok"}
	bridge := newTestHostImportBridge(t, hostImportExtendedManifest(), testHTTPBroker(transport, nil), nil, 1)

	first := executeHostHTTPFetch(t, bridge, []byte(`{"url":"https://api.fixture.invalid/path","method":"POST","headers":{"Content-Type":"application/json"},"body_base64":"e30="}`))
	if !first.OK {
		t.Fatalf("first response = %+v, want success", first)
	}
	second := executeHostHTTPFetch(t, bridge, []byte(`{"url":"https://api.fixture.invalid/path"}`))
	if second.OK || second.ErrorCode != "budget_exhausted" {
		t.Fatalf("second response = %+v, want budget_exhausted", second)
	}
	if transport.Count() != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.Count())
	}
}

func TestHostImportHTTPFetchExtendedErrorsStayStatic(t *testing.T) {
	const sentinel = "fixture-pack-auth"
	leaking := testHTTPBroker(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed carrying " + sentinel)
	}), nil)
	bridge := newTestHostImportBridge(t, hostImportExtendedManifest(), leaking, nil, 4)

	raw := bridge.executeHTTPFetch(context.Background(), mustHostImportJSON(t, HostHTTPFetchRequest{
		Method: "POST",
		URL:    "https://api.fixture.invalid/path",
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer " + sentinel,
		},
		BodyBase64: "e30=",
	}))
	var response HostHTTPFetchResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK || response.ErrorCode != "fetch_failed" {
		t.Fatalf("response = %+v, want fetch_failed", response)
	}
	if response.Message != "fetch failed" {
		t.Fatalf("response message = %q, want static fetch failure", response.Message)
	}
	if strings.Contains(string(raw), sentinel) || strings.Contains(string(raw), "dial failed") {
		t.Fatalf("response leaked transport detail: raw=%s", raw)
	}
}
