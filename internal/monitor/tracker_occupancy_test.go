package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
)

func TestSetTargetBandwidth_SurgeParamsNotOccupancy(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg_occ_1", 8, false)
	createdAt := tracker.tasks["sg_occ_1"].CreatedAt

	time.Sleep(2 * time.Millisecond)
	tracker.SetTargetBandwidth("sg_occ_1", 5_000_000)

	tt := tracker.tasks["sg_occ_1"]
	if tt.TargetBandwidth != 5_000_000 {
		t.Fatalf("TargetBandwidth = %d, want 5000000", tt.TargetBandwidth)
	}
	if tt.AllocatedAt.IsZero() {
		t.Fatal("AllocatedAt should be set")
	}
	if !tt.CreatedAt.Equal(createdAt) {
		t.Error("SetTargetBandwidth must not refresh CreatedAt")
	}
	if tt.Status != "" {
		t.Errorf("Status = %q, want empty — Surge rows never synthesize active", tt.Status)
	}

	for _, o := range tracker.GetOccupancyTrackedTasks() {
		if o.GID == "sg_occ_1" {
			t.Fatal("params-only Surge row must not seed occupancy")
		}
	}
}

// TestGetOccupancyTrackedTasks_ExcludesPausedWaitingComplete covers occupancy
// inclusion/exclusion under the managed-admission model: real active rows
// seed occupancy; ordinary waiting/paused params — TargetBandwidth included —
// contribute zero. The only waiting occupancy is an explicit
// admitted-not-started reservation. GetActiveTrackedTasks stays active-only.
func TestGetOccupancyTrackedTasks_ExcludesPausedWaitingComplete(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg_active", 4, false)
	tracker.SetStatusFromEvent("sg_active", "active")
	tracker.tasks["sg_active"].TargetBandwidth = 1_000_000

	tracker.SetThreadInfo("sg_paused", 4, false)
	tracker.SetStatusFromEvent("sg_paused", "paused")
	tracker.tasks["sg_paused"].TargetBandwidth = 1_000_000

	tracker.EnsureTrackedFromEvent("sg_waiting", 0, "https://x", 4, "waiting")
	tracker.SetThreadInfo("sg_waiting", 4, false)
	tracker.tasks["sg_waiting"].TargetBandwidth = 1_000_000

	// The explicit pending reservation is the positive waiting case.
	tracker.EnsureTrackedFromEvent("sg_pending", 0, "https://x", 4, "waiting")
	tracker.SetScopeAndEnv("sg_pending", "wan", 0, "a.com", "env1")
	if ticket, created := tracker.ReserveAdmission("sg_pending", 7,
		smartthread.ThreadParams{Split: 4, TargetBandwidth: 1_000_000},
		time.Now(), 0, true); !created || ticket.GID == "" {
		t.Fatal("ReserveAdmission must create a pending reservation")
	}

	tracker.EnsureTrackedFromEvent("sg_done", 100, "https://x", 4, "active")
	tracker.SetTargetBandwidth("sg_done", 1_000_000)
	_ = tracker.MarkCompleteFromEvent("sg_done", "complete")

	// Empty status without TargetBandwidth must be excluded.
	tracker.SetThreadInfo("sg_empty", 4, false)

	occ := tracker.GetOccupancyTrackedTasks()
	gids := map[string]bool{}
	for _, o := range occ {
		gids[o.GID] = true
	}
	if !gids["sg_active"] {
		t.Error("active occupancy missing")
	}
	if !gids["sg_pending"] {
		t.Error("explicit pending reservation must seed occupancy")
	}
	if gids["sg_waiting"] || gids["sg_paused"] || gids["sg_done"] || gids["sg_empty"] {
		t.Errorf("unexpected occupancy gids: %v", gids)
	}
	if activeSetContains(tracker, "sg_waiting") || activeSetContains(tracker, "sg_pending") {
		t.Error("waiting rows must not widen GetActiveTrackedTasks")
	}
}

func TestGetOccupancyTrackedTasks_PendingReservationReleasedOnRemove(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_q", 0, "https://a.com/f", 9, "waiting")
	tracker.SetScopeAndEnv("sg_q", "wan", 0, "a.com", "env1")
	tracker.SetThreadInfo("sg_q", 9, false)

	// Ordinary waiting params are not occupancy under managed admission.
	for _, o := range tracker.GetOccupancyTrackedTasks() {
		if o.GID == "sg_q" {
			t.Fatal("ordinary waiting row must not seed occupancy")
		}
	}

	if ticket, created := tracker.ReserveAdmission("sg_q", 3,
		smartthread.ThreadParams{Split: 9, TargetBandwidth: 5_000_000},
		time.Now(), 0, true); !created || ticket.GID == "" {
		t.Fatal("ReserveAdmission must create a pending reservation")
	}

	occ := tracker.GetOccupancyTrackedTasks()
	found := false
	for _, o := range occ {
		if o.GID == "sg_q" && o.TargetBandwidth == 5_000_000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending reservation missing from occupancy: %#v", occ)
	}

	tracker.RemoveTask("sg_q")
	for _, o := range tracker.GetOccupancyTrackedTasks() {
		if o.GID == "sg_q" {
			t.Fatal("RemoveTask must drop the reservation from occupancy")
		}
	}
}

func TestGetOccupancyTrackedTasks_ExcludesEmptyStatusSurgePlaceholder(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.tasks["sg_ph"] = &TrackedTask{
		GID:             "sg_ph",
		TargetBandwidth: 2_000_000,
		Status:          "",
	}
	if occ := tracker.GetOccupancyTrackedTasks(); len(occ) != 0 {
		t.Fatalf("params-only Surge placeholder must be excluded, got %#v", occ)
	}
}

func TestSetTargetBandwidth_SurgePausedWriteClaimsNothing(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_p", 100, "https://x", 4, "active")
	tracker.SetStatusFromEvent("sg_p", "paused")
	tracker.SetTargetBandwidth("sg_p", 2_000_000)
	if tracker.tasks["sg_p"].Status != "paused" {
		t.Errorf("Status = %q, want paused (must not override non-empty)", tracker.tasks["sg_p"].Status)
	}
	for _, o := range tracker.GetOccupancyTrackedTasks() {
		if o.GID == "sg_p" {
			t.Fatal("paused Surge params write must not seed occupancy")
		}
	}
}

func TestGetOccupancyTrackedTasks_PausedRowsNeverOccupy(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_r1", 100, "https://x", 4, "active")
	tracker.SetScopeAndEnv("sg_r1", "wan", 0, "a.com", "env1")
	tracker.SetStatusFromEvent("sg_r1", "paused")

	// Paused with stale params contributes zero — no hold machinery remains.
	tracker.tasks["sg_r1"].TargetBandwidth = 5_000_000
	tracker.tasks["sg_r1"].AllocatedAt = time.Now().Add(-time.Hour)
	if len(tracker.GetOccupancyTrackedTasks()) != 0 {
		t.Fatal("paused row must not seed occupancy")
	}
	tracker.SetTargetBandwidth("sg_r1", 8_000_000)
	if len(tracker.GetOccupancyTrackedTasks()) != 0 {
		t.Fatal("paused params write must not seed occupancy")
	}
	if activeSetContains(tracker, "sg_r1") {
		t.Error("paused must not widen GetActiveTrackedTasks")
	}

	// A real start transitions to active membership.
	tracker.SetStatusFromEvent("sg_r1", "active")
	tracker.ObserveActivation("sg_r1", 0, true)
	if !activeSetContains(tracker, "sg_r1") {
		t.Error("active after resume should be in GetActiveTrackedTasks")
	}
	if len(tracker.GetOccupancyTrackedTasks()) != 1 {
		t.Fatal("active task should remain in occupancy")
	}
}

func TestUpdate_ActiveRowOccupiesWithoutLegacyHold(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_tick", 100, "https://x", 4, "active")
	tracker.SetStatusFromEvent("sg_tick", "paused")
	tracker.SetTargetBandwidth("sg_tick", 4_000_000)
	if len(tracker.GetOccupancyTrackedTasks()) != 0 {
		t.Fatal("paused row with params must not occupy")
	}

	tracker.Update([]rpc.Task{createMockTask("sg_tick", "active")}, nil, nil)
	if tracker.tasks["sg_tick"].Status != "active" {
		t.Errorf("Status = %q, want active", tracker.tasks["sg_tick"].Status)
	}
	if len(tracker.GetOccupancyTrackedTasks()) != 1 {
		t.Error("authoritative active row occupies via status alone")
	}
}
