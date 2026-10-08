import { describe, expect, it } from 'vitest'
import {
  CHUNK_COMPLETE,
  CHUNK_DOWNLOADING,
  CHUNK_PENDING,
  bucketChunkProgress,
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

describe('bucketChunkProgress', () => {
  it('returns null when the byte track is unusable', () => {
    const states = [CHUNK_DOWNLOADING]
    expect(bucketChunkProgress(states, null, 100, 400, 4)).toBeNull()
    expect(bucketChunkProgress(states, undefined, 100, 400, 4)).toBeNull()
    expect(bucketChunkProgress(states, [], 100, 400, 4)).toBeNull()
    expect(bucketChunkProgress(states, [10], 0, 400, 4)).toBeNull()
    expect(bucketChunkProgress(states, [10], -1, 400, 4)).toBeNull()
    expect(bucketChunkProgress(states, [10], 100, 0, 4)).toBeNull()
    expect(bucketChunkProgress(states, [10], 100, -5, 4)).toBeNull()
    expect(bucketChunkProgress(states, [10], 100, Number.NaN, 4)).toBeNull()
    expect(bucketChunkProgress([], [10], 100, 400, 4)).toBeNull()
    expect(bucketChunkProgress(states, [10], 100, 400, 0)).toBeNull()
    expect(bucketChunkProgress(states, [10], 100, 400, -3)).toBeNull()
    expect(bucketChunkProgress(states, [10], 100, 400, Number.NaN)).toBeNull()
  })

  it('a half-filled single chunk lights only the buckets its bytes cover', () => {
    // One 100B chunk in a 400B file: bucket 0 is [0,100), the rest are
    // outside the chunk entirely.
    const states = [CHUNK_DOWNLOADING]
    const out = bucketChunkProgress(states, [50], 100, 400, 4)!
    expect(Array.from(out)).toEqual([CHUNK_DOWNLOADING, CHUNK_PENDING, CHUNK_PENDING, CHUNK_PENDING])
    // Full recorded bytes complete the bucket even while status lags behind.
    const full = bucketChunkProgress(states, [100], 100, 400, 4)!
    expect(full[0]).toBe(CHUNK_COMPLETE)
  })

  it('a continuous multi-chunk frontier grades complete → downloading → pending', () => {
    const states = [CHUNK_COMPLETE, CHUNK_DOWNLOADING, CHUNK_PENDING, CHUNK_PENDING]
    const out = bucketChunkProgress(states, [100, 50, 0, 0], 100, 400, 4)!
    expect(Array.from(out)).toEqual([
      CHUNK_COMPLETE,
      CHUNK_DOWNLOADING,
      CHUNK_PENDING,
      CHUNK_PENDING,
    ])
  })

  it('sub-chunk buckets share a chunk by byte range', () => {
    const states = [CHUNK_COMPLETE, CHUNK_DOWNLOADING, CHUNK_PENDING, CHUNK_PENDING]
    // B=8 over T=400: every bucket covers 50B, half a chunk each.
    const out = bucketChunkProgress(states, [100, 50, 0, 0], 100, 400, 8)!
    // chunk0 complete → buckets 0-1 complete. chunk1 has 50B → bucket 2
    // ([100,150)) is fully downloaded, bucket 3 ([150,200)) sees nothing.
    expect(Array.from(out)).toEqual([
      CHUNK_COMPLETE,
      CHUNK_COMPLETE,
      CHUNK_COMPLETE,
      CHUNK_PENDING,
      CHUNK_PENDING,
      CHUNK_PENDING,
      CHUNK_PENDING,
      CHUNK_PENDING,
    ])
  })

  it('clamps the tail chunk to its real extent', () => {
    // T=350 with 100B chunks: chunk3 covers only [300,350).
    const states = [CHUNK_COMPLETE, CHUNK_COMPLETE, CHUNK_COMPLETE, CHUNK_DOWNLOADING]
    const out = bucketChunkProgress(states, [100, 100, 100, 25], 100, 350, 4)!
    // bucket3 = [262,350): chunk2 contributes [262,300)=38, chunk3 25B → 63/88.
    expect(out[3]).toBe(CHUNK_DOWNLOADING)
    // A bogus progress beyond the tail extent is clamped: 50B real caps the
    // overlap at [300,350)=50 → 38+50 = 88 fills the bucket.
    const clamped = bucketChunkProgress(states, [100, 100, 100, 100], 100, 350, 4)!
    expect(clamped[3]).toBe(CHUNK_COMPLETE)
  })

  it('counts bytes for a non-complete state even when the status says pending', () => {
    // A rescale may mark a chunk with real bytes pending; the bytes still count.
    const out = bucketChunkProgress([CHUNK_PENDING], [50], 100, 100, 1)!
    expect(out[0]).toBe(CHUNK_DOWNLOADING)
  })

  it('treats missing, negative and NaN progress as unknown; downloading keeps a +1 approx', () => {
    // progress shorter than states: chunk1 has no entry.
    const short = bucketChunkProgress([CHUNK_DOWNLOADING, CHUNK_DOWNLOADING], [100], 100, 200, 2)!
    expect(short[0]).toBe(CHUNK_COMPLETE)
    expect(short[1]).toBe(CHUNK_DOWNLOADING) // approx +1
    // Invalid entries behave the same as missing.
    for (const bad of [-1, Number.NaN]) {
      const out = bucketChunkProgress([CHUNK_DOWNLOADING], [bad], 100, 100, 1)!
      expect(out[0]).toBe(CHUNK_DOWNLOADING)
    }
    // The +1 approx grant lights every bucket the chunk overlaps.
    const out = bucketChunkProgress([CHUNK_DOWNLOADING], [0], 100, 100, 2)!
    expect(Array.from(out)).toEqual([CHUNK_DOWNLOADING, CHUNK_DOWNLOADING])
  })

  it('unknown state values count as non-complete and contribute no bytes', () => {
    const out = bucketChunkProgress([7, CHUNK_COMPLETE], [undefined as never, 100], 100, 200, 2)!
    expect(Array.from(out)).toEqual([CHUNK_PENDING, CHUNK_COMPLETE])
  })

  it('a uniform complete map stays complete at any bucket count', () => {
    for (const b of [1, 4, B]) {
      const states = filled(10, CHUNK_COMPLETE)
      const out = bucketChunkProgress(states, filled(10, 100), 100, 1000, b)!
      expect(out).toHaveLength(b)
      expect(out.every(v => v === CHUNK_COMPLETE)).toBe(true)
    }
  })

  it('handles 10k chunks x 128 buckets in a single pass', () => {
    const states = filled(10000, CHUNK_DOWNLOADING)
    const progress = filled(10000, 12345)
    const started = performance.now()
    const out = bucketChunkProgress(states, progress, 2 * 1024 * 1024, 10000 * 2 * 1024 * 1024, B)!
    expect(performance.now() - started).toBeLessThan(50)
    expect(out).toHaveLength(B)
    expect(out.every(v => v === CHUNK_DOWNLOADING)).toBe(true)
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
