<script lang="ts">
  import { onMount, tick } from 'svelte'
  import LiquidGlassPanel from '../lib/glass/LiquidGlassPanel.svelte'
  import { t } from '../lib/i18n'
  import { sanitizeDisplayFilename } from '../background/extractorKeys'
  import {
    EXTRACTOR_FOLDER_MAX_RUNES,
    EXTRACTOR_MAX_SESSION_ITEMS,
    EXTRACTOR_PICKER_WINDOW,
  } from '../background/extractorKeys'
  import {
    invert,
    selectAll,
    selectedBytes,
    selectableCount,
    toggleIndex,
    visibleWindow,
  } from '../background/pickerSelection'
  import { folderFieldForSubmit } from '../background/pickerFolder'
  import { activeFromRoot, isTrapFocusable, wrapTabIndex } from './pickerFocus'
  import { domPickerView } from './domPickerView.svelte'
  import type { DomLinkKind } from '../utils/messaging'

  let { effects = 'full' }: { effects?: 'full' | 'reduced' } = $props()

  let overlayEl = $state<HTMLElement | null>(null)
  let isSystemDark = $state(true)
  let selected = $state(new Set<number>())
  let activeIndex = $state(0)
  let createGroup = $state(false)
  let folderRaw = $state('')
  let lastCatalogKey = ''

  let snapshot = $derived(domPickerView.state)
  let items = $derived(snapshot.items)
  let selectable = $derived(selectableCount(items.length, EXTRACTOR_MAX_SESSION_ITEMS))
  let win = $derived(visibleWindow(activeIndex, items.length, EXTRACTOR_PICKER_WINDOW))
  let visibleItems = $derived(items.slice(win.start, win.end))
  let selectedCount = $derived(selected.size)
  let knownBytes = $derived(selectedBytes(items, selected))
  let hasKnownBytes = $derived(
    [...selected].some(index => typeof items[index]?.size_bytes === 'number'),
  )
  let groupEnabled = $derived(selectedCount >= 2)
  let submitting = $derived(snapshot.phase === 'submitting')
  let titleId = 'dom-picker-title'

  $effect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    isSystemDark = mq.matches
    const listener = (e: MediaQueryListEvent) => {
      isSystemDark = e.matches
    }
    mq.addEventListener('change', listener)
    return () => mq.removeEventListener('change', listener)
  })

  $effect(() => {
    const next = domPickerView.state.items
    const key = `${domPickerView.state.catalogId}:${next.map(row => row.index).join(',')}`
    if (key === lastCatalogKey) return
    lastCatalogKey = key
    selected = new Set<number>()
    activeIndex = 0
    createGroup = false
    folderRaw = domPickerView.state.folderPrefill
  })

  $effect(() => {
    if (!groupEnabled) createGroup = false
  })

  function formatBytes(n: number): string {
    if (n < 1024) return `${n} B`
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
    if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`
    return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`
  }

  function kindLabel(kind: DomLinkKind | undefined): string {
    if (kind === 'image') return t('dom_kind_image')
    if (kind === 'video') return t('dom_kind_video')
    if (kind === 'audio') return t('dom_kind_audio')
    if (kind === 'source') return t('dom_kind_source')
    return t('dom_kind_link')
  }

  function focusables(): HTMLElement[] {
    if (!overlayEl) return []
    return [...overlayEl.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled])')].filter(
      isTrapFocusable,
    )
  }

  function overlayActiveElement(): Element | null {
    const root = overlayEl?.getRootNode()
    return activeFromRoot(root instanceof ShadowRoot ? root : null, document.activeElement)
  }

  function focusActiveRow(): void {
    overlayEl?.querySelector<HTMLElement>(`[data-picker-index="${activeIndex}"]`)?.focus()
  }

  function focusFirstRow(): void {
    const row = overlayEl?.querySelector<HTMLElement>('[data-picker-row] input')
    if (row) {
      row.focus()
      return
    }
    focusables()[0]?.focus()
  }

  onMount(() => {
    focusFirstRow()
  })

  function onScrim(event: MouseEvent): void {
    if (!event.isTrusted) return
    domPickerView.onCancel()
  }

  function onCancel(event: MouseEvent): void {
    event.preventDefault()
    if (!event.isTrusted) return
    domPickerView.onCancel()
  }

  function onSubmit(event: MouseEvent): void {
    event.preventDefault()
    if (!event.isTrusted) return
    sendSubmit()
  }

  function sendSubmit(): void {
    if (submitting || selectedCount < 1) return
    const indices = [...selected].sort((a, b) => a - b)
    const fields = folderFieldForSubmit({
      createGroup,
      selectedCount,
      raw: folderRaw,
    })
    domPickerView.onSubmit({
      indices,
      create_group: fields.create_group,
      folder_name: fields.folder_name,
    })
  }

  function onToggle(index: number, event?: Event): void {
    if (event && !event.isTrusted) return
    selected = toggleIndex(selected, index, selectable)
    activeIndex = index
  }

  function onSelectAll(event: MouseEvent): void {
    if (!event.isTrusted) return
    selected = selectAll(selectable)
  }

  function onInvert(event: MouseEvent): void {
    if (!event.isTrusted) return
    selected = invert(selected, selectable)
  }

  function capFolderRunes(raw: string): string {
    const runes = [...raw]
    return runes.length > EXTRACTOR_FOLDER_MAX_RUNES
      ? runes.slice(0, EXTRACTOR_FOLDER_MAX_RUNES).join('')
      : raw
  }

  function onFolderInput(event: Event): void {
    const el = event.currentTarget as HTMLInputElement
    if ('isComposing' in event && event.isComposing) {
      folderRaw = el.value
      return
    }
    folderRaw = capFolderRunes(el.value)
    if (el.value !== folderRaw) el.value = folderRaw
  }

  async function moveActive(next: number): Promise<void> {
    if (items.length === 0) return
    activeIndex = Math.min(Math.max(0, next), items.length - 1)
    await tick()
    focusActiveRow()
  }

  function inPickerList(target: EventTarget | null): boolean {
    return target instanceof Element && Boolean(target.closest('[data-picker-row], .extractor-picker-list'))
  }

  function onKeydown(event: KeyboardEvent): void {
    if (!event.isTrusted) return
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      domPickerView.onCancel()
      return
    }
    if (event.key === 'Tab' && overlayEl) {
      const nodes = focusables()
      if (nodes.length === 0) return
      const current = nodes.indexOf(overlayActiveElement() as HTMLElement)
      const next = wrapTabIndex(nodes.length, current < 0 ? 0 : current, event.shiftKey)
      event.preventDefault()
      nodes[next]?.focus()
      return
    }
    const target = event.target as HTMLElement | null
    if (target?.tagName === 'BUTTON') return
    const inField = target?.tagName === 'INPUT' && (target as HTMLInputElement).type === 'text'
    if (inField) return
    if (!inPickerList(event.target)) return
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      void moveActive(activeIndex + 1)
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      void moveActive(activeIndex - 1)
      return
    }
    if (event.key === 'Home') {
      event.preventDefault()
      void moveActive(0)
      return
    }
    if (event.key === 'End') {
      event.preventDefault()
      void moveActive(items.length - 1)
      return
    }
    if (event.key === 'PageDown') {
      event.preventDefault()
      void moveActive(activeIndex + EXTRACTOR_PICKER_WINDOW)
      return
    }
    if (event.key === 'PageUp') {
      event.preventDefault()
      void moveActive(activeIndex - EXTRACTOR_PICKER_WINDOW)
      return
    }
    if (event.key === ' ' || event.key === 'Spacebar') {
      if (!inPickerList(event.target)) return
      event.preventDefault()
      onToggle(activeIndex)
      return
    }
    if (event.key === 'Enter') {
      if (!inPickerList(event.target)) return
      event.preventDefault()
      sendSubmit()
    }
  }
</script>

<div
  bind:this={overlayEl}
  class="extractor-picker-overlay"
  data-dom-picker="1"
  data-theme={isSystemDark ? 'dark' : 'light'}
  role="dialog"
  aria-modal="true"
  aria-labelledby={titleId}
  tabindex="-1"
  onkeydown={onKeydown}
>
  <button type="button" class="extractor-picker-scrim" tabindex="-1" aria-hidden="true" onclick={onScrim}></button>
  <div class="extractor-picker-panel">
    <LiquidGlassPanel radius="var(--radius-squircle-lg, 2rem)" {effects} class="extractor-picker-shell">
      <div class="extractor-picker-inner">
        <div class="extractor-picker-header">
          <h2 id={titleId} class="extractor-picker-title">{t('dom_picker_title')}</h2>
          <button type="button" class="extractor-picker-close" aria-label={t('picker_close_aria')} onclick={onCancel}>
            ✕
          </button>
        </div>

        {#if snapshot.truncated}
          <div class="extractor-picker-hint">{t('dom_truncated')}</div>
        {/if}
        {#if snapshot.storeUnproven}
          <div class="extractor-picker-hint">{t('dom_store_unproven')}</div>
        {/if}
        {#if snapshot.banner === 'busy'}
          <div class="extractor-picker-hint">{t('dom_busy')}</div>
        {/if}
        {#if snapshot.banner === 'pending'}
          <div class="extractor-picker-hint">{t('dom_ack_pending')}</div>
        {/if}

        <div class="extractor-picker-toolbar">
          <button type="button" class="extractor-picker-textbtn" disabled={submitting} onclick={onSelectAll}>
            {t('picker_select_all')}
          </button>
          <button type="button" class="extractor-picker-textbtn" disabled={submitting} onclick={onInvert}>
            {t('picker_invert')}
          </button>
          <span class="extractor-picker-hint">
            {t('picker_window_hint', [
              String(items.length === 0 ? 0 : win.start + 1),
              String(win.end),
              String(items.length),
            ])}
          </span>
        </div>

        <div class="extractor-picker-list">
          {#each visibleItems as item (item.index)}
            <label
              class="etched-panel extractor-picker-row"
              class:is-active={item.index === activeIndex}
              data-picker-row="1"
            >
              <input
                type="checkbox"
                data-picker-index={item.index}
                checked={selected.has(item.index)}
                disabled={submitting}
                onchange={event => onToggle(item.index, event)}
                onfocus={() => {
                  activeIndex = item.index
                }}
              />
              <span class="extractor-picker-name">
                {sanitizeDisplayFilename(item.filename) || t('capsule_item_generic')}
              </span>
              <span class="extractor-picker-size">
                {sanitizeDisplayFilename(item.origin) || ''}
                {item.origin ? ' · ' : ''}
                {sanitizeDisplayFilename(item.path) || ''}
                {item.path ? ' · ' : ''}
                {kindLabel(item.kind)}
                {typeof item.size_bytes === 'number' ? ` · ${formatBytes(item.size_bytes)}` : ''}
              </span>
            </label>
          {/each}
        </div>

        <div class="extractor-picker-summary">
          {t('picker_selected_count', [String(selectedCount)])}
          {#if hasKnownBytes}
            · {t('picker_selected_bytes', [formatBytes(knownBytes)])}
          {/if}
        </div>

        <label class="extractor-picker-group">
          <input type="checkbox" bind:checked={createGroup} disabled={!groupEnabled || submitting} />
          {t('picker_create_group')}
        </label>
        <input
          class="extractor-picker-folder"
          type="text"
          value={folderRaw}
          placeholder={t('picker_folder_placeholder')}
          disabled={!createGroup || submitting}
          oninput={onFolderInput}
          oncompositionend={onFolderInput}
        />

        <div class="extractor-picker-actions">
          <button
            type="button"
            class="extractor-picker-btn"
            disabled={submitting && snapshot.banner !== 'pending'}
            onclick={onCancel}
          >
            {t('picker_cancel')}
          </button>
          <button
            type="button"
            class="extractor-picker-btn is-primary"
            disabled={submitting || selectedCount < 1}
            aria-busy={submitting}
            onclick={onSubmit}
          >
            {t('picker_submit')}
          </button>
        </div>
      </div>
    </LiquidGlassPanel>
  </div>
</div>
