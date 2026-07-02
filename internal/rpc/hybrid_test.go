package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if m.activeErr != nil {
		return nil, m.activeErr
	}
	return m.activeResult, nil
}

func (m *mockEngine) TellActiveLite() ([]Task, error) {
	if m.activeErr != nil {
		return nil, m.activeErr
	}
	return m.activeResult, nil
}

func (m *mockEngine) TellActiveProgress() ([]TaskProgress, error) {
	if m.activeProgressErr != nil {
		return nil, m.activeProgressErr
	}
	return m.activeProgressRes, nil
}

func (m *mockEngine) TellWaiting(offset, num int) ([]Task, error) {
	if m.waitingErr != nil {
		return nil, m.waitingErr
	}
	return m.waitingResult, nil
}

func (m *mockEngine) TellWaitingLite(offset, num int) ([]Task, error) {
	if m.waitingErr != nil {
		return nil, m.waitingErr
	}
	return m.waitingResult, nil
}

func (m *mockEngine) TellStopped(offset, num int) ([]Task, error) {
	if m.stoppedErr != nil {
		return nil, m.stoppedErr
	}
	return m.stoppedResult, nil
}

func (m *mockEngine) TellStoppedLite(offset, num int) ([]Task, error) {
	if m.stoppedErr != nil {
		return nil, m.stoppedErr
	}
	return m.stoppedResult, nil
}

func (m *mockEngine) GetGlobalStat() (GlobalStat, error) {
	if m.globalStatErr != nil {
		return GlobalStat{}, m.globalStatErr
	}
	return m.globalStatRes, nil
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

func (m *mockEngine) IsSurgeActive() bool {
	return false
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

func TestHybridEngine_PauseResumeMultiResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"jsonrpc": "2.0",
			"id":      "goaria",
			"result":  []any{[]any{"OK"}},
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	Init(parts[len(parts)-1], "secret")

	aria2 := &mockEngine{}
	surge := &mockEngine{}
	hybrid := NewHybridEngine(aria2, surge)

	gids := []string{"sg_uuid123", "ar_hex456"}

	// Test PauseMultiResults
	results, err := hybrid.PauseMultiResults(gids)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if len(surge.pausedGids) != 1 || surge.pausedGids[0] != "uuid123" {
		t.Errorf("expected Surge paused with uuid123, got %v", surge.pausedGids)
	}

	foundSg := false
	foundAr := false
	for _, res := range results {
		if res.GID == "sg_uuid123" {
			foundSg = true
			if !res.OK {
				t.Errorf("expected sg_uuid123 to be OK")
			}
		}
		if res.GID == "ar_hex456" {
			foundAr = true
			if !res.OK {
				t.Errorf("expected ar_hex456 to be OK")
			}
		}
	}

	if !foundSg || !foundAr {
		t.Errorf("missing expected prefixed GID in results: %v", results)
	}

	// Test ResumeMultiResults
	_, err = hybrid.ResumeMultiResults(gids)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(surge.resumedGids) != 1 || surge.resumedGids[0] != "uuid123" {
		t.Errorf("expected Surge resumed with uuid123, got %v", surge.resumedGids)
	}
}

func TestHybridEngine_PartialFailure_Lists(t *testing.T) {
	surgeTasks := []Task{{GID: "sg1", Status: "paused"}}
	aria2Tasks := []Task{{GID: "ar1", Status: "active"}}
	surgeProgress := []TaskProgress{{GID: "sg1", CompletedLength: "100"}}
	aria2Progress := []TaskProgress{{GID: "ar1", CompletedLength: "200"}}

	tests := []struct {
		name      string
		surgeErr  error
		aria2Err  error
		wantErr   bool
		wantCount int
		wantGID   string
		// Lite variants: Tell*Lite only calls aria2, so expectations differ.
		wantErrLite   bool
		wantCountLite int
		wantGIDLite   string
	}{
		{"Aria2 fails, Surge succeeds", nil, errors.New("aria2 not ready"), false, 1, "sg_sg1", true, 0, ""},
		{"Surge fails, Aria2 succeeds", errors.New("surge error"), nil, false, 1, "ar_ar1", false, 1, "ar_ar1"},
		{"Both fail", errors.New("surge error"), errors.New("aria2 error"), true, 0, "", true, 0, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			surge := &mockEngine{
				activeResult:      surgeTasks,
				waitingResult:     surgeTasks,
				stoppedResult:     surgeTasks,
				activeProgressRes: surgeProgress,
				activeErr:         tc.surgeErr,
				waitingErr:        tc.surgeErr,
				stoppedErr:        tc.surgeErr,
				activeProgressErr: tc.surgeErr,
			}
			aria2 := &mockEngine{
				activeResult:      aria2Tasks,
				waitingResult:     aria2Tasks,
				stoppedResult:     aria2Tasks,
				activeProgressRes: aria2Progress,
				activeErr:         tc.aria2Err,
				waitingErr:        tc.aria2Err,
				stoppedErr:        tc.aria2Err,
				activeProgressErr: tc.aria2Err,
			}
			hybrid := NewHybridEngine(aria2, surge)

			// TellActive
			t.Run("TellActive", func(t *testing.T) {
				result, err := hybrid.TellActive()
				if tc.wantErr {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) != tc.wantCount || (len(result) > 0 && result[0].GID != tc.wantGID) {
					t.Errorf("expected %d tasks with GID %s, got %d tasks: %v", tc.wantCount, tc.wantGID, len(result), result)
				}
			})

			// TellActiveLite
			t.Run("TellActiveLite", func(t *testing.T) {
				result, err := hybrid.TellActiveLite()
				if tc.wantErrLite {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) != tc.wantCountLite || (len(result) > 0 && result[0].GID != tc.wantGIDLite) {
					t.Errorf("expected %d tasks with GID %s, got %d tasks: %v", tc.wantCountLite, tc.wantGIDLite, len(result), result)
				}
			})

			// TellWaiting
			t.Run("TellWaiting", func(t *testing.T) {
				result, err := hybrid.TellWaiting(0, 100)
				if tc.wantErr {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) != tc.wantCount || (len(result) > 0 && result[0].GID != tc.wantGID) {
					t.Errorf("expected %d tasks with GID %s, got %d tasks: %v", tc.wantCount, tc.wantGID, len(result), result)
				}
			})

			// TellWaitingLite
			t.Run("TellWaitingLite", func(t *testing.T) {
				result, err := hybrid.TellWaitingLite(0, 100)
				if tc.wantErrLite {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) != tc.wantCountLite || (len(result) > 0 && result[0].GID != tc.wantGIDLite) {
					t.Errorf("expected %d tasks with GID %s, got %d tasks: %v", tc.wantCountLite, tc.wantGIDLite, len(result), result)
				}
			})

			// TellStopped
			t.Run("TellStopped", func(t *testing.T) {
				result, err := hybrid.TellStopped(0, 100)
				if tc.wantErr {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) != tc.wantCount || (len(result) > 0 && result[0].GID != tc.wantGID) {
					t.Errorf("expected %d tasks with GID %s, got %d tasks: %v", tc.wantCount, tc.wantGID, len(result), result)
				}
			})

			// TellStoppedLite
			t.Run("TellStoppedLite", func(t *testing.T) {
				result, err := hybrid.TellStoppedLite(0, 100)
				if tc.wantErrLite {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) != tc.wantCountLite || (len(result) > 0 && result[0].GID != tc.wantGIDLite) {
					t.Errorf("expected %d tasks with GID %s, got %d tasks: %v", tc.wantCountLite, tc.wantGIDLite, len(result), result)
				}
			})

			// TellActiveProgress
			t.Run("TellActiveProgress", func(t *testing.T) {
				result, err := hybrid.TellActiveProgress()
				if tc.wantErr {
					if err == nil {
						t.Fatal("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result) != tc.wantCount || (len(result) > 0 && result[0].GID != tc.wantGID) {
					t.Errorf("expected %d progress entries with GID %s, got %d: %v", tc.wantCount, tc.wantGID, len(result), result)
				}
			})
		})
	}
}

func TestHybridEngine_PartialFailure_GetGlobalStat(t *testing.T) {
	// Aria2 fails, Surge succeeds
	surge := &mockEngine{
		globalStatRes: GlobalStat{DownloadSpeed: "1000"},
	}
	aria2 := &mockEngine{
		globalStatErr: errors.New("aria2 not ready"),
	}
	hybrid := NewHybridEngine(aria2, surge)

	stat, err := hybrid.GetGlobalStat()
	if err != nil {
		t.Fatalf("unexpected error when one engine fails: %v", err)
	}
	if stat.DownloadSpeed != "1000" {
		t.Errorf("expected Surge-only speed '1000', got '%s'", stat.DownloadSpeed)
	}

	// Surge fails, Aria2 succeeds
	surge2 := &mockEngine{globalStatErr: errors.New("surge error")}
	aria2ok := &mockEngine{globalStatRes: GlobalStat{DownloadSpeed: "3000"}}
	hybrid2 := NewHybridEngine(aria2ok, surge2)

	stat2, err2 := hybrid2.GetGlobalStat()
	if err2 != nil {
		t.Fatalf("unexpected error when Surge fails: %v", err2)
	}
	if stat2.DownloadSpeed != "3000" {
		t.Errorf("expected Aria2-only speed '3000', got '%s'", stat2.DownloadSpeed)
	}

	// Both fail
	surge3 := &mockEngine{globalStatErr: errors.New("surge error")}
	aria2Err := &mockEngine{globalStatErr: errors.New("aria2 error")}
	hybrid3 := NewHybridEngine(aria2Err, surge3)

	_, err3 := hybrid3.GetGlobalStat()
	if err3 == nil {
		t.Fatal("expected error when both engines fail, got nil")
	}
}

func TestHybridEngine_Aria2SplitClamp(t *testing.T) {
	aria2 := &mockEngine{addResultGid: "ar_clamped"}
	surge := &mockEngine{addResultErr: errors.New("surge unavailable")}
	hybrid := NewHybridEngine(aria2, surge)

	// Split=32 should be clamped to 16 on the Aria2 path
	_, err := hybrid.AddUri("http://example.com/file.zip", AddURIOptions{Split: 32})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(aria2.addedOptions) != 1 {
		t.Fatalf("expected 1 Aria2 add, got %d", len(aria2.addedOptions))
	}
	if aria2.addedOptions[0].Split != 16 {
		t.Errorf("Aria2 Split = %d, want 16 (clamped from 32)", aria2.addedOptions[0].Split)
	}
}

func TestHybridEngine_SurgeSplitNotClamped(t *testing.T) {
	aria2 := &mockEngine{}
	surge := &mockEngine{addResultGid: "sg_noclamp"}
	hybrid := NewHybridEngine(aria2, surge)

	// Split=32 should pass through to Surge unchanged
	_, err := hybrid.AddUri("http://example.com/file.zip", AddURIOptions{Split: 32})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(surge.addedOptions) != 1 {
		t.Fatalf("expected 1 Surge add, got %d", len(surge.addedOptions))
	}
	if surge.addedOptions[0].Split != 32 {
		t.Errorf("Surge Split = %d, want 32 (not clamped)", surge.addedOptions[0].Split)
	}
	if len(aria2.addedUrls) != 0 {
		t.Errorf("Aria2 should not have been called")
	}
}

func TestHybridEngine_Aria2SplitUnder16NotClamped(t *testing.T) {
	aria2 := &mockEngine{addResultGid: "ar_ok"}
	surge := &mockEngine{addResultErr: errors.New("surge unavailable")}
	hybrid := NewHybridEngine(aria2, surge)

	_, err := hybrid.AddUri("http://example.com/file.zip", AddURIOptions{Split: 8})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if aria2.addedOptions[0].Split != 8 {
		t.Errorf("Aria2 Split = %d, want 8 (unchanged)", aria2.addedOptions[0].Split)
	}
}
