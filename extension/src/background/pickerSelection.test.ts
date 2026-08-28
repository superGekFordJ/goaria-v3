import { describe, expect, it } from 'vitest'
import {
  defaultSelectedIndices,
  initialPickerSelection,
  invert,
  selectAll,
  selectedBytes,
  toggleIndex,
  visibleWindow,
  type PickerSelectPolicy,
} from './pickerSelection'

describe('pickerSelection', () => {
  it('defaults to the first 80 of 100 and select-all covers 128 of 128', () => {
    expect(defaultSelectedIndices(100)).toEqual(Array.from({ length: 80 }, (_, i) => i))
    expect(selectAll(128).size).toBe(128)
    expect(selectAll(128).has(127)).toBe(true)
    expect(selectAll(128).has(128)).toBe(false)
  })

  it('applies window vs empty initial-select policy without changing defaultSelectedIndices', () => {
    const windowed = initialPickerSelection('window', 100)
    expect(windowed).toEqual(defaultSelectedIndices(100))
    expect(windowed).toHaveLength(80)
    expect(initialPickerSelection('empty', 100)).toEqual([])
    expect(initialPickerSelection('empty', 0)).toEqual([])
    expect(defaultSelectedIndices(100)).toEqual(Array.from({ length: 80 }, (_, i) => i))
    expect(() => initialPickerSelection('burst' as PickerSelectPolicy, 10)).toThrow(
      /unknown picker select policy/,
    )
  })

  it('inverts the full 100-item universe even when only 80 rows are visible', () => {
    const selected = new Set(defaultSelectedIndices(100, 80))
    const win = visibleWindow(0, 100, 80)
    expect(win).toEqual({ start: 0, end: 80 })
    const flipped = invert(selected, 100)
    expect(flipped.size).toBe(20)
    for (let i = 80; i < 100; i++) expect(flipped.has(i)).toBe(true)
    expect(flipped.has(0)).toBe(false)
  })

  it('ignores out-of-universe toggles and does not treat missing sizes as zero', () => {
    const selected = toggleIndex(new Set([0]), 99, 3)
    expect([...selected]).toEqual([0])
    const bytes = selectedBytes(
      [{ size_bytes: 10 }, { filename: 'x' } as { size_bytes?: number }, { size_bytes: 5 }],
      new Set([0, 1, 2]),
    )
    expect(bytes).toBe(15)
    expect(selectedBytes([{ size_bytes: undefined }, {}], new Set([0, 1]))).toBe(0)
  })

  it('shifts the visible window so the active index stays in view', () => {
    expect(visibleWindow(0, 100, 80)).toEqual({ start: 0, end: 80 })
    const nearEnd = visibleWindow(90, 100, 80)
    expect(nearEnd.end - nearEnd.start).toBe(80)
    expect(90 >= nearEnd.start && 90 < nearEnd.end).toBe(true)
    expect(visibleWindow(50, 40, 80)).toEqual({ start: 0, end: 40 })
  })
})
