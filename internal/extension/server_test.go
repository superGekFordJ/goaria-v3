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

func TestOrigin_AllowedChrome(t *testing.T) {
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

	// Server should close the connection.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection close for invalid secret")
	}
}

func TestDownload_ForwardedToTaskService(t *testing.T) {
	// HARD GATE: downloads must go through TaskAdder, never rpc.DownloadEngine.
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
