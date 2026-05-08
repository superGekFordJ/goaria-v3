import { describe, it, expect, beforeEach } from 'vitest'
import { mergeTasks, isTaskEqual, dedupByGid } from '../utils'
import { clearMetadataCache, cacheMetadata } from '../metadata'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

const mockGroup = {
  id: 'dg-utils',
  kind: 'batch',
  name: 'Batch 2026-05-07 dg-utils',
  folder_name: 'Batch 2026-05-07 dg-utils',
  dir: '/downloads/Batch 2026-05-07 dg-utils',
  item_count: 5,
  created_at: 1770000000,
}

const mockTask = (gid: string, overrides: Partial<Task> = {}): Task => ({
  gid,
  title: 'test-title',
  status: 'active',
  totalLength: '1000',
  completedLength: '500',
  downloadSpeed: '100',
  errorCode: '',
  errorMessage: '',
  files: [],
  dir: '',
  ...overrides,
})

describe('Task Utils', () => {
  describe('isTaskEqual', () => {
    it('should return true for identical tasks', () => {
      const t1 = mockTask('1')
      const t2 = mockTask('1')
      expect(isTaskEqual(t1, t2)).toBe(true)
    })

    it('should return false if critical fields differ', () => {
      const t1 = mockTask('1', { completedLength: '500' })
      const t2 = mockTask('1', { completedLength: '600' })
      expect(isTaskEqual(t1, t2)).toBe(false)
    })

    it('should ignore non-critical fields', () => {
        const t1 = mockTask('1', { files: [] })
        const t2 = mockTask('1', { files: [{ path: '/a', uris: [] }] })
        // isTaskEqual doesn't check files
        expect(isTaskEqual(t1, t2)).toBe(true)
    })

    it('should treat download_group changes as meaningful', () => {
      const t1 = mockTask('1')
      const t2 = mockTask('1', { download_group: mockGroup })
      expect(isTaskEqual(t1, t2)).toBe(false)
    })
  })

  describe('dedupByGid', () => {
    it('should remove duplicates', () => {
      const list = [mockTask('1'), mockTask('2'), mockTask('1')]
      const result = dedupByGid(list)
      expect(result.length).toBe(2)
      expect(result.map(t => t.gid)).toEqual(['1', '2'])
    })
  })

  describe('mergeTasks', () => {
    beforeEach(() => {
        clearMetadataCache()
    })

    it('should preserve metadata when updating from Lite task', () => {
      // Initial state: Task HAS metadata (e.g. from previous fetch or cache)
      const oldTask = mockTask('1', {
          files: [{ path: '/downloads/video.mp4', uris: [] }],
          dir: '/downloads'
      })
      const oldList = [oldTask]

      // New state: Task is "Lite" (no files), but progress updated
      const newTaskLite = mockTask('1', {
          completedLength: '600', // Progress changed
          files: [] // Missing files
      })
      const newList = [newTaskLite]

      const result = mergeTasks(oldList, newList)

      expect(result.changed).toBe(true)
      expect(result.merged[0].completedLength).toBe('600')
      expect(result.merged[0].files).toBeDefined()
      expect(result.merged[0].files![0].path).toBe('/downloads/video.mp4')
    })

    it('should apply cached metadata to new Lite task', () => {
      // Pre-cache metadata
      const fullTask = mockTask('2', { files: [{ path: '/cached/file.mkv', uris: [] }], dir: '/cached' })
      cacheMetadata(fullTask)

      const oldList: Task[] = []
      const newTaskLite = mockTask('2', { files: [] })

      const result = mergeTasks(oldList, [newTaskLite])

      expect(result.merged[0].files![0].path).toBe('/cached/file.mkv')
    })

    it('should NOT return old task if metadata was gained', () => {
        const oldTask = mockTask('3', { files: [] }) // "Parsing..."
        const newTask = mockTask('3', { files: [{ path: '/real.iso', uris: [] }] }) // Resolved

        const result = mergeTasks([oldTask], [newTask])

        expect(result.changed).toBe(true)
        expect(result.merged[0].files![0].path).toBe('/real.iso')
        expect(result.merged[0]).not.toBe(oldTask) // Should be new object
    })

    it('should preserve richer download_group metadata on sparse incoming task', () => {
      const oldTask = mockTask('4', { download_group: mockGroup, files: [] })
      const newTask = mockTask('4', { download_group: undefined, files: [] })

      const result = mergeTasks([oldTask], [newTask])

      expect(result.changed).toBe(false)
      expect(result.merged[0]).toBe(oldTask)
      expect(result.merged[0].download_group?.id).toBe('dg-utils')
    })
  })
})
