package extension

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturingDirectCommitter records the parsed request the commit path saw so
// the page_title plumbing can be proven end to end.
type capturingDirectCommitter struct {
	fakeDirectCommitter
	mu   sync.Mutex
	seen []DirectBatchRequest
}

func (c *capturingDirectCommitter) HandleDirectBatch(ctx context.Context, env RequestEnvelope, req DirectBatchRequest) DirectCommitResult {
	c.mu.Lock()
	c.seen = append(c.seen, req)
	c.mu.Unlock()
	return c.fakeDirectCommitter.HandleDirectBatch(ctx, env, req)
}

func (c *capturingDirectCommitter) snapshotSeen() []DirectBatchRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]DirectBatchRequest, len(c.seen))
	copy(out, c.seen)
	return out
}

func directBatchPayload(id string, items ...string) string {
	return `{"type":"` + MsgTypeDownloadBatch + `","request_id":"` + id + `","items":[` +
		strings.Join(items, ",") + `]}`
}

// page_title lands on the parsed item; it is optional and never required.
func TestDirectBatchHLS_PageTitleParsed(t *testing.T) {
	raw := directBatchPayload("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		`{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://cdn.invalid/index.m3u8","page_title":"Live Show"}`,
		`{"client_item_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","url":"https://cdn.invalid/b.bin"}`,
	)
	req, code := ParseDirectBatchRequest([]byte(raw))
	if code != "" {
		t.Fatalf("parse rejected valid batch: %s", code)
	}
	if req.Items[0].PageTitle != "Live Show" {
		t.Fatalf("Items[0].PageTitle = %q, want Live Show", req.Items[0].PageTitle)
	}
	if req.Items[1].PageTitle != "" {
		t.Fatalf("Items[1].PageTitle = %q, want empty", req.Items[1].PageTitle)
	}
}

// Whole-request atomicity: any malformed page_title — or a smuggled
// strategy/camelCase key — rejects the batch before anything commits.
func TestDirectBatchHLS_PageTitleStrictValidation(t *testing.T) {
	item := func(extra string) string {
		base := `"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://cdn.invalid/index.m3u8"`
		if extra != "" {
			base += "," + extra
		}
		return `{` + base + `}`
	}
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	t.Run("invalid values reject the whole request", func(t *testing.T) {
		bad := []struct {
			name  string
			extra string
		}{
			{"number", `"page_title":42`},
			{"bool", `"page_title":true`},
			{"array", `"page_title":["x"]`},
			{"object", `"page_title":{"x":1}`},
			{"null", `"page_title":null`},
			{"oversize", `"page_title":"` + strings.Repeat("x", 1025) + `"`},
			{"newline", `"page_title":"a\nb"`},
			{"carriage return", `"page_title":"a\rb"`},
			{"nul escape", `"page_title":"a\` + string(rune(8)) + `"`},
			{"strategy wire field", `"strategy":"hls"`},
			{"camelCase pageTitle", `"pageTitle":"x"`},
			{"mime_type wire field", `"mime_type":"application/x-mpegurl"`},
		}
		for _, tc := range bad {
			raw := directBatchPayload(id, item(tc.extra))
			if _, code := ParseDirectBatchRequest([]byte(raw)); code != ErrCodeInvalidRequest {
				t.Fatalf("%s: code = %q, want invalid_request", tc.name, code)
			}
		}
	})

	t.Run("boundary sizes and empty string accepted", func(t *testing.T) {
		title1024 := strings.Repeat("é", 512) // exactly 1024 UTF-8 bytes
		if len(title1024) != 1024 {
			t.Fatalf("fixture size = %d", len(title1024))
		}
		titleJSON, _ := json.Marshal(title1024)
		raw := directBatchPayload(id, item(`"page_title":`+string(titleJSON)))
		req, code := ParseDirectBatchRequest([]byte(raw))
		if code != "" {
			t.Fatalf("1024-byte title rejected: %s", code)
		}
		if req.Items[0].PageTitle != title1024 {
			t.Fatal("1024-byte title truncated")
		}
		raw = directBatchPayload(id, item(`"page_title":""`))
		if _, code = ParseDirectBatchRequest([]byte(raw)); code != "" {
			t.Fatalf("empty title rejected: %s", code)
		}
	})
}

// The dispatch path passes the parsed title through to the committer.
func TestDirectBatchHLS_DispatchCarriesTitle(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("direct-secret")
	direct := &capturingDirectCommitter{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{DirectCommitter: direct})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "direct-secret")
	defer conn.Close()

	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	writeDirectBatch(t, conn, id,
		`"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://cdn.invalid/index.m3u8","page_title":"Live Show"}]`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.Type != MsgTypeDownloadBatchAck || ack.ErrorCode != "" {
		t.Fatalf("batch ack = %+v", ack)
	}
	seen := direct.snapshotSeen()
	if len(seen) != 1 {
		t.Fatalf("committer calls = %d, want 1", len(seen))
	}
	if got := seen[0].Items[0].PageTitle; got != "Live Show" {
		t.Fatalf("committer PageTitle = %q, want Live Show", got)
	}
}

// A batch rejected on title hygiene never occupies the sticky receipt: the
// same request_id is free to be retried with a corrected payload.
func TestDirectBatchHLS_InvalidTitleLeavesNoReceipt(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("direct-secret")
	direct := &capturingDirectCommitter{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{DirectCommitter: direct})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "direct-secret")
	defer conn.Close()

	id := "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"
	writeDirectBatch(t, conn, id,
		`"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://cdn.invalid/index.m3u8","page_title":123}]`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeInvalidRequest {
		t.Fatalf("invalid title ack = %+v, want invalid_request", ack)
	}
	if direct.calls.Load() != 0 {
		t.Fatalf("committer ran on invalid request, calls=%d", direct.calls.Load())
	}

	writeDirectBatch(t, conn, id,
		`"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://cdn.invalid/index.m3u8","page_title":"Fixed"}]`)
	ack = parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != "" {
		t.Fatalf("retry with same id rejected: %+v", ack)
	}
	if direct.calls.Load() != 1 {
		t.Fatalf("committer calls = %d, want 1", direct.calls.Load())
	}
}

// page_title participates in the payload digest: identical retries hit the
// cache, a changed title conflicts on the same request_id.
func TestDirectBatchHLS_TitleJoinsIdempotencyDigest(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("direct-secret")
	direct := &capturingDirectCommitter{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{DirectCommitter: direct})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "direct-secret")
	defer conn.Close()

	id := "cccccccc-bbbb-cccc-dddd-eeeeeeeeeeee"
	extra := `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://cdn.invalid/index.m3u8","page_title":"First"}]`
	writeDirectBatch(t, conn, id, extra)
	if ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second)); ack.ErrorCode != "" {
		t.Fatalf("first batch ack = %+v", ack)
	}

	writeDirectBatch(t, conn, id, extra)
	if ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second)); ack.ErrorCode != "" {
		t.Fatalf("identical replay must hit the cache, got %+v", ack)
	}
	if direct.calls.Load() != 1 {
		t.Fatalf("replay re-ran commit, calls=%d", direct.calls.Load())
	}

	writeDirectBatch(t, conn, id,
		`"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://cdn.invalid/index.m3u8","page_title":"Changed"}]`)
	if ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second)); ack.ErrorCode != ErrCodeIdempotencyConflict {
		t.Fatalf("changed title on same id = %+v, want idempotency_conflict", ack)
	}
	if direct.calls.Load() != 1 {
		t.Fatalf("conflict re-ran commit, calls=%d", direct.calls.Load())
	}
}
