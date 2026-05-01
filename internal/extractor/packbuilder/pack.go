package packbuilder

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goaria-v3/internal/extractor"
)

const LockSchemaVersion = 1

type SignedPackAssets struct {
	Manifest        extractor.Manifest
	ManifestJSON    []byte
	Payload         []byte
	Signature       []byte
	PackZip         []byte
	PublicKey       ed25519.PublicKey
	AssetSHA256     string
	ManifestSHA256  string
	PayloadSHA256   string
	SignatureSHA256 string
}

type LockFile struct {
	SchemaVersion int         `json:"schema_version"`
	Packs         []LockEntry `json:"packs"`
}

type LockEntry struct {
	PackID          string   `json:"pack_id"`
	PackVersion     string   `json:"pack_version"`
	AssetURL        string   `json:"asset_url,omitempty"`
	AssetPath       string   `json:"asset_path,omitempty"`
	AssetSHA256     string   `json:"asset_sha256"`
	PublicKeys      []string `json:"public_keys"`
	ManifestSHA256  string   `json:"manifest_sha256,omitempty"`
	PayloadSHA256   string   `json:"payload_sha256,omitempty"`
	SignatureSHA256 string   `json:"signature_sha256,omitempty"`
}

type WriteResult struct {
	Assets        SignedPackAssets
	OutDir        string
	LockPath      string
	ManifestPath  string
	PayloadPath   string
	SignaturePath string
	PackZipPath   string
	Lock          LockFile
}

func BuildSignedHostCallFixture() (SignedPackAssets, error) {
	payload := BuildHostCallFixturePayload()
	manifest := HostCallFixtureManifest(payload)
	manifestJSON, err := marshalDeterministicJSON(manifest)
	if err != nil {
		return SignedPackAssets{}, fmt.Errorf("encode manifest: %w", err)
	}
	publicKey, privateKey := DeterministicFixtureKeyPair()
	signature := ed25519.Sign(privateKey, manifestJSON)
	packZip, err := StrictPackZip(manifestJSON, payload, signature)
	if err != nil {
		return SignedPackAssets{}, err
	}

	assets := SignedPackAssets{
		Manifest:        manifest,
		ManifestJSON:    manifestJSON,
		Payload:         payload,
		Signature:       signature,
		PackZip:         packZip,
		PublicKey:       publicKey,
		AssetSHA256:     SHA256Hex(packZip),
		ManifestSHA256:  SHA256Hex(manifestJSON),
		PayloadSHA256:   SHA256Hex(payload),
		SignatureSHA256: SHA256Hex(signature),
	}
	if err := VerifySignedAssets(assets); err != nil {
		return SignedPackAssets{}, err
	}

	return assets, nil
}

func DeterministicFixtureKeyPair() (ed25519.PublicKey, ed25519.PrivateKey) {
	seedHash := sha256.Sum256([]byte("goaria hostcall fixture deterministic signing seed v1"))
	privateKey := ed25519.NewKeyFromSeed(seedHash[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)

	return publicKey, privateKey
}

func StrictPackZip(manifestJSON []byte, payload []byte, signature []byte) ([]byte, error) {
	entries := []struct {
		name string
		data []byte
	}{
		{name: "manifest.json", data: manifestJSON},
		{name: "payload.wasm", data: payload},
		{name: "manifest.sig", data: signature},
	}

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	fixedTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, entry := range entries {
		if len(entry.data) == 0 {
			_ = writer.Close()
			return nil, fmt.Errorf("zip entry %s is empty", entry.name)
		}
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetMode(0o644)
		header.Modified = fixedTime
		file, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("create zip entry %s: %w", entry.name, err)
		}
		if _, err := file.Write(entry.data); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("write zip entry %s: %w", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}

	return buffer.Bytes(), nil
}

func VerifySignedAssets(assets SignedPackAssets) error {
	if assets.Manifest.PayloadSHA256 != assets.PayloadSHA256 {
		return fmt.Errorf("manifest payload_sha256 = %q, want %q", assets.Manifest.PayloadSHA256, assets.PayloadSHA256)
	}
	policy := extractor.DefaultTrustPolicy()
	policy.TrustedPublicKeys = []ed25519.PublicKey{assets.PublicKey}
	verified, err := extractor.VerifyEmbeddedPack(extractor.EmbeddedPack{
		ManifestJSON: assets.ManifestJSON,
		Payload:      assets.Payload,
		Signature:    assets.Signature,
	}, policy)
	if err != nil {
		return fmt.Errorf("verify signed fixture: %w", err)
	}
	if verified.Manifest.PackID != HostCallFixturePackID || verified.Manifest.PackVersion != HostCallFixtureVersion {
		return fmt.Errorf("verified fixture identity = %s/%s", verified.Manifest.PackID, verified.Manifest.PackVersion)
	}

	return nil
}

func WriteHostCallFixture(outDir string, lockOut string) (WriteResult, error) {
	outDir = strings.TrimSpace(outDir)
	lockOut = strings.TrimSpace(lockOut)
	if outDir == "" {
		return WriteResult{}, errors.New("out dir must be non-empty")
	}
	if lockOut == "" {
		return WriteResult{}, errors.New("lock out must be non-empty")
	}

	absOutDir, err := filepath.Abs(filepath.Clean(outDir))
	if err != nil {
		return WriteResult{}, fmt.Errorf("resolve out dir: %w", err)
	}
	absLockOut, err := filepath.Abs(filepath.Clean(lockOut))
	if err != nil {
		return WriteResult{}, fmt.Errorf("resolve lock out: %w", err)
	}
	if !pathWithin(absOutDir, absLockOut) {
		return WriteResult{}, errors.New("lock out must be inside out dir")
	}

	assets, err := BuildSignedHostCallFixture()
	if err != nil {
		return WriteResult{}, err
	}
	packZipPath := filepath.Join(absOutDir, HostCallFixtureAssetName)
	lock := LockForAssetPath(relativeAssetPath(filepath.Dir(absLockOut), packZipPath), assets)
	lockJSON, err := marshalDeterministicJSON(lock)
	if err != nil {
		return WriteResult{}, fmt.Errorf("encode lock: %w", err)
	}

	paths := map[string][]byte{
		filepath.Join(absOutDir, "manifest.json"): assets.ManifestJSON,
		filepath.Join(absOutDir, "payload.wasm"):  assets.Payload,
		filepath.Join(absOutDir, "manifest.sig"):  assets.Signature,
		packZipPath:                               assets.PackZip,
		absLockOut:                                lockJSON,
	}
	for filePath := range paths {
		if !pathWithin(absOutDir, filePath) {
			return WriteResult{}, fmt.Errorf("refusing to write outside out dir: %s", filePath)
		}
	}
	if err := os.MkdirAll(absOutDir, 0o755); err != nil {
		return WriteResult{}, fmt.Errorf("create out dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absLockOut), 0o755); err != nil {
		return WriteResult{}, fmt.Errorf("create lock dir: %w", err)
	}
	for filePath, data := range paths {
		if err := os.WriteFile(filePath, data, 0o644); err != nil {
			return WriteResult{}, fmt.Errorf("write %s: %w", filepath.Base(filePath), err)
		}
	}

	return WriteResult{
		Assets:        assets,
		OutDir:        absOutDir,
		LockPath:      absLockOut,
		ManifestPath:  filepath.Join(absOutDir, "manifest.json"),
		PayloadPath:   filepath.Join(absOutDir, "payload.wasm"),
		SignaturePath: filepath.Join(absOutDir, "manifest.sig"),
		PackZipPath:   packZipPath,
		Lock:          lock,
	}, nil
}

func LockForAssetPath(assetPath string, assets SignedPackAssets) LockFile {
	return LockFile{
		SchemaVersion: LockSchemaVersion,
		Packs: []LockEntry{{
			PackID:          assets.Manifest.PackID,
			PackVersion:     assets.Manifest.PackVersion,
			AssetPath:       filepath.ToSlash(assetPath),
			AssetSHA256:     assets.AssetSHA256,
			PublicKeys:      []string{hex.EncodeToString(assets.PublicKey)},
			ManifestSHA256:  assets.ManifestSHA256,
			PayloadSHA256:   assets.PayloadSHA256,
			SignatureSHA256: assets.SignatureSHA256,
		}},
	}
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func marshalDeterministicJSON(value any) ([]byte, error) {
	bytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(bytes, '\n'), nil
}

func relativeAssetPath(lockDir string, assetPath string) string {
	rel, err := filepath.Rel(lockDir, assetPath)
	if err != nil || rel == "" || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return assetPath
	}

	return rel
}

func pathWithin(base string, child string) bool {
	rel, err := filepath.Rel(base, child)
	if err != nil {
		return false
	}
	return rel == "." || rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
