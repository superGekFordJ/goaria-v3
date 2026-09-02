package extractor

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
)

const (
	privateAuthRuntimeBundleInvalidError      = "private auth runtime bundle is invalid"
	privateAuthRuntimeBundlePathEnv           = "GOARIA_EXTRACTOR_PRIVATE_AUTH_RUNTIME_BUNDLE"
	privateAuthRuntimeBundleExpectedSHA256Env = "GOARIA_EXTRACTOR_PRIVATE_AUTH_RUNTIME_SHA256"

	privateAuthRuntimeMaxLoginTimeoutMillis   = 10 * 60 * 1000
	privateAuthRuntimeMaxCallbackBodyBytes    = 64 * 1024
	privateAuthRuntimeMaxCollectorJSBytes     = 64 * 1024
	privateAuthRuntimeCallbackTransportMode   = "local_post"
	privateAuthRuntimeCallbackContentTypeJSON = "application/json"
	privateAuthRuntimeCaptureFormatJSON       = "json"
)

var (
	embeddedPrivateAuthRuntimeBundleJSON   []byte
	embeddedPrivateAuthRuntimeBundleSHA256 string
)

type PrivateAuthRuntimeBundleLoadOptions struct {
	ExpectedAuthRuntimePrivateSHA256     string
	ExpectedAuthRuntimePublicFingerprint string
}

type PrivateAuthRuntimeBundle struct {
	packOrder []VerifiedPackIdentity
	packs     map[VerifiedPackIdentity]PrivateAuthRuntimePack
}

type PrivateAuthRuntimePack struct {
	PackIdentity    VerifiedPackIdentity
	StoreBinding    PrivateAuthRuntimeStoreBinding
	Profiles        []PrivateAuthRuntimeProfile
	Preflight       PrivateAuthRuntimePreflightPolicy
	Provisioning    PrivateAuthRuntimeProvisioningPolicy
	Materialization PrivateAuthRuntimeMaterializationPolicy
	Normalization   PrivateAuthRuntimeNormalizationPolicy
}

type PrivateAuthRuntimeProfile struct {
	ProfileRef AuthProfileID
	Kind       AuthSecretKind
	Login      PrivateAuthRuntimeLoginDescriptor
}

type PrivateAuthRuntimeStoreBinding struct {
	Scope       string
	ProfileRefs []AuthProfileID
}

type PrivateAuthRuntimePreflightPolicy struct {
	Mode    string
	Missing string
	Expired string
}

type PrivateAuthRuntimeProvisioningPolicy struct {
	Mode        string
	ProfileRefs []AuthProfileID
}

type PrivateAuthRuntimeMaterializationPolicy struct {
	ProfileRefs []AuthProfileID
}

type PrivateAuthRuntimeNormalizationPolicy struct {
	RejectCRLF bool
	TrimSpace  bool
}

type PrivateAuthRuntimeLoginDescriptor struct {
	URL                 string
	AllowedDomains      []DomainRule
	TimeoutMillis       int
	CallbackTransport   PrivateAuthRuntimeCallbackTransport
	CollectorJS         string
	Capture             PrivateAuthRuntimeCaptureContract
	callbackConfigured  bool
	collectorConfigured bool
	captureConfigured   bool
}

type PrivateAuthRuntimeCallbackTransport struct {
	Mode         string
	ContentTypes []string
	MaxBodyBytes int64
}

type PrivateAuthRuntimeCaptureContract struct {
	Format               string
	SecretCandidates     []string
	KindField            string
	ExpiresAtField       string
	RedactedDisplayField string
}

type privateAuthRuntimeBundleEnvelopeDTO struct {
	SchemaVersion                int             `json:"schema_version"`
	BundleID                     string          `json:"bundle_id"`
	BundleVersion                string          `json:"bundle_version"`
	AuthRuntimePrivateSHA256     string          `json:"auth_runtime_private_sha256"`
	AuthRuntimePublicFingerprint string          `json:"auth_runtime_public_fingerprint,omitempty"`
	Runtime                      json.RawMessage `json:"runtime"`
}

type privateAuthRuntimeDTO struct {
	Packs []privateAuthRuntimePackDTO `json:"packs"`
}

type privateAuthRuntimePackDTO struct {
	VerifiedPackIdentity privateAuthRuntimeIdentityDTO              `json:"verified_pack_identity"`
	StoreBinding         privateAuthRuntimeStoreBindingDTO          `json:"store_binding"`
	Profiles             []privateAuthRuntimeProfileDTO             `json:"profiles"`
	Preflight            privateAuthRuntimePreflightPolicyDTO       `json:"preflight"`
	Provisioning         privateAuthRuntimeProvisioningPolicyDTO    `json:"provisioning"`
	Materialization      privateAuthRuntimeMaterializationPolicyDTO `json:"materialization"`
	Normalization        privateAuthRuntimeNormalizationPolicyDTO   `json:"normalization"`
}

type privateAuthRuntimeIdentityDTO struct {
	PackID          string `json:"pack_id"`
	PackVersion     string `json:"pack_version"`
	AssetSHA256     string `json:"asset_sha256"`
	ManifestSHA256  string `json:"manifest_sha256"`
	PayloadSHA256   string `json:"payload_sha256"`
	SignatureSHA256 string `json:"signature_sha256"`
	PublicKeySHA256 string `json:"public_key_sha256"`
}

type privateAuthRuntimeProfileDTO struct {
	ProfileRef string                               `json:"profile_ref"`
	Kind       AuthSecretKind                       `json:"kind"`
	Login      privateAuthRuntimeLoginDescriptorDTO `json:"login"`
}

type privateAuthRuntimeStoreBindingDTO struct {
	Scope       string   `json:"scope"`
	ProfileRefs []string `json:"profile_refs"`
}

type privateAuthRuntimePreflightPolicyDTO struct {
	Mode    string  `json:"mode"`
	Missing *string `json:"missing"`
	Expired *string `json:"expired"`
}

type privateAuthRuntimeProvisioningPolicyDTO struct {
	Mode        string   `json:"mode"`
	ProfileRefs []string `json:"profile_refs"`
}

type privateAuthRuntimeMaterializationPolicyDTO struct {
	ProfileRefs []string `json:"profile_refs"`
}

type privateAuthRuntimeNormalizationPolicyDTO struct {
	RejectCRLF *bool `json:"reject_crlf"`
	TrimSpace  *bool `json:"trim_space"`
}

type privateAuthRuntimeLoginDescriptorDTO struct {
	URL               string                                  `json:"url"`
	AllowedDomains    *[]DomainRule                           `json:"allowed_domains"`
	TimeoutMillis     *int                                    `json:"timeout_millis"`
	CallbackTransport *privateAuthRuntimeCallbackTransportDTO `json:"callback_transport"`
	CollectorJS       *string                                 `json:"collector_js"`
	Capture           *privateAuthRuntimeCaptureContractDTO   `json:"capture"`
}

type privateAuthRuntimeCallbackTransportDTO struct {
	Mode         string   `json:"mode"`
	ContentTypes []string `json:"content_types"`
	MaxBodyBytes int64    `json:"max_body_bytes"`
}

type privateAuthRuntimeCaptureContractDTO struct {
	Format               string   `json:"format"`
	SecretCandidates     []string `json:"secret_candidates"`
	KindField            string   `json:"kind_field,omitempty"`
	ExpiresAtField       string   `json:"expires_at_field,omitempty"`
	RedactedDisplayField string   `json:"redacted_display_field,omitempty"`
}

func NewPrivateAuthRuntimeBundle(raw []byte, opts PrivateAuthRuntimeBundleLoadOptions) (*PrivateAuthRuntimeBundle, error) {
	bundle, err := newPrivateAuthRuntimeBundle(raw, opts)
	if err != nil {
		return nil, errors.New(privateAuthRuntimeBundleInvalidError)
	}

	return bundle, nil
}

func LoadPrivateAuthRuntimeBundleFromFile(path string, opts PrivateAuthRuntimeBundleLoadOptions) (*PrivateAuthRuntimeBundle, error) {
	if path == "" {
		return nil, errors.New(privateAuthRuntimeBundleInvalidError)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New(privateAuthRuntimeBundleInvalidError)
	}

	return NewPrivateAuthRuntimeBundle(raw, opts)
}

func PrivateAuthRuntimeBundleRuntimeSourceState() PrivateBundleSourceState {
	return classifyRuntimeSourceState(os.Getenv(privateAuthRuntimeBundlePathEnv) != "", len(embeddedPrivateAuthRuntimeBundleJSON) > 0)
}

func LoadPrivateAuthRuntimeBundleFromRuntimeSources() (*PrivateAuthRuntimeBundle, error) {
	envPath := os.Getenv(privateAuthRuntimeBundlePathEnv)
	envExpectedSHA256 := os.Getenv(privateAuthRuntimeBundleExpectedSHA256Env)
	sourceState := PrivateAuthRuntimeBundleRuntimeSourceState()

	switch sourceState {
	case PrivateBundleSourceStateNone:
		return nil, nil
	case PrivateBundleSourceStateAmbiguous:
		return nil, errors.New(privateAuthRuntimeBundleInvalidError)
	case PrivateBundleSourceStateEnv:
		return LoadPrivateAuthRuntimeBundleFromFile(envPath, PrivateAuthRuntimeBundleLoadOptions{
			ExpectedAuthRuntimePrivateSHA256: envExpectedSHA256,
		})
	case PrivateBundleSourceStateEmbedded:
		return NewPrivateAuthRuntimeBundle(embeddedPrivateAuthRuntimeBundleJSON, PrivateAuthRuntimeBundleLoadOptions{
			ExpectedAuthRuntimePrivateSHA256: embeddedPrivateAuthRuntimeBundleSHA256,
		})
	default:
		return nil, errors.New(privateAuthRuntimeBundleInvalidError)
	}
}

func (b *PrivateAuthRuntimeBundle) PackCount() int {
	if b == nil {
		return 0
	}

	return len(b.packs)
}

func (b *PrivateAuthRuntimeBundle) PackIdentities() []VerifiedPackIdentity {
	if b == nil || len(b.packOrder) == 0 {
		return nil
	}

	return append([]VerifiedPackIdentity(nil), b.packOrder...)
}

func (b *PrivateAuthRuntimeBundle) PackRuntime(identity VerifiedPackIdentity) (PrivateAuthRuntimePack, bool) {
	if b == nil || len(b.packs) == 0 {
		return PrivateAuthRuntimePack{}, false
	}
	pack, ok := b.packs[identity]
	if !ok {
		return PrivateAuthRuntimePack{}, false
	}

	return clonePrivateAuthRuntimePack(pack), true
}

func newPrivateAuthRuntimeBundle(raw []byte, opts PrivateAuthRuntimeBundleLoadOptions) (*PrivateAuthRuntimeBundle, error) {
	raw = cloneBytes(raw)
	if len(raw) == 0 {
		return nil, errors.New("empty auth runtime bundle")
	}

	var envelope privateAuthRuntimeBundleEnvelopeDTO
	if err := decodePrivateAuthRuntimeBundleJSON(raw, &envelope); err != nil {
		return nil, err
	}
	if err := validatePrivateAuthRuntimeBundleEnvelope(envelope, opts); err != nil {
		return nil, err
	}

	var runtime privateAuthRuntimeDTO
	if err := decodePrivateAuthRuntimeBundleJSON(envelope.Runtime, &runtime); err != nil {
		return nil, err
	}
	if len(runtime.Packs) == 0 {
		return nil, errors.New("private auth runtime bundle has no packs")
	}

	packs := make(map[VerifiedPackIdentity]PrivateAuthRuntimePack, len(runtime.Packs))
	packOrder := make([]VerifiedPackIdentity, 0, len(runtime.Packs))
	for _, packDTO := range runtime.Packs {
		identity := packDTO.VerifiedPackIdentity.verifiedPackIdentity()
		if err := validatePrivateAuthRuntimeBundleIdentity(identity); err != nil {
			return nil, err
		}
		if _, ok := packs[identity]; ok {
			return nil, errors.New("private auth runtime bundle contains duplicate pack identity")
		}

		pack, err := privateAuthRuntimePackFromDTO(identity, packDTO)
		if err != nil {
			return nil, err
		}
		packs[identity] = clonePrivateAuthRuntimePack(pack)
		packOrder = append(packOrder, identity)
	}

	return &PrivateAuthRuntimeBundle{packOrder: packOrder, packs: packs}, nil
}

func decodePrivateAuthRuntimeBundleJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("private auth runtime bundle contains trailing JSON data")
	}

	return nil
}

func validatePrivateAuthRuntimeBundleEnvelope(envelope privateAuthRuntimeBundleEnvelopeDTO, opts PrivateAuthRuntimeBundleLoadOptions) error {
	if envelope.SchemaVersion != 1 {
		return errors.New("private auth runtime bundle schema version is unsupported")
	}
	if err := validateOpaquePolicyRef("bundle_id", envelope.BundleID); err != nil {
		return err
	}
	if err := validatePrivatePolicyBundleVersion(envelope.BundleVersion); err != nil {
		return err
	}
	if len(envelope.Runtime) == 0 {
		return errors.New("private auth runtime bundle runtime is empty")
	}
	if err := validateLowerHexSHA256Field("auth_runtime_private_sha256", envelope.AuthRuntimePrivateSHA256); err != nil {
		return err
	}
	if sha256HexString(envelope.Runtime) != envelope.AuthRuntimePrivateSHA256 {
		return errors.New("private auth runtime bundle runtime hash mismatch")
	}
	if err := validateExpectedPrivateAuthRuntimeSHA256(opts.ExpectedAuthRuntimePrivateSHA256, envelope.AuthRuntimePrivateSHA256); err != nil {
		return err
	}
	if envelope.AuthRuntimePublicFingerprint != "" {
		if err := validateLowerHexSHA256Field("auth_runtime_public_fingerprint", envelope.AuthRuntimePublicFingerprint); err != nil {
			return err
		}
	}
	if err := validateExpectedAuthRuntimePublicFingerprint(opts.ExpectedAuthRuntimePublicFingerprint, envelope.AuthRuntimePublicFingerprint); err != nil {
		return err
	}

	return nil
}

func validateExpectedPrivateAuthRuntimeSHA256(expected string, actual string) error {
	if expected == "" {
		return nil
	}
	if strings.TrimSpace(expected) != expected {
		return errors.New("expected private auth runtime sha256 is invalid")
	}
	if err := validateLowerHexSHA256Field("expected_auth_runtime_private_sha256", expected); err != nil {
		return err
	}
	if expected != actual {
		return errors.New("expected private auth runtime sha256 does not match")
	}

	return nil
}

func validateExpectedAuthRuntimePublicFingerprint(expected string, actual string) error {
	if expected == "" {
		return nil
	}
	if strings.TrimSpace(expected) != expected {
		return errors.New("expected auth runtime public fingerprint is invalid")
	}
	if err := validateLowerHexSHA256Field("expected_auth_runtime_public_fingerprint", expected); err != nil {
		return err
	}
	if expected != actual {
		return errors.New("expected auth runtime public fingerprint does not match")
	}

	return nil
}

func validatePrivateAuthRuntimeBundleIdentity(identity VerifiedPackIdentity) error {
	if err := validatePackID(identity.PackID); err != nil {
		return err
	}
	if err := validatePackVersion(identity.PackVersion); err != nil {
		return err
	}
	if err := validateLowerHexSHA256Field("asset_sha256", identity.AssetSHA256); err != nil {
		return err
	}
	if err := validateLowerHexSHA256Field("manifest_sha256", identity.ManifestSHA256); err != nil {
		return err
	}
	if err := validateLowerHexSHA256Field("payload_sha256", identity.PayloadSHA256); err != nil {
		return err
	}
	if err := validateLowerHexSHA256Field("signature_sha256", identity.SignatureSHA256); err != nil {
		return err
	}
	if err := validateLowerHexSHA256Field("public_key_sha256", identity.PublicKeySHA256); err != nil {
		return err
	}

	return nil
}

func (dto privateAuthRuntimeIdentityDTO) verifiedPackIdentity() VerifiedPackIdentity {
	return VerifiedPackIdentity(dto)
}

func privateAuthRuntimePackFromDTO(identity VerifiedPackIdentity, dto privateAuthRuntimePackDTO) (PrivateAuthRuntimePack, error) {
	profiles, profileByRef, err := privateAuthRuntimeProfilesFromDTO(dto.Profiles)
	if err != nil {
		return PrivateAuthRuntimePack{}, err
	}
	storeBinding, storeRefs, err := privateAuthRuntimeStoreBindingFromDTO(dto.StoreBinding, profileByRef)
	if err != nil {
		return PrivateAuthRuntimePack{}, err
	}
	provisioning, provisioningRefs, err := privateAuthRuntimeProvisioningFromDTO(dto.Provisioning, profileByRef)
	if err != nil {
		return PrivateAuthRuntimePack{}, err
	}
	materialization, materializationRefs, err := privateAuthRuntimeMaterializationFromDTO(dto.Materialization, profileByRef)
	if err != nil {
		return PrivateAuthRuntimePack{}, err
	}
	preflight, err := privateAuthRuntimePreflightFromDTO(dto.Preflight)
	if err != nil {
		return PrivateAuthRuntimePack{}, err
	}
	normalization, err := privateAuthRuntimeNormalizationFromDTO(dto.Normalization)
	if err != nil {
		return PrivateAuthRuntimePack{}, err
	}
	if err := validatePrivateAuthRuntimeCrossSection(preflight, provisioning, provisioningRefs, materializationRefs, storeRefs); err != nil {
		return PrivateAuthRuntimePack{}, err
	}

	return PrivateAuthRuntimePack{
		PackIdentity:    identity,
		StoreBinding:    storeBinding,
		Profiles:        profiles,
		Preflight:       preflight,
		Provisioning:    provisioning,
		Materialization: materialization,
		Normalization:   normalization,
	}, nil
}

func privateAuthRuntimeProfilesFromDTO(dtos []privateAuthRuntimeProfileDTO) ([]PrivateAuthRuntimeProfile, map[AuthProfileID]PrivateAuthRuntimeProfile, error) {
	if len(dtos) == 0 {
		return nil, nil, errors.New("private auth runtime profiles are required")
	}

	profiles := make([]PrivateAuthRuntimeProfile, 0, len(dtos))
	profileByRef := make(map[AuthProfileID]PrivateAuthRuntimeProfile, len(dtos))
	for _, dto := range dtos {
		profileRef := AuthProfileID(dto.ProfileRef)
		if err := validateAuthProfileID(profileRef); err != nil {
			return nil, nil, err
		}
		if _, ok := profileByRef[profileRef]; ok {
			return nil, nil, errors.New("private auth runtime contains duplicate profile ref")
		}
		if err := validateAuthSecretKind(dto.Kind); err != nil {
			return nil, nil, err
		}
		login, err := privateAuthRuntimeLoginFromDTO(dto.Login)
		if err != nil {
			return nil, nil, err
		}
		profile := PrivateAuthRuntimeProfile{ProfileRef: profileRef, Kind: dto.Kind, Login: login}
		profiles = append(profiles, profile)
		profileByRef[profileRef] = clonePrivateAuthRuntimeProfile(profile)
	}

	return profiles, profileByRef, nil
}

func privateAuthRuntimeLoginFromDTO(dto privateAuthRuntimeLoginDescriptorDTO) (PrivateAuthRuntimeLoginDescriptor, error) {
	if dto.URL != "" {
		if err := validatePrivateAuthRuntimeLoginURL(dto.URL); err != nil {
			return PrivateAuthRuntimeLoginDescriptor{}, err
		}
	}
	allowedDomains := []DomainRule(nil)
	if dto.AllowedDomains != nil {
		if err := validateDomainRules(*dto.AllowedDomains); err != nil {
			return PrivateAuthRuntimeLoginDescriptor{}, err
		}
		allowedDomains = cloneDomainRules(*dto.AllowedDomains)
	}
	timeoutMillis := 0
	if dto.TimeoutMillis != nil {
		if *dto.TimeoutMillis <= 0 || *dto.TimeoutMillis > privateAuthRuntimeMaxLoginTimeoutMillis {
			return PrivateAuthRuntimeLoginDescriptor{}, errors.New("private auth runtime login timeout is invalid")
		}
		timeoutMillis = *dto.TimeoutMillis
	}
	var callbackTransport PrivateAuthRuntimeCallbackTransport
	callbackConfigured := false
	if dto.CallbackTransport != nil {
		var err error
		callbackTransport, err = privateAuthRuntimeCallbackTransportFromDTO(*dto.CallbackTransport)
		if err != nil {
			return PrivateAuthRuntimeLoginDescriptor{}, err
		}
		callbackConfigured = true
	}
	collectorJS := ""
	collectorConfigured := false
	if dto.CollectorJS != nil {
		var err error
		collectorJS, err = validatePrivateAuthRuntimeCollectorJS(*dto.CollectorJS)
		if err != nil {
			return PrivateAuthRuntimeLoginDescriptor{}, err
		}
		collectorConfigured = true
	}
	var capture PrivateAuthRuntimeCaptureContract
	captureConfigured := false
	if dto.Capture != nil {
		var err error
		capture, err = privateAuthRuntimeCaptureFromDTO(*dto.Capture)
		if err != nil {
			return PrivateAuthRuntimeLoginDescriptor{}, err
		}
		captureConfigured = true
	}

	return PrivateAuthRuntimeLoginDescriptor{
		URL:                 dto.URL,
		AllowedDomains:      allowedDomains,
		TimeoutMillis:       timeoutMillis,
		CallbackTransport:   callbackTransport,
		CollectorJS:         collectorJS,
		Capture:             capture,
		callbackConfigured:  callbackConfigured,
		collectorConfigured: collectorConfigured,
		captureConfigured:   captureConfigured,
	}, nil
}

func privateAuthRuntimeCallbackTransportFromDTO(dto privateAuthRuntimeCallbackTransportDTO) (PrivateAuthRuntimeCallbackTransport, error) {
	if dto.Mode != privateAuthRuntimeCallbackTransportMode {
		return PrivateAuthRuntimeCallbackTransport{}, errors.New("private auth runtime callback transport mode is invalid")
	}
	if dto.MaxBodyBytes <= 0 || dto.MaxBodyBytes > privateAuthRuntimeMaxCallbackBodyBytes {
		return PrivateAuthRuntimeCallbackTransport{}, errors.New("private auth runtime callback body limit is invalid")
	}
	if len(dto.ContentTypes) == 0 {
		return PrivateAuthRuntimeCallbackTransport{}, errors.New("private auth runtime callback content types are required")
	}
	contentTypes := make([]string, 0, len(dto.ContentTypes))
	seen := make(map[string]struct{}, len(dto.ContentTypes))
	for _, contentType := range dto.ContentTypes {
		if contentType != privateAuthRuntimeCallbackContentTypeJSON {
			return PrivateAuthRuntimeCallbackTransport{}, errors.New("private auth runtime callback content type is invalid")
		}
		if _, ok := seen[contentType]; ok {
			return PrivateAuthRuntimeCallbackTransport{}, errors.New("private auth runtime callback content types contain duplicates")
		}
		seen[contentType] = struct{}{}
		contentTypes = append(contentTypes, contentType)
	}

	return PrivateAuthRuntimeCallbackTransport{Mode: dto.Mode, ContentTypes: contentTypes, MaxBodyBytes: dto.MaxBodyBytes}, nil
}

func validatePrivateAuthRuntimeCollectorJS(source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", errors.New("private auth runtime collector source is required")
	}
	if len([]byte(source)) > privateAuthRuntimeMaxCollectorJSBytes || strings.ContainsRune(source, '\x00') {
		return "", errors.New("private auth runtime collector source is invalid")
	}

	return source, nil
}

func privateAuthRuntimeCaptureFromDTO(dto privateAuthRuntimeCaptureContractDTO) (PrivateAuthRuntimeCaptureContract, error) {
	if dto.Format != privateAuthRuntimeCaptureFormatJSON {
		return PrivateAuthRuntimeCaptureContract{}, errors.New("private auth runtime capture format is invalid")
	}
	if len(dto.SecretCandidates) == 0 {
		return PrivateAuthRuntimeCaptureContract{}, errors.New("private auth runtime capture candidates are required")
	}
	secretCandidates := make([]string, 0, len(dto.SecretCandidates))
	seen := make(map[string]struct{}, len(dto.SecretCandidates))
	for _, candidate := range dto.SecretCandidates {
		if err := validatePrivateAuthRuntimeCaptureFieldPath(candidate); err != nil {
			return PrivateAuthRuntimeCaptureContract{}, err
		}
		if _, ok := seen[candidate]; ok {
			return PrivateAuthRuntimeCaptureContract{}, errors.New("private auth runtime capture candidates contain duplicates")
		}
		seen[candidate] = struct{}{}
		secretCandidates = append(secretCandidates, candidate)
	}
	for _, optional := range []string{dto.KindField, dto.ExpiresAtField, dto.RedactedDisplayField} {
		if optional == "" {
			continue
		}
		if err := validatePrivateAuthRuntimeCaptureFieldPath(optional); err != nil {
			return PrivateAuthRuntimeCaptureContract{}, err
		}
	}

	return PrivateAuthRuntimeCaptureContract{
		Format:               dto.Format,
		SecretCandidates:     secretCandidates,
		KindField:            dto.KindField,
		ExpiresAtField:       dto.ExpiresAtField,
		RedactedDisplayField: dto.RedactedDisplayField,
	}, nil
}

func validatePrivateAuthRuntimeCaptureFieldPath(path string) error {
	if path == "" || len(path) > 128 || strings.ContainsAny(path, " \t\r\n\x00*[]") || strings.Contains(path, "..") {
		return errors.New("private auth runtime capture field path is invalid")
	}
	parts := strings.Split(path, ".")
	if len(parts) > 8 {
		return errors.New("private auth runtime capture field path is invalid")
	}
	for _, part := range parts {
		if !isPrivateAuthRuntimeCaptureIdentifier(part) {
			return errors.New("private auth runtime capture field path is invalid")
		}
	}

	return nil
}

func isPrivateAuthRuntimeCaptureIdentifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		case c == '_':
			if i == 0 || i == len(value)-1 || value[i-1] == '_' {
				return false
			}
		default:
			return false
		}
	}
	last := value[len(value)-1]

	return last >= 'a' && last <= 'z' || last >= '0' && last <= '9'
}

func validatePrivateAuthRuntimeLoginURL(rawURL string) error {
	if rawURL == "" || strings.TrimSpace(rawURL) != rawURL || strings.ContainsAny(rawURL, "\r\n\x00") {
		return errors.New("private auth runtime login url is invalid")
	}
	parsed, _, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return errors.New("private auth runtime login url is invalid")
	}
	if parsed.Scheme != "https" {
		return errors.New("private auth runtime login url must use https")
	}
	if privateAuthRuntimeURLPathHasTraversal(parsed.EscapedPath()) || privateAuthRuntimeURLPathHasTraversal(parsed.Path) {
		return errors.New("private auth runtime login url path is invalid")
	}

	return nil
}

func privateAuthRuntimeURLPathHasTraversal(urlPath string) bool {
	if urlPath == "" {
		return false
	}
	if strings.Contains(urlPath, "\\") {
		return true
	}
	for segment := range strings.SplitSeq(urlPath, "/") {
		for range 2 {
			decoded, err := url.PathUnescape(segment)
			if err != nil {
				return true
			}
			if decoded == segment {
				break
			}
			segment = decoded
		}
		if segment == "." || segment == ".." || strings.Contains(segment, "/") || strings.Contains(segment, "\\") {
			return true
		}
	}

	return false
}

func privateAuthRuntimeStoreBindingFromDTO(dto privateAuthRuntimeStoreBindingDTO, profiles map[AuthProfileID]PrivateAuthRuntimeProfile) (PrivateAuthRuntimeStoreBinding, map[AuthProfileID]struct{}, error) {
	if dto.Scope != "pack" {
		return PrivateAuthRuntimeStoreBinding{}, nil, errors.New("private auth runtime store binding scope is invalid")
	}
	refs, refSet, err := privateAuthRuntimeProfileRefs("store_binding.profile_refs", dto.ProfileRefs, profiles, true)
	if err != nil {
		return PrivateAuthRuntimeStoreBinding{}, nil, err
	}

	return PrivateAuthRuntimeStoreBinding{Scope: dto.Scope, ProfileRefs: refs}, refSet, nil
}

func privateAuthRuntimePreflightFromDTO(dto privateAuthRuntimePreflightPolicyDTO) (PrivateAuthRuntimePreflightPolicy, error) {
	if dto.Mode != "required" && dto.Mode != "optional" {
		return PrivateAuthRuntimePreflightPolicy{}, errors.New("private auth runtime preflight mode is invalid")
	}
	if dto.Missing == nil || *dto.Missing != "refresh" && *dto.Missing != "fail" {
		return PrivateAuthRuntimePreflightPolicy{}, errors.New("private auth runtime preflight missing policy is invalid")
	}
	if dto.Expired == nil || *dto.Expired != "refresh" && *dto.Expired != "fail" {
		return PrivateAuthRuntimePreflightPolicy{}, errors.New("private auth runtime preflight expired policy is invalid")
	}

	return PrivateAuthRuntimePreflightPolicy{Mode: dto.Mode, Missing: *dto.Missing, Expired: *dto.Expired}, nil
}

func privateAuthRuntimeProvisioningFromDTO(dto privateAuthRuntimeProvisioningPolicyDTO, profiles map[AuthProfileID]PrivateAuthRuntimeProfile) (PrivateAuthRuntimeProvisioningPolicy, map[AuthProfileID]struct{}, error) {
	switch dto.Mode {
	case "none":
		if len(dto.ProfileRefs) != 0 {
			return PrivateAuthRuntimeProvisioningPolicy{}, nil, errors.New("private auth runtime provisioning refs are invalid")
		}

		return PrivateAuthRuntimeProvisioningPolicy{Mode: dto.Mode}, map[AuthProfileID]struct{}{}, nil
	case "webview":
		refs, refSet, err := privateAuthRuntimeProfileRefs("provisioning.profile_refs", dto.ProfileRefs, profiles, true)
		if err != nil {
			return PrivateAuthRuntimeProvisioningPolicy{}, nil, err
		}
		for _, ref := range refs {
			login := profiles[ref].Login
			if login.URL == "" {
				return PrivateAuthRuntimeProvisioningPolicy{}, nil, errors.New("private auth runtime provisioning login url is required")
			}
			if !login.callbackConfigured || !login.collectorConfigured || !login.captureConfigured {
				return PrivateAuthRuntimeProvisioningPolicy{}, nil, errors.New("private auth runtime callback metadata is required")
			}
		}

		return PrivateAuthRuntimeProvisioningPolicy{Mode: dto.Mode, ProfileRefs: refs}, refSet, nil
	default:
		return PrivateAuthRuntimeProvisioningPolicy{}, nil, errors.New("private auth runtime provisioning mode is invalid")
	}
}

func privateAuthRuntimeMaterializationFromDTO(dto privateAuthRuntimeMaterializationPolicyDTO, profiles map[AuthProfileID]PrivateAuthRuntimeProfile) (PrivateAuthRuntimeMaterializationPolicy, map[AuthProfileID]struct{}, error) {
	refs, refSet, err := privateAuthRuntimeProfileRefs("materialization.profile_refs", dto.ProfileRefs, profiles, true)
	if err != nil {
		return PrivateAuthRuntimeMaterializationPolicy{}, nil, err
	}
	for _, ref := range refs {
		if err := validateAuthSecretKind(profiles[ref].Kind); err != nil {
			return PrivateAuthRuntimeMaterializationPolicy{}, nil, err
		}
	}

	return PrivateAuthRuntimeMaterializationPolicy{ProfileRefs: refs}, refSet, nil
}

func privateAuthRuntimeNormalizationFromDTO(dto privateAuthRuntimeNormalizationPolicyDTO) (PrivateAuthRuntimeNormalizationPolicy, error) {
	if dto.RejectCRLF == nil || !*dto.RejectCRLF {
		return PrivateAuthRuntimeNormalizationPolicy{}, errors.New("private auth runtime normalization must reject CRLF")
	}
	if dto.TrimSpace == nil {
		return PrivateAuthRuntimeNormalizationPolicy{}, errors.New("private auth runtime normalization trim_space is required")
	}

	return PrivateAuthRuntimeNormalizationPolicy{RejectCRLF: *dto.RejectCRLF, TrimSpace: *dto.TrimSpace}, nil
}

func privateAuthRuntimeProfileRefs(field string, rawRefs []string, profiles map[AuthProfileID]PrivateAuthRuntimeProfile, requireNonEmpty bool) ([]AuthProfileID, map[AuthProfileID]struct{}, error) {
	if requireNonEmpty && len(rawRefs) == 0 {
		return nil, nil, errors.New("private auth runtime profile refs are required")
	}
	refs := make([]AuthProfileID, 0, len(rawRefs))
	refSet := make(map[AuthProfileID]struct{}, len(rawRefs))
	for _, rawRef := range rawRefs {
		ref := AuthProfileID(rawRef)
		if err := validateAuthProfileID(ref); err != nil {
			return nil, nil, err
		}
		if _, ok := refSet[ref]; ok {
			return nil, nil, errors.New(field + " contains duplicates")
		}
		if _, ok := profiles[ref]; !ok {
			return nil, nil, errors.New(field + " contains undeclared profile ref")
		}
		refSet[ref] = struct{}{}
		refs = append(refs, ref)
	}

	return refs, refSet, nil
}

func validatePrivateAuthRuntimeCrossSection(preflight PrivateAuthRuntimePreflightPolicy, provisioning PrivateAuthRuntimeProvisioningPolicy, provisioningRefs map[AuthProfileID]struct{}, materializationRefs map[AuthProfileID]struct{}, storeRefs map[AuthProfileID]struct{}) error {
	for ref := range materializationRefs {
		if _, ok := storeRefs[ref]; !ok {
			return errors.New("private auth runtime materialization refs must be store-bound")
		}
	}
	if preflight.Missing == "refresh" || preflight.Expired == "refresh" {
		if provisioning.Mode != "webview" {
			return errors.New("private auth runtime refresh requires provisioning")
		}
		for ref := range materializationRefs {
			if _, ok := provisioningRefs[ref]; !ok {
				return errors.New("private auth runtime refresh provisioning refs are incomplete")
			}
		}
	}

	return nil
}

func clonePrivateAuthRuntimePack(pack PrivateAuthRuntimePack) PrivateAuthRuntimePack {
	pack.StoreBinding.ProfileRefs = cloneAuthProfileIDSlice(pack.StoreBinding.ProfileRefs)
	pack.Profiles = clonePrivateAuthRuntimeProfiles(pack.Profiles)
	pack.Provisioning.ProfileRefs = cloneAuthProfileIDSlice(pack.Provisioning.ProfileRefs)
	pack.Materialization.ProfileRefs = cloneAuthProfileIDSlice(pack.Materialization.ProfileRefs)

	return pack
}

func clonePrivateAuthRuntimeProfiles(profiles []PrivateAuthRuntimeProfile) []PrivateAuthRuntimeProfile {
	if profiles == nil {
		return nil
	}
	cloned := make([]PrivateAuthRuntimeProfile, len(profiles))
	for i, profile := range profiles {
		cloned[i] = clonePrivateAuthRuntimeProfile(profile)
	}

	return cloned
}

func clonePrivateAuthRuntimeProfile(profile PrivateAuthRuntimeProfile) PrivateAuthRuntimeProfile {
	profile.Login.AllowedDomains = cloneDomainRules(profile.Login.AllowedDomains)
	profile.Login.CallbackTransport.ContentTypes = cloneStringSlice(profile.Login.CallbackTransport.ContentTypes)
	profile.Login.Capture.SecretCandidates = cloneStringSlice(profile.Login.Capture.SecretCandidates)

	return profile
}

func cloneAuthProfileIDSlice(values []AuthProfileID) []AuthProfileID {
	if values == nil {
		return nil
	}
	cloned := make([]AuthProfileID, len(values))
	copy(cloned, values)

	return cloned
}
