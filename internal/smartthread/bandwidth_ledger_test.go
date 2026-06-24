package smartthread

import (
	"testing"
)

func TestBandwidthLedger_SeedsFromActiveBandwidth(t *testing.T) {
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope string) int64 {
		if scope == "wan" {
			return 5000000
		}
		return 0
	}

	ledger := NewBandwidthLedger()
	reserved := ledger.Reserved("wan")
	if reserved != 5000000 {
		t.Errorf("Reserved(wan) = %d, want 5000000 (seeded from active)", reserved)
	}
}

func TestBandwidthLedger_ReserveAccumulates(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wan": 1000000},
	}

	ledger.Reserve("wan", 2000000)
	if got := ledger.Reserved("wan"); got != 3000000 {
		t.Errorf("Reserved(wan) = %d, want 3000000", got)
	}

	ledger.Reserve("wan", 500000)
	if got := ledger.Reserved("wan"); got != 3500000 {
		t.Errorf("Reserved(wan) = %d, want 3500000", got)
	}
}

func TestBandwidthLedger_ScopeIsolation(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wan": 1000000, "lan": 500000},
	}

	ledger.Reserve("wan", 2000000)
	if got := ledger.Reserved("lan"); got != 500000 {
		t.Errorf("Reserved(lan) = %d, want 500000 (unaffected by wan reserve)", got)
	}
}

func TestBandwidthLedger_EmptyScopeDefaultsWan(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wan": 1000000},
	}

	if got := ledger.Reserved(""); got != 1000000 {
		t.Errorf("Reserved(\"\") = %d, want 1000000 (defaults to wan)", got)
	}

	ledger.Reserve("", 500000)
	if got := ledger.Reserved("wan"); got != 1500000 {
		t.Errorf("Reserved(wan) after Reserve(\"\") = %d, want 1500000", got)
	}
}

func TestBandwidthLedger_NilSafety(t *testing.T) {
	var ledger *BandwidthLedger

	if got := ledger.Reserved("wan"); got != 0 {
		t.Errorf("nil.Reserved = %d, want 0", got)
	}

	ledger.Reserve("wan", 1000000) // should not panic
}

func TestBandwidthLedger_NegativeBandwidthIgnored(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wan": 1000000},
	}

	ledger.Reserve("wan", -500000)
	if got := ledger.Reserved("wan"); got != 1000000 {
		t.Errorf("Reserved(wan) = %d, want 1000000 (negative ignored)", got)
	}
}
