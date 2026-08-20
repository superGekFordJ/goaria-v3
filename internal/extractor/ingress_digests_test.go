package extractor

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestHashIngressHostGoldenVector(t *testing.T) {
	salt := append(make([]byte, 15), 0x01)
	got := hashIngressHost(salt, "example.com")
	const want = "43555cdbabd343dc782e44cdabbb56241898532bb1d8319d21e22a6997e6840b"
	if got != want {
		t.Fatalf("golden digest = %s, want %s", got, want)
	}
}

func TestIngressDigestSourceGoldenSnapshot(t *testing.T) {
	src := NewIngressDigestSource(&Registry{packs: []VerifiedPack{{
		Manifest: Manifest{Domains: []DomainRule{{Host: "example.com"}}},
	}}})
	src.saltRead = bytes.NewReader(append(make([]byte, 15), 0x01))
	snap, ok := src.Snapshot()
	if !ok {
		t.Fatal("Snapshot() ok=false")
	}
	if snap.Version != ingressDigestVersion {
		t.Fatalf("Version=%d, want %d", snap.Version, ingressDigestVersion)
	}
	if snap.Salt != "00000000000000000000000000000001" {
		t.Fatalf("Salt=%q", snap.Salt)
	}
	if len(snap.ExactDigests) != 1 || snap.ExactDigests[0] != "43555cdbabd343dc782e44cdabbb56241898532bb1d8319d21e22a6997e6840b" {
		t.Fatalf("ExactDigests=%v", snap.ExactDigests)
	}
	if snap.SubdomainDigests == nil {
		t.Fatal("SubdomainDigests must be non-nil empty slice")
	}
}

func TestIngressDigestSourceLegacyExactVsSubdomainSplit(t *testing.T) {
	src := NewIngressDigestSource(&Registry{packs: []VerifiedPack{{
		Manifest: Manifest{Domains: []DomainRule{
			{Host: "cdn.example.test"},
			{Host: "example.test", IncludeSubdomains: true},
		}},
	}}})
	if !src.Ready() {
		t.Fatal("Ready()=false, want true")
	}
	if len(src.exact) != 1 || src.exact[0] != "cdn.example.test" {
		t.Fatalf("exact hosts=%v, want [cdn.example.test]", src.exact)
	}
	if len(src.subdomain) != 1 || src.subdomain[0] != "example.test" {
		t.Fatalf("subdomain hosts=%v, want [example.test]", src.subdomain)
	}
	if containsString(src.exact, "example.test") {
		t.Fatal("subdomain rule must not put host in exact set")
	}

	snap, ok := src.Snapshot()
	if !ok {
		t.Fatal("Snapshot() ok=false")
	}
	salt, err := decodeSaltHex(snap.Salt)
	if err != nil {
		t.Fatalf("salt: %v", err)
	}
	cdnDigest := hashIngressHost(salt, "cdn.example.test")
	apexDigest := hashIngressHost(salt, "example.test")
	if !containsString(snap.ExactDigests, cdnDigest) {
		t.Fatal("exact set missing cdn.example.test digest")
	}
	if containsString(snap.SubdomainDigests, cdnDigest) {
		t.Fatal("exact cdn.example.test digest must not be a parent-walk target")
	}
	if containsString(snap.ExactDigests, apexDigest) {
		t.Fatal("IncludeSubdomains host must not appear in exact_digests")
	}
	if !containsString(snap.SubdomainDigests, apexDigest) {
		t.Fatal("IncludeSubdomains host missing from subdomain_digests")
	}
}

func TestIngressDigestSourceAliasIngressOnly(t *testing.T) {
	pack := syntheticAliasVerifiedPack()
	resolver := &fakeHostPolicyResolver{policy: syntheticHostPolicy(pack.Identity)}
	registry := &Registry{packs: []VerifiedPack{pack}, hostPolicyResolver: resolver}
	src := NewIngressDigestSource(registry)
	if !src.Ready() {
		t.Fatal("Ready()=false")
	}
	if len(src.exact) != 1 || src.exact[0] != "share.alpha.test" {
		t.Fatalf("alias exact=%v, want [share.alpha.test]", src.exact)
	}
	if containsString(src.exact, "api.alpha.test") || containsString(src.subdomain, "api.alpha.test") {
		t.Fatal("broker api.alpha.test must not be collected")
	}
	if containsString(src.exact, "files.alpha.test") || containsString(src.subdomain, "files.alpha.test") {
		t.Fatal("output files.alpha.test must not be collected")
	}

	snap, ok := src.Snapshot()
	if !ok {
		t.Fatal("Snapshot() ok=false")
	}
	salt, err := decodeSaltHex(snap.Salt)
	if err != nil {
		t.Fatalf("salt: %v", err)
	}
	if !containsString(snap.ExactDigests, hashIngressHost(salt, "share.alpha.test")) {
		t.Fatal("share.alpha.test digest missing")
	}
	if containsString(snap.ExactDigests, hashIngressHost(salt, "api.alpha.test")) ||
		containsString(snap.SubdomainDigests, hashIngressHost(salt, "api.alpha.test")) ||
		containsString(snap.ExactDigests, hashIngressHost(salt, "files.alpha.test")) ||
		containsString(snap.SubdomainDigests, hashIngressHost(salt, "files.alpha.test")) {
		t.Fatal("broker/output hosts were hashed")
	}
}

func TestIngressDigestSourceEmptyRegistryNotReady(t *testing.T) {
	src := NewIngressDigestSource(&Registry{})
	if src.Ready() {
		t.Fatal("Ready()=true on empty registry")
	}
	reader := &countingFailReader{}
	src.saltRead = reader
	if _, ok := src.Snapshot(); ok {
		t.Fatal("Snapshot() ok=true without hosts; must not read salt")
	}
	if reader.reads != 0 {
		t.Fatalf("ReadFull must be skipped when Ready is false, reads=%d", reader.reads)
	}
}

func TestIngressDigestSourceJSONOmitsFixtureHosts(t *testing.T) {
	const fixture = "cdn.example.test"
	registry := &Registry{packs: []VerifiedPack{{
		Manifest: Manifest{Domains: []DomainRule{{Host: fixture}}},
	}}}
	src := NewIngressDigestSource(registry)
	snap, ok := src.Snapshot()
	if !ok {
		t.Fatal("Snapshot() ok=false")
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	blob := string(raw) + snap.Salt + strings.Join(snap.ExactDigests, "") + strings.Join(snap.SubdomainDigests, "")
	if strings.Contains(blob, fixture) {
		t.Fatalf("snapshot leaked fixture host %q: %s", fixture, raw)
	}
	packs := registry.Packs()
	if len(packs) != 1 || len(packs[0].Manifest.Domains) != 1 || packs[0].Manifest.Domains[0].Host != fixture {
		t.Fatalf("Packs() should still contain %q, got %#v", fixture, packs)
	}
}

func decodeSaltHex(s string) ([]byte, error) {
	if s != strings.ToLower(s) {
		return nil, io.ErrUnexpectedEOF
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(out) != ingressDigestSaltBytes {
		return nil, io.ErrUnexpectedEOF
	}
	return out, nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

type countingFailReader struct {
	reads int
}

func (r *countingFailReader) Read([]byte) (int, error) {
	r.reads++
	return 0, io.ErrUnexpectedEOF
}
