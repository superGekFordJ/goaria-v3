/* eslint-disable vue/one-component-per-file */
import { mount } from '@vue/test-utils'
import { defineComponent, h, reactive, type PropType } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
import type { TaskGroupHint } from '../../stores/task/grouping'
import TaskList from './TaskList.vue'
import type {
  DownloadGroupBackendMasterItem,
  DownloadGroupMasterItem,
} from '../../stores/downloadGroups'

type TaskStoreMock = {
  activeTasks: Task[]
  waitingTasks: Task[]
  stoppedTasks: Task[]
  selectedCount: number
  getSelectedGids: string[]
  getSelectedGroupKeys: string[]
  clearSelection: ReturnType<typeof vi.fn>
  selectAll: ReturnType<typeof vi.fn>
  remove: ReturnType<typeof vi.fn>
  batchRemove: ReturnType<typeof vi.fn>
  fetchStoppedTasks: ReturnType<typeof vi.fn>
}

const rawStoreMocks = vi.hoisted(() => ({
  taskStore: {
    activeTasks: [],
    waitingTasks: [],
    stoppedTasks: [],
    selectedCount: 0,
    getSelectedGids: [],
    getSelectedGroupKeys: [],
    clearSelection: vi.fn(),
    selectAll: vi.fn(),
    remove: vi.fn(),
    batchRemove: vi.fn(),
    fetchStoppedTasks: vi.fn(),
  } as TaskStoreMock,
  downloadGroupStore: {
    masterItems: [] as DownloadGroupMasterItem[],
    backendCards: [] as unknown[],
    operationNotice: null,
    clearOperationNotice: vi.fn(),
    isGroupOperationBusy: vi.fn(() => false),
    pauseGroup: vi.fn(),
    resumeGroup: vi.fn(),
    openGroupFolder: vi.fn(),
    removeGroup: vi.fn(),
  },
  uiStore: {
    activeTab: 'downloads' as 'downloads' | 'stopped',
    openDownloadGroupDetail: vi.fn(),
  },
}))

const storeMocks = {
  taskStore: reactive(rawStoreMocks.taskStore),
  downloadGroupStore: reactive(rawStoreMocks.downloadGroupStore),
  uiStore: reactive(rawStoreMocks.uiStore),
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => storeMocks.taskStore,
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => storeMocks.uiStore,
}))

vi.mock('../../stores/downloadGroups', async importOriginal => {
  const actual = await importOriginal<typeof import('../../stores/downloadGroups')>()
  return {
    ...actual,
    useDownloadGroupStore: () => storeMocks.downloadGroupStore,
  }
})

const TaskCardStub = defineComponent({
  name: 'TaskCard',
  props: {
    task: {
      type: Object as PropType<Task>,
      required: true,
    },
    groupHint: {
      type: Object as PropType<TaskGroupHint | undefined>,
      required: false,
      default: undefined,
    },
  },
  emits: ['confirm-delete'],
  template:
    '<article class="task-card-stub" :data-card-gid="task.gid" :data-group-key="groupHint?.groupKey || \'\'" :data-visible-count="groupHint?.visibleCount || \'\'" :data-ordinal="groupHint?.ordinal || \'\'"></article>',
})

const TaskHeaderStub = defineComponent({ name: 'TaskHeader', template: '<header />' })
const TaskSearchStub = defineComponent({ name: 'TaskSearch', template: '<div />' })
const BatchActionBarStub = defineComponent({
  name: 'BatchActionBar',
  emits: ['confirm-batch-delete'],
  template:
    '<button data-test="batch-action-bar-stub" @click="$emit(\'confirm-batch-delete\')"></button>',
})

const DownloadGroupCardStub = defineComponent({
  name: 'DownloadGroupCard',
  props: {
    item: { type: Object as PropType<DownloadGroupMasterItem>, required: true },
    operationBusy: { type: Object, required: false, default: undefined },
  },
  emits: ['open', 'pause', 'resume', 'remove', 'open-folder'],
  template: `<article
    class="download-group-card-stub"
    :data-group-key="item.group_key"
    :data-item-type="item.type"
    :data-speed="item.type === 'backend' ? item.card.download_speed : ''"
    :data-progress="item.type === 'backend' ? item.card.progress : ''"
    @click="$emit('open', item.group_key)"
  >
    <button data-test="group-pause" @click.stop="$emit('pause', item.group_key)">pause</button>
    <button data-test="group-resume" @click.stop="$emit('resume', item.group_key)">resume</button>
    <button data-test="group-open-folder" @click.stop="$emit('open-folder', item.group_key)">open</button>
    <button data-test="group-remove" @click.stop="$emit('remove', item.group_key)">remove</button>
  </article>`,
})

const RecycleScrollerStub = defineComponent({
  name: 'RecycleScroller',
  props: {
    items: { type: Array as PropType<Task[]>, required: true },
    itemSize: { type: [Number, Object] as PropType<number | null>, required: false, default: null },
    keyField: { type: String, required: true },
    buffer: { type: Number, required: true },
  },
  setup(props, { attrs, slots }) {
    return () => {
      const children = props.items.flatMap((item, index) => slots.default?.({ item, index }) ?? [])
      return h(
        'div',
        { ...attrs, 'data-test': 'recycle-scroller-stub', 'data-key-field': props.keyField },
        children,
      )
    }
  },
})

const group = {
  id: 'dg-visible',
  kind: 'batch',
  name: 'Batch 2026-05-07 dg-visible',
  folder_name: 'Batch 2026-05-07 dg-visible',
  dir: '/downloads/Batch 2026-05-07 dg-visible',
  item_count: 6,
  created_at: 1770000000,
}

function createTask(index: number, grouped = false): Task {
  return {
    gid: `gid-${index.toString().padStart(2, '0')}`,
    status: 'active',
    totalLength: '1024',
    completedLength: '128',
    downloadSpeed: '64',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: `/downloads/file-${index}.bin`, uris: [] }],
    ...(grouped ? { download_group: group } : {}),
  } as Task
}

function mountList(tasks: Task[]) {
  storeMocks.taskStore.activeTasks = tasks
  storeMocks.taskStore.waitingTasks = []
  storeMocks.taskStore.stoppedTasks = []
  storeMocks.uiStore.activeTab = 'downloads'
  return mount(TaskList, {
    attachTo: document.body,
    global: {
      stubs: {
        TaskCard: TaskCardStub,
        TaskHeader: TaskHeaderStub,
        TaskSearch: TaskSearchStub,
        BatchActionBar: BatchActionBarStub,
        DownloadGroupCard: DownloadGroupCardStub,
        RecycleScroller: RecycleScrollerStub,
      },
    },
  })
}

describe('TaskList visible grouping', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMocks.taskStore.selectedCount = 0
    storeMocks.taskStore.getSelectedGids = []
    storeMocks.taskStore.getSelectedGroupKeys = []
    storeMocks.downloadGroupStore.masterItems = []
    storeMocks.downloadGroupStore.backendCards = []
  })

  it('passes grouped hints to cards and leaves ungrouped cards without hints in non-virtual list', () => {
    const wrapper = mountList([createTask(1, true), createTask(2, false), createTask(3, true)])

    expect(wrapper.find('[data-card-gid="gid-01"]').attributes('data-group-key')).toBe('dg-visible')
    expect(wrapper.find('[data-card-gid="gid-01"]').attributes('data-visible-count')).toBe('2')
    expect(wrapper.find('[data-card-gid="gid-03"]').attributes('data-ordinal')).toBe('2')
    expect(wrapper.find('[data-card-gid="gid-02"]').attributes('data-group-key')).toBe('')

    wrapper.unmount()
  })

  it('passes grouped hints in the virtual branch with display-entry keying', () => {
    const tasks = Array.from({ length: 16 }, (_, index) => createTask(index, index < 3))
    const wrapper = mountList(tasks)

    const scroller = wrapper.find('[data-test="recycle-scroller-stub"]')
    expect(scroller.exists()).toBe(true)
    expect(scroller.attributes('data-key-field')).toBe('key')
    expect(wrapper.find('[data-card-gid="gid-00"]').attributes('data-visible-count')).toBe('3')
    expect(wrapper.find('[data-card-gid="gid-15"]').attributes('data-group-key')).toBe('')

    wrapper.unmount()
  })

  it('selects visible GIDs for Ctrl+A and keeps batch actions task based', async () => {
    const wrapper = mountList([createTask(1, true), createTask(2, true)])

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
    await wrapper.vm.$nextTick()

    expect(storeMocks.taskStore.selectAll).toHaveBeenCalledWith(['gid-01', 'gid-02'], [])

    wrapper.unmount()
  })

  it('TaskList renders inline Downloads group cards and hides grouped member task rows', () => {
    storeMocks.downloadGroupStore.masterItems = [
      {
        type: 'backend',
        group_key: 'dg-visible',
        card: {
          group_key: 'dg-visible',
          status: 'active',
          counts: { active: 1, waiting: 0, paused: 0 },
        },
      } as DownloadGroupMasterItem,
    ]

    const wrapper = mountList([createTask(1, true), createTask(2, false), createTask(3, true)])

    expect(wrapper.find('[data-group-key="dg-visible"]').exists()).toBe(true)
    expect(wrapper.find('[data-card-gid="gid-01"]').exists()).toBe(false)
    expect(wrapper.find('[data-card-gid="gid-03"]').exists()).toBe(false)
    expect(wrapper.find('[data-card-gid="gid-02"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('TaskList renders terminal Completed group cards and never renders placeholders in Completed', () => {
    storeMocks.uiStore.activeTab = 'stopped'
    storeMocks.taskStore.activeTasks = []
    storeMocks.taskStore.waitingTasks = []
    storeMocks.taskStore.stoppedTasks = [
      { ...createTask(1, true), status: 'complete' },
      createTask(2, false),
    ]
    storeMocks.downloadGroupStore.masterItems = [
      {
        type: 'placeholder',
        group_key: 'dg-pending',
        placeholder: {
          group_key: 'dg-pending',
          download_group: group,
          created_at: 1,
          expires_at: 2,
          source: 'batch-add',
        },
      },
      {
        type: 'backend',
        group_key: 'dg-visible',
        card: {
          group_key: 'dg-visible',
          status: 'complete',
          counts: { active: 0, waiting: 0, paused: 0 },
        },
      },
    ] as DownloadGroupMasterItem[]

    const wrapper = mount(TaskList, {
      attachTo: document.body,
      global: {
        stubs: {
          TaskCard: TaskCardStub,
          TaskHeader: TaskHeaderStub,
          TaskSearch: TaskSearchStub,
          BatchActionBar: BatchActionBarStub,
          DownloadGroupCard: DownloadGroupCardStub,
          RecycleScroller: RecycleScrollerStub,
        },
      },
    })

    expect(wrapper.find('[data-group-key="dg-visible"]').exists()).toBe(true)
    expect(wrapper.find('[data-group-key="dg-pending"]').exists()).toBe(false)
    expect(wrapper.find('[data-card-gid="gid-01"]').exists()).toBe(false)
    expect(wrapper.find('[data-card-gid="gid-02"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('TaskList Ctrl+A selects visible tasks and backend groups separately', async () => {
    storeMocks.downloadGroupStore.masterItems = [
      {
        type: 'backend',
        group_key: 'dg-visible',
        card: {
          group_key: 'dg-visible',
          status: 'active',
          counts: { active: 1, waiting: 0, paused: 0 },
        },
      } as DownloadGroupMasterItem,
    ]
    const wrapper = mountList([createTask(1, true), createTask(2, false)])

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
    await wrapper.vm.$nextTick()

    expect(storeMocks.taskStore.selectAll).toHaveBeenCalledWith(['gid-02'], ['dg-visible'])
    wrapper.unmount()
  })

  it('Completed suppresses completed members of a known live group whose card is not emitted', () => {
    storeMocks.uiStore.activeTab = 'stopped'
    storeMocks.taskStore.activeTasks = []
    storeMocks.taskStore.waitingTasks = []
    storeMocks.taskStore.stoppedTasks = [
      { ...createTask(1, true), status: 'complete' },
      createTask(2, false),
    ]
    storeMocks.downloadGroupStore.masterItems = [
      {
        type: 'backend',
        group_key: 'dg-visible',
        card: {
          group_key: 'dg-visible',
          status: 'active',
          counts: { active: 1, waiting: 0, paused: 0, complete: 1 },
        },
      } as DownloadGroupMasterItem,
    ]

    const wrapper = mount(TaskList, {
      attachTo: document.body,
      global: {
        stubs: {
          TaskCard: TaskCardStub,
          TaskHeader: TaskHeaderStub,
          TaskSearch: TaskSearchStub,
          BatchActionBar: BatchActionBarStub,
          DownloadGroupCard: DownloadGroupCardStub,
          RecycleScroller: RecycleScrollerStub,
        },
      },
    })

    expect(wrapper.find('[data-group-key="dg-visible"]').exists()).toBe(false)
    expect(wrapper.find('[data-card-gid="gid-01"]').exists()).toBe(false)
    expect(wrapper.find('[data-card-gid="gid-02"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('TaskList inline group actions call groupKey store actions without task-level IPC', async () => {
    storeMocks.downloadGroupStore.masterItems = [
      {
        type: 'backend',
        group_key: 'dg-visible',
        card: {
          group_key: 'dg-visible',
          status: 'active',
          counts: { active: 1, waiting: 0, paused: 0 },
        },
      } as DownloadGroupMasterItem,
    ]
    const wrapper = mountList([createTask(1, true), createTask(2, false)])

    await wrapper.find('[data-test="group-pause"]').trigger('click')
    await wrapper.find('[data-test="group-open-folder"]').trigger('click')

    expect(storeMocks.downloadGroupStore.pauseGroup).toHaveBeenCalledWith('dg-visible')
    expect(storeMocks.downloadGroupStore.openGroupFolder).toHaveBeenCalledWith('dg-visible')
    expect(storeMocks.taskStore.remove).not.toHaveBeenCalled()
    expect(storeMocks.taskStore.batchRemove).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('mixed delete calls BatchRemove for task GIDs and RemoveDownloadGroup for group keys', async () => {
    storeMocks.taskStore.selectedCount = 2
    storeMocks.taskStore.getSelectedGids = ['gid-selected']
    storeMocks.taskStore.getSelectedGroupKeys = ['dg-selected']
    storeMocks.taskStore.batchRemove.mockResolvedValue(undefined)
    storeMocks.downloadGroupStore.removeGroup.mockResolvedValue(null)
    const wrapper = mountList([createTask(1, false)])

    await wrapper.find('[data-test="batch-action-bar-stub"]').trigger('click')
    await wrapper.vm.$nextTick()
    const buttons = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.fixed button'))
    const confirm = buttons.find(button => button.textContent?.includes('taskList.confirm'))
    expect(confirm).toBeTruthy()
    confirm!.click()
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    await Promise.resolve()

    expect(storeMocks.taskStore.batchRemove).toHaveBeenCalledWith(['gid-selected'], false)
    expect(storeMocks.downloadGroupStore.removeGroup).toHaveBeenCalledWith('dg-selected', false)
    expect(storeMocks.taskStore.clearSelection).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('mixed delete skips BatchRemove when only group keys are selected', async () => {
    storeMocks.taskStore.selectedCount = 1
    storeMocks.taskStore.getSelectedGids = []
    storeMocks.taskStore.getSelectedGroupKeys = ['dg-only']
    storeMocks.downloadGroupStore.removeGroup.mockResolvedValue(null)
    const wrapper = mountList([createTask(1, false)])

    await wrapper.find('[data-test="batch-action-bar-stub"]').trigger('click')
    await wrapper.vm.$nextTick()
    const buttons = Array.from(document.body.querySelectorAll<HTMLButtonElement>('.fixed button'))
    const confirm = buttons.find(button => button.textContent?.includes('taskList.confirm'))
    expect(confirm).toBeTruthy()
    confirm!.click()
    await wrapper.vm.$nextTick()
    await Promise.resolve()

    expect(storeMocks.taskStore.batchRemove).not.toHaveBeenCalled()
    expect(storeMocks.downloadGroupStore.removeGroup).toHaveBeenCalledWith('dg-only', false)
    wrapper.unmount()
  })

  it('active group card speed/progress changes after backend card refetch without clicking manual refresh', async () => {
    const item = {
      type: 'backend',
      group_key: 'dg-visible',
      card: {
        group_key: 'dg-visible',
        status: 'active',
        download_speed: '10',
        progress: 0.1,
        counts: { active: 1, waiting: 0, paused: 0 },
      },
    } as DownloadGroupBackendMasterItem
    storeMocks.downloadGroupStore.masterItems = [item]
    const wrapper = mountList([createTask(1, true)])

    expect(wrapper.find('[data-group-key="dg-visible"]').exists()).toBe(true)
    expect(wrapper.find('[data-group-key="dg-visible"]').attributes('data-speed')).toBe('10')

    item.card = {
      ...item.card,
      download_speed: '50',
      progress: 0.75,
    }
    storeMocks.downloadGroupStore.masterItems = [item]
    await wrapper.vm.$nextTick()

    expect(wrapper.find('[data-group-key="dg-visible"]').attributes('data-speed')).toBe('50')
    expect(wrapper.find('[data-group-key="dg-visible"]').attributes('data-progress')).toBe('0.75')
    wrapper.unmount()
  })

  it('a completed terminal group appears in Completed and hides member files after store card status updates', async () => {
    const terminalItem = {
      type: 'backend',
      group_key: 'dg-visible',
      card: {
        group_key: 'dg-visible',
        status: 'complete',
        counts: { active: 0, waiting: 0, paused: 0, complete: 2 },
      },
    } as DownloadGroupMasterItem
    storeMocks.taskStore.activeTasks = [{ ...createTask(1, true), status: 'active' }]
    storeMocks.taskStore.waitingTasks = []
    storeMocks.taskStore.stoppedTasks = [{ ...createTask(1, true), status: 'complete' }]
    storeMocks.downloadGroupStore.masterItems = [terminalItem]

    storeMocks.uiStore.activeTab = 'downloads'
    const downloadsWrapper = mount(TaskList, {
      attachTo: document.body,
      global: {
        stubs: {
          TaskCard: TaskCardStub,
          TaskHeader: TaskHeaderStub,
          TaskSearch: TaskSearchStub,
          BatchActionBar: BatchActionBarStub,
          DownloadGroupCard: DownloadGroupCardStub,
          RecycleScroller: RecycleScrollerStub,
        },
      },
    })
    expect(
      downloadsWrapper.find('.download-group-card-stub[data-group-key="dg-visible"]').exists(),
    ).toBe(false)
    downloadsWrapper.unmount()

    storeMocks.uiStore.activeTab = 'stopped'
    const completedWrapper = mount(TaskList, {
      attachTo: document.body,
      global: {
        stubs: {
          TaskCard: TaskCardStub,
          TaskHeader: TaskHeaderStub,
          TaskSearch: TaskSearchStub,
          BatchActionBar: BatchActionBarStub,
          DownloadGroupCard: DownloadGroupCardStub,
          RecycleScroller: RecycleScrollerStub,
        },
      },
    })
    expect(
      completedWrapper.find('.download-group-card-stub[data-group-key="dg-visible"]').exists(),
    ).toBe(true)
    expect(completedWrapper.find('[data-card-gid="gid-01"]').exists()).toBe(false)
    completedWrapper.unmount()
  })
})
