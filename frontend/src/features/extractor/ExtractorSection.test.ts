import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'
import ExtractorSection from './ExtractorSection.vue'
import {
  ExtractorSource,
  ExtractorState,
} from '../../../bindings/goaria-v3/internal/wailsapp/models'

// Mock vue-i18n
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
  }),
}))

const mockState = ref(
  new ExtractorState({
    available: true,
    sources: [],
    recovery_errors: [],
  }),
)
const mockLoading = ref(false)
const mockBusy = ref(false)
const mockError = ref<string | null>(null)
const mockRemoteUrl = ref('')

const mockFns = {
  loadInitialState: vi.fn(),
  loadPackFile: vi.fn(),
  loadPackDirectory: vi.fn(),
  loadPackURL: vi.fn(),
  reloadSource: vi.fn(),
  removeSource: vi.fn(),
  dispose: vi.fn(),
}

vi.mock('./useExtractorState', () => ({
  useExtractorState: () => ({
    state: mockState,
    loading: mockLoading,
    busy: mockBusy,
    error: mockError,
    remoteUrl: mockRemoteUrl,
    ...mockFns,
  }),
  mapErrorCodeToI18nKey: (code?: string) => (code ? `extractor.errors.${code}` : 'extractor.errors.generic'),
}))

function createSource(id: string, name: string, status = 'ready', errorCode = ''): ExtractorSource {
  return new ExtractorSource({
    source_id: id,
    kind: 'local_zip',
    display_name: name,
    pack_id: `com.example.${id}`,
    pack_version: '1.2.3',
    signer_fingerprint: 'abcdef1234567890abcdef',
    status,
    error_code: errorCode,
  })
}

describe('ExtractorSection.vue', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockState.value = new ExtractorState({
      available: true,
      sources: [],
      recovery_errors: [],
    })
    mockLoading.value = false
    mockBusy.value = false
    mockError.value = null
    mockRemoteUrl.value = ''
  })

  it('calls loadInitialState on mount and dispose on unmount', () => {
    const wrapper = mount(ExtractorSection)
    expect(mockFns.loadInitialState).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    expect(mockFns.dispose).toHaveBeenCalledTimes(1)
  })

  it('renders three equal Load actions and empty state', () => {
    const wrapper = mount(ExtractorSection)

    expect(wrapper.find('[data-testid="load-zip-btn"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="load-directory-btn"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="load-url-btn"]').exists()).toBe(true)
    const urlInput = wrapper.find('[data-testid="url-input"]')
    expect(urlInput.exists()).toBe(true)
    expect(urlInput.attributes('aria-label')).toBe('extractor.urlInput.label')
    expect(urlInput.attributes('id')).toBe('extractor-url-input')
    expect(urlInput.attributes('aria-describedby')).toBe('extractor-url-help')

    const label = wrapper.find('label[for="extractor-url-input"]')
    expect(label.exists()).toBe(true)
    expect(label.text()).toBe('extractor.urlInput.label')

    const help = wrapper.find('#extractor-url-help')
    expect(help.exists()).toBe(true)
    expect(help.text()).toBe('extractor.urlInput.help')

    expect(wrapper.find('[data-testid="empty-state"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="empty-state"]').text()).toContain('extractor.source.empty')
  })

  it('shows loading state and retry button on load failure', async () => {
    mockLoading.value = true
    const wrapper = mount(ExtractorSection)

    expect(wrapper.find('[data-testid="loading-indicator"]').exists()).toBe(true)

    mockLoading.value = false
    mockError.value = 'extractor.errors.ipcError'
    await nextTick()

    const errorSpan = wrapper.find('[data-testid="error-notice"] span')
    expect(errorSpan.classes()).toContain('break-words')
    expect(errorSpan.attributes('title')).toBe('extractor.errors.ipcError')

    const retryBtn = wrapper.find('[data-testid="retry-btn"]')
    expect(retryBtn.exists()).toBe(true)
    await retryBtn.trigger('click')
    expect(mockFns.loadInitialState).toHaveBeenCalledTimes(2) // 1 mount + 1 click
  })

  it('renders sources in backend order with safe fields, short fingerprint, and status light', () => {
    const s1 = createSource('s1', 'First Pack')
    const s2 = createSource('s2', 'Second Pack', 'unavailable', 'signer_changed')
    mockState.value = new ExtractorState({
      available: true,
      sources: [s1, s2],
      recovery_errors: [],
    })

    const wrapper = mount(ExtractorSection)
    const rows = wrapper.findAll('[data-testid="source-row"]')
    expect(rows).toHaveLength(2)

    // Order preserved
    expect(rows[0].text()).toContain('First Pack')
    expect(rows[0].text()).toContain('com.example.s1')
    expect(rows[0].text()).toContain('1.2.3')
    // Shortened fingerprint (12 chars)
    expect(rows[0].text()).toContain('abcdef123456')
    expect(rows[0].find('[data-testid="status-light-ready"]').exists()).toBe(true)
    expect(rows[0].find('[data-testid="status-light-ready"] .sr-only').text()).toBe('extractor.source.status.ready')

    expect(rows[1].text()).toContain('Second Pack')
    expect(rows[1].find('[data-testid="status-light-unavailable"]').exists()).toBe(true)
    expect(rows[1].find('[data-testid="status-light-unavailable"] .sr-only').text()).toBe('extractor.source.status.unavailable')
    expect(rows[1].text()).toContain('extractor.errors.signer_changed')
  })

  it('wires Reload and Remove buttons with exact source IDs', async () => {
    mockState.value = new ExtractorState({
      available: true,
      sources: [createSource('s1', 'Pack 1')],
      recovery_errors: [],
    })

    const wrapper = mount(ExtractorSection)
    const reloadBtn = wrapper.find('[data-testid="reload-btn-s1"]')
    const removeBtn = wrapper.find('[data-testid="remove-btn-s1"]')

    // Accessibility attributes
    expect(reloadBtn.attributes('aria-label')).toBe('extractor.actions.reload')
    expect(removeBtn.attributes('aria-label')).toBe('extractor.actions.remove')

    await reloadBtn.trigger('click')
    expect(mockFns.reloadSource).toHaveBeenCalledWith('s1')

    await removeBtn.trigger('click')
    expect(mockFns.removeSource).toHaveBeenCalledWith('s1')
  })

  it('disables mutation actions when busy', () => {
    mockBusy.value = true
    mockState.value = new ExtractorState({
      available: true,
      sources: [createSource('s1', 'Pack 1')],
      recovery_errors: [],
    })

    const wrapper = mount(ExtractorSection)
    expect(wrapper.find('[data-testid="load-zip-btn"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="load-directory-btn"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="load-url-btn"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="reload-btn-s1"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="remove-btn-s1"]').attributes('disabled')).toBeDefined()
  })

  it('shows recovery warning and deduplicated mapped recovery errors', () => {
    mockState.value = new ExtractorState({
      available: true,
      sources: [],
      recovery_errors: ['source_unreadable', 'source_unreadable', 'lock_invalid'],
    })

    const wrapper = mount(ExtractorSection)
    const warning = wrapper.find('[data-testid="recovery-warning"]')
    expect(warning.exists()).toBe(true)
    expect(warning.text()).toContain('extractor.notices.recoveryWarning')

    const items = wrapper.findAll('[data-testid="recovery-error-item"]')
    expect(items).toHaveLength(2)
    expect(items[0].text()).toContain('extractor.errors.source_unreadable')
    expect(items[1].text()).toContain('extractor.errors.lock_invalid')
  })

  it('falls back to generic error when unavailable source has empty error code', () => {
    const s1 = createSource('s1', 'Broken Pack', 'unavailable', '')
    mockState.value = new ExtractorState({
      available: true,
      sources: [s1],
      recovery_errors: [],
    })

    const wrapper = mount(ExtractorSection)
    const row = wrapper.find('[data-testid="source-row"]')
    expect(row.text()).toContain('extractor.errors.generic')
  })

  it('shows unavailable banner and disables all actions when available is false', async () => {
    mockState.value = new ExtractorState({
      available: false,
      sources: [createSource('s1', 'Pack 1')],
      recovery_errors: [],
    })

    const wrapper = mount(ExtractorSection)
    expect(wrapper.find('[data-testid="unavailable-banner"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="load-zip-btn"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="load-directory-btn"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="load-url-btn"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="reload-btn-s1"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="remove-btn-s1"]').attributes('disabled')).toBeDefined()

    // When error is present, unavailable banner is omitted to avoid dual-banner conflict
    mockError.value = 'extractor.errors.ipcError'
    await nextTick()
    expect(wrapper.find('[data-testid="unavailable-banner"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="error-notice"]').exists()).toBe(true)
  })

  it('ensures sensitive markers never leak outside the active URL input', () => {
    const sensitiveQuery = '?token=supersecrettoken123&session=privatesession'
    mockRemoteUrl.value = `https://example.com/pack.lock.json${sensitiveQuery}`
    mockError.value = 'extractor.errors.signerChanged'

    const wrapper = mount(ExtractorSection)

    // The query string is only in the input element's value attribute
    const input = wrapper.find('[data-testid="url-input"]').element as HTMLInputElement
    expect(input.value).toContain(sensitiveQuery)

    // In all other text content of the component, neither sensitive query nor raw error text appears
    const allText = wrapper.text()
    expect(allText).not.toContain('supersecrettoken123')
    expect(allText).not.toContain('privatesession')
    expect(allText).not.toContain('C:\\')
    expect(allText).not.toContain('/etc/')
  })
})
