package extractor

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const (
	hostImportFixtureRegisterDownloadAuth = hostImportFixture(HostImportRegisterDownloadAuth)
	hostImportFixtureHostTime             = hostImportFixture(HostImportHostTime)
)

func downloadAuthBridgeManifest() Manifest {
	manifest := hostImportManifest()
	manifest.Capabilities = []Capability{CapabilityHTTPFetch, CapabilityAuthProfile, CapabilityDownloadAuth}

	return manifest
}

func newDownloadAuthTestBridge(t *testing.T, manifest Manifest, registry *DownloadAuthRegistry, maxHostCalls uint32) *hostImportBridge {
	t.Helper()
	budget, err := NewHostCallBudget(maxHostCalls)
	if err != nil {
		t.Fatalf("NewHostCallBudget() error = %v", err)
	}

	return newHostImportBridge(VerifiedPack{Manifest: manifest}, budget, HostImportConfig{DownloadAuth: registry}, 7)
}

func TestBridgeRegisterDownloadAuthMintsRef(t *testing.T) {
	registry := NewDownloadAuthRegistry()
	bridge := newDownloadAuthTestBridge(t, downloadAuthBridgeManifest(), registry, 8)

	raw := bridge.executeRegisterDownloadAuth(context.Background(), mustHostImportJSON(t, HostRegisterDownloadAuthRequest{
		Kind:  "bearer",
		Token: "guest-session-token",
	}))
	var response HostRegisterDownloadAuthResponse
	decodeHostImportTestResponse(t, raw, &response)
	if !response.OK {
		t.Fatalf("response = %+v, want ok", response)
	}
	if err := validateDownloadAuthRef(response.DownloadAuthRef); err != nil {
		t.Fatalf("response ref %q malformed: %v", response.DownloadAuthRef, err)
	}
	if registry.EntryCount() != 1 {
		t.Fatalf("EntryCount() = %d, want 1", registry.EntryCount())
	}
	// The minted ref is owned by this invocation: binding under a foreign
	// invocation must fail.
	if err := registry.Bind(bridge.invocation+1, response.DownloadAuthRef, bridge.packIdentity, "files.fixture.invalid"); err == nil {
		t.Fatal("Bind() under foreign invocation succeeded, want rejection")
	}
}

func TestBridgeRegisterDownloadAuthRejectsWithoutCapability(t *testing.T) {
	registry := NewDownloadAuthRegistry()
	manifest := downloadAuthBridgeManifest()
	manifest.Capabilities = []Capability{CapabilityHTTPFetch}
	bridge := newDownloadAuthTestBridge(t, manifest, registry, 8)

	raw := bridge.executeRegisterDownloadAuth(context.Background(), mustHostImportJSON(t, HostRegisterDownloadAuthRequest{
		Kind:  "bearer",
		Token: "guest-session-token",
	}))
	var response HostRegisterDownloadAuthResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK || response.ErrorCode != "policy_denied" {
		t.Fatalf("response = %+v, want policy_denied", response)
	}
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want denied call stored nothing", registry.EntryCount())
	}
}

func TestBridgeRegisterDownloadAuthRequestValidation(t *testing.T) {
	cases := []struct {
		name    string
		request []byte
	}{
		{name: "wrong kind", request: mustHostImportJSON(t, HostRegisterDownloadAuthRequest{Kind: "cookie", Token: "abc"})},
		{name: "empty token", request: mustHostImportJSON(t, HostRegisterDownloadAuthRequest{Kind: "bearer", Token: ""})},
		{name: "prefixed token", request: mustHostImportJSON(t, HostRegisterDownloadAuthRequest{Kind: "bearer", Token: "Bearer abc"})},
		{name: "crlf token", request: []byte(`{"kind":"bearer","token":"ab\ncd"}`)},
		{name: "unknown field", request: []byte(`{"kind":"bearer","token":"abc","extra":1}`)},
		{name: "trailing json", request: []byte(`{"kind":"bearer","token":"abc"} {}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewDownloadAuthRegistry()
			bridge := newDownloadAuthTestBridge(t, downloadAuthBridgeManifest(), registry, 8)
			raw := bridge.executeRegisterDownloadAuth(context.Background(), tc.request)
			var response HostRegisterDownloadAuthResponse
			decodeHostImportTestResponse(t, raw, &response)
			if response.OK || response.ErrorCode != "invalid_request" {
				t.Fatalf("response = %+v, want invalid_request", response)
			}
			if registry.EntryCount() != 0 {
				t.Fatalf("EntryCount() = %d, want invalid call stored nothing", registry.EntryCount())
			}
		})
	}
}

func TestBridgeRegisterDownloadAuthPerInvocationCap(t *testing.T) {
	registry := NewDownloadAuthRegistry()
	bridge := newDownloadAuthTestBridge(t, downloadAuthBridgeManifest(), registry, 32)
	for i := range downloadAuthPendingPerInvoke {
		raw := bridge.executeRegisterDownloadAuth(context.Background(), mustHostImportJSON(t, HostRegisterDownloadAuthRequest{
			Kind:  "bearer",
			Token: "token",
		}))
		var response HostRegisterDownloadAuthResponse
		decodeHostImportTestResponse(t, raw, &response)
		if !response.OK {
			t.Fatalf("call %d response = %+v, want ok", i, response)
		}
	}
	raw := bridge.executeRegisterDownloadAuth(context.Background(), mustHostImportJSON(t, HostRegisterDownloadAuthRequest{
		Kind:  "bearer",
		Token: "token",
	}))
	var response HostRegisterDownloadAuthResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK || response.ErrorCode != "registry_full" {
		t.Fatalf("response = %+v, want registry_full", response)
	}
	if registry.EntryCount() != downloadAuthPendingPerInvoke {
		t.Fatalf("EntryCount() = %d, want %d", registry.EntryCount(), downloadAuthPendingPerInvoke)
	}
}

func TestBridgeRegisterDownloadAuthNotConfigured(t *testing.T) {
	bridge := newDownloadAuthTestBridge(t, downloadAuthBridgeManifest(), nil, 8)
	raw := bridge.executeRegisterDownloadAuth(context.Background(), mustHostImportJSON(t, HostRegisterDownloadAuthRequest{
		Kind:  "bearer",
		Token: "token",
	}))
	var response HostRegisterDownloadAuthResponse
	decodeHostImportTestResponse(t, raw, &response)
	if response.OK || response.ErrorCode != "not_configured" {
		t.Fatalf("response = %+v, want not_configured", response)
	}
}

func TestBridgeRegisterDownloadAuthConsumesBudgetOnFailure(t *testing.T) {
	registry := NewDownloadAuthRegistry()
	bridge := newDownloadAuthTestBridge(t, downloadAuthBridgeManifest(), registry, 1)

	// A failing call still spends the single budget unit.
	raw := bridge.executeRegisterDownloadAuth(context.Background(), mustHostImportJSON(t, HostRegisterDownloadAuthRequest{
		Kind:  "cookie",
		Token: "abc",
	}))
	var first HostRegisterDownloadAuthResponse
	decodeHostImportTestResponse(t, raw, &first)
	if first.OK || first.ErrorCode != "invalid_request" {
		t.Fatalf("first response = %+v, want invalid_request", first)
	}
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want failed registration stored nothing", registry.EntryCount())
	}

	raw = bridge.executeRegisterDownloadAuth(context.Background(), mustHostImportJSON(t, HostRegisterDownloadAuthRequest{
		Kind:  "bearer",
		Token: "abc",
	}))
	var second HostRegisterDownloadAuthResponse
	decodeHostImportTestResponse(t, raw, &second)
	if second.OK || second.ErrorCode != "budget_exhausted" {
		t.Fatalf("second response = %+v, want budget_exhausted", second)
	}
}

func TestBridgeHostTimeReturnsCachedSnapshot(t *testing.T) {
	bridge := newDownloadAuthTestBridge(t, downloadAuthBridgeManifest(), nil, 4)
	first := bridge.executeHostTime(context.Background(), []byte(`{}`))
	var a HostTimeResponse
	decodeHostImportTestResponse(t, first, &a)
	if !a.OK || a.UnixSecs <= 0 {
		t.Fatalf("first response = %+v, want ok with unix_secs", a)
	}
	second := bridge.executeHostTime(context.Background(), []byte(`{}`))
	var b HostTimeResponse
	decodeHostImportTestResponse(t, second, &b)
	if !b.OK || b.UnixSecs != a.UnixSecs {
		t.Fatalf("second response = %+v, want cached snapshot %d", b, a.UnixSecs)
	}
}

func TestBridgeHostTimeRejectsNonEmptyRequest(t *testing.T) {
	bridge := newDownloadAuthTestBridge(t, downloadAuthBridgeManifest(), nil, 16)
	for _, raw := range [][]byte{
		[]byte(`{"zone":"utc"}`),
		[]byte(`{} {}`),
		[]byte(``),
		[]byte(`null`),
		[]byte(`[]`),
		[]byte(`"now"`),
		[]byte(`5`),
		[]byte(`[{}]`),
	} {
		response := bridge.executeHostTime(context.Background(), raw)
		var decoded HostTimeResponse
		decodeHostImportTestResponse(t, response, &decoded)
		if decoded.OK || decoded.ErrorCode != "invalid_request" {
			t.Fatalf("request %s → %+v, want invalid_request", raw, decoded)
		}
	}
}

func TestBridgeHTTPFetchOmitBrowserContextValidation(t *testing.T) {
	bridge := newTestHostImportBridge(t, hostImportManifest(), testHTTPBroker(&hostImportRecordingTransport{statusCode: http.StatusOK}, nil), nil, 8)

	cases := []struct {
		name    string
		request string
	}{
		{name: "omit with auth profile", request: `{"url":"https://api.fixture.invalid/x","omit_browser_context":true,"auth_profile_ref":"default"}`},
		{name: "mistyped field", request: `{"url":"https://api.fixture.invalid/x","omit_browser_context":"yes"}`},
		{name: "unknown field", request: `{"url":"https://api.fixture.invalid/x","omit_browser_context":true,"surprise":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := bridge.executeHTTPFetch(context.Background(), []byte(tc.request))
			var response HostHTTPFetchResponse
			decodeHostImportTestResponse(t, raw, &response)
			if response.OK || response.ErrorCode != "invalid_request" {
				t.Fatalf("response = %+v, want invalid_request", response)
			}
		})
	}
}

func TestHTTPBrokerOmitBrowserContextSuppressesBrowserState(t *testing.T) {
	var seen http.Header
	broker := testHTTPBroker(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return textResponse(200, "ok"), nil
	}), nil)
	bc := grantBrokerContext(liveBrokerGrant(grantBrokerTarget, "GET",
		BrowserHeader{Name: "x-fixture-token", Value: grantSecretB},
	))
	ctx := WithBrowserContext(context.Background(), bc)

	_, err := broker.Fetch(ctx, HTTPFetchRequest{
		PackID:             "xpk-alpha001",
		Manifest:           alphaCookieManifest(),
		Method:             http.MethodGet,
		URL:                grantBrokerTarget,
		OmitBrowserContext: true,
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := seen.Get("Cookie"); got != "" {
		t.Fatalf("Cookie = %q, want suppressed", got)
	}
	if got := seen.Get("X-Fixture-Token"); got != "" {
		t.Fatalf("grant header = %q, want suppressed", got)
	}
	if got := seen.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %q, want suppressed", got)
	}
}

func TestHTTPBrokerOmitBrowserContextForbidsAuthProfile(t *testing.T) {
	transport := &recordingTransport{}
	resolver := fakeAuthResolver{secret: ResolvedAuthSecret{HeaderName: "Cookie", HeaderValue: "s=1", Kind: AuthSecretKindCookie}}
	broker := testHTTPBroker(transport, resolver)

	_, err := broker.Fetch(context.Background(), HTTPFetchRequest{
		PackID:             "xpk-alpha001",
		Manifest:           alphaCookieManifest(),
		Method:             http.MethodGet,
		URL:                "https://api.alpha.test/x",
		AuthProfileID:      "alpha-secret",
		OmitBrowserContext: true,
	})
	if err == nil {
		t.Fatal("Fetch() error = nil, want omit+profile rejection")
	}
	if transport.Count() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.Count())
	}
}

func TestPreflightDownloadAuthImports(t *testing.T) {
	importing := buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
		hostImports:    []hostImportFixture{hostImportFixtureRegisterDownloadAuth, hostImportFixtureHostTime},
	})

	t.Run("capability grants both imports", func(t *testing.T) {
		pack := verifiedRunnerPack(t, importing, func(values map[string]any) {
			values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityDownloadAuth)}
		})
		if err := PreflightWASMModule(context.Background(), pack); err != nil {
			t.Fatalf("PreflightWASMModule() error = %v", err)
		}
	})

	t.Run("register import requires capability", func(t *testing.T) {
		pack := verifiedRunnerPack(t, importing, func(values map[string]any) {
			values["capabilities"] = []string{string(CapabilityParseWASM)}
		})
		err := PreflightWASMModule(context.Background(), pack)
		if err == nil || !strings.Contains(err.Error(), HostImportRegisterDownloadAuth) {
			t.Fatalf("PreflightWASMModule() error = %v, want %s rejection", err, HostImportRegisterDownloadAuth)
		}
	})

	t.Run("host time needs no capability", func(t *testing.T) {
		timeOnly := buildRunnerFixtureWASM(wasmFixtureConfig{
			abiVersion:     CurrentABIVersion,
			matchJSON:      `{"matched":true}`,
			extractJSON:    `{"items":[]}`,
			memoryMinPages: 1,
			hostImports:    []hostImportFixture{hostImportFixtureHostTime},
		})
		pack := verifiedRunnerPack(t, timeOnly, func(values map[string]any) {
			values["capabilities"] = []string{string(CapabilityParseWASM)}
		})
		if err := PreflightWASMModule(context.Background(), pack); err != nil {
			t.Fatalf("PreflightWASMModule() error = %v", err)
		}
	})
}

func TestRunnerRegisterDownloadAuthStoresInvocationEntry(t *testing.T) {
	request := `{"kind":"bearer","token":"guest-token"}`
	fixture := buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
		hostImports:    []hostImportFixture{hostImportFixtureRegisterDownloadAuth},
		extractHostCalls: []hostImportFixtureCall{{
			Name:    hostImportFixtureRegisterDownloadAuth,
			Request: request,
			Count:   1,
		}},
	})
	pack := verifiedRunnerPack(t, fixture, func(values map[string]any) {
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityDownloadAuth)}
	})
	registry := NewDownloadAuthRegistry()
	runner := NewRunnerWithConfig(RunnerConfig{DownloadAuth: registry})

	output, err := runner.Extract(context.Background(), pack, ExtractInput{URL: "https://share.fixture.invalid/s/abc"})
	if err != nil {
		t.Fatalf("Runner.Extract() error = %v", err)
	}
	if output.InvocationID() == 0 {
		t.Fatal("ExtractOutput.InvocationID() = 0, want scoped invocation")
	}
	if registry.EntryCount() != 1 {
		t.Fatalf("EntryCount() = %d, want pending registration", registry.EntryCount())
	}
	// Committed end with no bound item purges the pending entry.
	registry.EndInvocation(output.InvocationID(), true)
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want unbound entry purged", registry.EntryCount())
	}
}

func TestRunnerRegisterDownloadAuthDeniedWithoutCapability(t *testing.T) {
	request := `{"kind":"bearer","token":"guest-token"}`
	fixture := buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion:     CurrentABIVersion,
		matchJSON:      `{"matched":true}`,
		extractJSON:    `{"items":[]}`,
		memoryMinPages: 1,
		hostImports:    []hostImportFixture{hostImportFixtureRegisterDownloadAuth},
		extractHostCalls: []hostImportFixtureCall{{
			Name:    hostImportFixtureRegisterDownloadAuth,
			Request: request,
			Count:   1,
		}},
	})
	pack := verifiedRunnerPack(t, fixture, func(values map[string]any) {
		values["capabilities"] = []string{string(CapabilityParseWASM)}
	})
	registry := NewDownloadAuthRegistry()
	runner := NewRunnerWithConfig(RunnerConfig{DownloadAuth: registry})

	// The fixture ignores the denied response; the run must succeed with no
	// stored entry and no panic.
	if _, err := runner.Extract(context.Background(), pack, ExtractInput{URL: "https://share.fixture.invalid/s/abc"}); err != nil {
		t.Fatalf("Runner.Extract() error = %v", err)
	}
	if registry.EntryCount() != 0 {
		t.Fatalf("EntryCount() = %d, want denied registration stored nothing", registry.EntryCount())
	}
}

func TestRunnerExtractRejectsMalformedDownloadAuthRef(t *testing.T) {
	fixture := buildRunnerFixtureWASM(wasmFixtureConfig{
		abiVersion: CurrentABIVersion,
		matchJSON:  `{"matched":true}`,
		extractJSON: `{"items":[{"url":"https://download.fixture.invalid/file.bin",` +
			`"download_auth_ref":"raw-token-not-an-opaque-ref"}]}`,
		memoryMinPages: 1,
	})
	pack := verifiedRunnerPack(t, fixture, func(values map[string]any) {
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityDownloadAuth)}
	})
	runner := NewRunnerWithConfig(RunnerConfig{DownloadAuth: NewDownloadAuthRegistry()})

	if _, err := runner.Extract(context.Background(), pack, ExtractInput{URL: "https://share.fixture.invalid/s/abc"}); err == nil {
		t.Fatal("Runner.Extract() error = nil, want malformed download_auth_ref rejection")
	}
}
