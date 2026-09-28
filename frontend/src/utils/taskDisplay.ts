const SIZE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']

function unitIndex(bytes: number): number {
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), SIZE_UNITS.length - 1)
  return Math.max(0, i)
}

// Format bytes to human readable with bounds safety
export function formatSize(b: string | number | undefined): string {
  if (b === undefined || b === null || b === '') return '0 B'
  const bytes = Number(b)
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const i = unitIndex(bytes)
  return (bytes / Math.pow(1024, i)).toFixed(2) + ' ' + SIZE_UNITS[i]
}

// Speed number without unit (pair with speedUnit)
export function formatSpeed(b: string | number | undefined): string {
  if (!b || b === '0') return '0'
  const bytes = Number(b)
  if (!Number.isFinite(bytes) || bytes <= 0) return '0'
  const i = unitIndex(bytes)
  return (bytes / Math.pow(1024, i)).toFixed(1)
}

export function speedUnit(b: string | number | undefined): string {
  if (!b || b === '0') return 'B/s'
  const bytes = Number(b)
  if (!Number.isFinite(bytes) || bytes <= 0) return 'B/s'
  return SIZE_UNITS[unitIndex(bytes)] + '/s'
}

// Real runtime connection telemetry only; null when absent (no fallbacks).
export function parseLiveThreadCount(task: unknown): number | null {
  const raw = (task as { threads?: string | number } | null | undefined)?.threads
  if (raw !== undefined && raw !== null && raw !== '') {
    const parsed = Number(raw)
    if (Number.isFinite(parsed) && parsed > 0) return parsed
  }
  return null
}

const MINUTE_MS = 60_000
const HOUR_MS = 60 * MINUTE_MS
const DAY_MS = 24 * HOUR_MS

/**
 * Relative time for a unix-seconds timestamp. Under a minute (or in the
 * future, e.g. clock skew) returns justNowText; beyond a day falls back to a
 * short localized date-time.
 */
export function formatRelativeTime(
  tsSec: number,
  nowMs: number,
  locale: string,
  justNowText: string,
): string {
  const tsMs = tsSec * 1000
  const diff = nowMs - tsMs
  if (diff < MINUTE_MS) return justNowText
  if (diff < HOUR_MS) {
    return new Intl.RelativeTimeFormat(locale, { numeric: 'auto', style: 'short' }).format(
      -Math.floor(diff / MINUTE_MS),
      'minute',
    )
  }
  if (diff < DAY_MS) {
    return new Intl.RelativeTimeFormat(locale, { numeric: 'auto', style: 'short' }).format(
      -Math.floor(diff / HOUR_MS),
      'hour',
    )
  }
  return new Intl.DateTimeFormat(locale, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(tsMs))
}

export function formatAbsoluteTime(tsSec: number, locale: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'medium' }).format(
    new Date(tsSec * 1000),
  )
}

export interface SourceDisplay {
  /** Hostname (with non-default port); raw string when unparsable. */
  label: string
  /** Full URL without userinfo, for tooltips. */
  title: string
  /** Primary URL as given, for copying. */
  raw: string
  /** Number of additional mirrors beyond the primary. */
  extraCount: number
}

export function sourceDisplay(uris: readonly string[] | null | undefined): SourceDisplay | null {
  const list = (uris ?? []).map(u => u?.trim()).filter((u): u is string => !!u)
  const unique = [...new Set(list)]
  const raw = unique[0]
  if (!raw) return null
  const extraCount = unique.length - 1

  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return { label: raw, title: raw, raw, extraCount }
  }
  if (!parsed.hostname) {
    return { label: raw, title: raw, raw, extraCount }
  }
  // URL.port is empty for the scheme's default port.
  const label = parsed.port ? `${parsed.hostname}:${parsed.port}` : parsed.hostname
  parsed.username = ''
  parsed.password = ''
  return { label, title: parsed.toString(), raw, extraCount }
}

/**
 * Keeps, in priority order, the cells that fit a `columns`-wide grid within
 * `maxRows` rows under CSS sparse auto-placement. A cell that would spill
 * past the last row is dropped without advancing the cursor, so the result
 * is exactly what the browser lays out for the kept cells.
 */
export function packGridCells<T extends { span: number }>(
  cells: readonly T[],
  columns: number,
  maxRows: number,
): T[] {
  const kept: T[] = []
  let row = 0
  let col = 0
  for (const cell of cells) {
    const span = Math.min(Math.max(1, cell.span), columns)
    let r = row
    let c = col
    if (c + span > columns) {
      r += 1
      c = 0
    }
    if (r >= maxRows) continue
    kept.push(cell)
    row = r
    col = c + span
  }
  return kept
}

/** Directory of a file path (either separator); '' when there is none. */
export function parentDirectory(path: string | null | undefined): string {
  if (!path) return ''
  const idx = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  if (idx <= 0) return ''
  return path.slice(0, idx)
}
