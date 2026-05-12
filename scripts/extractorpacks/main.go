package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
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
	fullPackCount      = 2

	workflowVariantGenericNoPack = "generic-no-pack"
	workflowVariantFullPack      = "full-pack"

	envExtractorReleaseVariant     = "EXTRACTOR_RELEASE_VARIANT"
	envFullPackMetadataB64         = "EXTRACTOR_FULL_PACK_METADATA_B64"
	envPrivatePolicyBundleB64      = "EXTRACTOR_PRIVATE_POLICY_BUNDLE_B64"
	envPrivatePolicyExpectedSHA256 = "EXTRACTOR_PRIVATE_POLICY_SHA256"
	envFullPackLocalAssetDir       = "EXTRACTOR_FULL_PACK_LOCAL_ASSET_DIR"
	defaultWorkflowMetadataPath    = "build/extractor/cache/full_pack_assets.json"
	defaultWorkflowTempLockPath    = "build/extractor/cache/full_pack.lock.json"
	defaultWorkflowPackEmbedPath   = "internal/extractor/embedded_packs_release_gen.go"
	defaultWorkflowPolicyEmbedPath = "internal/extractor/private_policy_bundle_release_gen.go"
	defaultWorkflowProvenancePath  = "build/extractor/verified_packs.provenance.json"
	defaultWorkflowEvidenceSummary = "build/extractor/extractor_build_evidence.summary.json"
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

type fullPackMetadataFile struct {
	SchemaVersion    int                     `json:"schema_version"`
	ReleaseTag       string                  `json:"release_tag"`
	BaseAssetURL     string                  `json:"base_asset_url,omitempty"`
	AssetURLTemplate string                  `json:"asset_url_template,omitempty"`
	Packs            []fullPackMetadataEntry `json:"packs"`
}

type fullPackMetadataEntry struct {
	AssetName       string   `json:"asset_name"`
	PackID          string   `json:"pack_id"`
	PackVersion     string   `json:"pack_version"`
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
	SameOrigin    bool
	VerifiedOut   *[]verifiedAsset
}

type renderLockOptions struct {
	MetadataPath string
	OutLockPath  string
}

type fullPackVerifyOptions struct {
	MetadataPath  string
	TempLockPath  string
	OutPath       string
	ProvenanceOut string
	LocalAssetDir string
	CleanOutputs  bool
	HTTPClient    *http.Client
	NoNameAudit   func(string, []byte) error
	VerifiedOut   *[]verifiedAsset
}

type workflowPaths struct {
	MetadataPath  string
	TempLockPath  string
	PackEmbedPath string
	PolicyOutPath string
	ProvenanceOut string
	SummaryOut    string
}

type workflowPrepareOptions struct {
	Mode              string
	MetadataB64       string
	MetadataInputPath string
	PolicyB64         string
	PolicyInputPath   string
	PolicySHA256      string
	LocalAssetDir     string
	Paths             workflowPaths
	HTTPClient        *http.Client
	NoNameAudit       func(string, []byte) error
}

type workflowCleanupOptions struct {
	Paths workflowPaths
}

type workflowEvidenceSummary struct {
	SchemaVersion            int      `json:"schema_version"`
	Variant                  string   `json:"variant"`
	PackAssetCount           int      `json:"pack_asset_count"`
	HostPolicyBundleInjected bool     `json:"host_policy_bundle_injected"`
	PackVerificationRequired bool     `json:"pack_verification_required"`
	GeneratedPackEmbed       bool     `json:"generated_pack_embed"`
	PublicProvenanceWritten  bool     `json:"public_provenance_written"`
	PublicEvidenceOnly       bool     `json:"public_evidence_only"`
	CustodyInputCategories   []string `json:"custody_input_categories,omitempty"`
	EvidenceOutputLabels     []string `json:"evidence_output_labels"`
}

type packParts struct {
	ManifestJSON []byte
	Payload      []byte
	Signature    []byte
}

type verifiedAsset struct {
	Entry                 packLockEntry
	Parts                 packParts
	Manifest              extractor.Manifest
	Identity              extractor.VerifiedPackIdentity
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

var (
	opaqueAssetNamePattern  = regexp.MustCompile(`^asset-[a-z0-9][a-z0-9-]{2,48}\.pack\.zip$`)
	opaquePackIDPattern     = regexp.MustCompile(`^xpk-[a-z0-9][a-z0-9-]{2,48}$`)
	opaquePackVersionRegexp = regexp.MustCompile(`^opaque-[a-z0-9][a-z0-9-]{0,48}$`)
	opaqueReleaseTagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,63}$`)
	assetURLTemplateKey     = regexp.MustCompile(`"asset_url_template"\s*:`)
)

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: extractorpacks verify [--lock path] [--out path] [--provenance-out path] [--required] [--allow-file] | render-lock --metadata path --out-lock path | verify-full-pack --metadata path --temp-lock path --out path [--provenance-out path] [--cleanup] | prepare-workflow [--mode variant] | cleanup-workflow")
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
	case "render-lock":
		flags := flag.NewFlagSet("render-lock", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		opts := renderLockOptions{}
		flags.StringVar(&opts.MetadataPath, "metadata", "", "full-pack metadata file")
		flags.StringVar(&opts.OutLockPath, "out-lock", "", "temporary generated lock output")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		return renderFullPackLockCommand(opts)
	case "verify-full-pack":
		flags := flag.NewFlagSet("verify-full-pack", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		opts := fullPackVerifyOptions{}
		flags.StringVar(&opts.MetadataPath, "metadata", "", "full-pack metadata file")
		flags.StringVar(&opts.TempLockPath, "temp-lock", "", "temporary generated lock output")
		flags.StringVar(&opts.OutPath, "out", "", "generated Go embed output")
		flags.StringVar(&opts.ProvenanceOut, "provenance-out", filepath.Join("build", "extractor", "verified_packs.provenance.json"), "generated public provenance output")
		flags.BoolVar(&opts.CleanOutputs, "cleanup", false, "remove generated outputs and temporary lock after successful verification")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		return verifyFullPack(opts)
	case "prepare-workflow":
		flags := flag.NewFlagSet("prepare-workflow", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		opts := workflowPrepareOptions{Paths: defaultWorkflowPaths()}
		flags.StringVar(&opts.Mode, "mode", os.Getenv(envExtractorReleaseVariant), "workflow extractor package variant")
		flags.StringVar(&opts.MetadataB64, "metadata-b64", os.Getenv(envFullPackMetadataB64), "base64 encoded full-pack metadata")
		flags.StringVar(&opts.MetadataInputPath, "metadata-input", "", "test-only full-pack metadata input path")
		flags.StringVar(&opts.PolicyB64, "policy-b64", os.Getenv(envPrivatePolicyBundleB64), "base64 encoded host policy bundle")
		flags.StringVar(&opts.PolicyInputPath, "policy-input", "", "test-only host policy bundle input path")
		flags.StringVar(&opts.PolicySHA256, "policy-sha256", os.Getenv(envPrivatePolicyExpectedSHA256), "optional expected host policy bundle sha256")
		flags.StringVar(&opts.LocalAssetDir, "local-asset-dir", os.Getenv(envFullPackLocalAssetDir), "workflow local full-pack asset directory")
		registerWorkflowPathFlags(flags, &opts.Paths)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		return prepareWorkflow(opts)
	case "cleanup-workflow":
		flags := flag.NewFlagSet("cleanup-workflow", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		opts := workflowCleanupOptions{Paths: defaultWorkflowPaths()}
		registerWorkflowPathFlags(flags, &opts.Paths)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}

		return cleanupWorkflow(opts)
	default:
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func defaultWorkflowPaths() workflowPaths {
	return workflowPaths{
		MetadataPath:  defaultWorkflowMetadataPath,
		TempLockPath:  defaultWorkflowTempLockPath,
		PackEmbedPath: defaultWorkflowPackEmbedPath,
		PolicyOutPath: defaultWorkflowPolicyEmbedPath,
		ProvenanceOut: defaultWorkflowProvenancePath,
		SummaryOut:    defaultWorkflowEvidenceSummary,
	}
}

func registerWorkflowPathFlags(flags *flag.FlagSet, paths *workflowPaths) {
	flags.StringVar(&paths.MetadataPath, "metadata-cache", paths.MetadataPath, "workflow metadata cache output")
	flags.StringVar(&paths.TempLockPath, "temp-lock", paths.TempLockPath, "workflow temporary lock output")
	flags.StringVar(&paths.PackEmbedPath, "pack-out", paths.PackEmbedPath, "workflow generated pack embed output")
	flags.StringVar(&paths.PolicyOutPath, "policy-out", paths.PolicyOutPath, "workflow generated host policy embed output")
	flags.StringVar(&paths.ProvenanceOut, "provenance-out", paths.ProvenanceOut, "workflow public provenance output")
	flags.StringVar(&paths.SummaryOut, "summary-out", paths.SummaryOut, "workflow public evidence summary output")
}

func renderFullPackLockCommand(opts renderLockOptions) error {
	if strings.TrimSpace(opts.MetadataPath) == "" {
		return errors.New("--metadata must be non-empty")
	}
	if strings.TrimSpace(opts.OutLockPath) == "" {
		return errors.New("--out-lock must be non-empty")
	}
	if err := rejectProductionLockOutput(opts.OutLockPath); err != nil {
		return err
	}

	metadata, err := readFullPackMetadata(opts.MetadataPath)
	if err != nil {
		return err
	}
	lock, err := renderFullPackLock(metadata)
	if err != nil {
		return err
	}
	lockBytes, err := marshalRenderedFullPackLock(lock)
	if err != nil {
		return err
	}
	if err := auditNoNameBytes("rendered lock", lockBytes); err != nil {
		return err
	}

	return writeFileAtomic(opts.OutLockPath, lockBytes, 0o644)
}

func verifyFullPack(opts fullPackVerifyOptions) (err error) {
	if strings.TrimSpace(opts.MetadataPath) == "" {
		return errors.New("--metadata must be non-empty")
	}
	if strings.TrimSpace(opts.TempLockPath) == "" {
		return errors.New("--temp-lock must be non-empty")
	}
	if strings.TrimSpace(opts.OutPath) == "" {
		return errors.New("--out must be non-empty")
	}
	if err := rejectFullPackOutputPaths(opts.TempLockPath, opts.OutPath, opts.ProvenanceOut); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = removeGeneratedOutputs(verifyOptions{OutPath: opts.OutPath, ProvenanceOut: opts.ProvenanceOut})
			_ = os.Remove(opts.TempLockPath)
		}
	}()

	metadata, err := readFullPackMetadata(opts.MetadataPath)
	if err != nil {
		return err
	}
	lock, err := renderedFullPackLockForVerification(metadata, opts.TempLockPath, opts.LocalAssetDir)
	if err != nil {
		return err
	}
	lockBytes, err := marshalRenderedFullPackLock(lock)
	if err != nil {
		return err
	}
	if err := auditNoNameBytes("rendered lock", lockBytes); err != nil {
		return err
	}
	if err := writeFileAtomic(opts.TempLockPath, lockBytes, 0o644); err != nil {
		return fmt.Errorf("write temporary lock: %w", err)
	}

	useLocalAssets := strings.TrimSpace(opts.LocalAssetDir) != ""
	verifyOpts := verifyOptions{
		LockPath:      opts.TempLockPath,
		OutPath:       opts.OutPath,
		ProvenanceOut: opts.ProvenanceOut,
		Required:      true,
		AllowFile:     useLocalAssets,
		HTTPClient:    opts.HTTPClient,
		SameOrigin:    !useLocalAssets,
		VerifiedOut:   opts.VerifiedOut,
	}
	if err := verifyPacks(verifyOpts); err != nil {
		return sanitizeFullPackVerifyError(err)
	}

	audit := opts.NoNameAudit
	if audit == nil {
		audit = auditNoNameBytes
	}
	if err := auditFullPackGeneratedOutputs(opts.OutPath, opts.ProvenanceOut, audit); err != nil {
		return err
	}

	if opts.CleanOutputs {
		if err := removeGeneratedOutputs(verifyOpts); err != nil {
			return err
		}
		if err := os.Remove(opts.TempLockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := auditNoNameBytes("command output", []byte("full-pack verification completed\n")); err != nil {
		return err
	}

	return nil
}

func rejectFullPackOutputPaths(paths ...string) error {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := rejectProductionLockOutput(path); err != nil {
			return err
		}
	}

	return nil
}

func auditFullPackGeneratedOutputs(outPath string, provenanceOut string, audit func(string, []byte) error) error {
	generated, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("read generated embed code for audit: %w", err)
	}
	if audit == nil {
		audit = auditNoNameBytes
	}
	if err := audit("generated embed", generated); err != nil {
		return err
	}
	if provenanceOut != "" {
		provenance, err := os.ReadFile(provenanceOut)
		if err != nil {
			return fmt.Errorf("read generated provenance for audit: %w", err)
		}
		if err := audit("generated provenance", provenance); err != nil {
			return err
		}
	}

	return nil
}

func prepareWorkflow(opts workflowPrepareOptions) (err error) {
	mode := strings.TrimSpace(opts.Mode)
	if mode == "" {
		mode = workflowVariantGenericNoPack
	}
	if err := validateWorkflowVariant(mode); err != nil {
		return err
	}
	if err := validateWorkflowPaths(opts.Paths); err != nil {
		return err
	}

	if mode == workflowVariantGenericNoPack {
		if err := cleanupWorkflow(workflowCleanupOptions{Paths: opts.Paths}); err != nil {
			return err
		}

		return writeWorkflowEvidenceSummary(opts.Paths.SummaryOut, workflowEvidenceSummary{
			SchemaVersion:            lockSchemaVersion,
			Variant:                  workflowVariantGenericNoPack,
			PackAssetCount:           0,
			HostPolicyBundleInjected: false,
			PackVerificationRequired: false,
			GeneratedPackEmbed:       false,
			PublicProvenanceWritten:  false,
			PublicEvidenceOnly:       true,
			EvidenceOutputLabels:     []string{"extractor_build_evidence.summary.json"},
		})
	}

	defer func() {
		if err != nil {
			_ = cleanupWorkflow(workflowCleanupOptions{Paths: opts.Paths})
		}
	}()

	metadataRaw, err := workflowCustodyInput(workflowCustodyInputOptions{
		B64:       opts.MetadataB64,
		InputPath: opts.MetadataInputPath,
		Label:     "full-pack metadata",
	})
	if err != nil {
		return err
	}
	if err := auditNoNameBytes("metadata", metadataRaw); err != nil {
		return err
	}
	if err := writeFileAtomic(opts.Paths.MetadataPath, append([]byte(nil), metadataRaw...), 0o600); err != nil {
		return errors.New("write workflow metadata failed")
	}

	policyRaw, err := workflowCustodyInput(workflowCustodyInputOptions{
		B64:       opts.PolicyB64,
		InputPath: opts.PolicyInputPath,
		Label:     "host policy bundle",
	})
	if err != nil {
		return err
	}

	verified := make([]verifiedAsset, 0, fullPackCount)
	if err := verifyFullPack(fullPackVerifyOptions{
		MetadataPath:  opts.Paths.MetadataPath,
		TempLockPath:  opts.Paths.TempLockPath,
		OutPath:       opts.Paths.PackEmbedPath,
		ProvenanceOut: opts.Paths.ProvenanceOut,
		LocalAssetDir: opts.LocalAssetDir,
		HTTPClient:    opts.HTTPClient,
		NoNameAudit:   opts.NoNameAudit,
		VerifiedOut:   &verified,
	}); err != nil {
		return err
	}
	if len(verified) != fullPackCount {
		return errors.New("full-pack workflow preparation failed")
	}
	if err := validatePrivatePolicyForVerifiedAssets(policyRaw, opts.PolicySHA256, verified); err != nil {
		return err
	}
	if err := writePrivatePolicyEmbed(opts.Paths.PolicyOutPath, policyRaw, opts.PolicySHA256); err != nil {
		return err
	}

	return writeWorkflowEvidenceSummary(opts.Paths.SummaryOut, workflowEvidenceSummary{
		SchemaVersion:            lockSchemaVersion,
		Variant:                  workflowVariantFullPack,
		PackAssetCount:           len(verified),
		HostPolicyBundleInjected: true,
		PackVerificationRequired: true,
		GeneratedPackEmbed:       true,
		PublicProvenanceWritten:  false,
		PublicEvidenceOnly:       true,
		CustodyInputCategories:   []string{"full_pack_metadata", "host_policy_bundle"},
		EvidenceOutputLabels:     []string{"extractor_build_evidence.summary.json"},
	})
}

func cleanupWorkflow(opts workflowCleanupOptions) error {
	if err := validateWorkflowPaths(opts.Paths); err != nil {
		return err
	}
	files := []string{
		opts.Paths.PackEmbedPath,
		opts.Paths.PolicyOutPath,
		opts.Paths.ProvenanceOut,
		opts.Paths.TempLockPath,
		opts.Paths.MetadataPath,
		opts.Paths.SummaryOut,
	}
	var errs []error
	for _, filePath := range files {
		if strings.TrimSpace(filePath) == "" {
			continue
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, errors.New("workflow cleanup failed"))
		}
	}

	return errors.Join(errs...)
}

func validateWorkflowVariant(mode string) error {
	switch mode {
	case workflowVariantGenericNoPack, workflowVariantFullPack:
		return nil
	default:
		return errors.New("workflow extractor variant is invalid")
	}
}

func validateWorkflowPaths(paths workflowPaths) error {
	for _, filePath := range []string{paths.MetadataPath, paths.TempLockPath, paths.PackEmbedPath, paths.PolicyOutPath, paths.ProvenanceOut, paths.SummaryOut} {
		if strings.TrimSpace(filePath) == "" {
			return errors.New("workflow output path is invalid")
		}
		if err := rejectDangerousWorkflowPath(filePath); err != nil {
			return err
		}
		if err := rejectProductionLockOutput(filePath); err != nil {
			return err
		}
	}

	return nil
}

func rejectDangerousWorkflowPath(filePath string) error {
	if strings.TrimSpace(filePath) == "" {
		return errors.New("workflow output path is invalid")
	}
	cleaned := guardComparablePath(filePath)
	volume := strings.TrimRight(strings.ToLower(filepath.VolumeName(filePath)), ":")
	if cleaned == "." || cleaned == "/" || cleaned == "" || volume != "" && cleaned == volume+":" {
		return errors.New("workflow output path is invalid")
	}
	base := filepath.Base(filepath.Clean(filePath))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return errors.New("workflow output path is invalid")
	}

	return nil
}

type workflowCustodyInputOptions struct {
	B64       string
	InputPath string
	Label     string
}

func workflowCustodyInput(opts workflowCustodyInputOptions) ([]byte, error) {
	hasB64 := strings.TrimSpace(opts.B64) != ""
	hasPath := strings.TrimSpace(opts.InputPath) != ""
	if hasB64 == hasPath {
		return nil, fmt.Errorf("%s custody input is required", opts.Label)
	}
	if hasPath {
		raw, err := os.ReadFile(opts.InputPath)
		if err != nil || len(raw) == 0 {
			return nil, fmt.Errorf("%s custody input is invalid", opts.Label)
		}

		return raw, nil
	}

	decoded, err := base64.StdEncoding.Strict().DecodeString(opts.B64)
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("%s custody input is invalid", opts.Label)
	}

	return decoded, nil
}

func validatePrivatePolicyForVerifiedAssets(raw []byte, expectedSHA string, verified []verifiedAsset) error {
	resolver, err := extractor.NewPrivatePolicyBundleResolver(raw, extractor.PrivatePolicyBundleLoadOptions{ExpectedPolicyPrivateSHA256: expectedSHA})
	if err != nil {
		return errors.New("host policy bundle is invalid")
	}
	if len(verified) != fullPackCount {
		return errors.New("host policy bundle is invalid")
	}
	if err := validatePrivatePolicyIdentitySet(raw, verified); err != nil {
		return err
	}
	for _, asset := range verified {
		if _, err := resolver.ResolveHostPolicy(context.Background(), extractor.HostPolicyRequest{PackIdentity: asset.Identity, Manifest: asset.Manifest}); err != nil {
			return errors.New("host policy bundle is invalid")
		}
	}

	return nil
}

type workflowPolicyEnvelope struct {
	Policy json.RawMessage `json:"policy"`
}

type workflowPolicyFile struct {
	Packs []workflowPolicyPack `json:"packs"`
}

type workflowPolicyPack struct {
	VerifiedPackIdentity workflowPolicyIdentity `json:"verified_pack_identity"`
}

type workflowPolicyIdentity struct {
	PackID          string `json:"pack_id"`
	PackVersion     string `json:"pack_version"`
	AssetSHA256     string `json:"asset_sha256"`
	ManifestSHA256  string `json:"manifest_sha256"`
	PayloadSHA256   string `json:"payload_sha256"`
	SignatureSHA256 string `json:"signature_sha256"`
	PublicKeySHA256 string `json:"public_key_sha256"`
}

func validatePrivatePolicyIdentitySet(raw []byte, verified []verifiedAsset) error {
	var envelope workflowPolicyEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Policy) == 0 {
		return errors.New("host policy bundle is invalid")
	}
	var policy workflowPolicyFile
	if err := json.Unmarshal(envelope.Policy, &policy); err != nil {
		return errors.New("host policy bundle is invalid")
	}
	if len(policy.Packs) != len(verified) {
		return errors.New("host policy bundle is invalid")
	}
	policyIdentities := make(map[extractor.VerifiedPackIdentity]struct{}, len(policy.Packs))
	for _, pack := range policy.Packs {
		identity := pack.VerifiedPackIdentity.identity()
		if _, ok := policyIdentities[identity]; ok {
			return errors.New("host policy bundle is invalid")
		}
		policyIdentities[identity] = struct{}{}
	}
	for _, asset := range verified {
		if _, ok := policyIdentities[asset.Identity]; !ok {
			return errors.New("host policy bundle is invalid")
		}
	}

	return nil
}

func (identity workflowPolicyIdentity) identity() extractor.VerifiedPackIdentity {
	return extractor.VerifiedPackIdentity{
		PackID:          identity.PackID,
		PackVersion:     identity.PackVersion,
		AssetSHA256:     identity.AssetSHA256,
		ManifestSHA256:  identity.ManifestSHA256,
		PayloadSHA256:   identity.PayloadSHA256,
		SignatureSHA256: identity.SignatureSHA256,
		PublicKeySHA256: identity.PublicKeySHA256,
	}
}

func writePrivatePolicyEmbed(outPath string, raw []byte, expectedSHA string) error {
	embedSHA, err := privatePolicyExpectedSHAForEmbed(raw, expectedSHA)
	if err != nil {
		return err
	}

	var builder strings.Builder
	builder.WriteString("// Code generated by go run ./scripts/extractorpacks prepare-workflow; DO NOT EDIT.\n\n")
	builder.WriteString("package extractor\n\n")
	builder.WriteString("func init() {\n")
	builder.WriteString("\tembeddedPrivatePolicyBundleJSON = ")
	builder.WriteString(byteSliceLiteral(raw, "\t"))
	builder.WriteString("\n")
	if embedSHA != "" {
		fmt.Fprintf(&builder, "\tembeddedPrivatePolicyBundleSHA256 = %q\n", embedSHA)
	}
	builder.WriteString("}\n")

	formatted, err := format.Source([]byte(builder.String()))
	if err != nil {
		return errors.New("generate host policy bundle failed")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "private_policy_bundle_release_gen.go", formatted, parser.AllErrors); err != nil {
		return errors.New("generate host policy bundle failed")
	}

	return writeFileAtomic(outPath, formatted, 0o600)
}

func privatePolicyExpectedSHAForEmbed(raw []byte, expectedSHA string) (string, error) {
	if strings.TrimSpace(expectedSHA) != "" {
		return expectedSHA, nil
	}
	var envelope struct {
		PolicyPrivateSHA256 string `json:"policy_private_sha256"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", errors.New("host policy bundle is invalid")
	}
	if envelope.PolicyPrivateSHA256 == "" {
		return "", errors.New("host policy bundle is invalid")
	}

	return envelope.PolicyPrivateSHA256, nil
}

func writeWorkflowEvidenceSummary(outPath string, summary workflowEvidenceSummary) error {
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := auditNoNameBytes("evidence", raw); err != nil {
		return err
	}

	return writeFileAtomic(outPath, raw, 0o644)
}

func cloneVerifiedAssets(assets []verifiedAsset) []verifiedAsset {
	if assets == nil {
		return nil
	}
	cloned := make([]verifiedAsset, len(assets))
	for i, asset := range assets {
		cloned[i] = asset
		cloned[i].Entry.PublicKeys = append([]string(nil), asset.Entry.PublicKeys...)
		cloned[i].Parts.ManifestJSON = append([]byte(nil), asset.Parts.ManifestJSON...)
		cloned[i].Parts.Payload = append([]byte(nil), asset.Parts.Payload...)
		cloned[i].Parts.Signature = append([]byte(nil), asset.Parts.Signature...)
		cloned[i].PublicKeys = append([]ed25519.PublicKey(nil), asset.PublicKeys...)
		for j := range cloned[i].PublicKeys {
			cloned[i].PublicKeys[j] = append(ed25519.PublicKey(nil), asset.PublicKeys[j]...)
		}
		cloned[i].PublicKeyHex = append([]string(nil), asset.PublicKeyHex...)
		cloned[i].PublicKeyFingerprints = append([]string(nil), asset.PublicKeyFingerprints...)
	}

	return cloned
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
	if opts.VerifiedOut != nil {
		*opts.VerifiedOut = cloneVerifiedAssets(verified)
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

func readFullPackMetadata(metadataPath string) (fullPackMetadataFile, error) {
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		return fullPackMetadataFile{}, fmt.Errorf("read full-pack metadata: %w", err)
	}

	return decodeFullPackMetadata(raw)
}

func decodeFullPackMetadata(raw []byte) (fullPackMetadataFile, error) {
	if err := auditNoNameBytes("metadata", raw); err != nil {
		return fullPackMetadataFile{}, err
	}
	if err := auditDecodedFullPackMetadataJSON(raw); err != nil {
		return fullPackMetadataFile{}, err
	}

	var metadata fullPackMetadataFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return fullPackMetadataFile{}, sanitizeFullPackMetadataDecodeError(err)
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fullPackMetadataFile{}, errors.New("decode full-pack metadata: trailing JSON data")
	}
	if err := validateFullPackMetadata(metadata); err != nil {
		return fullPackMetadataFile{}, err
	}

	return metadata, nil
}

func sanitizeFullPackMetadataDecodeError(err error) error {
	if err == nil {
		return nil
	}

	return errors.New("decode full-pack metadata failed")
}

func auditDecodedFullPackMetadataJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return sanitizeFullPackMetadataDecodeError(err)
	}

	if auditDecodedJSONValue(value) {
		return errors.New("metadata no-name audit failed")
	}

	return nil
}

func auditDecodedJSONValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if key != "asset_url_template" && containsForbiddenNoNameTerm(key) || auditDecodedJSONValue(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if auditDecodedJSONValue(nested) {
				return true
			}
		}
	case string:
		return containsForbiddenNoNameTerm(typed)
	}

	return false
}

func validateFullPackMetadata(metadata fullPackMetadataFile) error {
	if metadata.SchemaVersion != lockSchemaVersion {
		return fmt.Errorf("full-pack metadata schema_version %d is unsupported", metadata.SchemaVersion)
	}
	if err := validateOpaqueReleaseTag(metadata.ReleaseTag); err != nil {
		return err
	}
	if (strings.TrimSpace(metadata.BaseAssetURL) == "") == (strings.TrimSpace(metadata.AssetURLTemplate) == "") {
		return errors.New("full-pack metadata must declare exactly one of base_asset_url or asset_url_template")
	}
	if metadata.BaseAssetURL != "" {
		if err := validateBaseAssetURL(metadata.BaseAssetURL); err != nil {
			return err
		}
	}
	if metadata.AssetURLTemplate != "" {
		if err := validateAssetURLTemplate(metadata.AssetURLTemplate); err != nil {
			return err
		}
	}
	if len(metadata.Packs) != fullPackCount {
		return fmt.Errorf("full-pack metadata must contain exactly %d packs", fullPackCount)
	}

	seenPackIDs := make(map[string]struct{}, len(metadata.Packs))
	seenAssetNames := make(map[string]struct{}, len(metadata.Packs))
	for i, pack := range metadata.Packs {
		if err := validateOpaqueAssetName(pack.AssetName); err != nil {
			return fmt.Errorf("pack %d asset_name: %w", i, err)
		}
		if err := validateOpaquePackID(pack.PackID); err != nil {
			return fmt.Errorf("pack %d pack_id: %w", i, err)
		}
		if err := validateOpaquePackVersion(pack.PackVersion); err != nil {
			return fmt.Errorf("pack %d pack_version: %w", i, err)
		}
		if _, ok := seenPackIDs[pack.PackID]; ok {
			return errors.New("full-pack metadata contains duplicate pack_id")
		}
		seenPackIDs[pack.PackID] = struct{}{}
		if _, ok := seenAssetNames[pack.AssetName]; ok {
			return errors.New("full-pack metadata contains duplicate asset_name")
		}
		seenAssetNames[pack.AssetName] = struct{}{}

		if err := validateLowerHexSHA256("asset_sha256", pack.AssetSHA256); err != nil {
			return err
		}
		if err := validateOptionalFullPackSHA256("manifest_sha256", pack.ManifestSHA256); err != nil {
			return err
		}
		if err := validateOptionalFullPackSHA256("payload_sha256", pack.PayloadSHA256); err != nil {
			return err
		}
		if err := validateOptionalFullPackSHA256("signature_sha256", pack.SignatureSHA256); err != nil {
			return err
		}
		if _, _, _, err := decodePublicKeys(pack.PublicKeys); err != nil {
			return err
		}
	}

	return nil
}

func sanitizeFullPackVerifyError(err error) error {
	if err == nil {
		return nil
	}

	return errors.New("full-pack verification failed")
}

func validateOptionalFullPackSHA256(field string, value string) error {
	if value == "" {
		return nil
	}

	return validateLowerHexSHA256(field, value)
}

func validateOpaqueReleaseTag(value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return errors.New("release_tag must be non-empty and trimmed")
	}
	if !opaqueReleaseTagPattern.MatchString(value) || strings.Contains(value, "..") {
		return errors.New("release_tag must be an opaque lowercase release label")
	}
	if hasUnsafePublicToken(value) || containsForbiddenNoNameTerm(value) {
		return errors.New("release_tag must be an opaque lowercase release label")
	}

	return nil
}

func validateOpaqueAssetName(value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return errors.New("asset_name must be non-empty and trimmed")
	}
	if !opaqueAssetNamePattern.MatchString(value) || strings.Count(value, ".") != 2 || strings.Contains(value, "..") {
		return errors.New("asset_name must be an opaque pack filename")
	}
	if hasUnsafePublicToken(value) || containsForbiddenNoNameTerm(value) {
		return errors.New("asset_name must be an opaque pack filename")
	}

	return nil
}

func validateOpaquePackID(value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return errors.New("pack_id must be non-empty and trimmed")
	}
	if !opaquePackIDPattern.MatchString(value) {
		return errors.New("pack_id must be opaque lowercase")
	}
	if hasUnsafePublicToken(value) || containsForbiddenNoNameTerm(value) || strings.Contains(value, ".") {
		return errors.New("pack_id must be opaque lowercase")
	}

	return nil
}

func validateOpaquePackVersion(value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return errors.New("pack_version must be non-empty and trimmed")
	}
	if !opaquePackVersionRegexp.MatchString(value) {
		return errors.New("pack_version must be opaque lowercase")
	}
	if hasUnsafePublicToken(value) || containsForbiddenNoNameTerm(value) || strings.Contains(value, ".") {
		return errors.New("pack_version must be opaque lowercase")
	}

	return nil
}

func hasUnsafePublicToken(value string) bool {
	if strings.ContainsAny(value, "\\/:?#@&=+%\r\n\t ") {
		return true
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}

	return false
}

func validateBaseAssetURL(rawURL string) error {
	if strings.TrimSpace(rawURL) != rawURL || rawURL == "" {
		return errors.New("base_asset_url must be non-empty and trimmed")
	}
	if containsURLUnsafeText(rawURL) || containsForbiddenNoNameTerm(rawURL) {
		return errors.New("base_asset_url must be a safe public HTTPS asset URL base")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("base_asset_url is malformed")
	}
	if err := validateProductionAssetURL(parsed); err != nil {
		return err
	}
	if err := validateFullPackAssetURL(parsed); err != nil {
		return err
	}

	return nil
}

func validateAssetURLTemplate(template string) error {
	if strings.TrimSpace(template) != template || template == "" {
		return errors.New("asset_url_template must be non-empty and trimmed")
	}
	if containsURLUnsafeText(template) || containsForbiddenNoNameTerm(template) {
		return errors.New("asset_url_template must be a safe public HTTPS asset URL template")
	}
	if strings.Count(template, "{release_tag}") != 1 || strings.Count(template, "{asset_name}") != 1 {
		return errors.New("asset_url_template must contain release_tag and asset_name placeholders")
	}
	if strings.Count(template, "{") != 2 || strings.Count(template, "}") != 2 {
		return errors.New("asset_url_template contains unsupported placeholders")
	}
	probe := strings.ReplaceAll(template, "{release_tag}", "v0.0.0-alpha")
	probe = strings.ReplaceAll(probe, "{asset_name}", "asset-alpha001.pack.zip")
	parsed, err := url.Parse(probe)
	if err != nil {
		return errors.New("asset_url_template is malformed")
	}
	if err := validateProductionAssetURL(parsed); err != nil {
		return err
	}

	return validateFullPackAssetURL(parsed)
}

func renderFullPackLock(metadata fullPackMetadataFile) (lockFile, error) {
	if err := validateFullPackMetadata(metadata); err != nil {
		return lockFile{}, err
	}

	lock := lockFile{SchemaVersion: lockSchemaVersion, Packs: make([]packLockEntry, 0, len(metadata.Packs))}
	for _, pack := range metadata.Packs {
		assetURL, err := releaseAssetURL(metadata, pack.AssetName)
		if err != nil {
			return lockFile{}, err
		}
		lock.Packs = append(lock.Packs, packLockEntry{
			PackID:          pack.PackID,
			PackVersion:     pack.PackVersion,
			AssetURL:        assetURL,
			AssetSHA256:     pack.AssetSHA256,
			PublicKeys:      append([]string(nil), pack.PublicKeys...),
			ManifestSHA256:  pack.ManifestSHA256,
			PayloadSHA256:   pack.PayloadSHA256,
			SignatureSHA256: pack.SignatureSHA256,
		})
	}

	return lock, nil
}

func renderedFullPackLockForVerification(metadata fullPackMetadataFile, tempLockPath string, localAssetDir string) (lockFile, error) {
	if strings.TrimSpace(localAssetDir) == "" {
		return renderFullPackLock(metadata)
	}

	return renderLocalFullPackLock(metadata, tempLockPath, localAssetDir)
}

func renderLocalFullPackLock(metadata fullPackMetadataFile, tempLockPath string, localAssetDir string) (lockFile, error) {
	if err := validateFullPackMetadata(metadata); err != nil {
		return lockFile{}, err
	}
	assetPaths, err := resolveLocalFullPackAssetPaths(tempLockPath, localAssetDir, metadata.Packs)
	if err != nil {
		return lockFile{}, err
	}

	lock := lockFile{SchemaVersion: lockSchemaVersion, Packs: make([]packLockEntry, 0, len(metadata.Packs))}
	for i, pack := range metadata.Packs {
		lock.Packs = append(lock.Packs, packLockEntry{
			PackID:          pack.PackID,
			PackVersion:     pack.PackVersion,
			AssetPath:       assetPaths[i],
			AssetSHA256:     pack.AssetSHA256,
			PublicKeys:      append([]string(nil), pack.PublicKeys...),
			ManifestSHA256:  pack.ManifestSHA256,
			PayloadSHA256:   pack.PayloadSHA256,
			SignatureSHA256: pack.SignatureSHA256,
		})
	}

	return lock, nil
}

func resolveLocalFullPackAssetPaths(tempLockPath string, localAssetDir string, packs []fullPackMetadataEntry) ([]string, error) {
	if strings.TrimSpace(localAssetDir) != localAssetDir || localAssetDir == "" {
		return nil, errors.New("local full-pack asset directory is invalid")
	}
	lockDir, err := canonicalPathForGuard(filepath.Dir(tempLockPath))
	if err != nil {
		return nil, errors.New("local full-pack asset directory is invalid")
	}
	assetDir, err := canonicalPathForGuard(localAssetDir)
	if err != nil {
		return nil, errors.New("local full-pack asset directory is invalid")
	}
	if !isPathUnderGuardDir(assetDir, lockDir) {
		return nil, errors.New("local full-pack asset directory is invalid")
	}

	resolved := make([]string, 0, len(packs))
	for _, pack := range packs {
		assetPath := filepath.Join(assetDir, pack.AssetName)
		if !isPathUnderGuardDir(assetPath, assetDir) {
			return nil, errors.New("local full-pack asset directory is invalid")
		}
		rel, err := filepath.Rel(lockDir, assetPath)
		if err != nil || rel == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("local full-pack asset directory is invalid")
		}
		resolved = append(resolved, filepath.ToSlash(rel))
	}

	return resolved, nil
}

func releaseAssetURL(metadata fullPackMetadataFile, assetName string) (string, error) {
	var rawURL string
	if metadata.BaseAssetURL != "" {
		parsed, err := url.Parse(metadata.BaseAssetURL)
		if err != nil {
			return "", errors.New("base_asset_url is malformed")
		}
		appendAssetNameToURLPath(parsed, assetName)
		rawURL = parsed.String()
	} else {
		rawURL = strings.ReplaceAll(metadata.AssetURLTemplate, "{release_tag}", metadata.ReleaseTag)
		rawURL = strings.ReplaceAll(rawURL, "{asset_name}", url.PathEscape(assetName))
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("rendered asset_url is malformed")
	}
	if err := validateProductionAssetURL(parsed); err != nil {
		return "", err
	}
	if err := validateFullPackAssetURL(parsed); err != nil {
		return "", err
	}

	return parsed.String(), nil
}

func appendAssetNameToURLPath(parsed *url.URL, assetName string) {
	escapedAsset := url.PathEscape(assetName)
	if parsed.EscapedPath() == "" || parsed.EscapedPath() == "/" {
		parsed.RawPath = ""
		parsed.Path = "/" + assetName

		return
	}

	escapedPath := strings.TrimRight(parsed.EscapedPath(), "/") + "/" + escapedAsset
	parsed.RawPath = escapedPath
	unescaped, err := url.PathUnescape(escapedPath)
	if err != nil {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + assetName

		return
	}
	parsed.Path = unescaped
}

func validateFullPackAssetURL(parsed *url.URL) error {
	if hasUnsafeURLPath(parsed.EscapedPath()) || hasUnsafeURLPath(parsed.Path) {
		return errors.New("full-pack asset URL path must be safe")
	}
	if err := validateFullPackHost(parsed); err != nil {
		return err
	}

	return nil
}

func hasUnsafeURLPath(urlPath string) bool {
	if urlPath == "" {
		return false
	}
	if strings.Contains(urlPath, "\\") {
		return true
	}
	segments := strings.Split(urlPath, "/")
	for _, segment := range segments {
		if segment == "." || segment == ".." {
			return true
		}
		for i := 0; i < 2; i++ {
			value, err := url.PathUnescape(segment)
			if err != nil {
				return true
			}
			if value == segment {
				break
			}
			segment = value
		}
		if segment == "." || segment == ".." || strings.Contains(segment, "/") || strings.Contains(segment, "\\") {
			return true
		}
	}

	return false
}

func validateFullPackHost(parsed *url.URL) error {
	host := parsed.Hostname()
	if host == "" {
		return errors.New("full-pack asset URL host must be safe")
	}
	if strings.EqualFold(host, "localhost") {
		return errors.New("full-pack asset URL host must not be localhost")
	}
	if ip := net.ParseIP(host); ip != nil {
		return errors.New("full-pack asset URL host must not be an IP literal")
	}

	return nil
}

func marshalRenderedFullPackLock(lock lockFile) ([]byte, error) {
	bytes, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode rendered lock: %w", err)
	}

	return append(bytes, '\n'), nil
}

func rejectProductionLockOutput(lockPath string) error {
	if strings.TrimSpace(lockPath) == "" {
		return errors.New("temporary lock path is invalid")
	}
	protectedDir := filepath.Join("build", "extractor")
	cleanSlash := strings.ToLower(strings.ReplaceAll(filepath.ToSlash(filepath.Clean(lockPath)), "\\", "/"))
	if cleanSlash == "build/extractor/packs.lock.json" || strings.HasSuffix(cleanSlash, "/build/extractor/packs.lock.json") || strings.HasSuffix(cleanSlash, "\\build\\extractor\\packs.lock.json") {
		return errors.New("full-pack commands refuse to write the tracked production lock")
	}
	cleaned, err := canonicalPathForGuard(lockPath)
	if err != nil {
		return errors.New("temporary lock path is invalid")
	}
	production, err := canonicalPathForGuard(filepath.Join(protectedDir, "packs.lock.json"))
	if err != nil {
		return errors.New("temporary lock path is invalid")
	}
	protected, err := canonicalPathForGuard(protectedDir)
	if err != nil {
		return errors.New("temporary lock path is invalid")
	}
	if sameGuardPath(cleaned, production) || isPathUnderGuardDir(cleaned, protected) && guardComparablePath(filepath.Base(cleaned)) == "packs.lock.json" {
		return errors.New("full-pack commands refuse to write the tracked production lock")
	}
	if resolved, err := filepath.EvalSymlinks(lockPath); err == nil {
		resolvedCanonical, err := canonicalPathForGuard(resolved)
		if err != nil {
			return errors.New("temporary lock path is invalid")
		}
		if sameGuardPath(resolvedCanonical, production) || isPathUnderGuardDir(resolvedCanonical, protected) && guardComparablePath(filepath.Base(resolvedCanonical)) == "packs.lock.json" {
			return errors.New("full-pack commands refuse to write the tracked production lock")
		}
	}

	return nil
}

func canonicalPathForGuard(filePath string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(filePath))
	if err != nil {
		return "", err
	}

	return filepath.Clean(absolute), nil
}

func sameGuardPath(a string, b string) bool {
	return guardComparablePath(a) == guardComparablePath(b)
}

func isPathUnderGuardDir(filePath string, dir string) bool {
	fileComparable := guardComparablePath(filePath)
	dirComparable := guardComparablePath(dir)

	return fileComparable == dirComparable || strings.HasPrefix(fileComparable, dirComparable+"/")
}

func guardComparablePath(filePath string) string {
	cleaned := filepath.Clean(filePath)
	cleaned = filepath.ToSlash(cleaned)
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")
	components := strings.Split(cleaned, "/")
	for i, component := range components {
		components[i] = strings.TrimRight(component, ". ")
	}
	cleaned = strings.Join(components, "/")

	return strings.TrimRight(strings.ToLower(cleaned), "/")
}

func auditNoNameBytes(surface string, data []byte) error {
	if containsForbiddenNoNameTerm(string(data)) {
		return fmt.Errorf("%s no-name audit failed", surface)
	}

	return nil
}

func containsForbiddenNoNameTerm(value string) bool {
	normalized := strings.ToLower(value)
	normalized = assetURLTemplateKey.ReplaceAllString(normalized, `"":`)
	for _, term := range []string{
		"policy_private_sha256",
		"private_policy",
		"domain_policy_refs",
		"broker_policy_refs",
		"url_template",
		"auth_profile",
		"auth_scope",
		"supported_site",
		"provider",
		"private",
		"secret",
		"token",
		"cookie",
		"authorization",
		"protected-root-marker",
		"protected-temp-root",
	} {
		if strings.Contains(normalized, term) {
			return true
		}
	}

	return false
}

func containsURLUnsafeText(value string) bool {
	if strings.ContainsAny(value, "\r\n\t ") {
		return true
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}

	return false
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
		Manifest:              verified.Manifest,
		Identity:              verified.Identity,
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

	return fetchHTTPSAsset(opts.HTTPClient, parsed.String(), opts.SameOrigin)
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

func fetchHTTPSAsset(client *http.Client, rawURL string, sameOrigin bool) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	client = cloneHTTPClientWithRedirectCheck(client, sameOrigin)

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

func cloneHTTPClientWithRedirectCheck(client *http.Client, sameOrigin bool) *http.Client {
	cloned := *client
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := validateProductionAssetURL(req.URL); err != nil {
			return errUnsafeAssetRedirect
		}
		if sameOrigin && len(via) > 0 && via[0] != nil && !sameURLOrigin(via[0].URL, req.URL) {
			return errUnsafeAssetRedirect
		}

		return nil
	}
	if cloned.Timeout == 0 {
		cloned.Timeout = defaultHTTPTimeout
	}

	return &cloned
}

func sameURLOrigin(a *url.URL, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}

	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
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
