package extractor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	maxIndexSizeBytes = 1024 * 1024 // 1 MiB
	maxSourceCount    = 128
	maxLocatorBytes   = 8192
)

// Test hooks for deterministic failure simulation in tests
var (
	testHookStagingWriteFail    func() error
	testHookStagingSyncFail     func() error
	testHookFinalizeRenameFail  func() error
	testHookIndexReplaceFail    func() error
	testHookCleanupFail         func() error
	testHookIndexTempCreateFail func() error
)

type runtimeIndexSource struct {
	SourceID          string            `json:"source_id"`
	Kind              RuntimeSourceKind `json:"kind"`
	Locator           string            `json:"locator"`
	PackID            string            `json:"pack_id"`
	PackVersion       string            `json:"pack_version"`
	SignerFingerprint string            `json:"signer_fingerprint"`
	CacheGeneration   string            `json:"cache_generation"`
}

type runtimeIndexFile struct {
	SchemaVersion int                  `json:"schema_version"`
	Sources       []runtimeIndexSource `json:"sources"`
}

type runtimeStore struct {
	dataRoot string
}

func newRuntimeStore(dataRoot string) *runtimeStore {
	return &runtimeStore{dataRoot: dataRoot}
}

func (s *runtimeStore) indexPath() string {
	return filepath.Join(s.dataRoot, "sources.json")
}

func (s *runtimeStore) packsDir() string {
	return filepath.Join(s.dataRoot, "packs")
}

func (s *runtimeStore) stagingDir() string {
	return filepath.Join(s.dataRoot, "staging")
}

func (s *runtimeStore) generationDir(packID, gen string) string {
	return filepath.Join(s.packsDir(), packID, gen)
}

func (s *runtimeStore) readIndex() ([]runtimeIndexSource, bool, error) {
	path := s.indexPath()
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("stat sources.json: %w", err)
	}

	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("sources.json is not a regular file")
	}
	if info.Size() > maxIndexSizeBytes {
		return nil, false, fmt.Errorf("sources.json size %d exceeds limit %d", info.Size(), maxIndexSizeBytes)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, fmt.Errorf("read sources.json: %w", err)
	}

	if int64(len(raw)) > maxIndexSizeBytes {
		return nil, false, fmt.Errorf("sources.json bytes %d exceeds limit %d", len(raw), maxIndexSizeBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var indexFile runtimeIndexFile
	if err := dec.Decode(&indexFile); err != nil {
		return nil, false, fmt.Errorf("decode sources.json: %w", err)
	}

	var trailing struct{}
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, false, errors.New("sources.json contains trailing data")
	}

	if indexFile.SchemaVersion != 1 {
		return nil, false, fmt.Errorf("sources.json schema_version %d != 1", indexFile.SchemaVersion)
	}

	if len(indexFile.Sources) > maxSourceCount {
		return nil, false, fmt.Errorf("sources count %d exceeds maximum limit %d", len(indexFile.Sources), maxSourceCount)
	}

	seenSourceIDs := make(map[string]struct{}, len(indexFile.Sources))
	seenPackIDs := make(map[string]struct{}, len(indexFile.Sources))
	type packGenKey struct {
		packID string
		gen    string
	}
	seenPackGens := make(map[packGenKey]struct{}, len(indexFile.Sources))

	validatedSources := make([]runtimeIndexSource, 0, len(indexFile.Sources))
	for _, row := range indexFile.Sources {
		if err := validateLowerHex32Field("source_id", row.SourceID); err != nil {
			return nil, false, err
		}
		if _, exists := seenSourceIDs[row.SourceID]; exists {
			return nil, false, fmt.Errorf("duplicate source_id: %s", row.SourceID)
		}
		seenSourceIDs[row.SourceID] = struct{}{}

		if err := validateLowerHex32Field("cache_generation", row.CacheGeneration); err != nil {
			return nil, false, err
		}

		if err := validateLowerHexSHA256Field("signer_fingerprint", row.SignerFingerprint); err != nil {
			return nil, false, err
		}

		if err := validatePackID(row.PackID); err != nil {
			return nil, false, fmt.Errorf("invalid pack_id %q: %w", row.PackID, err)
		}
		if _, exists := seenPackIDs[row.PackID]; exists {
			return nil, false, fmt.Errorf("duplicate pack_id: %s", row.PackID)
		}
		seenPackIDs[row.PackID] = struct{}{}

		pgKey := packGenKey{packID: row.PackID, gen: row.CacheGeneration}
		if _, exists := seenPackGens[pgKey]; exists {
			return nil, false, fmt.Errorf("duplicate (pack_id, cache_generation): (%s, %s)", row.PackID, row.CacheGeneration)
		}
		seenPackGens[pgKey] = struct{}{}

		if err := validatePackVersion(row.PackVersion); err != nil {
			return nil, false, fmt.Errorf("invalid pack_version %q: %w", row.PackVersion, err)
		}

		switch row.Kind {
		case RuntimeSourceKindLocalZip, RuntimeSourceKindLocalDirectory, RuntimeSourceKindRemoteLock:
		default:
			return nil, false, fmt.Errorf("invalid source kind: %s", row.Kind)
		}

		if err := validateDurableLocator(row.Kind, row.Locator); err != nil {
			return nil, false, fmt.Errorf("invalid locator: %w", err)
		}

		validatedSources = append(validatedSources, row)
	}

	return validatedSources, true, nil
}

func validateDurableLocator(kind RuntimeSourceKind, locator string) error {
	if locator == "" {
		return errors.New("locator is empty")
	}
	if len(locator) > maxLocatorBytes {
		return fmt.Errorf("locator exceeds %d bytes", maxLocatorBytes)
	}
	if strings.ContainsRune(locator, '\x00') {
		return errors.New("locator contains NUL character")
	}
	for _, r := range locator {
		if unicode.IsControl(r) {
			return errors.New("locator contains control characters")
		}
	}

	switch kind {
	case RuntimeSourceKindLocalZip, RuntimeSourceKindLocalDirectory:
		if !filepath.IsAbs(locator) {
			return errors.New("local locator must be an absolute path")
		}
		if filepath.Clean(locator) != locator {
			return errors.New("local locator is not clean")
		}
	case RuntimeSourceKindRemoteLock:
		if _, err := validateRemoteLockURL(locator); err != nil {
			return fmt.Errorf("invalid remote lock URL: %w", err)
		}
	default:
		return fmt.Errorf("unknown kind: %s", kind)
	}

	return nil
}

func validateLowerHex32Field(fieldName, value string) error {
	if len(value) != 32 {
		return fmt.Errorf("%s must be 32 lowercase hex characters, got length %d", fieldName, len(value))
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return fmt.Errorf("%s must be lowercase hex characters", fieldName)
		}
	}
	return nil
}

func generateRandomHex(byteCount int) (string, error) {
	b := make([]byte, byteCount)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *runtimeStore) writeCandidateToStaging(gen string, candidate RuntimePackCandidate) (string, error) {
	if testHookStagingWriteFail != nil {
		if err := testHookStagingWriteFail(); err != nil {
			return "", err
		}
	}

	stagingGenDir := filepath.Join(s.stagingDir(), gen)
	if err := os.MkdirAll(stagingGenDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir staging: %w", err)
	}

	files := []struct {
		name string
		data []byte
	}{
		{"manifest.json", candidate.ManifestJSON},
		{"payload.wasm", candidate.VerifiedPack.Payload},
		{"manifest.sig", candidate.Signature},
		{"lock.json", candidate.LockJSON},
	}

	if len(candidate.ZipBytes) > 0 {
		files = append(files, struct {
			name string
			data []byte
		}{"asset.pack.zip", candidate.ZipBytes})
	}

	for _, f := range files {
		filePath := filepath.Join(stagingGenDir, f.name)
		handle, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = os.RemoveAll(stagingGenDir)
			return "", fmt.Errorf("create %s: %w", f.name, err)
		}
		if _, err := handle.Write(f.data); err != nil {
			_ = handle.Close()
			_ = os.RemoveAll(stagingGenDir)
			return "", fmt.Errorf("write %s: %w", f.name, err)
		}
		if testHookStagingSyncFail != nil {
			if hookErr := testHookStagingSyncFail(); hookErr != nil {
				_ = handle.Close()
				_ = os.RemoveAll(stagingGenDir)
				return "", hookErr
			}
		}
		if err := handle.Sync(); err != nil {
			_ = handle.Close()
			_ = os.RemoveAll(stagingGenDir)
			return "", fmt.Errorf("sync %s: %w", f.name, err)
		}
		if err := handle.Close(); err != nil {
			_ = os.RemoveAll(stagingGenDir)
			return "", fmt.Errorf("close %s: %w", f.name, err)
		}
	}

	return stagingGenDir, nil
}

func (s *runtimeStore) finalizeCandidateGeneration(packID, gen string) error {
	if testHookFinalizeRenameFail != nil {
		if err := testHookFinalizeRenameFail(); err != nil {
			return err
		}
	}

	stagingGenDir := filepath.Join(s.stagingDir(), gen)
	targetPackDir := filepath.Join(s.packsDir(), packID)
	if err := os.MkdirAll(targetPackDir, 0o700); err != nil {
		return fmt.Errorf("mkdir pack dir: %w", err)
	}

	targetGenDir := filepath.Join(targetPackDir, gen)
	if _, err := os.Lstat(targetGenDir); err == nil {
		return fmt.Errorf("target generation %s already exists", gen)
	}

	if err := os.Rename(stagingGenDir, targetGenDir); err != nil {
		return fmt.Errorf("finalize generation: %w", err)
	}

	return nil
}

func (s *runtimeStore) replaceIndex(sources []runtimeIndexSource) error {
	if testHookIndexReplaceFail != nil {
		if err := testHookIndexReplaceFail(); err != nil {
			return err
		}
	}

	indexFile := runtimeIndexFile{
		SchemaVersion: 1,
		Sources:       sources,
	}

	data, err := json.MarshalIndent(indexFile, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal index: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(s.dataRoot, 0o700); err != nil {
		return fmt.Errorf("mkdir data root: %w", err)
	}

	if testHookIndexTempCreateFail != nil {
		if err := testHookIndexTempCreateFail(); err != nil {
			return err
		}
	}

	tmpFile, err := os.CreateTemp(s.dataRoot, "sources.json.*.tmp")
	if err != nil {
		return fmt.Errorf("create temp index: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod temp index: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temp index: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync temp index: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp index: %w", err)
	}

	finalPath := s.indexPath()
	if err := os.Rename(tmpName, finalPath); err != nil {
		return fmt.Errorf("replace index: %w", err)
	}

	_ = os.Chmod(finalPath, 0o600)
	return nil
}

func (s *runtimeStore) deleteGeneration(packID, gen string) error {
	if testHookCleanupFail != nil {
		if err := testHookCleanupFail(); err != nil {
			return err
		}
	}
	targetDir := s.generationDir(packID, gen)
	return os.RemoveAll(targetDir)
}

func (s *runtimeStore) readCachedCandidate(ctx context.Context, packID, gen string, kind RuntimeSourceKind) (RuntimePackCandidate, error) {
	genDir := s.generationDir(packID, gen)
	info, err := os.Lstat(genDir)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, errors.New("generation is not a directory"))
	}

	manifestBytes, err := readStrictCacheFile(genDir, "manifest.json", MaxPackManifestBytes)
	if err != nil {
		return RuntimePackCandidate{}, err
	}

	payloadBytes, err := readStrictCacheFile(genDir, "payload.wasm", MaxPackPayloadBytes)
	if err != nil {
		return RuntimePackCandidate{}, err
	}

	sigBytes, err := readStrictCacheFile(genDir, "manifest.sig", MaxPackSignatureBytes)
	if err != nil {
		return RuntimePackCandidate{}, err
	}

	lockBytes, err := readStrictCacheFile(genDir, "lock.json", MaxPackManifestBytes)
	if err != nil {
		return RuntimePackCandidate{}, err
	}

	var zipBytes []byte
	isZip := false
	if kind == RuntimeSourceKindLocalZip || kind == RuntimeSourceKindRemoteLock {
		isZip = true
		zipBytes, err = readStrictCacheFile(genDir, "asset.pack.zip", MaxPackAssetBytes)
		if err != nil {
			return RuntimePackCandidate{}, err
		}
	}

	return VerifyRuntimePackComponents(ctx, manifestBytes, payloadBytes, sigBytes, lockBytes, zipBytes, isZip)
}

func readStrictCacheFile(dir, name string, limit int64) ([]byte, error) {
	filePath := filepath.Join(dir, name)
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, fmt.Errorf("%s is not regular", name))
	}
	if info.Size() == 0 || info.Size() > limit {
		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, fmt.Errorf("%s size %d invalid", name, info.Size()))
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	defer f.Close()

	data, err := readLimitedBytes(f, limit, name)
	if err != nil {
		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, err)
	}
	return data, nil
}
