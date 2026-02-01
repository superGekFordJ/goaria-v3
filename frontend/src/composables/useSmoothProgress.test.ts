import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// Track cleanup callbacks to simulate onUnmounted
const cleanupCallbacks: Array<() => void> = [];

// Mock lifecycle hooks
vi.mock('vue', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue')>();
  return {
    ...actual,
    onMounted: (fn: () => void) => fn(),
    onUnmounted: (fn: () => void) => cleanupCallbacks.push(fn),
  };
});

import { useSmoothProgress } from './useSmoothProgress';

describe('useSmoothProgress', () => {
  let requestAnimationFrameMock: ReturnType<typeof vi.fn>;
  let cancelAnimationFrameMock: ReturnType<typeof vi.fn>;
  let now: number;
  let rafCallback: FrameRequestCallback | null = null;
  let rafIdCounter = 0;

  beforeEach(() => {
    vi.useFakeTimers();
    now = 1000;
    rafIdCounter = 0;
    cleanupCallbacks.length = 0; // Reset cleanup callbacks

    // Mock performance.now
    vi.spyOn(performance, 'now').mockImplementation(() => now);

    // Mock RAF
    rafCallback = null;
    requestAnimationFrameMock = vi.fn((cb) => {
      rafCallback = cb;
      rafIdCounter++;
      return rafIdCounter;
    });
    cancelAnimationFrameMock = vi.fn();

    vi.stubGlobal('requestAnimationFrame', requestAnimationFrameMock);
    vi.stubGlobal('cancelAnimationFrame', cancelAnimationFrameMock);
  });

  afterEach(() => {
    // Execute cleanup to stop RAF loops
    cleanupCallbacks.forEach(cb => cb());
    cleanupCallbacks.length = 0;
    
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  // Helper to advance RAF loop
  const advanceFrame = (deltaMs: number) => {
    now += deltaMs;
    if (rafCallback) {
        // execute the callback
        const cb = rafCallback;
        rafCallback = null; // consume
        cb(now);
    }
  };

  it('should initialize with default values', () => {
    const { displayDownloaded, totalBytes, smoothSpeed } = useSmoothProgress();
    expect(displayDownloaded.value).toBe(0);
    expect(totalBytes.value).toBe(1);
    expect(smoothSpeed.value).toBe(0);
  });

  it('should update stats instantly if display is 0', () => {
    const { displayDownloaded, totalBytes, updateStats } = useSmoothProgress();

    updateStats({
        downloaded: 100,
        speed: 10,
        total: 1000
    });

    expect(displayDownloaded.value).toBe(100);
    expect(totalBytes.value).toBe(1000);
  });

  it('should smooth progress when speed > 0', async () => {
    const { displayDownloaded, updateStats } = useSmoothProgress();

    // Initial state
    updateStats({ downloaded: 100, speed: 50, total: 1000 });
    expect(displayDownloaded.value).toBe(100);

    // Simulate one frame update after 1 second
    advanceFrame(1000);

    // displayDownloaded should have increased but lag behind target
    expect(displayDownloaded.value).toBeGreaterThan(100);
    expect(displayDownloaded.value).toBeLessThan(150);
  });

  it('should smoothly sync to real value when speed is 0', () => {
    const { displayDownloaded, updateStats } = useSmoothProgress();

    // Set initial
    updateStats({ downloaded: 100, speed: 50, total: 1000 });

    // Simulate some progress locally (e.g. up to 120)
    displayDownloaded.value = 120;

    // Now backend says we paused at 150
    updateStats({ downloaded: 150, speed: 0, total: 1000 });

    // Advance frame
    advanceFrame(100);

    // Should move towards 150 but respect max delta (0.5% of 1000 = 5)
    // With LERP factor 0.1, movement should be gradual
    expect(displayDownloaded.value).toBeGreaterThan(120);
    expect(displayDownloaded.value).toBeLessThan(150);
  });

  it('should apply EMA speed smoothing', () => {
    const { smoothSpeed, updateStats } = useSmoothProgress();

    // First update - cold start, use speed directly
    updateStats({ downloaded: 100, speed: 100, total: 1000 });
    expect(smoothSpeed.value).toBe(100);

    // Second update - EMA should blend
    // EMA: 0.1 * 50 + 0.9 * 100 = 5 + 90 = 95
    advanceFrame(500); // Advance time for proper elapsed calculation
    updateStats({ downloaded: 150, speed: 50, total: 1000 });
    expect(smoothSpeed.value).toBeCloseTo(95, 0);
  });

  it('should limit max scale change per frame (prevents jitter)', () => {
    const { displayDownloaded, updateStats } = useSmoothProgress();

    // Start at known position
    updateStats({ downloaded: 500, speed: 100, total: 1000 });
    expect(displayDownloaded.value).toBe(500);

    // Simulate a sudden jump backwards (speed drop scenario)
    advanceFrame(100);

    // Force display ahead of target to simulate prediction overshoot
    displayDownloaded.value = 600;

    // Update with much lower actual value
    updateStats({ downloaded: 510, speed: 10, total: 1000 });
    advanceFrame(16); // One frame

    // Display should NOT jump back (monotonicity enforcement)
    // Even though prediction overshoot, we just pause (delta clamped to 0)
    expect(displayDownloaded.value).toBeGreaterThanOrEqual(600);
  });

  it('should cap at 99.9% if incomplete', () => {
    const { displayDownloaded, updateStats } = useSmoothProgress();

    // 99.9% complete but not "completed" status (implied by real < total)
    const total = 1000;
    const limit = total * 0.999;
    
    updateStats({ downloaded: limit - 10, speed: 1000, total });

    // Advance frames to push prediction beyond total
    for (let i = 0; i < 20; i++) {
        advanceFrame(100);
    }

    // Should be capped exactly at 999 (99.9% of 1000)
    expect(displayDownloaded.value).toBeLessThanOrEqual(limit);
    // And allow to reach 100% only when realDownloaded reaches total
    
    // Now finish it
    updateStats({ downloaded: total, speed: 0, total });
    advanceFrame(100);
    expect(displayDownloaded.value).toBeLessThanOrEqual(total);
  });

  it('should never exceed 100% progress', () => {
    const { displayDownloaded, updateStats } = useSmoothProgress();

    updateStats({ downloaded: 950, speed: 100, total: 1000 });

    // Advance multiple frames with high speed prediction
    for (let i = 0; i < 60; i++) {
      advanceFrame(100);
    }

    // Display should never exceed total (1000 bytes)
    expect(displayDownloaded.value).toBeLessThanOrEqual(1000);
  });

  it('should cleanup RAF on unmount', () => {
    const { displayDownloaded } = useSmoothProgress();
    
    // Initial RAF should be requested
    expect(requestAnimationFrameMock).toHaveBeenCalled();
    const lastRafId = requestAnimationFrameMock.mock.results[0].value;

    // Trigger cleanup (simulate unmount)
    cleanupCallbacks.forEach(cb => cb());
    
    expect(cancelAnimationFrameMock).toHaveBeenCalledWith(lastRafId);
  });

  it('should stop prediction immediately when speed becomes 0 (fix pause overshoot)', () => {
    const { displayDownloaded, smoothSpeed, updateStats } = useSmoothProgress();

    // 1. High speed download
    updateStats({ downloaded: 100, speed: 1000, total: 2000 });
    expect(smoothSpeed.value).toBe(1000);

    // 2. Sudden pause
    updateStats({ downloaded: 100, speed: 0, total: 2000 });
    
    // EMA/Smooth speed should be cut to 0 instantly, not decayed
    expect(smoothSpeed.value).toBe(0);

    // 3. Advance frames
    advanceFrame(100);
    
    // Display should NOT increase (because realDownloaded is still 100)
    // It should stay close to 100
    expect(displayDownloaded.value).toBe(100);
  });

  it('should force speed to 0 when status is paused even if speed > 0 (status override)', () => {
    const { smoothSpeed, updateStats } = useSmoothProgress();

    // 1. Downloading
    updateStats({ downloaded: 100, speed: 1000, total: 2000, status: 'active' });
    expect(smoothSpeed.value).toBe(1000);

    // 2. Pause Event (Backend reports 'paused', but Speed might still be stale/non-zero)
    updateStats({ downloaded: 120, speed: 1000, total: 2000, status: 'paused' });

    // 3. Expect immediate kill
    expect(smoothSpeed.value).toBe(0);
  });
});
