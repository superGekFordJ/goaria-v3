export type CookieStoreHint = {
  id?: unknown
  tabIds?: unknown
}

export function pickCookieStoreId(
  tabId: number,
  tabCookieStoreId: unknown,
  stores: CookieStoreHint[],
): string | undefined {
  if (typeof tabCookieStoreId === 'string') {
    const trimmed = tabCookieStoreId.trim()
    if (trimmed !== '') return trimmed
  }
  let found: string | undefined
  for (const store of stores) {
    if (!Array.isArray(store.tabIds) || !store.tabIds.includes(tabId)) continue
    if (typeof store.id !== 'string') continue
    const id = store.id.trim()
    if (id === '') continue
    if (found !== undefined && found !== id) return undefined
    found = id
  }
  return found
}
