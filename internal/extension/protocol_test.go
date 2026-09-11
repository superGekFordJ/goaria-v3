package extension

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBrowserHeaderGrantCapabilityConstant(t *testing.T) {
	if CapExtractorHeaderContext != "extractor.header_context" {
		t.Fatalf("CapExtractorHeaderContext = %q, want %q", CapExtractorHeaderContext, "extractor.header_context")
	}
}

func TestBrowserHeaderGrantWireShape(t *testing.T) {
	req := ExtractorResolveRequest{
		Type:      MsgTypeExtractorResolve,
		RequestID: "9f1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d",
		SourceURL: "https://share.alpha.test/item",
		Cookies:   []BrowserCookie{},
		BrowserHeaderGrants: []BrowserHeaderGrant{{
			SourceOrigin:     "https://share.alpha.test",
			TargetURL:        "https://api.alpha.test/v1/item?id=fixture",
			Method:           "GET",
			CapturedAtUnixMs: 1735689600123,
			ExpiresAtUnixMs:  1735689660123,
			Headers: []BrowserHeader{
				{Name: "authorization", Value: "fixture-value-a"},
				{Name: "x-request-proof", Value: "fixture-value-b"},
			},
		}},
	}
	raw := mustMarshal(t, req)

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("unmarshal resolve request: %v", err)
	}
	grantsRaw, ok := rawMap["browser_header_grants"]
	if !ok {
		t.Fatalf("browser_header_grants missing: %s", raw)
	}
	var grants []map[string]json.RawMessage
	if err := json.Unmarshal(grantsRaw, &grants); err != nil {
		t.Fatalf("browser_header_grants must be an array: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}
	for _, key := range []string{"source_origin", "target_url", "method", "captured_at_unix_ms", "expires_at_unix_ms", "headers"} {
		if _, ok := grants[0][key]; !ok {
			t.Fatalf("grant missing %s: %s", key, grantsRaw)
		}
	}
	if len(grants[0]) != 6 {
		t.Fatalf("grant carries extra keys: %s", grantsRaw)
	}
	var headers []map[string]json.RawMessage
	if err := json.Unmarshal(grants[0]["headers"], &headers); err != nil {
		t.Fatalf("headers must be a non-null array: %v", err)
	}
	if len(headers) != 2 {
		t.Fatalf("header count = %d, want 2", len(headers))
	}
	for i, h := range headers {
		if len(h) != 2 {
			t.Fatalf("header %d carries extra keys: %v", i, h)
		}
		if _, ok := h["name"]; !ok {
			t.Fatalf("header %d missing name", i)
		}
		if _, ok := h["value"]; !ok {
			t.Fatalf("header %d missing value", i)
		}
	}

	var decoded ExtractorResolveRequest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	got := decoded.BrowserHeaderGrants[0]
	if got.CapturedAtUnixMs != 1735689600123 || got.ExpiresAtUnixMs != 1735689660123 {
		t.Fatalf("timestamps not lossless: %+v", got)
	}
	if got.Headers[0].Name != "authorization" || got.Headers[1].Name != "x-request-proof" {
		t.Fatalf("header names reordered or lost: %+v", got.Headers)
	}
}

func TestBrowserHeaderGrantOmittedWhenEmpty(t *testing.T) {
	base := ExtractorResolveRequest{
		Type:      MsgTypeExtractorResolve,
		SourceURL: "https://share.alpha.test/item",
		Cookies:   []BrowserCookie{},
	}
	for name, req := range map[string]ExtractorResolveRequest{
		"nil":   base,
		"empty": {Type: base.Type, SourceURL: base.SourceURL, Cookies: base.Cookies, BrowserHeaderGrants: []BrowserHeaderGrant{}},
	} {
		raw := mustMarshal(t, req)
		if bytes.Contains(raw, []byte("browser_header_grants")) {
			t.Fatalf("%s grants must be omitted, got %s", name, raw)
		}
	}
}
