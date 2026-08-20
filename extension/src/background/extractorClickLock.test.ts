import { describe, expect, it } from 'vitest'
import {
  cancelAllClicks,
  cancelTabClick,
  clickEpochOf,
  hasInFlight,
  releaseClick,
  tryLockClick,
} from './extractorClickLock'

describe('extractorClickLock', () => {
  it('captures epoch at lock time and ignores a mismatched release', () => {
    const first = tryLockClick(7)
    expect(first).toBe(0)
    expect(hasInFlight(7)).toBe(true)
    expect(tryLockClick(7)).toBeUndefined()
    cancelTabClick(7)
    expect(clickEpochOf(7)).toBe(1)
    expect(hasInFlight(7)).toBe(false)
    const second = tryLockClick(7)
    expect(second).toBe(1)
    releaseClick(7, first ?? 0)
    expect(hasInFlight(7)).toBe(true)
    releaseClick(7, second ?? 0)
    expect(hasInFlight(7)).toBe(false)
  })

  it('bumps every locked tab on cancelAllClicks', () => {
    tryLockClick(2)
    tryLockClick(3)
    cancelAllClicks()
    expect(hasInFlight(2)).toBe(false)
    expect(hasInFlight(3)).toBe(false)
    expect(clickEpochOf(2)).toBeGreaterThan(0)
    expect(clickEpochOf(3)).toBeGreaterThan(0)
  })
})
