import { describe, expect, it } from 'vitest'
import {
  CHUNK_COMPLETE,
  CHUNK_DOWNLOADING,
  CHUNK_PENDING,
  bucketChunkStates,
  chunkBucketRange,
  layoutChunkGrid,
} from './chunkMap'

const B = 112

function filled(n: number, state: number): number[] {
  return new Array<number>(n).fill(state)
}

describe('chunkBucketRange', () => {
  it.each([B, B + 1, 10007, 100000])('N=%i >= B: ranges tile [0, N) exactly once', n => {
    let cursor = 0
    for (let b = 0; b < B; b++) {
      const [start, end] = chunkBucketRange(b, n, B)
      expect(start).toBe(cursor)
      expect(end).toBeGreaterThan(start)
      cursor = end
    }
    expect(cursor).toBe(n)
  })

  it.each([1, 3, B - 1])('N=%i < B: every bucket maps to exactly one real chunk', n => {
    const seen = new Set<number>()
    let previous = 0
    for (let b = 0; b < B; b++) {
      const [start, end] = chunkBucketRange(b, n, B)
      expect(end - start).toBe(1)
      expect(start).toBeGreaterThanOrEqual(previous)
      expect(start).toBeLessThan(n)
      previous = start
      seen.add(start)
    }
    expect(seen.size).toBe(n)
  })
})

describe('bucketChunkStates', () => {
  it('returns an empty array when there is nothing to draw', () => {
    expect(bucketChunkStates([], B)).toHaveLength(0)
    expect(bucketChunkStates([1, 2], 0)).toHaveLength(0)
    expect(bucketChunkStates([1, 2], -4)).toHaveLength(0)
    expect(bucketChunkStates([1, 2], Number.NaN)).toHaveLength(0)
  })

  it('returns a fixed-length typed array', () => {
    const out = bucketChunkStates(filled(10007, CHUNK_COMPLETE), B)
    expect(out).toBeInstanceOf(Uint8Array)
    expect(out).toHaveLength(B)
  })

  it('floors a fractional bucket count', () => {
    expect(bucketChunkStates([CHUNK_COMPLETE], 3.9)).toHaveLength(3)
  })

  it('any downloading chunk wins its bucket', () => {
    const states = filled(B * 4, CHUNK_COMPLETE)
    states[4 * 7 + 2] = CHUNK_DOWNLOADING
    states[4 * 9 + 1] = CHUNK_PENDING
    const out = bucketChunkStates(states, B)
    expect(out[7]).toBe(CHUNK_DOWNLOADING)
    expect(out[9]).toBe(CHUNK_PENDING)
    expect(out[8]).toBe(CHUNK_COMPLETE)
  })

  it('a bucket is complete only when every chunk is complete', () => {
    const out = bucketChunkStates(
      [CHUNK_COMPLETE, CHUNK_COMPLETE, CHUNK_COMPLETE, CHUNK_PENDING],
      2,
    )
    expect(Array.from(out)).toEqual([CHUNK_COMPLETE, CHUNK_PENDING])
  })

  it('treats unknown values as pending', () => {
    const out = bucketChunkStates([CHUNK_COMPLETE, 7, -1, Number.NaN], 4)
    expect(Array.from(out)).toEqual([CHUNK_COMPLETE, CHUNK_PENDING, CHUNK_PENDING, CHUNK_PENDING])
  })

  it.each([
    ['all pending', CHUNK_PENDING],
    ['all complete', CHUNK_COMPLETE],
  ])('%s stays uniform', (_label, state) => {
    for (const n of [1, 3, B - 1, B, B + 1, 10007]) {
      expect(bucketChunkStates(filled(n, state), B).every(v => v === state)).toBe(true)
    }
  })

  it('a single downloading chunk lights exactly one bucket when N >= B', () => {
    for (const n of [B, B + 1, 10007, 100000]) {
      const states = filled(n, CHUNK_PENDING)
      states[Math.floor(n / 2)] = CHUNK_DOWNLOADING
      const out = bucketChunkStates(states, B)
      expect(out.filter(v => v === CHUNK_DOWNLOADING)).toHaveLength(1)
    }
  })

  it('N < B stretches real chunks in order and never invents states', () => {
    const out = bucketChunkStates([CHUNK_COMPLETE, CHUNK_DOWNLOADING, CHUNK_PENDING], B)
    expect(out).toHaveLength(B)
    const firstDownloading = out.indexOf(CHUNK_DOWNLOADING)
    const firstPending = out.indexOf(CHUNK_PENDING)
    expect(out[0]).toBe(CHUNK_COMPLETE)
    expect(firstDownloading).toBeGreaterThan(0)
    expect(firstPending).toBeGreaterThan(firstDownloading)
    expect(out[B - 1]).toBe(CHUNK_PENDING)
    // Monotone: once a later chunk starts, an earlier one never reappears.
    const order = Array.from(out).filter((v, i, arr) => i === 0 || v !== arr[i - 1])
    expect(order).toEqual([CHUNK_COMPLETE, CHUNK_DOWNLOADING, CHUNK_PENDING])
  })

  it('keeps the frontier contiguous: a downloading run maps to a contiguous bucket run', () => {
    const n = 10007
    const states = filled(n, CHUNK_PENDING)
    for (let i = 0; i < 4000; i++) states[i] = CHUNK_COMPLETE
    for (let i = 4000; i < 4400; i++) states[i] = CHUNK_DOWNLOADING
    const out = Array.from(bucketChunkStates(states, B))
    const first = out.indexOf(CHUNK_DOWNLOADING)
    const last = out.lastIndexOf(CHUNK_DOWNLOADING)
    expect(out.slice(first, last + 1).every(v => v === CHUNK_DOWNLOADING)).toBe(true)
    expect(out.slice(0, first).every(v => v === CHUNK_COMPLETE)).toBe(true)
    expect(out.slice(last + 1).every(v => v === CHUNK_PENDING)).toBe(true)
  })

  it('handles 100k chunks in a single pass', () => {
    const states = filled(100000, CHUNK_COMPLETE)
    const started = performance.now()
    const out = bucketChunkStates(states, B)
    expect(performance.now() - started).toBeLessThan(50)
    expect(out.every(v => v === CHUNK_COMPLETE)).toBe(true)
  })
})

describe('layoutChunkGrid', () => {
  it('returns null for empty or invalid boxes', () => {
    expect(layoutChunkGrid(0, 30, 1)).toBeNull()
    expect(layoutChunkGrid(160, 0, 1)).toBeNull()
    expect(layoutChunkGrid(160, 30, 0)).toBeNull()
    expect(layoutChunkGrid(Number.NaN, 30, 1)).toBeNull()
  })

  it('keeps whole beats of four columns', () => {
    for (const width of [120, 130, 144, 160, 176]) {
      const grid = layoutChunkGrid(width, 30, 1)!
      expect(grid.columns % 4).toBe(0)
      expect(grid.columns).toBeLessThanOrEqual(32)
    }
  })

  it.each([1, 1.25, 1.5, 2])(
    'snaps to whole device pixels and stays inside the box at dpr %s',
    dpr => {
      for (const width of [120, 130, 160, 176]) {
        const height = 30
        const grid = layoutChunkGrid(width, height, dpr)!
        const deviceWidth = Math.round(width * dpr)
        const deviceHeight = Math.round(height * dpr)
        expect(grid.columnX.every(Number.isInteger)).toBe(true)
        expect(grid.rowY.every(Number.isInteger)).toBe(true)
        expect(Number.isInteger(grid.columnWidth)).toBe(true)
        expect(Number.isInteger(grid.rowHeight)).toBe(true)
        expect(grid.columnX[0]).toBeGreaterThanOrEqual(0)
        expect(grid.columnX[grid.columns - 1] + grid.columnWidth).toBeLessThanOrEqual(deviceWidth)
        expect(grid.rowY[grid.rows - 1] + grid.rowHeight).toBeLessThanOrEqual(deviceHeight)
        for (let c = 1; c < grid.columns; c++) {
          expect(grid.columnX[c]).toBeGreaterThan(grid.columnX[c - 1] + grid.columnWidth - 1)
        }
      }
    },
  )

  it('column count does not change with the display scale', () => {
    const base = layoutChunkGrid(150, 30, 1)!.columns
    expect(layoutChunkGrid(150, 30, 1.25)!.columns).toBe(base)
    expect(layoutChunkGrid(150, 30, 1.5)!.columns).toBe(base)
  })

  it('falls back to partial columns in a box narrower than one beat', () => {
    const grid = layoutChunkGrid(12, 30, 1)!
    expect(grid.columns).toBeGreaterThan(0)
    expect(grid.columns).toBeLessThan(4)
  })
})
