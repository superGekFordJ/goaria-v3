import { mount } from '@vue/test-utils'
import { nextTick, onRenderTracked } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import TaskCard from './TaskCard.vue'
import TaskProgressBar from './TaskProgressBar.vue'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'

const storeMocks = vi.hoisted(() => ({
  taskStore: {
    activeTasks: [] as Task[],
    isSelected: vi.fn().mockReturnValue(false),
    toggleSelect: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    openTaskFolder: vi.fn(),
  },
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => ({
    openDetailGid: null,
    effectsTier: 'balanced',
    openTaskDetail: vi.fn(),
    closeTaskDetail: vi.fn(),
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => storeMocks.taskStore,
}))

function mockTask(overrides: Partial<Task> = {}): Task {
  return {
    gid: 'sg_progress_iso',
    status: 'active',
    totalLength: '1000',
    completedLength: '200',
    downloadSpeed: '100',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: '/downloads/archive.zip', uris: [] }],
    ...overrides,
  } as Task
}

describe('TaskProgressBar leaf isolation', () => {
  let now = 1000
  let rafCallback: FrameRequestCallback | null = null
  let rafMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.clearAllMocks()
    now = 1000
    rafCallback = null
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    let id = 0
    rafMock = vi.fn((cb: FrameRequestCallback) => {
      rafCallback = cb
      return ++id
    })
    vi.stubGlobal('requestAnimationFrame', rafMock)
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('updates scaleX inside TaskProgressBar on rAF without tracking displayDownloaded in TaskCard', async () => {
    const trackedKeys: unknown[] = []
    const wrapper = mount(TaskCard, {
      props: { task: mockTask() },
      global: {
        plugins: [
          {
            install(app) {
              app.mixin({
                setup() {
                  onRenderTracked(e => {
                    trackedKeys.push(e.key)
                  })
                },
              })
            },
          },
        ],
      },
    })

    expect(wrapper.findComponent(TaskProgressBar).exists()).toBe(true)
    const fill = wrapper.find('.progress-bar-fill')
    expect(fill.attributes('style')).toContain('scaleX(0.2)')

    // Advance 200ms frame — only TaskProgressBar's displayDownloaded ref mutates
    now += 200
    rafCallback?.(now)
    await nextTick()

    const updatedStyle = fill.attributes('style') || ''
    expect(updatedStyle).not.toContain('scaleX(0.2)')
    expect(updatedStyle).toMatch(/scaleX\(0\.20\d+\)/)
    wrapper.unmount()
  })

  it('does not mount TaskProgressBar or schedule rAF for complete or error tasks', () => {
    const completeWrapper = mount(TaskCard, {
      props: { task: mockTask({ status: 'complete', downloadSpeed: '0' }) },
    })
    expect(completeWrapper.findComponent(TaskProgressBar).exists()).toBe(false)
    expect(rafMock).not.toHaveBeenCalled()
    completeWrapper.unmount()

    const errorWrapper = mount(TaskCard, {
      props: { task: mockTask({ status: 'error', downloadSpeed: '0' }) },
    })
    expect(errorWrapper.findComponent(TaskProgressBar).exists()).toBe(false)
    expect(rafMock).not.toHaveBeenCalled()
    errorWrapper.unmount()
  })
})
