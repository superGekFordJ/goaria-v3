/* eslint-disable vue/one-component-per-file */
import { mount } from '@vue/test-utils'
import { KeepAlive, defineComponent, h, nextTick, reactive, ref, type PropType } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
import TaskList from './TaskList.vue'

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
    masterItems: [],
    operationNotice: null,
    clearOperationNotice: vi.fn(),
    isGroupOperationBusy: vi.fn(() => false),
    pauseGroup: vi.fn(),
    resumeGroup: vi.fn(),
    openGroupFolder: vi.fn(),
    removeGroup: vi.fn(),
  },
  uiStore: {
    activeTab: 'downloads' as 'downloads' | 'stopped' | 'settings',
    openDownloadGroupDetail: vi.fn(),
  },
}))

const storeMocks = {
  taskStore: rawStoreMocks.taskStore,
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
    useDownloadGroupStore: () => rawStoreMocks.downloadGroupStore,
  }
})

const TaskCardStub = defineComponent({
  name: 'TaskCard',
  props: {
    task: { type: Object as PropType<Task>, required: true },
  },
  emits: ['confirm-delete'],
  template: '<article class="task-card-stub" :data-card-gid="task.gid"></article>',
})

const TaskHeaderStub = defineComponent({
  name: 'TaskHeader',
  template: '<header data-test="task-header" />',
})
const TaskSearchStub = defineComponent({
  name: 'TaskSearch',
  template: '<div data-test="task-search" />',
})
const BatchActionBarStub = defineComponent({
  name: 'BatchActionBar',
  emits: ['confirm-batch-delete'],
  template: '<div data-test="batch-action-bar-stub"></div>',
})

const RecycleScrollerStub = defineComponent({
  name: 'RecycleScroller',
  props: {
    items: { type: Array as PropType<Task[]>, required: true },
    itemSize: { type: [Number, Object] as PropType<number | null>, required: false, default: null },
    keyField: { type: String, required: true },
  },
  setup(props, { attrs, slots }) {
    return () =>
      h(
        'div',
        { ...attrs, 'data-test': 'recycle-scroller-stub', 'data-key-field': props.keyField },
        props.items.flatMap((item, index) => slots.default?.({ item, index }) ?? []),
      )
  },
})

function task(gid: string, status = 'active'): Task {
  return {
    gid,
    status,
    totalLength: '100',
    completedLength: '10',
    downloadSpeed: '1',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: `/downloads/${gid}.bin`, uris: [] }],
  } as Task
}

function mountList(props: Record<string, unknown> = {}) {
  return mount(TaskList, {
    attachTo: document.body,
    props,
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

describe('TaskList group detail mode', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMocks.taskStore.activeTasks = []
    storeMocks.taskStore.waitingTasks = []
    storeMocks.taskStore.stoppedTasks = []
    storeMocks.taskStore.selectedCount = 0
    storeMocks.taskStore.getSelectedGids = []
    storeMocks.taskStore.getSelectedGroupKeys = []
    storeMocks.uiStore.activeTab = 'downloads'
  })

  it('TaskList group-detail mode renders backend split active waiting stopped tasks', () => {
    const wrapper = mountList({
      mode: 'group-detail',
      detailKey: 'dg-detail',
      detailTasks: {
        active: [task('gid-active', 'active')],
        waiting: [task('gid-waiting', 'waiting')],
        stopped: [task('gid-stopped', 'complete')],
      },
    })

    const gids = wrapper.findAll('.task-card-stub').map(node => node.attributes('data-card-gid'))
    expect(gids).toEqual(['gid-active', 'gid-waiting', 'gid-stopped'])
    expect(wrapper.find('[data-test="task-header"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="task-search"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('TaskList group-detail Ctrl+A selects visible task GIDs not group_key', async () => {
    storeMocks.uiStore.activeTab = 'downloads'
    const wrapper = mountList({
      mode: 'group-detail',
      detailKey: 'dg-not-selected',
      detailTasks: {
        active: [task('gid-one')],
        waiting: [task('gid-two')],
        stopped: [],
      },
    })

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
    await wrapper.vm.$nextTick()

    expect(storeMocks.taskStore.selectAll).toHaveBeenCalledWith(['gid-one', 'gid-two'], [])
    wrapper.unmount()
  })

  it('TaskList group-detail mode clears selected GIDs on unmount when closing detail', () => {
    storeMocks.uiStore.activeTab = 'downloads'
    const wrapper = mountList({
      mode: 'group-detail',
      detailKey: 'dg-close',
      detailTasks: {
        active: [task('gid-selected')],
        waiting: [],
        stopped: [],
      },
    })

    vi.clearAllMocks()
    wrapper.unmount()

    expect(storeMocks.taskStore.clearSelection).toHaveBeenCalled()
  })

  it('TaskList group-detail mode clears selection and ignores Ctrl+A after KeepAlive tab deactivation', async () => {
    storeMocks.uiStore.activeTab = 'downloads'
    const Host = defineComponent({
      components: { KeepAlive, TaskList },
      setup() {
        const showDetail = ref(true)
        return { showDetail }
      },
      template: `
        <KeepAlive>
          <TaskList
            v-if="showDetail"
            mode="group-detail"
            detail-key="dg-keepalive"
            :detail-tasks="{
              active: [{ gid: 'gid-visible', status: 'active', totalLength: '100', completedLength: '0', downloadSpeed: '0', errorCode: '', errorMessage: '', dir: '', files: [] }],
              waiting: [],
              stopped: []
            }"
          />
        </KeepAlive>
      `,
    })
    const wrapper = mount(Host, {
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

    vi.clearAllMocks()
    storeMocks.uiStore.activeTab = 'settings'
    await nextTick()

    expect(storeMocks.taskStore.clearSelection).toHaveBeenCalled()

    vi.clearAllMocks()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
    await nextTick()

    expect(storeMocks.taskStore.selectAll).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('default TaskList clears flat-list selection before deactivating when switching to settings', async () => {
    storeMocks.taskStore.activeTasks = [task('gid-flat')]
    storeMocks.uiStore.activeTab = 'downloads'
    const wrapper = mountList()

    vi.clearAllMocks()
    storeMocks.uiStore.activeTab = 'settings'
    await nextTick()

    expect(storeMocks.taskStore.clearSelection).toHaveBeenCalled()

    vi.clearAllMocks()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
    await nextTick()

    expect(storeMocks.taskStore.selectAll).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('group-detail TaskList clears stale flat-list selection immediately on mount', () => {
    storeMocks.uiStore.activeTab = 'downloads'
    mountList({
      mode: 'group-detail',
      detailKey: 'dg-enter',
      detailTasks: {
        active: [task('gid-detail')],
        waiting: [],
        stopped: [],
      },
    }).unmount()

    expect(storeMocks.taskStore.clearSelection).toHaveBeenCalled()
  })

  it('TaskList default tab mode keeps downloads and stopped behavior unchanged', () => {
    storeMocks.taskStore.activeTasks = [task('gid-active')]
    storeMocks.taskStore.waitingTasks = [task('gid-waiting', 'waiting')]
    storeMocks.uiStore.activeTab = 'downloads'
    const wrapper = mountList()

    expect(wrapper.find('[data-test="task-header"]').exists()).toBe(true)
    expect(
      wrapper.findAll('.task-card-stub').map(node => node.attributes('data-card-gid')),
    ).toEqual(['gid-active', 'gid-waiting'])

    expect(wrapper.find('[data-test="task-search"]').exists()).toBe(false)
    expect(storeMocks.taskStore.fetchStoppedTasks).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
