package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeResolver struct {
	ready           bool
	code            string
	result          *ResolveResult
	calls           atomic.Int32
	invalidateCalls atomic.Int32
	block           chan struct{}
	started         chan struct{}
	panicNow        bool
	rewrite         func([]byte) []byte
}

func (f *fakeResolver) Ready() bool { return f.ready }

func (f *fakeResolver) HandleResolve(ctx context.Context, _ RequestEnvelope, _ json.RawMessage) ResolveResult {
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
			return ResolveResult{ErrorCode: ErrCodeBusy}
		}
	}
	if f.result != nil {
		return *f.result
	}
	code := f.code
	if code == "" {
		code = ErrCodeUnsupported
	}
	return ResolveResult{ErrorCode: code}
}

func (f *fakeResolver) Invalidate() {
	f.invalidateCalls.Add(1)
}

func (f *fakeResolver) RewriteCachedResolve(cached []byte) []byte {
	if f.rewrite != nil {
		return f.rewrite(cached)
	}

	return cached
}

type fakeCommitter struct {
	ready  bool
	code   string
	result *CommitResult
	calls  atomic.Int32
}

func (f *fakeCommitter) Ready() bool { return f.ready }

func (f *fakeCommitter) HandleCommit(_ context.Context, _ RequestEnvelope, _ json.RawMessage) CommitResult {
	f.calls.Add(1)
	if f.result != nil {
		return *f.result
	}
	code := f.code
	if code == "" {
		code = ErrCodeUnsupported
	}
	return CommitResult{ErrorCode: code}
}

type fakeDigests struct {
	ready   bool
	ok      bool
	version int
	salt    string
	exact   []string
	sub     []string
	seq     atomic.Uint64
}

func (f *fakeDigests) Ready() bool { return f.ready }

func (f *fakeDigests) Snapshot() (MatchDigestSnapshot, bool) {
	if !f.ok {
		return MatchDigestSnapshot{}, false
	}
	version := f.version
	if version == 0 {
		version = MatchDigestVersion
	}
	salt := f.salt
	if salt == "" {
		n := f.seq.Add(1)
		salt = "0000000000000000000000000000000" + trimHexDigit(n)
	}
	exact := f.exact
	if exact == nil {
		exact = []string{}
	}
	sub := f.sub
	if sub == nil {
		sub = []string{}
	}
	return MatchDigestSnapshot{
		Version:          version,
		Salt:             salt,
		ExactDigests:     exact,
		SubdomainDigests: sub,
	}, true
}

func trimHexDigit(n uint64) string {
	const hexdigits = "0123456789abcdef"
	return string(hexdigits[n%16])
}

func assertNoMatchKey(t *testing.T, raw []byte) {
	t.Helper()
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatalf("raw map: %v", err)
	}
	if _, ok := rawMap["match"]; ok {
		t.Fatalf("match key must be omitted, got %s", raw)
	}
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
	return slices.Contains(caps, want)
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

func writeBatch(t *testing.T, conn *websocket.Conn, id string, extra string) {
	t.Helper()
	payload := `{"type":"` + MsgTypeBatchDownload + `","request_id":"` + id + `"`
	if extra != "" {
		payload += "," + extra
	}
	payload += `}`
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write batch: %v", err)
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
	assertNoMatchKey(t, raw)
}

func TestAuthAck_MVPOmitsExtractorCaps(t *testing.T) {
	withAllowEmptySecret(t, true)
	store := NewSecretStore()
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:        &fakeResolver{ready: true},
		Committer:       &fakeCommitter{ready: true},
		DirectCommitter: &fakeDirectCommitter{ready: true},
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
	if hasCap(ack.Capabilities, CapExtractorResolve) || hasCap(ack.Capabilities, CapExtractorBatch) || hasCap(ack.Capabilities, CapDownloadBatch) {
		t.Fatalf("MVP empty secret must not advertise extractor or download.batch caps: %v", ack.Capabilities)
	}
	assertNoMatchKey(t, raw)
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

func TestAuthAck_MatchPublishedWhenResolveAndDigestsReady(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("match-secret")
	digests := &fakeDigests{
		ready: true,
		ok:    true,
		salt:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		exact: []string{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		sub:   []string{},
	}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
		Digests:  digests,
	})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "match-secret"}))
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !hasCap(ack.Capabilities, CapExtractorResolve) {
		t.Fatalf("want extractor.resolve, got %v", ack.Capabilities)
	}
	if ack.Match == nil {
		t.Fatalf("match missing: %s", raw)
	}
	if ack.Match.DigestVersion != MatchDigestVersion {
		t.Fatalf("digest_version=%d", ack.Match.DigestVersion)
	}
	if len(ack.Match.Salt) != 32 {
		t.Fatalf("salt len=%d", len(ack.Match.Salt))
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatal(err)
	}
	matchRaw, ok := rawMap["match"]
	if !ok {
		t.Fatal("match key missing")
	}
	var matchMap map[string]json.RawMessage
	if err := json.Unmarshal(matchRaw, &matchMap); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"digest_version", "salt", "exact_digests", "subdomain_digests"} {
		if _, exists := matchMap[key]; !exists {
			t.Fatalf("match missing %s: %s", key, matchRaw)
		}
	}
	if string(matchMap["exact_digests"]) == "null" || len(matchMap["exact_digests"]) == 0 || matchMap["exact_digests"][0] != '[' {
		t.Fatalf("exact_digests must be a JSON array, got %s", matchMap["exact_digests"])
	}
	if string(matchMap["subdomain_digests"]) == "null" || len(matchMap["subdomain_digests"]) == 0 || matchMap["subdomain_digests"][0] != '[' {
		t.Fatalf("subdomain_digests must be a JSON array, got %s", matchMap["subdomain_digests"])
	}
}

func TestAuthAck_MatchOmittedWhenDigestsNotReady(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("match-secret")
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
		Digests:  &fakeDigests{ready: false, ok: true},
	})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "match-secret"}))
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !hasCap(ack.Capabilities, CapExtractorResolve) {
		t.Fatalf("resolver ready should still grant resolve: %v", ack.Capabilities)
	}
	assertNoMatchKey(t, raw)
}

func TestAuthAck_MatchOmittedWhenResolverNotReady(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("match-secret")
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: false},
		Digests:  &fakeDigests{ready: true, ok: true},
	})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "match-secret"}))
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if hasCap(ack.Capabilities, CapExtractorResolve) {
		t.Fatalf("Digests.Ready must not grant resolve: %v", ack.Capabilities)
	}
	assertNoMatchKey(t, raw)
}

func TestAuthAck_MVPOmitsMatchWhenBothReady(t *testing.T) {
	withAllowEmptySecret(t, true)
	store := NewSecretStore()
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
		Digests:  &fakeDigests{ready: true, ok: true},
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
	if hasCap(ack.Capabilities, CapExtractorResolve) {
		t.Fatalf("MVP must not advertise extractor.resolve: %v", ack.Capabilities)
	}
	assertNoMatchKey(t, raw)
}

func TestAuthAck_SnapshotFailureOmitsMatchKeepsSocket(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("match-secret")
	adder := &fakeTaskAdder{gid: "snap-gid"}
	srv := newTestServer(t, adder, store)
	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
		Digests:  &fakeDigests{ready: true, ok: false},
	})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "match-secret"}))
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !hasCap(ack.Capabilities, CapExtractorResolve) {
		t.Fatalf("caps should still include resolve: %v", ack.Capabilities)
	}
	assertNoMatchKey(t, raw)

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteJSON(DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/f.bin"}); err != nil {
		t.Fatalf("write download: %v", err)
	}
	dlRaw := readRaw(t, conn, 2*time.Second)
	var dl DownloadResponse
	if err := json.Unmarshal(dlRaw, &dl); err != nil {
		t.Fatalf("download ack: %v raw=%s", err, dlRaw)
	}
	if !dl.Success || dl.GID != "snap-gid" {
		t.Fatalf("socket should still accept download, got %+v", dl)
	}
}

func TestAuthAck_TwoConnectionsDifferentSalts(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("match-secret")
	digests := &fakeDigests{ready: true, ok: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
		Digests:  digests,
	})
	defer srv.Stop()
	startSrv(t, srv)

	auth := func() string {
		conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
		defer conn.Close()
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "match-secret"}))
		raw := readRaw(t, conn, 2*time.Second)
		var ack AuthAck
		if err := json.Unmarshal(raw, &ack); err != nil {
			t.Fatal(err)
		}
		if ack.Match == nil || ack.Match.Salt == "" {
			t.Fatalf("match/salt missing: %s", raw)
		}
		return ack.Match.Salt
	}
	salt1 := auth()
	salt2 := auth()
	if salt1 == salt2 {
		t.Fatalf("connections shared salt %q", salt1)
	}
}

func TestExtractorResolve_LateSetLinkageDoesNotPushMatch(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
		Digests:  &fakeDigests{ready: true, ok: true},
	})

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteJSON(DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/f.bin"}); err != nil {
		t.Fatalf("write download: %v", err)
	}
	dlRaw := readRaw(t, conn, 2*time.Second)
	var dl DownloadResponse
	if err := json.Unmarshal(dlRaw, &dl); err != nil {
		t.Fatalf("download ack: %v raw=%s", err, dlRaw)
	}
	if dl.Type != MsgTypeDownloadAck {
		t.Fatalf("next frame type=%s, want download_ack (no capability_update/match push)", dl.Type)
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
	for range 2 {
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
	for range 4 {
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
	for range 2 {
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

func TestExtractorResolve_ConnDropCompletesSuccessfulStub(t *testing.T) {
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

	connB := dialAuthed(t, srv, "prod-secret")
	defer connB.Close()
	writeResolve(t, connB, "r-drop", `"n":1`)
	_ = connA.Close()
	close(block)

	ack := parseTypedAck(t, readRaw(t, connB, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnsupported || ack.RequestID != "r-drop" {
		t.Fatalf("coalesced waiter should get successful stub, got %+v", ack)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("owner drop must not re-run a successful stub, calls=%d", resolver.calls.Load())
	}

	writeResolve(t, connB, "r-drop", `"n":1`)
	replay := parseTypedAck(t, readRaw(t, connB, 2*time.Second))
	if replay.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("replay should hit cached stub, got %+v", replay)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("cache hit must not start a second handler, calls=%d", resolver.calls.Load())
	}
}

func TestExtractorResolve_ConnDropZeroWaitersReplayHits(t *testing.T) {
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
	writeResolve(t, connA, "r-solo", `"n":1`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not start")
	}
	_ = connA.Close()
	close(block)

	gen := store.Generation()
	deadline := time.Now().Add(2 * time.Second)
	for !srv.idemp.hasCompleted(gen, MsgTypeExtractorResolve, "r-solo") {
		if time.Now().After(deadline) {
			t.Fatal("want completed cache entry after owner drop with no waiters")
		}
		time.Sleep(5 * time.Millisecond)
	}

	connB := dialAuthed(t, srv, "prod-secret")
	defer connB.Close()
	writeResolve(t, connB, "r-solo", `"n":1`)
	replay := parseTypedAck(t, readRaw(t, connB, 2*time.Second))
	if replay.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("later replay should hit cached stub, got %+v", replay)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("zero-waiter owner drop must complete for later replay, calls=%d", resolver.calls.Load())
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

func TestExtractorResolve_SuccessAckHasNoURLs(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	mime := "application/octet-stream"
	resolver := &fakeResolver{ready: true, result: &ResolveResult{
		Matched:    true,
		SessionID:  "s1",
		TotalCount: 1,
		TotalBytes: 12,
		Items: []ResolveDisplayItem{{
			ItemID:    "i1",
			Filename:  "a.bin",
			SizeBytes: 12,
			MimeType:  mime,
		}},
	}}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-ok", `"source_url":"https://share.alpha.test/s"`)
	raw := readRaw(t, conn, 2*time.Second)
	var ack ExtractorResolveAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal: %v raw=%s", err, raw)
	}
	if ack.ErrorCode != "" || ack.Matched == nil || !*ack.Matched || ack.SessionID != "s1" {
		t.Fatalf("ack = %+v", ack)
	}
	if len(ack.Items) != 1 || ack.Items[0].Filename != "a.bin" || ack.Items[0].MimeType != mime {
		t.Fatalf("items = %+v", ack.Items)
	}
	lower := bytes.ToLower(raw)
	for _, forbidden := range [][]byte{[]byte("http://"), []byte("https://"), []byte("auth_profile"), []byte("header_profile")} {
		if bytes.Contains(lower, forbidden) {
			t.Fatalf("ack leaked %s: %s", forbidden, raw)
		}
	}
}

func TestExtractorResolve_MatchedFalseHasEmptyItems(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true, result: &ResolveResult{Matched: false}}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-miss", `"source_url":"https://share.alpha.test/nope"`)
	raw := readRaw(t, conn, 2*time.Second)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal map: %v raw=%s", err, raw)
	}
	if _, ok := fields["error_code"]; ok {
		t.Fatalf("matched:false must omit error_code: %s", raw)
	}
	if _, ok := fields["session_id"]; ok {
		t.Fatalf("matched:false must omit session_id: %s", raw)
	}
	var ack ExtractorResolveAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if ack.Matched == nil || *ack.Matched {
		t.Fatalf("matched = %v", ack.Matched)
	}
	if ack.Items == nil || len(ack.Items) != 0 {
		t.Fatalf("items = %#v, want []", ack.Items)
	}
}

func TestNotifyUnpairedAndStop_InvalidateResolver(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	_ = conn.Close()

	srv.NotifyUnpaired()
	if resolver.invalidateCalls.Load() != 1 {
		t.Fatalf("NotifyUnpaired Invalidate calls=%d, want 1", resolver.invalidateCalls.Load())
	}
	srv.Stop()
	if resolver.invalidateCalls.Load() != 2 {
		t.Fatalf("Stop Invalidate calls=%d, want 2", resolver.invalidateCalls.Load())
	}
}

func TestNotifyUnpaired_RotationFailureStillInvalidates(t *testing.T) {
	origReader := randReader
	randReader = &failingReader{}
	t.Cleanup(func() { randReader = origReader })

	store := NewSecretStore()
	store.SetSecret("old-secret")
	genBefore := store.Generation()
	resolver := &fakeResolver{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "old-secret")
	writeResolve(t, conn, "r-rotfail", `"source_url":"https://share.alpha.test/s"`)
	_ = parseTypedAck(t, readRaw(t, conn, 2*time.Second))

	srv.NotifyUnpaired()
	if store.GetSecret() != "old-secret" {
		t.Fatalf("secret changed to %q", store.GetSecret())
	}
	if store.Generation() != genBefore {
		t.Fatalf("generation = %d, want %d", store.Generation(), genBefore)
	}
	if resolver.invalidateCalls.Load() < 1 {
		t.Fatal("Invalidate must run when rotation fails")
	}
	_ = conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.GetStatus().ConnectedClients == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn2 := dialAuthed(t, srv, "old-secret")
	defer conn2.Close()
	writeResolve(t, conn2, "r-rotfail", `"source_url":"https://share.alpha.test/s"`)
	ack := parseTypedAck(t, readRaw(t, conn2, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnsupported {
		t.Fatalf("idemp should be cleared, got %+v", ack)
	}
	if resolver.calls.Load() != 2 {
		t.Fatalf("idemp should miss after unpair, calls=%d", resolver.calls.Load())
	}
}

func TestExtractorResolve_RewriteCachedSuccessToSessionExpired(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	resolver := &fakeResolver{ready: true, result: &ResolveResult{
		Matched:   true,
		SessionID: "leased-session",
		Items:     []ResolveDisplayItem{{ItemID: "i1", Filename: "a.bin"}},
	}}
	resolver.rewrite = func(cached []byte) []byte {
		var prev ExtractorResolveAck
		if err := json.Unmarshal(cached, &prev); err != nil {
			t.Fatalf("rewrite cached: %v", err)
		}
		out, err := json.Marshal(ExtractorResolveAck{
			Type:      prev.Type,
			RequestID: prev.RequestID,
			ErrorCode: ErrCodeSessionExpired,
			Items:     []ExtractorResolveAckItem{},
		})
		if err != nil {
			t.Fatalf("marshal rewrite: %v", err)
		}
		return out
	}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{Resolver: resolver})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeResolve(t, conn, "r-rewrite", `"source_url":"https://share.alpha.test/s"`)
	first := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if first.ErrorCode != "" {
		t.Fatalf("first ack = %+v", first)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("first calls=%d", resolver.calls.Load())
	}

	writeResolve(t, conn, "r-rewrite", `"source_url":"https://share.alpha.test/s"`)
	raw := readRaw(t, conn, 2*time.Second)
	ack := parseTypedAck(t, raw)
	if ack.ErrorCode != ErrCodeSessionExpired || ack.RequestID != "r-rewrite" {
		t.Fatalf("rewritten ack = %+v raw=%s", ack, raw)
	}
	if resolver.calls.Load() != 1 {
		t.Fatalf("rewrite must not re-run HandleResolve, calls=%d", resolver.calls.Load())
	}
}

func TestBatchDownload_SuccessAckHasItemIDsAndNoURLs(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	itemID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
		Committer: &fakeCommitter{ready: true, result: &CommitResult{
			Success:          true,
			GroupKey:         "dg-1",
			SucceededItemIDs: []string{itemID},
		}},
	})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeBatch(t, conn, "b-ok", `"session_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","item_ids":["`+itemID+`"]`)
	raw := readRaw(t, conn, 2*time.Second)
	var ack BatchDownloadAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal: %v raw=%s", err, raw)
	}
	if ack.Type != MsgTypeBatchDownloadAck || !ack.Success || ack.GroupKey != "dg-1" {
		t.Fatalf("ack = %+v raw=%s", ack, raw)
	}
	if len(ack.SucceededItemIDs) != 1 || ack.SucceededItemIDs[0] != itemID {
		t.Fatalf("succeeded = %#v", ack.SucceededItemIDs)
	}
	if ack.DuplicateItemIDs == nil {
		t.Fatal("duplicate_item_ids must be [] not null")
	}
	lower := bytes.ToLower(raw)
	for _, forbidden := range [][]byte{
		[]byte("http://"),
		[]byte("https://"),
		[]byte("auth_profile"),
		[]byte("header_profile"),
		[]byte("download.fixture.invalid"),
	} {
		if bytes.Contains(lower, forbidden) {
			t.Fatalf("ack leaked %s: %s", forbidden, raw)
		}
	}
	if !bytes.Contains(raw, []byte(`"success":true`)) || !bytes.Contains(raw, []byte(itemID)) {
		t.Fatalf("ack missing success/item_id: %s", raw)
	}
}

func TestBatchDownload_AckStripsEngineURLFromItemErrors(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	itemID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
		Committer: &fakeCommitter{ready: true, result: &CommitResult{
			Success:          false,
			SucceededItemIDs: []string{},
			DuplicateItemIDs: []string{},
			ErrorsByItemID: map[string]string{
				itemID: "engine failed https://download.fixture.invalid/?token=leak apr-x r-9",
			},
		}},
	})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeBatch(t, conn, "b-leak", `"session_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","item_ids":["`+itemID+`"]`)
	raw := readRaw(t, conn, 2*time.Second)
	var ack BatchDownloadAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal: %v raw=%s", err, raw)
	}
	if ack.ErrorCode != "" || ack.Success {
		t.Fatalf("ack = %+v raw=%s", ack, raw)
	}
	if ack.ErrorsByItemID[itemID] != CommitItemErrorAddFailed {
		t.Fatalf("errors_by_item_id = %#v, want opaque add failed", ack.ErrorsByItemID)
	}
	lower := bytes.ToLower(raw)
	for _, forbidden := range [][]byte{
		[]byte("https://download.fixture.invalid"),
		[]byte("http://"),
		[]byte("https://"),
		[]byte("apr-"),
		[]byte("r-9"),
		[]byte("token=leak"),
	} {
		if bytes.Contains(lower, forbidden) {
			t.Fatalf("ack leaked %s: %s", forbidden, raw)
		}
	}
}

func TestBatchDownload_WholeRequestOmitsErrorsByItemID(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:  &fakeResolver{ready: true},
		Committer: &fakeCommitter{ready: true, result: &CommitResult{ErrorCode: ErrCodeSessionExpired}},
	})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeBatch(t, conn, "b-expired", `"session_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","item_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`)
	raw := readRaw(t, conn, 2*time.Second)
	ack := parseTypedAck(t, raw)
	if ack.ErrorCode != ErrCodeSessionExpired || ack.RequestID != "b-expired" {
		t.Fatalf("ack = %+v raw=%s", ack, raw)
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		t.Fatal(err)
	}
	if _, ok := rawMap["errors_by_item_id"]; ok {
		t.Fatalf("whole-request error must omit errors_by_item_id: %s", raw)
	}
	if _, ok := rawMap["succeeded_item_ids"]; ok {
		t.Fatalf("whole-request error must omit succeeded_item_ids: %s", raw)
	}
}

func TestBatchDownload_SkipIdempotencyRetriesImmediately(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	itemID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	committer := &fakeCommitter{ready: true, result: &CommitResult{
		ErrorCode:       ErrCodeUnavailable,
		SkipIdempotency: true,
	}}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:  &fakeResolver{ready: true},
		Committer: committer,
	})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	extra := `"session_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","item_ids":["` + itemID + `"]`
	writeBatch(t, conn, "b-skip", extra)
	first := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if first.ErrorCode != ErrCodeUnavailable || first.RequestID != "b-skip" {
		t.Fatalf("first ack = %+v", first)
	}
	writeBatch(t, conn, "b-skip", extra)
	second := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if second.ErrorCode != ErrCodeUnavailable || second.RequestID != "b-skip" {
		t.Fatalf("retry ack = %+v", second)
	}
	if committer.calls.Load() != 2 {
		t.Fatalf("SkipIdempotency must re-run HandleCommit, calls=%d", committer.calls.Load())
	}
}

func TestBatchDownload_PreConsumeDenylistStaysSticky(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	committer := &denylistCommitter{}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:  &fakeResolver{ready: true},
		Committer: committer,
	})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	extra := `"session_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","item_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"url":"https://files.alpha.test/x.bin"`
	writeBatch(t, conn, "b-deny", extra)
	first := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if first.ErrorCode != ErrCodeInvalidRequest || first.RequestID != "b-deny" {
		t.Fatalf("first ack = %+v", first)
	}
	writeBatch(t, conn, "b-deny", extra)
	second := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if second.ErrorCode != ErrCodeInvalidRequest || second.RequestID != "b-deny" {
		t.Fatalf("sticky ack = %+v", second)
	}
	if committer.calls.Load() != 1 {
		t.Fatalf("pre-consume denylist must stay 60s-sticky, calls=%d", committer.calls.Load())
	}
}

type denylistCommitter struct {
	calls atomic.Int32
}

func (c *denylistCommitter) Ready() bool { return true }

func (c *denylistCommitter) HandleCommit(_ context.Context, _ RequestEnvelope, raw json.RawMessage) CommitResult {
	c.calls.Add(1)
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(raw, &extra); err != nil {
		return CommitResult{ErrorCode: ErrCodeInvalidRequest}
	}
	if _, ok := extra["url"]; ok {
		return CommitResult{ErrorCode: ErrCodeInvalidRequest}
	}
	return CommitResult{Success: true, SucceededItemIDs: []string{}}
}
