package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type fakeDirectCommitter struct {
	ready           bool
	result          *DirectCommitResult
	calls           atomic.Int32
	lookupCalls     atomic.Int32
	invalidateCalls atomic.Int32
	block           chan struct{}
	started         chan struct{}
	status          DirectStatusSnapshot
	statusOK        bool
}

func (f *fakeDirectCommitter) Ready() bool { return f.ready }

func (f *fakeDirectCommitter) AdmitPending(string, string) bool { return true }

func (f *fakeDirectCommitter) AbandonPending(string) {}

func (f *fakeDirectCommitter) HandleDirectBatch(ctx context.Context, _ RequestEnvelope, _ DirectBatchRequest) DirectCommitResult {
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
			return DirectCommitResult{ErrorCode: ErrCodeBusy, SkipIdempotency: true}
		}
	}
	if f.result != nil {
		return *f.result
	}
	return DirectCommitResult{
		Success:          true,
		SucceededItemIDs: []string{},
		DuplicateItemIDs: []string{},
		ErrorsByItemID:   map[string]string{},
	}
}

func (f *fakeDirectCommitter) LookupStatus(string) (DirectStatusSnapshot, bool) {
	f.lookupCalls.Add(1)
	if f.invalidateCalls.Load() > 0 {
		return DirectStatusSnapshot{}, false
	}
	if !f.statusOK {
		return DirectStatusSnapshot{}, false
	}
	return f.status, true
}

func (f *fakeDirectCommitter) Invalidate() {
	f.invalidateCalls.Add(1)
}

func TestAuthAck_DirectBatchRequiresReadyAndSecret(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("direct-secret")
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		DirectCommitter: &fakeDirectCommitter{ready: true},
	})
	defer srv.Stop()
	startSrv(t, srv)

	conn := dialWS(t, srv.GetStatus().WSPort, "chrome-extension://abc")
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteMessage(websocket.TextMessage, mustMarshal(t, AuthMessage{Type: MsgTypeAuth, Secret: "direct-secret"}))
	raw := readRaw(t, conn, 2*time.Second)
	var ack AuthAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatal(err)
	}
	if !hasCap(ack.Capabilities, CapDownloadBatch) {
		t.Fatalf("want download.batch, got %v", ack.Capabilities)
	}
	if hasCap(ack.Capabilities, CapExtractorResolve) || hasCap(ack.Capabilities, CapExtractorBatch) {
		t.Fatalf("extractor caps must stay off: %v", ack.Capabilities)
	}
}

func TestDownloadBatch_MissingCapUnavailable(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	direct := &fakeDirectCommitter{ready: true}
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeDirectBatch(t, conn, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://download.fixture.invalid/a.bin"}]`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnavailable {
		t.Fatalf("missing cap ack = %+v", ack)
	}
	if direct.calls.Load() != 0 {
		t.Fatalf("HandleDirectBatch must not run, calls=%d", direct.calls.Load())
	}
}

func TestDownloadBatch_LateSetLinkageDoesNotGrant(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	direct := &fakeDirectCommitter{ready: true}
	srv := newTestServer(t, nil, store)
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	srv.SetLinkage(Linkage{DirectCommitter: direct})
	writeDirectBatch(t, conn, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://download.fixture.invalid/a.bin"}]`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeUnavailable {
		t.Fatalf("snapshot must keep download.batch off, got %+v", ack)
	}
	if direct.calls.Load() != 0 {
		t.Fatalf("HandleDirectBatch must not run, calls=%d", direct.calls.Load())
	}
}

func TestDownloadBatch_SchemaInvalidNotSticky(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	direct := &fakeDirectCommitter{ready: true}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{DirectCommitter: direct})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeDirectBatch(t, conn, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://download.fixture.invalid/a.bin"}],"session_id":"nope"`)
	first := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if first.ErrorCode != ErrCodeInvalidRequest {
		t.Fatalf("first ack = %+v", first)
	}
	writeDirectBatch(t, conn, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://download.fixture.invalid/a.bin"}],"session_id":"nope"`)
	second := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if second.ErrorCode != ErrCodeInvalidRequest {
		t.Fatalf("retry ack = %+v", second)
	}
	if direct.calls.Load() != 0 {
		t.Fatalf("schema invalid must not enter committer, calls=%d", direct.calls.Load())
	}
}

func TestDownloadBatch_SharesBatchGateBusy(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	direct := &fakeDirectCommitter{ready: true, block: block, started: started}
	committer := &fakeCommitter{ready: true, result: &CommitResult{Success: true}}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{
		Resolver:        &fakeResolver{ready: true},
		Committer:       committer,
		DirectCommitter: direct,
	})
	defer srv.Stop()
	t.Cleanup(func() { close(block) })
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeDirectBatch(t, conn, "aaaaaaaa-bbbb-cccc-dddd-111111111111", `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://download.fixture.invalid/a.bin"}]`)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("direct batch did not start")
	}

	writeBatch(t, conn, "b-busy", `"session_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","item_ids":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]`)
	ack := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if ack.ErrorCode != ErrCodeBusy {
		t.Fatalf("shared gate ack = %+v", ack)
	}
	if committer.calls.Load() != 0 {
		t.Fatalf("extractor commit must wait behind gate, calls=%d", committer.calls.Load())
	}
}

func TestDownloadBatchStatus_NeverAddUri(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	direct := &fakeDirectCommitter{
		ready:    true,
		statusOK: true,
		status:   DirectStatusSnapshot{Status: DirectBatchStatusPending},
	}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{DirectCommitter: direct})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	writeDirectBatchStatus(t, conn, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	raw := readRaw(t, conn, 2*time.Second)
	var ack DirectBatchStatusAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("status ack: %v raw=%s", err, raw)
	}
	if ack.Type != MsgTypeDownloadBatchStatusAck || ack.Status != DirectBatchStatusPending {
		t.Fatalf("status ack = %+v", ack)
	}
	if direct.calls.Load() != 0 {
		t.Fatalf("status must not HandleDirectBatch, calls=%d", direct.calls.Load())
	}
	if direct.lookupCalls.Load() != 1 {
		t.Fatalf("lookup calls = %d", direct.lookupCalls.Load())
	}
}

func TestDownloadBatchStatus_NotFoundAfterInvalidate(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	direct := &fakeDirectCommitter{
		ready:    true,
		statusOK: true,
		status: DirectStatusSnapshot{
			Status:           DirectBatchStatusComplete,
			Success:          true,
			SucceededItemIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{DirectCommitter: direct})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")

	writeDirectBatchStatus(t, conn, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	raw := readRaw(t, conn, 2*time.Second)
	var ack DirectBatchStatusAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		t.Fatalf("status ack: %v raw=%s", err, raw)
	}
	if ack.Status != DirectBatchStatusComplete {
		t.Fatalf("pre-unpair status = %+v raw=%s", ack, raw)
	}

	srv.NotifyUnpaired()
	if direct.invalidateCalls.Load() < 1 {
		t.Fatalf("unpair must Invalidate Direct receipts, calls=%d", direct.invalidateCalls.Load())
	}
	_ = conn.Close()

	newSecret := store.GetSecret()
	if newSecret == "" {
		t.Fatal("expected a secret after unpair")
	}
	conn2 := dialAuthed(t, srv, newSecret)
	defer conn2.Close()
	writeDirectBatchStatus(t, conn2, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	raw2 := readRaw(t, conn2, 2*time.Second)
	var ack2 DirectBatchStatusAck
	if err := json.Unmarshal(raw2, &ack2); err != nil {
		t.Fatalf("post-unpair status ack: %v raw=%s", err, raw2)
	}
	if ack2.Status != DirectBatchStatusNotFound {
		t.Fatalf("want not_found after unpair wipe, got %+v raw=%s", ack2, raw2)
	}
}

func TestDownloadBatch_SkipIdempotencyRetriesImmediately(t *testing.T) {
	store := NewSecretStore()
	store.SetSecret("prod-secret")
	direct := &fakeDirectCommitter{ready: true, result: &DirectCommitResult{
		ErrorCode:       ErrCodeUnavailable,
		SkipIdempotency: true,
	}}
	srv := newTestServer(t, nil, store)
	srv.SetLinkage(Linkage{DirectCommitter: direct})
	defer srv.Stop()
	startSrv(t, srv)
	conn := dialAuthed(t, srv, "prod-secret")
	defer conn.Close()

	extra := `"items":[{"client_item_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","url":"https://download.fixture.invalid/a.bin"}]`
	writeDirectBatch(t, conn, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", extra)
	first := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if first.ErrorCode != ErrCodeUnavailable {
		t.Fatalf("first ack = %+v", first)
	}
	writeDirectBatch(t, conn, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", extra)
	second := parseTypedAck(t, readRaw(t, conn, 2*time.Second))
	if second.ErrorCode != ErrCodeUnavailable {
		t.Fatalf("retry ack = %+v", second)
	}
	if direct.calls.Load() != 2 {
		t.Fatalf("SkipIdempotency must re-run HandleDirectBatch, calls=%d", direct.calls.Load())
	}
}

func TestMarshalDirectBatchResult_EmptyErrorsObject(t *testing.T) {
	data, err := marshalDirectBatchResult(MsgTypeDownloadBatchAck, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", DirectCommitResult{
		Success:          true,
		SucceededItemIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		DuplicateItemIDs: []string{},
		ErrorsByItemID:   map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"errors_by_item_id":{}`)) {
		t.Fatalf("errors_by_item_id must be object, got %s", data)
	}
	if bytes.Contains(data, []byte(`"errors_by_item_id":null`)) {
		t.Fatalf("errors_by_item_id must not be null: %s", data)
	}
}

func TestDirectBatchStatusAck_CompleteKeepsPartitions(t *testing.T) {
	data, err := json.Marshal(DirectBatchStatusAck{
		Type:             MsgTypeDownloadBatchStatusAck,
		RequestID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Status:           DirectBatchStatusComplete,
		Success:          false,
		SucceededItemIDs: []string{},
		DuplicateItemIDs: []string{},
		ErrorsByItemID:   map[string]string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": CommitItemErrorAddFailed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"success":false`)) {
		t.Fatalf("complete failure must keep success:false, got %s", data)
	}
	if !bytes.Contains(data, []byte(`"succeeded_item_ids":[]`)) || !bytes.Contains(data, []byte(`"duplicate_item_ids":[]`)) {
		t.Fatalf("complete partitions must stay arrays, got %s", data)
	}
}

func TestDirectBatchStatusAck_CompleteSuccessEmptyErrorsObject(t *testing.T) {
	data, err := json.Marshal(DirectBatchStatusAck{
		Type:             MsgTypeDownloadBatchStatusAck,
		RequestID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Status:           DirectBatchStatusComplete,
		Success:          true,
		SucceededItemIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		DuplicateItemIDs: []string{},
		ErrorsByItemID:   map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"success":true`)) {
		t.Fatalf("complete success must keep success:true, got %s", data)
	}
	if !bytes.Contains(data, []byte(`"errors_by_item_id":{}`)) {
		t.Fatalf("complete success must emit empty errors object, got %s", data)
	}
	if bytes.Contains(data, []byte(`"errors_by_item_id":null`)) {
		t.Fatalf("errors_by_item_id must not be null: %s", data)
	}
}

func writeDirectBatch(t *testing.T, conn interface {
	SetWriteDeadline(time.Time) error
	WriteMessage(int, []byte) error
}, id, extra string,
) {
	t.Helper()
	payload := `{"type":"` + MsgTypeDownloadBatch + `","request_id":"` + id + `"`
	if extra != "" {
		payload += "," + extra
	}
	payload += `}`
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write direct batch: %v", err)
	}
}

func writeDirectBatchStatus(t *testing.T, conn interface {
	SetWriteDeadline(time.Time) error
	WriteMessage(int, []byte) error
}, id string,
) {
	t.Helper()
	payload := `{"type":"` + MsgTypeDownloadBatchStatus + `","request_id":"` + id + `"}`
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
		t.Fatalf("write direct status: %v", err)
	}
}
