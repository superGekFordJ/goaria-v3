package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

// TestConvergence_CrossEnvIsolation_ApprovedDelta verifies that approvedDelta
// keys on scope+envKey, so two tasks in the same scope but different envKeys
// accumulate independently — a ScaleUp in envA does not consume envB's headroom.
func TestConvergence_CrossEnvIsolation_ApprovedDelta(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// globalPeak = 10 MB/s per env. vThreadAvg = 10 MB/s (1 thread).
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "envA")
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "envB")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	gidA := "sg_env_a"
	gidB := "sg_env_b"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gidA, Status: "active", Scope: "wan", EnvKey: "envA", Domain: "example.com", IsKeepAlive: false, CompletedLength: 60 * 1024 * 1024},
			{GID: gidB, Status: "active", Scope: "wan", EnvKey: "envB", Domain: "example.com", IsKeepAlive: false, CompletedLength: 60 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gidA: makeWorkers(8, 2*1024*1024),
			gidB: makeWorkers(8, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear("example.com")

	for _, gid := range []string{gidA, gidB} {
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseStable
		s.bestEff = 1_310_720
		s.peakWorkers = 8
		s.prevCompleted = 10 * 1024 * 1024
		setPrevSampleAgoState(s, 5*time.Second)
		ct.mu.Unlock()
	}

	approvedDelta := make(map[string]int)

	// Process task A (envA): should get +1 and consume envA's headroom.
	psA, okA := ct.processTask(tracker.tasks[0], false, approvedDelta)
	if !okA || psA.delta != 1 {
		t.Fatalf("expected envA task probe-up +1, got ok=%v delta=%d", okA, psA.delta)
	}
	if psA.envKey != "envA" {
		t.Fatalf("expected pendingScale envKey=envA, got %q", psA.envKey)
	}
	if psA.delta > 0 {
		approvedDelta[psA.scope+psA.envKey] += psA.delta
	}

	// Process task B (envB): should also get +1 because approvedDelta key is
	// "wanenvB", independent from "wanenvA". envA's consumption doesn't block envB.
	psB, okB := ct.processTask(tracker.tasks[1], false, approvedDelta)
	if !okB || psB.delta != 1 {
		t.Fatalf("expected envB task probe-up +1 (independent approvedDelta), got ok=%v delta=%d", okB, psB.delta)
	}
	if psB.envKey != "envB" {
		t.Fatalf("expected pendingScale envKey=envB, got %q", psB.envKey)
	}
	if psB.delta > 0 {
		approvedDelta[psB.scope+psB.envKey] += psB.delta
	}

	// Verify approvedDelta has two separate keys.
	if approvedDelta["wanenvA"] != 1 {
		t.Errorf("approvedDelta[wanenvA] = %d, want 1", approvedDelta["wanenvA"])
	}
	if approvedDelta["wanenvB"] != 1 {
		t.Errorf("approvedDelta[wanenvB] = %d, want 1", approvedDelta["wanenvB"])
	}
}

// TestConvergence_BandwidthRelease_CrossEnvNoBeneficiary verifies that
// bandwidthRelease only elects candidates with matching envKey — a completed
// task in envA should NOT trigger ScaleUp for a task in envB, even if they
// share the same domain and scope.
func TestConvergence_BandwidthRelease_CrossEnvNoBeneficiary(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Provide enough global peak so V_available passes.
	speedstats.AddRecordV2(100*1024*1024, 1, 100*1024*1024, false, 50, "example.com", "wan", "envA")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	// Only task B (envB) is active; completed task was in envA.
	beneficiaryGid := "sg_env_b_only"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", EnvKey: "envB", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	// Completed task was in envA — different env from the active task (envB).
	completedGid := "sg_completed_envA"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "envA"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan", EnvKey: "envB"}},
		map[string]bool{},
		nil,
	)
	for _, r := range releases {
		if r.gid == beneficiaryGid {
			t.Fatal("expected bandwidthRelease to NOT elect envB task when completed task was envA (cross-env isolation)")
		}
	}
}

// TestConvergence_BandwidthRelease_SameEnvBeneficiary verifies that
// bandwidthRelease DOES elect a candidate when envKey matches, confirming the
// positive case that the cross-env test above guards against.
func TestConvergence_BandwidthRelease_SameEnvBeneficiary(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	speedstats.AddRecordV2(100*1024*1024, 1, 100*1024*1024, false, 50, "example.com", "wan", "envA")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	beneficiaryGid := "sg_env_a_beneficiary"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", EnvKey: "envA", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_completed_same_env"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "envA"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan", EnvKey: "envA"}},
		map[string]bool{},
		nil,
	)
	found := false
	for _, r := range releases {
		if r.gid == beneficiaryGid {
			found = true
			if r.envKey != "envA" {
				t.Errorf("expected release envKey=envA, got %q", r.envKey)
			}
		}
	}
	if !found {
		t.Fatal("expected bandwidthRelease to elect same-env beneficiary")
	}
}
