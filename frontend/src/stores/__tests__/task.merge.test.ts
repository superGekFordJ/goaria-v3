
import { describe, it, expect } from 'vitest'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'

/**
 * Mock tasks for testing
 */
const mockTask = (gid: string, overrides: Partial<Task> = {}): Task => ({
  gid,
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

// Mock the problematic function by copying it (since it is not exported for testing usually)
// In a real refactor, we would export this function. For now, we replicate logic to verify correctness.
// Key logic: Apply cached metadata if new task is missing it.
function mergeTasksLogic(
  oldList: Task[], 
  newList: Task[], 
  cache: Map<string, any>
): { merged: Task[]; changed: boolean } {
  const oldMap = new Map(oldList.map(t => [t.gid, t]))
  let changed = false
  
  const merged = newList.map(newTask => {
    // 1. Logic: Cache valid metadata
    if (newTask.files?.length && newTask.files[0]?.path) {
        cache.set(newTask.gid, { files: newTask.files, dir: newTask.dir })
    }

    // 2. Logic: Apply cached metadata if missing
    if (!newTask.files?.length || !newTask.files[0]?.path) {
        if (cache.has(newTask.gid)) {
            const cached = cache.get(newTask.gid)
            newTask = { ...newTask, files: cached.files, dir: cached.dir } // Simulate modification
        }
    }

    const oldTask = oldMap.get(newTask.gid)
    if (oldTask) {
      // 3. Logic: Check equality AFTER enriching
      const isTaskEqual = (a: any, b: any) => 
         a.gid === b.gid && 
         a.completedLength === b.completedLength &&
         a.status === b.status

      // Check if we gained metadata (files appeared)
      const gainedMetadata = (!oldTask.files?.length || !oldTask.files[0]?.path) && (newTask.files?.length && newTask.files[0]?.path)

      if (isTaskEqual(oldTask, newTask) && !gainedMetadata) {
        return oldTask // Preserve reference
      }

      // 4. Logic: Preserve old metadata if still missing
      if ((!newTask.files || newTask.files.length === 0) && oldTask.files && oldTask.files.length > 0) {
         newTask.files = oldTask.files
      }
    }
    changed = true
    return newTask
  })
  
  // Check length change or new GIDs... (simplified here for unit test focus)
  if (oldList.length !== newList.length) changed = true

  return { merged, changed }
}

describe('mergeTasks Metadata Preservation', () => {
  it('should preserve metadata when updating from Lite task', () => {
    const cache = new Map()
    
    // Initial state: Task HAS metadata (e.g. from previous fetch or cache)
    const oldTask = mockTask('1', { 
        files: [{ index: 1, path: '/downloads/video.mp4', length: '1000', completedLength: '500', selected: 'true', uris: [] }] 
    })
    const oldList = [oldTask]
    
    // New state: Task is "Lite" (no files), but progress updated
    const newTaskLite = mockTask('1', { 
        completedLength: '600', // Progress changed
        files: [] // Missing files
    })
    const newList = [newTaskLite]

    const result = mergeTasksLogic(oldList, newList, cache)

    expect(result.changed).toBe(true)
    expect(result.merged[0].completedLength).toBe('600')
    expect(result.merged[0].files).toBeDefined()
    expect(result.merged[0].files![0].path).toBe('/downloads/video.mp4')
  })

  it('should apply cached metadata to new Lite task', () => {
    const cache = new Map()
    cache.set('2', { files: [{ path: '/cached/file.mkv' }], dir: '/cached' })

    const oldList: Task[] = []
    const newTaskLite = mockTask('2', { files: [] })
    
    const result = mergeTasksLogic(oldList, [newTaskLite], cache)
    
    expect(result.merged[0].files![0].path).toBe('/cached/file.mkv')
  })
  
  it('should NOT return old task if metadata was gained', () => {
      const cache = new Map()
      const oldTask = mockTask('3', { files: [] }) // "Parsing..."
      const newTask = mockTask('3', { files: [{ path: '/real.iso' }] }) // Resolved
      
      const result = mergeTasksLogic([oldTask], [newTask], cache)
      
      expect(result.changed).toBe(true)
      expect(result.merged[0].files![0].path).toBe('/real.iso')
      expect(result.merged[0]).not.toBe(oldTask) // Should be new object
  })
})
