package smartthread

import (
	"testing"
)

func TestBandwidthLedger_SeedsFromActiveBandwidth(t *testing.T) {
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 {
		if scope == "wan" && envKey == "env1" {
			return 5000000
		}
		return 0
	}

	ledger := NewBandwidthLedger([]TrackedTaskInfo{
		{GID: "sg_1", Scope: "wan", EnvKey: "env1"},
	})
	reserved := ledger.Reserved("wan", "env1")
	if reserved != 5000000 {
		t.Errorf("Reserved(wan, env1) = %d, want 5000000 (seeded from active)", reserved)
	}
}

func TestBandwidthLedger_ReserveAccumulates(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000},
	}

	ledger.Reserve("wan", "env1", 2000000)
	if got := ledger.Reserved("wan", "env1"); got != 3000000 {
		t.Errorf("Reserved(wan, env1) = %d, want 3000000", got)
	}

	ledger.Reserve("wan", "env1", 500000)
	if got := ledger.Reserved("wan", "env1"); got != 3500000 {
		t.Errorf("Reserved(wan, env1) = %d, want 3500000", got)
	}
}

func TestBandwidthLedger_ScopeIsolation(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000, "lanenv1": 500000},
	}

	ledger.Reserve("wan", "env1", 2000000)
	if got := ledger.Reserved("lan", "env1"); got != 500000 {
		t.Errorf("Reserved(lan, env1) = %d, want 500000 (unaffected by wan reserve)", got)
	}
}

func TestBandwidthLedger_EnvIsolation(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000, "wanenv2": 2000000},
	}

	ledger.Reserve("wan", "env1", 500000)
	if got := ledger.Reserved("wan", "env2"); got != 2000000 {
		t.Errorf("Reserved(wan, env2) = %d, want 2000000 (unaffected by env1 reserve)", got)
	}
}

func TestBandwidthLedger_EmptyScopeDefaultsWan(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000},
	}

	if got := ledger.Reserved("", "env1"); got != 1000000 {
		t.Errorf("Reserved(\"\", env1) = %d, want 1000000 (defaults to wan)", got)
	}

	ledger.Reserve("", "env1", 500000)
	if got := ledger.Reserved("wan", "env1"); got != 1500000 {
		t.Errorf("Reserved(wan, env1) after Reserve(\"\", env1) = %d, want 1500000", got)
	}
}

func TestBandwidthLedger_NilSafety(t *testing.T) {
	var ledger *BandwidthLedger

	if got := ledger.Reserved("wan", "env1"); got != 0 {
		t.Errorf("nil.Reserved = %d, want 0", got)
	}

	ledger.Reserve("wan", "env1", 1000000) // should not panic
}

func TestBandwidthLedger_NegativeBandwidthIgnored(t *testing.T) {
	ledger := &BandwidthLedger{
		reserved: map[string]int64{"wanenv1": 1000000},
	}

	ledger.Reserve("wan", "env1", -500000)
	if got := ledger.Reserved("wan", "env1"); got != 1000000 {
		t.Errorf("Reserved(wan, env1) = %d, want 1000000 (negative ignored)", got)
	}
}
