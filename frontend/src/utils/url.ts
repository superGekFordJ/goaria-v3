import type { useTaskStore } from '../stores/task'

export const isValidUrl = (text: string): boolean => {
  return /^(https?|ftp|sftp|magnet):/i.test(text)
}

export const isDuplicateUri = (uri: string, taskStore: ReturnType<typeof useTaskStore>): boolean => {
  const needle = uri.trim()
  if (!needle) return false
  return taskStore.allUris.has(needle)
}
