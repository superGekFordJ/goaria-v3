package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	_, err := fetchHTTPSAsset(server.Client(), server.URL+"/fixture.pack.zip")
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

func manifestJSONForPayload(t *testing.T, payload []byte) []byte {
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
