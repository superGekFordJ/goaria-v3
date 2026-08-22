//go:build extractor

package wailsapp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

func TestExtensionResolver_HostCallFixtureOpaqueAck(t *testing.T) {
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	adapter := newExtensionResolveAdapter(dispatcher)

	hostOnly := false
	raw := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name:     "sid",
			Value:    "browser-sid",
			Domain:   ".fixture.invalid",
			Path:     "/",
			Secure:   boolPtr(true),
			HostOnly: &hostOnly,
		}},
	})
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode != "" || !result.Matched || result.SessionID == "" || len(result.Items) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Items[0].MimeType != "application/octet-stream" {
		t.Fatalf("mime = %q", result.Items[0].MimeType)
	}
	if result.Items[0].ItemID == "fixture-item" {
		t.Fatal("item_id must not reuse pack item id")
	}
	if transport.Count() != 1 {
		t.Fatalf("transport calls = %d, want 1", transport.Count())
	}
	if got := transport.LastURL(); got != packbuilder.HostCallFixtureAPIURL {
		t.Fatalf("broker URL = %q", got)
	}
	if got := transport.LastCookie(); got != "sid=browser-sid" {
		t.Fatalf("api hop Cookie = %q", got)
	}

	ackRaw := mustJSON(t, extension.ExtractorResolveAck{
		Type:       extension.MsgTypeExtractorResolveAck,
		Matched:    boolPtr(result.Matched),
		SessionID:  result.SessionID,
		TotalCount: result.TotalCount,
		TotalBytes: result.TotalBytes,
		Items: []extension.ExtractorResolveAckItem{{
			ItemID:    result.Items[0].ItemID,
			Filename:  result.Items[0].Filename,
			SizeBytes: result.Items[0].SizeBytes,
			MimeType:  result.Items[0].MimeType,
		}},
	})
	for _, forbidden := range []string{"download.fixture.invalid", "https://", "http://", packbuilder.HostCallFixtureItemURL} {
		if bytes.Contains(ackRaw, []byte(forbidden)) {
			t.Fatalf("ack leaked %q: %s", forbidden, ackRaw)
		}
	}

	leased, ok := adapter.lookupLeasedItem(result.SessionID, result.Items[0].ItemID)
	if !ok {
		t.Fatal("lease miss")
	}
	if leased.URL != packbuilder.HostCallFixtureItemURL {
		t.Fatalf("lease URL = %q", leased.URL)
	}
	if leased.MimeType != "application/octet-stream" {
		t.Fatalf("lease mime = %q", leased.MimeType)
	}
}

func TestExtensionResolver_UnknownSourceMatchedFalse(t *testing.T) {
	start := time.Now()
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true}`})
	adapter := newExtensionResolveAdapter(dispatcher)
	raw := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: "https://share.alpha.test/nope",
	})
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode != "" || result.Matched || result.SessionID != "" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Items) != 0 {
		t.Fatalf("items = %#v", result.Items)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("matched:false took %s", time.Since(start))
	}
}

func TestExtensionResolver_RejectsInboundHeadersAndURL(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true}`})
	adapter := newExtensionResolveAdapter(dispatcher)
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, []byte(
		`{"source_url":"https://share.alpha.test/nope","headers":["Cookie: sid=x"]}`,
	))
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("headers key: %+v", result)
	}
	result = adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, []byte(
		`{"url":"https://share.alpha.test/nope"}`,
	))
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("url key: %+v", result)
	}
}

func TestExtensionResolver_SingleflightAndCookieSplit(t *testing.T) {
	block := make(chan struct{})
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`, block: block}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	adapter := newExtensionResolveAdapter(dispatcher)

	hostOnly := false
	payloadA := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name: "sid", Value: "one", Domain: ".fixture.invalid", Path: "/", Secure: boolPtr(true), HostOnly: &hostOnly,
		}},
	})
	payloadB := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name: "sid", Value: "two", Domain: ".fixture.invalid", Path: "/", Secure: boolPtr(true), HostOnly: &hostOnly,
		}},
	})

	type outcome struct {
		result extension.ResolveResult
	}
	first := make(chan outcome, 1)
	second := make(chan outcome, 1)
	go func() {
		first <- outcome{result: adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, payloadA)}
	}()
	go func() {
		second <- outcome{result: adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, payloadA)}
	}()
	waitForTransportCalls(t, transport, 1)
	time.Sleep(50 * time.Millisecond)
	close(block)
	a := <-first
	b := <-second
	if a.result.ErrorCode != "" || b.result.ErrorCode != "" {
		t.Fatalf("in-flight results a=%+v b=%+v", a.result, b.result)
	}
	if a.result.SessionID == "" || a.result.SessionID == b.result.SessionID {
		t.Fatalf("want two session ids, got %q %q", a.result.SessionID, b.result.SessionID)
	}
	if transport.Count() != 1 {
		t.Fatalf("same cookies in-flight: transport=%d, want 1", transport.Count())
	}

	other := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, payloadB)
	if other.ErrorCode != "" || !other.Matched {
		t.Fatalf("different cookies: %+v", other)
	}
	if transport.Count() != 2 {
		t.Fatalf("different cookies: transport=%d, want 2", transport.Count())
	}
}

func TestExtensionResolver_InvalidateDropsLeaseAndRewritesCache(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	adapter := newExtensionResolveAdapter(dispatcher)
	hostOnly := false
	raw := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name: "sid", Value: "browser-sid", Domain: ".fixture.invalid", Path: "/", Secure: boolPtr(true), HostOnly: &hostOnly,
		}},
	})
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode != "" || result.SessionID == "" {
		t.Fatalf("result = %+v", result)
	}
	cached := mustJSON(t, extension.ExtractorResolveAck{
		Type:      extension.MsgTypeExtractorResolveAck,
		RequestID: "r1",
		Matched:   boolPtr(true),
		SessionID: result.SessionID,
		Items:     []extension.ExtractorResolveAckItem{{ItemID: result.Items[0].ItemID, Filename: result.Items[0].Filename}},
	})
	adapter.Invalidate()
	if _, ok := adapter.lookupLeasedItem(result.SessionID, result.Items[0].ItemID); ok {
		t.Fatal("lease must miss after Invalidate")
	}
	rewritten := adapter.RewriteCachedResolve(cached)
	var ack extension.ExtractorResolveAck
	if err := json.Unmarshal(rewritten, &ack); err != nil {
		t.Fatalf("rewrite unmarshal: %v", err)
	}
	if ack.ErrorCode != extension.ErrCodeSessionExpired || ack.RequestID != "r1" {
		t.Fatalf("rewritten = %+v", ack)
	}
}

func TestExtensionResolver_MissingHostOnlyIsInvalid(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true}`})
	adapter := newExtensionResolveAdapter(dispatcher)
	raw := []byte(`{"source_url":"https://share.fixture.invalid/s/fixture-item","cookies":[{"name":"sid","value":"x","domain":".fixture.invalid","path":"/","secure":true}]}`)
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("missing host_only: %+v", result)
	}
}

func TestExtensionResolver_MissingSecureIsInvalid(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true}`})
	adapter := newExtensionResolveAdapter(dispatcher)
	raw := []byte(`{"source_url":"https://share.fixture.invalid/s/fixture-item","cookies":[{"name":"sid","value":"x","domain":".fixture.invalid","path":"/","host_only":false}]}`)
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("missing secure: %+v", result)
	}
}

func TestExtensionResolver_EmptyCookieNameIsInvalid(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true}`})
	adapter := newExtensionResolveAdapter(dispatcher)
	raw := []byte(`{"source_url":"https://share.fixture.invalid/s/fixture-item","cookies":[{"name":"","value":"x","domain":".fixture.invalid","path":"/","secure":true,"host_only":false}]}`)
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("empty cookie name: %+v", result)
	}
}

func TestExtensionResolver_CookieOctetRejected(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	adapter := newExtensionResolveAdapter(dispatcher)
	raw := []byte(`{"source_url":"https://share.fixture.invalid/s/fixture-item","cookies":[{"name":"sid","value":"a; b","domain":".fixture.invalid","path":"/","secure":true,"host_only":false},{"name":"session","value":"ok","domain":".fixture.invalid","path":"/","secure":true,"host_only":false}]}`)
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode == extension.ErrCodeInvalidRequest {
		t.Fatalf("illegal cookie-octet should be skipped, not abort resolve: %+v", result)
	}
}

func TestExtensionResolver_UnrelatedCookieDomainSkipped(t *testing.T) {
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	adapter := newExtensionResolveAdapter(dispatcher)
	raw := []byte(`{"source_url":"https://share.fixture.invalid/s/fixture-item","cookies":[{"name":"sid","value":"cross-site","domain":".other.test","path":"/","secure":true,"host_only":false},{"name":"session","value":"ok","domain":".fixture.invalid","path":"/","secure":true,"host_only":false}]}`)
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode == extension.ErrCodeInvalidRequest {
		t.Fatalf("unrelated domain should be skipped, not abort resolve: %+v", result)
	}
	if got := transport.LastCookie(); got != "session=ok" {
		t.Fatalf("Cookie = %q, want session=ok without cross-site sid", got)
	}
}

func TestCanonicalFlightSourceURL_OmitsDefaultPorts(t *testing.T) {
	a := canonicalFlightSourceURL("https://Share.Fixture.Invalid:443/s/fixture-item")
	b := canonicalFlightSourceURL("https://share.fixture.invalid/s/fixture-item")
	if a != b {
		t.Fatalf("default https port must coalesce: %q vs %q", a, b)
	}
}

func TestExtensionResolver_InvalidateDoesNotJoinDyingFlight(t *testing.T) {
	block := make(chan struct{})
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`, block: block}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	adapter := newExtensionResolveAdapter(dispatcher)
	hostOnly := false
	raw := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name: "sid", Value: "browser-sid", Domain: ".fixture.invalid", Path: "/", Secure: boolPtr(true), HostOnly: &hostOnly,
		}},
	})

	firstDone := make(chan extension.ResolveResult, 1)
	go func() {
		firstDone <- adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	}()
	waitForTransportCalls(t, transport, 1)
	adapter.Invalidate()

	secondDone := make(chan extension.ResolveResult, 1)
	go func() {
		secondDone <- adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	}()
	waitForTransportCalls(t, transport, 2)
	close(block)
	first := <-firstDone
	second := <-secondDone
	if first.SessionID != "" {
		t.Fatalf("cancelled leader must not mint: %+v", first)
	}
	if second.ErrorCode != "" || second.SessionID == "" {
		t.Fatalf("post-invalidate resolve: %+v", second)
	}
}

func TestExtensionResolver_CanonicalCookieDomainsShareFlight(t *testing.T) {
	block := make(chan struct{})
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`, block: block}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	adapter := newExtensionResolveAdapter(dispatcher)
	hostOnly := false
	payloadDot := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name: "sid", Value: "same", Domain: ".fixture.invalid", Path: "/", Secure: boolPtr(true), HostOnly: &hostOnly,
		}},
	})
	payloadBare := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name: "sid", Value: "same", Domain: "fixture.invalid", Path: "/", Secure: boolPtr(true), HostOnly: &hostOnly,
		}},
	})

	firstDone := make(chan extension.ResolveResult, 1)
	secondDone := make(chan extension.ResolveResult, 1)
	go func() {
		firstDone <- adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, payloadDot)
	}()
	waitForTransportCalls(t, transport, 1)
	go func() {
		secondDone <- adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, payloadBare)
	}()
	time.Sleep(50 * time.Millisecond)
	if transport.Count() != 1 {
		t.Fatalf("canonical domains must share flight, transport=%d", transport.Count())
	}
	close(block)
	a := <-firstDone
	b := <-secondDone
	if a.ErrorCode != "" || b.ErrorCode != "" {
		t.Fatalf("results a=%+v b=%+v", a, b)
	}
	if a.SessionID == "" || a.SessionID == b.SessionID {
		t.Fatalf("want two session ids, got %q %q", a.SessionID, b.SessionID)
	}
}

func TestExtensionResolver_WaitersShareLastStatus(t *testing.T) {
	block := make(chan struct{})
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`, statusCode: http.StatusUnauthorized, block: block}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	adapter := newExtensionResolveAdapter(dispatcher)
	hostOnly := false
	raw := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name: "sid", Value: "expired", Domain: ".fixture.invalid", Path: "/", Secure: boolPtr(true), HostOnly: &hostOnly,
		}},
	})
	firstDone := make(chan extension.ResolveResult, 1)
	secondDone := make(chan extension.ResolveResult, 1)
	go func() {
		firstDone <- adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	}()
	waitForTransportCalls(t, transport, 1)
	go func() {
		secondDone <- adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	}()
	time.Sleep(50 * time.Millisecond)
	if transport.Count() != 1 {
		t.Fatalf("same cookies must share flight, transport=%d", transport.Count())
	}
	close(block)
	a := <-firstDone
	b := <-secondDone
	if a.ErrorCode != b.ErrorCode {
		t.Fatalf("waiter classification diverged: a=%+v b=%+v", a, b)
	}
}

func TestMapResolveError_AuthExpiredUsesLeaderStatus(t *testing.T) {
	if got := mapResolveError(extractor.ErrGenericAuthResolution, http.StatusUnauthorized); got.ErrorCode != extension.ErrCodeAuthExpired {
		t.Fatalf("401: %+v", got)
	}
	if got := mapResolveError(extractor.ErrGenericAuthResolution, http.StatusForbidden); got.ErrorCode != extension.ErrCodePackError {
		t.Fatalf("403: %+v", got)
	}
	if got := mapResolveError(extractor.ErrGenericAuthResolution, 0); got.ErrorCode != extension.ErrCodePackError {
		t.Fatalf("missing status: %+v", got)
	}
}

func TestExtensionResolver_LookupEvictsExpiredSession(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	adapter := newExtensionResolveAdapter(dispatcher)
	hostOnly := false
	raw := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name: "sid", Value: "browser-sid", Domain: ".fixture.invalid", Path: "/", Secure: boolPtr(true), HostOnly: &hostOnly,
		}},
	})
	result := adapter.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.SessionID == "" {
		t.Fatalf("result = %+v", result)
	}
	adapter.mu.Lock()
	session := adapter.sessions[result.SessionID]
	session.inserted = time.Now().Add(-resolveSessionTTL - time.Second)
	adapter.mu.Unlock()
	if _, ok := adapter.lookupLeasedItem(result.SessionID, result.Items[0].ItemID); ok {
		t.Fatal("expired lease must miss")
	}
	adapter.mu.Lock()
	_, exists := adapter.sessions[result.SessionID]
	adapter.mu.Unlock()
	if exists {
		t.Fatal("expired session must be deleted")
	}
}

func TestExtensionResolver_InsertSessionEvictsLeastRecentlyUsedAtLimit(t *testing.T) {
	adapter := newExtensionResolveAdapter(nil)
	base := time.Now()
	adapter.mu.Lock()
	for i := 0; i < maxResolveSessions; i++ {
		id := string(rune('a' + i))
		adapter.insertSessionLocked(id, &leasedResolveSession{
			inserted: base,
			lastUsed: base.Add(time.Duration(i) * time.Second),
		})
	}
	adapter.insertSessionLocked("newest", &leasedResolveSession{
		inserted: base,
		lastUsed: base.Add(maxResolveSessions * time.Second),
	})
	_, oldestExists := adapter.sessions["a"]
	_, nextExists := adapter.sessions["b"]
	_, newestExists := adapter.sessions["newest"]
	count := len(adapter.sessions)
	adapter.mu.Unlock()

	if count != maxResolveSessions {
		t.Fatalf("session count = %d, want %d", count, maxResolveSessions)
	}
	if oldestExists {
		t.Fatal("least recently used session must be evicted")
	}
	if !nextExists || !newestExists {
		t.Fatal("newer sessions must remain after LRU eviction")
	}
}

func TestSanitizeAckMime_TypeSubtypeOnly(t *testing.T) {
	if got := sanitizeAckMime("application/octet-stream"); got != "application/octet-stream" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeAckMime("https://share.alpha.test/x"); got != "" {
		t.Fatalf("url mime leaked %q", got)
	}
	if got := sanitizeAckMime("text/plain; charset=utf-8"); got != "" {
		t.Fatalf("params leaked %q", got)
	}
}

func newHostCallFixtureDispatcher(t *testing.T, transport http.RoundTripper) (*extractor.AddTaskDispatcher, extractor.VerifiedPack) {
	t.Helper()
	assets, err := packbuilder.BuildSignedHostCallFixture()
	if err != nil {
		t.Fatalf("BuildSignedHostCallFixture() error = %v", err)
	}
	pack, err := extractor.VerifyEmbeddedPack(extractor.EmbeddedPack{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}, fixtureTrustPolicy(assets.PublicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	resolver := fixtureHostPolicy(pack)
	registry, rejections := extractor.NewRegistryWithHostPolicyResolver([]extractor.EmbeddedPack{{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}}, fixtureTrustPolicy(assets.PublicKey), resolver)
	if len(rejections) != 0 {
		t.Fatalf("rejections = %#v", rejections)
	}
	runner := extractor.NewRunnerWithConfig(extractor.RunnerConfig{
		HTTPBroker:         extractor.NewHTTPBroker(extractor.HTTPBrokerConfig{Transport: transport, HostPolicyResolver: resolver}),
		HostPolicyResolver: resolver,
	})
	return extractor.NewAddTaskDispatcher(extractor.AddTaskDispatcherConfig{
		Registry: registry,
		Runner:   runner,
	}), pack
}

func fixtureTrustPolicy(publicKey ed25519.PublicKey) extractor.TrustPolicy {
	policy := extractor.DefaultTrustPolicy()
	policy.TrustedPublicKeys = []ed25519.PublicKey{publicKey}

	return policy
}

func fixtureHostPolicy(pack extractor.VerifiedPack) extractor.HostPolicyResolver {
	return fixtureHostPolicyResolver{policy: extractor.ResolvedHostPolicy{
		PolicyID:            "hpr-fixture001",
		PolicyVersion:       "fixture-1",
		PolicySHA256:        strings.Repeat("a", 64),
		PackIdentity:        pack.Identity,
		DomainPolicyRefs:    []string{packbuilder.HostCallFixtureDomainPolicyRef},
		BrokerPolicyRefs:    []string{packbuilder.HostCallFixtureBrokerPolicyRef},
		AllowedCapabilities: []extractor.Capability{extractor.CapabilityParseWASM, extractor.CapabilityHTTPFetch},
		IngressDomains:      []extractor.DomainRule{{Host: "share.fixture.invalid"}},
		BrokerDomains:       []extractor.DomainRule{{Host: "api.fixture.invalid"}, {Host: "download.fixture.invalid"}},
		OutputDomains: []extractor.HostPolicyOutputRule{{
			Host:         "download.fixture.invalid",
			PathPrefixes: []string{"/"},
		}},
		Endpoints: []extractor.HostPolicyEndpoint{{
			BrokerPolicyRef:  packbuilder.HostCallFixtureBrokerPolicyRef,
			EndpointRef:      packbuilder.HostCallFixtureEndpointRef,
			URLTemplate:      packbuilder.HostCallFixtureAPIBaseURL + "/{id}",
			Methods:          []string{http.MethodGet},
			TimeoutMillis:    1000,
			MaxResponseBytes: 4096,
		}},
	}}
}

type fixtureHostPolicyResolver struct {
	policy extractor.ResolvedHostPolicy
}

func (r fixtureHostPolicyResolver) ResolveHostPolicy(context.Context, extractor.HostPolicyRequest) (extractor.ResolvedHostPolicy, error) {
	return r.policy, nil
}

type recordingCookieTransport struct {
	mu         sync.Mutex
	body       string
	statusCode int
	block      <-chan struct{}
	calls      int
	cookies    []string
	urls       []string
}

func (t *recordingCookieTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.calls++
	cookie := ""
	if req != nil {
		cookie = req.Header.Get("Cookie")
		if req.URL != nil {
			t.urls = append(t.urls, req.URL.String())
		}
	}
	t.cookies = append(t.cookies, cookie)
	body := t.body
	statusCode := t.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	block := t.block
	t.mu.Unlock()
	if block != nil {
		<-block
	}

	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (t *recordingCookieTransport) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.calls
}

func (t *recordingCookieTransport) LastURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.urls) == 0 {
		return ""
	}

	return t.urls[len(t.urls)-1]
}

func (t *recordingCookieTransport) LastCookie() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.cookies) == 0 {
		return ""
	}

	return t.cookies[len(t.cookies)-1]
}

func waitForTransportCalls(t *testing.T, transport *recordingCookieTransport, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if transport.Count() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transport calls = %d, want >= %d", transport.Count(), want)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	return raw
}

func boolPtr(v bool) *bool {
	return &v
}
