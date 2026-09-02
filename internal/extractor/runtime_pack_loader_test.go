package extractor_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/extractor"
	"goaria-v3/internal/extractor/packbuilder"
)

func TestVerifyRuntimePackComponentsValid(t *testing.T) {
	assets, _ := packbuilder.BuildSignedHostCallFixture()
	lock := packbuilder.LockForAssetPath(packbuilder.HostCallFixtureAssetName, assets)
	lockJSON, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := extractor.VerifyRuntimePackComponents(
		context.Background(),
		assets.ManifestJSON,
		assets.Payload,
		assets.Signature,
		lockJSON,
		assets.PackZip,
		true,
	)
	if err != nil {
		t.Fatalf("VerifyRuntimePackComponents() error = %v", err)
	}
	if candidate.VerifiedPack.Manifest.PackID != packbuilder.HostCallFixturePackID {
		t.Fatalf("PackID = %q, want %q", candidate.VerifiedPack.Manifest.PackID, packbuilder.HostCallFixturePackID)
	}
	if !bytes.Equal(candidate.ManifestJSON, assets.ManifestJSON) {
		t.Fatal("ManifestJSON mismatch")
	}
	if !bytes.Equal(candidate.Signature, assets.Signature) {
		t.Fatal("Signature mismatch")
	}
	if !bytes.Equal(candidate.LockJSON, lockJSON) {
		t.Fatal("LockJSON mismatch")
	}
	if !bytes.Equal(candidate.ZipBytes, assets.PackZip) {
		t.Fatal("ZipBytes mismatch")
	}

	// Defensive copy verification: mutating returned candidate slices must not corrupt internal state
	candidate.ManifestJSON[0] = 0xff
	candidate.Signature[0] = 0xff
	candidate.LockJSON[0] = 0xff
	candidate.ZipBytes[0] = 0xff
	candidate.VerifiedPack.Payload[0] = 0xff

	// Re-verify that source assets were not modified
	if assets.ManifestJSON[0] == 0xff || assets.Signature[0] == 0xff || assets.PackZip[0] == 0xff || assets.Payload[0] == 0xff {
		t.Fatal("source fixture was mutated by candidate mutation")
	}
}

func TestVerifyRuntimePackComponentsDirectoryValid(t *testing.T) {
	assets, _ := packbuilder.BuildSignedHostCallFixture()
	lock := packbuilder.LockForAssetPath(packbuilder.HostCallFixtureAssetName, assets)
	lockJSON, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}

	// For directory, isExplicitZip is false and originalZipBytes is nil
	candidate, err := extractor.VerifyRuntimePackComponents(
		context.Background(),
		assets.ManifestJSON,
		assets.Payload,
		assets.Signature,
		lockJSON,
		nil,
		false,
	)
	if err != nil {
		t.Fatalf("VerifyRuntimePackComponents() directory error = %v", err)
	}
	if candidate.ZipBytes != nil {
		t.Fatalf("candidate.ZipBytes = %#v, want nil for directory", candidate.ZipBytes)
	}
	if candidate.VerifiedPack.Identity.AssetSHA256 != assets.AssetSHA256 {
		t.Fatalf("Identity.AssetSHA256 = %s, want lock pin %s", candidate.VerifiedPack.Identity.AssetSHA256, assets.AssetSHA256)
	}
}

func TestVerifyRuntimePackComponentsErrors(t *testing.T) {
	assets, _ := packbuilder.BuildSignedHostCallFixture()
	freshLock := func() packbuilder.LockFile {
		return packbuilder.LockForAssetPath(packbuilder.HostCallFixtureAssetName, assets)
	}
	mustMarshalJSON := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal json: %v", err)
		}
		return b
	}
	validLockJSON := mustMarshalJSON(freshLock())
	badWASMManifest, badWASMPayload, badWASMSig, badWASMLock := buildPreflightFailingComponents()

	tests := []struct {
		name          string
		manifest      []byte
		payload       []byte
		signature     []byte
		lock          []byte
		zipBytes      []byte
		isExplicitZip bool
		wantCode      extractor.RuntimePackLoadErrorCode
	}{
		{
			name:          "unknown field in lock JSON",
			manifest:      assets.ManifestJSON,
			payload:       assets.Payload,
			signature:     assets.Signature,
			lock:          []byte(`{"schema_version":1,"extra":"field","packs":[]}`),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:          "trailing data in lock JSON",
			manifest:      assets.ManifestJSON,
			payload:       assets.Payload,
			signature:     assets.Signature,
			lock:          append(validLockJSON, []byte(` {trailing}`)...),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "schema_version 2 rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.SchemaVersion = 2
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "empty packs in lock",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs = nil
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "multiple packs in lock",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs = append(l.Packs, l.Packs[0])
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "asset_url present in runtime lock v1 rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetURL = "https://example.invalid/pack.zip"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "unsafe asset_path with traversal",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetPath = "../unsafe.pack.zip"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "unsafe asset_path without .pack.zip suffix",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetPath = "asset.tar.gz"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "percent escaped in asset_path rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetPath = "foo%20bar.pack.zip"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "query in asset_path rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetPath = "foo.pack.zip?bar=1"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "fragment in asset_path rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetPath = "foo.pack.zip#frag"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "windows drive path in asset_path rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetPath = "C:\\foo.pack.zip"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "unc path in asset_path rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetPath = "\\\\server\\share\\foo.pack.zip"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "control characters in asset_path rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetPath = "foo\x00bar.pack.zip"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "lock pack_id mismatch with manifest",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PackID = "different-pack-id"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "lock pack_version mismatch with manifest",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PackVersion = "9.9.9"
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "lock public_keys empty",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PublicKeys = nil
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "lock public_keys multiple",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PublicKeys = append(l.Packs[0].PublicKeys, l.Packs[0].PublicKeys[0])
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "uppercase public_keys in lock rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PublicKeys[0] = strings.ToUpper(l.Packs[0].PublicKeys[0])
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "non-hex public_keys in lock rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PublicKeys[0] = strings.Repeat("z", 64)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "wrong length public_keys in lock rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PublicKeys[0] = strings.Repeat("a", 63)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "uppercase manifest_sha256 in lock rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].ManifestSHA256 = strings.ToUpper(l.Packs[0].ManifestSHA256)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "non-hex manifest_sha256 in lock rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].ManifestSHA256 = strings.Repeat("z", 64)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "wrong length manifest_sha256 in lock rejected",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].ManifestSHA256 = strings.Repeat("a", 63)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorLockInvalid,
		},
		{
			name:      "lock manifest_sha256 mismatch",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].ManifestSHA256 = strings.Repeat("0", 64)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorHashMismatch,
		},
		{
			name:      "lock payload_sha256 mismatch",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PayloadSHA256 = strings.Repeat("0", 64)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorHashMismatch,
		},
		{
			name:      "manifest payload pin matches payload but lock payload pin differs",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].PayloadSHA256 = strings.Repeat("1", 64)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorHashMismatch,
		},
		{
			name:      "lock signature_sha256 mismatch",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].SignatureSHA256 = strings.Repeat("0", 64)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorHashMismatch,
		},
		{
			name:      "whole zip asset_sha256 mismatch",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				l.Packs[0].AssetSHA256 = strings.Repeat("0", 64)
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorHashMismatch,
		},
		{
			name:      "signature invalid with wrong public key",
			manifest:  assets.ManifestJSON,
			payload:   assets.Payload,
			signature: assets.Signature,
			lock: func() []byte {
				l := freshLock()
				pub, _, _ := ed25519.GenerateKey(nil)
				l.Packs[0].PublicKeys = []string{hex.EncodeToString(pub)}
				return mustMarshalJSON(l)
			}(),
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorSignatureInvalid,
		},
		{
			name:          "manifest invalid json",
			manifest:      []byte(`{"pack_id":`),
			payload:       assets.Payload,
			signature:     assets.Signature,
			lock:          validLockJSON,
			zipBytes:      assets.PackZip,
			isExplicitZip: true,
			wantCode:      extractor.RuntimePackLoadErrorManifestInvalid,
		},
		{
			name:          "wasm payload fails preflight",
			manifest:      badWASMManifest,
			payload:       badWASMPayload,
			signature:     badWASMSig,
			lock:          badWASMLock,
			zipBytes:      nil,
			isExplicitZip: false,
			wantCode:      extractor.RuntimePackLoadErrorWASMInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := extractor.VerifyRuntimePackComponents(
				context.Background(),
				tc.manifest,
				tc.payload,
				tc.signature,
				tc.lock,
				tc.zipBytes,
				tc.isExplicitZip,
			)
			if err == nil {
				t.Fatalf("VerifyRuntimePackComponents() succeeded, want error code %s", tc.wantCode)
			}
			var loadErr *extractor.RuntimePackLoadError
			if !errors.As(err, &loadErr) {
				t.Fatalf("error is not *RuntimePackLoadError: %v", err)
			}
			if loadErr.Code != tc.wantCode {
				t.Fatalf("loadErr.Code = %s, want %s", loadErr.Code, tc.wantCode)
			}

			// Public Error() string must be redacted and generic
			msg := err.Error()
			if !strings.HasPrefix(msg, "pack load failed: ") {
				t.Fatalf("Error() = %q, want generic prefix", msg)
			}
			for _, leak := range []string{"/", "\\", "http", "pack.zip", "test", "sha256", "fixture"} {
				if strings.Contains(strings.ToLower(msg), leak) {
					t.Fatalf("Error() message leaked sensitive content")
				}
			}
		})
	}
}

func TestLoadLocalPackZip(t *testing.T) {
	outDir := t.TempDir()
	lockPath := filepath.Join(outDir, packbuilder.HostCallFixturePackID+".lock.json")
	writeRes, err := packbuilder.WriteHostCallFixture(outDir, lockPath)
	if err != nil {
		t.Fatalf("WriteHostCallFixture() error = %v", err)
	}

	t.Run("valid local zip and sibling lock", func(t *testing.T) {
		candidate, err := extractor.LoadLocalPackZip(context.Background(), writeRes.PackZipPath)
		if err != nil {
			t.Fatalf("LoadLocalPackZip() error = %v", err)
		}
		if candidate.VerifiedPack.Manifest.PackID != packbuilder.HostCallFixturePackID {
			t.Fatalf("PackID = %q", candidate.VerifiedPack.Manifest.PackID)
		}
		if len(candidate.ZipBytes) == 0 {
			t.Fatal("candidate.ZipBytes is empty for local zip")
		}
		if candidate.VerifiedPack.Identity.AssetSHA256 != writeRes.Assets.AssetSHA256 {
			t.Fatalf("AssetSHA256 = %s, want %s", candidate.VerifiedPack.Identity.AssetSHA256, writeRes.Assets.AssetSHA256)
		}
	})

	t.Run("empty or whitespace zip path", func(t *testing.T) {
		_, errEmpty := extractor.LoadLocalPackZip(context.Background(), "")
		assertErrorCode(t, errEmpty, extractor.RuntimePackLoadErrorSourceUnreadable)

		_, errWS := extractor.LoadLocalPackZip(context.Background(), "   ")
		assertErrorCode(t, errWS, extractor.RuntimePackLoadErrorSourceUnreadable)
	})

	t.Run("missing zip file", func(t *testing.T) {
		_, err := extractor.LoadLocalPackZip(context.Background(), filepath.Join(outDir, "nonexistent.pack.zip"))
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceUnreadable)
	})

	t.Run("missing sibling lock", func(t *testing.T) {
		isolatedDir := t.TempDir()
		zipCopyPath := filepath.Join(isolatedDir, packbuilder.HostCallFixtureAssetName)
		if err := os.WriteFile(zipCopyPath, writeRes.Assets.PackZip, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := extractor.LoadLocalPackZip(context.Background(), zipCopyPath)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorLockMissing)
	})

	t.Run("malformed sibling lock json", func(t *testing.T) {
		isolatedDir := t.TempDir()
		zipCopyPath := filepath.Join(isolatedDir, packbuilder.HostCallFixtureAssetName)
		if err := os.WriteFile(zipCopyPath, writeRes.Assets.PackZip, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(isolatedDir, packbuilder.HostCallFixturePackID+".lock.json"), []byte("{malformed json"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := extractor.LoadLocalPackZip(context.Background(), zipCopyPath)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorLockInvalid)
	})

	t.Run("directory passed as zipPath", func(t *testing.T) {
		_, err := extractor.LoadLocalPackZip(context.Background(), outDir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceShapeInvalid)
	})

	t.Run("symlink zip rejected", func(t *testing.T) {
		symDir := t.TempDir()
		symPath := filepath.Join(symDir, "sym.pack.zip")
		if err := os.Symlink(writeRes.PackZipPath, symPath); err != nil {
			t.Skip("symlinks not supported in environment")
		}
		_, err := extractor.LoadLocalPackZip(context.Background(), symPath)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceShapeInvalid)
	})

	t.Run("lock asset_path leaf mismatch", func(t *testing.T) {
		mismatchDir := t.TempDir()
		renamedZip := filepath.Join(mismatchDir, "other_name.pack.zip")
		if err := os.WriteFile(renamedZip, writeRes.Assets.PackZip, 0o644); err != nil {
			t.Fatal(err)
		}
		lockData, _ := os.ReadFile(lockPath)
		if err := os.WriteFile(filepath.Join(mismatchDir, packbuilder.HostCallFixturePackID+".lock.json"), lockData, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := extractor.LoadLocalPackZip(context.Background(), renamedZip)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorLockInvalid)
	})

	t.Run("lock asset_sha256 mismatch", func(t *testing.T) {
		mismatchDir := t.TempDir()
		zipCopy := filepath.Join(mismatchDir, packbuilder.HostCallFixtureAssetName)
		if err := os.WriteFile(zipCopy, writeRes.Assets.PackZip, 0o644); err != nil {
			t.Fatal(err)
		}
		tamperedLock := writeRes.Lock
		tamperedLock.Packs[0].AssetSHA256 = strings.Repeat("e", 64)
		tamperedJSON, _ := json.Marshal(tamperedLock)
		if err := os.WriteFile(filepath.Join(mismatchDir, packbuilder.HostCallFixturePackID+".lock.json"), tamperedJSON, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := extractor.LoadLocalPackZip(context.Background(), zipCopy)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorHashMismatch)
	})
}

func TestLoadLocalPackDirectory(t *testing.T) {
	outDir := t.TempDir()
	lockPath := filepath.Join(outDir, packbuilder.HostCallFixturePackID+".lock.json")
	writeRes, err := packbuilder.WriteHostCallFixture(outDir, lockPath)
	if err != nil {
		t.Fatalf("WriteHostCallFixture() error = %v", err)
	}
	lockJSON, err := json.Marshal(writeRes.Lock)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("valid local directory ignores extra files and sibling zip", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(outDir, "extra.txt"), []byte("ignore me"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(writeRes.PackZipPath, []byte("corrupted sibling zip bytes"), 0o644); err != nil {
			t.Fatal(err)
		}

		candidate, err := extractor.LoadLocalPackDirectory(context.Background(), outDir)
		if err != nil {
			t.Fatalf("LoadLocalPackDirectory() error = %v", err)
		}
		if candidate.VerifiedPack.Manifest.PackID != packbuilder.HostCallFixturePackID {
			t.Fatalf("PackID = %q", candidate.VerifiedPack.Manifest.PackID)
		}
		if candidate.ZipBytes != nil {
			t.Fatalf("candidate.ZipBytes = %#v, want nil for directory loader", candidate.ZipBytes)
		}
		if candidate.VerifiedPack.Identity.AssetSHA256 != writeRes.Assets.AssetSHA256 {
			t.Fatalf("AssetSHA256 = %s, want %s", candidate.VerifiedPack.Identity.AssetSHA256, writeRes.Assets.AssetSHA256)
		}
	})

	t.Run("sibling zip completely absent succeeds", func(t *testing.T) {
		isolatedDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(isolatedDir, "manifest.json"), writeRes.Assets.ManifestJSON, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(isolatedDir, "payload.wasm"), writeRes.Assets.Payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(isolatedDir, "manifest.sig"), writeRes.Assets.Signature, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(isolatedDir, packbuilder.HostCallFixturePackID+".lock.json"), lockJSON, 0o644); err != nil {
			t.Fatal(err)
		}

		candidate, err := extractor.LoadLocalPackDirectory(context.Background(), isolatedDir)
		if err != nil {
			t.Fatalf("LoadLocalPackDirectory() error = %v", err)
		}
		if candidate.ZipBytes != nil {
			t.Fatalf("ZipBytes must be nil for directory loader, got %#v", candidate.ZipBytes)
		}
	})

	t.Run("empty or whitespace dir path", func(t *testing.T) {
		_, errEmpty := extractor.LoadLocalPackDirectory(context.Background(), "")
		assertErrorCode(t, errEmpty, extractor.RuntimePackLoadErrorSourceUnreadable)

		_, errWS := extractor.LoadLocalPackDirectory(context.Background(), "   ")
		assertErrorCode(t, errWS, extractor.RuntimePackLoadErrorSourceUnreadable)
	})

	t.Run("file passed as dirPath", func(t *testing.T) {
		_, err := extractor.LoadLocalPackDirectory(context.Background(), writeRes.ManifestPath)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceShapeInvalid)
	})

	t.Run("missing manifest.json in directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "payload.wasm"), writeRes.Assets.Payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.sig"), writeRes.Assets.Signature, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := extractor.LoadLocalPackDirectory(context.Background(), dir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceShapeInvalid)
	})

	t.Run("missing lock in directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), writeRes.Assets.ManifestJSON, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload.wasm"), writeRes.Assets.Payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.sig"), writeRes.Assets.Signature, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := extractor.LoadLocalPackDirectory(context.Background(), dir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorLockMissing)
	})

	t.Run("empty component file in directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "payload.wasm"), writeRes.Assets.Payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.sig"), writeRes.Assets.Signature, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := extractor.LoadLocalPackDirectory(context.Background(), dir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceShapeInvalid)
	})

	t.Run("loose payload oversize exceeds 16MB", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), writeRes.Assets.ManifestJSON, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "payload.wasm"), make([]byte, extractor.MaxPackPayloadBytes+1), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "manifest.sig"), writeRes.Assets.Signature, 0o644)
		_ = os.WriteFile(filepath.Join(dir, packbuilder.HostCallFixturePackID+".lock.json"), lockJSON, 0o644)
		_, err := extractor.LoadLocalPackDirectory(context.Background(), dir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceShapeInvalid)
	})

	t.Run("loose signature oversize exceeds 128KB", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), writeRes.Assets.ManifestJSON, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "payload.wasm"), writeRes.Assets.Payload, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "manifest.sig"), make([]byte, extractor.MaxPackSignatureBytes+1), 0o644)
		_ = os.WriteFile(filepath.Join(dir, packbuilder.HostCallFixturePackID+".lock.json"), lockJSON, 0o644)
		_, err := extractor.LoadLocalPackDirectory(context.Background(), dir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceShapeInvalid)
	})

	t.Run("loose component hash tampered", func(t *testing.T) {
		dir := t.TempDir()
		tamperedPayload := append(append([]byte(nil), writeRes.Assets.Payload...), 0x00)
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), writeRes.Assets.ManifestJSON, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "payload.wasm"), tamperedPayload, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "manifest.sig"), writeRes.Assets.Signature, 0o644)
		_ = os.WriteFile(filepath.Join(dir, packbuilder.HostCallFixturePackID+".lock.json"), lockJSON, 0o644)
		_, err := extractor.LoadLocalPackDirectory(context.Background(), dir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorHashMismatch)
	})

	t.Run("loose component signature invalid", func(t *testing.T) {
		dir := t.TempDir()
		tamperedSig := bytes.Repeat([]byte{0x55}, 64)
		tamperedSigSHA := sha256.Sum256(tamperedSig)
		tamperedLock := writeRes.Lock
		tamperedLock.Packs[0].SignatureSHA256 = hex.EncodeToString(tamperedSigSHA[:])
		tamperedLockJSON, _ := json.Marshal(tamperedLock)

		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), writeRes.Assets.ManifestJSON, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "payload.wasm"), writeRes.Assets.Payload, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "manifest.sig"), tamperedSig, 0o644)
		_ = os.WriteFile(filepath.Join(dir, packbuilder.HostCallFixturePackID+".lock.json"), tamperedLockJSON, 0o644)
		_, err := extractor.LoadLocalPackDirectory(context.Background(), dir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSignatureInvalid)
	})

	t.Run("manifest pack_id with path traversal rejected", func(t *testing.T) {
		dir := t.TempDir()
		badManifest := []byte(`{"pack_id":"../bad_path","pack_version":"1.0.0","abi_version":1,"domains":[{"host":"example.invalid"}]}`)
		_ = os.WriteFile(filepath.Join(dir, "manifest.json"), badManifest, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "payload.wasm"), writeRes.Assets.Payload, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "manifest.sig"), writeRes.Assets.Signature, 0o644)
		_ = os.WriteFile(filepath.Join(dir, "test.lock.json"), lockJSON, 0o644)
		_, err := extractor.LoadLocalPackDirectory(context.Background(), dir)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorManifestInvalid)
	})
}

func TestLoadRemotePackLock(t *testing.T) {
	assets, _ := packbuilder.BuildSignedHostCallFixture()
	lock := packbuilder.LockForAssetPath(packbuilder.HostCallFixtureAssetName, assets)
	lockJSON, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("valid remote fetch returns candidate", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/packs/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusOK,
					body:       assets.PackZip,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		candidate, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		if err != nil {
			t.Fatalf("LoadRemotePackLock() error = %v", err)
		}
		if candidate.VerifiedPack.Manifest.PackID != packbuilder.HostCallFixturePackID {
			t.Fatalf("PackID = %q", candidate.VerifiedPack.Manifest.PackID)
		}
		if len(candidate.ZipBytes) == 0 {
			t.Fatal("candidate.ZipBytes is empty for remote lock")
		}
		if candidate.VerifiedPack.Identity.AssetSHA256 != assets.AssetSHA256 {
			t.Fatalf("AssetSHA256 = %s, want %s", candidate.VerifiedPack.Identity.AssetSHA256, assets.AssetSHA256)
		}

		// Verify headers on recorded requests: Accept set, no Referer, Authorization, Cookie
		for i, req := range transport.RecordedRequests() {
			if req.Header.Get("Referer") != "" {
				t.Fatalf("unexpected Referer in request %d", i)
			}
			if req.Header.Get("Authorization") != "" {
				t.Fatalf("unexpected Authorization in request %d", i)
			}
			if req.Header.Get("Cookie") != "" {
				t.Fatalf("unexpected Cookie in request %d", i)
			}
		}
	})

	t.Run("query bearing lock drops query on sibling asset and redacts errors", func(t *testing.T) {
		secretToken := "super_secret_token_12345"
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/packs/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusInternalServerError,
					body:       []byte("internal server error"),
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		lockURL := "https://example.invalid/packs/" + packbuilder.HostCallFixturePackID + ".lock.json?token=" + secretToken
		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), lockURL, client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)

		// Assert asset request had no query
		reqs := transport.RecordedRequests()
		if len(reqs) != 2 {
			t.Fatalf("requests count = %d, want 2", len(reqs))
		}
		if !strings.Contains(reqs[0].URL.RawQuery, "token="+secretToken) {
			t.Fatal("first request did not contain expected query parameter")
		}
		if strings.Contains(reqs[1].URL.RawQuery, "token") || strings.Contains(reqs[1].URL.RawQuery, secretToken) || reqs[1].URL.RawQuery != "" {
			t.Fatal("second request leaked query string or token parameter")
		}

		// Assert error string does not leak the token or url
		errMsg := err.Error()
		if strings.Contains(errMsg, secretToken) || strings.Contains(errMsg, "token") || strings.Contains(errMsg, "example.invalid") {
			t.Fatal("error message leaked secret query parameter or host URL")
		}
	})

	t.Run("redirect strips Referer Authorization and Cookie headers", func(t *testing.T) {
		secretToken := "sensitive_redirect_token_999"
		targetLockPath := "/target/" + packbuilder.HostCallFixturePackID + ".lock.json"
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/source.lock.json": {
					statusCode: http.StatusFound,
					headers: map[string]string{
						"Location": "https://example.invalid" + targetLockPath,
					},
				},
				targetLockPath: {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/target/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusOK,
					body:       assets.PackZip,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		candidate, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/source.lock.json?token="+secretToken, client)
		if err != nil {
			t.Fatalf("LoadRemotePackLock failed on same-origin redirect: %v", err)
		}
		if candidate.VerifiedPack.Manifest.PackID != packbuilder.HostCallFixturePackID {
			t.Fatalf("candidate PackID = %q", candidate.VerifiedPack.Manifest.PackID)
		}

		reqs := transport.RecordedRequests()
		if len(reqs) < 2 {
			t.Fatalf("expected at least 2 requests, got %d", len(reqs))
		}
		for i, req := range reqs {
			if req.Header.Get("Referer") != "" {
				t.Fatalf("Referer header unexpectedly present on request index %d", i)
			}
			if req.Header.Get("Authorization") != "" {
				t.Fatalf("Authorization header unexpectedly present on request index %d", i)
			}
			if req.Header.Get("Cookie") != "" {
				t.Fatalf("Cookie header unexpectedly present on request index %d", i)
			}
		}
	})

	t.Run("same origin port varying redirect succeeds", func(t *testing.T) {
		targetLockPath := "/packs/" + packbuilder.HostCallFixturePackID + ".lock.json"
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/initial.lock.json": {
					statusCode: http.StatusFound,
					headers: map[string]string{
						"Location": "https://example.invalid" + targetLockPath,
					},
				},
				targetLockPath: {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/packs/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusOK,
					body:       assets.PackZip,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		// Initial request has explicit default port :443
		candidate, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid:443/initial.lock.json", client)
		if err != nil {
			t.Fatalf("LoadRemotePackLock failed on port-varying same-origin redirect: %v", err)
		}
		if candidate.VerifiedPack.Manifest.PackID != packbuilder.HostCallFixturePackID {
			t.Fatalf("candidate PackID = %q", candidate.VerifiedPack.Manifest.PackID)
		}
	})

	t.Run("cross origin redirect is rejected", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/initial.lock.json": {
					statusCode: http.StatusFound,
					headers: map[string]string{
						"Location": "https://attacker.invalid/target.lock.json",
					},
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/initial.lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("too many redirects is rejected", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/r0.lock.json": {statusCode: http.StatusFound, headers: map[string]string{"Location": "https://example.invalid/r1.lock.json"}},
				"/r1.lock.json": {statusCode: http.StatusFound, headers: map[string]string{"Location": "https://example.invalid/r2.lock.json"}},
				"/r2.lock.json": {statusCode: http.StatusFound, headers: map[string]string{"Location": "https://example.invalid/r3.lock.json"}},
				"/r3.lock.json": {statusCode: http.StatusFound, headers: map[string]string{"Location": "https://example.invalid/r4.lock.json"}},
				"/r4.lock.json": {statusCode: http.StatusFound, headers: map[string]string{"Location": "https://example.invalid/r5.lock.json"}},
				"/r5.lock.json": {statusCode: http.StatusFound, headers: map[string]string{"Location": "https://example.invalid/r6.lock.json"}},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/r0.lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("unicode asset leaf loads properly", func(t *testing.T) {
		unicodeAssetName := "fixture_测试.pack.zip"
		unicodeLock := packbuilder.LockForAssetPath(unicodeAssetName, assets)
		unicodeLockJSON, err := json.Marshal(unicodeLock)
		if err != nil {
			t.Fatal(err)
		}

		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       unicodeLockJSON,
				},
				"/packs/" + unicodeAssetName: {
					statusCode: http.StatusOK,
					body:       assets.PackZip,
				},
				"/packs/fixture_%E6%B5%8B%E8%AF%95.pack.zip": {
					statusCode: http.StatusOK,
					body:       assets.PackZip,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		candidate, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		if err != nil {
			t.Fatalf("LoadRemotePackLock failed for unicode asset leaf: %v", err)
		}
		if candidate.VerifiedPack.Manifest.PackID != packbuilder.HostCallFixturePackID {
			t.Fatalf("PackID = %q", candidate.VerifiedPack.Manifest.PackID)
		}
	})

	t.Run("oversized lock Content-Length rejected", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					headers:    map[string]string{"Content-Length": "300000"},
					body:       lockJSON,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("oversized lock streaming body rejected", func(t *testing.T) {
		oversizedBody := append(append([]byte(nil), lockJSON...), bytes.Repeat([]byte(" "), 300000)...)
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       oversizedBody,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("oversized asset Content-Length rejected", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/packs/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusOK,
					headers:    map[string]string{"Content-Length": "70000000"},
					body:       assets.PackZip,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("oversized asset streaming body rejected", func(t *testing.T) {
		// Provide an asset stream exceeding 64 MiB (MaxPackAssetBytes) without Content-Length
		overflowStream := io.LimitReader(repeatingByteReader('A'), extractor.MaxPackAssetBytes+1024)
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/packs/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusOK,
					bodyReader: overflowStream,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("total operation timeout establishes 30s deadline shared by lock and asset requests", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/packs/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusOK,
					body:       assets.PackZip,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		start := time.Now()
		candidate, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		if err != nil {
			t.Fatalf("LoadRemotePackLock() unexpected error: %v", err)
		}
		if candidate.VerifiedPack.Manifest.PackID != packbuilder.HostCallFixturePackID {
			t.Fatalf("PackID = %q", candidate.VerifiedPack.Manifest.PackID)
		}

		deadlines := transport.RecordedDeadlines()
		if len(deadlines) != 2 {
			t.Fatalf("expected 2 requests with recorded deadlines, got %d", len(deadlines))
		}
		if deadlines[0].IsZero() || deadlines[1].IsZero() {
			t.Fatal("expected non-zero deadlines on both lock and asset requests")
		}
		if !deadlines[0].Equal(deadlines[1]) {
			t.Fatalf("lock deadline %v and asset deadline %v are not identical", deadlines[0], deadlines[1])
		}
		remaining := time.Until(deadlines[0])
		if remaining < 25*time.Second || remaining > 31*time.Second {
			t.Fatalf("expected ~30s operation timeout, got remaining %v from %v", remaining, start)
		}
	})

	t.Run("context cancellation fails with remote_failed", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := extractor.LoadRemotePackLockWithClientForTest(ctx, "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("context deadline exceeded fails with remote_failed", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Microsecond)
		defer cancel()
		time.Sleep(5 * time.Millisecond)

		_, err := extractor.LoadRemotePackLockWithClientForTest(ctx, "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("truncated zip asset rejected", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/packs/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusOK,
					body:       assets.PackZip[:20], // truncated zip
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorSourceShapeInvalid)
	})

	t.Run("asset redirect to IP literal rejected by check redirect", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
				"/packs/" + packbuilder.HostCallFixtureAssetName: {
					statusCode: http.StatusFound,
					headers: map[string]string{
						"Location": "https://127.0.0.1/pack.zip",
					},
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("redirect target re-resolving to private IP rejected at transport dial layer", func(t *testing.T) {
		ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/initial.lock.json" {
				w.Header().Set("Location", "https://rebound.example.invalid/target.lock.json")
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		resolver := &rebindingTestResolver{}
		var dialCalls int
		dialer := func(ctx context.Context, network string, address string) (net.Conn, error) {
			dialCalls++
			if dialCalls == 1 {
				return net.Dial("tcp", ts.Listener.Addr().String())
			}
			t.Fatal("dialer should not be reached on private IP resolution")
			return nil, errors.New("dial blocked")
		}

		guardedTransport := extractor.NewPrivateIPGuardedTransportForTest(resolver, dialer)
		guardedTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		client := extractor.NewRuntimePackHTTPClientForTest(guardedTransport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://rebound.example.invalid/initial.lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
		if dialCalls != 1 {
			t.Fatalf("expected exactly 1 dial before private IP block, got %d", dialCalls)
		}
	})

	t.Run("URL validation rejections", func(t *testing.T) {
		tests := []struct {
			name     string
			url      string
			wantCode extractor.RuntimePackLoadErrorCode
		}{
			{
				name:     "http scheme rejected",
				url:      "http://example.invalid/pack.lock.json",
				wantCode: extractor.RuntimePackLoadErrorRemoteDenied,
			},
			{
				name:     "userinfo rejected",
				url:      "https://user:pass@example.invalid/pack.lock.json",
				wantCode: extractor.RuntimePackLoadErrorRemoteDenied,
			},
			{
				name:     "fragment rejected",
				url:      "https://example.invalid/pack.lock.json#frag",
				wantCode: extractor.RuntimePackLoadErrorRemoteDenied,
			},
			{
				name:     "ipv4 literal rejected",
				url:      "https://127.0.0.1/pack.lock.json",
				wantCode: extractor.RuntimePackLoadErrorRemoteDenied,
			},
			{
				name:     "ipv6 literal rejected",
				url:      "https://[::1]/pack.lock.json",
				wantCode: extractor.RuntimePackLoadErrorRemoteDenied,
			},
			{
				name:     "non lock.json leaf rejected",
				url:      "https://example.invalid/pack.zip",
				wantCode: extractor.RuntimePackLoadErrorRemoteDenied,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				_, err := extractor.LoadRemotePackLock(context.Background(), tc.url)
				assertErrorCode(t, err, tc.wantCode)
			})
		}
	})

	t.Run("lock pack_id mismatch with leaf", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/wrong_name.lock.json": {
					statusCode: http.StatusOK,
					body:       lockJSON,
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/wrong_name.lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorLockInvalid)
	})

	t.Run("non 2xx lock response", func(t *testing.T) {
		transport := &fakeRemoteTransport{
			responses: map[string]fakeHTTPResponse{
				"/packs/" + packbuilder.HostCallFixturePackID + ".lock.json": {
					statusCode: http.StatusNotFound,
					body:       []byte("not found"),
				},
			},
		}
		client := extractor.NewRuntimePackHTTPClientForTest(transport)

		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://example.invalid/packs/"+packbuilder.HostCallFixturePackID+".lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})

	t.Run("private IP resolution rejected", func(t *testing.T) {
		fakeResolver := testIPResolver{
			ips: map[string][]net.IPAddr{
				"private.example.invalid": {{IP: net.ParseIP("127.0.0.1")}},
			},
		}
		guardedTransport := extractor.NewPrivateIPGuardedTransportForTest(fakeResolver, func(ctx context.Context, network string, address string) (net.Conn, error) {
			return nil, errors.New("dial should not be reached")
		})
		client := extractor.NewRuntimePackHTTPClientForTest(guardedTransport)
		_, err := extractor.LoadRemotePackLockWithClientForTest(context.Background(), "https://private.example.invalid/packs/test.lock.json", client)
		assertErrorCode(t, err, extractor.RuntimePackLoadErrorRemoteFailed)
	})
}

type rebindingTestResolver struct {
	mu    sync.Mutex
	calls int
}

func (r *rebindingTestResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == 1 {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
}

type testIPResolver struct {
	ips map[string][]net.IPAddr
}

func (r testIPResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if addrs, ok := r.ips[host]; ok {
		return addrs, nil
	}

	return nil, fmt.Errorf("unknown host: %s", host)
}

type fakeRemoteTransport struct {
	mu        sync.Mutex
	responses map[string]fakeHTTPResponse
	requests  []*http.Request
	deadlines []time.Time
}

type fakeHTTPResponse struct {
	statusCode    int
	headers       map[string]string
	body          []byte
	bodyReader    io.Reader
	hasExplicitCL bool
	contentLength int64
	err           error
}

func (t *fakeRemoteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}

	t.mu.Lock()
	t.requests = append(t.requests, req.Clone(req.Context()))
	if dl, ok := req.Context().Deadline(); ok {
		t.deadlines = append(t.deadlines, dl)
	} else {
		t.deadlines = append(t.deadlines, time.Time{})
	}
	t.mu.Unlock()

	resp, ok := t.responses[req.URL.Path]
	if !ok {
		resp, ok = t.responses[req.URL.String()]
	}
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("not found in fake")),
			Request:    req,
		}, nil
	}
	if resp.err != nil {
		return nil, resp.err
	}

	header := make(http.Header)
	for k, v := range resp.headers {
		header.Set(k, v)
	}

	var bodyRC io.ReadCloser
	contentLen := int64(-1)
	if resp.bodyReader != nil {
		bodyRC = io.NopCloser(resp.bodyReader)
		if resp.hasExplicitCL {
			contentLen = resp.contentLength
		}
	} else {
		bodyRC = io.NopCloser(bytes.NewReader(resp.body))
		contentLen = int64(len(resp.body))
		if clStr, ok := resp.headers["Content-Length"]; ok {
			if cl, err := strconv.ParseInt(clStr, 10, 64); err == nil {
				contentLen = cl
			}
		}
	}

	return &http.Response{
		StatusCode:    resp.statusCode,
		Header:        header,
		Body:          bodyRC,
		ContentLength: contentLen,
		Request:       req,
	}, nil
}

func (t *fakeRemoteTransport) RecordedRequests() []*http.Request {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*http.Request(nil), t.requests...)
}

func (t *fakeRemoteTransport) RecordedDeadlines() []time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]time.Time(nil), t.deadlines...)
}

func assertErrorCode(t *testing.T, err error, wantCode extractor.RuntimePackLoadErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s, got nil", wantCode)
	}
	var loadErr *extractor.RuntimePackLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("error is not *RuntimePackLoadError: %v", err)
	}
	if loadErr.Code != wantCode {
		t.Fatalf("loadErr.Code = %s, want %s (err = %v)", loadErr.Code, wantCode, err)
	}
}

type repeatingByteReader byte

func (r repeatingByteReader) Read(p []byte) (n int, err error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func buildPreflightFailingComponents() ([]byte, []byte, []byte, []byte) {
	badPayload := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0xff, 0xff}
	manifest := packbuilder.HostCallFixtureManifest(badPayload)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		panic(err)
	}
	publicKey, privateKey := packbuilder.DeterministicFixtureKeyPair()
	sig := ed25519.Sign(privateKey, manifestJSON)
	assets := packbuilder.SignedPackAssets{
		Manifest:        manifest,
		ManifestJSON:    manifestJSON,
		Payload:         badPayload,
		Signature:       sig,
		PublicKey:       publicKey,
		AssetSHA256:     strings.Repeat("0", 64),
		ManifestSHA256:  packbuilder.SHA256Hex(manifestJSON),
		PayloadSHA256:   packbuilder.SHA256Hex(badPayload),
		SignatureSHA256: packbuilder.SHA256Hex(sig),
	}
	lock := packbuilder.LockForAssetPath(packbuilder.HostCallFixtureAssetName, assets)
	lockJSON, err := json.Marshal(lock)
	if err != nil {
		panic(err)
	}
	return manifestJSON, badPayload, sig, lockJSON
}
