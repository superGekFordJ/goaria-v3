// Package hls — HlsDownloader is the control-plane orchestrator for one HLS
// VOD lifecycle: bootstrap the prepared media table, mount the .surge
// prefix, fetch/decrypt the init segment, run the first media segment
// single-flight, fan out once, then verify physical completion. It emits
// only EventFirstByte; started/completed/error/removed events belong to the
// scheduler and lifecycle manager. TotalSize stays 0 until physical
// success. One instance runs exactly once.
package hls

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// hlsControlPoll is the control-loop fallback wake; the health watchdog is
// the primary periodic wake source.
const hlsControlPoll = 250 * time.Millisecond

// errIncompletePrefix marks a prefix validation failure at a publication
// point — the durable checkpoint is never advanced past it.
var errIncompletePrefix = errors.New("hls: prefix validation failed")

// HlsDownloader orchestrates a single HLS download run.
type HlsDownloader struct {
	ID           string
	ProgressChan chan<- types.DownloadEvent
	State        *progress.DownloadProgress
	Runtime      *types.RuntimeConfig

	Headers      map[string]string
	Mirrors      []string
	Limiter      types.ByteLimiter
	HLSResume    *types.HLSResumeState
	IsResume     bool
	CreatedAt    int64
	RateLimit    int64
	RateLimitSet bool

	// HostLimiter optionally shares a caller-provided limiter; nil uses the
	// package default. Client is an instance test seam — a real engine gets
	// its own pool lease regardless.
	HostLimiter *transport.HostRateLimiter
	Client      *http.Client

	// TotalSize is 0 until physical completion is proven.
	TotalSize int64

	runOnce atomic.Bool

	ctrlMu     sync.Mutex
	closing    bool
	engine     *segmentEngine
	window     *ReorderWindow
	out        *os.File
	maxWorkers int
	runCfg     types.RuntimeConfig

	wakeCh chan struct{}

	firstSegOnce     sync.Once
	firstPendingIdx  int
	resumeBaseline   int64 // verified bytes provable before this run's init
	firstByteEmitted atomic.Bool

	slowMu       sync.Mutex
	slowOverride float64
	slowSet      bool

	durableMu sync.Mutex
	durable   *types.HLSResumeState

	manifestURL string
	destPath    string
	surgePath   string
	taskHeaders map[string]string // run-scoped clone — the caller map is never handed to workers
	runProg     *progress.DownloadProgress

	healthCancel context.CancelFunc
	healthDone   chan struct{}
}

// NewHlsDownloader constructs an instance; Run performs the lifecycle.
func NewHlsDownloader(id string, progressChan chan<- types.DownloadEvent, state *progress.DownloadProgress, runtime *types.RuntimeConfig) *HlsDownloader {
	return &HlsDownloader{
		ID:           id,
		ProgressChan: progressChan,
		State:        state,
		Runtime:      runtime,
		wakeCh:       make(chan struct{}, 1),
	}
}

// Run executes the download. manifestURL is the task identity anchor;
// destPath is the final output path (the working file is
// destPath+".surge"). Returns nil only on verified physical success.
func (d *HlsDownloader) Run(ctx context.Context, manifestURL, destPath string) error {
	if !d.runOnce.CompareAndSwap(false, true) {
		return newHLSContractError("Run invoked twice on the same HlsDownloader")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := d.run(ctx, manifestURL, destPath)
	if err == nil {
		return nil
	}
	// A fired parent ctx owns the terminal identity: any stray
	// network/storage error must not impersonate the cancel.
	if perr := ctx.Err(); perr != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(perr, err)
	}
	return err
}

// ResumeSnapshot returns an independent clone of the last durably usable
// prefix (the mount-prepared seed, or a later synced checkpoint). It never
// exposes the live window seed.
func (d *HlsDownloader) ResumeSnapshot() *types.HLSResumeState {
	d.durableMu.Lock()
	defer d.durableMu.Unlock()
	return d.durable.Clone()
}

// run is the orchestration body; see Run for the cancel-identity wrapper.
func (d *HlsDownloader) run(ctx context.Context, manifestURL, destPath string) error {
	runCfg := types.DefaultRuntimeConfig()
	if d.Runtime != nil {
		rc := *d.Runtime
		runCfg = &rc
	}
	d.runCfg = *runCfg
	d.manifestURL = strings.TrimSpace(manifestURL)
	d.destPath = destPath
	d.surgePath = destPath + types.IncompleteSuffix
	headers := maps.Clone(d.Headers)
	d.taskHeaders = headers

	seed := d.HLSResume.Clone()
	if seed == nil {
		seed = &types.HLSResumeState{}
	}
	if strings.TrimSpace(seed.ManifestURL) == "" {
		seed.ManifestURL = d.manifestURL
	}
	if seed.ManifestURL != d.manifestURL {
		// Explicit source change: never mix old segment refs with new
		// credentials — rebase to a minimal invalidated seed.
		seed = seed.RebaseManifestURL(d.manifestURL)
	}
	credBase, err := parseHLSBinaryURL(seed.ManifestURL)
	if err != nil {
		return err
	}

	// Ordered teardown assembled as we go: close control entry, stop the
	// engine, wait terminal, join health, clear stats, checkpoint/verify,
	// then close window and file. Registered in reverse execution order.
	d.ctrlMu.Lock()
	d.closing = false
	d.engine = nil
	d.window = nil
	d.out = nil
	d.ctrlMu.Unlock()
	defer d.detachRun()

	prepared, err := d.bootstrap(ctx, seed, credBase, headers, runCfg)
	if err != nil {
		return err
	}
	mnt, err := d.mount(ctx, destPath, prepared)
	if err != nil {
		return err
	}
	d.publishMount(mnt)

	prog := d.State
	if prog == nil {
		prog = progress.New(d.ID, 0)
	}
	d.runProg = prog
	// Progress base once: both counters adopt the prepared prefix, stale
	// pending/error/stats are cleared, one session timing anchor.
	prog.Bytes.Downloaded.Store(mnt.seed.VerifiedBytes)
	prog.Bytes.VerifiedProgress.Store(mnt.seed.VerifiedBytes)
	prog.Session.SyncSessionStart(mnt.seed.VerifiedBytes)
	prog.SetPendingResumeState(nil)
	prog.Error.Store(nil)
	prog.SetWorkerStats(nil)

	d.setDurable(mnt.seed)

	if countUnfinishedMedia(mnt.seed) == 0 {
		// Zero-network completion: no engine start, no FirstByte, no
		// fan-out — verify the on-disk reality directly.
		return d.finishPhysical(mnt)
	}

	e, err := d.startEngine(ctx, mnt, prog, runCfg, headers)
	if err != nil {
		return err
	}

	err = d.controlLoop(ctx, e)
	// Terminal ordering: seal control entries, wait engine, join health,
	// clear stats, then branch by cause.
	d.ctrlMu.Lock()
	d.closing = true
	d.ctrlMu.Unlock()

	werr := e.wait()

	d.joinHealth()
	prog.SetWorkerStats(nil)

	return d.settleRun(ctx, mnt, prog, werr, err)
}

// publishMount registers the mounted artifacts for bridge/callback access.
func (d *HlsDownloader) publishMount(mnt *hlsMount) {
	d.ctrlMu.Lock()
	defer d.ctrlMu.Unlock()
	d.out = mnt.out
	d.window = mnt.window
	d.resumeBaseline = mnt.resumeBaseline
}

// publishEngine registers the live engine under the control gate.
func (d *HlsDownloader) publishEngine(e *segmentEngine) {
	d.ctrlMu.Lock()
	defer d.ctrlMu.Unlock()
	d.engine = e
}

// detachRun is the deferred teardown for exits before the engine stage
// (bootstrap/mount/init failures) plus the final window+file close on
// every path. Engine/health teardown happens inline in run().
func (d *HlsDownloader) detachRun() {
	d.ctrlMu.Lock()
	d.closing = true
	window := d.window
	out := d.out
	d.engine = nil
	d.window = nil
	d.out = nil
	d.ctrlMu.Unlock()
	if window != nil {
		_ = window.Close()
	}
	if out != nil {
		_ = out.Close()
	}
}

func (d *HlsDownloader) setDurable(s *types.HLSResumeState) {
	d.durableMu.Lock()
	defer d.durableMu.Unlock()
	d.durable = s.Clone()
}

func (d *HlsDownloader) engineForControl() *segmentEngine {
	d.ctrlMu.Lock()
	defer d.ctrlMu.Unlock()
	if d.closing {
		return nil
	}
	return d.engine
}

func (d *HlsDownloader) isClosing() bool {
	d.ctrlMu.Lock()
	defer d.ctrlMu.Unlock()
	return d.closing
}

func (d *HlsDownloader) wake() {
	select {
	case d.wakeCh <- struct{}{}:
	default:
	}
}

// startEngine builds the segment engine, publishes it for control
// bridges, starts the resident health watchdog, then spawns the first
// single-flight worker. On a start failure the watchdog is joined and the
// engine unpublished so no bridge can reach a dead engine.
func (d *HlsDownloader) startEngine(ctx context.Context, mnt *hlsMount, prog *progress.DownloadProgress, runCfg *types.RuntimeConfig, headers map[string]string) (*segmentEngine, error) {
	workerCap := runCfg.GetMaxConnectionsPerDownload()
	if workerCap <= 0 {
		workerCap = 1
	}
	if w := runCfg.GetWorkers(); w > 0 && w < workerCap {
		workerCap = w
	}
	if runCfg.SequentialDownload {
		workerCap = 1
	}
	d.maxWorkers = workerCap
	d.firstPendingIdx = mnt.seed.MergedSegments

	var e *segmentEngine
	e, err := newSegmentEngine(segmentEngineParams{
		Seed:        mnt.seed,
		Window:      mnt.window,
		Progress:    prog,
		Client:      d.Client,
		RunCfg:      runCfg,
		TaskHeaders: headers,
		Limiter:     d.Limiter,
		HostLimiter: d.HostLimiter,
		Parent:      ctx,
		MaxWorkers:  workerCap,
		OnAccepted: func(index int, res IngestResult, attemptStart time.Time) {
			d.onSegmentAccepted(e, index, res, attemptStart)
		},
	})
	if err != nil {
		return nil, err
	}
	d.publishEngine(e)
	d.startHealth(ctx, e, prog)
	if err := e.start(1); err != nil {
		d.joinHealth()
		prog.SetWorkerStats(nil)
		d.ctrlMu.Lock()
		d.engine = nil
		d.ctrlMu.Unlock()
		return nil, err
	}
	return e, nil
}

// controlLoop drives admission until settlement, cancellation, or engine
// stop. It never calls wait(): wait seals the engine, so runtime monitoring
// must use lifecycleView instead.
func (d *HlsDownloader) controlLoop(ctx context.Context, e *segmentEngine) error {
	poll := time.NewTicker(hlsControlPoll)
	defer poll.Stop()
	for {
		life := e.lifecycleView()
		rem := e.queue.remaining()
		switch {
		case ctx.Err() != nil:
			e.stop()
			return ctx.Err()
		case d.isClosing() || e.stopped():
			e.stop()
			return nil
		case rem == 0 && life.workers == 0:
			return nil
		case rem > 0 && life.workers == 0:
			// Drain-to-zero before terminal — respawn one worker so the
			// claim never parks mid-run.
			if !e.spawnWorker() {
				e.spawn(true)
			}
			continue
		}
		select {
		case <-ctx.Done():
			e.stop()
			return ctx.Err()
		case <-life.workersDone:
		case <-d.wakeCh:
		case <-poll.C:
		}
	}
}

// settleRun is the post-wait funnel: physical success verification, the
// non-cancel error checkpoint path, or the cancel path (no new writes).
func (d *HlsDownloader) settleRun(ctx context.Context, mnt *hlsMount, prog *progress.DownloadProgress, werr, cerr error) error {
	if ctx.Err() == nil && werr == nil && cerr == nil {
		return d.finishPhysical(mnt)
	}
	if ctx.Err() != nil {
		// Cancel path: keep only what was already durably synced.
		d.realignProgress(prog)
		if werr != nil && !errors.Is(werr, ctx.Err()) {
			return errors.Join(ctx.Err(), werr)
		}
		return ctx.Err()
	}
	if cerr != nil && werr == nil {
		// Control loop ended for a non-ctx reason (closing) — still treat
		// an unfinished engine as an error, not success.
		werr = cerr
	}
	if werr == nil {
		werr = newHLSContractError("engine drained without a terminal cause")
	}
	// Non-cancel error path: capture the durable prefix, sync, persist.
	d.checkpointOnError(mnt, prog, &werr)
	return werr
}

// finishPhysical verifies the terminal invariants and commits success.
func (d *HlsDownloader) finishPhysical(mnt *hlsMount) error {
	snap := mnt.window.SnapshotMergedPrefix()
	if snap.MergedSegments != snap.TotalSegments ||
		snap.VerifiedBytes != snap.MergedBytes ||
		snap.SpillPath != "" || snap.SpillFileSize != 0 || len(snap.SpillEntries) != 0 {
		return newHLSContractError("completion check: unmerged state at drain")
	}
	if err := validateSeed(snap); err != nil {
		return errors.Join(errIncompletePrefix, err)
	}
	if err := mnt.window.Sync(); err != nil {
		return err
	}
	fi, err := mnt.out.Stat()
	if err != nil {
		return newStorageError("stat", mnt.surgePath, err)
	}
	if fi.Size() != snap.MergedBytes {
		return errors.Join(errIncompletePrefix,
			newHLSContractError("surge size %d, merged %d", fi.Size(), snap.MergedBytes))
	}
	// Close before committing success — close errors are not swallowed.
	if err := mnt.window.Close(); err != nil {
		return err
	}
	if err := mnt.out.Close(); err != nil {
		return newStorageError("close", mnt.surgePath, err)
	}
	d.ctrlMu.Lock()
	d.window = nil
	d.out = nil
	d.ctrlMu.Unlock()
	// Stale arena cleanup is best-effort after handles are released.
	spillPath := spillPathFor(mnt.surgePath, d.ID)
	if err := os.Remove(spillPath); err != nil && !os.IsNotExist(err) {
		utils.Debug("hls: remove stale spill %q: %v", spillPath, err)
	}
	d.setDurable(snap)
	d.TotalSize = snap.MergedBytes
	return nil
}

// checkpointOnError preserves the last provable prefix on a non-cancel
// failure so the scheduler retry/paused-resume can rebuild from it.
func (d *HlsDownloader) checkpointOnError(mnt *hlsMount, prog *progress.DownloadProgress, werr *error) {
	window := mnt.window
	out := mnt.out
	if window == nil || out == nil {
		d.realignProgress(prog)
		return
	}
	snap := window.SnapshotMergedPrefix()
	if err := validateSeed(snap); err != nil {
		*werr = errors.Join(*werr, err)
		d.realignProgress(prog)
		return
	}
	if err := window.Sync(); err != nil {
		// Keep the previous durable checkpoint; report alongside the cause.
		*werr = errors.Join(*werr, err)
		d.realignProgress(prog)
		return
	}
	if err := d.saveCheckpoint(snap); err != nil {
		*werr = errors.Join(*werr, err)
		d.realignProgress(prog)
		return
	}
	d.setDurable(snap)
	d.realignProgress(prog)
}

// realignProgress pulls the live counters back to the chosen durable
// prefix after all workers have exited — never during a run.
func (d *HlsDownloader) realignProgress(prog *progress.DownloadProgress) {
	snap := d.ResumeSnapshot()
	if snap == nil || prog == nil {
		return
	}
	prog.Bytes.Downloaded.Store(snap.VerifiedBytes)
	prog.Bytes.VerifiedProgress.Store(snap.VerifiedBytes)
}

// saveCheckpoint writes the full task-backed record for a synced prefix.
func (d *HlsDownloader) saveCheckpoint(snap *types.HLSResumeState) error {
	if err := validateSeed(snap); err != nil {
		return errors.Join(errIncompletePrefix, err)
	}
	prog := d.runProg
	var elapsed int64
	if prog != nil {
		_, totalElapsed, _ := prog.Session.GetElapsed(prog.IsPaused())
		elapsed = totalElapsed.Nanoseconds()
	}
	var rate int64
	var set bool
	if prog != nil {
		rate, set = prog.GetRateLimit()
	}
	rec := &types.DownloadRecord{
		ID:           d.ID,
		URL:          d.manifestURL,
		URLHash:      store.URLHash(d.manifestURL),
		DestPath:     d.destPath,
		Filename:     filepath.Base(d.destPath),
		Strategy:     types.StrategyHLS,
		TotalSize:    0,
		Downloaded:   snap.VerifiedBytes,
		HLSResume:    snap.Clone(),
		Elapsed:      elapsed,
		Headers:      maps.Clone(d.Headers),
		Mirrors:      append([]string(nil), d.Mirrors...),
		RateLimit:    rate,
		RateLimitSet: set,
		Workers:      d.runCfg.Workers,
		MinChunkSize: d.runCfg.MinChunkSize,
		CreatedAt:    d.CreatedAt,
	}
	return store.SaveStateWithOptions(d.manifestURL, d.destPath, rec, store.SaveStateOptions{SkipFileHash: true})
}

// onSegmentAccepted is the single-flight acceptance gate: the first
// pending media segment (index == prepared MergedSegments, Direct append)
// performs the hard pre-fanout checkpoint, emits FirstByte once for fresh
// runs, then opens admission and fans out. Any checkpoint failure is a
// terminal orchestration error via latchFatal — never "log and fan out".
func (d *HlsDownloader) onSegmentAccepted(e *segmentEngine, index int, res IngestResult, attemptStart time.Time) {
	defer d.wake()
	if index != d.firstPendingIdx || !res.Direct || !res.Accepted {
		return
	}
	d.firstSegOnce.Do(func() {
		window := d.window
		if window == nil {
			e.latchFatal(newHLSContractError("accepted segment without window"))
			return
		}
		snap := window.SnapshotMergedPrefix()
		if err := window.Sync(); err != nil {
			e.latchFatal(err)
			return
		}
		if err := validateSeed(snap); err != nil {
			e.latchFatal(errors.Join(errIncompletePrefix, err))
			return
		}
		if err := d.saveCheckpoint(snap); err != nil {
			e.latchFatal(err)
			return
		}
		d.setDurable(snap)
		if !d.IsResume && d.resumeBaseline == 0 && d.firstByteEmitted.CompareAndSwap(false, true) {
			d.sendFirstByte(e.ctx, time.Since(attemptStart).Milliseconds())
		}
		e.setAdmissionOpen(true)
		target := d.maxWorkers
		for spawned := 1; spawned < target; spawned++ {
			// Bound new workers by feedable work — leased tasks are not
			// infinitely re-spawnable supply.
			if e.queue.remaining() <= e.lifecycleView().workers {
				break
			}
			if !e.spawnWorker() {
				break
			}
		}
	})
}

// sendFirstByte emits the one-time TTFB signal. The send obeys ctx
// cancellation and a bounded wait so a full/closed channel can never block
// cancel convergence; no goroutine is spawned.
func (d *HlsDownloader) sendFirstByte(ctx context.Context, ttfbMs int64) {
	ch := d.ProgressChan
	if ch == nil {
		return
	}
	defer func() { _ = recover() }()
	ev := types.DownloadEvent{Type: types.EventFirstByte, DownloadID: d.ID, TTFBMs: ttfbMs}
	select {
	case ch <- ev:
		return
	case <-ctx.Done():
		return
	default:
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case ch <- ev:
	case <-ctx.Done():
	case <-timer.C:
		utils.Debug("hls: first-byte event dropped for %s (channel saturated)", d.ID)
	}
}

func countUnfinishedMedia(s *types.HLSResumeState) int {
	if s == nil {
		return 0
	}
	n := 0
	for i := 0; i < s.TotalSegments; i++ {
		if s.CompletedBitmap[i>>3]&(1<<uint(i&7)) == 0 {
			n++
		}
	}
	return n
}
