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

	mu       sync.Mutex
	states   map[string]*convergenceState
	stopChan chan struct{}
	stopOnce sync.Once
}

func NewConvergenceTicker(engine *rpc.HybridEngine, tracker TrackerProvider, telemetry TelemetryProvider) *ConvergenceTicker {
	return &ConvergenceTicker{
		engine:    engine,
		tracker:   tracker,
		telemetry: telemetry,
		limits:    GetDefaultServerLimits(),
		states:    make(map[string]*convergenceState),
		stopChan:  make(chan struct{}),
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
	activeGids := make(map[string]bool)
	var pending []pendingScale

	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		activeGids[task.GID] = true
		if ps, ok := c.processTask(task); ok {
			pending = append(pending, ps)
		}
	}

	// Self-cleanup: remove states for GIDs no longer active
	c.mu.Lock()
	for gid := range c.states {
		if !activeGids[gid] {
			delete(c.states, gid)
		}
	}
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
	for _, ws := range stats {
		aggregateSpeed += ws.EMASpeed
	}
	currentWorkers := len(stats)

	vThreadAvg, ok := speedstats.GetRecentPeakByScope(task.Scope)
	if !ok || vThreadAvg <= 0 {
		return pendingScale{}, false
	}

	expectedThroughput := float64(vThreadAvg) * float64(currentWorkers)
	if expectedThroughput <= 0 {
		return pendingScale{}, false
	}

	throughputRatio := aggregateSpeed / expectedThroughput

	_, domain, _ := c.tracker.GetScope(gid)

	nMax, hasLimit := c.limits.GetNMax(domain)
	if hasLimit && currentWorkers >= nMax {
		c.mu.Lock()
		s := c.getOrCreateState(gid)
		s.scaleUpCycles = 0
		c.mu.Unlock()
		return pendingScale{}, false
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
		if s.scaleUpCycles >= scaleUpStableCycles && task.IsKeepAlive {
			result = pendingScale{gid: gid, delta: 1}
			hasResult = true
			log.Printf("[convergence] scale-up: gid=%s workers=%d ratio=%.2f keepAlive=true",
					gid, currentWorkers, throughputRatio)
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
	c.mu.Unlock()
}
