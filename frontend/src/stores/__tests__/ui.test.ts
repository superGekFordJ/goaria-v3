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
    document.documentElement.removeAttribute('data-effects-glow')
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
    expect(document.documentElement.getAttribute('data-effects-glow')).toBe('static')
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

  it('gates data-effects-glow at level 95 boundary', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setEffectsLevel(94)
    expect(document.documentElement.getAttribute('data-effects-glow')).toBe('static')

    store.setEffectsLevel(95)
    expect(document.documentElement.getAttribute('data-effects-glow')).toBe('breathe')

    store.setEffectsLevel(100)
    expect(document.documentElement.getAttribute('data-effects-glow')).toBe('breathe')

    store.setEffectsLevel(94)
    expect(document.documentElement.getAttribute('data-effects-glow')).toBe('static')

    store.setEffectsLevel(71)
    expect(document.documentElement.getAttribute('data-effects-glow')).toBe('static')
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
    expect(document.documentElement.getAttribute('data-effects-glow')).toBe('breathe')
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
    expect(document.documentElement.getAttribute('data-effects-glow')).toBe('static')
  })
})

describe('uiStore prismHue', () => {
  beforeEach(() => {
    document.documentElement.style.removeProperty('--prism-hue')
    localStorage.clear()
  })

  it('clamps hue to 0-360 and sets --prism-hue CSS var', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setPrismHue(266)
    expect(store.prismHue).toBe(266)
    expect(document.documentElement.style.getPropertyValue('--prism-hue')).toBe('266')

    store.setPrismHue(400)
    expect(store.prismHue).toBe(360)
    store.setPrismHue(-20)
    expect(store.prismHue).toBe(0)
    store.setPrismHue(150.6)
    expect(store.prismHue).toBe(151)
  })

  it('setPrismHue updates live hue without touching prismHuePersisted', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setPrismHue(10)
    store.setPrismHue(120)
    store.setPrismHue(300)

    expect(store.prismHue).toBe(300)
    expect(store.prismHuePersisted).toBe(280)
  })

  it('commitPrismHue flushes persisted hue immediately', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setPrismHue(66)
    expect(store.prismHuePersisted).toBe(280)

    store.commitPrismHue()
    expect(store.prismHuePersisted).toBe(66)

    store.commitPrismHue(180)
    expect(store.prismHue).toBe(180)
    expect(store.prismHuePersisted).toBe(180)
  })

  it('hydrates legacy persisted prismHue key into both refs', async () => {
    localStorage.setItem('ui', JSON.stringify({ prismHue: 266 }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.prismHue).toBe(266)
    expect(store.prismHuePersisted).toBe(266)
    expect(document.documentElement.style.getPropertyValue('--prism-hue')).toBe('266')
  })

  it('prefers prismHuePersisted over legacy prismHue key', async () => {
    localStorage.setItem('ui', JSON.stringify({ prismHue: 100, prismHuePersisted: 200 }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.prismHue).toBe(200)
    expect(store.prismHuePersisted).toBe(200)
  })

  it('defaults to 280 when no persisted hue exists', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.prismHue).toBe(280)
    expect(store.prismHuePersisted).toBe(280)
  })
})

describe('uiStore prismTone', () => {
  beforeEach(() => {
    document.documentElement.style.removeProperty('--prism-c-ink')
    document.documentElement.style.removeProperty('--prism-c-fill')
    document.documentElement.style.removeProperty('--prism-hsl-s')
    localStorage.clear()
  })

  it('defaults to vivid and injects scaled chroma CSS vars', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setTheme('dark')
    expect(store.prismTone).toBe('vivid')
    expect(document.documentElement.style.getPropertyValue('--prism-c-ink')).toBe('0.1700')
    expect(document.documentElement.style.getPropertyValue('--prism-c-fill')).toBe('0.1700')
    expect(document.documentElement.style.getPropertyValue('--prism-hsl-s')).toBe('85%')

    store.setTheme('light')
    expect(document.documentElement.style.getPropertyValue('--prism-c-ink')).toBe('0.1400')
    expect(document.documentElement.style.getPropertyValue('--prism-c-fill')).toBe('0.1900')
  })

  it('setPrismTone rescales chroma vars and rejects unknown tones', async () => {
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.setTheme('light')
    store.setPrismTone('mist')
    expect(store.prismTone).toBe('mist')
    expect(document.documentElement.style.getPropertyValue('--prism-c-ink')).toBe('0.0630')
    expect(document.documentElement.style.getPropertyValue('--prism-c-fill')).toBe('0.0855')
    expect(document.documentElement.style.getPropertyValue('--prism-hsl-s')).toBe('38%')

    store.setPrismTone('neon')
    expect(store.prismTone).toBe('vivid')
    expect(document.documentElement.style.getPropertyValue('--prism-c-fill')).toBe('0.1900')
  })

  it('normalises an invalid persisted tone back to vivid on init', async () => {
    localStorage.setItem('ui', JSON.stringify({ prismTone: 'psychedelic' }))
    const { setActivePinia, createPinia } = await import('pinia')
    const { useUIStore } = await import('../ui')
    setActivePinia(createPinia())
    const store = useUIStore()

    store.initTheme()
    expect(store.prismTone).toBe('vivid')
  })
})
