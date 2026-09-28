import { describe, expect, it } from 'vitest'
import {
  formatRelativeTime,
  formatSize,
  formatSpeed,
  packGridCells,
  parentDirectory,
  parseLiveThreadCount,
  sourceDisplay,
  speedUnit,
} from './taskDisplay'

describe('migrated card formatters', () => {
  it.each([
    [undefined, '0 B'],
    ['', '0 B'],
    ['0', '0 B'],
    ['-5', '0 B'],
    ['abc', '0 B'],
    [512, '512.00 B'],
    ['1024', '1.00 KB'],
    [1536, '1.50 KB'],
    [7520000, '7.17 MB'],
    [1024 ** 6, '1024.00 PB'],
  ])('formatSize(%s) = %s', (input, want) => {
    expect(formatSize(input as string | number | undefined)).toBe(want)
  })

  it.each([
    [undefined, '0', 'B/s'],
    ['0', '0', 'B/s'],
    [0, '0', 'B/s'],
    ['nope', '0', 'B/s'],
    [900, '900.0', 'B/s'],
    ['512000', '500.0', 'KB/s'],
    [5 * 1024 * 1024, '5.0', 'MB/s'],
  ])('formatSpeed/speedUnit(%s)', (input, value, unit) => {
    expect(formatSpeed(input as string | number | undefined)).toBe(value)
    expect(speedUnit(input as string | number | undefined)).toBe(unit)
  })

  it('parseLiveThreadCount reads only positive telemetry', () => {
    expect(parseLiveThreadCount({ threads: '8' })).toBe(8)
    expect(parseLiveThreadCount({ threads: 3 })).toBe(3)
    expect(parseLiveThreadCount({ threads: '0' })).toBeNull()
    expect(parseLiveThreadCount({ threads: '' })).toBeNull()
    expect(parseLiveThreadCount({})).toBeNull()
    expect(parseLiveThreadCount(null)).toBeNull()
  })
})

describe('formatRelativeTime', () => {
  const now = Date.UTC(2026, 0, 15, 12, 0, 0)
  const sec = (ms: number) => Math.floor(ms / 1000)

  it('uses the just-now text under a minute and for future timestamps', () => {
    expect(formatRelativeTime(sec(now - 30_000), now, 'en', 'JUST')).toBe('JUST')
    expect(formatRelativeTime(sec(now + 60_000), now, 'en', 'JUST')).toBe('JUST')
  })

  it('switches to minutes, hours, then a short date', () => {
    const minutes = formatRelativeTime(sec(now - 5 * 60_000), now, 'en', 'JUST')
    expect(minutes).toMatch(/5/)
    expect(minutes).toMatch(/min/)
    const hours = formatRelativeTime(sec(now - 3 * 3_600_000), now, 'en', 'JUST')
    expect(hours).toMatch(/3/)
    expect(hours).toMatch(/hr|hour/)
    const older = formatRelativeTime(sec(now - 3 * 86_400_000), now, 'en', 'JUST')
    expect(older).toMatch(/Jan/)
  })
})

describe('sourceDisplay', () => {
  it('returns null without usable uris', () => {
    expect(sourceDisplay(undefined)).toBeNull()
    expect(sourceDisplay(['', '  '])).toBeNull()
  })

  it('shows host, keeps non-default ports, strips userinfo from the title', () => {
    const s = sourceDisplay(['https://user:secret@example.com:8443/a/b.zip?x=1'])
    expect(s?.label).toBe('example.com:8443')
    expect(s?.title).toBe('https://example.com:8443/a/b.zip?x=1')
    expect(s?.title).not.toContain('secret')
    expect(s?.raw).toBe('https://user:secret@example.com:8443/a/b.zip?x=1')
    expect(sourceDisplay(['https://example.com:443/f'])?.label).toBe('example.com')
  })

  it('counts extra unique mirrors as +N', () => {
    const s = sourceDisplay(['http://a/f', 'http://b/f', 'http://a/f', 'http://c/f'])
    expect(s?.label).toBe('a')
    expect(s?.extraCount).toBe(2)
  })

  it('falls back to the raw string when unparsable', () => {
    const s = sourceDisplay(['not a url'])
    expect(s).toEqual({ label: 'not a url', title: 'not a url', raw: 'not a url', extraCount: 0 })
  })
})

describe('packGridCells', () => {
  const cell = (id: string, span: number) => ({ id, span })
  const ids = (cells: Array<{ id: string }>) => cells.map(c => c.id)

  it('fills two rows of four in priority order', () => {
    const packed = packGridCells(
      [cell('a', 1), cell('b', 2), cell('c', 1), cell('d', 2), cell('e', 1), cell('f', 1), cell('g', 1)],
      4,
      2,
    )
    expect(ids(packed)).toEqual(['a', 'b', 'c', 'd', 'e', 'f'])
  })

  it('shifts later cells left when an earlier one is absent', () => {
    const packed = packGridCells([cell('a', 1), cell('b', 2), cell('d', 1), cell('src', 2)], 4, 2)
    expect(ids(packed)).toEqual(['a', 'b', 'd', 'src'])
  })

  it('drops a wide cell that would spill into a third row', () => {
    const packed = packGridCells([cell('err', 4), cell('src', 2), cell('save', 2), cell('x', 2)], 4, 2)
    expect(ids(packed)).toEqual(['err', 'src', 'save'])
  })

  it('backfills a narrow cell after dropping a wide one, matching CSS layout', () => {
    const packed = packGridCells([cell('a', 2), cell('b', 1), cell('c', 2), cell('d', 2), cell('e', 1)], 4, 2)
    // a b | c d  -> e has no room; a b _ | c d
    expect(ids(packed)).toEqual(['a', 'b', 'c', 'd'])
    const backfill = packGridCells([cell('a', 2), cell('b', 1), cell('c', 4), cell('d', 1)], 4, 2)
    // c wraps to row 2 (full), d cannot fit anymore
    expect(ids(backfill)).toEqual(['a', 'b', 'c'])
  })
})

describe('parentDirectory', () => {
  it('handles both separators', () => {
    expect(parentDirectory('C:\\Downloads\\file.zip')).toBe('C:\\Downloads')
    expect(parentDirectory('/home/u/file.zip')).toBe('/home/u')
    expect(parentDirectory('file.zip')).toBe('')
    expect(parentDirectory(undefined)).toBe('')
  })
})
