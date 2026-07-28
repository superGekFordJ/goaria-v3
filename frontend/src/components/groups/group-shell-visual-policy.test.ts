import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import DownloadGroupCard from './DownloadGroupCard.vue'
import type { DownloadGroupMasterItem } from '../../stores/downloadGroups'
import type { DownloadGroupCard as BackendCard } from '../../../bindings/goaria-v3/internal/downloadgroups/models'

const taskStoreMock = vi.hoisted(() => ({
  isGroupSelected: vi.fn(() => false),
  toggleSelectGroup: vi.fn(),
}))

const quietForbiddenFragments = [
  ['go', 'file'].join(''),
  ['go', 'file.io'].join(''),
  ['i', 'bb'].join(''),
  ['i', 'bb.co'].join(''),
  ['i.', 'i', 'bb.co'].join(''),
  ['supported', 'site'].join('-'),
  ['supported', 'sites'].join(' '),
  ['market', 'place'].join(''),
  ['plugin', 'browser'].join(' '),
  ['site', 'catalog'].join(' '),
  ['provider', 'list'].join(' '),
]

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
  }),
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => taskStoreMock,
}))

function backendCard(): BackendCard {
  return {
    group_key: 'dg-visual',
    kind: 'batch',
    display_name: 'Generic Batch',
    fallback_name: 'Generic Fallback',
    name_status: 'fallback',
    status: 'active',
    degraded: true,
    warnings: [{ code: 'missing_members', severity: 'warning' }],
    counts: {
      expected: 3,
      resolved: 2,
      missing: 1,
      active: 1,
      waiting: 0,
      paused: 0,
      complete: 1,
      error: 0,
      history_only: 0,
    },
    total_length: '1000',
    completed_length: '500',
    download_speed: '10',
    progress: 0.5,
    created_at: 1,
    updated_at: 2,
    folder_label: 'Generic Folder',
    has_folder: true,
  } as BackendCard
}

function item(): DownloadGroupMasterItem {
  return { type: 'backend', group_key: 'dg-visual', card: backendCard() }
}

describe('group shell visual policy', () => {
  it('New group components render no provider/site/support advertising terms', () => {
    const wrapper = mount(DownloadGroupCard, { props: { item: item() } })
    const renderedText = wrapper.text().toLowerCase()

    for (const fragment of quietForbiddenFragments) {
      expect(renderedText).not.toContain(fragment)
    }

    wrapper.unmount()
  })

  it('Inline/detail cards include glass-panel or glass-panel-subtle and --radius-squircle-* usage', async () => {
    const wrapper = mount(DownloadGroupCard, { props: { item: item() } })
    const card = wrapper.find('.download-group-card')
    expect(card.classes()).toContain('glass-panel')
    expect(card.attributes('class')).toContain('rounded-[var(--radius-squircle-xl)]')

    const shellSource = await import('./DownloadGroupShell.vue?raw')
    const cardSource = await import('./DownloadGroupCard.vue?raw')
    const source = `${shellSource.default}\n${cardSource.default}`
    expect(source).toContain('glass-panel-subtle')
    expect(source).toMatch(/--radius-squircle-/)
    expect(source).not.toContain('download-group-grid')
    wrapper.unmount()
  })

  it('New group component style blocks contain no hardcoded hex colors and no legacy rgb colors', async () => {
    const shellSource = await import('./DownloadGroupShell.vue?raw')
    const cardSource = await import('./DownloadGroupCard.vue?raw')
    const noticeSource = await import('./DownloadGroupOperationNotice.vue?raw')
    const removeDialogSource = await import('./DownloadGroupRemoveDialog.vue?raw')
    const styles = [
      String(shellSource.default),
      String(cardSource.default),
      String(noticeSource.default),
      String(removeDialogSource.default),
    ]
      .map(raw => raw.match(/<style scoped>[\s\S]*?<\/style>/)?.[0] ?? '')
      .join('\n')

    expect(styles).not.toMatch(/#[0-9a-f]{3,8}\b/i)
    expect(styles).not.toMatch(/(?<!color-mix\(in s)\brgba?\(/i)
  })

  it('Placeholder/degraded warning and notice classes do not require replay-prone animations under reduced effects', async () => {
    document.documentElement.setAttribute('data-effects', 'reduced')
    const placeholderItem: DownloadGroupMasterItem = {
      type: 'placeholder',
      group_key: 'dg-placeholder',
      placeholder: {
        group_key: 'dg-placeholder',
        download_group: {
          id: 'dg-placeholder',
          kind: 'batch',
          name: 'Pending Batch',
          folder_name: 'Pending Batch',
          dir: '/downloads/pending',
          item_count: 4,
          created_at: 1,
        },
        created_at: 1,
        expires_at: 2,
        source: 'batch-add',
      },
    }
    const wrapper = mount(DownloadGroupCard, { props: { item: placeholderItem } })
    const classes = wrapper.find('.download-group-card').attributes('class') ?? ''
    const shellSource = await import('./DownloadGroupShell.vue?raw')
    const noticeSource = await import('./DownloadGroupOperationNotice.vue?raw')
    const source = `${shellSource.default}\n${noticeSource.default}`

    expect(classes).not.toMatch(/animate|pulse|float|ping|shimmer/i)
    expect(source).toContain('download-group-operation-notice-warning')
    expect(source).toContain('download-group-operation-notice-error')

    wrapper.unmount()
    document.documentElement.setAttribute('data-effects', 'balanced')
  })

  it('Placeholder/degraded cards render normally under balanced effects', async () => {
    document.documentElement.setAttribute('data-effects', 'balanced')
    const placeholderItem: DownloadGroupMasterItem = {
      type: 'placeholder',
      group_key: 'dg-placeholder-balanced',
      placeholder: {
        group_key: 'dg-placeholder-balanced',
        download_group: {
          id: 'dg-placeholder-balanced',
          kind: 'batch',
          name: 'Pending Batch',
          folder_name: 'Pending Batch',
          dir: '/downloads/pending',
          item_count: 4,
          created_at: 1,
        },
        created_at: 1,
        expires_at: 2,
        source: 'batch-add',
      },
    }
    const wrapper = mount(DownloadGroupCard, { props: { item: placeholderItem } })
    const classes = wrapper.find('.download-group-card').attributes('class') ?? ''

    // balanced should not kill animations (unlike reduced)
    expect(classes).not.toMatch(/animate-none/)

    wrapper.unmount()
    document.documentElement.setAttribute('data-effects', 'balanced')
  })

  it('warning and operation notice severity styling is tokenized and provider-neutral', async () => {
    const shellSource = await import('./DownloadGroupShell.vue?raw')
    const cardSource = await import('./DownloadGroupCard.vue?raw')
    const noticeSource = await import('./DownloadGroupOperationNotice.vue?raw')
    const source = `${shellSource.default}\n${cardSource.default}\n${noticeSource.default}`

    expect(source).toContain('download-group-chip-warning')
    expect(source).toContain('download-group-chip-error')
    expect(source).toContain('download-group-operation-notice-success')
    expect(source).toContain('download-group-operation-notice-warning')
    expect(source).toContain('download-group-operation-notice-error')
    expect(source).toMatch(/color-mix\(in srgb, var\(--status-/)
    for (const fragment of quietForbiddenFragments) {
      expect(source.toLowerCase()).not.toContain(fragment)
    }
  })

  it('Transparency mode is preserved without component-level backdrop-filter overrides', async () => {
    const shellSource = await import('./DownloadGroupShell.vue?raw')
    const cardSource = await import('./DownloadGroupCard.vue?raw')
    const noticeSource = await import('./DownloadGroupOperationNotice.vue?raw')
    const removeDialogSource = await import('./DownloadGroupRemoveDialog.vue?raw')
    const source = `${shellSource.default}\n${cardSource.default}\n${noticeSource.default}\n${removeDialogSource.default}`

    expect(source).toContain('glass-panel')
    expect(source).not.toMatch(/backdrop-filter\s*:/i)
    expect(source).not.toMatch(/-webkit-backdrop-filter\s*:/i)
  })

  it('placeholder display name is not reused as a folder label when folder_name is empty', () => {
    const placeholderItem: DownloadGroupMasterItem = {
      type: 'placeholder',
      group_key: 'dg-display-only',
      placeholder: {
        group_key: 'dg-display-only',
        download_group: {
          id: 'dg-display-only',
          kind: 'batch',
          name: 'Display Only Name',
          folder_name: '',
          dir: '',
          item_count: 2,
          created_at: 1,
        },
        created_at: 1,
        expires_at: 2,
        source: 'batch-add',
      },
    }
    const wrapper = mount(DownloadGroupCard, { props: { item: placeholderItem } })

    expect(wrapper.text()).toContain('Display Only Name')
    expect(wrapper.text()).not.toContain('downloadGroups.folderLabel')
    wrapper.unmount()
  })

  it('DownloadGroupCard exposes selectable backend cards and disables placeholder selection', () => {
    const backendWrapper = mount(DownloadGroupCard, { props: { item: item() } })
    const backendCheckbox = backendWrapper.find<HTMLInputElement>('input[type="checkbox"]')

    expect(backendCheckbox.exists()).toBe(true)
    expect(backendCheckbox.element.disabled).toBe(false)
    backendWrapper.unmount()

    const placeholderItem: DownloadGroupMasterItem = {
      type: 'placeholder',
      group_key: 'dg-placeholder-selection',
      placeholder: {
        group_key: 'dg-placeholder-selection',
        download_group: {
          id: 'dg-placeholder-selection',
          kind: 'batch',
          name: 'Pending Batch',
          folder_name: 'Pending Batch',
          dir: '/downloads/pending',
          item_count: 2,
          created_at: 1,
        },
        created_at: 1,
        expires_at: 2,
        source: 'batch-add',
      },
    }
    const placeholderWrapper = mount(DownloadGroupCard, { props: { item: placeholderItem } })
    const placeholderCheckbox = placeholderWrapper.find<HTMLInputElement>('input[type="checkbox"]')

    expect(placeholderCheckbox.exists()).toBe(true)
    expect(placeholderCheckbox.element.disabled).toBe(true)
    placeholderWrapper.unmount()
  })
})
