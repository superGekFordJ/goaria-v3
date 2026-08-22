package smartthread

import (
	"strings"
	"sync"
	"time"
)

// ActiveBandwidthFunc returns the current total download speed for a given scope+envKey.
// This is injected from outside (monitor.MacroBandwidthByScope) to avoid an
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
	// Note: NewBandwidthLedger no longer lumps this provider into the seed
	// (hybrid per-task seed replaces it). Residual callers may still inject it.
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
// It seeds from hybrid per-task occupancy (TargetBandwidth while cold, telemetry
// once ready/aged), then accumulates per-task TargetBandwidth as tasks are
// calculated. Domain occupancy is tracked separately in reservedByDomain.
//
// BatchAddURI shares one ledger across up to 12 concurrent submit goroutines.
// Callers must hold WithAlloc across Reserved* → Calculate → Clamp → Reserve*
// so domain/global claims cannot TOCTOU. Release* on AddUri failure may run
// outside the alloc lock.
//
// Usage:
//
//	ledger := NewBandwidthLedger(activeTasks)
//	ledger.WithAlloc(func() {
//	    reserved := ledger.Reserved(scope, envKey)
//	    domainReserved := ledger.ReservedByDomain(scope, domain)
//	    params := Calculate(CalcParams{..., ReservedBandwidth: reserved,
//	        ReservedDomainBandwidth: domainReserved})
//	    params = ClampToServerLimit(params, ...)
//	    ledger.Reserve(scope, envKey, params.TargetBandwidth)
//	    ledger.ReserveByDomain(scope, domain, params.TargetBandwidth)
//	    ledger.ReserveWorkers(scope, domain, params.Split)
//	})
type BandwidthLedger struct {
	allocMu          sync.Mutex // serializes claim path for concurrent batch submit
	mu               sync.Mutex // protects map fields
	reserved         map[string]int64
	reservedByDomain map[string]int64 // key = scope|domain
	reservedWorkers  map[string]int   // key = scope|domain, batch-accumulated worker reservations
}

// WithAlloc runs fn while holding the allocation lock. Nil ledger runs fn
// without locking (Resume / tests with no batch ledger).
func (l *BandwidthLedger) WithAlloc(fn func()) {
	if fn == nil {
		return
	}
	if l == nil {
		fn()
		return
	}
	l.allocMu.Lock()
	defer l.allocMu.Unlock()
	fn()
}

// NewBandwidthLedger creates a ledger seeded with hybrid per-task occupancy.
// Each task contributes independently (no once-per-key MacroBandwidth lump).
func NewBandwidthLedger(activeTasks []TrackedTaskInfo) *BandwidthLedger {
	l := &BandwidthLedger{
		reserved:         make(map[string]int64),
		reservedByDomain: make(map[string]int64),
		reservedWorkers:  make(map[string]int),
	}
	now := time.Now()
	for _, t := range activeTasks {
		if t.Scope == "" {
			continue
		}
		contrib := hybridSeedContrib(t, now)
		if contrib <= 0 {
			continue
		}
		key := t.Scope + t.EnvKey
		l.reserved[key] += contrib
		if t.Domain != "" {
			l.reservedByDomain[limitKey(t.Scope, t.Domain)] += contrib
		}
	}
	return l
}

// hybridSeedContrib returns the occupancy contribution for one task.
// Surge: !MacroReady → max(Target, telem); MacroReady → telem.
// Aria2: within aria2ColdSeedWindow → max(Target, telem); else telem.
func hybridSeedContrib(t TrackedTaskInfo, now time.Time) int64 {
	telem := max(t.TelemetryBps, 0)
	target := max(t.TargetBandwidth, 0)
	if strings.HasPrefix(t.GID, "sg_") {
		if !t.MacroReady {
			return max64(target, telem)
		}
		return telem
	}
	if !t.AllocatedAt.IsZero() && now.Sub(t.AllocatedAt) < aria2ColdSeedWindow {
		return max64(target, telem)
	}
	return telem
}

// Reserved returns the total reserved bandwidth for the given scope+envKey
// (hybrid seed baseline + batch-accumulated reservations).
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

// Release subtracts bandwidth from the scope+envKey running total.
// Called when AddUri fails. Floor at 0.
func (l *BandwidthLedger) Release(scope, envKey string, bandwidth int64) {
	if l == nil || bandwidth <= 0 {
		return
	}
	if scope == "" {
		scope = "wan"
	}
	key := scope + envKey
	l.mu.Lock()
	defer l.mu.Unlock()
	v := max(l.reserved[key]-bandwidth, 0)
	l.reserved[key] = v
}

// ReservedByDomain returns reserved bandwidth for the given scope+domain.
func (l *BandwidthLedger) ReservedByDomain(scope, domain string) int64 {
	if l == nil {
		return 0
	}
	if scope == "" || domain == "" {
		return 0
	}
	key := limitKey(scope, domain)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reservedByDomain[key]
}

// ReserveByDomain adds bandwidth to the scope+domain running total.
func (l *BandwidthLedger) ReserveByDomain(scope, domain string, bandwidth int64) {
	if l == nil || bandwidth <= 0 {
		return
	}
	if scope == "" || domain == "" {
		return
	}
	key := limitKey(scope, domain)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reservedByDomain[key] += bandwidth
}

// ReleaseByDomain subtracts bandwidth from the scope+domain running total.
// Floor at 0.
func (l *BandwidthLedger) ReleaseByDomain(scope, domain string, bandwidth int64) {
	if l == nil || bandwidth <= 0 {
		return
	}
	if scope == "" || domain == "" {
		return
	}
	key := limitKey(scope, domain)
	l.mu.Lock()
	defer l.mu.Unlock()
	v := max(l.reservedByDomain[key]-bandwidth, 0)
	l.reservedByDomain[key] = v
}

// ReservedWorkers returns the batch-accumulated worker reservations for the
// given scope+domain. Used by ClampToServerLimit to prevent TOCTOU oversell
// when multiple concurrent goroutines each query existingDomainWorkers before
// telemetry updates.
func (l *BandwidthLedger) ReservedWorkers(scope, domain string) int {
	if l == nil {
		return 0
	}
	if scope == "" || domain == "" {
		return 0
	}
	key := limitKey(scope, domain)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reservedWorkers[key]
}

// ReserveWorkers adds count to the batch-accumulated worker reservations for
// the given scope+domain. Called after ClampToServerLimit to record the
// actual split that will be launched.
func (l *BandwidthLedger) ReserveWorkers(scope, domain string, count int) {
	if l == nil || count <= 0 {
		return
	}
	if scope == "" || domain == "" {
		return
	}
	key := limitKey(scope, domain)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reservedWorkers[key] += count
}

// ReleaseWorkers subtracts count from the batch-accumulated worker
// reservations. Called when AddUri fails and the reserved quota should be
// returned to the pool. Floor at 0.
func (l *BandwidthLedger) ReleaseWorkers(scope, domain string, count int) {
	if l == nil || count <= 0 {
		return
	}
	if scope == "" || domain == "" {
		return
	}
	key := limitKey(scope, domain)
	l.mu.Lock()
	defer l.mu.Unlock()
	v := max(l.reservedWorkers[key]-count, 0)
	l.reservedWorkers[key] = v
}
