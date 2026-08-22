//go:build extractor

package wailsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"

	"github.com/gorilla/websocket"
)

type testReadyResolver struct{}

func (testReadyResolver) Ready() bool { return true }

func (testReadyResolver) HandleResolve(context.Context, extension.RequestEnvelope, json.RawMessage) extension.ResolveResult {
	return extension.ResolveResult{ErrorCode: extension.ErrCodeUnsupported}
}

func (testReadyResolver) Invalidate() {}

func (testReadyResolver) RewriteCachedResolve(cached []byte) []byte { return cached }

type testReadyDigests struct{}

func (testReadyDigests) Ready() bool { return true }

func (testReadyDigests) Snapshot() (extension.MatchDigestSnapshot, bool) {
	return extension.MatchDigestSnapshot{
		Version:          extension.MatchDigestVersion,
		Salt:             "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ExactDigests:     []string{},
		SubdomainDigests: []string{},
	}, true
}

func TestConfigureExtensionLinkageAppliesPending(t *testing.T) {
	app := NewApp(Options{})
	app.setPendingExtensionLinkage(extension.Linkage{
		Resolver: testReadyResolver{},
		Digests:  testReadyDigests{},
	})

	store := extension.NewSecretStore()
	store.SetSecret("link-secret")
	srv := extension.NewServer(nil, nil, store)
	ConfigureExtensionLinkage(app, srv)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ack := dialAuthAck(t, srv.GetStatus().WSPort, "link-secret")
	if !hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("pending linkage should advertise extractor.resolve, got %v", ack.Capabilities)
	}
	if ack.Match == nil {
		t.Fatal("match missing after pending linkage")
	}
}

func TestConfigureExtensionLinkageNilPendingNoop(t *testing.T) {
	app := NewApp(Options{})
	store := extension.NewSecretStore()
	store.SetSecret("link-secret")
	srv := extension.NewServer(nil, nil, store)
	ConfigureExtensionLinkage(app, srv)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ack := dialAuthAck(t, srv.GetStatus().WSPort, "link-secret")
	if hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("nil pending must stay request_id-only, got %v", ack.Capabilities)
	}
	if ack.Match != nil {
		t.Fatal("match must be omitted without pending linkage")
	}
}

func TestAuthAck_RealIngressDigestOmitsFixtureHost(t *testing.T) {
	const fixture = "cdn.example.test"
	src := extractor.NewIngressDigestSourceFromLegacyRules([]extractor.DomainRule{{Host: fixture}})
	if !src.Ready() {
		t.Fatal("digest source Ready()=false")
	}

	store := extension.NewSecretStore()
	store.SetSecret("link-secret")
	srv := extension.NewServer(nil, nil, store)
	srv.SetLinkage(extension.Linkage{
		Resolver: testReadyResolver{},
		Digests:  &ingressDigestAdapter{src: src},
	})
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ack, raw := dialAuthAckRaw(t, srv.GetStatus().WSPort, "link-secret")
	if ack.Match == nil {
		t.Fatalf("match missing: %s", raw)
	}
	if bytes.Contains(raw, []byte(fixture)) {
		t.Fatalf("fixture host leaked in auth_ack: %s", raw)
	}
}

func TestPendingLinkageFromDispatcherKeepsDigests(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	linkage := pendingLinkageFromDispatcher(dispatcher)
	if linkage.Resolver == nil || !linkage.Resolver.Ready() {
		t.Fatal("resolver must be ready")
	}
	if linkage.Digests == nil || !linkage.Digests.Ready() {
		t.Fatal("factory must keep Digests ready")
	}
	if linkage.Committer != nil {
		t.Fatal("batch committer must stay nil")
	}

	app := NewApp(Options{})
	app.setPendingExtensionLinkage(linkage)
	store := extension.NewSecretStore()
	store.SetSecret("link-secret")
	srv := extension.NewServer(nil, nil, store)
	ConfigureExtensionLinkage(app, srv)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ack := dialAuthAck(t, srv.GetStatus().WSPort, "link-secret")
	if !hasCap(ack.Capabilities, extension.CapExtractorResolve) {
		t.Fatalf("pending linkage should advertise extractor.resolve, got %v", ack.Capabilities)
	}
	if hasCap(ack.Capabilities, extension.CapExtractorBatch) {
		t.Fatalf("extractor.batch must stay off, got %v", ack.Capabilities)
	}
	if ack.Match == nil {
		t.Fatal("match missing after dispatcher linkage")
	}
}

func TestAttachBatchCommitterGrantsExtractorBatchAndKeepsMatch(t *testing.T) {
	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	linkage := pendingLinkageFromDispatcher(dispatcher)
	if linkage.Committer != nil {
		t.Fatal("factory-alone Committer must stay nil")
	}
	adapter := extractor.NewTasksAdapter(dispatcher, nil)
	app := NewApp(Options{})
	linkage = attachBatchCommitter(linkage, adapter, app)
	if linkage.Committer == nil || !linkage.Committer.Ready() {
		t.Fatal("attach must set Ready Committer on the existing resolver")
	}

	app.setPendingExtensionLinkage(linkage)
	store := extension.NewSecretStore()
	store.SetSecret("link-secret")
	srv := extension.NewServer(nil, nil, store)
	ConfigureExtensionLinkage(app, srv)
	defer srv.Stop()
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ack := dialAuthAck(t, srv.GetStatus().WSPort, "link-secret")
	if !hasCap(ack.Capabilities, extension.CapExtractorResolve) || !hasCap(ack.Capabilities, extension.CapExtractorBatch) {
		t.Fatalf("attach path should advertise resolve+batch, got %v", ack.Capabilities)
	}
	if ack.Match == nil {
		t.Fatal("match missing after attach; Digests must be preserved")
	}
}

func dialAuthAck(t *testing.T, port int, secret string) extension.AuthAck {
	t.Helper()
	ack, _ := dialAuthAckRaw(t, port, secret)
	return ack
}

func dialAuthAckRaw(t *testing.T, port int, secret string) (extension.AuthAck, []byte) {
	t.Helper()
	u := fmt.Sprintf("ws://127.0.0.1:%d/", port)
	h := http.Header{}
	h.Set("Origin", "chrome-extension://abc")
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	conn, resp, err := dialer.Dial(u, h)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteJSON(extension.AuthMessage{Type: extension.MsgTypeAuth, Secret: secret}); err != nil {
		t.Fatalf("auth write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("auth_ack read: %v", err)
	}
	var ack extension.AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("auth_ack unmarshal: %v raw=%s", err, raw)
	}
	return ack, raw
}

func hasCap(caps []string, want string) bool {
	return slices.Contains(caps, want)
}
