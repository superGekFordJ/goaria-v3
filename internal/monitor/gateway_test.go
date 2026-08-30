package monitor

import (
	"net"
	"testing"
)

func TestParseLinuxProcNetRoute(t *testing.T) {
	sampleRoute := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
eth0	00000000	FE01A8C0	0003	0	0	100	00000000	0	0	0
eth0	0001A8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
eth1	00000000	010011AC	0003	0	0	101	00000000	0	0	0
eth2	00000000	00000000	0001	0	0	102	00000000	0	0	0
`
	t.Run("Match_eth0_CustomGateway254", func(t *testing.T) {
		got := parseLinuxProcNetRoute(sampleRoute, "eth0")
		if got == nil {
			t.Fatalf("expected gateway for eth0, got nil")
		}
		want := net.IPv4(192, 168, 1, 254)
		if !got.Equal(want) {
			t.Errorf("got %s, want %s", got.String(), want.String())
		}
	})

	t.Run("Match_eth1_Gateway", func(t *testing.T) {
		got := parseLinuxProcNetRoute(sampleRoute, "eth1")
		if got == nil {
			t.Fatalf("expected gateway for eth1, got nil")
		}
		want := net.IPv4(172, 17, 0, 1)
		if !got.Equal(want) {
			t.Errorf("got %s, want %s", got.String(), want.String())
		}
	})

	t.Run("NoFlags_eth2", func(t *testing.T) {
		got := parseLinuxProcNetRoute(sampleRoute, "eth2")
		if got != nil {
			t.Errorf("expected nil for eth2 (no RTF_GATEWAY flag), got %s", got.String())
		}
	})

	t.Run("NonExistent_eth9", func(t *testing.T) {
		got := parseLinuxProcNetRoute(sampleRoute, "eth9")
		if got != nil {
			t.Errorf("expected nil for eth9, got %s", got.String())
		}
	})
}

func TestParseLinuxIPRouteOutput(t *testing.T) {
	sampleSingle := "default via 192.168.1.254 proto dhcp src 192.168.1.50 metric 100\n"
	sampleMultipath := `default nexthop via 192.168.1.1 dev eth0 weight 1 nexthop via 10.0.0.1 dev eth1 weight 1`

	t.Run("Single_Route", func(t *testing.T) {
		got := parseLinuxIPRouteOutput(sampleSingle, "eth0")
		if got == nil || !got.Equal(net.IPv4(192, 168, 1, 254)) {
			t.Fatalf("expected 192.168.1.254, got %v", got)
		}
	})

	t.Run("Multipath_Delegates_To_eth0", func(t *testing.T) {
		got := parseLinuxIPRouteOutput(sampleMultipath, "eth0")
		if got == nil || !got.Equal(net.IPv4(192, 168, 1, 1)) {
			t.Fatalf("expected 192.168.1.1, got %v", got)
		}
	})

	t.Run("Multipath_Delegates_To_eth1", func(t *testing.T) {
		got := parseLinuxIPRouteOutput(sampleMultipath, "eth1")
		if got == nil || !got.Equal(net.IPv4(10, 0, 0, 1)) {
			t.Fatalf("expected 10.0.0.1, got %v", got)
		}
	})
}

func TestParseLinuxGeneralIPRouteOutput_Multipath(t *testing.T) {
	sampleMultipath := `default nexthop via 192.168.1.1 dev eth0 weight 1 nexthop via 10.0.0.1 dev eth1 weight 1`

	t.Run("Multipath_Match_eth0", func(t *testing.T) {
		got := parseLinuxGeneralIPRouteOutput(sampleMultipath, "eth0")
		if got == nil || !got.Equal(net.IPv4(192, 168, 1, 1)) {
			t.Errorf("got %v, want 192.168.1.1", got)
		}
	})

	t.Run("Multipath_Match_eth1", func(t *testing.T) {
		got := parseLinuxGeneralIPRouteOutput(sampleMultipath, "eth1")
		if got == nil || !got.Equal(net.IPv4(10, 0, 0, 1)) {
			t.Errorf("got %v, want 10.0.0.1", got)
		}
	})

	t.Run("Multipath_NoMatch_eth2", func(t *testing.T) {
		got := parseLinuxGeneralIPRouteOutput(sampleMultipath, "eth2")
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}

func TestParseLinuxNeighOutput(t *testing.T) {
	sample := `192.168.1.1 dev eth0 lladdr aa:bb:cc:dd:ee:ff REACHABLE
10.0.0.1 dev eth1 lladdr 11:22:33:44:55:66 STALE
192.168.1.254 dev eth0 lladdr 00:11:22:33:44:55 DELAY`

	t.Run("Match_eth0_192.168.1.1", func(t *testing.T) {
		got := parseLinuxNeighOutput(sample, "192.168.1.1", "eth0")
		if got != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("got %q, want aa:bb:cc:dd:ee:ff", got)
		}
	})

	t.Run("Mismatch_Dev", func(t *testing.T) {
		got := parseLinuxNeighOutput(sample, "192.168.1.1", "eth1")
		if got != "" {
			t.Errorf("expected empty for mismatched dev, got %q", got)
		}
	})

	t.Run("Match_eth0_192.168.1.254", func(t *testing.T) {
		got := parseLinuxNeighOutput(sample, "192.168.1.254", "eth0")
		if got != "00:11:22:33:44:55" {
			t.Errorf("got %q, want 00:11:22:33:44:55", got)
		}
	})
}

func TestParseLinuxARPOutput(t *testing.T) {
	sample := `Address                  HWtype  HWaddress           Flags Mask            Iface
192.168.1.1              ether   aa:bb:cc:dd:ee:ff   C                     eth0
10.0.0.1                 ether   11:22:33:44:55:66   C                     eth1`

	got := parseLinuxARPOutput(sample, "192.168.1.1", "eth0")
	if got != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("got %q, want aa:bb:cc:dd:ee:ff", got)
	}

	gotWrongDev := parseLinuxARPOutput(sample, "192.168.1.1", "eth1")
	if gotWrongDev != "" {
		t.Errorf("expected empty for wrong dev, got %q", gotWrongDev)
	}
}

func TestParseDarwinRouteOutput(t *testing.T) {
	sample := `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.254
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
        0         0         0         0         0         0      1500         0`

	t.Run("Match_en0", func(t *testing.T) {
		got := parseDarwinRouteOutput(sample, "en0")
		if got == nil || !got.Equal(net.IPv4(192, 168, 1, 254)) {
			t.Errorf("got %v, want 192.168.1.254", got)
		}
	})

	t.Run("Mismatch_en1", func(t *testing.T) {
		got := parseDarwinRouteOutput(sample, "en1")
		if got != nil {
			t.Errorf("expected nil for en1, got %v", got)
		}
	})
}

func TestParseDarwinARPOutput(t *testing.T) {
	sample := `? (192.168.1.1) at aa:bb:cc:dd:ee:ff on en0 ifscope [ethernet]
gateway (192.168.1.254) at 0:11:22:33:44:55 on en0 [ethernet]
? (10.0.0.1) at 11:22:33:44:55:66 on en1 ifscope [ethernet]
? (192.168.1.100) at (incomplete) on en0 ifscope [ethernet]`

	t.Run("Match_Parentheses_IP_en0", func(t *testing.T) {
		got := parseDarwinARPOutput(sample, "192.168.1.1", "en0")
		if got != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("got %q, want aa:bb:cc:dd:ee:ff", got)
		}
	})

	t.Run("Match_Named_Gateway_en0", func(t *testing.T) {
		got := parseDarwinARPOutput(sample, "192.168.1.254", "en0")
		if got != "0:11:22:33:44:55" {
			t.Errorf("got %q, want 0:11:22:33:44:55", got)
		}
	})

	t.Run("Mismatch_Interface", func(t *testing.T) {
		got := parseDarwinARPOutput(sample, "192.168.1.1", "en1")
		if got != "" {
			t.Errorf("expected empty for mismatched interface, got %q", got)
		}
	})

	t.Run("Incomplete_Entry", func(t *testing.T) {
		got := parseDarwinARPOutput(sample, "192.168.1.100", "en0")
		if got != "" {
			t.Errorf("expected empty for incomplete ARP, got %q", got)
		}
	})
}

func TestParseDarwinNetstatOutput(t *testing.T) {
	sample := `Routing tables

Internet:
Destination        Gateway            Flags        Netif Expire
default            192.168.10.1       UGScg          en0
default            10.0.0.1           UGScg          en1
127.0.0.1          127.0.0.1          UH             lo0
`
	t.Run("Match_en0", func(t *testing.T) {
		got := parseDarwinNetstatOutput(sample, "en0")
		if got == nil || !got.Equal(net.IPv4(192, 168, 10, 1)) {
			t.Errorf("got %v, want 192.168.10.1", got)
		}
	})

	t.Run("Match_en1", func(t *testing.T) {
		got := parseDarwinNetstatOutput(sample, "en1")
		if got == nil || !got.Equal(net.IPv4(10, 0, 0, 1)) {
			t.Errorf("got %v, want 10.0.0.1", got)
		}
	})
}

func TestParseWindowsRoutePrintOutput(t *testing.T) {
	sample := `===========================================================================
Interface List
 10...00 ff 12 34 56 78 ......Realtek PCIe GbE Family Controller
===========================================================================

IPv4 Route Table
===========================================================================
Active Routes:
Network Destination        Netmask          Gateway       Interface  Metric
          0.0.0.0          0.0.0.0     192.168.1.254    192.168.1.100     25
        127.0.0.0        255.0.0.0         On-link         127.0.0.1    331
===========================================================================
`
	t.Run("Match_InterfaceIP", func(t *testing.T) {
		ifaceIPs := []net.IP{net.IPv4(192, 168, 1, 100)}
		got := parseWindowsRoutePrintOutput(sample, ifaceIPs)
		if got == nil || !got.Equal(net.IPv4(192, 168, 1, 254)) {
			t.Errorf("got %v, want 192.168.1.254", got)
		}
	})

	t.Run("NoMatch_DifferentIP", func(t *testing.T) {
		ifaceIPs := []net.IP{net.IPv4(10, 0, 0, 50)}
		got := parseWindowsRoutePrintOutput(sample, ifaceIPs)
		if got != nil {
			t.Errorf("expected nil for non-matching iface IP, got %v", got)
		}
	})
}

func TestParseWindowsARPOutput(t *testing.T) {
	sampleEnglish := `Interface: 192.168.1.100 --- 0xa
  Internet Address      Physical Address      Type
  192.168.1.1           aa-bb-cc-dd-ee-ff     dynamic
  192.168.1.254         11-22-33-44-55-66     dynamic

Interface: 10.0.0.50 --- 0xb
  Internet Address      Physical Address      Type
  10.0.0.1              99-88-77-66-55-44     dynamic
`
	sampleChinese := `接口: 192.168.1.100 --- 0xa
  Internet Address      Physical Address      Type
  192.168.1.1           aa-bb-cc-dd-ee-ff     dynamic
  192.168.1.254         11-22-33-44-55-66     dynamic

接口: 10.0.0.50 --- 0xb
  Internet Address      Physical Address      Type
  10.0.0.1              99-88-77-66-55-44     dynamic
`

	t.Run("English_Scoped_Match_192.168.1.1", func(t *testing.T) {
		got := parseWindowsARPOutput(sampleEnglish, "192.168.1.1", "192.168.1.100")
		if got != "aa-bb-cc-dd-ee-ff" {
			t.Errorf("got %q, want aa-bb-cc-dd-ee-ff", got)
		}
	})

	t.Run("Chinese_Scoped_Match_192.168.1.254", func(t *testing.T) {
		got := parseWindowsARPOutput(sampleChinese, "192.168.1.254", "192.168.1.100")
		if got != "11-22-33-44-55-66" {
			t.Errorf("got %q, want 11-22-33-44-55-66", got)
		}
	})

	t.Run("Chinese_Scoped_Match_10.0.0.1", func(t *testing.T) {
		got := parseWindowsARPOutput(sampleChinese, "10.0.0.1", "10.0.0.50")
		if got != "99-88-77-66-55-44" {
			t.Errorf("got %q, want 99-88-77-66-55-44", got)
		}
	})

	t.Run("Scoped_Mismatch_Interface", func(t *testing.T) {
		got := parseWindowsARPOutput(sampleChinese, "192.168.1.1", "10.0.0.50")
		if got != "" {
			t.Errorf("expected empty for wrong interface section, got %q", got)
		}
	})
}

func TestIsMACField(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"aa:bb:cc:dd:ee:ff", true},
		{"AA-BB-CC-DD-EE-FF", true},
		{"0:11:22:33:44:55", true},
		{"0-11-22-33-44-55", true},
		{"(incomplete)", false},
		{"192.168.1.1", false},
		{"en0", false},
		{"", false},
		{"aa:bb:cc:dd:ee", false},
		{"aa:bb:cc:dd:ee:ff:gg", false},
	}
	for _, tt := range tests {
		got := isMACField(tt.input)
		if got != tt.want {
			t.Errorf("isMACField(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestDiscoverGatewayIP_Live(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip("cannot enumerate network interfaces")
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isSkipInterface(iface.Name) {
			continue
		}
		gw := discoverGatewayIP(iface)
		if gw != nil {
			mac := arpLookup(iface, gw)
			t.Logf("Live discover on iface %q (idx=%d): gw=%s macResolved=%v", iface.Name, iface.Index, gw.String(), mac != "")
		}
	}
}
