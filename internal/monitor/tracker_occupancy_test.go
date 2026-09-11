package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func TestSetTargetBandwidth_PersistsAllocatedAtAndOccupancy(t *testing.T) {
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
	if tt.Status != "active" {
		t.Errorf("Status = %q, want active (empty → active on bw>0)", tt.Status)
	}

	occ := tracker.GetOccupancyTrackedTasks()
	found := false
	for _, o := range occ {
		if o.GID == "sg_occ_1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("placeholder with TargetBandwidth should appear in GetOccupancyTrackedTasks")
	}
}

// TestGetOccupancyTrackedTasks_ExcludesPausedWaitingComplete covers occupancy
// inclusion/exclusion after SPEC-247: waiting with TargetBandwidth>0 is
// intentionally included (reverses SPEC-242's blanket waiting exclusion).
// Waiting with bw==0, paused-without-hold, complete, and empty-without-bw
// remain excluded. GetActiveTrackedTasks stays active-only.
func TestGetOccupancyTrackedTasks_ExcludesPausedWaitingComplete(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg_active", 4, false)
	tracker.SetTargetBandwidth("sg_active", 1_000_000)

	tracker.SetThreadInfo("sg_paused", 4, false)
	tracker.SetTargetBandwidth("sg_paused", 1_000_000)
	tracker.SetStatusFromEvent("sg_paused", "paused")

	tracker.SetThreadInfo("sg_waiting", 4, false)
	tracker.EnsureTrackedFromEvent("sg_waiting", 0, "https://x", 4, "waiting")
	tracker.tasks["sg_waiting"].TargetBandwidth = 1_000_000

	tracker.SetThreadInfo("sg_waiting0", 2, false)
	tracker.EnsureTrackedFromEvent("sg_waiting0", 0, "https://x", 2, "waiting")
	// TargetBandwidth left at 0 — still excluded.

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
	if !gids["sg_waiting"] {
		t.Error("waiting with TargetBandwidth>0 must seed occupancy (SPEC-247)")
	}
	if gids["sg_paused"] || gids["sg_waiting0"] || gids["sg_done"] || gids["sg_empty"] {
		t.Errorf("unexpected occupancy gids: %v", gids)
	}
	if activeSetContains(tracker, "sg_waiting") {
		t.Error("waiting claim must not widen GetActiveTrackedTasks")
	}
}

func TestGetOccupancyTrackedTasks_WaitingClaimReleasedOnRemove(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_q", 0, "https://a.com/f", 9, "waiting")
	tracker.SetScopeAndEnv("sg_q", "wan", 0, "a.com", "env1")
	tracker.SetThreadInfo("sg_q", 9, false)
	tracker.SetTargetBandwidth("sg_q", 5_000_000)

	occ := tracker.GetOccupancyTrackedTasks()
	found := false
	for _, o := range occ {
		if o.GID == "sg_q" && o.TargetBandwidth == 5_000_000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("waiting+bw claim missing from occupancy: %#v", occ)
	}

	tracker.RemoveTask("sg_q")
	for _, o := range tracker.GetOccupancyTrackedTasks() {
		if o.GID == "sg_q" {
			t.Fatal("RemoveTask must drop waiting claim from occupancy")
		}
	}
}

func TestGetOccupancyTrackedTasks_IncludesEmptyStatusPlaceholder(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.tasks["sg_ph"] = &TrackedTask{
		GID:             "sg_ph",
		TargetBandwidth: 2_000_000,
		Status:          "",
	}
	occ := tracker.GetOccupancyTrackedTasks()
	if len(occ) != 1 || occ[0].GID != "sg_ph" {
		t.Fatalf("empty-status placeholder with TargetBandwidth should be included, got %#v", occ)
	}
}

func TestSetTargetBandwidth_ResumeHoldWhilePaused(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_p", 100, "https://x", 4, "active")
	tracker.SetStatusFromEvent("sg_p", "paused")
	tracker.SetTargetBandwidth("sg_p", 2_000_000)
	if tracker.tasks["sg_p"].Status != "paused" {
		t.Errorf("Status = %q, want paused (must not override non-empty)", tracker.tasks["sg_p"].Status)
	}
	if !tracker.tasks["sg_p"].resumeOccupancyHold {
		t.Error("expected resumeOccupancyHold after SetTargetBandwidth while paused")
	}
}

func TestGetOccupancyTrackedTasks_ResumeHoldWhilePaused(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_r1", 100, "https://x", 4, "active")
	tracker.SetScopeAndEnv("sg_r1", "wan", 0, "a.com", "env1")
	tracker.SetStatusFromEvent("sg_r1", "paused")

	// Long-paused without hold must stay excluded.
	tracker.tasks["sg_r1"].TargetBandwidth = 5_000_000
	tracker.tasks["sg_r1"].AllocatedAt = time.Now().Add(-time.Hour)
	if len(tracker.GetOccupancyTrackedTasks()) != 0 {
		t.Fatal("long-paused without hold must not seed occupancy")
	}

	// Resume hook write while still paused → hold → visible.
	tracker.SetTargetBandwidth("sg_r1", 8_000_000)
	occ := tracker.GetOccupancyTrackedTasks()
	if len(occ) != 1 || occ[0].GID != "sg_r1" || occ[0].TargetBandwidth != 8_000_000 {
		t.Fatalf("resume hold occupancy = %#v, want sg_r1 @ 8MB", occ)
	}
	if activeSetContains(tracker, "sg_r1") {
		t.Error("paused+hold must not widen GetActiveTrackedTasks")
	}

	// EventResumed clears hold; active path takes over.
	tracker.SetStatusFromEvent("sg_r1", "active")
	if tracker.tasks["sg_r1"].resumeOccupancyHold {
		t.Error("hold should clear on status transition")
	}
	if !activeSetContains(tracker, "sg_r1") {
		t.Error("active after resume should be in GetActiveTrackedTasks")
	}
	if len(tracker.GetOccupancyTrackedTasks()) != 1 {
		t.Fatal("active task should remain in occupancy")
	}
}

func TestUpdate_ClearsResumeOccupancyHold(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_tick", 100, "https://x", 4, "active")
	tracker.SetStatusFromEvent("sg_tick", "paused")
	tracker.SetTargetBandwidth("sg_tick", 4_000_000)
	if !tracker.tasks["sg_tick"].resumeOccupancyHold {
		t.Fatal("expected hold before Update")
	}

	tracker.Update([]rpc.Task{createMockTask("sg_tick", "active")}, nil, nil)
	if tracker.tasks["sg_tick"].resumeOccupancyHold {
		t.Error("Update/updateActiveTask must clear resumeOccupancyHold")
	}
	if tracker.tasks["sg_tick"].Status != "active" {
		t.Errorf("Status = %q, want active", tracker.tasks["sg_tick"].Status)
	}
}
