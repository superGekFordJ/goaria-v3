import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { resolveLocale, setI18nLocale } from '../i18n'
import { type SkinId, DEFAULT_SKIN_ID, normaliseSkinId } from '../utils/skinCatalog'
import {
  prismButtonText,
  prismChroma,
  prismFillLightness,
  prismHslSaturation,
  normalisePrismTone,
  DEFAULT_PRISM_TONE,
  type PrismTone,
} from '../utils/prismSpectrum'

export type LocalePreference = 'auto' | 'zh-CN' | 'zh-TW' | 'en' | 'ja' | 'es' | 'de'
export type ThemeMode = 'system' | 'light' | 'dark'
export type { SkinId } from '../utils/skinCatalog'
export type { PrismTone } from '../utils/prismSpectrum'
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

let livePersistUnloadBound = false

function clampEffectsLevel(level: number): number {
  return Math.max(0, Math.min(100, Math.round(level)))
}

export const useUIStore = defineStore(
  'ui',
  () => {
    // State
    const activeTab = ref<ActiveTab>('downloads')
    const selectedDownloadGroupKey = ref<string | null>(null)
    const locale = ref<LocalePreference>('auto')
    const themeMode = ref<ThemeMode>('system')
    const skinId = ref<SkinId>(DEFAULT_SKIN_ID)
    // Live hue — drives --prism-hue every tick; not in persist.pick.
    const prismHue = ref<number>(280)
    // Committed mirror — pinia persist only watches this (slider commit / unload).
    const prismHuePersisted = ref<number>(280)
    // Curated chroma stop (晶艳/澄光/烟岚) — discrete control, persisted directly.
    const prismTone = ref<PrismTone>(DEFAULT_PRISM_TONE)
    const density = ref<Density>('comfortable')
    // Live visual level — drives CSS every tick; not in persist.pick.
    const effectsLevel = ref<number>(50)
    // Committed mirror — pinia persist only watches this (slider commit / unload).
    const effectsLevelPersisted = ref<number>(50)
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

    function clampPrismHue(hue: number): number {
      return Math.max(0, Math.min(360, Math.round(hue)))
    }

    function resolvedThemeNow(): 'light' | 'dark' {
      if (themeMode.value === 'system') {
        return typeof window !== 'undefined' &&
          window.matchMedia('(prefers-color-scheme: light)').matches
          ? 'light'
          : 'dark'
      }
      return themeMode.value
    }

    function applyPrismChrome() {
      if (typeof document === 'undefined') return
      const theme = resolvedThemeNow()
      const root = document.documentElement
      const { ink, fill } = prismChroma(theme, prismTone.value)
      root.style.setProperty('--prism-hue', String(prismHue.value))
      root.style.setProperty('--prism-fill-l', String(prismFillLightness(prismHue.value, theme)))
      root.style.setProperty('--prism-btn-text', prismButtonText(prismHue.value, theme, prismTone.value))
      root.style.setProperty('--prism-c-ink', ink.toFixed(4))
      root.style.setProperty('--prism-c-fill', fill.toFixed(4))
      root.style.setProperty('--prism-hsl-s', prismHslSaturation(prismTone.value))
    }

    function setPrismHue(hue: number) {
      prismHue.value = clampPrismHue(hue)
      applyPrismChrome()
    }

    function setPrismTone(tone: unknown) {
      prismTone.value = normalisePrismTone(tone)
      applyPrismChrome()
    }

    function flushPrismHuePersist() {
      const clamped = clampPrismHue(prismHue.value)
      if (prismHuePersisted.value !== clamped) {
        prismHuePersisted.value = clamped
      }
    }

    function commitPrismHue(hue?: number) {
      if (hue !== undefined) {
        setPrismHue(hue)
      }
      flushPrismHuePersist()
    }

    function setDensity(newDensity: Density) {
      density.value = newDensity
      applyDensity()
    }

    const effectsTier = computed<EffectsTier>(() => levelToTier(effectsLevel.value))

    function flushEffectsLevelPersist() {
      const clamped = clampEffectsLevel(effectsLevel.value)
      if (effectsLevelPersisted.value !== clamped) {
        effectsLevelPersisted.value = clamped
      }
    }

    function setEffectsLevel(level: number) {
      effectsLevel.value = clampEffectsLevel(level)
      applyEffects()
    }

    function commitEffectsLevel(level?: number) {
      if (level !== undefined) {
        effectsLevel.value = clampEffectsLevel(level)
        applyEffects()
      }
      flushEffectsLevelPersist()
    }

    function flushLivePersist() {
      flushEffectsLevelPersist()
      flushPrismHuePersist()
    }

    function bindLivePersistFlush() {
      if (livePersistUnloadBound || typeof window === 'undefined') return
      livePersistUnloadBound = true
      window.addEventListener('beforeunload', flushLivePersist)
      window.addEventListener('pagehide', flushLivePersist)
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
          applyPrismChrome()
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
        applyPrismChrome()
      }
    }

    function applySkin() {
      const root = document.documentElement
      root.setAttribute('data-skin', skinId.value)
      applyPrismChrome()
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
      // Breathing glow on task cards is gated behind a finer threshold than the
      // three-tier system: only level >= 95 enables the ::after opacity breathe.
      // 71–94 stays visually equivalent to balanced (static glow, no breathing),
      // avoiding per-active-card compositing layers for the majority of full-tier users.
      root.setAttribute('data-effects-glow', level >= 95 ? 'breathe' : 'static')
      root.style.setProperty('--ui-effects-level', String(level))

      let blur: number
      if (level <= 70) {
        blur = 8 + 40 * Math.pow((70 - level) / 70, 2)
      } else {
        blur = 8 - 6 * Math.pow((level - 70) / 30, 2)
      }
      root.style.setProperty('--glass-blur', `${blur.toFixed(2)}px`)

      let opacity: number
      if (level <= 70) {
        opacity = 0.3 + 0.7 * Math.pow((70 - level) / 70, 2)
      } else {
        opacity = 0.3 - 0.1 * Math.pow((level - 70) / 30, 2)
      }
      root.style.setProperty('--glass-opacity', opacity.toFixed(4))
    }

    /**
     * Initialize theme and skin on app startup
     * Should be called in App.vue onMounted
     */
    function normalizeEffectsLevel() {
      try {
        const raw = localStorage.getItem('ui')
        if (raw) {
          const parsed = JSON.parse(raw) as Record<string, unknown>
          const old = parsed.effects
          if (typeof old === 'string') {
            const migrated = old === 'full' ? 100 : old === 'reduced' ? 0 : 50
            effectsLevel.value = migrated
            effectsLevelPersisted.value = migrated
            return
          }
          const committed = parsed.effectsLevelPersisted
          if (typeof committed === 'number' && !Number.isNaN(committed)) {
            const level = clampEffectsLevel(committed)
            effectsLevel.value = level
            effectsLevelPersisted.value = level
            return
          }
          // Pre-split persist wrote live `effectsLevel` into the ui blob.
          const legacyLive = parsed.effectsLevel
          if (typeof legacyLive === 'number' && !Number.isNaN(legacyLive)) {
            const level = clampEffectsLevel(legacyLive)
            effectsLevel.value = level
            effectsLevelPersisted.value = level
            return
          }
        }
      } catch {
        // ignore parse errors
      }
      if (
        typeof effectsLevelPersisted.value === 'number' &&
        !Number.isNaN(effectsLevelPersisted.value)
      ) {
        effectsLevel.value = clampEffectsLevel(effectsLevelPersisted.value)
        return
      }
      effectsLevel.value = 50
      effectsLevelPersisted.value = 50
    }

    function normalizePrismHue() {
      let hue: number | undefined
      try {
        const raw = localStorage.getItem('ui')
        if (raw) {
          const parsed = JSON.parse(raw) as Record<string, unknown>
          const committed = parsed.prismHuePersisted
          const legacy = parsed.prismHue
          if (typeof committed === 'number' && !Number.isNaN(committed)) {
            hue = committed
          } else if (typeof legacy === 'number' && !Number.isNaN(legacy)) {
            // Pre-split blobs persisted the live `prismHue` key directly.
            hue = legacy
          }
        }
      } catch {
        // ignore parse errors
      }
      if (hue === undefined && !Number.isNaN(prismHuePersisted.value)) {
        hue = prismHuePersisted.value
      }
      const clamped = hue === undefined ? 280 : clampPrismHue(hue)
      prismHue.value = clamped
      prismHuePersisted.value = clamped
    }

    function initTheme() {
      normalizeNavigationState()
      // Defensive: normalise persisted skinId in case it was set to an unknown value
      skinId.value = normaliseSkinId(skinId.value)
      prismTone.value = normalisePrismTone(prismTone.value)
      normalizeEffectsLevel()
      normalizePrismHue()
      bindLivePersistFlush()
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
      prismHue,
      prismHuePersisted,
      prismTone,
      density,
      effectsLevel,
      effectsLevelPersisted,
      effectsTier,
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
      setPrismHue,
      commitPrismHue,
      setPrismTone,
      setDensity,
      setEffectsLevel,
      commitEffectsLevel,
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
        'prismHuePersisted',
        'prismTone',
        'density',
        'effectsLevelPersisted',
      ],
    },
  },
)
