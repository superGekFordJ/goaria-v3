package utils

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// SameSite checks if two hosts share the same registrable domain (eTLD+1).
func SameSite(a, b string) bool {
	aHost := a
	if h, _, err := net.SplitHostPort(a); err == nil {
		aHost = h
	}
	bHost := b
	if h, _, err := net.SplitHostPort(b); err == nil {
		bHost = h
	}

	if strings.EqualFold(aHost, bHost) {
		return true
	}

	aDomain, errA := publicsuffix.EffectiveTLDPlusOne(aHost)
	bDomain, errB := publicsuffix.EffectiveTLDPlusOne(bHost)
	if errA != nil || errB != nil {
		return false // unknown/invalid TLD — fail closed
	}
	return strings.EqualFold(aDomain, bDomain)
}

// CopyRedirectHeaders restores sensitive headers that Go's default http.Client strips
// on cross-origin redirects, provided the redirect remains on the same-site.
// For cross-site redirects, it defers to the standard library's safe defaults (which
// retain safe headers like Range/User-Agent and update Referer, but strip credentials).
// It also merges each hop's Set-Cookie into the outgoing Cookie header so
// cookie-bounce endpoints work without a Jar.
func CopyRedirectHeaders(dst, src *http.Request) {
	if dst == nil || src == nil {
		return
	}
	if dst.URL != nil && src.URL != nil &&
		strings.EqualFold(dst.URL.Scheme, src.URL.Scheme) &&
		SameSite(dst.URL.Host, src.URL.Host) {
		// ponytail: We manually forward Cookie and Authorization on same-site redirects
		// instead of using strict browser security boundaries (like http.CookieJar).
		// Why?
		// 1. Cookies: The Surge extension provides a flattened Cookie string (e.g., "session=123")
		//    without original Domain/Path metadata. A strict CookieJar would treat this as a host-only
		//    cookie and refuse to send it to same-site CDN subdomains (breaking downloads).
		// 2. Authorization: Fetch natively drops Authorization on ALL cross-origin redirects. Real
		//    browsers only send Basic Auth to sibling origins if challenged with a matching 401 Realm.
		//    Surge lacks a 401 challenge-response engine, so we must proactively forward Auth to
		//    siblings to support authenticated CDN redirects (e.g. members.easynews.com -> iad-dl-08.easynews.com).
		// Known Ceiling: This explicitly leaks credentials to distinct sibling origins under the same
		// eTLD+1 (e.g., tenant-a.saas.com -> tenant-b.saas.com). Fixing this requires an architectural
		// rewrite to ingest full cookie metadata from the extension and build a 401 retry interceptor.
		// We use SameSite as a safe, deliberate heuristic compromise.
		for _, k := range []string{"Cookie", "Cookie2", "Authorization"} {
			if v := src.Header.Values(k); len(v) > 0 {
				dst.Header[k] = append([]string(nil), v...)
			}
		}
	}
	mergeRedirectCookies(dst)
}

// FORK-PATCH: cookie-bounce endpoints answer with
// Set-Cookie + Location:self and expect the new cookie on the next hop.
// Without a Jar the Set-Cookie is dropped and every hop looks like the
// first, looping until the redirect cap. Merge each hop's Set-Cookie into
// the outgoing Cookie header with attribute validation; carry over the
// previous hop's accumulated cookies only under the same-site+scheme gate.
func mergeRedirectCookies(dst *http.Request) {
	resp := dst.Response // response that caused this redirect
	if resp == nil || dst.URL == nil || dst.Header == nil {
		return
	}
	prev := resp.Request // == via[len(via)-1]; nil in synthetic calls
	now := time.Now()

	// Ordered name->value set seeded from dst's current Cookie header
	// (initial-header copy + the same-site restore above).
	var names []string
	vals := make(map[string]string)
	upsert := func(c *http.Cookie) {
		if _, ok := vals[c.Name]; !ok {
			names = append(names, c.Name)
		}
		vals[c.Name] = c.Value
	}
	remove := func(name string) {
		if _, ok := vals[name]; !ok {
			return
		}
		delete(vals, name)
		for i, n := range names {
			if n == name {
				names = append(names[:i], names[i+1:]...)
				return
			}
		}
	}

	for _, c := range dst.Cookies() {
		upsert(c)
	}

	// Carry-over: replay the previous hop's accumulated Cookie only when
	// this hop stays same-site and same-scheme (same gate as the restore
	// above). Cross-site hops never inherit accumulated credentials.
	// Flattened pairs carry no Domain/Path metadata, so per-cookie scope is
	// not re-checked here — a host-only cookie set earlier may ride to a
	// sibling subdomain (accepted ceiling, locked by test).
	if prev != nil && prev.URL != nil &&
		strings.EqualFold(dst.URL.Scheme, prev.URL.Scheme) &&
		SameSite(dst.URL.Host, prev.URL.Host) {
		for _, c := range prev.Cookies() {
			upsert(c)
		}
	}

	// Apply this hop's Set-Cookie with attribute validation. prev is the
	// setter request — without it there is no setter origin to validate
	// against, so merging is skipped entirely.
	if prev != nil && prev.URL != nil {
		for _, c := range resp.Cookies() {
			if c.Name == "" {
				continue
			}
			if !cookieSendable(c, prev.URL, dst.URL) {
				continue
			}
			if cookieExpired(c, now) {
				remove(c.Name)
			} else {
				upsert(c)
			}
		}
	}

	// Rebuild a single flat Cookie header; drop the key entirely if empty.
	pairs := make([]string, 0, len(names))
	for _, n := range names {
		if s := (&http.Cookie{Name: n, Value: vals[n]}).String(); s != "" {
			pairs = append(pairs, s)
		}
	}
	if len(pairs) == 0 {
		dst.Header.Del("Cookie")
		return
	}
	dst.Header.Set("Cookie", strings.Join(pairs, "; "))
}

// cookieSendable reports whether a Set-Cookie issued by setterURL may be
// replayed to dstURL, per a simplified RFC 6265 attribute model.
func cookieSendable(c *http.Cookie, setterURL, dstURL *url.URL) bool {
	setterHost := strings.ToLower(setterURL.Hostname())
	dstHost := strings.ToLower(dstURL.Hostname())

	if c.Domain == "" {
		// Host-only: only the exact setter host may receive it — sibling
		// subdomains are out even when they share the registrable site.
		if setterHost != dstHost {
			return false
		}
	} else {
		d := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		if d == "" {
			return false
		}
		// The setter may only plant cookies for its own domain or a parent.
		if !domainMatch(setterHost, d) {
			return false
		}
		if !domainMatch(dstHost, d) {
			return false
		}
		// Reject cookies scoped to a public suffix (e.g. Domain=co.uk).
		if _, err := publicsuffix.EffectiveTLDPlusOne(d); err != nil {
			return false
		}
	}

	if c.Secure {
		if dstURL.Scheme != "https" {
			return false
		}
		// RFC 6265bis: never accept Secure cookies from an insecure scheme.
		if !strings.EqualFold(setterURL.Scheme, "https") {
			return false
		}
	}

	cp := c.Path
	if cp == "" || cp[0] != '/' {
		cp = defaultCookiePath(setterURL.Path)
	}
	return pathMatch(dstURL.Path, cp)
}

// cookieExpired reports whether the cookie is a deletion instruction:
// Max-Age <= 0 (parsing normalizes Max-Age=0 to -1), or a past Expires when
// no Max-Age is present. Positive Max-Age wins over Expires per RFC 6265.
func cookieExpired(c *http.Cookie, now time.Time) bool {
	if c.MaxAge < 0 {
		return true
	}
	return c.MaxAge == 0 && !c.Expires.IsZero() && !c.Expires.After(now)
}

// domainMatch reports whether host domain-matches domain (RFC 6265 §5.1.3):
// exact equality, or host is a subdomain of domain. Both must be lowercased.
// IP literals only match exactly.
func domainMatch(host, domain string) bool {
	if host == domain {
		return true
	}
	if net.ParseIP(host) != nil {
		return false
	}
	return strings.HasSuffix(host, "."+domain)
}

// defaultCookiePath implements RFC 6265 §5.1.4 default-path: "/" unless the
// request path contains more than one "/", in which case the path up to (not
// including) the right-most "/".
func defaultCookiePath(p string) string {
	if len(p) < 2 || p[0] != '/' {
		return "/"
	}
	i := strings.LastIndexByte(p, '/')
	if i == 0 {
		return "/"
	}
	return p[:i]
}

// pathMatch implements RFC 6265 §5.1.4 path-match.
func pathMatch(reqPath, cookiePath string) bool {
	if reqPath == "" {
		reqPath = "/"
	}
	if reqPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(reqPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || reqPath[len(cookiePath)] == '/'
}
