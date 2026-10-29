//go:build extractor

package wailsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/tasks"
)

// The snapshot-fixed commit service must join the same dedup registry the App
// uses everywhere else, and hot-refresh rebuilds must keep sharing it.

type dedupLinkageFixture struct {
	app     *App
	mgr     *extractor.ExtractorRuntimeManager
	engine  *recordingCommitEngine
	snap    *extractor.RuntimeSnapshot
	srcID   string
	linkage extension.Linkage
}

func newDedupLinkageFixture(t *testing.T) *dedupLinkageFixture {
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

	dataRoot := t.TempDir()
	packDir := filepath.Join(t.TempDir(), "fixture-pack")
	lockOut := filepath.Join(packDir, packbuilder.HostCallFixturePackID+".lock.json")
	if _, err := packbuilder.WriteHostCallFixture(packDir, lockOut); err != nil {
		t.Fatalf("WriteHostCallFixture: %v", err)
	}
	mgr, err := extractor.NewExtractorRuntimeManager(context.Background(), extractor.ExtractorRuntimeManagerConfig{
		DataRoot:           dataRoot,
		HostPolicyResolver: fakeSetupHostPolicyResolver{},
		HTTPTransport:      &recordingCookieTransport{body: `{"ok":true,"item":"fixture-item"}`},
	})
	if err != nil {
		t.Fatalf("NewExtractorRuntimeManager: %v", err)
	}
	srcState, err := mgr.LoadSource(context.Background(), extractor.RuntimeSourceSpec{
		Kind:    extractor.RuntimeSourceKindLocalDirectory,
		Locator: packDir,
	})
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}

	snap := mgr.CurrentSnapshot()
	engine := &recordingCommitEngine{failURLs: map[string]struct{}{}}
	app := NewApp(Options{DownloadEngine: engine})
	linkage := buildExtensionLinkageFromSnapshot(snap, engine, app.deduper)

	return &dedupLinkageFixture{
		app:     app,
		mgr:     mgr,
		engine:  engine,
		snap:    snap,
		srcID:   srcState.SourceID,
		linkage: linkage,
	}
}

func commitServiceFromLinkage(t *testing.T, l extension.Linkage) *tasks.Service {
	t.Helper()
	committer, ok := l.Committer.(*extensionBatchAdapter)
	if !ok {
		t.Fatalf("expected *extensionBatchAdapter, got %T", l.Committer)
	}
	svc, ok := committer.service.(*tasks.Service)
	if !ok {
		t.Fatalf("expected *tasks.Service, got %T", committer.service)
	}
	return svc
}

func TestExtensionLinkageAddDeduper_CommitServiceSharesAppRegistry(t *testing.T) {
	fx := newDedupLinkageFixture(t)

	svc := commitServiceFromLinkage(t, fx.linkage)
	if svc.Deduper != fx.app.deduper {
		t.Fatal("snapshot-fixed commit service must share the App-owned deduper")
	}
	if svc.Adapter != fx.snap.TasksAdapter() {
		t.Fatal("commit service adapter must stay fixed to its snapshot")
	}

	// A rebuilt linkage for a new snapshot keeps the same App registry while
	// its adapter pins the new snapshot.
	if _, err := fx.mgr.ReloadSource(context.Background(), fx.srcID); err != nil {
		t.Fatalf("ReloadSource: %v", err)
	}
	snap2 := fx.mgr.CurrentSnapshot()
	linkage2 := buildExtensionLinkageFromSnapshot(snap2, fx.engine, fx.app.deduper)
	svc2 := commitServiceFromLinkage(t, linkage2)
	if svc2.Deduper != fx.app.deduper {
		t.Fatal("refreshed linkage must keep sharing the App-owned deduper")
	}
	if svc2.Adapter != snap2.TasksAdapter() {
		t.Fatal("refreshed commit service must pin the new snapshot adapter")
	}
}

func TestExtensionLinkageAddDeduper_CommitCoalescesWithAppSeam(t *testing.T) {
	fx := newDedupLinkageFixture(t)
	resAdapter, ok := fx.linkage.Resolver.(*extensionResolveAdapter)
	if !ok {
		t.Fatalf("expected *extensionResolveAdapter, got %T", fx.linkage.Resolver)
	}
	committer, ok := fx.linkage.Committer.(*extensionBatchAdapter)
	if !ok {
		t.Fatalf("expected *extensionBatchAdapter, got %T", fx.linkage.Committer)
	}
	resolved := resolveFixtureSession(t, resAdapter)
	itemID := resolved.Items[0].ItemID
	itemURL := packbuilder.HostCallFixtureItemURL

	// Direction 1: the App seam owns the add; the snapshot-fixed commit is the
	// loser and must surface a per-item duplicate without an engine call.
	if gid, err := fx.app.taskService().AddUriFromExtension(extension.DownloadRequest{URL: itemURL}); err != nil || gid == "" {
		t.Fatalf("seed extension add: gid=%q err=%v", gid, err)
	}
	if got := fx.engine.callCount(); got != 1 {
		t.Fatalf("engine adds = %d, want 1", got)
	}
	result := committer.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-dedup-1"}, commitRaw(resolved.SessionID, []string{itemID}, false, ""))
	if result.ErrorCode != "" {
		t.Fatalf("commit error_code = %q", result.ErrorCode)
	}
	if !slices.Contains(result.DuplicateItemIDs, itemID) {
		t.Fatalf("duplicate_item_ids = %#v, want %s", result.DuplicateItemIDs, itemID)
	}
	if slices.Contains(result.SucceededItemIDs, itemID) {
		t.Fatalf("loser must not report success: %#v", result.SucceededItemIDs)
	}
	if got := fx.engine.callCount(); got != 1 {
		t.Fatalf("engine adds = %d, want 1 (coalesced onto the App claim)", got)
	}

	// Direction 2: the commit owns the add; a later App-seam call hits the
	// done-record and reports duplicate.
	leased, ok := resAdapter.lookupLeasedItem(resolved.SessionID, itemID)
	if ok {
		second := extractor.CloneResolvedAddItem(leased)
		second.URL = "https://download.fixture.invalid/second.bin"
		secondID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		insertLeasedItem(t, resAdapter, resolved.SessionID, secondID, second)

		// itemID was consumed by the first commit; re-lease it so the second
		// commit carries only the new item.
		secondRes := committer.HandleCommit(context.Background(), extension.RequestEnvelope{RequestID: "req-dedup-2"}, commitRaw(resolved.SessionID, []string{secondID}, false, ""))
		if secondRes.ErrorCode != "" || !slices.Contains(secondRes.SucceededItemIDs, secondID) {
			t.Fatalf("second commit = %+v, want success", secondRes)
		}
		if got := fx.engine.callCount(); got != 2 {
			t.Fatalf("engine adds = %d, want 2", got)
		}
		if got := fx.app.taskService().AddUri(second.URL); got != "duplicate" {
			t.Fatalf("late AddUri = %q, want duplicate via shared done-record", got)
		}
		if got := fx.engine.callCount(); got != 2 {
			t.Fatalf("engine adds = %d, want 2 (done-record blocks re-add)", got)
		}
	}
}

func TestExtensionLinkageAddDeduper_RefreshKeepsSharedRegistryThroughWire(t *testing.T) {
	fx := newDedupLinkageFixture(t)
	fx.app.setExtractorRuntime(&taggedExtractorRuntime{manager: fx.mgr})

	store := extension.NewSecretStore()
	store.SetSecret("dedup-secret")
	srv := extension.NewServer(nil, fx.app.taskService(), store)
	fx.app.SetExtensionServer(srv)
	ConfigureExtensionLinkage(fx.app, srv)
	if err := srv.Start(0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	// Seed a done-record into the App registry through the extension seam.
	itemURL := packbuilder.HostCallFixtureItemURL
	gid, err := fx.app.taskService().AddUriFromExtension(extension.DownloadRequest{URL: itemURL})
	if err != nil || gid == "" {
		t.Fatalf("seed extension add: gid=%q err=%v", gid, err)
	}
	if got := fx.engine.callCount(); got != 1 {
		t.Fatalf("engine adds = %d, want 1", got)
	}

	// Hot-refresh the linkage, then exercise it through a fresh connection.
	if _, err := fx.mgr.ReloadSource(context.Background(), fx.srcID); err != nil {
		t.Fatalf("ReloadSource: %v", err)
	}
	fx.app.refreshExtensionLinkage()

	port := srv.GetStatus().WSPort
	conn := dialDedupConn(t, port, "dedup-secret")
	defer conn.Close()

	// The refreshed linkage resolves sessions on the new snapshot.
	writeDedupMsg(t, conn, map[string]any{
		"type":       extension.MsgTypeExtractorResolve,
		"request_id": "r-resolve",
		"source_url": packbuilder.HostCallFixtureShareURL,
		"cookies": []map[string]any{{
			"name":      "sid",
			"value":     "browser-sid",
			"domain":    ".fixture.invalid",
			"path":      "/",
			"secure":    true,
			"host_only": false,
		}},
	})
	var resAck extension.ExtractorResolveAck
	readDedupMsg(t, conn, &resAck)
	if resAck.SessionID == "" || len(resAck.Items) == 0 {
		t.Fatalf("resolve ack = %+v, want session with items", resAck)
	}
	itemID := resAck.Items[0].ItemID

	// The refreshed commit service coalesces onto the App-owned done-record:
	// the item lands in duplicate_item_ids with no second engine add.
	writeDedupMsg(t, conn, map[string]any{
		"type":       extension.MsgTypeBatchDownload,
		"request_id": "r-commit",
		"session_id": resAck.SessionID,
		"item_ids":   []string{itemID},
	})
	var batchAck extension.BatchDownloadAck
	readDedupMsg(t, conn, &batchAck)
	if batchAck.ErrorCode != "" {
		t.Fatalf("batch ack error_code = %q", batchAck.ErrorCode)
	}
	if !slices.Contains(batchAck.DuplicateItemIDs, itemID) {
		t.Fatalf("duplicate_item_ids = %#v, want %s (refreshed service must share the registry)", batchAck.DuplicateItemIDs, itemID)
	}
	if got := fx.engine.callCount(); got != 1 {
		t.Fatalf("engine adds = %d, want 1 (refreshed commit coalesced)", got)
	}

	// The single-item seam reaches the same registry and surfaces the known
	// GID on the duplicate ack.
	writeDedupMsg(t, conn, map[string]any{
		"type":       extension.MsgTypeDownload,
		"request_id": "r-dl",
		"url":        itemURL,
	})
	var dlAck extension.DownloadResponse
	readDedupMsg(t, conn, &dlAck)
	if dlAck.Success || dlAck.Error != "duplicate" || dlAck.GID != gid {
		t.Fatalf("download ack = %+v, want duplicate with gid %q", dlAck, gid)
	}
}

func dialDedupConn(t *testing.T, port int, secret string) *websocket.Conn {
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
	if err := conn.WriteJSON(extension.AuthMessage{Type: extension.MsgTypeAuth, Secret: secret}); err != nil {
		t.Fatalf("auth write: %v", err)
	}
	var ack extension.AuthAck
	readDedupMsg(t, conn, &ack)
	if !hasCap(ack.Capabilities, extension.CapExtractorResolve) || !hasCap(ack.Capabilities, extension.CapExtractorBatch) {
		t.Fatalf("caps = %v, want resolve+batch advertised after refresh", ack.Capabilities)
	}
	return conn
}

func writeDedupMsg(t *testing.T, conn *websocket.Conn, msg map[string]any) {
	t.Helper()
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readDedupMsg(t *testing.T, conn *websocket.Conn, out any) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
}
