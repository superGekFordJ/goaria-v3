import { describe, it, expect, beforeEach } from 'vitest'
import { levelToTier } from '../ui'

describe('levelToTier', () => {
  it.each([
    [0, 'reduced'],
    [30, 'reduced'],
    [31, 'balanced'],
    [50, 'balanced'],
    [70, 'balanced'],
    [71, 'full'],
    [100, 'full'],
  ])('%i -> %s', (level, tier) => {
    expect(levelToTier(level)).toBe(tier)
  })

  it('handles out-of-range values', () => {
    expect(levelToTier(-5)).toBe('reduced')
    expect(levelToTier(150)).toBe('full')
  })
})

describe('uiStore applyEffects', () => {
  beforeEach(() => {
    document.documentElement.removeAttribute('data-effects')
    document.documentElement.style.removeProperty('--glass-blur')
    document.documentElement.style.removeProperty('--glass-opacity')
    document.documentElement.style.removeProperty('--ui-effects-level')
    localStorage.clear()
  })

  it('sets CSS variables and data-effects attribute on setEffectsLevel', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setEffectsLevel(75)

    expect(document.documentElement.getAttribute('data-effects')).toBe('full')
    expect(document.documentElement.style.getPropertyValue('--ui-effects-level')).toBe('75')
    expect(document.documentElement.style.getPropertyValue('--glass-blur')).toBe(
      `${Math.max(2, 24 - (75 / 100) * 22)}px`,
    )
    expect(document.documentElement.style.getPropertyValue('--glass-opacity')).toBe(
      String(0.08 + (75 / 100) * 0.32),
    )
  })

  it('clamps effectsLevel to 0-100', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setEffectsLevel(200)
    expect(store.effectsLevel).toBe(100)

    store.setEffectsLevel(-50)
    expect(store.effectsLevel).toBe(0)
  })

  it('migrates legacy persisted effects:full to effectsLevel 100', async () => {
    localStorage.setItem('ui', JSON.stringify({ effects: 'full' }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.effectsLevel).toBe(100)
  })

  it('migrates legacy persisted effects:reduced to effectsLevel 0', async () => {
    localStorage.setItem('ui', JSON.stringify({ effects: 'reduced' }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.effectsLevel).toBe(0)
  })

  it('defaults to effectsLevel 50 when no legacy data', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.effectsLevel).toBe(50)
  })
})
