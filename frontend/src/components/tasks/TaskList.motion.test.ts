/* eslint-disable vue/one-component-per-file */
import { mount } from '@vue/test-utils'
import { defineComponent, h, type PropType } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
import TaskList from './TaskList.vue'
import type { DownloadGroupMasterItem } from '../../stores/downloadGroups'

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

type UIStoreMock = {
  activeTab: 'downloads' | 'stopped'
}

const storeMocks = vi.hoisted(() => ({
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
    operationNotice: null,
    clearOperationNotice: vi.fn(),
    isGroupOperationBusy: vi.fn(() => false),
    pauseGroup: vi.fn(),
    resumeGroup: vi.fn(),
    openGroupFolder: vi.fn(),
    removeGroup: vi.fn(),
  },
  uiStore: {
    activeTab: 'downloads',
  } as UIStoreMock,
}))

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
      type: Object,
      required: false,
      default: undefined,
    },
  },
  emits: ['confirm-delete'],
  template: '<article class="task-card-stub" :data-card-gid="task.gid"></article>',
})

const TaskHeaderStub = defineComponent({
  name: 'TaskHeader',
  template: '<header data-test="task-header-stub"></header>',
})

const TaskSearchStub = defineComponent({
  name: 'TaskSearch',
  template: '<div data-test="task-search-stub"></div>',
})

const BatchActionBarStub = defineComponent({
  name: 'BatchActionBar',
  emits: ['confirm-batch-delete'],
  template: '<div data-test="batch-action-bar-stub"></div>',
})

const RecycleScrollerStub = defineComponent({
  name: 'RecycleScroller',
  props: {
    items: {
      type: Array as PropType<Task[]>,
      required: true,
    },
    itemSize: {
      type: [Number, Object] as PropType<number | null>,
      required: false,
      default: null,
    },
    keyField: {
      type: String,
      required: true,
    },
    buffer: {
      type: Number,
      required: true,
    },
  },
  setup(props, { attrs, slots }) {
    return () => {
      const children = props.items.flatMap((item, index) => slots.default?.({ item, index }) ?? [])
      return h('div', { ...attrs, 'data-test': 'recycle-scroller-stub' }, children)
    }
  },
})

function createTask(index: number): Task {
  return {
    gid: `gid-${index.toString().padStart(2, '0')}`,
    status: 'active',
    totalLength: '1024',
    completedLength: '128',
    downloadSpeed: '64',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [
      {
        path: `/downloads/file-${index}.bin`,
        uris: [{ uri: `https://example.com/file-${index}.bin`, status: 'used' }],
      },
    ],
  } as Task
}

function createTasks(count: number): Task[] {
  return Array.from({ length: count }, (_, index) => createTask(index))
}

function mountTaskList(tasks: Task[]) {
  storeMocks.taskStore.activeTasks = tasks
  storeMocks.taskStore.waitingTasks = []
  storeMocks.taskStore.stoppedTasks = []
  storeMocks.uiStore.activeTab = 'downloads'

  return mount(TaskList, {
    global: {
      stubs: {
        TaskCard: TaskCardStub,
        TaskHeader: TaskHeaderStub,
        TaskSearch: TaskSearchStub,
        BatchActionBar: BatchActionBarStub,
        DownloadGroupCard: defineComponent({ name: 'DownloadGroupCard', template: '<article />' }),
        RecycleScroller: RecycleScrollerStub,
      },
    },
  })
}

describe('TaskList virtualized row motion', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMocks.taskStore.activeTasks = []
    storeMocks.taskStore.waitingTasks = []
    storeMocks.taskStore.stoppedTasks = []
    storeMocks.taskStore.selectedCount = 0
    storeMocks.taskStore.getSelectedGids = []
    storeMocks.taskStore.getSelectedGroupKeys = []
    storeMocks.downloadGroupStore.masterItems = []
    storeMocks.uiStore.activeTab = 'downloads'
  })

  it('uses the 16-task virtual branch without spring class or stagger delay', () => {
    const wrapper = mountTaskList(createTasks(16))

    const scroller = wrapper.find('[data-test="recycle-scroller-stub"]')
    expect(scroller.exists()).toBe(true)

    const virtualRows = wrapper.findAll('.task-list-virtual-row')
    expect(virtualRows).toHaveLength(16)
    expect(wrapper.findAll('.task-list-virtual-row.animate-spring-in')).toHaveLength(0)

    for (const row of virtualRows) {
      expect(row.classes()).not.toContain('animate-spring-in')
      expect(row.classes()).not.toContain('spring-in')
      expect(row.classes()).not.toContain('spring-out')
      expect(row.attributes('style') ?? '').not.toMatch(/animation-?delay/i)
    }

    wrapper.unmount()
  })

  it('keeps the 15-task small-list branch animated with stagger delay', () => {
    const wrapper = mountTaskList(createTasks(15))

    expect(wrapper.find('[data-test="recycle-scroller-stub"]').exists()).toBe(false)
    expect(wrapper.find('.task-list-virtual-row').exists()).toBe(false)

    const animatedRows = wrapper.findAll('.animate-spring-in')
    expect(animatedRows).toHaveLength(15)
    expect(animatedRows[1].attributes('style') ?? '').toContain('animation-delay: 50ms')

    wrapper.unmount()
  })
})
