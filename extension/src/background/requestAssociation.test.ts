import { describe, expect, it } from 'vitest'
import { createPendingMap } from './requestAssociation'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (err: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('requestAssociation', () => {
  it('matches download_ack by request_id when present', async () => {
    const map = createPendingMap<string>()
    const a = deferred<string>()
    const b = deferred<string>()
    map.add({ id: 'a', kind: 'download', resolve: a.resolve, reject: a.reject })
    map.add({ id: 'b', kind: 'download', resolve: b.resolve, reject: b.reject })

    const routed = map.routeMessage({ type: 'download_ack', request_id: 'b' })
    expect(routed.kind).toBe('download_ack')
    if (routed.kind === 'download_ack') routed.entry.resolve('ok-b')

    await expect(b.promise).resolves.toBe('ok-b')
    expect(map.size()).toBe(1)
  })

  it('falls back to FIFO when download_ack omits request_id', async () => {
    const map = createPendingMap<string>()
    const first = deferred<string>()
    const second = deferred<string>()
    map.add({ id: '1', kind: 'download', resolve: first.resolve, reject: first.reject })
    map.add({ id: '2', kind: 'download', resolve: second.resolve, reject: second.reject })

    const routed = map.routeMessage({ type: 'download_ack' })
    expect(routed.kind).toBe('download_ack')
    if (routed.kind === 'download_ack') routed.entry.resolve('fifo')

    await expect(first.promise).resolves.toBe('fifo')
    expect(map.size()).toBe(1)
  })

  it('does not steal FIFO when protocol_error carries another id', async () => {
    const map = createPendingMap<string>()
    const download = deferred<string>()
    const other = deferred<string>()
    map.add({ id: 'dl-1', kind: 'download', resolve: download.resolve, reject: download.reject })
    map.add({ id: 'err-a', kind: 'rpc', resolve: other.resolve, reject: other.reject })

    const routed = map.routeMessage({
      type: 'protocol_error',
      request_id: 'err-a',
      error_code: 'unsupported',
    })
    expect(routed.kind).toBe('protocol_error')
    if (routed.kind === 'protocol_error') {
      routed.entry.reject(new Error('unsupported'))
    }

    await expect(other.promise).rejects.toThrow('unsupported')
    expect(map.size()).toBe(1)

    const fifo = map.routeMessage({ type: 'download_ack' })
    expect(fifo.kind).toBe('download_ack')
    if (fifo.kind === 'download_ack') fifo.entry.resolve('download-ok')
    await expect(download.promise).resolves.toBe('download-ok')
  })

  it('ignores protocol_error without request_id so FIFO stays intact', () => {
    const map = createPendingMap<string>()
    const download = deferred<string>()
    map.add({ id: 'dl-1', kind: 'download', resolve: download.resolve, reject: download.reject })
    const routed = map.routeMessage({ type: 'protocol_error', error_code: 'unsupported' })
    expect(routed.kind).toBe('ignored')
    expect(map.size()).toBe(1)
  })

  it('ignores unknown server types', () => {
    const map = createPendingMap<string>()
    const download = deferred<string>()
    map.add({ id: 'dl-1', kind: 'download', resolve: download.resolve, reject: download.reject })
    expect(map.routeMessage({ type: 'capability_update' }).kind).toBe('ignored')
    expect(map.size()).toBe(1)
  })

  it('does not FIFO-steal an rpc pending on download_ack with unknown request_id', () => {
    const map = createPendingMap<string>()
    const rpc = deferred<string>()
    const download = deferred<string>()
    map.add({ id: 'rpc-1', kind: 'rpc', resolve: rpc.resolve, reject: rpc.reject })
    map.add({ id: 'dl-1', kind: 'download', resolve: download.resolve, reject: download.reject })

    const unknown = map.routeMessage({ type: 'download_ack', request_id: 'missing' })
    expect(unknown.kind).toBe('ignored')
    expect(map.size()).toBe(2)

    const fifo = map.routeMessage({ type: 'download_ack' })
    expect(fifo.kind).toBe('download_ack')
    if (fifo.kind === 'download_ack') {
      expect(fifo.entry.id).toBe('dl-1')
    }
    expect(map.size()).toBe(1)
  })

  it('ignores download_ack whose request_id matches a non-download pending', () => {
    const map = createPendingMap<string>()
    const rpc = deferred<string>()
    const download = deferred<string>()
    map.add({ id: 'shared-id', kind: 'rpc', resolve: rpc.resolve, reject: rpc.reject })
    map.add({ id: 'dl-1', kind: 'download', resolve: download.resolve, reject: download.reject })

    const routed = map.routeMessage({ type: 'download_ack', request_id: 'shared-id' })
    expect(routed.kind).toBe('ignored')
    expect(map.size()).toBe(2)
  })

  it('refuses a second pending entry with the same id', async () => {
    const map = createPendingMap<string>()
    const first = deferred<string>()
    const second = deferred<string>()
    expect(map.add({ id: 'dup', kind: 'rpc', resolve: first.resolve, reject: first.reject })).toBe(
      true,
    )
    expect(
      map.add({ id: 'dup', kind: 'rpc', resolve: second.resolve, reject: second.reject }),
    ).toBe(false)
    expect(map.size()).toBe(1)
    expect(map.has('dup')).toBe(true)

    const routed = map.routeMessage({ type: 'extractor_resolve_ack', request_id: 'dup' })
    expect(routed.kind).toBe('typed_ack')
    if (routed.kind === 'typed_ack') routed.entry.resolve('first')
    await expect(first.promise).resolves.toBe('first')
    expect(map.size()).toBe(0)
  })

  it('ignores extractor ack whose request_id matches a download pending', () => {
    const map = createPendingMap<string>()
    const download = deferred<string>()
    map.add({
      id: 'shared-id',
      kind: 'download',
      resolve: download.resolve,
      reject: download.reject,
    })
    const routed = map.routeMessage({ type: 'extractor_resolve_ack', request_id: 'shared-id' })
    expect(routed.kind).toBe('ignored')
    expect(map.size()).toBe(1)
  })

  it('routes download_batch_ack by exact request_id for direct_batch pending', async () => {
    const map = createPendingMap<string>()
    const batch = deferred<string>()
    const download = deferred<string>()
    map.add({ id: 'batch-id', kind: 'direct_batch', resolve: batch.resolve, reject: batch.reject })
    map.add({ id: 'dl-1', kind: 'download', resolve: download.resolve, reject: download.reject })

    const routed = map.routeMessage({ type: 'download_batch_ack', request_id: 'batch-id' })
    expect(routed.kind).toBe('typed_ack')
    if (routed.kind === 'typed_ack') routed.entry.resolve('batch-ok')
    await expect(batch.promise).resolves.toBe('batch-ok')
    expect(map.size()).toBe(1)

    const fifo = map.routeMessage({ type: 'download_ack' })
    expect(fifo.kind).toBe('download_ack')
    if (fifo.kind === 'download_ack') fifo.entry.resolve('download-ok')
    await expect(download.promise).resolves.toBe('download-ok')
  })

  it('routes download_batch_status_ack by exact request_id', async () => {
    const map = createPendingMap<string>()
    const status = deferred<string>()
    map.add({
      id: 'batch-id',
      kind: 'direct_batch_status',
      resolve: status.resolve,
      reject: status.reject,
    })
    const routed = map.routeMessage({
      type: 'download_batch_status_ack',
      request_id: 'batch-id',
      status: 'pending',
    })
    expect(routed.kind).toBe('typed_ack')
    if (routed.kind === 'typed_ack') routed.entry.resolve('status-ok')
    await expect(status.promise).resolves.toBe('status-ok')
  })

  it('does not complete a status wait with a late download_batch_ack', async () => {
    const map = createPendingMap<string>()
    const status = deferred<string>()
    map.add({
      id: 'batch-id',
      kind: 'direct_batch_status',
      resolve: status.resolve,
      reject: status.reject,
    })
    const late = map.routeMessage({
      type: 'download_batch_ack',
      request_id: 'batch-id',
      success: true,
    })
    expect(late.kind).toBe('ignored')
    expect(map.size()).toBe(1)

    const routed = map.routeMessage({
      type: 'download_batch_status_ack',
      request_id: 'batch-id',
      status: 'complete',
    })
    expect(routed.kind).toBe('typed_ack')
    if (routed.kind === 'typed_ack') routed.entry.resolve('status-ok')
    await expect(status.promise).resolves.toBe('status-ok')
  })

  it('ignores download_ack whose request_id matches a direct_batch pending', () => {
    const map = createPendingMap<string>()
    const batch = deferred<string>()
    map.add({
      id: 'shared-id',
      kind: 'direct_batch',
      resolve: batch.resolve,
      reject: batch.reject,
    })
    const routed = map.routeMessage({ type: 'download_ack', request_id: 'shared-id' })
    expect(routed.kind).toBe('ignored')
    expect(map.size()).toBe(1)
  })

  it('does not FIFO-steal a direct_batch pending on download_ack without id', () => {
    const map = createPendingMap<string>()
    const batch = deferred<string>()
    const download = deferred<string>()
    map.add({ id: 'batch-id', kind: 'direct_batch', resolve: batch.resolve, reject: batch.reject })
    map.add({ id: 'dl-1', kind: 'download', resolve: download.resolve, reject: download.reject })

    const fifo = map.routeMessage({ type: 'download_ack' })
    expect(fifo.kind).toBe('download_ack')
    if (fifo.kind === 'download_ack') {
      expect(fifo.entry.id).toBe('dl-1')
    }
    expect(map.size()).toBe(1)
    expect(map.has('batch-id')).toBe(true)
  })

  it('ignores download_batch_ack whose request_id matches a download pending', () => {
    const map = createPendingMap<string>()
    const download = deferred<string>()
    map.add({
      id: 'shared-id',
      kind: 'download',
      resolve: download.resolve,
      reject: download.reject,
    })
    const routed = map.routeMessage({ type: 'download_batch_ack', request_id: 'shared-id' })
    expect(routed.kind).toBe('ignored')
    expect(map.size()).toBe(1)
  })
})
