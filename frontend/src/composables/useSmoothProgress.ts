import { ref, onMounted, onUnmounted } from 'vue';

export interface DownloadStats {
  downloaded: number;
  speed: number;
  total: number;
  status?: string; // Add status to detect pauses immediately
}

export function useSmoothProgress() {
  // ... (state vars remain same) ...
  // Core state
  const displayDownloaded = ref(0); // Visually displayed progress (bytes)
  const realDownloaded = ref(0);    // Real progress from backend (bytes)
  const totalBytes = ref(1);        // Total size (initialized to 1 to prevent division by zero)

  // Smoothing state
  const smoothSpeed = ref(0);       // EMA-smoothed speed
  let deviation = 0;                // Accumulated prediction deviation
  let lastUpdateTimestamp = 0;      // Timestamp of last backend update
  let rafId = 0;                    // Initialize safely

  // Algorithm parameters
  const EMA_ALPHA = 0.1;            // Speed smoothing factor (lower = smoother)
  const SMOOTHING_FACTOR = 0.1;     // Display value tracking factor (LERP)
  const DEVIATION_DECAY = 0.05;     // Deviation compensation decay rate (lower = gentler correction)
  const MAX_SCALE_DELTA = 0.005;    // Max scale change per frame (0.5%)
  const PREMATURE_CAP = 0.999;      // Cap at 99.9% if not truly finished

  // ... (updateLoop remains same) ...
  // Render loop (60FPS)
  const updateLoop = () => {
    const now = performance.now();

    if (smoothSpeed.value > 0) {
      // 1. Calculate Delta Time
      const elapsedSeconds = (now - lastUpdateTimestamp) / 1000;

      // 2. Prediction using smoothed speed
      const predictedBytes = realDownloaded.value + (smoothSpeed.value * elapsedSeconds);

      // 3. Deviation compensation
      const deviationCompensation = deviation * DEVIATION_DECAY;
      const targetBytes = predictedBytes - deviationCompensation;

      // 4. Max delta clamping
      const maxDelta = totalBytes.value * MAX_SCALE_DELTA;
      const rawDelta = targetBytes - displayDownloaded.value;
      
      // Enforce Monotonicity: clampedDelta cannot be negative when downloading
      // This prevents "bounce back" even if we overshot. We just pause.
      const clampedDelta = Math.max(0, Math.min(rawDelta, maxDelta));

      // 5. Apply smoothed delta
      displayDownloaded.value += clampedDelta * SMOOTHING_FACTOR;
    } else {
      // If speed is 0, allow backward movement only if correcting a large error
      // But generally we still prefer syncing smoothly without jumping back
      const maxDelta = totalBytes.value * MAX_SCALE_DELTA;
      const rawDelta = realDownloaded.value - displayDownloaded.value;
      const clampedDelta = Math.max(-maxDelta, Math.min(rawDelta, maxDelta));
      displayDownloaded.value += clampedDelta * SMOOTHING_FACTOR;
    }

    // Ensure display never exceeds 100% (or 99.9% if incomplete)
    let maxAllowed = totalBytes.value;
    if (realDownloaded.value < totalBytes.value) {
        maxAllowed = totalBytes.value * PREMATURE_CAP;
    }
    displayDownloaded.value = Math.min(displayDownloaded.value, maxAllowed);

    rafId = requestAnimationFrame(updateLoop);
  };

  // Update function exposed to external components
  const updateStats = (stats: DownloadStats) => {
    const prevSpeed = smoothSpeed.value;
    const prevReal = realDownloaded.value;

    // Sanitize inputs
    const newDownloaded = Number(stats.downloaded) || 0;
    let newSpeed = Number(stats.speed) || 0;
    const newTotal = Math.max(Number(stats.total) || 0, 1); // Prevent zero total

    // STATUS OVERRIDE: If not active, force speed to 0 immediately
    // This fixes the lag between "pause" event and "speed=0" data
    if (stats.status && stats.status !== 'active') {
        newSpeed = 0;
    }

    // Update real values
    realDownloaded.value = newDownloaded;
    totalBytes.value = newTotal;

    // Detect significant backward jumps (restart/reset)
    // If new downloaded is significantly less than previous (e.g. < 50%), immediate reset
    if (newDownloaded < prevReal * 0.5) {
        displayDownloaded.value = newDownloaded;
        deviation = 0;
        smoothSpeed.value = 0; // Reset speed smoothing
    }

    // EMA speed smoothing
    if (newSpeed === 0) {
        // Immediate cutoff if speed is 0 (paused/completed)
        // This prevents "ghost prediction" where prediction continues based on residual EMA speed
        smoothSpeed.value = 0;
    } else if (prevSpeed === 0 || newDownloaded < prevReal) {
      // Cold start or reset: use new speed directly
      smoothSpeed.value = newSpeed;
    } else {
      // Exponential Moving Average
      smoothSpeed.value = EMA_ALPHA * newSpeed + (1 - EMA_ALPHA) * prevSpeed;
    }

    // Calculate deviation (actual progress vs expected progress)
    const elapsed = (performance.now() - lastUpdateTimestamp) / 1000;
    if (elapsed > 0 && prevReal > 0 && newDownloaded >= prevReal) {
      const expectedProgress = prevSpeed * elapsed;
      const actualProgress = newDownloaded - prevReal;
      deviation = actualProgress - expectedProgress;
    }

    // Update tracking state
    lastUpdateTimestamp = performance.now();

    // Initialize display if this is the first update or reset
    if (displayDownloaded.value === 0 && newDownloaded > 0) {
      displayDownloaded.value = newDownloaded;
      deviation = 0; // No deviation on first update
    }
  };

  onMounted(() => {
    lastUpdateTimestamp = performance.now();
    rafId = requestAnimationFrame(updateLoop);
  });

  onUnmounted(() => {
    if (rafId) {
      cancelAnimationFrame(rafId);
    }
  });

  return {
    displayDownloaded,
    totalBytes,
    smoothSpeed, // Exposed for debugging
    updateStats
  };
}
