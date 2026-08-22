//go:build extractor

package wailsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

type recordingCommitEngine struct {
	mu       sync.Mutex
	urls     []string
	failURLs map[string]struct{}
	failAll  bool
	failNth  int
	addCalls int
}

func (e *recordingCommitEngine) AddUri(url string, _ rpc.AddURIOptions) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addCalls++
	e.urls = append(e.urls, url)
	if e.failAll {
		return "", errors.New("engine failed https://download.fixture.invalid/?token=leak apr-x r-9")
	}
	if e.failNth > 0 && e.addCalls == e.failNth {
		return "", errors.New("engine failed https://download.fixture.invalid/?token=leak apr-x r-9")
	}
	if _, ok := e.failURLs[url]; ok {
		return "", errors.New("engine failed https://download.fixture.invalid/?token=leak apr-x r-9")
	}
	return "gid-ok", nil
}

func (e *recordingCommitEngine) snapshotURLs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.urls))
	copy(out, e.urls)
	return out
}

func (e *recordingCommitEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.addCalls
}

func (e *recordingCommitEngine) Pause(string) error         { return nil }
func (e *recordingCommitEngine) Resume(string) error        { return nil }
func (e *recordingCommitEngine) PauseMulti([]string) error  { return nil }
func (e *recordingCommitEngine) ResumeMulti([]string) error { return nil }
func (e *recordingCommitEngine) Remove(string, bool) error  { return nil }
func (e *recordingCommitEngine) TellStatus(string, []string) (rpc.Task, error) {
	return rpc.Task{}, nil
}

func (e *recordingCommitEngine) TellStatusMulti([]string, []string) ([]rpc.Task, error) {
	return nil, nil
}
func (e *recordingCommitEngine) TellActive() ([]rpc.Task, error)                 { return nil, nil }
func (e *recordingCommitEngine) TellActiveLite() ([]rpc.Task, error)             { return nil, nil }
func (e *recordingCommitEngine) TellActiveProgress() ([]rpc.TaskProgress, error) { return nil, nil }
func (e *recordingCommitEngine) TellWaiting(int, int) ([]rpc.Task, error)        { return nil, nil }
func (e *recordingCommitEngine) TellWaitingLite(int, int) ([]rpc.Task, error)    { return nil, nil }
func (e *recordingCommitEngine) TellStopped(int, int) ([]rpc.Task, error)        { return nil, nil }
func (e *recordingCommitEngine) TellStoppedLite(int, int) ([]rpc.Task, error)    { return nil, nil }
func (e *recordingCommitEngine) GetGlobalStat() (rpc.GlobalStat, error) {
	return rpc.GlobalStat{}, nil
}
func (e *recordingCommitEngine) SaveSession() error                         { return nil }
func (e *recordingCommitEngine) ChangeGlobalOption(map[string]string) error { return nil }
func (e *recordingCommitEngine) StreamEvents(context.Context) (<-chan any, func(), error) {
	ch := make(chan any)
	close(ch)
	return ch, func() {}, nil
}
func (e *recordingCommitEngine) IsSurgeActive() bool { return false }

type batchCommitHarness struct {
	app    *App
	lease  *extensionResolveAdapter
	minter *extractor.TasksAdapter
	engine *recordingCommitEngine
	commit *extensionBatchAdapter
}

func setupBatchCommitHarness(t *testing.T) *batchCommitHarness {
	t.Helper()
	originalConfig := config.Get()
	originalSave := history.SaveEnabled
	history.DisableSaveForTest()
	history.Clear()
	monitor.ResetDownloadGroupNamerForTest()
	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	config.SetTestConfig(&config.AppConfig{
		DownloadDir:     t.TempDir(),
		SmartThreadMode: false,
		MaxConnections:  "8",
	})
	t.Cleanup(func() {
		history.Clear()
		history.SetSaveEnabled(originalSave)
		monitor.ResetDownloadGroupNamerForTest()
		monitor.ResetTaskGroupStoreForTest("", true)
		config.SetTestConfig(originalConfig)
	})

	dispatcher, _ := newHostCallFixtureDispatcher(t, &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`})
	lease := newExtensionResolveAdapter(dispatcher)
	minter := extractor.NewTasksAdapter(dispatcher, nil)
	engine := &recordingCommitEngine{failURLs: map[string]struct{}{}}
	app := NewApp(Options{DownloadEngine: engine})
	app.setExtractorAdapter(minter)

	return &batchCommitHarness{
		app:    app,
		lease:  lease,
		minter: minter,
		engine: engine,
		commit: &extensionBatchAdapter{lease: lease, minter: minter, app: app},
	}
}

func resolveFixtureSession(t *testing.T, lease *extensionResolveAdapter) extension.ResolveResult {
	t.Helper()
	hostOnly := false
	raw := mustJSON(t, extension.ExtractorResolveRequest{
		Type:      extension.MsgTypeExtractorResolve,
		SourceURL: packbuilder.HostCallFixtureShareURL,
		Cookies: []extension.BrowserCookie{{
			Name:     "sid",
			Value:    "browser-sid",
			Domain:   ".fixture.invalid",
			Path:     "/",
			Secure:   new(true),
			HostOnly: &hostOnly,
		}},
	})
	result := lease.HandleResolve(context.Background(), extension.RequestEnvelope{}, raw)
	if result.ErrorCode != "" || !result.Matched || result.SessionID == "" || len(result.Items) == 0 {
		t.Fatalf("resolve result = %+v", result)
	}

	return result
}

func commitRaw(sessionID string, itemIDs []string, createGroup bool, folderName string) json.RawMessage {
	payload := map[string]any{
		"type":       extension.MsgTypeBatchDownload,
		"session_id": sessionID,
		"item_ids":   itemIDs,
	}
	if createGroup {
		payload["create_group"] = true
	}
	if folderName != "" {
		payload["folder_name"] = folderName
	}
	raw, _ := json.Marshal(payload)

	return raw
}

func insertLeasedItem(t *testing.T, lease *extensionResolveAdapter, sessionID, itemID string, item extractor.ResolvedAddItem) {
	t.Helper()
	lease.mu.Lock()
	defer lease.mu.Unlock()
	session, ok := lease.sessions[sessionID]
	if !ok {
		t.Fatalf("session %s missing", sessionID)
	}
	session.items[itemID] = extractor.CloneResolvedAddItem(item)
}

func assertAckHasNoHost(t *testing.T, result extension.CommitResult) {
	t.Helper()
	raw, err := json.Marshal(extension.BatchDownloadAck{
		Type:             extension.MsgTypeBatchDownloadAck,
		Success:          result.Success,
		GroupKey:         result.GroupKey,
		SucceededItemIDs: result.SucceededItemIDs,
		DuplicateItemIDs: result.DuplicateItemIDs,
		ErrorsByItemID:   result.ErrorsByItemID,
		ErrorCode:        result.ErrorCode,
	})
	if err != nil {
		t.Fatalf("marshal ack: %v", err)
	}
	for _, forbidden := range []string{"download.fixture.invalid", "https://", "http://", packbuilder.HostCallFixtureItemURL, "auth_profile", "header_profile", "apr-", "r-9", "token=leak"} {
		if bytes.Contains(bytes.ToLower(raw), []byte(strings.ToLower(forbidden))) {
			t.Fatalf("ack leaked %q: %s", forbidden, raw)
		}
	}
}

func TestExtensionBatch_HostCallFixtureCommitConsumesLease(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID

	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-1"}, commitRaw(resolved.SessionID, []string{itemID}, false, ""))
	if result.ErrorCode != "" || !result.Success {
		t.Fatalf("commit = %+v", result)
	}
	if len(result.SucceededItemIDs) != 1 || result.SucceededItemIDs[0] != itemID {
		t.Fatalf("succeeded = %#v", result.SucceededItemIDs)
	}
	urls := h.engine.snapshotURLs()
	if len(urls) != 1 || urls[0] != packbuilder.HostCallFixtureItemURL {
		t.Fatalf("AddUri urls = %#v, want [%q]", urls, packbuilder.HostCallFixtureItemURL)
	}
	assertAckHasNoHost(t, result)
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID); ok {
		t.Fatal("lookupLeasedItem must miss after successful consume")
	}

	second := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-2"}, commitRaw(resolved.SessionID, []string{itemID}, false, ""))
	if second.ErrorCode != extension.ErrCodeInvalidRequest && second.ErrorCode != extension.ErrCodeSessionExpired {
		t.Fatalf("second commit error_code = %q, want invalid_request or session_expired", second.ErrorCode)
	}
}

func TestExtensionBatch_DuplicateHandlesInvalidRequestKeepsLease(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-dup"}, commitRaw(resolved.SessionID, []string{itemID, itemID}, false, ""))
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("error_code = %q, want invalid_request", result.ErrorCode)
	}
	if h.engine.callCount() != 0 {
		t.Fatalf("AddUri calls = %d, want 0", h.engine.callCount())
	}
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID); !ok {
		t.Fatal("lease must stay intact after duplicate handles")
	}
}

func TestExtensionBatch_MixedUnknownIDDoesNotConsume(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	unknown := "cccccccccccccccccccccccccccccccc"
	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-mix"}, commitRaw(resolved.SessionID, []string{itemID, unknown}, false, ""))
	if result.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("error_code = %q, want invalid_request", result.ErrorCode)
	}
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID); !ok {
		t.Fatal("good id must remain leased")
	}
}

func TestExtensionBatch_PartialAddUriFailureRestores(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	firstID := resolved.Items[0].ItemID
	secondID := "dddddddddddddddddddddddddddddddd"
	leased, ok := h.lease.lookupLeasedItem(resolved.SessionID, firstID)
	if !ok {
		t.Fatal("expected leased item")
	}
	second := extractor.CloneResolvedAddItem(leased)
	second.URL = "https://download.fixture.invalid/other.bin"
	insertLeasedItem(t, h.lease, resolved.SessionID, secondID, second)
	h.engine.failURLs[second.URL] = struct{}{}

	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-partial"}, commitRaw(resolved.SessionID, []string{firstID, secondID}, false, ""))
	if result.ErrorCode != "" || result.Success {
		t.Fatalf("commit = %+v, want success=false without envelope error", result)
	}
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, firstID); ok {
		t.Fatal("succeeded id must be consumed")
	}
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, secondID); !ok {
		t.Fatal("failed id must be restored")
	}
	if result.ErrorsByItemID[secondID] != extension.CommitItemErrorAddFailed {
		t.Fatalf("errors_by_item_id = %#v, want opaque add failed", result.ErrorsByItemID)
	}
	assertAckHasNoHost(t, result)
}

func TestExtensionBatch_InvalidateSkipsRestoreAndReceipts(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-unpair"}, commitRaw(resolved.SessionID, []string{itemID}, false, ""))
	if result.ErrorCode != "" {
		t.Fatalf("commit = %+v", result)
	}
	h.lease.Invalidate()
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID); ok {
		t.Fatal("lease must be gone after Invalidate")
	}
	replay := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-unpair"}, commitRaw(resolved.SessionID, []string{itemID}, false, ""))
	if replay.ErrorCode != extension.ErrCodeSessionExpired && replay.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("after unpair error_code = %q", replay.ErrorCode)
	}
	if h.engine.callCount() != 1 {
		t.Fatalf("Invalidate must drop receipts so a new consume is attempted; AddUri=%d", h.engine.callCount())
	}
}

func TestExtensionBatch_ExtraDeputyKeysInvalidRequest(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	for _, extra := range []string{
		`"url":"https://download.fixture.invalid/x"`,
		`"final_url":"https://download.fixture.invalid/x"`,
		`"headers":["Cookie: sid=x"]`,
		`"items":[]`,
		`"cookies":[]`,
		`"source_url":"https://share.fixture.invalid/s"`,
		`"auth_profile_ref":"apr-x"`,
		`"header_profile_ref":"hpr-x"`,
		`"gid":"g1"`,
		`"gids":["g1"]`,
	} {
		raw := []byte(`{"type":"batch_download","session_id":"` + resolved.SessionID + `","item_ids":["` + itemID + `"],` + extra + `}`)
		result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-extra"}, json.RawMessage(raw))
		if result.ErrorCode != extension.ErrCodeInvalidRequest {
			t.Fatalf("extra %s error_code = %q", extra, result.ErrorCode)
		}
	}
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID); !ok {
		t.Fatal("lease must stay intact after extra-key reject")
	}
}

func TestExtensionBatch_CreateGroupTwoHandlesOneURLOmitsGroupKey(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	firstID := resolved.Items[0].ItemID
	secondID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	leased, ok := h.lease.lookupLeasedItem(resolved.SessionID, firstID)
	if !ok {
		t.Fatal("expected leased item")
	}
	insertLeasedItem(t, h.lease, resolved.SessionID, secondID, leased)

	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-one-url"}, commitRaw(resolved.SessionID, []string{firstID, secondID}, true, "Album"))
	if result.ErrorCode != "" {
		t.Fatalf("commit = %+v", result)
	}
	if result.GroupKey != "" {
		t.Fatalf("group_key = %q, want empty for one unique URL", result.GroupKey)
	}
}

func TestExtensionBatch_InvalidDownloadDirRestoresAndRetries(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	firstID := resolved.Items[0].ItemID
	secondID := "ffffffffffffffffffffffffffffffff"
	leased, ok := h.lease.lookupLeasedItem(resolved.SessionID, firstID)
	if !ok {
		t.Fatal("expected leased item")
	}
	second := extractor.CloneResolvedAddItem(leased)
	second.URL = "https://download.fixture.invalid/other.bin"
	insertLeasedItem(t, h.lease, resolved.SessionID, secondID, second)

	prev := config.Get()
	broken := *prev
	broken.DownloadDir = ""
	config.SetTestConfig(&broken)

	raw := commitRaw(resolved.SessionID, []string{firstID, secondID}, true, "Album")
	first := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-dir"}, raw)
	if first.ErrorCode != extension.ErrCodeUnavailable {
		t.Fatalf("first commit = %+v, want unavailable", first)
	}
	if !first.SkipIdempotency {
		t.Fatal("post-consume unavailable must SkipIdempotency")
	}
	if h.engine.callCount() != 0 {
		t.Fatalf("AddUri calls = %d, want 0", h.engine.callCount())
	}
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, firstID); !ok {
		t.Fatal("first id must be restored")
	}
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, secondID); !ok {
		t.Fatal("second id must be restored")
	}

	config.SetTestConfig(prev)
	retry := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-dir"}, raw)
	if retry.ErrorCode != "" || !retry.Success {
		t.Fatalf("retry commit = %+v", retry)
	}
	if h.engine.callCount() != 2 {
		t.Fatalf("retry AddUri calls = %d, want 2", h.engine.callCount())
	}
}

func TestExtensionBatch_ReservedFolderNameFallsBack(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	firstID := resolved.Items[0].ItemID
	secondID := "ffffffffffffffffffffffffffffffff"
	leased, ok := h.lease.lookupLeasedItem(resolved.SessionID, firstID)
	if !ok {
		t.Fatal("expected leased item")
	}
	second := extractor.CloneResolvedAddItem(leased)
	second.URL = "https://download.fixture.invalid/other.bin"
	insertLeasedItem(t, h.lease, resolved.SessionID, secondID, second)

	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-con"}, commitRaw(resolved.SessionID, []string{firstID, secondID}, true, "CON.txt"))
	if result.ErrorCode != "" || !result.Success {
		t.Fatalf("commit = %+v", result)
	}
	if result.GroupKey == "" {
		t.Fatal("group_key missing, reserved folder must still create a group")
	}
	base := config.Get().DownloadDir
	if _, err := os.Stat(filepath.Join(base, "CON.txt")); !os.IsNotExist(err) {
		t.Fatalf("CON.txt must not be created, stat err = %v", err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var folder string
	for _, entry := range entries {
		if entry.IsDir() && strings.Contains(entry.Name(), "Collection") {
			folder = entry.Name()
			break
		}
	}
	if folder == "" || strings.EqualFold(folder, "CON") || strings.EqualFold(folder, "CON.txt") {
		t.Fatalf("on-disk FolderName = %q, want timestamp Collection fallback", folder)
	}
}

func TestExtensionBatch_ReceiptReplaysWithoutSecondAddUri(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	raw := commitRaw(resolved.SessionID, []string{itemID}, false, "")
	first := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-receipt"}, raw)
	if first.ErrorCode != "" || !first.Success {
		t.Fatalf("first commit = %+v", first)
	}
	second := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-receipt"}, raw)
	if second.ErrorCode != "" || !second.Success {
		t.Fatalf("receipt replay = %+v", second)
	}
	if h.engine.callCount() != 1 {
		t.Fatalf("AddUri calls = %d, want 1 after receipt replay", h.engine.callCount())
	}
}

func TestExtensionBatch_ReceiptAt61SecondsThenExpiresWithoutSecondAddUri(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	const requestID = "req-receipt-boundaries"
	raw := commitRaw(resolved.SessionID, []string{itemID}, false, "")
	first := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: requestID}, raw)
	if first.ErrorCode != "" || !first.Success {
		t.Fatalf("first commit = %+v", first)
	}

	h.lease.mu.Lock()
	receipt := h.lease.receipts[requestID]
	receipt.stored = time.Now().Add(-time.Minute - time.Second)
	h.lease.receipts[requestID] = receipt
	h.lease.mu.Unlock()
	afterIdempotency := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: requestID}, raw)
	if afterIdempotency.ErrorCode != "" || !afterIdempotency.Success {
		t.Fatalf("receipt after idempotency window = %+v", afterIdempotency)
	}
	if h.engine.callCount() != 1 {
		t.Fatalf("AddUri calls = %d, want 1 after receipt replay", h.engine.callCount())
	}

	h.lease.mu.Lock()
	receipt = h.lease.receipts[requestID]
	receipt.stored = time.Now().Add(-commitReceiptTTL - time.Second)
	h.lease.receipts[requestID] = receipt
	h.lease.mu.Unlock()
	expired := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: requestID}, raw)
	if expired.ErrorCode != extension.ErrCodeSessionExpired && expired.ErrorCode != extension.ErrCodeInvalidRequest {
		t.Fatalf("expired receipt error_code = %q", expired.ErrorCode)
	}
	if h.engine.callCount() != 1 {
		t.Fatalf("AddUri calls = %d, want 1 after receipt expiry", h.engine.callCount())
	}
}

func TestExtensionBatch_NilPolicyNonAliasStillCommits(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	leased, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID)
	if !ok {
		t.Fatal("expected leased item")
	}
	leased.HostPolicy = nil
	leased.PackManifest.DomainPolicyRefs = nil
	leased.PackManifest.Domains = []extractor.DomainRule{{Host: "share.fixture.invalid"}}
	insertLeasedItem(t, h.lease, resolved.SessionID, itemID, leased)

	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-nil-policy"}, commitRaw(resolved.SessionID, []string{itemID}, false, ""))
	if result.ErrorCode != "" || !result.Success {
		t.Fatalf("nil policy non-alias commit = %+v", result)
	}
}

func TestExtensionBatch_RestoreSkippedAfterInvalidate(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	clones, token, errCode := h.lease.consumeLeasedItems(resolved.SessionID, []string{itemID})
	if errCode != "" {
		t.Fatalf("consume error_code = %q", errCode)
	}
	h.lease.Invalidate()
	h.lease.restoreLeasedItems(token, []string{itemID}, clones)
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID); ok {
		t.Fatal("restore must not write into a new epoch")
	}
}

func TestExtensionBatch_StoreReceiptNoOpAfterInvalidate(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	_, token, errCode := h.lease.consumeLeasedItems(resolved.SessionID, []string{itemID})
	if errCode != "" {
		t.Fatalf("consume error_code = %q", errCode)
	}
	h.lease.Invalidate()
	h.lease.storeReceipt("req-race", "digest", extension.CommitResult{Success: true}, token.epoch)
	if _, status := h.lease.lookupReceipt("req-race", "digest"); status != receiptMiss {
		t.Fatalf("lookupReceipt status = %d, want miss after Invalidate", status)
	}
}

func TestExtensionBatch_HTTPOutputRejected(t *testing.T) {
	h := setupBatchCommitHarness(t)
	resolved := resolveFixtureSession(t, h.lease)
	itemID := resolved.Items[0].ItemID
	leased, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID)
	if !ok {
		t.Fatal("expected leased item")
	}
	leased.URL = "http://download.fixture.invalid/files/a.bin"
	insertLeasedItem(t, h.lease, resolved.SessionID, itemID, leased)

	result := h.commit.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-http"}, commitRaw(resolved.SessionID, []string{itemID}, false, ""))
	if result.ErrorCode != "" || result.Success {
		t.Fatalf("http commit = %+v, want per-item fail", result)
	}
	if result.ErrorsByItemID[itemID] != extension.CommitItemErrorNotAllowed {
		t.Fatalf("errors_by_item_id = %#v", result.ErrorsByItemID)
	}
	if h.engine.callCount() != 0 {
		t.Fatalf("AddUri calls = %d, want 0", h.engine.callCount())
	}
	if _, ok := h.lease.lookupLeasedItem(resolved.SessionID, itemID); !ok {
		t.Fatal("http-rejected id must be restored")
	}
	assertAckHasNoHost(t, result)
}
