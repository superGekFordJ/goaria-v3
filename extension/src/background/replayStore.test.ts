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
    async getAll() {
      const all: Record<string, unknown> = {}
      for (const [key, value] of data) {
        all[key] = value
      }
      return all
    },
  }
}

describe('replayStore', () => {
  it('persists and loads a UUID by request id', async () => {
    const storage = memoryStorage()
    const store = createReplayStore(storage, 'replay_', 60_000, () => 1_000)
    await store.persist('extractor_resolve', 'uuid-1')
    const loaded = await store.load('uuid-1')
    expect(loaded?.requestId).toBe('uuid-1')
    const reused = await store.persistOrReuse('extractor_resolve', 'uuid-1')
    expect(reused).toBe('uuid-1')
  })

  it('does not share a UUID across two overlapping persists of the same type', async () => {
    const storage = memoryStorage()
    const store = createReplayStore(storage, 'replay_', 60_000, () => 1_000)
    const first = await store.persistOrReuse('extractor_resolve', 'uuid-1')
    const second = await store.persistOrReuse('extractor_resolve', 'uuid-2')
    expect(first).toBe('uuid-1')
    expect(second).toBe('uuid-2')
    expect(first).not.toBe(second)
    expect((await store.load('uuid-1'))?.requestId).toBe('uuid-1')
    expect((await store.load('uuid-2'))?.requestId).toBe('uuid-2')
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
    const loaded = await store.load('uuid-throw')
    expect(loaded?.requestId).toBe('uuid-throw')
  })

  it('drops expired records', async () => {
    let now = 1_000
    const storage = memoryStorage()
    const store = createReplayStore(storage, 'replay_', 50, () => now)
    await store.persist('extractor_resolve', 'uuid-exp')
    now = 2_000
    const loaded = await store.load('uuid-exp')
    expect(loaded).toBeNull()
  })

  it('overwrites the stored type on persistOrReuse type mismatch', async () => {
    const storage = memoryStorage()
    const store = createReplayStore(storage, 'replay_', 60_000, () => 1_000)
    await store.persist('extractor_resolve', 'uuid-1')
    const reused = await store.persistOrReuse('batch_download', 'uuid-1')
    expect(reused).toBe('uuid-1')
    expect((await store.load('uuid-1'))?.type).toBe('batch_download')
  })

  it('sweeps expired memory keys on persist', async () => {
    let now = 1_000
    const storage = memoryStorage()
    const store = createReplayStore(storage, 'replay_', 50, () => now)
    await store.persist('extractor_resolve', 'uuid-old')
    expect(storage.data.has('replay_uuid-old')).toBe(true)
    now = 2_000
    await store.persist('extractor_resolve', 'uuid-new')
    expect(storage.data.has('replay_uuid-old')).toBe(false)
    expect(storage.data.has('replay_uuid-new')).toBe(true)
  })

  it('sweeps expired storage keys after a cold store using getAll', async () => {
    let now = 1_000
    const storage = memoryStorage()
    const first = createReplayStore(storage, 'replay_', 50, () => now)
    await first.persist('extractor_resolve', 'uuid-old')
    now = 2_000
    const second = createReplayStore(storage, 'replay_', 50, () => now)
    await second.persist('extractor_resolve', 'uuid-new')
    expect(storage.data.has('replay_uuid-old')).toBe(false)
    expect(storage.data.has('replay_uuid-new')).toBe(true)
  })
})
