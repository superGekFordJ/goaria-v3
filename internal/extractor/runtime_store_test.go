package extractor

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeStoreReadIndexSchemaAndValidation(t *testing.T) {
	tempDir := t.TempDir()
	store := newRuntimeStore(tempDir)

	t.Run("missing index returns not exists", func(t *testing.T) {
		sources, exists, err := store.readIndex()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if exists {
			t.Fatal("expected exists = false")
		}
		if sources != nil {
			t.Fatal("expected nil sources")
		}
	})

	t.Run("valid index parses successfully", func(t *testing.T) {
		idx := runtimeIndexFile{
			SchemaVersion: 1,
			Sources: []runtimeIndexSource{
				{
					SourceID:          strings.Repeat("a", 32),
					Kind:              RuntimeSourceKindLocalZip,
					Locator:           filepath.Join(tempDir, "sample.pack.zip"),
					PackID:            "xpk-alpha",
					PackVersion:       "1.0.0",
					SignerFingerprint: strings.Repeat("b", 64),
					CacheGeneration:   strings.Repeat("c", 32),
				},
			},
		}
		raw, err := json.Marshal(idx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.indexPath(), raw, 0o600); err != nil {
			t.Fatal(err)
		}

		sources, exists, err := store.readIndex()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !exists {
			t.Fatal("expected exists = true")
		}
		if len(sources) != 1 {
			t.Fatalf("expected 1 source, got %d", len(sources))
		}
		if sources[0].PackID != "xpk-alpha" {
			t.Fatalf("unexpected pack_id: %s", sources[0].PackID)
		}
	})

	t.Run("rejects schema_version != 1", func(t *testing.T) {
		raw := []byte(`{"schema_version": 2, "sources": []}`)
		_ = os.WriteFile(store.indexPath(), raw, 0o600)
		_, _, err := store.readIndex()
		if err == nil {
			t.Fatal("expected error on schema_version 2")
		}
	})

	t.Run("rejects unknown fields", func(t *testing.T) {
		raw := []byte(`{"schema_version": 1, "sources": [], "unexpected": "value"}`)
		_ = os.WriteFile(store.indexPath(), raw, 0o600)
		_, _, err := store.readIndex()
		if err == nil {
			t.Fatal("expected error on unknown field")
		}
	})

	t.Run("rejects trailing json data", func(t *testing.T) {
		raw := []byte(`{"schema_version": 1, "sources": []} trailing`)
		_ = os.WriteFile(store.indexPath(), raw, 0o600)
		_, _, err := store.readIndex()
		if err == nil {
			t.Fatal("expected error on trailing data")
		}
	})

	t.Run("rejects oversized index", func(t *testing.T) {
		oversized := make([]byte, maxIndexSizeBytes+1)
		_ = os.WriteFile(store.indexPath(), oversized, 0o600)
		_, _, err := store.readIndex()
		if err == nil {
			t.Fatal("expected error on oversized file")
		}
	})

	t.Run("rejects more than 128 sources", func(t *testing.T) {
		var sources []runtimeIndexSource
		for i := range 129 {
			idBytes := make([]byte, 16)
			idBytes[0] = byte(i)
			sources = append(sources, runtimeIndexSource{
				SourceID:          hex.EncodeToString(idBytes),
				Kind:              RuntimeSourceKindLocalZip,
				Locator:           filepath.Join(tempDir, "sample.pack.zip"),
				PackID:            "xpk-test",
				PackVersion:       "1.0.0",
				SignerFingerprint: strings.Repeat("a", 64),
				CacheGeneration:   hex.EncodeToString(idBytes),
			})
		}
		raw, _ := json.Marshal(runtimeIndexFile{SchemaVersion: 1, Sources: sources})
		_ = os.WriteFile(store.indexPath(), raw, 0o600)
		_, _, err := store.readIndex()
		if err == nil {
			t.Fatal("expected error on > 128 sources")
		}
	})

	t.Run("rejects duplicate source_id", func(t *testing.T) {
		sources := []runtimeIndexSource{
			{
				SourceID:          strings.Repeat("a", 32),
				Kind:              RuntimeSourceKindLocalZip,
				Locator:           filepath.Join(tempDir, "1.zip"),
				PackID:            "xpk-1",
				PackVersion:       "1.0.0",
				SignerFingerprint: strings.Repeat("f", 64),
				CacheGeneration:   strings.Repeat("1", 32),
			},
			{
				SourceID:          strings.Repeat("a", 32),
				Kind:              RuntimeSourceKindLocalZip,
				Locator:           filepath.Join(tempDir, "2.zip"),
				PackID:            "xpk-2",
				PackVersion:       "1.0.0",
				SignerFingerprint: strings.Repeat("f", 64),
				CacheGeneration:   strings.Repeat("2", 32),
			},
		}
		raw, _ := json.Marshal(runtimeIndexFile{SchemaVersion: 1, Sources: sources})
		_ = os.WriteFile(store.indexPath(), raw, 0o600)
		_, _, err := store.readIndex()
		if err == nil {
			t.Fatal("expected error on duplicate source_id")
		}
	})

	t.Run("rejects duplicate pack_id", func(t *testing.T) {
		sources := []runtimeIndexSource{
			{
				SourceID:          strings.Repeat("1", 32),
				Kind:              RuntimeSourceKindLocalZip,
				Locator:           filepath.Join(tempDir, "1.zip"),
				PackID:            "xpk-dup",
				PackVersion:       "1.0.0",
				SignerFingerprint: strings.Repeat("f", 64),
				CacheGeneration:   strings.Repeat("1", 32),
			},
			{
				SourceID:          strings.Repeat("2", 32),
				Kind:              RuntimeSourceKindLocalZip,
				Locator:           filepath.Join(tempDir, "2.zip"),
				PackID:            "xpk-dup",
				PackVersion:       "1.0.0",
				SignerFingerprint: strings.Repeat("f", 64),
				CacheGeneration:   strings.Repeat("2", 32),
			},
		}
		raw, _ := json.Marshal(runtimeIndexFile{SchemaVersion: 1, Sources: sources})
		_ = os.WriteFile(store.indexPath(), raw, 0o600)
		_, _, err := store.readIndex()
		if err == nil {
			t.Fatal("expected error on duplicate pack_id")
		}
	})

	t.Run("rejects invalid locators", func(t *testing.T) {
		badLocators := []struct {
			kind RuntimeSourceKind
			loc  string
		}{
			{RuntimeSourceKindLocalZip, "relative/path.zip"},
			{RuntimeSourceKindLocalZip, filepath.Join(tempDir, "sample\x00path.zip")},
			{RuntimeSourceKindLocalZip, filepath.Join(tempDir, "sample\npath.zip")},
			{RuntimeSourceKindRemoteLock, "http://insecure.test/sample.lock.json"},
			{RuntimeSourceKindRemoteLock, "https://user:pass@example.com/sample.lock.json"},
			{RuntimeSourceKindRemoteLock, "https://example.com/sample.lock.json#fragment"},
			{RuntimeSourceKindRemoteLock, "https://example.com/not-a-lock.zip"},
		}

		for _, tc := range badLocators {
			sources := []runtimeIndexSource{
				{
					SourceID:          strings.Repeat("1", 32),
					Kind:              tc.kind,
					Locator:           tc.loc,
					PackID:            "xpk-1",
					PackVersion:       "1.0.0",
					SignerFingerprint: strings.Repeat("f", 64),
					CacheGeneration:   strings.Repeat("1", 32),
				},
			}
			raw, _ := json.Marshal(runtimeIndexFile{SchemaVersion: 1, Sources: sources})
			_ = os.WriteFile(store.indexPath(), raw, 0o600)
			_, _, err := store.readIndex()
			if err == nil {
				t.Fatalf("expected error for invalid locator %q", tc.loc)
			}
		}
	})
}

func TestRuntimeStoreStagingFinalizeAndCleanup(t *testing.T) {
	tempDir := t.TempDir()
	store := newRuntimeStore(tempDir)

	pubKey, privKey := deterministicKeyPair(1)
	payload := validRunnerFixtureWASM()
	rawManifest := mustManifestJSON(t, payload, func(m map[string]any) {
		m["pack_id"] = "xpk-persisted"
		m["pack_version"] = "1.0.0"
	})
	sig := ed25519.Sign(privKey, rawManifest)
	lockRaw := mustRuntimeLockJSON(t, "xpk-persisted", "1.0.0", "sample.pack.zip", []byte("sample-zip-bytes"), rawManifest, payload, sig, pubKey)

	candidateZip := RuntimePackCandidate{
		VerifiedPack: VerifiedPack{
			Manifest: Manifest{
				PackID:         "xpk-persisted",
				PackVersion:    "1.0.0",
				ABIVersion:     CurrentABIVersion,
				PayloadSHA256:  sha256HexString(payload),
				Capabilities:   []Capability{CapabilityParseWASM},
				Domains:        []DomainRule{{Host: "fixture.invalid"}},
				ResourceLimits: DefaultTrustPolicy().MaxResourceLimits,
			},
			Payload: cloneBytes(payload),
		},
		ManifestJSON: rawManifest,
		Signature:    sig,
		LockJSON:     lockRaw,
		ZipBytes:     []byte("sample-zip-bytes"),
	}

	gen1 := strings.Repeat("a", 32)

	t.Run("stage and finalize zip candidate writes 5 files", func(t *testing.T) {
		stagingDir, err := store.writeCandidateToStaging(gen1, candidateZip)
		if err != nil {
			t.Fatalf("writeCandidateToStaging error: %v", err)
		}

		// Verify files exist in staging
		for _, name := range []string{"manifest.json", "payload.wasm", "manifest.sig", "lock.json", "asset.pack.zip"} {
			if _, err := os.Stat(filepath.Join(stagingDir, name)); err != nil {
				t.Fatalf("missing %s in staging: %v", name, err)
			}
		}

		// Finalize
		if err := store.finalizeCandidateGeneration("xpk-persisted", gen1); err != nil {
			t.Fatalf("finalize error: %v", err)
		}

		finalGenDir := store.generationDir("xpk-persisted", gen1)
		for _, name := range []string{"manifest.json", "payload.wasm", "manifest.sig", "lock.json", "asset.pack.zip"} {
			if _, err := os.Stat(filepath.Join(finalGenDir, name)); err != nil {
				t.Fatalf("missing %s in finalized generation: %v", name, err)
			}
		}

		// Read back candidate from cache
		recovered, err := store.readCachedCandidate(context.Background(), "xpk-persisted", gen1, RuntimeSourceKindLocalZip)
		if err != nil {
			t.Fatalf("readCachedCandidate error: %v", err)
		}
		if recovered.VerifiedPack.Manifest.PackID != "xpk-persisted" {
			t.Fatalf("unexpected recovered pack id: %s", recovered.VerifiedPack.Manifest.PackID)
		}
	})

	t.Run("finalize rejects existing generation", func(t *testing.T) {
		// Attempting to finalize gen1 again should error
		_, _ = store.writeCandidateToStaging(gen1, candidateZip)
		err := store.finalizeCandidateGeneration("xpk-persisted", gen1)
		if err == nil {
			t.Fatal("expected error on existing generation")
		}
		_ = os.RemoveAll(filepath.Join(store.stagingDir(), gen1))
	})

	t.Run("cleanup deletes only exact generation directory and preserves parent", func(t *testing.T) {
		packParent := filepath.Join(store.packsDir(), "xpk-persisted")
		if _, err := os.Stat(packParent); err != nil {
			t.Fatalf("pack parent should exist: %v", err)
		}

		if err := store.deleteGeneration("xpk-persisted", gen1); err != nil {
			t.Fatalf("deleteGeneration error: %v", err)
		}

		// gen1 is gone
		if _, err := os.Stat(store.generationDir("xpk-persisted", gen1)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("gen1 should be deleted: %v", err)
		}

		// pack parent still exists!
		if _, err := os.Stat(packParent); err != nil {
			t.Fatalf("pack parent should STILL exist after generation cleanup: %v", err)
		}
	})

	t.Run("atomic replaceIndex writes valid sources.json", func(t *testing.T) {
		sources := []runtimeIndexSource{
			{
				SourceID:          strings.Repeat("1", 32),
				Kind:              RuntimeSourceKindLocalZip,
				Locator:           filepath.Join(tempDir, "1.zip"),
				PackID:            "xpk-1",
				PackVersion:       "1.0.0",
				SignerFingerprint: strings.Repeat("f", 64),
				CacheGeneration:   strings.Repeat("1", 32),
			},
		}

		if err := store.replaceIndex(sources); err != nil {
			t.Fatalf("replaceIndex error: %v", err)
		}

		readBack, exists, err := store.readIndex()
		if err != nil || !exists || len(readBack) != 1 {
			t.Fatalf("readIndex failed after replace: %v, %v, len=%d", err, exists, len(readBack))
		}
		if readBack[0].SourceID != strings.Repeat("1", 32) {
			t.Fatalf("unexpected source id: %s", readBack[0].SourceID)
		}
	})
}

func mustRuntimeLockJSON(t *testing.T, packID, packVersion, assetPath string, zipBytes, manifestJSON, payload, signature []byte, pubKey ed25519.PublicKey) []byte {
	t.Helper()
	assetHash := sha256HexString(zipBytes)
	if len(zipBytes) == 0 {
		assetHash = strings.Repeat("0", 64)
	}
	lock := runtimeLockFile{
		SchemaVersion: 1,
		Packs: []runtimeLockEntry{
			{
				PackID:          packID,
				PackVersion:     packVersion,
				AssetPath:       assetPath,
				AssetSHA256:     assetHash,
				PublicKeys:      []string{hex.EncodeToString(pubKey)},
				ManifestSHA256:  sha256HexString(manifestJSON),
				PayloadSHA256:   sha256HexString(payload),
				SignatureSHA256: sha256HexString(signature),
			},
		},
	}
	raw, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
