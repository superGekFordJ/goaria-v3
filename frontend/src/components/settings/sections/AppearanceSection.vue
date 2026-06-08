<script setup lang="ts">
  import { Palette, Monitor, Sun, Moon, Languages, ChevronDown, Check } from 'lucide-vue-next'
  import { useI18n } from 'vue-i18n'
  import { ref, computed } from 'vue'
  import SectionCard from './SectionCard.vue'
  import { useUIStore, type ThemeMode, type LocalePreference } from '../../../stores/ui'
  import { skinCatalog, type SkinId } from '../../../utils/skinCatalog'

  const uiStore = useUIStore()
  const { t } = useI18n()

  const showLanguageDropdown = ref(false)

  const selectLocale = (locale: LocalePreference) => {
    uiStore.setLocale(locale)
    showLanguageDropdown.value = false
  }

  const resolvedTheme = computed(() => {
    if (uiStore.themeMode === 'light') return 'light'
    if (uiStore.themeMode === 'dark') return 'dark'
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
  })
</script>

<template>
  <SectionCard
    :title="t('appearance.title')"
    :description="t('appearance.description')"
    :icon="Palette"
    icon-class="bg-indigo-500/10 text-indigo-400"
  >
    <!-- Language Selector -->
    <div class="mb-6 relative">
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
          class="absolute z-50 top-full left-0 right-0 mt-2 p-1 rounded-xl bg-[var(--glass-bg)] border border-[var(--glass-border)] backdrop-blur-xl shadow-2xl origin-top"
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
      <button
        type="button"
        class="w-full flex items-center justify-between p-4 rounded-xl border bg-[var(--btn-glass-bg)] border-[var(--glass-border)] text-[var(--app-text)] hover:border-[var(--neon-primary)]/30 transition-all duration-200"
        @click="uiStore.setEffects(uiStore.effects === 'full' ? 'reduced' : 'full')"
      >
        <div class="text-left">
          <span class="text-sm font-medium block">
            {{ t('appearance.advancedMaterialsTitle') }}
          </span>
          <span class="text-[10px] text-[var(--app-text-subtle)] block mt-1">
            {{ t('appearance.advancedMaterialsDesc') }}
          </span>
        </div>
        <!-- Toggle Switch -->
        <div
          :class="[
            'w-12 h-7 rounded-full relative transition-all duration-300 cursor-pointer shrink-0 ml-4',
            uiStore.effects === 'full' ? 'bg-[var(--neon-primary)]' : 'bg-[var(--btn-glass-bg)]',
          ]"
        >
          <div
            :class="[
              'absolute top-1 w-5 h-5 rounded-full bg-[var(--card-bg)] shadow-lg ring-1 ring-[var(--glass-border)] transition-all duration-300',
              uiStore.effects === 'full' ? 'left-6' : 'left-1',
            ]"
          ></div>
        </div>
      </button>
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
