package extractor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

type RuntimePackLoadErrorCode string

const (
	RuntimePackLoadErrorSourceUnreadable   RuntimePackLoadErrorCode = "source_unreadable"
	RuntimePackLoadErrorSourceShapeInvalid RuntimePackLoadErrorCode = "source_shape_invalid"
	RuntimePackLoadErrorLockMissing        RuntimePackLoadErrorCode = "lock_missing"
	RuntimePackLoadErrorLockInvalid        RuntimePackLoadErrorCode = "lock_invalid"
	RuntimePackLoadErrorHashMismatch       RuntimePackLoadErrorCode = "hash_mismatch"
	RuntimePackLoadErrorSignatureInvalid   RuntimePackLoadErrorCode = "signature_invalid"
	RuntimePackLoadErrorManifestInvalid    RuntimePackLoadErrorCode = "manifest_invalid"
	RuntimePackLoadErrorWASMInvalid        RuntimePackLoadErrorCode = "wasm_invalid"
	RuntimePackLoadErrorRemoteDenied       RuntimePackLoadErrorCode = "remote_denied"
	RuntimePackLoadErrorRemoteFailed       RuntimePackLoadErrorCode = "remote_failed"
)

type RuntimePackLoadError struct {
	Code RuntimePackLoadErrorCode
	err  error
}

func (e *RuntimePackLoadError) Error() string {
	if e == nil {
		return ""
	}

	return fmt.Sprintf("pack load failed: %s", e.Code)
}

func (e *RuntimePackLoadError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.err
}

func newRuntimePackLoadError(code RuntimePackLoadErrorCode, cause error) error {
	return &RuntimePackLoadError{
		Code: code,
		err:  cause,
	}
}

type RuntimePackCandidate struct {
	VerifiedPack VerifiedPack
	ManifestJSON []byte
	Signature    []byte
	LockJSON     []byte
	ZipBytes     []byte
}

type runtimeLockFile struct {
	SchemaVersion int                `json:"schema_version"`
	Packs         []runtimeLockEntry `json:"packs"`
}

type runtimeLockEntry struct {
	PackID          string   `json:"pack_id"`
	PackVersion     string   `json:"pack_version"`
	AssetURL        string   `json:"asset_url,omitempty"`
	AssetPath       string   `json:"asset_path"`
	AssetSHA256     string   `json:"asset_sha256"`
	PublicKeys      []string `json:"public_keys"`
	ManifestSHA256  string   `json:"manifest_sha256"`
	PayloadSHA256   string   `json:"payload_sha256"`
	SignatureSHA256 string   `json:"signature_sha256"`
}

func VerifyRuntimePackComponents(
	ctx context.Context,
	rawManifest []byte,
	payload []byte,
	signature []byte,
	rawLock []byte,
	originalZipBytes []byte,
	isExplicitZip bool,
) (RuntimePackCandidate, error) {
	manifest, err := decodeManifestStrict(rawManifest)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorManifestInvalid, err)
	}
	if err := ValidateManifest(manifest, DefaultTrustPolicy()); err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorManifestInvalid, err)
	}

	lock, err := decodeStrictRuntimeLock(rawLock)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, err)
	}

	entry := lock.Packs[0]
	if entry.PackID != manifest.PackID || entry.PackVersion != manifest.PackVersion {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, errors.New("lock pack identity mismatch"))
	}

	manifestSHA256 := sha256HexString(rawManifest)
	if manifestSHA256 != entry.ManifestSHA256 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorHashMismatch, errors.New("manifest sha256 mismatch"))
	}
	payloadSHA256 := sha256HexString(payload)
	if payloadSHA256 != entry.PayloadSHA256 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorHashMismatch, errors.New("payload sha256 mismatch"))
	}
	signatureSHA256 := sha256HexString(signature)
	if signatureSHA256 != entry.SignatureSHA256 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorHashMismatch, errors.New("signature sha256 mismatch"))
	}

	if isExplicitZip {
		if len(originalZipBytes) == 0 {
			return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorHashMismatch, errors.New("empty zip bytes"))
		}
		recomputedZipSHA256 := sha256HexString(originalZipBytes)
		if recomputedZipSHA256 != entry.AssetSHA256 {
			return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorHashMismatch, errors.New("asset zip sha256 mismatch"))
		}
	}

	pubKeyBytes, err := hex.DecodeString(entry.PublicKeys[0])
	if err != nil || len(pubKeyBytes) != ed25519.PublicKeySize {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, errors.New("invalid public key"))
	}

	policy := DefaultTrustPolicy()
	policy.TrustedPublicKeys = []ed25519.PublicKey{ed25519.PublicKey(pubKeyBytes)}
	verifiedPack, err := VerifyEmbeddedPack(EmbeddedPack{
		ManifestJSON: rawManifest,
		Payload:      payload,
		Signature:    signature,
		AssetSHA256:  entry.AssetSHA256,
	}, policy)
	if err != nil {
		if strings.Contains(err.Error(), "signature verification failed") {
			return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSignatureInvalid, err)
		}
		if strings.Contains(err.Error(), "sha256") {
			return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorHashMismatch, err)
		}

		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorManifestInvalid, err)
	}

	if err := PreflightWASMModule(ctx, verifiedPack); err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorWASMInvalid, err)
	}

	var zipCopy []byte
	if isExplicitZip && len(originalZipBytes) > 0 {
		zipCopy = cloneBytes(originalZipBytes)
	}

	return RuntimePackCandidate{
		VerifiedPack: cloneVerifiedPack(verifiedPack),
		ManifestJSON: cloneBytes(rawManifest),
		Signature:    cloneBytes(signature),
		LockJSON:     cloneBytes(rawLock),
		ZipBytes:     zipCopy,
	}, nil
}

func decodeStrictRuntimeLock(raw []byte) (runtimeLockFile, error) {
	if len(raw) == 0 {
		return runtimeLockFile{}, errors.New("lock is empty")
	}
	if int64(len(raw)) > MaxPackManifestBytes {
		return runtimeLockFile{}, fmt.Errorf("lock exceeds %d bytes", MaxPackManifestBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	var lock runtimeLockFile
	if err := decoder.Decode(&lock); err != nil {
		return runtimeLockFile{}, err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return runtimeLockFile{}, errors.New("lock contains trailing JSON data")
	}

	if lock.SchemaVersion != 1 {
		return runtimeLockFile{}, fmt.Errorf("lock schema_version %d != 1", lock.SchemaVersion)
	}
	if len(lock.Packs) != 1 {
		return runtimeLockFile{}, fmt.Errorf("lock must contain exactly 1 pack, got %d", len(lock.Packs))
	}

	entry := lock.Packs[0]
	if entry.AssetURL != "" {
		return runtimeLockFile{}, errors.New("asset_url must be absent or empty")
	}
	if err := validateAssetFilename(entry.AssetPath); err != nil {
		return runtimeLockFile{}, fmt.Errorf("validate asset_path: %w", err)
	}
	if err := validateLowerHexSHA256Field("asset_sha256", entry.AssetSHA256); err != nil {
		return runtimeLockFile{}, err
	}
	if err := validateLowerHexSHA256Field("manifest_sha256", entry.ManifestSHA256); err != nil {
		return runtimeLockFile{}, err
	}
	if err := validateLowerHexSHA256Field("payload_sha256", entry.PayloadSHA256); err != nil {
		return runtimeLockFile{}, err
	}
	if err := validateLowerHexSHA256Field("signature_sha256", entry.SignatureSHA256); err != nil {
		return runtimeLockFile{}, err
	}
	if len(entry.PublicKeys) != 1 {
		return runtimeLockFile{}, fmt.Errorf("public_keys must contain exactly 1 key, got %d", len(entry.PublicKeys))
	}
	key := entry.PublicKeys[0]
	if len(key) != ed25519.PublicKeySize*2 || key != strings.ToLower(key) {
		return runtimeLockFile{}, errors.New("public_keys must be lowercase hex encoded Ed25519 keys")
	}
	decodedKey, err := hex.DecodeString(key)
	if err != nil || len(decodedKey) != ed25519.PublicKeySize {
		return runtimeLockFile{}, errors.New("public_keys must be valid Ed25519 keys")
	}

	return lock, nil
}

func validateAssetFilename(name string) error {
	if name == "" || name == "." || name == ".." {
		return errors.New("asset filename must be non-empty")
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, ":") ||
		strings.Contains(name, "?") || strings.Contains(name, "#") || strings.Contains(name, "%") {
		return errors.New("asset filename contains invalid path separators or special characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("asset filename contains control characters")
		}
	}
	if !strings.HasSuffix(name, ".pack.zip") {
		return errors.New("asset filename must end with .pack.zip")
	}
	if path.Clean(name) != name || filepath.Base(name) != name {
		return errors.New("asset filename is not a clean leaf filename")
	}

	return nil
}

func LoadLocalPackZip(ctx context.Context, zipPath string) (RuntimePackCandidate, error) {
	if strings.TrimSpace(zipPath) == "" {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, errors.New("zip path is empty"))
	}
	cleanPath := filepath.Clean(zipPath)

	info, err := os.Lstat(cleanPath)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	if info.IsDir() || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, errors.New("zip is not a regular file"))
	}
	if info.Size() == 0 || info.Size() > MaxPackAssetBytes {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, fmt.Errorf("zip size %d invalid", info.Size()))
	}

	file, err := os.Open(cleanPath)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	defer file.Close()

	zipBytes, err := readLimitedBytes(file, MaxPackAssetBytes, "pack zip")
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, err)
	}

	archive, err := ExtractStrictPackZip(zipBytes)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, err)
	}

	manifest, err := decodeManifestStrict(archive.ManifestJSON)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorManifestInvalid, err)
	}
	if err := ValidateManifest(manifest, DefaultTrustPolicy()); err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorManifestInvalid, err)
	}

	lockPath := filepath.Join(filepath.Dir(cleanPath), manifest.PackID+".lock.json")
	lockInfo, err := os.Lstat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockMissing, err)
		}

		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	if lockInfo.IsDir() || !lockInfo.Mode().IsRegular() || lockInfo.Mode()&os.ModeSymlink != 0 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, errors.New("lock is not a regular file"))
	}
	if lockInfo.Size() == 0 || lockInfo.Size() > MaxPackManifestBytes {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, fmt.Errorf("lock size %d invalid", lockInfo.Size()))
	}

	lockFileHandle, err := os.Open(lockPath)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	defer lockFileHandle.Close()

	lockBytes, err := readLimitedBytes(lockFileHandle, MaxPackManifestBytes, "lock file")
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, err)
	}

	lock, err := decodeStrictRuntimeLock(lockBytes)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, err)
	}
	if lock.Packs[0].AssetPath != filepath.Base(cleanPath) {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, fmt.Errorf("lock asset_path %q does not match zip file leaf %q", lock.Packs[0].AssetPath, filepath.Base(cleanPath)))
	}

	return VerifyRuntimePackComponents(ctx, archive.ManifestJSON, archive.Payload, archive.Signature, lockBytes, zipBytes, true)
}

func LoadLocalPackDirectory(ctx context.Context, dirPath string) (RuntimePackCandidate, error) {
	if strings.TrimSpace(dirPath) == "" {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, errors.New("dir path is empty"))
	}
	cleanDir := filepath.Clean(dirPath)

	dirInfo, err := os.Lstat(cleanDir)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, errors.New("target is not a regular directory"))
	}

	manifestBytes, err := readLocalComponentFile(cleanDir, "manifest.json", MaxPackManifestBytes)
	if err != nil {
		return RuntimePackCandidate{}, err
	}
	payloadBytes, err := readLocalComponentFile(cleanDir, "payload.wasm", MaxPackPayloadBytes)
	if err != nil {
		return RuntimePackCandidate{}, err
	}
	sigBytes, err := readLocalComponentFile(cleanDir, "manifest.sig", MaxPackSignatureBytes)
	if err != nil {
		return RuntimePackCandidate{}, err
	}

	manifest, err := decodeManifestStrict(manifestBytes)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorManifestInvalid, err)
	}
	if err := ValidateManifest(manifest, DefaultTrustPolicy()); err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorManifestInvalid, err)
	}

	lockPath := filepath.Join(cleanDir, manifest.PackID+".lock.json")
	lockInfo, err := os.Lstat(lockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockMissing, err)
		}

		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	if lockInfo.IsDir() || !lockInfo.Mode().IsRegular() || lockInfo.Mode()&os.ModeSymlink != 0 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, errors.New("lock is not a regular file"))
	}
	if lockInfo.Size() == 0 || lockInfo.Size() > MaxPackManifestBytes {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, fmt.Errorf("lock size %d invalid", lockInfo.Size()))
	}

	lockFileHandle, err := os.Open(lockPath)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	defer lockFileHandle.Close()

	lockBytes, err := readLimitedBytes(lockFileHandle, MaxPackManifestBytes, "lock file")
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, err)
	}

	return VerifyRuntimePackComponents(ctx, manifestBytes, payloadBytes, sigBytes, lockBytes, nil, false)
}

func readLocalComponentFile(dir string, name string, limit int64) ([]byte, error) {
	filePath := filepath.Join(dir, name)
	info, err := os.Lstat(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, err)
		}

		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceUnreadable, err)
	}
	if info.IsDir() || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, fmt.Errorf("file %q is not a regular file", name))
	}
	if info.Size() == 0 || info.Size() > limit {
		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, fmt.Errorf("file %q size %d invalid", name, info.Size()))
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
	if len(data) == 0 {
		return nil, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, fmt.Errorf("file %q is empty", name))
	}

	return data, nil
}

func LoadRemotePackLock(ctx context.Context, lockURL string) (RuntimePackCandidate, error) {
	client := newRuntimePackHTTPClient(defaultSecureHTTPTransport())
	return loadRemotePackLockWithClient(ctx, lockURL, client)
}

func newRuntimePackHTTPClient(transport http.RoundTripper) *http.Client {
	client := &http.Client{
		Transport: transport,
	}
	return cloneHTTPClientWithRedirectCheck(client, true)
}

func loadRemotePackLockWithClient(ctx context.Context, rawLockURL string, client *http.Client) (RuntimePackCandidate, error) {
	// Establish a single operation-level timeout covering both lock and asset HTTP fetches
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	parsedURL, err := validateRemoteLockURL(rawLockURL)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteDenied, err)
	}

	lockReq, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, err)
	}
	lockReq.Header.Set("Accept", "application/json")

	lockResp, err := client.Do(lockReq)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, err)
	}
	defer lockResp.Body.Close()

	if lockResp.StatusCode < 200 || lockResp.StatusCode >= 300 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, fmt.Errorf("lock fetch status %d", lockResp.StatusCode))
	}
	if lockResp.ContentLength > MaxPackManifestBytes {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, errors.New("lock content-length exceeds limit"))
	}

	lockBytes, err := readLimitedBytes(lockResp.Body, MaxPackManifestBytes, "remote lock")
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, err)
	}

	lock, err := decodeStrictRuntimeLock(lockBytes)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, err)
	}

	effectiveLockURL := lockResp.Request.URL
	if effectiveLockURL == nil {
		effectiveLockURL = parsedURL
	}
	if path.Base(effectiveLockURL.Path) != lock.Packs[0].PackID+".lock.json" {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, fmt.Errorf("lock pack_id %q does not match effective URL leaf %q", lock.Packs[0].PackID, path.Base(effectiveLockURL.Path)))
	}

	relAssetURL, err := url.Parse(lock.Packs[0].AssetPath)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorLockInvalid, err)
	}
	resolvedAssetURL := effectiveLockURL.ResolveReference(relAssetURL)
	if !sameURLOrigin(effectiveLockURL, resolvedAssetURL) {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteDenied, errors.New("resolved asset url crosses origin"))
	}
	if !strings.EqualFold(resolvedAssetURL.Scheme, "https") {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteDenied, errors.New("resolved asset url is not https"))
	}
	// Drop query and fragment from sibling asset request
	resolvedAssetURL.RawQuery = ""
	resolvedAssetURL.Fragment = ""

	assetReq, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, resolvedAssetURL.String(), nil)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, err)
	}
	assetReq.Header.Set("Accept", "application/zip, application/octet-stream")

	assetResp, err := client.Do(assetReq)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, err)
	}
	defer assetResp.Body.Close()

	if assetResp.StatusCode < 200 || assetResp.StatusCode >= 300 {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, fmt.Errorf("asset fetch status %d", assetResp.StatusCode))
	}
	if assetResp.ContentLength > MaxPackAssetBytes {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, errors.New("asset content-length exceeds limit"))
	}

	assetBytes, err := readLimitedBytes(assetResp.Body, MaxPackAssetBytes, "remote asset zip")
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorRemoteFailed, err)
	}

	archive, err := ExtractStrictPackZip(assetBytes)
	if err != nil {
		return RuntimePackCandidate{}, newRuntimePackLoadError(RuntimePackLoadErrorSourceShapeInvalid, err)
	}

	return VerifyRuntimePackComponents(ctx, archive.ManifestJSON, archive.Payload, archive.Signature, lockBytes, assetBytes, true)
}

func validateRemoteLockURL(rawLockURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawLockURL)
	if trimmed == "" {
		return nil, errors.New("lock URL is empty")
	}
	if strings.Contains(rawLockURL, "#") {
		return nil, errors.New("lock URL must not contain fragments")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return nil, errors.New("lock URL scheme must be https")
	}
	if parsed.User != nil {
		return nil, errors.New("lock URL must not contain credentials")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("lock URL must not contain fragments")
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return nil, errors.New("lock URL must contain hostname")
	}
	if strings.Contains(parsed.Host, "%") {
		return nil, errors.New("lock URL host must not contain escapes")
	}
	if strings.Contains(parsed.Host, ":") && parsed.Port() == "" {
		return nil, errors.New("lock URL host contains invalid port")
	}
	if _, err := netip.ParseAddr(hostname); err == nil {
		return nil, errors.New("lock URL host must not be an IP literal")
	}

	leaf := path.Base(parsed.Path)
	if leaf == "." || leaf == "/" || !strings.HasSuffix(leaf, ".lock.json") || leaf == ".lock.json" {
		return nil, errors.New("lock URL path leaf must end with .lock.json")
	}

	return parsed, nil
}

func cloneHTTPClientWithRedirectCheck(client *http.Client, sameOrigin bool) *http.Client {
	cloned := *client
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if !strings.EqualFold(req.URL.Scheme, "https") {
			return errors.New("redirect target scheme is not https")
		}
		if req.URL.User != nil || req.URL.Fragment != "" {
			return errors.New("redirect target has userinfo or fragment")
		}
		if _, err := netip.ParseAddr(req.URL.Hostname()); err == nil {
			return errors.New("redirect target host is ip literal")
		}
		if sameOrigin && len(via) > 0 && via[0] != nil && !sameURLOrigin(via[0].URL, req.URL) {
			return errors.New("redirect target crosses origin")
		}

		// Delete Referer, Authorization, and Cookie on redirect to prevent query or credential leakage
		req.Header.Del("Referer")
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")

		return nil
	}

	return &cloned
}

func urlEffectivePort(u *url.URL) string {
	if u == nil {
		return ""
	}
	port := u.Port()
	if port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return ""
}

func sameURLOrigin(a *url.URL, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}

	if !strings.EqualFold(a.Scheme, b.Scheme) {
		return false
	}
	if !strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	return urlEffectivePort(a) == urlEffectivePort(b)
}
