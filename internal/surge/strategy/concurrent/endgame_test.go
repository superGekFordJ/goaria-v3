package concurrent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// makeEndGameDownloader creates a ConcurrentDownloader with pre-populated
// activeTasks for end-game testing. Each active task has CurrentOffset and
// StopAt set to the given values.
func makeEndGameDownloader(tasks []struct {
	offset int64
	length int64
	hedged int32
},
) *ConcurrentDownloader {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	for i, t := range tasks {
		at := &ActiveTask{
			Task: types.Task{Offset: t.offset, Length: t.length},
		}
		at.CurrentOffset.Store(t.offset)
		at.StopAt.Store(t.offset + t.length)
		at.Hedged.Store(t.hedged)
		d.activeTasks[i] = at
	}
	return d
}

func TestIsEndGame_QueueHasTasks(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{{offset: 0, length: 100, hedged: 0}})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: 100})

	if d.isEndGame(queue) {
		t.Error("isEndGame should return false when queue has tasks")
	}
}

func TestIsEndGame_NoActiveTasks(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)

	if d.isEndGame(queue) {
		t.Error("isEndGame should return false when no active tasks")
	}
}

func TestIsEndGame_NoIdleWorkers(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{{offset: 0, length: 100, hedged: 0}})

	queue := NewTaskQueue()

	if d.isEndGame(queue) {
		t.Error("isEndGame should return false when no idle workers")
	}
}

func TestIsEndGame_True(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{{offset: 0, length: 100, hedged: 0}})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)

	if !d.isEndGame(queue) {
		t.Error("isEndGame should return true: queue empty, active tasks, idle workers")
	}
}

func TestHedgeAll_HedgesAllEligible(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{
		{offset: 0, length: 100, hedged: 0},
		{offset: 100, length: 200, hedged: 0},
		{offset: 300, length: 300, hedged: 0},
	})

	queue := NewTaskQueue()

	n := d.HedgeAll(queue)
	if n != 3 {
		t.Fatalf("HedgeAll returned %d, want 3", n)
	}

	if queue.Len() != 3 {
		t.Errorf("queue should have 3 tasks, got %d", queue.Len())
	}

	for i := range 3 {
		if d.activeTasks[i].Hedged.Load() != 1 {
			t.Errorf("active task %d Hedged should be 1", i)
		}
	}

	queue.Close()
}

func TestHedgeAll_SkipsHedged(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{
		{offset: 0, length: 100, hedged: 0},
		{offset: 100, length: 200, hedged: 1},
		{offset: 300, length: 300, hedged: 0},
	})

	queue := NewTaskQueue()

	n := d.HedgeAll(queue)
	if n != 2 {
		t.Fatalf("HedgeAll returned %d, want 2", n)
	}

	queue.Close()
}

func TestHedgeAll_SkipsZeroRemaining(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{
		{offset: 100, length: 0, hedged: 0},
	})

	queue := NewTaskQueue()

	n := d.HedgeAll(queue)
	if n != 0 {
		t.Fatalf("HedgeAll returned %d, want 0", n)
	}

	queue.Close()
}

func TestHedgeAll_DisabledByPoison(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{{offset: 0, length: 100, hedged: 0}})

	d.hedgeDisabled.Store(true)

	queue := NewTaskQueue()

	n := d.HedgeAll(queue)
	if n != 0 {
		t.Fatalf("HedgeAll returned %d, want 0 when disabled", n)
	}

	queue.Close()
}

func TestHedgeWork_DisabledByPoison(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{{offset: 0, length: 100, hedged: 0}})

	d.hedgeDisabled.Store(true)

	queue := NewTaskQueue()

	if d.HedgeWork(queue) {
		t.Error("HedgeWork should return false when hedgeDisabled is true")
	}

	queue.Close()
}

func TestRecordHedgeError_DisablesAtThreshold(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}

	for range types.HedgeErrorThreshold {
		d.recordHedgeError()
	}

	if !d.hedgeDisabled.Load() {
		t.Error("hedgeDisabled should be true after reaching threshold")
	}
}

func TestRecordHedgeError_BelowThreshold(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}

	for range types.HedgeErrorThreshold - 1 {
		d.recordHedgeError()
	}

	if d.hedgeDisabled.Load() {
		t.Error("hedgeDisabled should be false below threshold")
	}
}

func TestRecordHedgeSuccess_DecaysNotZeros(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}

	for range types.HedgeErrorThreshold {
		d.recordHedgeError()
	}

	if !d.hedgeDisabled.Load() {
		t.Fatal("hedgeDisabled should be true after threshold errors")
	}

	d.recordHedgeSuccess()

	if d.consecutiveHedgeErrors.Load() != int32(types.HedgeErrorThreshold)-1 {
		t.Errorf("consecutiveHedgeErrors should be %d after one success, got %d",
			int32(types.HedgeErrorThreshold)-1, d.consecutiveHedgeErrors.Load())
	}

	if !d.hedgeDisabled.Load() {
		t.Error("hedgeDisabled should still be true after one success")
	}

	for range types.HedgeErrorThreshold - 1 {
		d.recordHedgeSuccess()
	}

	if d.consecutiveHedgeErrors.Load() != 0 {
		t.Errorf("consecutiveHedgeErrors should be 0 after sustained successes, got %d",
			d.consecutiveHedgeErrors.Load())
	}

	if d.hedgeDisabled.Load() {
		t.Error("hedgeDisabled should be false after counter decays to 0")
	}
}

func TestRecordHedgeSuccess_NoopWhenNotDisabled(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}

	d.recordHedgeSuccess()

	if d.consecutiveHedgeErrors.Load() != 0 {
		t.Error("consecutiveHedgeErrors should still be 0")
	}

	if d.hedgeDisabled.Load() {
		t.Error("hedgeDisabled should still be false")
	}
}

func TestEndGame_HedgeAllIntegratesWithBalancer(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{
		{offset: 0, length: 1000, hedged: 0},
		{offset: 1000, length: 2000, hedged: 0},
	})

	queue := NewTaskQueue()
	queue.idleWorkers.Add(2)

	if !d.isEndGame(queue) {
		t.Fatal("should be in end-game state")
	}

	n := d.HedgeAll(queue)
	if n != 2 {
		t.Fatalf("HedgeAll returned %d, want 2", n)
	}

	if queue.Len() != 2 {
		t.Errorf("queue should have 2 hedge tasks, got %d", queue.Len())
	}

	if d.HedgeWork(queue) {
		t.Error("HedgeWork should return false after HedgeAll hedged all tasks")
	}

	queue.Close()
}

func TestEndGame_ConcurrentAccess(t *testing.T) {
	d := makeEndGameDownloader([]struct {
		offset int64
		length int64
		hedged int32
	}{
		{offset: 0, length: 1000, hedged: 0},
		{offset: 1000, length: 1000, hedged: 0},
		{offset: 2000, length: 1000, hedged: 0},
	})

	queue := NewTaskQueue()
	defer queue.Close()

	var wg sync.WaitGroup

	wg.Go(func() {
		for range 200 {
			d.recordHedgeError()
			d.recordHedgeSuccess()
		}
	})

	wg.Go(func() {
		for range 200 {
			d.activeMu.Lock()
			for _, at := range d.activeTasks {
				at.Hedged.Store(0)
				at.SharedMaxOffset = nil
			}
			d.activeMu.Unlock()
			d.HedgeAll(queue)
			queue.DrainRemaining()
		}
	})

	wg.Go(func() {
		for range 200 {
			d.HedgeWork(queue)
		}
	})

	wg.Go(func() {
		for range 200 {
			_ = d.hedgeDisabled.Load()
			_ = d.consecutiveHedgeErrors.Load()
		}
	})

	wg.Wait()
}

// newActiveFixture builds an ActiveTask the same way makeEndGameDownloader
// does, plus an optional pre-existing SharedMaxOffset pointer.
func newActiveFixture(offset, length int64, hedged int32, shared *atomic.Int64) *ActiveTask {
	at := &ActiveTask{
		Task:            types.Task{Offset: offset, Length: length},
		SharedMaxOffset: shared,
	}
	at.CurrentOffset.Store(offset)
	at.StopAt.Store(offset + length)
	at.Hedged.Store(hedged)
	return at
}

// TestIsEndGame_FalseOnLargeRemaining isolates the global byte cap: a single
// active with 10MiB remaining still fits under its own 16MiB per-worker
// budget, so only EndGameMaxRemaining can refuse end-game.
func TestIsEndGame_FalseOnLargeRemaining(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{MinChunkSize: 16 * utils.MiB},
	}
	d.activeTasks[0] = newActiveFixture(0, 10*utils.MiB, 0, nil)

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)

	if d.isEndGame(queue) {
		t.Error("isEndGame should return false when totalRemaining exceeds the global cap")
	}
}

// TestIsEndGame_ByteBoundaries pins the inclusive global cap and the strict
// per-worker comparison.
func TestIsEndGame_ByteBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		minChunk  int64
		remaining int64
		want      bool
	}{
		{"cap inclusive", 8 * utils.MiB, types.EndGameMaxRemaining, true},
		{"cap exceeded", 8 * utils.MiB, types.EndGameMaxRemaining + 1, false},
		{"per-worker strict", 2 * utils.MiB, 2 * utils.MiB, false},
		{"per-worker under", 2 * utils.MiB, 2*utils.MiB - 1, true},
		{"all completed", 2 * utils.MiB, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &ConcurrentDownloader{
				activeTasks: make(map[int]*ActiveTask),
				Runtime:     &types.RuntimeConfig{MinChunkSize: tc.minChunk},
			}
			d.activeTasks[0] = newActiveFixture(0, tc.remaining, 0, nil)

			queue := NewTaskQueue()
			queue.idleWorkers.Add(1)

			if got := d.isEndGame(queue); got != tc.want {
				t.Errorf("isEndGame() = %v, want %v (remaining=%d, minChunk=%d)",
					got, tc.want, tc.remaining, tc.minChunk)
			}
		})
	}
}

// TestMidGame_FastWorkerFinishes_StealsNotHedges verifies that mid-game large
// remainders are never hedge-duplicated: both hedge entries refuse without
// marking, and StealWork still splits the biggest range in half.
func TestMidGame_FastWorkerFinishes_StealsNotHedges(t *testing.T) {
	chunk := int64(10 * utils.MiB)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	for i := range 3 {
		d.activeTasks[i] = newActiveFixture(int64(i)*chunk, chunk, 0, nil)
	}

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)
	defer queue.Close()

	if d.isEndGame(queue) {
		t.Fatal("mid-game fixture must not report end-game")
	}
	if d.HedgeWork(queue) {
		t.Fatal("HedgeWork must refuse >= MaxHedgeChunk remainders")
	}
	if n := d.HedgeAll(queue); n != 0 {
		t.Fatalf("HedgeAll must refuse >= MaxHedgeChunk remainders, got %d", n)
	}
	for i, at := range d.activeTasks {
		if at.Hedged.Load() != 0 {
			t.Fatalf("active %d must not be marked hedged on refusal", i)
		}
		at.SharedMaxOffsetMu.RLock()
		ptr := at.SharedMaxOffset
		at.SharedMaxOffsetMu.RUnlock()
		if ptr != nil {
			t.Fatalf("active %d must not get a shared pointer on refusal", i)
		}
	}
	if queue.Len() != 0 {
		t.Fatalf("refused hedge must not enqueue, len=%d", queue.Len())
	}

	if !d.StealWork(queue) {
		t.Fatal("StealWork should split a 10MiB active in normal phase")
	}
	if queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 stolen task", queue.Len())
	}
	stolen, _ := queue.Pop()

	var victim *ActiveTask
	victimCount := 0
	for _, at := range d.activeTasks {
		if at.StopAt.Load() != at.Task.Offset+at.Task.Length {
			victimCount++
			victim = at
		}
	}
	if victimCount != 1 {
		t.Fatalf("exactly one victim should be split, got %d", victimCount)
	}
	if front := victim.StopAt.Load() - victim.Task.Offset; front != 5*utils.MiB {
		t.Fatalf("victim keeps %d bytes, want %d", front, 5*utils.MiB)
	}
	if stolen.Offset != victim.StopAt.Load() || stolen.Length != 5*utils.MiB {
		t.Fatalf("stolen = {%d,%d}, want {%d,%d}", stolen.Offset, stolen.Length,
			victim.StopAt.Load(), 5*utils.MiB)
	}
	if stolen.SharedMaxOffset == nil {
		t.Fatal("stolen task must carry an independent SharedMaxOffset")
	}
	victim.SharedMaxOffsetMu.RLock()
	vptr := victim.SharedMaxOffset
	victim.SharedMaxOffsetMu.RUnlock()
	if vptr != nil {
		t.Fatal("stolen task's pointer must be independent of the victim's")
	}
}

// TestEndGame_BalancerStealsBeforeHedging drives the real runBalancer: on an
// end-game tick the first enqueue must be the stolen half of the big task,
// after which the same tick's batch sweep races the remaining small tail.
// The fake idle worker never consumes, so the queue stays non-empty and the
// observation is stable.
func TestEndGame_BalancerStealsBeforeHedging(t *testing.T) {
	big := int64(5 * utils.MiB / 2)   // splittable tail
	small := int64(1 * utils.MiB / 2) // hedge-size tail
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	d.activeTasks[0] = newActiveFixture(0, big, 0, nil)
	d.activeTasks[1] = newActiveFixture(big, small, 0, nil)

	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)
	defer queue.Close()

	if !d.isEndGame(queue) {
		t.Fatal("fixture must start in end-game state")
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { d.runBalancer(ctx, queue) })

	// Wait for the full first end-game tick: steal push + batch-sweep push.
	deadline := time.Now().Add(3 * time.Second)
	for queue.Len() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	drained := queue.DrainRemaining()
	if len(drained) != 2 {
		t.Fatalf("drained %d tasks, want 2 (stolen half + batch-swept racer)", len(drained))
	}
	stolen := drained[0]
	if stolen.Offset != big/2 || stolen.Length != big/2 {
		t.Fatalf("first enqueue = {%d,%d}, want stolen {%d,%d} from the big task",
			stolen.Offset, stolen.Length, big/2, big/2)
	}
	racer := drained[1]
	if racer.Offset != big || racer.Length != small {
		t.Fatalf("second enqueue = {%d,%d}, want hedge racer {%d,%d} for the small tail",
			racer.Offset, racer.Length, big, small)
	}
	if got := d.activeTasks[0].StopAt.Load(); got != big/2 {
		t.Fatalf("big task StopAt = %d, want %d", got, big/2)
	}
	if got := d.activeTasks[0].Hedged.Load(); got != 0 {
		t.Fatalf("big task must not be hedged: post-steal remainder %d stays splittable", big/2)
	}
	if got := d.activeTasks[1].Hedged.Load(); got != 1 {
		t.Fatal("batch sweep must hedge the eligible small tail on the same tick")
	}
	d.activeTasks[1].SharedMaxOffsetMu.RLock()
	ptr := d.activeTasks[1].SharedMaxOffset
	d.activeTasks[1].SharedMaxOffsetMu.RUnlock()
	if ptr == nil || racer.SharedMaxOffset != ptr {
		t.Fatal("racer must carry the victim's SharedMaxOffset pointer")
	}
}

// TestHedgeWork_SizeGate pins the MaxHedgeChunk ceiling on the on-demand
// fallback entry: remainders below the boundary race, at/above do not, and a
// large task cannot shadow an eligible small tail.
func TestHedgeWork_SizeGate(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	boundary := int64(types.MaxHedgeChunk)

	for _, tc := range []struct {
		name      string
		remaining int64
		want      bool
	}{
		{"zero", 0, false},
		{"just under", boundary - 1, true},
		{"at boundary", boundary, false},
		{"just over", boundary + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &ConcurrentDownloader{
				activeTasks: make(map[int]*ActiveTask),
				Runtime:     &types.RuntimeConfig{},
			}
			at := newActiveFixture(0, tc.remaining, 0, nil)
			d.activeTasks[0] = at
			queue := NewTaskQueue()
			defer queue.Close()

			if got := d.HedgeWork(queue); got != tc.want {
				t.Fatalf("HedgeWork(%d) = %v, want %v", tc.remaining, got, tc.want)
			}
			if !tc.want {
				if at.Hedged.Load() != 0 {
					t.Error("rejected hedge must not set Hedged")
				}
				at.SharedMaxOffsetMu.RLock()
				ptr := at.SharedMaxOffset
				at.SharedMaxOffsetMu.RUnlock()
				if ptr != nil {
					t.Error("rejected hedge must not initialize SharedMaxOffset")
				}
				if queue.Len() != 0 {
					t.Errorf("rejected hedge must not enqueue, len=%d", queue.Len())
				}
			}
		})
	}

	// A big task must not shadow an eligible small tail even outside end-game.
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	big := newActiveFixture(0, 8*utils.MiB, 0, nil)
	small := newActiveFixture(8*utils.MiB, floor, 0, nil)
	d.activeTasks[0] = big
	d.activeTasks[1] = small
	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)
	defer queue.Close()

	if d.isEndGame(queue) {
		t.Fatal("mixed fixture must not report end-game")
	}
	if !d.HedgeWork(queue) {
		t.Fatal("HedgeWork should hedge the eligible small tail")
	}
	if queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", queue.Len())
	}
	tsk, _ := queue.Pop()
	if tsk.Offset != small.Task.Offset || tsk.Length != floor {
		t.Fatalf("racer = {%d,%d}, want small tail {%d,%d}", tsk.Offset, tsk.Length, small.Task.Offset, floor)
	}
	if big.Hedged.Load() != 0 {
		t.Fatal("big task must not be hedged")
	}
	if small.Hedged.Load() != 1 {
		t.Fatal("small task should be hedged")
	}
}

// TestHedgeAll_SizeGate applies the same ceiling to the proactive entry.
func TestHedgeAll_SizeGate(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	boundary := int64(types.MaxHedgeChunk)

	for _, tc := range []struct {
		name      string
		remaining int64
		want      int
	}{
		{"zero", 0, 0},
		{"just under", boundary - 1, 1},
		{"at boundary", boundary, 0},
		{"just over", boundary + 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &ConcurrentDownloader{
				activeTasks: make(map[int]*ActiveTask),
				Runtime:     &types.RuntimeConfig{},
			}
			at := newActiveFixture(0, tc.remaining, 0, nil)
			d.activeTasks[0] = at
			queue := NewTaskQueue()
			defer queue.Close()

			if got := d.HedgeAll(queue); got != tc.want {
				t.Fatalf("HedgeAll(%d) = %d, want %d", tc.remaining, got, tc.want)
			}
			if tc.want == 0 {
				if at.Hedged.Load() != 0 {
					t.Error("rejected hedge must not set Hedged")
				}
				at.SharedMaxOffsetMu.RLock()
				ptr := at.SharedMaxOffset
				at.SharedMaxOffsetMu.RUnlock()
				if ptr != nil {
					t.Error("rejected hedge must not initialize SharedMaxOffset")
				}
				if queue.Len() != 0 {
					t.Errorf("rejected hedge must not enqueue, len=%d", queue.Len())
				}
			}
		})
	}

	// Mixed load outside end-game: only the small tail is duplicated.
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	big := newActiveFixture(0, 8*utils.MiB, 0, nil)
	small := newActiveFixture(8*utils.MiB, floor, 0, nil)
	d.activeTasks[0] = big
	d.activeTasks[1] = small
	queue := NewTaskQueue()
	queue.idleWorkers.Add(1)
	defer queue.Close()

	if d.isEndGame(queue) {
		t.Fatal("mixed fixture must not report end-game")
	}
	if n := d.HedgeAll(queue); n != 1 {
		t.Fatalf("HedgeAll = %d, want 1 (only the small tail)", n)
	}
	if queue.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", queue.Len())
	}
	tsk, _ := queue.Pop()
	if tsk.Offset != small.Task.Offset || tsk.Length != floor {
		t.Fatalf("racer = {%d,%d}, want small tail {%d,%d}", tsk.Offset, tsk.Length, small.Task.Offset, floor)
	}
	if big.Hedged.Load() != 0 {
		t.Fatal("big task must not be hedged")
	}
	if small.Hedged.Load() != 1 {
		t.Fatal("small task should be hedged")
	}
}

// TestHedgeWork_EffectiveRemaining verifies the candidate range honors the
// shared-progress view: with local at 0 and shared at StopAt-floor, only
// [shared, StopAt) is duplicated on the pre-existing pointer.
func TestHedgeWork_EffectiveRemaining(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	stopAt := int64(2 * utils.MiB)

	shared := &atomic.Int64{}
	shared.Store(stopAt - floor)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	at := newActiveFixture(0, stopAt, 0, shared)
	d.activeTasks[0] = at

	queue := NewTaskQueue()
	defer queue.Close()

	if !d.HedgeWork(queue) {
		t.Fatal("HedgeWork should hedge the effective tail")
	}
	tsk, ok := queue.Pop()
	if !ok {
		t.Fatal("expected one racer task")
	}
	if tsk.Offset != stopAt-floor || tsk.Length != floor {
		t.Fatalf("racer = {%d,%d}, want {%d,%d}", tsk.Offset, tsk.Length, stopAt-floor, floor)
	}
	if tsk.SharedMaxOffset != shared {
		t.Fatal("racer must reuse the pre-existing SharedMaxOffset pointer")
	}
	if at.CurrentOffset.Load() != 0 {
		t.Fatal("hedge must not move the victim's local CurrentOffset")
	}
	if at.StopAt.Load() != stopAt {
		t.Fatal("hedge must not move the victim's StopAt")
	}

	// shared reaching or passing StopAt leaves nothing to race.
	for _, tc := range []struct {
		name   string
		shared int64
	}{
		{"shared reaches stop", stopAt},
		{"shared beyond stop", stopAt + floor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shared2 := &atomic.Int64{}
			shared2.Store(tc.shared)
			d := &ConcurrentDownloader{
				activeTasks: make(map[int]*ActiveTask),
				Runtime:     &types.RuntimeConfig{},
			}
			at := newActiveFixture(0, stopAt, 0, shared2)
			d.activeTasks[0] = at
			queue := NewTaskQueue()
			defer queue.Close()

			if d.HedgeWork(queue) {
				t.Error("HedgeWork must refuse when effective remaining is zero")
			}
			if at.Hedged.Load() != 0 {
				t.Error("refused hedge must not set Hedged")
			}
			if queue.Len() != 0 {
				t.Errorf("refused hedge must not enqueue, len=%d", queue.Len())
			}
		})
	}
}

// TestHedgeAll_EffectiveRemaining applies the same shared-progress accounting
// to the proactive entry.
func TestHedgeAll_EffectiveRemaining(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	stopAt := int64(2 * utils.MiB)

	shared := &atomic.Int64{}
	shared.Store(stopAt - floor)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	at := newActiveFixture(0, stopAt, 0, shared)
	d.activeTasks[0] = at

	queue := NewTaskQueue()
	defer queue.Close()

	if n := d.HedgeAll(queue); n != 1 {
		t.Fatalf("HedgeAll = %d, want 1", n)
	}
	tsk, ok := queue.Pop()
	if !ok {
		t.Fatal("expected one racer task")
	}
	if tsk.Offset != stopAt-floor || tsk.Length != floor {
		t.Fatalf("racer = {%d,%d}, want {%d,%d}", tsk.Offset, tsk.Length, stopAt-floor, floor)
	}
	if tsk.SharedMaxOffset != shared {
		t.Fatal("racer must reuse the pre-existing SharedMaxOffset pointer")
	}
	if at.CurrentOffset.Load() != 0 || at.StopAt.Load() != stopAt {
		t.Fatal("hedge must not move the victim's local offset or StopAt")
	}
}

// TestHedgeAll_RepeatedAndMixedCalls verifies a single active task can never
// accumulate more than one racer, across sequential and concurrent calls to
// both hedge entries.
func TestHedgeAll_RepeatedAndMixedCalls(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	newFixture := func() (*ConcurrentDownloader, *TaskQueue) {
		d := &ConcurrentDownloader{
			activeTasks: make(map[int]*ActiveTask),
			Runtime:     &types.RuntimeConfig{},
		}
		d.activeTasks[0] = newActiveFixture(0, floor, 0, nil)
		return d, NewTaskQueue()
	}

	// HedgeAll then HedgeWork then HedgeAll on the same active — one racer.
	d1, q1 := newFixture()
	defer q1.Close()
	if n := d1.HedgeAll(q1); n != 1 {
		t.Fatalf("first HedgeAll = %d, want 1", n)
	}
	if d1.HedgeWork(q1) {
		t.Fatal("HedgeWork must not add a second racer")
	}
	if n := d1.HedgeAll(q1); n != 0 {
		t.Fatalf("second HedgeAll = %d, want 0", n)
	}
	if q1.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", q1.Len())
	}

	// Reverse order on a fresh fixture: HedgeWork then HedgeAll.
	d2, q2 := newFixture()
	defer q2.Close()
	if !d2.HedgeWork(q2) {
		t.Fatal("first HedgeWork should succeed")
	}
	if n := d2.HedgeAll(q2); n != 0 {
		t.Fatalf("HedgeAll after HedgeWork = %d, want 0", n)
	}
	if q2.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1", q2.Len())
	}

	// One goroutine per entry racing concurrently — still a single racer.
	d3, q3 := newFixture()
	defer q3.Close()
	var wg sync.WaitGroup
	wg.Go(func() { d3.HedgeAll(q3) })
	wg.Go(func() { d3.HedgeWork(q3) })
	wg.Wait()
	if q3.Len() != 1 {
		t.Fatalf("queue.Len() = %d, want 1 after concurrent calls", q3.Len())
	}
	if d3.activeTasks[0].Hedged.Load() != 1 {
		t.Fatal("victim should be hedged exactly once")
	}
}

// TestTailPhase_HedgeCancelResidualDedup runs the production cancel tail after
// a hedge: the residual requeue after queue Close is retained, shares the
// racer's pointer, and prefer+merge collapses it to the last quarter.
func TestTailPhase_HedgeCancelResidualDedup(t *testing.T) {
	floor := int64(types.MinChunkDynamicFloor)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{},
	}
	victim := newActiveFixture(0, floor, 0, nil)
	d.activeTasks[0] = victim

	queue := NewTaskQueue()
	if !d.HedgeWork(queue) {
		t.Fatal("HedgeWork should hedge the small tail")
	}
	victim.SharedMaxOffsetMu.RLock()
	shared := victim.SharedMaxOffset
	victim.SharedMaxOffsetMu.RUnlock()
	if shared == nil {
		t.Fatal("hedge must initialize SharedMaxOffset")
	}

	// Advance local to 1/2 and the shared pointer to 3/4 of the range.
	victim.CurrentOffset.Store(floor / 2)
	shared.Store(3 * floor / 4)

	// Close first: the production cancel tail must still retain the residual.
	queue.Close()
	d.releaseActiveOnCancel(0, victim, victim.Task, queue)

	d.activeMu.Lock()
	_, stillActive := d.activeTasks[0]
	d.activeMu.Unlock()
	if stillActive {
		t.Fatal("active task must be removed after releaseActiveOnCancel")
	}

	drained := queue.DrainRemaining()
	if len(drained) != 2 {
		t.Fatalf("drained %d tasks, want racer + residual", len(drained))
	}
	for _, tsk := range drained {
		if tsk.SharedMaxOffset != shared {
			t.Fatal("racer and residual must share the same SharedMaxOffset pointer")
		}
	}

	merged := mergeOverlappingTasks(preferMaxOffsetSameSharedMaxOffset(drained))
	if len(merged) != 1 {
		t.Fatalf("merged tasks = %d, want 1", len(merged))
	}
	if merged[0].Offset != 3*floor/4 || merged[0].Length != floor/4 {
		t.Fatalf("merged = {%d,%d}, want last quarter {%d,%d}",
			merged[0].Offset, merged[0].Length, 3*floor/4, floor/4)
	}
}
