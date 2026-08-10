/* eslint-disable vue/one-component-per-file */
import { mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, reactive, type PropType } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
import TaskList from './TaskList.vue'
import type { DownloadGroupMasterItem } from '../../stores/downloadGroups'

const flipMocks = vi.hoisted(() => ({
  capture: vi.fn(),
  play: vi.fn(),
  clear: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('../../composables/useFLIPAnimation', () => ({
  useFLIPAnimation: () => ({
    capture: flipMocks.capture,
    play: flipMocks.play,
    clear: flipMocks.clear,
  }),
}))

const taskStoreMock = reactive({
  activeTasks: [] as Task[],
  waitingTasks: [] as Task[],
  stoppedTasks: [] as Task[],
  selectedCount: 0,
  getSelectedGids: [] as string[],
  getSelectedGroupKeys: [] as string[],
  clearSelection: vi.fn(),
  selectAll: vi.fn(),
  remove: vi.fn(),
  batchRemove: vi.fn(),
  fetchStoppedTasks: vi.fn(),
})

const uiStoreMock = reactive({
  activeTab: 'downloads' as 'downloads' | 'stopped',
})

const downloadGroupStoreMock = {
  masterItems: [] as DownloadGroupMasterItem[],
  operationNotice: null,
  clearOperationNotice: vi.fn(),
  isGroupOperationBusy: vi.fn(() => false),
  pauseGroup: vi.fn(),
  resumeGroup: vi.fn(),
  openGroupFolder: vi.fn(),
  removeGroup: vi.fn(),
}

vi.mock('../../stores/task', () => ({
  useTaskStore: () => taskStoreMock,
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => uiStoreMock,
}))

vi.mock('../../stores/downloadGroups', async importOriginal => {
  const actual = await importOriginal<typeof import('../../stores/downloadGroups')>()
  return {
    ...actual,
    useDownloadGroupStore: () => downloadGroupStoreMock,
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
  template: '<header />',
})

const TaskSearchStub = defineComponent({
  name: 'TaskSearch',
  template: '<div />',
})

const BatchActionBarStub = defineComponent({
  name: 'BatchActionBar',
  emits: ['confirm-batch-delete'],
  template: '<div />',
})

const ErrorFilterTagStub = defineComponent({
  name: 'ErrorFilterTag',
  props: {
    errorCount: { type: Number, required: true },
    active: { type: Boolean, required: true },
  },
  emits: ['toggle'],
  template: '<div data-test="error-filter-tag-stub"></div>',
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

function createTask(index: number, status: Task['status'] = 'active'): Task {
  return {
    gid: `gid-${index.toString().padStart(2, '0')}`,
    status,
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

function mountTaskList() {
  return mount(TaskList, {
    global: {
      stubs: {
        TaskCard: TaskCardStub,
        TaskHeader: TaskHeaderStub,
        TaskSearch: TaskSearchStub,
        BatchActionBar: BatchActionBarStub,
        ErrorFilterTag: ErrorFilterTagStub,
        DownloadGroupCard: defineComponent({ name: 'DownloadGroupCard', template: '<article />' }),
        TaskListEmptyState: defineComponent({
          name: 'TaskListEmptyState',
          template: '<div />',
        }),
        TaskListDeleteModal: defineComponent({
          name: 'TaskListDeleteModal',
          template: '<div />',
        }),
        TaskListBatchDeleteModal: defineComponent({
          name: 'TaskListBatchDeleteModal',
          template: '<div />',
        }),
        DownloadGroupOperationNotice: defineComponent({
          name: 'DownloadGroupOperationNotice',
          template: '<div />',
        }),
        DownloadGroupRemoveDialog: defineComponent({
          name: 'DownloadGroupRemoveDialog',
          template: '<div />',
        }),
        RecycleScroller: RecycleScrollerStub,
      },
    },
  })
}

describe('TaskList errorCount watcher', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    taskStoreMock.activeTasks = []
    taskStoreMock.waitingTasks = []
    taskStoreMock.stoppedTasks = []
    taskStoreMock.selectedCount = 0
    taskStoreMock.getSelectedGids = []
    taskStoreMock.getSelectedGroupKeys = []
    downloadGroupStoreMock.masterItems = []
    uiStoreMock.activeTab = 'downloads'
  })

  it('sets skipNextFlip when errorCount crosses zero (tag disappears)', async () => {
    taskStoreMock.stoppedTasks = Array.from({ length: 5 }, (_, i) => createTask(i, 'error'))
    uiStoreMock.activeTab = 'stopped'

    const wrapper = mountTaskList()

    await nextTick()
    await nextTick()

    flipMocks.clear.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = Array.from({ length: 5 }, (_, i) => createTask(i, 'complete'))

    await nextTick()
    await nextTick()

    expect(flipMocks.clear).toHaveBeenCalled()
    expect(flipMocks.play).not.toHaveBeenCalled()

    wrapper.unmount()
  })

  it('sets skipNextFlip when errorCount crosses zero (tag appears)', async () => {
    taskStoreMock.stoppedTasks = Array.from({ length: 5 }, (_, i) => createTask(i, 'complete'))
    uiStoreMock.activeTab = 'stopped'

    const wrapper = mountTaskList()

    await nextTick()
    await nextTick()

    flipMocks.clear.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = [
      ...Array.from({ length: 5 }, (_, i) => createTask(i, 'complete')),
      ...Array.from({ length: 3 }, (_, i) => createTask(i + 5, 'error')),
    ]

    await nextTick()
    await nextTick()

    expect(flipMocks.clear).toHaveBeenCalled()
    expect(flipMocks.play).not.toHaveBeenCalled()

    wrapper.unmount()
  })
})
