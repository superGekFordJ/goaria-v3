import { bench, describe } from 'vitest'

type Task = { gid: string; [key: string]: unknown }

const _dedupGidSet = new Set<string>()

function dedupByGid(list: Task[]): Task[] {
  _dedupGidSet.clear()
  return (list || []).filter(t => {
    const gid = t?.gid
    if (!gid || _dedupGidSet.has(gid)) return false
    _dedupGidSet.add(gid)
    return true
  })
}

describe('Task Array Creation Benchmark', () => {
  const stoppedCount = 500
  const activeCount = 1000
  const waitingCount = 1000

  const _stoppedGidSet = new Set<string>()
  for (let i = 0; i < stoppedCount; i++) _stoppedGidSet.add(`stopped-${i}`)

  const res = {
    active: Array.from({ length: activeCount }, (_, i) => ({ gid: `active-${i}` })),
    waiting: Array.from({ length: waitingCount }, (_, i) => ({ gid: `waiting-${i}` })),
  }

  res.active.push({ gid: 'active-0' })
  res.active.push({ gid: 'stopped-0' })
  res.waiting.push({ gid: 'waiting-0' })
  res.waiting.push({ gid: 'active-1' })

  bench('Baseline: Multiple passes (.filter + dedup + for loop)', () => {
    const _activeGidSet = new Set<string>()
    const active = dedupByGid((res.active || []).filter((t: Task) => !_stoppedGidSet.has(t.gid)))
    _activeGidSet.clear()
    for (const t of active) {
      _activeGidSet.add(t.gid)
    }
    const waiting = dedupByGid(
      (res.waiting || []).filter(
        (t: Task) => !_activeGidSet.has(t.gid) && !_stoppedGidSet.has(t.gid),
      ),
    )
    if (active.length + waiting.length === 0) throw new Error('invalid benchmark result')
  })

  bench('Optimized: Single pass with inline sets', () => {
    const _activeGidSet = new Set<string>()
    const active: Task[] = []
    _activeGidSet.clear()

    for (const t of res.active || []) {
      const gid = t?.gid
      if (!gid || _stoppedGidSet.has(gid) || _activeGidSet.has(gid)) continue
      _activeGidSet.add(gid)
      active.push(t)
    }

    const _waitingGidSet = new Set<string>()
    const waiting: Task[] = []
    for (const t of res.waiting || []) {
      const gid = t?.gid
      if (!gid || _activeGidSet.has(gid) || _stoppedGidSet.has(gid) || _waitingGidSet.has(gid))
        continue
      _waitingGidSet.add(gid)
      waiting.push(t)
    }

    if (active.length + waiting.length === 0) throw new Error('invalid benchmark result')
  })
})
