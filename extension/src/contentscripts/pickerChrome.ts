export function pickerCatalogKey(identity: string, indices: Iterable<number>): string {
  return `${identity}:${[...indices].join(',')}`
}

export function pickerItemIdentity(items: ReadonlyArray<{ index: number }>): string {
  return items.map(item => String(item.index)).join(',')
}

export function formatPickerBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`
  return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`
}
