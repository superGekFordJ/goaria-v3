package extension

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func parseAuthAck(t *testing.T, conn *websocket.Conn) AuthAck {
	t.Helper()
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("unmarshal auth_ack: %v raw=%s", err, raw)
	}
	return ack
}

func TestLinkageReplacement_TargetedAndPreservedState(t *testing.T) {
	store := NewSecretStore()
	secret := store.GenerateSecret()
	store.SetSecret(secret)
	initialSecretGen := store.Generation()

	oldRes := &fakeResolver{ready: true, code: "old"}
	oldDigests := &fakeDigests{ready: true, ok: true, salt: "11111111111111111111111111111111"}
	oldCommitter := &fakeCommitter{ready: true, code: "old"}
	direct := &fakeDirectCommitter{ready: true}

	srv := NewServer(nil, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:        oldRes,
		Digests:         oldDigests,
		Committer:       oldCommitter,
		DirectCommitter: direct,
	})
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn1 := dialAuthed(t, srv, secret)
	if !srv.GetStatus().Paired {
		t.Fatal("expected paired: true after auth")
	}

	newRes := &fakeResolver{ready: true, code: "new"}
	newDigests := &fakeDigests{ready: true, ok: true, salt: "22222222222222222222222222222222"}
	newCommitter := &fakeCommitter{ready: true, code: "new"}

	srv.ReplaceExtractorLinkage(Linkage{
		Resolver:  newRes,
		Digests:   newDigests,
		Committer: newCommitter,
	})

	// 1. Invalidate only old Resolver
	if oldRes.invalidateCalls.Load() != 1 {
		t.Fatalf("expected old resolver invalidated once, got %d", oldRes.invalidateCalls.Load())
	}
	if newRes.invalidateCalls.Load() != 0 {
		t.Fatalf("expected new resolver not invalidated, got %d", newRes.invalidateCalls.Load())
	}
	if direct.invalidateCalls.Load() != 0 {
		t.Fatal("expected direct committer NOT invalidated")
	}

	// 2. Preserved state: paired, secret, secret generation, direct committer
	if !srv.GetStatus().Paired {
		t.Fatal("expected paired state preserved")
	}
	if store.Generation() != initialSecretGen {
		t.Fatalf("expected secret generation preserved, got %d vs %d", store.Generation(), initialSecretGen)
	}
	if store.GetSecret() != secret {
		t.Fatal("expected secret value preserved")
	}

	// 3. conn1 was closed by replacement
	conn1.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, _, err := conn1.ReadMessage()
	if err == nil {
		t.Fatal("expected conn1 read to fail after replacement socket close")
	}

	// 4. Server listener is alive and accepts reconnect
	conn2 := dialAuthed(t, srv, secret)
	defer conn2.Close()
}

func TestLinkageReplacement_EmptyReadyTransitions(t *testing.T) {
	store := NewSecretStore()
	secret := store.GenerateSecret()
	store.SetSecret(secret)
	direct := &fakeDirectCommitter{ready: true}

	srv := NewServer(nil, nil, store)
	// Start with empty extractor linkage, but ready direct committer
	srv.SetLinkage(Linkage{DirectCommitter: direct})
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	// 1. empty -> client sees no extractor capabilities, match is nil, download.batch present
	conn1 := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	_ = conn1.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: secret}))
	ack1 := parseAuthAck(t, conn1)
	if hasCap(ack1.Capabilities, CapExtractorResolve) || hasCap(ack1.Capabilities, CapExtractorBatch) {
		t.Fatalf("empty linkage must not have extractor caps: %v", ack1.Capabilities)
	}
	if !hasCap(ack1.Capabilities, CapDownloadBatch) {
		t.Fatalf("direct download.batch must be present: %v", ack1.Capabilities)
	}
	if ack1.Match != nil {
		t.Fatal("match must be nil for empty linkage")
	}
	conn1.Close()

	// 2. empty -> ready
	res1 := &fakeResolver{ready: true, code: "res1"}
	dig1 := &fakeDigests{ready: true, ok: true, salt: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	com1 := &fakeCommitter{ready: true, code: "com1"}
	srv.ReplaceExtractorLinkage(Linkage{Resolver: res1, Digests: dig1, Committer: com1})

	conn2 := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	_ = conn2.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: secret}))
	ack2 := parseAuthAck(t, conn2)
	if !hasCap(ack2.Capabilities, CapExtractorResolve) || !hasCap(ack2.Capabilities, CapExtractorBatch) {
		t.Fatalf("ready linkage must have extractor caps: %v", ack2.Capabilities)
	}
	if ack2.Match == nil || ack2.Match.Salt != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("unexpected match on ready linkage: %#v", ack2.Match)
	}
	conn2.Close()

	// 3. ready -> ready with new salt
	res2 := &fakeResolver{ready: true, code: "res2"}
	dig2 := &fakeDigests{ready: true, ok: true, salt: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	com2 := &fakeCommitter{ready: true, code: "com2"}
	srv.ReplaceExtractorLinkage(Linkage{Resolver: res2, Digests: dig2, Committer: com2})

	conn3 := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	_ = conn3.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: secret}))
	ack3 := parseAuthAck(t, conn3)
	if ack3.Match == nil || ack3.Match.Salt != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("expected updated match salt: %#v", ack3.Match)
	}
	conn3.Close()

	// 4. ready -> empty
	srv.ReplaceExtractorLinkage(Linkage{})

	conn4 := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	_ = conn4.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: secret}))
	ack4 := parseAuthAck(t, conn4)
	if hasCap(ack4.Capabilities, CapExtractorResolve) || hasCap(ack4.Capabilities, CapExtractorBatch) {
		t.Fatalf("reverted empty linkage must not have extractor caps: %v", ack4.Capabilities)
	}
	if !hasCap(ack4.Capabilities, CapDownloadBatch) {
		t.Fatalf("direct download.batch must still be present: %v", ack4.Capabilities)
	}
	if ack4.Match != nil {
		t.Fatal("match must be nil after reverting to empty linkage")
	}
	conn4.Close()
}

func TestLinkageReplacement_IdempotencyRetirementAndDirectPreserved(t *testing.T) {
	store := NewSecretStore()
	secret := store.GenerateSecret()
	store.SetSecret(secret)

	res1 := &fakeResolver{
		ready: true,
		result: &ResolveResult{
			Matched:   true,
			SessionID: "sess-1",
			Items:     []ResolveDisplayItem{{ItemID: "item-1", Filename: "file-1"}},
		},
	}
	dig1 := &fakeDigests{ready: true, ok: true, salt: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	direct := &fakeDirectCommitter{
		ready: true,
		result: &DirectCommitResult{
			Success:          true,
			SucceededItemIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}

	srv := NewServer(nil, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:        res1,
		Digests:         dig1,
		DirectCommitter: direct,
	})
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn1 := dialAuthed(t, srv, secret)

	// Resolve request on conn1
	writeResolve(t, conn1, "req-resolve-1", `"url":"https://example.com"`)
	rawResp1 := readRaw(t, conn1, 2*time.Second)
	var ack1 ExtractorResolveAck
	if err := json.Unmarshal(rawResp1, &ack1); err != nil || ack1.ErrorCode != "" {
		t.Fatalf("resolve 1 failed: %v, raw: %s", err, rawResp1)
	}

	directReqID := "11111111-1111-4111-8111-111111111111"
	// Direct batch request on conn1
	writeDirectBatch(t, conn1, directReqID, `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://example.com/file"}]`)
	rawDirect1 := readRaw(t, conn1, 2*time.Second)
	var directAck1 DirectBatchAck
	if err := json.Unmarshal(rawDirect1, &directAck1); err != nil || !directAck1.Success {
		t.Fatalf("direct batch 1 failed: %v, raw: %s", err, rawDirect1)
	}

	// Now replace extractor linkage
	res2 := &fakeResolver{
		ready: true,
		result: &ResolveResult{
			Matched:   true,
			SessionID: "sess-2",
			Items:     []ResolveDisplayItem{{ItemID: "item-2", Filename: "file-2"}},
		},
	}
	dig2 := &fakeDigests{ready: true, ok: true, salt: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	srv.ReplaceExtractorLinkage(Linkage{
		Resolver: res2,
		Digests:  dig2,
	})

	conn2 := dialAuthed(t, srv, secret)
	defer conn2.Close()

	// Sending resolve with the same request ID req-resolve-1 on conn2 must resolve fresh on res2, NOT hit res1 cache!
	writeResolve(t, conn2, "req-resolve-1", `"url":"https://example.com"`)
	rawResp2 := readRaw(t, conn2, 2*time.Second)
	var ack2 ExtractorResolveAck
	if err := json.Unmarshal(rawResp2, &ack2); err != nil || ack2.ErrorCode != "" {
		t.Fatalf("resolve 2 failed: %v, raw: %s", err, rawResp2)
	}
	if len(ack2.Items) != 1 || ack2.Items[0].ItemID != "item-2" {
		t.Fatalf("expected fresh resolve from res2 (item-2), got %#v (hit old cache)", ack2.Items)
	}

	// Sending direct batch with same request ID on conn2 MUST hit direct idempotency cache!
	writeDirectBatch(t, conn2, directReqID, `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://example.com/file"}]`)
	rawDirect2 := readRaw(t, conn2, 2*time.Second)
	var directAck2 DirectBatchAck
	if err := json.Unmarshal(rawDirect2, &directAck2); err != nil || !directAck2.Success {
		t.Fatalf("direct batch 2 failed: %v, raw: %s", err, rawDirect2)
	}
	// Verify that directCommitter was not invoked a second time (idempotency hit!)
	if direct.calls.Load() != 1 {
		t.Fatalf("expected direct idempotency hit (calls = 1), got %d", direct.calls.Load())
	}
}

func TestLinkageReplacement_LateOldHandlerCannotPolluteNewGeneration(t *testing.T) {
	store := NewSecretStore()
	secret := store.GenerateSecret()
	store.SetSecret(secret)

	blockCh := make(chan struct{})
	startedCh := make(chan struct{}, 1)
	res1 := &fakeResolver{
		ready:   true,
		block:   blockCh,
		started: startedCh,
		result: &ResolveResult{
			Matched:   true,
			SessionID: "sess-1-old",
			Items:     []ResolveDisplayItem{{ItemID: "old-item", Filename: "old-file"}},
		},
	}
	dig1 := &fakeDigests{ready: true, ok: true, salt: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}

	srv := NewServer(nil, nil, store)
	srv.SetLinkage(Linkage{Resolver: res1, Digests: dig1})
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn1 := dialAuthed(t, srv, secret)

	// Send resolve with req-race
	writeResolve(t, conn1, "req-race", `"url":"https://example.com"`)

	// Wait until handler starts
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for old handler to start")
	}

	// While old handler is blocked in-flight, replace linkage
	res2 := &fakeResolver{
		ready: true,
		result: &ResolveResult{
			Matched:   true,
			SessionID: "sess-2-new",
			Items:     []ResolveDisplayItem{{ItemID: "new-item", Filename: "new-file"}},
		},
	}
	dig2 := &fakeDigests{ready: true, ok: true, salt: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	srv.ReplaceExtractorLinkage(Linkage{Resolver: res2, Digests: dig2})

	// Now unblock old handler
	close(blockCh)

	// Connect new client
	conn2 := dialAuthed(t, srv, secret)
	defer conn2.Close()

	// Send resolve with the same req-race
	writeResolve(t, conn2, "req-race", `"url":"https://example.com"`)
	rawResp2 := readRaw(t, conn2, 2*time.Second)
	var ack2 ExtractorResolveAck
	if err := json.Unmarshal(rawResp2, &ack2); err != nil || ack2.ErrorCode != "" {
		t.Fatalf("resolve on conn2 failed: %v, raw: %s", err, rawResp2)
	}
	if len(ack2.Items) != 1 || ack2.Items[0].ItemID != "new-item" {
		t.Fatalf("expected resolution on res2 (new-item), got %#v (polluted by late old handler!)", ack2.Items)
	}
}

func TestLinkageReplacement_BeforeStartOrWithoutActiveConns(t *testing.T) {
	store := NewSecretStore()
	srv := NewServer(nil, nil, store)

	// Replace before Start(0)
	srv.ReplaceExtractorLinkage(Linkage{})

	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	// Replace with 0 active connections
	srv.ReplaceExtractorLinkage(Linkage{
		Resolver: &fakeResolver{ready: true},
	})
	srv.ReplaceExtractorLinkage(Linkage{})
}

type barrierCommitter struct {
	ready    bool
	started  chan struct{}
	block    chan struct{}
	finished chan struct{}
	result   CommitResult
}

func (c *barrierCommitter) Ready() bool { return c.ready }

func (c *barrierCommitter) HandleCommit(_ context.Context, _ RequestEnvelope, _ json.RawMessage) CommitResult {
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.block != nil {
		<-c.block
	}
	if c.finished != nil {
		defer close(c.finished)
	}
	return c.result
}

func TestLinkageReplacement_LateOldCommitCannotPolluteNewGeneration(t *testing.T) {
	store := NewSecretStore()
	secret := store.GenerateSecret()
	store.SetSecret(secret)

	blockCh := make(chan struct{})
	startedCh := make(chan struct{}, 1)
	finishedCh := make(chan struct{})
	comm1 := &barrierCommitter{
		ready:    true,
		started:  startedCh,
		block:    blockCh,
		finished: finishedCh,
		result: CommitResult{
			Success:          true,
			SucceededItemIDs: []string{"old-success-item"},
		},
	}
	res1 := &fakeResolver{ready: true}

	srv := NewServer(nil, nil, store)
	srv.SetLinkage(Linkage{Resolver: res1, Committer: comm1})
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	conn1 := dialAuthed(t, srv, secret)

	// Send batch_download with req-commit-race
	writeBatch(t, conn1, "req-commit-race", `"session_id":"s1","item_ids":["i1"]`)

	// Wait until comm1 starts
	select {
	case <-startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for old commit handler to start")
	}

	// While old commit handler is blocked in-flight, replace linkage with comm2
	comm2 := &barrierCommitter{
		ready: true,
		result: CommitResult{
			Success:          true,
			SucceededItemIDs: []string{"new-success-item"},
		},
	}
	res2 := &fakeResolver{ready: true}
	srv.ReplaceExtractorLinkage(Linkage{Resolver: res2, Committer: comm2})

	// Now unblock old handler so it finishes on old generation
	close(blockCh)

	// Wait until old handler finishes execution
	select {
	case <-finishedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for old commit handler to finish")
	}

	// Ensure the server's batch gate has been fully released before sending new request
	for range 50 {
		if srv.batchInFlight.Load() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if srv.batchInFlight.Load() != 0 {
		t.Fatalf("batch gate was not released after old handler completion")
	}

	// Connect new client
	conn2 := dialAuthed(t, srv, secret)
	defer conn2.Close()

	// Send batch_download with the same request ID
	writeBatch(t, conn2, "req-commit-race", `"session_id":"s1","item_ids":["i1"]`)
	rawResp2 := readRaw(t, conn2, 2*time.Second)
	var ack2 BatchDownloadAck
	if err := json.Unmarshal(rawResp2, &ack2); err != nil || !ack2.Success {
		t.Fatalf("commit on conn2 failed: %v, raw: %s", err, rawResp2)
	}
	if len(ack2.SucceededItemIDs) != 1 || ack2.SucceededItemIDs[0] != "new-success-item" {
		t.Fatalf("expected commit on comm2 (new-success-item), got %#v (polluted by late old handler!)", ack2.SucceededItemIDs)
	}
}
