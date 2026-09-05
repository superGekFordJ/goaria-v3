import { ref } from 'vue'
import {
  ExtractorOperationResult,
  ExtractorState,
} from '../../../bindings/goaria-v3/internal/wailsapp/models'
import {
  GetExtractorState,
  LoadExtractorPackDirectory,
  LoadExtractorPackFile,
  LoadExtractorPackURL,
  ReloadExtractorSource,
  RemoveExtractorSource,
} from '../../../bindings/goaria-v3/internal/wailsapp/app'

const ERROR_CODE_TO_I18N_KEY: Record<string, string> = {
  unavailable: 'extractor.errors.unavailable',
  invalid_source_kind: 'extractor.errors.invalidSourceKind',
  invalid_source_spec: 'extractor.errors.invalidSourceSpec',
  invalid_source_id: 'extractor.errors.invalidSourceId',
  source_limit_reached: 'extractor.errors.sourceLimitReached',
  source_unreadable: 'extractor.errors.sourceUnreadable',
  source_shape_invalid: 'extractor.errors.sourceShapeInvalid',
  lock_missing: 'extractor.errors.lockMissing',
  lock_invalid: 'extractor.errors.lockInvalid',
  hash_mismatch: 'extractor.errors.hashMismatch',
  signature_invalid: 'extractor.errors.signatureInvalid',
  manifest_invalid: 'extractor.errors.manifestInvalid',
  wasm_invalid: 'extractor.errors.wasmInvalid',
  remote_denied: 'extractor.errors.remoteDenied',
  remote_failed: 'extractor.errors.remoteFailed',
  pack_id_conflict: 'extractor.errors.packIdConflict',
  signer_changed: 'extractor.errors.signerChanged',
  pack_identity_changed: 'extractor.errors.packIdentityChanged',
  policy_unavailable: 'extractor.errors.policyUnavailable',
  auth_runtime_unavailable: 'extractor.errors.authRuntimeUnavailable',
  persist_failed: 'extractor.errors.persistFailed',
  concurrent_change: 'extractor.errors.concurrentChange',
  state_invalid: 'extractor.errors.stateInvalid',
  generic: 'extractor.errors.generic',
}

export function mapErrorCodeToI18nKey(errorCode?: string): string {
  if (errorCode === 'cancelled') return ''
  if (!errorCode) return 'extractor.errors.generic'
  return ERROR_CODE_TO_I18N_KEY[errorCode] || 'extractor.errors.generic'
}

export function useExtractorState() {
  const state = ref<ExtractorState>(
    new ExtractorState({
      available: false,
      sources: [],
      recovery_errors: [],
    }),
  )

  const loading = ref(false)
  const busy = ref(false)
  const error = ref<string | null>(null)
  const remoteUrl = ref('')

  let disposed = false

  const dispose = () => {
    disposed = true
  }

  const loadInitialState = async () => {
    if (disposed) return
    loading.value = true
    error.value = null
    try {
      const res = await GetExtractorState()
      if (!disposed) {
        state.value = res
      }
    } catch {
      if (!disposed) {
        error.value = 'extractor.errors.ipcError'
      }
    } finally {
      if (!disposed) {
        loading.value = false
      }
    }
  }

  const executeMutation = async (
    mutationFn: () => Promise<ExtractorOperationResult>,
    onSuccess?: () => void,
  ) => {
    if (busy.value || disposed) return
    busy.value = true
    error.value = null

    try {
      const result = await mutationFn()
      if (disposed) return

      state.value = result.state
      if (result.cancelled) {
        error.value = null
      } else if (result.success) {
        error.value = null
        onSuccess?.()
      } else {
        error.value = mapErrorCodeToI18nKey(result.error_code)
      }
    } catch {
      if (disposed) return
      error.value = 'extractor.errors.ipcError'
      try {
        const reconciled = await GetExtractorState()
        if (!disposed) {
          state.value = reconciled
        }
      } catch {
        // Silently swallow reconciliation error
      }
    } finally {
      if (!disposed) {
        busy.value = false
      }
    }
  }

  const loadPackFile = async () => {
    await executeMutation(() => LoadExtractorPackFile())
  }

  const loadPackDirectory = async () => {
    await executeMutation(() => LoadExtractorPackDirectory())
  }

  const loadPackURL = async () => {
    const trimmed = remoteUrl.value.trim()
    if (!trimmed) return
    await executeMutation(
      () => LoadExtractorPackURL(trimmed),
      () => {
        remoteUrl.value = ''
      },
    )
  }

  const reloadSource = async (sourceId: string) => {
    if (!sourceId) return
    await executeMutation(() => ReloadExtractorSource(sourceId))
  }

  const removeSource = async (sourceId: string) => {
    if (!sourceId) return
    await executeMutation(() => RemoveExtractorSource(sourceId))
  }

  return {
    state,
    loading,
    busy,
    error,
    remoteUrl,
    loadInitialState,
    loadPackFile,
    loadPackDirectory,
    loadPackURL,
    reloadSource,
    removeSource,
    dispose,
  }
}
