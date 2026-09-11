//go:build extractor

package wailsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

func fixtureGrantMap(t *testing.T, mutate func(map[string]any)) map[string]any {
	t.Helper()
	nowMs := time.Now().UnixMilli()
	obj := map[string]any{
		"source_origin":       "https://share.fixture.invalid",
		"target_url":          packbuilder.HostCallFixtureAPIURL,
		"method":              "GET",
		"captured_at_unix_ms": nowMs - 500,
		"expires_at_unix_ms":  nowMs + 30_000,
		"headers":             []any{map[string]any{"name": "x-fixture-token", "value": "fixture-x"}},
	}
	if mutate != nil {
		mutate(obj)
	}
	return obj
}

func resolveRawWithGrants(t *testing.T, grantsJSON string) json.RawMessage {
	t.Helper()
	raw := fmt.Sprintf(`{"type":"extractor_resolve","request_id":"r-g","source_url":%q,"cookies":[{"name":"sid","value":"browser-sid","domain":".fixture.invalid","path":"/","secure":true,"host_only":false}],"browser_header_grants":%s}`,
		packbuilder.HostCallFixtureShareURL, grantsJSON)
	return json.RawMessage(raw)
}

func grantedResolveCtx() context.Context {
	return extension.WithHeaderContextGrant(context.Background(), true)
}

func TestExtensionResolver_BrowserHeaderGrantWireShapes(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	lease := newExtensionResolveAdapter(dispatcher)

	type table struct {
		name      string
		grantsRaw string
		raw       string
		wantErr   string
	}
	cases := []table{
		{name: "null", grantsRaw: `null`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "empty array tolerated", grantsRaw: `[]`},
		{name: "object not array", grantsRaw: `{}`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "string not array", grantsRaw: `"x"`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "number not array", grantsRaw: `123`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "grant missing headers key", grantsRaw: `[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/resolve/fixture-item","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2}]`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "grant extra key", grantsRaw: `[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/resolve/fixture-item","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2,"headers":[],"extra":1}]`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "grant headers null", grantsRaw: `[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/resolve/fixture-item","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2,"headers":null}]`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "header missing value key", grantsRaw: `[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/resolve/fixture-item","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2,"headers":[{"name":"x-a"}]}]`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "header extra key", grantsRaw: `[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/resolve/fixture-item","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2,"headers":[{"name":"x-a","value":"v","x":1}]}]`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "duplicate grant key", grantsRaw: `[{"source_origin":"https://share.fixture.invalid","source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/resolve/fixture-item","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2,"headers":[{"name":"x-a","value":"v"}]}]`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "duplicate header key", grantsRaw: `[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/resolve/fixture-item","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2,"headers":[{"name":"x-a","name":"x-a","value":"v"}]}]`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "variant key name", raw: `{"type":"extractor_resolve","request_id":"r-g","source_url":"https://share.fixture.invalid/s/fixture-item","Browser_Header_Grants":[]}`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "two variant keys", raw: `{"type":"extractor_resolve","request_id":"r-g","source_url":"https://share.fixture.invalid/s/fixture-item","browser_header_grants":[],"Browser_Header_Grants":[]}`, wantErr: extension.ErrCodeInvalidRequest},
		{name: "string captured", grantsRaw: `[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/resolve/fixture-item","method":"GET","captured_at_unix_ms":"1","expires_at_unix_ms":2,"headers":[{"name":"x-a","value":"v"}]}]`, wantErr: extension.ErrCodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			if raw == "" {
				raw = string(resolveRawWithGrants(t, tc.grantsRaw))
			}
			result := lease.HandleResolve(grantedResolveCtx(), extension.RequestEnvelope{RequestID: "r-g"}, json.RawMessage(raw))
			if tc.wantErr == "" {
				if result.ErrorCode == extension.ErrCodeInvalidRequest {
					t.Fatalf("error_code = invalid_request, want tolerated")
				}
				return
			}
			if result.ErrorCode != tc.wantErr {
				t.Fatalf("error_code = %q, want %q", result.ErrorCode, tc.wantErr)
			}
		})
	}
}

func TestExtensionResolver_BrowserHeaderGrantValidationWireCases(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	lease := newExtensionResolveAdapter(dispatcher)

	mutators := map[string]func(map[string]any){
		"method POST":              func(m map[string]any) { m["method"] = "POST" },
		"method lower":             func(m map[string]any) { m["method"] = "get" },
		"target http":              func(m map[string]any) { m["target_url"] = "http://api.fixture.invalid/resolve/fixture-item" },
		"source mismatch":          func(m map[string]any) { m["source_origin"] = "https://other.fixture.invalid" },
		"source not canonical":     func(m map[string]any) { m["source_origin"] = "https://SHARE.fixture.invalid" },
		"grant expired":            func(m map[string]any) { m["expires_at_unix_ms"] = time.Now().Add(-time.Second).UnixMilli() },
		"captured beyond skew":     func(m map[string]any) { m["captured_at_unix_ms"] = time.Now().Add(2 * time.Minute).UnixMilli() },
		"ttl too long":             func(m map[string]any) { m["expires_at_unix_ms"] = m["captured_at_unix_ms"].(int64) + 61_000 },
		"header cookie ineligible": func(m map[string]any) { m["headers"] = []any{map[string]any{"name": "cookie", "value": "sid=1"}} },
		"header name uppercase":    func(m map[string]any) { m["headers"] = []any{map[string]any{"name": "X-Fixture", "value": "v"}} },
		"header denied prefix": func(m map[string]any) {
			m["headers"] = []any{map[string]any{"name": "x-forwarded-for", "value": "1.2.3.4"}}
		},
		"header value whitespace": func(m map[string]any) { m["headers"] = []any{map[string]any{"name": "x-a", "value": " v"}} },
		"authorization basic": func(m map[string]any) {
			m["headers"] = []any{map[string]any{"name": "authorization", "value": "Basic dXNlcg=="}}
		},
		"authorization bare token": func(m map[string]any) {
			m["headers"] = []any{map[string]any{"name": "authorization", "value": "justtoken"}}
		},
	}
	for name, mutate := range mutators {
		t.Run(name, func(t *testing.T) {
			obj := fixtureGrantMap(t, mutate)
			raw, err := json.Marshal([]any{obj})
			if err != nil {
				t.Fatalf("marshal grant: %v", err)
			}
			result := lease.HandleResolve(grantedResolveCtx(), extension.RequestEnvelope{RequestID: "r-g"}, resolveRawWithGrants(t, string(raw)))
			if result.ErrorCode != extension.ErrCodeInvalidRequest {
				t.Fatalf("error_code = %q, want invalid_request", result.ErrorCode)
			}
		})
	}
}

func TestExtensionResolver_NineGrantsAndDuplicateScopeRejected(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	lease := newExtensionResolveAdapter(dispatcher)

	var nine []any
	for i := range 9 {
		nine = append(nine, fixtureGrantMap(t, func(m map[string]any) {
			m["target_url"] = fmt.Sprintf("https://api.fixture.invalid/resolve/fixture-item?n=%d", i)
		}))
	}
	raw, err := json.Marshal(nine)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result := lease.HandleResolve(grantedResolveCtx(), extension.RequestEnvelope{}, resolveRawWithGrants(t, string(raw)))
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("9 grants error_code = %q", result.ErrorCode)
	}

	dupScope, err := json.Marshal([]any{fixtureGrantMap(t, nil), fixtureGrantMap(t, nil)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result = lease.HandleResolve(grantedResolveCtx(), extension.RequestEnvelope{}, resolveRawWithGrants(t, string(dupScope)))
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("duplicate scope error_code = %q", result.ErrorCode)
	}
}

func TestExtensionResolver_UnsolicitedGrantRejectedByAdapter(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	lease := newExtensionResolveAdapter(dispatcher)
	raw, err := json.Marshal([]any{fixtureGrantMap(t, nil)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	payload := resolveRawWithGrants(t, string(raw))

	if result := lease.HandleResolve(context.Background(), extension.RequestEnvelope{}, payload); result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("no-marker ctx error_code = %q, want invalid_request", result.ErrorCode)
	}
	if result := lease.HandleResolve(extension.WithHeaderContextGrant(context.Background(), false), extension.RequestEnvelope{}, payload); result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("marker=false ctx error_code = %q, want invalid_request", result.ErrorCode)
	}
}

func TestExtensionResolver_GrantHeadersReachTransport(t *testing.T) {
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	lease := newExtensionResolveAdapter(dispatcher)
	obj := fixtureGrantMap(t, func(m map[string]any) {
		m["headers"] = []any{
			map[string]any{"name": "authorization", "value": "Bearer fixture-grant-secret"},
			map[string]any{"name": "x-fixture-token", "value": "fixture-x-token-value"},
		}
	})
	grantsRaw, err := json.Marshal([]any{obj})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := fmt.Sprintf(`{"type":"extractor_resolve","request_id":"r-g","source_url":%q,"cookies":[{"name":"sid","value":"browser-sid","domain":".fixture.invalid","path":"/","secure":true,"host_only":false}],"user_agent":"fixture-ua","accept_language":"en-US","referer":%q,"browser_header_grants":%s}`,
		packbuilder.HostCallFixtureShareURL, packbuilder.HostCallFixtureShareURL, string(grantsRaw))

	result := lease.HandleResolve(grantedResolveCtx(), extension.RequestEnvelope{}, json.RawMessage(raw))
	if result.ErrorCode != "" || !result.Matched {
		t.Fatalf("resolve = %+v", result)
	}
	headers := transport.Headers()
	var grantHop http.Header
	for _, h := range headers {
		if h.Get("X-Fixture-Token") != "" || h.Get("Authorization") != "" {
			grantHop = h
		}
	}
	if grantHop == nil {
		t.Fatalf("no hop carried grant headers: %#v", headers)
	}
	if grantHop.Get("Authorization") != "Bearer fixture-grant-secret" {
		t.Fatalf("Authorization = %q", grantHop.Get("Authorization"))
	}
	if grantHop.Get("X-Fixture-Token") != "fixture-x-token-value" {
		t.Fatalf("X-Fixture-Token = %q", grantHop.Get("X-Fixture-Token"))
	}
	if grantHop.Get("Cookie") != "" {
		t.Fatalf("grant hop must not carry Cookie: %q", grantHop.Get("Cookie"))
	}
	if grantHop.Get("Referer") != "https://share.fixture.invalid/" {
		t.Fatalf("Referer = %q, want source origin with trailing slash", grantHop.Get("Referer"))
	}
	if grantHop.Get("User-Agent") != "fixture-ua" {
		t.Fatalf("User-Agent = %q", grantHop.Get("User-Agent"))
	}

	ack := mustJSON(t, result)
	for _, forbidden := range []string{"fixture-grant-secret", "fixture-x-token-value", "browser-sid"} {
		if strings.Contains(string(ack), forbidden) {
			t.Fatalf("resolve result leaked %q", forbidden)
		}
	}
}

func TestExtensionResolver_GrantFlightKeyIsolation(t *testing.T) {
	block := make(chan struct{})
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`, block: block}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	lease := newExtensionResolveAdapter(dispatcher)
	ctx := grantedResolveCtx()

	build := func(value string) json.RawMessage {
		obj := fixtureGrantMap(t, func(m map[string]any) {
			m["headers"] = []any{map[string]any{"name": "x-fixture-token", "value": value}}
		})
		raw, err := json.Marshal([]any{obj})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return resolveRawWithGrants(t, string(raw))
	}
	rawA := build("fixture-grant-a")
	rawB := build("fixture-grant-b")

	var wg sync.WaitGroup
	results := make(chan extension.ResolveResult, 2)
	for _, raw := range []json.RawMessage{rawA, rawB} {
		wg.Add(1)
		go func(raw json.RawMessage) {
			defer wg.Done()
			results <- lease.HandleResolve(ctx, extension.RequestEnvelope{}, raw)
		}(raw)
	}
	waitForTransportCalls(t, transport, 2)
	close(block)
	wg.Wait()
	close(results)

	seen := map[string]bool{}
	for _, h := range transport.Headers() {
		if v := h.Get("X-Fixture-Token"); v != "" {
			seen[v] = true
		}
	}
	if !seen["fixture-grant-a"] || !seen["fixture-grant-b"] {
		t.Fatalf("different grants must not coalesce: %#v", seen)
	}
}

func TestExtensionResolver_IdenticalGrantPayloadsStillMerge(t *testing.T) {
	block := make(chan struct{})
	transport := &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`, block: block}
	dispatcher, _ := newHostCallFixtureDispatcher(t, transport)
	lease := newExtensionResolveAdapter(dispatcher)
	ctx := grantedResolveCtx()

	grantsRaw, err := json.Marshal([]any{fixtureGrantMap(t, nil)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw := resolveRawWithGrants(t, string(grantsRaw))

	var wg sync.WaitGroup
	results := make(chan extension.ResolveResult, 2)
	for range 2 {
		wg.Go(func() {
			results <- lease.HandleResolve(ctx, extension.RequestEnvelope{}, raw)
		})
	}
	waitForTransportCalls(t, transport, 1)
	close(block)
	wg.Wait()
	close(results)
	if transport.Count() != 1 {
		t.Fatalf("identical payloads must merge, transport calls = %d", transport.Count())
	}
	for result := range results {
		if result.ErrorCode != "" || !result.Matched {
			t.Fatalf("merged resolve = %+v", result)
		}
	}
}

func TestExtensionResolver_FlightKeyFormatCompatibility(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	lease := newExtensionResolveAdapter(dispatcher)

	plain := lease.flightKey(parsedResolveInput{SourceURL: "https://share.fixture.invalid/s/fixture-item"})
	if parts := strings.Split(plain, "\n"); len(parts) != 3 {
		t.Fatalf("no-context flightKey must keep the 3-segment format, got %d segments", len(parts))
	}

	withUA := lease.flightKey(parsedResolveInput{
		SourceURL: "https://share.fixture.invalid/s/fixture-item",
		Browser:   extractor.BrowserRequestContext{UserAgent: "ua"},
	})
	if parts := strings.Split(withUA, "\n"); len(parts) != 4 {
		t.Fatalf("typed-field flightKey must gain one fingerprint segment, got %d segments", len(parts))
	}
	if !strings.HasPrefix(withUA, strings.Split(plain, "\n")[0]+"\n") {
		t.Fatal("fingerprint segment must not shift the canonical source segment")
	}
}

func TestExtensionResolver_CanonicalOriginConsistency(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "https://EXAMPLE.com:443/x", want: "https://example.com", ok: true},
		{raw: "http://share.fixture.invalid:8080/s", want: "http://share.fixture.invalid:8080", ok: true},
		{raw: "https://127.0.0.1/x", ok: false},
		{raw: "ftp://share.fixture.invalid", ok: false},
	} {
		got, ok := canonicalOrigin(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("canonicalOrigin(%q) = %q,%v; want %q,%v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}
