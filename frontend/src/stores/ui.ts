import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { resolveLocale, setI18nLocale } from '../i18n'
import { type SkinId, DEFAULT_SKIN_ID, normaliseSkinId } from '../utils/skinCatalog'

export type LocalePreference = 'auto' | 'zh-CN' | 'zh-TW' | 'en' | 'ja' | 'es' | 'de'
export type ThemeMode = 'system' | 'light' | 'dark'
export type { SkinId } from '../utils/skinCatalog'
export type Density = 'compact' | 'comfortable'
export type EffectsTier = 'reduced' | 'balanced' | 'full'
export type ActiveTab = 'downloads' | 'stopped' | 'settings'

export function levelToTier(level: number): EffectsTier {
  if (level <= 30) return 'reduced'
  if (level <= 70) return 'balanced'
  return 'full'
}

let systemThemeMedia: MediaQueryList | null = null
let detachSystemThemeListener: (() => void) | null = null

export const useUIStore = defineStore(
  'ui',
  () => {
    // State
    const activeTab = ref<ActiveTab>('downloads')
    const selectedDownloadGroupKey = ref<string | null>(null)
    const locale = ref<LocalePreference>('auto')
    const themeMode = ref<ThemeMode>('system')
    const skinId = ref<SkinId>(DEFAULT_SKIN_ID)
    const density = ref<Density>('comfortable')
    const effectsLevel = ref<number>(50)
    const pendingPasteUri = ref('')
    const pendingPasteUris = ref<string[]>([])

    // Actions
    function normalizeActiveTab(tab: unknown): ActiveTab {
      if (tab === 'stopped' || tab === 'settings') return tab
      return 'downloads'
    }

    function normalizeSelectedDownloadGroupKey() {
      const normalizedKey = selectedDownloadGroupKey.value?.trim() || ''
      selectedDownloadGroupKey.value = normalizedKey || null
    }

    function normalizeNavigationState() {
      const persistedTab = activeTab.value as string
      const normalizedTab = normalizeActiveTab(persistedTab)
      activeTab.value = normalizedTab
      normalizeSelectedDownloadGroupKey()

      if (persistedTab !== 'groups') return
      if (!selectedDownloadGroupKey.value) {
        selectedDownloadGroupKey.value = null
      }
    }

    function setActiveTab(tab: string) {
      activeTab.value = normalizeActiveTab(tab)
      selectedDownloadGroupKey.value = null
    }

    function openDownloadGroupDetail(groupKey: string) {
      const normalizedKey = groupKey.trim()
      if (!normalizedKey) return
      if (activeTab.value !== 'downloads' && activeTab.value !== 'stopped') {
        activeTab.value = 'downloads'
      }
      selectedDownloadGroupKey.value = normalizedKey
    }

    function closeDownloadGroupDetail() {
      selectedDownloadGroupKey.value = null
    }

    function clearDownloadGroupSelection() {
      selectedDownloadGroupKey.value = null
    }

    function setPendingPasteUri(uri: string) {
      pendingPasteUri.value = uri
    }

    function consumePendingPasteUri() {
      pendingPasteUri.value = ''
    }

    function setPendingPasteUris(uris: string[]) {
      pendingPasteUris.value = uris
    }

    function consumePendingPasteUris(): string[] {
      const uris = pendingPasteUris.value
      pendingPasteUris.value = []
      return uris
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
      skinId.value = normaliseSkinId(newSkin)
      applySkin()
    }

    function setDensity(newDensity: Density) {
      density.value = newDensity
      applyDensity()
    }

    const effectsTier = computed<EffectsTier>(() => levelToTier(effectsLevel.value))
    const effects = computed<EffectsTier>(() => effectsTier.value)

    function setEffectsLevel(level: number) {
      const clamped = Math.max(0, Math.min(100, Math.round(level)))
      effectsLevel.value = clamped
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
      const level = effectsLevel.value
      const tier = effectsTier.value
      root.setAttribute('data-effects', tier)
      root.style.setProperty('--ui-effects-level', String(level))
      root.style.setProperty('--glass-blur', `${Math.max(2, 24 - (level / 100) * 22)}px`)
      root.style.setProperty('--glass-opacity', String(0.08 + (level / 100) * 0.32))
    }

    /**
     * Initialize theme and skin on app startup
     * Should be called in App.vue onMounted
     */
    function normalizeEffectsLevel() {
      // Check for legacy persisted `effects: 'full'|'reduced'` and migrate
      try {
        const raw = localStorage.getItem('ui')
        if (raw) {
          const parsed = JSON.parse(raw) as Record<string, unknown>
          const old = parsed.effects
          if (typeof old === 'string') {
            effectsLevel.value = old === 'full' ? 100 : old === 'reduced' ? 0 : 50
            return
          }
        }
      } catch {
        // ignore parse errors
      }
      if (typeof effectsLevel.value !== 'number' || Number.isNaN(effectsLevel.value)) {
        effectsLevel.value = 50
      }
    }

    function initTheme() {
      normalizeNavigationState()
      // Defensive: normalise persisted skinId in case it was set to an unknown value
      skinId.value = normaliseSkinId(skinId.value)
      normalizeEffectsLevel()
      applyTheme()
      applySkin()
      applyDensity()
      applyEffects()
    }

    return {
      // State
      activeTab,
      selectedDownloadGroupKey,
      themeMode,
      skinId,
      density,
      effectsLevel,
      effectsTier,
      effects,
      pendingPasteUri,
      pendingPasteUris,
      // Actions
      setActiveTab,
      normalizeNavigationState,
      openDownloadGroupDetail,
      closeDownloadGroupDetail,
      clearDownloadGroupSelection,
      setPendingPasteUri,
      consumePendingPasteUri,
      setPendingPasteUris,
      consumePendingPasteUris,
      setTheme,
      setSkin,
      setDensity,
      setEffectsLevel,
      initTheme,
      // Locale
      locale,
      setLocale,
      initLocale,
    }
  },
  {
    persist: {
      pick: [
        'activeTab',
        'selectedDownloadGroupKey',
        'locale',
        'themeMode',
        'skinId',
        'density',
        'effectsLevel',
      ],
    },
  },
)
