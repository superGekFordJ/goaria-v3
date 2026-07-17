package smartthread

import (
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/speedstats"
)

// mockEnvKey mimics monitor.ComputeEnvKey with a deterministic, readable hash
// (routeCode + ":" + mac) so tests can predict collision outcomes without
// importing monitor (which would create an import cycle in this package).
func mockEnvKey(routeCode, mac string) string {
	return routeCode + ":" + mac
}

const (
	physMB = 1024 * 1024
)

// baseParams builds a CalcParams with all injections wired to a single MAC.
func baseParams(envKey string, macs []string, ledger *BandwidthLedger) CalcParams {
	return CalcParams{
		EnvKey:            envKey,
		Ledger:            ledger,
		ActiveMACsFunc:    func() []string { return macs },
		ComputeEnvKeyFunc: mockEnvKey,
	}
}

func TestApplyPhysicalCeiling(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	const mac = "aabbccddeeff"
	hDirect := mockEnvKey("0", mac)
	hProxy := mockEnvKey("1", mac)

	// Seed historical peaks: wan+direct=100MB, wan+proxy=100MB.
	speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, "host.com", "wan", hDirect)
	speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, "host.com", "wan", hProxy)

	t.Run("Degrade_NilLedger", func(t *testing.T) {
		p := baseParams(hDirect, []string{mac}, nil)
		p.Ledger = nil
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (degrade on nil ledger)", got, 5*physMB)
		}
	})

	t.Run("Degrade_NilActiveMACsFunc", func(t *testing.T) {
		p := baseParams(hDirect, []string{mac}, &BandwidthLedger{reserved: map[string]int64{}})
		p.ActiveMACsFunc = nil
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (degrade on nil ActiveMACsFunc)", got, 5*physMB)
		}
	})

	t.Run("Degrade_NilComputeEnvKeyFunc", func(t *testing.T) {
		p := baseParams(hDirect, []string{mac}, &BandwidthLedger{reserved: map[string]int64{}})
		p.ComputeEnvKeyFunc = nil
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (degrade on nil ComputeEnvKeyFunc)", got, 5*physMB)
		}
	})

	t.Run("Degrade_EmptyEnvKey", func(t *testing.T) {
		p := baseParams("", []string{mac}, &BandwidthLedger{reserved: map[string]int64{}})
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (degrade on empty envKey)", got, 5*physMB)
		}
	})

	t.Run("Degrade_EmptyMACs", func(t *testing.T) {
		p := baseParams(hDirect, []string{}, &BandwidthLedger{reserved: map[string]int64{}})
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (degrade on empty MACs)", got, 5*physMB)
		}
	})

	t.Run("Degrade_NilMACs", func(t *testing.T) {
		p := baseParams(hDirect, nil, &BandwidthLedger{reserved: map[string]int64{}})
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (degrade on nil MACs)", got, 5*physMB)
		}
	})

	t.Run("Degrade_CollisionFail", func(t *testing.T) {
		p := baseParams("deadbeef", []string{mac}, &BandwidthLedger{reserved: map[string]int64{}})
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (degrade on collision fail)", got, 5*physMB)
		}
	})

	t.Run("Degrade_AllBucketsZero", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(func() {
			speedstats.ResetRecordsForTest()
			speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, "host.com", "wan", hDirect)
			speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, "host.com", "wan", hProxy)
		})
		p := baseParams(hDirect, []string{mac}, &BandwidthLedger{reserved: map[string]int64{}})
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (degrade on all buckets zero)", got, 5*physMB)
		}
	})

	t.Run("PhysicalCeiling_TighterThanLogical", func(t *testing.T) {
		// physicalPeak=100MB, totalReserved=98MB → vPhysical=2MB < vLogical=5MB.
		ledger := &BandwidthLedger{reserved: map[string]int64{
			"wan" + hDirect: 8 * physMB,
			"wan" + hProxy:  90 * physMB,
		}}
		p := baseParams(hDirect, []string{mac}, ledger)
		if got := applyPhysicalCeiling(5*physMB, p); got != 2*physMB {
			t.Errorf("got %d, want %d (physical tighter)", got, 2*physMB)
		}
	})

	t.Run("PhysicalCeiling_LooserThanLogical", func(t *testing.T) {
		// physicalPeak=100MB, totalReserved=10MB → vPhysical=90MB > vLogical=5MB → min=5MB.
		ledger := &BandwidthLedger{reserved: map[string]int64{
			"wan" + hDirect: 10 * physMB,
		}}
		p := baseParams(hDirect, []string{mac}, ledger)
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (logical tighter, no relaxation)", got, 5*physMB)
		}
	})

	t.Run("PhysicalCeiling_ZeroAfterDeduction", func(t *testing.T) {
		// physicalPeak=100MB, totalReserved=150MB → vPhysical=0.
		ledger := &BandwidthLedger{reserved: map[string]int64{
			"wan" + hDirect: 50 * physMB,
			"wan" + hProxy:  100 * physMB,
		}}
		p := baseParams(hDirect, []string{mac}, ledger)
		if got := applyPhysicalCeiling(5*physMB, p); got != 0 {
			t.Errorf("got %d, want 0 (over-subscribed NIC)", got)
		}
	})

	t.Run("Collision_DirectRoute", func(t *testing.T) {
		ledger := &BandwidthLedger{reserved: map[string]int64{}}
		p := baseParams(hDirect, []string{mac}, ledger)
		// physicalPeak=100MB, totalReserved=0 → vPhysical=100MB > vLogical=5MB → 5MB.
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (direct route collision)", got, 5*physMB)
		}
	})

	t.Run("Collision_ProxyRoute", func(t *testing.T) {
		ledger := &BandwidthLedger{reserved: map[string]int64{}}
		p := baseParams(hProxy, []string{mac}, ledger)
		if got := applyPhysicalCeiling(5*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (proxy route collision)", got, 5*physMB)
		}
	})

	t.Run("MultiNIC_OnlyMatchedDeducted", func(t *testing.T) {
		// Two NICs; only the matched one's reserved is deducted.
		const mac2 = "112233445566"
		h2Direct := mockEnvKey("0", mac2)
		// Seed peak for mac2's direct bucket so it has data too.
		speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, "host2.com", "wan", h2Direct)

		ledger := &BandwidthLedger{reserved: map[string]int64{
			"wan" + hDirect:  0,
			"wan" + hProxy:   90 * physMB, // matched NIC proxy traffic
			"wan" + h2Direct: 80 * physMB, // other NIC — must NOT be deducted
		}}
		p := baseParams(hDirect, []string{mac, mac2}, ledger)
		// matched=mac: physicalPeak=max(100,100,0,0)=100MB, totalReserved=0+90=90MB → vPhysical=10MB.
		if got := applyPhysicalCeiling(50*physMB, p); got != 10*physMB {
			t.Errorf("got %d, want %d (only matched NIC deducted)", got, 10*physMB)
		}
	})

	t.Run("ScopeDefaultWan", func(t *testing.T) {
		// Reserved("", hDirect) defaults to wan per BandwidthLedger.
		ledger := &BandwidthLedger{reserved: map[string]int64{
			"wan" + hDirect: 8 * physMB,
			"wan" + hProxy:  90 * physMB,
		}}
		p := baseParams(hDirect, []string{mac}, ledger)
		// Same as TighterThanLogical; verifies scope-default-wan path in formula.
		if got := applyPhysicalCeiling(5*physMB, p); got != 2*physMB {
			t.Errorf("got %d, want %d (scope default wan)", got, 2*physMB)
		}
	})

	t.Run("LAN_ReservedIncludedInTotal", func(t *testing.T) {
		// LAN reserved on the same NIC counts toward totalReserved.
		ledger := &BandwidthLedger{reserved: map[string]int64{
			"lan" + hDirect: 95 * physMB,
		}}
		p := baseParams(hDirect, []string{mac}, ledger)
		// physicalPeak=100MB, totalReserved=95MB → vPhysical=5MB < vLogical=20MB → 5MB.
		if got := applyPhysicalCeiling(20*physMB, p); got != 5*physMB {
			t.Errorf("got %d, want %d (LAN reserved included)", got, 5*physMB)
		}
	})
}

func TestCalculate_PhysicalCeilingIntegration(t *testing.T) {
	setupTestConfig(t)
	t.Cleanup(func() { config.Current.EnablePhysicalMacAwareBandwidth = false })
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	const mac = "aabbccddeeff"
	hDirect := mockEnvKey("0", mac)
	hProxy := mockEnvKey("1", mac)
	const domain = "example.com"

	// vThreadAvg: 12.5MB/s single-thread efficiency (100MB peak / 8 threads).
	// vSinglePeak (example.com, wan, hDirect) = 100MB so vAvailable is the bottleneck.
	speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, domain, "wan", hDirect)
	// vGlobalPeak (logical, wan+hDirect) = 100MB.
	speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, "other.com", "wan", hDirect)
	// Physical proxy bucket (wan+hProxy) = 100MB so physicalPeak=100MB.
	speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, "other.com", "wan", hProxy)

	ledger := &BandwidthLedger{reserved: map[string]int64{
		"wan" + hDirect: 10 * physMB, // logical reserved (same bucket)
		"wan" + hProxy:  80 * physMB, // proxy traffic on same NIC — ignored by logical ceiling
	}}

	macs := []string{mac}
	calc := func(physicalOn bool) ThreadParams {
		config.Current.EnablePhysicalMacAwareBandwidth = physicalOn
		return Calculate(CalcParams{
			FileSize:          1 * 1024 * 1024 * 1024, // 1GB
			MaxConnections:    16,
			Scope:             "wan",
			Domain:            domain,
			EnvKey:            hDirect,
			ReservedBandwidth: ledger.Reserved("wan", hDirect),
			Ledger:            ledger,
			ActiveMACsFunc:    func() []string { return macs },
			ComputeEnvKeyFunc: mockEnvKey,
		})
	}

	// Switch OFF: logical only. vAvailable = 100-10 = 90MB.
	off := calc(false)
	// Switch ON: physical ceiling. vPhysical = 100-(10+80) = 10MB << 90MB.
	on := calc(true)

	if on.Split >= off.Split {
		t.Errorf("physical ceiling should tighten Split: on=%d off=%d", on.Split, off.Split)
	}
	// vAvailable<=0 or <vGlobalPeak/10 → floor=congestionFloor=2.
	// vPhysical=10MB, vGlobalPeak=100MB → 10 < 10 (100/10) is false, so floor=1.
	// But Split must still be smaller due to much lower vAvailable.
	if on.Split < 1 {
		t.Errorf("Split=%d, want >= 1", on.Split)
	}
}

func TestCalculate_PhysicalCeilingDisabledNoChange(t *testing.T) {
	setupTestConfig(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	const mac = "aabbccddeeff"
	hDirect := mockEnvKey("0", mac)
	const domain = "example.com"

	speedstats.AddRecordV2(2*physMB, 1, 10*physMB, false, 50, domain, "wan", hDirect)
	speedstats.AddRecordV2(100*physMB, 8, 1*1024*1024*1024, false, 50, "other.com", "wan", hDirect)

	// Switch OFF: injections present but ignored — result equals no-injection.
	config.Current.EnablePhysicalMacAwareBandwidth = false
	withInj := Calculate(CalcParams{
		FileSize:          1 * 1024 * 1024 * 1024,
		MaxConnections:    16,
		Scope:             "wan",
		Domain:            domain,
		EnvKey:            hDirect,
		ReservedBandwidth: 10 * physMB,
		Ledger:            &BandwidthLedger{reserved: map[string]int64{}},
		ActiveMACsFunc:    func() []string { return []string{mac} },
		ComputeEnvKeyFunc: mockEnvKey,
	})
	noInj := Calculate(CalcParams{
		FileSize:          1 * 1024 * 1024 * 1024,
		MaxConnections:    16,
		Scope:             "wan",
		Domain:            domain,
		EnvKey:            hDirect,
		ReservedBandwidth: 10 * physMB,
	})
	if withInj.Split != noInj.Split {
		t.Errorf("disabled switch must ignore injections: withInj=%d noInj=%d", withInj.Split, noInj.Split)
	}
}
