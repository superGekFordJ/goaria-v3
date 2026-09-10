import { describe, expect, it } from 'vitest'
import {
  skinCatalog,
  validSkinIds,
  DEFAULT_SKIN_ID,
  getSkinMeta,
  normaliseSkinId,
  type SkinId,
} from './skinCatalog'

describe('skinCatalog', () => {
  it('contains all 7 curated skins in order', () => {
    const expectedIds: SkinId[] = [
      'obsidian',
      'ceramic',
      'aurora',
      'ember',
      'titanium',
      'surge',
      'prism',
    ]
    expect(skinCatalog.map(s => s.id)).toEqual(expectedIds)
    expect(skinCatalog).toHaveLength(7)
  })

  it('has sequential sort orders from 0 to 6', () => {
    skinCatalog.forEach((skin, index) => {
      expect(skin.sortOrder).toBe(index)
    })
  })

  it('includes titanium (Seat 5) with correct preview and metadata', () => {
    const titanium = getSkinMeta('titanium')
    expect(titanium).toBeDefined()
    expect(titanium?.sortOrder).toBe(4)
    expect(titanium?.labelKey).toBe('appearance.skins.titanium.name')
    expect(titanium?.descriptionKey).toBe('appearance.skins.titanium.description')
    expect(titanium?.conceptKey).toBe('appearance.skins.titanium.concept')
    expect(titanium?.preview.dark).toEqual({ from: '#f8fafc', to: '#94a3b8' })
    expect(titanium?.preview.light).toEqual({ from: '#1e293b', to: '#64748b' })
  })

  it('includes surge (Seat 6) with correct preview and metadata', () => {
    const surge = getSkinMeta('surge')
    expect(surge).toBeDefined()
    expect(surge?.sortOrder).toBe(5)
    expect(surge?.labelKey).toBe('appearance.skins.surge.name')
    expect(surge?.descriptionKey).toBe('appearance.skins.surge.description')
    expect(surge?.conceptKey).toBe('appearance.skins.surge.concept')
    expect(surge?.preview.dark).toEqual({ from: '#f43f5e', to: '#8b5cf6' })
    expect(surge?.preview.light).toEqual({ from: '#be123c', to: '#6d28d9' })
  })

  it('includes prism (Seat 7) with correct preview and metadata', () => {
    const prism = getSkinMeta('prism')
    expect(prism).toBeDefined()
    expect(prism?.sortOrder).toBe(6)
    expect(prism?.labelKey).toBe('appearance.skins.prism.name')
    expect(prism?.descriptionKey).toBe('appearance.skins.prism.description')
    expect(prism?.conceptKey).toBe('appearance.skins.prism.concept')
    expect(prism?.preview.dark).toEqual({ from: '#a855f7', to: '#06b6d4' })
    expect(prism?.preview.light).toEqual({ from: '#7c3aed', to: '#0284c7' })
  })

  it('validSkinIds contains all 7 skin IDs', () => {
    expect(validSkinIds.has('obsidian')).toBe(true)
    expect(validSkinIds.has('ceramic')).toBe(true)
    expect(validSkinIds.has('aurora')).toBe(true)
    expect(validSkinIds.has('ember')).toBe(true)
    expect(validSkinIds.has('titanium')).toBe(true)
    expect(validSkinIds.has('surge')).toBe(true)
    expect(validSkinIds.has('prism')).toBe(true)
    expect(validSkinIds.has('unknown')).toBe(false)
  })

  it('normaliseSkinId validates valid skins and falls back to default for invalid values', () => {
    expect(normaliseSkinId('titanium')).toBe('titanium')
    expect(normaliseSkinId('surge')).toBe('surge')
    expect(normaliseSkinId('prism')).toBe('prism')
    expect(normaliseSkinId('obsidian')).toBe('obsidian')
    expect(normaliseSkinId('ceramic')).toBe('ceramic')
    expect(normaliseSkinId('aurora')).toBe('aurora')
    expect(normaliseSkinId('ember')).toBe('ember')
    expect(normaliseSkinId('non-existent')).toBe(DEFAULT_SKIN_ID)
    expect(normaliseSkinId(null)).toBe(DEFAULT_SKIN_ID)
    expect(normaliseSkinId(undefined)).toBe(DEFAULT_SKIN_ID)
    expect(normaliseSkinId(123)).toBe(DEFAULT_SKIN_ID)
  })
})
