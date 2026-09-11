package extractor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Browser header grants are ephemeral, resolve-scoped credentials captured by
// the browser extension. They are validated once at ingress, carried only via
// request context, and consumed per broker hop on exact method+URL+TTL match.

const (
	maxBrowserHeaderGrantsPerResolve    = 8
	maxBrowserHeaderGrantHeaders        = 8
	maxBrowserHeaderNameBytes           = 128
	maxBrowserHeaderValueBytes          = 4096
	maxBrowserHeaderAggregateValueBytes = 8192
	maxBrowserGrantSourceOriginBytes    = 512
	maxBrowserGrantSourceInputBytes     = 2048
	maxBrowserGrantTargetURLBytes       = 4096
	browserHeaderGrantTTLMillis         = 60_000
	browserHeaderGrantClockSkewMillis   = 30_000
)

// Keep in sync with DENIED_X_EXACT / DENIED_X_PREFIXES and the ambient auth
// scheme list in extension/src/background/browserHeaderGrant.ts.
var deniedBrowserGrantHeaderExact = map[string]struct{}{
	"x-real-ip":         {},
	"x-client-ip":       {},
	"x-host":            {},
	"x-original-url":    {},
	"x-original-host":   {},
	"x-original-path":   {},
	"x-original-method": {},
	"x-rewrite-url":     {},
	"x-method-override": {},
}

var deniedBrowserGrantHeaderPrefixes = []string{
	"x-forwarded-",
	"x-http-method",
	"x-proxy-",
	"x-goaria-",
	"x-override-",
}

var ambientBrowserGrantAuthSchemes = map[string]struct{}{
	"basic":     {},
	"digest":    {},
	"ntlm":      {},
	"negotiate": {},
}

var errInvalidBrowserHeaderGrant = errors.New("invalid browser header grant")

// BrowserHeader is one validated grant header: lowercase token name, trimmed
// control-free value.
type BrowserHeader struct {
	Name  string
	Value string
}

// BrowserHeaderGrant is a validated, immutable browser header grant.
type BrowserHeaderGrant struct {
	SourceOrigin     string
	TargetURL        string
	Method           string
	CapturedAtUnixMs int64
	ExpiresAtUnixMs  int64
	Headers          []BrowserHeader
}

// BrowserHeaderGrantSpec is the unvalidated wire shape of one grant.
type BrowserHeaderGrantSpec struct {
	SourceOrigin     string
	TargetURL        string
	Method           string
	CapturedAtUnixMs int64
	ExpiresAtUnixMs  int64
	Headers          []BrowserHeaderSpec
}

// BrowserHeaderSpec is the unvalidated wire shape of one grant header.
type BrowserHeaderSpec struct {
	Name  string
	Value string
}

// CanonicalBrowserGrantSourceOrigin returns the canonical scheme://host[:port]
// form of a source origin. Inputs must be http(s), have no userinfo, and pass
// the same host rules as broker ingress URLs. The input cap bounds
// pre-canonicalization URLs (source_url/referer); the canonical origin output
// is itself capped at maxBrowserGrantSourceOriginBytes.
func CanonicalBrowserGrantSourceOrigin(raw string) (string, bool) {
	if raw == "" || len(raw) > maxBrowserGrantSourceInputBytes {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	if parsed.User != nil {
		return "", false
	}
	host, ok := ParseHTTPURLHost(raw)
	if !ok || host == "" {
		return "", false
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	origin := parsed.Scheme + "://" + host
	if port != "" {
		origin += ":" + port
	}
	if len(origin) > maxBrowserGrantSourceOriginBytes {
		return "", false
	}

	return origin, true
}

// CanonicalBrowserGrantTargetURL returns the canonical https://host[:port] +
// EscapedPath + ?query form of a grant target URL. Fragments, non-HTTPS
// schemes, userinfo, and hosts failing ParseHTTPURLHost are rejected.
func CanonicalBrowserGrantTargetURL(raw string) (string, bool) {
	if raw == "" || len(raw) > maxBrowserGrantTargetURLBytes || strings.Contains(raw, "#") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "", false
	}
	if parsed.Scheme != "https" {
		return "", false
	}
	if parsed.User != nil {
		return "", false
	}
	host, ok := ParseHTTPURLHost(raw)
	if !ok || host == "" {
		return "", false
	}
	port := parsed.Port()
	if port == "443" {
		port = ""
	}
	var b strings.Builder
	b.WriteString("https://")
	b.WriteString(host)
	if port != "" {
		b.WriteByte(':')
		b.WriteString(port)
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	b.WriteString(path)
	if parsed.RawQuery != "" {
		b.WriteByte('?')
		b.WriteString(parsed.RawQuery)
	}
	canonical := b.String()
	if len(canonical) > maxBrowserGrantTargetURLBytes {
		return "", false
	}

	return canonical, true
}

// ValidateBrowserHeaderGrants validates and normalizes wire grant specs into
// immutable grants. sourceOrigin is the canonical origin of the resolve
// source URL. Any failure returns the static error with no grant content.
func ValidateBrowserHeaderGrants(specs []BrowserHeaderGrantSpec, sourceOrigin string, now time.Time) ([]BrowserHeaderGrant, error) {
	if len(specs) == 0 || len(specs) > maxBrowserHeaderGrantsPerResolve {
		return nil, errInvalidBrowserHeaderGrant
	}
	nowMs := now.UnixMilli()
	grants := make([]BrowserHeaderGrant, 0, len(specs))
	scopes := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		grant, err := validateBrowserHeaderGrant(spec, sourceOrigin, nowMs)
		if err != nil {
			return nil, err
		}
		scope := grant.Method + " " + grant.TargetURL
		if _, dup := scopes[scope]; dup {
			return nil, errInvalidBrowserHeaderGrant
		}
		scopes[scope] = struct{}{}
		grants = append(grants, grant)
	}

	return grants, nil
}

func validateBrowserHeaderGrant(spec BrowserHeaderGrantSpec, sourceOrigin string, nowMs int64) (BrowserHeaderGrant, error) {
	canonicalSource, ok := CanonicalBrowserGrantSourceOrigin(spec.SourceOrigin)
	if !ok || canonicalSource != spec.SourceOrigin || spec.SourceOrigin != sourceOrigin {
		return BrowserHeaderGrant{}, errInvalidBrowserHeaderGrant
	}
	canonicalTarget, ok := CanonicalBrowserGrantTargetURL(spec.TargetURL)
	if !ok || canonicalTarget != spec.TargetURL {
		return BrowserHeaderGrant{}, errInvalidBrowserHeaderGrant
	}
	if spec.Method != http.MethodGet && spec.Method != http.MethodHead {
		return BrowserHeaderGrant{}, errInvalidBrowserHeaderGrant
	}
	if spec.CapturedAtUnixMs <= 0 || spec.ExpiresAtUnixMs <= 0 ||
		spec.ExpiresAtUnixMs <= spec.CapturedAtUnixMs ||
		spec.ExpiresAtUnixMs-spec.CapturedAtUnixMs > browserHeaderGrantTTLMillis ||
		spec.CapturedAtUnixMs > nowMs+browserHeaderGrantClockSkewMillis ||
		spec.ExpiresAtUnixMs <= nowMs {
		return BrowserHeaderGrant{}, errInvalidBrowserHeaderGrant
	}
	headers, err := validateBrowserGrantHeaders(spec.Headers)
	if err != nil {
		return BrowserHeaderGrant{}, err
	}

	return BrowserHeaderGrant{
		SourceOrigin:     spec.SourceOrigin,
		TargetURL:        spec.TargetURL,
		Method:           spec.Method,
		CapturedAtUnixMs: spec.CapturedAtUnixMs,
		ExpiresAtUnixMs:  spec.ExpiresAtUnixMs,
		Headers:          headers,
	}, nil
}

func validateBrowserGrantHeaders(specs []BrowserHeaderSpec) ([]BrowserHeader, error) {
	if len(specs) == 0 || len(specs) > maxBrowserHeaderGrantHeaders {
		return nil, errInvalidBrowserHeaderGrant
	}
	seen := make(map[string]struct{}, len(specs))
	aggregate := 0
	headers := make([]BrowserHeader, 0, len(specs))
	for _, spec := range specs {
		name := spec.Name
		if name == "" || !isHTTPToken(name) || name != strings.ToLower(name) || len(name) > maxBrowserHeaderNameBytes {
			return nil, errInvalidBrowserHeaderGrant
		}
		if !isEligibleBrowserGrantHeaderName(name) {
			return nil, errInvalidBrowserHeaderGrant
		}
		if _, dup := seen[name]; dup {
			return nil, errInvalidBrowserHeaderGrant
		}
		seen[name] = struct{}{}
		value := spec.Value
		if !isValidBrowserGrantHeaderValue(value) {
			return nil, errInvalidBrowserHeaderGrant
		}
		aggregate += len(value)
		if aggregate > maxBrowserHeaderAggregateValueBytes {
			return nil, errInvalidBrowserHeaderGrant
		}
		if name == "authorization" && !validBrowserGrantAuthorizationValue(value) {
			return nil, errInvalidBrowserHeaderGrant
		}
		headers = append(headers, BrowserHeader{Name: name, Value: value})
	}
	slices.SortFunc(headers, func(a, b BrowserHeader) int {
		return strings.Compare(a.Name, b.Name)
	})

	return headers, nil
}

func isEligibleBrowserGrantHeaderName(name string) bool {
	if name == "authorization" {
		return true
	}
	if !strings.HasPrefix(name, "x-") {
		return false
	}
	if _, denied := deniedBrowserGrantHeaderExact[name]; denied {
		return false
	}
	for _, prefix := range deniedBrowserGrantHeaderPrefixes {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}

	return true
}

func isValidBrowserGrantHeaderValue(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}

	return len(value) <= maxBrowserHeaderValueBytes
}

// validBrowserGrantAuthorizationValue requires "<scheme><SP><credentials>"
// form and rejects ambient schemes the browser would auto-send.
func validBrowserGrantAuthorizationValue(value string) bool {
	scheme, credentials, ok := strings.Cut(value, " ")
	if !ok || scheme == "" || !isHTTPToken(scheme) || credentials == "" {
		return false
	}
	_, ambient := ambientBrowserGrantAuthSchemes[strings.ToLower(scheme)]

	return !ambient
}

// browserGrantMatch returns the live grant whose method and canonical target
// exactly match the current hop, or nil. Matching is deliberately exact: no
// eTLD+1, suffix, or same-origin broadening.
func browserGrantMatch(grants []BrowserHeaderGrant, method, rawURL string, now time.Time) *BrowserHeaderGrant {
	if len(grants) == 0 {
		return nil
	}
	// Fragments never reach the wire; match on the fragment-free spelling.
	if i := strings.IndexByte(rawURL, '#'); i >= 0 {
		rawURL = rawURL[:i]
	}
	canonical, ok := CanonicalBrowserGrantTargetURL(rawURL)
	if !ok {
		return nil
	}
	nowMs := now.UnixMilli()
	for i := range grants {
		grant := &grants[i]
		if grant.Method != method || grant.ExpiresAtUnixMs <= nowMs {
			continue
		}
		if grant.TargetURL == canonical {
			return grant
		}
	}

	return nil
}

// BrowserContextFingerprint is an irreversible, deterministic fingerprint of
// the browser context that influences fetch behavior. Grant order does not
// matter. An empty context (no typed fields, no grants) returns "".
func BrowserContextFingerprint(bc BrowserRequestContext) string {
	if bc.UserAgent == "" && bc.AcceptLanguage == "" && bc.RefererOrigin == "" && len(bc.Grants) == 0 {
		return ""
	}
	grants := slices.Clone(bc.Grants)
	slices.SortFunc(grants, func(a, b BrowserHeaderGrant) int {
		return strings.Compare(a.Method+" "+a.TargetURL, b.Method+" "+b.TargetURL)
	})
	sum := sha256.New()
	sum.Write([]byte("v1"))
	sum.Write([]byte{0})
	writeFingerprintField(sum, bc.UserAgent)
	writeFingerprintField(sum, bc.AcceptLanguage)
	writeFingerprintField(sum, bc.RefererOrigin)
	for _, grant := range grants {
		writeFingerprintField(sum, grant.SourceOrigin)
		writeFingerprintField(sum, grant.TargetURL)
		writeFingerprintField(sum, grant.Method)
		writeFingerprintField(sum, strconv.FormatInt(grant.CapturedAtUnixMs, 10))
		writeFingerprintField(sum, strconv.FormatInt(grant.ExpiresAtUnixMs, 10))
		headers := slices.Clone(grant.Headers)
		slices.SortFunc(headers, func(a, b BrowserHeader) int {
			return strings.Compare(a.Name, b.Name)
		})
		for _, header := range headers {
			writeFingerprintField(sum, header.Name)
			writeFingerprintField(sum, header.Value)
		}
	}

	return hex.EncodeToString(sum.Sum(nil))
}

func writeFingerprintField(sum hash.Hash, field string) {
	_, _ = sum.Write([]byte(field))
	_, _ = sum.Write([]byte{0})
}
