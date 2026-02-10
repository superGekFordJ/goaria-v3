import { describe, bench } from 'vitest'
import { ref, computed, toRaw } from 'vue'

describe('Array Merge Benchmark', () => {
  const ARRAY_SIZE = 1000
  const activeRaw = Array.from({ length: ARRAY_SIZE }, (_, i) => ({ gid: `a-${i}`, val: i }))
  const waitingRaw = Array.from({ length: ARRAY_SIZE }, (_, i) => ({ gid: `w-${i}`, val: i }))

  const tasks = ref({
    active: activeRaw,
    waiting: waitingRaw
  })

  // Access via computed to simulate store
  const activeTasks = computed(() => tasks.value.active)
  const waitingTasks = computed(() => tasks.value.waiting)

  bench('Spread Reactive', () => {
    const active = activeTasks.value
    const waiting = waitingTasks.value
    const res = [...active, ...waiting]
    if (res.length !== ARRAY_SIZE * 2) throw new Error('fail')
  })

  bench('Spread Raw', () => {
    const active = toRaw(activeTasks.value)
    const waiting = toRaw(waitingTasks.value)
    const res = [...active, ...waiting]
    if (res.length !== ARRAY_SIZE * 2) throw new Error('fail')
  })

  bench('Concat Reactive', () => {
    const active = activeTasks.value
    const waiting = waitingTasks.value
    const res = active.concat(waiting)
    if (res.length !== ARRAY_SIZE * 2) throw new Error('fail')
  })

  bench('Concat Raw', () => {
    const active = toRaw(activeTasks.value)
    const waiting = toRaw(waitingTasks.value)
    const res = active.concat(waiting)
    if (res.length !== ARRAY_SIZE * 2) throw new Error('fail')
  })
})
