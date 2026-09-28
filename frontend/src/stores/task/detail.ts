import { ref } from 'vue'
import { GetTaskDetails } from '../../../bindings/goaria-v3/internal/wailsapp/app.js'
import type { TaskDetail } from '../../../bindings/goaria-v3/internal/tasks/models'

export type TaskDetailPhase = 'pending' | 'ready' | 'failed'

export interface TaskDetailState {
  gid: string
  // Status the data was requested for; lets a reopened card skip stale data.
  status: string
  phase: TaskDetailPhase
  detail?: TaskDetail
  found?: boolean
}

export function setupTaskDetail() {
  const taskDetail = ref<TaskDetailState | null>(null)
  let detailRequestSeq = 0

  async function fetchTaskDetail(gid: string, status: string): Promise<void> {
    const key = gid.trim()
    if (!key) return
    const seq = ++detailRequestSeq
    const current = taskDetail.value
    const silent = current?.gid === key && current.status === status && current.phase === 'ready'
    if (!silent) {
      taskDetail.value = { gid: key, status, phase: 'pending' }
    }

    let failed = false
    let next: TaskDetailState | null = null
    try {
      const result = await GetTaskDetails([key])
      const envelope = result?.[key]
      if (!envelope || !envelope.found) {
        next = { gid: key, status, phase: 'ready', found: false }
      } else if (!envelope.detail || envelope.detail.gid !== key) {
        // Identity mismatch: never render another task's facts.
        failed = true
      } else {
        next = { gid: key, status, phase: 'ready', found: true, detail: envelope.detail }
      }
    } catch {
      failed = true
    }

    if (seq !== detailRequestSeq) return
    if (failed) {
      // A failed silent refresh keeps the last good data.
      if (!silent) taskDetail.value = { gid: key, status, phase: 'failed' }
      return
    }
    taskDetail.value = next
  }

  // State is kept after close: consumers match gid + status before rendering.
  return { taskDetail, fetchTaskDetail }
}
