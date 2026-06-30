package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
)

// fireAndKill advances ticks until one kill fires, returning the tick count.
// Uses dead worker (speed=0) + healthy peer pattern.
func fireAndKill(t *testing.T, d *CDNDetector, clock *time.Time, ctrl *mockCDNControl) {
	t.Helper()
	prev := ctrl.killsCount()
	for i := 0; i < 30; i++ {
		advanceTick(d, clock)
		if ctrl.killsCount() > prev {
			return
		}
	}
	t.Fatal("expected a kill to fire within 30 ticks")
}

// TestCDNDetector_KillCountCap_DegradesToDrain verifies that after cdnMaxKillCount
// kills without improvement, the next fire degrades to DrainWorker instead of Kill.
func TestCDNDetector_KillCountCap_DegradesToDrain(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100, 206),        // dead: speed ~0, below dead floor
		matureWorker(2, 5*types.MB, 206), // healthy peer
	)
	d, clock := newTestDetector(ctrl)

	// Kill 3 times (cdnMaxKillCount=3). Each kill requires sustain(5s) + cooldown(10s) = 15s.
	for i := 0; i < cdnMaxKillCount; i++ {
		fireAndKill(t, d, clock, ctrl)
	}

	if got := ctrl.killsCount(); got != cdnMaxKillCount {
		t.Fatalf("expected %d kills before drain, got %d", cdnMaxKillCount, got)
	}

	// Advance past cooldown for the next fire, which should be a drain.
	prevDrains := ctrl.drainsCount()
	for i := 0; i < 30; i++ {
		advanceTick(d, clock)
		if ctrl.drainsCount() > prevDrains {
			break
		}
	}

	if got := ctrl.drainsCount(); got != 1 {
		t.Fatalf("expected 1 drain after %d kills, got %d", cdnMaxKillCount, got)
	}
	if got := ctrl.killsCount(); got != cdnMaxKillCount {
		t.Fatalf("expected kill count to stay at %d after drain, got %d", cdnMaxKillCount, got)
	}
}

// TestCDNDetector_KillCountReset_OnRecovery verifies that killCount resets to 0
// when the worker is no longer classified as dead/throttle (reason == "").
func TestCDNDetector_KillCountReset_OnRecovery(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(2, 5*types.MB, 206),
	)
	d, clock := newTestDetector(ctrl)

	// Kill 2 times (killCount=2, below cap).
	for i := 0; i < 2; i++ {
		fireAndKill(t, d, clock, ctrl)
	}
	if got := ctrl.killsCount(); got != 2 {
		t.Fatalf("expected 2 kills, got %d", got)
	}

	// Recover worker 1 to healthy speed → reason == "" → killCount reset.
	setStats(ctrl,
		matureWorker(1, 5*types.MB, 206),
		matureWorker(2, 5*types.MB, 206),
	)
	for i := 0; i < 5; i++ {
		advanceTick(d, clock)
	}

	// Make worker 1 dead again → should start fresh (killCount=0 → 1st kill, not 3rd → drain).
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(2, 5*types.MB, 206),
	)

	prevKills := ctrl.killsCount()
	prevDrains := ctrl.drainsCount()
	for i := 0; i < 30; i++ {
		advanceTick(d, clock)
		if ctrl.killsCount() > prevKills {
			break
		}
	}

	// Should be a kill (not drain) because killCount was reset.
	if ctrl.killsCount() != prevKills+1 {
		t.Fatalf("expected 1 kill after recovery+reset, got %d kills", ctrl.killsCount()-prevKills)
	}
	if ctrl.drainsCount() != prevDrains {
		t.Fatalf("expected 0 drains after recovery+reset, got %d drains", ctrl.drainsCount()-prevDrains)
	}
}

// TestCDNDetector_KillCountReset_OnWarmup verifies that killCount resets when
// a worker enters warmup (isWarmup → resetHeld → resetKillCount).
func TestCDNDetector_KillCountReset_OnWarmup(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(2, 5*types.MB, 206),
	)
	d, clock := newTestDetector(ctrl)

	// Kill 2 times.
	for i := 0; i < 2; i++ {
		fireAndKill(t, d, clock, ctrl)
	}

	// Make worker 1 warmup (young connection, low bytes).
	warmup := matureWorker(1, 100, 206)
	warmup.WorkerStartUnix = d.now().UnixNano()
	warmup.SessionBytes = 0
	setStats(ctrl, warmup, matureWorker(2, 5*types.MB, 206))
	for i := 0; i < 5; i++ {
		advanceTick(d, clock)
	}

	// Worker 1 matures and is dead again → killCount should be 0 (reset by warmup).
	mature := matureWorker(1, 100, 206)
	setStats(ctrl, mature, matureWorker(2, 5*types.MB, 206))

	prevKills := ctrl.killsCount()
	prevDrains := ctrl.drainsCount()
	for i := 0; i < 30; i++ {
		advanceTick(d, clock)
		if ctrl.killsCount() > prevKills {
			break
		}
	}

	if ctrl.killsCount() != prevKills+1 {
		t.Fatalf("expected kill (not drain) after warmup reset, got kills=%d drains=%d",
			ctrl.killsCount()-prevKills, ctrl.drainsCount()-prevDrains)
	}
}

// TestCDNDetector_KillCountAbsentEviction verifies that killCount is cleared
// when a worker is evicted after being absent for cdnWorkerEvictTicks+1 ticks.
func TestCDNDetector_KillCountAbsentEviction(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(2, 5*types.MB, 206),
	)
	d, clock := newTestDetector(ctrl)

	// Kill 2 times.
	for i := 0; i < 2; i++ {
		fireAndKill(t, d, clock, ctrl)
	}

	// Remove worker 1 from stats (simulating drain/exit).
	setStats(ctrl, matureWorker(2, 5*types.MB, 206))
	for i := 0; i < cdnWorkerEvictTicks+2; i++ {
		advanceTick(d, clock)
	}

	// Worker 1 state should be evicted.
	d.mu.Lock()
	gids := d.workerState["sg_test"]
	if gids != nil {
		if _, exists := gids[1]; exists {
			d.mu.Unlock()
			t.Fatal("expected worker 1 state to be evicted after absence")
		}
	}
	d.mu.Unlock()

	// Re-add worker 1 as dead → killCount should be 0 (fresh state).
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(2, 5*types.MB, 206),
	)

	prevKills := ctrl.killsCount()
	prevDrains := ctrl.drainsCount()
	for i := 0; i < 30; i++ {
		advanceTick(d, clock)
		if ctrl.killsCount() > prevKills {
			break
		}
	}

	if ctrl.killsCount() != prevKills+1 {
		t.Fatalf("expected kill (not drain) after absent eviction, got kills=%d drains=%d",
			ctrl.killsCount()-prevKills, ctrl.drainsCount()-prevDrains)
	}
}

// TestCDNDetector_DrainAtMostOnePerGidPerTick verifies that at most one drain
// action fires per gid per tick, even when multiple workers qualify.
func TestCDNDetector_DrainAtMostOnePerGidPerTick(t *testing.T) {
	ctrl := newMockCDNControl()
	// Two dead workers + one healthy peer. Both dead have killCount >= cdnMaxKillCount.
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(3, 100, 206),
		matureWorker(2, 5*types.MB, 206),
	)
	d, clock := newTestDetector(ctrl)

	// Kill worker 1 three times to reach killCount cap.
	for i := 0; i < cdnMaxKillCount; i++ {
		fireAndKill(t, d, clock, ctrl)
	}

	// Now worker 1 should drain on next fire. But worker 3 also needs to be
	// killed 3 times first. Let's just verify that after enough ticks, at most
	// one drain happens per tick.
	prevDrains := 0
	for i := 0; i < 50; i++ {
		advanceTick(d, clock)
		nowDrains := ctrl.drainsCount()
		if nowDrains-prevDrains > 1 {
			t.Errorf("tick %d: %d drains in one tick, want <= 1", i, nowDrains-prevDrains)
		}
		prevDrains = nowDrains
	}
	if ctrl.drainsCount() == 0 {
		t.Error("expected at least one drain over 50 ticks")
	}
}
