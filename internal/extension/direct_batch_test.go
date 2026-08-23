package extension

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDirectBatchRequest_UnknownKey(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": "https://download.fixture.invalid/a.bin"},
		},
		"session_id": "nope",
	})
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("unknown key code = %q", code)
	}
}

func TestParseDirectBatchRequest_DuplicateClientItemID(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{"client_item_id": id, "url": "https://download.fixture.invalid/a.bin"},
			{"client_item_id": id, "url": "https://download.fixture.invalid/b.bin"},
		},
	})
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("duplicate id code = %q", code)
	}
}

func TestParseDirectBatchRequest_OversizeItems(t *testing.T) {
	items := make([]map[string]any, MaxResolveSessionItems+1)
	for i := range items {
		items[i] = map[string]any{
			"client_item_id": strings.Repeat("a", 31) + string(rune('0'+(i%10))),
			"url":            "https://download.fixture.invalid/a.bin",
		}
	}
	// unique 32-hex ids
	for i := range items {
		items[i]["client_item_id"] = uniqueHex32(i)
	}
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items":      items,
	})
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("oversize code = %q", code)
	}
}

func TestParseDirectBatchRequest_UserinfoRejected(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": "https://user:pass@download.fixture.invalid/a.bin"},
		},
	})
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("userinfo code = %q", code)
	}
}

func TestParseDirectBatchRequest_FragmentStrippedSameOwner(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": "https://download.fixture.invalid/a.bin#one"},
			{"client_item_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "url": "https://download.fixture.invalid/a.bin#two"},
		},
	})
	req, code := ParseDirectBatchRequest(raw)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if req.Items[0].CanonicalURL != req.Items[1].CanonicalURL {
		t.Fatalf("canonical = %q vs %q", req.Items[0].CanonicalURL, req.Items[1].CanonicalURL)
	}
	if strings.Contains(req.Items[0].CanonicalURL, "#") {
		t.Fatalf("fragment leaked: %q", req.Items[0].CanonicalURL)
	}
}

func TestParseDirectBatchRequest_QueryOrderPreserved(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": "https://download.fixture.invalid/a.bin?b=2&a=1"},
			{"client_item_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "url": "https://download.fixture.invalid/a.bin?a=1&b=2"},
		},
	})
	req, code := ParseDirectBatchRequest(raw)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if req.Items[0].CanonicalURL == req.Items[1].CanonicalURL {
		t.Fatalf("query order should produce distinct owners: %q", req.Items[0].CanonicalURL)
	}
}

func TestParseDirectBatchRequest_FinalURLDoesNotReplaceURL(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{
				"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"url":            "https://download.fixture.invalid/a.bin",
				"final_url":      "https://cdn.fixture.invalid/real.bin",
			},
		},
	})
	req, code := ParseDirectBatchRequest(raw)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if req.Items[0].CanonicalURL != "https://download.fixture.invalid/a.bin" {
		t.Fatalf("url replaced: %q", req.Items[0].CanonicalURL)
	}
	if req.Items[0].FinalURL != "https://cdn.fixture.invalid/real.bin" {
		t.Fatalf("final_url = %q", req.Items[0].FinalURL)
	}
}

func TestParseDirectBatchRequest_DeniedHeaders(t *testing.T) {
	cases := []string{
		"Authorization: Bearer secret",
		"Host: evil.example.com",
		"Range: bytes=0-1",
		"Proxy-Authorization: Basic x",
		"Connection: close",
	}
	for _, header := range cases {
		raw := directBatchJSON(t, map[string]any{
			"type":       MsgTypeDownloadBatch,
			"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"items": []map[string]any{
				{
					"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					"url":            "https://download.fixture.invalid/a.bin",
					"headers":        []string{header},
				},
			},
		})
		if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
			t.Fatalf("header %q code = %q", header, code)
		}
	}
}

func TestParseDirectBatchRequest_DuplicateHeaderNames(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{
				"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"url":            "https://download.fixture.invalid/a.bin",
				"headers":        []string{"Cookie: a=1", "cookie: b=2"},
			},
		},
	})
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("duplicate header code = %q", code)
	}
}

func TestParseDirectBatchRequest_AllowedHeaders(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
		"items": []map[string]any{
			{
				"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"url":            "HTTPS://Download.Fixture.Invalid/a.bin",
				"headers": []string{
					"Cookie: sid=1",
					"Referer: https://page.fixture.invalid/",
					"User-Agent: GoAriaTest/1.0",
					"Accept: */*",
					"Accept-Language: en",
				},
			},
		},
	})
	req, code := ParseDirectBatchRequest(raw)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if req.RequestID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("request_id = %q", req.RequestID)
	}
	if req.Items[0].CanonicalURL != "https://download.fixture.invalid/a.bin" {
		t.Fatalf("canonical = %q", req.Items[0].CanonicalURL)
	}
	if len(req.Items[0].Headers) != 5 {
		t.Fatalf("headers = %#v", req.Items[0].Headers)
	}
}

func TestParseDirectBatchRequest_PayloadCap(t *testing.T) {
	raw := bytesRepeatJSON(maxDirectBatchPayloadBytes + 1)
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("payload cap code = %q", code)
	}
}

func TestParseDirectBatchRequest_FloatFileSize(t *testing.T) {
	raw := []byte(`{"type":"download_batch","request_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://download.fixture.invalid/a.bin","file_size":1.5}]}`)
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("float file_size code = %q", code)
	}
}

func TestParseDirectBatchRequest_EmptyItems(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items":      []map[string]any{},
	})
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("empty items code = %q", code)
	}
}

func TestParseDirectBatchRequest_FolderNameCRLF(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":        MsgTypeDownloadBatch,
		"request_id":  "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"folder_name": "Album\r\n",
		"items": []map[string]any{
			{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": "https://download.fixture.invalid/a.bin"},
		},
	})
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("folder CRLF code = %q", code)
	}
}

func TestParseDirectBatchRequest_IPv6Host(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": "http://[2001:db8::1]/a.bin"},
		},
	})
	req, code := ParseDirectBatchRequest(raw)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if req.Items[0].CanonicalURL != "http://[2001:db8::1]/a.bin" {
		t.Fatalf("canonical = %q", req.Items[0].CanonicalURL)
	}
}

func TestParseDirectBatchRequest_HTTPVsHTTPSDistinct(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"items": []map[string]any{
			{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": "http://download.fixture.invalid/a.bin"},
			{"client_item_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "url": "https://download.fixture.invalid/a.bin"},
		},
	})
	req, code := ParseDirectBatchRequest(raw)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if req.Items[0].CanonicalURL == req.Items[1].CanonicalURL {
		t.Fatalf("http/https must be distinct owners")
	}
}

func TestParseDirectBatchRequest_RejectedSchemes(t *testing.T) {
	schemes := []string{
		"ftp://download.fixture.invalid/a.bin",
		"magnet:?xt=urn:btih:abc",
		"blob:https://download.fixture.invalid/a",
		"data:text/plain,hi",
		"file:///tmp/a.bin",
		"ws://download.fixture.invalid/a",
	}
	for _, u := range schemes {
		raw := directBatchJSON(t, map[string]any{
			"type":       MsgTypeDownloadBatch,
			"request_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"items": []map[string]any{
				{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": u},
			},
		})
		if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
			t.Fatalf("scheme %q code = %q", u, code)
		}
	}
}

func TestParseDirectBatchRequest_NonUUIDRequestID(t *testing.T) {
	raw := directBatchJSON(t, map[string]any{
		"type":       MsgTypeDownloadBatch,
		"request_id": "not-a-uuid",
		"items": []map[string]any{
			{"client_item_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "url": "https://download.fixture.invalid/a.bin"},
		},
	})
	if _, code := ParseDirectBatchRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("non-uuid code = %q", code)
	}
}

func TestParseDirectBatchStatusRequest_UnknownKey(t *testing.T) {
	raw := []byte(`{"type":"download_batch_status","request_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","items":[]}`)
	if _, code := ParseDirectBatchStatusRequest(raw); code != ErrCodeInvalidRequest {
		t.Fatalf("status unknown key code = %q", code)
	}
}

func TestParseDirectBatchStatusRequest_UUID(t *testing.T) {
	raw := []byte(`{"type":"download_batch_status","request_id":"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"}`)
	id, code := ParseDirectBatchStatusRequest(raw)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if id != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("id = %q", id)
	}
}

func uniqueHex32(i int) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for j := range 32 {
		out[j] = 'a'
	}
	n := i
	for p := 31; n > 0 && p >= 0; p-- {
		out[p] = hexdigits[n%16]
		n /= 16
	}
	return string(out)
}

func directBatchJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func bytesRepeatJSON(n int) []byte {
	raw := make([]byte, n)
	for i := range raw {
		raw[i] = 'x'
	}
	return raw
}
