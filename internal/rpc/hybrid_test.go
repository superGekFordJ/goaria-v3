package rpc

import (
	"context"
	"errors"
	"testing"
)

type mockEngine struct {
	addedUrls         []string
	addedOptions      []AddURIOptions
	addResultGid      string
	addResultErr      error
	pausedGids        []string
	resumedGids       []string
	removedGids       []string
	removedDeleteFile []bool
	statusGid         string
	statusResult      Task
	statusErr         error
	activeResult      []Task
	activeErr         error
	activeProgressRes []TaskProgress
	activeProgressErr error
	waitingResult     []Task
	waitingErr        error
	stoppedResult     []Task
	stoppedErr        error
	globalStatRes     GlobalStat
	globalStatErr     error
	saveSessionCalls  int
	changeGlobalCalls []map[string]string
	closeCalls        int
}

func (m *mockEngine) AddUri(url string, options AddURIOptions) (string, error) {
	m.addedUrls = append(m.addedUrls, url)
	m.addedOptions = append(m.addedOptions, options)
	return m.addResultGid, m.addResultErr
}

func (m *mockEngine) Pause(gid string) error {
	m.pausedGids = append(m.pausedGids, gid)
	return nil
}

func (m *mockEngine) Resume(gid string) error {
	m.resumedGids = append(m.resumedGids, gid)
	return nil
}

func (m *mockEngine) PauseMulti(gids []string) error {
	m.pausedGids = append(m.pausedGids, gids...)
	return nil
}

func (m *mockEngine) ResumeMulti(gids []string) error {
	m.resumedGids = append(m.resumedGids, gids...)
	return nil
}

func (m *mockEngine) Remove(gid string, deleteFile bool) error {
	m.removedGids = append(m.removedGids, gid)
	m.removedDeleteFile = append(m.removedDeleteFile, deleteFile)
	return nil
}

func (m *mockEngine) TellStatus(gid string, keys []string) (Task, error) {
	m.statusGid = gid
	return m.statusResult, m.statusErr
}

func (m *mockEngine) TellStatusMulti(gids []string, keys []string) ([]Task, error) {
	var res []Task
	for _, gid := range gids {
		t := m.statusResult
		t.GID = gid
		res = append(res, t)
	}
	return res, m.statusErr
}

func (m *mockEngine) TellActive() ([]Task, error) {
	return m.activeResult, m.activeErr
}

func (m *mockEngine) TellActiveLite() ([]Task, error) {
	return m.activeResult, m.activeErr
}

func (m *mockEngine) TellActiveProgress() ([]TaskProgress, error) {
	return m.activeProgressRes, m.activeProgressErr
}

func (m *mockEngine) TellWaiting(offset, num int) ([]Task, error) {
	return m.waitingResult, m.waitingErr
}

func (m *mockEngine) TellWaitingLite(offset, num int) ([]Task, error) {
	return m.waitingResult, m.waitingErr
}

func (m *mockEngine) TellStopped(offset, num int) ([]Task, error) {
	return m.stoppedResult, m.stoppedErr
}

func (m *mockEngine) TellStoppedLite(offset, num int) ([]Task, error) {
	return m.stoppedResult, m.stoppedErr
}

func (m *mockEngine) GetGlobalStat() (GlobalStat, error) {
	return m.globalStatRes, m.globalStatErr
}

func (m *mockEngine) SaveSession() error {
	m.saveSessionCalls++
	return nil
}

func (m *mockEngine) ChangeGlobalOption(options map[string]string) error {
	m.changeGlobalCalls = append(m.changeGlobalCalls, options)
	return nil
}

func (m *mockEngine) StreamEvents(ctx context.Context) (<-chan any, func(), error) {
	return nil, func() {}, nil
}

func (m *mockEngine) Close() {
	m.closeCalls++
}

func TestHybridEngine_StaticRouting(t *testing.T) {
	aria2 := &mockEngine{addResultGid: "ar_task_1"}
	surge := &mockEngine{}
	hybrid := NewHybridEngine(aria2, surge)

	// Magnet links should statically route to Aria2
	gid, err := hybrid.AddUri("magnet:?xt=urn:btih:12345", AddURIOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != "ar_ar_task_1" {
		t.Errorf("expected GID 'ar_ar_task_1', got '%s'", gid)
	}
	if len(aria2.addedUrls) != 1 {
		t.Errorf("expected 1 url added to Aria2, got %d", len(aria2.addedUrls))
	}
	if len(surge.addedUrls) != 0 {
		t.Errorf("expected 0 urls added to Surge, got %d", len(surge.addedUrls))
	}
}

func TestHybridEngine_NormalSurgePath(t *testing.T) {
	aria2 := &mockEngine{}
	surge := &mockEngine{addResultGid: "sg_task_1"}
	hybrid := NewHybridEngine(aria2, surge)

	// HTTP link should route to Surge first
	gid, err := hybrid.AddUri("https://example.com/file.zip", AddURIOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != "sg_sg_task_1" {
		t.Errorf("expected GID 'sg_sg_task_1', got '%s'", gid)
	}
	if len(surge.addedUrls) != 1 {
		t.Errorf("expected 1 url added to Surge, got %d", len(surge.addedUrls))
	}
	if len(aria2.addedUrls) != 0 {
		t.Errorf("expected 0 urls added to Aria2, got %d", len(aria2.addedUrls))
	}
}

func TestHybridEngine_DynamicFallback(t *testing.T) {
	aria2 := &mockEngine{addResultGid: "ar_fallback_task"}
	surge := &mockEngine{addResultErr: errors.New("unsupported range or network error")}
	hybrid := NewHybridEngine(aria2, surge)

	// HTTP link should fall back to Aria2 when Surge fails
	gid, err := hybrid.AddUri("http://example.com/large.zip", AddURIOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != "ar_ar_fallback_task" {
		t.Errorf("expected GID 'ar_ar_fallback_task', got '%s'", gid)
	}
	if len(surge.addedUrls) != 1 {
		t.Errorf("expected 1 url attempted on Surge, got %d", len(surge.addedUrls))
	}
	if len(aria2.addedUrls) != 1 {
		t.Errorf("expected fallback to Aria2, got %d", len(aria2.addedUrls))
	}
}

func TestHybridEngine_PrefixStripping(t *testing.T) {
	aria2 := &mockEngine{}
	surge := &mockEngine{}
	hybrid := NewHybridEngine(aria2, surge)

	// Test Pause
	_ = hybrid.Pause("sg_uuid123")
	if len(surge.pausedGids) != 1 || surge.pausedGids[0] != "uuid123" {
		t.Errorf("expected Surge paused with uuid123, got %v", surge.pausedGids)
	}

	_ = hybrid.Pause("ar_hex456")
	if len(aria2.pausedGids) != 1 || aria2.pausedGids[0] != "hex456" {
		t.Errorf("expected Aria2 paused with hex456, got %v", aria2.pausedGids)
	}

	// Test Resume
	_ = hybrid.Resume("sg_uuid123")
	if len(surge.resumedGids) != 1 || surge.resumedGids[0] != "uuid123" {
		t.Errorf("expected Surge resumed with uuid123, got %v", surge.resumedGids)
	}

	_ = hybrid.Resume("ar_hex456")
	if len(aria2.resumedGids) != 1 || aria2.resumedGids[0] != "hex456" {
		t.Errorf("expected Aria2 resumed with hex456, got %v", aria2.resumedGids)
	}

	// Test Remove
	_ = hybrid.Remove("sg_uuid123", true)
	if len(surge.removedGids) != 1 || surge.removedGids[0] != "uuid123" || !surge.removedDeleteFile[0] {
		t.Errorf("expected Surge remove uuid123 with deleteFile=true")
	}

	_ = hybrid.Remove("ar_hex456", false)
	if len(aria2.removedGids) != 1 || aria2.removedGids[0] != "hex456" || aria2.removedDeleteFile[0] {
		t.Errorf("expected Aria2 remove hex456 with deleteFile=false")
	}
}

func TestHybridEngine_StatusMapping(t *testing.T) {
	aria2 := &mockEngine{
		statusResult:      Task{GID: "hex456", Status: "active"},
		activeResult:      []Task{{GID: "hex456", Status: "active"}},
		waitingResult:     []Task{{GID: "hex789", Status: "waiting"}},
		stoppedResult:     []Task{{GID: "hex012", Status: "complete"}},
		activeProgressRes: []TaskProgress{{GID: "hex456", CompletedLength: "100"}},
	}
	surge := &mockEngine{
		statusResult:      Task{GID: "uuid123", Status: "active"},
		activeResult:      []Task{{GID: "uuid123", Status: "active"}},
		waitingResult:     []Task{{GID: "uuid789", Status: "waiting"}},
		stoppedResult:     []Task{{GID: "uuid012", Status: "complete"}},
		activeProgressRes: []TaskProgress{{GID: "uuid123", CompletedLength: "200"}},
	}
	hybrid := NewHybridEngine(aria2, surge)

	// TellStatus
	task, _ := hybrid.TellStatus("sg_uuid123", nil)
	if task.GID != "sg_uuid123" {
		t.Errorf("expected GID sg_uuid123, got %s", task.GID)
	}

	task, _ = hybrid.TellStatus("ar_hex456", nil)
	if task.GID != "ar_hex456" {
		t.Errorf("expected GID ar_hex456, got %s", task.GID)
	}

	// TellActive
	actives, _ := hybrid.TellActive()
	if len(actives) != 2 {
		t.Fatalf("expected 2 active tasks, got %d", len(actives))
	}
	if actives[0].GID != "sg_uuid123" || actives[1].GID != "ar_hex456" {
		t.Errorf("unexpected GIDs in active tasks: %s, %s", actives[0].GID, actives[1].GID)
	}

	// TellActiveProgress
	progresses, _ := hybrid.TellActiveProgress()
	if len(progresses) != 2 {
		t.Fatalf("expected 2 active progresses, got %d", len(progresses))
	}
	if progresses[0].GID != "sg_uuid123" || progresses[1].GID != "ar_hex456" {
		t.Errorf("unexpected GIDs in active progresses: %s, %s", progresses[0].GID, progresses[1].GID)
	}

	// TellWaiting
	waitings, _ := hybrid.TellWaiting(0, 10)
	if len(waitings) != 2 {
		t.Fatalf("expected 2 waiting tasks, got %d", len(waitings))
	}
	if waitings[0].GID != "sg_uuid789" || waitings[1].GID != "ar_hex789" {
		t.Errorf("unexpected GIDs in waiting tasks: %s, %s", waitings[0].GID, waitings[1].GID)
	}

	// TellStopped
	stoppeds, _ := hybrid.TellStopped(0, 10)
	if len(stoppeds) != 2 {
		t.Fatalf("expected 2 stopped tasks, got %d", len(stoppeds))
	}
	if stoppeds[0].GID != "sg_uuid012" || stoppeds[1].GID != "ar_hex012" {
		t.Errorf("unexpected GIDs in stopped tasks: %s, %s", stoppeds[0].GID, stoppeds[1].GID)
	}
}

func TestHybridEngine_BeforeSaveHook(t *testing.T) {
	aria2 := &mockEngine{addResultGid: "aria2_raw"}
	surge := &mockEngine{addResultGid: "surge_raw"}
	hybrid := NewHybridEngine(aria2, surge)

	var hookGid string
	opts := AddURIOptions{
		BeforeSave: func(gid string) error {
			hookGid = gid
			return nil
		},
	}

	// Case 1: Surge success
	gid, err := hybrid.AddUri("http://example.com/file", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != "sg_surge_raw" {
		t.Errorf("expected GID sg_surge_raw, got %s", gid)
	}
	if hookGid != "sg_surge_raw" {
		t.Errorf("expected hook to be called with sg_surge_raw, got %s", hookGid)
	}

	// Case 2: Static routing
	hookGid = ""
	gid, err = hybrid.AddUri("magnet:?xt=urn:btih:123", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gid != "ar_aria2_raw" {
		t.Errorf("expected GID ar_aria2_raw, got %s", gid)
	}
	if hookGid != "ar_aria2_raw" {
		t.Errorf("expected hook to be called with ar_aria2_raw, got %s", hookGid)
	}
}
