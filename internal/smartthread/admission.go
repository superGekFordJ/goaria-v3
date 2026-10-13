package smartthread

import (
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/speedstats"
)

// Admission-local quantity ceilings. They deliberately mirror the legacy
// cold-start thresholds and are not user-facing knobs.
const (
	// admissionColdScopeLimit refuses new admissions on a scope+env with no
	// measured bandwidth history once this many commitments already exist.
	admissionColdScopeLimit = 5
	// admissionExplorationDomainLimit caps commitments on a domain that has
	// no scope+env record (exploration candidates stay conservative).
	admissionExplorationDomainLimit = 5
)

// AdmissionBlock is a bitmask of the resource dimensions that blocked an
// admission candidate. It only carries useful blocking dimensions — it is
// not a parallel reason enum.
type AdmissionBlock uint8

const (
	// AdmissionBlockGlobal: global slot headroom exhausted.
	AdmissionBlockGlobal AdmissionBlock = 1 << iota
	// AdmissionBlockScope: scope+env bandwidth headroom (or cold-scope count).
	AdmissionBlockScope
	// AdmissionBlockDomain: domain exploration count, worker connections, or
	// domain bandwidth headroom.
	AdmissionBlockDomain
	// AdmissionBlockPhysical: physical NIC bandwidth headroom.
	AdmissionBlockPhysical
)

// AdmissionParams is the complete scalar input for one admission decision.
// The caller prepares the nine fields from a scheduler snapshot, occupancy,
// and configuration; the gate validates them and never reaches back into
// pool or tracker state.
type AdmissionParams struct {
	// Calc is the full Calculate input for the candidate; FileSize is the
	// remaining size hint (0 with SizeKnown means an empty/checkpoint file,
	// 0 with !SizeKnown means unknown size).
	Calc CalcParams
	// Upper is the caller-confirmed ceiling produced by the legacy/add path:
	// Split bounds the suggestion, MinSize/TargetBandwidth bound semantics.
	Upper ThreadParams
	// Smart selects the smart evaluation path; false keeps manual params.
	Smart bool
	// SizeKnown distinguishes a known remaining size (>=0, incl. 0) from an
	// unknown one (requires FileSize == 0).
	SizeKnown bool
	// ShortAvailable marks the candidate eligible for the short-download
	// exception (prepared by the caller from short-list qualification).
	ShortAvailable bool
	// SlotHeadroom is the remaining global admission budget for this round
	// (0..MaxConcurrentDownloadsUpper).
	SlotHeadroom int
	// ScopeEnvCommitted counts committed tasks sharing the candidate's exact
	// scope+env — the cold-scope guard input.
	ScopeEnvCommitted int
	// DomainCommitted counts committed tasks sharing scope|domain across all
	// envs (env-blind) — the exploration guard input.
	DomainCommitted int
	// DomainWorkerHeadroom is the base connection headroom for scope|domain
	// before this round's incremental reservations are deducted.
	DomainWorkerHeadroom int
}

// AdmissionDecision is the gate output. Allowed is the only admission
// criterion; a rejected decision always carries a zero Params.
type AdmissionDecision struct {
	Allowed bool
	// Blocked is the bitmask of blocking dimensions (zero on full allowance,
	// retained on a short exception so the caller knows what was short).
	Blocked AdmissionBlock
	// Params is the admitted parameter suggestion (zero when !Allowed).
	Params ThreadParams
	// Short marks a decision admitted only via the short-candidate exception.
	Short bool
}

// admissionResources is the internal measured view behind one smart
// evaluation: shared per-thread efficiency plus per-dimension
// known/available bandwidth. Unknown dimensions never block and never fake
// a zero budget.
type admissionResources struct {
	threadEfficiency  int64
	exploration       bool
	scopeKnown        bool
	scopeAvailable    int64
	domainKnown       bool
	domainAvailable   int64
	physicalKnown     bool
	physicalAvailable int64
}

// admissionResourcesFor measures the shared-resource dimensions for one
// candidate from speedstats and the ledger carried in calc. Efficiency uses
// the domain p75 → scope p75/2 chain and floors at minThreadEfficiency when
// no usable history exists.
func admissionResourcesFor(calc CalcParams) admissionResources {
	var res admissionResources
	eff, ok := threadEfficiency(calc.Domain, calc.Scope, calc.EnvKey)
	if !ok || eff < minThreadEfficiency {
		eff = minThreadEfficiency
	}
	res.threadEfficiency = eff
	res.exploration = !speedstats.HasDomainScopeEnvRecord(calc.Domain, calc.Scope, calc.EnvKey)

	if peak, ok := speedstats.GetGlobalPeak(calc.Scope, calc.EnvKey); ok {
		res.scopeKnown = true
		res.scopeAvailable = max(peak-calc.Ledger.Reserved(calc.Scope, calc.EnvKey), 0)
	}
	if peak, ok := speedstats.GetDomainPeak(calc.Domain, calc.Scope, calc.EnvKey); ok {
		res.domainKnown = true
		res.domainAvailable = max(peak-calc.Ledger.ReservedByDomain(calc.Scope, calc.Domain), 0)
	}
	if avail, ok := physicalHeadroom(calc); ok {
		res.physicalKnown = true
		res.physicalAvailable = avail
	}
	return res
}

// EvaluateAdmission evaluates one admission candidate against shared
// resource dimensions. It is a pure function: no pool, tracker, or config
// writes. Allowed is the sole admission criterion; rejections carry a zero
// Params and a Blocked mask of the dimensions that refused.
func EvaluateAdmission(p AdmissionParams) AdmissionDecision {
	var zero AdmissionDecision

	// --- 1. Basic input validation ---
	if p.SlotHeadroom < 0 || p.SlotHeadroom > config.MaxConcurrentDownloadsUpper {
		return zero
	}
	if p.Upper.Split < 1 || p.Upper.Split > config.MaxConnectionsUpper ||
		p.Upper.MinSize < 0 || p.Upper.TargetBandwidth < 0 {
		return zero
	}
	if p.SizeKnown {
		// Known size includes 0 (empty/checkpoint candidate); negative is
		// never a valid known remaining size.
		if p.Calc.FileSize < 0 {
			return zero
		}
	} else if p.Calc.FileSize != 0 {
		return zero
	}
	if p.Smart {
		if p.ScopeEnvCommitted < 0 || p.DomainCommitted < 0 || p.DomainWorkerHeadroom < 0 {
			return zero
		}
		if p.Calc.MaxConnections < 0 || p.Calc.MaxConnections > config.MaxConnectionsUpper {
			return zero
		}
	}

	// --- 2. Global slot count ---
	if p.SlotHeadroom == 0 {
		return AdmissionDecision{Blocked: AdmissionBlockGlobal}
	}

	// --- 3. Off-mode fast path: manual params pass through untouched ---
	if !p.Smart {
		return AdmissionDecision{Allowed: true, Params: p.Upper}
	}

	// --- 4. Smart preparation and local counts ---
	if (p.Calc.Scope != "wan" && p.Calc.Scope != "lan") || p.Calc.Domain == "" ||
		p.Calc.Ledger == nil || p.Calc.ActiveMACsFunc == nil || p.Calc.ComputeEnvKeyFunc == nil ||
		p.Calc.MinThreadLife <= 0 {
		return zero
	}
	res := admissionResourcesFor(p.Calc)
	if !res.scopeKnown && p.ScopeEnvCommitted >= admissionColdScopeLimit {
		return AdmissionDecision{Blocked: AdmissionBlockScope}
	}
	if res.exploration && p.DomainCommitted >= admissionExplorationDomainLimit {
		return AdmissionDecision{Blocked: AdmissionBlockDomain}
	}

	// --- 5. Connections: deduct this round's incremental reservations once ---
	connRemaining := max(p.DomainWorkerHeadroom-p.Calc.Ledger.ReservedWorkers(p.Calc.Scope, p.Calc.Domain), 0)
	if connRemaining == 0 {
		return AdmissionDecision{Blocked: AdmissionBlockDomain}
	}

	// --- 6. Compute the suggestion from the same ledger ---
	var suggest ThreadParams
	if p.SizeKnown && p.Calc.FileSize == 0 {
		// Known-empty candidate: one worker, no unknown-size branch.
		suggest = ThreadParams{Split: 1, NSat: 1, MinSize: p.Upper.MinSize, IsExploration: res.exploration}
	} else {
		calc := p.Calc
		calc.ReservedBandwidth = calc.Ledger.Reserved(calc.Scope, calc.EnvKey)
		calc.ReservedDomainBandwidth = calc.Ledger.ReservedByDomain(calc.Scope, calc.Domain)
		suggest = Calculate(calc)
	}

	// Clamp to the caller upper and remaining connections — never widen.
	split := max(min(min(suggest.Split, p.Upper.Split), connRemaining), 1)
	suggest.Split = split
	// MinSize: Upper 0 stays 0; a positive suggested value tightens, a
	// non-positive one defers to Upper.
	if p.Upper.MinSize == 0 {
		suggest.MinSize = 0
	} else if suggest.MinSize <= 0 || suggest.MinSize > p.Upper.MinSize {
		suggest.MinSize = p.Upper.MinSize
	}

	// --- 7. Bandwidth: independent known-dimension check ---
	var blocked AdmissionBlock
	if res.scopeKnown && res.scopeAvailable < res.threadEfficiency {
		blocked |= AdmissionBlockScope
	}
	if res.domainKnown && res.domainAvailable < res.threadEfficiency {
		blocked |= AdmissionBlockDomain
	}
	if res.physicalKnown && res.physicalAvailable < res.threadEfficiency {
		blocked |= AdmissionBlockPhysical
	}
	if blocked == 0 {
		suggest.TargetBandwidth = saturatingMulPositive(res.threadEfficiency, int64(split))
		return AdmissionDecision{Allowed: true, Params: suggest}
	}

	// --- 8. Short-candidate exception: only scope/physical may block ---
	if blocked&^(AdmissionBlockScope|AdmissionBlockPhysical) != 0 {
		return AdmissionDecision{Blocked: blocked}
	}
	if !p.ShortAvailable || !p.SizeKnown || p.Calc.FileSize < 0 {
		return AdmissionDecision{Blocked: blocked}
	}
	if p.Calc.FileSize > saturatingMulPositive(res.threadEfficiency, int64(progressLeaseTTL/time.Second)) {
		return AdmissionDecision{Blocked: blocked}
	}
	return AdmissionDecision{
		Allowed: true,
		Blocked: blocked,
		Short:   true,
		Params: ThreadParams{
			Split:           1,
			NSat:            1,
			MinSize:         suggest.MinSize,
			IsExploration:   res.exploration,
			TargetBandwidth: res.threadEfficiency,
		},
	}
}
