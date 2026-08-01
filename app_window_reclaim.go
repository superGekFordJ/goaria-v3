package main

import (
	"runtime"
	"runtime/debug"
	"time"
)

// Windows tray reclaim: debounced GC + FreeOSMemory after successful DestroyWindow.
// Cancel on CreateWindow; never clear TaskCache / Tracker / Monitor.
var (
	windowReclaimDelay   = 2 * time.Second
	windowReclaimFn      = func() { runtime.GC(); debug.FreeOSMemory() }
	windowReclaimEnabled = func() bool { return runtime.GOOS == "windows" }
	// windowReclaimHeadless defaults to a.window == nil under windowMu; injectable for tests.
	windowReclaimHeadless = func(a *App) bool {
		a.windowMu.Lock()
		headless := a.window == nil
		a.windowMu.Unlock()
		return headless
	}
)

// scheduleWindowReclaim arms a single AfterFunc reclaim (Windows only). Safe under windowMu.
func (a *App) scheduleWindowReclaim() {
	if !windowReclaimEnabled() {
		return
	}

	a.reclaimMu.Lock()
	defer a.reclaimMu.Unlock()

	if a.reclaimTimer != nil {
		a.reclaimTimer.Stop()
	}
	a.reclaimTimer = time.AfterFunc(windowReclaimDelay, a.runWindowReclaim)
}

// cancelWindowReclaim stops any pending reclaim timer (best-effort).
func (a *App) cancelWindowReclaim() {
	a.reclaimMu.Lock()
	defer a.reclaimMu.Unlock()

	if a.reclaimTimer != nil {
		a.reclaimTimer.Stop()
		a.reclaimTimer = nil
	}
}

// runWindowReclaim gates on headless (window nil) then runs reclaim off windowMu.
// Do not clear reclaimTimer here: a stale AfterFunc (Stop returned false) must not
// wipe a newer timer armed by a later scheduleWindowReclaim.
func (a *App) runWindowReclaim() {
	if !windowReclaimHeadless(a) {
		return
	}

	windowReclaimFn()
}
