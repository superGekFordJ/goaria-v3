package smartthread

// ActiveBandwidthFunc returns the current total download speed for a given scope.
// This is injected from outside (monitor.ActiveBandwidthByScope) to avoid an
// import cycle between smartthread and monitor.
type ActiveBandwidthFunc func(scope string) int64

// noActiveBandwidth is the default when no injector is set.
func noActiveBandwidth(scope string) int64 { return 0 }

var activeBandwidthProvider ActiveBandwidthFunc = noActiveBandwidth

// SetActiveBandwidthProvider injects the real bandwidth query function.
// Called once at startup from app initialization (after monitor is ready).
func SetActiveBandwidthProvider(fn ActiveBandwidthFunc) {
	if fn == nil {
		fn = noActiveBandwidth
	}
	activeBandwidthProvider = fn
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
type BandwidthLedger struct {
	reserved map[string]int64
}

// NewBandwidthLedger creates a ledger seeded with current active bandwidth
// per scope. This ensures the first task in a batch sees the real-time
// bandwidth pressure from already-running downloads.
func NewBandwidthLedger() *BandwidthLedger {
	return &BandwidthLedger{
		reserved: map[string]int64{
			"wan": activeBandwidthProvider("wan"),
			"lan": activeBandwidthProvider("lan"),
		},
	}
}

// Reserved returns the total reserved bandwidth for the given scope
// (active baseline + batch-accumulated reservations).
func (l *BandwidthLedger) Reserved(scope string) int64 {
	if l == nil {
		return 0
	}
	if scope == "" {
		scope = "wan"
	}
	return l.reserved[scope]
}

// Reserve adds bandwidth to the scope's running total.
func (l *BandwidthLedger) Reserve(scope string, bandwidth int64) {
	if l == nil || bandwidth <= 0 {
		return
	}
	if scope == "" {
		scope = "wan"
	}
	l.reserved[scope] += bandwidth
}
