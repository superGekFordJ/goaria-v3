<script setup lang="ts">
  /**
   * TestSimulator - In-App WebView 性能测试工具
   * 在 wails3 dev 模式下通过 URL hash #test-simulator 激活
   * 用于测试真实 WebView 环境下的渲染性能和内存消耗
   */
  import { ref, computed, onUnmounted } from 'vue'
  import { useTaskStore } from '@/stores/task'
  import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'

  const taskStore = useTaskStore()
  const renderTime = ref(0)
  const memoryBefore = ref(0)
  const memoryAfter = ref(0)
  const lastOperation = ref('')
  const isRunning = ref(false)

  const memoryDelta = computed(() => {
    if (!memoryAfter.value || !memoryBefore.value) return 0
    return ((memoryAfter.value - memoryBefore.value) / 1024 / 1024).toFixed(2)
  })

  const currentMemory = computed(() => {
    if (!memoryAfter.value) return 0
    return (memoryAfter.value / 1024 / 1024).toFixed(2)
  })

  function createMockTask(index: number, status: string = 'complete'): Task {
    return {
      gid: `sim-${Date.now()}-${index.toString().padStart(6, '0')}`,
      title: `sim-task-${index}`,
      status,
      totalLength: `${Math.floor(Math.random() * 1000000000) + 100000}`,
      completedLength: `${Math.floor(Math.random() * 1000000000) + 100000}`,
      downloadSpeed: '0',
      dir: 'D:\\Downloads',
      errorCode: '',
      errorMessage: '',
      files: [
        {
          path: `D:\\Downloads\\simulated-file-${index}.zip`,
          uris: [{ uri: `https://example.com/file-${index}.zip`, status: 'used' }],
        },
      ],
    }
  }

  interface PerformanceMemory {
    usedJSHeapSize: number
  }

  function getMemoryUsage(): number {
    const perf = performance as typeof performance & { memory?: PerformanceMemory }
    return perf.memory?.usedJSHeapSize || 0
  }

  async function injectTasks(count: number) {
    if (isRunning.value) return
    isRunning.value = true
    lastOperation.value = `注入 ${count} 个任务...`

    // 记录初始内存
    memoryBefore.value = getMemoryUsage()

    // 生成任务
    const newTasks: Task[] = []
    for (let i = 0; i < count; i++) {
      // 混合状态
      const status = i % 10 === 0 ? 'error' : 'complete'
      newTasks.push(createMockTask(i, status))
    }

    // 测量渲染时间
    const startTime = performance.now()

    // 注入到 store
    taskStore.tasks = {
      active: taskStore.tasks.active,
      waiting: taskStore.tasks.waiting,
      stopped: [...newTasks, ...taskStore.tasks.stopped],
    }

    // 等待 Vue 响应式更新和 DOM 渲染
    await new Promise(resolve => requestAnimationFrame(resolve))
    await new Promise(resolve => setTimeout(resolve, 100))

    renderTime.value = Math.round(performance.now() - startTime)
    memoryAfter.value = getMemoryUsage()
    lastOperation.value = `完成注入 ${count} 个任务`
    isRunning.value = false
  }

  async function injectDownloadsTasks(count: number, status: 'active' | 'waiting' = 'active') {
    if (isRunning.value) return
    isRunning.value = true
    lastOperation.value = `注入 ${count} 个 ${status} 任务...`
    memoryBefore.value = getMemoryUsage()

    const startTime = performance.now()
    const newTasks: Task[] = []
    for (let i = 0; i < count; i++) {
      newTasks.push(createMockTask(i, status))
    }

    if (status === 'active') {
      taskStore.tasks = {
        ...taskStore.tasks,
        active: [...newTasks, ...taskStore.tasks.active],
      }
    } else {
      taskStore.tasks = {
        ...taskStore.tasks,
        waiting: [...newTasks, ...taskStore.tasks.waiting],
      }
    }

    await new Promise(resolve => requestAnimationFrame(resolve))
    await new Promise(resolve => setTimeout(resolve, 100))

    renderTime.value = Math.round(performance.now() - startTime)
    memoryAfter.value = getMemoryUsage()
    lastOperation.value = `完成注入 ${count} 个 ${status} 任务`
    isRunning.value = false
  }

  function shuffleTasks() {
    if (isRunning.value) return
    const startTime = performance.now()

    const shuffleArray = <T,>(arr: T[]): T[] => {
      const copy = [...arr]
      for (let i = copy.length - 1; i > 0; i--) {
        const j = Math.floor(Math.random() * (i + 1))
        ;[copy[i], copy[j]] = [copy[j], copy[i]]
      }
      return copy
    }

    taskStore.tasks = {
      active: shuffleArray(taskStore.tasks.active),
      waiting: shuffleArray(taskStore.tasks.waiting),
      stopped: shuffleArray(taskStore.tasks.stopped),
    }

    renderTime.value = Math.round(performance.now() - startTime)
    lastOperation.value = '🔀 随机重排任务完成'
  }

  function toggleBatchTaskStatus(count: number) {
    if (isRunning.value) return
    const startTime = performance.now()

    const activeList = [...taskStore.tasks.active]
    const waitingList = [...taskStore.tasks.waiting]
    const stoppedList = [...taskStore.tasks.stopped]

    let movedCount = 0

    for (let i = 0; i < count; i++) {
      const action = Math.floor(Math.random() * 3)

      if (action === 0 && waitingList.length > 0) {
        const idx = Math.floor(Math.random() * waitingList.length)
        const [task] = waitingList.splice(idx, 1)
        activeList.unshift({ ...task, status: 'active' })
        movedCount++
      } else if (action === 1 && activeList.length > 0) {
        const idx = Math.floor(Math.random() * activeList.length)
        const [task] = activeList.splice(idx, 1)
        waitingList.unshift({ ...task, status: 'paused' })
        movedCount++
      } else if (stoppedList.length > 0) {
        const idx = Math.floor(Math.random() * stoppedList.length)
        const [task] = stoppedList.splice(idx, 1)
        activeList.unshift({ ...task, status: 'active' })
        movedCount++
      }
    }

    taskStore.tasks = {
      active: activeList,
      waiting: waitingList,
      stopped: stoppedList,
    }

    renderTime.value = Math.round(performance.now() - startTime)
    lastOperation.value = `⚡ 批量变更 ${movedCount} 个任务状态`
  }

  const isAutoSimulating = ref(false)
  let autoSimTimer: ReturnType<typeof setInterval> | null = null

  function toggleAutoSimulation() {
    if (isAutoSimulating.value) {
      if (autoSimTimer) clearInterval(autoSimTimer)
      autoSimTimer = null
      isAutoSimulating.value = false
      lastOperation.value = '自动连续模拟已停止'
    } else {
      isAutoSimulating.value = true
      lastOperation.value = '🔁 自动连续模拟运行中 (每 2 秒变更一次)...'
      autoSimTimer = setInterval(() => {
        toggleBatchTaskStatus(3)
      }, 2000)
    }
  }

  onUnmounted(() => {
    if (autoSimTimer) {
      clearInterval(autoSimTimer)
      autoSimTimer = null
    }
  })

  function clearSimulatedTasks() {
    if (isAutoSimulating.value) {
      toggleAutoSimulation()
    }
    memoryBefore.value = getMemoryUsage()

    const startTime = performance.now()

    // 清除所有 sim- 开头的任务
    taskStore.tasks = {
      active: taskStore.tasks.active.filter(t => !t.gid.startsWith('sim-')),
      waiting: taskStore.tasks.waiting.filter(t => !t.gid.startsWith('sim-')),
      stopped: taskStore.tasks.stopped.filter(t => !t.gid.startsWith('sim-')),
    }

    renderTime.value = Math.round(performance.now() - startTime)
    memoryAfter.value = getMemoryUsage()
    lastOperation.value = '已清除所有模拟任务'
  }

  declare const gc: (() => void) | undefined

  function runGC() {
    if (typeof gc === 'function') {
      gc()
      memoryAfter.value = getMemoryUsage()
      lastOperation.value = '已触发 GC'
    } else {
      lastOperation.value = 'GC 不可用 (需要 --expose-gc 标志)'
    }
  }
</script>

<template>
  <div class="test-simulator">
    <div class="header">
      <span class="title">🧪 性能测试模拟器</span>
      <span class="subtitle">WebView 真实环境测试</span>
    </div>

    <div class="actions">
      <button :disabled="isRunning" class="btn" @click="injectDownloadsTasks(10, 'waiting')">
        +10 暂停任务
      </button>
      <button :disabled="isRunning" class="btn" @click="injectDownloadsTasks(10, 'active')">
        +10 活跃任务
      </button>
      <button :disabled="isRunning" class="btn" @click="injectTasks(100)">+100 任务</button>
      <button :disabled="isRunning" class="btn btn-danger" @click="injectTasks(1000)">
        +1000 任务
      </button>
    </div>

    <div class="actions">
      <button :disabled="isRunning" class="btn btn-primary" @click="shuffleTasks">
        🔀 随机重排
      </button>
      <button :disabled="isRunning" class="btn btn-primary" @click="toggleBatchTaskStatus(1)">
        ⚡ 变更单任务
      </button>
      <button :disabled="isRunning" class="btn btn-primary" @click="toggleBatchTaskStatus(5)">
        ⚡ 批量变更(5个)
      </button>
      <button
        class="btn"
        :class="isAutoSimulating ? 'btn-danger' : 'btn-primary'"
        @click="toggleAutoSimulation"
      >
        {{ isAutoSimulating ? '⏸ 停止自动模拟' : '🔁 自动连续模拟' }}
      </button>
    </div>

    <div class="actions">
      <button class="btn btn-secondary" @click="clearSimulatedTasks">清除模拟任务</button>
      <button class="btn btn-secondary" @click="runGC">触发 GC</button>
    </div>

    <div class="metrics">
      <div class="metric">
        <span class="label">渲染时间</span>
        <span class="value" :class="{ warning: renderTime > 100, danger: renderTime > 500 }">
          {{ renderTime }} ms
        </span>
      </div>
      <div class="metric">
        <span class="label">内存增量</span>
        <span
          class="value"
          :class="{ warning: Number(memoryDelta) > 20, danger: Number(memoryDelta) > 50 }"
        >
          {{ memoryDelta }} MB
        </span>
      </div>
      <div class="metric">
        <span class="label">当前内存</span>
        <span class="value">{{ currentMemory }} MB</span>
      </div>
      <div class="metric">
        <span class="label">Total 数量</span>
        <span class="value">{{ taskStore.allTasksCount }}</span>
      </div>
    </div>

    <div v-if="lastOperation" class="status">
      {{ lastOperation }}
    </div>
  </div>
</template>

<style scoped>
  .test-simulator {
    position: fixed;
    bottom: 80px;
    right: 16px;
    background: rgba(30, 30, 40, 0.95);
    border: 1px solid rgba(255, 255, 255, 0.1);
    border-radius: 12px;
    padding: 16px;
    min-width: 280px;
    z-index: 9999;
    backdrop-filter: blur(10px);
    font-family:
      system-ui,
      -apple-system,
      sans-serif;
  }

  .header {
    display: flex;
    flex-direction: column;
    margin-bottom: 12px;
  }

  .title {
    font-size: 14px;
    font-weight: 600;
    color: #fff;
  }

  .subtitle {
    font-size: 11px;
    color: rgba(255, 255, 255, 0.5);
  }

  .actions {
    display: flex;
    gap: 8px;
    margin-bottom: 12px;
    flex-wrap: wrap;
  }

  .btn {
    padding: 6px 12px;
    border: none;
    border-radius: 6px;
    background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
    color: white;
    font-size: 12px;
    cursor: pointer;
    transition: all 0.2s;
  }

  .btn:hover:not(:disabled) {
    transform: translateY(-1px);
    box-shadow: 0 4px 12px rgba(102, 126, 234, 0.4);
  }

  .btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .btn-secondary {
    background: rgba(255, 255, 255, 0.1);
  }

  .btn-danger {
    background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
  }

  .metrics {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
    margin-bottom: 12px;
  }

  .metric {
    background: rgba(255, 255, 255, 0.05);
    padding: 8px;
    border-radius: 6px;
  }

  .label {
    display: block;
    font-size: 10px;
    color: rgba(255, 255, 255, 0.5);
    margin-bottom: 2px;
  }

  .value {
    font-size: 14px;
    font-weight: 600;
    color: #4ade80;
  }

  .value.warning {
    color: #fbbf24;
  }

  .value.danger {
    color: #f87171;
  }

  .status {
    font-size: 11px;
    color: rgba(255, 255, 255, 0.7);
    text-align: center;
    padding: 8px;
    background: rgba(255, 255, 255, 0.05);
    border-radius: 6px;
  }
</style>
