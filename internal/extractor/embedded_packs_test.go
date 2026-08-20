package extractor

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestEmbeddedReleasePacksNoPackFallback(t *testing.T) {
	withEmbeddedReleaseState(t, nil, nil, false)

	if EmbeddedReleasePackCount() != 0 || HasEmbeddedReleasePacks() || EmbeddedReleaseRequired() {
		t.Fatalf("empty embedded release state helpers returned count=%d has=%t required=%t", EmbeddedReleasePackCount(), HasEmbeddedReleasePacks(), EmbeddedReleaseRequired())
	}
	if packs := EmbeddedReleasePacks(); len(packs) != 0 {
		t.Fatalf("EmbeddedReleasePacks() length = %d, want 0", len(packs))
	}
	if keys := EmbeddedReleaseTrustedPublicKeys(); len(keys) != 0 {
		t.Fatalf("EmbeddedReleaseTrustedPublicKeys() length = %d, want 0", len(keys))
	}

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher for no-pack fallback")
	}
}

func TestNewEmbeddedReleaseDispatcherFailsClosedWhenRequiredNoPacks(t *testing.T) {
	withEmbeddedReleaseState(t, nil, nil, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err == nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = nil, want required no-pack error")
	}
	if dispatcher != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher for required no-pack state")
	}
	if !strings.Contains(err.Error(), "required") || !strings.Contains(err.Error(), "none are configured") {
		t.Fatalf("error %q does not describe required no-pack failure", err.Error())
	}
}

func TestEmbeddedReleasePacksDefensiveCopies(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(32)
	pack := signedTestPack(t, privateKey, []byte("fixture wasm bytes"), nil)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, false)

	packs := EmbeddedReleasePacks()
	keys := EmbeddedReleaseTrustedPublicKeys()
	if len(packs) != 1 || len(keys) != 1 {
		t.Fatalf("embedded accessors returned packs=%d keys=%d, want 1/1", len(packs), len(keys))
	}
	packs[0].ManifestJSON[0] = '{' + 1
	packs[0].Payload[0] = 'X'
	packs[0].Signature[0] ^= 0xff
	keys[0][0] ^= 0xff

	freshPacks := EmbeddedReleasePacks()
	freshKeys := EmbeddedReleaseTrustedPublicKeys()
	if string(freshPacks[0].Payload) != "fixture wasm bytes" {
		t.Fatalf("embedded payload mutated to %q", freshPacks[0].Payload)
	}
	if freshPacks[0].ManifestJSON[0] != pack.ManifestJSON[0] {
		t.Fatalf("embedded manifest bytes were mutated")
	}
	if freshPacks[0].Signature[0] != pack.Signature[0] {
		t.Fatalf("embedded signature bytes were mutated")
	}
	if freshKeys[0][0] != publicKey[0] {
		t.Fatalf("embedded public key bytes were mutated")
	}
}

func TestNewEmbeddedReleaseDispatcherLoadsVerifiedPacks(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(33)
	pack := signedTestPack(t, privateKey, []byte("verified payload"), nil)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)
	if EmbeddedReleasePackCount() != 1 || !HasEmbeddedReleasePacks() || !EmbeddedReleaseRequired() {
		t.Fatalf("embedded release state helpers returned count=%d has=%t required=%t", EmbeddedReleasePackCount(), HasEmbeddedReleasePacks(), EmbeddedReleaseRequired())
	}

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned nil dispatcher")
	}
}

func TestEmbeddedReleaseDispatcherAliasResolverOptionalFailClosed(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(37)
	pack := signedTestPack(t, privateKey, []byte("alias verified payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["pack_version"] = "opaque-1"
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
	})
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, false)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() no resolver error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("dispatcher = nil, want verified alias dispatcher")
	}
	if matches := dispatcher.registry.FindByURL("https://share.alpha.test/item"); len(matches) != 0 {
		t.Fatalf("alias matches without resolver = %d, want 0", len(matches))
	}

	verified := dispatcher.registry.Packs()[0]
	dispatcher, err = NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{
		HostPolicyResolver: &fakeHostPolicyResolver{policy: syntheticHostPolicy(verified.Identity)},
	})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() with resolver error = %v", err)
	}
	if matches := dispatcher.registry.FindByURL("https://share.alpha.test/item"); len(matches) != 1 {
		t.Fatalf("alias matches with resolver = %d, want 1", len(matches))
	}
}

func TestNewEmbeddedReleaseDispatcherFailsClosedWhenRequiredAliasHasNoResolver(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(38)
	pack := signedTestPack(t, privateKey, []byte("alias verified payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["pack_version"] = "opaque-1"
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
	})
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err == nil {
		t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() error = nil, want required alias policy error")
	}
	if dispatcher != nil {
		t.Fatal("dispatcher != nil for required alias without resolver")
	}
	if !strings.Contains(err.Error(), "required embedded extractor alias host policy is invalid") {
		t.Fatalf("error = %q, want generic required alias policy error", err.Error())
	}
}

func TestNewEmbeddedReleaseDispatcherFailsClosedWhenRequiredAliasPolicyMismatches(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(39)
	pack := signedTestPack(t, privateKey, []byte("alias verified payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["pack_version"] = "opaque-1"
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
	})
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	policy := syntheticHostPolicy(verified.Identity)
	policy.PackIdentity.PublicKeySHA256 = hashString('9')
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{HostPolicyResolver: &fakeHostPolicyResolver{policy: policy}})
	if err == nil {
		t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() error = nil, want mismatched policy error")
	}
	if dispatcher != nil {
		t.Fatal("dispatcher != nil for required alias mismatched resolver")
	}
	if strings.Contains(err.Error(), hashString('9')) || !strings.Contains(err.Error(), "required embedded extractor alias host policy is invalid") {
		t.Fatalf("error is not generic/redacted: %q", err.Error())
	}
}

func TestNewEmbeddedReleaseDispatcherRequiredAliasWithBundleResolver(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(40)
	pack := signedTestPack(t, privateKey, []byte("alias verified payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["pack_version"] = "opaque-1"
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
	})
	pack.AssetSHA256 = hashString('a')
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	raw, privateSHA := mustPrivatePolicyBundleJSON(t, verified.Identity, nil)
	resolver, err := NewPrivatePolicyBundleResolver(raw, PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: privateSHA})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	authRuntimeBundle := newPrivateAuthRuntimeBundleForTest(t, verified.Identity)
	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{HostPolicyResolver: resolver, AuthRuntimeBundle: authRuntimeBundle})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("dispatcher = nil for required alias bundle resolver")
	}
	if matches := dispatcher.registry.FindByURL("https://share.alpha.test/item"); len(matches) != 1 {
		t.Fatalf("alias matches with bundle resolver = %d, want 1", len(matches))
	}
}

func TestNewEmbeddedReleaseDispatcherRequiredAuthCapablePackRequiresAuthRuntime(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(43)
	pack := signedAuthCapableTestPack(t, privateKey, []byte("auth release payload"), nil)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err == nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = nil, dispatcher=%#v", dispatcher)
	}
	if dispatcher != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher for missing auth runtime")
	}
	if err.Error() != "required embedded authenticated extractor auth runtime is not configured" {
		t.Fatalf("error = %q, want generic auth runtime error", err.Error())
	}
}

func TestNewEmbeddedReleaseDispatcherRequiredAuthCapablePackRejectsMismatchedAuthRuntime(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(44)
	pack := signedAuthCapableTestPack(t, privateKey, []byte("auth release payload"), nil)
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	mismatchedIdentity := verified.Identity
	mismatchedIdentity.PackID = "xpk-beta001"
	bundle := newPrivateAuthRuntimeBundleForTest(t, mismatchedIdentity)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{AuthRuntimeBundle: bundle})
	if err == nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = nil, dispatcher=%#v", dispatcher)
	}
	if dispatcher != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher for mismatched auth runtime")
	}
	if err.Error() != "required embedded authenticated extractor auth runtime is not configured" {
		t.Fatalf("error = %q, want generic auth runtime error", err.Error())
	}
}

func TestNewEmbeddedReleaseDispatcherRequiredAuthCapablePackAcceptsMatchingAuthRuntime(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(45)
	pack := signedAuthCapableTestPack(t, privateKey, []byte("auth release payload"), nil)
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	bundle := newPrivateAuthRuntimeBundleForTest(t, verified.Identity)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{AuthRuntimeBundle: bundle})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() dispatcher = nil")
	}
}

func TestNewEmbeddedReleaseDispatcherRequiredNonAuthPackDoesNotRequireAuthRuntime(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(46)
	pack := signedTestPack(t, privateKey, []byte("non auth release payload"), nil)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() dispatcher = nil")
	}
}

func TestNewEmbeddedReleaseDispatcherFailsClosedWhenAnyPackRejectedRequired(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(36)
	validPack := signedTestPack(t, privateKey, []byte("verified payload"), nil)
	tamperedPack := signedTestPack(t, privateKey, []byte("tampered payload must not leak"), nil)
	tamperedPack.Signature[0] ^= 0xff
	withEmbeddedReleaseState(t, []EmbeddedPack{validPack, tamperedPack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err == nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = nil, want partial rejection fail-closed error")
	}
	if dispatcher != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher after required partial rejection")
	}
	if !strings.Contains(err.Error(), "rejected configured packs") {
		t.Fatalf("error %q does not describe partial rejection", err.Error())
	}
	if strings.Contains(err.Error(), "tampered payload") {
		t.Fatalf("error leaks payload bytes: %q", err.Error())
	}
}

func TestNewEmbeddedReleaseDispatcherFailsClosedWhenAllPacksRejected(t *testing.T) {
	_, privateKey := deterministicKeyPair(34)
	wrongPublicKey, _ := deterministicKeyPair(35)
	pack := signedTestPack(t, privateKey, []byte("private fixture payload must not leak"), nil)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{wrongPublicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err == nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = nil, want fail-closed error")
	}
	if dispatcher != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher after rejecting all packs")
	}
	if !strings.Contains(err.Error(), "rejected configured packs") {
		t.Fatalf("error %q does not describe fail-closed rejection", err.Error())
	}
	if strings.Contains(err.Error(), "private fixture payload") {
		t.Fatalf("error leaks payload bytes: %q", err.Error())
	}
}

func withEmbeddedReleaseState(t *testing.T, packs []EmbeddedPack, keys []ed25519.PublicKey, required bool) {
	t.Helper()

	oldPacks := embeddedReleasePacks
	oldKeys := embeddedReleaseTrustedPublicKeys
	oldRequired := embeddedReleaseRequired
	embeddedReleasePacks = packs
	embeddedReleaseTrustedPublicKeys = keys
	embeddedReleaseRequired = required
	t.Cleanup(func() {
		embeddedReleasePacks = oldPacks
		embeddedReleaseTrustedPublicKeys = oldKeys
		embeddedReleaseRequired = oldRequired
	})
}

func signedAuthCapableTestPack(t *testing.T, privateKey ed25519.PrivateKey, payload []byte, mutate func(map[string]any)) EmbeddedPack {
	t.Helper()

	pack := signedTestPack(t, privateKey, payload, func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["capabilities"] = []string{string(CapabilityParseWASM), string(CapabilityHTTPFetch), string(CapabilityAuthProfile)}
		if mutate != nil {
			mutate(values)
		}
	})
	pack.AssetSHA256 = strings.Repeat("a", 64)

	return pack
}

func newPrivateAuthRuntimeBundleForTest(t *testing.T, identity VerifiedPackIdentity) *PrivateAuthRuntimeBundle {
	t.Helper()

	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: identity, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"}}, nil)
	bundle, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}

	return bundle
}
