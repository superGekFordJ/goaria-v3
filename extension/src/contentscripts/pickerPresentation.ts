import { sanitizeDisplayFilename } from '../background/extractorKeys'

export type PickerCategory = 'all' | 'video' | 'audio' | 'image' | 'archive' | 'document' | 'other'
export type ItemCategory = Exclude<PickerCategory, 'all'>

export const PICKER_CATEGORIES: readonly PickerCategory[] = [
  'all',
  'video',
  'audio',
  'image',
  'archive',
  'document',
  'other',
] as const

const ARCHIVE_MIME_SET: ReadonlySet<string> = new Set([
  'application/zip',
  'application/x-zip-compressed',
  'application/x-rar',
  'application/x-rar-compressed',
  'application/vnd.rar',
  'application/x-7z-compressed',
  'application/x-7z',
  'application/x-tar',
  'application/x-gtar',
  'application/gzip',
  'application/x-gzip',
  'application/x-bzip',
  'application/x-bzip2',
  'application/x-xz',
  'application/x-zstd',
  'application/x-compressed',
  'application/x-archive',
  'application/x-cpio',
  'application/x-iso9660-image',
  'application/x-apple-diskimage',
])

const DOCUMENT_MIME_SET: ReadonlySet<string> = new Set([
  'application/pdf',
  'application/msword',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'application/vnd.ms-excel',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  'application/vnd.ms-powerpoint',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  'application/rtf',
  'application/epub+zip',
  'application/vnd.oasis.opendocument.text',
  'application/vnd.oasis.opendocument.spreadsheet',
  'application/vnd.oasis.opendocument.presentation',
])

const COMPOUND_EXTENSIONS: ReadonlyMap<string, ItemCategory> = new Map([
  ['tar.gz', 'archive'],
  ['tar.bz2', 'archive'],
  ['tar.xz', 'archive'],
  ['tar.zst', 'archive'],
  ['tar.z', 'archive'],
])

const EXTENSION_MAP: ReadonlyMap<string, ItemCategory> = new Map([
  // video
  ['mp4', 'video'],
  ['mkv', 'video'],
  ['mov', 'video'],
  ['avi', 'video'],
  ['webm', 'video'],
  ['flv', 'video'],
  ['m4v', 'video'],
  ['ts', 'video'],
  ['wmv', 'video'],
  ['3gp', 'video'],
  ['rmvb', 'video'],
  ['asf', 'video'],
  ['vob', 'video'],
  ['ogv', 'video'],
  // audio
  ['mp3', 'audio'],
  ['flac', 'audio'],
  ['wav', 'audio'],
  ['aac', 'audio'],
  ['ogg', 'audio'],
  ['m4a', 'audio'],
  ['opus', 'audio'],
  ['wma', 'audio'],
  ['alac', 'audio'],
  ['aiff', 'audio'],
  ['mid', 'audio'],
  ['midi', 'audio'],
  ['ape', 'audio'],
  // image
  ['jpg', 'image'],
  ['jpeg', 'image'],
  ['png', 'image'],
  ['gif', 'image'],
  ['webp', 'image'],
  ['svg', 'image'],
  ['bmp', 'image'],
  ['ico', 'image'],
  ['tiff', 'image'],
  ['tif', 'image'],
  ['avif', 'image'],
  ['heic', 'image'],
  ['heif', 'image'],
  ['apng', 'image'],
  // archive
  ['zip', 'archive'],
  ['rar', 'archive'],
  ['7z', 'archive'],
  ['7zip', 'archive'],
  ['tar', 'archive'],
  ['gz', 'archive'],
  ['bz2', 'archive'],
  ['xz', 'archive'],
  ['zst', 'archive'],
  ['tgz', 'archive'],
  ['tbz2', 'archive'],
  ['txz', 'archive'],
  ['iso', 'archive'],
  ['dmg', 'archive'],
  ['pkg', 'archive'],
  ['deb', 'archive'],
  ['rpm', 'archive'],
  ['apk', 'archive'],
  ['cab', 'archive'],
  ['z', 'archive'],
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
  ['csv', 'document'],
  ['tsv', 'document'],
  ['pages', 'document'],
  ['numbers', 'document'],
  ['key', 'document'],
  ['odt', 'document'],
  ['ods', 'document'],
  ['odp', 'document'],
])

function extractExtensionCategory(filenameOrPath: string): ItemCategory | null {
  const trimmed = filenameOrPath.trim()
  if (!trimmed) return null
  const slash = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'))
  const base = slash >= 0 ? trimmed.slice(slash + 1) : trimmed
  const lastDot = base.lastIndexOf('.')
  if (lastDot <= 0) return null

  // Check compound extension
  const prevDot = base.lastIndexOf('.', lastDot - 1)
  if (prevDot >= 0) {
    const compound = base.slice(prevDot + 1).toLowerCase()
    const compoundCategory = COMPOUND_EXTENSIONS.get(compound)
    if (compoundCategory) return compoundCategory
  }

  const ext = base.slice(lastDot + 1).toLowerCase()
  return EXTENSION_MAP.get(ext) ?? null
}

export function categorizePickerItem(item: {
  filename?: string
  mime_type?: string
  kind?: string
  path?: string
}): ItemCategory {
  // 1. Normalized MIME
  if (item.mime_type) {
    const mime = item.mime_type.split(';')[0]?.trim().toLowerCase()
    if (mime) {
      if (mime.startsWith('video/')) return 'video'
      if (mime.startsWith('audio/')) return 'audio'
      if (mime.startsWith('image/')) return 'image'
      if (mime.startsWith('text/') || DOCUMENT_MIME_SET.has(mime)) return 'document'
      if (ARCHIVE_MIME_SET.has(mime)) return 'archive'
    }
  }

  // 2. DOM semantic kind
  if (item.kind === 'video') return 'video'
  if (item.kind === 'audio') return 'audio'
  if (item.kind === 'image') return 'image'

  // 3. Recognized extension from filename, then pathname
  if (item.filename) {
    const cat = extractExtensionCategory(item.filename)
    if (cat) return cat
  }
  if (item.path) {
    const cat = extractExtensionCategory(item.path)
    if (cat) return cat
  }

  // 4. Fallback
  return 'other'
}

export function getCategoryCounts<
  T extends { filename?: string; mime_type?: string; kind?: string; path?: string },
>(items: ReadonlyArray<T>): Record<PickerCategory, number> {
  const counts: Record<PickerCategory, number> = {
    all: items.length,
    video: 0,
    audio: 0,
    image: 0,
    archive: 0,
    document: 0,
    other: 0,
  }

  for (const item of items) {
    const cat = categorizePickerItem(item)
    counts[cat] = (counts[cat] ?? 0) + 1
  }

  return counts
}

export function getAvailableCategories(counts: Record<PickerCategory, number>): PickerCategory[] {
  return PICKER_CATEGORIES.filter(cat => cat === 'all' || (counts[cat] ?? 0) > 0)
}

export function filterPickerItems<
  T extends { filename?: string; mime_type?: string; kind?: string; path?: string },
>(items: ReadonlyArray<T>, category: PickerCategory): T[] {
  if (category === 'all') return [...items]
  return items.filter(item => categorizePickerItem(item) === category)
}

export function safeDecodeDisplayPath(path: string | undefined): string {
  if (!path) return ''
  const clean = path.split('?')[0]?.split('#')[0] ?? ''
  if (!clean) return ''
  try {
    const masked = clean
      .replace(/%2[fF]/g, '__GOARIA_ESC_2F__')
      .replace(/%5[cC]/g, '__GOARIA_ESC_5C__')
    const decoded = decodeURIComponent(masked)
    return decoded
      .replace(/__GOARIA_ESC_2F__/g, '%2F')
      .replace(/__GOARIA_ESC_5C__/g, '%5C')
  } catch {
    try {
      return clean.replace(/(%[0-9A-Fa-f]{2})+/g, match => {
        if (/^%(?:2[fF]|5[cC])$/i.test(match)) return match
        try {
          return decodeURIComponent(match)
        } catch {
          return match
        }
      })
    } catch {
      return clean
    }
  }
}

export function formatDisplayHost(origin: string | undefined): string {
  if (!origin) return ''
  const trimmed = origin.trim()
  if (!trimmed) return ''
  const withoutScheme = trimmed.replace(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//, '')
  return withoutScheme.replace(/\/+$/, '')
}

export function formatDisplaySecondary(item: {
  origin?: string
  path?: string
  kind?: string
}): string {
  const host = formatDisplayHost(item.origin)
  const decodedPath = safeDecodeDisplayPath(item.path)
  if (host && decodedPath) {
    const p = decodedPath.startsWith('/') ? decodedPath : `/${decodedPath}`
    return `${host}${p}`
  }
  if (host) return host
  if (decodedPath) return decodedPath
  return ''
}

export function isValidKnownSize(size: unknown): size is number {
  return typeof size === 'number' && Number.isSafeInteger(size) && size > 0
}

export function getDisplayFilename(
  filename: string | undefined,
  position: number,
  fallbackLabel: string,
): string {
  const sanitized = sanitizeDisplayFilename(filename)
  if (sanitized) return sanitized
  return `${fallbackLabel} #${position}`
}
