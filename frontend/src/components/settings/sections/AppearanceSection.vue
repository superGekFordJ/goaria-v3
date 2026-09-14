<script setup lang="ts">
  import { Palette, Monitor, Sun, Moon, Languages, ChevronDown, Check } from '@lucide/vue'
  import { useI18n } from 'vue-i18n'
  import { ref, computed, onMounted, onUnmounted } from 'vue'
  import SectionCard from './SectionCard.vue'
  import LiquidGlassSlider from '../../common/LiquidGlassSlider.vue'
  import { useUIStore, type ThemeMode, type LocalePreference } from '../../../stores/ui'
  import { skinCatalog, getSkinMeta, type SkinId } from '../../../utils/skinCatalog'
  import { PRISM_TONES } from '../../../utils/prismSpectrum'

  const uiStore = useUIStore()
  const { t } = useI18n()

  const showLanguageDropdown = ref(false)
  const languageDropdownRef = ref<HTMLElement | null>(null)

  const handleClickOutsideLanguage = (event: MouseEvent) => {
    if (
      showLanguageDropdown.value &&
      languageDropdownRef.value &&
      !languageDropdownRef.value.contains(event.target as Node)
    ) {
      showLanguageDropdown.value = false
    }
  }

  onMounted(() => {
    document.addEventListener('click', handleClickOutsideLanguage)
  })

  onUnmounted(() => {
    document.removeEventListener('click', handleClickOutsideLanguage)
  })

  const selectLocale = (locale: LocalePreference) => {
    uiStore.setLocale(locale)
    showLanguageDropdown.value = false
  }

  const resolvedTheme = computed(() => {
    if (uiStore.themeMode === 'light') return 'light'
    if (uiStore.themeMode === 'dark') return 'dark'
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
  })

  const tierLabel = computed(() => {
    const tier = uiStore.effectsTier
    if (tier === 'reduced') return t('appearance.effectsLow')
    if (tier === 'balanced') return t('appearance.effectsBalanced')
    return t('appearance.effectsHigh')
  })

  function onSliderUpdate(v: number) {
    uiStore.setEffectsLevel(v)
  }

  function onSliderCommit(v: number) {
    uiStore.commitEffectsLevel(v)
  }

  const currentSkinMeta = computed(() => {
    return getSkinMeta(uiStore.skinId) ?? skinCatalog[0]
  })

  const currentConceptLine = computed(() => {
    return t(currentSkinMeta.value.conceptKey)
      .split(/[、・,，]/)
      .map(w => w.trim())
      .filter(Boolean)
      .join(' · ')
  })
</script>

<template>
  <SectionCard
    :class="{ 'relative z-20': showLanguageDropdown }"
    :title="t('appearance.title')"
    :description="t('appearance.description')"
    :icon="Palette"
    icon-class="bg-indigo-500/10 text-indigo-400"
  >
    <!-- Language Selector -->
    <div ref="languageDropdownRef" class="mb-6 relative z-10">
      <label
        class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
      >
        {{ t('appearance.language') }}
      </label>
      
      <!-- Trigger Button -->
      <button
        type="button"
        class="w-full flex items-center justify-between p-4 rounded-xl border bg-[var(--btn-glass-bg)] border-[var(--glass-border)] text-[var(--app-text)] hover:border-[var(--neon-primary)]/30 transition-all duration-200 group"
        @click="showLanguageDropdown = !showLanguageDropdown"
      >
        <div class="flex items-center gap-3">
          <div 
            class="flex items-center justify-center w-8 h-8 rounded-lg bg-[var(--app-text)]/5 text-[var(--app-text)] group-hover:text-[var(--neon-primary)] transition-colors duration-200"
          >
            <Monitor v-if="uiStore.locale === 'auto'" :size="18" />
            <Languages v-else :size="18" />
          </div>
          <div class="text-left">
            <span class="text-sm font-medium block">
              {{
                uiStore.locale === 'auto'
                  ? t('appearance.auto')
                  : uiStore.locale === 'zh-CN'
                    ? '中文 (简体)'
                    : uiStore.locale === 'zh-TW'
                      ? '中文 (繁體)'
                      : uiStore.locale === 'ja'
                        ? '日本語'
                        : uiStore.locale === 'es'
                          ? 'Español'
                          : uiStore.locale === 'de'
                            ? 'Deutsch'
                            : 'English'
              }}
            </span>
            <span class="text-[10px] text-[var(--app-text-subtle)] block">
              {{ 
                uiStore.locale === 'auto' 
                  ? t('appearance.language') 
                  : uiStore.locale === 'zh-CN' 
                    ? 'Chinese (Simplified)' 
                    : uiStore.locale === 'zh-TW'
                      ? 'Chinese (Traditional)'
                      : uiStore.locale === 'ja'
                        ? 'Japanese'
                        : uiStore.locale === 'es'
                          ? 'Spanish'
                          : uiStore.locale === 'de'
                            ? 'German'
                            : 'English' 
              }}
            </span>
          </div>
        </div>
        <ChevronDown 
          :size="16" 
          class="text-[var(--app-text-subtle)] transition-transform duration-200"
          :class="{ 'rotate-180': showLanguageDropdown }"
        />
      </button>

      <!-- Dropdown Menu -->
      <Transition name="slide-fade">
        <div
          v-if="showLanguageDropdown"
          class="absolute z-20 top-full left-0 right-0 mt-2 p-1 rounded-xl glass-panel-solid origin-top"
        >
          <button
            v-for="localeOption in [
              'auto',
              'zh-CN',
              'zh-TW',
              'en',
              'ja',
              'es',
              'de',
            ] as LocalePreference[]"
            :key="localeOption"
            type="button"
            class="w-full flex items-center justify-between p-3 rounded-lg transition-all duration-200 group"
            :class="[
              uiStore.locale === localeOption
                ? 'bg-[var(--neon-primary)]/10 text-[var(--neon-primary)]'
                : 'text-[var(--app-text)] hover:bg-[var(--app-text)]/5',
            ]"
            @click="selectLocale(localeOption)"
          >
            <div class="flex items-center gap-3">
              <Monitor
                v-if="localeOption === 'auto'"
                :size="16"
                :class="
                  uiStore.locale === localeOption
                    ? 'text-[var(--neon-primary)]'
                    : 'text-[var(--app-text-subtle)]'
                "
              />
              <Languages
                v-else
                :size="16"
                :class="
                  uiStore.locale === localeOption
                    ? 'text-[var(--neon-primary)]'
                    : 'text-[var(--app-text-subtle)]'
                "
              />
              <span class="text-xs font-medium">
                {{
                  localeOption === 'auto'
                    ? t('appearance.auto')
                    : localeOption === 'zh-CN'
                      ? '中文 (简体)'
                      : localeOption === 'zh-TW'
                        ? '中文 (繁體)'
                        : localeOption === 'ja'
                          ? '日本語'
                          : localeOption === 'es'
                            ? 'Español'
                            : localeOption === 'de'
                              ? 'Deutsch'
                              : 'English'
                }}
              </span>
            </div>
            <Check v-if="uiStore.locale === localeOption" :size="14" />
          </button>
        </div>
      </Transition>
    </div>

    <!-- Theme Mode Selector -->
    <div class="mb-6">
      <label
        class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
      >
        {{ t('appearance.themeMode') }}
      </label>
      <div class="grid grid-cols-3 gap-2">
        <button
          v-for="mode in ['system', 'light', 'dark'] as ThemeMode[]"
          :key="mode"
          :class="[
            'flex flex-col items-center gap-2 p-4 rounded-xl border transition-all duration-200',
            uiStore.themeMode === mode
              ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30 text-[var(--neon-primary)]'
              : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] text-[var(--app-text-muted)] hover:border-[var(--neon-primary)]/20',
          ]"
          @click="uiStore.setTheme(mode)"
        >
          <Monitor v-if="mode === 'system'" :size="20" />
          <Sun v-else-if="mode === 'light'" :size="20" />
          <Moon v-else :size="20" />
          <span class="text-[10px] font-semibold">
            {{
              mode === 'system'
                ? t('appearance.system')
                : mode === 'light'
                  ? t('appearance.light')
                  : t('appearance.dark')
            }}
          </span>
        </button>
      </div>
    </div>

    <!-- Skin Selector: Orbital Swatches Track + The Specimen Stage -->
    <div class="mb-6">
      <label
        class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
      >
        {{ t('appearance.skinStyle') }}
      </label>

      <!-- Upper: Orbital Swatches Track (Sleek Instrument Rail) -->
      <div
        class="p-1.5 rounded-2xl bg-black/[0.04] dark:bg-black/25 border border-black/5 dark:border-[var(--glass-border)] shadow-[inset_0_1px_2px_rgba(0,0,0,0.06)] dark:shadow-[inset_0_1px_3px_rgba(0,0,0,0.4)] mb-3"
      >
        <div class="grid grid-cols-7 gap-1">
          <button
            v-for="skin in skinCatalog"
            :key="skin.id"
            type="button"
            :class="[
              'group flex flex-col items-center gap-1.5 py-2 px-1 rounded-xl transition-all duration-200 cursor-pointer text-center relative',
              uiStore.skinId === skin.id
                ? 'swatch-active bg-white/60 dark:bg-white/[0.08] border border-black/[0.06] dark:border-white/12 shadow-[0_1px_3px_rgba(0,0,0,0.04),inset_0_1px_0_rgba(255,255,255,0.8)] backdrop-blur-xs'
                : 'border border-transparent hover:bg-black/[0.03] dark:hover:bg-white/[0.04] opacity-75 hover:opacity-100',
            ]"
            @click="uiStore.setSkin(skin.id as SkinId)"
          >
            <!-- Swatch squircle container: keeps stable centered position (no vertical lift) -->
            <div class="relative flex items-center justify-center shrink-0">
              <!-- Prism active only: continuous 2px chromatic rainbow ring -->
              <div
                v-if="skin.id === 'prism' && uiStore.skinId === 'prism'"
                class="absolute -inset-[2px] rounded-[14px] bg-[conic-gradient(from_180deg,#f43f5e,#fbbf24,#10b981,#06b6d4,#3b82f6,#8b5cf6,#f43f5e)] shadow-[0_0_12px_var(--neon-glow)] pointer-events-none"
              ></div>

              <!-- Swatch squircle: jewel appearance with specular rim (or rainbow-framed for Prism) -->
              <div
                :class="[
                  'w-8 h-8 rounded-xl shrink-0 transition-all duration-200 flex items-center justify-center relative shadow-sm overflow-hidden',
                  uiStore.skinId === skin.id
                    ? skin.id === 'prism'
                      ? 'shadow-[0_2px_8px_rgba(0,0,0,0.12)]'
                      : 'ring-2 ring-white dark:ring-white/90 shadow-[0_2px_8px_rgba(0,0,0,0.12)] dark:shadow-[0_0_12px_var(--neon-glow)]'
                    : 'ring-1 ring-black/10 dark:ring-white/15',
                ]"
                :style="{
                  background:
                    skin.id === 'prism'
                      ? 'linear-gradient(135deg, #f43f5e, #fbbf24, #10b981, #06b6d4, #8b5cf6)'
                      : `linear-gradient(135deg, ${resolvedTheme === 'light' ? skin.preview.light.from : skin.preview.dark.from}, ${resolvedTheme === 'light' ? skin.preview.light.to : skin.preview.dark.to})`,
                }"
              >
                <!-- Prism: Celestial star flare with subtle hover micro-interaction -->
                <span
                  v-if="skin.id === 'prism'"
                  class="text-[11px] text-white font-bold drop-shadow-[0_1px_2px_rgba(0,0,0,0.6)] select-none transition-transform duration-200 group-hover:scale-110 group-hover:rotate-12"
                >
                  ✦
                </span>
              </div>
            </div>

            <!-- Name -->
            <span
              :class="[
                'text-[10px] tracking-tight truncate w-full transition-colors duration-150',
                uiStore.skinId === skin.id
                  ? 'text-[var(--neon-primary)] font-bold'
                  : 'text-[var(--app-text-subtle)] group-hover:text-[var(--app-text)]',
              ]"
            >
              {{ t(skin.labelKey) }}
            </span>
          </button>
        </div>
      </div>

      <!-- Lower: The Specimen Stage (Unified 2-Column Architecture, Zero Layout Shift) -->
      <div
        class="relative p-4 rounded-xl border border-[var(--skin-surface-border)] bg-[var(--skin-surface-tint)] backdrop-blur-md transition-all duration-300 min-h-[122px] flex items-center justify-between gap-4 overflow-hidden shadow-[0_4px_20px_-4px_rgba(0,0,0,0.05),0_1px_3px_rgba(0,0,0,0.03)] dark:shadow-[0_10px_25px_-10px_var(--neon-glow)]"
      >
        <!-- Left: Literary Poetry & Controls -->
        <div class="min-w-0 flex-1 flex flex-col justify-center">
          <div class="flex items-center gap-2 mb-1">
            <span class="text-base font-bold text-[var(--app-text)] tracking-tight">
              {{ t(currentSkinMeta.labelKey) }}
            </span>
            <span
              class="text-[9px] font-medium tracking-wider px-2 py-0.5 rounded-full bg-[var(--neon-primary)]/15 text-[var(--neon-primary)] border border-[var(--neon-primary)]/30"
            >
              {{ t(currentSkinMeta.tagKey) }}
            </span>
            <!-- If Prism: Dynamic Hue Badge -->
            <span
              v-if="uiStore.skinId === 'prism'"
              class="text-[9px] font-mono font-bold px-2 py-0.5 rounded-full bg-[var(--neon-primary)]/20 text-[var(--neon-primary)] border border-[var(--neon-primary)]/30"
            >
              {{ uiStore.prismHue }}°
            </span>
          </div>

          <p class="text-xs text-[var(--app-text)]/90 mb-1 leading-relaxed truncate">
            {{ t(currentSkinMeta.descriptionKey) }}
          </p>

          <!-- Non-Prism: Poetic Concept Keywords -->
          <div
            v-if="uiStore.skinId !== 'prism'"
            class="text-[10px] text-[var(--neon-primary)]/80 font-medium tracking-wide flex items-center gap-1.5"
          >
            <span
              class="w-1.5 h-1.5 rounded-full bg-[var(--neon-primary)]/70 shadow-[0_0_6px_var(--neon-primary)]"
            ></span>
            <span class="truncate">{{ currentConceptLine }}</span>
          </div>

          <!-- Prism: Continuous Optical Spectrum Slider -->
          <div v-else class="space-y-1 pt-0.5">
            <div
              class="flex items-center justify-between gap-3 text-[10px] text-[var(--neon-primary)]/80 font-medium"
            >
              <div class="flex items-center gap-1.5 min-w-0">
                <span
                  class="w-1.5 h-1.5 rounded-full bg-[var(--neon-primary)]/70 shadow-[0_0_6px_var(--neon-primary)]"
                ></span>
                <span class="truncate">{{ currentConceptLine }}</span>
              </div>
              <!-- Spectrum tone: three curated chroma stops (晶艳 · 澄光 · 烟岚) -->
              <div
                role="group"
                :aria-label="t('appearance.prismTone.label')"
                class="flex items-center gap-0.5 p-0.5 rounded-full shrink-0 bg-black/[0.04] dark:bg-black/25 border border-black/5 dark:border-[var(--glass-border)]"
              >
                <button
                  v-for="tone in PRISM_TONES"
                  :key="tone"
                  type="button"
                  :class="[
                    'px-2 py-0.5 rounded-full text-[9px] font-semibold tracking-wide transition-all duration-200 cursor-pointer',
                    uiStore.prismTone === tone
                      ? 'bg-[var(--neon-primary)]/15 text-[var(--neon-primary)] shadow-[0_0_8px_var(--neon-glow)]'
                      : 'text-[var(--app-text-subtle)] hover:text-[var(--app-text)]',
                  ]"
                  @click="uiStore.setPrismTone(tone)"
                >
                  {{ t(`appearance.prismTone.${tone}`) }}
                </button>
              </div>
            </div>
            <div class="flex items-center pr-3 pt-0.5">
              <input
                type="range"
                min="0"
                max="360"
                :value="uiStore.prismHue"
                class="w-full spectrum-slider cursor-pointer"
                @input="
                  (e: Event) => uiStore.setPrismHue(Number((e.target as HTMLInputElement).value))
                "
                @change="
                  (e: Event) => uiStore.commitPrismHue(Number((e.target as HTMLInputElement).value))
                "
              />
            </div>
          </div>
        </div>

        <!-- Right: Living Specimen Instrument Card (Shared by All 7 Skins, Reacts LIVE in Prism) -->
        <div
          class="specimen-card shrink-0 w-48 sm:w-52 p-3 rounded-xl bg-white/75 dark:bg-black/30 border border-black/5 dark:border-[var(--glass-border)] shadow-xs backdrop-blur-sm flex flex-col justify-between"
        >
          <div
            class="flex items-center justify-between text-[9px] font-mono text-[var(--app-text-subtle)] font-medium"
          >
            <span class="tracking-wider uppercase">LIVE SPECIMEN</span>
            <span class="font-mono font-bold text-[var(--neon-primary)]">28.4 MB/s</span>
          </div>
          <div class="my-1.5">
            <div
              class="h-1.5 w-full rounded-full bg-black/5 dark:bg-[var(--glass-border)] overflow-hidden"
            >
              <div
                class="h-full rounded-full shadow-[0_0_10px_var(--skin-ambient-glow)] transition-all duration-200"
                :style="{
                  width: '76%',
                  background:
                    'linear-gradient(90deg, var(--skin-accent-from), var(--skin-accent-to))',
                }"
              ></div>
            </div>
          </div>
          <div class="flex items-center justify-end">
            <div
              class="text-[10px] font-semibold px-3 py-1 rounded-md transition-all duration-200 select-none text-center truncate"
              :style="{
                background:
                  'linear-gradient(135deg, var(--skin-accent-from), var(--skin-accent-to))',
                color: 'var(--neon-btn-text)',
                boxShadow:
                  resolvedTheme === 'light'
                    ? '0 1px 4px rgba(0,0,0,0.15)'
                    : '0 2px 8px var(--neon-glow)',
              }"
            >
              + {{ t('taskHeader.startDownload') }}
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Advanced Materials (Effects) -->
    <div>
      <label
        class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
      >
        {{ t('appearance.advancedMaterials') }}
      </label>
      <div
        class="w-full p-4 rounded-xl border bg-[var(--btn-glass-bg)] border-[var(--glass-border)]"
      >
        <div class="flex items-center justify-between mb-3">
          <div class="text-left">
            <span class="text-[10px] text-[var(--app-text-subtle)] block">
              {{ t('appearance.advancedMaterialsDesc') }}
            </span>
          </div>
          <span class="text-[10px] font-bold uppercase tracking-widest text-[var(--neon-primary)]">
            {{ tierLabel }}
          </span>
        </div>
        <LiquidGlassSlider
          :model-value="uiStore.effectsLevel"
          :min="0"
          :max="100"
          :step="1"
          :aria-label="t('appearance.advancedMaterials')"
          :aria-valuetext="tierLabel"
          @update:model-value="onSliderUpdate"
          @change="onSliderCommit"
        />
      </div>
    </div>
  </SectionCard>
</template>

<style scoped>
  .slide-fade-enter-active,
  .slide-fade-leave-active {
    transition: all 0.2s ease;
  }

  .slide-fade-enter-from,
  .slide-fade-leave-to {
    opacity: 0;
    transform: translateY(-8px) scale(0.98);
  }

  .spectrum-slider {
    -webkit-appearance: none;
    appearance: none;
    height: 7px;
    border-radius: 9999px;
    background: linear-gradient(
      to right,
      #f43f5e 0%,
      #fbbf24 18%,
      #10b981 35%,
      #06b6d4 52%,
      #3b82f6 70%,
      #8b5cf6 85%,
      #f43f5e 100%
    );
    background: linear-gradient(
      to right in oklch longer hue,
      oklch(0.7 var(--prism-c-fill, 0.19) 0),
      oklch(0.7 var(--prism-c-fill, 0.19) 0)
    );
    /* WYSIWYG: track chroma follows the active prism tone; the registered
       <number> var eases the rainbow between stops (晶艳 · 澄光 · 烟岚). */
    transition: --prism-c-fill 0.3s ease;
    outline: none;
    box-shadow: inset 0 1px 3px rgba(0, 0, 0, 0.5), 0 0 0 1px rgba(255, 255, 255, 0.1);
  }

  .spectrum-slider::-webkit-slider-thumb {
    -webkit-appearance: none;
    appearance: none;
    width: 17px;
    height: 17px;
    border-radius: 50%;
    background: #ffffff;
    border: 2px solid rgba(255, 255, 255, 0.95);
    box-shadow: 0 1px 3px rgba(0, 0, 0, 0.25), 0 0 8px var(--neon-glow);
    cursor: pointer;
    transition: transform 0.15s ease;
  }

  .spectrum-slider::-webkit-slider-thumb:hover {
    transform: scale(1.2);
  }

  /* Light window-transparency anti-fog: the selected swatch and specimen card
     must stay near the OS material like the sidebar liquid glass
     (visual_system §5.1a) instead of stacking milky white. */
  :root[data-window-transparency='acrylic'][data-theme='light'] .swatch-active,
  :root[data-window-transparency='mica'][data-theme='light'] .swatch-active,
  :root[data-window-transparency='tabbed'][data-theme='light'] .swatch-active {
    background: rgba(255, 255, 255, 0.22);
    box-shadow:
      0 1px 3px rgba(0, 0, 0, 0.04),
      inset 0 1px 0 rgba(255, 255, 255, 0.35);
  }

  :root[data-window-transparency='acrylic'][data-theme='light'] .specimen-card,
  :root[data-window-transparency='mica'][data-theme='light'] .specimen-card,
  :root[data-window-transparency='tabbed'][data-theme='light'] .specimen-card {
    background: rgba(255, 255, 255, 0.38);
  }
</style>
