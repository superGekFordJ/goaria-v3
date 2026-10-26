package extension

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The hls single-item request rides the same DownloadRequest wire shape:
// snake_case keys, page_title only when non-empty, and no camelCase leaks.
func TestHLSDownloadProtocol_WireRoundTrip(t *testing.T) {
	req := DownloadRequest{
		Type:          MsgTypeHLS,
		RequestID:     "hls-req-1",
		URL:           "https://cdn.invalid/playlist.m3u8",
		FinalURL:      "https://edge.invalid/final.m3u8",
		Headers:       []string{"Cookie: sid=abc", "X-Tag: v"},
		FileSize:      2048,
		SkipHeadProbe: true,
		DedupKey:      "dedup-1",
		Filename:      "show.ts",
		DownloadPage:  "https://page.invalid/watch",
		PageTitle:     "My Show",
	}
	data := mustMarshal(t, req)

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("marshal output is not JSON: %v", err)
	}
	if got := string(wire["type"]); got != `"hls"` {
		t.Fatalf(`wire type = %s, want "hls"`, got)
	}
	if got := string(wire["page_title"]); got != `"My Show"` {
		t.Fatalf(`wire page_title = %s, want "My Show"`, got)
	}
	for _, key := range []string{"pageTitle", "fileSize", "skipHeadProbe", "dedupKey", "downloadPage", "finalUrl"} {
		if _, present := wire[key]; present {
			t.Fatalf("camelCase key %q leaked onto the wire: %s", key, data)
		}
	}
	for _, key := range []string{"final_url", "headers", "file_size", "skip_head_probe", "dedup_key", "filename", "download_page"} {
		if _, present := wire[key]; !present {
			t.Fatalf("missing snake_case key %q in %s", key, data)
		}
	}

	var back DownloadRequest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, req) {
		t.Fatalf("round trip mismatch: %+v want %+v", back, req)
	}
}

func TestHLSDownloadProtocol_PageTitleOmittedWhenEmpty(t *testing.T) {
	data := mustMarshal(t, DownloadRequest{Type: MsgTypeHLS, URL: "https://cdn.invalid/a.m3u8"})
	if strings.Contains(string(data), "page_title") {
		t.Fatalf("empty title must be omitted, got %s", data)
	}
	data = mustMarshal(t, DownloadRequest{Type: MsgTypeDownload, URL: "https://cdn.invalid/a.bin"})
	if strings.Contains(string(data), "page_title") {
		t.Fatalf("download request must not gain the key, got %s", data)
	}
}

// The lenient single-item contract: a JSON null title decodes to the empty
// string, which is the normal no-title downgrade — only malformed types and
// hygiene failures reject.
func TestHLSDownloadProtocol_NullTitleDecodesEmpty(t *testing.T) {
	var req DownloadRequest
	if err := json.Unmarshal([]byte(`{"type":"hls","url":"https://cdn.invalid/a.m3u8","page_title":null}`), &req); err != nil {
		t.Fatalf("null page_title must decode, got %v", err)
	}
	if req.PageTitle != "" {
		t.Fatalf("PageTitle = %q, want empty", req.PageTitle)
	}
}

// The hls type adds no capability, keeps the version, and never appears in
// the computed capability list — old hosts simply see an unknown message.
func TestHLSDownloadProtocol_ConstantsUnchanged(t *testing.T) {
	if MsgTypeHLS != "hls" {
		t.Fatalf("MsgTypeHLS = %q", MsgTypeHLS)
	}
	if ProtocolVersion != 2 {
		t.Fatalf("ProtocolVersion = %d, want 2", ProtocolVersion)
	}
	caps := computeConnectionCapabilities("secret", Linkage{})
	if len(caps) != 1 || caps[0] != CapRequestID {
		t.Fatalf("bare-linkage caps = %v, want [request_id]", caps)
	}
	caps = computeConnectionCapabilities("secret", Linkage{
		DirectCommitter: &fakeDirectCommitter{ready: true},
	})
	if !hasCap(caps, CapDownloadBatch) {
		t.Fatalf("download.batch missing: %v", caps)
	}
	for _, cap := range caps {
		if cap == MsgTypeHLS || strings.Contains(cap, "hls") {
			t.Fatalf("no HLS capability may exist: %v", caps)
		}
	}
}
