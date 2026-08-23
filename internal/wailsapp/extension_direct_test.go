package wailsapp

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

type recordingDirectEngine struct {
	mu       sync.Mutex
	urls     []string
	addCalls int
}

func (e *recordingDirectEngine) AddUri(url string, _ rpc.AddURIOptions) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addCalls++
	e.urls = append(e.urls, url)
	return "gid-ok", nil
}

func (e *recordingDirectEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.addCalls
}

func (e *recordingDirectEngine) Pause(string) error         { return nil }
func (e *recordingDirectEngine) Resume(string) error        { return nil }
func (e *recordingDirectEngine) PauseMulti([]string) error  { return nil }
func (e *recordingDirectEngine) ResumeMulti([]string) error { return nil }
func (e *recordingDirectEngine) Remove(string, bool) error  { return nil }
func (e *recordingDirectEngine) TellStatus(string, []string) (rpc.Task, error) {
	return rpc.Task{}, nil
}

func (e *recordingDirectEngine) TellStatusMulti([]string, []string) ([]rpc.Task, error) {
	return nil, nil
}
func (e *recordingDirectEngine) TellActive() ([]rpc.Task, error)                 { return nil, nil }
func (e *recordingDirectEngine) TellActiveLite() ([]rpc.Task, error)             { return nil, nil }
func (e *recordingDirectEngine) TellActiveProgress() ([]rpc.TaskProgress, error) { return nil, nil }
func (e *recordingDirectEngine) TellWaiting(int, int) ([]rpc.Task, error)        { return nil, nil }
func (e *recordingDirectEngine) TellWaitingLite(int, int) ([]rpc.Task, error)    { return nil, nil }
func (e *recordingDirectEngine) TellStopped(int, int) ([]rpc.Task, error)        { return nil, nil }
func (e *recordingDirectEngine) TellStoppedLite(int, int) ([]rpc.Task, error)    { return nil, nil }
func (e *recordingDirectEngine) GetGlobalStat() (rpc.GlobalStat, error) {
	return rpc.GlobalStat{}, nil
}
func (e *recordingDirectEngine) SaveSession() error                         { return nil }
func (e *recordingDirectEngine) ChangeGlobalOption(map[string]string) error { return nil }
func (e *recordingDirectEngine) StreamEvents(context.Context) (<-chan any, func(), error) {
	ch := make(chan any)
	close(ch)
	return ch, func() {}, nil
}
func (e *recordingDirectEngine) IsSurgeActive() bool { return false }

func setupDirectAdapter(t *testing.T) (*directBatchAdapter, *recordingDirectEngine) {
	t.Helper()
	originalConfig := config.Get()
	originalSave := history.SaveEnabled
	history.DisableSaveForTest()
	history.Clear()
	monitor.ResetDownloadGroupNamerForTest()
	monitor.ResetTaskGroupStoreForTest(t.TempDir()+"/download_groups.json", true)
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
	engine := &recordingDirectEngine{}
	app := NewApp(Options{DownloadEngine: engine})
	return newDirectBatchAdapter(app), engine
}

func TestDirectBatchAdapter_PendingThenComplete(t *testing.T) {
	adapter, engine := setupDirectAdapter(t)
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if !adapter.AdmitPending(id, "digest-a") {
		t.Fatal("admit pending")
	}
	snap, ok := adapter.LookupStatus(id)
	if !ok || snap.Status != extension.DirectBatchStatusPending {
		t.Fatalf("pending snapshot = %+v ok=%v", snap, ok)
	}
	if engine.callCount() != 0 {
		t.Fatal("pending lookup must not AddUri")
	}

	req := extension.DirectBatchRequest{
		RequestID:     id,
		PayloadDigest: "digest-a",
		Items: []extension.DirectBatchItem{
			{ClientItemID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CanonicalURL: "https://download.fixture.invalid/a.bin"},
		},
	}
	result := adapter.HandleDirectBatch(context.Background(), extension.RequestEnvelope{RequestID: id}, req)
	if result.ErrorCode != "" || !result.Success {
		t.Fatalf("commit result = %+v", result)
	}
	if engine.callCount() != 1 {
		t.Fatalf("AddUri calls = %d", engine.callCount())
	}
	snap, ok = adapter.LookupStatus(id)
	if !ok || snap.Status != extension.DirectBatchStatusComplete {
		t.Fatalf("complete snapshot = %+v ok=%v", snap, ok)
	}

	replay := adapter.HandleDirectBatch(context.Background(), extension.RequestEnvelope{RequestID: id}, req)
	if replay.ErrorCode != "" || !replay.Success {
		t.Fatalf("replay result = %+v", replay)
	}
	if engine.callCount() != 1 {
		t.Fatalf("same digest must not AddUri again, calls=%d", engine.callCount())
	}

	conflict := adapter.HandleDirectBatch(context.Background(), extension.RequestEnvelope{RequestID: id}, extension.DirectBatchRequest{
		RequestID:     id,
		PayloadDigest: "digest-b",
		Items:         req.Items,
	})
	if conflict.ErrorCode != extension.ErrCodeIdempotencyConflict {
		t.Fatalf("different digest = %+v", conflict)
	}
	if engine.callCount() != 1 {
		t.Fatalf("conflict must not AddUri, calls=%d", engine.callCount())
	}

	emptyDigest := adapter.HandleDirectBatch(context.Background(), extension.RequestEnvelope{RequestID: id}, extension.DirectBatchRequest{
		RequestID: id,
		Items:     req.Items,
	})
	if emptyDigest.ErrorCode != extension.ErrCodeIdempotencyConflict {
		t.Fatalf("empty digest = %+v", emptyDigest)
	}
	if engine.callCount() != 1 {
		t.Fatalf("empty digest must not AddUri, calls=%d", engine.callCount())
	}
}

func TestDirectBatchAdapter_AbandonPendingLeavesComplete(t *testing.T) {
	adapter, _ := setupDirectAdapter(t)
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if !adapter.AdmitPending(id, "digest-a") {
		t.Fatal("admit")
	}
	adapter.storeComplete(id, extension.DirectCommitResult{Success: true, SucceededItemIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, adapter.epoch, adapter.currentGenerationLocked(), "digest-a")
	adapter.AbandonPending(id)
	snap, ok := adapter.LookupStatus(id)
	if !ok || snap.Status != extension.DirectBatchStatusComplete {
		t.Fatalf("complete must survive AbandonPending, snap=%+v ok=%v", snap, ok)
	}
}

func TestDirectBatchAdapter_AdmitPendingOnCompleteDoesNotOverwrite(t *testing.T) {
	adapter, _ := setupDirectAdapter(t)
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if !adapter.AdmitPending(id, "digest-a") {
		t.Fatal("admit")
	}
	adapter.storeComplete(id, extension.DirectCommitResult{Success: true, SucceededItemIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, adapter.epoch, adapter.currentGenerationLocked(), "digest-a")
	if !adapter.AdmitPending(id, "digest-b") {
		t.Fatal("complete admit must return true so HandleDirectBatch can replay or conflict")
	}
	snap, ok := adapter.LookupStatus(id)
	if !ok || snap.Status != extension.DirectBatchStatusComplete {
		t.Fatalf("AdmitPending must not flip complete to pending, snap=%+v ok=%v", snap, ok)
	}
}

func TestDirectBatchAdapter_InvalidateClearsReceipts(t *testing.T) {
	adapter, _ := setupDirectAdapter(t)
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if !adapter.AdmitPending(id, "digest-a") {
		t.Fatal("admit")
	}
	adapter.storeComplete(id, extension.DirectCommitResult{Success: true, SucceededItemIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, adapter.epoch, adapter.currentGenerationLocked(), "digest-a")
	adapter.Invalidate()
	if _, ok := adapter.LookupStatus(id); ok {
		t.Fatal("invalidate must yield not_found")
	}
}

func TestDirectBatchAdapter_StoreCompleteIgnoresStaleEpoch(t *testing.T) {
	adapter, _ := setupDirectAdapter(t)
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if !adapter.AdmitPending(id, "digest-a") {
		t.Fatal("admit")
	}
	adapter.mu.Lock()
	epoch := adapter.receipts[id].epoch
	generation := adapter.receipts[id].generation
	adapter.mu.Unlock()
	adapter.Invalidate()
	adapter.storeComplete(id, extension.DirectCommitResult{Success: true, SucceededItemIDs: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, epoch, generation, "digest-a")
	if _, ok := adapter.LookupStatus(id); ok {
		t.Fatal("stale storeComplete must not re-enter receipts")
	}
}

func TestDirectBatchAdapter_ExpiredCompleteNotFound(t *testing.T) {
	adapter, _ := setupDirectAdapter(t)
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	if !adapter.AdmitPending(id, "digest-a") {
		t.Fatal("admit")
	}
	adapter.mu.Lock()
	epoch := adapter.receipts[id].epoch
	generation := adapter.receipts[id].generation
	adapter.mu.Unlock()
	adapter.storeComplete(id, extension.DirectCommitResult{Success: true}, epoch, generation, "digest-a")
	adapter.mu.Lock()
	rec := adapter.receipts[id]
	rec.stored = time.Now().Add(-directReceiptTTL - time.Second)
	adapter.receipts[id] = rec
	adapter.mu.Unlock()
	if _, ok := adapter.LookupStatus(id); ok {
		t.Fatal("expired complete must be not_found")
	}
}

func TestDirectBatchAdapter_CapBusyNeverEvictsPending(t *testing.T) {
	adapter, _ := setupDirectAdapter(t)
	for i := range maxDirectReceipts {
		if !adapter.AdmitPending(fmt.Sprintf("pending-%03d", i), "d") {
			t.Fatalf("admit %d failed early", i)
		}
	}
	if adapter.AdmitPending("overflow", "d") {
		t.Fatal("overflow pending must be busy")
	}
}
