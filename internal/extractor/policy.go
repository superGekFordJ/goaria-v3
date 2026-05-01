package extractor

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
)

type TrustPolicy struct {
	CurrentABIVersion   uint32
	AllowedCapabilities map[Capability]struct{}
	MaxResourceLimits   ResourceLimits
	TrustedPublicKeys   []ed25519.PublicKey
}

func DefaultTrustPolicy() TrustPolicy {
	return TrustPolicy{
		CurrentABIVersion: CurrentABIVersion,
		AllowedCapabilities: map[Capability]struct{}{
			CapabilityParseWASM:   {},
			CapabilityHTTPFetch:   {},
			CapabilityAuthProfile: {},
		},
		MaxResourceLimits: ResourceLimits{
			TimeoutMillis:    10_000,
			MaxMemoryPages:   256,
			MaxHostCalls:     128,
			MaxResponseBytes: 10 * 1024 * 1024,
			MaxOutputItems:   1_000,
			MaxOutputBytes:   1024 * 1024,
		},
	}
}

func ValidateManifest(manifest Manifest, policy TrustPolicy) error {
	if policy.CurrentABIVersion == 0 {
		return errors.New("trust policy current abi_version must be positive")
	}

	if err := validatePackID(manifest.PackID); err != nil {
		return err
	}
	if err := validatePackVersion(manifest.PackVersion); err != nil {
		return err
	}
	if manifest.ABIVersion != policy.CurrentABIVersion {
		return fmt.Errorf("manifest abi_version %d does not match host abi_version %d", manifest.ABIVersion, policy.CurrentABIVersion)
	}
	if err := validatePayloadSHA256(manifest.PayloadSHA256); err != nil {
		return err
	}
	if err := validateCapabilities(manifest.Capabilities, policy.AllowedCapabilities); err != nil {
		return err
	}
	if err := validateDomainRules(manifest.Domains); err != nil {
		return err
	}
	if err := validateResourceLimits(manifest.ResourceLimits, policy.MaxResourceLimits); err != nil {
		return err
	}

	return nil
}

func normalizeManifest(manifest Manifest) Manifest {
	manifest.Capabilities = append([]Capability(nil), manifest.Capabilities...)
	manifest.Domains = cloneDomainRules(manifest.Domains)
	for i := range manifest.Domains {
		manifest.Domains[i].Host = strings.ToLower(manifest.Domains[i].Host)
	}

	return manifest
}

func validatePackID(id string) error {
	if len(id) < 3 || len(id) > 64 {
		return fmt.Errorf("pack_id length must be between 3 and 64 characters")
	}
	if !isLowerSlugEdge(id[0]) || !isLowerSlugEdge(id[len(id)-1]) {
		return errors.New("pack_id must start and end with a lowercase letter or digit")
	}
	for i := 1; i < len(id)-1; i++ {
		if !isLowerSlugChar(id[i]) {
			return fmt.Errorf("pack_id contains invalid character %q", id[i])
		}
	}

	return nil
}

func isLowerSlugEdge(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

func isLowerSlugChar(c byte) bool {
	return isLowerSlugEdge(c) || c == '.' || c == '_' || c == '-'
}

func validatePackVersion(version string) error {
	if version == "" || strings.TrimSpace(version) != version {
		return errors.New("pack_version must be non-empty and trimmed")
	}
	for _, r := range version {
		if r == '/' || r == '\\' || r == 0 || isASCIIWhitespace(r) {
			return errors.New("pack_version must not contain whitespace or path separators")
		}
	}

	return nil
}

func validatePayloadSHA256(hash string) error {
	if len(hash) != 64 {
		return fmt.Errorf("payload_sha256 must be 64 lowercase hex characters")
	}
	for _, r := range hash {
		if !isLowerHex(r) {
			return fmt.Errorf("payload_sha256 contains invalid character %q", r)
		}
	}

	return nil
}

func isLowerHex(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f'
}

func validateCapabilities(capabilities []Capability, allowed map[Capability]struct{}) error {
	if len(capabilities) == 0 {
		return errors.New("manifest must declare at least one capability")
	}
	if len(allowed) == 0 {
		return errors.New("trust policy allows no capabilities")
	}

	seen := make(map[Capability]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if capability == "" {
			return errors.New("capability must be non-empty")
		}
		if _, ok := seen[capability]; ok {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = struct{}{}

		if _, ok := allowed[capability]; !ok {
			return fmt.Errorf("capability %q is not allowed", capability)
		}
	}

	return nil
}

func validateDomainRules(domains []DomainRule) error {
	if len(domains) == 0 {
		return errors.New("manifest must declare at least one domain")
	}

	for _, rule := range domains {
		if err := validateDomainRule(rule); err != nil {
			return err
		}
	}

	return nil
}

func validateDomainRule(rule DomainRule) error {
	host := rule.Host
	if host == "" {
		return errors.New("domain host must be non-empty")
	}
	if host != strings.TrimSpace(host) || host != strings.ToLower(host) {
		return fmt.Errorf("domain host %q must be lowercase and trimmed", host)
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/\\?#@:") {
		return fmt.Errorf("domain host %q must not contain scheme, path, port, credentials, query, or fragment", host)
	}
	if host == "*" || strings.Contains(host, "*") {
		return fmt.Errorf("domain host %q must not contain wildcard syntax", host)
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return fmt.Errorf("domain host %q contains invalid label boundaries", host)
	}

	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return fmt.Errorf("domain host %q must include at least two labels", host)
	}
	for _, label := range labels {
		if err := validateDomainLabel(label); err != nil {
			return fmt.Errorf("domain host %q: %w", host, err)
		}
	}

	return nil
}

func validateDomainLabel(label string) error {
	if label == "" {
		return errors.New("domain label must be non-empty")
	}
	if len(label) > 63 {
		return errors.New("domain label is too long")
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return errors.New("domain label must not start or end with hyphen")
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return fmt.Errorf("domain label contains invalid character %q", c)
	}

	return nil
}

func validateResourceLimits(limits ResourceLimits, maxima ResourceLimits) error {
	if err := validatePositiveInt("timeout_millis", limits.TimeoutMillis, maxima.TimeoutMillis); err != nil {
		return err
	}
	if err := validatePositiveUint32("max_memory_pages", limits.MaxMemoryPages, maxima.MaxMemoryPages); err != nil {
		return err
	}
	if err := validatePositiveUint32("max_host_calls", limits.MaxHostCalls, maxima.MaxHostCalls); err != nil {
		return err
	}
	if err := validatePositiveInt64("max_response_bytes", limits.MaxResponseBytes, maxima.MaxResponseBytes); err != nil {
		return err
	}
	if err := validatePositiveUint32("max_output_items", limits.MaxOutputItems, maxima.MaxOutputItems); err != nil {
		return err
	}
	if err := validatePositiveInt64("max_output_bytes", limits.MaxOutputBytes, maxima.MaxOutputBytes); err != nil {
		return err
	}

	return nil
}

func validatePositiveInt(name string, value int, max int) error {
	if value <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	if max <= 0 {
		return fmt.Errorf("trust policy maximum for %s must be positive", name)
	}
	if value > max {
		return fmt.Errorf("%s exceeds trust policy maximum", name)
	}

	return nil
}

func validatePositiveUint32(name string, value uint32, max uint32) error {
	if value == 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	if max == 0 {
		return fmt.Errorf("trust policy maximum for %s must be positive", name)
	}
	if value > max {
		return fmt.Errorf("%s exceeds trust policy maximum", name)
	}

	return nil
}

func validatePositiveInt64(name string, value int64, max int64) error {
	if value <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	if max <= 0 {
		return fmt.Errorf("trust policy maximum for %s must be positive", name)
	}
	if value > max {
		return fmt.Errorf("%s exceeds trust policy maximum", name)
	}

	return nil
}

func isASCIIWhitespace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	default:
		return false
	}
}

func cloneDomainRules(rules []DomainRule) []DomainRule {
	if rules == nil {
		return nil
	}

	cloned := make([]DomainRule, len(rules))
	copy(cloned, rules)

	return cloned
}
