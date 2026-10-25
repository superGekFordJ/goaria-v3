package orchestrator

import (
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

// TestHLSSpillResume_PausePreservesMountedBaseline covers the pause matrix
// row "baseline > 0, first segment of this run not yet verified" through the
// real worker path: run 1 pauses with a pending+overflow mix so the detail
// gob holds a merged prefix plus an 18-entry spill ledger; the resumed run
// adopts that ledger, queues only the still-gated gap segment, and is paused
// after its engine started but before any new media segment verifies. The
// second pause must publish a full checkpoint State carrying the mounted
// VerifiedBytes — never a zeroed or prefix-only projection — repersist the
// detail with the same V, and emit no extra FirstByte.
func TestHLSSpillResume_PausePreservesMountedBaseline(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	g := newGatedSpillOrigin(t, nil)
	defer g.openGate()

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, g.o.url("/media.m3u8"), dir, "video.ts")
	g.waitOutOfOrder(t)

	if err := s.mgr.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	s.waitMasterStatus(t, id, "paused", 10*time.Second)
	wantV := g.spillPendingSum()
	assertSpillCheckpoint(t, id, wantV, spillResumeSegs-2)

	// Resume with the gate still closed: the adopted ledger leaves only the
	// gap segment queued, and its second request parks at the origin — the
	// run settles into the engine without verifying any new segment.
	if err := s.mgr.Resume(id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForCond(t, 10*time.Second, "resumed run's gap segment in flight", func() bool {
		return g.s1Hits.Load() >= 2
	})
	// The resumed run's EventStarted must flip the master row back to
	// downloading first — otherwise the "paused" wait below could return on
	// the stale run-1 status before the second EventPaused is consumed.
	s.waitMasterStatus(t, id, "downloading", 10*time.Second)

	if err := s.mgr.Pause(id); err != nil {
		t.Fatalf("second Pause: %v", err)
	}
	// The fenced publisher saves the detail before EventPaused leaves, so
	// observing the second event already proves the checkpoint write.
	waitForCond(t, 10*time.Second, "second EventPaused", func() bool {
		return s.countEvents(id, types.EventPaused) >= 2
	})
	entry := s.waitMasterStatus(t, id, "paused", 10*time.Second)
	if entry.Downloaded != wantV {
		t.Fatalf("master Downloaded = %d, want mounted V=%d", entry.Downloaded, wantV)
	}

	// The second EventPaused carries the pending record as its full State.
	var lastPaused *types.DownloadEvent
	pausedCount := 0
	for _, ev := range s.eventsFor(id) {
		if ev.Type == types.EventPaused {
			pausedCount++
			e := ev
			lastPaused = &e
		}
	}
	if pausedCount != 2 || lastPaused == nil {
		t.Fatalf("EventPaused count = %d, want exactly 2", pausedCount)
	}
	st := lastPaused.State
	if st == nil || st.HLSResume == nil {
		t.Fatal("second EventPaused carried no checkpoint state — mounted baseline was dropped")
	}
	if st.ID != id || st.Strategy != types.StrategyHLS {
		t.Fatalf("event state identity wrong: %q %q", st.ID, st.Strategy)
	}
	hr := st.HLSResume
	if st.Downloaded != hr.VerifiedBytes || lastPaused.Downloaded != hr.VerifiedBytes {
		t.Fatalf("event D=%d state D=%d V=%d — Downloaded must equal VerifiedBytes",
			lastPaused.Downloaded, st.Downloaded, hr.VerifiedBytes)
	}
	if hr.VerifiedBytes != wantV || hr.MergedSegments != 1 || hr.MergedBytes != int64(len(g.bodies[0])) {
		t.Fatalf("mounted V=%d merged=%d segs %d bytes, want V=%d merged=1",
			hr.VerifiedBytes, hr.MergedSegments, hr.MergedBytes, wantV)
	}
	if len(hr.SpillEntries) != spillResumeSegs-2 ||
		hr.SpillEntries[0].SegmentIndex != 2 ||
		hr.SpillEntries[len(hr.SpillEntries)-1].SegmentIndex != spillResumeSegs-1 {
		t.Fatalf("spill entries = %+v — the adopted ledger must survive the pause", hr.SpillEntries)
	}
	if hr.SpillPath == "" || hr.SpillFileSize <= 0 {
		t.Fatalf("spill bookkeeping lost: path=%q size=%d", hr.SpillPath, hr.SpillFileSize)
	}
	if hr.CompletedBitmap[0]&(1<<1) != 0 {
		t.Fatal("the still-gated gap segment s1 was claimed")
	}

	// The detail gob is repersisted with the same V — the durable invariant
	// survives a pause taken before any new segment verified.
	assertSpillCheckpoint(t, id, wantV, spillResumeSegs-2)

	// No byte was refetched across both runs: the merged prefix and every
	// spilled segment hit the origin exactly once, and the gap segment saw
	// exactly one parked attempt per run.
	assertSpillReused(t, g)
	if n := g.s1Hits.Load(); n != 2 {
		t.Fatalf("s1 fetches = %d, want 2 (one parked attempt per run)", n)
	}

	// A mounted-baseline resume emits no new FirstByte — only run 1 did.
	if n := s.countEvents(id, types.EventFirstByte); n != 1 {
		t.Fatalf("EventFirstByte count = %d, want exactly 1 (first run only)", n)
	}
	if n := s.countEvents(id, types.EventStarted); n != 2 {
		t.Fatalf("EventStarted count = %d, want 2", n)
	}
	if n := s.countEvents(id, types.EventResumed); n != 1 {
		t.Fatalf("EventResumed count = %d, want 1", n)
	}
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d", n)
	}
	if n := s.countEvents(id, types.EventComplete); n != 0 {
		t.Fatalf("EventComplete count = %d — the gap segment never landed", n)
	}
}
