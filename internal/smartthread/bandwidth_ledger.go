package smartthread

import (
	"math"
	"strings"
	"sync"
	"time"
)

// ActiveBandwidthFunc returns the unified occupancy sum for a scope+envKey
// bucket evaluated at the given instant. Injected from monitor to avoid an
// import cycle between smartthread and monitor.
type ActiveBandwidthFunc func(scope, envKey string, now time.Time) int64

// noActiveBandwidth is the default when no injector is set.
func noActiveBandwidth(scope, envKey string, now time.Time) int64 { return 0 }

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

// NewBandwidthLedger creates a ledger seeded with the unified per-task
// occupancy evaluated at a single captured instant.
func NewBandwidthLedger(activeTasks []TrackedTaskInfo) *BandwidthLedger {
	return NewBandwidthLedgerAt(activeTasks, time.Now())
}

// NewBandwidthLedgerAt seeds the ledger evaluating every task's occupancy at
// the same instant. Each task contributes independently (no once-per-key
// MacroBandwidth lump). Scope sums stay env-exact; domain sums stay
// environment-blind under scope|domain.
func NewBandwidthLedgerAt(activeTasks []TrackedTaskInfo, now time.Time) *BandwidthLedger {
	l := &BandwidthLedger{
		reserved:         make(map[string]int64),
		reservedByDomain: make(map[string]int64),
		reservedWorkers:  make(map[string]int),
	}
	for _, t := range activeTasks {
		if t.Scope == "" {
			continue
		}
		contrib := TaskOccupancy(t, now)
		if contrib <= 0 {
			continue
		}
		key := t.Scope + t.EnvKey
		l.reserved[key] = saturatingAdd64(l.reserved[key], contrib)
		if t.Domain != "" {
			lk := limitKey(t.Scope, t.Domain)
			l.reservedByDomain[lk] = saturatingAdd64(l.reservedByDomain[lk], contrib)
		}
	}
	return l
}

// saturatingAdd64 adds non-negative occupancy values without overflow into
// the negative range.
func saturatingAdd64(a, b int64) int64 {
	if b < 0 {
		return a
	}
	sum := a + b
	if sum < a {
		return math.MaxInt64
	}
	return sum
}

// taskOccupancyTelemetry selects the single measured-throughput input for a
// task. Surge tasks with a latched macroReady use the raw engine sample —
// including a measured zero, which never falls back to cache. All other
// tasks use the cache speed when it is a valid measurement. Missing or
// invalid measurements contribute zero; they are distinct from a measured 0
// only through the caller's validity flag, never through bps>0 inference.
func taskOccupancyTelemetry(t TrackedTaskInfo) int64 {
	if strings.HasPrefix(t.GID, "sg_") && t.MacroReady {
		return max(t.RawBps, 0)
	}
	if t.TelemetryValid {
		return max(t.TelemetryBps, 0)
	}
	return 0
}

// claimContribution is the compat/lease formula shared by every claim source:
// max(max(Target, 0), telem). It never stacks Target on top of telem.
func claimContribution(t TrackedTaskInfo, telem int64) int64 {
	return max(max(t.TargetBandwidth, 0), telem)
}

// activationLeaseActive reports whether the real Target floor still holds at
// now: up to activationNoProgressTTL without growth, or progressLeaseTTL
// past a first growth recorded inside the initial window. Invalid instants
// (now before activation, first-progress before activation or in the future)
// never grant a floor; a first growth at/after the initial TTL does not
// revive the lease.
func activationLeaseActive(activatedAt, firstProgressAt, now time.Time) bool {
	if activatedAt.IsZero() || now.Before(activatedAt) {
		return false
	}
	if firstProgressAt.IsZero() || firstProgressAt.Before(activatedAt) || firstProgressAt.After(now) {
		return now.Sub(activatedAt) < activationNoProgressTTL
	}
	if firstProgressAt.Sub(activatedAt) >= activationNoProgressTTL {
		return false
	}
	return now.Sub(firstProgressAt) < progressLeaseTTL
}

// addPlaceholderLive reports whether the bounded Aria2 confirmed-add claim is
// still inside its window. A zero anchor is not a valid placeholder.
func addPlaceholderLive(anchor, now time.Time) bool {
	if anchor.IsZero() || now.Before(anchor) {
		return false
	}
	return now.Sub(anchor) < aria2AddPlaceholderWindow
}

// TaskOccupancy is the single per-task occupancy formula shared by ledger
// seeding, the scope provider, domain aggregation, release/settle, and
// Calculate inputs. Order of evaluation: eligibility/claim source first,
// then one telemetry pick, then exactly one contribution.
//
// Real active tasks contribute telem, upgraded to the claim formula while a
// lease/placeholder/compat claim is live. Ordinary waiting/paused and
// terminal tasks contribute zero. Explicit AdmissionReserved inputs
// contribute their reservation regardless of status (scalar expression
// only — no production writer or gate exists yet).
func TaskOccupancy(t TrackedTaskInfo, now time.Time) int64 {
	if t.GID == "" {
		return 0
	}
	if t.Status == "complete" || t.Status == "error" {
		return 0
	}
	telem := taskOccupancyTelemetry(t)
	if t.AdmissionReserved {
		// Explicit admitted-not-started reuses ActivatedAt as its anchor and
		// the same 45s/15s lease: while live the reservation claims its
		// Target floor regardless of the (non-terminal) list status — a
		// waiting/admitted row counts without entering processTask. Lease
		// expiry drops the contribution, never the dispatch identity; with
		// no anchor the row falls through to ordinary eligibility so a
		// retained waiting claim can still apply.
		if activationLeaseActive(t.ActivatedAt, t.FirstProgressAt, now) {
			return claimContribution(t, telem)
		}
	}
	surge := strings.HasPrefix(t.GID, "sg_")
	switch t.Status {
	case "waiting":
		// Surge keeps the retained queued claim (never lease-expired);
		// authoritative Aria2 waiting is immediately zero even when a
		// synthetic-active placeholder or cache speed still exists.
		if surge && t.TargetBandwidth > 0 {
			return claimContribution(t, telem)
		}
		return 0
	case "paused":
		// Only the resume-hold compat lineage claims occupancy while paused;
		// an add-time claim that merely transitioned to paused, ordinary or
		// long-paused tasks, and non-Surge paused tasks contribute zero.
		if surge && t.CompatClaim == CompatSurgeResume {
			return claimContribution(t, telem)
		}
		return 0
	case "active":
		if activationLeaseActive(t.ActivatedAt, t.FirstProgressAt, now) {
			return claimContribution(t, telem)
		}
		if surge && (t.CompatClaim == CompatSurgeClaim || t.CompatClaim == CompatSurgeResume) {
			return claimContribution(t, telem)
		}
		if !surge && t.CompatClaim == CompatAria2Add && addPlaceholderLive(t.AddAnchorAt, now) {
			return claimContribution(t, telem)
		}
		return telem
	case "":
		// Pre-status placeholder: the Target itself is the Surge claim
		// evidence; Aria2 needs the bounded confirmed-add marker.
		if t.TargetBandwidth <= 0 {
			return 0
		}
		if surge {
			return claimContribution(t, telem)
		}
		if t.CompatClaim == CompatAria2Add && addPlaceholderLive(t.AddAnchorAt, now) {
			return claimContribution(t, telem)
		}
		return 0
	default:
		return 0
	}
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
