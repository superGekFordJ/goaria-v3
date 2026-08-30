import { describe, expect, it } from 'vitest'
import {
  categorizePickerItem,
  filterPickerItems,
  formatDisplayHost,
  formatDisplaySecondary,
  getAvailableCategories,
  getCategoryCounts,
  getDisplayFilename,
  isValidKnownSize,
  safeDecodeDisplayPath,
} from './pickerPresentation'

describe('pickerPresentation', () => {
  it('categorizes by MIME type with highest precedence', () => {
    expect(categorizePickerItem({ mime_type: 'video/mp4', filename: 'song.mp3' })).toBe('video')
    expect(categorizePickerItem({ mime_type: 'audio/mpeg', filename: 'video.mp4' })).toBe('audio')
    expect(categorizePickerItem({ mime_type: 'image/webp; charset=utf-8', filename: 'archive.zip' })).toBe('image')
    expect(categorizePickerItem({ mime_type: 'application/zip', filename: 'notes.txt' })).toBe('archive')
    expect(categorizePickerItem({ mime_type: 'application/pdf' })).toBe('document')
    expect(categorizePickerItem({ mime_type: 'text/plain' })).toBe('document')
  })

  it('categorizes by DOM semantic kind when MIME is absent', () => {
    expect(categorizePickerItem({ kind: 'image', filename: 'download.bin' })).toBe('image')
    expect(categorizePickerItem({ kind: 'video', filename: 'download.bin' })).toBe('video')
    expect(categorizePickerItem({ kind: 'audio', filename: 'download.bin' })).toBe('audio')
    // 'link' and 'source' fall through to extension
    expect(categorizePickerItem({ kind: 'link', filename: 'clip.mp4' })).toBe('video')
    expect(categorizePickerItem({ kind: 'source', filename: 'clip.mp4' })).toBe('video')
  })

  it('categorizes by compound and single extensions from filename, then pathname', () => {
    // Compound extension
    expect(categorizePickerItem({ filename: 'bundle.tar.gz' })).toBe('archive')
    expect(categorizePickerItem({ filename: 'bundle.tar.bz2' })).toBe('archive')
    expect(categorizePickerItem({ filename: 'bundle.tar.xz' })).toBe('archive')

    // Single extensions
    expect(categorizePickerItem({ filename: 'movie.MKV' })).toBe('video')
    expect(categorizePickerItem({ filename: 'song.flac' })).toBe('audio')
    expect(categorizePickerItem({ filename: 'photo.JPEG' })).toBe('image')
    expect(categorizePickerItem({ filename: 'doc.docx' })).toBe('document')
    expect(categorizePickerItem({ filename: 'data.csv' })).toBe('document')
    expect(categorizePickerItem({ filename: 'setup.iso' })).toBe('archive')

    // Fallback to path extension when filename has no extension
    expect(categorizePickerItem({ filename: 'download', path: '/media/video.webm' })).toBe('video')
    expect(categorizePickerItem({ filename: 'download', path: '/docs/manual.pdf' })).toBe('document')

    // Fallback to other
    expect(categorizePickerItem({ filename: 'unknown.xyz' })).toBe('other')
    expect(categorizePickerItem({})).toBe('other')
  })

  it('computes category counts and filters categories while hiding zero counts', () => {
    const items = [
      { filename: 'v1.mp4' },
      { filename: 'v2.mkv' },
      { filename: 'img.png' },
      { filename: 'doc.pdf' },
      { filename: 'misc.dat' },
    ]
    const counts = getCategoryCounts(items)
    expect(counts.all).toBe(5)
    expect(counts.video).toBe(2)
    expect(counts.image).toBe(1)
    expect(counts.document).toBe(1)
    expect(counts.other).toBe(1)
    expect(counts.audio).toBe(0)
    expect(counts.archive).toBe(0)

    const available = getAvailableCategories(counts)
    expect(available).toEqual(['all', 'video', 'image', 'document', 'other'])
    expect(available).not.toContain('audio')
    expect(available).not.toContain('archive')

    const videoFiltered = filterPickerItems(items, 'video')
    expect(videoFiltered).toHaveLength(2)
    expect(videoFiltered[0].filename).toBe('v1.mp4')
    expect(videoFiltered[1].filename).toBe('v2.mkv')

    const allFiltered = filterPickerItems(items, 'all')
    expect(allFiltered).toHaveLength(5)
  })

  it('safely decodes path segments while preserving encoded separators and discarding queries', () => {
    expect(safeDecodeDisplayPath('/downloads/my%20archive.zip')).toBe('/downloads/my archive.zip')
    expect(safeDecodeDisplayPath('/path/%E4%BD%A0%E5%A5%BD.pdf')).toBe('/path/\u4f60\u597d.pdf')
    // Preserves %2F and %5C
    expect(safeDecodeDisplayPath('/path/segment%2Fwith%2Fslash')).toBe('/path/segment%2Fwith%2Fslash')
    expect(safeDecodeDisplayPath('/path/segment%5Cwith%5Cbackslash')).toBe(
      '/path/segment%5Cwith%5Cbackslash',
    )
    // Discards query/fragment by contract
    expect(safeDecodeDisplayPath('/path/file.mp4?token=secret#hash')).toBe('/path/file.mp4')
    // Malformed escape fallback
    expect(safeDecodeDisplayPath('/path/malformed%80%')).toBe('/path/malformed%80%')
    expect(safeDecodeDisplayPath(undefined)).toBe('')
  })

  it('formats display host by removing scheme and preserving port', () => {
    expect(formatDisplayHost('https://example.com')).toBe('example.com')
    expect(formatDisplayHost('http://example.com:8080/')).toBe('example.com:8080')
    expect(formatDisplayHost('ftp://192.168.1.1:3000')).toBe('192.168.1.1:3000')
    expect(formatDisplayHost('subdomain.site.org')).toBe('subdomain.site.org')
    expect(formatDisplayHost(undefined)).toBe('')
  })

  it('formats secondary metadata combining host and decoded path', () => {
    expect(
      formatDisplaySecondary({
        origin: 'https://example.com',
        path: '/downloads/my%20file.pdf',
      }),
    ).toBe('example.com/downloads/my file.pdf')

    expect(
      formatDisplaySecondary({
        origin: 'http://cdn.site.com:8080',
        path: 'images/pic%201.png',
      }),
    ).toBe('cdn.site.com:8080/images/pic 1.png')

    expect(formatDisplaySecondary({ origin: 'https://example.com' })).toBe('example.com')
    expect(formatDisplaySecondary({ path: '/files/test.zip' })).toBe('/files/test.zip')
    expect(formatDisplaySecondary({})).toBe('')
  })

  it('validates positive safe integer sizes', () => {
    expect(isValidKnownSize(1024)).toBe(true)
    expect(isValidKnownSize(1)).toBe(true)
    expect(isValidKnownSize(Number.MAX_SAFE_INTEGER)).toBe(true)

    expect(isValidKnownSize(0)).toBe(false)
    expect(isValidKnownSize(-10)).toBe(false)
    expect(isValidKnownSize(10.5)).toBe(false)
    expect(isValidKnownSize(NaN)).toBe(false)
    expect(isValidKnownSize(Infinity)).toBe(false)
    expect(isValidKnownSize(undefined)).toBe(false)
    expect(isValidKnownSize('1024')).toBe(false)
  })

  it('generates display filename with 1-based position fallback', () => {
    expect(getDisplayFilename('my_report.pdf', 1, 'Item')).toBe('my_report.pdf')
    expect(getDisplayFilename('', 3, 'Item')).toBe('Item #3')
    expect(getDisplayFilename(undefined, 5, 'Download')).toBe('Download #5')
  })
})
