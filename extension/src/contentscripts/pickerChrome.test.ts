import { describe, expect, it } from 'vitest'
import { formatPickerBytes, pickerCatalogKey } from './pickerChrome'

describe('pickerCatalogKey', () => {
  it('joins identity and indices the same way live pickers reset catalogs', () => {
    expect(pickerCatalogKey('page-token', [0, 1, 2])).toBe('page-token:0,1,2')
    expect(pickerCatalogKey('catalog-id', [])).toBe('catalog-id:')
    expect(pickerCatalogKey('', [7])).toBe(':7')
  })
})

describe('formatPickerBytes', () => {
  it('uses B then one-decimal KB/MB/GB', () => {
    expect(formatPickerBytes(0)).toBe('0 B')
    expect(formatPickerBytes(1023)).toBe('1023 B')
    expect(formatPickerBytes(1024)).toBe('1.0 KB')
    expect(formatPickerBytes(1536)).toBe('1.5 KB')
    expect(formatPickerBytes(1024 * 1024)).toBe('1.0 MB')
    expect(formatPickerBytes(1024 * 1024 * 1024)).toBe('1.0 GB')
  })
})
