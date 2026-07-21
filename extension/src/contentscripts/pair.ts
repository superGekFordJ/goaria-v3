import { sendMessage } from 'webext-bridge/content-script'
import type { PairSecretMessage } from '../utils/messaging'
import { t } from '../lib/i18n'

// pair.ts is an extension-injected content script, so it can use the
// extension's native browser.i18n.getMessage() (not the desktop vue-i18n).
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
    setStatus(t('pair_no_secret'))
    return
  }

  let result: { ok: boolean } | undefined
  try {
    result = await sendMessage<{ ok: boolean }>(
      'pair:secret',
      { secret } satisfies PairSecretMessage,
      'background',
    )
  } catch {
    // Background SW may not be ready yet, or the message round-trip failed.
    setStatus(t('pair_retry'))
    return
  }

  if (!result?.ok) {
    // Background rejected the secret (empty or storage write failure).
    setStatus(t('pair_retry'))
    return
  }

  setStatus(t('pair_success'))
  // Some browsers block window.close() for non-script-opened tabs.
  // Wait 3s for the success message to be visible, then attempt close;
  // if the tab is still alive 200ms later, close was blocked.
  setTimeout(() => {
    window.close()
    // pair_success was already shown for 3s; if close was blocked, only the
    // manual-close hint is needed (avoids repeating "you can close this tab").
    setTimeout(() => setStatus(t('pair_close_fallback')), 200)
  }, 3000)
}

const PAIR_PATH = '/__goaria_pair__/pair.html'

// Defensive self-filter: the manifest match uses a trailing * to absorb the
// ?n=<nonce> query, but if the URL structure ever changes this guard prevents
// side effects on unrelated pages.
if (location.pathname === PAIR_PATH) {
  void pair()
}
