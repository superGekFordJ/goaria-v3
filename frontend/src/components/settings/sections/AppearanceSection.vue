<script setup lang="ts">
  import { Palette, Monitor, Sun, Moon, Languages, ChevronDown, Check } from '@lucide/vue'
  import { useI18n } from 'vue-i18n'
  import { ref, computed, onMounted, onUnmounted } from 'vue'
  import SectionCard from './SectionCard.vue'
  import LiquidGlassSlider from '../../common/LiquidGlassSlider.vue'
  import { useUIStore, type ThemeMode, type LocalePreference } from '../../../stores/ui'
  import { skinCatalog, type SkinId } from '../../../utils/skinCatalog'

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
</script>

<template>
  <SectionCard
    class="relative z-[70]"
    :title="t('appearance.title')"
    :description="t('appearance.description')"
    :icon="Palette"
    icon-class="bg-indigo-500/10 text-indigo-400"
  >
    <!-- Language Selector -->
    <div ref="languageDropdownRef" class="mb-6 relative z-50">
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
          class="absolute z-50 top-full left-0 right-0 mt-2 p-1 rounded-xl glass-panel-solid origin-top"
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

    <!-- Skin Selector -->
    <div class="mb-6">
      <label
        class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
      >
        {{ t('appearance.skinStyle') }}
      </label>
      <div class="grid grid-cols-2 gap-3">
        <button
          v-for="skin in skinCatalog"
          :key="skin.id"
          :class="[
            'flex items-center gap-3 p-4 rounded-xl border transition-all duration-200 text-left',
            uiStore.skinId === skin.id
              ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30'
              : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] hover:border-[var(--neon-primary)]/20',
          ]"
          @click="uiStore.setSkin(skin.id as SkinId)"
        >
          <div
            class="w-8 h-8 rounded-lg shrink-0"
            :style="{
              background: `linear-gradient(135deg, ${resolvedTheme === 'light' ? skin.preview.light.from : skin.preview.dark.from}, ${resolvedTheme === 'light' ? skin.preview.light.to : skin.preview.dark.to})`,
            }"
          ></div>
          <div class="min-w-0">
            <span
              :class="[
                'text-xs font-semibold block truncate',
                uiStore.skinId === skin.id
                  ? 'text-[var(--neon-primary)]'
                  : 'text-[var(--app-text)]/80',
              ]"
            >
              {{ t(skin.labelKey) }}
            </span>
            <span class="text-[9px] text-[var(--app-text-subtle)] block truncate">
              {{ t(skin.descriptionKey) }}
            </span>
            <span class="text-[8px] text-[var(--app-text-subtle)]/60 italic block truncate">
              {{ t(skin.conceptKey) }}
            </span>
          </div>
        </button>
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
          <span
            class="text-[10px] font-bold uppercase tracking-widest text-[var(--neon-primary)]"
          >
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
</style>
