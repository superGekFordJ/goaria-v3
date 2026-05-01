package extractor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	ABIExportVersion = "goaria_abi_version"
	ABIExportAlloc   = "goaria_alloc"
	ABIExportFree    = "goaria_free"
	ABIExportMatch   = "goaria_match"
	ABIExportExtract = "goaria_extract"
)

const (
	HostImportModule            = "goaria_host"
	HostImportHTTPFetch         = "http_fetch"
	HostImportAuthProfileStatus = "auth_profile_status"
)

const (
	abiMemoryExport  = "memory"
	maxABIInputBytes = 64 * 1024

	maxABIReasonBytes        = 512
	maxABIStringFieldBytes   = 1024
	maxABIURLBytes           = 2048
	maxABIMetadataEntries    = 16
	maxABIMetadataKeyBytes   = 64
	maxABIMetadataValueBytes = 512
)

type MatchInput struct {
	URL string `json:"url"`
}

type MatchOutput struct {
	Matched    bool   `json:"matched"`
	Confidence uint8  `json:"confidence,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type ExtractInput struct {
	URL string `json:"url"`
}

type ExtractOutput struct {
	Items []ExtractedItemRef `json:"items"`
}

type ExtractedItemRef struct {
	ID               string            `json:"id,omitempty"`
	URL              string            `json:"url,omitempty"`
	Filename         string            `json:"filename,omitempty"`
	SizeBytes        int64             `json:"size_bytes,omitempty"`
	MimeType         string            `json:"mime_type,omitempty"`
	AuthProfileRef   string            `json:"auth_profile_ref,omitempty"`
	HeaderProfileRef string            `json:"header_profile_ref,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type HostCallBudget struct {
	mu        sync.Mutex
	remaining uint32
}

func NewHostCallBudget(maxHostCalls uint32) (*HostCallBudget, error) {
	if maxHostCalls == 0 {
		return nil, errors.New("host call budget must be positive")
	}

	return &HostCallBudget{remaining: maxHostCalls}, nil
}

// Consume must be called by future host capability imports before doing work.
// SPEC-029 defines only the budget primitive and does not expose host HTTP,
// auth, WebView, filesystem, environment, or secret-bearing imports.
func (b *HostCallBudget) Consume() error {
	if b == nil {
		return errors.New("host call budget is nil")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.remaining == 0 {
		return errors.New("host call budget exhausted")
	}
	b.remaining--

	return nil
}

func (b *HostCallBudget) Remaining() uint32 {
	if b == nil {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.remaining
}

func packABIResult(ptr, length uint32) uint64 {
	return uint64(ptr)<<32 | uint64(length)
}

func unpackABIResult(result uint64) (ptr uint32, length uint32) {
	return uint32(result >> 32), uint32(result)
}

func DecodeMatchOutputStrict(raw []byte) (MatchOutput, error) {
	var output MatchOutput
	if err := decodeJSONStrict(raw, &output); err != nil {
		return MatchOutput{}, err
	}

	return output, nil
}

func DecodeExtractOutputStrict(raw []byte) (ExtractOutput, error) {
	var output ExtractOutput
	if err := decodeJSONStrict(raw, &output); err != nil {
		return ExtractOutput{}, err
	}

	return output, nil
}

func ValidateMatchInput(input MatchInput) error {
	return validateABIURL(input.URL, "match input url")
}

func ValidateExtractInput(input ExtractInput) error {
	return validateABIURL(input.URL, "extract input url")
}

func ValidateMatchOutput(output MatchOutput) error {
	if len(output.Reason) > maxABIReasonBytes {
		return fmt.Errorf("match reason exceeds %d bytes", maxABIReasonBytes)
	}
	if err := validateSafeString(output.Reason, "match reason"); err != nil {
		return err
	}

	return nil
}

func ValidateExtractOutput(output ExtractOutput, limits ResourceLimits) error {
	if uint32(len(output.Items)) > limits.MaxOutputItems {
		return fmt.Errorf("extract output item count exceeds max_output_items")
	}

	for i, item := range output.Items {
		if err := validateExtractedItemRef(item); err != nil {
			return fmt.Errorf("extract output item %d: %w", i, err)
		}
	}

	return nil
}

func decodeJSONStrict(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("output contains trailing JSON data")
	}

	return nil
}

func validateExtractedItemRef(item ExtractedItemRef) error {
	if item.SizeBytes < 0 {
		return errors.New("size_bytes must not be negative")
	}
	if item.URL != "" {
		if len(item.URL) > maxABIURLBytes {
			return fmt.Errorf("url exceeds %d bytes", maxABIURLBytes)
		}
		if err := validateABIURL(item.URL, "item url"); err != nil {
			return err
		}
	}

	stringFields := []struct {
		name  string
		value string
	}{
		{name: "id", value: item.ID},
		{name: "filename", value: item.Filename},
		{name: "mime_type", value: item.MimeType},
		{name: "auth_profile_ref", value: item.AuthProfileRef},
		{name: "header_profile_ref", value: item.HeaderProfileRef},
	}
	for _, field := range stringFields {
		if len(field.value) > maxABIStringFieldBytes {
			return fmt.Errorf("%s exceeds %d bytes", field.name, maxABIStringFieldBytes)
		}
		if err := validateSafeString(field.value, field.name); err != nil {
			return err
		}
	}

	return validateMetadata(item.Metadata)
}

func validateMetadata(metadata map[string]string) error {
	if len(metadata) > maxABIMetadataEntries {
		return fmt.Errorf("metadata has more than %d entries", maxABIMetadataEntries)
	}

	for key, value := range metadata {
		if key == "" {
			return errors.New("metadata key must be non-empty")
		}
		if len(key) > maxABIMetadataKeyBytes {
			return fmt.Errorf("metadata key exceeds %d bytes", maxABIMetadataKeyBytes)
		}
		if len(value) > maxABIMetadataValueBytes {
			return fmt.Errorf("metadata value exceeds %d bytes", maxABIMetadataValueBytes)
		}
		if err := validateSafeString(key, "metadata key"); err != nil {
			return err
		}
		if err := validateSafeString(value, "metadata value"); err != nil {
			return err
		}
		if isCredentialShapedMetadataKey(key) {
			return fmt.Errorf("metadata key %q is credential-shaped", key)
		}
	}

	return nil
}

func validateABIURL(rawURL string, field string) error {
	if rawURL == "" {
		return fmt.Errorf("%s must be non-empty", field)
	}
	if len(rawURL) > maxABIURLBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxABIURLBytes)
	}
	if err := validateSafeString(rawURL, field); err != nil {
		return err
	}
	if strings.TrimSpace(rawURL) != rawURL {
		return fmt.Errorf("%s must be trimmed", field)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s is malformed", field)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must use http or https", field)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s must not contain credentials", field)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("%s must include host", field)
	}
	if strings.Contains(parsed.Host, "%") {
		return fmt.Errorf("%s host must not contain escapes", field)
	}
	if strings.Contains(parsed.Host, ":") && parsed.Port() == "" {
		return fmt.Errorf("%s host contains invalid port", field)
	}

	return nil
}

func validateSafeString(value string, field string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid utf-8", field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s must not contain control characters", field)
		}
	}

	return nil
}

func isCredentialShapedMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalizedUnderscore := strings.NewReplacer("-", "_", " ", "_", ".", "_", ":", "_", "/", "_").Replace(normalized)

	switch normalized {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "token", "secret", "api_key", "x-api-key":
		return true
	}
	switch normalizedUnderscore {
	case "authorization", "proxy_authorization", "cookie", "set_cookie", "token", "secret", "api_key", "x_api_key":
		return true
	}

	parts := strings.FieldsFunc(normalizedUnderscore, func(r rune) bool { return r == '_' })
	for _, part := range parts {
		switch part {
		case "authorization", "cookie", "token", "secret":
			return true
		}
	}

	credentialSubstrings := []string{
		"_authorization_", "_auth_token_", "_bearer_token_", "_access_token_", "_refresh_token_",
		"_session_cookie_", "_client_secret_", "_api_key_", "_x_api_key_",
	}
	padded := "_" + normalizedUnderscore + "_"
	for _, substring := range credentialSubstrings {
		if strings.Contains(padded, substring) {
			return true
		}
	}

	return false
}
