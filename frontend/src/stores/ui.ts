import { defineStore } from 'pinia'
import { ref } from 'vue'

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
    const themeMode = ref<ThemeMode>('system')
    const skinId = ref<SkinId>('obsidian')
    const density = ref<Density>('comfortable')
    const effects = ref<Effects>('full')

    // Actions
    function setActiveTab(tab: string) {
      activeTab.value = tab
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
      // Actions
      setActiveTab,
      setTheme,
      setSkin,
      setDensity,
      setEffects,
      initTheme,
    }
  },
  {
    persist: true,
  },
)
