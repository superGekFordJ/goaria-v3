package monitor

import (
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/utils"
)

// CDN throttle fingerprint tuning knobs.
const (
	cdnDetectorInterval = 1 * time.Second
	cdnHistoryWindow    = 10 * time.Second
	cdnWarmupGrace      = 5 * time.Second
	cdnWarmupBytes      = 1 * utils.MiB
	cdnSustainDuration  = 5 * time.Second
	cdnSteadyCoV        = 0.05
	cdnThrottleRelRatio = 0.5
	cdnHealthyPeerFloor = 1 * utils.MiB // healthy peers must be clearly faster than this
	cdnDeadFloorBps     = 2 * utils.KiB // ~0才算死，避免误杀低天花板下的公平分享者
	cdnWorkerEvictTicks = 3
	cdnActionCooldown   = 10 * time.Second
	cdnMaxKillCount     = 3 // after 3 kills without improvement, drain instead of kill
)

// cdnSurgeControl is the per-download control surface the detector drives.
// *rpc.SurgeEngine satisfies this; tests inject a mock.
type cdnSurgeControl interface {
	GetWorkerStats(gid string) []types.WorkerSnapshot
	KillWorker(gid string, workerID int) bool
	DrainWorker(gid string, workerID int) bool
	SetSlowWorkerThreshold(gid string, v float64)
}

// cdnWorkerState tracks per-(gid, worker) debounce/cooldown state.
type cdnWorkerState struct {
	heldSince   time.Time // when the current kill-verdict started being held
	lastKill    time.Time // for cdnActionCooldown
	absentTicks int       // consecutive ticks absent from stats (grace before prune)
	killCount   int       // consecutive kills without speed improvement
}

// CDNDetector is a self-contained 1s ticker that samples per-worker telemetry,
// runs a priority decision tree, takes over the engine's relative slow-speed
// cancel (threshold→0), and hard-kills individual CDN-throttled/dead workers.
// It does NOT detect global congestion (left to the macro convergence layer)
// and does NOT touch N_max or worker counts.
type CDNDetector struct {
	control       cdnSurgeControl
	history       *WorkerHistory
	getActiveGids func() []string // returns sg_-prefixed active gids

	interval time.Duration
	now      func() time.Time

	mu          sync.Mutex
	workerState map[string]map[int]*cdnWorkerState

	stopChan chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// NewCDNDetector builds a detector. windowCap is the per-worker ring size
// (defaults to cdnHistoryWindow/cdnDetectorInterval).
func NewCDNDetector(control cdnSurgeControl, history *WorkerHistory, getActiveGids func() []string) *CDNDetector {
	windowCap := int(cdnHistoryWindow / cdnDetectorInterval)
	if windowCap < 1 {
		windowCap = 1
	}
	if history == nil {
		history = NewWorkerHistory(windowCap)
	}
	return &CDNDetector{
		control:       control,
		history:       history,
		getActiveGids: getActiveGids,
		interval:      cdnDetectorInterval,
		now:           time.Now,
		workerState:   make(map[string]map[int]*cdnWorkerState),
	}
}

// Start launches the detector goroutine. It is safe to call once per instance.
func (d *CDNDetector) Start() {
	d.done = make(chan struct{})
	d.stopChan = make(chan struct{})
	go d.loop()
}

// Stop signals the detector goroutine and waits for it to exit.
// Safe to call concurrently; the close is guarded by sync.Once.
func (d *CDNDetector) Stop() {
	if d.stopChan == nil {
		return
	}
	d.stopOnce.Do(func() { close(d.stopChan) })
	<-d.done
}

func (d *CDNDetector) loop() {
	defer close(d.done)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopChan:
			return
		case <-ticker.C:
			d.tick()
		}
	}
}

// tick samples active gids, records history, and runs the decision tree.
func (d *CDNDetector) tick() {
	now := d.now()
	activeGids := d.getActiveGids()
	presentGids := make(map[string]bool, len(activeGids))
	for _, gid := range activeGids {
		rawGid, ok := stripSurgePrefix(gid)
		if !ok {
			continue
		}
		presentGids[gid] = true
		stats := d.control.GetWorkerStats(rawGid)
		if len(stats) == 0 {
			continue
		}
		d.history.Observe(gid, stats, now)
		presentIDs := make(map[int]bool, len(stats))
		for _, s := range stats {
			presentIDs[s.WorkerID] = true
		}
		d.history.EvictAbsent(gid, presentIDs, cdnWorkerEvictTicks)
		// Prune stale per-worker state, mirroring EvictAbsent's grace: a worker
		// absent for more than cdnWorkerEvictTicks consecutive ticks is removed,
		// so a single-absent-still-alive worker only resets its sustain/cooldown.
		d.mu.Lock()
		if gids, ok := d.workerState[gid]; ok {
			for wid, ws := range gids {
				if presentIDs[wid] {
					ws.absentTicks = 0
					continue
				}
				ws.absentTicks++
				if ws.absentTicks > cdnWorkerEvictTicks {
					delete(gids, wid)
				}
			}
			if len(gids) == 0 {
				delete(d.workerState, gid)
			}
		}
		d.mu.Unlock()
		d.evaluateGid(gid, rawGid, stats, now)
	}

	// Drop history/state for gids that are no longer active.
	for _, gid := range d.history.ActiveGIDs() {
		if !presentGids[gid] {
			d.history.RemoveGID(gid)
			d.mu.Lock()
			delete(d.workerState, gid)
			d.mu.Unlock()
		}
	}
}

// evaluateGid runs the priority decision tree for one download.
func (d *CDNDetector) evaluateGid(gid, rawGid string, stats []types.WorkerSnapshot, now time.Time) {
	// currentWorkers <= 1: killing the only worker loses current chunk progress.
	if len(stats) <= 1 {
		return
	}

	// Threshold takeover: disable the engine's relative-slow cancel so the
	// detector owns slow-worker disposal. Re-issued every tick (idempotent
	// atomic store) so a fresh downloader recreated after pause/resume is
	// re-armed even if the detector never observed the gap.
	d.control.SetSlowWorkerThreshold(rawGid, 0)

	// Deterministic iteration order.
	ordered := make([]types.WorkerSnapshot, len(stats))
	copy(ordered, stats)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].WorkerID < ordered[j].WorkerID })

	for _, s := range ordered {
		wid := s.WorkerID
		// P1 warmup exemption.
		if d.isWarmup(s, now) {
			d.resetHeld(gid, wid)
			continue
		}
		// P2 poison guard: 4xx (auth/WAF) — reconnect just re-hits the wall.
		if isPoison(s.HTTPStatus) {
			d.resetHeld(gid, wid)
			continue
		}
		// Healthy-peer floor: the unified self-contained guard against killing
		// during global slowdown / all-dead / single-worker scenarios.
		median := d.healthyPeerMedian(ordered, wid, now)
		if median <= cdnHealthyPeerFloor {
			d.resetHeld(gid, wid)
			continue
		}

		win := d.history.Window(gid, wid)
		reason := d.classify(win, s, median)
		if reason == "" {
			d.resetHeld(gid, wid)
			d.resetKillCount(gid, wid)
			continue
		}

		// Debounce (sustain) + cooldown.
		if d.shouldFire(gid, wid, now) {
			ws := d.getWorkerStateForFire(gid, wid)
			if ws.killCount >= cdnMaxKillCount {
				if d.control.DrainWorker(rawGid, wid) {
					log.Printf("cdn-throttle-drain: gid=%s worker=%d killCount=%d reason=%s", gid, wid, ws.killCount, reason)
					return
				}
			} else {
				if d.control.KillWorker(rawGid, wid) {
					d.recordKill(gid, wid, now)
					log.Printf("cdn-throttle-kill: gid=%s worker=%d killCount=%d reason=%s", gid, wid, ws.killCount, reason)
					return
				}
			}
		}
		// Verdict holds but not yet sustained/cooled: keep its timer, try next.
	}
}

// classify returns "dead", "throttle", or "" per the priority tree.
// P3 (dead) is checked before P4 (throttle): a near-zero worker that also
// looks steady is treated as dead.
func (d *CDNDetector) classify(win []timedSnapshot, s types.WorkerSnapshot, median float64) string {
	// P3 dead: every window sample below the dead floor (dodged engine 3s stall).
	if allBelowFloor(win, cdnDeadFloorBps) {
		return "dead"
	}
	// P4 throttle: steady (low CoV) AND clearly slower than healthy peers.
	mean, cov := speedMoments(win)
	if mean <= 0 {
		// mean=0 → not steady; P4 does not fire (falls to P3, already checked).
		return ""
	}
	if cov < cdnSteadyCoV && s.EMASpeed < cdnThrottleRelRatio*median {
		return "throttle"
	}
	return ""
}

// isWarmup is the P1 exemption: a young/low-volume/limiter-blocked/hedged
// worker is not yet a reliable signal.
func (d *CDNDetector) isWarmup(s types.WorkerSnapshot, now time.Time) bool {
	if s.WorkerStartUnix > 0 && now.Sub(time.Unix(0, s.WorkerStartUnix)) < cdnWarmupGrace {
		return true
	}
	if s.SessionBytes < cdnWarmupBytes {
		return true
	}
	return s.WaitingOnLimiter || s.Hedged
}

// isPoison reports a 4xx response status (auth/WAF poison — don't reconnect).
func isPoison(status int32) bool {
	return status >= 400 && status < 500
}

// healthyPeerMedian returns the median EMASpeed of healthy peers (non-target,
// non-warmup, with positive speed). Returns 0 when there are none.
func (d *CDNDetector) healthyPeerMedian(stats []types.WorkerSnapshot, targetWid int, now time.Time) float64 {
	speeds := make([]float64, 0, len(stats))
	for _, s := range stats {
		if s.WorkerID == targetWid {
			continue
		}
		if d.isWarmup(s, now) {
			continue
		}
		if s.EMASpeed <= 0 {
			continue
		}
		if isPoison(s.HTTPStatus) {
			continue
		}
		speeds = append(speeds, s.EMASpeed)
	}
	if len(speeds) == 0 {
		return 0
	}
	sort.Float64s(speeds)
	return medianFloat(speeds)
}

// shouldFire reports whether the verdict has been sustained for cdnSustainDuration
// and the per-worker cooldown has expired.
func (d *CDNDetector) shouldFire(gid string, wid int, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws := d.getWorkerStateLocked(gid, wid)
	if ws.heldSince.IsZero() {
		ws.heldSince = now
	}
	if now.Sub(ws.heldSince) < cdnSustainDuration {
		return false
	}
	if !ws.lastKill.IsZero() && now.Sub(ws.lastKill) < cdnActionCooldown {
		return false
	}
	return true
}

// resetHeld clears the sustain timer when the verdict no longer holds.
func (d *CDNDetector) resetHeld(gid string, wid int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws := d.getWorkerStateLocked(gid, wid)
	ws.heldSince = time.Time{}
}

func (d *CDNDetector) resetKillCount(gid string, wid int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws := d.getWorkerStateLocked(gid, wid)
	ws.killCount = 0
}

// getWorkerStateForFire returns the worker state under lock for kill/drain decision.
func (d *CDNDetector) getWorkerStateForFire(gid string, wid int) *cdnWorkerState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.getWorkerStateLocked(gid, wid)
}

func (d *CDNDetector) recordKill(gid string, wid int, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ws := d.getWorkerStateLocked(gid, wid)
	ws.lastKill = now
	ws.heldSince = time.Time{}
	ws.killCount++
}

func (d *CDNDetector) getWorkerStateLocked(gid string, wid int) *cdnWorkerState {
	gids, ok := d.workerState[gid]
	if !ok {
		gids = make(map[int]*cdnWorkerState)
		d.workerState[gid] = gids
	}
	ws, ok := gids[wid]
	if !ok {
		ws = &cdnWorkerState{}
		gids[wid] = ws
	}
	return ws
}

// stripSurgePrefix returns the raw gid for an "sg_"-prefixed gid.
func stripSurgePrefix(gid string) (string, bool) {
	if !strings.HasPrefix(gid, "sg_") {
		return "", false
	}
	return gid[3:], true
}

// allBelowFloor reports whether every window sample's EMASpeed is below floor.
func allBelowFloor(win []timedSnapshot, floor float64) bool {
	if len(win) == 0 {
		return false // no data yet → don't claim dead
	}
	for _, s := range win {
		if s.EMASpeed >= floor {
			return false
		}
	}
	return true
}

// speedMoments returns the mean and coefficient of variation (stddev/mean) of
// EMASpeed over the window.
func speedMoments(win []timedSnapshot) (mean, cov float64) {
	if len(win) == 0 {
		return 0, 0
	}
	var sum float64
	for _, s := range win {
		sum += s.EMASpeed
	}
	mean = sum / float64(len(win))
	if mean <= 0 {
		return 0, 0
	}
	var sq float64
	for _, s := range win {
		diff := s.EMASpeed - mean
		sq += diff * diff
	}
	stddev := math.Sqrt(sq / float64(len(win)))
	return mean, stddev / mean
}

func medianFloat(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
