import { createI18n } from 'vue-i18n'
import zhCN from './locales/zh-CN.json'
import zhTW from './locales/zh-TW.json'
import en from './locales/en.json'
import ja from './locales/ja.json'
import es from './locales/es.json'
import de from './locales/de.json'

// Resolve locale based on preference
export function resolveLocale(preference: string): string {
  if (preference === 'auto') {
    const systemLang = navigator.language
    // Traditional Chinese
    if (['zh-TW', 'zh-HK', 'zh-MO', 'zh-Hant'].some(tag => systemLang.startsWith(tag))) {
      return 'zh-TW'
    }
    // Simplified Chinese
    if (systemLang.startsWith('zh')) {
      return 'zh-CN'
    }
    if (systemLang.startsWith('ja')) {
      return 'ja'
    }
    if (systemLang.startsWith('es')) {
      return 'es'
    }
    if (systemLang.startsWith('de')) {
      return 'de'
    }
    return 'en'
  }
  return preference
}

export const i18n = createI18n({
  legacy: false, // Use Composition API
  locale: 'zh-CN', // Initial locale (will be updated by store)
  fallbackLocale: 'en',
  messages: {
    'zh-CN': zhCN,
    'zh-TW': zhTW,
    en: en,
    ja: ja,
    es: es,
    de: de,
  },
})

export function setI18nLocale(locale: string) {
  if (i18n.global.locale.value !== locale) {
    // @ts-expect-error: Allow dynamic locale assignment
    i18n.global.locale.value = locale
  }
}
