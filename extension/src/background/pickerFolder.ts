import { EXTRACTOR_FOLDER_MAX_RUNES } from './extractorKeys'

const FOLDER_FORBIDDEN = new Set('\\/:*?"<>|'.split(''))
const MAX_FOLDER_BYTES = 1024

function shouldStripFolderChar(ch: string): boolean {
  const code = ch.codePointAt(0) ?? 0
  if (code < 32 || code === 127) return true
  if (code >= 0x80 && code <= 0x9f) return true
  if (code === 0x2028 || code === 0x2029) return true
  return FOLDER_FORBIDDEN.has(ch)
}

export function filterFolderName(raw: unknown): string | undefined {
  if (typeof raw !== 'string') return undefined
  let text = ''
  for (const ch of raw) {
    if (shouldStripFolderChar(ch)) continue
    text += ch
  }
  text = text.trim()
  if (!text) return undefined
  const runes = [...text]
  if (runes.length > EXTRACTOR_FOLDER_MAX_RUNES) {
    text = runes.slice(0, EXTRACTOR_FOLDER_MAX_RUNES).join('').trim()
  }
  return text || undefined
}

export type FolderSubmitFields = {
  create_group?: true
  folder_name?: string
}

export function folderFieldForSubmit(opts: {
  createGroup: boolean
  selectedCount: number
  raw: string
}): FolderSubmitFields {
  if (!opts.createGroup || opts.selectedCount < 2) return {}
  const fields: FolderSubmitFields = { create_group: true }
  const name = filterFolderName(opts.raw)
  if (!name) return fields
  if (name.includes('\r') || name.includes('\n')) return fields
  try {
    if (new TextEncoder().encode(name).length > MAX_FOLDER_BYTES) return fields
  } catch {
    return fields
  }
  fields.folder_name = name
  return fields
}
