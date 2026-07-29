package concurrent

import (
	"sync"
	"testing"

	"goaria-v3/internal/surge/types"
)

// TestResumeOnRetryOffset_TaskPublishRace exercises concurrent Task publish
// vs health-style reads under activeMu. Under -race this fails if
// activeTask.Task is assigned without holding activeMu.
func TestResumeOnRetryOffset_TaskPublishRace(t *testing.T) {
	d := &ConcurrentDownloader{}
	d.activeTasks = make(map[int]*ActiveTask)

	active := &ActiveTask{
		Task: types.Task{Offset: 0, Length: 1 << 20},
	}
	active.CurrentOffset.Store(0)
	active.StopAt.Store(1 << 20)
	d.activeTasks[0] = active

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			off := int64(i % 1024)
			active.CurrentOffset.Store(off)
			task := types.Task{Offset: 0, Length: 1 << 20}
			d.resumeOnRetryOffset(&task, active)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// Mimic checkWorkerHealth: read Task.Offset under activeMu.
			d.activeMu.Lock()
			_ = active.Task.Offset
			downloaded := active.CurrentOffset.Load() - active.Task.Offset
			if downloaded < 0 {
				downloaded = 0
			}
			_ = downloaded
			d.activeMu.Unlock()
		}
	}()

	wg.Wait()
}
