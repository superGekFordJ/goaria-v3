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

	privateAuthRuntimeMaxLoginTimeoutMillis = 10 * 60 * 1000
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
	URL            string
	AllowedDomains []DomainRule
	TimeoutMillis  int
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
	VerifiedPackIdentity privatePolicyBundleIdentityDTO             `json:"verified_pack_identity"`
	StoreBinding         privateAuthRuntimeStoreBindingDTO          `json:"store_binding"`
	Profiles             []privateAuthRuntimeProfileDTO             `json:"profiles"`
	Preflight            privateAuthRuntimePreflightPolicyDTO       `json:"preflight"`
	Provisioning         privateAuthRuntimeProvisioningPolicyDTO    `json:"provisioning"`
	Materialization      privateAuthRuntimeMaterializationPolicyDTO `json:"materialization"`
	Normalization        privateAuthRuntimeNormalizationPolicyDTO   `json:"normalization"`
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
	URL            string        `json:"url"`
	AllowedDomains *[]DomainRule `json:"allowed_domains"`
	TimeoutMillis  *int          `json:"timeout_millis"`
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

func LoadPrivateAuthRuntimeBundleFromRuntimeSources() (*PrivateAuthRuntimeBundle, error) {
	envPath := os.Getenv(privateAuthRuntimeBundlePathEnv)
	envExpectedSHA256 := os.Getenv(privateAuthRuntimeBundleExpectedSHA256Env)
	hasEnvSource := envPath != ""
	hasEmbeddedSource := len(embeddedPrivateAuthRuntimeBundleJSON) > 0

	switch {
	case !hasEnvSource && !hasEmbeddedSource:
		return nil, nil
	case hasEnvSource && hasEmbeddedSource:
		return nil, errors.New(privateAuthRuntimeBundleInvalidError)
	case hasEnvSource:
		return LoadPrivateAuthRuntimeBundleFromFile(envPath, PrivateAuthRuntimeBundleLoadOptions{
			ExpectedAuthRuntimePrivateSHA256: envExpectedSHA256,
		})
	default:
		return NewPrivateAuthRuntimeBundle(embeddedPrivateAuthRuntimeBundleJSON, PrivateAuthRuntimeBundleLoadOptions{
			ExpectedAuthRuntimePrivateSHA256: embeddedPrivateAuthRuntimeBundleSHA256,
		})
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
		if err := validatePrivatePolicyBundleIdentity(identity); err != nil {
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
	if err := validateSHA256Hex("auth_runtime_private_sha256", envelope.AuthRuntimePrivateSHA256); err != nil {
		return err
	}
	if sha256Hex(envelope.Runtime) != envelope.AuthRuntimePrivateSHA256 {
		return errors.New("private auth runtime bundle runtime hash mismatch")
	}
	if err := validateExpectedPrivateAuthRuntimeSHA256(opts.ExpectedAuthRuntimePrivateSHA256, envelope.AuthRuntimePrivateSHA256); err != nil {
		return err
	}
	if envelope.AuthRuntimePublicFingerprint != "" {
		if err := validateSHA256Hex("auth_runtime_public_fingerprint", envelope.AuthRuntimePublicFingerprint); err != nil {
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
	if err := validateSHA256Hex("expected_auth_runtime_private_sha256", expected); err != nil {
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
	if err := validateSHA256Hex("expected_auth_runtime_public_fingerprint", expected); err != nil {
		return err
	}
	if expected != actual {
		return errors.New("expected auth runtime public fingerprint does not match")
	}

	return nil
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

	return PrivateAuthRuntimeLoginDescriptor{
		URL:            dto.URL,
		AllowedDomains: allowedDomains,
		TimeoutMillis:  timeoutMillis,
	}, nil
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
	for _, segment := range strings.Split(urlPath, "/") {
		for i := 0; i < 2; i++ {
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
			if profiles[ref].Login.URL == "" {
				return PrivateAuthRuntimeProvisioningPolicy{}, nil, errors.New("private auth runtime provisioning login url is required")
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
