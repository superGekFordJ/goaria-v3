package monitor

import "testing"

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"colon lower", "aa:bb:cc:dd:ee:ff", "aabbccddeeff"},
		{"colon upper", "AA:BB:CC:DD:EE:FF", "aabbccddeeff"},
		{"dash lower", "aa-bb-cc-dd-ee-ff", "aabbccddeeff"},
		{"dash upper", "AA-BB-CC-DD-EE-FF", "aabbccddeeff"},
		{"mixed case", "Aa:Bb:Cc:Dd:Ee:Ff", "aabbccddeeff"},
		{"mixed separators", "aa-bb:cc-dd:ee-ff", "aabbccddeeff"},
		{"empty", "", ""},
		{"no separators", "aabbccddeeff", "aabbccddeeff"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeMAC(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeMAC(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestComputeEnvKey_Deterministic(t *testing.T) {
	// Same inputs must produce the same output.
	k1 := ComputeEnvKey(routeCodeDirect, "aa:bb:cc:dd:ee:ff")
	k2 := ComputeEnvKey(routeCodeDirect, "aa:bb:cc:dd:ee:ff")
	if k1 != k2 {
		t.Fatalf("identical inputs produced different keys: %q vs %q", k1, k2)
	}
}

func TestComputeEnvKey_RouteCodeDistinguishes(t *testing.T) {
	// Same MAC but different routeCode must produce different keys.
	direct := ComputeEnvKey(routeCodeDirect, "aa:bb:cc:dd:ee:ff")
	proxy := ComputeEnvKey(routeCodeProxy, "aa:bb:cc:dd:ee:ff")
	if direct == proxy {
		t.Fatalf("direct and proxy route produced same key: %q", direct)
	}
}

func TestComputeEnvKey_MACFormatAgnostic(t *testing.T) {
	// Different MAC formats (colon vs dash vs upper) must produce the same key.
	colon := ComputeEnvKey(routeCodeDirect, "aa:bb:cc:dd:ee:ff")
	dash := ComputeEnvKey(routeCodeDirect, "aa-bb-cc-dd-ee-ff")
	upper := ComputeEnvKey(routeCodeDirect, "AA:BB:CC:DD:EE:FF")
	if colon != dash || colon != upper {
		t.Fatalf("MAC format not normalized: colon=%q dash=%q upper=%q", colon, dash, upper)
	}
}

func TestComputeEnvKey_EmptyMAC_FixedBucket(t *testing.T) {
	// Empty MAC must produce a deterministic fixed bucket (not random).
	k1 := ComputeEnvKey(routeCodeDirect, "")
	k2 := ComputeEnvKey(routeCodeDirect, "")
	if k1 != k2 {
		t.Fatalf("empty MAC produced non-deterministic key: %q vs %q", k1, k2)
	}

	// Empty MAC with different routeCode must still distinguish.
	directEmpty := ComputeEnvKey(routeCodeDirect, "")
	proxyEmpty := ComputeEnvKey(routeCodeProxy, "")
	if directEmpty == proxyEmpty {
		t.Fatalf("empty MAC with different route produced same key: %q", directEmpty)
	}

	// Empty MAC key must differ from a real MAC key.
	realMAC := ComputeEnvKey(routeCodeDirect, "aa:bb:cc:dd:ee:ff")
	if directEmpty == realMAC {
		t.Fatalf("empty MAC key matches real MAC key: %q", directEmpty)
	}
}

func TestComputeEnvKey_KeyLength(t *testing.T) {
	// EnvKey is the first 8 hex chars of sha256 (4 bytes → 8 hex).
	k := ComputeEnvKey(routeCodeDirect, "aa:bb:cc:dd:ee:ff")
	if len(k) != 8 {
		t.Errorf("envKey length = %d, want 8", len(k))
	}
}
