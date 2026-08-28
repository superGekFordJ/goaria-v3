import { afterEach, describe, expect, it } from 'vitest'
import {
  bumpDirectConnectGeneration,
  currentDirectConnectGeneration,
  resetDirectConnectGenerationForTests,
} from './domConnectGeneration'

describe('directConnectGeneration', () => {
  afterEach(() => {
    resetDirectConnectGenerationForTests(0)
  })

  it('starts at 0 and bumps as an integer', () => {
    expect(currentDirectConnectGeneration()).toBe(0)
    expect(bumpDirectConnectGeneration()).toBe(1)
    expect(currentDirectConnectGeneration()).toBe(1)
    expect(bumpDirectConnectGeneration()).toBe(2)
  })
})
