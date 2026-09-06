<script setup lang="ts">
  import {
    computed,
    nextTick,
    onBeforeUnmount,
    onDeactivated,
    ref,
    useId,
    watch,
    type Component,
  } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Loader2, CheckCircle, AlertCircle, ChevronDown, Compass } from '@lucide/vue'
  import LiquidGlassPanel from '../../common/LiquidGlassPanel.vue'

  export type SaveStatus = 'idle' | 'loading' | 'saving' | 'saved' | 'error'
  export interface SettingsNavigationSection {
    id: string
    labelKey: string
    icon: Component
  }

  const props = defineProps<{
    sections: SettingsNavigationSection[]
    activeSection: string
    floating: boolean
    status: SaveStatus
    errorKey: string
  }>()
  const emit = defineEmits<{ navigate: [id: string] }>()
  const { t } = useI18n()
  const navigationId = useId()
  const statusId = useId()
  const expanded = ref(false)
  const isTesting = typeof process !== 'undefined' && process.env?.NODE_ENV === 'test'
  const isCollapsing = ref(false)
  const isNavVisible = ref(false)
  let collapseTimeout: ReturnType<typeof setTimeout> | null = null
  let navHideTimeout: ReturnType<typeof setTimeout> | null = null

  const surfaceRef = ref<HTMLElement | null>(null)
  const triggerRef = ref<HTMLButtonElement | null>(null)
  const navigationRef = ref<HTMLElement | null>(null)
  const active = computed(
    () => props.sections.find(section => section.id === props.activeSection) ?? props.sections[0],
  )
  const sectionLabel = computed(() =>
    active.value ? t(active.value.labelKey) : t('settings.navigation.label'),
  )
  const statusText = computed(() => {
    if (props.status === 'error') return t(props.errorKey)
    if (props.status === 'loading') return t('settings.navigation.loading')
    if (props.status === 'idle') return t('settings.autoSave')
    return t(`settings.${props.status}`)
  })
  const triggerText = computed(() => {
    if (expanded.value) return t('settings.navigation.label')
    return props.status === 'idle' ? sectionLabel.value : statusText.value
  })
  const panelHeight = computed(
    () => 44 + Math.ceil(props.sections.length / 2) * 46 + (props.status === 'error' ? 64 : 0),
  )

  function clearCollapseTimers() {
    if (collapseTimeout) {
      clearTimeout(collapseTimeout)
      collapseTimeout = null
    }
    if (navHideTimeout) {
      clearTimeout(navHideTimeout)
      navHideTimeout = null
    }
  }

  function close(restoreFocus = false) {
    if (expanded.value) {
      isCollapsing.value = true
      clearCollapseTimers()
      if (isTesting) {
        isNavVisible.value = false
        isCollapsing.value = false
      } else {
        isNavVisible.value = true
        // 阶段一：60ms 磁贴极速微缩退场，清空内部舞台，杜绝边框收拢时的挤压感
        navHideTimeout = setTimeout(() => {
          isNavVisible.value = false
          navHideTimeout = null
        }, 60)
        // 阶段二：外壳以弹簧物理曲线平滑收缩吸入胶囊，340ms 完成生命周期
        collapseTimeout = setTimeout(() => {
          isCollapsing.value = false
          collapseTimeout = null
        }, 340)
      }
    } else {
      isNavVisible.value = false
    }
    expanded.value = false
    if (restoreFocus) triggerRef.value?.focus({ preventScroll: true })
  }

  function toggle() {
    if (expanded.value) {
      close()
    } else {
      expanded.value = true
      isNavVisible.value = true
      isCollapsing.value = false
      clearCollapseTimers()
    }
  }

  async function openFromKeyboard() {
    expanded.value = true
    isNavVisible.value = true
    isCollapsing.value = false
    clearCollapseTimers()
    await nextTick()
    if (!expanded.value) return
    const links = navigationRef.value?.querySelectorAll<HTMLAnchorElement>('[data-section-link]')
    const index = Math.max(
      0,
      props.sections.findIndex(section => section.id === props.activeSection),
    )
    links?.[index]?.focus({ preventScroll: true })
  }

  function navigate(id: string) {
    close(true)
    emit('navigate', id)
  }

  function handleNavigationKey(event: KeyboardEvent) {
    const links = Array.from(
      navigationRef.value?.querySelectorAll<HTMLAnchorElement>('[data-section-link]') ?? [],
    )
    const current = links.indexOf(document.activeElement as HTMLAnchorElement)
    if (current < 0) return
    const offsets: Record<string, number> = {
      ArrowRight: 1,
      ArrowLeft: -1,
      ArrowDown: 2,
      ArrowUp: -2,
    }
    let index: number
    if (event.key === 'Home') index = 0
    else if (event.key === 'End') index = links.length - 1
    else if (event.key in offsets) {
      index = Math.max(0, Math.min(links.length - 1, current + offsets[event.key]))
    } else return
    event.preventDefault()
    links[index]?.focus({ preventScroll: true })
  }

  function handleOutside(event: Event) {
    if (event.target instanceof Node && !surfaceRef.value?.contains(event.target)) close()
  }

  function removeListeners() {
    document.removeEventListener('pointerdown', handleOutside, true)
    document.removeEventListener('focusin', handleOutside)
  }

  watch(
    expanded,
    open => {
      removeListeners()
      if (open) {
        document.addEventListener('pointerdown', handleOutside, true)
        document.addEventListener('focusin', handleOutside)
      }
    },
    { flush: 'sync' },
  )
  onDeactivated(() => {
    clearCollapseTimers()
    close()
  })
  onBeforeUnmount(() => {
    clearCollapseTimers()
    removeListeners()
  })
</script>

<template>
  <div
    class="capsule-anchor"
    :data-docking="floating ? 'floating' : 'docked'"
    data-testid="floating-save-status"
  >
    <div ref="surfaceRef" class="capsule-motion" @keydown.esc.stop.prevent="close(true)">
      <LiquidGlassPanel
        radius="rounded-[inherit]"
        class="command-capsule"
        :class="{ 'is-open': expanded, 'is-collapsing': isCollapsing }"
        :style="{ '--capsule-height': `${panelHeight}px` }"
        :data-status="status"
      >
        <div class="capsule-content">
          <button
            ref="triggerRef"
            type="button"
            class="capsule-trigger"
            data-testid="settings-navigation-toggle"
            :aria-expanded="expanded"
            :aria-controls="navigationId"
            :aria-describedby="statusId"
            :aria-label="t('settings.navigation.toggle', { section: sectionLabel })"
            :title="`${sectionLabel} · ${statusText}`"
            @click="toggle"
            @keydown.down.prevent="openFromKeyboard"
          >
            <component
              :is="expanded ? Compass : (active?.icon ?? Compass)"
              :size="13"
              class="capsule-section-icon"
              aria-hidden="true"
            />
            <span class="capsule-label">{{ triggerText }}</span>
            <span class="capsule-status" :title="statusText" aria-hidden="true">
              <Loader2
                v-if="status === 'saving' || status === 'loading'"
                :size="12"
                class="animate-spin"
              />
              <CheckCircle v-else-if="status === 'saved'" :size="12" />
              <AlertCircle v-else-if="status === 'error'" :size="12" />
              <span v-else class="capsule-ready-dot"></span>
            </span>
            <ChevronDown
              :size="12"
              class="capsule-chevron"
              :class="{ 'is-open': expanded }"
              aria-hidden="true"
            />
          </button>
          <div
            v-show="expanded || isNavVisible"
            class="capsule-navigation-body"
            :class="{ 'is-leaving': isCollapsing }"
          >
            <nav
              :id="navigationId"
              ref="navigationRef"
              :aria-label="t('settings.navigation.label')"
              class="capsule-tiles"
              @keydown="handleNavigationKey"
            >
              <a
                v-for="section in sections"
                :key="section.id"
                :href="`#settings-section-${section.id}`"
                :data-section-link="section.id"
                :aria-current="section.id === activeSection ? 'location' : undefined"
                class="capsule-tile"
                @click.prevent="navigate(section.id)"
              >
                <span class="capsule-tile-icon">
                  <component :is="section.icon" :size="15" aria-hidden="true" />
                </span>
                <span>{{ t(section.labelKey) }}</span>
              </a>
            </nav>
            <p v-if="status === 'error'" class="capsule-error">{{ statusText }}</p>
          </div>
        </div>
      </LiquidGlassPanel>
    </div>
    <span :id="statusId" class="sr-only" role="status" aria-live="polite" aria-atomic="true">
      {{ statusText }}
    </span>
  </div>
</template>

<style scoped>
  .capsule-anchor {
    position: absolute;
    inset-inline: 16px;
    top: var(--capsule-top, 32px);
    z-index: 30;
    display: flex;
    justify-content: center;
    pointer-events: none;
  }

  .capsule-motion {
    max-width: 100%;
    pointer-events: auto;
  }

  .command-capsule {
    width: 164px;
    height: 34px;
    max-width: 100%;
    max-height: var(--capsule-space, calc(100vh - 96px));
    border-radius: 17px;
    overflow: hidden;
    box-shadow: 0 2px 8px color-mix(in srgb, var(--app-text) 6%, transparent);
    transition:
      width 0.18s cubic-bezier(0.4, 0, 1, 1),
      height 0.18s cubic-bezier(0.4, 0, 1, 1),
      border-radius 0.18s ease,
      box-shadow 0.3s ease;
  }

  .command-capsule.is-open {
    width: 360px;
    height: var(--capsule-height);
    border-radius: 24px;
    box-shadow:
      0 16px 36px -4px color-mix(in srgb, var(--app-text) 14%, transparent),
      0 6px 16px 0 color-mix(in srgb, var(--app-text) 6%, transparent),
      inset 0 1px 0 0 color-mix(in srgb, var(--app-text) 16%, transparent),
      inset 0 -1px 0 0 color-mix(in srgb, var(--neon-primary) 18%, transparent);
    transition:
      width 0.4s cubic-bezier(0.34, 1.56, 0.64, 1),
      height 0.4s cubic-bezier(0.34, 1.56, 0.64, 1),
      border-radius 0.4s ease,
      box-shadow 0.3s ease;
    animation: capsule-expand 0.4s cubic-bezier(0.34, 1.56, 0.64, 1);
  }

  .command-capsule.is-collapsing {
    transition:
      width 0.28s cubic-bezier(0.34, 1.35, 0.64, 1) 0.05s,
      height 0.28s cubic-bezier(0.34, 1.35, 0.64, 1) 0.05s,
      border-radius 0.28s ease 0.05s,
      box-shadow 0.28s ease;
    animation: capsule-collapse 0.34s cubic-bezier(0.34, 1.35, 0.64, 1);
  }

  .command-capsule :deep(.absolute) {
    transition: none;
  }

  [data-docking='floating'] .command-capsule {
    box-shadow: var(--glass-shadow);
  }

  .capsule-content {
    display: flex;
    flex-direction: column;
    width: 100%;
    min-width: 0;
    height: 100%;
    transition: background 0.3s ease;
  }

  /* 展开态：仅在内容层覆上自上而下的保真遮罩，上半部分实心保证文字绝对可读，底部羽化露出最底层真实的液态玻璃 */
  .command-capsule.is-open .capsule-content {
    background: linear-gradient(
      180deg,
      var(--card-bg) 0%,
      var(--card-bg) 62%,
      color-mix(in srgb, var(--card-bg) 75%, transparent) 78%,
      color-mix(in srgb, var(--card-bg) 18%, transparent) 90%,
      transparent 100%
    );
    border-radius: inherit;
  }

  .capsule-trigger {
    display: flex;
    align-items: center;
    gap: 7px;
    width: 100%;
    height: 34px;
    min-height: 34px;
    padding: 0 14px;
    border: none;
    outline: none;
    background: transparent;
    -webkit-appearance: none;
    appearance: none;
    border-radius: 17px;
    color: var(--app-text-muted);
    cursor: pointer;
    text-align: left;
    box-shadow: none;
  }

  .command-capsule.is-open .capsule-trigger {
    border-radius: 24px 24px 0 0;
    height: 34px;
    min-height: 34px;
  }

  .capsule-label {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
  }

  .capsule-section-icon,
  .capsule-chevron {
    flex-shrink: 0;
    color: var(--app-text-subtle);
  }

  .capsule-chevron {
    transition: transform 0.18s ease;
  }

  .capsule-chevron.is-open {
    transform: rotate(180deg);
  }

  .capsule-status {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 12px;
    flex-shrink: 0;
    color: var(--status-complete);
  }

  [data-status='saving'] .capsule-status,
  [data-status='loading'] .capsule-status {
    color: var(--neon-primary);
  }

  [data-status='error'] .capsule-status {
    color: var(--status-error);
  }

  .capsule-ready-dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: var(--status-complete);
  }

  [data-effects-glow='breathe'] .capsule-ready-dot {
    animation: capsule-breathe 3s ease-in-out infinite;
  }

  .capsule-navigation-body {
    min-height: 0;
    overflow-y: hidden;
    overscroll-behavior: contain;
    padding: 2px 10px 10px;
    scrollbar-width: none;
    -ms-overflow-style: none;
    transform-origin: top center;
    transition: transform 0.28s cubic-bezier(0.34, 1.3, 0.64, 1);
  }

  .capsule-navigation-body::-webkit-scrollbar {
    display: none;
    width: 0;
    height: 0;
  }

  .capsule-navigation-body.is-leaving {
    transform: scale(0.85) translateY(-4px);
    transition: transform 0.06s cubic-bezier(0.4, 0, 1, 1);
    pointer-events: none;
  }

  .capsule-tiles {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 6px;
  }

  .capsule-tile {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 40px;
    padding: 6px 10px;
    border-radius: var(--radius-squircle-sm);
    background: var(--btn-glass-bg);
    box-shadow: inset 0 1px 0 color-mix(in srgb, var(--app-text) 5%, transparent);
    color: var(--app-text-muted);
    font-size: 11px;
    font-weight: 500;
    line-height: 14px;
    overflow-wrap: anywhere;
    text-decoration: none;
    transition:
      background-color 0.16s ease,
      color 0.16s ease,
      transform 0.16s ease;
  }

  .capsule-tile-icon {
    flex-shrink: 0;
    color: var(--app-text-subtle);
  }

  .capsule-tile:hover,
  .capsule-tile[aria-current='location'] {
    background: color-mix(in srgb, var(--neon-primary) 12%, var(--btn-glass-bg));
    color: var(--neon-primary);
  }

  .capsule-tile[aria-current='location'] .capsule-tile-icon {
    color: inherit;
  }

  .capsule-tile:active {
    transform: scale(0.98);
  }

  .capsule-trigger:focus-visible,
  .capsule-tile:focus-visible {
    outline: 2px solid var(--neon-primary);
    outline-offset: -3px;
  }

  .capsule-error {
    margin: 10px 4px 0;
    font-size: 11px;
    line-height: 16px;
    color: var(--status-error);
    overflow-wrap: anywhere;
  }

  @keyframes capsule-expand {
    0% {
      transform: translateZ(0) scale(0.85);
    }
    50% {
      transform: translateZ(0) scale(1.03);
    }
    100% {
      transform: translateZ(0) scale(1);
    }
  }

  @keyframes capsule-collapse {
    0% {
      transform: translateZ(0) scale(1);
    }
    30% {
      transform: translateZ(0) scale(0.95);
    }
    70% {
      transform: translateZ(0) scale(1.025);
    }
    100% {
      transform: translateZ(0) scale(1);
    }
  }

  @keyframes capsule-breathe {
    50% {
      transform: scale(1.2);
    }
  }

  [data-effects='reduced'] .capsule-ready-dot {
    animation: none;
  }

  @media (prefers-reduced-motion: reduce) {
    .capsule-ready-dot {
      animation: none;
    }
  }
</style>
