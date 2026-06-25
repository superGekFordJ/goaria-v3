package smartthread

import (
	"log"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

// TrackedTaskInfo is a minimal view of monitor.TrackedTask for convergence.
type TrackedTaskInfo struct {
	GID         string
	Status      string
	Scope       string
	Domain      string
	IsKeepAlive bool
}

// TrackerProvider provides task tracking data to the convergence ticker.
type TrackerProvider interface {
	GetActiveTrackedTasks() []TrackedTaskInfo
	GetScope(gid string) (scope, domain string, ok bool)
}

// TelemetryProvider provides per-worker telemetry snapshots.
type TelemetryProvider interface {
	Get(gid string) []types.WorkerSnapshot
}

type convergenceState struct {
	scaleDownCycles int
	scaleUpCycles   int
	releaseCycles   int
}

type ConvergenceTicker struct {
	engine    *rpc.HybridEngine
	tracker   TrackerProvider
	telemetry TelemetryProvider
	limits    *ServerLimitStore

	mu             sync.Mutex
	states         map[string]*convergenceState
	prevActiveGids map[string]string // gid → scope, for bandwidth borrowing diff
	stopChan       chan struct{}
	stopOnce       sync.Once
}

func NewConvergenceTicker(engine *rpc.HybridEngine, tracker TrackerProvider, telemetry TelemetryProvider) *ConvergenceTicker {
	return &ConvergenceTicker{
		engine:         engine,
		tracker:        tracker,
		telemetry:      telemetry,
		limits:         GetDefaultServerLimits(),
		states:         make(map[string]*convergenceState),
		prevActiveGids: make(map[string]string),
		stopChan:       make(chan struct{}),
	}
}

func (c *ConvergenceTicker) Start() {
	go c.run()
}

func (c *ConvergenceTicker) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopChan)
	})
}

func (c *ConvergenceTicker) run() {
	ticker := time.NewTicker(c.currentInterval())
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.tick()
			ticker.Reset(c.currentInterval())
		}
	}
}

func (c *ConvergenceTicker) currentInterval() time.Duration {
	if config.Current != nil && config.Current.ConvergenceInterval > 0 {
		return time.Duration(config.Current.ConvergenceInterval) * time.Second
	}
	return convergenceInterval
}

// pendingScale collects batched scale operations to execute after all tasks are processed.
type pendingScale struct {
	gid   string
	delta int
}

func (c *ConvergenceTicker) tick() {
	if c.engine == nil || c.tracker == nil || c.telemetry == nil {
		return
	}

	activeTasks := c.tracker.GetActiveTrackedTasks()
	activeGids := make(map[string]string) // gid → scope
	var pending []pendingScale
	pendingGids := make(map[string]bool) // GIDs already scaled this tick

	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		activeGids[task.GID] = task.Scope
		if ps, ok := c.processTask(task); ok {
			pending = append(pending, ps)
			pendingGids[ps.gid] = true
		}
	}

	// Bandwidth borrowing: detect tasks that disappeared since last tick.
	// For each completed task, find same-scope keep-alive tasks and trigger
	// ScaleUp with degraded filter (bandwidthReleaseCycles = 1).
	// Skip GIDs already scaled by processTask to prevent double ScaleUp.
	c.mu.Lock()
	for gid, scope := range c.prevActiveGids {
		if activeGids[gid] != "" {
			continue // still active
		}
		// Task disappeared — look for same-scope keep-alive tasks to benefit
		for _, t := range activeTasks {
			if !strings.HasPrefix(t.GID, "sg_") || !t.IsKeepAlive {
				continue
			}
			if t.Scope != scope {
				continue
			}
			if pendingGids[t.GID] {
				continue // already scaled by processTask this tick
			}
			s := c.getOrCreateState(t.GID)
			s.releaseCycles++
			if s.releaseCycles >= bandwidthReleaseCycles {
				pending = append(pending, pendingScale{gid: t.GID, delta: 1})
				pendingGids[t.GID] = true
				log.Printf("[convergence] bandwidth-borrow: gid=%s scope=%s released by completed gid=%s",
					t.GID, scope, gid)
				s.releaseCycles = 0
				s.scaleUpCycles = 0
			}
		}
	}
	c.mu.Unlock()

	// Self-cleanup: remove states for GIDs no longer active
	c.mu.Lock()
	for gid := range c.states {
		if activeGids[gid] == "" {
			delete(c.states, gid)
		}
	}
	// Update prevActiveGids for next tick's bandwidth borrowing diff
	c.prevActiveGids = activeGids
	c.mu.Unlock()

	// Batch: execute all pending scale operations after processing all tasks
	for _, ps := range pending {
		c.engine.ScaleWorkers(ps.gid, ps.delta)
	}
}

// processTask evaluates a single task and returns a pending scale operation if one is needed.
func (c *ConvergenceTicker) processTask(task TrackedTaskInfo) (pendingScale, bool) {
	gid := task.GID

	stats := c.telemetry.Get(gid)
	if stats == nil || len(stats) == 0 {
		return pendingScale{}, false
	}

	var aggregateSpeed float64
	var retryCountSum int32
	for _, ws := range stats {
		aggregateSpeed += ws.EMASpeed
		retryCountSum += ws.RetryCount
	}
	currentWorkers := len(stats)

	// Domain-isolated V_thread_avg with 0.5x scope fallback.
	// Empty domain → GetRecentPeakByDomain returns false → falls back to scope
	// with 0.5x penalty (more conservative than pre-hotfix behavior, but safer).
	vThreadAvg, ok := speedstats.GetRecentPeakByDomain(task.Domain, task.Scope)
	if !ok {
		vThreadAvg, ok = speedstats.GetRecentPeakByScope(task.Scope)
		if ok {
			vThreadAvg = vThreadAvg / 2
		}
	}
	if !ok || vThreadAvg <= 0 {
		return pendingScale{}, false
	}

	// Clamp to minThreadEfficiency to prevent degenerate expectedThroughput
	// after 0.5x penalty on a already-low scope median.
	if vThreadAvg < minThreadEfficiency {
		vThreadAvg = minThreadEfficiency
	}

	expectedThroughput := float64(vThreadAvg) * float64(currentWorkers)
	if expectedThroughput <= 0 {
		return pendingScale{}, false
	}

	throughputRatio := aggregateSpeed / expectedThroughput

	_, domain, _ := c.tracker.GetScope(gid)

	// C1: Server connection hard-limit fuse — detect conn errors and lock N_max.
	// Uses RetryCount sum from telemetry (per-worker current chunk retries) rather than
	// ProgressState.consecutiveConnErrors, which is maintained as a backup mechanism in the
	// engine but not read here. RetryCount directly reflects concurrent server rejections.
	if retryCountSum >= int32(connErrorThreshold) {
		if _, hasLimit := c.limits.GetNMax(domain); !hasLimit {
			c.limits.SetNMax(domain, currentWorkers)
			log.Printf("[convergence] server-limit-fuse: domain=%s N_max=%d locked (retryCountSum=%d)",
				domain, currentWorkers, retryCountSum)
		}
	}

	nMax, hasLimit := c.limits.GetNMax(domain)
	if hasLimit && currentWorkers >= nMax {
		c.mu.Lock()
		s := c.getOrCreateState(gid)
		s.scaleUpCycles = 0
		c.mu.Unlock()
		return pendingScale{}, false
	}

	// M1: V_available check — only ScaleUp if global bandwidth has room.
	vAvailable := false
	if globalPeak, ok := speedstats.GetGlobalPeak(task.Scope); ok && globalPeak > 0 {
		activeBw := activeBandwidthProvider(task.Scope)
		vAvailable = globalPeak-activeBw >= vThreadAvg
	} else {
		vAvailable = true // no data yet — don't block ScaleUp
	}

	c.mu.Lock()
	s := c.getOrCreateState(gid)

	var result pendingScale
	hasResult := false

	switch {
	case throughputRatio < throughputFloorRatio:
		s.scaleDownCycles++
		s.scaleUpCycles = 0
		if s.scaleDownCycles >= scaleDownStableCycles {
			if currentWorkers > 1 {
				result = pendingScale{gid: gid, delta: -1}
				hasResult = true
				log.Printf("[convergence] scale-down: gid=%s workers=%d ratio=%.2f releaseCycles=%d",
					gid, currentWorkers, throughputRatio, s.releaseCycles+1)
			}
			s.scaleDownCycles = 0
			s.releaseCycles++
		}

	case throughputRatio >= throughputStableRatio:
		s.scaleUpCycles++
		s.scaleDownCycles = 0
		if s.scaleUpCycles >= scaleUpStableCycles && task.IsKeepAlive && vAvailable {
			result = pendingScale{gid: gid, delta: 1}
			hasResult = true
			log.Printf("[convergence] scale-up: gid=%s workers=%d ratio=%.2f keepAlive=true vAvailable=%v",
				gid, currentWorkers, throughputRatio, vAvailable)
			s.scaleUpCycles = 0
		}

	default:
		s.scaleDownCycles = 0
		s.scaleUpCycles = 0
	}

	c.mu.Unlock()
	return result, hasResult
}

func (c *ConvergenceTicker) getOrCreateState(gid string) *convergenceState {
	s, ok := c.states[gid]
	if !ok {
		s = &convergenceState{}
		c.states[gid] = s
	}
	return s
}

func (c *ConvergenceTicker) RemoveTask(gid string) {
	c.mu.Lock()
	delete(c.states, gid)
	delete(c.prevActiveGids, gid)
	c.mu.Unlock()
}
