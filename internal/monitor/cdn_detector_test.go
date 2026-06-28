package monitor

import (
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
)

// mockCDNControl records Kill/SetSlowThreshold calls and serves fixed stats.
type mockCDNControl struct {
	mu         sync.Mutex
	stats      map[string][]types.WorkerSnapshot
	kills      []mockKill
	thresholds map[string]float64
}

type mockKill struct {
	gid      string
	workerID int
}

func newMockCDNControl() *mockCDNControl {
	return &mockCDNControl{
		stats:      make(map[string][]types.WorkerSnapshot),
		thresholds: make(map[string]float64),
	}
}

func (m *mockCDNControl) GetWorkerStats(gid string) []types.WorkerSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.stats[gid]
	if len(src) == 0 {
		return nil
	}
	out := make([]types.WorkerSnapshot, len(src))
	copy(out, src)
	return out
}

func (m *mockCDNControl) KillWorker(gid string, workerID int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kills = append(m.kills, mockKill{gid, workerID})
	return true
}

func (m *mockCDNControl) SetSlowWorkerThreshold(gid string, v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.thresholds[gid] = v
}

func (m *mockCDNControl) killsCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.kills)
}

// newTestDetector builds a detector wired to a mock control and a controllable
// clock. The returned clock is advanced by callers to drive ticks.
func newTestDetector(ctrl *mockCDNControl) (*CDNDetector, *time.Time) {
	clock := time.Now()
	d := NewCDNDetector(ctrl, nil, func() []string { return []string{"sg_test"} })
	d.now = func() time.Time { return clock }
	return d, &clock
}

// setStats replaces the mock stats for "test".
func setStats(ctrl *mockCDNControl, snapshots ...types.WorkerSnapshot) {
	ctrl.mu.Lock()
	ctrl.stats["test"] = snapshots
	ctrl.mu.Unlock()
}

// advanceTick moves the clock forward one interval and runs a detector tick.
func advanceTick(d *CDNDetector, clock *time.Time) {
	*clock = clock.Add(cdnDetectorInterval)
	d.tick()
}

// matureWorker returns a snapshot that is out of warmup (old connection, >1MB
// cumulative, not limiter-blocked, not hedged) with the given speed/status.
func matureWorker(id int, speed float64, status int32) types.WorkerSnapshot {
	return types.WorkerSnapshot{
		WorkerID:         id,
		EMASpeed:         speed,
		WorkerStartUnix:  time.Now().Add(-60 * time.Second).UnixNano(),
		SessionBytes:     5 * types.MB,
		HTTPStatus:       status,
		LastActivityUnix: time.Now().UnixNano(),
	}
}

// TestCDNDetector_ThresholdTakeover verifies SetSlowWorkerThreshold(0) is
// issued when a gid reports >=2 workers. It is re-issued idempotently every
// tick so a fresh downloader after pause/resume is re-armed.
func TestCDNDetector_ThresholdTakeover(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl, matureWorker(1, 5*types.MB, 206), matureWorker(2, 5*types.MB, 206))
	d, clock := newTestDetector(ctrl)

	advanceTick(d, clock)
	if got := ctrl.thresholds["test"]; got != 0 {
		t.Errorf("threshold after first tick = %v, want 0 (takeover)", got)
	}
	// Subsequent ticks re-issue the same idempotent value (stays 0).
	advanceTick(d, clock)
	if got := ctrl.thresholds["test"]; got != 0 {
		t.Errorf("threshold changed on second tick = %v, want 0", got)
	}
}

// TestCDNDetector_WarmupExemption (P1): a young/low-volume worker is never
// killed even if it is the slowest.
func TestCDNDetector_WarmupExemption(t *testing.T) {
	ctrl := newMockCDNControl()
	fast := matureWorker(2, 5*types.MB, 206)
	newborn := matureWorker(1, 1*types.KB, 206)
	newborn.WorkerStartUnix = time.Now().UnixNano() // just started
	newborn.SessionBytes = 0
	setStats(ctrl, newborn, fast)
	d, clock := newTestDetector(ctrl)
	for i := 0; i < 20; i++ {
		advanceTick(d, clock)
	}
	if ctrl.killsCount() != 0 {
		t.Errorf("warmup worker killed %d times, want 0", ctrl.killsCount())
	}
}

// TestCDNDetector_ThrottleKill (P4): a steady low-speed worker with a clearly
// faster healthy peer is killed after the sustain duration.
func TestCDNDetector_ThrottleKill(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100*types.KB, 206), // throttled: steady 100KB/s
		matureWorker(2, 5*types.MB, 206),   // healthy peer: 5MB/s
	)
	d, clock := newTestDetector(ctrl)
	// Need >=10 ticks to fill the window + 5 ticks sustain.
	for i := 0; i < 18; i++ {
		advanceTick(d, clock)
	}
	if ctrl.killsCount() == 0 {
		t.Fatal("expected throttled worker to be killed after sustain")
	}
	if ctrl.kills[0].workerID != 1 {
		t.Errorf("killed worker %d, want 1 (the throttled one)", ctrl.kills[0].workerID)
	}
}

// TestCDNDetector_ThrottleSustainDebounce verifies no kill fires before the
// sustain duration elapses.
func TestCDNDetector_ThrottleSustainDebounce(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100*types.KB, 206),
		matureWorker(2, 5*types.MB, 206),
	)
	d, clock := newTestDetector(ctrl)
	// The verdict holds from the first sample; sustain is 5s, so the kill lands
	// on the 6th tick. Run 5 ticks and confirm no kill yet.
	for i := 0; i < 5; i++ {
		advanceTick(d, clock)
	}
	if ctrl.killsCount() != 0 {
		t.Fatalf("killed before sustain duration: %d kills, want 0", ctrl.killsCount())
	}
	// One more tick crosses the 5s sustain threshold → exactly one kill.
	advanceTick(d, clock)
	if got := ctrl.killsCount(); got != 1 {
		t.Errorf("after sustain: %d kills, want 1", got)
	}
}

// TestCDNDetector_DeadKill (P3): a near-zero worker with a healthy peer is killed.
func TestCDNDetector_DeadKill(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100, 206),        // ~0, below dead floor (2KB)
		matureWorker(2, 5*types.MB, 206), // healthy peer
	)
	d, clock := newTestDetector(ctrl)
	for i := 0; i < 18; i++ {
		advanceTick(d, clock)
	}
	if ctrl.killsCount() == 0 {
		t.Fatal("expected dead worker to be killed")
	}
	if ctrl.kills[0].workerID != 1 {
		t.Errorf("killed worker %d, want 1 (the dead one)", ctrl.kills[0].workerID)
	}
}

// TestCDNDetector_AllSlowNoKill: when all peers are uniformly slow (no healthy
// peer above the floor), no kill fires — global low ceiling is left to macro.
func TestCDNDetector_AllSlowNoKill(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100*types.KB, 206),
		matureWorker(2, 120*types.KB, 206),
	)
	d, clock := newTestDetector(ctrl)
	for i := 0; i < 18; i++ {
		advanceTick(d, clock)
	}
	if ctrl.killsCount() != 0 {
		t.Errorf("killed %d during all-slow, want 0 (no healthy peer floor)", ctrl.killsCount())
	}
}

// TestCDNDetector_AllDeadNoKill: when all workers are near-zero (no healthy
// peer), no kill fires — avoid WAF reconnect storms.
func TestCDNDetector_AllDeadNoKill(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(2, 100, 206),
	)
	d, clock := newTestDetector(ctrl)
	for i := 0; i < 18; i++ {
		advanceTick(d, clock)
	}
	if ctrl.killsCount() != 0 {
		t.Errorf("killed %d during all-dead, want 0 (avoid reconnect storm)", ctrl.killsCount())
	}
}

// TestCDNDetector_PoisonNoKill (P2): a 4xx worker is not killed (reconnect
// would just re-hit the wall).
func TestCDNDetector_PoisonNoKill(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100*types.KB, 403), // poisoned
		matureWorker(2, 5*types.MB, 206),
	)
	d, clock := newTestDetector(ctrl)
	for i := 0; i < 18; i++ {
		advanceTick(d, clock)
	}
	if ctrl.killsCount() != 0 {
		t.Errorf("killed 4xx-poisoned worker %d times, want 0", ctrl.killsCount())
	}
}

// TestCDNDetector_SingleWorkerNoKill: currentWorkers <= 1 never acts.
func TestCDNDetector_SingleWorkerNoKill(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl, matureWorker(1, 100, 206))
	d, clock := newTestDetector(ctrl)
	for i := 0; i < 18; i++ {
		advanceTick(d, clock)
	}
	if ctrl.killsCount() != 0 {
		t.Errorf("killed single worker %d times, want 0", ctrl.killsCount())
	}
}

// TestCDNDetector_OneKillPerTick verifies at most one Kill per gid per tick even
// when two workers both qualify.
func TestCDNDetector_OneKillPerTick(t *testing.T) {
	ctrl := newMockCDNControl()
	// Two dead workers + one healthy peer. Both dead qualify but only one kill/tick.
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(3, 100, 206),
		matureWorker(2, 5*types.MB, 206),
	)
	d, clock := newTestDetector(ctrl)
	// Each tick may kill at most one worker (two dead workers alternate, each
	// gated by its own per-worker cooldown).
	prev := 0
	for i := 0; i < 30; i++ {
		advanceTick(d, clock)
		now := ctrl.killsCount()
		if now-prev > 1 {
			t.Errorf("tick %d: %d kills in one tick, want <= 1", i, now-prev)
		}
		prev = now
	}
	if ctrl.killsCount() == 0 {
		t.Error("expected at least one kill over 30 ticks")
	}
}

// TestCDNDetector_Cooldown verifies a killed worker is not killed again within
// the cooldown window. The test isolates the cooldown path by advancing past
// cdnSustainDuration (so the sustain check passes) while staying within
// cdnActionCooldown, then advancing past the cooldown to confirm the re-kill.
func TestCDNDetector_Cooldown(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl,
		matureWorker(1, 100, 206),
		matureWorker(2, 5*types.MB, 206),
	)
	d, clock := newTestDetector(ctrl)
	// Drive past sustain to land the first kill.
	for i := 0; i < 18; i++ {
		advanceTick(d, clock)
	}
	first := ctrl.killsCount()
	if first == 0 {
		t.Fatal("expected at least one kill before cooldown test")
	}
	// Advance past cdnSustainDuration so sustain is satisfied, but stay within
	// cdnActionCooldown — the cooldown alone must block the re-kill.
	for i := 0; i < 6; i++ {
		advanceTick(d, clock)
	}
	if got := ctrl.killsCount(); got != first {
		t.Errorf("kills increased within cooldown (sustain satisfied): %d -> %d, want %d", first, got, first)
	}
	// Advance past cdnActionCooldown — the re-kill should now fire.
	for i := 0; i < 6; i++ {
		advanceTick(d, clock)
	}
	if got := ctrl.killsCount(); got == first {
		t.Error("expected re-kill after cooldown expired, but kill count unchanged")
	}
}

// TestCDNDetector_GIDRemoved cleans up state when a gid disappears.
func TestCDNDetector_GIDRemoved(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl, matureWorker(1, 5*types.MB, 206), matureWorker(2, 5*types.MB, 206))
	d, clock := newTestDetector(ctrl)
	advanceTick(d, clock)
	d.mu.Lock()
	hasState := len(d.workerState["sg_test"]) > 0
	d.mu.Unlock()
	if !hasState {
		t.Fatal("expected worker state populated")
	}
	// Make the gid disappear (no stats, not in active gids).
	ctrl.mu.Lock()
	delete(ctrl.stats, "test")
	ctrl.mu.Unlock()
	d.getActiveGids = func() []string { return nil }
	advanceTick(d, clock)
	d.mu.Lock()
	_, stillThere := d.workerState["sg_test"]
	d.mu.Unlock()
	if stillThere {
		t.Error("worker state should be cleared after gid disappears")
	}
}

// TestCDNDetector_ConcurrentStop verifies Stop is safe under concurrent calls
// (sync.Once guards the channel close — no double-close panic).
func TestCDNDetector_ConcurrentStop(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl, matureWorker(1, 5*types.MB, 206))
	d, _ := newTestDetector(ctrl)
	d.Start()
	// Give the loop a moment to spin up.
	time.Sleep(20 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Stop()
		}()
	}
	wg.Wait()
}

// TestCDNDetector_ThresholdReissuedAfterGap simulates a pause/resume where the
// gid briefly disappears then reappears (fresh downloader). The threshold
// takeover must be re-issued on the reappearance tick.
func TestCDNDetector_ThresholdReissuedAfterGap(t *testing.T) {
	ctrl := newMockCDNControl()
	setStats(ctrl, matureWorker(1, 5*types.MB, 206), matureWorker(2, 5*types.MB, 206))
	d, clock := newTestDetector(ctrl)

	// First tick: takeover issued.
	advanceTick(d, clock)
	if got := ctrl.thresholds["test"]; got != 0 {
		t.Fatalf("threshold after first tick = %v, want 0", got)
	}

	// Simulate pause: gid disappears.
	ctrl.mu.Lock()
	delete(ctrl.stats, "test")
	ctrl.mu.Unlock()
	d.getActiveGids = func() []string { return nil }
	advanceTick(d, clock)

	// Simulate resume: gid reappears with a fresh downloader. Reset the mock
	// threshold to a non-zero sentinel to prove the detector re-issues 0.
	ctrl.mu.Lock()
	ctrl.thresholds["test"] = 0.30
	ctrl.stats["test"] = []types.WorkerSnapshot{
		matureWorker(1, 5*types.MB, 206), matureWorker(2, 5*types.MB, 206),
	}
	ctrl.mu.Unlock()
	d.getActiveGids = func() []string { return []string{"sg_test"} }
	advanceTick(d, clock)
	if got := ctrl.thresholds["test"]; got != 0 {
		t.Errorf("threshold after resume = %v, want 0 (re-armed)", got)
	}
}
