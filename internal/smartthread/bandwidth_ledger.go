package smartthread

import "sync"

// ActiveBandwidthFunc returns the current total download speed for a given scope+envKey.
// This is injected from outside (monitor.ActiveBandwidthByScope) to avoid an
// import cycle between smartthread and monitor.
type ActiveBandwidthFunc func(scope, envKey string) int64

// noActiveBandwidth is the default when no injector is set.
func noActiveBandwidth(scope, envKey string) int64 { return 0 }

var (
	// activeBandwidthProvider is read by the convergence tick without a lock.
	// This is safe only because sync.Once guarantees a single write (via
	// SetActiveBandwidthProvider) that happens-before all subsequent reads.
	// If this is ever changed to support hot-swapping, it must become atomic
	// or mutex-guarded.
	activeBandwidthProvider     ActiveBandwidthFunc = noActiveBandwidth
	activeBandwidthProviderOnce sync.Once
)

// SetActiveBandwidthProvider injects the real bandwidth query function.
// Called once at startup from app initialization (after monitor is ready).
func SetActiveBandwidthProvider(fn ActiveBandwidthFunc) {
	activeBandwidthProviderOnce.Do(func() {
		if fn == nil {
			fn = noActiveBandwidth
		}
		activeBandwidthProvider = fn
	})
}

// BandwidthLedger tracks per-scope reserved bandwidth within a batch add session.
// It seeds from the real-time active bandwidth (monitor.ActiveBandwidthByScope)
// at construction time, then accumulates per-task TargetBandwidth as tasks are
// calculated.
//
// Usage:
//
//	ledger := NewBandwidthLedger()
//	for _, candidate := range batch {
//	    reserved := ledger.Reserved(scope)
//	    params := Calculate(CalcParams{..., ReservedBandwidth: reserved})
//	    ledger.Reserve(scope, params.TargetBandwidth)
//	}
//
// BandwidthLedger is accessed during batch-add (single goroutine) but its
// Reserved/Reserve methods are also safe for concurrent use — the mutex was
// added to guard against the convergence tick reading
// activeBandwidthProvider while a batch add is in progress.
type BandwidthLedger struct {
	mu       sync.Mutex
	reserved map[string]int64
}

// NewBandwidthLedger creates a ledger seeded with current active bandwidth
// per scope+envKey. The activeTasks parameter provides the set of currently
// running tasks for pre-scan seeding (pre-scan, not lazy init).
func NewBandwidthLedger(activeTasks []TrackedTaskInfo) *BandwidthLedger {
	l := &BandwidthLedger{
		reserved: make(map[string]int64),
	}
	seen := make(map[string]bool)
	for _, t := range activeTasks {
		if t.Scope == "" {
			continue
		}
		key := t.Scope + t.EnvKey
		if seen[key] {
			continue
		}
		seen[key] = true
		l.reserved[key] = activeBandwidthProvider(t.Scope, t.EnvKey)
	}
	return l
}

// Reserved returns the total reserved bandwidth for the given scope+envKey
// (active baseline + batch-accumulated reservations).
func (l *BandwidthLedger) Reserved(scope, envKey string) int64 {
	if l == nil {
		return 0
	}
	if scope == "" {
		scope = "wan"
	}
	key := scope + envKey
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reserved[key]
}

// Reserve adds bandwidth to the scope+envKey running total.
func (l *BandwidthLedger) Reserve(scope, envKey string, bandwidth int64) {
	if l == nil || bandwidth <= 0 {
		return
	}
	if scope == "" {
		scope = "wan"
	}
	key := scope + envKey
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reserved[key] += bandwidth
}
