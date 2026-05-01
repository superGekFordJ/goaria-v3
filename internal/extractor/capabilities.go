package extractor

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const redactedMarker = "[REDACTED]"

var tokenLikeQueryKeys = map[string]struct{}{
	"access_token": {},
	"api_key":      {},
	"auth":         {},
	"credential":   {},
	"key":          {},
	"policy":       {},
	"secret":       {},
	"sig":          {},
	"signature":    {},
	"token":        {},
	"x-api-key":    {},
}

var sensitiveHeaderStartPattern = regexp.MustCompile(`(?i)\b(authorization|cookie|set-cookie|proxy-authorization|x-[a-z0-9-]*(?:api[-_]?key|auth|token|secret)[a-z0-9-]*)\s*[:=]\s*`)

type CapabilityContext struct {
	PackID     string
	Manifest   Manifest
	Capability Capability
}

func ManifestHasCapability(manifest Manifest, capability Capability) bool {
	for _, declared := range manifest.Capabilities {
		if declared == capability {
			return true
		}
	}

	return false
}

func ValidateCapabilityURL(ctx CapabilityContext, rawURL string) error {
	if err := validatePackID(ctx.PackID); err != nil {
		return redactErrorf("invalid capability request pack_id: %w", err)
	}
	if ctx.Manifest.PackID != ctx.PackID {
		return redactErrorf("capability request pack_id %q does not match manifest pack_id %q", ctx.PackID, ctx.Manifest.PackID)
	}
	if !ManifestHasCapability(ctx.Manifest, ctx.Capability) {
		return redactErrorf("pack %q missing required capability %q", ctx.PackID, ctx.Capability)
	}
	if _, err := allowedHTTPURLForManifest(ctx.Manifest, rawURL); err != nil {
		return err
	}

	return nil
}

func allowedHTTPURLForManifest(manifest Manifest, rawURL string) (*url.URL, error) {
	parsed, host, err := parseSafeHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	if !manifestMatchesHost(manifest, host) {
		return nil, redactErrorf("url %s is not allowed by manifest domain rules", rawURL)
	}

	return parsed, nil
}

func parseSafeHTTPURL(rawURL string) (*url.URL, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", redactErrorf("parse url %s: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", redactErrorf("url %s must use http or https", rawURL)
	}
	if parsed.User != nil {
		return nil, "", redactErrorf("url %s must not include userinfo", rawURL)
	}

	host, ok := parseHTTPURLHost(rawURL)
	if !ok {
		return nil, "", redactErrorf("url %s has an unsafe or unsupported host", rawURL)
	}

	return parsed, host, nil
}

func RedactSensitive(input string, knownSecrets ...string) string {
	if input == "" {
		return ""
	}

	redacted := input
	for _, secret := range knownSecrets {
		if secret == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, secret, redactedMarker)
	}
	redacted = redactQuerySecrets(redacted)
	redacted = redactSensitiveHeaderValues(redacted)

	return redacted
}

func redactSensitiveHeaderValues(input string) string {
	var builder strings.Builder
	for offset := 0; offset < len(input); {
		loc := sensitiveHeaderStartPattern.FindStringIndex(input[offset:])
		if loc == nil {
			builder.WriteString(input[offset:])
			break
		}

		start := offset + loc[0]
		prefixEnd := offset + loc[1]
		builder.WriteString(input[offset:start])
		builder.WriteString(strings.TrimRight(input[start:prefixEnd], " \t"))
		builder.WriteByte(' ')
		builder.WriteString(redactedMarker)

		valueEnd := lineEndIndex(input, prefixEnd)
		if next := sensitiveHeaderStartPattern.FindStringIndex(input[prefixEnd:valueEnd]); next != nil {
			valueEnd = prefixEnd + next[0]
		}
		offset = valueEnd
	}

	return builder.String()
}

func lineEndIndex(input string, start int) int {
	for i := start; i < len(input); i++ {
		if input[i] == '\r' || input[i] == '\n' {
			return i
		}
	}

	return len(input)
}

func redactQuerySecrets(input string) string {
	for key := range tokenLikeQueryKeys {
		input = redactQueryKey(input, key)
	}

	return input
}

func redactQueryKey(input string, key string) string {
	pattern := regexp.MustCompile(`(?i)([?&;]` + regexp.QuoteMeta(key) + `=)([^&#;\s]+)`)

	return pattern.ReplaceAllString(input, `${1}`+redactedMarker)
}

type sensitiveError string

func (e sensitiveError) Error() string {
	return string(e)
}

func redactedError(err error, knownSecrets ...string) error {
	if err == nil {
		return nil
	}

	return sensitiveError(RedactSensitive(err.Error(), knownSecrets...))
}

func redactErrorf(format string, args ...any) error {
	return redactedError(fmt.Errorf(format, args...))
}

func appendNonEmptySecrets(dst []string, values ...string) []string {
	for _, value := range values {
		if value != "" {
			dst = append(dst, value)
		}
	}

	return dst
}

func isSecretHeaderName(name string) bool {
	lower := strings.ToLower(name)
	if lower == "authorization" || lower == "cookie" || lower == "set-cookie" || lower == "proxy-authorization" {
		return true
	}

	return strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "api-key") || strings.Contains(lower, "apikey")
}

func minPositiveDurationMillis(values ...int) int {
	min := 0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if min == 0 || value < min {
			min = value
		}
	}

	return min
}

func minPositiveInt64(values ...int64) int64 {
	var min int64
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if min == 0 || value < min {
			min = value
		}
	}

	return min
}
