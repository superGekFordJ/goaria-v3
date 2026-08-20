package packbuilder

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packabi"
)

func TestBuildSignedHostCallFixtureDeterministicAndVerifiable(t *testing.T) {
	first, err := BuildSignedHostCallFixture()
	if err != nil {
		t.Fatalf("BuildSignedHostCallFixture() error = %v", err)
	}
	second, err := BuildSignedHostCallFixture()
	if err != nil {
		t.Fatalf("second BuildSignedHostCallFixture() error = %v", err)
	}

	if !bytes.Equal(first.ManifestJSON, second.ManifestJSON) || !bytes.Equal(first.Payload, second.Payload) || !bytes.Equal(first.Signature, second.Signature) || !bytes.Equal(first.PackZip, second.PackZip) {
		t.Fatal("fixture assets are not deterministic")
	}
	if !bytes.Contains(first.Payload, []byte(packabi.HostImportModule)) || !bytes.Contains(first.Payload, []byte(packabi.HostImportHTTPFetch)) {
		t.Fatalf("payload does not contain expected host import strings")
	}
	if !bytes.Contains(first.Payload, []byte(HostCallFixtureBrokerPolicyRef)) || !bytes.Contains(first.Payload, []byte(HostCallFixtureEndpointRef)) {
		t.Fatalf("payload missing ref-mode request fields")
	}
	if bytes.Contains(first.Payload, []byte(HostCallFixtureAPIURL)) {
		t.Fatalf("payload embeds raw API URL %q", HostCallFixtureAPIURL)
	}
	if !bytes.Contains(first.Payload, []byte(HostCallFixtureItemURL)) {
		t.Fatalf("payload missing synthetic output URL")
	}

	policy := extractor.DefaultTrustPolicy()
	policy.TrustedPublicKeys = []ed25519.PublicKey{first.PublicKey}
	verified, err := extractor.VerifyEmbeddedPack(extractor.EmbeddedPack{
		ManifestJSON: first.ManifestJSON,
		Payload:      first.Payload,
		Signature:    first.Signature,
	}, policy)
	if err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	if verified.Manifest.PackID != HostCallFixturePackID || verified.Manifest.PackVersion != HostCallFixtureVersion {
		t.Fatalf("verified fixture identity = %s/%s", verified.Manifest.PackID, verified.Manifest.PackVersion)
	}
	if first.AssetSHA256 != SHA256Hex(first.PackZip) || first.PayloadSHA256 != SHA256Hex(first.Payload) || first.ManifestSHA256 != SHA256Hex(first.ManifestJSON) || first.SignatureSHA256 != SHA256Hex(first.Signature) {
		t.Fatalf("asset hashes do not match bytes")
	}
}

func TestStrictPackZipContainsOnlyRequiredRootEntries(t *testing.T) {
	assets, err := BuildSignedHostCallFixture()
	if err != nil {
		t.Fatalf("BuildSignedHostCallFixture() error = %v", err)
	}
	entries := readZipEntries(t, assets.PackZip)

	wantNames := []string{"manifest.json", "payload.wasm", "manifest.sig"}
	if len(entries) != len(wantNames) {
		t.Fatalf("zip entries = %d, want %d", len(entries), len(wantNames))
	}
	for _, name := range wantNames {
		if _, ok := entries[name]; !ok {
			t.Fatalf("zip missing %s; entries=%v", name, mapKeys(entries))
		}
	}
	if !bytes.Equal(entries["manifest.json"], assets.ManifestJSON) || !bytes.Equal(entries["payload.wasm"], assets.Payload) || !bytes.Equal(entries["manifest.sig"], assets.Signature) {
		t.Fatalf("zip entry bytes do not match assets")
	}
}

func TestFixtureManifestDefaultsArePublicSafe(t *testing.T) {
	assets, err := BuildSignedHostCallFixture()
	if err != nil {
		t.Fatalf("BuildSignedHostCallFixture() error = %v", err)
	}

	manifest := assets.Manifest
	if manifest.PackID != HostCallFixturePackID || manifest.PackVersion != HostCallFixtureVersion || manifest.ABIVersion != packabi.CurrentABIVersion {
		t.Fatalf("manifest identity/abi = %#v", manifest)
	}
	if strings.Join(capabilityStrings(manifest.Capabilities), ",") != "cap.parse.wasm,cap.http.fetch" {
		t.Fatalf("manifest capabilities = %#v", manifest.Capabilities)
	}
	if len(manifest.Domains) != 0 {
		t.Fatalf("manifest domains = %#v", manifest.Domains)
	}
	if strings.Join(manifest.DomainPolicyRefs, ",") != HostCallFixtureDomainPolicyRef || strings.Join(manifest.BrokerPolicyRefs, ",") != HostCallFixtureBrokerPolicyRef {
		t.Fatalf("manifest refs domain=%#v broker=%#v", manifest.DomainPolicyRefs, manifest.BrokerPolicyRefs)
	}
	if err := extractor.ValidateManifest(manifest, extractor.DefaultTrustPolicy()); err != nil {
		t.Fatalf("ValidateManifest() error = %v", err)
	}
	assertNoSecretMarkers(t, assets.ManifestJSON)
	assertNoSecretMarkers(t, assets.Payload)
}

func TestWriteHostCallFixtureWritesAssetsAndLock(t *testing.T) {
	outDir := t.TempDir()
	lockPath := filepath.Join(outDir, "hostcall_fixture.lock.json")
	result, err := WriteHostCallFixture(outDir, lockPath)
	if err != nil {
		t.Fatalf("WriteHostCallFixture() error = %v", err)
	}

	for _, path := range []string{result.ManifestPath, result.PayloadPath, result.SignaturePath, result.PackZipPath, result.LockPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected output %s: %v", path, err)
		}
	}
	if result.Lock.SchemaVersion != LockSchemaVersion || len(result.Lock.Packs) != 1 {
		t.Fatalf("lock = %#v", result.Lock)
	}
	entry := result.Lock.Packs[0]
	if entry.AssetPath != HostCallFixtureAssetName || entry.AssetURL != "" {
		t.Fatalf("lock asset path/url = %q/%q", entry.AssetPath, entry.AssetURL)
	}
	if entry.AssetSHA256 != result.Assets.AssetSHA256 || entry.ManifestSHA256 != result.Assets.ManifestSHA256 || entry.PayloadSHA256 != result.Assets.PayloadSHA256 || entry.SignatureSHA256 != result.Assets.SignatureSHA256 {
		t.Fatalf("lock hashes do not match assets: %#v", entry)
	}
	if len(entry.PublicKeys) != 1 || entry.PublicKeys[0] != hex.EncodeToString(result.Assets.PublicKey) {
		t.Fatalf("lock public keys = %#v", entry.PublicKeys)
	}

	rawLock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	var decoded LockFile
	if err := json.Unmarshal(rawLock, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(lock) error = %v", err)
	}
	assertNoSecretMarkers(t, rawLock)
}

func TestWriteHostCallFixtureRejectsLockOutsideOutDir(t *testing.T) {
	base := t.TempDir()
	outDir := filepath.Join(base, "out")
	if _, err := WriteHostCallFixture(outDir, filepath.Join(base, "outside.lock.json")); err == nil {
		t.Fatal("WriteHostCallFixture() error = nil, want outside-lock rejection")
	}
}

func readZipEntries(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader() error = %v", err)
	}
	entries := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		open, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(open); err != nil {
			_ = open.Close()
			t.Fatalf("read %s: %v", file.Name, err)
		}
		_ = open.Close()
		entries[file.Name] = buf.Bytes()
	}

	return entries
}

func mapKeys(input map[string][]byte) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}

	return keys
}

func capabilityStrings(capabilities []extractor.Capability) []string {
	values := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		values = append(values, string(capability))
	}

	return values
}

func assertNoSecretMarkers(t *testing.T, data []byte) {
	t.Helper()
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"raw-token", "raw_token", "private-key", "private_key", "authorization:", "cookie:", "x-api-key"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("generated fixture contains forbidden marker %q", forbidden)
		}
	}
}
