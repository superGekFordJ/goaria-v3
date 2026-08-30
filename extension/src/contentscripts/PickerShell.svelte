<script
  lang="ts"
  generics="T extends { index: number; filename?: string; origin?: string; path?: string; kind?: string; size_bytes?: number; mime_type?: string }"
>
  import { onMount, tick, type Snippet } from 'svelte'
  import LiquidGlassPanel from '../lib/glass/LiquidGlassPanel.svelte'
  import { t } from '../lib/i18n'
  import {
    EXTRACTOR_FOLDER_MAX_RUNES,
    EXTRACTOR_MAX_SESSION_ITEMS,
  } from '../background/extractorKeys'
  import {
    initialPickerSelection,
    invert,
    selectAll,
    selectedBytes,
    selectableCount,
    toggleIndex,
    type PickerSelectPolicy,
  } from '../background/pickerSelection'
  import { folderFieldForSubmit } from '../background/pickerFolder'
  import { activeFromRoot, isTrapFocusable, restoreSelector, wrapTabIndex } from './pickerFocus'
  import { formatPickerBytes, pickerItemIdentity } from './pickerChrome'
  import {
    categorizePickerItem,
    filterPickerItems,
    formatDisplaySecondary,
    getAvailableCategories,
    getCategoryCounts,
    getDisplayFilename,
    isValidKnownSize,
    type PickerCategory,
  } from './pickerPresentation'
  import PickerFileIcon from './PickerFileIcon.svelte'

  let {
    effects = 'full',
    source,
    title,
    titleId: titleIdProp,
    items,
    catalogKey,
    selectPolicy,
    submitting,
    cancelDisabled,
    folderPrefill = '',
    restoreFocus = false,
    onCancel,
    onSubmit,
    banner,
  }: {
    effects?: 'full' | 'reduced'
    source: 'extractor' | 'dom' | 'burst'
    title: string
    titleId?: string
    items: ReadonlyArray<T>
    catalogKey: string
    selectPolicy: PickerSelectPolicy
    submitting: boolean
    cancelDisabled: boolean
    folderPrefill?: string
    restoreFocus: boolean
    onCancel: () => void
    onSubmit: (payload: { indices: number[]; create_group?: boolean; folder_name?: string }) => void
    banner?: Snippet
  } = $props()

  let overlayEl = $state<HTMLElement | null>(null)
  let isSystemDark = $state(true)
  let selected = $state(new Set<number>())
  let activeIndex = $state(0)
  let activeCategory = $state<PickerCategory>('all')
  let createGroup = $state(false)
  let folderRaw = $state('')
  let lastCatalogKey = ''
  let lastItemIdentity = ''

  let selectable = $derived(selectableCount(items.length, EXTRACTOR_MAX_SESSION_ITEMS))
  let categoryCounts = $derived(getCategoryCounts(items))
  let availableCategories = $derived(getAvailableCategories(categoryCounts))
  let filteredItems = $derived(filterPickerItems(items, activeCategory))
  let filteredIndices = $derived(filteredItems.map(it => it.index))

  let selectedCount = $derived(selected.size)
  let knownBytes = $derived(selectedBytes(items, selected))
  let hasKnownBytes = $derived(
    items.some(item => selected.has(item.index) && isValidKnownSize(item.size_bytes)),
  )
  let groupEnabled = $derived(selectedCount >= 2)
  let titleId = $derived(
    titleIdProp ??
      (source === 'extractor'
        ? 'extractor-picker-title'
        : source === 'burst'
          ? 'burst-picker-title'
          : 'dom-picker-title'),
  )

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
    const key = catalogKey
    const identity = pickerItemIdentity(items)
    if (key === lastCatalogKey && identity === lastItemIdentity) return
    lastCatalogKey = key
    lastItemIdentity = identity
    selected = new Set(initialPickerSelection(selectPolicy, items))
    activeIndex = items[0]?.index ?? 0
    activeCategory = 'all'
    createGroup = false
    folderRaw = folderPrefill ?? ''
  })

  $effect(() => {
    if (!groupEnabled) createGroup = false
  })

  function focusables(): HTMLElement[] {
    if (!overlayEl) return []
    return [
      ...overlayEl.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), [tabindex="0"]',
      ),
    ].filter(isTrapFocusable)
  }

  function overlayActiveElement(): Element | null {
    const root = overlayEl?.getRootNode()
    return activeFromRoot(root instanceof ShadowRoot ? root : null, document.activeElement)
  }

  function focusActiveRow(): void {
    overlayEl?.querySelector<HTMLElement>(`input[data-picker-index="${activeIndex}"]`)?.focus()
  }

  function focusFirstRow(): void {
    const row = overlayEl?.querySelector<HTMLElement>('[data-picker-row] input')
    if (row) {
      row.focus()
      return
    }
    focusables()[0]?.focus()
  }

  function restoreCapsuleFocus(): void {
    const host = document.getElementById('goaria-shadow-host')
    const action = host?.shadowRoot?.querySelector<HTMLElement>(restoreSelector)
    action?.focus()
  }

  onMount(() => {
    focusFirstRow()
    return () => {
      if (!restoreFocus) return
      void tick().then(() => restoreCapsuleFocus())
    }
  })

  function handleScrim(event: MouseEvent): void {
    if (!event.isTrusted || cancelDisabled) return
    onCancel()
  }

  function handleCancel(event?: Event): void {
    if (event) {
      event.preventDefault()
      if (!event.isTrusted) return
    }
    if (cancelDisabled) return
    onCancel()
  }

  function handleSubmitClick(event: MouseEvent): void {
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
    onSubmit({
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
    selected = selectAll(filteredIndices, selected)
  }

  function onInvert(event: MouseEvent): void {
    if (!event.isTrusted) return
    selected = invert(selected, filteredIndices)
  }

  function switchCategory(cat: PickerCategory): void {
    activeCategory = cat
    if (!filteredItems.some(it => it.index === activeIndex)) {
      const first = filteredItems[0]
      if (first) {
        activeIndex = first.index
      }
    }
  }

  function categoryLabel(cat: PickerCategory): string {
    switch (cat) {
      case 'all':
        return t('picker_category_all')
      case 'video':
        return t('picker_category_video')
      case 'audio':
        return t('picker_category_audio')
      case 'image':
        return t('picker_category_image')
      case 'archive':
        return t('picker_category_archive')
      case 'document':
        return t('picker_category_document')
      case 'other':
        return t('picker_category_other')
    }
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

  async function moveActive(nextPos: number): Promise<void> {
    if (filteredItems.length === 0) return
    const boundedPos = Math.min(Math.max(0, nextPos), filteredItems.length - 1)
    const targetItem = filteredItems[boundedPos]
    if (!targetItem) return
    activeIndex = targetItem.index
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
      if (!cancelDisabled) onCancel()
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

    const currentPos = filteredItems.findIndex(it => it.index === activeIndex)
    const validPos = currentPos >= 0 ? currentPos : 0

    if (event.key === 'ArrowDown') {
      event.preventDefault()
      void moveActive(validPos + 1)
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      void moveActive(validPos - 1)
      return
    }
    if (event.key === 'Home') {
      event.preventDefault()
      void moveActive(0)
      return
    }
    if (event.key === 'End') {
      event.preventDefault()
      void moveActive(filteredItems.length - 1)
      return
    }
    if (event.key === 'PageDown') {
      event.preventDefault()
      void moveActive(validPos + 6)
      return
    }
    if (event.key === 'PageUp') {
      event.preventDefault()
      void moveActive(validPos - 6)
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
  data-extractor-picker={source === 'extractor' ? '1' : undefined}
  data-dom-picker={source === 'dom' ? '1' : undefined}
  data-burst-picker={source === 'burst' ? '1' : undefined}
  data-theme={isSystemDark ? 'dark' : 'light'}
  data-effects={effects}
  role="dialog"
  aria-modal="true"
  aria-labelledby={titleId}
  tabindex="-1"
  onkeydown={onKeydown}
>
  <button
    type="button"
    class="extractor-picker-scrim"
    tabindex="-1"
    aria-hidden="true"
    onclick={handleScrim}
  ></button>
  <div class="extractor-picker-panel">
    <LiquidGlassPanel radius="var(--radius-squircle-lg, 1.5rem)" {effects} class="extractor-picker-shell">
      <div class="extractor-picker-inner">
        <div class="extractor-picker-header">
          <div class="extractor-picker-title-group">
            <h2 id={titleId} class="extractor-picker-title">{title}</h2>
            <span class="extractor-picker-header-count">
              ({t('picker_selected_count', [String(selectedCount)])})
            </span>
          </div>
          <button
            type="button"
            class="extractor-picker-close"
            aria-label={t('picker_close_aria')}
            disabled={cancelDisabled}
            onclick={handleCancel}
          >
            ✕
          </button>
        </div>

        {@render banner?.()}

        <div class="extractor-picker-toolbar">
          <div class="extractor-picker-selection-actions">
            <button
              type="button"
              class="extractor-picker-textbtn"
              disabled={submitting}
              onclick={onSelectAll}
            >
              {t('picker_select_all')}
            </button>
            <button
              type="button"
              class="extractor-picker-textbtn"
              disabled={submitting}
              onclick={onInvert}
            >
              {t('picker_invert')}
            </button>
          </div>

          <div class="extractor-picker-filter-chips">
            {#each availableCategories as cat (cat)}
              <button
                type="button"
                class="extractor-picker-chip"
                class:is-active={activeCategory === cat}
                aria-pressed={activeCategory === cat}
                disabled={submitting}
                onclick={() => switchCategory(cat)}
              >
                <span>{categoryLabel(cat)}</span>
                <span class="extractor-picker-chip-count">{categoryCounts[cat] ?? 0}</span>
              </button>
            {/each}
          </div>
        </div>

        <div class="extractor-picker-list">
          {#if filteredItems.length === 0}
            <div class="extractor-picker-empty">
              {t('dom_empty_scan_body')}
            </div>
          {:else}
            {#each filteredItems as item (item.index)}
              {@const isSelected = selected.has(item.index)}
              {@const itemCat = categorizePickerItem(item)}
              {@const displayName = getDisplayFilename(
                item.filename,
                item.index + 1,
                t('capsule_item_generic'),
              )}
              {@const secondaryText = formatDisplaySecondary(item)}
              <label
                class="extractor-picker-row"
                class:is-selected={isSelected}
                class:is-active={item.index === activeIndex}
                class:is-disabled={submitting}
                data-picker-row="1"
              >
                <input
                  type="checkbox"
                  data-picker-index={item.index}
                  tabindex={item.index === activeIndex ? 0 : -1}
                  checked={isSelected}
                  disabled={submitting}
                  onchange={event => onToggle(item.index, event)}
                  onfocus={() => {
                    activeIndex = item.index
                  }}
                />
                <div class="extractor-picker-icon-wrapper">
                  <PickerFileIcon category={itemCat} size={18} />
                </div>
                <div class="extractor-picker-item-body">
                  <span class="extractor-picker-name" title={displayName}>
                    {displayName}
                  </span>
                  {#if secondaryText}
                    <span class="extractor-picker-secondary" title={secondaryText}>
                      {secondaryText}
                    </span>
                  {/if}
                </div>
                <span class="extractor-picker-size-badge">
                  {#if isValidKnownSize(item.size_bytes)}
                    {formatPickerBytes(item.size_bytes)}
                  {:else}
                    {t('picker_size_unknown')}
                  {/if}
                </span>
              </label>
            {/each}
          {/if}
        </div>

        <div class="extractor-picker-summary" role="status" aria-live="polite">
          {t('picker_selected_count', [String(selectedCount)])}
          {#if hasKnownBytes}
            · {t('picker_selected_bytes', [formatPickerBytes(knownBytes)])}
          {/if}
        </div>

        <div class="extractor-picker-group-section">
          <div class="extractor-picker-group-row">
            <label class="extractor-picker-group" class:is-disabled={!groupEnabled || submitting}>
              <input
                type="checkbox"
                bind:checked={createGroup}
                disabled={!groupEnabled || submitting}
              />
              <span>{t('picker_create_group')}</span>
            </label>
            {#if !groupEnabled}
              <span class="extractor-picker-group-hint">{t('picker_group_hint')}</span>
            {/if}
          </div>
          <div class="extractor-picker-folder-wrap">
            <label for="extractor-picker-folder-input" class="extractor-picker-folder-label">
              {t('picker_folder_label')}
            </label>
            <input
              id="extractor-picker-folder-input"
              class="extractor-picker-folder"
              type="text"
              value={folderRaw}
              placeholder={t('picker_folder_placeholder')}
              disabled={!createGroup || submitting}
              oninput={onFolderInput}
              oncompositionend={onFolderInput}
            />
          </div>
        </div>

        <div class="extractor-picker-actions">
          <button
            type="button"
            class="extractor-picker-btn"
            disabled={cancelDisabled}
            onclick={handleCancel}
          >
            {t('picker_cancel')}
          </button>
          <button
            type="button"
            class="extractor-picker-btn is-primary"
            disabled={submitting || selectedCount < 1}
            aria-busy={submitting}
            onclick={handleSubmitClick}
          >
            {t('picker_submit')}
          </button>
        </div>
      </div>
    </LiquidGlassPanel>
  </div>
</div>
