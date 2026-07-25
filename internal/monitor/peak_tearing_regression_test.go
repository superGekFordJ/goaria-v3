package monitor

import (
	"testing"
)

// Fixed: event path must not overwrite PeakSpeed without PeakThreadCount.
// Progress events only refresh lengths; PeakSpeed stays with RecordPeakEfficiency.
func TestEventPath_DoesNotTearPeakThreadPairing(t *testing.T) {
	prev := State.HasWindow()
	State.SetWindowExists(false)
	defer State.SetWindowExists(prev)

	tr := NewTaskTracker()
	gid := "sg_tear-pair"
	tr.EnsureTrackedFromEvent(gid, 200*1024*1024, "https://ex.com/f", 16, "active")
	tr.SetScopeAndEnv(gid, "wan", 40, "ex.com", "envA")

	tr.RecordPeakEfficiency(gid, 50*1024*1024, 16)

	tr.mu.RLock()
	tt := tr.tasks[gid]
	if tt.PeakSpeed != 50*1024*1024 || tt.PeakThreadCount != 16 {
		t.Fatalf("setup: PeakSpeed=%d PeakThreadCount=%d", tt.PeakSpeed, tt.PeakThreadCount)
	}
	if tt.PeakEnvKey != "envA" {
		t.Fatalf("PeakEnvKey seeded by SetScopeAndEnv = %q, want envA", tt.PeakEnvKey)
	}
	tr.mu.RUnlock()

	// High event-band spike must be ignored for PeakSpeed; lengths still advance.
	tr.UpdateProgressFromEvent(gid, 200*1024*1024, 100*1024*1024)

	tr.mu.RLock()
	tt = tr.tasks[gid]
	peakAfter, thrAfter, cl := tt.PeakSpeed, tt.PeakThreadCount, tt.CompletedLength
	tr.mu.RUnlock()

	if peakAfter != 50*1024*1024 {
		t.Fatalf("PeakSpeed after event = %d, want 50MiB/s (unchanged)", peakAfter)
	}
	if thrAfter != 16 {
		t.Fatalf("PeakThreadCount after event = %d, want 16", thrAfter)
	}
	if cl != 100*1024*1024 {
		t.Fatalf("CompletedLength after event = %d, want 100MiB", cl)
	}
}

// Regression: Surge TrackedTask.CompletedLength is not updated by tick Update
// (Hybrid TellActiveLite is Aria2-only). Progress events own this field.
func TestSurge_CompletedLengthDependsOnProgressEvents(t *testing.T) {
	tr := NewTaskTracker()
	gid := "sg_cl-event"
	tr.EnsureTrackedFromEvent(gid, 200*1024*1024, "https://ex.com/f", 8, "active")

	tr.mu.RLock()
	if tr.tasks[gid].CompletedLength != 0 {
		t.Fatalf("CompletedLength after EnsureTracked = %d, want 0", tr.tasks[gid].CompletedLength)
	}
	tr.mu.RUnlock()

	tr.Update(nil, nil, nil)
	tr.mu.RLock()
	if tr.tasks[gid].CompletedLength != 0 {
		t.Fatalf("CompletedLength after empty Update = %d, want 0", tr.tasks[gid].CompletedLength)
	}
	tr.mu.RUnlock()

	tr.UpdateProgressFromEvent(gid, 200*1024*1024, 60*1024*1024)
	tr.mu.RLock()
	cl := tr.tasks[gid].CompletedLength
	tl := tr.tasks[gid].TotalLength
	tr.mu.RUnlock()
	if cl != 60*1024*1024 || tl != 200*1024*1024 {
		t.Fatalf("after UpdateProgressFromEvent: Completed=%d Total=%d", cl, tl)
	}
}

// RecordPeakEfficiency refreshes PeakEnvKey when accepting a new PeakSpeed.
func TestRecordPeakEfficiency_WritesPeakEnvKeyOnAccept(t *testing.T) {
	tr := NewTaskTracker()
	gid := "sg_peak-env"
	tr.EnsureTrackedFromEvent(gid, 200*1024*1024, "https://ex.com/f", 8, "active")
	tr.SetScopeAndEnv(gid, "wan", 40, "ex.com", "envA")

	tr.mu.Lock()
	tr.tasks[gid].CurrentEnvKey = "envB"
	tr.mu.Unlock()

	tr.RecordPeakEfficiency(gid, 40*1024*1024, 8)

	tr.mu.RLock()
	tt := tr.tasks[gid]
	peak, thr, peakEnv, cur := tt.PeakSpeed, tt.PeakThreadCount, tt.PeakEnvKey, tt.CurrentEnvKey
	tr.mu.RUnlock()

	if peak != 40*1024*1024 || thr != 8 {
		t.Fatalf("RecordPeak wrote Peak=%d Thr=%d", peak, thr)
	}
	if cur != "envB" {
		t.Fatalf("CurrentEnvKey=%q, want envB", cur)
	}
	if peakEnv != "envB" {
		t.Fatalf("PeakEnvKey=%q, want envB (accepted PeakSpeed follows CurrentEnvKey)", peakEnv)
	}
}
