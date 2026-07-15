package monitor

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/engine/events"
)

// mockTickEngine is a configurable DownloadEngine for tick() recovery tests.
// It allows controlling the return values (tasks + error) of TellActiveLite,
// TellWaitingLite, TellStoppedLite, and their non-Lite fallbacks.
type mockTickEngine struct {
	mockSurgeActiveEngine

	mu            sync.Mutex
	active        []rpc.Task
	waiting       []rpc.Task
	stopped       []rpc.Task
	activeErr     error
	activeLiteErr error
}

func (e *mockTickEngine) TellActiveLite() ([]rpc.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeLiteErr != nil {
		return nil, e.activeLiteErr
	}
	return e.active, nil
}

func (e *mockTickEngine) TellActive() ([]rpc.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeErr != nil {
		return nil, e.activeErr
	}
	return e.active, nil
}

func (e *mockTickEngine) TellWaitingLite(offset, num int) ([]rpc.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.waiting, nil
}

func (e *mockTickEngine) TellWaiting(offset, num int) ([]rpc.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.waiting, nil
}

func (e *mockTickEngine) TellStoppedLite(offset, num int) ([]rpc.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopped, nil
}

func (e *mockTickEngine) TellStopped(offset, num int) ([]rpc.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopped, nil
}

func (e *mockTickEngine) TellActiveProgress() ([]rpc.TaskProgress, error) {
	return nil, nil
}

func (e *mockTickEngine) GetGlobalStat() (rpc.GlobalStat, error) {
	return rpc.GlobalStat{}, nil
}

func (e *mockTickEngine) SaveSession() error { return nil }

func (e *mockTickEngine) ChangeGlobalOption(map[string]string) error { return nil }

func (e *mockTickEngine) StreamEvents(ctx context.Context) (<-chan any, func(), error) {
	ch := make(chan any)
	cleanup := func() {}
	return ch, cleanup, nil
}

func (e *mockTickEngine) TellStatus(gid string, keys []string) (rpc.Task, error) {
	return rpc.Task{}, fmt.Errorf("mock: no engine")
}

func (e *mockTickEngine) TellStatusMulti(gids []string, keys []string) ([]rpc.Task, error) {
	return nil, nil
}

func (e *mockTickEngine) setActiveErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activeLiteErr = err
	e.activeErr = err
}

func (e *mockTickEngine) setLists(active, waiting, stopped []rpc.Task) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active = active
	e.waiting = waiting
	e.stopped = stopped
}

// newTickRecoveryMonitor builds a Monitor wired with a mockTickEngine for
// tick() recovery tests. The caller must defer cache cleanup.
func newTickRecoveryMonitor(t *testing.T, engine rpc.DownloadEngine) *Monitor {
	t.Helper()
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:              hub,
		pusher:           pusher,
		tracker:          tracker,
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * time.Second,
		windowInterval:   1 * time.Second,
		deletedGids:      make(map[string]time.Time),
		telemetry:        NewTelemetryCache(),
	}
	Cache.engine = engine
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	t.Cleanup(func() {
		State.SetWindowExists(prevWindow)
		Cache.arActive = nil
		Cache.arWaiting = nil
		Cache.arStopped = nil
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
		Cache.engine = nil
	})
	return m
}

func resetCacheAr() {
	Cache.arActive = nil
	Cache.arWaiting = nil
	Cache.arStopped = nil
}

// --- Task 5: Startup Recovery Signal Tests ---

// TestStartupRecovery_Aria2RecoveredOnFirstSuccessfulTick verifies that
// aria2Recovered is set to true after the first successful tick.
func TestStartupRecovery_Aria2RecoveredOnFirstSuccessfulTick(t *testing.T) {
	engine := &mockTickEngine{}
	engine.setLists(
		[]rpc.Task{{GID: "ar_task1", Status: "active", TotalLength: "1000"}},
		nil, nil,
	)
	m := newTickRecoveryMonitor(t, engine)

	if m.aria2Recovered.Load() {
		t.Fatal("expected aria2Recovered=false before first tick")
	}

	m.tick()

	if !m.aria2Recovered.Load() {
		t.Fatal("expected aria2Recovered=true after successful tick")
	}
}

// TestStartupRecovery_Aria2NotRecoveredOnTickError verifies that aria2Recovered
// stays false when TellActiveLite returns an error.
func TestStartupRecovery_Aria2NotRecoveredOnTickError(t *testing.T) {
	engine := &mockTickEngine{}
	engine.setActiveErr(fmt.Errorf("aria2c unreachable"))
	m := newTickRecoveryMonitor(t, engine)

	m.tick()

	if m.aria2Recovered.Load() {
		t.Fatal("expected aria2Recovered=false after tick error")
	}

	found := false
	for _, task := range Cache.GetActive() {
		if task.GID == "ar_task1" {
			found = true
		}
	}
	if found {
		t.Fatal("expected no ar_ tasks in cache after tick error (early return)")
	}
}

// TestStartupRecovery_SurgeRecoveredOnFirstSuccessfulReconcile verifies that
// surgeRecovered is set to true after the first successful reconcileSurgeCache.
func TestStartupRecovery_SurgeRecoveredOnFirstSuccessfulReconcile(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	m.surgeEng = &rpc.SurgeEngine{}
	resetCacheSg()

	reader.setLists(
		[]rpc.Task{{GID: "task1", Status: "downloading"}},
		nil, nil,
	)

	if m.surgeRecovered.Load() {
		t.Fatal("expected surgeRecovered=false before first reconcile")
	}

	m.reconcileSurgeCache()

	if !m.surgeRecovered.Load() {
		t.Fatal("expected surgeRecovered=true after successful reconcile")
	}
}

// TestStartupRecovery_SurgeNotRecoveredOnReconcileError verifies that
// surgeRecovered stays false when TellActive returns an error.
func TestStartupRecovery_SurgeNotRecoveredOnReconcileError(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	m.surgeEng = &rpc.SurgeEngine{}
	resetCacheSg()

	reader.mu.Lock()
	reader.err = context.DeadlineExceeded
	reader.mu.Unlock()

	m.reconcileSurgeCache()

	if m.surgeRecovered.Load() {
		t.Fatal("expected surgeRecovered=false after reconcile error")
	}
}

// TestStartupRecovery_RecoveryComplete_BothEngines verifies that
// RecoveryComplete returns true when both engines have recovered.
func TestStartupRecovery_RecoveryComplete_BothEngines(t *testing.T) {
	engine := &mockTickEngine{}
	m := newTickRecoveryMonitor(t, engine)
	m.surgeEng = &rpc.SurgeEngine{}

	m.aria2Recovered.Store(true)
	m.surgeRecovered.Store(true)

	if !m.RecoveryComplete() {
		t.Fatal("expected RecoveryComplete=true with both engines recovered")
	}
}

// TestStartupRecovery_RecoveryComplete_Aria2OnlyMode verifies that in Aria2-only
// mode (surgeEng == nil), RecoveryComplete only checks aria2Recovered.
func TestStartupRecovery_RecoveryComplete_Aria2OnlyMode(t *testing.T) {
	engine := &mockTickEngine{}
	engine.setLists(
		[]rpc.Task{{GID: "ar_task1", Status: "active"}},
		nil, nil,
	)
	m := newTickRecoveryMonitor(t, engine)

	m.tick()

	if !m.RecoveryComplete() {
		t.Fatal("expected RecoveryComplete=true in Aria2-only mode after tick")
	}

	// When aria2Recovered is false, RecoveryComplete should be false.
	m2 := newTickRecoveryMonitor(t, &mockTickEngine{})
	if m2.RecoveryComplete() {
		t.Fatal("expected RecoveryComplete=false when aria2Recovered=false")
	}
}

// TestStartupRecovery_RecoveryComplete_OneEngineDead verifies that
// RecoveryComplete returns true when only one engine has recovered
// (does not block waiting for a dead engine).
func TestStartupRecovery_RecoveryComplete_OneEngineDead(t *testing.T) {
	engine := &mockTickEngine{}
	m := newTickRecoveryMonitor(t, engine)
	m.surgeEng = &rpc.SurgeEngine{}

	// aria2c failed, Surge succeeded.
	m.aria2Recovered.Store(false)
	m.surgeRecovered.Store(true)

	if !m.RecoveryComplete() {
		t.Fatal("expected RecoveryComplete=true with surgeRecovered=true, aria2Recovered=false")
	}

	// aria2c succeeded, Surge failed.
	m.aria2Recovered.Store(true)
	m.surgeRecovered.Store(false)

	if !m.RecoveryComplete() {
		t.Fatal("expected RecoveryComplete=true with aria2Recovered=true, surgeRecovered=false")
	}

	// Both failed.
	m.aria2Recovered.Store(false)
	m.surgeRecovered.Store(false)

	if m.RecoveryComplete() {
		t.Fatal("expected RecoveryComplete=false with both engines unrecovered")
	}
}

// TestStartupRecovery_FastRetryBeforeRecovery verifies that currentTickInterval
// returns 1s when aria2Recovered is false, regardless of window state.
func TestStartupRecovery_FastRetryBeforeRecovery(t *testing.T) {
	engine := &mockTickEngine{}
	m := newTickRecoveryMonitor(t, engine)

	// aria2Recovered defaults to false.
	// With window=true and hasAria2Tasks=false, normal logic would return
	// headlessInterval (5s) because IsSurgeActive()=true and no aria2 tasks.
	// But fast-retry branch should return 1s.
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	got := m.currentTickInterval()
	if got != 1*time.Second {
		t.Errorf("currentTickInterval() = %v, want 1s (fast retry before recovery)", got)
	}
}

// TestStartupRecovery_NormalIntervalAfterRecovery verifies that
// currentTickInterval returns the normal interval after aria2Recovered is set.
func TestStartupRecovery_NormalIntervalAfterRecovery(t *testing.T) {
	engine := &mockTickEngine{}
	m := newTickRecoveryMonitor(t, engine)

	m.aria2Recovered.Store(true)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// With window=true, IsSurgeActive()=true (mockSurgeActiveEngine), and
	// hasAria2Tasks()=true, normal logic returns windowInterval.
	m.prevActiveGids = map[string]bool{"ar_task1": true}

	got := m.currentTickInterval()
	if got != m.windowInterval {
		t.Errorf("currentTickInterval() = %v, want %v (window interval after recovery)", got, m.windowInterval)
	}
}

// TestStartupRecovery_FastRetryHeadlessBeforeRecovery verifies that
// currentTickInterval returns 1s in headless mode when not yet recovered.
func TestStartupRecovery_FastRetryHeadlessBeforeRecovery(t *testing.T) {
	engine := &mockTickEngine{}
	m := newTickRecoveryMonitor(t, engine)

	prevWindow := State.HasWindow()
	State.SetWindowExists(false)
	defer State.SetWindowExists(prevWindow)

	got := m.currentTickInterval()
	if got != 1*time.Second {
		t.Errorf("currentTickInterval() = %v, want 1s (fast retry in headless before recovery)", got)
	}
}

// TestStartupRecovery_Aria2BecameUnavailableLog verifies that after a successful
// tick, a subsequent failing tick does not reset aria2Recovered and does not panic.
func TestStartupRecovery_Aria2BecameUnavailableLog(t *testing.T) {
	engine := &mockTickEngine{}
	engine.setLists(
		[]rpc.Task{{GID: "ar_task1", Status: "active", TotalLength: "1000"}},
		nil, nil,
	)
	m := newTickRecoveryMonitor(t, engine)

	// First tick: success.
	m.tick()
	if !m.aria2Recovered.Load() {
		t.Fatal("expected aria2Recovered=true after first successful tick")
	}

	// Second tick: failure (engine became unavailable).
	engine.setActiveErr(fmt.Errorf("connection refused"))
	m.tick()

	// aria2Recovered should NOT be reset.
	if !m.aria2Recovered.Load() {
		t.Fatal("expected aria2Recovered to remain true after engine became unavailable")
	}
}

// TestStartupRecovery_RecoveryLoggedOnce verifies that maybeLogRecoveryComplete
// only logs the recovery-complete message once via m.recoveryLogged, capturing
// log output to assert the exact string appears exactly once.
func TestStartupRecovery_RecoveryLoggedOnce(t *testing.T) {
	originalOutput := log.Writer()
	defer log.SetOutput(originalOutput)

	// Dual-engine variant: both recovered → "aria2c + Surge" logged once.
	t.Run("DualEngine", func(t *testing.T) {
		var buf bytes.Buffer
		log.SetOutput(&buf)

		engine := &mockTickEngine{}
		m := newTickRecoveryMonitor(t, engine)
		m.surgeEng = &rpc.SurgeEngine{}
		m.aria2Recovered.Store(true)
		m.surgeRecovered.Store(true)

		m.maybeLogRecoveryComplete()
		m.maybeLogRecoveryComplete()
		m.maybeLogRecoveryComplete()

		out := buf.String()
		if got := strings.Count(out, "Startup recovery complete (aria2c + Surge)"); got != 1 {
			t.Errorf("dual-engine recovery log appeared %d times, want 1\noutput:\n%s", got, out)
		}
		if strings.Contains(out, "aria2c only") {
			t.Errorf("dual-engine variant should not log aria2c-only message\noutput:\n%s", out)
		}
	})

	// Aria2-only variant: surgeEng == nil → "aria2c only" logged once.
	t.Run("Aria2Only", func(t *testing.T) {
		var buf bytes.Buffer
		log.SetOutput(&buf)

		engine := &mockTickEngine{}
		m := newTickRecoveryMonitor(t, engine)
		m.aria2Recovered.Store(true)

		m.maybeLogRecoveryComplete()
		m.maybeLogRecoveryComplete()
		m.maybeLogRecoveryComplete()

		out := buf.String()
		if got := strings.Count(out, "Startup recovery complete (aria2c only)"); got != 1 {
			t.Errorf("aria2-only recovery log appeared %d times, want 1\noutput:\n%s", got, out)
		}
		if strings.Contains(out, "aria2c + Surge") {
			t.Errorf("aria2-only variant should not log dual-engine message\noutput:\n%s", out)
		}
	})
}

// TestStartupRecovery_Aria2UnavailableLoggedOnce verifies that after aria2c
// recovers then starts failing, the warn-level "became unavailable" message
// fires only once; subsequent failures log at debug level (spam suppression).
func TestStartupRecovery_Aria2UnavailableLoggedOnce(t *testing.T) {
	originalOutput := log.Writer()
	defer log.SetOutput(originalOutput)

	var buf bytes.Buffer
	log.SetOutput(&buf)

	engine := &mockTickEngine{}
	engine.setLists(
		[]rpc.Task{{GID: "ar_task1", Status: "active", TotalLength: "1000"}},
		nil, nil,
	)
	m := newTickRecoveryMonitor(t, engine)

	// First tick: success → aria2Recovered becomes true.
	m.tick()
	if !m.aria2Recovered.Load() {
		t.Fatal("expected aria2Recovered=true after first successful tick")
	}
	if m.aria2UnavailableLogged.Load() {
		t.Fatal("expected aria2UnavailableLogged=false before any failure")
	}

	// Second tick: failure (engine became unavailable) → warn-level log.
	engine.setActiveErr(fmt.Errorf("connection refused"))
	m.tick()

	if !m.aria2UnavailableLogged.Load() {
		t.Fatal("expected aria2UnavailableLogged=true after first failure post-recovery")
	}
	out := buf.String()
	if got := strings.Count(out, "Aria2 engine became unavailable"); got != 1 {
		t.Errorf("warn-level 'became unavailable' log appeared %d times after first failure, want 1\noutput:\n%s", got, out)
	}

	// Third tick: another failure → debug-level log, no second warn-level.
	m.tick()

	out = buf.String()
	if got := strings.Count(out, "Aria2 engine became unavailable"); got != 1 {
		t.Errorf("warn-level 'became unavailable' log appeared %d times after second failure, want 1\noutput:\n%s", got, out)
	}
	if got := strings.Count(out, "Aria2 engine still unavailable"); got != 1 {
		t.Errorf("debug-level 'still unavailable' log appeared %d times after second failure, want 1\noutput:\n%s", got, out)
	}
}

// --- Task 6: Switching Transition Race Regression Tests ---

// TestSwitchingRace_TickUpdateFromAria2DoesNotTouchSgSlices verifies that
// UpdateFromAria2 only replaces ar_ slices and does not touch sg_ slices.
func TestSwitchingRace_TickUpdateFromAria2DoesNotTouchSgSlices(t *testing.T) {
	resetCacheSg()
	resetCacheAr()
	defer func() {
		resetCacheSg()
		resetCacheAr()
	}()

	// Pre-populate sg_ active slice.
	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")

	// Construct slices with both ar_ and sg_ tasks (simulating a non-Lite
	// merged result that includes Surge tasks).
	active := []rpc.Task{
		{GID: "ar_task1", Status: "active", TotalLength: "500"},
		{GID: "sg_task1", Status: "active", TotalLength: "999"},
	}

	Cache.UpdateFromAria2(active, nil, nil)

	// sg_ task should still be in active (sg_ slice not overwritten).
	sgFound := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			sgFound = true
			if task.TotalLength != "1000" {
				t.Errorf("sg_task1 TotalLength = %s, want 1000 (sg_ slice should be untouched)", task.TotalLength)
			}
		}
	}
	if !sgFound {
		t.Fatal("expected sg_task1 in active after UpdateFromAria2 (sg_ slice preserved)")
	}

	// ar_ task should be in active (ar_ slice replaced).
	arFound := false
	for _, task := range Cache.GetActive() {
		if task.GID == "ar_task1" {
			arFound = true
		}
	}
	if !arFound {
		t.Fatal("expected ar_task1 in active after UpdateFromAria2 (ar_ slice replaced)")
	}
}

// TestSwitchingRace_ConcurrentTickAndEventBridge_NoDoubleOperation verifies
// that concurrent tick + event bridge + reconcileSurgeCache do not cause
// race conditions or double operations.
func TestSwitchingRace_ConcurrentTickAndEventBridge_NoDoubleOperation(t *testing.T) {
	resetHistoryForTest(t)
	resetCacheSg()
	resetCacheAr()
	defer func() {
		resetCacheSg()
		resetCacheAr()
	}()

	engine := &mockTickEngine{}
	engine.setLists(
		[]rpc.Task{{GID: "ar_task1", Status: "active", TotalLength: "1000"}},
		nil, nil,
	)
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:              hub,
		pusher:           NewPusher(hub),
		tracker:          NewTaskTracker(),
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * time.Second,
		windowInterval:   1 * time.Second,
		deletedGids:      make(map[string]time.Time),
		telemetry:        NewTelemetryCache(),
	}
	Cache.engine = engine
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() { Cache.engine = nil }()

	// Pre-populate sg_ active for the complete event to process.
	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	m.tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Set up surge list reader for reconcile.
	reader := &mockSurgeListReader{}
	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})
	m.surgePollReader = reader

	before := historyCount()

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		m.tick()
	}()

	go func() {
		defer wg.Done()
		m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
			DownloadID: "task1",
			Total:      1000,
		})
	}()

	go func() {
		defer wg.Done()
		m.reconcileSurgeCache()
	}()

	wg.Wait()

	// ar_ task should be in cache (tick wrote it).
	arFound := false
	for _, task := range Cache.GetActive() {
		if task.GID == "ar_task1" {
			arFound = true
		}
	}
	if !arFound {
		t.Fatal("expected ar_task1 in active after concurrent tick")
	}

	// processedComplete should be set (dedup prevents double processing).
	if !m.tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}

	// History should have at most one write for sg_task1 complete.
	after := historyCount()
	if after-before > 1 {
		t.Errorf("history writes = %d, want <= 1 (dedup should prevent double processing)", after-before)
	}
}

// TestSwitchingRace_EventBridgeStartsImmediatelyNoWaitForTickRecovery verifies
// that the event bridge can start before tick recovery completes without
// causing cache inconsistency.
func TestSwitchingRace_EventBridgeStartsImmediatelyNoWaitForTickRecovery(t *testing.T) {
	resetHistoryForTest(t)
	resetCacheSg()
	resetCacheAr()
	defer func() {
		resetCacheSg()
		resetCacheAr()
	}()

	engine := &mockTickEngine{}
	engine.setActiveErr(fmt.Errorf("aria2c not ready"))
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:              hub,
		pusher:           NewPusher(hub),
		tracker:          NewTaskTracker(),
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * time.Second,
		windowInterval:   1 * time.Second,
		deletedGids:      make(map[string]time.Time),
		telemetry:        NewTelemetryCache(),
	}
	Cache.engine = engine
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() { Cache.engine = nil }()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		m.tick()
	}()

	go func() {
		defer wg.Done()
		m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
			DownloadID: "task1",
			URL:        "https://example.com/file.zip",
			Total:      1000,
		})
	}()

	wg.Wait()

	// tick failed → aria2Recovered should be false.
	if m.aria2Recovered.Load() {
		t.Fatal("expected aria2Recovered=false (tick failed)")
	}

	// ar_ slice should be empty (tick did not write).
	for _, task := range Cache.GetActive() {
		if task.GID == "ar_task1" {
			t.Fatal("expected no ar_ tasks in cache (tick failed early return)")
		}
	}

	// sg_ slice should contain the Surge task (event path independent).
	sgFound := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			sgFound = true
		}
	}
	if !sgFound {
		t.Fatal("expected sg_task1 in active (event path wrote independently of tick)")
	}
}

// TestSwitchingRace_LastTickRoundOverlapsEventBridgeStartup verifies that
// even if TellActiveLite returns sg_ tasks (defensive scenario), tick's
// filterSurgeTasks prevents them from entering UpdateFromAria2, and the
// event path's MoveTaskToWaiting operates on sg_ slices independently.
func TestSwitchingRace_LastTickRoundOverlapsEventBridgeStartup(t *testing.T) {
	resetHistoryForTest(t)
	resetCacheSg()
	resetCacheAr()
	defer func() {
		resetCacheSg()
		resetCacheAr()
	}()

	// Pre-populate sg_ slices (simulating prior surgePollLoop fill).
	Cache.AddSgTask(rpc.Task{
		GID:    "sg_task1",
		Status: "active",
	}, "active")

	// Engine returns sg_ tasks in TellActiveLite (defensive scenario —
	// HybridEngine should not, but we test the filterSurgeTasks guard).
	engine := &mockTickEngine{}
	engine.setLists(
		[]rpc.Task{
			{GID: "ar_task1", Status: "active", TotalLength: "500"},
			{GID: "sg_task1", Status: "active", TotalLength: "999"},
		},
		nil, nil,
	)
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:              hub,
		pusher:           NewPusher(hub),
		tracker:          NewTaskTracker(),
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * time.Second,
		windowInterval:   1 * time.Second,
		deletedGids:      make(map[string]time.Time),
		telemetry:        NewTelemetryCache(),
	}
	Cache.engine = engine
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() { Cache.engine = nil }()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		m.tick()
	}()

	go func() {
		defer wg.Done()
		m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{
			DownloadID: "task1",
		})
	}()

	wg.Wait()

	// ar_ task should be in active (tick wrote ar_ slice).
	arFound := false
	for _, task := range Cache.GetActive() {
		if task.GID == "ar_task1" {
			arFound = true
		}
	}
	if !arFound {
		t.Fatal("expected ar_task1 in active after tick")
	}

	// sg_task1 should be in waiting (event path moved it via DownloadPausedMsg).
	sgWaiting := false
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_task1" {
			sgWaiting = true
		}
	}
	if !sgWaiting {
		t.Fatal("expected sg_task1 in waiting after DownloadPausedMsg event")
	}

	// sg_task1 should NOT be in active (moved to waiting by event path).
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in active (moved to waiting by event path)")
		}
	}
}
