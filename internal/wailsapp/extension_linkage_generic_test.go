//go:build !extractor

package wailsapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/extension"

	"github.com/gorilla/websocket"
)

type genericExtensionTaskAdder struct {
	mu      sync.Mutex
	request extension.DownloadRequest
	calls   int
}

func (a *genericExtensionTaskAdder) AddUriFromExtension(req extension.DownloadRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.request = req
	a.calls++

	return "fixture-gid", nil
}

func TestConfigureExtensionLinkageGenericProtocol2KeepsDownloadAndOmitsExtractor(t *testing.T) {
	adder := &genericExtensionTaskAdder{}
	store := extension.NewSecretStore()
	store.SetSecret("generic-fixture-secret")
	srv := extension.NewServer(nil, adder, store)
	srv.SetHostVersion("3.3.0")
	ConfigureExtensionLinkage(NewApp(Options{}), srv)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn := dialGenericExtension(t, srv.GetStatus().WSPort)
	defer conn.Close()
	writeGenericJSON(t, conn, extension.AuthMessage{
		Type:            extension.MsgTypeAuth,
		Secret:          "generic-fixture-secret",
		ClientVersion:   "0.2.0",
		ProtocolVersion: extension.ProtocolVersion,
	})

	var ack extension.AuthAck
	raw := readGenericJSON(t, conn, &ack)
	if ack.Type != extension.MsgTypeAuthAck || ack.ProtocolVersion != extension.ProtocolVersion {
		t.Fatalf("unexpected auth ack: %+v", ack)
	}
	if ack.HostVersion != "3.3.0" {
		t.Fatalf("host_version = %q", ack.HostVersion)
	}
	if !hasGenericCapability(ack.Capabilities, extension.CapRequestID) {
		t.Fatalf("request_id missing: %v", ack.Capabilities)
	}
	if hasGenericCapability(ack.Capabilities, extension.CapExtractorResolve) ||
		hasGenericCapability(ack.Capabilities, extension.CapExtractorBatch) {
		t.Fatalf("unexpected extractor capabilities: %v", ack.Capabilities)
	}
	if ack.Match != nil {
		t.Fatal("generic auth ack must omit match")
	}
	var ackFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ackFields); err != nil {
		t.Fatalf("decode auth ack fields: %v", err)
	}
	if _, ok := ackFields["match"]; ok {
		t.Fatalf("generic auth ack contains match: %s", raw)
	}

	assertGenericUnavailable(
		t,
		conn,
		extension.MsgTypeExtractorResolve,
		extension.MsgTypeExtractorResolveAck,
		"resolve-fixture",
	)
	assertGenericUnavailable(
		t,
		conn,
		extension.MsgTypeBatchDownload,
		extension.MsgTypeBatchDownloadAck,
		"batch-fixture",
	)

	writeGenericJSON(t, conn, extension.DownloadRequest{
		Type:      extension.MsgTypeDownload,
		RequestID: "download-fixture",
		URL:       "https://download.alpha.test/file.bin",
	})
	var downloadAck extension.DownloadResponse
	readGenericJSON(t, conn, &downloadAck)
	if downloadAck.Type != extension.MsgTypeDownloadAck ||
		!downloadAck.Success || downloadAck.GID != "fixture-gid" ||
		downloadAck.RequestID != "download-fixture" {
		t.Fatalf("unexpected download ack: %+v", downloadAck)
	}

	adder.mu.Lock()
	defer adder.mu.Unlock()
	if adder.calls != 1 || adder.request.URL != "https://download.alpha.test/file.bin" {
		t.Fatalf("task adder calls=%d request=%+v", adder.calls, adder.request)
	}
}

func dialGenericExtension(t *testing.T, port int) *websocket.Conn {
	t.Helper()
	headers := http.Header{}
	headers.Set("Origin", "chrome-extension://fixture")
	conn, resp, err := (&websocket.Dialer{HandshakeTimeout: 2 * time.Second}).Dial(
		fmt.Sprintf("ws://127.0.0.1:%d/", port),
		headers,
	)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("dial: %v", err)
	}

	return conn
}

func writeGenericJSON(t *testing.T, conn *websocket.Conn, value any) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}
	if err := conn.WriteJSON(value); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readGenericJSON(t *testing.T, conn *websocket.Conn, value any) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}

	return raw
}

func assertGenericUnavailable(
	t *testing.T,
	conn *websocket.Conn,
	messageType string,
	ackType string,
	requestID string,
) {
	t.Helper()
	writeGenericJSON(t, conn, extension.RequestEnvelope{Type: messageType, RequestID: requestID})
	var ack extension.TypedAck
	readGenericJSON(t, conn, &ack)
	if ack.Type != ackType ||
		ack.RequestID != requestID ||
		ack.ErrorCode != extension.ErrCodeUnavailable {
		t.Fatalf("%s ack: %+v", messageType, ack)
	}
}

func hasGenericCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}

	return false
}
