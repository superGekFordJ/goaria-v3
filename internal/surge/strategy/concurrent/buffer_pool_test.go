package concurrent

import (
	"sync"
	"testing"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func TestTieredBufferPool_GetPut(t *testing.T) {
	p := NewTieredBufferPool()

	for tier := range tierCount {
		bufPtr := p.Get(tier)
		if len(*bufPtr) != tierSizes[tier] {
			t.Errorf("tier %d: expected len=%d, got len=%d", tier, tierSizes[tier], len(*bufPtr))
		}
		if cap(*bufPtr) != tierSizes[tier] {
			t.Errorf("tier %d: expected cap=%d, got cap=%d", tier, tierSizes[tier], cap(*bufPtr))
		}

		// Put and Get again — should reuse the same buffer
		p.Put(bufPtr)
		bufPtr2 := p.Get(tier)
		if len(*bufPtr2) != tierSizes[tier] {
			t.Errorf("tier %d: after Put/Get expected len=%d, got len=%d", tier, tierSizes[tier], len(*bufPtr2))
		}
	}
}

func TestTieredBufferPool_CapFilter(t *testing.T) {
	p := NewTieredBufferPool()

	// Create an oversized buffer (> MaxPoolBufferCap)
	big := make([]byte, types.MaxPoolBufferCap+1)
	bigPtr := &big

	// Put it — should be discarded
	p.Put(bigPtr)

	// Get from any tier — should return a freshly allocated buffer, not the big one
	got := p.Get(tierMedium)
	if cap(*got) != tierSizes[tierMedium] {
		t.Errorf("expected cap=%d after discarding oversized, got cap=%d", tierSizes[tierMedium], cap(*got))
	}
}

func TestTieredBufferPool_PutWrongTier(t *testing.T) {
	p := NewTieredBufferPool()

	// Create a buffer with cap that doesn't match any tier (256KB)
	wrong := make([]byte, 256*utils.KiB)
	wrongPtr := &wrong

	// Put it — should be discarded (no matching tier)
	p.Put(wrongPtr)

	// Get from each tier — should return freshly allocated buffers
	for tier := range tierCount {
		got := p.Get(tier)
		if cap(*got) != tierSizes[tier] {
			t.Errorf("tier %d: expected cap=%d (fresh alloc), got cap=%d", tier, tierSizes[tier], cap(*got))
		}
	}
}

func TestTierForSpeed(t *testing.T) {
	tests := []struct {
		speed float64
		want  bufferTier
	}{
		{0, tierSmall},
		{1 * float64(utils.MiB), tierSmall},
		{9.9 * float64(utils.MiB), tierSmall},
		{10 * float64(utils.MiB), tierSmall},    // exactly 10MB/s → not > 10MB/s → small
		{10.1 * float64(utils.MiB), tierMedium}, // just above 10MB/s → medium
		{15 * float64(utils.MiB), tierMedium},
		{50 * float64(utils.MiB), tierMedium},  // exactly 50MB/s → not > 50MB/s → medium
		{50.1 * float64(utils.MiB), tierLarge}, // just above 50MB/s → large
		{60 * float64(utils.MiB), tierLarge},
		{100 * float64(utils.MiB), tierLarge},
	}

	for _, tt := range tests {
		got := TierForSpeed(tt.speed)
		if got != tt.want {
			t.Errorf("TierForSpeed(%.1f MB/s) = %d, want %d", tt.speed/float64(utils.MiB), got, tt.want)
		}
	}
}

func TestTierForBufferSize(t *testing.T) {
	tests := []struct {
		size int
		want bufferTier
	}{
		{0, tierSmall},
		{16 * utils.KiB, tierSmall},
		{32 * utils.KiB, tierSmall},   // exactly 32KB → tierSmall (>= 32KB)
		{256 * utils.KiB, tierSmall},  // 256KB >= 32KB but < 512KB → tierSmall
		{512 * utils.KiB, tierMedium}, // exactly 512KB → tierMedium
		{1 * utils.MiB, tierLarge},    // exactly 1MB → tierLarge
		{2 * utils.MiB, tierLarge},    // > 1MB → tierLarge
	}

	for _, tt := range tests {
		got := TierForBufferSize(tt.size)
		if got != tt.want {
			t.Errorf("TierForBufferSize(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}

func TestTieredBufferPool_ConcurrentAccess(t *testing.T) {
	p := NewTieredBufferPool()
	var wg sync.WaitGroup

	goroutines := 50
	iterations := 100

	for range goroutines {
		wg.Go(func() {
			for j := range iterations {
				tier := bufferTier(j % int(tierCount))
				bufPtr := p.Get(tier)
				if len(*bufPtr) != tierSizes[tier] {
					t.Errorf("expected len=%d, got len=%d", tierSizes[tier], len(*bufPtr))
				}
				p.Put(bufPtr)
			}
		})
	}

	wg.Wait()
}
