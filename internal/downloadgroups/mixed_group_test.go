package downloadgroups

import (
	"errors"
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

// mixedGroupTestMonitor builds a Monitor with a hub+pusher and registers it
// via monitor.State so that downloadgroups operations can emit task:move and
// remove deltas. Returns the hub for event capture and a cleanup func.
func mixedGroupTestMonitor(t *testing.T) (*events.Hub, func()) {
	t.Helper()
	hub := events.NewHub(nil)
	m := monitor.NewMonitorForTest(hub)
	monitor.State.SetMonitor(m)
	return hub, func() {
		monitor.State.SetMonitor(nil)
	}
}

// seedMixedGroup inserts 2 sg_ active and 2 ar_ active tasks into Cache under
// the same DownloadGroup. Returns the group and the 4 GIDs.
func seedMixedGroup(t *testing.T, groupID string) (rpc.DownloadGroup, []string) {
	t.Helper()
	group := groupReadTestGroup(groupID, 4)
	sg1 := groupReadTask("sg_a1", "active", &group, "100", "10", "50")
	sg2 := groupReadTask("sg_a2", "active", &group, "100", "20", "60")
	ar1 := groupReadTask("ar_b1", "active", &group, "100", "10", "70")
	ar2 := groupReadTask("ar_b2", "active", &group, "100", "20", "80")
	monitor.Cache.AddSgTask(sg1, "active")
	monitor.Cache.AddSgTask(sg2, "active")
	monitor.Cache.UpdateFromAria2([]rpc.Task{ar1, ar2}, nil, nil)
	for _, gid := range []string{sg1.GID, sg2.GID, ar1.GID, ar2.GID} {
		monitor.RegisterTaskGroup(gid, group)
	}
	return group, []string{sg1.GID, sg2.GID, ar1.GID, ar2.GID}
}

// mockMultiResults simulates HybridEngine.pauseResumeMultiResults: splits GIDs
// by prefix, calls per-engine pause/resume, and returns per-GID results. The
// surgeErr/aria2Err simulate engine failures for graceful-degradation tests.
func mockMultiResults(surgeErr, aria2Err error) func(gids []string) ([]rpc.MultiCallItemResult, error) {
	return func(gids []string) ([]rpc.MultiCallItemResult, error) {
		results := make([]rpc.MultiCallItemResult, 0, len(gids))
		for _, gid := range gids {
			item := rpc.MultiCallItemResult{GID: gid, OK: true}
			if monitor.IsSgGid(gid) && surgeErr != nil {
				item.OK = false
				item.Error = surgeErr.Error()
			} else if !monitor.IsSgGid(gid) && aria2Err != nil {
				item.OK = false
				item.Error = aria2Err.Error()
			}
			results = append(results, item)
		}
		return results, nil
	}
}

func taskInCacheList(gid string, list string) bool {
	var tasks []rpc.Task
	switch list {
	case "active":
		tasks = monitor.Cache.GetActive()
	case "waiting":
		tasks = monitor.Cache.GetWaiting()
	case "stopped":
		tasks = monitor.Cache.GetStopped()
	}
	for _, t := range tasks {
		if t.GID == gid {
			return true
		}
	}
	return false
}

func TestMixedGroup_PauseResume_CacheMoveAndTaskMove(t *testing.T) {
	setupDownloadGroupsTest(t)
	hub, cleanup := mixedGroupTestMonitor(t)
	defer cleanup()

	var moveEvents []events.TaskMove
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		moveEvents = append(moveEvents, move)
	})

	group, gids := seedMixedGroup(t, "dg-mixed-pause")

	origPause := PauseMultiResults
	origResume := ResumeMultiResults
	PauseMultiResults = mockMultiResults(nil, nil)
	ResumeMultiResults = mockMultiResults(nil, nil)
	defer func() {
		PauseMultiResults = origPause
		ResumeMultiResults = origResume
	}()

	result := PauseDownloadGroup(group.ID)
	if !result.OK || result.Succeeded != 4 || result.Failed != 0 {
		t.Fatalf("unexpected pause result: %#v", result)
	}
	for _, gid := range gids {
		if !taskInCacheList(gid, "waiting") {
			t.Errorf("expected gid %s in waiting after pause", gid)
		}
	}
	sgMoves := 0
	for _, mv := range moveEvents {
		if mv.From == "active" && mv.To == "waiting" && monitor.IsSgGid(mv.GID) {
			sgMoves++
		}
	}
	if sgMoves != 2 {
		t.Errorf("expected 2 sg_ task:move events on pause, got %d (events=%v)", sgMoves, moveEvents)
	}

	moveEvents = nil
	result = ResumeDownloadGroup(group.ID)
	if !result.OK || result.Succeeded != 4 || result.Failed != 0 {
		t.Fatalf("unexpected resume result: %#v", result)
	}
	for _, gid := range gids {
		if !taskInCacheList(gid, "active") {
			t.Errorf("expected gid %s in active after resume", gid)
		}
	}
	sgMoves = 0
	for _, mv := range moveEvents {
		if mv.From == "waiting" && mv.To == "active" && monitor.IsSgGid(mv.GID) {
			sgMoves++
		}
	}
	if sgMoves != 2 {
		t.Errorf("expected 2 sg_ task:move events on resume, got %d (events=%v)", sgMoves, moveEvents)
	}
}

func TestMixedGroup_Remove_CacheRemoveAndOptimisticUI(t *testing.T) {
	setupDownloadGroupsTest(t)
	hub, cleanup := mixedGroupTestMonitor(t)
	defer cleanup()

	var removeDeltas []events.TaskDelta
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		if delta.Type == "remove" {
			removeDeltas = append(removeDeltas, delta)
		}
	})

	group, _ := seedMixedGroup(t, "dg-mixed-remove")

	result := RemoveDownloadGroup(group.ID, false, mockBatchRemove)
	if !result.OK || result.Succeeded != 4 {
		t.Fatalf("unexpected remove result: %#v", result)
	}
	if !result.Refresh.Tasks || !result.Refresh.Groups || !result.Refresh.Detail {
		t.Fatalf("expected full refresh hint, got %#v", result.Refresh)
	}

	sgGids := []string{"sg_a1", "sg_a2"}
	arGids := []string{"ar_b1", "ar_b2"}
	for _, gid := range sgGids {
		if taskInCacheList(gid, "active") || taskInCacheList(gid, "waiting") || taskInCacheList(gid, "stopped") {
			t.Errorf("expected sg_ gid %s removed from Cache directly", gid)
		}
	}
	sgRemoveDeltas := 0
	for _, d := range removeDeltas {
		if d.Type == "remove" && monitor.IsSgGid(d.GID) {
			sgRemoveDeltas++
		}
	}
	if sgRemoveDeltas != 2 {
		t.Errorf("expected 2 sg_ remove deltas, got %d (deltas=%v)", sgRemoveDeltas, removeDeltas)
	}
	for _, gid := range arGids {
		if monitor.GetStoredTaskGroup(gid) != nil {
			t.Errorf("expected ar_ gid %s stored group removed", gid)
		}
	}
}

func TestMixedGroup_SpeedStats_Aggregation(t *testing.T) {
	setupDownloadGroupsTest(t)

	group := groupReadTestGroup("dg-mixed-speed", 3)
	sgActive := groupReadTask("sg_speed1", "active", &group, "1000", "100", "100")
	sgComplete := groupReadTask("sg_speed2", "complete", &group, "1000", "1000", "0")
	arActive := groupReadTask("ar_speed1", "active", &group, "1000", "200", "200")
	monitor.Cache.AddSgTask(sgActive, "active")
	monitor.Cache.AddSgTask(sgComplete, "stopped")
	monitor.Cache.UpdateFromAria2([]rpc.Task{arActive}, nil, nil)
	for _, gid := range []string{sgActive.GID, sgComplete.GID, arActive.GID} {
		monitor.RegisterTaskGroup(gid, group)
	}

	detail := GetDownloadGroupDetail(group.ID)
	if !detail.Found {
		t.Fatalf("expected detail found, got %#v", detail)
	}
	if detail.Group.DownloadSpeed != "300" {
		t.Errorf("initial speed = %s, want 300 (sg_100 + ar_200, complete excluded)", detail.Group.DownloadSpeed)
	}

	monitor.Cache.PatchTaskProgress("sg_speed1", "150", "150", "1000")
	detail = GetDownloadGroupDetail(group.ID)
	if detail.Group.DownloadSpeed != "350" {
		t.Errorf("after sg_ speed patch = %s, want 350", detail.Group.DownloadSpeed)
	}

	monitor.Cache.PatchTaskProgress("ar_speed1", "250", "250", "1000")
	detail = GetDownloadGroupDetail(group.ID)
	if detail.Group.DownloadSpeed != "400" {
		t.Errorf("after ar_ speed patch = %s, want 400", detail.Group.DownloadSpeed)
	}
}

func TestMixedGroup_AggregateStatus_SurgeCompleteAria2cActive(t *testing.T) {
	setupDownloadGroupsTest(t)

	group := groupReadTestGroup("dg-mixed-status", 2)
	sgActive := groupReadTask("sg_status1", "active", &group, "1000", "100", "50")
	arActive := groupReadTask("ar_status1", "active", &group, "1000", "200", "60")
	monitor.Cache.AddSgTask(sgActive, "active")
	monitor.Cache.UpdateFromAria2([]rpc.Task{arActive}, nil, nil)
	for _, gid := range []string{sgActive.GID, arActive.GID} {
		monitor.RegisterTaskGroup(gid, group)
	}

	detail := GetDownloadGroupDetail(group.ID)
	if detail.Group.Status != DownloadGroupStatusActive {
		t.Fatalf("initial status = %s, want active", detail.Group.Status)
	}

	monitor.Cache.MoveTaskToStopped("sg_status1", "complete")
	detail = GetDownloadGroupDetail(group.ID)
	if detail.Group.Status != DownloadGroupStatusActive {
		t.Errorf("after sg_ complete, status = %s, want active (ar_ still active)", detail.Group.Status)
	}

	monitor.Cache.MoveTaskToStopped("ar_status1", "complete")
	detail = GetDownloadGroupDetail(group.ID)
	if detail.Group.Status != DownloadGroupStatusComplete {
		t.Errorf("after both complete, status = %s, want complete", detail.Group.Status)
	}
}

func TestMixedGroup_EngineUnavailable_GracefulDegrade(t *testing.T) {
	setupDownloadGroupsTest(t)
	_, cleanup := mixedGroupTestMonitor(t)
	defer cleanup()

	origPause := PauseMultiResults
	defer func() { PauseMultiResults = origPause }()

	t.Run("surge fails, aria2 succeeds", func(t *testing.T) {
		surgeErr := errors.New("surge unavailable")
		PauseMultiResults = mockMultiResults(surgeErr, nil)

		group, gids := seedMixedGroup(t, "dg-mixed-degrade-sg")

		result := PauseDownloadGroup(group.ID)
		if result.OK {
			t.Fatalf("expected not OK (partial failure), got %#v", result)
		}
		if result.Succeeded != 2 || result.Failed != 2 {
			t.Fatalf("expected 2 succeeded (ar_) and 2 failed (sg_), got %#v", result)
		}
		for _, gid := range gids {
			if monitor.IsSgGid(gid) {
				item := findOperationItem(t, result, gid)
				if item.Status != DownloadGroupOperationItemFailed {
					t.Errorf("expected sg_ gid %s failed, got %#v", gid, item)
				}
			} else {
				item := findOperationItem(t, result, gid)
				if item.Status != DownloadGroupOperationItemSucceeded {
					t.Errorf("expected ar_ gid %s succeeded, got %#v", gid, item)
				}
			}
		}
		if !result.Refresh.Tasks || !result.Refresh.Groups || !result.Refresh.Detail {
			t.Fatalf("expected refresh hint for partial failure, got %#v", result.Refresh)
		}
	})

	t.Run("aria2 fails, surge succeeds", func(t *testing.T) {
		aria2Err := errors.New("aria2 unavailable")
		PauseMultiResults = mockMultiResults(nil, aria2Err)

		group, gids := seedMixedGroup(t, "dg-mixed-degrade-ar")

		result := PauseDownloadGroup(group.ID)
		if result.OK {
			t.Fatalf("expected not OK (partial failure), got %#v", result)
		}
		if result.Succeeded != 2 || result.Failed != 2 {
			t.Fatalf("expected 2 succeeded (sg_) and 2 failed (ar_), got %#v", result)
		}
		for _, gid := range gids {
			if monitor.IsSgGid(gid) {
				item := findOperationItem(t, result, gid)
				if item.Status != DownloadGroupOperationItemSucceeded {
					t.Errorf("expected sg_ gid %s succeeded, got %#v", gid, item)
				}
			} else {
				item := findOperationItem(t, result, gid)
				if item.Status != DownloadGroupOperationItemFailed {
					t.Errorf("expected ar_ gid %s failed, got %#v", gid, item)
				}
			}
		}
	})
}

func TestCrossEngineTransfer_NotSupported_Verification(t *testing.T) {
	setupDownloadGroupsTest(t)
	if !monitor.IsSgGid("sg_test123") {
		t.Error("IsSgGid(sg_test123) should be true")
	}
	if monitor.IsSgGid("ar_abc456") {
		t.Error("IsSgGid(ar_abc456) should be false")
	}
	if monitor.IsSgGid("noprefix") {
		t.Error("IsSgGid(noprefix) should be false (defaults to ar)")
	}

	sgTask := groupReadTask("sg_transfer1", "active", nil, "100", "10", "5")
	arTask := groupReadTask("ar_transfer1", "active", nil, "100", "10", "5")
	monitor.Cache.AddSgTask(sgTask, "active")
	monitor.Cache.UpdateFromAria2([]rpc.Task{arTask}, nil, nil)

	monitor.Cache.MoveTaskToWaiting("sg_transfer1", "paused")
	if !taskInCacheList("sg_transfer1", "waiting") {
		t.Error("sg_ task should be in waiting after MoveTaskToWaiting")
	}
	if taskInCacheList("sg_transfer1", "active") {
		t.Error("sg_ task should not be in active after move")
	}

	monitor.Cache.MoveTaskToActive("ar_transfer1", "active")
	if !taskInCacheList("ar_transfer1", "active") {
		t.Error("ar_ task should remain in active")
	}

	monitor.Cache.RemoveTask("sg_transfer1")
	if taskInCacheList("sg_transfer1", "waiting") {
		t.Error("sg_ task should be removed from all lists")
	}
	monitor.Cache.RemoveTask("ar_transfer1")
	if taskInCacheList("ar_transfer1", "active") {
		t.Error("ar_ task should be removed from all lists")
	}
}

func TestIsMixedGroup(t *testing.T) {
	mkTargets := func(gids ...string) []downloadGroupOperationTarget {
		targets := make([]downloadGroupOperationTarget, len(gids))
		for i, g := range gids {
			targets[i] = downloadGroupOperationTarget{gid: g}
		}
		return targets
	}
	if isMixedGroup(mkTargets("sg_a", "sg_b")) {
		t.Error("pure sg_ group should not be mixed")
	}
	if isMixedGroup(mkTargets("ar_a", "ar_b")) {
		t.Error("pure ar_ group should not be mixed")
	}
	if !isMixedGroup(mkTargets("sg_a", "ar_b")) {
		t.Error("sg_+ar_ group should be mixed")
	}
	if isMixedGroup(nil) {
		t.Error("nil targets should not be mixed")
	}
}
