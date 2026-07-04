import { sendMessage } from 'webext-bridge/content-script'
import type { PairSecretMessage } from '../utils/messaging'

// Bilingual copy: the pairing page is standalone HTML outside the Wails
// WebView, so it has no access to the i18n system.
const TEXT_SUCCESS = 'Pairing successful! You can close this tab. / 绑定成功！可以关闭此标签页。'
const TEXT_NO_SECRET = 'Pairing failed: no secret found. / 绑定失败：未找到密钥。'
const TEXT_RETRY = 'Pairing failed: background not reachable. Please reload. / 绑定失败：无法连接后台，请刷新重试。'
const TEXT_CLOSE_FALLBACK = 'You can close this tab manually. / 可手动关闭此标签页。'

function setStatus(text: string): void {
  const p = document.querySelector('p')
  if (p) {
    p.textContent = text
  } else {
    // Fallback: append a paragraph if the page lacks one.
    const fallback = document.createElement('p')
    fallback.textContent = text
    document.body.appendChild(fallback)
  }
}

async function pair(): Promise<void> {
  const cfgEl = document.querySelector('#cfg')
  const secret = cfgEl?.getAttribute('data-secret') ?? ''
  if (!secret) {
    setStatus(TEXT_NO_SECRET)
    return
  }

  let result: { ok: boolean } | undefined
  try {
    result = await sendMessage<{ ok: boolean }>('pair:secret', { secret } satisfies PairSecretMessage, 'background')
  } catch {
    // Background SW may not be ready yet, or the message round-trip failed.
    setStatus(TEXT_RETRY)
    return
  }

  if (!result?.ok) {
    // Background rejected the secret (empty or storage write failure).
    setStatus(TEXT_RETRY)
    return
  }

  setStatus(TEXT_SUCCESS)
  // Some browsers block window.close() for non-script-opened tabs.
  // Wait 3s for the success message to be visible, then attempt close;
  // if the tab is still alive 200ms later, close was blocked.
  setTimeout(() => {
    window.close()
    setTimeout(() => setStatus(`${TEXT_SUCCESS}\n${TEXT_CLOSE_FALLBACK}`), 200)
  }, 3000)
}

void pair()
