import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useExtractorState } from './useExtractorState'
import {
  ExtractorOperationResult,
  ExtractorSource,
  ExtractorState,
} from '../../../bindings/goaria-v3/internal/wailsapp/models'
import * as AppBindings from '../../../bindings/goaria-v3/internal/wailsapp/app'

// Mock App bindings
vi.mock('../../../bindings/goaria-v3/internal/wailsapp/app', () => ({
  GetExtractorState: vi.fn(),
  LoadExtractorPackFile: vi.fn(),
  LoadExtractorPackDirectory: vi.fn(),
  LoadExtractorPackURL: vi.fn(),
  ReloadExtractorSource: vi.fn(),
  RemoveExtractorSource: vi.fn(),
}))

function createSampleState(sources: ExtractorSource[] = [], recoveryErrors: string[] = []): ExtractorState {
  return new ExtractorState({
    available: true,
    sources,
    recovery_errors: recoveryErrors,
  })
}

function createSampleSource(id: string, name: string, status = 'ready', errorCode = ''): ExtractorSource {
  return new ExtractorSource({
    source_id: id,
    kind: 'local_zip',
    display_name: name,
    pack_id: 'com.example.pack',
    pack_version: '1.0.0',
    signer_fingerprint: 'abcdef123456',
    status,
    error_code: errorCode,
  })
}

describe('useExtractorState', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('performs one initial GetExtractorState read on load', async () => {
    const initialState = createSampleState([createSampleSource('s1', 'Pack 1')])
    vi.mocked(AppBindings.GetExtractorState).mockResolvedValue(initialState)

    const { state, loading, loadInitialState } = useExtractorState()
    expect(loading.value).toBe(false)
    expect(state.value.sources).toHaveLength(0)

    const promise = loadInitialState()
    expect(loading.value).toBe(true)
    await promise

    expect(loading.value).toBe(false)
    expect(AppBindings.GetExtractorState).toHaveBeenCalledTimes(1)
    expect(state.value.sources).toHaveLength(1)
    expect(state.value.sources[0].display_name).toBe('Pack 1')
  })

  it('handles transport failure on initial mount without exposing raw error', async () => {
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(AppBindings.GetExtractorState).mockRejectedValue(new Error('SensitiveHostPathError: C:\\secret\\path'))

    const { state, loading, error, loadInitialState } = useExtractorState()
    await loadInitialState()

    expect(loading.value).toBe(false)
    expect(error.value).toBe('extractor.errors.ipcError')
    expect(state.value.sources).toHaveLength(0)
    // Must not leak raw path or error string
    expect(error.value).not.toContain('C:\\secret\\path')
    consoleSpy.mockRestore()
  })

  it('calls zero-argument local picker bindings and replaces state canonically', async () => {
    const initial = createSampleState()
    const updated = createSampleState([createSampleSource('s1', 'Local Pack')])
    vi.mocked(AppBindings.GetExtractorState).mockResolvedValue(initial)
    vi.mocked(AppBindings.LoadExtractorPackFile).mockResolvedValue(
      new ExtractorOperationResult({
        success: true,
        cancelled: false,
        state: updated,
      }),
    )

    const { state, busy, loadPackFile } = useExtractorState()
    await loadPackFile()

    expect(AppBindings.LoadExtractorPackFile).toHaveBeenCalledTimes(1)
    expect(AppBindings.LoadExtractorPackFile).toHaveBeenCalledWith()
    expect(busy.value).toBe(false)
    expect(state.value.sources).toHaveLength(1)
    expect(state.value.sources[0].source_id).toBe('s1')
  })

  it('calls zero-argument directory picker and replaces state canonically', async () => {
    const updated = createSampleState([createSampleSource('dir1', 'Unpacked Pack')])
    vi.mocked(AppBindings.LoadExtractorPackDirectory).mockResolvedValue(
      new ExtractorOperationResult({
        success: true,
        cancelled: false,
        state: updated,
      }),
    )

    const { state, loadPackDirectory } = useExtractorState()
    await loadPackDirectory()

    expect(AppBindings.LoadExtractorPackDirectory).toHaveBeenCalledTimes(1)
    expect(AppBindings.LoadExtractorPackDirectory).toHaveBeenCalledWith()
    expect(state.value.sources).toHaveLength(1)
    expect(state.value.sources[0].source_id).toBe('dir1')
  })

  it('passes query-bearing lock URL without modification and clears input on success', async () => {
    const testUrl = '  https://repo.example.com/extractor.lock.json?sig=token123&session=secret  '
    const cleanUrl = 'https://repo.example.com/extractor.lock.json?sig=token123&session=secret'
    const updated = createSampleState([createSampleSource('remote1', 'Remote Pack')])

    vi.mocked(AppBindings.LoadExtractorPackURL).mockResolvedValue(
      new ExtractorOperationResult({
        success: true,
        cancelled: false,
        state: updated,
      }),
    )

    const { state, remoteUrl, loadPackURL, error } = useExtractorState()
    remoteUrl.value = testUrl

    await loadPackURL()

    expect(AppBindings.LoadExtractorPackURL).toHaveBeenCalledTimes(1)
    expect(AppBindings.LoadExtractorPackURL).toHaveBeenCalledWith(cleanUrl)
    expect(remoteUrl.value).toBe('')
    expect(error.value).toBeNull()
    expect(state.value.sources).toHaveLength(1)
  })

  it('retains remoteUrl input on validation or mutation failure', async () => {
    const testUrl = 'https://repo.example.com/extractor.lock.json?sig=token123'
    const initial = createSampleState()

    vi.mocked(AppBindings.LoadExtractorPackURL).mockResolvedValue(
      new ExtractorOperationResult({
        success: false,
        cancelled: false,
        error_code: 'remote_denied',
        state: initial,
      }),
    )

    const { remoteUrl, loadPackURL, error } = useExtractorState()
    remoteUrl.value = testUrl

    await loadPackURL()

    expect(remoteUrl.value).toBe(testUrl)
    expect(error.value).toBe('extractor.errors.remoteDenied')
    expect(error.value).not.toContain('token123')
  })

  it('sends only source_id for Reload and Remove', async () => {
    const s1 = createSampleSource('s1', 'My Pack')
    const initial = createSampleState([s1])
    const empty = createSampleState([])

    vi.mocked(AppBindings.ReloadExtractorSource).mockResolvedValue(
      new ExtractorOperationResult({ success: true, cancelled: false, state: initial }),
    )
    vi.mocked(AppBindings.RemoveExtractorSource).mockResolvedValue(
      new ExtractorOperationResult({ success: true, cancelled: false, state: empty }),
    )

    const { state, reloadSource, removeSource } = useExtractorState()
    state.value = initial

    await reloadSource('s1')
    expect(AppBindings.ReloadExtractorSource).toHaveBeenCalledWith('s1')

    await removeSource('s1')
    expect(AppBindings.RemoveExtractorSource).toHaveBeenCalledWith('s1')
    expect(state.value.sources).toHaveLength(0)
  })

  it('enforces a single busy guard and rejects concurrent actions', async () => {
    let resolveFirst: (res: ExtractorOperationResult) => void
    const firstPromise = new Promise<ExtractorOperationResult>(r => {
      resolveFirst = r
    })
    vi.mocked(AppBindings.LoadExtractorPackFile).mockReturnValue(
      firstPromise as unknown as ReturnType<typeof AppBindings.LoadExtractorPackFile>,
    )

    const { busy, loadPackFile, removeSource } = useExtractorState()

    const p1 = loadPackFile()
    expect(busy.value).toBe(true)

    // Attempt second mutation while first is busy
    const p2 = removeSource('s1')
    await p2

    expect(AppBindings.RemoveExtractorSource).not.toHaveBeenCalled()

    resolveFirst!(
      new ExtractorOperationResult({
        success: true,
        cancelled: false,
        state: createSampleState(),
      }),
    )
    await p1
    expect(busy.value).toBe(false)
  })

  it('treats cancellation quietly with canonical state applied and no error', async () => {
    const oldState = createSampleState([createSampleSource('s1', 'Old')])
    vi.mocked(AppBindings.LoadExtractorPackFile).mockResolvedValue(
      new ExtractorOperationResult({
        success: false,
        cancelled: true,
        state: oldState,
      }),
    )

    const { state, busy, error, loadPackFile } = useExtractorState()
    await loadPackFile()

    expect(busy.value).toBe(false)
    expect(error.value).toBeNull()
    expect(state.value.sources).toHaveLength(1)
  })

  it('maps known error codes and falls back to generic key for unknown codes', async () => {
    const initial = createSampleState()

    vi.mocked(AppBindings.ReloadExtractorSource).mockResolvedValue(
      new ExtractorOperationResult({
        success: false,
        cancelled: false,
        error_code: 'signer_changed',
        state: initial,
      }),
    )

    const { error, reloadSource } = useExtractorState()
    await reloadSource('s1')
    expect(error.value).toBe('extractor.errors.signerChanged')

    vi.mocked(AppBindings.ReloadExtractorSource).mockResolvedValue(
      new ExtractorOperationResult({
        success: false,
        cancelled: false,
        error_code: 'completely_unknown_code',
        state: initial,
      }),
    )

    await reloadSource('s1')
    expect(error.value).toBe('extractor.errors.generic')
  })

  it('handles mutation transport failure and performs one-shot reconciliation', async () => {
    const reconciledState = createSampleState([createSampleSource('reconciled', 'Reconciled Pack')])
    vi.mocked(AppBindings.ReloadExtractorSource).mockRejectedValue(new Error('Connection dropped'))
    vi.mocked(AppBindings.GetExtractorState).mockResolvedValue(reconciledState)

    const { state, error, busy, reloadSource } = useExtractorState()
    await reloadSource('s1')

    expect(busy.value).toBe(false)
    expect(error.value).toBe('extractor.errors.ipcError')
    expect(AppBindings.GetExtractorState).toHaveBeenCalledTimes(1)
    expect(state.value.sources).toHaveLength(1)
    expect(state.value.sources[0].source_id).toBe('reconciled')
  })

  it('does not mutate disposed state when promises resolve late after unmount', async () => {
    let resolveLate: (res: ExtractorOperationResult) => void
    const latePromise = new Promise<ExtractorOperationResult>(r => {
      resolveLate = r
    })
    vi.mocked(AppBindings.LoadExtractorPackFile).mockReturnValue(
      latePromise as unknown as ReturnType<typeof AppBindings.LoadExtractorPackFile>,
    )

    const { state, busy, loadPackFile, dispose } = useExtractorState()
    const initialSources = state.value.sources

    const p = loadPackFile()
    expect(busy.value).toBe(true)

    // Component unmounts
    dispose()

    // Promise finishes after unmount
    resolveLate!(
      new ExtractorOperationResult({
        success: true,
        cancelled: false,
        state: createSampleState([createSampleSource('late', 'Late Pack')]),
      }),
    )
    await p

    // State must remain unmutated by the late resolution
    expect(state.value.sources).toBe(initialSources)
  })

  it('does not patch sources optimistically on Remove', async () => {
    const s1 = createSampleSource('s1', 'Pack 1')
    const initial = createSampleState([s1])

    let resolveRemove: (res: ExtractorOperationResult) => void
    const removePromise = new Promise<ExtractorOperationResult>(r => {
      resolveRemove = r
    })
    vi.mocked(AppBindings.RemoveExtractorSource).mockReturnValue(
      removePromise as unknown as ReturnType<typeof AppBindings.RemoveExtractorSource>,
    )

    const { state, removeSource } = useExtractorState()
    state.value = initial

    const p = removeSource('s1')
    // During execution, row must NOT be deleted optimistically
    expect(state.value.sources).toHaveLength(1)

    // Resolve with backend state (empty)
    resolveRemove!(
      new ExtractorOperationResult({
        success: true,
        cancelled: false,
        state: createSampleState([]),
      }),
    )
    await p

    expect(state.value.sources).toHaveLength(0)
  })
})
