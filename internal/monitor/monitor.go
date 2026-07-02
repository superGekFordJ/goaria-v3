package monitor

import (
	"fmt"
	"log"
	"path/filepath"
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
	"goaria-v3/internal/speedstats"

	"github.com/wailsapp/wails/v3/pkg/application"
)

var scopeClassifier = speedstats.NewScopeClassifier()

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

	// Convergence tick for runtime scale up/down
	convergence *smartthread.ConvergenceTicker

	// CDN throttle fingerprint detector (self-contained 1s ticker)
	cdnDetector *CDNDetector

	// Cached SurgeEngine ref for HybridEngine (nil in Aria2-only mode); avoids
	// re-acquiring it on every Surge event including high-frequency ProgressMsg.
	surgeEng *rpc.SurgeEngine

	// Network environment fingerprint cache (MAC → envKey)
	netEnv *NetEnvCache

	// Per-GID last intention for stale pause event defense (last-intention-wins).
	pauseResumeIntentions map[string]string
	pauseResumeVersionMu  sync.RWMutex

	// Surge polling fallback: periodic reconciliation + event stream reconnect.
	surgePollInterval    time.Duration
	surgeStreamConnected atomic.Bool
	surgePollStopChan    chan struct{}
	surgePollWg          sync.WaitGroup
	surgePollReader      surgeListReader

	// Startup recovery flags: set once after the first successful tick
	// (aria2c) / reconcileSurgeCache (Surge) round. Used for recovery
	// completion logging and fast-retry interval before first success.
	aria2Recovered         atomic.Bool
	surgeRecovered         atomic.Bool
	recoveryLogged         sync.Once
	aria2UnavailableLogged atomic.Bool
}

func New(app *application.App, hub *events.Hub, systray *application.SystemTray, engine rpc.DownloadEngine) *Monitor {
	m := &Monitor{
		app:                   app,
		hub:                   hub,
		systray:               systray,
		engine:                engine,
		stopChan:              make(chan struct{}),
		forceTickChan:         make(chan struct{}, 1),
		headlessInterval:      5 * time.Second, // 无头模式：5秒
		windowInterval:        1 * time.Second, // 窗口模式：1秒（由前端主导）
		shouldFetchStopped:    true,            // 初始时获取一次 stopped 任务
		lastStoppedFetchTime:  time.Now(),
		deletedGids:           make(map[string]time.Time),
		prevActiveGids:        make(map[string]bool),
		prevWaitingGids:       make(map[string]bool),
		telemetry:             NewTelemetryCache(),
		pauseResumeIntentions: make(map[string]string),
		surgePollInterval:     10 * time.Second,
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
	return &Monitor{
		hub:                   hub,
		pusher:                NewPusher(hub),
		deletedGids:           make(map[string]time.Time),
		pauseResumeIntentions: make(map[string]string),
	}
}

// Intention strings recorded by BumpPauseResumeIntention and checked by
// shouldDiscardStalePause. Shared across packages to avoid stringly-typed drift.
const (
	PauseResumeIntentionPause  = "pause"
	PauseResumeIntentionResume = "resume"
)

func (m *Monitor) BumpPauseResumeIntention(gid string, action string) {
	if m == nil || gid == "" {
		return
	}
	m.pauseResumeVersionMu.Lock()
	defer m.pauseResumeVersionMu.Unlock()
	if m.pauseResumeIntentions == nil {
		m.pauseResumeIntentions = make(map[string]string)
	}
	m.pauseResumeIntentions[gid] = action
}

func (m *Monitor) shouldDiscardStalePause(gid string) bool {
	if m == nil || gid == "" {
		return false
	}
	m.pauseResumeVersionMu.RLock()
	defer m.pauseResumeVersionMu.RUnlock()
	if m.pauseResumeIntentions == nil {
		return false
	}
	intention, exists := m.pauseResumeIntentions[gid]
	if !exists {
		return false
	}
	return intention == PauseResumeIntentionResume
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
		m.convergence = smartthread.NewConvergenceTicker(he, adapter, m.telemetry, adapter, adapter)
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
		m.surgePollWg.Add(1)
		go func() {
			defer m.surgePollWg.Done()
			m.surgePollLoop()
		}()
	}

	go m.runLoop()
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
	m.stopOnce.Do(func() {
		close(m.stopChan)
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

// handleTaskComplete 处理任务完成
func (m *Monitor) handleTaskComplete(task *TrackedTask) {
	if task == nil {
		return
	}

	// 清理 convergence state（无论后续路径如何，都应清理）
	if m.convergence != nil {
		m.convergence.RemoveTask(task.GID)
	}

	// 如果 FilePath 为空，尝试从元数据缓存获取
	// 场景：快速完成的任务可能在 Tracker 更新前已完成，导致 FilePath 未被填充
	if task.FilePath == "" {
		if meta := Cache.GetMetadata(task.GID); meta != nil && len(meta.Files) > 0 {
			task.FilePath = meta.Files[0]
			task.Dir = meta.Dir
			if meta.SourceURL != "" {
				task.SourceURL = meta.SourceURL
			}
			if task.DownloadGroup == nil && meta.DownloadGroup != nil {
				task.DownloadGroup = copyDownloadGroup(meta.DownloadGroup)
			}
			log.Printf("[Monitor] Recovered file info from cache for task: %s -> %s", task.GID, task.FilePath)
		}
	}
	if task.DownloadGroup == nil {
		task.DownloadGroup = Cache.GetTaskGroup(task.GID)
		if task.DownloadGroup == nil {
			task.DownloadGroup = GetStoredTaskGroup(task.GID)
		}
	}

	// 如果仍然为空，尝试直接调用 RPC 获取
	if task.FilePath == "" {
		if t, err := m.engine.TellStatus(task.GID, nil); err == nil && len(t.Files) > 0 && t.Files[0].Path != "" {
			task.FilePath = t.Files[0].Path
			task.Dir = t.Dir
			if len(t.Files[0].Uris) > 0 {
				task.SourceURL = t.Files[0].Uris[0].Uri
			}
			if task.DownloadGroup == nil && t.DownloadGroup != nil {
				task.DownloadGroup = copyDownloadGroup(t.DownloadGroup)
			}
			log.Printf("[Monitor] Recovered file info from RPC for task: %s -> %s", task.GID, task.FilePath)
		}
	}

	// 1. 记录速度统计（仅 >50MB 文件）— 不依赖 FilePath，先于 history 执行
	if task.TotalLength > speedstats.MinFileSize && task.PeakSpeed > 0 {
		// Fallback chain: PeakThreadCount (convergence-recorded) → ThreadCount (initial) → config
		threadCount := task.PeakThreadCount
		threadSource := "peakThreadCount"
		if threadCount <= 0 {
			threadCount = task.ThreadCount
			threadSource = "threadCount"
		}
		if threadCount <= 0 {
			threadCount, _ = strconv.Atoi(config.Current.MaxConnections)
			if threadCount <= 0 {
				threadCount = 8
			}
			threadSource = "config"
		}
		isExploration := task.IsExploration

		// Fallback: tasks that bypassed service_add.go (external RPC, resume after restart)
		// have empty Domain/Scope. Classify from SourceURL to prevent polluting the wan pool.
		if task.Domain == "" && task.SourceURL != "" {
			scope, domain := scopeClassifier.ClassifyByURL(task.SourceURL)
			task.Scope = scope
			task.Domain = domain
			// PeakEnvKey stays empty — no netenv participation in fallback path.
		}

		// isExploration 直接从 tracker 记录取值，不再重新计算或减半

		// Skip recording if envKey is empty — external RPC/wake-up tasks have no
		// envKey and would pollute env-aware history buckets.
		if task.PeakEnvKey == "" {
			log.Printf("[Monitor] Skipping speed stats for task %s: no envKey (external RPC or wake-up path)", task.GID)
		} else if task.Domain == "" {
			// Skip recording if we still have no domain — a record without domain is useless
			// for BBR (GetDomainPeak/GetRTprop can't match) and would only pollute V_global_peak.
			log.Printf("[Monitor] Skipping speed stats for task %s: no domain/URL available", task.GID)
		} else if he, ok := m.engine.(*rpc.HybridEngine); ok {
			if _, rateLimited := he.GetRateLimit(task.GID); rateLimited {
				// Rate limit guard: skip AddRecordV2 to avoid polluting speedstats with
				// rate-limited throughput (which doesn't reflect server capacity for BBR).
				log.Printf("[Monitor] Skipping speed stats for task %s: rate-limited (would pollute BBR)", task.GID)
			} else {
				speedstats.AddRecordV2(task.PeakSpeed, threadCount, task.TotalLength, isExploration, task.TTFBMs, task.Domain, task.Scope, task.PeakEnvKey)
				log.Printf("[Monitor] Speed stats recorded: peak=%d, threads=%d (source=%s), exploration=%v, scope=%s, envKey=%s, domain=%s, ttfb=%d",
					task.PeakSpeed, threadCount, threadSource, isExploration, task.Scope, task.PeakEnvKey, task.Domain, task.TTFBMs)
			}
		} else {
			speedstats.AddRecordV2(task.PeakSpeed, threadCount, task.TotalLength, isExploration, task.TTFBMs, task.Domain, task.Scope, task.PeakEnvKey)
			log.Printf("[Monitor] Speed stats recorded: peak=%d, threads=%d (source=%s), exploration=%v, scope=%s, envKey=%s, domain=%s, ttfb=%d",
				task.PeakSpeed, threadCount, threadSource, isExploration, task.Scope, task.PeakEnvKey, task.Domain, task.TTFBMs)
		}
	}

	if task.FilePath == "" {
		log.Printf("[Monitor] Task %s completed but no file path available, skipping history", task.GID)
		return
	}

	log.Printf("[Monitor] Task completed: %s, peak speed: %d B/s", task.GID, task.PeakSpeed)

	// 2. 写入历史记录
	history.Add(history.HistoryEntry{
		GID:             task.GID,
		Title:           filepath.Base(task.FilePath),
		Dir:             task.Dir,
		Path:            task.FilePath,
		TotalLength:     fmt.Sprintf("%d", task.TotalLength),
		CompletedLength: fmt.Sprintf("%d", task.CompletedLength),
		Source:          task.SourceURL,
		DownloadGroup:   copyDownloadGroup(task.DownloadGroup),
	})
	if task.DownloadGroup != nil {
		RemoveTaskGroup(task.GID)
		QueueDownloadGroupName(task.DownloadGroup.ID)
	}

	log.Printf("[Monitor] History recorded: %s", task.GID)
}

// InvalidateTask 使指定任务的缓存失效并发送删除事件
// 用于 RemoveTask 后确保前端和缓存同步
func (m *Monitor) InvalidateTask(gid string) {
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

	// 3. 发送删除事件通知前端
	if m.hub != nil {
		m.hub.EmitTaskDelta(events.TaskDelta{
			Type: "remove",
			GID:  gid,
		})
	}

	// 4. 清理遥测缓存
	if m.telemetry != nil {
		m.telemetry.Remove(gid)
	}

	// 5. 清理 convergence state
	if m.convergence != nil {
		m.convergence.RemoveTask(gid)
	}

	// 6. 清理 pause/resume intention，避免长期运行内存泄漏
	m.pauseResumeVersionMu.Lock()
	if m.pauseResumeIntentions != nil {
		delete(m.pauseResumeIntentions, gid)
	}
	m.pauseResumeVersionMu.Unlock()

	log.Printf("[Monitor] Task invalidated: %s", gid)
}

// EmitTaskMoveForGroupOp emits a task:move event for a group pause/resume
// operation on a Surge GID. Called by downloadgroups.pauseResumeDownloadGroup
// after Cache.MoveTaskTo* so the frontend list migration synchronizes with
// Cache immediately, without waiting for the Surge event path's EmitTaskMove
// (which is an idempotent no-op if the task has already moved).
func (m *Monitor) EmitTaskMoveForGroupOp(gid, from, to string) {
	if m == nil || m.hub == nil || gid == "" {
		return
	}
	task := findTaskInCache(gid)
	if task == nil {
		return
	}
	m.hub.EmitTaskMove(events.TaskMove{
		GID:  gid,
		From: from,
		To:   to,
		Task: *task,
	})
}

// PushRemoveDelta queues a remove delta for a group delete operation on a
// Surge GID. Called by downloadgroups.RemoveDownloadGroup after
// Cache.RemoveTask so the frontend removes the task immediately, without
// waiting for the Surge event path's remove delta (idempotent if the task
// is already removed from frontend state).
func (m *Monitor) PushRemoveDelta(gid string) {
	if m == nil || m.pusher == nil || gid == "" {
		return
	}
	m.pusher.Queue(events.TaskDelta{Type: "remove", GID: gid})
	m.pusher.FlushNow()
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

// trackerAdapter wraps TaskTracker to satisfy smartthread.TrackerProvider,
// PeakEfficiencyRecorder, and RateLimitChecker interfaces.
type trackerAdapter struct {
	*TaskTracker
	engine *rpc.HybridEngine
}

func (a *trackerAdapter) GetActiveTrackedTasks() []smartthread.TrackedTaskInfo {
	tasks := a.TaskTracker.GetActiveTrackedTasks()
	result := make([]smartthread.TrackedTaskInfo, len(tasks))
	for i, t := range tasks {
		result[i] = smartthread.TrackedTaskInfo{
			GID:             t.GID,
			Status:          t.Status,
			Scope:           t.Scope,
			Domain:          t.Domain,
			EnvKey:          t.CurrentEnvKey,
			IsKeepAlive:     t.IsKeepAlive,
			CompletedLength: t.CompletedLength,
			MinChunk:        t.MinChunk,
			TotalLength:     t.TotalLength,
		}
	}
	return result
}

// GetRateLimit implements RateLimitChecker by delegating to the HybridEngine.
func (a *trackerAdapter) GetRateLimit(gid string) (int64, bool) {
	if a.engine == nil {
		return 0, false
	}
	return a.engine.GetRateLimit(gid)
}
