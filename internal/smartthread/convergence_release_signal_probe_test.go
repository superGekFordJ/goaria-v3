package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// TestConvergence_RemoveTask_PreservesDisappearanceSignal proves the production
// complete path (leave active → RemoveTask) keeps prevActiveGids. Release and
// windowInvalidated are asserted in separate subtests so bandwidthRelease is not
// double-fired (once manually, once inside tick).
//
// Forbidden: hand-injecting ct.prevActiveGids — prev* must come from tick #1.
func TestConvergence_RemoveTask_PreservesDisappearanceSignal(t *testing.T) {
	setup := func(t *testing.T) (donor, beneficiary string, tracker *mockTracker, telemetry *mockTelemetry, ct *ConvergenceTicker) {
		t.Helper()
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

		donor = "sg_probe_donor"
		beneficiary = "sg_probe_ben"
		tracker = &mockTracker{
			tasks: []TrackedTaskInfo{
				{GID: donor, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 50 * 1024 * 1024, TotalLength: 100 * 1024 * 1024},
				{GID: beneficiary, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 10 * 1024 * 1024, TotalLength: 100 * 1024 * 1024},
			},
		}
		telemetry = &mockTelemetry{
			data: map[string][]types.WorkerSnapshot{
				donor:       makeWorkers(9, 2*1024*1024),
				beneficiary: makeWorkers(1, 2*1024*1024),
			},
		}
		ct = newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker,
			telemetry,
		)
		t.Cleanup(func() { ct.Stop() })

		ct.tick()

		ct.mu.Lock()
		_, donorPrev := ct.prevActiveGids[donor]
		ct.mu.Unlock()
		if !donorPrev {
			t.Fatal("expected donor in prevActiveGids after first tick")
		}

		// Production complete path: leave active set, then RemoveTask.
		tracker.tasks = []TrackedTaskInfo{tracker.tasks[1]}
		delete(telemetry.data, donor)
		ct.RemoveTask(donor)

		ct.mu.Lock()
		_, donorPrevAfterRemove := ct.prevActiveGids[donor]
		ct.mu.Unlock()
		if !donorPrevAfterRemove {
			t.Fatal("RemoveTask must preserve donor in prevActiveGids until tick replace")
		}
		if bps, ready := ct.LastRawBps(donor); bps != 0 || ready {
			t.Errorf("LastRawBps after RemoveTask: got (%d,%v), want (0,false)", bps, ready)
		}
		return donor, beneficiary, tracker, telemetry, ct
	}

	t.Run("release_via_live_maps", func(t *testing.T) {
		_, beneficiary, tracker, _, ct := setup(t)

		ct.mu.Lock()
		benState := ct.states[beneficiary]
		if benState != nil {
			benState.phase = phaseStable
			benState.kneeFrozen = false
			benState.blackout = false
		}
		ct.mu.Unlock()

		activeGids := map[string]gidInfo{
			beneficiary: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		}
		releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil)
		if len(releases) != 1 || releases[0].gid != beneficiary || releases[0].delta != 1 {
			t.Fatalf("complete-path release: got %#v, want beneficiary +1", releases)
		}
	})

	t.Run("windowInvalidated_via_tick", func(t *testing.T) {
		donor, beneficiary, tracker, _, ct := setup(t)

		ct.mu.Lock()
		benState := ct.states[beneficiary]
		if benState != nil {
			benState.sustainCount = 3
			benState.prevCompleted = 5 * 1024 * 1024
			benState.prevSampleAt = time.Now().Add(-time.Minute)
			benState.phase = phaseStable
			benState.kneeFrozen = false
			benState.blackout = false
		}
		ct.mu.Unlock()

		ct.tick()

		ct.mu.Lock()
		benState = ct.states[beneficiary]
		sustainAfter := -1
		prevCompleted := int64(-1)
		var prevSampleAt time.Time
		if benState != nil {
			sustainAfter = benState.sustainCount
			prevCompleted = benState.prevCompleted
			prevSampleAt = benState.prevSampleAt
		}
		_, donorStillPrev := ct.prevActiveGids[donor]
		ct.mu.Unlock()
		if sustainAfter != 0 {
			t.Fatalf("windowInvalidated should reset sustainCount to 0, got %d", sustainAfter)
		}
		if prevCompleted != tracker.tasks[0].CompletedLength {
			t.Fatalf("windowInvalidated should rebuild prevCompleted, got %d want %d", prevCompleted, tracker.tasks[0].CompletedLength)
		}
		if prevSampleAt.IsZero() {
			t.Fatal("windowInvalidated should rebuild prevSampleAt")
		}
		if donorStillPrev {
			t.Fatal("expected donor absent from prevActiveGids after tick replace")
		}
	})
}

// TestConvergence_PausePath_DisappearanceVisibleWithoutRemoveTask confirms pause
// leaves prevActiveGids intact, so the next tick sees the donor disappear and
// elects a same-domain beneficiary.
func TestConvergence_PausePath_DisappearanceVisibleWithoutRemoveTask(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	donor := "sg_pause_donor"
	beneficiary := "sg_pause_ben"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: donor, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 50 * 1024 * 1024},
			{GID: beneficiary, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 10 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			donor:       makeWorkers(9, 2*1024*1024),
			beneficiary: makeWorkers(1, 2*1024*1024),
		},
	}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker,
		telemetry,
	)
	defer ct.Stop()

	ct.tick()

	// Pause path (§12.7): leave active set WITHOUT RemoveTask.
	tracker.tasks = []TrackedTaskInfo{tracker.tasks[1]}
	delete(telemetry.data, donor)

	ct.mu.Lock()
	benState := ct.states[beneficiary]
	if benState != nil {
		benState.sustainCount = 3
		benState.phase = phaseStable
		benState.kneeFrozen = false
		benState.blackout = false
	}
	_, donorStillPrev := ct.prevActiveGids[donor]
	ct.mu.Unlock()
	if !donorStillPrev {
		t.Fatal("pause must not call RemoveTask; donor should remain in prevActiveGids until tick")
	}

	activeGids := map[string]gidInfo{
		beneficiary: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil)
	if len(releases) != 1 || releases[0].gid != beneficiary || releases[0].delta != 1 {
		t.Fatalf("pause-path release: got %#v, want beneficiary +1", releases)
	}
}

// TestConvergence_UserDelete_RemoveTaskStillReleasesBandwidth covers InvalidateTask /
// reconcile RemoveTask: same preserve-prev* semantics as complete.
func TestConvergence_UserDelete_RemoveTaskStillReleasesBandwidth(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	donor := "sg_delete_donor"
	beneficiary := "sg_delete_ben"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: donor, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 50 * 1024 * 1024},
			{GID: beneficiary, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 10 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			donor:       makeWorkers(9, 2*1024*1024),
			beneficiary: makeWorkers(1, 2*1024*1024),
		},
	}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker,
		telemetry,
	)
	defer ct.Stop()

	ct.tick()

	tracker.tasks = []TrackedTaskInfo{tracker.tasks[1]}
	delete(telemetry.data, donor)
	ct.RemoveTask(donor) // InvalidateTask / reconcileSurgeCache call the same entrypoint

	ct.mu.Lock()
	benState := ct.states[beneficiary]
	if benState != nil {
		benState.phase = phaseStable
		benState.kneeFrozen = false
		benState.blackout = false
	}
	ct.mu.Unlock()

	activeGids := map[string]gidInfo{
		beneficiary: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil)
	if len(releases) != 1 || releases[0].gid != beneficiary || releases[0].delta != 1 {
		t.Fatalf("user-delete release: got %#v, want beneficiary +1", releases)
	}
}

// TestConvergence_BandwidthRelease_ZeroTelemetrySkipped aligns claim F1 fix:
// zero-telemetry candidates are ineligible; a real telemetered peer wins.
func TestConvergence_BandwidthRelease_ZeroTelemetrySkipped(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	emptyGid := "sg_zero_telemetry"
	realGid := "sg_has_telemetry"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: emptyGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com"},
			{GID: realGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com"},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			realGid: makeWorkers(2, 2*1024*1024),
			// emptyGid intentionally absent → len(stats)==0
		},
	}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker,
		telemetry,
	)
	defer ct.Stop()

	for _, gid := range []string{emptyGid, realGid} {
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseStable
		s.kneeFrozen = false
		s.blackout = false
		ct.mu.Unlock()
	}

	completedGid := "sg_completed_zero_tele"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	activeGids := map[string]gidInfo{
		emptyGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		realGid:  {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil)
	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}
	if releases[0].gid != realGid {
		t.Fatalf("expected real telemetered winner %s, got %s", realGid, releases[0].gid)
	}
}

func TestConvergence_BandwidthRelease_AllZeroTelemetryNoRelease(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	emptyA := "sg_zero_a"
	emptyB := "sg_zero_b"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: emptyA, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com"},
			{GID: emptyB, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com"},
		},
	}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{}}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker,
		telemetry,
	)
	defer ct.Stop()

	for _, gid := range []string{emptyA, emptyB} {
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseStable
		s.kneeFrozen = false
		s.blackout = false
		ct.mu.Unlock()
	}

	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		"sg_completed_all_zero": {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	pendingGids := map[string]bool{}
	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{
			emptyA: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			emptyB: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		},
		pendingGids,
		nil,
		nil,
		nil,
	)
	if len(releases) != 0 {
		t.Fatalf("all-zero telemetry must produce no release, got %#v", releases)
	}
	if len(pendingGids) != 0 {
		t.Fatalf("all-zero telemetry must not burn pendingGids, got %#v", pendingGids)
	}
}

// TestConvergence_DStats_ExcludesCompleteStillListed verifies dying 100% tasks
// do not inflate domain activeWorkers / retry sums. TotalLength==0 remains counted.
func TestConvergence_DStats_ExcludesCompleteStillListed(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	t.Run("dying_task_excluded_from_fuse_and_release_clamp", func(t *testing.T) {
		living := "sg_dstats_living"
		dying := "sg_dstats_dying"
		donor := "sg_dstats_donor"
		key := limitKey("wan", "example.com")

		dyingWorkers := makeWorkers(8, 2*1024*1024)
		for i := range dyingWorkers {
			dyingWorkers[i].RetryCount = 2 // would fuse if counted (sum=16)
		}
		tracker := &mockTracker{
			tasks: []TrackedTaskInfo{
				{
					GID: living, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
					CompletedLength: 10 * 1024 * 1024, TotalLength: 100 * 1024 * 1024,
				},
				{
					GID: dying, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
					CompletedLength: 100 * 1024 * 1024, TotalLength: 100 * 1024 * 1024,
				},
			},
		}
		telemetry := &mockTelemetry{
			data: map[string][]types.WorkerSnapshot{
				living: makeWorkers(2, 2*1024*1024),
				dying:  dyingWorkers,
			},
		}
		ct := newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker,
			telemetry,
		)
		defer ct.Stop()
		ct.limits.Clear(key)

		ct.tick()

		if nMax, ok := ct.limits.GetNMax(key); ok {
			t.Fatalf("dying retries must not fuse N_max; got N_max=%d", nMax)
		}

		// N_max=3: living-only (2)+1 fits; inflated living+dying (10)+1 clamps.
		ct.limits.SetNMax(key, 3)
		t.Cleanup(func() { ct.limits.Clear(key) })
		ct.mu.Lock()
		s := ct.states[living]
		if s != nil {
			s.phase = phaseStable
			s.kneeFrozen = false
			s.blackout = false
		}
		ct.prevActiveGids = map[string]gidInfo{
			donor:  {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			living: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			dying:  {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		}
		ct.mu.Unlock()

		releases := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{
				living: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
				dying:  {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			},
			map[string]bool{},
			nil,
			map[string]*domainStats{key: {activeWorkers: 2}},
			nil,
		)
		if len(releases) != 1 || releases[0].gid != living {
			t.Fatalf("dying workers must not block release via clamp, got %#v", releases)
		}

		blocked := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{
				living: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
				dying:  {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			},
			map[string]bool{},
			nil,
			map[string]*domainStats{key: {activeWorkers: 10}},
			nil,
		)
		if len(blocked) != 0 {
			t.Fatalf("control: inflated dStats should block release, got %#v", blocked)
		}
	})

	t.Run("unknown_size_still_counted", func(t *testing.T) {
		unknown := "sg_dstats_unknown"
		key := limitKey("wan", "example.com")
		workers := makeWorkers(3, 2*1024*1024)
		for i := range workers {
			workers[i].RetryCount = 2 // sum=6 ≥ threshold → fuse if counted
		}
		tracker := &mockTracker{
			tasks: []TrackedTaskInfo{
				{
					GID: unknown, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
					CompletedLength: 50 * 1024 * 1024, TotalLength: 0,
				},
			},
		}
		telemetry := &mockTelemetry{
			data: map[string][]types.WorkerSnapshot{unknown: workers},
		}
		ct := newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker,
			telemetry,
		)
		defer ct.Stop()
		ct.limits.Clear(key)
		t.Cleanup(func() { ct.limits.Clear(key) })

		ct.tick()

		nMax, ok := ct.limits.GetNMax(key)
		if !ok || nMax != 3 {
			t.Fatalf("TotalLength==0 must still aggregate into fuse; GetNMax=(%d,%v)", nMax, ok)
		}

		ct.mu.Lock()
		s := ct.states[unknown]
		if s != nil {
			s.phase = phaseStable
			s.kneeFrozen = false
			s.blackout = false
		}
		ct.prevActiveGids = map[string]gidInfo{
			"sg_dstats_unknown_donor": {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			unknown:                   {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		}
		ct.mu.Unlock()

		releases := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{unknown: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
			map[string]bool{},
			nil,
			map[string]*domainStats{key: {activeWorkers: 3}},
			nil,
		)
		if len(releases) != 0 {
			t.Fatalf("TotalLength==0 workers must still count toward clamp, got %#v", releases)
		}
	})

	// Complete-lags-tick: draining 1-worker dying peer sorts first and would burn
	// the +1 / pendingGids slot unless election applies the same complete guard.
	t.Run("dying_candidate_skipped_in_election", func(t *testing.T) {
		living := "sg_elect_living"
		dying := "sg_elect_dying"
		donor := "sg_elect_donor"
		tracker := &mockTracker{
			tasks: []TrackedTaskInfo{
				{
					GID: living, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
					CompletedLength: 10 * 1024 * 1024, TotalLength: 100 * 1024 * 1024,
				},
				{
					GID: dying, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
					CompletedLength: 100 * 1024 * 1024, TotalLength: 100 * 1024 * 1024,
				},
			},
		}
		telemetry := &mockTelemetry{
			data: map[string][]types.WorkerSnapshot{
				living: makeWorkers(9, 2*1024*1024),
				dying:  makeWorkers(1, 2*1024*1024), // would win ascending election without complete skip
			},
		}
		ct := newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker,
			telemetry,
		)
		defer ct.Stop()

		for _, gid := range []string{living, dying} {
			ct.mu.Lock()
			s := ct.getOrCreateState(gid)
			s.phase = phaseStable
			s.kneeFrozen = false
			s.blackout = false
			ct.mu.Unlock()
		}

		ct.mu.Lock()
		ct.prevActiveGids = map[string]gidInfo{
			donor:  {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			living: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			dying:  {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		}
		ct.mu.Unlock()

		pendingGids := map[string]bool{}
		releases := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{
				living: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
				dying:  {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
			},
			pendingGids,
			nil,
			nil,
			nil,
		)
		if len(releases) != 1 || releases[0].gid != living || releases[0].delta != 1 {
			t.Fatalf("living peer must win over dying candidate, got %#v", releases)
		}
		if pendingGids[dying] {
			t.Fatal("dying candidate must not burn pendingGids")
		}
	})
}
