package extractor

import "testing"

func TestCookieMatchesRequest_DomainCookieAttachesToSubdomain(t *testing.T) {
	cookie := SessionCookie{Name: "sid", Domain: ".alpha.test", Path: "/", HostOnly: false}
	if !CookieMatchesRequest(cookie, "https://api.alpha.test/x") {
		t.Fatal("Domain cookie .alpha.test should match https://api.alpha.test/x")
	}
}

func TestCookieMatchesRequest_HostOnlyDoesNotMatchSubdomain(t *testing.T) {
	cookie := SessionCookie{Name: "sid", Domain: "alpha.test", Path: "/", HostOnly: true}
	if CookieMatchesRequest(cookie, "https://api.alpha.test/x") {
		t.Fatal("host-only alpha.test must not match https://api.alpha.test/x")
	}
}

func TestCookieMatchesRequest_SecureSkippedOnHTTP(t *testing.T) {
	cookie := SessionCookie{Name: "sid", Domain: "api.alpha.test", Path: "/", Secure: true, HostOnly: true}
	if CookieMatchesRequest(cookie, "http://api.alpha.test/x") {
		t.Fatal("secure cookie must not match http://api.alpha.test/x")
	}
}

func TestCookieMatchesRequest_PathPrefix(t *testing.T) {
	cookie := SessionCookie{Name: "sid", Domain: "api.alpha.test", Path: "/s", HostOnly: true}
	if !CookieMatchesRequest(cookie, "https://api.alpha.test/s") {
		t.Fatal("path /s should match /s")
	}
	if !CookieMatchesRequest(cookie, "https://api.alpha.test/s/item") {
		t.Fatal("path /s should match /s/item")
	}
	if CookieMatchesRequest(cookie, "https://api.alpha.test/other") {
		t.Fatal("path /s must not match /other")
	}
}

func TestCookieMatchesRequest_LeadingDotNormalizes(t *testing.T) {
	cookie := SessionCookie{Name: "sid", Domain: ".alpha.test", Path: "/", HostOnly: false}
	if !CookieMatchesRequest(cookie, "https://api.alpha.test/x") {
		t.Fatal("leading-dot .alpha.test should normalize and match api.alpha.test")
	}
}

func TestCookieMatchesRequest_PublicSuffixDomainSkipped(t *testing.T) {
	for _, domain := range []string{"com", "co.uk", "test"} {
		cookie := SessionCookie{Name: "sid", Domain: domain, Path: "/", HostOnly: false}
		if CookieMatchesRequest(cookie, "https://api.alpha.test/x") {
			t.Fatalf("public suffix Domain %q must be skipped", domain)
		}
		if CookieMatchesRequest(cookie, "https://www.example.co.uk/") && domain == "co.uk" {
			t.Fatal("public suffix Domain co.uk must be skipped")
		}
	}
}

func TestCookieMatchesRequest_ExampleCoUkMatchesWWW(t *testing.T) {
	cookie := SessionCookie{Name: "sid", Domain: "example.co.uk", Path: "/", HostOnly: false}
	if !CookieMatchesRequest(cookie, "https://www.example.co.uk/") {
		t.Fatal("Domain example.co.uk should match https://www.example.co.uk/")
	}
}

func TestCookieMatchesRequest_HostPrefixRequiresHTTPSHostOnlyRootPath(t *testing.T) {
	base := SessionCookie{Name: "__Host-sid", Domain: "api.alpha.test", Path: "/", Secure: true, HostOnly: true}
	if !CookieMatchesRequest(base, "https://api.alpha.test/x") {
		t.Fatal("__Host-sid should match when https + host_only + path /")
	}
	insecure := base
	insecure.Secure = false
	if CookieMatchesRequest(insecure, "https://api.alpha.test/x") {
		t.Fatal("__Host-sid must be skipped without secure")
	}
	notHostOnly := base
	notHostOnly.HostOnly = false
	if CookieMatchesRequest(notHostOnly, "https://api.alpha.test/x") {
		t.Fatal("__Host-sid must be skipped without host_only")
	}
	nestedPath := base
	nestedPath.Path = "/s"
	if CookieMatchesRequest(nestedPath, "https://api.alpha.test/s") {
		t.Fatal("__Host-sid must be skipped unless path is /")
	}
	if CookieMatchesRequest(base, "http://api.alpha.test/x") {
		t.Fatal("__Host-sid must be skipped on http")
	}
}

func TestCookieMatchesRequest_SecurePrefixRequiresHTTPSSecure(t *testing.T) {
	cookie := SessionCookie{Name: "__Secure-sid", Domain: "api.alpha.test", Path: "/", Secure: true, HostOnly: true}
	if !CookieMatchesRequest(cookie, "https://api.alpha.test/x") {
		t.Fatal("__Secure-sid should match https + secure")
	}
	insecure := cookie
	insecure.Secure = false
	if CookieMatchesRequest(insecure, "https://api.alpha.test/x") {
		t.Fatal("__Secure-sid must be skipped without secure")
	}
	if CookieMatchesRequest(cookie, "http://api.alpha.test/x") {
		t.Fatal("__Secure-sid must be skipped on http")
	}
}

func TestCookiesForRequest_RetainsSameNameDifferentPaths(t *testing.T) {
	cookies := []SessionCookie{
		{Name: "sid", Value: "root", Domain: "api.alpha.test", Path: "/", HostOnly: true},
		{Name: "sid", Value: "nested", Domain: "api.alpha.test", Path: "/s", HostOnly: true},
	}
	header := CookiesForRequest(cookies, "https://api.alpha.test/s/item")
	if header == "" {
		t.Fatal("both matching sid cookies should be retained")
	}
	if !containsCookiePair(header, "sid=nested") || !containsCookiePair(header, "sid=root") {
		t.Fatalf("header = %q, want both sid cookies", header)
	}
}

func TestCookieMatchesRequest_IPOnlyMatchesHostOnlyIPCookie(t *testing.T) {
	hostOnlyIP := SessionCookie{Name: "sid", Domain: "192.0.2.1", Path: "/", HostOnly: true}
	if !CookieMatchesRequest(hostOnlyIP, "http://192.0.2.1/x") {
		t.Fatal("host-only IP cookie should match the same IP request")
	}
	domainIP := SessionCookie{Name: "sid", Domain: "192.0.2.1", Path: "/", HostOnly: false}
	if CookieMatchesRequest(domainIP, "http://192.0.2.1/x") {
		t.Fatal("Domain cookies must never match IP requests")
	}
}

func TestCookiesForRequest_DropsNonOctetPairs(t *testing.T) {
	cookies := []SessionCookie{
		{Name: "sid", Value: "ok", Domain: "api.alpha.test", Path: "/", HostOnly: true},
		{Name: "session", Value: "a; b", Domain: "api.alpha.test", Path: "/", HostOnly: true},
	}
	header := CookiesForRequest(cookies, "https://api.alpha.test/x")
	if header != "sid=ok" {
		t.Fatalf("header = %q, want sid=ok without split value", header)
	}
}

func TestCanonicalCookieFields_NormalizesDomainPathName(t *testing.T) {
	got := CanonicalCookieFields(SessionCookie{
		Name: "SID", Domain: ".Alpha.TEST", Path: "", Secure: true, HostOnly: false,
	})
	if got.Name != "sid" || got.Domain != "alpha.test" || got.Path != "/" || !got.Secure || got.HostOnly {
		t.Fatalf("canonical = %+v", got)
	}
}

func TestCookieDomainRelatedToSource_KeepsSiblingAndDropsCrossSite(t *testing.T) {
	const source = "https://share.alpha.test/s"
	if !CookieDomainRelatedToSource(".alpha.test", source) {
		t.Fatal("Domain cookie .alpha.test should relate to share.alpha.test")
	}
	if !CookieDomainRelatedToSource("api.alpha.test", source) {
		t.Fatal("sibling api.alpha.test should relate to share.alpha.test")
	}
	if CookieDomainRelatedToSource(".other.test", source) {
		t.Fatal("other.test must not relate to share.alpha.test")
	}
	if CookieDomainRelatedToSource("", source) {
		t.Fatal("empty cookie domain must not relate")
	}
	if CookieDomainRelatedToSource("test", source) {
		t.Fatal("public-suffix Domain test must not relate via suffix fallback")
	}
	if CookieDomainRelatedToSource(".test", source) {
		t.Fatal("leading-dot public suffix must not relate via suffix fallback")
	}
	if !CookieDomainRelatedToSource("localhost", "https://localhost/s") {
		t.Fatal("exact localhost host should relate when PSL lookup fails")
	}
}

func TestValidCookieName_RejectsEquals(t *testing.T) {
	if ValidCookieName("foo=bar") {
		t.Fatal("cookie-name must reject '='")
	}
	if !ValidCookieName("sid") {
		t.Fatal("sid should be a valid cookie-name")
	}
}

func TestCookiesForRequest_EmptyFilteredList(t *testing.T) {
	cookies := []SessionCookie{
		{Name: "sid", Domain: "files.alpha.test", Path: "/", HostOnly: true},
	}
	if got := CookiesForRequest(cookies, "https://api.alpha.test/x"); got != "" {
		t.Fatalf("CookiesForRequest() = %q, want empty", got)
	}
	if got := CookiesForRequest(nil, "https://api.alpha.test/x"); got != "" {
		t.Fatalf("CookiesForRequest(nil) = %q, want empty", got)
	}
}

func containsCookiePair(header, pair string) bool {
	for _, part := range splitCookieHeader(header) {
		if part == pair {
			return true
		}
	}

	return false
}

func splitCookieHeader(header string) []string {
	parts := make([]string, 0)
	start := 0
	for i := 0; i < len(header); i++ {
		if i+1 < len(header) && header[i] == ';' && header[i+1] == ' ' {
			parts = append(parts, header[start:i])
			start = i + 2
			i++
		}
	}
	if start <= len(header) {
		parts = append(parts, header[start:])
	}

	return parts
}
