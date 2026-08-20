import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import TaskCard from './TaskCard.vue'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
import type { TaskGroupHint } from '../../stores/task/grouping'

const quietForbiddenFragments = [
  ['supported', 'site'].join('-'),
  ['supported', 'sites'].join(' '),
  ['market', 'place'].join(''),
  ['plugin', 'browser'].join(' '),
  ['site', 'catalog'].join(' '),
  ['provider', 'list'].join(' '),
]

vi.mock('../../stores/task', () => ({
  useTaskStore: () => ({
    isSelected: () => false,
    toggleSelect: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    openTaskFolder: vi.fn(),
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
  }),
}))

function task(): Task {
  return {
    gid: 'gid-policy',
    status: 'active',
    totalLength: '1000',
    completedLength: '100',
    downloadSpeed: '10',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: '/downloads/policy.bin', uris: [] }],
  } as Task
}

const hint: TaskGroupHint = {
  groupKey: 'dg-policy',
  folderLabel: 'Batch 2026-05-07 dg-policy',
  folderPath: '/downloads/Batch 2026-05-07 dg-policy',
  totalCount: 5,
  visibleCount: 2,
  ordinal: 1,
  isAutoFoldered: true,
}

describe('group UX visual and quiet-support policy', () => {
  it('renders no quiet-support or advertising terms in grouped UX', () => {
    const wrapper = mount(TaskCard, { props: { task: task(), groupHint: hint } })

    const renderedText = wrapper.text().toLowerCase()
    for (const fragment of quietForbiddenFragments) {
      expect(renderedText).not.toContain(fragment)
    }

    wrapper.unmount()
  })

  it('does not add replay-prone animation classes to group chips under reduced effects', () => {
    document.documentElement.setAttribute('data-effects', 'reduced')
    const wrapper = mount(TaskCard, { props: { task: task(), groupHint: hint } })
    const chipClasses = wrapper.find('.task-group-chip').classes().join(' ')

    expect(chipClasses).not.toMatch(/animate|pulse|float|ping|shimmer/i)

    wrapper.unmount()
    document.documentElement.setAttribute('data-effects', 'balanced')
  })

  it('allows animation classes on group chips under balanced effects', () => {
    document.documentElement.setAttribute('data-effects', 'balanced')
    const wrapper = mount(TaskCard, { props: { task: task(), groupHint: hint } })
    const chipClasses = wrapper.find('.task-group-chip').classes().join(' ')

    // balanced should not suppress animations (unlike reduced)
    expect(chipClasses).not.toMatch(/animate-none/)

    wrapper.unmount()
    document.documentElement.setAttribute('data-effects', 'balanced')
  })

  it('keeps group selector CSS free of hardcoded hex or legacy rgb color functions', async () => {
    const cardSource = await import('./TaskCard.vue?raw')
    const headerSource = await import('./TaskHeader.vue?raw')
    const groupCssBlock = [String(cardSource.default), String(headerSource.default)]
      .map(raw => raw.match(/\.task-(?:group-chip|header-group-hint)[\s\S]*?<\/style>/)?.[0] ?? '')
      .join('\n')

    expect(groupCssBlock).not.toMatch(/#[0-9a-f]{3,8}\b/i)
    expect(groupCssBlock).not.toMatch(/(?<!color-mix\(in s)\brgba?\(/i)
  })
})
