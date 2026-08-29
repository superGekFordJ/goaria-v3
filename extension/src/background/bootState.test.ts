import { beforeEach, describe, expect, it } from 'vitest'
import { isBootReady, resetBootReadyForTests, setBootReady } from './bootState'

describe('bootState', () => {
  beforeEach(() => {
    resetBootReadyForTests()
  })

  it('defaults to false and is not the websocket gate', () => {
    expect(isBootReady()).toBe(false)
    setBootReady(true)
    expect(isBootReady()).toBe(true)
    resetBootReadyForTests()
    expect(isBootReady()).toBe(false)
  })
})
