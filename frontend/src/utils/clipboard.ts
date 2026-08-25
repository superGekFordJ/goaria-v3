import { Clipboard } from '@wailsio/runtime'

export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await Clipboard.SetText(text)
    return true
  } catch {
    return false
  }
}

export async function clearClipboardIfMatches(url: string): Promise<void> {
  try {
    const current = await Clipboard.Text()
    if (current && current.includes(url)) {
      await Clipboard.SetText('')
    }
  } catch {
    // Clipboard read may be denied; best-effort.
  }
}
