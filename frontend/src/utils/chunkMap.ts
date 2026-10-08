/** Per-chunk download states as reported by the Surge engine. */
export const CHUNK_PENDING = 0
export const CHUNK_DOWNLOADING = 1
export const CHUNK_COMPLETE = 2

export type ChunkState = typeof CHUNK_PENDING | typeof CHUNK_DOWNLOADING | typeof CHUNK_COMPLETE

/**
 * Half-open chunk range `[start, end)` summarized by bucket `index`. With at
 * least as many chunks as buckets the ranges tile `[0, chunkCount)` exactly
 * once; with fewer, neighbouring buckets repeat the same single chunk so the
 * map stretches evenly instead of inventing chunks that do not exist.
 */
export function chunkBucketRange(
  index: number,
  chunkCount: number,
  bucketCount: number,
): [number, number] {
  const start = Math.floor((index * chunkCount) / bucketCount)
  const end = Math.floor(((index + 1) * chunkCount) / bucketCount)
  return [start, Math.max(end, start + 1)]
}

/**
 * Folds per-chunk states into `bucketCount` buckets, frontier first: any
 * downloading chunk makes the bucket downloading; otherwise a bucket made only
 * of complete chunks is complete; anything else is pending. Values outside the
 * known states count as pending. Returns an empty array when there is nothing
 * to draw (no chunks or no usable bucket count).
 */
export function bucketChunkStates(states: ArrayLike<number>, bucketCount: number): Uint8Array {
  const chunkCount = states?.length ?? 0
  const buckets = Number.isFinite(bucketCount) ? Math.floor(bucketCount) : 0
  if (chunkCount === 0 || buckets <= 0) return new Uint8Array(0)

  const out = new Uint8Array(buckets)
  for (let b = 0; b < buckets; b++) {
    const [start, end] = chunkBucketRange(b, chunkCount, buckets)
    let allComplete = true
    let downloading = false
    for (let i = start; i < end; i++) {
      const state = states[i]
      if (state === CHUNK_DOWNLOADING) {
        downloading = true
        break
      }
      if (state !== CHUNK_COMPLETE) allComplete = false
    }
    out[b] = downloading ? CHUNK_DOWNLOADING : allComplete ? CHUNK_COMPLETE : CHUNK_PENDING
  }
  return out
}

/**
 * Byte-level downsampling: bucket `b` covers the byte range
 * `[floor(b·T/B), floor((b+1)·T/B))` of the real file, and a chunk
 * contributes its downloaded bytes ∩ that range instead of a flat state —
 * so a partially filled chunk lights only the share of buckets it truly
 * occupies. `progress[c] > 0` counts bytes for any non-complete state (the
 * recorded byte count is a higher truth than a status that lags or was
 * rescaled), and is clamped to the chunk's real extent so a short tail
 * chunk cannot spill past the file end.
 *
 * Returns null when the byte track is unusable — missing/empty progress,
 * non-positive chunkSize, invalid totalSize, no states, or an unusable
 * bucket count — so callers fall back to the discrete `bucketChunkStates`.
 */
export function bucketChunkProgress(
  states: ArrayLike<number>,
  progress: ArrayLike<number> | null | undefined,
  chunkSize: number,
  totalSize: number,
  bucketCount: number,
): Uint8Array | null {
  const chunkCount = states?.length ?? 0
  const buckets = Number.isFinite(bucketCount) ? Math.floor(bucketCount) : 0
  if (
    chunkCount === 0 ||
    buckets <= 0 ||
    !progress ||
    progress.length === 0 ||
    !(chunkSize > 0) ||
    !Number.isFinite(totalSize) ||
    totalSize <= 0
  ) {
    return null
  }

  const out = new Uint8Array(buckets)
  for (let b = 0; b < buckets; b++) {
    const blockStart = Math.floor((b * totalSize) / buckets)
    const blockEnd = Math.min(Math.floor(((b + 1) * totalSize) / buckets), totalSize)
    const blockSize = blockEnd - blockStart
    if (blockSize <= 0) {
      out[b] = CHUNK_PENDING
      continue
    }

    const startChunk = Math.min(Math.floor(blockStart / chunkSize), chunkCount - 1)
    const endChunk = Math.min(Math.floor((blockEnd - 1) / chunkSize), chunkCount - 1)
    let downloaded = 0
    let allCompleted = true
    let approx = false

    for (let c = startChunk; c <= endChunk; c++) {
      const state = states[c]
      if (state !== CHUNK_COMPLETE) allCompleted = false

      const chunkStart = c * chunkSize
      const chunkEnd = chunkStart + chunkSize
      const is = Math.max(blockStart, chunkStart)
      const ie = Math.min(blockEnd, chunkEnd)
      if (ie - is <= 0) continue

      if (state === CHUNK_COMPLETE) {
        downloaded += ie - is
        continue
      }
      const p = c < progress.length && Number.isFinite(progress[c]) ? progress[c] : null
      if (p !== null && p > 0) {
        const extent = Math.min(chunkSize, totalSize - chunkStart)
        const validEnd = chunkStart + Math.min(p, extent)
        const vo = Math.min(ie, validEnd) - is
        if (vo > 0) downloaded += vo
      } else if (state === CHUNK_DOWNLOADING) {
        // No byte count for a downloading chunk: grant one byte so the
        // bucket reads active, and flag it so an approximate sliver can
        // never promote the whole bucket to complete on byte math alone.
        downloaded += 1
        approx = true
      }
    }

    out[b] =
      allCompleted || (!approx && downloaded >= blockSize)
        ? CHUNK_COMPLETE
        : downloaded > 0
          ? CHUNK_DOWNLOADING
          : CHUNK_PENDING
  }
  return out
}

// Fiber-column geometry in CSS pixels: slim vertical capsules on a 5px pitch,
// grouped into beats of four columns, each column split into stacked segments.
const COLUMN_WIDTH = 2.8
const COLUMN_PITCH = 5
const BEAT_COLUMNS = 4
const BEAT_GAP = 3
const MAX_COLUMNS = 32
const SEGMENT_ROWS = 4
const SEGMENT_GAP = 2

/**
 * Device-pixel grid for the chunk map. Buckets map column-major
 * (`index = column * rows + row`) so chunk order reads left to right.
 */
export interface ChunkMapGrid {
  columns: number
  rows: number
  /** Left edge of each column, device pixels. */
  columnX: number[]
  columnWidth: number
  /** Top edge of each segment row, device pixels. */
  rowY: number[]
  rowHeight: number
}

function columnOffset(column: number): number {
  return column * COLUMN_PITCH + Math.floor(column / BEAT_COLUMNS) * BEAT_GAP
}

function columnsThatFit(cssWidth: number): number {
  let columns = 0
  while (columns < MAX_COLUMNS && columnOffset(columns) + COLUMN_WIDTH <= cssWidth) columns++
  // Whole beats only, unless the box cannot even hold one.
  return columns >= BEAT_COLUMNS ? columns - (columns % BEAT_COLUMNS) : columns
}

/**
 * Lays the fiber columns out inside a `cssWidth` x `cssHeight` box. Column
 * count is decided in CSS pixels (so it does not jump between display
 * scales); every edge is then snapped to whole device pixels for crisp
 * capsules at fractional ratios. Returns null when nothing fits.
 */
export function layoutChunkGrid(
  cssWidth: number,
  cssHeight: number,
  dpr: number,
): ChunkMapGrid | null {
  if (!(cssWidth > 0) || !(cssHeight > 0) || !(dpr > 0)) return null
  const columns = columnsThatFit(cssWidth)
  const rowHeightCss = (cssHeight - (SEGMENT_ROWS - 1) * SEGMENT_GAP) / SEGMENT_ROWS
  if (columns === 0 || rowHeightCss < 1) return null

  const extent = columnOffset(columns - 1) + COLUMN_WIDTH
  const left = (cssWidth - extent) / 2
  const columnWidth = Math.max(1, Math.round(COLUMN_WIDTH * dpr))
  const columnX: number[] = []
  for (let c = 0; c < columns; c++) columnX.push(Math.round((left + columnOffset(c)) * dpr))

  const rowHeight = Math.max(1, Math.round(rowHeightCss * dpr))
  const rowY: number[] = []
  for (let r = 0; r < SEGMENT_ROWS; r++) {
    rowY.push(Math.round(r * (rowHeightCss + SEGMENT_GAP) * dpr))
  }
  return { columns, rows: SEGMENT_ROWS, columnX, columnWidth, rowY, rowHeight }
}
