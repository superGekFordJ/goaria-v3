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

// ==================== Admission reservation (GID/token ticket) ====================

func admissionTestParams() smartthread.ThreadParams {
	return smartthread.ThreadParams{
		Split:           4,
		MinSize:         1 << 20,
		TargetBandwidth: 3_000_000,
	}
}

func TestAdmissionReservation_ReserveWritesPendingFacts(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_r1", 1000, "https://x", 4, "waiting")

	now := time.Now()
	ticket, created := tracker.ReserveAdmission("sg_r1", 7, admissionTestParams(), now, 200, true)
	if !created || ticket != (AdmissionTicket{GID: "sg_r1", Token: 7}) {
		t.Fatalf("reserve = %+v created=%v", ticket, created)
	}

	tt := tracker.tasks["sg_r1"]
	if !tt.admittedNotStarted || tt.admissionToken != 7 {
		t.Fatalf("pending/token = %v/%d, want true/7", tt.admittedNotStarted, tt.admissionToken)
	}
	if tt.Status != "waiting" {
		t.Errorf("reservation synthesized status %q", tt.Status)
	}
	if tt.TargetBandwidth != 3_000_000 || !tt.ActivatedAt.Equal(now) || !tt.AllocatedAt.Equal(now) {
		t.Errorf("floor/anchor = %d/%v/%v", tt.TargetBandwidth, tt.ActivatedAt, tt.AllocatedAt)
	}
	if tt.progressBaseline != 200 || !tt.progressBaselineKnown {
		t.Errorf("baseline = %d/%v", tt.progressBaseline, tt.progressBaselineKnown)
	}
	if tt.ThreadCount != 4 || tt.MinChunk != 1<<20 {
		t.Errorf("param hints = %d/%d", tt.ThreadCount, tt.MinChunk)
	}
	if activeSetContains(tracker, "sg_r1") {
		t.Error("reservation must not widen the active set")
	}
}

func TestAdmissionReservation_ReserveRefusals(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_w", 1000, "https://x", 4, "waiting")
	tracker.EnsureTrackedFromEvent("sg_a", 1000, "https://x", 4, "active")
	tracker.EnsureTrackedFromEvent("ar_w", 1000, "https://x", 4, "waiting")

	cases := []struct {
		name  string
		gid   string
		token uint64
		now   time.Time
	}{
		{"non-sg gid", "ar_w", 1, time.Now()},
		{"zero token", "sg_w", 0, time.Now()},
		{"zero anchor", "sg_w", 1, time.Time{}},
		{"active row", "sg_a", 1, time.Now()},
		{"missing row", "sg_ghost", 1, time.Now()},
	}
	for _, tc := range cases {
		if ticket, created := tracker.ReserveAdmission(tc.gid, tc.token, admissionTestParams(), tc.now, 0, false); created {
			t.Errorf("%s: reserve created %+v", tc.name, ticket)
		}
	}
	if ticket, created := tracker.ReserveAdmission("sg_w", 1, smartthread.ThreadParams{Split: 0}, time.Now(), 0, false); created {
		t.Errorf("split=0 reserve created %+v", ticket)
	}
	// Terminal-accepted row refuses.
	tracker.MarkCompleteFromEvent("sg_w", "complete")
	if _, created := tracker.ReserveAdmission("sg_w", 5, admissionTestParams(), time.Now(), 0, false); created {
		t.Error("terminal-accepted row accepted a reservation")
	}
}

func TestAdmissionReservation_TokenMonotonicity(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_tok", 1000, "https://x", 4, "waiting")
	now := time.Now()

	// High-water token wins; an older token is a stale opportunity.
	if _, created := tracker.ReserveAdmission("sg_tok", 9, admissionTestParams(), now, 0, false); !created {
		t.Fatal("initial reserve refused")
	}
	if _, created := tracker.ReserveAdmission("sg_tok", 4, admissionTestParams(), now, 0, false); created {
		t.Fatal("older token reserved over the high-water mark")
	}
	if got := tracker.tasks["sg_tok"].admissionToken; got != 9 {
		t.Fatalf("token moved to %d, want 9", got)
	}

	// A newer token supersedes (re-admission of a later opportunity).
	if ticket, created := tracker.ReserveAdmission("sg_tok", 11, admissionTestParams(), now, 0, false); !created || ticket.Token != 11 {
		t.Fatalf("newer token reserve = %+v/%v", ticket, created)
	}
	if got := tracker.tasks["sg_tok"].admissionToken; got != 11 {
		t.Fatalf("token = %d, want 11", got)
	}

	// Same-token reapply is an idempotent no-op still reporting the ticket.
	if ticket, created := tracker.ReserveAdmission("sg_tok", 11, admissionTestParams(), now, 0, false); created || ticket.Token != 11 {
		t.Fatalf("same-token reapply = %+v/%v, want idempotent ticket", ticket, created)
	}
}

func TestAdmissionReservation_ReleaseConditional(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_rel", 1000, "https://x", 4, "waiting")
	now := time.Now()
	ticket, created := tracker.ReserveAdmission("sg_rel", 3, admissionTestParams(), now, 50, true)
	if !created {
		t.Fatal("reserve refused")
	}

	// Wrong token / wrong gid leave the reservation untouched.
	if tracker.ReleaseAdmission("sg_rel", ticket.Token+1) {
		t.Fatal("release accepted a wrong token")
	}
	if tracker.ReleaseAdmission("sg_other", ticket.Token) {
		t.Fatal("release accepted a different gid")
	}
	tt := tracker.tasks["sg_rel"]
	if !tt.admittedNotStarted || tt.TargetBandwidth != 3_000_000 {
		t.Fatal("stale release disturbed the live reservation")
	}

	// Matching ticket releases pending + floor + anchor exactly once.
	if !tracker.ReleaseAdmission("sg_rel", ticket.Token) {
		t.Fatal("matching release refused")
	}
	tt = tracker.tasks["sg_rel"]
	if tt.admittedNotStarted || tt.TargetBandwidth != 0 || !tt.ActivatedAt.IsZero() {
		t.Fatalf("released row keeps reservation facts: pending=%v target=%d anchor=%v",
			tt.admittedNotStarted, tt.TargetBandwidth, tt.ActivatedAt)
	}
	if tt.Status != "waiting" {
		t.Fatalf("release rewrote status to %q", tt.Status)
	}
	// Repeat release of the consumed ticket is a no-op, not a rollback.
	if tracker.ReleaseAdmission("sg_rel", ticket.Token) {
		t.Fatal("repeat release of consumed ticket reported success")
	}
}

func TestAdmissionReservation_StartedNotReleasable(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_st", 1000, "https://x", 4, "waiting")
	ticket, created := tracker.ReserveAdmission("sg_st", 6, admissionTestParams(), time.Now(), 0, false)
	if !created {
		t.Fatal("reserve refused")
	}

	// The real start consumes the pending flag; a late release must not roll
	// back the running task's facts.
	tracker.ObserveActivation("sg_st", 0, false)
	tt := tracker.tasks["sg_st"]
	if tt.admittedNotStarted {
		t.Fatal("started task still carries pending reservation")
	}
	if tracker.ReleaseAdmission("sg_st", ticket.Token) {
		t.Fatal("release rolled back a started task")
	}
	if tt.ActivatedAt.IsZero() {
		t.Fatal("release wiped the real activation anchor")
	}
}

func TestAdmissionReservation_WaitingReAckKeepsReservation(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_wa", 1000, "https://x", 4, "waiting")
	now := time.Now()
	if _, created := tracker.ReserveAdmission("sg_wa", 2, admissionTestParams(), now, 0, false); !created {
		t.Fatal("reserve refused")
	}

	// Authoritative waiting restates the same opportunity — pending, floor,
	// and anchor survive.
	tracker.Update(nil, []rpc.Task{createMockTask("sg_wa", "waiting")}, nil)
	tt := tracker.tasks["sg_wa"]
	if !tt.admittedNotStarted || tt.admissionToken != 2 {
		t.Fatal("waiting re-ack dropped the reservation")
	}
	if tt.TargetBandwidth != 3_000_000 || !tt.ActivatedAt.Equal(now) {
		t.Fatal("waiting re-ack moved the floor or anchor")
	}
}

func TestAdmissionReservation_PausedEndsLineage(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_pd", 1000, "https://x", 4, "waiting")
	now := time.Now()
	if _, created := tracker.ReserveAdmission("sg_pd", 8, admissionTestParams(), now, 0, false); !created {
		t.Fatal("reserve refused")
	}

	tracker.SetStatusFromEvent("sg_pd", "paused")
	tt := tracker.tasks["sg_pd"]
	if tt.admittedNotStarted || tt.TargetBandwidth != 0 || !tt.ActivatedAt.IsZero() {
		t.Fatalf("paused boundary kept reservation facts: %+v", tt.admittedNotStarted)
	}
	// The ended lineage does not resurrect a legacy waiting claim either.
	for _, o := range tracker.GetOccupancyTrackedTasks() {
		if o.GID == "sg_pd" {
			t.Error("ended reservation still contributes occupancy")
		}
	}
	// And the same ticket can never reserve again after its consumption.
	if _, created := tracker.ReserveAdmission("sg_pd", 8, admissionTestParams(), now, 0, false); created {
		t.Fatal("consumed ticket re-reserved")
	}
}

func TestAdmissionReservation_StatusActiveDoesNotStart(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_sa", 1000, "https://x", 4, "waiting")
	if _, created := tracker.ReserveAdmission("sg_sa", 1, admissionTestParams(), time.Now(), 0, false); !created {
		t.Fatal("reserve refused")
	}

	// A status-only "active" write is not a start: pending stays, no compat
	// claim is minted, and the active set is unchanged.
	tracker.SetStatusFromEvent("sg_sa", "active")
	tt := tracker.tasks["sg_sa"]
	if !tt.admittedNotStarted {
		t.Fatal("status write consumed the pending reservation")
	}
	if tt.compatClaim != smartthread.CompatNone {
		t.Fatalf("status write minted compat claim %v", tt.compatClaim)
	}
}

func TestAdmissionReservation_TerminalAndReopenClear(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_tc", 1000, "https://x", 4, "waiting")
	if _, created := tracker.ReserveAdmission("sg_tc", 5, admissionTestParams(), time.Now(), 0, false); !created {
		t.Fatal("reserve refused")
	}
	tracker.MarkCompleteFromEvent("sg_tc", "error")
	tt := tracker.tasks["sg_tc"]
	if tt.admittedNotStarted || tt.TargetBandwidth != 0 {
		t.Fatal("terminal boundary kept the reservation")
	}

	tracker.EnsureTrackedFromEvent("sg_rp", 1000, "https://x", 4, "waiting")
	if _, created := tracker.ReserveAdmission("sg_rp", 5, admissionTestParams(), time.Now(), 0, false); !created {
		t.Fatal("reserve refused")
	}
	tracker.ReopenAfterStoppedToLive("sg_rp", "active")
	tt = tracker.tasks["sg_rp"]
	if tt.admittedNotStarted || tt.TargetBandwidth != 0 {
		t.Fatal("explicit reopen kept the reservation lineage")
	}
	// Ended lineage does not mint a resume claim (Target floor is gone).
	if tt.compatClaim != smartthread.CompatNone {
		t.Fatalf("reopen minted compat claim %v", tt.compatClaim)
	}
}
