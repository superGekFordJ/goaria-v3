package smartthread

import (
	"testing"
	"time"
)

const mb = 1024 * 1024

func TestClampToServerLimit_ClampsSplit(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb, NSat: 12}
	got := ClampToServerLimit(params, 100*mb, "example.com", store)

	if got.Split != 7 {
		t.Fatalf("Split: got %d, want 7", got.Split)
	}
	if got.TargetBandwidth != 7*mb {
		t.Fatalf("TargetBandwidth: got %d, want %d", got.TargetBandwidth, 7*mb)
	}
}

func TestClampToServerLimit_NoLimit_NoChange(t *testing.T) {
	store := NewServerLimitStore()

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb, NSat: 12}
	got := ClampToServerLimit(params, 100*mb, "unknown.com", store)

	if got != params {
		t.Fatalf("expected unchanged params, got %+v", got)
	}
}

func TestClampToServerLimit_SplitBelowNMax_NoChange(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("example.com", 7)

	params := ThreadParams{Split: 4, TargetBandwidth: 4 * mb, MinSize: mb, NSat: 6}
	got := ClampToServerLimit(params, 100*mb, "example.com", store)

	if got != params {
		t.Fatalf("expected unchanged params, got %+v", got)
	}

	t.Run("SplitEqualsNMax_NoChange", func(t *testing.T) {
		store := NewServerLimitStore()
		store.SetNMax("example.com", 7)

		params := ThreadParams{Split: 7, TargetBandwidth: 7 * mb, MinSize: mb, NSat: 6}
		got := ClampToServerLimit(params, 100*mb, "example.com", store)

		if got != params {
			t.Fatalf("expected unchanged params when Split==nMax, got %+v", got)
		}
	})
}

func TestClampToServerLimit_ExpiredLimit_NoChange(t *testing.T) {
	store := NewServerLimitStore()
	store.mu.Lock()
	store.limits["expired.com"] = &serverLimit{
		NMax:       7,
		DetectedAt: time.Now().Add(-25 * time.Hour),
		TTL:        serverLimitTTL,
	}
	store.mu.Unlock()

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb, NSat: 12}
	got := ClampToServerLimit(params, 100*mb, "expired.com", store)

	if got != params {
		t.Fatalf("expected unchanged params for expired limit, got %+v", got)
	}
}

func TestClampToServerLimit_NilStore_NoChange(t *testing.T) {
	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb, NSat: 12}
	got := ClampToServerLimit(params, 100*mb, "example.com", nil)

	if got != params {
		t.Fatalf("expected unchanged params for nil store, got %+v", got)
	}
}

func TestClampToServerLimit_MinSizeAdjustment(t *testing.T) {
	t.Run("perWorkerAboveMinSize_NoChange", func(t *testing.T) {
		store := NewServerLimitStore()
		store.SetNMax("example.com", 7)

		// 10MB / 7 ≈ 1.43MB > 1MB MinSize → MinSize unchanged.
		params := ThreadParams{Split: 16, MinSize: mb}
		got := ClampToServerLimit(params, 10*mb, "example.com", store)

		if got.MinSize != mb {
			t.Fatalf("MinSize: got %d, want %d", got.MinSize, mb)
		}
	})

	t.Run("perWorkerBelowMinSize_ClampedToFloor", func(t *testing.T) {
		store := NewServerLimitStore()
		store.SetNMax("example.com", 7)

		// 5MB / 7 ≈ 714KB < 1MB MinSize → MinSize would drop to 714KB but
		// minChunkSize floor keeps it at 1MB.
		params := ThreadParams{Split: 16, MinSize: mb}
		got := ClampToServerLimit(params, 5*mb, "example.com", store)

		if got.MinSize != minChunkSize {
			t.Fatalf("MinSize: got %d, want %d (minChunkSize floor)", got.MinSize, minChunkSize)
		}
	})
}

func TestClampToServerLimit_FileSizeZero_NoMinSizeAdjustment(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("example.com", 7)

	params := ThreadParams{Split: 16, MinSize: 0, TargetBandwidth: 16 * mb}
	got := ClampToServerLimit(params, 0, "example.com", store)

	if got.Split != 7 {
		t.Fatalf("Split: got %d, want 7", got.Split)
	}
	if got.MinSize != 0 {
		t.Fatalf("MinSize: got %d, want 0", got.MinSize)
	}
}

func TestClampToServerLimit_TargetBandwidthScales(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb}
	got := ClampToServerLimit(params, 100*mb, "example.com", store)

	want := int64(16 * mb * 7 / 16)
	if got.TargetBandwidth != want {
		t.Fatalf("TargetBandwidth: got %d, want %d", got.TargetBandwidth, want)
	}
}

func TestClampToServerLimit_TargetBandwidthZero_StaysZero(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 0}
	got := ClampToServerLimit(params, 100*mb, "example.com", store)

	if got.TargetBandwidth != 0 {
		t.Fatalf("TargetBandwidth: got %d, want 0", got.TargetBandwidth)
	}
}

func TestClampToServerLimit_NSatUnchanged(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("example.com", 7)

	params := ThreadParams{Split: 16, NSat: 12, TargetBandwidth: 16 * mb}
	got := ClampToServerLimit(params, 100*mb, "example.com", store)

	if got.NSat != 12 {
		t.Fatalf("NSat: got %d, want 12", got.NSat)
	}
	if got.IsExploration != params.IsExploration {
		t.Fatalf("IsExploration changed: got %v, want %v", got.IsExploration, params.IsExploration)
	}
}
