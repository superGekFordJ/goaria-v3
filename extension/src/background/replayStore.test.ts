import { describe, expect, it } from 'vitest'
import { createReplayStore, type ReplayStorage } from './replayStore'

function memoryStorage(): ReplayStorage & { data: Map<string, unknown> } {
  const data = new Map<string, unknown>()
  return {
    data,
    async get(key) {
      return data.get(key)
    },
    async set(key, value) {
      data.set(key, value)
    },
    async remove(key) {
      data.delete(key)
    },
  }
}

describe('replayStore', () => {
  it('persists and reuses a UUID for the same type', async () => {
    const storage = memoryStorage()
    const store = createReplayStore(storage, 'replay_', 60_000, () => 1_000)
    await store.persist('extractor_resolve', 'uuid-1')
    const loaded = await store.load('extractor_resolve')
    expect(loaded?.requestId).toBe('uuid-1')
    const reused = await store.persistOrReuse('extractor_resolve', 'uuid-2')
    expect(reused).toBe('uuid-1')
  })

  it('degrades to memory when storage throws', async () => {
    const throwing: ReplayStorage = {
      async get() {
        throw new Error('no session storage')
      },
      async set() {
        throw new Error('no session storage')
      },
      async remove() {
        throw new Error('no session storage')
      },
    }
    const store = createReplayStore(throwing, 'replay_', 60_000, () => 1_000)
    await expect(store.persist('batch_download', 'uuid-throw')).resolves.toBeUndefined()
    const loaded = await store.load('batch_download')
    expect(loaded?.requestId).toBe('uuid-throw')
  })

  it('drops expired records', async () => {
    let now = 1_000
    const storage = memoryStorage()
    const store = createReplayStore(storage, 'replay_', 50, () => now)
    await store.persist('extractor_resolve', 'uuid-exp')
    now = 2_000
    const loaded = await store.load('extractor_resolve')
    expect(loaded).toBeNull()
  })
})
