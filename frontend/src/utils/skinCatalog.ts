/**
 * Skin Catalog — Single source of truth for all skin metadata.
 *
 * Each skin is a curated material/signal proposition, not an arbitrary color swatch.
 * Storage IDs are immutable for backward compatibility; display names may differ.
 */

export const skinCatalog = [
  {
    id: 'obsidian',
    labelKey: 'appearance.skins.obsidian.name',
    descriptionKey: 'appearance.skins.obsidian.description',
    conceptKey: 'appearance.skins.obsidian.concept',
    preview: {
      dark: { from: '#06ffd5', to: '#22ff88' },
      light: { from: '#0f766e', to: '#047857' },
    },
    sortOrder: 0,
  },
  {
    id: 'ceramic',
    labelKey: 'appearance.skins.ceramic.name',
    descriptionKey: 'appearance.skins.ceramic.description',
    conceptKey: 'appearance.skins.ceramic.concept',
    preview: {
      dark: { from: '#0dd9b5', to: '#5eead4' },
      light: { from: '#0d9488', to: '#2dd4bf' },
    },
    sortOrder: 1,
  },
  {
    id: 'aurora',
    labelKey: 'appearance.skins.aurora.name',
    descriptionKey: 'appearance.skins.aurora.description',
    conceptKey: 'appearance.skins.aurora.concept',
    preview: {
      dark: { from: '#818cf8', to: '#38bdf8' },
      light: { from: '#6366f1', to: '#0ea5e9' },
    },
    sortOrder: 2,
  },
  {
    id: 'ember',
    labelKey: 'appearance.skins.ember.name',
    descriptionKey: 'appearance.skins.ember.description',
    conceptKey: 'appearance.skins.ember.concept',
    preview: {
      dark: { from: '#f59e0b', to: '#ef4444' },
      light: { from: '#b45309', to: '#c2410c' },
    },
    sortOrder: 3,
  },
] as const

/** Union type derived from the catalog — the single source of SkinId. */
export type SkinId = (typeof skinCatalog)[number]['id']

/** Default skin applied on first launch or when persisted value is invalid. */
export const DEFAULT_SKIN_ID: SkinId = 'obsidian'

/** Set of all valid skin IDs for quick validation. */
export const validSkinIds = new Set<string>(skinCatalog.map(s => s.id))

/** Look up a catalog entry by ID. */
export function getSkinMeta(id: SkinId) {
  return skinCatalog.find(s => s.id === id)
}

/** Validate and normalise a persisted skin ID, falling back to default. */
export function normaliseSkinId(raw: unknown): SkinId {
  if (typeof raw === 'string' && validSkinIds.has(raw)) {
    return raw as SkinId
  }
  return DEFAULT_SKIN_ID
}
