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
	pack.AssetSHA256 = strings.Repeat("a", 64)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, false)

	packs := EmbeddedReleasePacks()
	keys := EmbeddedReleaseTrustedPublicKeys()
	if len(packs) != 1 || len(keys) != 1 {
		t.Fatalf("embedded accessors returned packs=%d keys=%d, want 1/1", len(packs), len(keys))
	}
	packs[0].ManifestJSON[0] = '{' + 1
	packs[0].Payload[0] = 'X'
	packs[0].Signature[0] ^= 0xff
	packs[0].AssetSHA256 = strings.Repeat("b", 64)
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
	if freshPacks[0].AssetSHA256 != pack.AssetSHA256 {
		t.Fatalf("embedded asset sha was mutated")
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

func TestNewEmbeddedReleaseDispatcherAcceptsHostPolicyResolver(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(37)
	pack := signedAliasTestPack(t, privateKey, []byte("alias release payload"), nil)
	pack.AssetSHA256 = strings.Repeat("a", 64)
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	resolver := &fakeHostPolicyResolver{policy: validResolvedHostPolicy(verified.Identity, verified.Manifest)}
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{HostPolicyResolver: resolver})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() dispatcher = nil")
	}
	if matches := dispatcher.registry.FindByURL("https://share.alpha.test/path"); len(matches) != 1 {
		t.Fatalf("dispatcher registry resolver matches = %d, want 1", len(matches))
	}
	if dispatcher.runner == nil || dispatcher.runner.hostImports.HostPolicyResolver == nil {
		t.Fatalf("dispatcher runner did not retain host policy resolver")
	}
}

func TestNewEmbeddedReleaseDispatcherAliasWithoutResolverNoMatch(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(38)
	pack := signedAliasTestPack(t, privateKey, []byte("alias release payload"), nil)
	pack.AssetSHA256 = strings.Repeat("a", 64)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, false)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() dispatcher = nil")
	}
	if matches := dispatcher.registry.FindByURL("https://share.alpha.test/path"); len(matches) != 0 {
		t.Fatalf("dispatcher registry alias matches = %d, want no resolver no-match", len(matches))
	}
}

func TestNewEmbeddedReleaseDispatcherFailsClosedWhenRequiredAliasMissingPolicy(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(39)
	pack := signedAliasTestPack(t, privateKey, []byte("alias release payload"), nil)
	pack.AssetSHA256 = strings.Repeat("a", 64)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err == nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = nil, want required alias host-policy error")
	}
	if dispatcher != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher for missing required alias policy")
	}
	if err.Error() != "required embedded alias extractor host policy is not configured" {
		t.Fatalf("error = %q, want generic required alias policy error", err.Error())
	}
}

func TestNewEmbeddedReleaseDispatcherFailsClosedWhenRequiredAliasPolicyMismatches(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(40)
	pack := signedAliasTestPack(t, privateKey, []byte("alias release payload"), nil)
	pack.AssetSHA256 = strings.Repeat("a", 64)
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	mismatchedPolicy := validResolvedHostPolicy(verified.Identity, verified.Manifest)
	mismatchedPolicy.DomainPolicyRefs = []string{"dpr-alpha002"}
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{HostPolicyResolver: &fakeHostPolicyResolver{policy: mismatchedPolicy}})
	if err == nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = nil, want mismatched required alias policy error")
	}
	if dispatcher != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher for mismatched required alias policy")
	}
	if err.Error() != "required embedded alias extractor host policy is not configured" {
		t.Fatalf("error = %q, want generic required alias policy error", err.Error())
	}
}

func TestNewEmbeddedReleaseDispatcherAcceptsMatchingPrivateBundleResolver(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(41)
	pack := signedAliasTestPack(t, privateKey, []byte("alias release payload"), nil)
	pack.AssetSHA256 = strings.Repeat("a", 64)
	verified, err := VerifyEmbeddedPack(pack, policyWithKeys(publicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	resolver, err := NewPrivatePolicyBundleResolver(privatePolicyBundleRaw(t, []privatePolicyBundlePackFixture{{Identity: verified.Identity, Manifest: verified.Manifest}}, nil), PrivatePolicyBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivatePolicyBundleResolver() error = %v", err)
	}
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{HostPolicyResolver: resolver})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() dispatcher = nil")
	}
	if matches := dispatcher.registry.FindByURL("https://share.alpha.test/path"); len(matches) != 1 {
		t.Fatalf("dispatcher registry resolver matches = %d, want 1", len(matches))
	}
}

func TestNewEmbeddedReleaseDispatcherRequiredLegacyPackDoesNotRequireHostPolicy(t *testing.T) {
	publicKey, privateKey := deterministicKeyPair(42)
	pack := signedTestPack(t, privateKey, []byte("legacy release payload"), nil)
	withEmbeddedReleaseState(t, []EmbeddedPack{pack}, []ed25519.PublicKey{publicKey}, true)

	dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{})
	if err != nil {
		t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
	}
	if dispatcher == nil {
		t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() dispatcher = nil")
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

func TestNewEmbeddedReleaseDispatcherRequiredMixedPacksRequireRuntimeOnlyForAuthCapable(t *testing.T) {
	authPublicKey, authPrivateKey := deterministicKeyPair(47)
	nonAuthPublicKey, nonAuthPrivateKey := deterministicKeyPair(48)
	authPack := signedAuthCapableTestPack(t, authPrivateKey, []byte("auth release payload"), nil)
	nonAuthPack := signedTestPack(t, nonAuthPrivateKey, []byte("non auth release payload"), func(values map[string]any) {
		values["pack_id"] = "xpk-beta001"
		values["pack_version"] = "opaque-2"
	})
	nonAuthPack.AssetSHA256 = strings.Repeat("b", 64)
	authVerified, err := VerifyEmbeddedPack(authPack, policyWithKeys(authPublicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack(auth) error = %v", err)
	}
	nonAuthVerified, err := VerifyEmbeddedPack(nonAuthPack, policyWithKeys(nonAuthPublicKey))
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack(non-auth) error = %v", err)
	}
	packs := []EmbeddedPack{authPack, nonAuthPack}
	keys := []ed25519.PublicKey{authPublicKey, nonAuthPublicKey}

	t.Run("auth capable only accepted", func(t *testing.T) {
		withEmbeddedReleaseState(t, packs, keys, true)

		dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{AuthRuntimeBundle: newPrivateAuthRuntimeBundleForTest(t, authVerified.Identity)})
		if err != nil {
			t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = %v", err)
		}
		if dispatcher == nil {
			t.Fatal("NewEmbeddedReleaseAddTaskDispatcher() dispatcher = nil")
		}
	})

	t.Run("extra non auth runtime rejected", func(t *testing.T) {
		withEmbeddedReleaseState(t, packs, keys, true)

		dispatcher, err := NewEmbeddedReleaseAddTaskDispatcher(EmbeddedReleaseDispatcherConfig{AuthRuntimeBundle: newPrivateAuthRuntimeBundleForTest(t, authVerified.Identity, nonAuthVerified.Identity)})
		if err == nil {
			t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() error = nil, dispatcher=%#v", dispatcher)
		}
		if dispatcher != nil {
			t.Fatalf("NewEmbeddedReleaseAddTaskDispatcher() returned dispatcher for extra non-auth runtime")
		}
		if err.Error() != "required embedded authenticated extractor auth runtime is not configured" {
			t.Fatalf("error = %q, want generic auth runtime error", err.Error())
		}
	})
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

func newPrivateAuthRuntimeBundleForTest(t *testing.T, identities ...VerifiedPackIdentity) *PrivateAuthRuntimeBundle {
	t.Helper()
	if len(identities) == 0 {
		t.Fatal("newPrivateAuthRuntimeBundleForTest requires at least one identity")
	}

	fixtures := make([]privateAuthRuntimePackFixture, 0, len(identities))
	for i, identity := range identities {
		profileRef := "apr-alpha001"
		loginURL := "https://fixture.invalid/login"
		if i > 0 {
			profileRef = "apr-alpha002"
			loginURL = "https://example.test/login"
		}
		fixtures = append(fixtures, privateAuthRuntimePackFixture{Identity: identity, ProfileRef: profileRef, Kind: AuthSecretKindBearer, LoginURL: loginURL})
	}
	raw := privateAuthRuntimeBundleRaw(t, fixtures, nil)
	bundle, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}

	return bundle
}
