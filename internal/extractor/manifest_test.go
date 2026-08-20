package extractor

import (
	"strings"
	"testing"
)

func TestValidateManifestAcceptsValidManifest(t *testing.T) {
	manifest := validTestManifest()

	if err := ValidateManifest(manifest, DefaultTrustPolicy()); err != nil {
		t.Fatalf("ValidateManifest() error = %v", err)
	}
}

func TestValidateManifestAcceptsAliasManifestWithHTTPAuthCapabilities(t *testing.T) {
	manifest := validAliasTestManifest()

	if err := ValidateManifest(manifest, DefaultTrustPolicy()); err != nil {
		t.Fatalf("ValidateManifest() error = %v", err)
	}
}

func TestValidateManifestAcceptsAliasManifestWithoutBrokerRefsForParseOnly(t *testing.T) {
	manifest := validAliasTestManifest()
	manifest.Capabilities = []Capability{CapabilityParseWASM}
	manifest.BrokerPolicyRefs = nil

	if err := ValidateManifest(manifest, DefaultTrustPolicy()); err != nil {
		t.Fatalf("ValidateManifest() error = %v", err)
	}
}

func TestValidateManifestRejectsRequiredFieldFailures(t *testing.T) {
	validHash := strings.Repeat("0123456789abcdef", 4)

	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{
			name: "empty pack_id",
			mutate: func(manifest *Manifest) {
				manifest.PackID = ""
			},
		},
		{
			name: "uppercase pack_id",
			mutate: func(manifest *Manifest) {
				manifest.PackID = "FixturePack"
			},
		},
		{
			name: "space pack_id",
			mutate: func(manifest *Manifest) {
				manifest.PackID = "fixture pack"
			},
		},
		{
			name: "empty pack_version",
			mutate: func(manifest *Manifest) {
				manifest.PackVersion = ""
			},
		},
		{
			name: "whitespace pack_version",
			mutate: func(manifest *Manifest) {
				manifest.PackVersion = "1.0.0 beta"
			},
		},
		{
			name: "zero abi_version",
			mutate: func(manifest *Manifest) {
				manifest.ABIVersion = 0
			},
		},
		{
			name: "mismatched abi_version",
			mutate: func(manifest *Manifest) {
				manifest.ABIVersion = CurrentABIVersion + 1
			},
		},
		{
			name: "empty payload_sha256",
			mutate: func(manifest *Manifest) {
				manifest.PayloadSHA256 = ""
			},
		},
		{
			name: "short payload_sha256",
			mutate: func(manifest *Manifest) {
				manifest.PayloadSHA256 = validHash[:63]
			},
		},
		{
			name: "long payload_sha256",
			mutate: func(manifest *Manifest) {
				manifest.PayloadSHA256 = validHash + "0"
			},
		},
		{
			name: "uppercase payload_sha256",
			mutate: func(manifest *Manifest) {
				manifest.PayloadSHA256 = strings.ToUpper(validHash)
			},
		},
		{
			name: "non-hex payload_sha256",
			mutate: func(manifest *Manifest) {
				manifest.PayloadSHA256 = validHash[:63] + "g"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validTestManifest()
			tt.mutate(&manifest)

			if err := ValidateManifest(manifest, DefaultTrustPolicy()); err == nil {
				t.Fatal("ValidateManifest() error = nil, want error")
			}
		})
	}
}

func TestValidateManifestRejectsInvalidTrustPolicy(t *testing.T) {
	policy := DefaultTrustPolicy()
	policy.CurrentABIVersion = 0

	if err := ValidateManifest(validTestManifest(), policy); err == nil {
		t.Fatal("ValidateManifest() error = nil, want error")
	}
}

func TestValidateManifestRejectsUnknownCapabilities(t *testing.T) {
	manifest := validTestManifest()
	manifest.Capabilities = []Capability{CapabilityParseWASM, Capability("cap.filesystem.raw")}

	if err := ValidateManifest(manifest, DefaultTrustPolicy()); err == nil {
		t.Fatal("ValidateManifest() error = nil, want error")
	}
}

func TestValidateManifestRejectsMalformedDomains(t *testing.T) {
	invalidHosts := []string{
		"",
		"*",
		"*.example.com",
		"https://example.com",
		"example.com/path",
		"example.com:443",
		"com",
		"example..com",
		"-bad.example.com",
		"bad-.example.com",
		"Example.com",
		"example.com.",
		"exa_mple.com",
	}

	for _, host := range invalidHosts {
		t.Run("reject "+host, func(t *testing.T) {
			manifest := validTestManifest()
			manifest.Domains = []DomainRule{{Host: host}}

			if err := ValidateManifest(manifest, DefaultTrustPolicy()); err == nil {
				t.Fatal("ValidateManifest() error = nil, want error")
			}
		})
	}

	validDomains := []DomainRule{
		{Host: "fixture.invalid"},
		{Host: "assets.fixture.invalid", IncludeSubdomains: true},
	}

	for _, domain := range validDomains {
		t.Run("accept "+domain.Host, func(t *testing.T) {
			manifest := validTestManifest()
			manifest.Domains = []DomainRule{domain}

			if err := ValidateManifest(manifest, DefaultTrustPolicy()); err != nil {
				t.Fatalf("ValidateManifest() error = %v", err)
			}
		})
	}
}

func TestValidateManifestRejectsInvalidAliasPolicyMode(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{
			name: "nil domains",
			mutate: func(manifest *Manifest) {
				manifest.Domains = nil
			},
		},
		{
			name: "empty domains without refs",
			mutate: func(manifest *Manifest) {
				manifest.DomainPolicyRefs = nil
				manifest.BrokerPolicyRefs = nil
			},
		},
		{
			name: "mixed domains plus domain refs",
			mutate: func(manifest *Manifest) {
				manifest.Domains = []DomainRule{{Host: "opaque.test"}}
			},
		},
		{
			name: "mixed domains plus broker refs",
			mutate: func(manifest *Manifest) {
				manifest.Domains = []DomainRule{{Host: "opaque.test"}}
				manifest.DomainPolicyRefs = nil
			},
		},
		{
			name: "http capability without broker refs",
			mutate: func(manifest *Manifest) {
				manifest.BrokerPolicyRefs = nil
			},
		},
		{
			name: "auth capability without broker refs",
			mutate: func(manifest *Manifest) {
				manifest.Capabilities = []Capability{CapabilityParseWASM, CapabilityAuthProfile}
				manifest.BrokerPolicyRefs = nil
			},
		},
		{
			name: "broker refs without http or auth capability",
			mutate: func(manifest *Manifest) {
				manifest.Capabilities = []Capability{CapabilityParseWASM}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validAliasTestManifest()
			tt.mutate(&manifest)

			if err := ValidateManifest(manifest, DefaultTrustPolicy()); err == nil {
				t.Fatal("ValidateManifest() error = nil, want error")
			}
		})
	}
}

func TestValidateManifestRejectsMalformedAliasPolicyRefs(t *testing.T) {
	longRef := strings.Repeat("a", 65)
	malformedRefs := []string{
		"ab",
		longRef,
		"Alpha001",
		"dpr.alpha001",
		"dpr_alpha001",
		"dpr/alpha001",
		`dpr\alpha001`,
		"dpr:alpha001",
		"dpr@alpha001",
		"dpr?alpha001",
		"dpr#alpha001",
		"dpr%2falpha001",
		"dpr alpha001",
		"dpr\talpha001",
		"-alpha001",
		"alpha001-",
		"*.alpha001",
		"https://alpha001",
	}

	for _, ref := range malformedRefs {
		t.Run("domain "+ref, func(t *testing.T) {
			manifest := validAliasTestManifest()
			manifest.DomainPolicyRefs = []string{ref}

			if err := ValidateManifest(manifest, DefaultTrustPolicy()); err == nil {
				t.Fatal("ValidateManifest() error = nil, want error")
			}
		})

		t.Run("broker "+ref, func(t *testing.T) {
			manifest := validAliasTestManifest()
			manifest.BrokerPolicyRefs = []string{ref}

			if err := ValidateManifest(manifest, DefaultTrustPolicy()); err == nil {
				t.Fatal("ValidateManifest() error = nil, want error")
			}
		})
	}
}

func TestValidateManifestRejectsDuplicateAliasPolicyRefs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{
			name: "duplicate domain refs",
			mutate: func(manifest *Manifest) {
				manifest.DomainPolicyRefs = []string{"dpr-alpha001", "dpr-alpha001"}
			},
		},
		{
			name: "duplicate broker refs",
			mutate: func(manifest *Manifest) {
				manifest.BrokerPolicyRefs = []string{"bpr-alpha001", "bpr-alpha001"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validAliasTestManifest()
			tt.mutate(&manifest)

			if err := ValidateManifest(manifest, DefaultTrustPolicy()); err == nil {
				t.Fatal("ValidateManifest() error = nil, want error")
			}
		})
	}
}

func TestValidateManifestRejectsResourceLimitExcess(t *testing.T) {
	policy := DefaultTrustPolicy()

	tests := []struct {
		name   string
		mutate func(*ResourceLimits)
	}{
		{
			name: "zero timeout",
			mutate: func(limits *ResourceLimits) {
				limits.TimeoutMillis = 0
			},
		},
		{
			name: "negative timeout",
			mutate: func(limits *ResourceLimits) {
				limits.TimeoutMillis = -1
			},
		},
		{
			name: "timeout above max",
			mutate: func(limits *ResourceLimits) {
				limits.TimeoutMillis = policy.MaxResourceLimits.TimeoutMillis + 1
			},
		},
		{
			name: "zero memory pages",
			mutate: func(limits *ResourceLimits) {
				limits.MaxMemoryPages = 0
			},
		},
		{
			name: "memory pages above max",
			mutate: func(limits *ResourceLimits) {
				limits.MaxMemoryPages = policy.MaxResourceLimits.MaxMemoryPages + 1
			},
		},
		{
			name: "zero host calls",
			mutate: func(limits *ResourceLimits) {
				limits.MaxHostCalls = 0
			},
		},
		{
			name: "host calls above max",
			mutate: func(limits *ResourceLimits) {
				limits.MaxHostCalls = policy.MaxResourceLimits.MaxHostCalls + 1
			},
		},
		{
			name: "zero response bytes",
			mutate: func(limits *ResourceLimits) {
				limits.MaxResponseBytes = 0
			},
		},
		{
			name: "negative response bytes",
			mutate: func(limits *ResourceLimits) {
				limits.MaxResponseBytes = -1
			},
		},
		{
			name: "response bytes above max",
			mutate: func(limits *ResourceLimits) {
				limits.MaxResponseBytes = policy.MaxResourceLimits.MaxResponseBytes + 1
			},
		},
		{
			name: "zero output items",
			mutate: func(limits *ResourceLimits) {
				limits.MaxOutputItems = 0
			},
		},
		{
			name: "output items above max",
			mutate: func(limits *ResourceLimits) {
				limits.MaxOutputItems = policy.MaxResourceLimits.MaxOutputItems + 1
			},
		},
		{
			name: "zero output bytes",
			mutate: func(limits *ResourceLimits) {
				limits.MaxOutputBytes = 0
			},
		},
		{
			name: "negative output bytes",
			mutate: func(limits *ResourceLimits) {
				limits.MaxOutputBytes = -1
			},
		},
		{
			name: "output bytes above max",
			mutate: func(limits *ResourceLimits) {
				limits.MaxOutputBytes = policy.MaxResourceLimits.MaxOutputBytes + 1
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validTestManifest()
			tt.mutate(&manifest.ResourceLimits)

			if err := ValidateManifest(manifest, policy); err == nil {
				t.Fatal("ValidateManifest() error = nil, want error")
			}
		})
	}
}

func validTestManifest() Manifest {
	return Manifest{
		PackID:       "xpk-fixture01",
		PackVersion:  "1.0.0",
		ABIVersion:   CurrentABIVersion,
		Capabilities: []Capability{CapabilityParseWASM, CapabilityHTTPFetch},
		Domains: []DomainRule{
			{Host: "fixture.invalid", IncludeSubdomains: true},
		},
		ResourceLimits: ResourceLimits{
			TimeoutMillis:    1_000,
			MaxMemoryPages:   64,
			MaxHostCalls:     16,
			MaxResponseBytes: 1 << 20,
			MaxOutputItems:   100,
			MaxOutputBytes:   1 << 16,
		},
		PayloadSHA256: strings.Repeat("0123456789abcdef", 4),
	}
}

func validAliasTestManifest() Manifest {
	manifest := validTestManifest()
	manifest.PackID = "xpk-alpha001"
	manifest.PackVersion = "opaque-1"
	manifest.Capabilities = []Capability{CapabilityParseWASM, CapabilityHTTPFetch, CapabilityAuthProfile}
	manifest.Domains = []DomainRule{}
	manifest.DomainPolicyRefs = []string{"dpr-alpha001"}
	manifest.BrokerPolicyRefs = []string{"bpr-alpha001"}

	return manifest
}
