import type { useTaskStore } from '../stores/task'

type DuplicateUriStore = Pick<ReturnType<typeof useTaskStore>, 'allUris'>

export const isValidUrl = (text: string): boolean => {
  return /^(https?|ftp|sftp|magnet):/i.test(text)
}

const PAIRING_PATH_MARKER = '/__goaria_pair__/pair.html'

export const isPairingUrl = (text: string): boolean => {
  return text.includes(PAIRING_PATH_MARKER)
}

export const isDuplicateUri = (uri: string, taskStore: DuplicateUriStore): boolean => {
  const needle = uri.trim()
  if (!needle) return false
  return taskStore.allUris.has(needle)
}
