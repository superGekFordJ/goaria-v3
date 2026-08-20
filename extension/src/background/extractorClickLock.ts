const inFlightTabs = new Set<number>()
const clickEpoch = new Map<number, number>()

export function clickEpochOf(tabId: number): number {
  return clickEpoch.get(tabId) ?? 0
}

export function hasInFlight(tabId: number): boolean {
  return inFlightTabs.has(tabId)
}

export function tryLockClick(tabId: number): number | undefined {
  if (inFlightTabs.has(tabId)) return undefined
  inFlightTabs.add(tabId)
  return clickEpochOf(tabId)
}

export function releaseClick(tabId: number, epoch: number): void {
  if (clickEpochOf(tabId) !== epoch) return
  inFlightTabs.delete(tabId)
}

export function cancelTabClick(tabId: number): void {
  clickEpoch.set(tabId, clickEpochOf(tabId) + 1)
  inFlightTabs.delete(tabId)
}

export function cancelAllClicks(): void {
  const ids = new Set<number>([...inFlightTabs, ...clickEpoch.keys()])
  for (const id of ids) cancelTabClick(id)
}
