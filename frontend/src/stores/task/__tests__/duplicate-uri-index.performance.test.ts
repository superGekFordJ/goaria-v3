import { describe, it, expect, vi } from 'vitest'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'
import { setupState } from '../state'
import { isDuplicateUri, isValidUrl } from '../../../utils/url'

vi.mock('../../../../bindings/goaria-v3/app.js', () => ({
  UpdateTrayState: vi.fn(),
}))

type TaskStoreLike = Parameters<typeof isDuplicateUri>[1]

const REPEATS = 25
const CANDIDATE_COUNT = 120

function createTask(index: number, status: string, uri: string): Task {
  return {
    gid: `${status}-${index.toString().padStart(6, '0')}`,
    status,
    totalLength: '1000',
    completedLength: status === 'active' ? '500' : '1000',
    downloadSpeed: status === 'active' ? '100' : '0',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [
      {
        path: `/downloads/file-${index}.bin`,
        uris: [{ uri, status: 'used' }],
      },
    ],
  } as Task
}

function createStoppedTasks(count: number): Task[] {
  return Array.from({ length: count }, (_, index) =>
    createTask(index, 'complete', `https://example.com/stopped-${index}.bin`),
  )
}

function createActiveTask(iteration: number): Task {
  return createTask(iteration, 'active', `https://example.com/active-${iteration}.bin`)
}

function createWaitingTask(iteration: number): Task {
  return createTask(iteration, 'waiting', `https://example.com/waiting-${iteration}.bin`)
}

function createCandidates(stoppedCount: number, iteration: number): string[] {
  const candidates: string[] = []

  for (let index = 0; index < CANDIDATE_COUNT; index++) {
    if (index % 4 === 0) {
      candidates.push(`https://example.com/stopped-${(index * 37) % stoppedCount}.bin`)
    } else if (index % 4 === 1) {
      candidates.push(` https://example.com/stopped-${(index * 53) % stoppedCount}.bin `)
    } else if (index % 4 === 2) {
      candidates.push(`https://example.com/active-${iteration}.bin`)
    } else {
      candidates.push(`https://example.com/new-${iteration}-${index}.bin`)
    }
  }

  candidates[7] = `https://example.com/waiting-${iteration}.bin`
  return candidates
}

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b)
  const middle = Math.floor(sorted.length / 2)
  if (sorted.length % 2 === 1) return sorted[middle]
  return (sorted[middle - 1] + sorted[middle]) / 2
}

function measureInvalidatedDuplicateBatch(stoppedCount: number) {
  const state = setupState()
  const stopped = createStoppedTasks(stoppedCount)

  state.tasks.value = {
    active: [createActiveTask(0)],
    waiting: [createWaitingTask(0)],
    stopped,
  }

  expect(state.allUris.value.has('https://example.com/stopped-0.bin')).toBe(true)

  const cachedStart = performance.now()
  for (const candidate of createCandidates(stoppedCount, 0)) {
    isDuplicateUri(candidate, { allUris: state.allUris.value } as TaskStoreLike)
  }
  const cachedBatchMs = performance.now() - cachedStart

  const timings: number[] = []
  const duplicateCounts: number[] = []

  for (let iteration = 1; iteration <= REPEATS; iteration++) {
    const active = [createActiveTask(iteration)]
    const waiting = [createWaitingTask(iteration)]
    state.tasks.value = { active, waiting, stopped }

    const candidates = createCandidates(stoppedCount, iteration)
    const start = performance.now()
    let duplicates = 0
    for (const candidate of candidates) {
      if (isValidUrl(candidate.trim()) && isDuplicateUri(candidate, { allUris: state.allUris.value } as TaskStoreLike)) {
        duplicates++
      }
    }
    timings.push(performance.now() - start)
    duplicateCounts.push(duplicates)
  }

  return {
    stoppedCount,
    cachedBatchMs,
    timings,
    medianMs: median(timings),
    worstMs: Math.max(...timings),
    bestMs: Math.min(...timings),
    duplicateCounts,
  }
}

describe('duplicate URI index measurement', () => {
  it('characterizes first duplicate-check batch after active/waiting invalidation', () => {
    const results = [measureInvalidatedDuplicateBatch(1_000), measureInvalidatedDuplicateBatch(10_000)]

    for (const result of results) {
      expect(result.timings).toHaveLength(REPEATS)
      expect(result.duplicateCounts.every(count => count > 0)).toBe(true)
      expect(result.duplicateCounts.every(count => count < CANDIDATE_COUNT)).toBe(true)
      expect(result.worstMs).toBeGreaterThanOrEqual(result.bestMs)
    }

    console.table(
      results.map(result => ({
        stopped: result.stoppedCount,
        cachedBatchMs: Number(result.cachedBatchMs.toFixed(3)),
        medianInvalidatedBatchMs: Number(result.medianMs.toFixed(3)),
        worstInvalidatedBatchMs: Number(result.worstMs.toFixed(3)),
        bestInvalidatedBatchMs: Number(result.bestMs.toFixed(3)),
        rawInvalidatedBatchMs: result.timings.map(value => Number(value.toFixed(3))).join(', '),
      })),
    )
  })
})
