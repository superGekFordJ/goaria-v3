export type FileIconCategoryId =
  | 'default'
  | 'media'
  | 'archive'
  | 'executable'
  | 'document'
  | 'disk'

export const FILE_ICON_CATEGORIES = [
  'default',
  'media',
  'archive',
  'executable',
  'document',
  'disk',
] as const

// Module-level O(1) lookup tables, built once.
const EXTENSION_TO_CATEGORY: ReadonlyMap<string, FileIconCategoryId> = new Map([
  // executable / installer
  ['exe', 'executable'],
  ['msi', 'executable'],
  ['dmg', 'executable'],
  ['apk', 'executable'],
  ['deb', 'executable'],
  ['rpm', 'executable'],
  ['appimage', 'executable'],
  ['xpi', 'executable'],
  // media (video / audio)
  ['mp4', 'media'],
  ['mkv', 'media'],
  ['mov', 'media'],
  ['avi', 'media'],
  ['webm', 'media'],
  ['flv', 'media'],
  ['m4v', 'media'],
  ['mp3', 'media'],
  ['flac', 'media'],
  ['wav', 'media'],
  ['aac', 'media'],
  ['ogg', 'media'],
  ['m4a', 'media'],
  ['opus', 'media'],
  ['wma', 'media'],
  // disk / image
  ['iso', 'disk'],
  ['img', 'disk'],
  ['vhd', 'disk'],
  ['vhdx', 'disk'],
  ['wim', 'disk'],
  // archive
  ['zip', 'archive'],
  ['rar', 'archive'],
  ['7z', 'archive'],
  ['tar', 'archive'],
  ['gz', 'archive'],
  ['bz2', 'archive'],
  ['xz', 'archive'],
  ['zst', 'archive'],
  ['tgz', 'archive'],
  ['tbz2', 'archive'],
  ['txz', 'archive'],
  // document
  ['pdf', 'document'],
  ['epub', 'document'],
  ['doc', 'document'],
  ['docx', 'document'],
  ['xls', 'document'],
  ['xlsx', 'document'],
  ['ppt', 'document'],
  ['pptx', 'document'],
  ['txt', 'document'],
  ['md', 'document'],
  ['rtf', 'document'],
  ['mobi', 'document'],
  ['azw3', 'document'],
])

// Compound extensions checked first (e.g. "tar.gz"); all map to archive.
const COMPOUND_EXTENSIONS: ReadonlyMap<string, FileIconCategoryId> = new Map([
  ['tar.gz', 'archive'],
  ['tar.bz2', 'archive'],
  ['tar.xz', 'archive'],
])

function basenameOf(fileName: string): string {
  const slash = Math.max(fileName.lastIndexOf('/'), fileName.lastIndexOf('\\'))
  return slash >= 0 ? fileName.slice(slash + 1) : fileName
}

export function categorizeByFileName(fileName: string | null | undefined): FileIconCategoryId {
  if (!fileName) return 'default'
  const trimmed = fileName.trim()
  if (!trimmed) return 'default'

  const base = basenameOf(trimmed)
  const lastDot = base.lastIndexOf('.')
  // No dot, or dot is the leading char (hidden file like ".gitignore") -> default.
  if (lastDot <= 0) return 'default'

  // Compound "penultimate.last" segment takes precedence over single.
  const prevDot = base.lastIndexOf('.', lastDot - 1)
  if (prevDot >= 0) {
    const compound = base.slice(prevDot + 1).toLowerCase()
    const compoundHit = COMPOUND_EXTENSIONS.get(compound)
    if (compoundHit) return compoundHit
  }

  const lower = base.slice(lastDot + 1).toLowerCase()
  const direct = EXTENSION_TO_CATEGORY.get(lower)
  if (direct) return direct

  return 'default'
}
