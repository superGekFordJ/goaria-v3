package extension

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeTaskAdder records calls to AddUriFromExtension for the access-layer gate test.
type fakeTaskAdder struct {
	mu     sync.Mutex
	called bool
	req    DownloadRequest
	gid    string
	err    error
}

func (f *fakeTaskAdder) AddUriFromExtension(req DownloadRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	f.req = req
	return f.gid, f.err
}

func (f *fakeTaskAdder) wasCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

func newTestServer(t *testing.T, taskAdder TaskAdder, store *SecretStore) *Server {
	t.Helper()
	if store == nil {
		store = NewSecretStore()
	}
	if taskAdder == nil {
		taskAdder = &fakeTaskAdder{gid: "test-gid"}
	}
	return NewServer(nil, taskAdder, store)
}

// withAllowEmptySecret temporarily enables the dev escape hatch for tests
// that exercise the MVP empty-secret path. Restores the original value on cleanup.
func withAllowEmptySecret(t *testing.T, enabled bool) {
	t.Helper()
	orig := allowEmptySecret
	allowEmptySecret = enabled
	t.Cleanup(func() { allowEmptySecret = orig })
}

func dialWS(t *testing.T, port int, origin string) *websocket.Conn {
	t.Helper()
	u := fmt.Sprintf("ws://127.0.0.1:%d/", port)
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, resp, err := dialer.Dial(u, h)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("dial failed: %v", err)
	}
	return conn
}

// readAuthAck consumes the auth_ack the backend sends after successful (or
// skipped) auth, before the read loop starts processing downloads.
func readAuthAck(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read auth_ack: %v", err)
	}
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal auth_ack: %v", err)
	}
	if msg.Type != MsgTypeAuthAck {
		t.Fatalf("expected auth_ack, got %s", msg.Type)
	}
}

func TestStart_PortFallback(t *testing.T) {
	// Occupy 16801.
	blocker, err := net.Listen("tcp", "127.0.0.1:16801")
	if err != nil {
		t.Skipf("cannot occupy 16801: %v", err)
	}
	defer blocker.Close()

	srv := newTestServer(t, nil, nil)
	defer srv.Stop()

	if err := srv.Start(0); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	status := srv.GetStatus()
	if status.WSPort != 16802 {
		t.Fatalf("expected fallback to 16802, got %d", status.WSPort)
	}
}

// TestStart_PreferredPortTaken_FallsBack verifies that when the preferred port
// is occupied, Start falls back to the next available WSPortFallbacks entry.
func TestStart_PreferredPortTaken_FallsBack(t *testing.T) {
	blocker, err := net.Listen("tcp", "127.0.0.1:16801")
	if err != nil {
		t.Skipf("cannot occupy 16801: %v", err)
	}
	defer blocker.Close()

	srv := newTestServer(t, nil, nil)
	defer srv.Stop()

	if err := srv.Start(16801); err != nil {
		t.Fatalf("Start(16801) with 16801 occupied failed: %v", err)
	}
	status := srv.GetStatus()
	if status.WSPort != 16802 {
		t.Fatalf("expected fallback to 16802, got %d", status.WSPort)
	}
}

func TestOrigin_AllowedChrome(t *testing.T) {
	withAllowEmptySecret(t, true)
	srv := newTestServer(t, nil, nil)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abcdef")
	defer conn.Close()
	// MVP: no secret, should connect fine.
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`))
}

func TestOrigin_AllowedFirefox(t *testing.T) {
	withAllowEmptySecret(t, true)
	srv := newTestServer(t, nil, nil)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "moz-extension://abcdef")
	defer conn.Close()
}

func TestOrigin_RejectedWebPage(t *testing.T) {
	srv := newTestServer(t, nil, nil)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	port := srv.GetStatus().WSPort
	u := fmt.Sprintf("ws://127.0.0.1:%d/", port)
	h := http.Header{}
	h.Set("Origin", "https://evil.com")
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	_, resp, err := dialer.Dial(u, h)
	if err == nil {
		t.Fatal("expected dial to fail for web page origin")
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestOrigin_RejectedMissing(t *testing.T) {
	srv := newTestServer(t, nil, nil)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	port := srv.GetStatus().WSPort
	u := fmt.Sprintf("ws://127.0.0.1:%d/", port)
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	_, resp, err := dialer.Dial(u, nil)
	if err == nil {
		t.Fatal("expected dial to fail for missing origin")
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestStop_CleansListener(t *testing.T) {
	srv := newTestServer(t, nil, nil)
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	port := srv.GetStatus().WSPort
	srv.Stop()

	// Port should be re-bindable.
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("port not released after Stop: %v", err)
	}
	l.Close()
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func TestSecret_Empty_AllowsMVP(t *testing.T) {
	withAllowEmptySecret(t, true)
	store := NewSecretStore() // empty secret
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	// No auth message needed — should accept download directly.
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	req := DownloadRequest{
		Type:     MsgTypeDownload,
		URL:      "https://example.com/file.zip",
		Filename: "file.zip",
	}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))

	// MVP mode still sends auth_ack before entering the read loop.
	readAuthAck(t, conn)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestSecret_Valid_Authenticates(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("my-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	// Send auth first.
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	auth := AuthMessage{Type: MsgTypeAuth, Secret: "my-secret"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, auth))

	// Consume auth_ack before sending download.
	readAuthAck(t, conn)

	// Then send download.
	req := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/test.zip"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success with valid secret, got: %s", resp.Error)
	}
}

func TestSecret_Invalid_Rejects(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("correct-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	// Send wrong auth.
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	auth := AuthMessage{Type: MsgTypeAuth, Secret: "wrong-secret"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, auth))

	// Server should close the connection without sending auth_ack (the secret
	// mismatch returns before the auth_ack send). A read error is expected.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err == nil {
		// If a message arrived, it must not be auth_ack.
		var msg struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &msg) == nil && msg.Type == MsgTypeAuthAck {
			t.Fatal("auth_ack must not be sent for invalid secret")
		}
		t.Fatal("expected connection close for invalid secret")
	}
}

func TestDownload_ForwardedToTaskService(t *testing.T) {
	// HARD GATE: downloads must go through TaskAdder, never rpc.DownloadEngine.
	withAllowEmptySecret(t, true)
	adder := &fakeTaskAdder{gid: "gid-123"}
	store := NewSecretStore()
	srv := newTestServer(t, adder, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	req := DownloadRequest{
		Type:     MsgTypeDownload,
		URL:      "https://example.com/large.bin",
		Filename: "large.bin",
		FileSize: 1048576,
		Headers:  []string{"Cookie: session=abc"},
	}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))

	// MVP mode: auth_ack is sent before the read loop processes the download.
	readAuthAck(t, conn)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var resp DownloadResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !resp.Success {
		t.Fatalf("download failed: %s", resp.Error)
	}
	if resp.GID != "gid-123" {
		t.Fatalf("expected gid-123, got %s", resp.GID)
	}
	if !adder.wasCalled() {
		t.Fatal("TaskAdder.AddUriFromExtension was not called — download bypassed tasks.Service")
	}
	if adder.req.URL != "https://example.com/large.bin" {
		t.Fatalf("unexpected URL forwarded: %s", adder.req.URL)
	}
	if len(adder.req.Headers) != 1 || !strings.Contains(adder.req.Headers[0], "Cookie") {
		t.Fatalf("headers not forwarded: %v", adder.req.Headers)
	}
}

// TestPairing_AutoShutdownAfterAuth verifies the pairing service shuts down
// after the first successful WebSocket auth (即用即关).
func TestPairing_AutoShutdownAfterAuth(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("auto-shutdown-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ps := NewPairingService(store, nil)
	srv.SetPairingService(ps)
	if _, err := ps.Start(); err != nil {
		t.Fatalf("pairing Start: %v", err)
	}
	if !ps.IsActive() {
		t.Fatal("pairing service should be active before auth")
	}

	secret := store.GetSecret()
	if secret == "" {
		t.Fatal("secret should be set after pairing Start")
	}

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	auth := AuthMessage{Type: MsgTypeAuth, Secret: secret}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, auth))

	// Consume auth_ack before polling pairing state.
	readAuthAck(t, conn)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !ps.IsActive() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ps.IsActive() {
		t.Fatal("pairing service should be inactive after successful WebSocket auth")
	}
}

// TestSecret_DownloadBeforeAuth_Rejected verifies that sending a download
// request as the first message (before auth) in production mode is rejected.
func TestSecret_DownloadBeforeAuth_Rejected(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	adder := &fakeTaskAdder{gid: "should-not-reach"}
	srv := newTestServer(t, adder, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	req := DownloadRequest{Type: MsgTypeDownload, URL: "https://example.com/file.zip"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, req))

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection close when download sent before auth")
	}
	if adder.wasCalled() {
		t.Fatal("TaskAdder must not be called for unauthenticated download")
	}
}

// TestUnpair_RotatesSecret_NotClears verifies that unpairing rotates the
// stored secret rather than clearing it (closes the empty-secret backdoor).
func TestUnpair_RotatesSecret_NotClears(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("my-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()

	if store.GetSecret() == "" {
		t.Fatal("secret should be set before unpair")
	}
	srv.NotifyUnpaired()

	newSecret := store.GetSecret()
	if newSecret == "" {
		t.Fatal("secret should not be empty after unpair (rotated, not cleared)")
	}
	if newSecret == "my-secret" {
		t.Fatal("secret should be rotated to a new value after unpair")
	}
	status := srv.GetStatus()
	if status.Paired {
		t.Fatal("status should show unpaired after unpair")
	}
}

// TestUnpair_DisconnectsAuthenticatedConnections verifies that unpairing
// closes all active WebSocket connections, not just clears the secret.
func TestUnpair_DisconnectsAuthenticatedConnections(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("pair-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	auth := AuthMessage{Type: MsgTypeAuth, Secret: "pair-secret"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, auth))

	// Consume auth_ack before checking connected clients.
	readAuthAck(t, conn)

	if status := srv.GetStatus(); status.ConnectedClients != 1 {
		t.Fatalf("expected 1 connected client before unpair, got %d", status.ConnectedClients)
	}

	srv.NotifyUnpaired()

	// Connection should be closed by NotifyUnpaired.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}

	// Wait for the deferred cleanup to decrement connectedClients.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.GetStatus().ConnectedClients <= 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status := srv.GetStatus(); status.ConnectedClients != 0 {
		t.Fatalf("expected 0 connected clients after unpair, got %d", status.ConnectedClients)
	}
}

// TestStartPairing_ReusesActive verifies that repeated StartPairing calls
// reuse the active pairing service instead of leaking a new one.
func TestStartPairing_ReusesActive(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("test-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()

	url1, err := srv.StartPairing()
	if err != nil {
		t.Fatalf("first StartPairing: %v", err)
	}
	url2, err := srv.StartPairing()
	if err != nil {
		t.Fatalf("second StartPairing: %v", err)
	}
	if url1 != url2 {
		t.Fatalf("expected same URL on double-call, got %s vs %s", url1, url2)
	}
}

// TestStartPairing_ResetsPairedState verifies that starting a new pairing cycle
// (after a previous pairing completed and its service stopped) resets the paired
// flag so the next auth re-triggers NotifyPaired instead of leaking the service.
func TestStartPairing_ResetsPairedState(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("old-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()

	// Simulate already-paired state from a previous cycle.
	srv.mu.Lock()
	srv.paired = true
	srv.mu.Unlock()
	if !srv.GetStatus().Paired {
		t.Fatal("precondition: paired should be true")
	}

	if _, err := srv.StartPairing(); err != nil {
		t.Fatalf("StartPairing: %v", err)
	}

	// New cycle must reset paired so the next auth re-triggers NotifyPaired.
	if srv.GetStatus().Paired {
		t.Fatal("paired should be false after starting a new pairing cycle")
	}
	// Secret preserved across new pairing cycle (long-term API key model).
	if store.GetSecret() != "old-secret" {
		t.Fatalf("secret should be preserved across pairing cycle, got %q", store.GetSecret())
	}

	// Complete an auth with the preserved secret and verify NotifyPaired fires + service stops.
	ps := srv.pairingService
	if ps == nil {
		t.Fatal("pairing service should be set after StartPairing")
	}
	secret := store.GetSecret()
	if secret == "" {
		t.Fatal("secret should be set before pairing Start")
	}
	if err := srv.Start(0); err != nil {
		t.Fatalf("server Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	auth := AuthMessage{Type: MsgTypeAuth, Secret: secret}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, auth))

	// Consume auth_ack before polling pairing state.
	readAuthAck(t, conn)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !ps.IsActive() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ps.IsActive() {
		t.Fatal("new pairing service should stop after successful auth")
	}
	if !srv.GetStatus().Paired {
		t.Fatal("paired should be true again after successful auth")
	}
}

// TestStartPairing_PreservesSecretAcrossCycles verifies that repeated
// StartPairing calls keep the global secret stable (long-term API key model),
// complementing the pairing-level multi-browser regression test.
func TestStartPairing_PreservesSecretAcrossCycles(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("stable-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()

	if _, err := srv.StartPairing(); err != nil {
		t.Fatalf("first StartPairing: %v", err)
	}
	srv.mu.Lock()
	ps := srv.pairingService
	srv.mu.Unlock()
	if ps != nil {
		ps.Stop()
	}

	if _, err := srv.StartPairing(); err != nil {
		t.Fatalf("second StartPairing: %v", err)
	}
	srv.mu.Lock()
	ps = srv.pairingService
	srv.mu.Unlock()
	if ps != nil {
		ps.Stop()
	}

	if store.GetSecret() != "stable-secret" {
		t.Fatalf("secret should remain stable across pairing cycles, got %q", store.GetSecret())
	}
}

func TestHandleConn_EmptySecret_RejectedInProduction(t *testing.T) {
	withAllowEmptySecret(t, false)
	store := NewSecretStore()
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	// No auth message — server should close the connection (empty secret rejected).
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection close for empty secret in production")
	}
}

func TestHandleConn_EmptySecret_AllowedWithEnvVar(t *testing.T) {
	withAllowEmptySecret(t, true)
	store := NewSecretStore()
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	// MVP mode: auth_ack is sent before the read loop.
	readAuthAck(t, conn)
}

func TestHandleConn_AuthMismatch_EmitsAuthFailed(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("correct-secret")
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	authFailedCalled := false
	origSink := authFailedSink
	authFailedSink = func() { authFailedCalled = true }
	t.Cleanup(func() { authFailedSink = origSink })

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	auth := AuthMessage{Type: MsgTypeAuth, Secret: "wrong-secret"}
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, auth))

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection close for auth mismatch")
	}
	if !authFailedCalled {
		t.Fatal("authFailedSink should have been called on auth mismatch")
	}
}
