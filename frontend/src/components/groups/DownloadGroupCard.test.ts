import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import DownloadGroupCard from './DownloadGroupCard.vue'
import type { DownloadGroupMasterItem } from '../../stores/downloadGroups'
import type { DownloadGroupCard as BackendCard } from '../../../bindings/goaria-v3/internal/downloadgroups/models'

const taskStoreMock = vi.hoisted(() => ({
  isGroupSelected: vi.fn(() => false),
  toggleSelectGroup: vi.fn(),
}))

const progressMock = vi.hoisted(() => ({
  displayDownloaded: { value: 25 },
  totalBytes: { value: 100 },
  updateStats: vi.fn(),
}))

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

vi.mock('../../composables/useSmoothProgress', () => ({
  TASK_PROGRESS_CONFIG: {
    emaAlpha: 0.1,
    smoothingFactor: 0.1,
    deviationDecay: 0.07,
    maxScaleDelta: 0.009,
  },
  useSmoothProgress: vi.fn(() => progressMock),
}))

function backendCard(id = 'dg-card'): BackendCard {
  return {
    group_key: id,
    kind: 'batch',
    display_name: `Group ${id}`,
    fallback_name: `Fallback ${id}`,
    name_status: 'fallback',
    status: 'active',
    degraded: false,
    warnings: [],
    counts: {
      expected: 2,
      resolved: 2,
      missing: 0,
      active: 1,
      waiting: 0,
      paused: 0,
      complete: 1,
      error: 0,
      history_only: 0,
    },
    total_length: '100',
    completed_length: '50',
    download_speed: '5',
    progress: 0.5,
    created_at: 1,
    updated_at: 2,
    has_folder: false,
  } as BackendCard
}

function backendItem(id = 'dg-card'): DownloadGroupMasterItem {
  return { type: 'backend', group_key: id, card: backendCard(id) }
}

function placeholderItem(): DownloadGroupMasterItem {
  return {
    type: 'placeholder',
    group_key: 'dg-placeholder',
    placeholder: {
      group_key: 'dg-placeholder',
      download_group: {
        id: 'dg-placeholder',
        kind: 'batch',
        name: 'Pending Display',
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
}

describe('DownloadGroupCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    progressMock.displayDownloaded.value = 25
    progressMock.totalBytes.value = 100
    taskStoreMock.isGroupSelected.mockReturnValue(false)
  })

  it('DownloadGroupCard toggles selectedGroupKeys without opening detail', async () => {
    const wrapper = mount(DownloadGroupCard, { props: { item: backendItem('dg-select') } })
    const checkbox = wrapper.find('input[type="checkbox"]')

    await checkbox.trigger('click')

    expect(taskStoreMock.toggleSelectGroup).toHaveBeenCalledWith('dg-select')
    expect(wrapper.emitted('open')).toBeUndefined()
    wrapper.unmount()
  })

  it('placeholder cards keep selection disabled', async () => {
    const wrapper = mount(DownloadGroupCard, { props: { item: placeholderItem() } })
    const checkbox = wrapper.find<HTMLInputElement>('input[type="checkbox"]')

    expect(checkbox.element.disabled).toBe(true)
    await checkbox.trigger('click')

    expect(taskStoreMock.toggleSelectGroup).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('DownloadGroupCard uses smooth progress stats for aggregate progress', () => {
    progressMock.displayDownloaded.value = 40
    progressMock.totalBytes.value = 200
    const wrapper = mount(DownloadGroupCard, {
      props: { item: backendItem('dg-progress') },
    })

    expect(progressMock.updateStats).toHaveBeenCalledWith({
      downloaded: 50,
      speed: 5,
      total: 100,
      status: 'active',
    })
    expect(wrapper.find('.progress-bar-fill').attributes('style')).toContain('scaleX(0.2)')
    wrapper.unmount()
  })
})
