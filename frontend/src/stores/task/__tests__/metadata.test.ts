import { describe, it, expect, beforeEach } from 'vitest'
import { cacheMetadata, applyMetadataFromCache, removeMetadata, getMetadataCacheSize, clearMetadataCache } from '../metadata'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

const mockGroup = {
  id: 'dg-meta',
  kind: 'batch',
  name: 'Batch 2026-05-07 dg-meta',
  folder_name: 'Batch 2026-05-07 dg-meta',
  dir: '/downloads/Batch 2026-05-07 dg-meta',
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

  it('should not cache metadata when files and group metadata are missing', () => {
    const task = mockTask('1', { files: [] }) // Lite task
    cacheMetadata(task)
    expect(getMetadataCacheSize()).toBe(0)
  })

  it('should cache group metadata even when files are missing', () => {
    const task = mockTask('1', { files: [], download_group: mockGroup })
    cacheMetadata(task)
    expect(getMetadataCacheSize()).toBe(1)
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

  it('should apply cached group metadata to a lite task', () => {
    cacheMetadata(
      mockTask('1', {
        files: [{ path: '/downloads/file1', uris: [] }],
        dir: '/downloads',
        download_group: mockGroup,
      }),
    )

    const enrichedTask = applyMetadataFromCache(
      mockTask('1', { files: [], download_group: undefined }),
    )

    expect(enrichedTask.download_group?.id).toBe('dg-meta')
    expect(enrichedTask.files[0].path).toBe('/downloads/file1')
  })
})
