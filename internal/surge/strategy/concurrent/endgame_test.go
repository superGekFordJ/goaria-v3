package concurrent

import (
	"sync"
	"testing"

	"goaria-v3/internal/surge/types"
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

	for i := 0; i < 3; i++ {
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

	for i := 0; i < types.HedgeErrorThreshold; i++ {
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

	for i := 0; i < types.HedgeErrorThreshold-1; i++ {
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

	for i := 0; i < types.HedgeErrorThreshold; i++ {
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

	for i := 0; i < types.HedgeErrorThreshold-1; i++ {
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

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			d.recordHedgeError()
			d.recordHedgeSuccess()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			d.activeMu.Lock()
			for _, at := range d.activeTasks {
				at.Hedged.Store(0)
				at.SharedMaxOffset = nil
			}
			d.activeMu.Unlock()
			d.HedgeAll(queue)
			queue.DrainRemaining()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			d.HedgeWork(queue)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = d.hedgeDisabled.Load()
			_ = d.consecutiveHedgeErrors.Load()
		}
	}()

	wg.Wait()
}
