import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { copyToClipboard, clearClipboardIfMatches } from './clipboard'

const setTextMock = vi.fn()
const textMock = vi.fn()

vi.mock('@wailsio/runtime', () => ({
  Clipboard: {
    SetText: (text: string) => setTextMock(text),
    Text: () => textMock(),
  },
}))

describe('clipboard utils', () => {
  beforeEach(() => {
    setTextMock.mockResolvedValue(undefined)
    textMock.mockResolvedValue('')
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  describe('copyToClipboard', () => {
    it('writes text and returns true on success', async () => {
      const result = await copyToClipboard('hello')
      expect(setTextMock).toHaveBeenCalledWith('hello')
      expect(result).toBe(true)
    })

    it('returns false on write failure', async () => {
      setTextMock.mockRejectedValueOnce(new Error('denied'))
      const result = await copyToClipboard('hello')
      expect(result).toBe(false)
    })
  })

  describe('clearClipboardIfMatches', () => {
    it('clears clipboard when it contains the matching URL', async () => {
      textMock.mockResolvedValueOnce('http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc')
      await clearClipboardIfMatches('http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc')
      expect(setTextMock).toHaveBeenCalledWith('')
    })

    it('does not clear when clipboard content does not match', async () => {
      textMock.mockResolvedValueOnce('https://example.com/file.zip')
      await clearClipboardIfMatches('http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc')
      expect(setTextMock).not.toHaveBeenCalled()
    })

    it('swallows read errors silently', async () => {
      textMock.mockRejectedValueOnce(new Error('denied'))
      await expect(clearClipboardIfMatches('http://127.0.0.1:16810/pair')).resolves.toBeUndefined()
    })
  })
})
