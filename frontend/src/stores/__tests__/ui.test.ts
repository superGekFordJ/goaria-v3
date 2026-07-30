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
      (8 - 6 * Math.pow((75 - 70) / 30, 2)).toFixed(2) + 'px',
    )
    expect(document.documentElement.style.getPropertyValue('--glass-opacity')).toBe(
      (0.3 - 0.1 * Math.pow((75 - 70) / 30, 2)).toFixed(4),
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

  it('setEffectsLevel updates live CSS but does not touch effectsLevelPersisted', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setEffectsLevel(10)
    store.setEffectsLevel(20)
    store.setEffectsLevel(80)

    expect(store.effectsLevel).toBe(80)
    expect(store.effectsLevelPersisted).toBe(50)
  })

  it('commitEffectsLevel flushes persisted level immediately', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setEffectsLevel(66)
    expect(store.effectsLevelPersisted).toBe(50)

    store.commitEffectsLevel()
    expect(store.effectsLevelPersisted).toBe(66)

    store.commitEffectsLevel(90)
    expect(store.effectsLevel).toBe(90)
    expect(store.effectsLevelPersisted).toBe(90)
  })

  it('migrates legacy persisted effects:full to effectsLevel 100', async () => {
    localStorage.setItem('ui', JSON.stringify({ effects: 'full' }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.effectsLevel).toBe(100)
    expect(store.effectsLevelPersisted).toBe(100)
  })

  it('migrates legacy persisted effects:reduced to effectsLevel 0', async () => {
    localStorage.setItem('ui', JSON.stringify({ effects: 'reduced' }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.effectsLevel).toBe(0)
    expect(store.effectsLevelPersisted).toBe(0)
  })

  it('hydrates from legacy live effectsLevel key', async () => {
    localStorage.setItem('ui', JSON.stringify({ effectsLevel: 42 }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.effectsLevel).toBe(42)
    expect(store.effectsLevelPersisted).toBe(42)
  })

  it('hydrates from effectsLevelPersisted key', async () => {
    localStorage.setItem('ui', JSON.stringify({ effectsLevelPersisted: 77 }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.effectsLevel).toBe(77)
    expect(store.effectsLevelPersisted).toBe(77)
  })

  it('defaults to effectsLevel 50 when no legacy data', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.effectsLevel).toBe(50)
    expect(store.effectsLevelPersisted).toBe(50)
  })
})
