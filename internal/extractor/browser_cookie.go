package extractor

import (
	"net"
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/publicsuffix"
)

const maxCookieHeaderBytes = 8192

// SessionCookie is a request-scoped browser cookie with RFC 6265 provenance.
type SessionCookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	Secure   bool
	HostOnly bool
}

// CanonicalCookieFields returns matcher-normalized fields for fingerprints.
func CanonicalCookieFields(cookie SessionCookie) SessionCookie {
	return SessionCookie{
		Name:     strings.ToLower(strings.TrimSpace(cookie.Name)),
		Value:    cookie.Value,
		Domain:   normalizeCookieDomain(cookie.Domain),
		Path:     cookiePath(cookie.Path),
		Secure:   cookie.Secure,
		HostOnly: cookie.HostOnly,
	}
}

// CookieDomainRelatedToSource reports whether cookieDomain shares a
// registrable domain with sourceURL (sibling API hosts kept; cross-site dropped).
func CookieDomainRelatedToSource(cookieDomain, sourceURL string) bool {
	parsed, err := url.Parse(sourceURL)
	if err != nil {
		return false
	}
	sourceHost := strings.ToLower(parsed.Hostname())
	cookieHost := normalizeCookieDomain(cookieDomain)
	if sourceHost == "" || cookieHost == "" {
		return false
	}
	sourceETLD, sourceErr := publicsuffix.EffectiveTLDPlusOne(sourceHost)
	cookieETLD, cookieErr := publicsuffix.EffectiveTLDPlusOne(cookieHost)
	if sourceErr != nil || cookieErr != nil {
		return sourceHost == cookieHost
	}

	return sourceETLD == cookieETLD
}

// ValidCookieName reports whether name is a non-empty RFC 2616 token (RFC 6265 cookie-name).
func ValidCookieName(name string) bool {
	return name != "" && isHTTPToken(name)
}

// ValidCookieValue reports whether value is an RFC 6265 cookie-octet string (empty allowed).
func ValidCookieValue(value string) bool {
	return isCookieOctetString(value)
}

// CookieMatchesRequest reports whether cookie should be attached to rawURL.
func CookieMatchesRequest(cookie SessionCookie, rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	reqHost := strings.ToLower(parsed.Hostname())
	if reqHost == "" {
		return false
	}
	reqPath := parsed.EscapedPath()
	if reqPath == "" {
		reqPath = "/"
	}
	cookieDomain := normalizeCookieDomain(cookie.Domain)
	if cookieDomain == "" {
		return false
	}
	if !cookieDomainMatches(reqHost, cookieDomain, cookie.HostOnly) {
		return false
	}
	if !cookiePathMatches(cookiePath(cookie.Path), reqPath) {
		return false
	}
	if cookie.Secure && parsed.Scheme != "https" {
		return false
	}
	if strings.HasPrefix(cookie.Name, "__Host-") {
		if !cookie.Secure || !cookie.HostOnly || cookiePath(cookie.Path) != "/" || parsed.Scheme != "https" {
			return false
		}
	}
	if strings.HasPrefix(cookie.Name, "__Secure-") {
		if !cookie.Secure || parsed.Scheme != "https" {
			return false
		}
	}

	return true
}

// CookiesForRequest returns a Cookie header value for rawURL, or "" if none match.
func CookiesForRequest(cookies []SessionCookie, rawURL string) string {
	return serializeCookieHeader(cookiesMatchingRequest(cookies, rawURL))
}

func cookiesMatchingRequest(cookies []SessionCookie, rawURL string) []SessionCookie {
	if len(cookies) == 0 {
		return nil
	}
	matched := make([]SessionCookie, 0, len(cookies))
	for _, cookie := range cookies {
		if CookieMatchesRequest(cookie, rawURL) {
			matched = append(matched, cookie)
		}
	}

	return matched
}

func serializeCookieHeader(cookies []SessionCookie) string {
	if len(cookies) == 0 {
		return ""
	}
	ordered := append([]SessionCookie(nil), cookies...)
	sort.SliceStable(ordered, func(i, j int) bool {
		pi, pj := cookiePath(ordered[i].Path), cookiePath(ordered[j].Path)
		if len(pi) != len(pj) {
			return len(pi) > len(pj)
		}
		return ordered[i].Name < ordered[j].Name
	})
	parts := make([]string, 0, len(ordered))
	for _, cookie := range ordered {
		if !ValidCookieName(cookie.Name) || !ValidCookieValue(cookie.Value) {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	for len(parts) > 0 {
		header := strings.Join(parts, "; ")
		if len(header) <= maxCookieHeaderBytes && header != "" {
			return header
		}
		parts = parts[:len(parts)-1]
	}

	return ""
}

func normalizeCookieDomain(domain string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

func cookiePath(path string) string {
	if path == "" {
		return "/"
	}

	return path
}

func isCookieOctetString(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isCookieOctet(s[i]) {
			return false
		}
	}

	return true
}

func isHTTPToken(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHTTPTokenByte(s[i]) {
			return false
		}
	}

	return true
}

func isHTTPTokenByte(c byte) bool {
	if c <= 32 || c >= 127 {
		return false
	}
	switch c {
	case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
		return false
	default:
		return true
	}
}

func isCookieOctet(c byte) bool {
	switch {
	case c == 0x21:
		return true
	case c >= 0x23 && c <= 0x2B:
		return true
	case c >= 0x2D && c <= 0x3A:
		return true
	case c >= 0x3C && c <= 0x5B:
		return true
	case c >= 0x5D && c <= 0x7E:
		return true
	default:
		return false
	}
}

func cookieDomainMatches(reqHost, cookieDomain string, hostOnly bool) bool {
	if ip := net.ParseIP(reqHost); ip != nil {
		return hostOnly && cookieDomain == reqHost
	}
	if hostOnly {
		return reqHost == cookieDomain
	}
	if reqHost != cookieDomain && !strings.HasSuffix(reqHost, "."+cookieDomain) {
		return false
	}

	return domainCookiePublicSuffixOK(cookieDomain)
}

func domainCookiePublicSuffixOK(cookieDomain string) bool {
	etld1, err := publicsuffix.EffectiveTLDPlusOne(cookieDomain)
	if err != nil || etld1 == "" {
		return false
	}
	if cookieDomain == etld1 {
		return true
	}

	return strings.HasSuffix(cookieDomain, "."+etld1)
}

func cookiePathMatches(cookiePathValue, reqPath string) bool {
	if !strings.HasPrefix(reqPath, cookiePathValue) {
		return false
	}
	if reqPath == cookiePathValue {
		return true
	}
	if strings.HasSuffix(cookiePathValue, "/") {
		return true
	}

	return len(reqPath) > len(cookiePathValue) && reqPath[len(cookiePathValue)] == '/'
}
