import { describe, it, expect } from 'vitest'

// Mock Task type
type Task = { gid: string; [key: string]: unknown }

describe('Performance Benchmark: fetchActiveTasks Optimization', () => {
  it('benchmarks set reconstruction vs cached set', () => {
    // Setup
    const stoppedCount = 1000
    const iterations = 10000
    const stoppedTasks: Task[] = []
    for (let i = 0; i < stoppedCount; i++) {
        stoppedTasks.push({ gid: `task-${i}` })
    }

    const _stoppedGidSet = new Set<string>()

    // Baseline: Rebuild Set every time
    const startBaseline = performance.now()
    for (let i = 0; i < iterations; i++) {
        _stoppedGidSet.clear()
        for (const t of stoppedTasks) {
            _stoppedGidSet.add(t.gid)
        }
        // Simulate usage (optional, but realistic)
        _stoppedGidSet.has('task-500')
    }
    const endBaseline = performance.now()
    const baselineTime = endBaseline - startBaseline

    // Optimization: Check reference
    let lastStoppedTasksRef: Task[] | null = null
    // Reset set for fairness, though irrelevant
    _stoppedGidSet.clear()
    
    const startOptimized = performance.now()
    for (let i = 0; i < iterations; i++) {
        // In the loop, we simulate that stoppedTasks reference doesn't change most of the time
        // But we should also simulate it changing sometimes? 
        // The optimization target is the case where it DOESN'T change.
        // So we keep it constant here.
        if (stoppedTasks !== lastStoppedTasksRef) {
            _stoppedGidSet.clear()
            for (const t of stoppedTasks) {
                _stoppedGidSet.add(t.gid)
            }
            lastStoppedTasksRef = stoppedTasks
        }
        // Simulate usage
        _stoppedGidSet.has('task-500')
    }
    const endOptimized = performance.now()
    const optimizedTime = endOptimized - startOptimized

    console.log(`
      Performance Results (1000 tasks, ${iterations} iterations):
      ---------------------------------------------------
      Baseline (Rebuild): ${baselineTime.toFixed(2)}ms
      Optimized (Cached): ${optimizedTime.toFixed(2)}ms
      Improvement: ${(baselineTime / optimizedTime).toFixed(2)}x
    `)

    expect(optimizedTime).toBeLessThan(baselineTime)
  })
})
