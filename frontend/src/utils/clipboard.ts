export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

export async function clearClipboardIfMatches(url: string): Promise<void> {
  try {
    const current = await navigator.clipboard.readText()
    if (current.includes(url)) {
      await navigator.clipboard.writeText('')
    }
  } catch {
    // Clipboard read may be denied; best-effort.
  }
}
