package extractor

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateAuthRuntimeBundleLoadsAndReturnsCopies(t *testing.T) {
	identityOne := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	identityTwo := privateAuthRuntimeIdentity("xpk-alpha002", "opaque-2", "2")
	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{
		{Identity: identityOne, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"},
		{Identity: identityTwo, ProfileRef: "apr-alpha002", Kind: AuthSecretKindCookie, LoginURL: "https://api.alpha.test/login"},
	}, nil)

	bundle, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{})
	if err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = %v", err)
	}
	for i := range raw {
		raw[i] = 0
	}
	if got := bundle.PackCount(); got != 2 {
		t.Fatalf("PackCount() = %d, want 2", got)
	}
	identities := bundle.PackIdentities()
	if len(identities) != 2 || identities[0] != identityOne || identities[1] != identityTwo {
		t.Fatalf("PackIdentities() = %#v", identities)
	}
	identities[0] = privateAuthRuntimeIdentity("xpk-mutated001", "opaque-1", "3")
	if fresh := bundle.PackIdentities(); fresh[0] != identityOne {
		t.Fatalf("PackIdentities() returned mutable backing data: %#v", fresh)
	}

	pack, ok := bundle.PackRuntime(identityOne)
	if !ok {
		t.Fatal("PackRuntime() ok = false")
	}
	if pack.PackIdentity != identityOne || pack.StoreBinding.Scope != "pack" || len(pack.Profiles) != 1 {
		t.Fatalf("PackRuntime() returned unexpected pack: %#v", pack)
	}
	if pack.Profiles[0].ProfileRef != AuthProfileID("apr-alpha001") || pack.Profiles[0].Kind != AuthSecretKindBearer || pack.Profiles[0].Login.URL != "https://share.alpha.test/login" {
		t.Fatalf("PackRuntime() returned unexpected profile: %#v", pack.Profiles[0])
	}
	pack.StoreBinding.ProfileRefs[0] = "apr-mutated"
	pack.Profiles[0].ProfileRef = "apr-mutated"
	pack.Profiles[0].Login.AllowedDomains[0].Host = "mutated.alpha.test"
	pack.Profiles[0].Login.CallbackTransport.MaxBodyBytes = 1
	pack.Profiles[0].Login.Capture.SecretCandidates[0] = "mutated"
	pack.Provisioning.ProfileRefs[0] = "apr-mutated"
	pack.Materialization.ProfileRefs[0] = "apr-mutated"

	fresh, ok := bundle.PackRuntime(identityOne)
	if !ok {
		t.Fatal("PackRuntime() fresh ok = false")
	}
	if fresh.StoreBinding.ProfileRefs[0] != "apr-alpha001" || fresh.Profiles[0].ProfileRef != "apr-alpha001" || fresh.Profiles[0].Login.AllowedDomains[0].Host != "share.alpha.test" || fresh.Profiles[0].Login.CallbackTransport.ContentTypes[0] != "application/json" || fresh.Profiles[0].Login.Capture.SecretCandidates[0] != "secret" || fresh.Provisioning.ProfileRefs[0] != "apr-alpha001" || fresh.Materialization.ProfileRefs[0] != "apr-alpha001" {
		t.Fatalf("PackRuntime() did not return defensive copies: %#v", fresh)
	}
}

func TestPrivateAuthRuntimeBundleRejectsMalformedBundles(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	basePack := privateAuthRuntimePackFixture{Identity: identity, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"}
	validRaw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, nil)
	validHash := privateAuthRuntimeHash(t, validRaw)

	tests := []struct {
		name string
		raw  []byte
		opts PrivateAuthRuntimeBundleLoadOptions
	}{
		{name: "malformed json", raw: []byte(`{`)},
		{name: "unknown envelope field", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) { bundle["unknown"] = true })},
		{name: "unknown runtime field", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, runtime map[string]any, _ []map[string]any) { runtime["unknown"] = true })},
		{name: "unknown pack field", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) { packs[0]["unknown"] = true })},
		{name: "unknown nested section field", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["store_binding"].(map[string]any)["unknown"] = true
		})},
		{name: "trailing json", raw: append(cloneBytes(validRaw), []byte(` {}`)...)},
		{name: "trailing runtime json", raw: privateAuthRuntimeBundleRawWithInvalidRuntime(t, append(privateAuthRuntimeRuntimeRaw(t, []privateAuthRuntimePackFixture{basePack}, nil), []byte(` {}`)...))},
		{name: "unsupported schema", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) { bundle["schema_version"] = 2 })},
		{name: "bad bundle id", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["bundle_id"] = "arb.alpha001"
		})},
		{name: "bad bundle version", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["bundle_version"] = "opaque 1"
		})},
		{name: "missing runtime", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["runtime"] = json.RawMessage(nil)
		})},
		{name: "bad private sha", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["auth_runtime_private_sha256"] = strings.Repeat("A", 64)
		})},
		{name: "private sha mismatch", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["auth_runtime_private_sha256"] = strings.Repeat("1", 64)
		})},
		{name: "expected sha mismatch", raw: validRaw, opts: PrivateAuthRuntimeBundleLoadOptions{ExpectedAuthRuntimePrivateSHA256: strings.Repeat("2", 64)}},
		{name: "malformed expected sha", raw: validRaw, opts: PrivateAuthRuntimeBundleLoadOptions{ExpectedAuthRuntimePrivateSHA256: strings.Repeat("A", 64)}},
		{name: "whitespace expected sha", raw: validRaw, opts: PrivateAuthRuntimeBundleLoadOptions{ExpectedAuthRuntimePrivateSHA256: validHash + " "}},
		{name: "malformed public fingerprint", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["auth_runtime_public_fingerprint"] = strings.Repeat("G", 64)
		})},
		{name: "expected public fingerprint mismatch", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(bundle map[string]any, _ map[string]any, _ []map[string]any) {
			bundle["auth_runtime_public_fingerprint"] = strings.Repeat("3", 64)
		}), opts: PrivateAuthRuntimeBundleLoadOptions{ExpectedAuthRuntimePublicFingerprint: strings.Repeat("4", 64)}},
		{name: "malformed expected public fingerprint", raw: validRaw, opts: PrivateAuthRuntimeBundleLoadOptions{ExpectedAuthRuntimePublicFingerprint: strings.Repeat("Z", 64)}},
		{name: "zero packs", raw: privateAuthRuntimeBundleRaw(t, nil, nil)},
		{name: "malformed identity", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: mutatePrivateAuthRuntimeIdentity(identity, func(id *VerifiedPackIdentity) { id.ManifestSHA256 = "bad" }), ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"}}, nil)},
		{name: "duplicate identity", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack, basePack}, nil)},
		{name: "duplicate profile ref", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			profiles := packs[0]["profiles"].([]map[string]any)
			packs[0]["profiles"] = append(profiles, profiles[0])
		})},
		{name: "unsupported kind", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["kind"] = "basic"
		})},
		{name: "invalid login url", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["url"] = "http://share.alpha.test/login"
		})},
		{name: "invalid login traversal", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["url"] = "https://share.alpha.test/a/../login"
		})},
		{name: "invalid domain rule", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["allowed_domains"] = []map[string]any{{"host": "share"}}
		})},
		{name: "invalid store binding ref", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["store_binding"].(map[string]any)["profile_refs"] = []string{"apr-missing001"}
		})},
		{name: "invalid preflight enum", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["preflight"].(map[string]any)["missing"] = "ignore"
		})},
		{name: "invalid provisioning enum", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["provisioning"].(map[string]any)["mode"] = "manual"
		})},
		{name: "invalid provisioning ref", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["provisioning"].(map[string]any)["profile_refs"] = []string{"apr-missing001"}
		})},
		{name: "provisioning none with refs", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["provisioning"].(map[string]any)["mode"] = "none"
			packs[0]["preflight"].(map[string]any)["missing"] = "fail"
			packs[0]["preflight"].(map[string]any)["expired"] = "fail"
		})},
		{name: "provisioning missing login", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["url"] = ""
		})},
		{name: "webview missing callback transport", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			delete(packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any), "callback_transport")
		})},
		{name: "webview unsupported callback transport", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["callback_transport"].(map[string]any)["mode"] = "query"
		})},
		{name: "webview callback body too large", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["callback_transport"].(map[string]any)["max_body_bytes"] = int64(privateAuthRuntimeMaxCallbackBodyBytes + 1)
		})},
		{name: "webview callback content types empty", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["callback_transport"].(map[string]any)["content_types"] = []string{}
		})},
		{name: "webview callback content type invalid", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["callback_transport"].(map[string]any)["content_types"] = []string{"text/plain"}
		})},
		{name: "webview empty collector source", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["collector_js"] = "  "
		})},
		{name: "webview capture missing", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			delete(packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any), "capture")
		})},
		{name: "webview unsupported capture format", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["capture"].(map[string]any)["format"] = "text"
		})},
		{name: "webview capture candidates empty", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["capture"].(map[string]any)["secret_candidates"] = []string{}
		})},
		{name: "webview capture candidates duplicate", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["capture"].(map[string]any)["secret_candidates"] = []string{"secret", "secret"}
		})},
		{name: "webview invalid capture candidate", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["capture"].(map[string]any)["secret_candidates"] = []string{"capture[0].secret"}
		})},
		{name: "webview invalid capture optional path", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["profiles"].([]map[string]any)[0]["login"].(map[string]any)["capture"].(map[string]any)["kind_field"] = "bad path"
		})},
		{name: "invalid materialization ref", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["materialization"].(map[string]any)["profile_refs"] = []string{"apr-missing001"}
		})},
		{name: "normalization reject crlf false", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			packs[0]["normalization"].(map[string]any)["reject_crlf"] = false
		})},
		{name: "normalization missing trim space", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			delete(packs[0]["normalization"].(map[string]any), "trim_space")
		})},
		{name: "refresh provisioning incomplete", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			profiles := packs[0]["profiles"].([]map[string]any)
			profiles = append(profiles, privateAuthRuntimeProfileMap("apr-beta001", AuthSecretKindBearer, "https://api.alpha.test/login"))
			packs[0]["profiles"] = profiles
			packs[0]["store_binding"].(map[string]any)["profile_refs"] = []string{"apr-alpha001", "apr-beta001"}
			packs[0]["materialization"].(map[string]any)["profile_refs"] = []string{"apr-alpha001", "apr-beta001"}
			packs[0]["provisioning"].(map[string]any)["profile_refs"] = []string{"apr-alpha001"}
		})},
		{name: "materialization not store bound", raw: privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{basePack}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
			profiles := packs[0]["profiles"].([]map[string]any)
			profiles = append(profiles, privateAuthRuntimeProfileMap("apr-beta001", AuthSecretKindBearer, "https://api.alpha.test/login"))
			packs[0]["profiles"] = profiles
			packs[0]["materialization"].(map[string]any)["profile_refs"] = []string{"apr-beta001"}
			packs[0]["provisioning"].(map[string]any)["profile_refs"] = []string{"apr-alpha001", "apr-beta001"}
		})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle, err := NewPrivateAuthRuntimeBundle(tt.raw, tt.opts)
			if err == nil {
				t.Fatalf("NewPrivateAuthRuntimeBundle() error = nil, bundle=%#v", bundle)
			}
			assertGenericPrivateAuthRuntimeBundleError(t, err)
		})
	}

	if _, err := NewPrivateAuthRuntimeBundle(validRaw, PrivateAuthRuntimeBundleLoadOptions{ExpectedAuthRuntimePrivateSHA256: validHash}); err != nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() expected sha match error = %v", err)
	}
}

func TestPrivateAuthRuntimeBundleKeepsAuthSecretKindClosed(t *testing.T) {
	for _, kind := range []AuthSecretKind{AuthSecretKindBearer, AuthSecretKindCookie} {
		t.Run(string(kind), func(t *testing.T) {
			raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1"), ProfileRef: "apr-alpha001", Kind: kind, LoginURL: "https://share.alpha.test/login"}}, nil)
			if _, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{}); err != nil {
				t.Fatalf("NewPrivateAuthRuntimeBundle() kind %q error = %v", kind, err)
			}
		})
	}

	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1"), ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"}}, func(_ map[string]any, _ map[string]any, packs []map[string]any) {
		packs[0]["profiles"].([]map[string]any)[0]["kind"] = "header"
	})
	_, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{})
	if err == nil {
		t.Fatal("NewPrivateAuthRuntimeBundle() unsupported kind error = nil")
	}
	assertGenericPrivateAuthRuntimeBundleError(t, err)
}

func TestPrivateAuthRuntimeSourceLoading(t *testing.T) {
	withPrivateAuthRuntimeEnv(t, "", "")
	withEmbeddedPrivateAuthRuntimeBundleState(t, nil, "")

	bundle, err := LoadPrivateAuthRuntimeBundleFromRuntimeSources()
	if err != nil {
		t.Fatalf("LoadPrivateAuthRuntimeBundleFromRuntimeSources() no source error = %v", err)
	}
	if bundle != nil {
		t.Fatal("LoadPrivateAuthRuntimeBundleFromRuntimeSources() no source bundle != nil")
	}

	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: identity, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"}}, nil)
	runtimeHash := privateAuthRuntimeHash(t, raw)
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Run("env path loads", func(t *testing.T) {
		withPrivateAuthRuntimeEnv(t, path, runtimeHash)
		withEmbeddedPrivateAuthRuntimeBundleState(t, nil, "")

		bundle, err := LoadPrivateAuthRuntimeBundleFromRuntimeSources()
		if err != nil {
			t.Fatalf("LoadPrivateAuthRuntimeBundleFromRuntimeSources() error = %v", err)
		}
		if _, ok := bundle.PackRuntime(identity); !ok {
			t.Fatal("PackRuntime() from env ok = false")
		}
	})

	t.Run("env expected sha mismatch", func(t *testing.T) {
		withPrivateAuthRuntimeEnv(t, path, strings.Repeat("1", 64))
		withEmbeddedPrivateAuthRuntimeBundleState(t, nil, "")

		_, err := LoadPrivateAuthRuntimeBundleFromRuntimeSources()
		if err == nil {
			t.Fatal("LoadPrivateAuthRuntimeBundleFromRuntimeSources() error = nil, want mismatch")
		}
		assertGenericPrivateAuthRuntimeBundleError(t, err)
		assertNoForbiddenSubstrings(t, err.Error(), path, runtimeHash, string(raw), "share.alpha.test")
	})

	t.Run("embedded loads", func(t *testing.T) {
		withPrivateAuthRuntimeEnv(t, "", "")
		withEmbeddedPrivateAuthRuntimeBundleState(t, raw, runtimeHash)

		bundle, err := LoadPrivateAuthRuntimeBundleFromRuntimeSources()
		if err != nil {
			t.Fatalf("LoadPrivateAuthRuntimeBundleFromRuntimeSources() embedded error = %v", err)
		}
		if _, ok := bundle.PackRuntime(identity); !ok {
			t.Fatal("PackRuntime() from embedded ok = false")
		}
	})

	t.Run("env and embedded ambiguity", func(t *testing.T) {
		withPrivateAuthRuntimeEnv(t, path, runtimeHash)
		withEmbeddedPrivateAuthRuntimeBundleState(t, raw, runtimeHash)

		_, err := LoadPrivateAuthRuntimeBundleFromRuntimeSources()
		if err == nil {
			t.Fatal("LoadPrivateAuthRuntimeBundleFromRuntimeSources() error = nil, want ambiguity denial")
		}
		assertGenericPrivateAuthRuntimeBundleError(t, err)
		assertNoForbiddenSubstrings(t, err.Error(), path, runtimeHash, string(raw), "share.alpha.test")
	})

	t.Run("missing file path is redacted", func(t *testing.T) {
		missingPath := filepath.Join(t.TempDir(), "missing-runtime.json")
		withPrivateAuthRuntimeEnv(t, missingPath, "")
		withEmbeddedPrivateAuthRuntimeBundleState(t, nil, "")

		_, err := LoadPrivateAuthRuntimeBundleFromRuntimeSources()
		if err == nil {
			t.Fatal("LoadPrivateAuthRuntimeBundleFromRuntimeSources() error = nil, want file denial")
		}
		assertGenericPrivateAuthRuntimeBundleError(t, err)
		assertNoForbiddenSubstrings(t, err.Error(), missingPath)
	})
}

func TestPrivateAuthRuntimeBundleErrorsAreGeneric(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: identity, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"}}, nil)
	runtimeHash := privateAuthRuntimeHash(t, raw)
	path := filepath.Join(t.TempDir(), "custody-runtime.json")

	bundle, err := NewPrivateAuthRuntimeBundle(raw, PrivateAuthRuntimeBundleLoadOptions{ExpectedAuthRuntimePrivateSHA256: strings.Repeat("1", 64)})
	if err == nil {
		t.Fatalf("NewPrivateAuthRuntimeBundle() error = nil, bundle=%#v", bundle)
	}
	assertNoForbiddenSubstrings(t, err.Error(), runtimeHash, string(raw), path, "share.alpha.test", "https://share.alpha.test/login", "apr-alpha001", "opaque-secret-fixture")
	assertGenericPrivateAuthRuntimeBundleError(t, err)
}

func TestPrivateAuthRuntimeRuntimeSourceState(t *testing.T) {
	identity := privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	raw := privateAuthRuntimeBundleRaw(t, []privateAuthRuntimePackFixture{{Identity: identity, ProfileRef: "apr-alpha001", Kind: AuthSecretKindBearer, LoginURL: "https://share.alpha.test/login"}}, nil)
	runtimeHash := privateAuthRuntimeHash(t, raw)
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Run("none", func(t *testing.T) {
		withPrivateAuthRuntimeEnv(t, "", "")
		withEmbeddedPrivateAuthRuntimeBundleState(t, nil, "")
		if got := PrivateAuthRuntimeBundleRuntimeSourceState(); got != RuntimeSourceStateNone {
			t.Fatalf("PrivateAuthRuntimeBundleRuntimeSourceState() = %q, want none", got)
		}
	})

	t.Run("env", func(t *testing.T) {
		withPrivateAuthRuntimeEnv(t, path, runtimeHash)
		withEmbeddedPrivateAuthRuntimeBundleState(t, nil, "")
		if got := PrivateAuthRuntimeBundleRuntimeSourceState(); got != RuntimeSourceStateEnv {
			t.Fatalf("PrivateAuthRuntimeBundleRuntimeSourceState() = %q, want env", got)
		}
	})

	t.Run("embedded", func(t *testing.T) {
		withPrivateAuthRuntimeEnv(t, "", "")
		withEmbeddedPrivateAuthRuntimeBundleState(t, raw, runtimeHash)
		if got := PrivateAuthRuntimeBundleRuntimeSourceState(); got != RuntimeSourceStateEmbedded {
			t.Fatalf("PrivateAuthRuntimeBundleRuntimeSourceState() = %q, want embedded", got)
		}
	})

	t.Run("ambiguous", func(t *testing.T) {
		withPrivateAuthRuntimeEnv(t, path, runtimeHash)
		withEmbeddedPrivateAuthRuntimeBundleState(t, raw, runtimeHash)
		if got := PrivateAuthRuntimeBundleRuntimeSourceState(); got != RuntimeSourceStateAmbiguous {
			t.Fatalf("PrivateAuthRuntimeBundleRuntimeSourceState() = %q, want ambiguous", got)
		}
	})
}

type privateAuthRuntimePackFixture struct {
	Identity   VerifiedPackIdentity
	ProfileRef string
	Kind       AuthSecretKind
	LoginURL   string
}

func privateAuthRuntimeBundleRaw(t *testing.T, fixtures []privateAuthRuntimePackFixture, mutate func(map[string]any, map[string]any, []map[string]any)) []byte {
	t.Helper()
	bundleBase := map[string]any{
		"schema_version": 1,
		"bundle_id":      "arb-alpha001",
		"bundle_version": "opaque-1",
	}
	runtimeRaw := privateAuthRuntimeRuntimeRaw(t, fixtures, func(runtime map[string]any, packs []map[string]any) {
		if mutate != nil {
			mutate(bundleBase, runtime, packs)
		}
	})

	return privateAuthRuntimeBundleRawWithRuntime(t, runtimeRaw, func(envelope map[string]any) {
		for key, value := range bundleBase {
			envelope[key] = value
		}
	})
}

func privateAuthRuntimeRuntimeRaw(t *testing.T, fixtures []privateAuthRuntimePackFixture, mutate func(map[string]any, []map[string]any)) []byte {
	t.Helper()
	packs := make([]map[string]any, 0, len(fixtures))
	for _, fixture := range fixtures {
		packs = append(packs, privateAuthRuntimePackMap(fixture))
	}
	runtime := map[string]any{"packs": packs}
	if mutate != nil {
		mutate(runtime, packs)
	}
	raw, err := json.Marshal(runtime)
	if err != nil {
		t.Fatalf("json.Marshal(runtime) error = %v", err)
	}

	return raw
}

func privateAuthRuntimeBundleRawWithRuntime(t *testing.T, runtimeRaw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	bundle := map[string]any{
		"schema_version":              1,
		"bundle_id":                   "arb-alpha001",
		"bundle_version":              "opaque-1",
		"auth_runtime_private_sha256": sha256HexString(runtimeRaw),
		"runtime":                     json.RawMessage(runtimeRaw),
	}
	if mutate != nil {
		mutate(bundle)
	}
	if _, ok := bundle["auth_runtime_private_sha256"]; !ok {
		bundle["auth_runtime_private_sha256"] = sha256HexString(runtimeRaw)
	}
	if _, ok := bundle["runtime"]; !ok {
		bundle["runtime"] = json.RawMessage(runtimeRaw)
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("json.Marshal(bundle) error = %v", err)
	}

	return raw
}

func privateAuthRuntimeBundleRawWithInvalidRuntime(t *testing.T, runtimeRaw []byte) []byte {
	t.Helper()
	raw := []byte(`{"schema_version":1,"bundle_id":"arb-alpha001","bundle_version":"opaque-1","auth_runtime_private_sha256":"` + sha256HexString(runtimeRaw) + `","runtime":` + string(runtimeRaw) + `}`)

	return raw
}

func privateAuthRuntimePackMap(fixture privateAuthRuntimePackFixture) map[string]any {
	identity := fixture.Identity
	if identity.PackID == "" {
		identity = privateAuthRuntimeIdentity("xpk-alpha001", "opaque-1", "1")
	}
	profileRef := fixture.ProfileRef
	if profileRef == "" {
		profileRef = "apr-alpha001"
	}
	kind := fixture.Kind
	if kind == "" {
		kind = AuthSecretKindBearer
	}
	loginURL := fixture.LoginURL
	if loginURL == "" {
		loginURL = "https://share.alpha.test/login"
	}

	return map[string]any{
		"verified_pack_identity": map[string]any{
			"pack_id":           identity.PackID,
			"pack_version":      identity.PackVersion,
			"asset_sha256":      identity.AssetSHA256,
			"manifest_sha256":   identity.ManifestSHA256,
			"payload_sha256":    identity.PayloadSHA256,
			"signature_sha256":  identity.SignatureSHA256,
			"public_key_sha256": identity.PublicKeySHA256,
		},
		"store_binding": map[string]any{
			"scope":        "pack",
			"profile_refs": []string{profileRef},
		},
		"profiles": []map[string]any{privateAuthRuntimeProfileMap(profileRef, kind, loginURL)},
		"preflight": map[string]any{
			"mode":    "required",
			"missing": "refresh",
			"expired": "refresh",
		},
		"provisioning": map[string]any{
			"mode":         "webview",
			"profile_refs": []string{profileRef},
		},
		"materialization": map[string]any{
			"profile_refs": []string{profileRef},
		},
		"normalization": map[string]any{
			"reject_crlf": true,
			"trim_space":  true,
		},
	}
}

func privateAuthRuntimeProfileMap(profileRef string, kind AuthSecretKind, loginURL string) map[string]any {
	loginHost := "share.alpha.test"
	if parsed, err := url.Parse(loginURL); err == nil && parsed.Host != "" {
		loginHost = parsed.Host
	}
	return map[string]any{
		"profile_ref": profileRef,
		"kind":        kind,
		"login": map[string]any{
			"url":             loginURL,
			"allowed_domains": []map[string]any{{"host": loginHost}},
			"timeout_millis":  30000,
			"callback_transport": map[string]any{
				"mode":           "local_post",
				"content_types":  []string{"application/json"},
				"max_body_bytes": int64(16384),
			},
			"collector_js": "(() => { return function(ctx, postCapture) { return ctx && postCapture; }; })();",
			"capture": map[string]any{
				"format":                 "json",
				"secret_candidates":      []string{"secret", "capture.secret"},
				"kind_field":             "kind",
				"expires_at_field":       "expires_at",
				"redacted_display_field": "redacted_display",
			},
		},
	}
}

func privateAuthRuntimeIdentity(packID string, version string, seed string) VerifiedPackIdentity {
	return VerifiedPackIdentity{
		PackID:          packID,
		PackVersion:     version,
		AssetSHA256:     strings.Repeat(seed, 64),
		ManifestSHA256:  strings.Repeat("a", 64),
		PayloadSHA256:   strings.Repeat("b", 64),
		SignatureSHA256: strings.Repeat("c", 64),
		PublicKeySHA256: strings.Repeat("d", 64),
	}
}

func mutatePrivateAuthRuntimeIdentity(identity VerifiedPackIdentity, mutate func(*VerifiedPackIdentity)) VerifiedPackIdentity {
	mutate(&identity)

	return identity
}

func privateAuthRuntimeHash(t *testing.T, raw []byte) string {
	t.Helper()
	var envelope struct {
		Runtime json.RawMessage `json:"runtime"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	return sha256HexString(envelope.Runtime)
}

func assertGenericPrivateAuthRuntimeBundleError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want generic private auth runtime bundle error")
	}
	if err.Error() != privateAuthRuntimeBundleInvalidError {
		t.Fatalf("error = %q, want %q", err.Error(), privateAuthRuntimeBundleInvalidError)
	}
}

func withPrivateAuthRuntimeEnv(t *testing.T, path string, expectedSHA string) {
	t.Helper()
	oldPath, hadPath := os.LookupEnv(privateAuthRuntimeBundlePathEnv)
	oldSHA, hadSHA := os.LookupEnv(privateAuthRuntimeBundleExpectedSHA256Env)
	if path == "" {
		_ = os.Unsetenv(privateAuthRuntimeBundlePathEnv)
	} else {
		_ = os.Setenv(privateAuthRuntimeBundlePathEnv, path)
	}
	if expectedSHA == "" {
		_ = os.Unsetenv(privateAuthRuntimeBundleExpectedSHA256Env)
	} else {
		_ = os.Setenv(privateAuthRuntimeBundleExpectedSHA256Env, expectedSHA)
	}
	t.Cleanup(func() {
		if hadPath {
			_ = os.Setenv(privateAuthRuntimeBundlePathEnv, oldPath)
		} else {
			_ = os.Unsetenv(privateAuthRuntimeBundlePathEnv)
		}
		if hadSHA {
			_ = os.Setenv(privateAuthRuntimeBundleExpectedSHA256Env, oldSHA)
		} else {
			_ = os.Unsetenv(privateAuthRuntimeBundleExpectedSHA256Env)
		}
	})
}

func withEmbeddedPrivateAuthRuntimeBundleState(t *testing.T, raw []byte, expectedSHA string) {
	t.Helper()
	oldRaw := embeddedPrivateAuthRuntimeBundleJSON
	oldSHA := embeddedPrivateAuthRuntimeBundleSHA256
	embeddedPrivateAuthRuntimeBundleJSON = cloneBytes(raw)
	embeddedPrivateAuthRuntimeBundleSHA256 = expectedSHA
	t.Cleanup(func() {
		embeddedPrivateAuthRuntimeBundleJSON = oldRaw
		embeddedPrivateAuthRuntimeBundleSHA256 = oldSHA
	})
}
