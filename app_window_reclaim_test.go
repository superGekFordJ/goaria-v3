package main

import (
	"sync/atomic"
	"testing"
	"time"
)

func restoreWindowReclaimDefaults(t *testing.T, a *App) {
	t.Helper()
	origDelay := windowReclaimDelay
	origFn := windowReclaimFn
	origEnabled := windowReclaimEnabled
	origHeadless := windowReclaimHeadless
	t.Cleanup(func() {
		if a != nil {
			a.cancelWindowReclaim()
		}
		windowReclaimDelay = origDelay
		windowReclaimFn = origFn
		windowReclaimEnabled = origEnabled
		windowReclaimHeadless = origHeadless
	})
}

func waitReclaimCount(t *testing.T, count *atomic.Int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count.Load() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("reclaim count = %d, want %d within %s", count.Load(), want, timeout)
}

func TestWindowReclaim_Debounce(t *testing.T) {
	a := &App{}
	restoreWindowReclaimDefaults(t, a)
	windowReclaimEnabled = func() bool { return true }
	windowReclaimDelay = 30 * time.Millisecond
	windowReclaimHeadless = func(*App) bool { return true }

	var count atomic.Int32
	windowReclaimFn = func() { count.Add(1) }

	a.scheduleWindowReclaim()
	a.scheduleWindowReclaim()
	a.scheduleWindowReclaim()

	waitReclaimCount(t, &count, 1, 200*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if got := count.Load(); got != 1 {
		t.Fatalf("reclaim fired %d times, want 1", got)
	}
}

func TestWindowReclaim_Cancel(t *testing.T) {
	a := &App{}
	restoreWindowReclaimDefaults(t, a)
	windowReclaimEnabled = func() bool { return true }
	windowReclaimDelay = 40 * time.Millisecond
	windowReclaimHeadless = func(*App) bool { return true }

	var count atomic.Int32
	windowReclaimFn = func() { count.Add(1) }

	a.scheduleWindowReclaim()
	a.cancelWindowReclaim()

	time.Sleep(100 * time.Millisecond)
	if got := count.Load(); got != 0 {
		t.Fatalf("reclaim fired %d times after cancel, want 0", got)
	}
}

func TestWindowReclaim_GateSkipsWhenWindowPresent(t *testing.T) {
	a := &App{}
	restoreWindowReclaimDefaults(t, a)
	windowReclaimEnabled = func() bool { return true }
	windowReclaimDelay = 20 * time.Millisecond
	windowReclaimHeadless = func(*App) bool { return false }

	var count atomic.Int32
	windowReclaimFn = func() { count.Add(1) }

	a.scheduleWindowReclaim()

	time.Sleep(80 * time.Millisecond)
	if got := count.Load(); got != 0 {
		t.Fatalf("reclaim fired %d times while window present, want 0", got)
	}
}

func TestWindowReclaim_HappyHeadlessFire(t *testing.T) {
	a := &App{}
	restoreWindowReclaimDefaults(t, a)
	windowReclaimEnabled = func() bool { return true }
	windowReclaimDelay = 20 * time.Millisecond
	// Default headless probe: a.window == nil
	windowReclaimHeadless = func(a *App) bool {
		a.windowMu.Lock()
		headless := a.window == nil
		a.windowMu.Unlock()
		return headless
	}

	var count atomic.Int32
	windowReclaimFn = func() { count.Add(1) }

	// window nil → headless
	a.scheduleWindowReclaim()

	waitReclaimCount(t, &count, 1, 200*time.Millisecond)
}

func TestWindowReclaim_DisabledNoOp(t *testing.T) {
	a := &App{}
	restoreWindowReclaimDefaults(t, a)
	windowReclaimEnabled = func() bool { return false }
	windowReclaimDelay = 20 * time.Millisecond
	windowReclaimHeadless = func(*App) bool { return true }

	var count atomic.Int32
	windowReclaimFn = func() { count.Add(1) }

	a.scheduleWindowReclaim()

	time.Sleep(80 * time.Millisecond)
	if got := count.Load(); got != 0 {
		t.Fatalf("reclaim fired %d times when disabled, want 0", got)
	}
	a.reclaimMu.Lock()
	timerSet := a.reclaimTimer != nil
	a.reclaimMu.Unlock()
	if timerSet {
		t.Fatal("disabled schedule should not arm a timer")
	}
}

func TestWindowReclaim_CreateWindowCancelsPending(t *testing.T) {
	a := &App{}
	restoreWindowReclaimDefaults(t, a)
	windowReclaimEnabled = func() bool { return true }
	windowReclaimDelay = 50 * time.Millisecond
	windowReclaimHeadless = func(*App) bool { return true }

	var count atomic.Int32
	windowReclaimFn = func() { count.Add(1) }

	a.scheduleWindowReclaim()
	// CreateWindow entry cancel even when a.app == nil (early return path).
	a.CreateWindow()

	time.Sleep(120 * time.Millisecond)
	if got := count.Load(); got != 0 {
		t.Fatalf("reclaim fired %d times after CreateWindow cancel, want 0", got)
	}
}

func TestWindowReclaim_DestroyWindowSchedulesOnlyOnSuccess(t *testing.T) {
	a := &App{}
	restoreWindowReclaimDefaults(t, a)
	windowReclaimEnabled = func() bool { return true }
	windowReclaimDelay = 20 * time.Millisecond
	windowReclaimHeadless = func(*App) bool { return true }

	var count atomic.Int32
	windowReclaimFn = func() { count.Add(1) }

	// window nil → early destroy return
	a.DestroyWindow()

	time.Sleep(80 * time.Millisecond)
	if got := count.Load(); got != 0 {
		t.Fatalf("early DestroyWindow scheduled reclaim (%d fires), want 0", got)
	}
}

// Stale AfterFunc (Stop returned false) must not nil a newer timer, or cancel silently fails.
func TestWindowReclaim_StaleCallbackDoesNotOrphanNewerTimer(t *testing.T) {
	a := &App{}
	restoreWindowReclaimDefaults(t, a)
	windowReclaimEnabled = func() bool { return true }
	windowReclaimDelay = 40 * time.Millisecond
	windowReclaimHeadless = func(*App) bool { return true }

	var count atomic.Int32
	windowReclaimFn = func() { count.Add(1) }

	a.scheduleWindowReclaim() // newer timer armed

	// Simulate a previous timer's callback still running after reschedule.
	a.runWindowReclaim(windowReclaimFn, windowReclaimHeadless)
	waitReclaimCount(t, &count, 1, 200*time.Millisecond)

	a.cancelWindowReclaim()
	time.Sleep(100 * time.Millisecond)
	if got := count.Load(); got != 1 {
		t.Fatalf("stale callback orphaned newer timer: reclaim count=%d, want 1", got)
	}
}

func TestWindowReclaim_DefaultFnNoPanic(t *testing.T) {
	restoreWindowReclaimDefaults(t, nil)
	// Smoke: real GC pair must not panic.
	windowReclaimFn()
}
