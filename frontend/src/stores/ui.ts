import { defineStore } from 'pinia'
import { ref } from 'vue'
import { resolveLocale, setI18nLocale } from '../i18n'

export type LocalePreference = 'auto' | 'zh-CN' | 'zh-TW' | 'en' | 'ja' | 'es' | 'de'
export type ThemeMode = 'system' | 'light' | 'dark'
export type SkinId = 'obsidian' | 'ceramic'
export type Density = 'compact' | 'comfortable'
export type Effects = 'full' | 'reduced'

let systemThemeMedia: MediaQueryList | null = null
let detachSystemThemeListener: (() => void) | null = null

export const useUIStore = defineStore(
  'ui',
  () => {
    // State
    const activeTab = ref('downloads')
    const locale = ref<LocalePreference>('auto')
    const themeMode = ref<ThemeMode>('system')
    const skinId = ref<SkinId>('obsidian')
    const density = ref<Density>('comfortable')
    const effects = ref<Effects>('full')
    const pendingPasteUri = ref('')

    // Actions
    function setActiveTab(tab: string) {
      activeTab.value = tab
    }

    function setPendingPasteUri(uri: string) {
      pendingPasteUri.value = uri
    }

    function consumePendingPasteUri() {
      pendingPasteUri.value = ''
    }

    function setLocale(newLocale: LocalePreference) {
      locale.value = newLocale
      setI18nLocale(resolveLocale(newLocale))
    }

    function initLocale() {
      setI18nLocale(resolveLocale(locale.value))
    }

    function setTheme(newTheme: ThemeMode) {
      themeMode.value = newTheme
      applyTheme()
    }

    function setSkin(newSkin: SkinId) {
      skinId.value = newSkin
      applySkin()
    }

    function setDensity(newDensity: Density) {
      density.value = newDensity
      applyDensity()
    }

    function setEffects(newEffects: Effects) {
      effects.value = newEffects
      applyEffects()
    }

    function applyTheme() {
      const root = document.documentElement
      // Clear any previous system listeners
      if (detachSystemThemeListener) {
        detachSystemThemeListener()
        detachSystemThemeListener = null
      }

      if (themeMode.value === 'system') {
        // Resolve system theme and keep data-theme in sync for CSS selectors
        systemThemeMedia = window.matchMedia('(prefers-color-scheme: light)')
        const applySystemTheme = () => {
          const resolved = systemThemeMedia?.matches ? 'light' : 'dark'
          root.setAttribute('data-theme', resolved)
          root.setAttribute('data-theme-mode', 'system')
        }
        applySystemTheme()
        const listener = () => applySystemTheme()
        systemThemeMedia?.addEventListener('change', listener)
        detachSystemThemeListener = () => {
          systemThemeMedia?.removeEventListener('change', listener)
        }
      } else {
        root.setAttribute('data-theme', themeMode.value)
        root.setAttribute('data-theme-mode', 'explicit')
      }
    }

    function applySkin() {
      const root = document.documentElement
      root.setAttribute('data-skin', skinId.value)
    }

    function applyDensity() {
      const root = document.documentElement
      root.setAttribute('data-density', density.value)
    }

    function applyEffects() {
      const root = document.documentElement
      root.setAttribute('data-effects', effects.value)
    }

    /**
     * Initialize theme and skin on app startup
     * Should be called in App.vue onMounted
     */
    function initTheme() {
      applyTheme()
      applySkin()
      applyDensity()
      applyEffects()
    }

    return {
      // State
      activeTab,
      themeMode,
      skinId,
      density,
      effects,
      pendingPasteUri,
      // Actions
      setActiveTab,
      setPendingPasteUri,
      consumePendingPasteUri,
      setTheme,
      setSkin,
      setDensity,
      setEffects,
      initTheme,
      // Locale
      locale,
      setLocale,
      initLocale,
    }
  },
  {
    persist: {
      pick: ['activeTab', 'locale', 'themeMode', 'skinId', 'density', 'effects'],
    },
  },
)
