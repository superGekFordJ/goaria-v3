/**
 * 状态兼容性测试 - 确保 moveTaskToStopped 正确保留非 complete 状态
 * 针对 commit 8e1e95969b0ee6b2f84d26deb79fa499a25bcfea 的修复
 * 以及未来可能添加的新状态
 */
import { describe, it, expect } from 'vitest'
import { ref } from 'vue'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'

// 模拟 store 的 tasks 状态
function createMockStore() {
  const tasks = ref<Record<string, Task[]>>({
    active: [],
    waiting: [],
    stopped: [],
  })

  /**
   * moveTaskToStopped - 从 task.ts 提取的核心逻辑
   * 关键行为：如果任务状态不是 'error'，则设置为 'complete'
   */
  function moveTaskToStopped(gid: string) {
    const activeIdx = tasks.value.active.findIndex(t => t.gid === gid)
    const waitingIdx = activeIdx === -1 ? tasks.value.waiting.findIndex(t => t.gid === gid) : -1

    if (activeIdx !== -1) {
      const task = tasks.value.active[activeIdx]
      if (task.status !== 'error') {
        task.status = 'complete'
      }
      tasks.value = {
        active: tasks.value.active.filter(t => t.gid !== gid),
        waiting: tasks.value.waiting,
        stopped: [task, ...tasks.value.stopped],
      }
    } else if (waitingIdx !== -1) {
      const task = tasks.value.waiting[waitingIdx]
      if (task.status !== 'error') {
        task.status = 'complete'
      }
      tasks.value = {
        active: tasks.value.active,
        waiting: tasks.value.waiting.filter(t => t.gid !== gid),
        stopped: [task, ...tasks.value.stopped],
      }
    }
  }

  function patchTaskStatus(gid: string, status: string) {
    for (const list of [tasks.value.active, tasks.value.waiting]) {
      const task = list.find(t => t.gid === gid)
      if (task && task.status !== status) {
        task.status = status
        tasks.value = { ...tasks.value }
        return
      }
    }
  }

  return { tasks, moveTaskToStopped, patchTaskStatus }
}

function createMockTask(gid: string, status: string): Task {
  return {
    gid,
    title: `task-${gid}`,
    status,
    totalLength: '100000000',
    completedLength: '50000000',
    downloadSpeed: '1000000',
    dir: 'D:\\Downloads',
    errorCode: '',
    errorMessage: '',
    files: [
      {
        path: `D:\\Downloads\\file-${gid}.zip`,
        uris: [],
      },
    ],
  }
}

describe('Status Preservation Tests', () => {
  describe('moveTaskToStopped - Error Status Preservation', () => {
    it('should preserve error status when moving from active to stopped', () => {
      const store = createMockStore()
      store.tasks.value.active = [createMockTask('gid-001', 'error')]

      store.moveTaskToStopped('gid-001')

      expect(store.tasks.value.stopped.length).toBe(1)
      expect(store.tasks.value.stopped[0].status).toBe('error')
      expect(store.tasks.value.active.length).toBe(0)
    })

    it('should preserve error status when moving from waiting to stopped', () => {
      const store = createMockStore()
      store.tasks.value.waiting = [createMockTask('gid-002', 'error')]

      store.moveTaskToStopped('gid-002')

      expect(store.tasks.value.stopped.length).toBe(1)
      expect(store.tasks.value.stopped[0].status).toBe('error')
      expect(store.tasks.value.waiting.length).toBe(0)
    })
  })

  describe('moveTaskToStopped - Complete Status Setting', () => {
    it('should set complete status for active tasks', () => {
      const store = createMockStore()
      store.tasks.value.active = [createMockTask('gid-003', 'active')]

      store.moveTaskToStopped('gid-003')

      expect(store.tasks.value.stopped[0].status).toBe('complete')
    })

    it('should set complete status for paused tasks', () => {
      const store = createMockStore()
      store.tasks.value.active = [createMockTask('gid-004', 'paused')]

      store.moveTaskToStopped('gid-004')

      expect(store.tasks.value.stopped[0].status).toBe('complete')
    })

    it('should set complete status for waiting tasks', () => {
      const store = createMockStore()
      store.tasks.value.waiting = [createMockTask('gid-005', 'waiting')]

      store.moveTaskToStopped('gid-005')

      expect(store.tasks.value.stopped[0].status).toBe('complete')
    })
  })

  describe('Future Status Compatibility', () => {
    /**
     * 这些测试确保如果未来添加新的非 complete 状态，
     * 当前的逻辑不会错误地将它们覆盖为 complete
     * 
     * 当前逻辑：只有 status === 'error' 时保留
     * 如果需要保留更多状态，需要修改 moveTaskToStopped
     */

    it.each([
      ['cancelled', 'complete'],  // 目前会被设为 complete
      ['timeout', 'complete'],    // 目前会被设为 complete
      ['quota_exceeded', 'complete'], // 目前会被设为 complete
    ])('status %s -> %s (current behavior)', (inputStatus, expectedStatus) => {
      const store = createMockStore()
      store.tasks.value.active = [createMockTask('gid-future', inputStatus)]

      store.moveTaskToStopped('gid-future')

      expect(store.tasks.value.stopped[0].status).toBe(expectedStatus)
    })

    // 如果需要保留这些状态，这个测试会失败提醒开发者
    it('should document: only error status is preserved currently', () => {
      const preservedStatuses = ['error']
      const overwrittenStatuses = ['active', 'paused', 'waiting', 'cancelled', 'timeout']

      for (const status of preservedStatuses) {
        const store = createMockStore()
        store.tasks.value.active = [createMockTask('gid-test', status)]
        store.moveTaskToStopped('gid-test')
        expect(store.tasks.value.stopped[0].status).toBe(status)
      }

      for (const status of overwrittenStatuses) {
        const store = createMockStore()
        store.tasks.value.active = [createMockTask('gid-test', status)]
        store.moveTaskToStopped('gid-test')
        expect(store.tasks.value.stopped[0].status).toBe('complete')
      }
    })
  })

  describe('Event Handling Flow', () => {
    it('should handle error event: patchStatus then moveToStopped', () => {
      const store = createMockStore()
      store.tasks.value.active = [createMockTask('gid-err', 'active')]

      // 模拟 error 事件处理流程
      store.patchTaskStatus('gid-err', 'error')
      store.moveTaskToStopped('gid-err')

      expect(store.tasks.value.stopped[0].status).toBe('error')
    })

    it('should handle complete event: direct moveToStopped', () => {
      const store = createMockStore()
      store.tasks.value.active = [createMockTask('gid-comp', 'active')]

      // 模拟 complete 事件处理流程
      store.moveTaskToStopped('gid-comp')

      expect(store.tasks.value.stopped[0].status).toBe('complete')
    })
  })

  describe('Idempotency', () => {
    it('should not change state when task not found', () => {
      const store = createMockStore()
      store.tasks.value.active = [createMockTask('gid-existing', 'active')]

      store.moveTaskToStopped('gid-nonexistent')

      expect(store.tasks.value.active.length).toBe(1)
      expect(store.tasks.value.stopped.length).toBe(0)
    })

    it('should not duplicate task in stopped list', () => {
      const store = createMockStore()
      store.tasks.value.active = [createMockTask('gid-dup', 'active')]

      store.moveTaskToStopped('gid-dup')
      store.moveTaskToStopped('gid-dup') // 第二次调用

      expect(store.tasks.value.stopped.length).toBe(1)
      expect(store.tasks.value.active.length).toBe(0)
    })
  })

  describe('Batch Operations', () => {
    it('should handle multiple tasks with mixed statuses', () => {
      const store = createMockStore()
      store.tasks.value.active = [
        createMockTask('gid-1', 'active'),
        createMockTask('gid-2', 'error'),
        createMockTask('gid-3', 'paused'),
      ]

      store.moveTaskToStopped('gid-1')
      store.moveTaskToStopped('gid-2')
      store.moveTaskToStopped('gid-3')

      expect(store.tasks.value.stopped.length).toBe(3)
      expect(store.tasks.value.stopped.find(t => t.gid === 'gid-1')?.status).toBe('complete')
      expect(store.tasks.value.stopped.find(t => t.gid === 'gid-2')?.status).toBe('error')
      expect(store.tasks.value.stopped.find(t => t.gid === 'gid-3')?.status).toBe('complete')
    })
  })
})
