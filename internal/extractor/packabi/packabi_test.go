package packabi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPackABIConstantsMatchFrozenContract(t *testing.T) {
	if CurrentABIVersion != 1 {
		t.Fatalf("CurrentABIVersion = %d, want 1", CurrentABIVersion)
	}

	wantExports := map[string]string{
		"version": ABIExportVersion,
		"alloc":   ABIExportAlloc,
		"free":    ABIExportFree,
		"match":   ABIExportMatch,
		"extract": ABIExportExtract,
	}
	for name, got := range wantExports {
		if !strings.HasPrefix(got, "goaria_") {
			t.Fatalf("%s export = %q, want goaria_ prefix", name, got)
		}
	}

	if HostImportModule != "goaria_host" || HostImportHTTPFetch != "http_fetch" || HostImportAuthProfileStatus != "auth_profile_status" {
		t.Fatalf("host import names = %q/%q/%q", HostImportModule, HostImportHTTPFetch, HostImportAuthProfileStatus)
	}
	if CapabilityParseWASM != "cap.parse.wasm" || CapabilityHTTPFetch != "cap.http.fetch" || CapabilityAuthProfile != "cap.auth.profile" {
		t.Fatalf("capability constants drifted")
	}
}

func TestPackResultRoundTrip(t *testing.T) {
	ptr, length := UnpackResult(PackResult(0x12345678, 0x90abcdef))
	if ptr != 0x12345678 || length != 0x90abcdef {
		t.Fatalf("UnpackResult() = %#x/%#x", ptr, length)
	}
}

func TestPackABIJSONShapesUseStableSnakeCase(t *testing.T) {
	assertJSON(t, MatchInput{URL: "https://fixture.invalid/share"}, []string{`"url"`})
	assertJSON(t, MatchOutput{Matched: true, Confidence: 90, Reason: "fixture"}, []string{`"matched"`, `"confidence"`, `"reason"`})
	assertJSON(t, ExtractInput{URL: "https://fixture.invalid/share"}, []string{`"url"`})
	assertJSON(t, ExtractOutput{Items: []ExtractedItemRef{{
		ID:               "item-1",
		URL:              "https://download.fixture.invalid/artifact.bin",
		Filename:         "artifact.bin",
		SizeBytes:        123,
		MimeType:         "application/octet-stream",
		AuthProfileRef:   "fixture-auth",
		HeaderProfileRef: "fixture-headers",
		Metadata:         map[string]string{"source": "fixture"},
	}}}, []string{`"items"`, `"size_bytes"`, `"mime_type"`, `"auth_profile_ref"`, `"header_profile_ref"`, `"metadata"`})
	assertJSON(t, HostHTTPFetchRequest{
		Method:           "GET",
		URL:              "https://api.fixture.invalid/resolve/fixture-item",
		Headers:          map[string]string{"Accept": "application/json"},
		AuthProfileRef:   "fixture-auth",
		TimeoutMillis:    100,
		MaxResponseBytes: 512,
	}, []string{`"method"`, `"url"`, `"headers"`, `"auth_profile_ref"`, `"timeout_millis"`, `"max_response_bytes"`})
	assertJSONAbsent(t, HostHTTPFetchRequest{
		Method:           "GET",
		BrokerPolicyRef:  "bpr-alpha001",
		EndpointRef:      "ep-alpha001",
		Params:           map[string]string{"id": "fixture-item"},
		AuthProfileRef:   "alpha-secret",
		TimeoutMillis:    100,
		MaxResponseBytes: 512,
	}, []string{`"broker_policy_ref"`, `"endpoint_ref"`, `"params"`, `"auth_profile_ref"`, `"timeout_millis"`, `"max_response_bytes"`}, []string{`"url"`})
	assertJSON(t, HostHTTPFetchResponse{
		OK:         true,
		StatusCode: 200,
		FinalURL:   "https://api.fixture.invalid/resolve/fixture-item",
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		BodyBase64: "e30=",
	}, []string{`"ok"`, `"status_code"`, `"final_url"`, `"headers"`, `"body_base64"`})
	assertJSON(t, HostAuthProfileStatusRequest{AuthProfileRef: "fixture-auth", URL: "https://api.fixture.invalid/path"}, []string{`"auth_profile_ref"`, `"url"`})
	assertJSONAbsent(t, HostAuthProfileStatusRequest{
		AuthProfileRef:  "alpha-secret",
		BrokerPolicyRef: "bpr-alpha001",
		EndpointRef:     "ep-alpha001",
		Params:          map[string]string{"id": "fixture-item"},
	}, []string{`"auth_profile_ref"`, `"broker_policy_ref"`, `"endpoint_ref"`, `"params"`}, []string{`"url"`})
	assertJSON(t, HostAuthProfileStatusResponse{OK: true, Available: true, Kind: AuthSecretKindBearer, RedactedDisplay: "fi…re"}, []string{`"ok"`, `"available"`, `"kind"`, `"redacted_display"`})
}

func TestPackABIJSONShapesDoNotExposeSecretFields(t *testing.T) {
	values := []any{
		HostHTTPFetchRequest{},
		HostHTTPFetchResponse{},
		HostAuthProfileStatusRequest{},
		HostAuthProfileStatusResponse{},
		ExtractedItemRef{},
	}
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json.Marshal(%T) error = %v", value, err)
		}
		lower := strings.ToLower(string(raw))
		for _, forbidden := range []string{"authorization", "cookie", "token", "secret", "header_value", "raw_header", "raw_token"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%T JSON exposes forbidden field marker %q in %s", value, forbidden, raw)
			}
		}
	}
}

func assertJSONAbsent(t *testing.T, value any, fields []string, absent []string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", value, err)
	}
	for _, field := range fields {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("json.Marshal(%T) = %s, missing %s", value, raw, field)
		}
	}
	for _, field := range absent {
		if strings.Contains(string(raw), field) {
			t.Fatalf("json.Marshal(%T) = %s, unexpectedly contains %s", value, raw, field)
		}
	}
}

func assertJSON(t *testing.T, value any, fields []string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", value, err)
	}
	for _, field := range fields {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("json.Marshal(%T) = %s, missing %s", value, raw, field)
		}
	}
	for _, forbidden := range []string{"TimeoutMillis", "MaxResponseBytes", "AuthProfileRef", "StatusCode", "FinalURL", "BodyBase64", "RedactedDisplay"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("json.Marshal(%T) = %s, contains Go field name %q", value, raw, forbidden)
		}
	}
}
