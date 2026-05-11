package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"goaria-v3/internal/extractor"
)

const (
	lockSchemaVersion  = 1
	maxAssetBytes      = 64 * 1024 * 1024
	maxManifestBytes   = 256 * 1024
	maxPayloadBytes    = 32 * 1024 * 1024
	maxSignatureBytes  = 64 * 1024
	defaultHTTPTimeout = 30 * time.Second
)

type lockFile struct {
	SchemaVersion int             `json:"schema_version"`
	Packs         []packLockEntry `json:"packs"`
}

type packLockEntry struct {
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

type verifyOptions struct {
	LockPath      string
	OutPath       string
	ProvenanceOut string
	Required      bool
	AllowFile     bool
	CleanOutputs  bool
	HTTPClient    *http.Client
}

type packParts struct {
	ManifestJSON []byte
	Payload      []byte
	Signature    []byte
}

type verifiedAsset struct {
	Entry                 packLockEntry
	Parts                 packParts
	PublicKeys            []ed25519.PublicKey
	PublicKeyHex          []string
	PublicKeyFingerprints []string
	AssetSHA256           string
	ManifestSHA256        string
	PayloadSHA256         string
	SignatureSHA256       string
}

type provenanceFile struct {
	SchemaVersion int               `json:"schema_version"`
	Required      bool              `json:"required"`
	Packs         []provenanceEntry `json:"packs"`
}

type provenanceEntry struct {
	PackID                string   `json:"pack_id"`
	PackVersion           string   `json:"pack_version"`
	AssetURL              string   `json:"asset_url,omitempty"`
	AssetPath             string   `json:"asset_path,omitempty"`
	AssetSHA256           string   `json:"asset_sha256"`
	ManifestSHA256        string   `json:"manifest_sha256"`
	PayloadSHA256         string   `json:"payload_sha256"`
	SignatureSHA256       string   `json:"signature_sha256"`
	PublicKeyFingerprints []string `json:"public_key_fingerprints"`
}

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: extractorpacks verify [--lock path] [--out path] [--provenance-out path] [--required] [--allow-file]")
	}

	switch args[0] {
	case "verify":
		flags := flag.NewFlagSet("verify", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		opts := verifyOptions{}
		flags.StringVar(&opts.LockPath, "lock", filepath.Join("build", "extractor", "packs.lock.json"), "extractor pack lock file")
		flags.StringVar(&opts.OutPath, "out", filepath.Join("internal", "extractor", "embedded_packs_release_gen.go"), "generated Go embed output")
		flags.StringVar(&opts.ProvenanceOut, "provenance-out", filepath.Join("build", "extractor", "verified_packs.provenance.json"), "generated public provenance output")
		flags.BoolVar(&opts.Required, "required", false, "fail if no production packs are configured")
		flags.BoolVar(&opts.AllowFile, "allow-file", false, "allow fixture/local file assets")
		flags.BoolVar(&opts.CleanOutputs, "cleanup", false, "remove generated outputs after successful verification")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		return verifyPacks(opts)
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func verifyPacks(opts verifyOptions) (err error) {
	if strings.TrimSpace(opts.LockPath) == "" {
		return errors.New("--lock must be non-empty")
	}
	if strings.TrimSpace(opts.OutPath) == "" {
		return errors.New("--out must be non-empty")
	}
	defer func() {
		if err != nil {
			_ = removeGeneratedOutputs(opts)
		}
	}()

	lock, lockDir, err := readLock(opts.LockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !opts.Required {
			return removeGeneratedOutputs(opts)
		}
		return err
	}
	if lock.SchemaVersion != lockSchemaVersion {
		return fmt.Errorf("lock schema_version %d is unsupported", lock.SchemaVersion)
	}
	if len(lock.Packs) == 0 {
		if err := removeGeneratedOutputs(opts); err != nil {
			return err
		}
		if opts.Required {
			return errors.New("required extractor pack verification failed: lock contains no packs")
		}

		return nil
	}

	verified := make([]verifiedAsset, 0, len(lock.Packs))
	for i, entry := range lock.Packs {
		asset, err := verifyPackAsset(opts, lockDir, entry)
		if err != nil {
			return fmt.Errorf("verify pack %d (%s): %w", i, safePackID(entry.PackID), err)
		}
		verified = append(verified, asset)
	}

	code, err := generateEmbeddedPacksCode(verified, opts.Required)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(opts.OutPath, code, 0o644); err != nil {
		return fmt.Errorf("write generated embed code: %w", err)
	}

	if opts.ProvenanceOut != "" {
		provenance, err := generateProvenance(verified, opts.Required)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(opts.ProvenanceOut, provenance, 0o644); err != nil {
			return fmt.Errorf("write provenance: %w", err)
		}
	}
	if opts.CleanOutputs {
		return removeGeneratedOutputs(opts)
	}

	return nil
}

func readLock(lockPath string) (lockFile, string, error) {
	bytes, err := os.ReadFile(lockPath)
	if err != nil {
		return lockFile{}, "", fmt.Errorf("read lock file: %w", err)
	}

	var lock lockFile
	decoder := json.NewDecoder(strings.NewReader(string(bytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		return lockFile{}, "", fmt.Errorf("decode lock file: %w", err)
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return lockFile{}, "", errors.New("decode lock file: trailing JSON data")
	}

	return lock, filepath.Dir(lockPath), nil
}

func verifyPackAsset(opts verifyOptions, lockDir string, entry packLockEntry) (verifiedAsset, error) {
	assetBytes, err := readAsset(opts, lockDir, entry)
	if err != nil {
		return verifiedAsset{}, err
	}

	assetSHA := sha256Hex(assetBytes)
	if err := validateLowerHexSHA256("asset_sha256", entry.AssetSHA256); err != nil {
		return verifiedAsset{}, err
	}
	if assetSHA != entry.AssetSHA256 {
		return verifiedAsset{}, fmt.Errorf("asset checksum pin mismatch: got %s", assetSHA)
	}

	parts, err := extractStrictPackZip(assetBytes)
	if err != nil {
		return verifiedAsset{}, err
	}

	manifestSHA := sha256Hex(parts.ManifestJSON)
	payloadSHA := sha256Hex(parts.Payload)
	signatureSHA := sha256Hex(parts.Signature)
	if err := checkOptionalSHA256("manifest_sha256", entry.ManifestSHA256, manifestSHA); err != nil {
		return verifiedAsset{}, err
	}
	if err := checkOptionalSHA256("payload_sha256", entry.PayloadSHA256, payloadSHA); err != nil {
		return verifiedAsset{}, err
	}
	if err := checkOptionalSHA256("signature_sha256", entry.SignatureSHA256, signatureSHA); err != nil {
		return verifiedAsset{}, err
	}

	publicKeys, keyHex, fingerprints, err := decodePublicKeys(entry.PublicKeys)
	if err != nil {
		return verifiedAsset{}, err
	}
	policy := extractor.DefaultTrustPolicy()
	policy.TrustedPublicKeys = publicKeys
	verified, err := extractor.VerifyEmbeddedPack(extractor.EmbeddedPack{
		ManifestJSON: parts.ManifestJSON,
		Payload:      parts.Payload,
		Signature:    parts.Signature,
		AssetSHA256:  assetSHA,
	}, policy)
	if err != nil {
		return verifiedAsset{}, fmt.Errorf("verify signed manifest/payload: %w", err)
	}
	if verified.Manifest.PackID != entry.PackID {
		return verifiedAsset{}, fmt.Errorf("manifest pack_id %q does not match lock pack_id %q", verified.Manifest.PackID, entry.PackID)
	}
	if verified.Manifest.PackVersion != entry.PackVersion {
		return verifiedAsset{}, fmt.Errorf("manifest pack_version %q does not match lock pack_version %q", verified.Manifest.PackVersion, entry.PackVersion)
	}

	return verifiedAsset{
		Entry:                 entry,
		Parts:                 parts,
		PublicKeys:            publicKeys,
		PublicKeyHex:          keyHex,
		PublicKeyFingerprints: fingerprints,
		AssetSHA256:           assetSHA,
		ManifestSHA256:        manifestSHA,
		PayloadSHA256:         payloadSHA,
		SignatureSHA256:       signatureSHA,
	}, nil
}

func readAsset(opts verifyOptions, lockDir string, entry packLockEntry) ([]byte, error) {
	if entry.AssetPath != "" {
		if !opts.AllowFile {
			return nil, errors.New("asset_path requires explicit --allow-file fixture mode")
		}

		return readLocalAssetPath(lockDir, entry.AssetPath)
	}

	if entry.AssetURL == "" {
		return nil, errors.New("pack entry must declare asset_url or fixture-only asset_path")
	}
	parsed, err := url.Parse(entry.AssetURL)
	if err != nil {
		return nil, errors.New("asset_url is malformed")
	}
	if parsed.Scheme == "file" {
		if !opts.AllowFile {
			return nil, errors.New("file asset_url requires explicit --allow-file fixture mode")
		}

		return readLocalAssetPath(lockDir, fileURLPath(parsed))
	}

	if err := validateProductionAssetURL(parsed); err != nil {
		return nil, err
	}

	return fetchHTTPSAsset(opts.HTTPClient, parsed.String())
}

func readLocalAssetPath(lockDir string, assetPath string) ([]byte, error) {
	if strings.TrimSpace(assetPath) == "" {
		return nil, errors.New("asset_path must be non-empty")
	}
	path := assetPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(lockDir, path)
	}

	bytes, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read local asset: %w", err)
	}
	if len(bytes) > maxAssetBytes {
		return nil, fmt.Errorf("asset exceeds %d bytes", maxAssetBytes)
	}

	return bytes, nil
}

func fileURLPath(parsed *url.URL) string {
	if parsed.Host != "" {
		return "//" + parsed.Host + parsed.Path
	}

	return parsed.Path
}

func validateProductionAssetURL(parsed *url.URL) error {
	if parsed.Scheme != "https" {
		return errors.New("production asset_url must use https")
	}
	if parsed.User != nil {
		return errors.New("production asset_url must not include credentials")
	}
	if parsed.Host == "" {
		return errors.New("production asset_url must include a host")
	}
	if parsed.RawQuery != "" {
		return errors.New("production asset_url must not include query parameters")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return errors.New("production asset_url must not include fragments")
	}

	return nil
}

func fetchHTTPSAsset(client *http.Client, rawURL string) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	client = cloneHTTPClientWithRedirectCheck(client)

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errors.New("create asset request failed")
	}
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := client.Do(req)
	if err != nil {
		return nil, sanitizeFetchError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch public asset: HTTP status %d", resp.StatusCode)
	}

	return readLimited(resp.Body, maxAssetBytes, "asset")
}

func cloneHTTPClientWithRedirectCheck(client *http.Client) *http.Client {
	cloned := *client
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateProductionAssetURL(req.URL); err != nil {
			return errUnsafeAssetRedirect
		}

		return nil
	}
	if cloned.Timeout == 0 {
		cloned.Timeout = defaultHTTPTimeout
	}

	return &cloned
}

var errUnsafeAssetRedirect = errors.New("asset redirect target is not an allowed public HTTPS URL")

func sanitizeFetchError(err error) error {
	if errors.Is(err, errUnsafeAssetRedirect) {
		return fmt.Errorf("fetch public asset: %w", errUnsafeAssetRedirect)
	}

	return errors.New("fetch public asset failed")
}

func extractStrictPackZip(assetBytes []byte) (packParts, error) {
	reader, err := zip.NewReader(bytes.NewReader(assetBytes), int64(len(assetBytes)))
	if err != nil {
		return packParts{}, fmt.Errorf("open pack zip: %w", err)
	}

	limits := map[string]int64{
		"manifest.json": maxManifestBytes,
		"payload.wasm":  maxPayloadBytes,
		"manifest.sig":  maxSignatureBytes,
	}
	seen := make(map[string][]byte, len(limits))
	for _, file := range reader.File {
		name := file.Name
		if err := validateZipEntryName(name); err != nil {
			return packParts{}, err
		}
		limit, ok := limits[name]
		if !ok {
			return packParts{}, fmt.Errorf("unexpected zip entry %q", name)
		}
		if _, ok := seen[name]; ok {
			return packParts{}, fmt.Errorf("duplicate zip entry %q", name)
		}
		if modeType := file.FileInfo().Mode() & os.ModeType; modeType != 0 {
			return packParts{}, fmt.Errorf("zip entry %q must be a regular file", name)
		}
		if file.UncompressedSize64 > uint64(limit) {
			return packParts{}, fmt.Errorf("zip entry %q exceeds %d bytes", name, limit)
		}

		entryBytes, err := readZipEntry(file, limit)
		if err != nil {
			return packParts{}, err
		}
		seen[name] = entryBytes
	}

	missing := make([]string, 0)
	for name := range limits {
		if _, ok := seen[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return packParts{}, fmt.Errorf("pack zip missing required entries: %s", strings.Join(missing, ", "))
	}

	return packParts{
		ManifestJSON: seen["manifest.json"],
		Payload:      seen["payload.wasm"],
		Signature:    seen["manifest.sig"],
	}, nil
}

func validateZipEntryName(name string) error {
	if name == "" || strings.HasSuffix(name, "/") {
		return fmt.Errorf("zip entry %q must be a file", name)
	}
	if strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
		return fmt.Errorf("zip entry %q has an unsafe path", name)
	}
	if path.Clean(name) != name || strings.Contains(name, ":") {
		return fmt.Errorf("zip entry %q has an unsafe path", name)
	}

	return nil
}

func readZipEntry(file *zip.File, limit int64) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open zip entry %q: %w", file.Name, err)
	}
	defer reader.Close()

	return readLimited(reader, limit, fmt.Sprintf("zip entry %q", file.Name))
}

func readLimited(reader io.Reader, limit int64, label string) ([]byte, error) {
	bytes, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(bytes)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, limit)
	}

	return bytes, nil
}

func decodePublicKeys(values []string) ([]ed25519.PublicKey, []string, []string, error) {
	if len(values) == 0 {
		return nil, nil, nil, errors.New("public_keys must contain at least one Ed25519 public key")
	}

	keys := make([]ed25519.PublicKey, 0, len(values))
	keyHex := make([]string, 0, len(values))
	fingerprints := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if len(value) != ed25519.PublicKeySize*2 || value != strings.ToLower(value) {
			return nil, nil, nil, errors.New("public_keys must be lowercase hex encoded Ed25519 keys")
		}
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, nil, nil, errors.New("public_keys must be lowercase hex encoded Ed25519 keys")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		keys = append(keys, ed25519.PublicKey(decoded))
		keyHex = append(keyHex, value)
		fingerprints = append(fingerprints, sha256Hex(decoded))
	}

	return keys, keyHex, fingerprints, nil
}

func checkOptionalSHA256(field string, expected string, actual string) error {
	if expected == "" {
		return nil
	}
	if err := validateLowerHexSHA256(field, expected); err != nil {
		return err
	}
	if expected != actual {
		return fmt.Errorf("%s mismatch: got %s", field, actual)
	}

	return nil
}

func validateLowerHexSHA256(field string, value string) error {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return fmt.Errorf("%s must be 64 lowercase hex characters", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s must be 64 lowercase hex characters", field)
	}

	return nil
}

func generateEmbeddedPacksCode(verified []verifiedAsset, required bool) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString("// Code generated by go run ./scripts/extractorpacks verify; DO NOT EDIT.\n\n")
	builder.WriteString("package extractor\n\n")
	builder.WriteString("func init() {\n")
	fmt.Fprintf(&builder, "\tembeddedReleaseRequired = %t\n", required)
	for _, asset := range verified {
		fmt.Fprintf(&builder, "\t// pack_id: %s pack_version: %s asset_sha256: %s\n", asset.Entry.PackID, asset.Entry.PackVersion, asset.AssetSHA256)
		builder.WriteString("\tembeddedReleasePacks = append(embeddedReleasePacks, EmbeddedPack{\n")
		builder.WriteString("\t\tManifestJSON: ")
		builder.WriteString(byteSliceLiteral(asset.Parts.ManifestJSON, "\t\t"))
		builder.WriteString(",\n")
		builder.WriteString("\t\tPayload: ")
		builder.WriteString(byteSliceLiteral(asset.Parts.Payload, "\t\t"))
		builder.WriteString(",\n")
		builder.WriteString("\t\tSignature: ")
		builder.WriteString(byteSliceLiteral(asset.Parts.Signature, "\t\t"))
		builder.WriteString(",\n")
		fmt.Fprintf(&builder, "\t\tAssetSHA256: %q,\n", asset.AssetSHA256)
		builder.WriteString("\t})\n")
	}

	seenKeys := make(map[string]struct{})
	keyOrder := make([]string, 0)
	keyBytes := make(map[string][]byte)
	for _, asset := range verified {
		for i, value := range asset.PublicKeyHex {
			if _, ok := seenKeys[value]; ok {
				continue
			}
			seenKeys[value] = struct{}{}
			keyOrder = append(keyOrder, value)
			keyBytes[value] = asset.PublicKeys[i]
		}
	}
	for _, value := range keyOrder {
		fmt.Fprintf(&builder, "\t// public_key_hex: %s public_key_sha256: %s\n", value, sha256Hex(keyBytes[value]))
		builder.WriteString("\tembeddedReleaseTrustedPublicKeys = append(embeddedReleaseTrustedPublicKeys, ")
		builder.WriteString(byteSliceLiteral(keyBytes[value], "\t"))
		builder.WriteString(")\n")
	}
	builder.WriteString("}\n")

	formatted, err := format.Source([]byte(builder.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated embed code: %w", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "embedded_packs_release_gen.go", formatted, parser.AllErrors); err != nil {
		return nil, fmt.Errorf("parse generated embed code: %w", err)
	}

	return formatted, nil
}

func byteSliceLiteral(data []byte, indent string) string {
	if len(data) == 0 {
		return "[]byte{}"
	}

	var builder strings.Builder
	builder.WriteString("[]byte{")
	for i, value := range data {
		if i%12 == 0 {
			builder.WriteByte('\n')
			builder.WriteString(indent)
			builder.WriteByte('\t')
		}
		fmt.Fprintf(&builder, "0x%02x, ", value)
	}
	builder.WriteByte('\n')
	builder.WriteString(indent)
	builder.WriteByte('}')

	return builder.String()
}

func generateProvenance(verified []verifiedAsset, required bool) ([]byte, error) {
	provenance := provenanceFile{
		SchemaVersion: lockSchemaVersion,
		Required:      required,
		Packs:         make([]provenanceEntry, 0, len(verified)),
	}
	for _, asset := range verified {
		provenance.Packs = append(provenance.Packs, provenanceEntry{
			PackID:                asset.Entry.PackID,
			PackVersion:           asset.Entry.PackVersion,
			AssetURL:              asset.Entry.AssetURL,
			AssetPath:             asset.Entry.AssetPath,
			AssetSHA256:           asset.AssetSHA256,
			ManifestSHA256:        asset.ManifestSHA256,
			PayloadSHA256:         asset.PayloadSHA256,
			SignatureSHA256:       asset.SignatureSHA256,
			PublicKeyFingerprints: append([]string(nil), asset.PublicKeyFingerprints...),
		})
	}

	bytes, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode provenance: %w", err)
	}

	return append(bytes, '\n'), nil
}

func writeFileAtomic(filePath string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(filePath)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Remove(filePath)

	return os.Rename(tmpPath, filePath)
}

func removeGeneratedOutputs(opts verifyOptions) error {
	var errs []error
	for _, filePath := range []string{opts.OutPath, opts.ProvenanceOut} {
		if filePath == "" {
			continue
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func safePackID(packID string) string {
	if packID == "" {
		return "unknown"
	}

	return extractor.RedactSensitive(packID)
}
