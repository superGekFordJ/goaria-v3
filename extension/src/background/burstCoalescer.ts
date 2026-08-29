export type BurstPhase = 'coalescing' | 'picker' | 'submitting'

export type BurstWindowState = {
  captureId: string
  downloadIds: number[]
  firstItemAt: number
  lastItemAt: number
  phase: BurstPhase
}

export type AdmitResult =
  | { kind: 'legacy' }
  | { kind: 'admitted'; window: BurstWindowState }
  | { kind: 'legacy_overflow'; window: BurstWindowState }

export type CloseResult =
  | { kind: 'open' }
  | { kind: 'legacy_single'; window: BurstWindowState }
  | { kind: 'burst'; window: BurstWindowState }

export type CloseOptions = {
  soloQuietMs: number
  groupQuietMs: number
  maxMs: number
}

export function quietWindowFor(
  memberCount: number,
  opts: Pick<CloseOptions, 'soloQuietMs' | 'groupQuietMs'>,
): number {
  return memberCount <= 1 ? opts.soloQuietMs : opts.groupQuietMs
}

export function admitMember(opts: {
  sessionPresent: boolean
  window: BurstWindowState | null
  captureId: string
  downloadId: number
  now: number
  maxItems: number
}): AdmitResult {
  if (!opts.sessionPresent) return { kind: 'legacy' }
  const existing = opts.window
  if (existing && existing.phase !== 'coalescing') {
    return { kind: 'legacy' }
  }
  const ids = existing?.downloadIds ?? []
  if (ids.length >= opts.maxItems) {
    return { kind: 'legacy_overflow', window: existing! }
  }
  if (ids.includes(opts.downloadId)) {
    return { kind: 'admitted', window: existing! }
  }
  if (!existing) {
    return {
      kind: 'admitted',
      window: {
        captureId: opts.captureId,
        downloadIds: [opts.downloadId],
        firstItemAt: opts.now,
        lastItemAt: opts.now,
        phase: 'coalescing',
      },
    }
  }
  return {
    kind: 'admitted',
    window: {
      ...existing,
      downloadIds: [...existing.downloadIds, opts.downloadId],
      lastItemAt: opts.now,
    },
  }
}

export function evaluateClose(
  window: BurstWindowState,
  now: number,
  opts: CloseOptions,
): CloseResult {
  if (window.phase !== 'coalescing') return { kind: 'open' }
  const quietMs = quietWindowFor(window.downloadIds.length, opts)
  const quietDue = now >= window.lastItemAt + quietMs
  const maxDue = now >= window.firstItemAt + opts.maxMs
  if (!quietDue && !maxDue) return { kind: 'open' }
  if (window.downloadIds.length <= 1) return { kind: 'legacy_single', window }
  return { kind: 'burst', window }
}
