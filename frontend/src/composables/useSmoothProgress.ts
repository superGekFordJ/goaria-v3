import { ref, onMounted, onUnmounted } from 'vue'

export interface DownloadStats {
  downloaded: number
  speed: number
  total: number
  status?: string // Add status to detect pauses immediately
}

export interface SmoothProgressConfig {
  emaAlpha: number
  smoothingFactor: number
  deviationDecay: number
  maxScaleDelta: number
  prematureCap: number
}

// Aria2 路径：事件推送频率 ~1s（轮询 + Pusher 防抖），需要较保守的平滑参数来掩盖低频抖动。
export const TASK_PROGRESS_CONFIG = {
  emaAlpha: 0.1,
  smoothingFactor: 0.1,
  deviationDecay: 0.07,
  maxScaleDelta: 0.009,
} as const satisfies Partial<SmoothProgressConfig>

// Surge 路径：事件推送频率 ~200ms（150ms 事件 + 50ms 防抖），
// 预测误差极小，偏差补偿不再需要；大幅提高 smoothingFactor 和 maxScaleDelta
// 让显示值快速追踪真实进度——200ms 间隔下修正力度大也不会被肉眼察觉。
export const SURGE_TASK_PROGRESS_CONFIG = {
  emaAlpha: 0.2,
  smoothingFactor: 0.6,
  deviationDecay: 0.0,
  maxScaleDelta: 0.008,
} as const satisfies Partial<SmoothProgressConfig>

const DEFAULT_CONFIG: SmoothProgressConfig = {
  emaAlpha: 0.1, // Speed smoothing factor (lower = smoother)
  smoothingFactor: 0.1, // Display value tracking factor (LERP)
  deviationDecay: 0.05, // Deviation compensation decay rate (lower = gentler correction)
  maxScaleDelta: 0.005, // Max scale change per frame (0.5%)
  prematureCap: 0.999, // Cap at 99.9% if not truly finished
}

export function useSmoothProgress(configOverrides: Partial<SmoothProgressConfig> = {}) {
  const config: SmoothProgressConfig = {
    ...DEFAULT_CONFIG,
    ...configOverrides,
  }

  // ... (state vars remain same) ...
  // Core state
  const displayDownloaded = ref(0) // Visually displayed progress (bytes)
  const realDownloaded = ref(0) // Real progress from backend (bytes)
  const totalBytes = ref(1) // Total size (initialized to 1 to prevent division by zero)

  // Smoothing state
  const smoothSpeed = ref(0) // EMA-smoothed speed
  let deviation = 0 // Accumulated prediction deviation
  let lastUpdateTimestamp = 0 // Timestamp of last backend update
  let rafId = 0 // Initialize safely

  // ... (updateLoop remains same) ...
  // Render loop (60FPS)
  const updateLoop = () => {
    const now = performance.now()

    if (smoothSpeed.value > 0) {
      // 1. Calculate Delta Time
      const elapsedSeconds = (now - lastUpdateTimestamp) / 1000

      // 2. Prediction using smoothed speed
      const predictedBytes = realDownloaded.value + smoothSpeed.value * elapsedSeconds

      // 3. Deviation compensation
      const deviationCompensation = deviation * config.deviationDecay
      const targetBytes = predictedBytes - deviationCompensation

      // 4. Max delta clamping
      const maxDelta = totalBytes.value * config.maxScaleDelta
      const rawDelta = targetBytes - displayDownloaded.value

      // Enforce Monotonicity: clampedDelta cannot be negative when downloading
      // This prevents "bounce back" even if we overshot. We just pause.
      const clampedDelta = Math.max(0, Math.min(rawDelta, maxDelta))

      // 5. Apply smoothed delta
      displayDownloaded.value += clampedDelta * config.smoothingFactor
    } else {
      // If speed is 0, allow backward movement only if correcting a large error
      // But generally we still prefer syncing smoothly without jumping back
      const maxDelta = totalBytes.value * config.maxScaleDelta
      const rawDelta = realDownloaded.value - displayDownloaded.value
      const clampedDelta = Math.max(-maxDelta, Math.min(rawDelta, maxDelta))
      displayDownloaded.value += clampedDelta * config.smoothingFactor
    }

    // Ensure display never exceeds 100% (or 99.9% if incomplete)
    let maxAllowed = totalBytes.value
    if (realDownloaded.value < totalBytes.value) {
      maxAllowed = totalBytes.value * config.prematureCap
    }
    displayDownloaded.value = Math.min(displayDownloaded.value, maxAllowed)

    rafId = requestAnimationFrame(updateLoop)
  }

  // Update function exposed to external components
  const updateStats = (stats: DownloadStats) => {
    const prevSpeed = smoothSpeed.value
    const prevReal = realDownloaded.value

    // Sanitize inputs
    const newDownloaded = Number(stats.downloaded) || 0
    let newSpeed = Number(stats.speed) || 0
    const newTotal = Math.max(Number(stats.total) || 0, 1) // Prevent zero total

    // STATUS OVERRIDE: If not active, force speed to 0 immediately
    // This fixes the lag between "pause" event and "speed=0" data
    if (stats.status && stats.status !== 'active') {
      newSpeed = 0
    }

    // Update real values
    realDownloaded.value = newDownloaded
    totalBytes.value = newTotal

    // Detect significant backward jumps (restart/reset)
    // If new downloaded is significantly less than previous (e.g. < 50%), immediate reset
    if (newDownloaded < prevReal * 0.5) {
      displayDownloaded.value = newDownloaded
      deviation = 0
      smoothSpeed.value = 0 // Reset speed smoothing
    }

    // EMA speed smoothing
    if (newSpeed === 0) {
      // Immediate cutoff if speed is 0 (paused/completed)
      // This prevents "ghost prediction" where prediction continues based on residual EMA speed
      smoothSpeed.value = 0
    } else if (prevSpeed === 0 || newDownloaded < prevReal) {
      // Cold start or reset: use new speed directly
      smoothSpeed.value = newSpeed
    } else {
      // Exponential Moving Average
      smoothSpeed.value = config.emaAlpha * newSpeed + (1 - config.emaAlpha) * prevSpeed
    }

    // Calculate deviation (actual progress vs expected progress)
    // Skip when deviationDecay=0 — deviation is never consumed
    if (config.deviationDecay > 0) {
      const elapsed = (performance.now() - lastUpdateTimestamp) / 1000
      if (elapsed > 0 && prevReal > 0 && newDownloaded >= prevReal) {
        const expectedProgress = prevSpeed * elapsed
        const actualProgress = newDownloaded - prevReal
        deviation = actualProgress - expectedProgress
      }
    }

    // Update tracking state
    lastUpdateTimestamp = performance.now()

    // Initialize display if this is the first update or reset
    if (displayDownloaded.value === 0 && newDownloaded > 0) {
      displayDownloaded.value = newDownloaded
      deviation = 0 // No deviation on first update
    }
  }

  onMounted(() => {
    lastUpdateTimestamp = performance.now()
    rafId = requestAnimationFrame(updateLoop)
  })

  onUnmounted(() => {
    if (rafId) {
      cancelAnimationFrame(rafId)
    }
  })

  return {
    displayDownloaded,
    totalBytes,
    smoothSpeed, // Exposed for debugging
    updateStats,
  }
}
