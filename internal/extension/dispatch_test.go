package extension

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeResolver struct {
	ready    bool
	code     string
	calls    atomic.Int32
	block    chan struct{}
	started  chan struct{}
	panicNow bool
}

func (f *fakeResolver) Ready() bool { return f.ready }

func (f *fakeResolver) HandleResolve(ctx context.Context, _ RequestEnvelope, _ json.RawMessage) StubAck {
	if f.panicNow {
		f.panicNow = false
		panic("extractor resolve test panic")
	}
	f.calls.Add(1)
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return StubAck{ErrorCode: ErrCodeBusy}
		}
	}
	code := f.code
	if code == "" {
		code = ErrCodeUnsupported
	}
	return StubAck{ErrorCode: code}
}

type fakeCommitter struct {
	ready bool
	code  string
}

func (f *fakeCommitter) Ready() bool { return f.ready }

func (f *fakeCommitter) HandleCommit(_ context.Context, _ RequestEnvelope, _ json.RawMessage) StubAck {
	code := f.code
	if code == "" {
		code = ErrCodeUnsupported
	}
	return StubAck{ErrorCode: code}
}

func withAuthReadTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := authReadTimeout
	authReadTimeout = d
	t.Cleanup(func() { authReadTimeout = orig })
}

func withResolveConcurrency(t *testing.T, n int) {
	t.Helper()
	orig := resolveConcurrency
	resolveConcurrency = n
	t.Cleanup(func() { resolveConcurrency = orig })
}

func withPerConnInFlightMax(t *testing.T, n int) {
	t.Helper()
	orig := perConnInFlightMax
	perConnInFlightMax = n
	t.Cleanup(func() { perConnInFlightMax = orig })
}

func withIdempMaxWaiters(t *testing.T, n int) {
	t.Helper()
	orig := idempMaxWaiters
	idempMaxWaiters = n
	t.Cleanup(func() { idempMaxWaiters = orig })
}

func startSrv(t *testing.T, srv *Server) {
	t.Helper()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func dialAuthed(t *testing.T, srv *Server, secret string) *websocket.Conn {
	t.Helper()
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: secret}))
	readAuthAck(t, conn)
	return conn
}

func readRaw(t *testing.T, conn *websocket.Conn, timeout time.Duration) []byte {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return raw
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func writeResolve(t *testing.T, conn *websocket.Conn, id string, extra string) {
	t.Helper()
	payload := `{"type":"` + MsgTypeExtractorResolve + `","request_id":"` + id + `"`
	if extra != "" {
		payload += "," + extra
	}
	payload += `}`
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write resolve: %v", err)
	}
}

func parseTypedAck(t *testing.T, raw []byte) TypedAck {
	t.Helper()
	var ack TypedAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal typed ack: %v raw=%s", err, raw)
	}
	return ack
}

func TestAuthAck_Protocol2Capabilities(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("cap-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "cap-secret"}))

	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal auth_ack: %v", err)
	}
	if ack.Type != MsgTypeAuthAck {
		t.Fatalf("type: %s", ack.Type)
	}
	if ack.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol_version want %d got %d", ProtocolVersion, ack.ProtocolVersion)
	}
	if ack.Capabilities == nil {
		t.Fatal("capabilities must not be nil")
	}
	if !hasCap(ack.Capabilities, CapRequestID) {
		t.Fatalf("missing request_id: %v", ack.Capabilities)
	}
	if hasCap(ack.Capabilities, CapExtractorResolve) || hasCap(ack.Capabilities, CapExtractorBatch) {
		t.Fatalf("extractor caps without Ready: %v", ack.Capabilities)
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatal(err)
	}
	capsRaw := rawMap["capabilities"]
	if string(capsRaw) == "null" || len(capsRaw) == 0 || capsRaw[0] != '[' {
		t.Fatalf("capabilities JSON must be an array, got %s", capsRaw)
	}
}

func TestAuthAck_ExtractorCapsRequireReadyAndSecret(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("ready-secret")
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:  &fakeResolver{ready: true},
		Committer: &fakeCommitter{ready: true},
	})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "ready-secret"}))
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !hasCap(ack.Capabilities, CapExtractorResolve) || !hasCap(ack.Capabilities, CapExtractorBatch) {
		t.Fatalf("want extractor caps, got %v", ack.Capabilities)
	}
}

func TestAuthAck_MVPOmitsExtractorCaps(t *testing.T) {
	withAllowEmptySecret(t, true)
	store := NewSecretStore()
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:  &fakeResolver{ready: true},
		Committer: &fakeCommitter{ready: true},
	})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !hasCap(ack.Capabilities, CapRequestID) {
		t.Fatalf("MVP must still advertise request_id: %v", ack.Capabilities)
	}
	if hasCap(ack.Capabilities, CapExtractorResolve) || hasCap(ack.Capabilities, CapExtractorBatch) {
		t.Fatalf("MVP empty secret must not advertise extractor caps: %v", ack.Capabilities)
	}
}

func TestAuthAck_HostVersionEchoed(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("ver-secret")
	srv := newTestServer(t, nil, store)
	srv.SetHostVersion("dev")
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "ver-secret"}))
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.HostVersion != "dev" {
		t.Fatalf("host_version want dev, got %q", ack.HostVersion)
	}
}

func TestAuth_FirstFrameTimeoutSilentClose(t *testing.T) {
	withAuthReadTimeout(t, 50*time.Millisecond)
	store := NewSecretStore()
	store.SetSecret("timeout-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err == nil {
		var msg struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &msg)
		if msg.Type == MsgTypeAuthAck {
			t.Fatal("auth_ack must not be sent on first-frame timeout")
		}
		t.Fatal("expected connection close on first-frame timeout")
	}
}

func TestAuth_FirstFrameWrongTypeSilentClose(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping","secret":"prod-secret"}`))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err == nil {
		var msg struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &msg)
		if msg.Type == MsgTypeAuthAck {
			t.Fatal("auth_ack must not be sent when first frame type is not auth")
		}
		t.Fatal("expected connection close for non-auth first frame")
	}
}

func TestPostHandshakeAuthThenDownload_Production(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "prod-secret"}))
	req := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/a.bin"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))
	raw := readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != MsgTypeDownloadAck || !resp.Success {
		t.Fatalf("unexpected ack: %+v", resp)
	}
}

func TestPostHandshakeAuthThenDownload_MVP(t *testing.T) {
	withAllowEmptySecret(t, true)
	store := NewSecretStore()
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	readAuthAck(t, conn)

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: ""}))
	req := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/a.bin"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))
	raw := readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("MVP download after post-handshake auth failed: %s", resp.Error)
	}
}

func TestPingThenDownload_NoProtocolError(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`))
	req := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/a.bin"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))
	raw := readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type == MsgTypeProtocolError {
		t.Fatal("ping must not produce protocol_error")
	}
	if !resp.Success {
		t.Fatalf("download after ping failed: %s", resp.Error)
	}
}

func TestUnknownType_ProtocolErrorThenDownload(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"nope","request_id":"err-1"}`))
	raw := readRaw(t, conn, 2*time.Second)
	var pe ProtocolError
	if err := json.Unmarshal(raw, &pe); err != nil {
		t.Fatal(err)
	}
	if pe.Type != MsgTypeProtocolError || pe.ErrorCode != ErrCodeUnsupported || pe.RequestID != "err-1" {
		t.Fatalf("unexpected protocol_error: %+v", pe)
	}

	req := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/a.bin"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))
	raw = readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != MsgTypeDownloadAck || !resp.Success {
		t.Fatalf("download after protocol_error failed: %+v", resp)
	}
}

func TestDownload_RequestIDEchoAndOmitted(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	req := DownloadRequest{Type: MsgTypeDownload, RequestID: "dl-1", URL: "https://example.com/a.bin"}
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))
	raw := readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != "dl-1" {
		t.Fatalf("want echoed request_id, got %q", resp.RequestID)
	}

	req2 := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/b.bin"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req2))
	raw = readRaw(t, conn, 2*time.Second)
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatal(err)
	}
	if id, ok := rawMap["request_id"]; ok && string(id) != `""` && string(id) != "null" {
		t.Fatalf("request_id should be omitted when inbound had none, got %s", id)
	}
}

func TestDownload_InvalidJSONStillDownloadAck(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"download","request_id":"bad-1","url":123}`))
	raw := readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != MsgTypeDownloadAck || resp.Success || resp.Error != "invalid request" {
		t.Fatalf("want download_ack invalid request, got %+v", resp)
	}
	if resp.RequestID != "bad-1" {
		t.Fatalf("should echo request_id on invalid download JSON, got %q", resp.RequestID)
	}
}

func TestExtractorResolve_UnavailableWithoutCap(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	start := time.Now()
	writeResolve(t, conn, "r-unavail", `"source_url":"https://example.com"`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("unavailable took too long: %s", time.Since(start))
	}
	if ack.Type != MsgTypeExtractorResolveAck || ack.ErrorCode != ErrCodeUnavailable || ack.RequestID != "r-unavail" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
}

func TestExtractorResolve_StubUnsupportedFast(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	start := time.Now()
	writeResolve(t, conn, "r-stub", `"source_url":"https://example.com"`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("stub took too long: %s", time.Since(start))
	}
	if ack.Type != MsgTypeExtractorResolveAck || ack.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("unexpected ack: %+v", ack)
	}
}

func TestExtractorResolve_MissingRequestID(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: &fakeResolver{ready: true}})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"extractor_resolve"}`))
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeInvalidRequest {
		t.Fatalf("want invalid_request, got %+v", ack)
	}
}

func TestExtractorResolve_BusyQueueDepthZero(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 8)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() { close(block) })
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-busy-1", `"n":1`)
	writeResolve(t, conn, "r-busy-2", `"n":2`)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("handlers did not start")
		}
	}
	writeResolve(t, conn, "r-busy-3", `"n":3`)
	start := time.Now()
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("busy was not immediate: %s", time.Since(start))
	}
	if ack.ErrorCode != ErrCodeBusy || ack.RequestID != "r-busy-3" {
		t.Fatalf("want busy for third resolve, got %+v", ack)
	}
}

func TestExtractorResolve_PerConnInFlightBusy(t *testing.T) {
	withResolveConcurrency(t, 8)
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 8)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() { close(block) })
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	for i := 1; i <= 4; i++ {
		writeResolve(t, conn, "r-slot-"+string(rune('0'+i)), `"n":`+string(rune('0'+i)))
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("handlers did not start")
		}
	}
	writeResolve(t, conn, "r-slot-5", `"n":5`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeBusy || ack.RequestID != "r-slot-5" {
		t.Fatalf("want per-conn busy, got %+v", ack)
	}
}

func TestExtractorResolve_IdempotencyReplayAndConflict(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-idemp", `"source_url":"https://example.com/a"`)
	ack1 := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack1.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("first: %+v", ack1)
	}
	writeResolve(t, conn, "r-idemp", `"source_url":"https://example.com/a"`)
	ack2 := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack2.ErrorCode != ack1.ErrorCode {
		t.Fatalf("replay mismatch: %+v vs %+v", ack1, ack2)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("replay must not re-run handler, calls=%d", resolver.calls.Load())
	}

	writeResolve(t, conn, "r-idemp", `"source_url":"https://example.com/b"`)
	ack3 := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack3.ErrorCode != ErrCodeIdempotencyConflict {
		t.Fatalf("want conflict, got %+v", ack3)
	}
}

func TestExtractorResolve_InFlightCoalesce(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-coal", `"source_url":"https://example.com/a"`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not start")
	}
	writeResolve(t, conn, "r-coal", `"source_url":"https://example.com/a"`)
	close(block)
	ack1 := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	ack2 := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack1.ErrorCode != ErrCodeUnsupported || ack2.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("acks: %+v %+v", ack1, ack2)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("coalesce must run handler once, calls=%d", resolver.calls.Load())
	}
}

func TestUnpair_ClearsIdempotencyCache(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("old-secret")
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "old-secret")

	writeResolve(t, conn, "r-unpair", `"source_url":"https://example.com/a"`)
	_ = parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if resolver.calls.Load() != 1 {
		t.Fatalf("setup calls=%d", resolver.calls.Load())
	}
	genBefore := store.Generation()
	srv.NotifyUnpaired()
	if store.Generation() == genBefore {
		t.Fatal("unpair should bump secret generation")
	}
	_ = conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.GetStatus().ConnectedClients == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	newSecret := store.GetSecret()
	conn2 := dialAuthed(t, srv, newSecret)
	defer conn2.Close()
	writeResolve(t, conn2, "r-unpair", `"source_url":"https://example.com/a"`)
	ack := parseTypedAck(t, readRaw(t, conn2, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("after unpair: %+v", ack)
	}
	if resolver.calls.Load() != 2 {
		t.Fatalf("cache should miss after unpair, calls=%d", resolver.calls.Load())
	}
}

func TestReadLimit_OversizedFrameCloses(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	huge := make([]byte, int(wsReadLimit)+1)
	for i := range huge {
		huge[i] = 'x'
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, huge); err != nil {
		t.Fatalf("write oversized: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected connection to die after oversized frame")
	}
}

func TestExtractorResolve_ConcurrentStubWrites(t *testing.T) {
	withResolveConcurrency(t, 4)
	withPerConnInFlightMax(t, 4)
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 2)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-conc-1", `"n":1`)
	writeResolve(t, conn, "r-conc-2", `"n":2`)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("handlers did not start")
		}
	}
	close(block)
	ack1 := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	ack2 := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack1.ErrorCode != ErrCodeUnsupported || ack2.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("acks: %+v %+v", ack1, ack2)
	}
}

func TestExtractorResolve_MVPEmptySecretUnavailable(t *testing.T) {
	withAllowEmptySecret(t, true)
	store := NewSecretStore()
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	readAuthAck(t, conn)

	writeResolve(t, conn, "r-mvp", `"source_url":"https://example.com"`)
	start := time.Now()
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("unavailable took too long: %s", time.Since(start))
	}
	if ack.ErrorCode != ErrCodeUnavailable {
		t.Fatalf("MVP empty secret must not run resolve, got %+v", ack)
	}
	if resolver.calls.Load() != 0 {
		t.Fatalf("HandleResolve must not run, calls=%d", resolver.calls.Load())
	}
}

func TestExtractorResolve_LateSetLinkageDoesNotGrant(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	srv.SetLinkage(Linkage{Resolver: resolver})
	writeResolve(t, conn, "r-late", `"n":1`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnavailable {
		t.Fatalf("snapshot must keep extractor off, got %+v", ack)
	}
	if resolver.calls.Load() != 0 {
		t.Fatalf("HandleResolve must not run, calls=%d", resolver.calls.Load())
	}
}

func TestExtractorResolve_ResolveLimitHonoredAfterNewServer(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 4)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	withResolveConcurrency(t, 1)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() { close(block) })
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-gate-1", `"n":1`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not start")
	}
	writeResolve(t, conn, "r-gate-2", `"n":2`)
	start := time.Now()
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("busy was not immediate: %s", time.Since(start))
	}
	if ack.ErrorCode != ErrCodeBusy || ack.RequestID != "r-gate-2" {
		t.Fatalf("want busy after post-construct override, got %+v", ack)
	}
}

func TestExtractorResolve_CoalesceDoesNotStallDownload(t *testing.T) {
	withResolveConcurrency(t, 1)
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	startSrv(t, srv)
	connA := dialAuthed(t, srv, "prod-secret")
	defer connA.Close()
	connB := dialAuthed(t, srv, "prod-secret")
	defer connB.Close()

	writeResolve(t, connA, "r-coal", `"source_url":"https://example.com/a"`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not start")
	}
	writeResolve(t, connB, "r-coal", `"source_url":"https://example.com/a"`)
	req := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/file.bin"}
	connB.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = connB.WriteMessage(websocket.TextMessage, mustMarshal(t, req))
	raw := readRaw(t, connB, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != MsgTypeDownloadAck || !resp.Success {
		t.Fatalf("coalesce must not stall download FIFO, got %s", raw)
	}

	writeResolve(t, connB, "r-busy-other", `"n":9`)
	start := time.Now()
	busy := parseTypedAck(t, readRaw(t, connB, 2*time.Second))
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("busy was not immediate: %s", time.Since(start))
	}
	if busy.ErrorCode != ErrCodeBusy || busy.RequestID != "r-busy-other" {
		t.Fatalf("want busy for distinct id while coalesced, got %+v", busy)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("coalesce must run handler once, calls=%d", resolver.calls.Load())
	}
	close(block)
	coal := parseTypedAck(t, readRaw(t, connB, 2*time.Second))
	if coal.RequestID != "r-coal" || coal.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("coalesced waiter should get owner ack, got %+v", coal)
	}
}

func TestExtractorResolve_StopUnblocksCoalescedWaiter(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	startSrv(t, srv)
	connA := dialAuthed(t, srv, "prod-secret")
	defer connA.Close()
	connB := dialAuthed(t, srv, "prod-secret")
	defer connB.Close()

	writeResolve(t, connA, "r-stop", `"n":1`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not start")
	}
	writeResolve(t, connB, "r-stop", `"n":1`)
	srv.Stop()
	connB.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := connB.ReadMessage()
	if err == nil {
		ack := parseTypedAck(t, raw)
		if ack.ErrorCode != ErrCodeBusy {
			t.Fatalf("Stop waiter ack want busy or close, got %+v", ack)
		}
	}
	if srv.idemp.len() != 0 {
		t.Fatalf("Stop must clear idempotency cache, len=%d", srv.idemp.len())
	}
}

func TestExtractorResolve_WaiterCapImmediateBusy(t *testing.T) {
	withIdempMaxWaiters(t, 1)
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	startSrv(t, srv)
	connA := dialAuthed(t, srv, "prod-secret")
	defer connA.Close()
	connB := dialAuthed(t, srv, "prod-secret")
	defer connB.Close()

	writeResolve(t, connA, "r-cap", `"n":1`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not start")
	}
	writeResolve(t, connB, "r-cap", `"n":1`)
	writeResolve(t, connB, "r-cap", `"n":1`)
	start := time.Now()
	busy := parseTypedAck(t, readRaw(t, connB, 2*time.Second))
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("excess waiter busy was not immediate: %s", time.Since(start))
	}
	if busy.ErrorCode != ErrCodeBusy || busy.RequestID != "r-cap" {
		t.Fatalf("want busy for waiter over cap, got %+v", busy)
	}
	close(block)
	coal := parseTypedAck(t, readRaw(t, connB, 2*time.Second))
	if coal.RequestID != "r-cap" || coal.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("capped coalesced waiter should still get owner ack, got %+v", coal)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("waiter cap must not start a second handler, calls=%d", resolver.calls.Load())
	}
}

func TestExtractorResolve_ConnDropAbandonsNotCaches(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 2)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	startSrv(t, srv)
	connA := dialAuthed(t, srv, "prod-secret")
	writeResolve(t, connA, "r-drop", `"n":1`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not start")
	}
	_ = connA.Close()
	time.Sleep(50 * time.Millisecond)
	if resolver.calls.Load() != 1 {
		t.Fatalf("dropped socket must not cancel generation ctx into a new run, calls=%d", resolver.calls.Load())
	}
	close(block)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.idemp.len() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if srv.idemp.len() != 0 {
		t.Fatalf("dead connection must abandon, not cache, len=%d", srv.idemp.len())
	}

	connB := dialAuthed(t, srv, "prod-secret")
	defer connB.Close()
	writeResolve(t, connB, "r-drop", `"n":1`)
	replay := parseTypedAck(t, readRaw(t, connB, 2*time.Second))
	if replay.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("replay after abandon must re-run, got %+v", replay)
	}
	if resolver.calls.Load() != 2 {
		t.Fatalf("want handler re-run after conn drop abandon, calls=%d", resolver.calls.Load())
	}
}

func TestExtractorResolve_UnpairCancelsGenerationCtx(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	resolver := &fakeResolver{ready: true, block: block, started: started}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	writeResolve(t, conn, "r-unpair-ctx", `"n":1`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not start")
	}
	srv.NotifyUnpaired()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.idemp.len() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if srv.idemp.len() != 0 {
		t.Fatalf("unpair must clear in-flight cache, len=%d", srv.idemp.len())
	}
	close(block)

	newSecret := store.GetSecret()
	if newSecret == "" || newSecret == "prod-secret" {
		t.Fatalf("unpair should rotate secret, got %q", newSecret)
	}
	conn2 := dialAuthed(t, srv, newSecret)
	defer conn2.Close()
	writeResolve(t, conn2, "r-unpair-ctx", `"n":1`)
	ack := parseTypedAck(t, readRaw(t, conn2, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("new generation must run a fresh handler, got %+v", ack)
	}
	if resolver.calls.Load() != 2 {
		t.Fatalf("want re-run after unpair, calls=%d", resolver.calls.Load())
	}
}

func TestExtractorResolve_HandlerPanicAbandonsBusy(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true, panicNow: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-panic", `"n":1`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeBusy || ack.RequestID != "r-panic" {
		t.Fatalf("panic must abandon with busy, got %+v", ack)
	}

	req := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/file.bin"}
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req)); err != nil {
		t.Fatal(err)
	}
	raw := readRaw(t, conn, 2*time.Second)
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Type != MsgTypeDownloadAck || !resp.Success {
		t.Fatalf("server must survive handler panic, got %s", raw)
	}
}
