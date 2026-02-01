import { describe, it, expect, beforeEach } from 'vitest'
import { cacheMetadata, applyMetadataFromCache, removeMetadata, getMetadataCacheSize, clearMetadataCache } from '../metadata'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

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

describe('Task Metadata Cache', () => {
  beforeEach(() => {
    clearMetadataCache()
  })

  it('should cache metadata when files are present', () => {
    const task = mockTask('1', {
      files: [{ path: '/downloads/file1', uris: [] }],
      dir: '/downloads',
      totalLength: '1000'
    })

    cacheMetadata(task)
    expect(getMetadataCacheSize()).toBe(1)
  })

  it('should not cache metadata when files are missing', () => {
    const task = mockTask('1', { files: [] }) // Lite task
    cacheMetadata(task)
    expect(getMetadataCacheSize()).toBe(0)
  })

  it('should apply cached metadata to a lite task', () => {
    // Cache first
    const fullTask = mockTask('1', {
      files: [{ path: '/downloads/file1', uris: [] }],
      dir: '/downloads'
    })
    cacheMetadata(fullTask)

    // Apply to lite task
    const liteTask = mockTask('1', { files: [] })
    const enrichedTask = applyMetadataFromCache(liteTask)

    expect(enrichedTask.files).toEqual(fullTask.files)
    expect(enrichedTask.dir).toEqual(fullTask.dir)
  })

  it('should remove metadata', () => {
    const task = mockTask('1', {
      files: [{ path: '/downloads/file1', uris: [] }],
      dir: '/downloads'
    })
    cacheMetadata(task)
    expect(getMetadataCacheSize()).toBe(1)

    removeMetadata('1')
    expect(getMetadataCacheSize()).toBe(0)
  })
})
