package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
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

func TestFullPackVerifyUsesLocalAssetDirWithoutHTTPFetch(t *testing.T) {
	fixture := validWorkflowFullPackFixture(t)
	outDir := t.TempDir()
	tempLockPath := filepath.Join(outDir, "full_pack.lock.json")
	outPath := filepath.Join(outDir, "embedded.go")
	provenancePath := filepath.Join(outDir, "provenance.json")
	assetDir := writeLocalFullPackAssetDir(t, outDir, fixture.metadata, fixture.assets)

	if err := verifyFullPack(fullPackVerifyOptions{
		MetadataPath:  writeFullPackMetadata(t, fixture.metadata),
		TempLockPath:  tempLockPath,
		OutPath:       outPath,
		ProvenanceOut: provenancePath,
		LocalAssetDir: assetDir,
	}); err != nil {
		t.Fatalf("verifyFullPack() error = %v", err)
	}
	lockText := readTextFile(t, tempLockPath)
	if !strings.Contains(lockText, `"asset_path"`) || strings.Contains(lockText, `"asset_url"`) {
		t.Fatalf("full-pack local lock should use asset_path only: %s", lockText)
	}
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
	for _, tc := range []struct {
		surface string
		data    []byte
	}{
		{surface: "metadata", data: []byte(`{"private_policy":"secret-value"}`)},
		{surface: "provenance", data: []byte(`{"pack_id":"xpk-alpha001","note":"private"}`)},
		{surface: "evidence", data: []byte(`{"variant":"generic-no-pack","note":"private"}`)},
	} {
		t.Run(tc.surface, func(t *testing.T) {
			err := auditNoNameBytes(tc.surface, tc.data)
			if err == nil || err.Error() != tc.surface+" no-name audit failed" {
				t.Fatalf("auditNoNameBytes() error = %v, want category-only audit failure", err)
			}
			for _, forbidden := range []string{"private_policy", "secret-value", "private"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("audit error leaks matched value %q: %v", forbidden, err)
				}
			}
		})
	}

	allowed := []byte(`{"schema_version":1,"release_tag":"v0.0.0-alpha","asset_url_template":"https://release.example.test/assets/{release_tag}/{asset_name}","packs":[{"asset_name":"asset-alpha001.pack.zip","pack_id":"xpk-alpha001","pack_version":"opaque-1","asset_sha256":"` + strings.Repeat("0", 64) + `","public_keys":["` + strings.Repeat("a", 64) + `"]}]}`)
	if err := auditNoNameBytes("metadata", allowed); err != nil {
		t.Fatalf("auditNoNameBytes() allowed synthetic surface error = %v", err)
	}
}

func TestWorkflowPrepareGenericNoPackCleansStaleOutputsAndWritesSummary(t *testing.T) {
	paths := testWorkflowPaths(t)
	for _, filePath := range []string{paths.MetadataPath, paths.TempLockPath, paths.PackEmbedPath, paths.PolicyOutPath, paths.AuthRuntimeOutPath, paths.ProvenanceOut, paths.SummaryOut} {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := prepareWorkflow(workflowPrepareOptions{Mode: workflowVariantGenericNoPack, Paths: paths}); err != nil {
		t.Fatalf("prepareWorkflow() generic error = %v", err)
	}
	for _, filePath := range []string{paths.MetadataPath, paths.TempLockPath, paths.PackEmbedPath, paths.PolicyOutPath, paths.AuthRuntimeOutPath, paths.ProvenanceOut} {
		assertFileMissing(t, filePath)
	}
	summary := readWorkflowSummary(t, paths.SummaryOut)
	if summary.Variant != workflowVariantGenericNoPack || summary.PackAssetCount != 0 || summary.HostPolicyBundleInjected || summary.AuthRuntimeBundleInjected || summary.PackVerificationRequired {
		t.Fatalf("unexpected generic summary: %+v", summary)
	}
}

func TestWorkflowPrepareFullPackSyntheticSuccessAndCleanup(t *testing.T) {
	fixture := validWorkflowFullPackFixture(t)
	paths := testWorkflowPaths(t)

	if err := prepareWorkflow(workflowPrepareOptions{
		Mode:           workflowVariantFullPack,
		MetadataB64:    base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, fixture.metadata)),
		PolicyB64:      base64.StdEncoding.EncodeToString(fixture.policyRaw),
		AuthRuntimeB64: base64.StdEncoding.EncodeToString(fixture.authRuntimeRaw),
		Paths:          paths,
		HTTPClient:     fixture.client,
	}); err != nil {
		t.Fatalf("prepareWorkflow() full-pack error = %v", err)
	}

	for _, filePath := range []string{paths.MetadataPath, paths.TempLockPath, paths.PackEmbedPath, paths.PolicyOutPath, paths.AuthRuntimeOutPath, paths.ProvenanceOut, paths.SummaryOut} {
		if _, err := os.Stat(filePath); err != nil {
			t.Fatalf("expected workflow output %s: %v", filePath, err)
		}
	}
	summary := readWorkflowSummary(t, paths.SummaryOut)
	if summary.Variant != workflowVariantFullPack || summary.PackAssetCount != fullPackCount || !summary.HostPolicyBundleInjected || !summary.AuthRuntimeBundleInjected || !summary.GeneratedPackEmbed || summary.PublicProvenanceWritten {
		t.Fatalf("unexpected full-pack summary: %+v", summary)
	}
	summaryText := readTextFile(t, paths.SummaryOut)
	for _, forbidden := range []string{"policy_private_sha256", privatePolicyHashFromRaw(t, fixture.policyRaw), privateAuthRuntimeHashFromRaw(t, fixture.authRuntimeRaw), "private_policy", "private", "secret", "token", "cookie", "authorization", "asset_url", "asset_path", "release.example.test", "fixture.invalid", "example.test", "https://", "/assets/", defaultWorkflowAuthRuntimeEmbedPath} {
		if strings.Contains(summaryText, forbidden) {
			t.Fatalf("summary leaks forbidden value %q: %s", forbidden, summaryText)
		}
	}
	for _, label := range summary.EvidenceOutputLabels {
		if label != "extractor_build_evidence.summary.json" {
			t.Fatalf("workflow evidence label should not include provenance or paths: %+v", summary.EvidenceOutputLabels)
		}
	}
	policyEmbed := readTextFile(t, paths.PolicyOutPath)
	if !strings.Contains(policyEmbed, "embeddedPrivatePolicyBundleJSON") || !strings.Contains(policyEmbed, "embeddedPrivatePolicyBundleSHA256") {
		t.Fatalf("host policy embed missing expected seam variables: %s", policyEmbed)
	}
	if err := auditNoNameBytes("policy embed", []byte(policyEmbed)); err == nil {
		t.Fatalf("private generated policy file must remain excluded from public no-name audit")
	}
	authRuntimeEmbed := readTextFile(t, paths.AuthRuntimeOutPath)
	if !strings.Contains(authRuntimeEmbed, "embeddedPrivateAuthRuntimeBundleJSON") || !strings.Contains(authRuntimeEmbed, "embeddedPrivateAuthRuntimeBundleSHA256") {
		t.Fatalf("auth runtime embed missing expected seam variables: %s", authRuntimeEmbed)
	}
	if err := auditNoNameBytes("auth runtime embed", []byte(authRuntimeEmbed)); err == nil {
		t.Fatalf("private generated auth runtime file must remain excluded from public no-name audit")
	}

	if err := cleanupWorkflow(workflowCleanupOptions{Paths: paths}); err != nil {
		t.Fatalf("cleanupWorkflow() error = %v", err)
	}
	for _, filePath := range []string{paths.MetadataPath, paths.TempLockPath, paths.PackEmbedPath, paths.PolicyOutPath, paths.AuthRuntimeOutPath, paths.ProvenanceOut, paths.SummaryOut} {
		assertFileMissing(t, filePath)
	}
}

func TestWorkflowPrepareFullPackUsesLocalAssetDir(t *testing.T) {
	fixture := validWorkflowFullPackFixture(t)
	paths := testWorkflowPaths(t)
	assetDir := writeLocalFullPackAssetDir(t, filepath.Dir(paths.TempLockPath), fixture.metadata, fixture.assets)

	if err := prepareWorkflow(workflowPrepareOptions{
		Mode:           workflowVariantFullPack,
		MetadataB64:    base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, fixture.metadata)),
		PolicyB64:      base64.StdEncoding.EncodeToString(fixture.policyRaw),
		AuthRuntimeB64: base64.StdEncoding.EncodeToString(fixture.authRuntimeRaw),
		LocalAssetDir:  assetDir,
		Paths:          paths,
	}); err != nil {
		t.Fatalf("prepareWorkflow() local full-pack error = %v", err)
	}
	lockText := readTextFile(t, paths.TempLockPath)
	if !strings.Contains(lockText, `"asset_path"`) || strings.Contains(lockText, `"asset_url"`) {
		t.Fatalf("workflow local lock should use asset_path only: %s", lockText)
	}
	if err := cleanupWorkflow(workflowCleanupOptions{Paths: paths}); err != nil {
		t.Fatalf("cleanupWorkflow() error = %v", err)
	}
	assertWorkflowOutputsMissing(t, paths)
}

func TestWorkflowPrepareFullPackFailsClosedForMissingInputsAndBadChecksum(t *testing.T) {
	fixture := validWorkflowFullPackFixture(t)

	t.Run("missing metadata", func(t *testing.T) {
		paths := testWorkflowPaths(t)
		err := prepareWorkflow(workflowPrepareOptions{
			Mode:           workflowVariantFullPack,
			PolicyB64:      base64.StdEncoding.EncodeToString(fixture.policyRaw),
			AuthRuntimeB64: base64.StdEncoding.EncodeToString(fixture.authRuntimeRaw),
			Paths:          paths,
		})
		if err == nil {
			t.Fatal("prepareWorkflow() error = nil, want missing metadata failure")
		}
		assertWorkflowOutputsMissing(t, paths)
	})

	t.Run("missing policy", func(t *testing.T) {
		paths := testWorkflowPaths(t)
		err := prepareWorkflow(workflowPrepareOptions{
			Mode:           workflowVariantFullPack,
			MetadataB64:    base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, fixture.metadata)),
			AuthRuntimeB64: base64.StdEncoding.EncodeToString(fixture.authRuntimeRaw),
			Paths:          paths,
		})
		if err == nil {
			t.Fatal("prepareWorkflow() error = nil, want missing policy failure")
		}
		assertWorkflowOutputsMissing(t, paths)
	})

	t.Run("missing auth runtime", func(t *testing.T) {
		paths := testWorkflowPaths(t)
		err := prepareWorkflow(workflowPrepareOptions{
			Mode:        workflowVariantFullPack,
			MetadataB64: base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, fixture.metadata)),
			PolicyB64:   base64.StdEncoding.EncodeToString(fixture.policyRaw),
			Paths:       paths,
			HTTPClient:  fixture.client,
		})
		if err == nil {
			t.Fatal("prepareWorkflow() error = nil, want missing auth runtime failure")
		}
		if err.Error() != "auth runtime bundle custody input is required" {
			t.Fatalf("prepareWorkflow() error = %v, want missing auth runtime custody failure", err)
		}
		assertWorkflowOutputsMissing(t, paths)
	})

	t.Run("wrong checksum", func(t *testing.T) {
		paths := testWorkflowPaths(t)
		metadata := fixture.metadata
		metadata.Packs = append([]fullPackMetadataEntry(nil), fixture.metadata.Packs...)
		metadata.Packs[0].AssetSHA256 = strings.Repeat("0", 64)
		err := prepareWorkflow(workflowPrepareOptions{
			Mode:           workflowVariantFullPack,
			MetadataB64:    base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, metadata)),
			PolicyB64:      base64.StdEncoding.EncodeToString(fixture.policyRaw),
			AuthRuntimeB64: base64.StdEncoding.EncodeToString(fixture.authRuntimeRaw),
			Paths:          paths,
			HTTPClient:     fixture.client,
		})
		if err == nil || err.Error() != "full-pack verification failed" {
			t.Fatalf("prepareWorkflow() error = %v, want sanitized verification failure", err)
		}
		assertWorkflowOutputsMissing(t, paths)
	})
}

func TestWorkflowPrepareFullPackAuthRuntimeValidationFailures(t *testing.T) {
	fixture := validWorkflowFullPackFixture(t)

	t.Run("expected sha mismatch", func(t *testing.T) {
		paths := testWorkflowPaths(t)
		err := prepareWorkflow(workflowPrepareOptions{
			Mode:              workflowVariantFullPack,
			MetadataB64:       base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, fixture.metadata)),
			PolicyB64:         base64.StdEncoding.EncodeToString(fixture.policyRaw),
			AuthRuntimeB64:    base64.StdEncoding.EncodeToString(fixture.authRuntimeRaw),
			AuthRuntimeSHA256: strings.Repeat("1", 64),
			Paths:             paths,
			HTTPClient:        fixture.client,
		})
		if err == nil || err.Error() != "auth runtime bundle is invalid" {
			t.Fatalf("prepareWorkflow() error = %v, want auth runtime mismatch", err)
		}
		for _, forbidden := range []string{string(fixture.authRuntimeRaw), privateAuthRuntimeHashFromRaw(t, fixture.authRuntimeRaw), "apr-alpha001", "fixture.invalid", "https://"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("auth runtime error leaks %q: %v", forbidden, err)
			}
		}
		assertWorkflowOutputsMissing(t, paths)
	})

	t.Run("identity set mismatch", func(t *testing.T) {
		paths := testWorkflowPaths(t)
		mismatchedRaw := privateAuthRuntimeBundleRawForScript(t, []privatePolicyBundlePackFixtureForScript{{
			Identity: mutateScriptIdentity(identityForScriptAsset(renderedLockEntryForFixture(t, fixture, 0), fixture.assets[0]), func(id *extractor.VerifiedPackIdentity) {
				id.ManifestSHA256 = strings.Repeat("e", 64)
			}),
		}})
		err := prepareWorkflow(workflowPrepareOptions{
			Mode:           workflowVariantFullPack,
			MetadataB64:    base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, fixture.metadata)),
			PolicyB64:      base64.StdEncoding.EncodeToString(fixture.policyRaw),
			AuthRuntimeB64: base64.StdEncoding.EncodeToString(mismatchedRaw),
			Paths:          paths,
			HTTPClient:     fixture.client,
		})
		if err == nil || err.Error() != "auth runtime bundle is invalid" {
			t.Fatalf("prepareWorkflow() error = %v, want auth runtime identity mismatch", err)
		}
		for _, forbidden := range []string{string(mismatchedRaw), privateAuthRuntimeHashFromRaw(t, mismatchedRaw), "apr-alpha001", "fixture.invalid"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("auth runtime error leaks %q: %v", forbidden, err)
			}
		}
		assertWorkflowOutputsMissing(t, paths)
	})
}

func TestWorkflowPrepareFullPackNoNameAuditFailureCleansOutputs(t *testing.T) {
	fixture := validWorkflowFullPackFixture(t)
	paths := testWorkflowPaths(t)
	err := prepareWorkflow(workflowPrepareOptions{
		Mode:           workflowVariantFullPack,
		MetadataB64:    base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, fixture.metadata)),
		PolicyB64:      base64.StdEncoding.EncodeToString(fixture.policyRaw),
		AuthRuntimeB64: base64.StdEncoding.EncodeToString(fixture.authRuntimeRaw),
		Paths:          paths,
		HTTPClient:     fixture.client,
		NoNameAudit: func(surface string, data []byte) error {
			if surface == "generated provenance" {
				return auditNoNameBytes(surface, append(data, []byte("protected-root-marker")...))
			}

			return auditNoNameBytes(surface, data)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "generated provenance no-name audit failed") {
		t.Fatalf("prepareWorkflow() error = %v, want no-name audit failure", err)
	}
	if strings.Contains(err.Error(), "protected-root-marker") {
		t.Fatalf("audit error leaks forbidden marker: %v", err)
	}
	assertWorkflowOutputsMissing(t, paths)
}

func TestWorkflowSurfacesUseGenericVariantsAndEvidenceArtifacts(t *testing.T) {
	workflow := readTextFile(t, filepath.Join("..", "..", ".github", "workflows", "build.yml"))
	for _, want := range []string{
		"extractor_variant:",
		"generic-no-pack",
		"full-pack",
		"resolve-package-variant",
		"EXTRACTOR_RELEASE_VARIANT",
		"EXTRACTOR_FULL_PACK_METADATA_B64",
		"EXTRACTOR_PRIVATE_POLICY_BUNDLE_B64",
		"EXTRACTOR_PRIVATE_AUTH_RUNTIME_BUNDLE_B64",
		"EXTRACTOR_PRIVATE_AUTH_RUNTIME_SHA256",
		"EXTRACTOR_FULL_PACK_LOCAL_ASSET_DIR",
		"gh release download",
		"asset-*.pack.zip",
		"extractor-build-evidence-${{ needs.resolve-package-variant.outputs.extractor_variant }}-linux",
		"linux-packages-${{ needs.resolve-package-variant.outputs.extractor_variant }}",
		"wails3 task extractor:workflow:prepare",
		"wails3 task extractor:workflow:cleanup",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{"protected-root-marker", "endpoint-template-marker", "raw-secret-marker", "supported_site"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("workflow contains forbidden marker %q", forbidden)
		}
	}
	if strings.Contains(workflow, "build/extractor/verified_packs.provenance.json") {
		t.Fatalf("workflow must not upload URL/path-bearing pack provenance as public evidence")
	}
	if strings.Contains(workflow, defaultWorkflowPolicyEmbedPath) {
		t.Fatalf("workflow must not upload generated host policy code as public evidence")
	}
	if strings.Contains(workflow, defaultWorkflowAuthRuntimeEmbedPath) {
		t.Fatalf("workflow must not upload generated auth runtime code as public evidence")
	}

	taskfile := readTextFile(t, filepath.Join("..", "..", "Taskfile.yml"))
	for _, want := range []string{"extractor:workflow:prepare", "extractor:workflow:cleanup", "go run ./scripts/extractorpacks prepare-workflow", "go run ./scripts/extractorpacks cleanup-workflow"} {
		if !strings.Contains(taskfile, want) {
			t.Fatalf("Taskfile missing %q", want)
		}
	}
}

func TestWorkflowPrivateGeneratedPathsAreIgnored(t *testing.T) {
	gitignore := readTextFile(t, filepath.Join("..", "..", ".gitignore"))
	for _, ignored := range []string{defaultWorkflowPolicyEmbedPath, defaultWorkflowAuthRuntimeEmbedPath, defaultWorkflowEvidenceSummary} {
		if !strings.Contains(gitignore, ignored) {
			t.Fatalf(".gitignore does not cover %s", ignored)
		}
	}
}

func TestWorkflowErrorTextDoesNotEchoCustodyValues(t *testing.T) {
	paths := testWorkflowPaths(t)
	fixture := validWorkflowFullPackFixture(t)
	secretAuthRuntime := "raw-secret-marker-runtime"
	authRuntimePath := filepath.Join(t.TempDir(), "auth-runtime.json")
	if err := os.WriteFile(authRuntimePath, []byte(secretAuthRuntime), 0o600); err != nil {
		t.Fatal(err)
	}
	err := prepareWorkflow(workflowPrepareOptions{
		Mode:                 workflowVariantFullPack,
		MetadataB64:          base64.StdEncoding.EncodeToString(fullPackMetadataJSON(t, fixture.metadata)),
		PolicyB64:            base64.StdEncoding.EncodeToString(fixture.policyRaw),
		AuthRuntimeB64:       secretAuthRuntime,
		AuthRuntimeInputPath: authRuntimePath,
		Paths:                paths,
		HTTPClient:           fixture.client,
	})
	if err == nil {
		t.Fatal("prepareWorkflow() error = nil, want malformed custody failure")
	}
	if err.Error() != "auth runtime bundle custody input is required" {
		t.Fatalf("prepareWorkflow() error = %v, want auth runtime custody conflict", err)
	}
	for _, forbidden := range []string{secretAuthRuntime, authRuntimePath, "raw-secret-marker"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaks custody value %q: %v", forbidden, err)
		}
	}
	assertWorkflowOutputsMissing(t, paths)
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

type workflowFullPackFixture struct {
	metadata       fullPackMetadataFile
	policyRaw      []byte
	authRuntimeRaw []byte
	client         *http.Client
	assets         []testAsset
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

func validWorkflowFullPackFixture(t *testing.T) workflowFullPackFixture {
	t.Helper()
	assetOne := validOpaqueTestAsset(t, "xpk-alpha001", "opaque-1", 81)
	assetTwo := validOpaqueTestAsset(t, "xpk-alpha002", "opaque-2", 82)
	server := twoPackTLSServer(t, map[string][]byte{
		"/assets/v0.0.0-alpha/asset-alpha001.pack.zip": assetOne.bytes,
		"/assets/v0.0.0-alpha/asset-alpha002.pack.zip": assetTwo.bytes,
	})
	metadata := validFullPackMetadata(assetOne, assetTwo)
	metadata.BaseAssetURL = "https://release.example.test/assets/v0.0.0-alpha"
	lock, err := renderFullPackLock(metadata)
	if err != nil {
		t.Fatalf("renderFullPackLock() error = %v", err)
	}
	assets := []testAsset{assetOne, assetTwo}
	fixtures := make([]privatePolicyBundlePackFixtureForScript, 0, len(lock.Packs))
	for i, entry := range lock.Packs {
		fixtures = append(fixtures, privatePolicyBundlePackFixtureForScript{
			Identity: identityForScriptAsset(entry, assets[i]),
			Manifest: manifestForScriptAsset(t, assets[i]),
		})
	}

	return workflowFullPackFixture{
		metadata:       metadata,
		policyRaw:      privatePolicyBundleRawForScript(t, fixtures),
		authRuntimeRaw: privateAuthRuntimeBundleRawForScript(t, fixtures),
		client:         rewriteHostClient(t, server, "release.example.test"),
		assets:         []testAsset{assetOne, assetTwo},
	}
}

type privatePolicyBundlePackFixtureForScript struct {
	Identity extractor.VerifiedPackIdentity
	Manifest extractor.Manifest
}

func identityForScriptAsset(entry packLockEntry, asset testAsset) extractor.VerifiedPackIdentity {
	return extractor.VerifiedPackIdentity{
		PackID:          entry.PackID,
		PackVersion:     entry.PackVersion,
		AssetSHA256:     entry.AssetSHA256,
		ManifestSHA256:  entry.ManifestSHA256,
		PayloadSHA256:   entry.PayloadSHA256,
		SignatureSHA256: entry.SignatureSHA256,
		PublicKeySHA256: sha256Hex(asset.publicKey),
	}
}

func manifestForScriptAsset(t *testing.T, asset testAsset) extractor.Manifest {
	t.Helper()
	var manifest extractor.Manifest
	if err := json.Unmarshal(asset.parts.ManifestJSON, &manifest); err != nil {
		t.Fatalf("json.Unmarshal(manifest) error = %v", err)
	}

	return manifest
}

func privatePolicyBundleRawForScript(t *testing.T, fixtures []privatePolicyBundlePackFixtureForScript) []byte {
	t.Helper()
	packEntries := make([]map[string]any, 0, len(fixtures))
	for _, fixture := range fixtures {
		packEntries = append(packEntries, map[string]any{
			"verified_pack_identity": map[string]any{
				"pack_id":           fixture.Identity.PackID,
				"pack_version":      fixture.Identity.PackVersion,
				"asset_sha256":      fixture.Identity.AssetSHA256,
				"manifest_sha256":   fixture.Identity.ManifestSHA256,
				"payload_sha256":    fixture.Identity.PayloadSHA256,
				"signature_sha256":  fixture.Identity.SignatureSHA256,
				"public_key_sha256": fixture.Identity.PublicKeySHA256,
			},
			"domain_policy_refs":   append([]string(nil), fixture.Manifest.DomainPolicyRefs...),
			"broker_policy_refs":   append([]string(nil), fixture.Manifest.BrokerPolicyRefs...),
			"allowed_capabilities": append([]extractor.Capability(nil), fixture.Manifest.Capabilities...),
			"ingress_domain_rules": []extractor.DomainRule{{Host: "share.alpha.test"}},
			"broker_domain_rules":  []extractor.DomainRule{{Host: "api.alpha.test"}},
			"broker_endpoints": []map[string]any{{
				"broker_policy_ref": fixture.Manifest.BrokerPolicyRefs[0],
				"endpoint_ref":      "epr-alpha001",
				"url_template":      "https://api.alpha.test/resource/{id}",
				"auth_profile_refs": []string{"apr-alpha001"},
			}},
		})
	}
	policy := map[string]any{"packs": packEntries}
	policyRaw, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("json.Marshal(policy) error = %v", err)
	}
	bundle := map[string]any{
		"schema_version":        1,
		"bundle_id":             "hpb-alpha001",
		"bundle_version":        "opaque-1",
		"policy_private_sha256": sha256Hex(policyRaw),
		"policy":                json.RawMessage(policyRaw),
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("json.Marshal(bundle) error = %v", err)
	}

	return raw
}

func privateAuthRuntimeBundleRawForScript(t *testing.T, fixtures []privatePolicyBundlePackFixtureForScript) []byte {
	t.Helper()
	packEntries := make([]map[string]any, 0, len(fixtures))
	for i, fixture := range fixtures {
		profileRef := "apr-alpha001"
		loginHost := "fixture.invalid"
		loginURL := "https://fixture.invalid/login"
		if i%2 == 1 {
			profileRef = "apr-alpha002"
			loginHost = "example.test"
			loginURL = "https://example.test/login"
		}
		packEntries = append(packEntries, map[string]any{
			"verified_pack_identity": map[string]any{
				"pack_id":           fixture.Identity.PackID,
				"pack_version":      fixture.Identity.PackVersion,
				"asset_sha256":      fixture.Identity.AssetSHA256,
				"manifest_sha256":   fixture.Identity.ManifestSHA256,
				"payload_sha256":    fixture.Identity.PayloadSHA256,
				"signature_sha256":  fixture.Identity.SignatureSHA256,
				"public_key_sha256": fixture.Identity.PublicKeySHA256,
			},
			"store_binding": map[string]any{
				"scope":        "pack",
				"profile_refs": []string{profileRef},
			},
			"profiles": []map[string]any{{
				"profile_ref": profileRef,
				"kind":        string(extractor.AuthSecretKindBearer),
				"login": map[string]any{
					"url":             loginURL,
					"allowed_domains": []map[string]any{{"host": loginHost}},
					"timeout_millis":  30000,
				},
			}},
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
		})
	}
	runtime := map[string]any{"packs": packEntries}
	runtimeRaw, err := json.Marshal(runtime)
	if err != nil {
		t.Fatalf("json.Marshal(runtime) error = %v", err)
	}
	bundle := map[string]any{
		"schema_version":                  1,
		"bundle_id":                       "arb-alpha001",
		"bundle_version":                  "opaque-1",
		"auth_runtime_private_sha256":     sha256Hex(runtimeRaw),
		"auth_runtime_public_fingerprint": strings.Repeat("a", 64),
		"runtime":                         json.RawMessage(runtimeRaw),
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("json.Marshal(auth runtime bundle) error = %v", err)
	}

	return raw
}

func privatePolicyHashFromRaw(t *testing.T, raw []byte) string {
	t.Helper()
	var envelope struct {
		Policy json.RawMessage `json:"policy"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("json.Unmarshal(policy envelope) error = %v", err)
	}

	return sha256Hex(envelope.Policy)
}

func privateAuthRuntimeHashFromRaw(t *testing.T, raw []byte) string {
	t.Helper()
	var envelope struct {
		Runtime json.RawMessage `json:"runtime"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("json.Unmarshal(auth runtime envelope) error = %v", err)
	}

	return sha256Hex(envelope.Runtime)
}

func renderedLockEntryForFixture(t *testing.T, fixture workflowFullPackFixture, index int) packLockEntry {
	t.Helper()
	lock, err := renderFullPackLock(fixture.metadata)
	if err != nil {
		t.Fatalf("renderFullPackLock() error = %v", err)
	}
	if index < 0 || index >= len(lock.Packs) {
		t.Fatalf("lock index %d out of range", index)
	}

	return lock.Packs[index]
}

func mutateScriptIdentity(identity extractor.VerifiedPackIdentity, mutate func(*extractor.VerifiedPackIdentity)) extractor.VerifiedPackIdentity {
	mutated := identity
	mutate(&mutated)

	return mutated
}

func testWorkflowPaths(t *testing.T) workflowPaths {
	t.Helper()
	root := t.TempDir()

	return workflowPaths{
		MetadataPath:       filepath.Join(root, "build", "extractor", "cache", "full_pack_assets.json"),
		TempLockPath:       filepath.Join(root, "build", "extractor", "cache", "full_pack.lock.json"),
		PackEmbedPath:      filepath.Join(root, "internal", "extractor", "embedded_packs_release_gen.go"),
		PolicyOutPath:      filepath.Join(root, "internal", "extractor", "private_policy_bundle_release_gen.go"),
		AuthRuntimeOutPath: filepath.Join(root, "internal", "extractor", "private_auth_runtime_release_gen.go"),
		ProvenanceOut:      filepath.Join(root, "build", "extractor", "verified_packs.provenance.json"),
		SummaryOut:         filepath.Join(root, "build", "extractor", "extractor_build_evidence.summary.json"),
	}
}

func readWorkflowSummary(t *testing.T, path string) workflowEvidenceSummary {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workflow summary: %v", err)
	}
	var summary workflowEvidenceSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatalf("json.Unmarshal(summary) error = %v", err)
	}

	return summary
}

func assertWorkflowOutputsMissing(t *testing.T, paths workflowPaths) {
	t.Helper()
	for _, filePath := range []string{paths.MetadataPath, paths.TempLockPath, paths.PackEmbedPath, paths.PolicyOutPath, paths.AuthRuntimeOutPath, paths.ProvenanceOut, paths.SummaryOut} {
		assertFileMissing(t, filePath)
	}
}

func writeLocalFullPackAssetDir(t *testing.T, root string, metadata fullPackMetadataFile, assets []testAsset) string {
	t.Helper()
	if len(metadata.Packs) != len(assets) {
		t.Fatalf("metadata packs/assets length mismatch: %d/%d", len(metadata.Packs), len(assets))
	}
	assetDir := filepath.Join(root, "release_assets")
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatalf("mkdir asset dir: %v", err)
	}
	for i, pack := range metadata.Packs {
		if err := os.WriteFile(filepath.Join(assetDir, pack.AssetName), assets[i].bytes, 0o644); err != nil {
			t.Fatalf("write local asset %s: %v", pack.AssetName, err)
		}
	}

	return assetDir
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
