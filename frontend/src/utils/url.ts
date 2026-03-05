import type { useTaskStore } from '../stores/task'

export const isValidUrl = (text: string): boolean => {
  return /^(https?|ftp|sftp|magnet):/i.test(text)
}

export const isDuplicateUri = (uri: string, taskStore: ReturnType<typeof useTaskStore>): boolean => {
  const needle = uri.trim()
  if (!needle) return false
  for (const list of [taskStore.activeTasks, taskStore.waitingTasks, taskStore.stoppedTasks]) {
    for (const t of list) {
      for (const f of t.files || []) {
        for (const u of f.uris || []) {
          if (u?.uri === needle) return true
        }
      }
    }
  }
  return false
}
