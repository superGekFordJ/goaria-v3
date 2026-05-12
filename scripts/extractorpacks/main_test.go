package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaria-v3/internal/extractor"
)

func TestVerifyPackAssetValidFixtureGeneratesEmbed(t *testing.T) {
	fixtureLock := filepath.Join("..", "..", "internal", "extractor", "testdata", "supplychain", "fixture.lock.json")
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "embedded_packs_release_gen.go")
	provenancePath := filepath.Join(outDir, "verified_packs.provenance.json")

	if err := verifyPacks(verifyOptions{
		LockPath:      fixtureLock,
		OutPath:       outPath,
		ProvenanceOut: provenancePath,
		AllowFile:     true,
	}); err != nil {
		t.Fatalf("verifyPacks() error = %v", err)
	}

	generated, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read generated output: %v", err)
	}
	text := string(generated)
	for _, want := range []string{
		"package extractor",
		"EmbeddedPack",
		"AssetSHA256:",
		"pack_id: fixture-pack",
		"pack_version: 0.1.0-fixture",
		"public_key_hex: 5e212c0980e4b39fc09721134aa02109374edfd260c0d3d03cb501c8d65457a9",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated code missing %q\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"private", "NewKeyFromSeed", "seed", "goaria extractor supply-chain fixture private"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("generated code contains forbidden secret marker %q", forbidden)
		}
	}

	provenance, err := os.ReadFile(provenancePath)
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if !strings.Contains(string(provenance), "fixture-pack") || strings.Contains(string(provenance), "manifest.sig") {
		t.Fatalf("unexpected provenance contents: %s", provenance)
	}
	if strings.Contains(string(provenance), "domain_policy_refs") || strings.Contains(string(provenance), "broker_policy_refs") {
		t.Fatalf("provenance must not include alias ref fields in SPEC-081: %s", provenance)
	}
}

func TestVerifyPackAssetAliasFixtureGeneratesNoNameEmbed(t *testing.T) {
	asset := validAliasTestAsset(t)
	entry := asset.lockEntry()
	entry.PackID = "xpk-alpha001"
	lockPath := writeLockWithAsset(t, asset.bytes, entry)
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "embedded.go")
	provenancePath := filepath.Join(outDir, "provenance.json")

	if err := verifyPacks(verifyOptions{LockPath: lockPath, OutPath: outPath, ProvenanceOut: provenancePath, AllowFile: true}); err != nil {
		t.Fatalf("verifyPacks() error = %v", err)
	}
	generatedBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read generated output: %v", err)
	}
	generated := string(generatedBytes)
	for _, want := range []string{"xpk-alpha001", "AssetSHA256:", entry.AssetSHA256} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated alias code missing %q\n%s", want, generated)
		}
	}
	for _, forbidden := range []string{"domain_policy_refs", "broker_policy_refs", "provider", "private"} {
		if strings.Contains(generated, forbidden) {
			t.Fatalf("generated alias code contains forbidden public surface %q", forbidden)
		}
	}
	provenanceBytes, err := os.ReadFile(provenancePath)
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	provenance := string(provenanceBytes)
	if strings.Contains(provenance, "domain_policy_refs") || strings.Contains(provenance, "broker_policy_refs") {
		t.Fatalf("alias provenance must defer ref fields: %s", provenance)
	}
}

func TestVerifyPackAssetMissingFailsRequired(t *testing.T) {
	t.Run("missing lock", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "embedded.go")
		err := verifyPacks(verifyOptions{LockPath: filepath.Join(t.TempDir(), "missing.lock.json"), OutPath: outPath, Required: true})
		if err == nil {
			t.Fatalf("verifyPacks() error = nil, want error")
		}
		assertFileMissing(t, outPath)
	})

	t.Run("missing asset", func(t *testing.T) {
		lockPath := writeLock(t, t.TempDir(), packLockEntry{
			PackID:      "fixture-pack",
			PackVersion: "0.1.0-fixture",
			AssetPath:   "missing.pack.zip",
			AssetSHA256: strings.Repeat("0", 64),
			PublicKeys:  []string{fixturePublicKeyHex(t)},
		})
		outPath := filepath.Join(t.TempDir(), "embedded.go")
		err := verifyPacks(verifyOptions{LockPath: lockPath, OutPath: outPath, Required: true, AllowFile: true})
		if err == nil {
			t.Fatalf("verifyPacks() error = nil, want error")
		}
		assertFileMissing(t, outPath)
	})
}

func TestVerifyPackAssetWrongChecksumFailsBeforeSignature(t *testing.T) {
	asset := validTestAsset(t)
	entry := asset.lockEntry()
	entry.AssetSHA256 = strings.Repeat("0", 64)
	lockPath := writeLockWithAsset(t, asset.bytes, entry)
	outPath := filepath.Join(t.TempDir(), "embedded.go")

	err := verifyPacks(verifyOptions{LockPath: lockPath, OutPath: outPath, Required: true, AllowFile: true})
	if err == nil || !strings.Contains(err.Error(), "asset checksum pin mismatch") {
		t.Fatalf("verifyPacks() error = %v, want checksum mismatch", err)
	}
	assertFileMissing(t, outPath)
}

func TestVerifyPackAssetTamperedPayloadOrSignatureFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(packParts) packParts
	}{
		{name: "payload", mutate: func(parts packParts) packParts {
			parts.Payload = append([]byte(nil), parts.Payload...)
			parts.Payload[0] ^= 0xff
			return parts
		}},
		{name: "signature", mutate: func(parts packParts) packParts {
			parts.Signature = append([]byte(nil), parts.Signature...)
			parts.Signature[0] ^= 0xff
			return parts
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asset := validTestAsset(t)
			parts := tc.mutate(asset.parts)
			asset.bytes = zipBytesForParts(t, parts, nil)
			entry := asset.lockEntry()
			entry.AssetSHA256 = sha256Hex(asset.bytes)
			entry.ManifestSHA256 = ""
			entry.PayloadSHA256 = ""
			entry.SignatureSHA256 = ""
			lockPath := writeLockWithAsset(t, asset.bytes, entry)
			outPath := filepath.Join(t.TempDir(), "embedded.go")

			err := verifyPacks(verifyOptions{LockPath: lockPath, OutPath: outPath, Required: true, AllowFile: true})
			if err == nil {
				t.Fatalf("verifyPacks() error = nil, want tamper failure")
			}
			assertFileMissing(t, outPath)
		})
	}
}

func TestVerifyPackAssetRejectsUnsafeProductionURLs(t *testing.T) {
	for _, assetURL := range []string{
		"http://example.com/pack.zip",
		"https://user:pass@example.com/pack.zip",
		"https://example.com/pack.zip?token=abc",
		"https://example.com/pack.zip?access_token=abc",
		"https://example.com/pack.zip?auth=abc",
		"https://example.com/pack.zip?key=abc",
		"https://example.com/pack.zip?secret=abc",
		"https://example.com/pack.zip?api_key=abc",
		"https://example.com/pack.zip?signature=abc",
		"https://example.com/pack.zip?sig=abc",
		"https://example.com/pack.zip?X-Amz-Signature=abc",
		"https://example.com/pack.zip?X-Amz-Credential=abc",
		"https://example.com/pack.zip?credential=abc",
		"https://example.com/pack.zip?policy=abc",
		"https://example.com/pack.zip?download=1",
		"https://example.com/pack.zip#sha256=abc",
		"https://example.com/pack.zip#fragment",
	} {
		t.Run(assetURL, func(t *testing.T) {
			entry := validTestAsset(t).lockEntry()
			entry.AssetPath = ""
			entry.AssetURL = assetURL
			_, err := readAsset(verifyOptions{}, t.TempDir(), entry)
			if err == nil {
				t.Fatalf("readAsset() error = nil, want unsafe URL rejection")
			}
		})
	}
}

func TestVerifyPackAssetRejectsUnsafeZipShape(t *testing.T) {
	base := validTestAsset(t)
	tests := []struct {
		name  string
		parts packParts
		extra map[string][]byte
	}{
		{name: "extra", parts: base.parts, extra: map[string][]byte{"extra.txt": []byte("x")}},
		{name: "missing manifest", parts: packParts{Payload: base.parts.Payload, Signature: base.parts.Signature}},
		{name: "absolute path", parts: base.parts, extra: map[string][]byte{"/manifest.json": []byte("x")}},
		{name: "dot dot", parts: base.parts, extra: map[string][]byte{"../manifest.json": []byte("x")}},
		{name: "oversized manifest", parts: packParts{ManifestJSON: bytes.Repeat([]byte("x"), maxManifestBytes+1), Payload: base.parts.Payload, Signature: base.parts.Signature}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := extractStrictPackZip(zipBytesForParts(t, tc.parts, tc.extra))
			if err == nil {
				t.Fatalf("extractStrictPackZip() error = nil, want error")
			}
		})
	}

	t.Run("duplicate", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, name := range []string{"manifest.json", "payload.wasm", "manifest.sig", "manifest.json"} {
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte("x"))
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := extractStrictPackZip(buf.Bytes()); err == nil {
			t.Fatalf("extractStrictPackZip() error = nil, want duplicate error")
		}
	})
}

func TestVerifyPackAssetOptionalNoPackRemovesStaleGeneratedFile(t *testing.T) {
	dir := t.TempDir()
	lockPath := writeLock(t, dir)
	outPath := filepath.Join(dir, "embedded.go")
	provenancePath := filepath.Join(dir, "provenance.json")
	if err := os.WriteFile(outPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provenancePath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := verifyPacks(verifyOptions{LockPath: lockPath, OutPath: outPath, ProvenanceOut: provenancePath}); err != nil {
		t.Fatalf("verifyPacks() error = %v", err)
	}
	assertFileMissing(t, outPath)
	assertFileMissing(t, provenancePath)
}

func TestVerifyPackAssetHTTPSFixture(t *testing.T) {
	asset := validTestAsset(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(asset.bytes)
	}))
	defer server.Close()

	entry := asset.lockEntry()
	entry.AssetPath = ""
	entry.AssetURL = server.URL + "/fixture.pack.zip"
	lockPath := writeLock(t, t.TempDir(), entry)
	outPath := filepath.Join(t.TempDir(), "embedded.go")

	if err := verifyPacks(verifyOptions{LockPath: lockPath, OutPath: outPath, Required: true, HTTPClient: server.Client()}); err != nil {
		t.Fatalf("verifyPacks() error = %v", err)
	}
}

func TestFetchHTTPSAssetRedirectErrorDoesNotLeakCredentialURL(t *testing.T) {
	secretValues := []string{
		"raw-user",
		"raw-password",
		"amz-signature-secret",
		"amz-credential-secret",
		"short-sig-secret",
		"policy-secret",
		"fragment-secret",
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "https://raw-user:raw-password@example.com/private.pack.zip?X-Amz-Signature=amz-signature-secret&X-Amz-Credential=amz-credential-secret&sig=short-sig-secret&policy=policy-secret#fragment-secret"
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer server.Close()

	_, err := fetchHTTPSAsset(server.Client(), server.URL+"/fixture.pack.zip", false)
	if err == nil {
		t.Fatalf("fetchHTTPSAsset() error = nil, want redirect rejection")
	}
	errorText := err.Error()
	if !strings.Contains(errorText, "asset redirect target is not an allowed public HTTPS URL") {
		t.Fatalf("fetchHTTPSAsset() error = %q, want sanitized redirect rejection", errorText)
	}
	for _, forbidden := range secretValues {
		if strings.Contains(errorText, forbidden) {
			t.Fatalf("fetchHTTPSAsset() error leaks %q: %q", forbidden, errorText)
		}
	}
	for _, forbidden := range []string{"X-Amz-Signature", "X-Amz-Credential", "sig=", "policy=", "raw-user:raw-password", "#fragment"} {
		if strings.Contains(errorText, forbidden) {
			t.Fatalf("fetchHTTPSAsset() error leaks raw redirect URL component %q: %q", forbidden, errorText)
		}
	}
}

func TestFullPackMetadataRenderLockValidTwoPackOpaque(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	metadata := validFullPackMetadata(assetOne, assetTwo)

	lock, err := renderFullPackLock(metadata)
	if err != nil {
		t.Fatalf("renderFullPackLock() error = %v", err)
	}
	if lock.SchemaVersion != lockSchemaVersion || len(lock.Packs) != 2 {
		t.Fatalf("unexpected rendered lock shape: %+v", lock)
	}
	for i, entry := range lock.Packs {
		if entry.AssetPath != "" {
			t.Fatalf("rendered lock pack %d has fixture asset_path: %+v", i, entry)
		}
		if !strings.HasPrefix(entry.AssetURL, metadata.BaseAssetURL+"/") {
			t.Fatalf("rendered lock pack %d asset_url = %q", i, entry.AssetURL)
		}
		if !strings.Contains(entry.AssetURL, metadata.Packs[i].AssetName) {
			t.Fatalf("rendered lock pack %d missing asset name in URL: %q", i, entry.AssetURL)
		}
	}

	lockBytes, err := marshalRenderedFullPackLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	lockText := string(lockBytes)
	for _, want := range []string{"schema_version", "asset_url", "xpk-alpha001", "xpk-alpha002", "opaque-1", "opaque-2"} {
		if !strings.Contains(lockText, want) {
			t.Fatalf("rendered lock missing %q: %s", want, lockText)
		}
	}
	for _, forbidden := range []string{"asset_name", "release_tag", "asset_url_template", "asset_path", "policy_private_sha256", "domain_policy_refs", "broker_policy_refs"} {
		if strings.Contains(lockText, forbidden) {
			t.Fatalf("rendered lock contains forbidden field %q: %s", forbidden, lockText)
		}
	}
	if err := auditNoNameBytes("rendered lock", lockBytes); err != nil {
		t.Fatalf("auditNoNameBytes() error = %v", err)
	}
}

func TestFullPackMetadataTemplateRender(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	metadata := validFullPackMetadata(assetOne, assetTwo)
	metadata.BaseAssetURL = ""
	metadata.AssetURLTemplate = "https://release.example.test/assets/{release_tag}/{asset_name}"

	lock, err := renderFullPackLock(metadata)
	if err != nil {
		t.Fatalf("renderFullPackLock() error = %v", err)
	}
	if got, want := lock.Packs[0].AssetURL, "https://release.example.test/assets/v0.0.0-alpha/asset-alpha001.pack.zip"; got != want {
		t.Fatalf("rendered template URL = %q, want %q", got, want)
	}
}

func TestFullPackURLSafetyRejectsTraversalAndEscapedSegments(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	tests := []struct {
		name   string
		mutate func(*fullPackMetadataFile)
	}{
		{name: "base dot segment", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = "https://release.example.test/assets/../v0.0.0-alpha"
		}},
		{name: "base encoded dot", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = "https://release.example.test/assets/%2e%2e/v0.0.0-alpha"
		}},
		{name: "base encoded slash", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = "https://release.example.test/assets%2fv0.0.0-alpha"
		}},
		{name: "template dot segment", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = ""
			metadata.AssetURLTemplate = "https://release.example.test/assets/../{release_tag}/{asset_name}"
		}},
		{name: "template encoded dot", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = ""
			metadata.AssetURLTemplate = "https://release.example.test/assets/%2e%2e/{release_tag}/{asset_name}"
		}},
		{name: "template encoded slash", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = ""
			metadata.AssetURLTemplate = "https://release.example.test/assets%2f{release_tag}/{asset_name}"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metadata := validFullPackMetadata(assetOne, assetTwo)
			tc.mutate(&metadata)
			if _, err := renderFullPackLock(metadata); err == nil {
				t.Fatalf("renderFullPackLock() error = nil, want unsafe URL rejection")
			}
		})
	}
}

func TestFullPackBaseURLAppendDoesNotNormalizePath(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	metadata := validFullPackMetadata(assetOne, assetTwo)
	metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha/"

	lock, err := renderFullPackLock(metadata)
	if err != nil {
		t.Fatalf("renderFullPackLock() error = %v", err)
	}
	if got, want := lock.Packs[0].AssetURL, "https://release.example.test/assets/v0.0.0-alpha/asset-alpha001.pack.zip"; got != want {
		t.Fatalf("rendered base URL = %q, want %q", got, want)
	}
}

func TestFullPackVerifyUsesRenderedLockAndAuditsSurfaces(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	server := twoPackTLSServer(t, map[string][]byte{
		"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
		"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
	})
	client := rewriteHostClient(t, server, "release.example.test")
	metadata := validFullPackMetadata(assetOne, assetTwo)
	metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha"
	metadataPath := writeFullPackMetadata(t, metadata)
	outDir := t.TempDir()
	tempLockPath := filepath.Join(outDir, "full_pack.lock.json")
	outPath := filepath.Join(outDir, "embedded.go")
	provenancePath := filepath.Join(outDir, "provenance.json")

	if err := verifyFullPack(fullPackVerifyOptions{
		MetadataPath:  metadataPath,
		TempLockPath:  tempLockPath,
		OutPath:       outPath,
		ProvenanceOut: provenancePath,
		HTTPClient:    client,
	}); err != nil {
		t.Fatalf("verifyFullPack() error = %v", err)
	}

	lockBytes, err := os.ReadFile(tempLockPath)
	if err != nil {
		t.Fatalf("read temp lock: %v", err)
	}
	if !strings.Contains(string(lockBytes), metadata.BaseAssetURL) || strings.Contains(string(lockBytes), "asset_name") {
		t.Fatalf("unexpected temp lock contents: %s", lockBytes)
	}
	generated := readTextFile(t, outPath)
	for _, want := range []string{"EmbeddedPack", "AssetSHA256:", "public_key_hex:", "public_key_sha256:", "xpk-alpha001", "xpk-alpha002", assetOne.lockEntry().AssetSHA256, assetTwo.lockEntry().AssetSHA256} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated full-pack embed missing %q", want)
		}
	}
	provenance := readTextFile(t, provenancePath)
	for _, want := range []string{"xpk-alpha001", "xpk-alpha002", "public_key_fingerprints", metadata.BaseAssetURL} {
		if !strings.Contains(provenance, want) {
			t.Fatalf("full-pack provenance missing %q", want)
		}
	}
	for _, surface := range []string{generated, provenance} {
		for _, forbidden := range []string{"policy_private_sha256", "private_policy", "domain_policy_refs", "broker_policy_refs", "provider", "secret", "token", "cookie", "authorization"} {
			if strings.Contains(surface, forbidden) {
				t.Fatalf("full-pack surface contains forbidden term %q: %s", forbidden, surface)
			}
		}
	}
}

func TestFullPackMetadataRejectsLocalhostAndIPLiteral(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	for _, baseURL := range []string{
		"https://localhost/assets/v0.0.0-alpha",
		"https://127.0.0.1/assets/v0.0.0-alpha",
		"https://[::1]/assets/v0.0.0-alpha",
	} {
		t.Run(baseURL, func(t *testing.T) {
			metadata := validFullPackMetadata(assetOne, assetTwo)
			metadata.BaseAssetURL = baseURL
			if _, err := renderFullPackLock(metadata); err == nil {
				t.Fatalf("renderFullPackLock() error = nil, want host rejection")
			}
		})
	}
}

func TestFullPackVerifyRejectsCrossOriginRedirect(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(assetOne.bytes)
	}))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/assets/v0.0.0-alpha/asset-alpha001.pack.zip":
			http.Redirect(w, r, "https://asset-alt.example.test/asset-alpha001.pack.zip", http.StatusFound)
		case "/assets/v0.0.0-alpha/asset-alpha002.pack.zip":
			_, _ = w.Write(assetTwo.bytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := rewriteMultiHostClient(t, map[string]*httptest.Server{
		"release.example.test":   server,
		"asset-alt.example.test": target,
	})
	metadata := validFullPackMetadata(assetOne, assetTwo)
	metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha"
	outDir := t.TempDir()

	err := verifyFullPack(fullPackVerifyOptions{
		MetadataPath:  writeFullPackMetadata(t, metadata),
		TempLockPath:  filepath.Join(outDir, "full_pack.lock.json"),
		OutPath:       filepath.Join(outDir, "embedded.go"),
		ProvenanceOut: filepath.Join(outDir, "provenance.json"),
		HTTPClient:    client,
	})
	if err == nil || err.Error() != "full-pack verification failed" {
		t.Fatalf("verifyFullPack() error = %v, want sanitized full-pack redirect failure", err)
	}
	if strings.Contains(err.Error(), "asset-alt.example.test") || strings.Contains(err.Error(), "release.example.test") {
		t.Fatalf("verifyFullPack() error leaks redirect URL: %v", err)
	}
}

func TestFullPackVerifyCleanupRemovesTempAndGeneratedOutputs(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	server := twoPackTLSServer(t, map[string][]byte{
		"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
		"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
	})
	client := rewriteHostClient(t, server, "release.example.test")
	metadata := validFullPackMetadata(assetOne, assetTwo)
	metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha"
	outDir := t.TempDir()
	tempLockPath := filepath.Join(outDir, "full_pack.lock.json")
	outPath := filepath.Join(outDir, "embedded.go")
	provenancePath := filepath.Join(outDir, "provenance.json")

	if err := verifyFullPack(fullPackVerifyOptions{
		MetadataPath:  writeFullPackMetadata(t, metadata),
		TempLockPath:  tempLockPath,
		OutPath:       outPath,
		ProvenanceOut: provenancePath,
		CleanOutputs:  true,
		HTTPClient:    client,
	}); err != nil {
		t.Fatalf("verifyFullPack() error = %v", err)
	}
	assertFileMissing(t, tempLockPath)
	assertFileMissing(t, outPath)
	assertFileMissing(t, provenancePath)
}

func TestFullPackVerifyFailsClosedForAssetProblems(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	tamperedPayloadAsset := assetTwo
	tamperedPayloadParts := tamperedPayloadAsset.parts
	tamperedPayloadParts.Payload = append([]byte(nil), tamperedPayloadParts.Payload...)
	tamperedPayloadParts.Payload[0] ^= 0xff
	tamperedPayloadAsset.bytes = zipBytesForParts(t, tamperedPayloadParts, nil)
	tamperedSignatureAsset := assetTwo
	tamperedSignatureParts := tamperedSignatureAsset.parts
	tamperedSignatureParts.Signature = append([]byte(nil), tamperedSignatureParts.Signature...)
	tamperedSignatureParts.Signature[0] ^= 0xff
	tamperedSignatureAsset.bytes = zipBytesForParts(t, tamperedSignatureParts, nil)
	tests := []struct {
		name       string
		assetOne   testAsset
		assetTwo   testAsset
		routes     map[string][]byte
		mutateMeta func(*fullPackMetadataFile)
	}{
		{
			name:     "missing asset",
			assetOne: assetOne,
			assetTwo: assetTwo,
			routes: map[string][]byte{
				"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
			},
		},
		{
			name:     "wrong asset hash",
			assetOne: assetOne,
			assetTwo: assetTwo,
			routes: map[string][]byte{
				"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
				"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
			},
			mutateMeta: func(metadata *fullPackMetadataFile) {
				metadata.Packs[1].AssetSHA256 = strings.Repeat("0", 64)
			},
		},
		{
			name:     "wrong optional payload hash",
			assetOne: assetOne,
			assetTwo: assetTwo,
			routes: map[string][]byte{
				"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
				"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
			},
			mutateMeta: func(metadata *fullPackMetadataFile) {
				metadata.Packs[0].PayloadSHA256 = strings.Repeat("0", 64)
			},
		},
		{
			name:     "wrong optional manifest hash",
			assetOne: assetOne,
			assetTwo: assetTwo,
			routes: map[string][]byte{
				"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
				"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
			},
			mutateMeta: func(metadata *fullPackMetadataFile) {
				metadata.Packs[0].ManifestSHA256 = strings.Repeat("0", 64)
			},
		},
		{
			name:     "wrong optional signature hash",
			assetOne: assetOne,
			assetTwo: assetTwo,
			routes: map[string][]byte{
				"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
				"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
			},
			mutateMeta: func(metadata *fullPackMetadataFile) {
				metadata.Packs[0].SignatureSHA256 = strings.Repeat("0", 64)
			},
		},
		{
			name:     "tampered payload",
			assetOne: assetOne,
			assetTwo: tamperedPayloadAsset,
			routes: map[string][]byte{
				"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
				"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": tamperedPayloadAsset.bytes,
			},
			mutateMeta: func(metadata *fullPackMetadataFile) {
				metadata.Packs[1].AssetSHA256 = sha256Hex(tamperedPayloadAsset.bytes)
				metadata.Packs[1].PayloadSHA256 = ""
				metadata.Packs[1].SignatureSHA256 = ""
			},
		},
		{
			name:     "tampered signature",
			assetOne: assetOne,
			assetTwo: tamperedSignatureAsset,
			routes: map[string][]byte{
				"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
				"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": tamperedSignatureAsset.bytes,
			},
			mutateMeta: func(metadata *fullPackMetadataFile) {
				metadata.Packs[1].AssetSHA256 = sha256Hex(tamperedSignatureAsset.bytes)
				metadata.Packs[1].SignatureSHA256 = ""
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			routes := make(map[string][]byte, len(tc.routes)+1)
			for k, v := range tc.routes {
				routes[k] = v
			}
			if _, ok := routes["/assets/v0.0.0-alpha/asset-alpha002.pack.zip"]; !ok && tc.name != "missing asset" {
				routes["/assets/v0.0.0-alpha/asset-alpha002.pack.zip"] = tc.assetTwo.bytes
			}
			server := twoPackTLSServer(t, routes)
			client := rewriteHostClient(t, server, "release.example.test")
			metadata := validFullPackMetadata(tc.assetOne, tc.assetTwo)
			metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha"
			if tc.mutateMeta != nil {
				tc.mutateMeta(&metadata)
			}
			outDir := t.TempDir()
			err := verifyFullPack(fullPackVerifyOptions{
				MetadataPath:  writeFullPackMetadata(t, metadata),
				TempLockPath:  filepath.Join(outDir, "full_pack.lock.json"),
				OutPath:       filepath.Join(outDir, "embedded.go"),
				ProvenanceOut: filepath.Join(outDir, "provenance.json"),
				HTTPClient:    client,
			})
			if err == nil {
				t.Fatalf("verifyFullPack() error = nil, want fail-closed")
			}
			assertFileMissing(t, filepath.Join(outDir, "embedded.go"))
			assertFileMissing(t, filepath.Join(outDir, "provenance.json"))
		})
	}
}

func TestFullPackVerifyGeneratedSurfaceAuditFailureCleansOutputs(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	server := twoPackTLSServer(t, map[string][]byte{
		"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
		"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
	})
	client := rewriteHostClient(t, server, "release.example.test")
	metadata := validFullPackMetadata(assetOne, assetTwo)
	metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha"
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "embedded.go")
	provenancePath := filepath.Join(outDir, "provenance.json")
	err := verifyFullPack(fullPackVerifyOptions{
		MetadataPath:  writeFullPackMetadata(t, metadata),
		TempLockPath:  filepath.Join(outDir, "full_pack.lock.json"),
		OutPath:       outPath,
		ProvenanceOut: provenancePath,
		HTTPClient:    client,
		NoNameAudit: func(surface string, data []byte) error {
			if surface == "generated embed" {
				return auditNoNameBytes(surface, append(data, []byte("private_policy")...))
			}

			return auditNoNameBytes(surface, data)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "generated embed no-name audit failed") {
		t.Fatalf("verifyFullPack() error = %v, want generated embed audit failure", err)
	}
	if strings.Contains(err.Error(), "private_policy") {
		t.Fatalf("audit error leaks matched forbidden value: %v", err)
	}
	assertFileMissing(t, outPath)
	assertFileMissing(t, provenancePath)
}

func TestRenderLockCommandRejectsProductionLockPath(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	metadataPath := writeFullPackMetadata(t, validFullPackMetadata(assetOne, assetTwo))

	err := runCLI([]string{"render-lock", "--metadata", metadataPath, "--out-lock", filepath.Join("build", "extractor", "packs.lock.json")})
	if err == nil || !strings.Contains(err.Error(), "tracked production lock") {
		t.Fatalf("render-lock error = %v, want production lock guard", err)
	}
}

func TestRejectProductionLockOutputHardening(t *testing.T) {
	protected := filepath.Join("build", "extractor", "packs.lock.json")
	absProtected, err := filepath.Abs(protected)
	if err != nil {
		t.Fatal(err)
	}
	caseVariant := strings.ToUpper(filepath.ToSlash(protected))
	winVariant := strings.ReplaceAll(caseVariant, "/", "\\")
	for _, candidate := range []string{
		protected,
		filepath.Join(".", "build", "extractor", "..", "extractor", "packs.lock.json"),
		absProtected,
		caseVariant,
		winVariant,
		protected + ".",
		protected + " ",
		filepath.Join("build.", "extractor ", "packs.lock.json."),
	} {
		t.Run(candidate, func(t *testing.T) {
			if err := rejectProductionLockOutput(candidate); err == nil {
				t.Fatalf("rejectProductionLockOutput(%q) error = nil, want rejection", candidate)
			}
		})
	}
}

func TestRejectProductionLockOutputRejectsExistingSymlinkTarget(t *testing.T) {
	if _, err := os.Lstat(filepath.Join("build", "extractor")); err != nil {
		t.Skip("protected public build/extractor directory is unavailable")
	}
	linkPath := filepath.Join(t.TempDir(), "packs.lock.json")
	protected, err := filepath.Abs(filepath.Join("build", "extractor", "packs.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(protected, linkPath); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := rejectProductionLockOutput(linkPath); err == nil {
		t.Fatalf("rejectProductionLockOutput() error = nil, want symlink target rejection")
	}
}

func TestVerifyFullPackRejectsProtectedOutputPaths(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	metadataPath := writeFullPackMetadata(t, validFullPackMetadata(assetOne, assetTwo))
	protected := filepath.Join("build", "extractor", "packs.lock.json")
	caseVariant := strings.ToUpper(filepath.ToSlash(protected))
	tests := []struct {
		name          string
		outPath       string
		provenanceOut string
	}{
		{name: "out protected", outPath: protected, provenanceOut: filepath.Join(t.TempDir(), "provenance.json")},
		{name: "out case variant", outPath: caseVariant, provenanceOut: filepath.Join(t.TempDir(), "provenance.json")},
		{name: "provenance protected", outPath: filepath.Join(t.TempDir(), "embedded.go"), provenanceOut: protected},
		{name: "provenance relative variant", outPath: filepath.Join(t.TempDir(), "embedded.go"), provenanceOut: filepath.Join(".", "build", "extractor", "..", "extractor", "packs.lock.json")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempLockPath := filepath.Join(t.TempDir(), "full_pack.lock.json")
			if err := os.WriteFile(tempLockPath, []byte("sentinel"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := verifyFullPack(fullPackVerifyOptions{
				MetadataPath:  metadataPath,
				TempLockPath:  tempLockPath,
				OutPath:       tc.outPath,
				ProvenanceOut: tc.provenanceOut,
			})
			if err == nil || !strings.Contains(err.Error(), "tracked production lock") {
				t.Fatalf("verifyFullPack() error = %v, want protected path rejection", err)
			}
			if got := readTextFile(t, tempLockPath); got != "sentinel" {
				t.Fatalf("temp lock was modified/removed: %q", got)
			}
		})
	}
}

func TestVerifyFullPackRejectsTrailingDotSpaceProtectedPathAliases(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	metadataPath := writeFullPackMetadata(t, validFullPackMetadata(assetOne, assetTwo))
	protected := filepath.Join("build", "extractor", "packs.lock.json")
	tests := []struct {
		name          string
		tempLockPath  string
		outPath       string
		provenanceOut string
		sentinelPath  string
	}{
		{name: "temp trailing dot", tempLockPath: protected + ".", outPath: filepath.Join(t.TempDir(), "embedded.go"), provenanceOut: filepath.Join(t.TempDir(), "provenance.json")},
		{name: "temp trailing space", tempLockPath: protected + " ", outPath: filepath.Join(t.TempDir(), "embedded.go"), provenanceOut: filepath.Join(t.TempDir(), "provenance.json")},
		{name: "out trailing dot", tempLockPath: filepath.Join(t.TempDir(), "full_pack.lock.json"), outPath: protected + ".", provenanceOut: filepath.Join(t.TempDir(), "provenance.json"), sentinelPath: "temp"},
		{name: "out trailing space", tempLockPath: filepath.Join(t.TempDir(), "full_pack.lock.json"), outPath: protected + " ", provenanceOut: filepath.Join(t.TempDir(), "provenance.json"), sentinelPath: "temp"},
		{name: "provenance trailing dot", tempLockPath: filepath.Join(t.TempDir(), "full_pack.lock.json"), outPath: filepath.Join(t.TempDir(), "embedded.go"), provenanceOut: protected + ".", sentinelPath: "temp"},
		{name: "provenance trailing space", tempLockPath: filepath.Join(t.TempDir(), "full_pack.lock.json"), outPath: filepath.Join(t.TempDir(), "embedded.go"), provenanceOut: protected + " ", sentinelPath: "temp"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sentinelPath == "temp" {
				if err := os.WriteFile(tc.tempLockPath, []byte("sentinel"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyFullPack(fullPackVerifyOptions{
				MetadataPath:  metadataPath,
				TempLockPath:  tc.tempLockPath,
				OutPath:       tc.outPath,
				ProvenanceOut: tc.provenanceOut,
			})
			if err == nil || !strings.Contains(err.Error(), "tracked production lock") {
				t.Fatalf("verifyFullPack() error = %v, want trailing alias rejection", err)
			}
			if tc.sentinelPath == "temp" {
				if got := readTextFile(t, tc.tempLockPath); got != "sentinel" {
					t.Fatalf("temp lock was modified/removed: %q", got)
				}
			}
		})
	}
}

func TestFullPackMetadataValidationRejectsMalformedInputs(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	valid := validFullPackMetadata(assetOne, assetTwo)
	tests := []struct {
		name   string
		raw    []byte
		mutate func(*fullPackMetadataFile)
	}{
		{name: "zero packs", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs = nil }},
		{name: "one pack", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs = metadata.Packs[:1] }},
		{name: "three packs", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs = append(metadata.Packs, metadata.Packs[0]) }},
		{name: "duplicate pack id", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[1].PackID = metadata.Packs[0].PackID }},
		{name: "duplicate asset name", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[1].AssetName = metadata.Packs[0].AssetName }},
		{name: "malformed json", raw: []byte(`{"schema_version":`)},
		{name: "unknown field", raw: bytes.Replace(fullPackMetadataJSON(t, valid), []byte("\n}"), []byte(",\n  \"extra\": true\n}"), 1)},
		{name: "trailing json", raw: append(fullPackMetadataJSON(t, valid), []byte(` {}`)...)},
		{name: "malformed asset hash", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[0].AssetSHA256 = "ABC" }},
		{name: "malformed optional hash", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[0].ManifestSHA256 = "abc" }},
		{name: "empty public keys", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[0].PublicKeys = nil }},
		{name: "invalid public key", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[0].PublicKeys = []string{"abc"} }},
		{name: "missing url mode", mutate: func(metadata *fullPackMetadataFile) { metadata.BaseAssetURL = "" }},
		{name: "both url modes", mutate: func(metadata *fullPackMetadataFile) {
			metadata.AssetURLTemplate = "https://release.example.test/assets/{release_tag}/{asset_name}"
		}},
		{name: "non https base", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = "http://release.example.test/assets/v0.0.0-alpha"
		}},
		{name: "credential base", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = "https://user:pass@release.example.test/assets/v0.0.0-alpha"
		}},
		{name: "query base", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha?x=1"
		}},
		{name: "fragment base", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha#x"
		}},
		{name: "unsafe asset name uppercase", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[0].AssetName = "asset-Alpha001.pack.zip" }},
		{name: "unsafe asset name traversal", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[0].AssetName = "asset-alpha001../pack.zip" }},
		{name: "unsafe pack id", mutate: func(metadata *fullPackMetadataFile) {
			metadata.Packs[0].PackID = "https://release.example.test/xpk-alpha001"
		}},
		{name: "unsafe pack version", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[0].PackVersion = "opaque 1" }},
		{name: "unsafe release tag", mutate: func(metadata *fullPackMetadataFile) { metadata.ReleaseTag = "v0.0.0/alpha" }},
		{name: "forbidden field marker", raw: []byte(`{"schema_version":1,"release_tag":"v0.0.0-alpha","base_asset_url":"https://release.example.test/assets/v0.0.0-alpha","policy_private_sha256":"0000","packs":[]}`)},
		{name: "escaped forbidden field marker", raw: []byte(`{"schema_version":1,"release_tag":"v0.0.0-alpha","base_asset_url":"https://release.example.test/assets/v0.0.0-alpha","policy_\u0070rivate_sha256":"0000","packs":[]}`)},
		{name: "escaped forbidden string marker", raw: []byte(`{"schema_version":1,"release_tag":"v0.0.0-alpha","base_asset_url":"https://release.example.test/assets/v0.0.0-alpha","packs":[{"asset_name":"asset-alpha001.pack.zip","pack_id":"xpk-alpha001","pack_version":"opaque-1","asset_sha256":"` + strings.Repeat("0", 64) + `","public_keys":["` + strings.Repeat("a", 64) + `"],"note":"priv\u0061te_policy"},{"asset_name":"asset-alpha002.pack.zip","pack_id":"xpk-alpha002","pack_version":"opaque-2","asset_sha256":"` + strings.Repeat("0", 64) + `","public_keys":["` + strings.Repeat("b", 64) + `"]}]}`)},
		{name: "forbidden string marker", mutate: func(metadata *fullPackMetadataFile) { metadata.Packs[0].PackID = "xpk-provider001" }},
		{name: "bad template placeholder", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = ""
			metadata.AssetURLTemplate = "https://release.example.test/assets/{asset_name}"
		}},
		{name: "template query", mutate: func(metadata *fullPackMetadataFile) {
			metadata.BaseAssetURL = ""
			metadata.AssetURLTemplate = "https://release.example.test/assets/{release_tag}/{asset_name}?x=1"
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			if raw == nil {
				metadata := valid
				metadata.Packs = append([]fullPackMetadataEntry(nil), valid.Packs...)
				tc.mutate(&metadata)
				raw = fullPackMetadataJSON(t, metadata)
			}
			_, err := decodeFullPackMetadata(raw)
			if err == nil {
				t.Fatalf("decodeFullPackMetadata() error = nil, want rejection")
			}
			for _, forbidden := range []string{"policy_private_sha256", "secret-value", "protected-root-marker"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error leaks forbidden value %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestNoNameAuditRejectsForbiddenSurfaceWithoutMatchedValue(t *testing.T) {
	err := auditNoNameBytes("metadata", []byte(`{"private_policy":"secret-value"}`))
	if err == nil || err.Error() != "metadata no-name audit failed" {
		t.Fatalf("auditNoNameBytes() error = %v, want category-only audit failure", err)
	}
	if strings.Contains(err.Error(), "private_policy") || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("audit error leaks matched value: %v", err)
	}

	allowed := []byte(`{"schema_version":1,"release_tag":"v0.0.0-alpha","asset_url_template":"https://release.example.test/assets/{release_tag}/{asset_name}","packs":[{"asset_name":"asset-alpha001.pack.zip","pack_id":"xpk-alpha001","pack_version":"opaque-1","asset_sha256":"` + strings.Repeat("0", 64) + `","public_keys":["` + strings.Repeat("a", 64) + `"]}]}`)
	if err := auditNoNameBytes("metadata", allowed); err != nil {
		t.Fatalf("auditNoNameBytes() allowed synthetic surface error = %v", err)
	}
}

func TestFullPackVerifySanitizesManifestIdentityMismatch(t *testing.T) {
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	server := twoPackTLSServer(t, map[string][]byte{
		"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
		"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
	})
	client := rewriteHostClient(t, server, "release.example.test")
	metadata := validFullPackMetadata(assetOne, assetTwo)
	metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha"
	metadata.Packs[0].PackID = "xpk-alpha003"
	outDir := t.TempDir()

	err := verifyFullPack(fullPackVerifyOptions{
		MetadataPath:  writeFullPackMetadata(t, metadata),
		TempLockPath:  filepath.Join(outDir, "full_pack.lock.json"),
		OutPath:       filepath.Join(outDir, "embedded.go"),
		ProvenanceOut: filepath.Join(outDir, "provenance.json"),
		HTTPClient:    client,
	})
	if err == nil || err.Error() != "full-pack verification failed" {
		t.Fatalf("verifyFullPack() error = %v, want sanitized identity mismatch", err)
	}
	for _, forbidden := range []string{"xpk-alpha001", "xpk-alpha003", "manifest pack_id"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("verifyFullPack() error leaks %q: %v", forbidden, err)
		}
	}
}

func TestNewFullPackTestsDoNotContainProtectedPathLiterals(t *testing.T) {
	data, err := os.ReadFile("main_test.go")
	if err != nil {
		t.Fatalf("read main_test.go: %v", err)
	}
	start := bytes.Index(data, []byte("func TestFullPackMetadataRenderLockValidTwoPackOpaque"))
	if start < 0 {
		t.Fatalf("new full-pack test section not found")
	}
	section := string(data[start:])
	for _, forbidden := range []string{"D:/" + "coder", `D:\` + "coder", "GoAria-" + "Wails3"} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("new full-pack tests contain protected literal %q", forbidden)
		}
	}
}

type testAsset struct {
	bytes     []byte
	parts     packParts
	publicKey ed25519.PublicKey
}

func (a testAsset) lockEntry() packLockEntry {
	return packLockEntry{
		PackID:          "fixture-pack",
		PackVersion:     "0.1.0-fixture",
		AssetPath:       "fixture.pack.zip",
		AssetSHA256:     sha256Hex(a.bytes),
		PublicKeys:      []string{hex.EncodeToString(a.publicKey)},
		ManifestSHA256:  sha256Hex(a.parts.ManifestJSON),
		PayloadSHA256:   sha256Hex(a.parts.Payload),
		SignatureSHA256: sha256Hex(a.parts.Signature),
	}
}

func validTestAsset(t *testing.T) testAsset {
	t.Helper()
	publicKey, privateKey := deterministicTestKeyPair(77)
	payload := []byte("public inert fixture payload")
	manifestJSON := manifestJSONForPayload(t, payload)
	parts := packParts{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
	}

	return testAsset{bytes: zipBytesForParts(t, parts, nil), parts: parts, publicKey: publicKey}
}

func validAliasTestAsset(t *testing.T) testAsset {
	t.Helper()
	publicKey, privateKey := deterministicTestKeyPair(78)
	payload := []byte("public alias fixture payload")
	manifestJSON := manifestJSONForPayloadWithMutate(t, payload, func(values map[string]any) {
		values["pack_id"] = "xpk-alpha001"
		values["capabilities"] = []string{string(extractor.CapabilityParseWASM), string(extractor.CapabilityHTTPFetch)}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-alpha001"}
		values["broker_policy_refs"] = []string{"bpr-alpha001"}
	})
	parts := packParts{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
	}

	return testAsset{bytes: zipBytesForParts(t, parts, nil), parts: parts, publicKey: publicKey}
}

func validOpaqueTestAsset(t *testing.T, packID string, packVersion string, seedByte byte) testAsset {
	t.Helper()
	publicKey, privateKey := deterministicTestKeyPair(seedByte)
	payload := []byte("public opaque fixture payload " + packID)
	manifestJSON := manifestJSONForPayloadWithMutate(t, payload, func(values map[string]any) {
		values["pack_id"] = packID
		values["pack_version"] = packVersion
		values["capabilities"] = []string{string(extractor.CapabilityParseWASM), string(extractor.CapabilityHTTPFetch)}
		values["domains"] = []map[string]any{}
		values["domain_policy_refs"] = []string{"dpr-" + strings.TrimPrefix(packID, "xpk-")}
		values["broker_policy_refs"] = []string{"bpr-" + strings.TrimPrefix(packID, "xpk-")}
	})
	parts := packParts{
		ManifestJSON: manifestJSON,
		Payload:      payload,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
	}

	return testAsset{bytes: zipBytesForParts(t, parts, nil), parts: parts, publicKey: publicKey}
}

func validFullPackMetadata(assetOne testAsset, assetTwo testAsset) fullPackMetadataFile {
	entryOne := assetOne.lockEntry()
	entryTwo := assetTwo.lockEntry()

	return fullPackMetadataFile{
		SchemaVersion: lockSchemaVersion,
		ReleaseTag:    "v0.0.0-alpha",
		BaseAssetURL:  "https://release.example.test/assets/v0.0.0-alpha",
		Packs: []fullPackMetadataEntry{
			{
				AssetName:       "asset-alpha001.pack.zip",
				PackID:          "xpk-alpha001",
				PackVersion:     "opaque-1",
				AssetSHA256:     entryOne.AssetSHA256,
				PublicKeys:      entryOne.PublicKeys,
				ManifestSHA256:  entryOne.ManifestSHA256,
				PayloadSHA256:   entryOne.PayloadSHA256,
				SignatureSHA256: entryOne.SignatureSHA256,
			},
			{
				AssetName:       "asset-alpha002.pack.zip",
				PackID:          "xpk-alpha002",
				PackVersion:     "opaque-2",
				AssetSHA256:     entryTwo.AssetSHA256,
				PublicKeys:      entryTwo.PublicKeys,
				ManifestSHA256:  entryTwo.ManifestSHA256,
				PayloadSHA256:   entryTwo.PayloadSHA256,
				SignatureSHA256: entryTwo.SignatureSHA256,
			},
		},
	}
}

func fullPackMetadataJSON(t *testing.T, metadata fullPackMetadataFile) []byte {
	t.Helper()
	bytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	return append(bytes, '\n')
}

func writeFullPackMetadata(t *testing.T, metadata fullPackMetadataFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "full_pack_assets.json")
	if err := os.WriteFile(path, fullPackMetadataJSON(t, metadata), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

func twoPackTLSServer(t *testing.T, routes map[string][]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asset, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}
		_, _ = w.Write(asset)
	}))
	t.Cleanup(server.Close)

	return server
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func rewriteHostClient(t *testing.T, server *httptest.Server, host string) *http.Client {
	t.Helper()

	return rewriteMultiHostClient(t, map[string]*httptest.Server{host: server})
}

func rewriteMultiHostClient(t *testing.T, hosts map[string]*httptest.Server) *http.Client {
	t.Helper()
	base := firstTLSServer(t, hosts)
	client := base.Client()
	client.Transport = rewriteHostTransport{base: trustAllTLSServerTransport(t, mapServers(hosts)...), hosts: hosts}

	return client
}

type rewriteHostTransport struct {
	base  http.RoundTripper
	hosts map[string]*httptest.Server
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	server, ok := t.hosts[req.URL.Hostname()]
	if !ok {
		return t.base.RoundTrip(req)
	}
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		return nil, err
	}
	cloned := req.Clone(req.Context())
	cloned.URL = cloneURL(req.URL)
	cloned.URL.Scheme = serverURL.Scheme
	cloned.URL.Host = serverURL.Host

	return t.base.RoundTrip(cloned)
}

func cloneURL(value *url.URL) *url.URL {
	cloned := *value

	return &cloned
}

func firstTLSServer(t *testing.T, hosts map[string]*httptest.Server) *httptest.Server {
	t.Helper()
	for _, server := range hosts {
		return server
	}
	t.Fatal("rewriteMultiHostClient requires at least one server")

	return nil
}

func mapServers(hosts map[string]*httptest.Server) []*httptest.Server {
	servers := make([]*httptest.Server, 0, len(hosts))
	for _, server := range hosts {
		servers = append(servers, server)
	}

	return servers
}

func trustAllTLSServerTransport(t *testing.T, servers ...*httptest.Server) http.RoundTripper {
	t.Helper()
	if len(servers) == 0 {
		t.Fatal("trustAllTLSServerTransport requires at least one server")
	}
	transportConfig := servers[0].Client().Transport.(*http.Transport).TLSClientConfig
	pool := transportConfig.RootCAs
	if pool == nil {
		t.Fatal("test TLS server root pool is nil")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	for _, server := range servers[1:] {
		pool.AddCert(server.Certificate())
	}

	return transport
}

func manifestJSONForPayload(t *testing.T, payload []byte) []byte {
	t.Helper()

	return manifestJSONForPayloadWithMutate(t, payload, nil)
}

func manifestJSONForPayloadWithMutate(t *testing.T, payload []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	payloadHash := sha256.Sum256(payload)
	values := map[string]any{
		"pack_id":      "fixture-pack",
		"pack_version": "0.1.0-fixture",
		"abi_version":  extractor.CurrentABIVersion,
		"capabilities": []string{string(extractor.CapabilityParseWASM)},
		"domains":      []map[string]any{{"host": "example.test", "include_subdomains": true}},
		"resource_limits": map[string]any{
			"timeout_millis":     500,
			"max_memory_pages":   1,
			"max_host_calls":     1,
			"max_response_bytes": 1024,
			"max_output_items":   1,
			"max_output_bytes":   1024,
		},
		"payload_sha256": hex.EncodeToString(payloadHash[:]),
	}
	if mutate != nil {
		mutate(values)
	}
	manifestJSON, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	return manifestJSON
}

func zipBytesForParts(t *testing.T, parts packParts, extra map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		if data == nil {
			return
		}
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(0o644)
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add("manifest.json", parts.ManifestJSON)
	add("payload.wasm", parts.Payload)
	add("manifest.sig", parts.Signature)
	for name, data := range extra {
		add(name, data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func writeLockWithAsset(t *testing.T, asset []byte, entry packLockEntry) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.pack.zip"), asset, 0o644); err != nil {
		t.Fatal(err)
	}

	return writeLock(t, dir, entry)
}

func writeLock(t *testing.T, dir string, entries ...packLockEntry) string {
	t.Helper()
	lock := lockFile{SchemaVersion: lockSchemaVersion, Packs: entries}
	bytes, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "packs.lock.json")
	if err := os.WriteFile(lockPath, append(bytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	return lockPath
}

func deterministicTestKeyPair(seedByte byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := bytes.Repeat([]byte{seedByte}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)

	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func fixturePublicKeyHex(t *testing.T) string {
	t.Helper()
	asset := validTestAsset(t)

	return hex.EncodeToString(asset.publicKey)
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s missing, stat err = %v", path, err)
	}
}
