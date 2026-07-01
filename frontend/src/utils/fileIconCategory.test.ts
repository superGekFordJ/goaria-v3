import { describe, expect, it } from 'vitest'
import { categorizeByFileName } from './fileIconCategory'

describe('categorizeByFileName', () => {
  it('routes known extensions to their category', () => {
    expect(categorizeByFileName('setup.exe')).toBe('executable')
    expect(categorizeByFileName('app.dmg')).toBe('executable')
    expect(categorizeByFileName('app.msi')).toBe('executable')
    expect(categorizeByFileName('movie.mp4')).toBe('media')
    expect(categorizeByFileName('song.flac')).toBe('media')
    expect(categorizeByFileName('archive.zip')).toBe('archive')
    expect(categorizeByFileName('archive.7z')).toBe('archive')
    expect(categorizeByFileName('report.pdf')).toBe('document')
    expect(categorizeByFileName('notes.txt')).toBe('document')
    expect(categorizeByFileName('ubuntu.iso')).toBe('disk')
    expect(categorizeByFileName('disk.img')).toBe('disk')
  })

  it('falls back to default for unknown extensions', () => {
    expect(categorizeByFileName('data.xyz')).toBe('default')
    expect(categorizeByFileName('payload.bin')).toBe('default')
  })

  it('is case-insensitive', () => {
    expect(categorizeByFileName('MOVIE.MP4')).toBe('media')
    expect(categorizeByFileName('Movie.Mp4')).toBe('media')
    expect(categorizeByFileName('REPORT.PDF')).toBe('document')
  })

  it('handles compound extensions via the compound table', () => {
    expect(categorizeByFileName('archive.tar.gz')).toBe('archive')
    expect(categorizeByFileName('archive.tar.bz2')).toBe('archive')
    expect(categorizeByFileName('archive.tar.xz')).toBe('archive')
    expect(categorizeByFileName('backup.TAR.GZ')).toBe('archive')
  })

  it('routes .tar to archive via the single-segment table', () => {
    expect(categorizeByFileName('pack.tar')).toBe('archive')
  })

  it('returns default for files without an extension', () => {
    expect(categorizeByFileName('README')).toBe('default')
    expect(categorizeByFileName('Makefile')).toBe('default')
  })

  it('treats hidden dotfiles as default', () => {
    expect(categorizeByFileName('.gitignore')).toBe('default')
    expect(categorizeByFileName('.env')).toBe('default')
  })

  it('uses the last segment after multiple dots', () => {
    expect(categorizeByFileName('my.file.name.PDF')).toBe('document')
    expect(categorizeByFileName('a.b.c.mp4')).toBe('media')
  })

  it('trims surrounding whitespace', () => {
    expect(categorizeByFileName('  video.mp4  ')).toBe('media')
    expect(categorizeByFileName('\tsong.flac\n')).toBe('media')
  })

  it('strips path separators to take the basename', () => {
    expect(categorizeByFileName('C:\\downloads\\movie.mkv')).toBe('media')
    expect(categorizeByFileName('/home/user/song.flac')).toBe('media')
    expect(categorizeByFileName('C:\\downloads\\setup.exe')).toBe('executable')
  })

  it('returns default for null / undefined / empty', () => {
    expect(categorizeByFileName(null)).toBe('default')
    expect(categorizeByFileName(undefined)).toBe('default')
    expect(categorizeByFileName('')).toBe('default')
    expect(categorizeByFileName('   ')).toBe('default')
  })

  it('keeps .torrent on default (no dedicated torrent glyph)', () => {
    expect(categorizeByFileName('movie.torrent')).toBe('default')
  })

  it('handles non-ASCII filenames by basename + last dot', () => {
    expect(categorizeByFileName('电影.mp4')).toBe('media')
    expect(categorizeByFileName('/下载/报告.pdf')).toBe('document')
  })
})
