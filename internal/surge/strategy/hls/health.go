package hls

import (
	"context"
	"math"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// hlsSlowWorkerMinBytes is the minimum session bytes for the relative
// slow-worker comparison — a worker that has not yet moved a megabyte of
// media has no meaningful EMA to judge.
const hlsSlowWorkerMinBytes = utils.MiB

// startHealth spawns the resident watchdog for the duration of the run.
// The health ctx derives from the run's parent so Pause/Cancel detach the
// monitor together with the engine, and it is always joined before the
// window/file close.
func (d *HlsDownloader) startHealth(ctx context.Context, e *segmentEngine, prog *progress.DownloadProgress) {
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	d.ctrlMu.Lock()
	d.healthCancel = cancel
	d.healthDone = done
	d.ctrlMu.Unlock()
	go func() {
		defer close(done)
		d.runHealthMonitor(hctx, e, prog)
	}()
}

// joinHealth cancels and joins the watchdog — idempotent, safe before
// start and after detach.
func (d *HlsDownloader) joinHealth() {
	d.ctrlMu.Lock()
	cancel := d.healthCancel
	done := d.healthDone
	d.healthCancel = nil
	d.healthDone = nil
	d.ctrlMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// runHealthMonitor ticks once immediately, then every
// HealthCheckInterval: publish worker stats, cancel stalled network-phase
// attempts, run the relative slow-worker comparison, and wake the control
// loop so drain-to-zero respawns never wait a full tick.
func (d *HlsDownloader) runHealthMonitor(ctx context.Context, e *segmentEngine, prog *progress.DownloadProgress) {
	d.healthTick(e, prog, time.Now())
	ticker := time.NewTicker(types.HealthCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.healthTick(e, prog, time.Now())
		}
	}
}

func (d *HlsDownloader) healthTick(e *segmentEngine, prog *progress.DownloadProgress, now time.Time) {
	snaps := e.snapshotWorkers(now)
	if prog != nil {
		prog.SetWorkerStats(snaps)
	}
	if stall := d.runCfg.GetStallTimeout(); stall > 0 {
		for _, v := range e.sessionViews() {
			if v.AttemptGen == 0 || v.Phase != hlsPhaseNetwork || v.LastActivity <= 0 {
				continue
			}
			if now.Sub(time.Unix(0, v.LastActivity)) >= stall {
				e.cancelAttemptStall(v.ID, v.AttemptGen)
			}
		}
	}
	d.checkSlowWorkers(e, prog, now, snaps)
	d.wake()
}

// checkSlowWorkers is the minimal HLS adaptation of the concurrent
// relative slow-worker rule: only network-phase, non-draining sessions past
// the grace period with enough media bytes participate; kill (free requeue)
// replaces the byte-range worker teardown, and waiting-on-limiter workers
// are excluded via the phase gate. A single participant falls back to the
// runtime-default threshold against the session-wide speed, mirroring the
// concurrent tail rule.
func (d *HlsDownloader) checkSlowWorkers(e *segmentEngine, prog *progress.DownloadProgress, now time.Time, snaps []types.WorkerSnapshot) {
	views := e.sessionViews()
	if len(views) == 0 {
		return
	}
	byID := make(map[int]*types.WorkerSnapshot, len(snaps))
	for i := range snaps {
		byID[snaps[i].WorkerID] = &snaps[i]
	}
	grace := d.runCfg.GetSlowWorkerGracePeriod()
	var candIDs []int
	var speeds []float64
	total := 0.0
	for _, v := range views {
		if v.AttemptGen == 0 || v.Draining || v.Phase != hlsPhaseNetwork {
			continue
		}
		s := byID[v.ID]
		if s == nil {
			continue
		}
		if s.WaitingOnLimiter {
			continue
		}
		if now.Sub(time.Unix(0, s.WorkerStartUnix)) < grace {
			continue
		}
		if s.SessionBytes < int64(hlsSlowWorkerMinBytes) {
			continue
		}
		if s.EMASpeed <= 0 {
			continue
		}
		candIDs = append(candIDs, v.ID)
		speeds = append(speeds, s.EMASpeed)
		total += s.EMASpeed
	}
	if len(candIDs) == 0 {
		return
	}
	mean := total / float64(len(candIDs))
	if len(candIDs) < 2 && prog != nil {
		downloaded, _, _, sessionElapsed, _, sessionStart := prog.GetProgress()
		if sec := sessionElapsed.Seconds(); sec > 5 {
			if g := float64(downloaded-sessionStart) / sec; g > 0 {
				mean = g
			}
		}
	}
	threshold := d.slowThresholdOrDefault(d.runCfg.GetSlowWorkerThreshold())
	if len(candIDs) == 1 && threshold == 0 {
		threshold = d.runCfg.GetSlowWorkerThreshold()
	}
	if threshold <= 0 {
		return
	}
	for i, id := range candIDs {
		if speeds[i] < threshold*mean {
			e.cancelAttemptKill(id)
		}
	}
}

// slowThresholdOrDefault returns the live override when set, else def.
func (d *HlsDownloader) slowThresholdOrDefault(def float64) float64 {
	d.slowMu.Lock()
	defer d.slowMu.Unlock()
	if d.slowSet {
		return d.slowOverride
	}
	return def
}

// ScaleWorkers grows or drains the pool by delta. Positive deltas spawn up
// to the run cap and the feedable work bound; negative deltas mark the
// slowest non-draining workers draining while keeping at least one alive.
// Post-detach calls return 0.
func (d *HlsDownloader) ScaleWorkers(delta int) int {
	if delta == 0 {
		return 0
	}
	e := d.engineForControl()
	if e == nil {
		return 0
	}
	if delta > 0 {
		added := 0
		for range delta {
			if e.queue.remaining() <= e.lifecycleView().workers {
				break
			}
			if !e.spawnWorker() {
				break
			}
			added++
		}
		return added
	}
	// Scale down: slowest first, excluding already-draining workers and
	// keeping at least one non-draining worker alive.
	need := -delta
	snaps := e.snapshotWorkers(time.Now())
	views := e.sessionViews()
	draining := make(map[int]bool, len(views))
	for _, v := range views {
		if v.Draining {
			draining[v.ID] = true
		}
	}
	eligible := make([]types.WorkerSnapshot, 0, len(snaps))
	for _, s := range snaps {
		if !draining[s.WorkerID] {
			eligible = append(eligible, s)
		}
	}
	if len(eligible) <= 1 {
		return 0
	}
	sortSnapshotsBySpeed(eligible)
	count := 0
	for _, s := range eligible {
		if count >= need || len(eligible)-count <= 1 {
			break
		}
		if e.markDraining(s.WorkerID) {
			count++
		}
	}
	return -count
}

// sortSnapshotsBySpeed orders ascending by EMA speed with worker ID as the
// stable tie-break for drain selection.
func sortSnapshotsBySpeed(snaps []types.WorkerSnapshot) {
	for i := 1; i < len(snaps); i++ {
		for j := i; j > 0; j-- {
			a, b := snaps[j-1], snaps[j]
			if a.EMASpeed < b.EMASpeed ||
				(a.EMASpeed == b.EMASpeed && a.WorkerID <= b.WorkerID) {
				break
			}
			snaps[j-1], snaps[j] = b, a
		}
	}
}

// KillWorker cancels the worker's current interruptible attempt — the free
// requeue keeps the segment uncharged. Between attempts there is nothing
// to kill; false is correct. Post-detach calls return false.
func (d *HlsDownloader) KillWorker(id int) bool {
	e := d.engineForControl()
	if e == nil {
		return false
	}
	return e.cancelAttemptKill(id)
}

// DrainWorker marks a worker draining (it exits after its current attempt
// settles). The last non-draining worker is refused so the claim can never
// drain itself empty — the single-flight verification worker included.
// Post-detach calls return false.
func (d *HlsDownloader) DrainWorker(id int) bool {
	e := d.engineForControl()
	if e == nil {
		return false
	}
	// Refuse when this would drain the last non-draining worker.
	nonDraining := 0
	target := -1
	for _, v := range e.sessionViews() {
		if !v.Draining {
			nonDraining++
			if v.ID == id {
				target = id
			}
		}
	}
	if target < 0 {
		return false // already draining or gone
	}
	if nonDraining <= 1 {
		return false
	}
	return e.markDraining(id)
}

// SetSlowWorkerThreshold stores a finite [0,1] override — 0 disables the
// multi-worker relative check while the single-worker tail rule still
// falls back to the runtime default. Non-finite or out-of-range values are
// ignored.
func (d *HlsDownloader) SetSlowWorkerThreshold(v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
		return
	}
	d.slowMu.Lock()
	d.slowOverride = v
	d.slowSet = true
	d.slowMu.Unlock()
}
