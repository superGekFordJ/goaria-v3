export const restoreSelector = '[data-extractor-capsule-action]'

export function wrapTabIndex(count: number, current: number, shift: boolean): number {
  if (count <= 0) return 0
  if (count === 1) return 0
  const bounded = ((current % count) + count) % count
  if (shift) return bounded === 0 ? count - 1 : bounded - 1
  return bounded === count - 1 ? 0 : bounded + 1
}

export function isTrapFocusable(el: { getAttribute(name: string): string | null }): boolean {
  if (el.getAttribute('tabindex') === '-1') return false
  if (el.getAttribute('aria-hidden') === 'true') return false
  return true
}

export function activeFromRoot<T>(
  root: { activeElement: T | null } | null | undefined,
  fallback: T | null,
): T | null {
  return root ? root.activeElement : fallback
}
