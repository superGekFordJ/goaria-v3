package speedstats

// ResetRecordsForTest clears all in-memory records and cancels any pending
// save timer. For use by external test packages only.
func ResetRecordsForTest() {
	saveTimerMu.Lock()
	if saveTimer != nil {
		saveTimer.Stop()
		saveTimer = nil
	}
	saveTimerMu.Unlock()

	mu.Lock()
	records = nil
	mu.Unlock()
}
