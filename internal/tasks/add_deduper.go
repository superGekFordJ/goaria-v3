package tasks

import (
	"context"
	"errors"
	"sync"
	"time"
)

// addClaimRecordTTL bounds how long a settled "target exists" result keeps
// coalescing same-key submissions. It deliberately exceeds the engine list
// cache window so a stale snapshot cannot reopen the race right after an add.
const addClaimRecordTTL = 10 * time.Second

// addDeduperSweepInterval throttles the activity-triggered full-table sweep
// so steady traffic does not Range the map on every add.
const addDeduperSweepInterval = time.Second

// errAddClaimAborted settles a claim whose owner unwound without publishing a
// result (e.g. panic). Fixed text; never carries request data.
var errAddClaimAborted = errors.New("add claim aborted")

// addClaim is one dedup-target admission token. The owner writes the result
// fields once and then closes done; waiters only read them after observing
// the close, which provides the publication happens-before.
type addClaim struct {
	key  string
	done chan struct{}

	gid       string
	err       error
	available bool
	expiresAt time.Time
}

// addClaimResult is the outcome a non-owner takes away from a settled claim,
// and what the owner hands to finish.
type addClaimResult struct {
	gid       string
	err       error
	available bool
}

// AddDeduper is a shared claim registry that coalesces concurrent adds for
// the same dedup target so at most one caller reaches the engine during an
// admission window. The zero value is ready to use; a nil *AddDeduper on
// Service keeps the legacy per-batch serialization.
type AddDeduper struct {
	claims sync.Map // key -> *addClaim

	sweepMu   sync.Mutex
	lastSweep time.Time
}

// acquire resolves the caller's position for key. It returns (claim, zero,
// true) when the caller owns a fresh claim and must settle it via finish;
// otherwise it returns the result the caller must consume — an available
// duplicate marker with its known gid, or a real failure (the owner's
// published error, or ctx.Err() when the caller's context ended first).
// Waiters consume the token they attached to, never a map re-lookup.
func (d *AddDeduper) acquire(ctx context.Context, key string) (*addClaim, addClaimResult, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, addClaimResult{err: err}, false
	}
	d.sweepExpired()
	for {
		fresh := &addClaim{key: key, done: make(chan struct{})}
		actual, loaded := d.claims.LoadOrStore(key, fresh)
		if !loaded {
			return fresh, addClaimResult{}, true
		}
		claim := actual.(*addClaim)
		select {
		case <-claim.done:
			if res, consumable := claim.resultForNewCaller(); consumable {
				return nil, res, false
			}
			// Stale residue: remove only this exact token (ABA), then retry.
			d.claims.CompareAndDelete(key, claim)
			continue
		case <-ctx.Done():
			return nil, addClaimResult{err: ctx.Err()}, false
		default:
		}
		// In-flight token: attach as waiter without holding any lock.
		select {
		case <-claim.done:
			if res, consumable := claim.resultForAttachedWaiter(); consumable {
				return nil, res, false
			}
			d.claims.CompareAndDelete(key, claim)
		case <-ctx.Done():
			return nil, addClaimResult{err: ctx.Err()}, false
		}
	}
}

// finish publishes res on c and wakes every attached waiter. Available
// results stay for addClaimRecordTTL from settlement; failures release the
// key immediately so a later submit can retry while old waiters still read
// this token's published error.
func (d *AddDeduper) finish(c *addClaim, res addClaimResult) {
	c.gid = res.gid
	c.err = res.err
	c.available = res.available
	if res.available {
		c.expiresAt = time.Now().Add(addClaimRecordTTL)
		close(c.done)
		return
	}
	d.claims.CompareAndDelete(c.key, c)
	close(c.done)
}

// resultForNewCaller interprets a token that was already settled when it was
// loaded. Only a live available record is consumable; anything else is
// residue the caller may remove and race past, since failures release the
// key for retry rather than handing a new caller someone else's error.
func (c *addClaim) resultForNewCaller() (addClaimResult, bool) {
	if c.available && time.Now().Before(c.expiresAt) {
		return addClaimResult{gid: c.gid, available: true}, true
	}
	return addClaimResult{}, false
}

// resultForAttachedWaiter consumes the outcome of the token this caller
// attached to while it was in-flight: the owner's published result always
// applies, including its failure.
func (c *addClaim) resultForAttachedWaiter() (addClaimResult, bool) {
	if c.available {
		if time.Now().Before(c.expiresAt) {
			return addClaimResult{gid: c.gid, available: true}, true
		}
		return addClaimResult{}, false
	}
	err := c.err
	if err == nil {
		err = errAddClaimAborted
	}
	return addClaimResult{err: err}, true
}

// sweepExpired is the lazy, activity-triggered cleanup: at most once per
// interval it drops tokens that are both settled and stale. In-flight and
// fresh records are never touched; removal is token-identity checked.
func (d *AddDeduper) sweepExpired() {
	if !d.sweepMu.TryLock() {
		return
	}
	defer d.sweepMu.Unlock()
	now := time.Now()
	if now.Sub(d.lastSweep) < addDeduperSweepInterval {
		return
	}
	d.lastSweep = now
	d.claims.Range(func(key, value any) bool {
		claim := value.(*addClaim)
		select {
		case <-claim.done:
			if !claim.available || !now.Before(claim.expiresAt) {
				d.claims.CompareAndDelete(key, claim)
			}
		default:
		}
		return true
	})
}
