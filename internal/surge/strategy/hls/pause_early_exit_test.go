package hls

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// pausedEarlyExit's durable reset is gated on the resume baseline: the
// startEngine arm of the early-exit window has already published the
// mounted seed via setDurable, so a provable baseline keeps that
// publication while a zero baseline must collapse it to the zeroed
// projection before the pause product returns. The path only touches
// State/HLSResume/resumeBaseline/durable — no engine, window, or file —
// so the gate is exercised here directly, white-box, with the same
// publishers run() uses.
func TestHLSPause_EarlyExitDurableResetGate(t *testing.T) {
	// earlyExit assembles a downloader mid-window: claim is the enqueue-time
	// persisted resume state (nil = fresh task), durable is what a previous
	// setDurable published (nil = pre-mount arm), baseline is the mounted
	// resumeBaseline, counter is what run() adopted into the progress
	// counters before the failure.
	earlyExit := func(t *testing.T, id string, claim, durable *types.HLSResumeState, baseline, counter int64) (*HlsDownloader, *progress.DownloadProgress, error) {
		t.Helper()
		prog := progress.New(id, 0)
		ctx, cancel := context.WithCancel(context.Background())
		prog.SetCancelFunc(cancel)
		t.Cleanup(cancel)

		d := NewHlsDownloader(id, nil, prog, types.DefaultRuntimeConfig())
		d.HLSResume = claim
		if durable != nil {
			d.setDurable(durable)
		}
		d.ctrlMu.Lock()
		d.resumeBaseline = baseline
		d.ctrlMu.Unlock()
		// Mirror run()'s post-mount counter adoption.
		prog.Bytes.Downloaded.Store(counter)
		prog.Bytes.VerifiedProgress.Store(counter)

		// The pause bridge cancels the run ctx; the early-return error
		// carries the cancel identity, as a bootstrap/mount/startEngine
		// failure would.
		prog.Pause()
		return d, prog, d.pausedEarlyExit(ctx, fmt.Errorf("engine start: %w", ctx.Err()))
	}

	t.Run("zero baseline collapses the published mounted seed", func(t *testing.T) {
		// The :214 window shape for a fresh run: the mounted seed carries
		// only this run's init fetch — bytes that must not survive as a
		// durable claim for copy-back.
		seed := &types.HLSResumeState{
			ManifestURL:     "http://origin/vod.m3u8",
			TotalSegments:   2,
			CompletedBitmap: []byte{0x00},
			MergedBytes:     32,
			VerifiedBytes:   32,
			InitSegmentURI:  "http://origin/init.mp4",
			InitCompleted:   true,
			InitBytes:       32,
			Segments:        []types.HLSSegmentState{{Index: 0}, {Index: 1}},
		}
		d, prog, err := earlyExit(t, "pause-early-fresh", nil, seed, 0, 32)
		if !errors.Is(err, types.ErrPaused) || !errors.Is(err, context.Canceled) {
			t.Fatalf("pausedEarlyExit = %v, want ErrPaused joined with the cancel", err)
		}
		if got := prog.Bytes.Downloaded.Load(); got != 0 {
			t.Fatalf("counter = %d, want 0 for a claim-less run", got)
		}
		if pending := prog.TakePendingResumeState(); pending != nil {
			t.Fatalf("early pause stashed a record: %+v", pending)
		}
		snap := d.ResumeSnapshot()
		if snap == nil {
			t.Fatal("durable vanished — the reset must keep a zeroed projection, not nil")
		}
		if snap.VerifiedBytes != 0 || snap.MergedBytes != 0 || snap.MergedSegments != 0 ||
			snap.InitBytes != 0 || snap.InitCompleted || snap.SpillPath != "" ||
			len(snap.SpillEntries) != 0 || snap.CompletedBitmap[0] != 0 {
			t.Fatalf("durable projection not zeroed: %+v", snap)
		}
		if seed.VerifiedBytes != 32 {
			t.Fatalf("reset mutated the caller's mounted seed: V=%d", seed.VerifiedBytes)
		}
	})

	t.Run("provable baseline keeps the mounted seed", func(t *testing.T) {
		seed := &types.HLSResumeState{
			ManifestURL:     "http://origin/vod.m3u8",
			TotalSegments:   2,
			CompletedBitmap: []byte{0x01},
			MergedSegments:  1,
			MergedBytes:     64,
			VerifiedBytes:   64,
			Segments:        []types.HLSSegmentState{{Index: 0}, {Index: 1}},
		}
		claim := &types.HLSResumeState{VerifiedBytes: 64}
		d, prog, err := earlyExit(t, "pause-early-resume", claim, seed, 64, 64)
		if !errors.Is(err, types.ErrPaused) || !errors.Is(err, context.Canceled) {
			t.Fatalf("pausedEarlyExit = %v, want ErrPaused joined with the cancel", err)
		}
		// The persisted claim keeps the mounted counters — finalize(64), not 0.
		if got := prog.Bytes.Downloaded.Load(); got != 64 {
			t.Fatalf("counter = %d, want mounted 64 for a claim-backed run", got)
		}
		snap := d.ResumeSnapshot()
		if snap == nil || snap.VerifiedBytes != 64 || snap.MergedSegments != 1 || snap.CompletedBitmap[0] != 0x01 {
			t.Fatalf("baseline>0 must keep the mounted seed verbatim: %+v", snap)
		}
	})

	t.Run("pre-mount arm leaves the nil durable untouched", func(t *testing.T) {
		// The bootstrap/mount arms publish nothing — the reset branch must
		// stay a no-op rather than materialize a phantom snapshot.
		d, _, err := earlyExit(t, "pause-early-premount", nil, nil, 0, 0)
		if !errors.Is(err, types.ErrPaused) || !errors.Is(err, context.Canceled) {
			t.Fatalf("pausedEarlyExit = %v, want ErrPaused joined with the cancel", err)
		}
		if snap := d.ResumeSnapshot(); snap != nil {
			t.Fatalf("pre-mount arm materialized a durable: %+v", snap)
		}
	})

	t.Run("non-pause error passes through unjoined", func(t *testing.T) {
		prog := progress.New("pause-early-nop", 0)
		d := NewHlsDownloader("pause-early-nop", nil, prog, types.DefaultRuntimeConfig())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := fmt.Errorf("mount: %w", ctx.Err())
		if got := d.pausedEarlyExit(ctx, err); errors.Is(got, types.ErrPaused) || !errors.Is(got, context.Canceled) {
			t.Fatalf("pausedEarlyExit = %v, want the raw error without ErrPaused", got)
		}
	})
}
