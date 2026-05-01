package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

func TestHostcallFixtureCommandWritesVerifiableOutputs(t *testing.T) {
	outDir := t.TempDir()
	lockPath := filepath.Join(outDir, "hostcall_fixture.lock.json")
	var stdout bytes.Buffer

	if err := run([]string{"hostcall-fixture", "--out-dir", outDir, "--lock-out", lockPath}, &stdout); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	stdoutText := stdout.String()
	if !strings.Contains(stdoutText, "hostcall-fixture.pack.zip") || !strings.Contains(stdoutText, "asset_sha256:") {
		t.Fatalf("stdout = %q, missing output summary", stdoutText)
	}
	for _, forbidden := range []string{"private", "seed", "secret", "raw-token", "manifest.sig:"} {
		if strings.Contains(strings.ToLower(stdoutText), forbidden) {
			t.Fatalf("stdout leaked forbidden marker %q: %s", forbidden, stdoutText)
		}
	}

	manifestJSON := readFile(t, filepath.Join(outDir, "manifest.json"))
	payload := readFile(t, filepath.Join(outDir, "payload.wasm"))
	signature := readFile(t, filepath.Join(outDir, "manifest.sig"))
	zipBytes := readFile(t, filepath.Join(outDir, packbuilder.HostCallFixtureAssetName))
	lockJSON := readFile(t, lockPath)

	entries := zipEntryBytes(t, zipBytes)
	if len(entries) != 3 || !bytes.Equal(entries["manifest.json"], manifestJSON) || !bytes.Equal(entries["payload.wasm"], payload) || !bytes.Equal(entries["manifest.sig"], signature) {
		t.Fatalf("zip entries do not exactly match output files")
	}

	var manifest extractor.Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatalf("json.Unmarshal(manifest) error = %v", err)
	}
	if manifest.PayloadSHA256 != packbuilder.SHA256Hex(payload) {
		t.Fatalf("manifest payload hash = %s, want %s", manifest.PayloadSHA256, packbuilder.SHA256Hex(payload))
	}

	var lock packbuilder.LockFile
	if err := json.Unmarshal(lockJSON, &lock); err != nil {
		t.Fatalf("json.Unmarshal(lock) error = %v", err)
	}
	if lock.SchemaVersion != packbuilder.LockSchemaVersion || len(lock.Packs) != 1 {
		t.Fatalf("lock = %#v", lock)
	}
	entry := lock.Packs[0]
	if entry.PackID != packbuilder.HostCallFixturePackID || entry.AssetPath != packbuilder.HostCallFixtureAssetName || entry.AssetSHA256 != packbuilder.SHA256Hex(zipBytes) {
		t.Fatalf("lock entry = %#v", entry)
	}
	if entry.ManifestSHA256 != packbuilder.SHA256Hex(manifestJSON) || entry.PayloadSHA256 != packbuilder.SHA256Hex(payload) || entry.SignatureSHA256 != packbuilder.SHA256Hex(signature) {
		t.Fatalf("lock entry hashes = %#v", entry)
	}
	publicKey, _ := packbuilder.DeterministicFixtureKeyPair()
	if len(entry.PublicKeys) != 1 || entry.PublicKeys[0] != strings.ToLower(strings.TrimSpace(stdoutPublicKey(stdoutText))) {
		t.Fatalf("lock public keys = %#v stdout=%q", entry.PublicKeys, stdoutText)
	}
	if !ed25519.Verify(publicKey, manifestJSON, signature) {
		t.Fatal("manifest signature did not verify with fixture public key")
	}

	policy := extractor.DefaultTrustPolicy()
	policy.TrustedPublicKeys = []ed25519.PublicKey{publicKey}
	if _, err := extractor.VerifyEmbeddedPack(extractor.EmbeddedPack{ManifestJSON: manifestJSON, Payload: payload, Signature: signature}, policy); err != nil {
		t.Fatalf("VerifyEmbeddedPack() error = %v", err)
	}
	for _, generated := range [][]byte{manifestJSON, payload, lockJSON} {
		assertNoToolSecretMarkers(t, generated)
	}
}

func TestHostcallFixtureCommandRejectsUnknownCommand(t *testing.T) {
	if err := run([]string{"unknown"}, ioDiscard{}); err == nil {
		t.Fatal("run() error = nil, want unknown-command error")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return data
}

func zipEntryBytes(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader() error = %v", err)
	}
	entries := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		reader, err := file.Open()
		if err != nil {
			t.Fatalf("open zip entry %s: %v", file.Name, err)
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(reader); err != nil {
			_ = reader.Close()
			t.Fatalf("read zip entry %s: %v", file.Name, err)
		}
		_ = reader.Close()
		entries[file.Name] = buf.Bytes()
	}

	return entries
}

func stdoutPublicKey(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "public_key: ") {
			return strings.TrimPrefix(line, "public_key: ")
		}
	}

	return ""
}

func assertNoToolSecretMarkers(t *testing.T, data []byte) {
	t.Helper()
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"raw-token", "private-key", "private_key", "seed", "authorization:", "cookie:", "x-api-key", "fixturepack", "imagefixture"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("generated output contains forbidden marker %q", forbidden)
		}
	}
}
