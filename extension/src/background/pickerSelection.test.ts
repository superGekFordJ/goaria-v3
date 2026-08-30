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

  it('handles sparse item indices properly for default selection and initial policy', () => {
    const sparse = [{ index: 0 }, { index: 2 }]
    expect(defaultSelectedIndices(sparse)).toEqual([0, 2])
    expect(initialPickerSelection('window', sparse)).toEqual([0, 2])
    expect(initialPickerSelection('empty', sparse)).toEqual([])

    // 100 items with sparse indices 0, 2, 4...
    const largeSparse = Array.from({ length: 100 }, (_, i) => ({ index: i * 2 }))
    const selected = defaultSelectedIndices(largeSparse)
    expect(selected).toHaveLength(80)
    expect(selected[0]).toBe(0)
    expect(selected[79]).toBe(158)
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

  it('supports filtered subset selectAll and invert without losing hidden selections', () => {
    const initial = new Set([0, 5])
    const filterSubset = [2, 3]

    // selectAll on filter subset merges with existing selections
    const allFiltered = selectAll(filterSubset, initial)
    expect([...allFiltered].sort((a, b) => a - b)).toEqual([0, 2, 3, 5])

    // invert on filter subset flips only the filtered items, preserving 0 and 5
    const inverted = invert(initial, filterSubset)
    expect([...inverted].sort((a, b) => a - b)).toEqual([0, 2, 3, 5])

    const partiallySelected = new Set([0, 2, 5])
    const inverted2 = invert(partiallySelected, filterSubset)
    // 0 and 5 kept, 2 turned off, 3 turned on
    expect([...inverted2].sort((a, b) => a - b)).toEqual([0, 3, 5])
  })

  it('ignores out-of-universe toggles and does not treat missing/invalid sizes as positive', () => {
    const selected = toggleIndex(new Set([0]), 99, 3)
    expect([...selected]).toEqual([0])

    // Sparse selectable indices (array and Set)
    const sparseSelectable = [0, 2]
    const toggled = toggleIndex(new Set([0]), 2, sparseSelectable)
    expect([...toggled].sort((a, b) => a - b)).toEqual([0, 2])
    const noGhost = toggleIndex(new Set([0]), 1, sparseSelectable)
    expect([...noGhost]).toEqual([0])

    const setSelectable = new Set([0, 2])
    const toggledSet = toggleIndex(new Set([0]), 2, setSelectable)
    expect([...toggledSet].sort((a, b) => a - b)).toEqual([0, 2])
    const toggledOff = toggleIndex(toggledSet, 2, setSelectable)
    expect([...toggledOff]).toEqual([0])

    const bytes = selectedBytes(
      [
        { index: 0, size_bytes: 10 },
        { index: 1, filename: 'x' } as { index: number; size_bytes?: number },
        { index: 2, size_bytes: 5 },
      ],
      new Set([0, 1, 2]),
    )
    expect(bytes).toBe(15)

    // Sparse array index lookup
    const sparseBytes = selectedBytes(
      [
        { index: 0, size_bytes: 100 },
        { index: 2, size_bytes: 250 },
      ],
      new Set([2]),
    )
    expect(sparseBytes).toBe(250)

    // Non-positive or non-integer sizes ignored
    expect(
      selectedBytes(
        [
          { index: 0, size_bytes: 0 },
          { index: 1, size_bytes: -10 },
          { index: 2, size_bytes: 12.5 },
          { index: 3, size_bytes: undefined },
        ],
        new Set([0, 1, 2, 3]),
      ),
    ).toBe(0)
  })

  it('shifts the visible window so the active index stays in view', () => {
    expect(visibleWindow(0, 100, 80)).toEqual({ start: 0, end: 80 })
    const nearEnd = visibleWindow(90, 100, 80)
    expect(nearEnd.end - nearEnd.start).toBe(80)
    expect(90 >= nearEnd.start && 90 < nearEnd.end).toBe(true)
    expect(visibleWindow(50, 40, 80)).toEqual({ start: 0, end: 40 })
  })
})
