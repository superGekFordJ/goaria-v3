package extractor

import (
	"testing"
)

func TestCloneResolvedAddItem_DoesNotAliasPolicyOrManifest(t *testing.T) {
	t.Parallel()

	policy := &ResolvedHostPolicy{
		OutputDomains: []HostPolicyOutputRule{{
			Host:         "download.fixture.invalid",
			PathPrefixes: []string{"/files/"},
		}},
	}
	item := ResolvedAddItem{
		URL: "https://download.fixture.invalid/files/a.bin",
		PackManifest: Manifest{
			PackID:  "hostcall-fixture",
			Domains: []DomainRule{{Host: "share.fixture.invalid"}},
		},
		HostPolicy: policy,
		Metadata:   map[string]string{"filename": "a.bin"},
	}

	cloned := CloneResolvedAddItem(item)
	if cloned.HostPolicy == nil || cloned.HostPolicy == item.HostPolicy {
		t.Fatal("CloneResolvedAddItem HostPolicy pointer was aliased or nil")
	}
	if len(cloned.HostPolicy.OutputDomains) == 0 {
		t.Fatal("cloned OutputDomains is empty")
	}
	cloned.HostPolicy.OutputDomains[0].Host = "mutated.fixture.invalid"
	if item.HostPolicy.OutputDomains[0].Host != "download.fixture.invalid" {
		t.Fatal("CloneResolvedAddItem aliased HostPolicy.OutputDomains")
	}
	if len(cloned.PackManifest.Domains) == 0 {
		t.Fatal("cloned Manifest Domains is empty")
	}
	cloned.PackManifest.Domains[0].Host = "mutated.fixture.invalid"
	if item.PackManifest.Domains[0].Host != "share.fixture.invalid" {
		t.Fatal("CloneResolvedAddItem aliased Manifest Domains")
	}
	cloned.Metadata["filename"] = "mutated.bin"
	if item.Metadata["filename"] != "a.bin" {
		t.Fatal("CloneResolvedAddItem aliased Metadata")
	}
}

func TestValidateLeaseOutputURL_NilPolicyNonAliasOK(t *testing.T) {
	t.Parallel()

	item := ResolvedAddItem{
		URL: "https://download.fixture.invalid/files/a.bin",
		PackManifest: Manifest{
			PackID:  "hostcall-fixture",
			Domains: []DomainRule{{Host: "share.fixture.invalid"}},
		},
	}
	if err := ValidateLeaseOutputURL(item); err != nil {
		t.Fatalf("ValidateLeaseOutputURL() error = %v, want nil for non-alias nil policy", err)
	}
}

func TestValidateLeaseOutputURL_AliasNilPolicyFails(t *testing.T) {
	t.Parallel()

	item := ResolvedAddItem{
		URL: "https://files.alpha.test/downloads/file.bin",
		PackManifest: Manifest{
			PackID:           "xpk-alias",
			DomainPolicyRefs: []string{"dpr-alpha001"},
		},
	}
	if err := ValidateLeaseOutputURL(item); err == nil {
		t.Fatal("ValidateLeaseOutputURL() error = nil, want error for alias + nil policy")
	}
}

func TestValidateLeaseOutputURL_PolicyAllowDeny(t *testing.T) {
	t.Parallel()

	policy := &ResolvedHostPolicy{
		OutputDomains: []HostPolicyOutputRule{{
			Host:              "files.alpha.test",
			IncludeSubdomains: true,
			PathPrefixes:      []string{"/downloads/"},
		}},
	}
	allow := ResolvedAddItem{
		URL:        "https://files.alpha.test/downloads/file.bin",
		HostPolicy: policy,
	}
	if err := ValidateLeaseOutputURL(allow); err != nil {
		t.Fatalf("ValidateLeaseOutputURL(allow) error = %v", err)
	}

	deny := ResolvedAddItem{
		URL:        "https://files.alpha.test/private/file.bin",
		HostPolicy: policy,
	}
	if err := ValidateLeaseOutputURL(deny); err == nil {
		t.Fatal("ValidateLeaseOutputURL(deny) error = nil, want deny")
	}

	fixtureDeny := ResolvedAddItem{
		URL:        "https://download.fixture.invalid/files/a.bin",
		HostPolicy: policy,
	}
	if err := ValidateLeaseOutputURL(fixtureDeny); err == nil {
		t.Fatal("ValidateLeaseOutputURL(fixture host under alpha policy) error = nil, want deny")
	}
}

func TestValidateLeaseOutputURL_HTTPRejectedAtCommit(t *testing.T) {
	t.Parallel()

	item := ResolvedAddItem{
		URL: "http://download.fixture.invalid/files/a.bin",
		PackManifest: Manifest{
			PackID:  "hostcall-fixture",
			Domains: []DomainRule{{Host: "share.fixture.invalid"}},
		},
	}
	if err := ValidateLeaseOutputURL(item); err == nil {
		t.Fatal("ValidateLeaseOutputURL(http) error = nil, want https-only at lease commit")
	}
}

func TestValidateLeaseOutputURL_LegacyCredentialedDomainScope(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		PackID:  "xpk-legacy",
		Domains: []DomainRule{{Host: "files.alpha.test", IncludeSubdomains: true}},
	}
	credentialed := ResolvedAddItem{
		URL:             "https://attacker.example/x",
		PackManifest:    manifest,
		DownloadAuthRef: "dar-0123456789abcdef0123456789abcdef",
	}
	if err := ValidateLeaseOutputURL(credentialed); err == nil {
		t.Fatal("ValidateLeaseOutputURL() error = nil, want credentialed out-of-domain rejection")
	}

	inDomain := credentialed
	inDomain.URL = "https://cdn.files.alpha.test/x"
	if err := ValidateLeaseOutputURL(inDomain); err != nil {
		t.Fatalf("ValidateLeaseOutputURL() error = %v, want in-domain credentialed item accepted", err)
	}

	authProfile := ResolvedAddItem{
		URL:            "https://attacker.example/x",
		PackManifest:   manifest,
		AuthProfileRef: "apr-fixture01",
	}
	if err := ValidateLeaseOutputURL(authProfile); err == nil {
		t.Fatal("ValidateLeaseOutputURL() error = nil, want auth_profile_ref out-of-domain rejection")
	}

	uncredentialed := ResolvedAddItem{
		URL:          "https://attacker.example/x",
		PackManifest: manifest,
	}
	if err := ValidateLeaseOutputURL(uncredentialed); err != nil {
		t.Fatalf("ValidateLeaseOutputURL() error = %v, want uncredentialed cross-domain item accepted", err)
	}
}
