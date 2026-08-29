import { afterEach, describe, expect, it, vi } from 'vitest'
import { admitMember, evaluateClose, type BurstWindowState } from './burstCoalescer'

const QUIET = 1_000
const MAX = 15_000
const CAP = 128

function windowOf(ids: number[], first: number, last: number): BurstWindowState {
  return {
    captureId: 'cap-1',
    downloadIds: ids,
    firstItemAt: first,
    lastItemAt: last,
    phase: 'coalescing',
  }
}

describe('admitMember', () => {
  it('returns legacy and does not merge without a session', () => {
    const result = admitMember({
      sessionPresent: false,
      window: null,
      captureId: 'cap-1',
      downloadId: 1,
      now: 0,
      maxItems: CAP,
    })
    expect(result).toEqual({ kind: 'legacy' })
  })

  it('starts the max clock on the first member and resets quiet on each admit', () => {
    const first = admitMember({
      sessionPresent: true,
      window: null,
      captureId: 'cap-1',
      downloadId: 1,
      now: 100,
      maxItems: CAP,
    })
    expect(first.kind).toBe('admitted')
    if (first.kind !== 'admitted') return
    expect(first.window.firstItemAt).toBe(100)
    expect(first.window.lastItemAt).toBe(100)

    const second = admitMember({
      sessionPresent: true,
      window: first.window,
      captureId: 'cap-1',
      downloadId: 2,
      now: 800,
      maxItems: CAP,
    })
    expect(second.kind).toBe('admitted')
    if (second.kind !== 'admitted') return
    expect(second.window.firstItemAt).toBe(100)
    expect(second.window.lastItemAt).toBe(800)
    expect(second.window.downloadIds).toEqual([1, 2])
  })

  it('returns overflow for the 129th without dropping the first 128', () => {
    const ids = Array.from({ length: 128 }, (_, i) => i + 1)
    const full = windowOf(ids, 0, 50)
    const result = admitMember({
      sessionPresent: true,
      window: full,
      captureId: 'cap-1',
      downloadId: 129,
      now: 60,
      maxItems: CAP,
    })
    expect(result.kind).toBe('legacy_overflow')
    if (result.kind !== 'legacy_overflow') return
    expect(result.window.downloadIds).toHaveLength(128)
    expect(result.window.downloadIds[0]).toBe(1)
    expect(result.window.downloadIds[127]).toBe(128)
  })

  it('does not append once the window is in picker or submitting', () => {
    const picker: BurstWindowState = { ...windowOf([1, 2], 0, 10), phase: 'picker' }
    expect(
      admitMember({
        sessionPresent: true,
        window: picker,
        captureId: 'cap-1',
        downloadId: 3,
        now: 20,
        maxItems: CAP,
      }),
    ).toEqual({ kind: 'legacy' })
  })
})

describe('evaluateClose', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('stays open until quiet or max', () => {
    vi.useFakeTimers()
    vi.setSystemTime(500)
    const win = windowOf([1], 0, 0)
    expect(evaluateClose(win, 500, QUIET, MAX).kind).toBe('open')
  })

  it('closes a single member as legacy_single after quiet', () => {
    const win = windowOf([1], 0, 0)
    expect(evaluateClose(win, QUIET, QUIET, MAX).kind).toBe('legacy_single')
  })

  it('closes two or more members as burst after quiet', () => {
    const win = windowOf([1, 2], 0, 200)
    expect(evaluateClose(win, 200 + QUIET, QUIET, MAX).kind).toBe('burst')
  })

  it('closes as burst at max even if quiet has not elapsed since the last item', () => {
    const win = windowOf([1, 2], 0, MAX - 10)
    expect(evaluateClose(win, MAX, QUIET, MAX).kind).toBe('burst')
  })
})
