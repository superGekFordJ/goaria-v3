package monitor

import (
	"sync"
	"testing"
)

// ==================== RecordPeakEfficiency Tests====================

func TestRecordPeakEfficiency_FirstRecording(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-001", 100000000, "https://example.com/file.zip", 8, "active")

	tracker.RecordPeakEfficiency("sg-peak-001", 50*1024*1024, 10)

	tracked := tracker.tasks["sg-peak-001"]
	if tracked.PeakSpeed != 50*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d", tracked.PeakSpeed, 50*1024*1024)
	}
	if tracked.PeakThreadCount != 10 {
		t.Errorf("PeakThreadCount = %d, want 10", tracked.PeakThreadCount)
	}
}

func TestRecordPeakEfficiency_HigherThroughputSameEfficiency(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-002", 100000000, "https://example.com/file.zip", 8, "active")

	// First record: 50MB/s @ 10 workers = 5MB/s/thread
	tracker.RecordPeakEfficiency("sg-peak-002", 50*1024*1024, 10)
	// Second record: 60MB/s @ 12 workers = 5MB/s/thread (same eff, higher throughput)
	tracker.RecordPeakEfficiency("sg-peak-002", 60*1024*1024, 12)

	tracked := tracker.tasks["sg-peak-002"]
	if tracked.PeakSpeed != 60*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (higher throughput)", tracked.PeakSpeed, 60*1024*1024)
	}
	if tracked.PeakThreadCount != 12 {
		t.Errorf("PeakThreadCount = %d, want 12 (accepted same-eff higher throughput)", tracked.PeakThreadCount)
	}
}

func TestRecordPeakEfficiency_SameThroughputFewerWorkers(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-003", 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv("sg-peak-003", "wan", 50, "example.com", "envA")

	// First: 50MB/s @ 10 workers
	tracker.RecordPeakEfficiency("sg-peak-003", 50*1024*1024, 10)
	if tracker.tasks["sg-peak-003"].PeakEnvKey != "envA" {
		t.Fatalf("setup PeakEnvKey = %q, want envA", tracker.tasks["sg-peak-003"].PeakEnvKey)
	}

	tracker.mu.Lock()
	tracker.tasks["sg-peak-003"].CurrentEnvKey = "envB"
	tracker.mu.Unlock()

	// Second: 50MB/s @ 8 workers (same throughput, fewer workers, higher eff).
	// ThreadCount-only accept — PeakSpeed unchanged → PeakEnvKey must stay envA.
	tracker.RecordPeakEfficiency("sg-peak-003", 50*1024*1024, 8)

	tracked := tracker.tasks["sg-peak-003"]
	if tracked.PeakThreadCount != 8 {
		t.Errorf("PeakThreadCount = %d, want 8 (fewer workers at same throughput)", tracked.PeakThreadCount)
	}
	if tracked.PeakSpeed != 50*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (unchanged on ThreadCount-only accept)", tracked.PeakSpeed, 50*1024*1024)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA (ThreadCount-only must not refresh)", tracked.PeakEnvKey)
	}
}

// TestRecordPeakEfficiency_RejectBloatedN is the critical regression test:
// 50MB/s@10 → 53MB/s@32 should NOT overwrite peakWorkers (efficiency crashes from 5MB/s to 1.66MB/s)
func TestRecordPeakEfficiency_RejectBloatedN(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-004", 100000000, "https://example.com/file.zip", 8, "active")

	// Record efficient working point: 50MB/s @ 10 workers = 5MB/s/thread
	tracker.RecordPeakEfficiency("sg-peak-004", 50*1024*1024, 10)

	// Attempt bloated overwrite: 53MB/s @ 32 workers = 1.66MB/s/thread (eff crashes -67%)
	tracker.RecordPeakEfficiency("sg-peak-004", 53*1024*1024, 32)

	tracked := tracker.tasks["sg-peak-004"]
	// PeakSpeed should update (53 > 50, absolute throughput signal for V_target)
	if tracked.PeakSpeed != 53*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (absolute throughput should update)", tracked.PeakSpeed, 53*1024*1024)
	}
	// PeakThreadCount must NOT change — efficiency crashed, reject bloated N
	if tracked.PeakThreadCount != 10 {
		t.Errorf("PeakThreadCount = %d, want 10 (reject bloated N — efficiency guard)", tracked.PeakThreadCount)
	}
}

func TestRecordPeakEfficiency_ZeroWorkers(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-005", 100000000, "https://example.com/file.zip", 8, "active")

	tracker.RecordPeakEfficiency("sg-peak-005", 100*1024*1024, 0)

	tracked := tracker.tasks["sg-peak-005"]
	if tracked.PeakThreadCount != 0 {
		t.Errorf("PeakThreadCount = %d, want 0 (zero workers should be rejected)", tracked.PeakThreadCount)
	}
}

func TestRecordPeakEfficiency_NonexistentTask(t *testing.T) {
	tracker := NewTaskTracker()
	// Should not panic
	tracker.RecordPeakEfficiency("nonexistent", 100*1024*1024, 10)
}

func TestRecordPeakEfficiency_ConcurrentRace(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-race", 100000000, "https://example.com/file.zip", 8, "active")

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			speed := int64(50*1024*1024 + idx*1024*1024)
			workers := 10 + idx%5
			tracker.RecordPeakEfficiency("sg-peak-race", speed, workers)
		}(i)
	}
	wg.Wait()
	// Should not panic or race
	tracked := tracker.tasks["sg-peak-race"]
	if tracked.PeakThreadCount == 0 {
		t.Error("Expected PeakThreadCount to be set after concurrent writes")
	}
}

// TestRecordPeakEfficiency_M6_BestEffCreepPrevention verifies that the bestEff-anchored
// guard (C2 fix) prevents N creep via repeated borderline adoptions. With the old curEff
// anchoring, each marginal adoption lowered curEff, allowing progressively worse
// efficiency to pass. With bestEff anchoring, the guard is always referenced to the
// session-best efficiency, so borderline-high-N proposals are rejected.
func TestRecordPeakEfficiency_M6_BestEffCreepPrevention(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-creep", 100000000, "https://example.com/file.zip", 8, "active")

	// Record efficient working point: 50MB/s @ 10 workers = 5MB/s/thread
	tracker.RecordPeakEfficiency("sg-peak-creep", 50*1024*1024, 10)

	// Attempt borderline adoption: 56MB/s @ 16 workers = 3.5MB/s/thread
	// Throughput up 12% (>5% noise gate), but eff=3.5 < guardEff=4.25 (5*0.85)
	// → PeakSpeed updates, PeakThreadCount stays 10
	tracker.RecordPeakEfficiency("sg-peak-creep", 56*1024*1024, 16)

	tracked := tracker.tasks["sg-peak-creep"]
	if tracked.PeakThreadCount != 10 {
		t.Errorf("PeakThreadCount = %d, want 10 (bestEff guard rejects eff=3.5 < guardEff=4.25)", tracked.PeakThreadCount)
	}
	if tracked.PeakSpeed != 56*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (absolute throughput should still update)", tracked.PeakSpeed, 56*1024*1024)
	}

	// Second borderline attempt: 62MB/s @ 20 workers = 3.1MB/s/thread
	// With old curEff anchoring: curEff would be 3.5 (from previous rejected record's
	// PeakSpeed/PeakThreadCount = 56/16=3.5), guardEff=3.5*0.85=2.975, 3.1>2.975 → accepted!
	// With bestEff anchoring: guardEff=5*0.85=4.25, 3.1<4.25 → rejected.
	tracker.RecordPeakEfficiency("sg-peak-creep", 62*1024*1024, 20)

	tracked = tracker.tasks["sg-peak-creep"]
	if tracked.PeakThreadCount != 10 {
		t.Errorf("PeakThreadCount = %d, want 10 (bestEff guard prevents N creep on 2nd attempt)", tracked.PeakThreadCount)
	}
	if tracked.PeakSpeed != 62*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (absolute throughput should update)", tracked.PeakSpeed, 62*1024*1024)
	}

	// Verify BestEff is anchored to the session best (5MB/s/thread)
	if tracked.BestEff != 5*1024*1024 {
		t.Errorf("BestEff = %d, want %d (should be session best efficiency)", tracked.BestEff, 5*1024*1024)
	}
}

// TestRecordPeakEfficiency_RejectSewageMonster is the critical Bug 2 regression test:
// Record [32MB/s @ 32 threads] (eff=1MB/s/thread), then attempt [4MB/s @ 4 threads]
// (same eff=1MB/s/thread). The old code would adopt the smaller peakWorkers (4 < 32)
// while keeping the old PeakSpeed (32MB/s), creating a physically impossible
// "缝合怪" record: [32MB/s peak, 4 workers]. The new peakSpeedGuardBand (0.90)
// requires incoming speed ≥ 90% of peak (28.8MB/s) — 4MB/s is way below, so rejected.
func TestRecordPeakEfficiency_RejectSewageMonster(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-peak-sewage", 100000000, "https://example.com/file.zip", 32, "active")

	// Record: 32MB/s @ 32 threads = 1MB/s/thread
	tracker.RecordPeakEfficiency("sg-peak-sewage", 32*1024*1024, 32)

	tracked := tracker.tasks["sg-peak-sewage"]
	if tracked.PeakSpeed != 32*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want %d (initial record)", tracked.PeakSpeed, 32*1024*1024)
	}
	if tracked.PeakThreadCount != 32 {
		t.Fatalf("PeakThreadCount = %d, want 32 (initial record)", tracked.PeakThreadCount)
	}

	// Attempt "缝合怪" adoption: 4MB/s @ 4 threads = 1MB/s/thread (same efficiency)
	// But speed is only 12.5% of peak — far below 90% guard.
	tracker.RecordPeakEfficiency("sg-peak-sewage", 4*1024*1024, 4)

	tracked = tracker.tasks["sg-peak-sewage"]
	// PeakThreadCount must NOT change — speed guard rejects fraction-of-peak speed
	if tracked.PeakThreadCount != 32 {
		t.Errorf("PeakThreadCount = %d, want 32 (peakSpeedGuardBand rejects 4MB/s < 32*0.9=28.8MB/s)",
			tracked.PeakThreadCount)
	}
	// PeakSpeed should also NOT change (4 < 32, no update)
	if tracked.PeakSpeed != 32*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d (should not decrease)", tracked.PeakSpeed, 32*1024*1024)
	}
}

func TestRecordPeakEfficiency_WritesPeakEnvKeyOnFirstWrite(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg-peak-env-first"
	tracker.EnsureTrackedFromEvent(gid, 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracker.RecordPeakEfficiency(gid, 50*1024*1024, 10)

	tracked := tracker.tasks[gid]
	if tracked.PeakSpeed != 50*1024*1024 || tracked.PeakThreadCount != 10 {
		t.Fatalf("PeakSpeed=%d PeakThreadCount=%d", tracked.PeakSpeed, tracked.PeakThreadCount)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA on first PeakSpeed accept", tracked.PeakEnvKey)
	}
}

func TestRecordPeakEfficiency_PeakEnvKeyFollowsCurrentOnNewPeak(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg-peak-env-follow"
	tracker.EnsureTrackedFromEvent(gid, 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracker.RecordPeakEfficiency(gid, 50*1024*1024, 10)
	if tracker.tasks[gid].PeakEnvKey != "envA" {
		t.Fatalf("setup PeakEnvKey = %q, want envA", tracker.tasks[gid].PeakEnvKey)
	}

	tracker.mu.Lock()
	tracker.tasks[gid].CurrentEnvKey = "envB"
	tracker.mu.Unlock()

	// Higher throughput same-ish efficiency → accept PeakSpeed, refresh PeakEnvKey
	tracker.RecordPeakEfficiency(gid, 60*1024*1024, 12)

	tracked := tracker.tasks[gid]
	if tracked.PeakSpeed != 60*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want %d", tracked.PeakSpeed, 60*1024*1024)
	}
	if tracked.PeakEnvKey != "envB" {
		t.Errorf("PeakEnvKey = %q, want envB after mid-download env change", tracked.PeakEnvKey)
	}
}

func TestRecordPeakEfficiency_AbsoluteThroughputOnlyWritesPeakEnvKey(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg-peak-env-abs"
	tracker.EnsureTrackedFromEvent(gid, 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracker.RecordPeakEfficiency(gid, 50*1024*1024, 10)

	tracker.mu.Lock()
	tracker.tasks[gid].CurrentEnvKey = "envB"
	tracker.mu.Unlock()

	// Bloated N: PeakSpeed rises, PeakThreadCount stays — still accepts PeakSpeed
	tracker.RecordPeakEfficiency(gid, 53*1024*1024, 32)

	tracked := tracker.tasks[gid]
	if tracked.PeakSpeed != 53*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want %d", tracked.PeakSpeed, 53*1024*1024)
	}
	if tracked.PeakThreadCount != 10 {
		t.Fatalf("PeakThreadCount = %d, want 10", tracked.PeakThreadCount)
	}
	if tracked.PeakEnvKey != "envB" {
		t.Errorf("PeakEnvKey = %q, want envB on absolute-throughput-only PeakSpeed accept", tracked.PeakEnvKey)
	}
}

func TestRecordPeakEfficiency_EmptyCurrentEnvKeyDoesNotWipePeakEnvKey(t *testing.T) {
	tracker := NewTaskTracker()
	gid := "sg-peak-env-empty"
	tracker.EnsureTrackedFromEvent(gid, 100000000, "https://example.com/file.zip", 8, "active")
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracker.RecordPeakEfficiency(gid, 50*1024*1024, 10)
	if tracker.tasks[gid].PeakEnvKey != "envA" {
		t.Fatalf("setup PeakEnvKey = %q, want envA", tracker.tasks[gid].PeakEnvKey)
	}

	tracker.mu.Lock()
	tracker.tasks[gid].CurrentEnvKey = ""
	tracker.mu.Unlock()

	tracker.RecordPeakEfficiency(gid, 60*1024*1024, 12)

	tracked := tracker.tasks[gid]
	if tracked.PeakSpeed != 60*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want %d", tracked.PeakSpeed, 60*1024*1024)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA (empty CurrentEnvKey must not wipe)", tracked.PeakEnvKey)
	}
}

// TestTaskTracker_PeakEnvKeyAttribution verifies that PeakEnvKey is set to the
// CurrentEnvKey at the time PeakSpeed is achieved, and does NOT change when
// CurrentEnvKey later changes (if the new speed doesn't exceed the peak).
func TestTaskTracker_PeakEnvKeyAttribution(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	tracker := NewTaskTracker()

	// 1. Create task and set env=envA
	gid := "sg-peak-env-attribution"
	tracker.SetThreadInfo(gid, 8, false)
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected task to be tracked")
	}
	// Set CompletedLength > MinFileSize (50MB) so sampleSpeedInternal updates PeakSpeed.
	tracked.TotalLength = 100 * 1024 * 1024
	tracked.CompletedLength = 60 * 1024 * 1024

	// 2. Simulate speed sampling that achieves a new peak in envA.
	//    sampleSpeedInternal with threshold=1 (headless) updates PeakSpeed+PeakEnvKey.
	tracker.sampleSpeedInternal(tracked, 10*1024*1024, 1)

	if tracked.PeakSpeed != 10*1024*1024 {
		t.Fatalf("PeakSpeed = %d, want 10MB", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Fatalf("PeakEnvKey = %q, want envA (should match CurrentEnvKey at peak time)", tracked.PeakEnvKey)
	}

	// 3. Network changes → CurrentEnvKey switches to envB.
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envB")
	if tracked.CurrentEnvKey != "envB" {
		t.Fatalf("CurrentEnvKey = %q, want envB", tracked.CurrentEnvKey)
	}

	// 4. Simulate speed sampling that does NOT exceed the peak.
	//    PeakEnvKey must remain envA (peak attribution is immutable unless exceeded).
	tracker.sampleSpeedInternal(tracked, 5*1024*1024, 1)

	if tracked.PeakSpeed != 10*1024*1024 {
		t.Errorf("PeakSpeed = %d, want 10MB (unchanged)", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA (peak attribution must not change when speed doesn't exceed peak)", tracked.PeakEnvKey)
	}

	// 5. Speed now exceeds peak in envB → PeakEnvKey should update to envB.
	tracker.sampleSpeedInternal(tracked, 20*1024*1024, 1)

	if tracked.PeakSpeed != 20*1024*1024 {
		t.Errorf("PeakSpeed = %d, want 20MB (new peak)", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envB" {
		t.Errorf("PeakEnvKey = %q, want envB (should update to current env when peak exceeded)", tracked.PeakEnvKey)
	}
}

// TestSampleSpeedInternal_EmptyCurrentEnvKeyDoesNotWipePeakEnvKey verifies Aria2
// peak accept via acceptPeakSpeed does not clear PeakEnvKey when Current is empty.
func TestSampleSpeedInternal_EmptyCurrentEnvKeyDoesNotWipePeakEnvKey(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	tracker := NewTaskTracker()
	gid := "sg-sample-empty-current"
	tracker.SetThreadInfo(gid, 8, false)
	tracker.SetScopeAndEnv(gid, "wan", 50, "example.com", "envA")

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected task to be tracked")
	}
	tracked.TotalLength = 100 * 1024 * 1024
	tracked.CompletedLength = 60 * 1024 * 1024
	tracked.CurrentEnvKey = ""

	const newPeak = int64(15 * 1024 * 1024)
	tracker.sampleSpeedInternal(tracked, newPeak, 1)

	if tracked.PeakSpeed != newPeak {
		t.Errorf("PeakSpeed = %d, want %d", tracked.PeakSpeed, newPeak)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("PeakEnvKey = %q, want envA (empty Current must not wipe)", tracked.PeakEnvKey)
	}
}
