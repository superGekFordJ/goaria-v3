import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setupTaskDetail } from '../detail'

const bindingMocks = vi.hoisted(() => ({
  GetTaskDetails: vi.fn(),
}))

vi.mock('../../../../bindings/goaria-v3/internal/wailsapp/app.js', () => bindingMocks)

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function envelope(gid: string, detail: Record<string, unknown> = {}) {
  return { [gid]: { found: true, detail: { gid, ...detail } } }
}

describe('setupTaskDetail', () => {
  beforeEach(() => {
    bindingMocks.GetTaskDetails.mockReset()
  })

  it('goes pending then ready with the fetched detail', async () => {
    const d = deferred<unknown>()
    bindingMocks.GetTaskDetails.mockReturnValueOnce(d.promise)
    const { taskDetail, fetchTaskDetail } = setupTaskDetail()

    const p = fetchTaskDetail('ar_1', 'active')
    expect(taskDetail.value).toEqual({ gid: 'ar_1', status: 'active', phase: 'pending' })
    expect(bindingMocks.GetTaskDetails).toHaveBeenCalledWith(['ar_1'])

    d.resolve(envelope('ar_1', { peak_speed: 10 }))
    await p
    expect(taskDetail.value?.phase).toBe('ready')
    expect(taskDetail.value?.found).toBe(true)
    expect(taskDetail.value?.detail?.peak_speed).toBe(10)
  })

  it('drops out-of-order responses', async () => {
    const first = deferred<unknown>()
    const second = deferred<unknown>()
    bindingMocks.GetTaskDetails.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const { taskDetail, fetchTaskDetail } = setupTaskDetail()

    const p1 = fetchTaskDetail('ar_old', 'active')
    const p2 = fetchTaskDetail('ar_new', 'paused')
    second.resolve(envelope('ar_new', { added_at: 2 }))
    await p2
    first.resolve(envelope('ar_old', { added_at: 1 }))
    await p1

    expect(taskDetail.value?.gid).toBe('ar_new')
    expect(taskDetail.value?.detail?.added_at).toBe(2)
  })

  it('treats a gid identity mismatch as failed', async () => {
    bindingMocks.GetTaskDetails.mockResolvedValueOnce({
      ar_1: { found: true, detail: { gid: 'ar_other' } },
    })
    const { taskDetail, fetchTaskDetail } = setupTaskDetail()
    await fetchTaskDetail('ar_1', 'complete')
    expect(taskDetail.value).toEqual({ gid: 'ar_1', status: 'complete', phase: 'failed' })
  })

  it('treats a missing key or found:false as ready not-found', async () => {
    bindingMocks.GetTaskDetails.mockResolvedValueOnce({})
    const { taskDetail, fetchTaskDetail } = setupTaskDetail()
    await fetchTaskDetail('ar_gone', 'complete')
    expect(taskDetail.value).toMatchObject({ phase: 'ready', found: false })

    bindingMocks.GetTaskDetails.mockResolvedValueOnce({ ar_gone: { found: false } })
    await fetchTaskDetail('ar_gone', 'error')
    expect(taskDetail.value).toMatchObject({ status: 'error', phase: 'ready', found: false })
  })

  it('keeps old data when a silent refresh fails', async () => {
    bindingMocks.GetTaskDetails.mockResolvedValueOnce(envelope('ar_1', { peak_speed: 5 }))
    const { taskDetail, fetchTaskDetail } = setupTaskDetail()
    await fetchTaskDetail('ar_1', 'active')

    const d = deferred<unknown>()
    bindingMocks.GetTaskDetails.mockReturnValueOnce(d.promise)
    const p = fetchTaskDetail('ar_1', 'active')
    // Silent: no pending flash while refreshing.
    expect(taskDetail.value?.phase).toBe('ready')
    d.reject(new Error('boom'))
    await p
    expect(taskDetail.value?.phase).toBe('ready')
    expect(taskDetail.value?.detail?.peak_speed).toBe(5)
  })

  it('goes pending first when the status changed', async () => {
    bindingMocks.GetTaskDetails.mockResolvedValueOnce(envelope('ar_1', { peak_speed: 5 }))
    const { taskDetail, fetchTaskDetail } = setupTaskDetail()
    await fetchTaskDetail('ar_1', 'active')

    const d = deferred<unknown>()
    bindingMocks.GetTaskDetails.mockReturnValueOnce(d.promise)
    const p = fetchTaskDetail('ar_1', 'complete')
    expect(taskDetail.value).toEqual({ gid: 'ar_1', status: 'complete', phase: 'pending' })
    d.reject(new Error('boom'))
    await p
    expect(taskDetail.value?.phase).toBe('failed')
  })

  it('ignores blank gids', async () => {
    const { taskDetail, fetchTaskDetail } = setupTaskDetail()
    await fetchTaskDetail('   ', 'active')
    expect(taskDetail.value).toBeNull()
    expect(bindingMocks.GetTaskDetails).not.toHaveBeenCalled()
  })
})
