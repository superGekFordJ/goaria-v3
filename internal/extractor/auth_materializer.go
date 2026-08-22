package extractor

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type AuthMaterializer interface {
	MaterializeAuth(secret ResolvedAuthSecret) (MaterializedAuthSecret, error)
}

type DefaultAuthMaterializer struct{}

func NewDefaultAuthMaterializer() DefaultAuthMaterializer {
	return DefaultAuthMaterializer{}
}

type MaterializedAuthSecret struct {
	HeaderName      string
	Kind            AuthSecretKind
	RedactedDisplay string

	headerValue     string
	sensitiveValues []string
}

func (m MaterializedAuthSecret) HeaderValue() string {
	return m.headerValue
}

func (m MaterializedAuthSecret) SensitiveValues() []string {
	return append([]string(nil), m.sensitiveValues...)
}

func (m MaterializedAuthSecret) ApplyTo(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Del("Authorization")
	headers.Del("Cookie")
	if m.HeaderName == "" || m.headerValue == "" {
		return
	}
	headers.Set(m.HeaderName, m.headerValue)
}

func (m MaterializedAuthSecret) String() string {
	return fmt.Sprintf("MaterializedAuthSecret{header_name:%q kind:%q redacted_display:%q}", m.HeaderName, m.Kind, m.RedactedDisplay)
}

func (m MaterializedAuthSecret) GoString() string {
	return m.String()
}

func (DefaultAuthMaterializer) MaterializeAuth(secret ResolvedAuthSecret) (MaterializedAuthSecret, error) {
	knownForms := authSecretForms(secret.HeaderName, secret.HeaderValue)
	headerName, err := canonicalAuthSecretHeaderName(secret.HeaderName)
	if err != nil {
		return MaterializedAuthSecret{}, redactedError(err, knownForms...)
	}

	kind, err := authSecretKindForHeader(secret.Kind, headerName)
	if err != nil {
		return MaterializedAuthSecret{}, redactedError(err, knownForms...)
	}

	headerValue, sensitiveValues, err := materializedAuthHeaderValue(kind, secret.HeaderValue)
	if err != nil {
		return MaterializedAuthSecret{}, redactedError(err, knownForms...)
	}
	if len(knownForms) > 0 {
		sensitiveValues = compactUniqueNonEmpty(append(sensitiveValues, knownForms...))
	}

	return MaterializedAuthSecret{
		HeaderName:      headerName,
		Kind:            kind,
		RedactedDisplay: sanitizeMaterializedAuthDisplay(secret.RedactedDisplay, sensitiveValues),
		headerValue:     headerValue,
		sensitiveValues: sensitiveValues,
	}, nil
}

func canonicalAuthSecretHeaderName(headerName string) (string, error) {
	trimmed := strings.TrimSpace(headerName)
	if trimmed == "" {
		return "", errors.New("auth secret header name must be non-empty")
	}
	if strings.ContainsAny(headerName, "\r\n:") || !isHTTPHeaderToken(trimmed) {
		return "", errors.New("auth secret header name is invalid")
	}

	return http.CanonicalHeaderKey(trimmed), nil
}

func authSecretKindForHeader(kind AuthSecretKind, headerName string) (AuthSecretKind, error) {
	expectedKind, ok := authSecretKindInferredFromHeader(headerName)
	if !ok {
		return "", fmt.Errorf("auth secret header %q is not supported", headerName)
	}
	if kind == "" {
		return expectedKind, nil
	}
	if err := validateAuthSecretKind(kind); err != nil {
		return "", err
	}
	if kind != expectedKind {
		return "", fmt.Errorf("auth secret kind %q does not match header %q", kind, headerName)
	}

	return kind, nil
}

func authSecretKindInferredFromHeader(headerName string) (AuthSecretKind, bool) {
	switch http.CanonicalHeaderKey(strings.TrimSpace(headerName)) {
	case "Authorization":
		return AuthSecretKindBearer, true
	case "Cookie":
		return AuthSecretKindCookie, true
	default:
		return "", false
	}
}

func materializedAuthHeaderValue(kind AuthSecretKind, rawValue string) (string, []string, error) {
	if strings.TrimSpace(rawValue) == "" {
		return "", nil, errors.New("auth secret header value must be non-empty")
	}
	if strings.ContainsAny(rawValue, "\r\n") {
		return "", nil, errors.New("auth secret header value must not contain CR/LF")
	}

	switch kind {
	case AuthSecretKindBearer:
		return materializedBearerHeaderValue(rawValue)
	case AuthSecretKindCookie:
		return rawValue, authSecretForms("Cookie", rawValue), nil
	default:
		return "", nil, fmt.Errorf("auth secret kind %q is not supported", kind)
	}
}

func materializedBearerHeaderValue(rawValue string) (string, []string, error) {
	trimmed := strings.TrimSpace(rawValue)
	token := trimmed
	if strings.HasPrefix(strings.ToLower(trimmed), "bearer ") {
		token = strings.TrimSpace(trimmed[len("bearer "):])
		if token == "" {
			return "", nil, errors.New("bearer auth secret token must be non-empty")
		}
	}
	headerValue := "Bearer " + token
	forms := []string{rawValue, headerValue, token}
	forms = append(forms, authSecretForms("Authorization", headerValue)...)

	return headerValue, compactUniqueNonEmpty(forms), nil
}

func sanitizeMaterializedAuthDisplay(display string, sensitiveValues []string) string {
	if display == "" {
		return redactedMarker
	}
	sanitized := RedactSensitive(display, sensitiveValues...)
	if sanitized == "" || displayLeaksMaterializedSecrets(sanitized, sensitiveValues) {
		return redactedMarker
	}

	return sanitized
}

func displayLeaksMaterializedSecrets(display string, sensitiveValues []string) bool {
	return RedactSensitive(display, sensitiveValues...) != display
}

func authSecretForms(headerName string, headerValue string) []string {
	canonical := http.CanonicalHeaderKey(strings.TrimSpace(headerName))
	forms := []string{headerValue}
	if canonical == "Authorization" {
		trimmedValue := strings.TrimSpace(headerValue)
		if strings.HasPrefix(strings.ToLower(trimmedValue), "bearer ") {
			forms = append(forms, strings.TrimSpace(trimmedValue[len("bearer "):]))
		}
	}
	if canonical == "Cookie" {
		for part := range strings.SplitSeq(headerValue, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			forms = append(forms, part)
			if _, value, ok := strings.Cut(part, "="); ok {
				forms = append(forms, strings.TrimSpace(value))
			}
		}
	}

	return compactUniqueNonEmpty(forms)
}

func isHTTPHeaderToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
			return false
		}
	}

	return true
}
