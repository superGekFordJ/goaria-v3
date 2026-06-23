package concurrent

import (
	"sync"

	"goaria-v3/internal/surge/engine/types"
)

// FORK-PATCH: TieredBufferPool provides speed-adaptive buffer pooling with
// cap-filter protection. Replaces the fixed-size sync.Pool that
// could leak memory from oversized pinned slices (Go Issue #23199).
// Three tiers map to download speed ranges, reducing WriteAt syscall
// frequency for high-speed downloads while saving memory for low-speed ones.

type bufferTier int

const (
	tierSmall  bufferTier = iota // 32KB  — < 10MB/s
	tierMedium                   // 512KB — 10–50MB/s (current default)
	tierLarge                    // 1MB   — > 50MB/s
	tierCount                    // number of tiers
)

var tierSizes = [tierCount]int{
	tierSmall:  32 * types.KB,
	tierMedium: 512 * types.KB,
	tierLarge:  1 * types.MB,
}

// Speed thresholds for tier selection (bytes/sec).
const (
	tierSpeedLow  = 10 * float64(types.MB) // below → tierSmall
	tierSpeedHigh = 50 * float64(types.MB) // above → tierLarge
)

// TieredBufferPool manages three sync.Pool instances, one per buffer tier.
// Buffers are stored as *[]byte (pointer to slice) to avoid interface
// allocation on Get/Put.
type TieredBufferPool struct {
	pools [tierCount]sync.Pool
}

// NewTieredBufferPool creates a new TieredBufferPool with per-tier New
// functions that allocate full-length slices at the tier's size.
func NewTieredBufferPool() *TieredBufferPool {
	p := &TieredBufferPool{}
	for i := bufferTier(0); i < tierCount; i++ {
		size := tierSizes[i]
		p.pools[i].New = func() any {
			buf := make([]byte, size)
			return &buf
		}
	}
	return p
}

// Get returns a buffer from the specified tier's pool.
func (p *TieredBufferPool) Get(tier bufferTier) *[]byte {
	return p.pools[tier].Get().(*[]byte)
}

// Put returns a buffer to the matching tier's pool. If the buffer's
// capacity exceeds MaxPoolBufferCap, it is discarded to prevent memory
// leaks. If the capacity doesn't exactly match any tier, it is also
// discarded to prevent size mismatches in the pool.
func (p *TieredBufferPool) Put(bufPtr *[]byte) {
	c := cap(*bufPtr)
	if c > types.MaxPoolBufferCap {
		return // discard oversized buffer, let GC collect
	}
	for i := bufferTier(0); i < tierCount; i++ {
		if c == tierSizes[i] {
			p.pools[i].Put(bufPtr)
			return
		}
	}
	// cap doesn't match any tier, discard
}

// TierForSpeed maps a download speed (bytes/sec) to the appropriate tier.
func TierForSpeed(speed float64) bufferTier {
	if speed > tierSpeedHigh {
		return tierLarge
	}
	if speed > tierSpeedLow {
		return tierMedium
	}
	return tierSmall
}

// TierForBufferSize maps a configured buffer size to the closest tier.
// Used for initial tier selection from RuntimeConfig.WorkerBufferSize.
func TierForBufferSize(size int) bufferTier {
	for i := bufferTier(tierCount - 1); i >= 0; i-- {
		if size >= tierSizes[i] {
			return i
		}
	}
	return tierSmall
}

// TierForCap maps a buffer capacity to its tier. The second return value
// is false if the capacity doesn't match any tier.
func TierForCap(c int) (bufferTier, bool) {
	for i := bufferTier(0); i < tierCount; i++ {
		if c == tierSizes[i] {
			return i, true
		}
	}
	return tierSmall, false
}
