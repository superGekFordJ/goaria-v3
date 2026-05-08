/* eslint-disable vue/one-component-per-file */
import { mount } from '@vue/test-utils'
import { defineComponent, h, type PropType } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
import type { TaskGroupHint } from '../../stores/task/grouping'
import TaskList from './TaskList.vue'

type TaskStoreMock = {
  activeTasks: Task[]
  waitingTasks: Task[]
  stoppedTasks: Task[]
  selectedCount: number
  getSelectedGids: string[]
  clearSelection: ReturnType<typeof vi.fn>
  selectAll: ReturnType<typeof vi.fn>
  remove: ReturnType<typeof vi.fn>
  batchRemove: ReturnType<typeof vi.fn>
  fetchStoppedTasks: ReturnType<typeof vi.fn>
}

const storeMocks = vi.hoisted(() => ({
  taskStore: {
    activeTasks: [],
    waitingTasks: [],
    stoppedTasks: [],
    selectedCount: 0,
    getSelectedGids: [],
    clearSelection: vi.fn(),
    selectAll: vi.fn(),
    remove: vi.fn(),
    batchRemove: vi.fn(),
    fetchStoppedTasks: vi.fn(),
  } as TaskStoreMock,
  uiStore: {
    activeTab: 'downloads' as 'downloads' | 'stopped',
  },
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
  template: `<article class="task-card-stub" :data-card-gid="task.gid" :data-group-key="groupHint?.groupKey || ''" :data-item-count="groupHint?.itemCount || ''"></article>`,
})

const TaskHeaderStub = defineComponent({ name: 'TaskHeader', template: '<header />' })
const TaskSearchStub = defineComponent({ name: 'TaskSearch', template: '<div />' })
const BatchActionBarStub = defineComponent({
  name: 'BatchActionBar',
  emits: ['confirm-batch-delete'],
  template: '<div data-test="batch-action-bar-stub"></div>',
})

const RecycleScrollerStub = defineComponent({
  name: 'RecycleScroller',
  props: {
    items: { type: Array as PropType<Task[]>, required: true },
    itemSize: { type: Number, required: true },
    keyField: { type: String, required: true },
    buffer: { type: Number, required: true },
  },
  setup(props, { attrs, slots }) {
    return () => {
      const children = props.items.flatMap((item, index) => slots.default?.({ item, index }) ?? [])
      return h('div', { ...attrs, 'data-test': 'recycle-scroller-stub', 'data-key-field': props.keyField }, children)
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
        RecycleScroller: RecycleScrollerStub,
      },
    },
  })
}

describe('TaskList visible group hints', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMocks.taskStore.selectedCount = 0
    storeMocks.taskStore.getSelectedGids = []
  })

  it('passes grouped hints in the non-virtual branch while keeping the list flat', () => {
    const wrapper = mountList([createTask(1, true), createTask(2, false), createTask(3, true)])

    const cards = wrapper.findAll('.task-card-stub')
    expect(cards).toHaveLength(3)
    expect(wrapper.find('[data-card-gid="gid-01"]').attributes('data-group-key')).toBe('dg-visible')
    expect(wrapper.find('[data-card-gid="gid-03"]').attributes('data-item-count')).toBe('6')
    expect(wrapper.find('[data-card-gid="gid-02"]').attributes('data-group-key')).toBe('')

    wrapper.unmount()
  })

  it('passes grouped hints in the virtual branch without changing key-field', () => {
    const tasks = Array.from({ length: 16 }, (_, index) => createTask(index, index < 3))
    const wrapper = mountList(tasks)

    const scroller = wrapper.find('[data-test="recycle-scroller-stub"]')
    expect(scroller.exists()).toBe(true)
    expect(scroller.attributes('data-key-field')).toBe('gid')
    expect(wrapper.find('[data-card-gid="gid-00"]').attributes('data-group-key')).toBe('dg-visible')
    expect(wrapper.find('[data-card-gid="gid-15"]').attributes('data-group-key')).toBe('')

    wrapper.unmount()
  })

  it('keeps selection semantics GID-based for Ctrl+A', async () => {
    const wrapper = mountList([createTask(1, true), createTask(2, true)])

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
    await wrapper.vm.$nextTick()

    expect(storeMocks.taskStore.selectAll).toHaveBeenCalledWith(['gid-01', 'gid-02'])
    expect(storeMocks.taskStore.selectAll).not.toHaveBeenCalledWith(['dg-visible'])

    wrapper.unmount()
  })
})
