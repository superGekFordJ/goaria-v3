package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
)

// --- real activation facts ---

func TestObserveActivation_EstablishesAndIsIdempotent(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_a", 1000, "https://x", 4, "active")
	tracker.ObserveActivation("sg_a", 500, true)

	tt := tracker.tasks["sg_a"]
	first := tt.ActivatedAt
	if first.IsZero() {
		t.Fatal("activation not recorded")
	}
	if tt.progressBaseline != 500 || !tt.progressBaselineKnown {
		t.Fatalf("baseline = %d known=%v, want 500/true", tt.progressBaseline, tt.progressBaselineKnown)
	}

	// A duplicate start must not move the anchor or reset the baseline.
	time.Sleep(2 * time.Millisecond)
	tracker.ObserveActivation("sg_a", 999, true)
	if !tracker.tasks["sg_a"].ActivatedAt.Equal(first) {
		t.Error("duplicate activation moved ActivatedAt")
	}
	if tracker.tasks["sg_a"].progressBaseline != 500 {
		t.Error("duplicate activation reset baseline")
	}
}

func TestObserveActivation_UnknownBaselineUpgradedByFirstReliable(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_b", 1000, "https://x", 4, "active")
	// No trustworthy checkpoint at start → unknown baseline.
	tracker.ObserveActivation("sg_b", 0, false)
	if tracker.tasks["sg_b"].progressBaselineKnown {
		t.Fatal("baseline should be unknown")
	}

	// First reliable completed establishes the baseline — not growth.
	tracker.UpdateProgressFromEvent("sg_b", 1000, 400)
	if !tracker.tasks["sg_b"].progressBaselineKnown || tracker.tasks["sg_b"].progressBaseline != 400 {
		t.Fatalf("baseline = %d known=%v, want 400/true",
			tracker.tasks["sg_b"].progressBaseline, tracker.tasks["sg_b"].progressBaselineKnown)
	}
	if !tracker.tasks["sg_b"].FirstProgressAt.IsZero() {
		t.Error("baseline establishment must not count as first progress")
	}

	// Next growth beyond the baseline stamps first progress once.
	tracker.UpdateProgressFromEvent("sg_b", 1000, 450)
	if tracker.tasks["sg_b"].FirstProgressAt.IsZero() {
		t.Error("first growth above baseline not stamped")
	}
	first := tracker.tasks["sg_b"].FirstProgressAt
	time.Sleep(2 * time.Millisecond)
	tracker.UpdateProgressFromEvent("sg_b", 1000, 900)
	if !tracker.tasks["sg_b"].FirstProgressAt.Equal(first) {
		t.Error("later growth must not re-stamp FirstProgressAt")
	}
}

func TestObserveActivation_ResumeBaselineRejectsReplayAndRegression(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_r", 1000, "https://x", 4, "active")
	// Resume start: engine reports the recovered 500MB checkpoint as baseline.
	tracker.ObserveActivation("sg_r", 500, true)

	// A replay of the same amount is not growth.
	tracker.UpdateProgressFromEvent("sg_r", 1000, 500)
	if !tracker.tasks["sg_r"].FirstProgressAt.IsZero() {
		t.Error("replayed baseline amount must not count as first progress")
	}
	// A regressed value (re-verify rewinds) also cannot fake growth.
	tracker.UpdateProgressFromEvent("sg_r", 1000, 300)
	if !tracker.tasks["sg_r"].FirstProgressAt.IsZero() {
		t.Error("regressed value must not count as first progress")
	}
	if tracker.tasks["sg_r"].progressBaseline != 500 {
		t.Error("regression must not rewind the baseline")
	}
	// Genuine new growth stamps once.
	tracker.UpdateProgressFromEvent("sg_r", 1000, 600)
	if tracker.tasks["sg_r"].FirstProgressAt.IsZero() {
		t.Error("genuine growth above baseline not stamped")
	}
}

func TestTrackerFacts_TerminalAndPauseClearActivation(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_t", 1000, "https://x", 4, "active")
	tracker.ObserveActivation("sg_t", 100, true)

	tracker.SetStatusFromEvent("sg_t", "paused")
	if !tracker.tasks["sg_t"].ActivatedAt.IsZero() {
		t.Error("paused must end activation eligibility")
	}

	// Re-activate, then terminal clears facts and compat claims.
	tracker.SetStatusFromEvent("sg_t", "active")
	tracker.ObserveActivation("sg_t", 100, true)
	_ = tracker.MarkCompleteFromEvent("sg_t", "complete")
	tt := tracker.tasks["sg_t"]
	if !tt.ActivatedAt.IsZero() || tt.compatClaim != smartthread.CompatNone {
		t.Error("terminal must clear activation and compat claims")
	}
	// A late started event must not reopen facts on an accepted terminal.
	tracker.ObserveActivation("sg_t", 0, false)
	if !tracker.tasks["sg_t"].ActivatedAt.IsZero() {
		t.Error("activation on terminal-accepted gid must be refused")
	}
}

func TestTrackerFacts_WaitingEndsAria2Placeholder(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.MarkAdded("ar_q")
	if tracker.tasks["ar_q"].compatClaim != smartthread.CompatAria2Add ||
		tracker.tasks["ar_q"].addAnchorAt.IsZero() {
		t.Fatal("confirmed add should anchor the aria2 placeholder")
	}
	tracker.SetTargetBandwidth("ar_q", 5_000_000)

	// Authoritative waiting ends the placeholder immediately.
	tracker.Update(nil, []rpc.Task{{GID: "ar_q", Status: "waiting", CompletedLength: "0"}}, nil)
	tt := tracker.tasks["ar_q"]
	if tt.Status != "waiting" {
		t.Fatalf("status = %q, want waiting", tt.Status)
	}
	if tt.compatClaim != smartthread.CompatNone || !tt.addAnchorAt.IsZero() {
		t.Error("authoritative waiting must end the add placeholder")
	}

	// Late MarkAdded must not resurrect it.
	tracker.MarkAdded("ar_q")
	if tracker.tasks["ar_q"].compatClaim != smartthread.CompatNone {
		t.Error("late MarkAdded must not resurrect the placeholder on waiting status")
	}
}

func TestTrackerFacts_Aria2ActiveEstablishesActivation(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.MarkAdded("ar_a")
	tracker.SetTargetBandwidth("ar_a", 5_000_000)

	tracker.Update([]rpc.Task{createMockTask("ar_a", "active")}, nil, nil)
	tt := tracker.tasks["ar_a"]
	if tt.ActivatedAt.IsZero() {
		t.Fatal("authoritative active row must establish activation")
	}
	if !tt.progressBaselineKnown {
		t.Error("active row's completed is the reliable baseline")
	}
	if tt.compatClaim != smartthread.CompatNone {
		t.Error("real activation ends the add placeholder compat")
	}
}

func TestTrackerFacts_ParamWritesNeverRenewLease(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_p", 1000, "https://x", 4, "active")
	tracker.ObserveActivation("sg_p", 0, true)
	act := tracker.tasks["sg_p"].ActivatedAt

	// Parameter writes must not move activation/progress facts.
	tracker.SetTargetBandwidth("sg_p", 8_000_000)
	tracker.SetThreadInfo("sg_p", 12, false)
	tracker.SetScopeAndEnv("sg_p", "wan", 5, "a.com", "env1")
	tracker.SetTTFB("sg_p", 12)
	if !tracker.tasks["sg_p"].ActivatedAt.Equal(act) {
		t.Error("param writes moved ActivatedAt")
	}
}

func TestTrackerFacts_ResumeHoldTransfersToCompatClaim(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_h", 1000, "https://x", 4, "active")
	tracker.SetStatusFromEvent("sg_h", "paused")
	tracker.SetTargetBandwidth("sg_h", 4_000_000)
	if !tracker.tasks["sg_h"].resumeOccupancyHold {
		t.Fatal("resume write while paused should set the hold")
	}

	// Resume-ack "active" clears the private hold but transfers the claim —
	// no activation time is granted by the status write itself.
	tracker.SetStatusFromEvent("sg_h", "active")
	tt := tracker.tasks["sg_h"]
	if tt.resumeOccupancyHold {
		t.Error("hold should clear on status transition")
	}
	if tt.compatClaim != smartthread.CompatSurgeResume {
		t.Errorf("compat = %v, want CompatSurgeResume (claim survives until real start)", tt.compatClaim)
	}
	if !tt.ActivatedAt.IsZero() {
		t.Error("status write alone must not grant activation")
	}
}

func TestTrackerFacts_ReopenEndsLeaseKeepsClaim(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_ro", 1000, "https://x", 4, "active")
	tracker.SetTargetBandwidth("sg_ro", 4_000_000)
	tracker.ObserveActivation("sg_ro", 100, true)
	_ = tracker.MarkCompleteFromEvent("sg_ro", "error")

	tracker.ReopenAfterStoppedToLive("sg_ro", "active")
	tt := tracker.tasks["sg_ro"]
	if !tt.ActivatedAt.IsZero() {
		t.Error("reopen must end the previous run's lease")
	}
	if tt.compatClaim != smartthread.CompatSurgeResume {
		t.Errorf("compat = %v, want resume claim preserved across reopen", tt.compatClaim)
	}
}

func TestGetOccupancyTrackedTasks_AdmissionReservedIncluded(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.tasks["sg_adm"] = &TrackedTask{
		GID:                "sg_adm",
		Status:             "waiting",
		TargetBandwidth:    3_000_000,
		admittedNotStarted: true,
	}
	found := false
	for _, o := range tracker.GetOccupancyTrackedTasks() {
		if o.GID == "sg_adm" {
			found = true
		}
	}
	if !found {
		t.Error("explicit admitted-not-started input must be visible to occupancy")
	}
	if activeSetContains(tracker, "sg_adm") {
		t.Error("admitted-not-started must not widen the active set")
	}
}
