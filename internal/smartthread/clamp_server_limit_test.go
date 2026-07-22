package smartthread

import (
	"testing"
	"time"
)

const mb = 1024 * 1024

func TestClampToServerLimit_ClampsSplit(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb, NSat: 12}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, store)

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
	got := ClampToServerLimit(params, 100*mb, "wan", "unknown.com", 0, store)

	if got != params {
		t.Fatalf("expected unchanged params, got %+v", got)
	}
}

func TestClampToServerLimit_SplitBelowNMax_NoChange(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 4, TargetBandwidth: 4 * mb, MinSize: mb, NSat: 6}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, store)

	if got != params {
		t.Fatalf("expected unchanged params, got %+v", got)
	}

	t.Run("SplitEqualsNMax_NoChange", func(t *testing.T) {
		store := NewServerLimitStore()
		store.SetNMax("wan|example.com", 7)

		params := ThreadParams{Split: 7, TargetBandwidth: 7 * mb, MinSize: mb, NSat: 6}
		got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, store)

		if got != params {
			t.Fatalf("expected unchanged params when Split==nMax, got %+v", got)
		}
	})

	t.Run("ZeroNMax_NoChange", func(t *testing.T) {
		store := NewServerLimitStore()
		store.SetNMax("wan|example.com", 0)

		params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb, NSat: 12}
		got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, store)

		if got != params {
			t.Fatalf("expected unchanged params when nMax==0, got %+v", got)
		}
	})
}

func TestClampToServerLimit_ExpiredLimit_NoChange(t *testing.T) {
	store := NewServerLimitStore()
	store.mu.Lock()
	store.limits["wan|expired.com"] = &serverLimit{
		NMax:       7,
		DetectedAt: time.Now().Add(-25 * time.Hour),
		TTL:        serverLimitTTL,
	}
	store.mu.Unlock()

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb, NSat: 12}
	got := ClampToServerLimit(params, 100*mb, "wan", "expired.com", 0, store)

	if got != params {
		t.Fatalf("expected unchanged params for expired limit, got %+v", got)
	}
}

func TestClampToServerLimit_NilStore_NoChange(t *testing.T) {
	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb, NSat: 12}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, nil)

	if got != params {
		t.Fatalf("expected unchanged params for nil store, got %+v", got)
	}
}

func TestClampToServerLimit_MinSizeAdjustment(t *testing.T) {
	t.Run("perWorkerAboveMinSize_NoChange", func(t *testing.T) {
		store := NewServerLimitStore()
		store.SetNMax("wan|example.com", 7)

		params := ThreadParams{Split: 16, MinSize: mb}
		got := ClampToServerLimit(params, 10*mb, "wan", "example.com", 0, store)

		if got.MinSize != mb {
			t.Fatalf("MinSize: got %d, want %d", got.MinSize, mb)
		}
	})

	t.Run("perWorkerBelowMinSize_ClampedToFloor", func(t *testing.T) {
		store := NewServerLimitStore()
		store.SetNMax("wan|example.com", 7)

		params := ThreadParams{Split: 16, MinSize: mb}
		got := ClampToServerLimit(params, 5*mb, "wan", "example.com", 0, store)

		if got.MinSize != minChunkSize {
			t.Fatalf("MinSize: got %d, want %d (minChunkSize floor)", got.MinSize, minChunkSize)
		}
	})
}

func TestClampToServerLimit_FileSizeZero_NoMinSizeAdjustment(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 16, MinSize: 0, TargetBandwidth: 16 * mb}
	got := ClampToServerLimit(params, 0, "wan", "example.com", 0, store)

	if got.Split != 7 {
		t.Fatalf("Split: got %d, want 7", got.Split)
	}
	if got.MinSize != 0 {
		t.Fatalf("MinSize: got %d, want 0", got.MinSize)
	}
}

func TestClampToServerLimit_TargetBandwidthScales(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, store)

	want := int64(16 * mb * 7 / 16)
	if got.TargetBandwidth != want {
		t.Fatalf("TargetBandwidth: got %d, want %d", got.TargetBandwidth, want)
	}
}

func TestClampToServerLimit_TargetBandwidthZero_StaysZero(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 0}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, store)

	if got.TargetBandwidth != 0 {
		t.Fatalf("TargetBandwidth: got %d, want 0", got.TargetBandwidth)
	}
}

func TestClampToServerLimit_NSatUnchanged(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 16, NSat: 12, TargetBandwidth: 16 * mb}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, store)

	if got.NSat != 12 {
		t.Fatalf("NSat: got %d, want 12", got.NSat)
	}
	if got.IsExploration != params.IsExploration {
		t.Fatalf("IsExploration changed: got %v, want %v", got.IsExploration, params.IsExploration)
	}
}

// --- Domain-level clamp tests ---

func TestClampToServerLimit_DomainLevelClamp(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 10)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 7, store)

	if got.Split != 3 {
		t.Fatalf("Split: got %d, want 3 (nMax=10 - existing=7)", got.Split)
	}
}

func TestClampToServerLimit_ExistingExceedsNMax_FloorsAt1(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 5)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 8, store)

	if got.Split != 1 {
		t.Fatalf("Split: got %d, want 1 (floored, nMax=5 - existing=8 < 1)", got.Split)
	}
}

func TestClampToServerLimit_EmptyScope_NoChange(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb}
	got := ClampToServerLimit(params, 100*mb, "", "example.com", 0, store)

	if got != params {
		t.Fatalf("expected unchanged params for empty scope, got %+v", got)
	}
}

func TestClampToServerLimit_EmptyDomain_NoChange(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb}
	got := ClampToServerLimit(params, 100*mb, "wan", "", 0, store)

	if got != params {
		t.Fatalf("expected unchanged params for empty domain, got %+v", got)
	}
}

func TestClampToServerLimit_CompoundKey(t *testing.T) {
	store := NewServerLimitStore()
	store.SetNMax("wan|example.com", 7)

	params := ThreadParams{Split: 16, TargetBandwidth: 16 * mb, MinSize: mb}
	got := ClampToServerLimit(params, 100*mb, "wan", "example.com", 0, store)

	if got.Split != 7 {
		t.Fatalf("Split: got %d, want 7 (compound key wan|example.com)", got.Split)
	}
}
