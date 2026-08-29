import { beforeEach, describe, expect, it, vi } from 'vitest'

const firefoxMode = vi.hoisted(() => ({ on: false }))

vi.mock('../utils/extensionInfo', () => ({
  isFirefox: () => firefoxMode.on,
  isChrome: () => !firefoxMode.on,
  getExtensionBrowserTarget: () => (firefoxMode.on ? 'firefox' : 'chrome'),
}))

import { burstAliveMissShouldCancel } from './burstAlivePolicy'

describe('burstAliveMissShouldCancel', () => {
  beforeEach(() => {
    firefoxMode.on = false
  })

  it('treats a missed alive ping as Cancel on Chrome', () => {
    expect(burstAliveMissShouldCancel()).toBe(true)
  })

  it('does not treat a missed alive ping as Cancel on Firefox', () => {
    firefoxMode.on = true
    expect(burstAliveMissShouldCancel()).toBe(false)
  })
})
