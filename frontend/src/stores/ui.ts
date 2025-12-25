import { defineStore } from 'pinia'
import { ref } from 'vue'

export type ThemeMode = 'system' | 'light' | 'dark'
export type SkinId = 'obsidian' | 'ceramic'

export const useUIStore = defineStore(
  'ui',
  () => {
    // State
    const activeTab = ref('downloads')
    const themeMode = ref<ThemeMode>('system')
    const skinId = ref<SkinId>('obsidian')

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

    function applyTheme() {
      const root = document.documentElement
      if (themeMode.value === 'system') {
        root.removeAttribute('data-theme')
      } else {
        root.setAttribute('data-theme', themeMode.value)
      }
    }

    function applySkin() {
      const root = document.documentElement
      root.setAttribute('data-skin', skinId.value)
    }

    /**
     * Initialize theme and skin on app startup
     * Should be called in App.vue onMounted
     */
    function initTheme() {
      applyTheme()
      applySkin()
    }

    return {
      // State
      activeTab,
      themeMode,
      skinId,
      // Actions
      setActiveTab,
      setTheme,
      setSkin,
      initTheme,
    }
  },
  {
    persist: true,
  },
)
