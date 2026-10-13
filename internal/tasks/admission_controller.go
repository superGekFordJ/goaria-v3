package tasks

import (
	"strconv"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/surge/scheduler"
)

// admissionTickInterval is the fallback scan period. Event-driven Kick edges
// carry the latency; the ticker only guarantees a lost notification cannot
// strand a queued opportunity.
const admissionTickInterval = 2 * time.Second

// admissionEntry is the loop-owned per-opportunity record: the tracker ticket
// (zero until a live reservation exists), classification scalars used to count
// committed work per scope/domain, and the per-key short-qualification latch.
type admissionEntry struct {
	ticket    monitor.AdmissionTicket
	scope     string
	domain    string
	envKey    string
	shortUsed bool
}

// AdmissionController is the single resident admission loop for the managed
// Surge pool: FIFO snapshot → nine-field gate → Reserve → Admit → in-round
// accounting. It owns no second scheduler, queue, or recovery machinery; one
// goroutine serializes every decision.
type AdmissionController struct {
	engine *rpc.SurgeEngine

	kickCh chan struct{}
	stopCh chan struct{}
	doneCh chan struct{}

	mu      sync.Mutex
	started bool
	stopped bool

	// entries is owned exclusively by the controller goroutine; all access
	// happens inside evaluate on that goroutine.
	entries map[scheduler.AdmissionKey]*admissionEntry
}

func NewAdmissionController(engine *rpc.SurgeEngine) *AdmissionController {
	return &AdmissionController{
		engine:  engine,
		kickCh:  make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		entries: make(map[scheduler.AdmissionKey]*admissionEntry),
	}
}

// Kick requests one admission round. Capacity-1 and non-blocking: redundant
// notifications merge into a single pending wake.
func (c *AdmissionController) Kick() {
	if c == nil {
		return
	}
	select {
	case c.kickCh <- struct{}{}:
	default:
	}
}

// Start spawns the sole controller goroutine. Idempotent; a stopped instance
// never restarts.
func (c *AdmissionController) Start() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started || c.stopped {
		return
	}
	c.started = true
	go c.run()
}

// Stop signals the loop and joins the goroutine when it was started. Calling
// Stop on a never-started controller returns immediately.
func (c *AdmissionController) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	join := c.started
	c.mu.Unlock()
	close(c.stopCh)
	if join {
		<-c.doneCh
	}
}

func (c *AdmissionController) run() {
	defer close(c.doneCh)
	c.Kick() // first scan does not wait for the first tick
	ticker := time.NewTicker(admissionTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-c.kickCh:
		case <-ticker.C:
		}
		if c.stoppedNow() {
			return
		}
		c.evaluate(time.Now())
	}
}

// stoppedNow reports whether Stop was requested; checked before every commit
// so a returned Stop guarantees no further Admit calls.
func (c *AdmissionController) stoppedNow() bool {
	select {
	case <-c.stopCh:
		return true
	default:
		return false
	}
}

func scopeEnvBucket(scope, envKey string) string { return scope + "|" + envKey }
func scopeDomainBucket(scope, domain string) string {
	return scope + "|" + domain
}

// BuildOccupancyTaskInfosExcluding is BuildOccupancyTaskInfos with one GID
// filtered out so a self-evaluating waiter does not double-count its own
// reservation against itself.
func BuildOccupancyTaskInfosExcluding(excludeGID string) []smartthread.TrackedTaskInfo {
	infos := BuildOccupancyTaskInfos()
	if excludeGID == "" || len(infos) == 0 {
		return infos
	}
	out := make([]smartthread.TrackedTaskInfo, 0, len(infos))
	for _, info := range infos {
		if info.GID == excludeGID {
			continue
		}
		out = append(out, info)
	}
	return out
}

// ExistingDomainWorkersFromTelemetryExcluding matches
// ExistingDomainWorkersFromTelemetry but skips excludeGID so the candidate's
// own ThreadCount is not treated as peer occupancy.
func ExistingDomainWorkersFromTelemetryExcluding(scope, domain, excludeGID string) int {
	if scope == "" || domain == "" {
		return 0
	}
	tr := monitor.State.GetTracker()
	if tr == nil {
		return 0
	}
	occupied := tr.GetOccupancyTrackedTasks()
	mon := monitor.State.GetMonitor()
	var telemetry *monitor.TelemetryCache
	if mon != nil {
		telemetry = mon.GetTelemetry()
	}
	total := 0
	for _, t := range occupied {
		if excludeGID != "" && t.GID == excludeGID {
			continue
		}
		if t.Scope != scope || t.Domain != domain {
			continue
		}
		snapCount := 0
		if telemetry != nil {
			snapCount = len(telemetry.Get(t.GID))
		}
		n := max(t.ThreadCount, snapCount)
		total += n
	}
	return total
}

// admissionPrep is one evaluatable candidate with its frozen round inputs.
type admissionPrep struct {
	cand          scheduler.AdmissionCandidate
	entry         *admissionEntry
	gid           string
	pending       bool // tracker still holds a live reservation for this key
	pendingCeil   int  // retained reservation's ThreadCount ceiling; 0 = none
	scope         string
	domain        string
	envKey        string
	sizeKnown     bool
	remaining     int64
	shortAvail    bool
	scopeEnvCount int
	domainCount   int
	headroom      int
	ledger        *smartthread.BandwidthLedger
	workerHead    int
}

// reconcileTickets drops self-held entries whose opportunity vanished from
// both the candidate queue and the busy set, conditionally releasing the
// reservation they created. A dead key can never release a newer token.
func (c *AdmissionController) reconcileTickets(snap scheduler.AdmissionSnapshot, tracker *monitor.TaskTracker) {
	live := make(map[scheduler.AdmissionKey]bool, len(snap.Candidates)+len(snap.Busy))
	for _, cand := range snap.Candidates {
		live[cand.Key] = true
	}
	for _, slot := range snap.Busy {
		live[slot.Key] = true
	}
	for key, entry := range c.entries {
		if live[key] {
			continue
		}
		if entry.ticket.GID != "" {
			tracker.ReleaseAdmission(entry.ticket.GID, entry.ticket.Token)
		}
		delete(c.entries, key)
	}
}

// accountAccepted records a newly-live reservation once: common round ledger
// plus every self-excluding view that does not belong to this gid.
func accountReservation(ledger *smartthread.BandwidthLedger, views map[string]*smartthread.BandwidthLedger, gid, scope, domain, envKey string, params smartthread.ThreadParams) {
	apply := func(l *smartthread.BandwidthLedger) {
		if l == nil {
			return
		}
		l.Reserve(scope, envKey, params.TargetBandwidth)
		l.ReserveByDomain(scope, domain, params.TargetBandwidth)
		l.ReserveWorkers(scope, domain, params.Split)
	}
	apply(ledger)
	for viewGID, view := range views {
		if viewGID != gid {
			apply(view)
		}
	}
}

// evaluate runs one admission round: reconcile self-held tickets, classify
// committed work, freeze occupancy inputs, then scan the complete FIFO queue.
// Everything runs on the controller goroutine.
func (c *AdmissionController) evaluate(now time.Time) {
	if c.engine == nil {
		return
	}
	snap := c.engine.AdmissionSnapshot()
	if !snap.Managed || snap.Stopping {
		return
	}
	cfgPtr := config.Get()
	if cfgPtr == nil {
		return
	}
	cfg := *cfgPtr
	tracker := monitor.State.GetTracker()
	if tracker == nil {
		return
	}

	c.reconcileTickets(snap, tracker)

	// Committed work: admitted-but-unclaimed candidates plus every Busy claim,
	// deduped by key and classified through the retained entry scalars.
	scopeEnvCommitted := make(map[string]int)
	domainCommitted := make(map[string]int)
	seenCommitted := make(map[scheduler.AdmissionKey]bool)
	countCommitted := func(key scheduler.AdmissionKey) {
		if seenCommitted[key] {
			return
		}
		seenCommitted[key] = true
		e := c.entries[key]
		if e == nil {
			// A restart drops in-memory entries while committed keys survive
			// in the snapshot; resolve classification through the tracker row
			// so a foreign-window claim never escapes the scope/domain caps.
			if scope, domain, envKey, ok := tracker.GetScopeAndEnv("sg_" + key.ID); ok {
				scopeEnvCommitted[scopeEnvBucket(scope, envKey)]++
				domainCommitted[scopeDomainBucket(scope, domain)]++
			}
			return
		}
		scopeEnvCommitted[scopeEnvBucket(e.scope, e.envKey)]++
		domainCommitted[scopeDomainBucket(e.scope, e.domain)]++
	}
	for _, cand := range snap.Candidates {
		if cand.Admitted && !cand.InFlight {
			countCommitted(cand.Key)
		}
	}
	for _, slot := range snap.Busy {
		countCommitted(slot.Key)
	}

	maxConn, _ := strconv.Atoi(cfg.MaxConnections)
	if maxConn <= 0 {
		maxConn = 8
	}
	if maxConn > config.MaxConnectionsUpper {
		maxConn = config.MaxConnectionsUpper
	}
	smart := cfg.SmartThreadMode
	slotCap := snap.Capacity
	if !smart {
		// Off is a manual policy: the user's own count normalized to 1..32 is
		// the global cap. Smart scope/domain/short checks stay disabled.
		userMax, _ := strconv.Atoi(config.EffectiveMaxConcurrentDownloads(cfg))
		if userMax >= 1 && userMax < slotCap {
			slotCap = userMax
		}
	}

	// Short-qualification holders: one live holder per scope+env bucket,
	// derived from the entry latch plus the shared lease anchors. An expired
	// holder releases the bucket for a different key; the same key may not
	// re-qualify through it again.
	shortHolders := make(map[string]scheduler.AdmissionKey)
	for key, entry := range c.entries {
		if !entry.shortUsed || entry.ticket.GID == "" {
			continue
		}
		tt, ok := tracker.GetTrackedTask(entry.ticket.GID)
		if !ok || !smartthread.TargetLeaseActive(tt.ActivatedAt, tt.FirstProgressAt, now) {
			continue
		}
		bucket := scopeEnvBucket(entry.scope, entry.envKey)
		if _, taken := shortHolders[bucket]; !taken {
			shortHolders[bucket] = key
		}
	}

	// Prep pass over the complete FIFO order: monitor-accepted waiting rows
	// with finished metadata only. No I/O, probing, or DNS here.
	var queue []admissionPrep
	for _, cand := range snap.Candidates {
		if cand.Admitted || cand.InFlight {
			continue
		}
		if !cand.RetryAt.IsZero() && now.Before(cand.RetryAt) {
			continue
		}
		if !cand.RuntimeReady {
			continue
		}
		gid := "sg_" + cand.Key.ID
		tt, ok := tracker.GetTrackedTask(gid)
		if !ok || tt.Status != "waiting" {
			continue
		}
		scope, domain, envKey, ok := tracker.GetScopeAndEnv(gid)
		if !ok {
			// Add-path metadata writes are still settling; try next round.
			continue
		}
		entry := c.entries[cand.Key]
		if entry == nil {
			entry = &admissionEntry{}
			c.entries[cand.Key] = entry
		}
		entry.scope, entry.domain, entry.envKey = scope, domain, envKey

		prep := admissionPrep{
			cand:   cand,
			entry:  entry,
			gid:    gid,
			scope:  scope,
			domain: domain,
			envKey: envKey,
		}
		if ticket, isPending := tracker.PendingAdmissionTicket(gid); isPending && ticket.Token == cand.Key.Token {
			prep.pending = true
			prep.pendingCeil = tt.ThreadCount
		} else if entry.ticket.GID != "" {
			// The reservation fact ended at a monitor boundary; the map keeps
			// no phantom ticket.
			entry.ticket = monitor.AdmissionTicket{}
		}
		if cand.TotalSize > 0 {
			prep.sizeKnown = true
			prep.remaining = cand.TotalSize - cand.Downloaded
			if prep.remaining < 0 {
				prep.remaining = 0
			}
		}
		queue = append(queue, prep)
	}
	if len(queue) == 0 {
		return
	}

	// Frozen occupancy inputs, captured before any Reserve this round: one
	// common seed ledger, self-excluding views for retained pendings, and
	// per-domain worker baselines (ThreadCount vs live telemetry, no
	// bandwidth>0 filter).
	commonLedger := smartthread.NewBandwidthLedgerAt(BuildOccupancyTaskInfos(), now)
	views := make(map[string]*smartthread.BandwidthLedger)
	domainBaseline := make(map[string]int)
	baselineKey := func(p admissionPrep) string {
		if p.pending {
			return scopeDomainBucket(p.scope, p.domain) + "\x00" + p.gid
		}
		return scopeDomainBucket(p.scope, p.domain)
	}
	for _, p := range queue {
		key := baselineKey(p)
		if _, ok := domainBaseline[key]; ok {
			continue
		}
		if p.pending {
			if views[p.gid] == nil {
				views[p.gid] = smartthread.NewBandwidthLedgerAt(BuildOccupancyTaskInfosExcluding(p.gid), now)
			}
			domainBaseline[key] = ExistingDomainWorkersFromTelemetryExcluding(p.scope, p.domain, p.gid)
		} else {
			domainBaseline[key] = ExistingDomainWorkersFromTelemetry(p.scope, p.domain)
		}
	}

	netEnv := monitor.State.GetNetEnv()
	limits := smartthread.GetDefaultServerLimits()
	accepted := 0

	for _, p := range queue {
		if c.stoppedNow() {
			return
		}
		headroom := slotCap - snap.CommittedSlots - accepted
		if headroom <= 0 {
			// Global slot budget exhausted ends the round; local rejections
			// never stop the scan of other domains.
			return
		}

		upper := smartthread.ThreadParams{
			Split:   min(p.cand.Workers, maxConn),
			MinSize: p.cand.MinChunkSize,
		}
		if p.pending && p.pendingCeil > 0 && upper.Split > p.pendingCeil {
			// Retained reservations only tighten; never widen an old ticket's
			// connection estimate.
			upper.Split = p.pendingCeil
		}
		if upper.Split < 1 {
			continue
		}

		budget := maxConn
		if nMax, ok := limits.GetNMax(scopeDomainBucket(p.scope, p.domain)); ok && nMax > 0 {
			budget = nMax
		}
		workerHead := budget - domainBaseline[baselineKey(p)]
		if workerHead < 0 {
			workerHead = 0
		}

		shortAvail := false
		if smart && p.sizeKnown {
			bucket := scopeEnvBucket(p.scope, p.envKey)
			holder, held := shortHolders[bucket]
			switch {
			case p.entry.shortUsed && holder == p.cand.Key:
				// Same key continues its own live qualification.
				shortAvail = true
			case !held && !p.entry.shortUsed:
				shortAvail = true
			}
		}

		workLedger := commonLedger
		if p.pending {
			if view := views[p.gid]; view != nil {
				workLedger = view
			}
		}

		decision := smartthread.EvaluateAdmission(smartthread.AdmissionParams{
			Calc: smartthread.CalcParams{
				FileSize:       p.remaining,
				MaxConnections: maxConn,
				Scope:          p.scope,
				Domain:         p.domain,
				EnvKey:         p.envKey,
				Ledger:         workLedger,
				MinThreadLife:  cfg.MinThreadLife,
				ActiveMACsFunc: func() []string {
					if netEnv == nil {
						return nil
					}
					return netEnv.GetAllActiveMACs()
				},
				ComputeEnvKeyFunc: monitor.ComputeEnvKey,
			},
			Upper:                upper,
			Smart:                smart,
			SizeKnown:            p.sizeKnown,
			ShortAvailable:       shortAvail,
			SlotHeadroom:         headroom,
			ScopeEnvCommitted:    scopeEnvCommitted[scopeEnvBucket(p.scope, p.envKey)],
			DomainCommitted:      domainCommitted[scopeDomainBucket(p.scope, p.domain)],
			DomainWorkerHeadroom: workerHead,
		})
		if !decision.Allowed {
			continue
		}

		baseline := p.cand.Downloaded
		if baseline < 0 {
			baseline = 0
		}
		ticket, created := tracker.ReserveAdmission(p.gid, p.cand.Key.Token, decision.Params, now, baseline, true)
		if ticket.GID == "" {
			// Row left waiting between snapshot and reserve; no Admit.
			continue
		}
		p.entry.ticket = ticket
		switch c.engine.Admit(p.cand.Key, decision.Params.Split, decision.Params.MinSize) {
		case scheduler.AdmitAccepted:
			if created {
				// R1: account the new live reservation exactly once.
				accountReservation(commonLedger, views, p.gid, p.scope, p.domain, p.envKey, decision.Params)
				if decision.Short {
					p.entry.shortUsed = true
				}
			}
			// R4 when !created: retained ticket accepted — already in seed.
			accepted++
			scopeEnvCommitted[scopeEnvBucket(p.scope, p.envKey)]++
			domainCommitted[scopeDomainBucket(p.scope, p.domain)]++
		case scheduler.AdmitStale:
			if created {
				// R2: release only the reservation created this round; the
				// late release can never touch a newer token.
				tracker.ReleaseAdmission(ticket.GID, ticket.Token)
				p.entry.ticket = monitor.AdmissionTicket{}
				p.entry.shortUsed = false
			}
			// R5 when !created: not this round's ticket — next-round
			// reconciliation retires it. Either way the round ends here.
			return
		case scheduler.AdmitAlreadyAdmitted:
			// R6: no release, no renew, no accounting; committed counts are
			// re-derived from a fresh snapshot next round.
			return
		case scheduler.AdmitUnavailable:
			if created {
				// R3: keep the ticket for the next round and account its live
				// occupancy once — it is already a real tracker reservation.
				accountReservation(commonLedger, views, p.gid, p.scope, p.domain, p.envKey, decision.Params)
				if decision.Short {
					p.entry.shortUsed = true
				}
			}
			// R7 when !created: retained ticket keeps waiting. Both paths end
			// the round; a fact Kick or the 2s ticker drives the retry.
			return
		}
	}
}
