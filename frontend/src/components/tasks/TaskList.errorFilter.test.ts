 
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, KeepAlive, nextTick, reactive, ref, type PropType } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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
  effectsTier: 'full' as 'full' | 'balanced' | 'reduced',
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
      // The sticky header (error filter tag) lives in the #before slot, so the
      // stub has to render it to keep the two branches comparable.
      return h('div', { ...attrs, 'data-test': 'recycle-scroller-stub' }, [
        slots.before?.() ?? [],
        children,
      ])
    }
  },
})

const OtherPanelStub = defineComponent({
  name: 'OtherPanel',
  template: '<div data-test="other-panel" />',
})

const globalStubs = {
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
}

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

function createTasks(count: number, status: Task['status'], startIndex = 0): Task[] {
  return Array.from({ length: count }, (_, i) => createTask(startIndex + i, status))
}

function mountTaskList() {
  return mount(TaskList, {
    global: { stubs: globalStubs },
  })
}

// Mirrors App.vue: the panel lives inside <KeepAlive>, so switching away
// deactivates it instead of unmounting it.
function mountKeepAliveHost() {
  const showTaskList = ref(true)
  const host = defineComponent({
    name: 'KeepAliveHost',
    setup() {
      return () =>
        h(KeepAlive, null, {
          default: () => [h(showTaskList.value ? TaskList : OtherPanelStub)],
        })
    },
  })
  const wrapper = mount(host, { global: { stubs: globalStubs } })
  return { wrapper, showTaskList }
}

/** Flushes the pre/post watcher queue plus the nextTick that schedules play(). */
async function settle() {
  await nextTick()
  await flushPromises()
}

function activateErrorFilter(wrapper: ReturnType<typeof mountTaskList>) {
  wrapper.findComponent(ErrorFilterTagStub).vm.$emit('toggle')
  return settle()
}

describe('TaskList FLIP guards', () => {
  // The store mocks are module singletons: a wrapper leaked by a failing test
  // would keep reacting to them and pollute the next test's FLIP call counts.
  enableAutoUnmount(afterEach)

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
    uiStoreMock.effectsTier = 'full'
  })

  it('skips FLIP when the last error disappears and the destination is the plain list', async () => {
    taskStoreMock.stoppedTasks = [...createTasks(5, 'error'), ...createTasks(5, 'complete', 5)]
    uiStoreMock.activeTab = 'stopped'

    const wrapper = mountTaskList()
    await settle()
    await activateErrorFilter(wrapper)
    flipMocks.play.mockClear()

    // Deleting every error task also deactivates the filter: the 5 complete
    // tasks were never captured, so FLIP would treat all of them as entering.
    taskStoreMock.stoppedTasks = createTasks(5, 'complete', 5)
    await settle()

    expect(flipMocks.play).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="error-filter-tag-stub"]').exists()).toBe(false)
    expect(wrapper.findAll('.task-card-stub')).toHaveLength(5)
  })

  it('keeps skipping FLIP when the error collapse also crosses down to the plain list', async () => {
    // Regression: the boundary watcher used to reset the flag to false here,
    // which brought back the "whole list snaps up and bounces" bug.
    taskStoreMock.stoppedTasks = [...createTasks(20, 'error'), ...createTasks(10, 'complete', 20)]
    uiStoreMock.activeTab = 'stopped'

    const wrapper = mountTaskList()
    await settle()
    await activateErrorFilter(wrapper)
    expect(wrapper.find('[data-test="recycle-scroller-stub"]').exists()).toBe(true)
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = createTasks(10, 'complete', 20)
    await settle()

    expect(wrapper.find('[data-test="recycle-scroller-stub"]').exists()).toBe(false)
    expect(flipMocks.play).not.toHaveBeenCalled()
  })

  it('lets FLIP play when the error collapse crosses up into the virtual branch at the top', async () => {
    taskStoreMock.stoppedTasks = [...createTasks(5, 'error'), ...createTasks(20, 'complete', 5)]
    uiStoreMock.activeTab = 'stopped'

    const wrapper = mountTaskList()
    await settle()
    await activateErrorFilter(wrapper)
    expect(wrapper.find('[data-test="recycle-scroller-stub"]').exists()).toBe(false)
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = createTasks(20, 'complete', 5)
    await settle()

    expect(wrapper.find('[data-test="recycle-scroller-stub"]').exists()).toBe(true)
    expect(flipMocks.play).toHaveBeenCalled()
  })

  it('lets FLIP play when the tag appears', async () => {
    taskStoreMock.stoppedTasks = createTasks(5, 'complete')
    uiStoreMock.activeTab = 'stopped'

    const wrapper = mountTaskList()
    await settle()
    flipMocks.play.mockClear()

    // The tag is in flow while it fades in, so play() measures the grown
    // header and the cards glide down by the exact delta.
    taskStoreMock.stoppedTasks = [...createTasks(5, 'complete'), ...createTasks(3, 'error', 5)]
    await settle()

    expect(wrapper.find('[data-test="error-filter-tag-stub"]').exists()).toBe(true)
    expect(flipMocks.play).toHaveBeenCalled()
  })

  it('lets FLIP play across the 15↔16 boundary while scrolled to the top', async () => {
    taskStoreMock.stoppedTasks = createTasks(15, 'complete')
    uiStoreMock.activeTab = 'stopped'

    const wrapper = mountTaskList()
    await settle()
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = createTasks(16, 'complete')
    await settle()

    expect(wrapper.find('[data-test="recycle-scroller-stub"]').exists()).toBe(true)
    expect(flipMocks.play).toHaveBeenCalled()
  })

  it('skips FLIP across the boundary when the user has scrolled away from the top', async () => {
    taskStoreMock.stoppedTasks = createTasks(20, 'complete')
    uiStoreMock.activeTab = 'stopped'

    const wrapper = mountTaskList()
    await settle()

    // Recycled off-screen rows were never captured, so FLIP would tear here.
    const scrollRoot = wrapper.find('[data-task-scroll-root]')
    expect(scrollRoot.exists()).toBe(true)
    ;(scrollRoot.element as HTMLElement).scrollTop = 420
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = createTasks(10, 'complete')
    await settle()

    expect(wrapper.find('[data-test="recycle-scroller-stub"]').exists()).toBe(false)
    expect(flipMocks.play).not.toHaveBeenCalled()
  })

  it('skips play() entirely in the reduced effects tier', async () => {
    taskStoreMock.stoppedTasks = createTasks(5, 'complete')
    uiStoreMock.activeTab = 'stopped'
    uiStoreMock.effectsTier = 'reduced'

    mountTaskList()
    await settle()
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = createTasks(6, 'complete')
    await settle()

    // capture() stays warm so switching back to full effects animates at once.
    expect(flipMocks.capture).toHaveBeenCalled()
    expect(flipMocks.play).not.toHaveBeenCalled()
  })

  it('holds the KeepAlive activation guard even when the list crosses the boundary', async () => {
    // Regression: after a KeepAlive restore the boundary watcher used to clear
    // the activation guard, letting cards fly in from stale coordinates.
    taskStoreMock.stoppedTasks = createTasks(15, 'complete')
    uiStoreMock.activeTab = 'stopped'

    const { showTaskList } = mountKeepAliveHost()
    await settle()

    showTaskList.value = false
    await settle()
    showTaskList.value = true
    await settle()
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = createTasks(16, 'complete')
    await settle()

    expect(flipMocks.play).not.toHaveBeenCalled()
  })

  it('does not run FLIP while the panel is deactivated', async () => {
    taskStoreMock.stoppedTasks = createTasks(5, 'complete')
    uiStoreMock.activeTab = 'stopped'

    const { showTaskList } = mountKeepAliveHost()
    await settle()

    showTaskList.value = false
    await settle()
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = createTasks(6, 'complete')
    await settle()

    expect(flipMocks.capture).not.toHaveBeenCalled()
    expect(flipMocks.play).not.toHaveBeenCalled()
  })

  it('skips capture and play on Downloads when keys and sizes are unchanged', async () => {
    taskStoreMock.activeTasks = createTasks(3, 'active')
    uiStoreMock.activeTab = 'downloads'

    mountTaskList()
    await settle()
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.activeTasks = createTasks(3, 'active')
    await settle()

    expect(flipMocks.capture).not.toHaveBeenCalled()
    expect(flipMocks.play).not.toHaveBeenCalled()
  })

  it('still plays on Downloads when a new key is prepended', async () => {
    taskStoreMock.activeTasks = createTasks(2, 'active')
    uiStoreMock.activeTab = 'downloads'

    mountTaskList()
    await settle()
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.activeTasks = [createTask(9), ...createTasks(2, 'active')]
    await settle()

    expect(flipMocks.capture).toHaveBeenCalled()
    expect(flipMocks.capture).toHaveBeenCalledWith(['task:gid-00', 'task:gid-01'])
    expect(flipMocks.play).toHaveBeenCalled()
  })

  it('still plays on Downloads when the same keys change card size', async () => {
    taskStoreMock.activeTasks = createTasks(2, 'active')
    uiStoreMock.activeTab = 'downloads'

    mountTaskList()
    await settle()
    flipMocks.play.mockClear()

    taskStoreMock.activeTasks = createTasks(2, 'paused')
    await settle()

    expect(flipMocks.play).toHaveBeenCalled()
  })

  it('still plays on Stopped when keys and sizes are unchanged', async () => {
    taskStoreMock.stoppedTasks = createTasks(3, 'complete')
    uiStoreMock.activeTab = 'stopped'

    mountTaskList()
    await settle()
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.stoppedTasks = createTasks(3, 'complete')
    await settle()

    expect(flipMocks.capture).toHaveBeenCalled()
    expect(flipMocks.play).toHaveBeenCalled()
  })

  it('plays once when four paused waiting cards become active then skips the same-key follow-up', async () => {
    taskStoreMock.waitingTasks = createTasks(4, 'paused')
    taskStoreMock.activeTasks = []
    uiStoreMock.activeTab = 'downloads'

    mountTaskList()
    await settle()
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    const pausedKeys = ['task:gid-00', 'task:gid-01', 'task:gid-02', 'task:gid-03']
    taskStoreMock.waitingTasks = []
    taskStoreMock.activeTasks = createTasks(4, 'active')
    await settle()

    expect(flipMocks.capture).toHaveBeenCalledTimes(1)
    expect(flipMocks.capture).toHaveBeenCalledWith(pausedKeys)
    expect(flipMocks.play).toHaveBeenCalledTimes(1)

    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.activeTasks = createTasks(4, 'active')
    await settle()
    expect(flipMocks.capture).not.toHaveBeenCalled()
    expect(flipMocks.play).not.toHaveBeenCalled()
  })

  it('plays once for a prepend then skips the same-key follow-up', async () => {
    taskStoreMock.activeTasks = createTasks(2, 'active')
    uiStoreMock.activeTab = 'downloads'

    mountTaskList()
    await settle()
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.activeTasks = [createTask(9), ...createTasks(2, 'active')]
    await settle()
    expect(flipMocks.capture).toHaveBeenCalledTimes(1)
    expect(flipMocks.play).toHaveBeenCalledTimes(1)
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    taskStoreMock.activeTasks = [createTask(9), ...createTasks(2, 'active')]
    await settle()
    expect(flipMocks.capture).not.toHaveBeenCalled()
    expect(flipMocks.play).not.toHaveBeenCalled()
  })

  it('does not skip a same-tick prepend plus same-key replace', async () => {
    taskStoreMock.activeTasks = createTasks(2, 'active')
    uiStoreMock.activeTab = 'downloads'

    mountTaskList()
    await settle()
    flipMocks.capture.mockClear()
    flipMocks.play.mockClear()

    const prepended = [createTask(9), ...createTasks(2, 'active')]
    taskStoreMock.activeTasks = prepended
    taskStoreMock.activeTasks = prepended.map(task => ({ ...task }))
    await settle()

    expect(flipMocks.capture).toHaveBeenCalled()
    expect(flipMocks.play).toHaveBeenCalled()
  })
})
