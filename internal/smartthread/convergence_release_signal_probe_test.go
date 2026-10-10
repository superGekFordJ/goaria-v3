package smartthread

import (
	"math"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// TestConvergence_RemoveTask_PreservesDisappearanceSignal proves the production
// complete path (leave active → RemoveTask) keeps prevActiveGids. Subtests cover
// release-only, invalidate-only, and a single full tick() that asserts both
// without double-firing bandwidthRelease manually.
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
				// Donor carries real occupancy so its exit produces a
				// non-zero perturbation on the disappearance tick.
				{GID: donor, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 50 * 1024 * 1024, TotalLength: 100 * 1024 * 1024,
					TelemetryBps: 10 * 1024 * 1024, TelemetryValid: true},
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
		releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil, time.Now())
		if len(releases) != 1 || releases[0].gid != beneficiary || releases[0].delta != 1 {
			t.Fatalf("complete-path release: got %#v, want beneficiary +1", releases)
		}
	})

	t.Run("windowInvalidated_via_tick", func(t *testing.T) {
		donor, beneficiary, tracker, _, ct := setup(t)

		// Arm a pending down verdict (margin 5MB < E=10MB → dirty) plus
		// long-term evidence that local invalidation must not erase.
		ct.mu.Lock()
		benState := ct.states[beneficiary]
		if benState != nil {
			benState.sustainCount = 3
			benState.prevCompleted = 5 * 1024 * 1024
			benState.prevSampleAt = time.Now().Add(-time.Minute)
			benState.phase = phaseStable
			benState.lastStep = 1
			benState.probeBaseline = 10 * 1024 * 1024
			benState.probeBaselineWorkers = 1
			benState.peakSpeed = 20 * 1024 * 1024
			benState.kneeFrozen = false
			benState.blackout = false
		}
		ct.mu.Unlock()

		ct.tick()

		ct.mu.Lock()
		benState = ct.states[beneficiary]
		sustainAfter := -1
		prevCompleted := int64(-1)
		lastStep := -1
		lastRaw := int64(-1)
		peak := int64(-1)
		var prevSampleAt time.Time
		if benState != nil {
			sustainAfter = benState.sustainCount
			prevCompleted = benState.prevCompleted
			lastStep = benState.lastStep
			lastRaw = benState.lastRawBps
			peak = benState.peakSpeed
			prevSampleAt = benState.prevSampleAt
		}
		_, donorStillPrev := ct.prevActiveGids[donor]
		_, donorStillOcc := ct.prevActiveOccupancy[donor]
		ct.mu.Unlock()
		// Only the current verdict is cancelled — sustain/peak/raw survive.
		if lastStep != 0 {
			t.Fatalf("dirty tick must cancel pending verdict, lastStep=%d", lastStep)
		}
		if sustainAfter != 3 {
			t.Fatalf("dirty tick must preserve sustainCount, got %d", sustainAfter)
		}
		if lastRaw <= 0 {
			t.Fatalf("D2 must still publish on a dirty tick, lastRawBps=%d", lastRaw)
		}
		if peak != 20*1024*1024 {
			t.Fatalf("peak memory must survive invalidation, got %d", peak)
		}
		if prevCompleted != tracker.tasks[0].CompletedLength {
			t.Fatalf("dirty tick must advance prevCompleted, got %d want %d", prevCompleted, tracker.tasks[0].CompletedLength)
		}
		if prevSampleAt.IsZero() {
			t.Fatal("dirty tick must advance prevSampleAt")
		}
		if donorStillPrev || donorStillOcc {
			t.Fatal("expected donor pruned from prev* maps after tick consumes the exit")
		}
	})

	// Single full tick: arm+local-invalidate together (no manual bandwidthRelease).
	t.Run("release_and_invalidate_via_one_tick", func(t *testing.T) {
		donor, beneficiary, _, _, ct := setup(t)

		ct.mu.Lock()
		benState := ct.states[beneficiary]
		if benState != nil {
			benState.sustainCount = 3
			benState.prevCompleted = 5 * 1024 * 1024
			benState.prevSampleAt = time.Now().Add(-time.Minute)
			benState.phase = phaseStable
			benState.lastStep = 1
			benState.probeBaseline = 10 * 1024 * 1024
			benState.probeBaselineWorkers = 1
			benState.kneeFrozen = false
			benState.blackout = false
		}
		ct.mu.Unlock()

		ct.tick()

		ct.mu.Lock()
		benState = ct.states[beneficiary]
		sustainAfter := -1
		lastRaw := int64(-1)
		phase := -1
		if benState != nil {
			sustainAfter = benState.sustainCount
			lastRaw = benState.lastRawBps
			phase = benState.phase
		}
		lk := limitKey("wan", "example.com")
		_, armed := ct.pendingReleases[lk]
		_, donorStillPrev := ct.prevActiveGids[donor]
		ct.mu.Unlock()

		if sustainAfter != 3 {
			t.Fatalf("local invalidation must preserve sustainCount, got %d", sustainAfter)
		}
		if lastRaw <= 0 {
			t.Fatalf("D2 measurement must survive the dirty tick, lastRawBps=%d", lastRaw)
		}
		if !armed {
			t.Fatal("one tick should arm pendingReleases for disappeared donor")
		}
		if donorStillPrev {
			t.Fatal("donor should be pruned from prevActiveGids after tick")
		}
		// Arm free +1 is non-probing; ScaleWorkers may no-op in Hybrid test engine,
		// but phase must not become phaseProbingUp from the arm path.
		if phase == phaseProbingUp {
			t.Fatal("arm +1 via full tick must not enter phaseProbingUp")
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
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil, time.Now())
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
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil, time.Now())
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
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil, time.Now())
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
		nil, time.Now())

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
			nil, time.Now())

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
			nil, time.Now())

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
			nil, time.Now())

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
			nil, time.Now())

		if len(releases) != 1 || releases[0].gid != living || releases[0].delta != 1 {
			t.Fatalf("living peer must win over dying candidate, got %#v", releases)
		}
		if pendingGids[dying] {
			t.Fatal("dying candidate must not burn pendingGids")
		}
	})
}

// TestConvergence_LocalWindow_NetOccupancy proves the net-occupancy diff E:
// entry/exit sides cancel across both engines, a small pipeline is a small
// E (not a global reset trigger), E is net across domains, unattributed
// members still count toward E, zero measurements are real values, lease
// floors apply to entries, and stored previous values are used verbatim.
func TestConvergence_LocalWindow_NetOccupancy(t *testing.T) {
	mb := int64(1024 * 1024)
	now := time.Now()
	info := func(scope, domain string) gidInfo {
		return gidInfo{Scope: scope, Domain: domain, EnvKey: "testenv"}
	}
	row := func(gid, scope, domain string, telem int64) TrackedTaskInfo {
		return TrackedTaskInfo{
			GID: gid, Status: "active", Scope: scope, Domain: domain, EnvKey: "testenv",
			TelemetryBps: telem, TelemetryValid: telem > 0,
		}
	}
	// sampled+activeGids+prevGids built from gid → (scope,domain) triples.
	diff := func(cur []TrackedTaskInfo, prev map[string]gidInfo, prevOcc map[string]int64) (int64, map[string]bool, bool) {
		activeGids := make(map[string]gidInfo, len(cur))
		for _, r := range cur {
			activeGids[r.GID] = gidInfo{Scope: r.Scope, Domain: r.Domain, EnvKey: r.EnvKey}
		}
		return occupancyDelta(cur, activeGids, prev, prevOcc, now)
	}

	t.Run("two_engine_entry_exit_cancels", func(t *testing.T) {
		prev := map[string]gidInfo{
			"sg_long": info("wan", "a.com"),
			"ar_old":  info("wan", "b.com"),
		}
		prevOcc := map[string]int64{"sg_long": 50 * mb, "ar_old": 8 * mb}
		cur := []TrackedTaskInfo{
			row("sg_long", "wan", "a.com", 50*mb),
			row("ar_new", "wan", "c.com", 8*mb),
		}
		delta, changed, unknown := diff(cur, prev, prevOcc)
		if delta != 0 || unknown {
			t.Fatalf("E=(%d,unknown=%v), want (0,false): ar entry must cancel ar exit", delta, unknown)
		}
		if !changed[limitKey("wan", "b.com")] || !changed[limitKey("wan", "c.com")] {
			t.Fatalf("changedDomains missing b/c: %v", changed)
		}
	})

	t.Run("small_pipeline_net", func(t *testing.T) {
		prev := map[string]gidInfo{"sg_long": info("wan", "a.com")}
		prevOcc := map[string]int64{"sg_long": 50 * mb}
		cur := []TrackedTaskInfo{
			row("sg_long", "wan", "a.com", 50*mb),
			row("sg_pipe", "wan", "b.com", 512*1024),
		}
		delta, _, _ := diff(cur, prev, prevOcc)
		if delta != 512*1024 {
			t.Fatalf("E=%d, want %d — small pipeline is a measured small perturbation, not a reset", delta, 512*1024)
		}
	})

	t.Run("cross_domain_net", func(t *testing.T) {
		prev := map[string]gidInfo{"sg_a": info("wan", "x.com"), "sg_b": info("wan", "y.com")}
		prevOcc := map[string]int64{"sg_a": 10 * mb, "sg_b": 20 * mb}
		cur := []TrackedTaskInfo{
			row("sg_a", "wan", "x.com", 10*mb),
			row("sg_c", "wan", "z.com", 30*mb),
		}
		delta, changed, _ := diff(cur, prev, prevOcc)
		if delta != 10*mb {
			t.Fatalf("E=%d, want %d — |entry 30MB - exit 20MB| is net across domains", delta, 10*mb)
		}
		if !changed[limitKey("wan", "y.com")] || !changed[limitKey("wan", "z.com")] {
			t.Fatalf("changedDomains missing y/z: %v", changed)
		}
	})

	t.Run("missing_scope_domain_counts_in_e_only", func(t *testing.T) {
		prev := map[string]gidInfo{"sg_a": info("wan", "a.com")}
		prevOcc := map[string]int64{"sg_a": 10 * mb}
		cur := []TrackedTaskInfo{
			row("sg_a", "wan", "a.com", 10*mb),
			row("ar_none", "", "", 7*mb),
		}
		delta, changed, _ := diff(cur, prev, prevOcc)
		if delta != 7*mb {
			t.Fatalf("E=%d, want %d — unattributed entry still counts", delta, 7*mb)
		}
		if len(changed) != 0 {
			t.Fatalf("empty scope/domain must not mint wildcard limitKeys, got %v", changed)
		}
	})

	t.Run("zero_measurement_is_counted_as_zero", func(t *testing.T) {
		prev := map[string]gidInfo{"sg_a": info("wan", "a.com")}
		prevOcc := map[string]int64{"sg_a": 10 * mb}
		cur := []TrackedTaskInfo{
			row("sg_a", "wan", "a.com", 10*mb),
			row("sg_zero", "wan", "b.com", 0),
		}
		delta, changed, unknown := diff(cur, prev, prevOcc)
		if delta != 0 || unknown {
			t.Fatalf("E=(%d,%v), want (0,false) — a measured zero is not unknown", delta, unknown)
		}
		if !changed[limitKey("wan", "b.com")] {
			t.Fatalf("zero-occ entry is still a membership change: %v", changed)
		}
	})

	t.Run("lease_floor_entry_and_stored_exit_verbatim", func(t *testing.T) {
		// Entry with a live activation lease counts its Target floor, not telem.
		entry := TrackedTaskInfo{
			GID: "sg_lease", Status: "active", Scope: "wan", Domain: "a.com", EnvKey: "testenv",
			ActivatedAt: now.Add(-10 * time.Second), TargetBandwidth: 20 * mb,
			TelemetryBps: 1 * mb, TelemetryValid: true,
		}
		prev := map[string]gidInfo{"sg_a": info("wan", "a.com")}
		// Stored 30MB — e.g., a lease that has since expired. The stored value
		// is used verbatim; live re-evaluation would disagree.
		prevOcc := map[string]int64{"sg_a": 30 * mb}
		delta, _, _ := diff([]TrackedTaskInfo{entry}, prev, prevOcc)
		if delta != 10*mb {
			t.Fatalf("E=%d, want %d — entry uses lease floor 20MB, exit uses stored 30MB", delta, 10*mb)
		}
	})

	t.Run("unknown_exit_and_overflow_degrade", func(t *testing.T) {
		prev := map[string]gidInfo{"sg_a": info("wan", "a.com"), "sg_gone": info("wan", "b.com")}
		prevOcc := map[string]int64{"sg_a": 1 * mb} // sg_gone missing → unknown
		cur := []TrackedTaskInfo{row("sg_a", "wan", "a.com", 1*mb)}
		_, _, unknown := diff(cur, prev, prevOcc)
		if !unknown {
			t.Fatal("exit without stored occupancy must flag unknown")
		}

		prev2 := map[string]gidInfo{"sg_a": info("wan", "a.com"), "sg_x": info("wan", "b.com"), "sg_y": info("wan", "c.com")}
		prevOcc2 := map[string]int64{
			"sg_a": 1, "sg_x": math.MaxInt64 - 100, "sg_y": math.MaxInt64 - 100,
		}
		_, _, unknown = diff(cur, prev2, prevOcc2)
		if !unknown {
			t.Fatal("saturating exit sum must flag unknown")
		}
	})
}

// TestConvergence_LocalWindow_PreviousOccSurvivesRemove proves the previous-
// tick occupancy map survives RemoveTask/state deletion until the consuming
// tick: every sampled GID stores a value (zero included), exits use stored
// values verbatim even after a lease would have expired, and an Aria2 exit
// invalidates Surge verdicts without creating Aria2 state or release arms.
func TestConvergence_LocalWindow_PreviousOccSurvivesRemove(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	ben := "sg_occ_ben"     // pending verdict, margin 15MB — between donor telem and lease floor
	donor := "ar_occ_donor" // Aria2, live lease Target=20MB over telem=10MB
	zero := "sg_occ_zero"   // Surge member with measured-zero occupancy
	t0 := time.Now()
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: ben, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 10 * 1024 * 1024},
			{GID: donor, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "other.com",
				ActivatedAt: t0, TargetBandwidth: 20 * 1024 * 1024,
				TelemetryBps: 10 * 1024 * 1024, TelemetryValid: true},
			{GID: zero, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "zero.com"},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			ben:  makeWorkers(2, 2*1024*1024),
			zero: makeWorkers(1, 2*1024*1024),
		},
	}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry,
	)
	defer ct.Stop()

	ct.tickAt(t0)

	ct.mu.Lock()
	occDonor, okDonor := ct.prevActiveOccupancy[donor]
	occZero, okZero := ct.prevActiveOccupancy[zero]
	_, okBen := ct.prevActiveOccupancy[ben]
	ct.mu.Unlock()
	if !okDonor || occDonor != 20*1024*1024 {
		t.Fatalf("prevOcc[donor]=(%d,%v), want (20MB,true) — lease floor beats telem", occDonor, okDonor)
	}
	if !okZero || occZero != 0 {
		t.Fatalf("prevOcc[zero]=(%d,%v), want (0,true) — zeros are stored", occZero, okZero)
	}
	if !okBen {
		t.Fatal("prevOcc[ben] missing — every sampled GID must get an entry")
	}

	// Arm ben's pending down verdict: margin 0.5*(60MB/2)*1 = 15MB, inside
	// (telem 10MB, stored lease 20MB) — affected iff the stored value is used.
	ct.mu.Lock()
	s := ct.states[ben]
	s.phase = phaseSettling
	s.lastStep = 1
	s.probeBaseline = 60 * 1024 * 1024
	s.probeBaselineWorkers = 2
	s.prevCompleted = 10 * 1024 * 1024
	s.prevSampleAt = t0
	ct.mu.Unlock()

	// RemoveTask clears state but must preserve the pending exit evidence.
	ct.RemoveTask(zero)
	ct.mu.Lock()
	_, stateGone := ct.states[zero]
	_, prevKept := ct.prevActiveGids[zero]
	_, occKept := ct.prevActiveOccupancy[zero]
	ct.mu.Unlock()
	if stateGone || !prevKept || !occKept {
		t.Fatalf("RemoveTask must clear state only: state=%v prevGids=%v prevOcc=%v", stateGone, prevKept, occKept)
	}

	// Both exit; tick at t0+60s — the donor's lease is long expired now.
	tracker.tasks = tracker.tasks[:1]
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	delete(telemetry.data, zero)
	ct.tickAt(t0.Add(60 * time.Second))

	ct.mu.Lock()
	s = ct.states[ben]
	_, donorState := ct.states[donor]
	_, armOther := ct.pendingReleases[limitKey("wan", "other.com")]
	_, armZero := ct.pendingReleases[limitKey("wan", "zero.com")]
	_, benPrev := ct.prevActiveGids[ben]
	_, donorPrev := ct.prevActiveGids[donor]
	_, donorOccKept := ct.prevActiveOccupancy[donor]
	_, zeroOccKept := ct.prevActiveOccupancy[zero]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected ben state")
	}
	// E=20MB (stored lease) ≥ 15MB margin → dirty → verdict cancelled.
	if s.lastStep != 0 || s.phase != phaseStable {
		t.Fatalf("stored-lease exit must dirty the window: lastStep=%d phase=%d", s.lastStep, s.phase)
	}
	if s.lastRawBps <= 0 || !s.macroReady {
		t.Fatalf("D2 must still publish on the dirty tick: raw=%d ready=%v", s.lastRawBps, s.macroReady)
	}
	// Aria2 participates in E only — never gains state or a release arm.
	if donorState {
		t.Fatal("Aria2 exit must not create convergence state")
	}
	if armOther {
		t.Fatal("Aria2 disappearance must not arm bandwidthRelease")
	}
	if !armZero {
		t.Fatal("sg_ disappearance must still arm its domain")
	}
	// Consumed exits are pruned from both prev* maps; survivors remain.
	if donorPrev || !benPrev {
		t.Fatalf("prevActiveGids after consume: donor=%v ben=%v, want (false,true)", donorPrev, benPrev)
	}
	if donorOccKept || zeroOccKept {
		t.Fatal("consumed exits must be pruned from prevActiveOccupancy")
	}
}

// TestConvergence_LocalWindow_ReservationsExcluded proves the sampled-active
// filter: waiting claims, synthetic compat actives and admission reservations
// never enter the membership diff or Surge processing, yet still count
// toward the wide occupancy budget; a genuinely-started task is one entry,
// and a legacy active without an anchor stays a telem-only member.
func TestConvergence_LocalWindow_ReservationsExcluded(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	t0 := time.Now()
	rows := []TrackedTaskInfo{
		{GID: "sg_wait", Status: "waiting", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
			TargetBandwidth: 5 * 1024 * 1024, TelemetryBps: 1024 * 1024, TelemetryValid: true},
		{GID: "sg_syn", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
			CompatClaim: CompatSurgeClaim, TargetBandwidth: 6 * 1024 * 1024},
		{GID: "sg_resv", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
			AdmissionReserved: true, ActivatedAt: t0, TargetBandwidth: 7 * 1024 * 1024},
		{GID: "sg_old", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
			TelemetryBps: 3 * 1024 * 1024, TelemetryValid: true},
		{GID: "sg_start", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
			ActivatedAt: t0, TelemetryBps: 2 * 1024 * 1024, TelemetryValid: true},
	}
	tracker := &mockTracker{tasks: append([]TrackedTaskInfo(nil), rows...)}
	// Even with workers, excluded rows must not reach Surge processing.
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{}}
	for _, r := range rows {
		telemetry.data[r.GID] = makeWorkers(2, 2*1024*1024)
	}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry,
	)
	defer ct.Stop()

	ct.tickAt(t0)

	ct.mu.Lock()
	states := make(map[string]bool, len(ct.states))
	for gid := range ct.states {
		states[gid] = true
	}
	membership := make(map[string]gidInfo, len(ct.prevActiveGids))
	for gid, gi := range ct.prevActiveGids {
		membership[gid] = gi
	}
	ct.mu.Unlock()
	for _, excluded := range []string{"sg_wait", "sg_syn", "sg_resv"} {
		if states[excluded] {
			t.Fatalf("%s must not create convergence state", excluded)
		}
		if _, in := membership[excluded]; in {
			t.Fatalf("%s must not enter sampled-active membership", excluded)
		}
	}
	for _, included := range []string{"sg_old", "sg_start"} {
		if !states[included] || membership[included].Domain != "example.com" {
			t.Fatalf("%s must be a sampled member with state", included)
		}
	}
	if len(membership) != 2 {
		t.Fatalf("membership=%v, want exactly {sg_old, sg_start}", membership)
	}

	// Excluded rows still occupy budget through the unified formula.
	for _, r := range rows[:3] {
		if occ := TaskOccupancy(r, t0); occ <= 0 {
			t.Fatalf("%s excluded from diff must still count toward budget, occ=%d", r.GID, occ)
		}
	}

	// A genuinely-started task joins as exactly one new member next tick.
	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: "sg_new", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
		ActivatedAt: t0, TelemetryBps: 1024 * 1024, TelemetryValid: true,
	})
	telemetry.data["sg_new"] = makeWorkers(1, 2*1024*1024)
	ct.tickAt(t0.Add(5 * time.Second))
	ct.mu.Lock()
	_, newIn := ct.prevActiveGids["sg_new"]
	_, newOcc := ct.prevActiveOccupancy["sg_new"]
	memberCount := len(ct.prevActiveGids)
	ct.mu.Unlock()
	if !newIn || !newOcc || memberCount != 3 {
		t.Fatalf("started task must be one entry: in=%v occ=%v members=%d", newIn, newOcc, memberCount)
	}
}

// countingOccupancy wraps mockOccupancy with a per-tick call counter.
type countingOccupancy struct {
	mockOccupancy
	calls *int32
}

func (m *countingOccupancy) GetOccupancyTrackedTasks() []TrackedTaskInfo {
	(*m.calls)++
	return m.mockOccupancy.GetOccupancyTrackedTasks()
}

// TestConvergence_LocalWindow_SnapshotCostAndFallback proves collection cost:
// the wide OccupancyProvider is called once per tick (never per task and
// never to reconstruct a missing previous value), the nil-provider fallback
// copies the active snapshot without aliasing it, the diff is
// order-independent, and duplicate GIDs dedupe first-wins and count once.
func TestConvergence_LocalWindow_SnapshotCostAndFallback(t *testing.T) {
	mb := int64(1024 * 1024)

	t.Run("provider_called_once_per_tick", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		var calls int32
		tracker := &mockTracker{tasks: []TrackedTaskInfo{
			{GID: "sg_a", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "a.com",
				CompletedLength: 10 * mb},
		}}
		telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{
			"sg_a": makeWorkers(2, 2*1024*1024),
		}}
		ct := newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker, telemetry,
		)
		defer ct.Stop()
		ct.SetOccupancyProvider(&countingOccupancy{
			mockOccupancy: mockOccupancy{tasks: tracker.tasks},
			calls:         &calls,
		})
		ct.tick()
		ct.tick()
		if calls != 2 {
			t.Fatalf("occupancy provider calls=%d, want exactly 1 per tick", calls)
		}
	})

	t.Run("nil_provider_fallback_does_not_alias", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tracker := &mockTracker{tasks: []TrackedTaskInfo{
			{GID: "sg_a", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "a.com",
				CompletedLength: 10 * mb},
		}}
		telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{
			"sg_a": makeWorkers(2, 2*1024*1024),
		}}
		ct := newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker, telemetry,
		)
		defer ct.Stop()
		t0 := time.Now()
		ct.tickAt(t0)
		// Give the state a live raw measurement, then run a second tick at a
		// later instant: the raw/ready overlay must land on the snapshot copy,
		// never mutate the tracker's own rows.
		tracker.tasks[0].CompletedLength = 60 * mb
		ct.tickAt(t0.Add(5 * time.Second))
		if tracker.tasks[0].RawBps != 0 || tracker.tasks[0].MacroReady {
			t.Fatalf("fallback overlay aliased tracker row: raw=%d ready=%v",
				tracker.tasks[0].RawBps, tracker.tasks[0].MacroReady)
		}
	})

	t.Run("order_independence_and_first_wins_dedup", func(t *testing.T) {
		now := time.Now()
		prev := map[string]gidInfo{"sg_a": {Scope: "wan", Domain: "a.com", EnvKey: "testenv"}}
		prevOcc := map[string]int64{"sg_a": 10 * mb}
		mk := func() []TrackedTaskInfo {
			return []TrackedTaskInfo{
				{GID: "sg_a", Status: "active", Scope: "wan", Domain: "a.com", EnvKey: "testenv",
					TelemetryBps: 10 * mb, TelemetryValid: true},
				{GID: "ar_b", Status: "active", Scope: "wan", Domain: "b.com", EnvKey: "testenv",
					TelemetryBps: 8 * mb, TelemetryValid: true},
				{GID: "sg_c", Status: "active", Scope: "wan", Domain: "c.com", EnvKey: "testenv",
					TelemetryBps: 4 * mb, TelemetryValid: true},
			}
		}
		mkActive := func(cur []TrackedTaskInfo) map[string]gidInfo {
			out := make(map[string]gidInfo, len(cur))
			for _, r := range cur {
				out[r.GID] = gidInfo{Scope: r.Scope, Domain: r.Domain, EnvKey: r.EnvKey}
			}
			return out
		}
		cur1 := mk()
		d1, c1, u1 := occupancyDelta(cur1, mkActive(cur1), prev, prevOcc, now)
		cur2 := []TrackedTaskInfo{cur1[2], cur1[1], cur1[0]}
		d2, c2, u2 := occupancyDelta(cur2, mkActive(cur2), prev, prevOcc, now)
		if d1 != d2 || u1 != u2 || len(c1) != len(c2) {
			t.Fatalf("order must not change the diff: (%d,%v,%v) vs (%d,%v,%v)", d1, u1, c1, d2, u2, c2)
		}

		// Duplicate GID rows: first wins, counted once.
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tracker := &mockTracker{tasks: []TrackedTaskInfo{
			{GID: "sg_dup", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "first.com",
				TelemetryBps: 5 * mb, TelemetryValid: true},
			{GID: "sg_dup", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "second.com",
				TelemetryBps: 7 * mb, TelemetryValid: true},
			{GID: "sg_peer", Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "peer.com"},
		}}
		telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{
			"sg_dup":  makeWorkers(2, 2*1024*1024),
			"sg_peer": makeWorkers(1, 2*1024*1024),
		}}
		ct := newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker, telemetry,
		)
		defer ct.Stop()
		ct.tickAt(now)
		ct.mu.Lock()
		gi, in := ct.prevActiveGids["sg_dup"]
		occ := ct.prevActiveOccupancy["sg_dup"]
		ct.mu.Unlock()
		if !in || gi.Domain != "first.com" {
			t.Fatalf("dup GID must keep first row: %+v in=%v", gi, in)
		}
		if occ != 5*mb {
			t.Fatalf("dup GID occupancy=%d, want %d — first row counted once", occ, 5*mb)
		}
	})
}

// blockingOccupancy wedges the tick inside the occupancy provider call — the
// one provider invocation that runs outside c.mu — so a concurrent RemoveTask
// provably interleaves with an in-flight tick.
type blockingOccupancy struct {
	tasks   []TrackedTaskInfo
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingOccupancy) GetOccupancyTrackedTasks() []TrackedTaskInfo {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	out := make([]TrackedTaskInfo, len(b.tasks))
	copy(out, b.tasks)
	return out
}

// TestConvergence_LocalWindow_ConcurrentRemove runs RemoveTask while a tick is
// wedged inside the lock-free occupancy provider window. The victim's stored
// previous occupancy must survive RemoveTask long enough to feed the exit-side
// E (a wipe would degrade the whole tick to perturbUnknown and dirty even a
// high-margin task), the victim's state must never resurrect, and previous
// maps must stay consistent under the race detector.
func TestConvergence_LocalWindow_ConcurrentRemove(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	now := time.Now()

	victim := "sg_cr_victim"
	lowGid := "sg_cr_low"   // margin below victim exit → dirty either way
	highGid := "sg_cr_high" // margin above stored exit → dirty ONLY if wiped
	mkTask := func(gid, domain string, completed int64) TrackedTaskInfo {
		return TrackedTaskInfo{GID: gid, Status: "active", Scope: "wan",
			EnvKey: "testenv", Domain: domain, CompletedLength: completed}
	}
	tracker := &mockTracker{tasks: []TrackedTaskInfo{
		mkTask(victim, "victim.com", 10*mb),
		mkTask(lowGid, "peer.com", 10*mb),
		mkTask(highGid, "peer.com", 100*mb),
	}}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{
		victim:  makeWorkers(4, 2*1024*1024),
		lowGid:  makeWorkers(2, 2*1024*1024),
		highGid: makeWorkers(2, 2*1024*1024),
	}}
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry, recorder, &mockRateChecker{}, 0, 32,
	)
	defer ct.Stop()
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string, n time.Time) int64 { return 0 }

	// Stored exit-side value for the victim; peers only need membership.
	ct.InjectMacroOccupancyForTest(victim, 10*mb, true)
	ct.InjectMacroOccupancyForTest(lowGid, 10*mb, true)
	ct.InjectMacroOccupancyForTest(highGid, 10*mb, true)

	// Tick 1 — real tick produces prevActiveGids/prevActiveOccupancy.
	ct.tickAt(now)
	ct.mu.Lock()
	stored, ok := ct.prevActiveOccupancy[victim]
	ct.mu.Unlock()
	if !ok || stored != 10*mb {
		t.Fatalf("tick1 must store victim occupancy 10MB, got %d ok=%v", stored, ok)
	}

	// Prime pending verdicts between ticks (verdict state, not prev maps):
	//   low:  margin = 0.5*(10MB/2)*1 = 2.5MB < 10MB exit → affected
	//   high: margin = 0.5*(50MB/2)*1 = 12.5MB > 10MB exit → clean verdict
	ct.mu.Lock()
	sLow := ct.getOrCreateState(lowGid)
	sLow.phase = phaseProbingUp
	sLow.probeUpBaseline = 10 * mb
	sLow.probeUpBaselineWorkers = 2
	sLow.probeUpDelta = 1
	sHigh := ct.getOrCreateState(highGid)
	sHigh.phase = phaseProbingUp
	sHigh.probeUpBaseline = 50 * mb
	sHigh.probeUpBaselineWorkers = 2
	sHigh.probeUpDelta = 1
	ct.mu.Unlock()

	// Victim exits; tick 2 wedges inside the occupancy provider (c.mu free)
	// while RemoveTask deletes the live state.
	tracker.tasks = []TrackedTaskInfo{
		mkTask(lowGid, "peer.com", 10*mb),
		mkTask(highGid, "peer.com", 110*mb), // +10MB over 1s → raw 10MB < 50MB baseline → judged fail
	}
	gate := &blockingOccupancy{
		tasks:   tracker.tasks,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	ct.SetOccupancyProvider(gate)
	done := make(chan struct{})
	go func() {
		ct.tickAt(now.Add(1 * time.Second))
		close(done)
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tick never reached the occupancy provider window")
	}
	ct.RemoveTask(victim)
	close(gate.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tick did not finish after releasing provider")
	}

	ct.mu.Lock()
	_, resurrected := ct.states[victim]
	low := ct.states[lowGid]
	high := ct.states[highGid]
	prevOcc := make(map[string]int64, len(ct.prevActiveOccupancy))
	for k, v := range ct.prevActiveOccupancy {
		prevOcc[k] = v
	}
	ct.mu.Unlock()

	if resurrected {
		t.Fatal("RemoveTask'd state must not resurrect during the in-flight tick")
	}
	// Pending exit value consumed as a known quantity: the 10MB exit dirtied
	// only the inside-margin task; the high-margin task still judged clean —
	// impossible had RemoveTask erased prevActiveOccupancy (perturbUnknown
	// would have forced it dirty too).
	if low == nil || low.probeUpBaseline != 0 || low.probeUpDelta != 0 {
		t.Fatalf("low-margin probe must be cancelled dirty: %+v", low)
	}
	if _, recorded := recorder.records[lowGid]; recorded {
		t.Fatal("cancelled dirty verdict must not reach the peak recorder")
	}
	if high == nil || high.phase != phaseCeilingHit {
		t.Fatalf("high-margin probe must judge clean to CeilingHit, got %+v", high)
	}
	if _, recorded := recorder.records[highGid]; !recorded {
		t.Fatal("clean-judged failed verdict must reach the peak recorder")
	}
	if _, stillThere := prevOcc[victim]; stillThere || len(prevOcc) != 2 {
		t.Fatalf("post-tick prevOcc must hold only the two survivors, got %+v", prevOcc)
	}
}
