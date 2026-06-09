import { mount } from '@vue/test-utils'
import { defineComponent, reactive, type PropType } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import DownloadGroupShell from './DownloadGroupShell.vue'
import type {
  DownloadGroupCard,
  DownloadGroupDetailEnvelope,
} from '../../../bindings/goaria-v3/internal/downloadgroups/models'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'

const rawStoreMocks = vi.hoisted(() => ({
  uiStore: {
    activeTab: 'downloads' as 'downloads' | 'stopped' | 'settings',
    selectedDownloadGroupKey: 'dg-detail' as string | null,
    openDownloadGroupDetail: vi.fn(),
    closeDownloadGroupDetail: vi.fn(),
  },
  groupStore: {
    isLoading: false,
    isDetailLoading: false,
    degraded: false,
    error: null as string | null,
    detailError: null as string | null,
    currentDetail: null as DownloadGroupDetailEnvelope | null,
    operationNotice: null as unknown,
    fetchGroups: vi.fn().mockResolvedValue(null),
    fetchGroupDetail: vi.fn().mockResolvedValue(null),
    pauseGroup: vi.fn().mockResolvedValue(null),
    resumeGroup: vi.fn().mockResolvedValue(null),
    openGroupFolder: vi.fn().mockResolvedValue(null),
    removeGroup: vi.fn().mockResolvedValue(null),
    clearCurrentDetailForGroup: vi.fn(),
    clearOperationNotice: vi.fn(),
    isGroupOperationBusy: vi.fn().mockReturnValue(false),
  },
  taskStore: {
    clearSelection: vi.fn(),
    getSelectedGids: ['gid-selected'],
  },
}))

const storeMocks = {
  uiStore: reactive(rawStoreMocks.uiStore),
  groupStore: reactive(rawStoreMocks.groupStore),
  taskStore: reactive(rawStoreMocks.taskStore),
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
  }),
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => storeMocks.uiStore,
}))

vi.mock('../../stores/downloadGroups', () => ({
  useDownloadGroupStore: () => storeMocks.groupStore,
  OPERATION_WARNING_CODES: [
    'group_not_found',
    'partial_failure',
    'rpc_error',
    'folder_unsafe',
    'not_paused',
  ],
  normalizeDownloadGroupWarningSummaries: vi.fn(
    (warnings = [], nameStatus = '', degraded = false) => {
      const codes = [
        ...warnings.map((warning: { code: string; severity: string; count?: number }) => ({
          code: warning.code,
          severity: warning.severity || 'warning',
          count: warning.count || 1,
          labelKey: `downloadGroups.warning.code.${warning.code}.label`,
          descriptionKey: `downloadGroups.warning.code.${warning.code}.description`,
        })),
      ]
      if (nameStatus === 'degraded') {
        codes.push({
          code: 'name_degraded',
          severity: 'warning',
          count: 1,
          labelKey: 'downloadGroups.warning.code.name_degraded.label',
          descriptionKey: 'downloadGroups.warning.code.name_degraded.description',
        })
      }
      if (degraded && codes.length === 0) {
        codes.push({
          code: 'stale_group',
          severity: 'warning',
          count: 1,
          labelKey: 'downloadGroups.warning.code.stale_group.label',
          descriptionKey: 'downloadGroups.warning.code.stale_group.description',
        })
      }
      return codes
    },
  ),
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => storeMocks.taskStore,
}))

const TaskListStub = defineComponent({
  name: 'TaskList',
  props: {
    mode: { type: String, required: false, default: 'tab' },
    detailTasks: {
      type: Object as PropType<{ active: Task[]; waiting: Task[]; stopped: Task[] }>,
      required: false,
      default: undefined,
    },
    detailKey: { type: String, required: false, default: '' },
  },
  template: `<div
    data-test="task-list-detail"
    :data-mode="mode"
    :data-detail-key="detailKey"
    :data-active="detailTasks?.active?.length || 0"
    :data-active-progress="detailTasks?.active?.[0]?.completedLength || ''"
    :data-active-speed="detailTasks?.active?.[0]?.downloadSpeed || ''"
    :data-stopped="detailTasks?.stopped?.length || 0"
  ></div>`,
})

function card(id: string): DownloadGroupCard {
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
  } as DownloadGroupCard
}

function detailEnvelope(groupKey = 'dg-detail'): DownloadGroupDetailEnvelope {
  return {
    group_key: groupKey,
    found: true,
    group: card(groupKey),
    tasks: { active: [], waiting: [], stopped: [] },
    updated_at: 1,
    degraded: false,
    warnings: [],
  } as DownloadGroupDetailEnvelope
}

function mountShell() {
  return mount(DownloadGroupShell, {
    attachTo: document.body,
    global: {
      stubs: {
        TaskList: TaskListStub,
      },
    },
  })
}

describe('DownloadGroupShell', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMocks.uiStore.activeTab = 'downloads'
    storeMocks.uiStore.selectedDownloadGroupKey = 'dg-detail'
    storeMocks.uiStore.closeDownloadGroupDetail.mockImplementation(() => {
      storeMocks.uiStore.selectedDownloadGroupKey = null
    })
    storeMocks.groupStore.isLoading = false
    storeMocks.groupStore.isDetailLoading = false
    storeMocks.groupStore.degraded = false
    storeMocks.groupStore.error = null
    storeMocks.groupStore.detailError = null
    storeMocks.groupStore.currentDetail = detailEnvelope()
    storeMocks.groupStore.operationNotice = null
    storeMocks.groupStore.isGroupOperationBusy.mockReturnValue(false)
    storeMocks.taskStore.getSelectedGids = ['gid-selected']
  })

  it('DownloadGroupShell renders detail only and does not render a Groups master list', () => {
    const wrapper = mountShell()

    expect(wrapper.text()).toContain('Group dg-detail')
    expect(wrapper.find('[data-test="task-list-detail"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="group-card"]').exists()).toBe(false)
    expect(wrapper.html()).not.toContain('download-group-grid')
    expect(wrapper.text()).not.toContain('downloadGroups.emptyDescription')
    wrapper.unmount()
  })

  it('DownloadGroupShell refetches selected detail and shows not-found fallback', async () => {
    storeMocks.uiStore.selectedDownloadGroupKey = 'missing'
    storeMocks.groupStore.currentDetail = {
      group_key: 'missing',
      found: false,
      group: card('missing'),
      tasks: { active: [], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: true,
      warnings: [{ code: 'group_not_found', severity: 'warning' }],
    } as DownloadGroupDetailEnvelope

    const wrapper = mountShell()
    await wrapper.vm.$nextTick()

    expect(storeMocks.groupStore.fetchGroups).toHaveBeenCalled()
    expect(storeMocks.groupStore.fetchGroupDetail).toHaveBeenCalledWith('missing')
    expect(wrapper.text()).toContain('downloadGroups.detailNotFoundTitle')
    expect(wrapper.find('[data-test="task-list-detail"]').attributes('data-mode')).toBe(
      'group-detail',
    )
    wrapper.unmount()
  })

  it('DownloadGroupShell does not render stale detail tasks from a previous group key', () => {
    storeMocks.uiStore.selectedDownloadGroupKey = 'dg-current'
    storeMocks.groupStore.currentDetail = {
      group_key: 'dg-previous',
      found: true,
      group: card('dg-previous'),
      tasks: {
        active: [{ gid: 'gid-stale', status: 'active' } as Task],
        waiting: [],
        stopped: [{ gid: 'gid-stale-stopped', status: 'complete' } as Task],
      },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope

    const wrapper = mountShell()
    const detail = wrapper.find('[data-test="task-list-detail"]')

    expect(detail.attributes('data-detail-key')).toBe('dg-current')
    expect(detail.attributes('data-active')).toBe('0')
    expect(detail.attributes('data-stopped')).toBe('0')
    wrapper.unmount()
  })

  it('DownloadGroupShell wires pause resume and open-folder controls to group store actions', async () => {
    storeMocks.uiStore.selectedDownloadGroupKey = 'dg-actions'
    storeMocks.groupStore.currentDetail = detailEnvelope('dg-actions')
    storeMocks.groupStore.currentDetail.group.counts.paused = 1
    storeMocks.groupStore.currentDetail.group.has_folder = true
    const wrapper = mountShell()

    await wrapper.findAll('.download-group-detail-action')[0]?.trigger('click')
    await wrapper.findAll('.download-group-detail-action')[1]?.trigger('click')
    await wrapper.findAll('.download-group-detail-action')[2]?.trigger('click')

    expect(storeMocks.groupStore.pauseGroup).toHaveBeenCalledWith('dg-actions')
    expect(storeMocks.groupStore.resumeGroup).toHaveBeenCalledWith('dg-actions')
    expect(storeMocks.groupStore.openGroupFolder).toHaveBeenCalledWith('dg-actions')
    wrapper.unmount()
  })

  it('DownloadGroupShell confirms remove with delete files and returns to the preserved list', async () => {
    storeMocks.uiStore.selectedDownloadGroupKey = 'dg-remove'
    storeMocks.groupStore.currentDetail = detailEnvelope('dg-remove')
    const wrapper = mountShell()

    await wrapper.findAll('.download-group-detail-action')[3]?.trigger('click')
    await wrapper.vm.$nextTick()
    const checkbox = document.body.querySelector<HTMLInputElement>('input[type="checkbox"]')
    expect(checkbox).toBeTruthy()
    checkbox!.checked = true
    checkbox!.dispatchEvent(new Event('change', { bubbles: true }))
    await wrapper.vm.$nextTick()
    const confirm = document.body.querySelector<HTMLButtonElement>('.download-group-remove-confirm')
    expect(confirm).toBeTruthy()
    confirm!.click()
    await wrapper.vm.$nextTick()

    expect(storeMocks.groupStore.removeGroup).toHaveBeenCalledWith('dg-remove', true)
    expect(storeMocks.uiStore.closeDownloadGroupDetail).toHaveBeenCalled()
    expect(storeMocks.uiStore.activeTab).toBe('downloads')
    expect(storeMocks.groupStore.clearCurrentDetailForGroup).toHaveBeenCalledWith('dg-remove')
    expect(storeMocks.taskStore.clearSelection).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('DownloadGroupShell renders partial failure operation notice with result counts', () => {
    storeMocks.groupStore.operationNotice = {
      id: 1,
      group_key: 'dg-notice',
      action: 'resume',
      severity: 'warning',
      code: 'partial_failure',
      succeeded: 1,
      skipped: 2,
      failed: 1,
      noop: false,
      updated_at: 1,
      result: {
        group_key: 'dg-notice',
        action: 'resume',
        ok: false,
        found: true,
        noop: false,
        total_targets: 4,
        succeeded: 1,
        skipped: 2,
        failed: 1,
        items: [{ status: 'failed', code: 'rpc_error', message: 'backend redacted' }],
        warnings: [{ code: 'partial_failure', severity: 'warning', count: 1 }],
        refresh: { tasks: true, groups: true, detail: true },
        updated_at: 1,
      },
    }
    const wrapper = mountShell()

    expect(wrapper.text()).toContain('downloadGroups.operation.noticeTitle')
    expect(wrapper.text()).toContain('downloadGroups.operation.succeeded: 1')
    expect(wrapper.text()).toContain('downloadGroups.operation.skipped: 2')
    expect(wrapper.text()).toContain('downloadGroups.operation.failedCount: 1')
    expect(wrapper.text()).toContain('downloadGroups.operation.code.partial_failure')
    expect(wrapper.text()).not.toContain('backend redacted')
    wrapper.unmount()
  })

  it('DownloadGroupShell renders operation notice once in detail mode', () => {
    storeMocks.groupStore.operationNotice = {
      id: 2,
      group_key: 'dg-notice-once',
      action: 'pause',
      severity: 'success',
      code: 'paused',
      succeeded: 1,
      skipped: 0,
      failed: 0,
      noop: false,
      updated_at: 1,
      result: {
        group_key: 'dg-notice-once',
        action: 'pause',
        ok: true,
        found: true,
        noop: false,
        total_targets: 1,
        succeeded: 1,
        skipped: 0,
        failed: 0,
        items: [{ status: 'succeeded', code: 'paused' }],
        warnings: [],
        refresh: { tasks: false, groups: false, detail: false },
        updated_at: 1,
      },
    }
    const wrapper = mountShell()

    expect(wrapper.findAll('.download-group-operation-notice')).toHaveLength(1)
    wrapper.unmount()
  })

  it('DownloadGroupShell keeps stale not-found detail fallback after disappearing group', () => {
    storeMocks.uiStore.selectedDownloadGroupKey = 'dg-gone'
    storeMocks.groupStore.currentDetail = {
      group_key: 'dg-gone',
      found: false,
      group: card('dg-gone'),
      tasks: { active: [], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: true,
      warnings: [{ code: 'group_not_found', severity: 'warning' }],
    } as DownloadGroupDetailEnvelope
    const wrapper = mountShell()

    expect(wrapper.text()).toContain('downloadGroups.detailNotFoundTitle')
    expect(storeMocks.uiStore.openDownloadGroupDetail).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('DownloadGroupShell keeps task selection GID-based while group action buttons use group_key', async () => {
    storeMocks.uiStore.selectedDownloadGroupKey = 'dg-boundary'
    storeMocks.taskStore.getSelectedGids = ['gid-selected']
    storeMocks.groupStore.currentDetail = detailEnvelope('dg-boundary')
    storeMocks.groupStore.currentDetail.tasks.active = [
      { gid: 'gid-selected', status: 'active' } as Task,
    ]
    const wrapper = mountShell()

    await wrapper.findAll('.download-group-detail-action')[0]?.trigger('click')

    expect(storeMocks.groupStore.pauseGroup).toHaveBeenCalledWith('dg-boundary')
    expect(storeMocks.groupStore.pauseGroup).not.toHaveBeenCalledWith('gid-selected')
    wrapper.unmount()
  })

  it('selected detail member progress/speed changes after silent detail refetch', async () => {
    storeMocks.uiStore.selectedDownloadGroupKey = 'dg-detail'
    storeMocks.groupStore.currentDetail = {
      ...detailEnvelope('dg-detail'),
      tasks: {
        active: [
          {
            gid: 'gid-member',
            status: 'active',
            completedLength: '10',
            downloadSpeed: '1',
          } as Task,
        ],
        waiting: [],
        stopped: [],
      },
    } as DownloadGroupDetailEnvelope
    const wrapper = mountShell()

    expect(wrapper.find('[data-test="task-list-detail"]').attributes('data-active-progress')).toBe(
      '10',
    )
    expect(wrapper.find('[data-test="task-list-detail"]').attributes('data-active-speed')).toBe('1')

    storeMocks.groupStore.currentDetail = {
      ...detailEnvelope('dg-detail'),
      tasks: {
        active: [
          {
            gid: 'gid-member',
            status: 'active',
            completedLength: '70',
            downloadSpeed: '9',
          } as Task,
        ],
        waiting: [],
        stopped: [],
      },
    } as DownloadGroupDetailEnvelope
    await wrapper.vm.$nextTick()

    expect(wrapper.find('[data-test="task-list-detail"]').attributes('data-active-progress')).toBe(
      '70',
    )
    expect(wrapper.find('[data-test="task-list-detail"]').attributes('data-active-speed')).toBe('9')
    wrapper.unmount()
  })
})
