import { describe, expect, it } from 'vitest'
import { EXTRACTOR_FOLDER_MAX_RUNES } from './extractorKeys'
import { filterFolderName, folderFieldForSubmit } from './pickerFolder'

describe('filterFolderName', () => {
  it('strips reserved punctuation and controls, then caps runes', () => {
    expect(filterFolderName('  Album:Part/1  ')).toBe('AlbumPart1')
    expect(filterFolderName('a\\b/c:*?"<>|d')).toBe('abcd')
    expect(filterFolderName('a\nb\tc')).toBe('abc')
    expect(filterFolderName('   ')).toBeUndefined()
    const long = 'A'.repeat(EXTRACTOR_FOLDER_MAX_RUNES + 1)
    expect([...filterFolderName(long) ?? ''].length).toBe(EXTRACTOR_FOLDER_MAX_RUNES)
  })
})

describe('folderFieldForSubmit', () => {
  it('omits group fields when the toggle is off or only one row is selected', () => {
    expect(
      folderFieldForSubmit({ createGroup: false, selectedCount: 4, raw: 'Album' }),
    ).toEqual({})
    expect(
      folderFieldForSubmit({ createGroup: true, selectedCount: 1, raw: 'Album' }),
    ).toEqual({})
  })

  it('emits CON for host fallback and never sends CRLF', () => {
    expect(
      folderFieldForSubmit({ createGroup: true, selectedCount: 2, raw: 'CON' }),
    ).toEqual({ create_group: true, folder_name: 'CON' })
    expect(
      folderFieldForSubmit({ createGroup: true, selectedCount: 2, raw: 'ok\r\nname' }),
    ).toEqual({ create_group: true, folder_name: 'okname' })
    expect(
      folderFieldForSubmit({ createGroup: true, selectedCount: 2, raw: '' }),
    ).toEqual({ create_group: true })
  })
})
