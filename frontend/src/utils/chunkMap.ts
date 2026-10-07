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
