/* eslint-disable vue/one-component-per-file */
import { mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, reactive, type PropType } from 'vue'
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
  template:
    '<article class="task-card-stub" :data-card-gid="task.gid">{{ task.gid }}:{{ task.downloadSpeed }}</article>',
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

function createTask(gid: string, downloadSpeed: string): Task {
  return {
    gid,
    status: 'active',
    totalLength: '1024',
    completedLength: '128',
    downloadSpeed,
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [
      {
        path: `/downloads/${gid}.bin`,
        uris: [{ uri: `https://example.com/${gid}.bin`, status: 'used' }],
      },
    ],
  } as Task
}

function mountTaskList() {
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

describe('TaskList active/waiting combined-list reactivity', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMocks.taskStore.activeTasks = reactive([createTask('active-1', '64')]) as Task[]
    storeMocks.taskStore.waitingTasks = reactive([
      createTask('waiting-1', '0'),
      createTask('waiting-2', '0'),
    ]) as Task[]
    storeMocks.taskStore.stoppedTasks = []
    storeMocks.taskStore.selectedCount = 0
    storeMocks.taskStore.getSelectedGids = []
    storeMocks.taskStore.getSelectedGroupKeys = []
    storeMocks.downloadGroupStore.masterItems = []
    storeMocks.uiStore.activeTab = 'downloads'
  })

  it('keeps nested active task speed reactive when active and waiting lists are concatenated', async () => {
    const wrapper = mountTaskList()

    expect(wrapper.find('[data-card-gid="active-1"]').text()).toContain('active-1:64')

    storeMocks.taskStore.activeTasks[0].downloadSpeed = '2048'
    await nextTick()

    expect(wrapper.find('[data-card-gid="active-1"]').text()).toContain('active-1:2048')

    wrapper.unmount()
  })
})
