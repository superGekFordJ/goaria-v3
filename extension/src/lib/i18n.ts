import browser from 'webextension-polyfill'
import type { I18nKey } from './i18n-keys'

/** Resolve a localized message. Falls back to the key itself if missing. */
export function t(key: I18nKey, substitutions?: string[]): string {
  const msg = browser.i18n.getMessage(key, substitutions)
  return msg || key
}
