package monitor

import (
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// metadataCleanupInterval throttles periodic orphan metadata eviction from
// the Aria2 tick path. Cleanup runs at most once per this interval.
const metadataCleanupInterval = 30 * time.Second

// metadataCleanupGrace protects recently-fetched metadata from eviction,
// covering the window between metadata prefetch and the task appearing in
// engine lists. Matches the pendingStartGids TTL.
const metadataCleanupGrace = 30 * time.Second

// Monitor 后端监控器
type Monitor struct {
	app     *application.App
	hub     *events.Hub
	systray *application.SystemTray
	engine  rpc.DownloadEngine
	tracker *TaskTracker
	pusher  *Pusher

	stopChan      chan struct{}
	forceTickChan chan struct{}
	stopOnce      sync.Once

	// 轮询间隔
	headlessInterval time.Duration
	windowInterval   time.Duration

	// RPC polling optimization
	mu                      sync.Mutex
	shouldFetchStopped      bool
	shouldFetchStoppedUntil time.Time
	lastStopped             []rpc.Task
	lastStoppedFetchTime    time.Time

	// Recently deleted tasks to filter out during engine/cache synchronization races
	deletedGids map[string]time.Time

	// Previous tick state for transition detection
	prevActiveGids  map[string]bool
	prevWaitingGids map[string]bool

	// Per-worker telemetry cache
	telemetry *TelemetryCache

	// Per-GID latest engine chunk bitmap snapshot (Surge EventBatchProgress)
	chunkSnapshots *ChunkSnapshotCache

	// Convergence tick for runtime scale up/down
	convergence *smartthread.ConvergenceTicker

	// CDN throttle fingerprint detector (self-contained 1s ticker)
	cdnDetector *CDNDetector

	// Cached SurgeEngine ref for HybridEngine (nil in Aria2-only mode); avoids
	// re-acquiring it on every Surge event including high-frequency ProgressMsg.
	surgeEng *rpc.SurgeEngine

	// Idle memory reclaim timer for Surge engine after terminal events.
	idleReclaimMu      sync.Mutex
	idleReclaimTimer   *time.Timer
	idleReclaimStopped bool

	// Single-slot admission wake notification (not a subscriber bus):
	// installed once by the composition root, cleared on shutdown. The
	// callback is copied under kickMu and invoked without holding it.
	kickMu        sync.Mutex
	admissionKick func()

	// Network environment fingerprint cache (MAC → envKey)
	netEnv *NetEnvCache

	// Surge polling fallback: periodic reconciliation + event stream reconnect.
	surgePollInterval    time.Duration
	surgeStreamConnected atomic.Bool
	surgePollStopChan    chan struct{}
	surgePollWg          sync.WaitGroup
	surgePollReader      surgeListReader

	runLoopWg     sync.WaitGroup
	surgeBridgeWg sync.WaitGroup

	// Startup recovery flags: set once after the first successful tick
	// (aria2c) / reconcileSurgeCache (Surge) round. Used for recovery
	// completion logging and fast-retry interval before first success.
	aria2Recovered         atomic.Bool
	surgeRecovered         atomic.Bool
	recoveryLogged         sync.Once
	aria2UnavailableLogged atomic.Bool

	// lastMetadataCleanup tracks the last run of orphan metadata eviction
	// from tick(). The zero value means "never run", which triggers a
	// cleanup on the first post-recovery tick.
	lastMetadataCleanup time.Time
}

func New(app *application.App, hub *events.Hub, systray *application.SystemTray, engine rpc.DownloadEngine) *Monitor {
	m := &Monitor{
		app:                  app,
		hub:                  hub,
		systray:              systray,
		engine:               engine,
		stopChan:             make(chan struct{}),
		forceTickChan:        make(chan struct{}, 1),
		headlessInterval:     5 * time.Second, // 无头模式：5秒
		windowInterval:       1 * time.Second, // 窗口模式：1秒（由前端主导）
		shouldFetchStopped:   true,            // 初始时获取一次 stopped 任务
		lastStoppedFetchTime: time.Now(),
		deletedGids:          make(map[string]time.Time),
		prevActiveGids:       make(map[string]bool),
		prevWaitingGids:      make(map[string]bool),
		telemetry:            NewTelemetryCache(),
		chunkSnapshots:       NewChunkSnapshotCache(),
		surgePollInterval:    10 * time.Second,
	}

	Cache.engine = engine

	// 订阅任务变更事件，触发即时刷新
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		// 仅关注影响列表状态的事件（添加、暂停、恢复、删除）
		// 进度更新 (progress/complete/error) 暂不需要强制刷新，由轮询/pusher处理
		// 修正：AddUri 后需要立即看到任务，所以主要关注 add
		// Pause/Resume/Remove 也会改变 API 返回的状态，所以也需要刷新
		switch delta.Type {
		case "add", "pause", "resume":
			select {
			case m.forceTickChan <- struct{}{}:
				log.Printf("[Monitor] Triggering immediate update used by event: %s", delta.Type)
			default:
				// channel full, update already pending
			}
		case "remove", "complete", "error":
			m.mu.Lock()
			m.shouldFetchStopped = true
			m.shouldFetchStoppedUntil = time.Now().Add(1500 * time.Millisecond)
			m.mu.Unlock()
			select {
			case m.forceTickChan <- struct{}{}:
				log.Printf("[Monitor] Triggering immediate update (with stopped) used by event: %s", delta.Type)
			default:
			}
		}
	})

	// 创建任务追踪器
	m.tracker = NewTaskTracker()

	// 创建增量推送器
	m.pusher = NewPusher(hub)

	// 注册到全局状态
	State.SetTracker(m.tracker)

	return m
}

// NewMonitorForTest creates a Monitor with a hub and pusher for cross-package
// tests that need to emit task:move / remove deltas via monitor.State.
func NewMonitorForTest(hub *events.Hub) *Monitor {
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:            hub,
		pusher:         NewPusher(hub),
		tracker:        tracker,
		deletedGids:    make(map[string]time.Time),
		chunkSnapshots: NewChunkSnapshotCache(),
	}
	State.SetTracker(tracker)
	return m
}

// NewMonitorWithSurgeEngineForTest creates a test Monitor configured with a SurgeEngine.
func NewMonitorWithSurgeEngineForTest(se *rpc.SurgeEngine) *Monitor {
	m := NewMonitorForTest(events.NewHub(nil))
	m.surgeEng = se
	m.stopChan = make(chan struct{})
	return m
}

// HasPendingIdleReclaimForTesting reports whether an idle reclaim timer is currently pending.
func (m *Monitor) HasPendingIdleReclaimForTesting() bool {
	if m == nil {
		return false
	}
	m.idleReclaimMu.Lock()
	defer m.idleReclaimMu.Unlock()
	return m.idleReclaimTimer != nil && !m.idleReclaimStopped
}

type acceptedTaskChange struct {
	task rpc.Task
	from string
	to   string
	kind string
}

// ApplyPause folds a successful engine pause acknowledgement into local state.
// Call only after the RPC succeeded — a failed call must not reach it. A sg_
// GID whose live pool evidence is known and not paused wins over the ack; an
// explicit absent answer does not veto a pause because pausing a queued pool
// member legitimately removes it. Nil receiver, nil tracker, blank GID,
// missing/stopped/already-paused rows and an accepted terminal are no-ops.
func (m *Monitor) ApplyPause(gid string) {
	if m == nil || m.tracker == nil || strings.TrimSpace(gid) == "" {
		return
	}
	m.tracker.RunUnderLifecycle(gid, func() {
		if live := m.readSurgeLiveStatus(gid); live.Known && live.Status != "paused" {
			return
		}
		task, source, exists := Cache.GetTask(gid)
		if !exists || source == "stopped" || task.Status == "paused" ||
			(task.Status != "active" && task.Status != "waiting") ||
			m.tracker.TerminalAcceptedInCurrentGeneration(gid) {
			return
		}
		from := Cache.MoveTaskToWaiting(gid, "paused")
		if from == "" {
			return
		}
		m.tracker.EnsureTrackedFromEvent(gid, parseInt64(task.TotalLength), sourceURLFromTask(task), 0, "paused")
		m.tracker.SetStatusFromEvent(gid, "paused")
		if completed := parseInt64(task.CompletedLength); completed > 0 {
			m.tracker.UpdateProgressFromEvent(gid, parseInt64(task.TotalLength), completed)
		}
		task, _, _ = Cache.GetTask(gid)
		m.publishAcceptedTaskChange(acceptedTaskChange{task: task, from: from, to: "waiting", kind: "pause"})
	})
}

// ApplyResume folds a successful engine resume acknowledgement into local
// state. Call only after the RPC succeeded — a failed call must not reach it.
// For sg_ GIDs the in-gate live pool read decides: known active lands active,
// known waiting lands waiting, any other known status or an explicit absent
// refuses (a late ack never reopens a finished or removed download), and
// unknown keeps the ack's waiting fold. Non-Surge GIDs have no live seam and
// always take the waiting fold. Nil receiver, nil tracker, blank GID and
// unexplained live-row/terminal conflicts are no-ops; duplicate acks fold
// idempotently.
func (m *Monitor) ApplyResume(gid string) {
	if m == nil || m.tracker == nil || strings.TrimSpace(gid) == "" {
		return
	}
	m.tracker.RunUnderLifecycle(gid, func() {
		target := "waiting"
		live := m.readSurgeLiveStatus(gid)
		switch {
		case live.Known && live.Status == "active":
			target = "active"
		case live.Known && live.Status == "waiting":
			target = "waiting"
		case live.Known || live.Absent:
			return
		}
		task, source, exists := Cache.GetTask(gid)
		if exists {
			switch {
			case source == "active":
				return // already live; a resume ack never demotes an active row
			case source != "stopped":
				if task.Status == "waiting" && target == "waiting" {
					return // already folded
				}
				if task.Status != "paused" && task.Status != "waiting" {
					return
				}
				if m.tracker.TerminalAcceptedInCurrentGeneration(gid) {
					return // live row vs accepted terminal: keep the marker
				}
			}
		}
		from := ""
		if !exists {
			entry, ok := history.Get(gid)
			if !ok {
				return
			}
			task = history.ToStoppedTask(entry)
			task.Title = entry.Title
			if IsSgGid(gid) {
				Cache.AddSgTask(task, "stopped")
			} else {
				// Stage + move share one arMu section — readers only ever see
				// absent or waiting, never a transient stopped row.
				from = Cache.RestoreAriaHistoryTaskToWaiting(task, target)
			}
		}
		if from == "" {
			if target == "active" {
				from = Cache.MoveTaskToActive(gid, "active")
			} else {
				from = Cache.MoveTaskToWaiting(gid, "waiting")
			}
		}
		if from == "" {
			return
		}
		if from == "stopped" {
			if m.tracker.afterMoveBeforeRetire != nil {
				m.tracker.afterMoveBeforeRetire(gid)
			}
			retireHistoryAndReopenToStatusLocked(m.tracker, gid, target)
		}
		m.tracker.EnsureTrackedFromEvent(gid, parseInt64(task.TotalLength), sourceURLFromTask(task), 0, target)
		m.tracker.SetStatusFromEvent(gid, target)
		if target == "active" {
			baseline, known := surgeActivationBaseline(m.tracker, gid, live.CompletedLength, live.CompletedKnown)
			m.tracker.ObserveActivation(gid, baseline, known)
		}
		if completed := parseInt64(task.CompletedLength); completed > 0 {
			m.tracker.UpdateProgressFromEvent(gid, parseInt64(task.TotalLength), completed)
		}
		task, _, _ = Cache.GetTask(gid)
		to := "waiting"
		if target == "active" {
			to = "active"
		}
		m.publishAcceptedTaskChange(acceptedTaskChange{task: task, from: from, to: to, kind: "resume"})
	})
}

// publishAcceptedTaskChange emits an accepted change. Callers run it inside
// the per-GID lifecycle gate so move/delta publishes serialize in commit
// order; everything below is non-blocking or a leaf lock, and hub listeners
// must not re-enter RunUnderLifecycle for the same GID.
func (m *Monitor) publishAcceptedTaskChange(change acceptedTaskChange) {
	if m == nil || change.task.GID == "" {
		return
	}
	// Attach stored group/metadata facts before publishing: a new-task add
	// delta must carry DownloadGroup so the frontend can raise its group
	// placeholder instead of flashing a loose card, and moves keep the row's
	// group identity across lists.
	enriched := []rpc.Task{change.task}
	Cache.EnrichTasks(enriched)
	change.task = enriched[0]
	// Authoritative status change accepted — notify admission before the
	// hub/window early-return so headless runs still reach the controller.
	m.KickAdmission()
	if m.hub == nil {
		return
	}
	if change.from != "" && change.from != change.to {
		m.hub.EmitTaskMove(events.TaskMove{GID: change.task.GID, From: change.from, To: change.to, Task: change.task})
	}
	// An aria2 resume ack cannot verify where the task landed — there is no
	// live seam, so the local waiting fold is a guess. Shipping it through the
	// debounced delta channel lets it arrive after the authoritative
	// waiting→active move and demote a live row on the frontend; the move and
	// tick streams own aria2 wire truth. Surge acks keep the prompt delta.
	staleGuess := change.kind == "resume" && !IsSgGid(change.task.GID)
	if !staleGuess && State.HasWindow() && m.pusher != nil {
		m.pusher.Queue(events.TaskDelta{Type: change.kind, GID: change.task.GID, Payload: change.task})
	}
	m.hub.NotifyInternal(events.TaskDelta{Type: change.kind, GID: change.task.GID, Payload: change.task})
}

// SetAdmissionKick installs the single admission-controller notification
// slot; nil clears it. Not a subscriber bus — one owner, one callback.
func (m *Monitor) SetAdmissionKick(fn func()) {
	if m == nil {
		return
	}
	m.kickMu.Lock()
	m.admissionKick = fn
	m.kickMu.Unlock()
}

// KickAdmission reports an authoritative fact that may have freed or created
// a dispatch opportunity. The callback is a capacity-1 non-blocking wake, so
// it is safe to invoke under any lock including the GID gate; nil-safe and
// idempotent — the callback itself merges redundant edges.
func (m *Monitor) KickAdmission() {
	if m == nil {
		return
	}
	m.kickMu.Lock()
	fn := m.admissionKick
	m.kickMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (m *Monitor) removeLocalTaskLocked(gid string) {
	Cache.RemoveTask(gid)
	if m.telemetry != nil {
		m.telemetry.Remove(gid)
	}
	if m.chunkSnapshots != nil {
		m.chunkSnapshots.Remove(gid)
	}
	if m.tracker != nil {
		m.tracker.removeTaskLocked(gid)
	}
}

type surgeLiveStatusReader interface {
	ReadLiveStatus(id string) rpc.SurgeLiveStatus
}

// readSurgeLiveStatus converts only Surge GIDs and recognized raw pool statuses.
// An explicit list reader without the optional seam remains unknown, not engine fallback.
func (m *Monitor) readSurgeLiveStatus(gid string) rpc.SurgeLiveStatus {
	if m == nil || !strings.HasPrefix(gid, "sg_") || gid == "sg_" {
		return rpc.SurgeLiveStatus{}
	}
	var live rpc.SurgeLiveStatus
	id := strings.TrimPrefix(gid, "sg_")
	if m.surgePollReader != nil {
		if reader, ok := m.surgePollReader.(surgeLiveStatusReader); ok {
			live = reader.ReadLiveStatus(id)
		}
	} else {
		live = m.surgeEng.ReadLiveStatus(id)
	}
	if !live.Known {
		// Unknown stays unknown; an explicit absent answer must survive so
		// callers can distinguish "not tracked" from "status indeterminate".
		return rpc.SurgeLiveStatus{Absent: live.Absent}
	}
	switch live.Status {
	case "queued":
		live.Status = "waiting"
	case "downloading":
		live.Status = "active"
	case "pausing", "paused":
		live.Status = "paused"
	case "completed":
		live.Status = "complete"
	case "error":
		live.Status = "error"
	default:
		return rpc.SurgeLiveStatus{}
	}
	return live
}

// Caller holds the GID gate and has accepted a real stopped→live transition.
// This helper supplies neither authorization nor duplicate-ack detection.
func retireHistoryAndReopenToStatusLocked(tracker *TaskTracker, gid, targetStatus string) {
	if gid == "" || (targetStatus != "active" && targetStatus != "waiting" && targetStatus != "paused") {
		return
	}
	if tracker != nil {
		tracker.ReopenAfterStoppedToLive(gid, targetStatus)
		if tracker.retireBetweenReopenAndRemove != nil {
			tracker.retireBetweenReopenAndRemove(gid)
		}
	}
	if entry, ok := history.Get(gid); ok && entry.DownloadGroup != nil {
		RegisterTaskGroup(gid, *entry.DownloadGroup)
		Cache.SetTaskGroup(gid, *entry.DownloadGroup)
	}
	history.RemoveKeepingGroupStore(gid)
}

// markCompleteAndHandleLocked accepts terminal under the GID gate and returns
// the stamped terminal copy for post-gate finishing. Caller must already hold
// the GID lifecycle lock.
func (m *Monitor) markCompleteAndHandleLocked(gid, status string, prep func(*TrackedTask)) *TrackedTask {
	if m == nil || gid == "" {
		return nil
	}
	// Snapshots die with the download generation on every terminal accept,
	// including repeat terminals where MarkCompleteFromEvent returns nil.
	if m.chunkSnapshots != nil {
		m.chunkSnapshots.Remove(gid)
	}
	if m.tracker == nil {
		return nil
	}
	completed := m.tracker.MarkCompleteFromEvent(gid, status)
	if completed == nil {
		return nil
	}
	if prep != nil {
		prep(completed)
	}
	return completed
}

// markCompleteAndHandle accepts a terminal event under the per-GID lifecycle
// lock so it cannot race history retirement's reopen+remove critical section.
// With no tracker there is no gate to take; the Locked body still runs so the
// snapshot dies with the generation either way.
func (m *Monitor) markCompleteAndHandle(gid, status string, prep func(*TrackedTask)) {
	if m == nil || gid == "" {
		return
	}
	var accepted *TrackedTask
	if m.tracker != nil {
		m.tracker.RunUnderLifecycle(gid, func() {
			accepted = m.markCompleteAndHandleLocked(gid, status, prep)
		})
		m.finishTerminalCopy(accepted)
		return
	}
	m.finishTerminalCopy(m.markCompleteAndHandleLocked(gid, status, prep))
}

// moveToStoppedAndHandleGate serializes cache stopped membership with terminal
// acceptance and history.Add under one lifecycle gate. gateFn runs extra
// in-gate bookkeeping (e.g. the terminal master-mirror merge) that must run
// inside the acceptance section even for already-accepted repeats. publish
// also runs inside the same gate section — after acceptance, handed the
// committed canonical total — so per-GID delta emits serialize in commit
// order and the pusher's arrival-order dedup cannot be inverted by post-gate
// work (finishTerminalCopy blocks on recovery RPCs). With no tracker there is
// no gate to take; the Locked body still runs so the chunk snapshot dies with
// the generation either way.
func (m *Monitor) moveToStoppedAndHandleGate(gid, status, errorCode, errorMessage string, ensureTotal int64, gateFn func(), prep func(*TrackedTask), publish func(committedTotal int64)) {
	if m == nil || gid == "" {
		return
	}
	var accepted *TrackedTask
	body := func() {
		if gateFn != nil {
			gateFn()
		}
		total := ensureTotal
		if status == "complete" && total <= 0 {
			// In-gate canonical fallback: the current full row, not the
			// caller's pre-gate read.
			if task, _, ok := Cache.GetTask(gid); ok {
				total = parsePositiveLength(task.TotalLength)
			}
		}
		// One critical section: the move and the terminal-only field commit
		// (canonical total=completed for complete; code/message for error).
		Cache.MoveTaskToStoppedTerminal(gid, status, errorCode, errorMessage, total)
		if m.tracker != nil {
			if status == "complete" && total > 0 {
				m.tracker.EnsureTrackedComplete(gid, total)
			} else {
				m.tracker.EnsureTrackedFromEvent(gid, total, "", 0, "")
			}
		}
		accepted = m.markCompleteAndHandleLocked(gid, status, prep)
		// Repeat terminals (idempotent accept, accepted == nil) still
		// publish: at-least-once delivery is deliberate.
		if publish != nil {
			publish(total)
		}
	}
	if m.tracker != nil {
		m.tracker.RunUnderLifecycle(gid, body)
		m.finishTerminalCopy(accepted)
		return
	}
	body()
	m.finishTerminalCopy(accepted)
}

// RecoveryComplete reports whether at least one available engine has completed
// its first successful recovery round. Non-blocking; used for diagnostics, not
// for gating GetFullSnapshot. In Aria2-only mode (m.surgeEng == nil) only
// aria2Recovered is checked. If both engines are present, returns true when
// either has recovered — the unrecovered one keeps retrying independently.
func (m *Monitor) RecoveryComplete() bool {
	aria2OK := m.aria2Recovered.Load()
	surgeOK := m.surgeRecovered.Load()
	if m.surgeEng == nil {
		return aria2OK
	}
	if aria2OK && surgeOK {
		return true
	}
	return aria2OK || surgeOK
}

// maybeLogRecoveryComplete logs a one-time startup recovery complete message
// when all available engines have completed their first successful recovery
// round. Called from tick() and reconcileSurgeCache() after setting their
// own recovered flags.
func (m *Monitor) maybeLogRecoveryComplete() {
	aria2OK := m.aria2Recovered.Load()
	surgeOK := m.surgeRecovered.Load()
	if m.surgeEng == nil {
		if aria2OK {
			m.recoveryLogged.Do(func() {
				log.Printf("[Monitor] Startup recovery complete (aria2c only)")
			})
		}
		return
	}
	if aria2OK && surgeOK {
		m.recoveryLogged.Do(func() {
			log.Printf("[Monitor] Startup recovery complete (aria2c + Surge)")
		})
	}
}

func (m *Monitor) Start() {
	// Start network environment fingerprint cache (background MAC refresh)
	m.netEnv = NewNetEnvCache()
	State.SetNetEnv(m.netEnv)
	m.netEnv.Start()

	// Start convergence tick if engine is HybridEngine
	if he, ok := m.engine.(*rpc.HybridEngine); ok {
		adapter := &trackerAdapter{TaskTracker: m.tracker, engine: he}
		convIntervalSec := 0
		maxConn := 0
		convIntervalSec = config.Get().ConvergenceInterval
		if v, err := strconv.Atoi(config.Get().MaxConnections); err == nil && v > 0 {
			maxConn = v
		}
		m.convergence = smartthread.NewConvergenceTicker(he, adapter, m.telemetry, adapter, adapter, convIntervalSec, maxConn)
		m.convergence.SetOccupancyProvider(adapter)
		m.convergence.Start()
		// CDN throttle detector: only for HybridEngine (needs Surge control).
		if se, ok := he.SurgeEngineRef(); ok && se != nil {
			m.surgeEng = se
			m.surgePollReader = se
			m.cdnDetector = NewCDNDetector(se, nil, func() []string { return m.telemetry.ActiveGIDs() })
			m.cdnDetector.Start()
		}
	}

	// Start Surge event bridge (reconnect loop) and polling fallback goroutine.
	// Both are Surge-only; skipped in Aria2-only mode (m.surgeEng == nil).
	if m.surgeEng != nil {
		m.startSurgeEventBridge()
		m.surgePollStopChan = make(chan struct{})
		m.surgePollWg.Go(func() {
			m.surgePollLoop()
		})
	}

	m.runLoopWg.Go(func() {
		m.runLoop()
	})
}

func (m *Monitor) Stop() {
	if m.netEnv != nil {
		m.netEnv.Stop()
	}
	if m.cdnDetector != nil {
		m.cdnDetector.Stop()
	}
	if m.convergence != nil {
		m.convergence.Stop()
	}
	if m.surgePollStopChan != nil {
		close(m.surgePollStopChan)
		m.surgePollWg.Wait()
	}
	m.idleReclaimMu.Lock()
	m.idleReclaimStopped = true
	if m.idleReclaimTimer != nil {
		m.idleReclaimTimer.Stop()
		m.idleReclaimTimer = nil
	}
	m.idleReclaimMu.Unlock()
	m.stopOnce.Do(func() {
		if m.stopChan != nil {
			close(m.stopChan)
		}
		m.runLoopWg.Wait()
		m.surgeBridgeWg.Wait()
		StopDownloadGroupNamer()
	})
}

func (m *Monitor) runLoop() {
	ticker := time.NewTicker(m.currentTickInterval())
	defer ticker.Stop()

	// 启动时立即执行一次
	m.tick()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.tick()
			ticker.Reset(m.currentTickInterval())
		case <-m.forceTickChan:
			m.tick()
			ticker.Reset(m.currentTickInterval())
		}
	}
}

// InvalidateTask 使指定任务的缓存失效并发送删除事件
// 用于 RemoveTask 后确保前端和缓存同步
func (m *Monitor) InvalidateTask(gid string) {
	m.tracker.RunUnderLifecycle(gid, func() {
		// 1. 清理元数据缓存
		Cache.InvalidateMetadata(gid)
		RemoveTaskGroup(gid)

		// 2. 从 lastStopped 缓存中移除
		m.mu.Lock()
		m.deletedGids[gid] = time.Now()

		newStopped := make([]rpc.Task, 0, len(m.lastStopped))
		for _, t := range m.lastStopped {
			if t.GID != gid {
				newStopped = append(newStopped, t)
			}
		}
		m.lastStopped = newStopped
		m.shouldFetchStopped = true // 标记需要重新获取
		m.mu.Unlock()

		if m.telemetry != nil {
			m.telemetry.Remove(gid)
		}
		if m.chunkSnapshots != nil {
			m.chunkSnapshots.Remove(gid)
		}
		// Emit inside the gate so the remove emit serializes with concurrent
		// same-GID commits in commit order. m.mu is already released above;
		// delta listeners only take m.mu / push a bounded channel.
		if m.hub != nil {
			m.hub.EmitTaskDelta(events.TaskDelta{Type: "remove", GID: gid})
		}
	})
	if m.convergence != nil {
		m.convergence.RemoveTask(gid)
	}

	// Removal frees or voids admission opportunities; notify outside the
	// lifecycle gate.
	m.KickAdmission()

	log.Printf("[Monitor] Task invalidated: %s", gid)
}

// collectTelemetry gathers per-worker telemetry from the Surge engine for active Surge tasks.
// Reads from Cache.GetActive() so Surge tasks are covered even though tick no longer polls them.
func (m *Monitor) collectTelemetry() {
	if m.telemetry == nil {
		return
	}

	// Try to get the underlying SurgeEngine from HybridEngine
	he, ok := m.engine.(*rpc.HybridEngine)
	if !ok {
		return
	}
	se, ok := he.SurgeEngineRef()
	if !ok || se == nil {
		return
	}

	activeGids := make(map[string]bool)
	for _, task := range Cache.GetActive() {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		activeGids[task.GID] = true
		rawGid := task.GID[3:]
		stats := se.GetWorkerStats(rawGid)
		if stats != nil {
			m.telemetry.Set(task.GID, stats)
		} else {
			// Active GID but no worker stats (e.g., single-thread fallback after SessionReset).
			// Clear stale telemetry to prevent convergence ticker from acting on outdated snapshots.
			m.telemetry.Remove(task.GID)
		}
	}

	// Remove telemetry for GIDs that are no longer active
	for _, gid := range m.telemetry.ActiveGIDs() {
		if !activeGids[gid] {
			m.telemetry.Remove(gid)
		}
	}
}

// GetTelemetry returns the telemetry cache for external consumers.
func (m *Monitor) GetTelemetry() *TelemetryCache {
	return m.telemetry
}

// GetChunkSnapshots returns the chunk bitmap snapshot cache for pull readers
// (GetTaskDetails). Nil-receiver safe.
func (m *Monitor) GetChunkSnapshots() *ChunkSnapshotCache {
	if m == nil {
		return nil
	}
	return m.chunkSnapshots
}

// LastRawBps returns Convergence macro-band occupancy for gid.
// Missing/cold monitor or ticker → (0, false).
func (m *Monitor) LastRawBps(gid string) (bps int64, ready bool) {
	if m == nil || m.convergence == nil {
		return 0, false
	}
	return m.convergence.LastRawBps(gid)
}

// NewMonitorWithTelemetryForTest builds a Monitor with the given telemetry cache.
// For unit tests that need ExistingDomainWorkers / occupancy wiring without Start().
func NewMonitorWithTelemetryForTest(tc *TelemetryCache) *Monitor {
	if tc == nil {
		tc = NewTelemetryCache()
	}
	return &Monitor{telemetry: tc}
}

// trackerAdapter wraps TaskTracker to satisfy smartthread.TrackerProvider,
// OccupancyProvider, PeakEfficiencyRecorder, and RateLimitChecker interfaces.
type trackerAdapter struct {
	*TaskTracker
	engine *rpc.HybridEngine
}

// activeCacheSpeedByGID snapshots measured cache speeds keyed by gid. Reads
// only the cache's own lock; safe on the c.mu→adapter path because the
// cache never calls back into Convergence. Only parse-valid non-negative
// values count as measured — "0" is a real zero, while empty/invalid/negative
// strings are absent from the map so callers see them as missing telemetry.
// Only GetActive() is scanned, deliberately: a tracker-active task whose
// cache row is still in waiting sees TelemetryValid=false until the move
// lands — degrading to missing telemetry (zero contribution) is the safe
// direction and self-corrects once the cache catches up.
func activeCacheSpeedByGID() map[string]int64 {
	speeds := make(map[string]int64)
	if Cache == nil {
		return speeds
	}
	for _, t := range Cache.GetActive() {
		v, err := strconv.ParseInt(strings.TrimSpace(t.DownloadSpeed), 10, 64)
		if err != nil || v < 0 {
			continue
		}
		speeds[t.GID] = v
	}
	return speeds
}

// trackedTaskInfos maps tracker copies to smartthread occupancy inputs. It
// takes no locks itself and never touches Convergence state — Surge raw/ready
// is intentionally absent here; Convergence overlays it from its own states
// under c.mu, and the external collector overlays it after releasing locks.
func trackedTaskInfos(tasks []TrackedTask, speedByGID map[string]int64) []smartthread.TrackedTaskInfo {
	result := make([]smartthread.TrackedTaskInfo, 0, len(tasks))
	for _, t := range tasks {
		speed, measured := speedByGID[t.GID]
		result = append(result, smartthread.TrackedTaskInfo{
			GID:               t.GID,
			Status:            t.Status,
			Scope:             t.Scope,
			Domain:            t.Domain,
			EnvKey:            t.CurrentEnvKey,
			IsKeepAlive:       t.IsKeepAlive,
			CompletedLength:   t.CompletedLength,
			MinChunk:          t.MinChunk,
			TotalLength:       t.TotalLength,
			ThreadCount:       t.ThreadCount,
			TargetBandwidth:   t.TargetBandwidth,
			AllocatedAt:       t.AllocatedAt,
			TelemetryBps:      speed,
			TelemetryValid:    measured,
			ActivatedAt:       t.ActivatedAt,
			FirstProgressAt:   t.FirstProgressAt,
			CompatClaim:       t.compatClaim,
			AddAnchorAt:       t.addAnchorAt,
			AdmissionReserved: t.admittedNotStarted,
		})
	}
	return result
}

func (a *trackerAdapter) GetActiveTrackedTasks() []smartthread.TrackedTaskInfo {
	return trackedTaskInfos(a.TaskTracker.GetActiveTrackedTasks(), activeCacheSpeedByGID())
}

// GetOccupancyTrackedTasks implements smartthread.OccupancyProvider. Lock-free
// with respect to c.mu: tracker copies are immutable scalars, the cache read
// holds only the cache lock, and no Convergence method is invoked.
func (a *trackerAdapter) GetOccupancyTrackedTasks() []smartthread.TrackedTaskInfo {
	return trackedTaskInfos(a.TaskTracker.GetOccupancyTrackedTasks(), activeCacheSpeedByGID())
}

// GetRateLimit implements RateLimitChecker by delegating to the HybridEngine.
func (a *trackerAdapter) GetRateLimit(gid string) (int64, bool) {
	if a.engine == nil {
		return 0, false
	}
	return a.engine.GetRateLimit(gid)
}
