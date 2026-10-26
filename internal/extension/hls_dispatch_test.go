package extension

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A real WS hls request reaches the TaskAdder untouched — type, title,
// headers, and context survive — and the reply is the shared download_ack.
func TestHLSDownloadDispatch_ReachesTaskAdder(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("hls-secret")
	adder := &fakeTaskAdder{gid: "sg_hls-1"}
	srv := newTestServer(t, adder, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "hls-secret")
	defer conn.Close()

	payload := `{"type":"hls","request_id":"hls-req-1",` +
		`"url":"https://cdn.invalid/live.m3u8",` +
		`"final_url":"https://edge.invalid/final.m3u8",` +
		`"headers":["Cookie: sid=1","X-Tag: v"],` +
		`"file_size":4096,"skip_head_probe":true,` +
		`"dedup_key":"d1","filename":"show.ts",` +
		`"download_page":"https://page.invalid/watch",` +
		`"page_title":"My Show"}`
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write hls: %v", err)
	}

	raw := readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal ack: %v raw=%s", err, raw)
	}
	if resp.Type != MsgTypeDownloadAck {
		t.Fatalf("ack type = %q, want download_ack", resp.Type)
	}
	if !resp.Success || resp.GID != "sg_hls-1" {
		t.Fatalf("ack = %+v, want success gid sg_hls-1", resp)
	}
	if resp.RequestID != "hls-req-1" {
		t.Fatalf("request_id = %q, want echo", resp.RequestID)
	}
	if !adder.wasCalled() {
		t.Fatal("hls request did not reach TaskAdder")
	}
	got := adder.req
	if got.Type != MsgTypeHLS {
		t.Fatalf("req.Type = %q, want hls", got.Type)
	}
	if got.PageTitle != "My Show" {
		t.Fatalf("PageTitle = %q, want My Show", got.PageTitle)
	}
	if got.URL != "https://cdn.invalid/live.m3u8" || got.FinalURL != "https://edge.invalid/final.m3u8" {
		t.Fatalf("url/final_url swapped or lost: %+v", got)
	}
	if len(got.Headers) != 2 || got.Headers[0] != "Cookie: sid=1" {
		t.Fatalf("headers lost: %v", got.Headers)
	}
	if got.FileSize != 4096 || !got.SkipHeadProbe || got.DedupKey != "d1" ||
		got.Filename != "show.ts" || got.DownloadPage != "https://page.invalid/watch" {
		t.Fatalf("context fields lost: %+v", got)
	}
}

// Errors from the adder keep the original download_ack shape — hls never
// gains a dedicated ack type or error channel.
func TestHLSDownloadDispatch_AdderErrorKeepsDownloadAck(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("hls-secret")
	adder := &fakeTaskAdder{err: errors.New("queue busy")}
	srv := newTestServer(t, adder, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "hls-secret")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(
		`{"type":"hls","request_id":"hls-err-1","url":"https://cdn.invalid/live.m3u8"}`))
	raw := readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal ack: %v raw=%s", err, raw)
	}
	if resp.Type != MsgTypeDownloadAck || resp.Success || resp.Error != "queue busy" {
		t.Fatalf("adder error ack = %+v", resp)
	}
	if resp.RequestID != "hls-err-1" {
		t.Fatalf("request_id = %q, want echo", resp.RequestID)
	}
}

// page_title hygiene is enforced at the ingress edge: absent/empty/boundary
// sizes pass, control characters and oversize reject with a fixed error that
// never echoes the title, and the adder is untouched on rejection.
func TestHLSDownloadDispatch_PageTitleBoundaries(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("hls-secret")
	adder := &fakeTaskAdder{gid: "sg_ok"}
	srv := newTestServer(t, adder, store)
	defer srv.Stop()
	startSrv(t, srv)

	send := func(t *testing.T, conn *websocket.Conn, body string) DownloadResponse {
		t.Helper()
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(body)); err != nil {
			t.Fatalf("write: %v", err)
		}
		raw := readRaw(t, conn, 2*time.Second)
		var resp DownloadResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("unmarshal ack: %v raw=%s", err, raw)
		}
		return resp
	}

	t.Run("absent empty and exact-limit titles accepted", func(t *testing.T) {
		conn := dialAuthed(t, srv, "hls-secret")
		defer conn.Close()
		var seq int
		next := func() string { seq++; return "hls-ok-" + string(rune('a'+seq)) }

		resp := send(t, conn, `{"type":"hls","request_id":"`+next()+`","url":"https://cdn.invalid/a.m3u8"}`)
		if !resp.Success {
			t.Fatalf("absent title rejected: %+v", resp)
		}
		resp = send(t, conn, `{"type":"hls","request_id":"`+next()+`","url":"https://cdn.invalid/b.m3u8","page_title":""}`)
		if !resp.Success {
			t.Fatalf("empty title rejected: %+v", resp)
		}
		resp = send(t, conn, `{"type":"hls","request_id":"`+next()+`","url":"https://cdn.invalid/c.m3u8","page_title":null}`)
		if !resp.Success {
			t.Fatalf("null title rejected (lenient single-item contract): %+v", resp)
		}
		// Exactly 1024 UTF-8 bytes, including multibyte content, is the
		// inclusive boundary.
		title1024 := strings.Repeat("é", 512)
		if len(title1024) != 1024 {
			t.Fatalf("fixture size = %d", len(title1024))
		}
		body, _ := json.Marshal(map[string]any{
			"type": "hls", "request_id": next(),
			"url": "https://cdn.invalid/d.m3u8", "page_title": title1024,
		})
		resp = send(t, conn, string(body))
		if !resp.Success {
			t.Fatalf("1024-byte title rejected: %+v", resp)
		}
	})

	t.Run("oversize and control characters reject without adder", func(t *testing.T) {
		conn := dialAuthed(t, srv, "hls-secret")
		defer conn.Close()
		var seq int
		next := func() string { seq++; return "hls-bad-" + string(rune('a'+seq)) }

		cases := []struct {
			name  string
			title string
		}{
			{"1025 bytes", strings.Repeat("x", 1025)},
			{"1025 bytes multibyte", strings.Repeat("é", 513)},
			{"newline", "line\nbreak"},
			{"carriage return", "line\rbreak"},
			{"nul", "a\x00b"},
		}
		for _, tc := range cases {
			body, _ := json.Marshal(map[string]any{
				"type": "hls", "request_id": next(),
				"url": "https://cdn.invalid/e.m3u8", "page_title": tc.title,
			})
			resp := send(t, conn, string(body))
			if resp.Success {
				t.Fatalf("%s: invalid title accepted: %+v", tc.name, resp)
			}
			if resp.Error != "invalid page title" {
				t.Fatalf("%s: error = %q, want fixed safe string", tc.name, resp.Error)
			}
			if strings.Contains(resp.Error, "line") || strings.Contains(resp.Error, "a\x00b") {
				t.Fatalf("%s: title echoed in error", tc.name)
			}
		}
		if adder.wasCalled() == false {
			t.Fatal("prior subtests should have reached the adder")
		}
		// Rejection must not reach the adder: the last recorded request is
		// from the accepted subtest, so re-check by counting via a fresh adder
		// is unnecessary — instead verify a rejected request never overwrites
		// the recorded URL.
		if adder.req.URL != "https://cdn.invalid/d.m3u8" {
			t.Fatalf("adder req mutated by a rejected title: %+v", adder.req)
		}
	})

	t.Run("non-string title is a plain invalid request", func(t *testing.T) {
		conn := dialAuthed(t, srv, "hls-secret")
		defer conn.Close()
		resp := send(t, conn, `{"type":"hls","request_id":"hls-num","url":"https://cdn.invalid/f.m3u8","page_title":123}`)
		if resp.Success || resp.Error != "invalid request" {
			t.Fatalf("non-string title ack = %+v, want invalid request", resp)
		}
	})
}

// Legacy invariants: download still works, hls needs no extra capability,
// and missing/unknown types stay unsupported — there is no blank-type alias.
func TestHLSDownloadDispatch_LegacyInvariants(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("hls-secret")
	adder := &fakeTaskAdder{gid: "gid-dl"}
	srv := newTestServer(t, adder, store) // no linkage: hls needs no cap grant
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "hls-secret")
	defer conn.Close()

	write := func(body string) []byte {
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, []byte(body)); err != nil {
			t.Fatalf("write: %v", err)
		}
		return readRaw(t, conn, 2*time.Second)
	}

	raw := write(`{"type":"download","url":"https://cdn.invalid/plain.bin"}`)
	var dl DownloadResponse
	if err := json.Unmarshal(raw, &dl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if dl.Type != MsgTypeDownloadAck || !dl.Success {
		t.Fatalf("legacy download ack = %+v", dl)
	}
	if !adder.wasCalled() || adder.req.Type != MsgTypeDownload {
		t.Fatalf("legacy download not forwarded as download: %+v", adder.req)
	}

	raw = write(`{"type":"video_hls","request_id":"u-1","url":"https://cdn.invalid/x.m3u8"}`)
	var perr ProtocolError
	if err := json.Unmarshal(raw, &perr); err != nil {
		t.Fatalf("unmarshal protocol_error: %v raw=%s", err, raw)
	}
	if perr.Type != MsgTypeProtocolError || perr.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("unknown type ack = %+v, want protocol_error/unsupported", perr)
	}
	if perr.RequestID != "u-1" {
		t.Fatalf("protocol_error request_id = %q, want echo", perr.RequestID)
	}

	raw = write(`{"request_id":"u-2","url":"https://cdn.invalid/y.m3u8"}`)
	perr = ProtocolError{}
	if err := json.Unmarshal(raw, &perr); err != nil {
		t.Fatalf("unmarshal protocol_error: %v raw=%s", err, raw)
	}
	if perr.Type != MsgTypeProtocolError || perr.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("missing type ack = %+v, want unsupported (no blank-type alias)", perr)
	}
}
