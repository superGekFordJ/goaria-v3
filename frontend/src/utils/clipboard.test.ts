import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { copyToClipboard, clearClipboardIfMatches } from './clipboard'

describe('clipboard utils', () => {
  const writeText = vi.fn()
  const readText = vi.fn()

  beforeEach(() => {
    writeText.mockResolvedValue(undefined)
    readText.mockResolvedValue('')
    Object.defineProperty(globalThis, 'navigator', {
      value: { clipboard: { writeText, readText } },
      writable: true,
      configurable: true,
    })
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  describe('copyToClipboard', () => {
    it('writes text and returns true on success', async () => {
      const result = await copyToClipboard('hello')
      expect(writeText).toHaveBeenCalledWith('hello')
      expect(result).toBe(true)
    })

    it('returns false on write failure', async () => {
      writeText.mockRejectedValueOnce(new Error('denied'))
      const result = await copyToClipboard('hello')
      expect(result).toBe(false)
    })
  })

  describe('clearClipboardIfMatches', () => {
    it('clears clipboard when it contains the matching URL', async () => {
      readText.mockResolvedValueOnce('http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc')
      await clearClipboardIfMatches('http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc')
      expect(writeText).toHaveBeenCalledWith('')
    })

    it('does not clear when clipboard content does not match', async () => {
      readText.mockResolvedValueOnce('https://example.com/file.zip')
      await clearClipboardIfMatches('http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc')
      expect(writeText).not.toHaveBeenCalled()
    })

    it('swallows read errors silently', async () => {
      readText.mockRejectedValueOnce(new Error('denied'))
      await expect(clearClipboardIfMatches('http://127.0.0.1:16810/pair')).resolves.toBeUndefined()
    })
  })
})
