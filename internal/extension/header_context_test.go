package extension

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type headerCtxResolver struct {
	fakeResolver
	headerReady bool
	seenGranted atomic.Bool
	seenSet     atomic.Bool
}

func (r *headerCtxResolver) HeaderContextReady() bool { return r.headerReady }

func (r *headerCtxResolver) HandleResolve(ctx context.Context, env RequestEnvelope, raw json.RawMessage) ResolveResult {
	r.seenGranted.Store(HeaderContextGranted(ctx))
	r.seenSet.Store(true)
	return r.fakeResolver.HandleResolve(ctx, env, raw)
}

func newHeaderCtxResolver(ready, headerReady bool) *headerCtxResolver {
	return &headerCtxResolver{fakeResolver{ready: ready}, headerReady, atomic.Bool{}, atomic.Bool{}}
}

func parseAuthAckCaps(t *testing.T, raw []byte) []string {
	t.Helper()
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal auth_ack: %v", err)
	}
	return ack.Capabilities
}

func TestHeaderContextGrant_ContextMarkerDefaultsClosed(t *testing.T) {
	if HeaderContextGranted(context.Background()) {
		t.Fatal("absent marker must read false")
	}
	if HeaderContextGranted(nil) { //nolint:staticcheck // nil ctx must fail closed
		t.Fatal("nil ctx must read false")
	}
	if !HeaderContextGranted(WithHeaderContextGrant(context.Background(), true)) {
		t.Fatal("granted marker must read true")
	}
	if HeaderContextGranted(WithHeaderContextGrant(context.Background(), false)) {
		t.Fatal("explicit false marker must read false")
	}
}

func TestAuthAck_HeaderContextAdvertisedWhenResolverCapable(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := newHeaderCtxResolver(true, true)
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "prod-secret"}))
	raw := readRaw(t, conn, 2*time.Second)
	caps := parseAuthAckCaps(t, raw)
	if !slices.Contains(caps, CapExtractorResolve) {
		t.Fatalf("missing extractor.resolve: %v", caps)
	}
	if !slices.Contains(caps, CapExtractorHeaderContext) {
		t.Fatalf("missing extractor.header_context: %v", caps)
	}
	if slices.Index(caps, CapExtractorHeaderContext) < slices.Index(caps, CapExtractorResolve) {
		t.Fatalf("header_context must follow extractor.resolve: %v", caps)
	}
}

func TestAuthAck_HeaderContextOmittedWhenHeaderContextNotReady(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := newHeaderCtxResolver(true, false)
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-hdr-off", `"source_url":"https://example.com"`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	if !resolver.seenSet.Load() || resolver.seenGranted.Load() {
		t.Fatal("marker must be false on a connection without the capability")
	}
}

func TestExtractorResolve_UnsolicitedGrantRejectedWithoutCapability(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-uns", `"source_url":"https://example.com","browser_header_grants":[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/x","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2,"headers":[{"name":"x-a","value":"v"}]}]`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeInvalidRequest {
		t.Fatalf("unsolicited grant ack = %+v, want invalid_request", ack)
	}
	if resolver.calls.Load() != 0 {
		t.Fatalf("resolver must not run for unsolicited grant, calls=%d", resolver.calls.Load())
	}

	// Same request_id without grants must not hit idempotency: the rejection
	// happens before the idempotency slot is taken.
	writeResolve(t, conn, "r-uns", `"source_url":"https://example.com"`)
	ack = parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("same request_id without grants = %+v, want unsupported (no idempotency consumption)", ack)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls.Load())
	}
}

func TestExtractorResolve_UnsolicitedGrantCaseVariantRejected(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-case", `"source_url":"https://example.com","Browser_Header_Grants":[]`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeInvalidRequest {
		t.Fatalf("case-variant grant key ack = %+v, want invalid_request", ack)
	}
	if resolver.calls.Load() != 0 {
		t.Fatalf("resolver must not run, calls=%d", resolver.calls.Load())
	}
}

func TestExtractorResolve_GrantMarkerPassedOnAuthorizedConnection(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := newHeaderCtxResolver(true, true)
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-granted", `"source_url":"https://example.com","browser_header_grants":[{"source_origin":"https://share.fixture.invalid","target_url":"https://api.fixture.invalid/x","method":"GET","captured_at_unix_ms":1,"expires_at_unix_ms":2,"headers":[{"name":"x-a","value":"v"}]}]`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	if !resolver.seenSet.Load() {
		t.Fatal("resolver was not invoked")
	}
	if !resolver.seenGranted.Load() {
		t.Fatal("authorized connection must mark ctx granted")
	}
}

func TestExtractorResolve_GrantPresentOnAuthorizedConnectionOnlyMarksCapability(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := newHeaderCtxResolver(true, true)
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	// No grants in the payload; the marker still reflects the capability.
	writeResolve(t, conn, "r-nogrant", `"source_url":"https://example.com"`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	if !resolver.seenGranted.Load() {
		t.Fatal("marker must reflect capability grant, not payload presence")
	}
}
