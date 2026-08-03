package orchestrator

// reserveDiskBytes debits size against later enqueue prechecks until the
// download reaches a terminal lifecycle event (complete / error / removed).
func (mgr *LifecycleManager) reserveDiskBytes(id string, size int64) {
	if mgr == nil || id == "" || size <= 0 {
		return
	}
	mgr.diskReserveMu.Lock()
	defer mgr.diskReserveMu.Unlock()
	if mgr.diskReserveByID == nil {
		mgr.diskReserveByID = make(map[string]int64)
	}
	mgr.diskReserveByID[id] = size
}

// releaseDiskBytes clears a debit (idempotent).
func (mgr *LifecycleManager) releaseDiskBytes(id string) {
	if mgr == nil || id == "" {
		return
	}
	mgr.diskReserveMu.Lock()
	defer mgr.diskReserveMu.Unlock()
	delete(mgr.diskReserveByID, id)
}

func (mgr *LifecycleManager) pendingDiskReserved() int64 {
	if mgr == nil {
		return 0
	}
	mgr.diskReserveMu.Lock()
	defer mgr.diskReserveMu.Unlock()
	var sum int64
	for _, size := range mgr.diskReserveByID {
		sum += size
	}
	return sum
}
