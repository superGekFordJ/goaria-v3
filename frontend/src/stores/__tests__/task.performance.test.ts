/**
 * 性能测试 - 测试大量任务列表的渲染性能和内存消耗
 */
import { describe, it, expect, beforeEach } from 'vitest'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import { mergeTasks, dedupByGid } from '../task/utils'
import { clearMetadataCache } from '../task/metadata'

// 创建模拟任务的工厂函数
function createMockTask(index: number, status: string = 'complete'): Task {
  return {
    gid: `gid-${index.toString().padStart(6, '0')}`,
    title: `task-${index}`,
    status,
    totalLength: `${(Math.random() * 1000000000).toFixed(0)}`,
    completedLength: `${(Math.random() * 1000000000).toFixed(0)}`,
    downloadSpeed: '0',
    dir: 'D:\\Downloads',
    errorCode: '',
    errorMessage: '',
    files: [
      {
        path: `D:\\Downloads\\file-${index}.zip`,
        uris: [{ uri: `https://example.com/file-${index}.zip`, status: 'used' }],
      },
    ],
  }
}

// 批量生成任务
function generateMockTasks(count: number, status: string = 'complete'): Task[] {
  return Array.from({ length: count }, (_, i) => createMockTask(i, status))
}

describe('Performance Tests', () => {
  beforeEach(() => {
    clearMetadataCache()
  })

  describe('Task List Generation', () => {
    it('should generate 300 mock tasks efficiently', () => {
      const start = performance.now()
      const tasks = generateMockTasks(300)
      const elapsed = performance.now() - start

      expect(tasks.length).toBe(300)
      expect(elapsed).toBeLessThan(100) // 100ms 生成阈值
    })

    it('should generate 1000 mock tasks efficiently', () => {
      const start = performance.now()
      const tasks = generateMockTasks(1000)
      const elapsed = performance.now() - start

      expect(tasks.length).toBe(1000)
      expect(elapsed).toBeLessThan(500) // 500ms 生成阈值
    })
  })

  describe('mergeTasks Performance', () => {
    it('should merge 300 unchanged tasks efficiently (reference reuse)', () => {
      const tasks = generateMockTasks(300)

      // 模拟完全相同的任务列表（逐项相等）
      const sameTasks = tasks.map(t => ({ ...t }))

      const start = performance.now()
      const result = mergeTasks(tasks, sameTasks)
      const elapsed = performance.now() - start

      // 由于是新对象，changed 为 false（所有字段相等）
      // mergeTasks returns old object if equal
      expect(result.changed).toBe(false)
      // merged 应该是 sameTasks（即使字段相等，也返回原对象引用）
      expect(result.merged.length).toBe(300)
      expect(elapsed).toBeLessThan(50) // 50ms 合并阈值
    })

    it('should merge 1000 tasks with 10% changes efficiently', () => {
      const oldTasks = generateMockTasks(1000)
      const newTasks = oldTasks.map((t, i) => {
        if (i % 10 === 0) {
          return { ...t, completedLength: String(parseInt(t.completedLength || '0') + 1000) }
        }
        return t
      })

      const start = performance.now()
      const result = mergeTasks(oldTasks, newTasks)
      const elapsed = performance.now() - start

      expect(result.changed).toBe(true)
      expect(elapsed).toBeLessThan(100) // 100ms 合并阈值
    })

    it('should handle complete list replacement efficiently', () => {
      const oldTasks = generateMockTasks(500)
      const newTasks = generateMockTasks(500)

      const start = performance.now()
      const result = mergeTasks(oldTasks, newTasks)
      const elapsed = performance.now() - start

      expect(result.changed).toBe(true)
      expect(elapsed).toBeLessThan(100)
    })
  })

  describe('dedupByGid Performance', () => {
    it('should deduplicate 1000 unique tasks efficiently', () => {
      const tasks = generateMockTasks(1000)

      const start = performance.now()
      const deduped = dedupByGid(tasks)
      const elapsed = performance.now() - start

      expect(deduped.length).toBe(1000)
      expect(elapsed).toBeLessThan(50)
    })

    it('should handle 1000 tasks with 50% duplicates', () => {
      const baseTasks = generateMockTasks(500)
      const tasks = [...baseTasks, ...baseTasks] // 50% 重复

      const start = performance.now()
      const deduped = dedupByGid(tasks)
      const elapsed = performance.now() - start

      expect(deduped.length).toBe(500)
      expect(elapsed).toBeLessThan(50)
    })
  })

  describe('Memory Estimation', () => {
    it('should estimate memory for 300 tasks', () => {
      // 每个 Task 对象约 1-2KB (包含 files 数组)
      const tasks = generateMockTasks(300)
      const jsonSize = JSON.stringify(tasks).length

      // 300 任务的 JSON 序列化应该在合理范围内
      expect(jsonSize).toBeLessThan(500 * 1024) // 500KB JSON
      console.log(`300 tasks JSON size: ${(jsonSize / 1024).toFixed(2)} KB`)
    })

    it('should estimate memory for 1000 tasks', () => {
      const tasks = generateMockTasks(1000)
      const jsonSize = JSON.stringify(tasks).length

      expect(jsonSize).toBeLessThan(2 * 1024 * 1024) // 2MB JSON
      console.log(`1000 tasks JSON size: ${(jsonSize / 1024).toFixed(2)} KB`)
    })
  })
})
