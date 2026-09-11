package extractor

import (
	"strings"
	"testing"
	"time"
)

var grantTestNow = time.UnixMilli(1_800_000_000_000)

func validGrantSpec() BrowserHeaderGrantSpec {
	nowMs := grantTestNow.UnixMilli()
	return BrowserHeaderGrantSpec{
		SourceOrigin:     "https://share.fixture.invalid",
		TargetURL:        "https://api.fixture.invalid/resolve/fixture-item",
		Method:           "GET",
		CapturedAtUnixMs: nowMs - 1_000,
		ExpiresAtUnixMs:  nowMs + 5_000,
		Headers: []BrowserHeaderSpec{
			{Name: "x-fixture-token", Value: "fixture-x-token-value"},
			{Name: "authorization", Value: "Bearer fixture-grant-secret"},
		},
	}
}

func grantSpecMutated(t *testing.T, mutate func(*BrowserHeaderGrantSpec)) BrowserHeaderGrantSpec {
	t.Helper()
	spec := validGrantSpec()
	if mutate != nil {
		mutate(&spec)
	}
	return spec
}

func requireGrantError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("ValidateBrowserHeaderGrants() error = nil, want invalid browser header grant")
	}
	if err.Error() != "invalid browser header grant" {
		t.Fatalf("ValidateBrowserHeaderGrants() error = %q, want static invalid message", err.Error())
	}
}

func TestValidateBrowserHeaderGrants_Valid(t *testing.T) {
	spec := validGrantSpec()
	// Headers intentionally out of order; output must be name-sorted.
	grants, err := ValidateBrowserHeaderGrants([]BrowserHeaderGrantSpec{spec}, "https://share.fixture.invalid", grantTestNow)
	if err != nil {
		t.Fatalf("ValidateBrowserHeaderGrants() error = %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grants = %#v, want 1", grants)
	}
	got := grants[0]
	if got.SourceOrigin != spec.SourceOrigin || got.TargetURL != spec.TargetURL || got.Method != spec.Method ||
		got.CapturedAtUnixMs != spec.CapturedAtUnixMs || got.ExpiresAtUnixMs != spec.ExpiresAtUnixMs {
		t.Fatalf("grant fields = %#v", got)
	}
	if len(got.Headers) != 2 || got.Headers[0].Name != "authorization" || got.Headers[1].Name != "x-fixture-token" {
		t.Fatalf("headers = %#v, want name-sorted", got.Headers)
	}

	// HTTP source origin is allowed.
	httpSpec := grantSpecMutated(t, func(s *BrowserHeaderGrantSpec) { s.SourceOrigin = "http://share.fixture.invalid" })
	if _, err := ValidateBrowserHeaderGrants([]BrowserHeaderGrantSpec{httpSpec}, "http://share.fixture.invalid", grantTestNow); err != nil {
		t.Fatalf("http source origin: error = %v", err)
	}

	// Same target with different methods is a different scope.
	get := validGrantSpec()
	head := validGrantSpec()
	head.Method = "HEAD"
	if _, err := ValidateBrowserHeaderGrants([]BrowserHeaderGrantSpec{get, head}, "https://share.fixture.invalid", grantTestNow); err != nil {
		t.Fatalf("GET+HEAD same target: error = %v", err)
	}

	// Aggregate value size exactly at the cap is accepted.
	edge := validGrantSpec()
	edge.Headers = []BrowserHeaderSpec{
		{Name: "x-a", Value: strings.Repeat("a", 4096)},
		{Name: "x-b", Value: strings.Repeat("b", 4096)},
	}
	if _, err := ValidateBrowserHeaderGrants([]BrowserHeaderGrantSpec{edge}, "https://share.fixture.invalid", grantTestNow); err != nil {
		t.Fatalf("aggregate == 8192: error = %v", err)
	}

	// Eight distinct-scope grants accepted.
	var many []BrowserHeaderGrantSpec
	for i := range 8 {
		s := validGrantSpec()
		s.TargetURL = "https://api.fixture.invalid/r/" + string(rune('0'+i))
		many = append(many, s)
	}
	if _, err := ValidateBrowserHeaderGrants(many, "https://share.fixture.invalid", grantTestNow); err != nil {
		t.Fatalf("8 grants: error = %v", err)
	}

	// Validated output is defensively detached from the input slice.
	spec.Headers[0].Value = "mutated"
	if grants[0].Headers[1].Value == "mutated" {
		t.Fatal("validated grant aliases input header slice")
	}
}

func TestValidateBrowserHeaderGrants_Invalid(t *testing.T) {
	nowMs := grantTestNow.UnixMilli()
	longValue := strings.Repeat("v", 4097)
	longName := "x-" + strings.Repeat("n", 127) // 129 bytes

	type table struct {
		name         string
		mutate       func(*BrowserHeaderGrantSpec)
		specs        []BrowserHeaderGrantSpec
		sourceOrigin string
	}
	cases := []table{
		{name: "empty grant list", specs: nil},
		{name: "grant list empty slice", specs: []BrowserHeaderGrantSpec{}},
		{name: "zero headers", mutate: func(s *BrowserHeaderGrantSpec) { s.Headers = nil }},
		{name: "zero headers empty slice", mutate: func(s *BrowserHeaderGrantSpec) { s.Headers = []BrowserHeaderSpec{} }},
		{name: "name 129 bytes", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: longName, Value: "v"}}
		}},
		{name: "value 4097 bytes", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x-big", Value: longValue}}
		}},
		{name: "aggregate over 8192", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{
				{Name: "x-a", Value: strings.Repeat("a", 4096)},
				{Name: "x-b", Value: strings.Repeat("b", 4096)},
				{Name: "x-c", Value: "c"},
			}
		}},
		{name: "value NUL", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x-a", Value: "pre\x00post"}}
		}},
		{name: "value C0", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x-a", Value: "pre\x1fpost"}}
		}},
		{name: "value DEL", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x-a", Value: "pre\x7fpost"}}
		}},
		{name: "value leading space", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x-a", Value: " v"}}
		}},
		{name: "value trailing tab", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x-a", Value: "v\t"}}
		}},
		{name: "value empty", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x-a", Value: ""}}
		}},
		{name: "uppercase name", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "X-Fixture-Token", Value: "v"}}
		}},
		{name: "mixed case name", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x-Fixture", Value: "v"}}
		}},
		{name: "space in name", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x bad", Value: "v"}}
		}},
		{name: "brace in name", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "x{bad}", Value: "v"}}
		}},
		{name: "empty name", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "", Value: "v"}}
		}},
		{name: "authorization basic", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "authorization", Value: "Basic dXNlcjpwYXNz"}}
		}},
		{name: "authorization digest", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "authorization", Value: "Digest abc"}}
		}},
		{name: "authorization ntlm", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "authorization", Value: "NTLM TlRMTVNT"}}
		}},
		{name: "authorization negotiate", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "authorization", Value: "Negotiate YII="}}
		}},
		{name: "authorization no scheme separator", mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: "authorization", Value: "justtoken"}}
		}},
		{name: "method lower get", mutate: func(s *BrowserHeaderGrantSpec) { s.Method = "get" }},
		{name: "method mixed", mutate: func(s *BrowserHeaderGrantSpec) { s.Method = "Get" }},
		{name: "method post", mutate: func(s *BrowserHeaderGrantSpec) { s.Method = "POST" }},
		{name: "method empty", mutate: func(s *BrowserHeaderGrantSpec) { s.Method = "" }},
		{name: "captured zero", mutate: func(s *BrowserHeaderGrantSpec) { s.CapturedAtUnixMs = 0 }},
		{name: "expires zero", mutate: func(s *BrowserHeaderGrantSpec) { s.ExpiresAtUnixMs = 0 }},
		{name: "captured negative", mutate: func(s *BrowserHeaderGrantSpec) { s.CapturedAtUnixMs = -5 }},
		{name: "expires equals captured", mutate: func(s *BrowserHeaderGrantSpec) {
			s.ExpiresAtUnixMs = s.CapturedAtUnixMs
		}},
		{name: "expires before captured", mutate: func(s *BrowserHeaderGrantSpec) {
			s.ExpiresAtUnixMs = s.CapturedAtUnixMs - 1
		}},
		{name: "ttl over 60s", mutate: func(s *BrowserHeaderGrantSpec) {
			s.ExpiresAtUnixMs = s.CapturedAtUnixMs + 60_001
		}},
		{name: "captured beyond skew", mutate: func(s *BrowserHeaderGrantSpec) {
			s.CapturedAtUnixMs = nowMs + 30_001
			s.ExpiresAtUnixMs = nowMs + 60_000
		}},
		{name: "expired at now", mutate: func(s *BrowserHeaderGrantSpec) {
			s.ExpiresAtUnixMs = nowMs
		}},
		{name: "expired before now", mutate: func(s *BrowserHeaderGrantSpec) {
			s.ExpiresAtUnixMs = nowMs - 1
		}},
		{name: "source uppercase host", mutate: func(s *BrowserHeaderGrantSpec) {
			s.SourceOrigin = "https://SHARE.fixture.invalid"
		}},
		{name: "source redundant port", mutate: func(s *BrowserHeaderGrantSpec) {
			s.SourceOrigin = "https://share.fixture.invalid:443"
		}},
		{name: "source with path", mutate: func(s *BrowserHeaderGrantSpec) {
			s.SourceOrigin = "https://share.fixture.invalid/path"
		}},
		{name: "source userinfo", mutate: func(s *BrowserHeaderGrantSpec) {
			s.SourceOrigin = "https://user@share.fixture.invalid"
		}},
		{name: "source ftp", mutate: func(s *BrowserHeaderGrantSpec) {
			s.SourceOrigin = "ftp://share.fixture.invalid"
		}},
		{name: "source trailing dot", mutate: func(s *BrowserHeaderGrantSpec) {
			s.SourceOrigin = "https://share.fixture.invalid."
		}},
		{name: "target http", mutate: func(s *BrowserHeaderGrantSpec) {
			s.TargetURL = "http://api.fixture.invalid/resolve/fixture-item"
		}},
		{name: "target fragment", mutate: func(s *BrowserHeaderGrantSpec) {
			s.TargetURL = "https://api.fixture.invalid/resolve/fixture-item#frag"
		}},
		{name: "target userinfo", mutate: func(s *BrowserHeaderGrantSpec) {
			s.TargetURL = "https://u@api.fixture.invalid/resolve/fixture-item"
		}},
		{name: "target ipv4", mutate: func(s *BrowserHeaderGrantSpec) {
			s.TargetURL = "https://127.0.0.1/resolve/fixture-item"
		}},
		{name: "target percent host", mutate: func(s *BrowserHeaderGrantSpec) {
			s.TargetURL = "https://ex%61mple.com/x"
		}},
		{name: "target redundant port", mutate: func(s *BrowserHeaderGrantSpec) {
			s.TargetURL = "https://api.fixture.invalid:443/resolve/fixture-item"
		}},
		{name: "target uppercase host", mutate: func(s *BrowserHeaderGrantSpec) {
			s.TargetURL = "https://API.fixture.invalid/resolve/fixture-item"
		}},
		{name: "target too long", mutate: func(s *BrowserHeaderGrantSpec) {
			s.TargetURL = "https://api.fixture.invalid/" + strings.Repeat("p", 4096)
		}},
	}

	for _, denied := range []string{
		"x-real-ip", "x-client-ip", "x-host", "x-original-url", "x-original-host",
		"x-original-path", "x-original-method", "x-rewrite-url", "x-method-override",
	} {
		cases = append(cases, table{name: "denied exact " + denied, mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: denied, Value: "v"}}
		}})
	}
	for _, denied := range []string{
		"x-forwarded-for", "x-forwarded-host", "x-http-method-override", "x-http-method",
		"x-proxy-auth", "x-goaria-internal", "x-override-anything",
	} {
		cases = append(cases, table{name: "denied prefix " + denied, mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: denied, Value: "v"}}
		}})
	}
	for _, ineligible := range []string{
		"cookie", "host", "referer", "origin", "user-agent", "sec-fetch-mode",
		"set-cookie", "content-length", "connection", "te", "trailer", "upgrade",
		"proxy-authorization", "x",
	} {
		cases = append(cases, table{name: "ineligible " + ineligible, mutate: func(s *BrowserHeaderGrantSpec) {
			s.Headers = []BrowserHeaderSpec{{Name: ineligible, Value: "v"}}
		}})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			specs := tc.specs
			if specs == nil && tc.mutate != nil {
				specs = []BrowserHeaderGrantSpec{grantSpecMutated(t, tc.mutate)}
			}
			sourceOrigin := tc.sourceOrigin
			if sourceOrigin == "" {
				sourceOrigin = "https://share.fixture.invalid"
			}
			_, err := ValidateBrowserHeaderGrants(specs, sourceOrigin, grantTestNow)
			requireGrantError(t, err)
		})
	}
}

func TestValidateBrowserHeaderGrants_NineGrantsRejected(t *testing.T) {
	var specs []BrowserHeaderGrantSpec
	for i := range 9 {
		s := validGrantSpec()
		s.TargetURL = "https://api.fixture.invalid/r/" + string(rune('0'+i))
		specs = append(specs, s)
	}
	_, err := ValidateBrowserHeaderGrants(specs, "https://share.fixture.invalid", grantTestNow)
	requireGrantError(t, err)
}

func TestValidateBrowserHeaderGrants_SourceOriginMismatch(t *testing.T) {
	spec := grantSpecMutated(t, func(s *BrowserHeaderGrantSpec) {
		s.SourceOrigin = "https://other.fixture.invalid"
	})
	_, err := ValidateBrowserHeaderGrants([]BrowserHeaderGrantSpec{spec}, "https://share.fixture.invalid", grantTestNow)
	requireGrantError(t, err)
}

func TestValidateBrowserHeaderGrants_DuplicateScope(t *testing.T) {
	first := validGrantSpec()
	second := validGrantSpec()
	second.Headers = []BrowserHeaderSpec{{Name: "x-other", Value: "v"}}
	_, err := ValidateBrowserHeaderGrants([]BrowserHeaderGrantSpec{first, second}, "https://share.fixture.invalid", grantTestNow)
	requireGrantError(t, err)
}

func TestValidateBrowserHeaderGrants_DuplicateHeaderName(t *testing.T) {
	spec := grantSpecMutated(t, func(s *BrowserHeaderGrantSpec) {
		s.Headers = []BrowserHeaderSpec{
			{Name: "x-dup", Value: "one"},
			{Name: "x-dup", Value: "two"},
		}
	})
	_, err := ValidateBrowserHeaderGrants([]BrowserHeaderGrantSpec{spec}, "https://share.fixture.invalid", grantTestNow)
	requireGrantError(t, err)
}

func TestCanonicalBrowserGrantTargetURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "empty", raw: "", ok: false},
		{name: "basic", raw: "https://api.fixture.invalid/p?q=1", want: "https://api.fixture.invalid/p?q=1", ok: true},
		{name: "empty path", raw: "https://api.fixture.invalid", want: "https://api.fixture.invalid/", ok: true},
		{name: "uppercase host redundant port", raw: "https://EXAMPLE.com:443/p?q=1", want: "https://example.com/p?q=1", ok: true},
		{name: "non-default port kept", raw: "https://api.fixture.invalid:8443/p", want: "https://api.fixture.invalid:8443/p", ok: true},
		{name: "encoded path preserved", raw: "https://api.fixture.invalid/%2e%2e/x", want: "https://api.fixture.invalid/%2e%2e/x", ok: true},
		{name: "query order preserved", raw: "https://api.fixture.invalid/p?b=2&a=1", want: "https://api.fixture.invalid/p?b=2&a=1", ok: true},
		{name: "bare query marker dropped", raw: "https://api.fixture.invalid/p?", want: "https://api.fixture.invalid/p", ok: true},
		{name: "http rejected", raw: "http://api.fixture.invalid/p", ok: false},
		{name: "fragment rejected", raw: "https://api.fixture.invalid/p#frag", ok: false},
		{name: "userinfo rejected", raw: "https://u:p@api.fixture.invalid/p", ok: false},
		{name: "ipv4 rejected", raw: "https://127.0.0.1/p", ok: false},
		{name: "ipv6 rejected", raw: "https://[2001:db8::1]/p", ok: false},
		{name: "percent host rejected", raw: "https://ex%61mple.com/p", ok: false},
		{name: "bad port rejected", raw: "https://api.fixture.invalid:bad/p", ok: false},
		{name: "trailing dot rejected", raw: "https://api.fixture.invalid./p", ok: false},
		{name: "no host rejected", raw: "https:///p", ok: false},
		{name: "over 4096 rejected", raw: "https://api.fixture.invalid/" + strings.Repeat("p", 4096), ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalBrowserGrantTargetURL(tc.raw)
			if ok != tc.ok {
				t.Fatalf("CanonicalBrowserGrantTargetURL(%q) ok=%v, want %v", tc.raw, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("CanonicalBrowserGrantTargetURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if !ok && got != "" {
				t.Fatalf("reject must return empty, got %q", got)
			}
		})
	}
}

func TestCanonicalBrowserGrantSourceOrigin(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "empty", raw: "", ok: false},
		{name: "https basic", raw: "https://share.fixture.invalid", want: "https://share.fixture.invalid", ok: true},
		{name: "https path dropped", raw: "https://EXAMPLE.com:443/x", want: "https://example.com", ok: true},
		{name: "http basic", raw: "http://share.fixture.invalid", want: "http://share.fixture.invalid", ok: true},
		{name: "http default port dropped", raw: "http://share.fixture.invalid:80/x", want: "http://share.fixture.invalid", ok: true},
		{name: "non-default port kept", raw: "http://share.fixture.invalid:8080", want: "http://share.fixture.invalid:8080", ok: true},
		{name: "ftp rejected", raw: "ftp://share.fixture.invalid", ok: false},
		{name: "userinfo rejected", raw: "https://user@share.fixture.invalid", ok: false},
		{name: "ipv4 rejected", raw: "https://127.0.0.1", ok: false},
		{name: "percent host rejected", raw: "https://ex%61mple.com", ok: false},
		{name: "trailing dot rejected", raw: "https://share.fixture.invalid.", ok: false},
		{name: "no host rejected", raw: "https://", ok: false},
		{name: "over 512 rejected", raw: "https://" + strings.Repeat("a", 510), ok: false},
		// A long *input* URL is fine: only the canonical origin output is capped.
		{name: "long input url canonicalizes", raw: "https://share.fixture.invalid/" + strings.Repeat("p", 600), want: "https://share.fixture.invalid", ok: true},
		{name: "over 2048 input rejected", raw: "https://share.fixture.invalid/" + strings.Repeat("p", 2048), ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalBrowserGrantSourceOrigin(tc.raw)
			if ok != tc.ok {
				t.Fatalf("CanonicalBrowserGrantSourceOrigin(%q) ok=%v, want %v", tc.raw, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("CanonicalBrowserGrantSourceOrigin(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if !ok && got != "" {
				t.Fatalf("reject must return empty, got %q", got)
			}
		})
	}
}

func validateGrantForMatch(t *testing.T, mutate func(*BrowserHeaderGrantSpec)) BrowserHeaderGrant {
	t.Helper()
	spec := grantSpecMutated(t, mutate)
	grants, err := ValidateBrowserHeaderGrants([]BrowserHeaderGrantSpec{spec}, "https://share.fixture.invalid", grantTestNow)
	if err != nil {
		t.Fatalf("ValidateBrowserHeaderGrants() error = %v", err)
	}
	return grants[0]
}

func TestBrowserGrantMatch_MethodAndTarget(t *testing.T) {
	grant := validateGrantForMatch(t, nil)
	grants := []BrowserHeaderGrant{grant}

	if got, _ := browserGrantMatch(grants, "GET", grant.TargetURL, grantTestNow); got == nil || got.TargetURL != grant.TargetURL || got.Method != grant.Method {
		t.Fatalf("match = %#v, want hit", got)
	}
	if got, _ := browserGrantMatch(grants, "HEAD", grant.TargetURL, grantTestNow); got != nil {
		t.Fatalf("HEAD request must not match GET grant: %#v", got)
	}

	head := validateGrantForMatch(t, func(s *BrowserHeaderGrantSpec) { s.Method = "HEAD" })
	if got, _ := browserGrantMatch([]BrowserHeaderGrant{head}, "HEAD", head.TargetURL, grantTestNow); got == nil {
		t.Fatal("HEAD request must match HEAD grant")
	}
	if got, _ := browserGrantMatch([]BrowserHeaderGrant{head}, "GET", head.TargetURL, grantTestNow); got != nil {
		t.Fatal("GET request must not match HEAD grant")
	}

	// Non-canonical request URL that canonicalizes to the grant target hits,
	// and the returned wire target is the canonical form of the request URL.
	if got, target := browserGrantMatch(grants, "GET", "https://api.fixture.invalid:443/resolve/fixture-item", grantTestNow); got == nil || target != grant.TargetURL {
		t.Fatalf("match = %#v target = %q, want canonical hit", got, target)
	}
	if got, _ := browserGrantMatch(grants, "GET", "https://api.fixture.invalid/resolve/other", grantTestNow); got != nil {
		t.Fatalf("different path must not match: %#v", got)
	}
	if got, _ := browserGrantMatch(grants, "GET", grant.TargetURL+"?extra=1", grantTestNow); got != nil {
		t.Fatalf("different query must not match: %#v", got)
	}
	if got, _ := browserGrantMatch(nil, "GET", grant.TargetURL, grantTestNow); got != nil {
		t.Fatalf("nil grants must not match: %#v", got)
	}
	if got, _ := browserGrantMatch(grants, "GET", "not a url", grantTestNow); got != nil {
		t.Fatalf("unparseable request URL must not match: %#v", got)
	}
}

func TestBrowserGrantMatch_TTLBoundary(t *testing.T) {
	grant := validateGrantForMatch(t, nil)
	grants := []BrowserHeaderGrant{grant}
	atExpiry := time.UnixMilli(grant.ExpiresAtUnixMs)
	if got, _ := browserGrantMatch(grants, "GET", grant.TargetURL, atExpiry); got != nil {
		t.Fatalf("expired grant must not match at expires_at: %#v", got)
	}
	beforeExpiry := time.UnixMilli(grant.ExpiresAtUnixMs - 1)
	if got, _ := browserGrantMatch(grants, "GET", grant.TargetURL, beforeExpiry); got == nil {
		t.Fatal("live grant must match one ms before expiry")
	}
}

func fingerprintTestContext(t *testing.T) BrowserRequestContext {
	t.Helper()
	grant := validateGrantForMatch(t, nil)
	return BrowserRequestContext{
		Cookies:        []SessionCookie{{Name: "sid", Value: "fixture-cookie"}},
		UserAgent:      "fixture-ua",
		AcceptLanguage: "en-US",
		RefererOrigin:  "https://share.fixture.invalid",
		Grants:         []BrowserHeaderGrant{grant},
	}
}

func TestBrowserContextFingerprint_DeterministicAndOrderIndependent(t *testing.T) {
	bc := fingerprintTestContext(t)
	other := validateGrantForMatch(t, func(s *BrowserHeaderGrantSpec) {
		s.TargetURL = "https://api.fixture.invalid/resolve/other"
	})
	bc.Grants = append(bc.Grants, other)
	first := BrowserContextFingerprint(bc)
	if first == "" {
		t.Fatal("fingerprint must be non-empty")
	}
	if got := BrowserContextFingerprint(bc); got != first {
		t.Fatalf("fingerprint not deterministic: %q vs %q", first, got)
	}

	reordered := bc
	reordered.Grants = []BrowserHeaderGrant{other, bc.Grants[0]}
	if got := BrowserContextFingerprint(reordered); got != first {
		t.Fatalf("grant order must not change fingerprint: %q vs %q", first, got)
	}
}

func TestBrowserContextFingerprint_FieldSensitivity(t *testing.T) {
	base := fingerprintTestContext(t)
	want := BrowserContextFingerprint(base)

	mutations := map[string]func(*BrowserRequestContext){
		"user agent":      func(b *BrowserRequestContext) { b.UserAgent = "other-ua" },
		"accept language": func(b *BrowserRequestContext) { b.AcceptLanguage = "fr" },
		"referer origin":  func(b *BrowserRequestContext) { b.RefererOrigin = "https://other.fixture.invalid" },
		"header value": func(b *BrowserRequestContext) {
			b.Grants[0].Headers[0].Value = "Bearer fixture-other-secret"
		},
		"grant expires": func(b *BrowserRequestContext) { b.Grants[0].ExpiresAtUnixMs++ },
		"grant captured": func(b *BrowserRequestContext) {
			b.Grants[0].CapturedAtUnixMs--
		},
		"grant target": func(b *BrowserRequestContext) {
			b.Grants[0].TargetURL = "https://api.fixture.invalid/resolve/other"
		},
		"grant method": func(b *BrowserRequestContext) { b.Grants[0].Method = "HEAD" },
		"grant source": func(b *BrowserRequestContext) {
			b.Grants[0].SourceOrigin = "https://other.fixture.invalid"
		},
		"grant removed": func(b *BrowserRequestContext) { b.Grants = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bc := fingerprintTestContext(t)
			mutate(&bc)
			if got := BrowserContextFingerprint(bc); got == want {
				t.Fatalf("fingerprint must differ from base: %q", got)
			}
		})
	}
}

func TestBrowserContextFingerprint_EmptyForZeroContext(t *testing.T) {
	if got := BrowserContextFingerprint(BrowserRequestContext{}); got != "" {
		t.Fatalf("zero context fingerprint = %q, want empty", got)
	}
	if got := BrowserContextFingerprint(BrowserRequestContext{
		Cookies: []SessionCookie{{Name: "sid", Value: "fixture-cookie"}},
	}); got != "" {
		t.Fatalf("cookie-only context fingerprint = %q, want empty", got)
	}
}

func TestWithBrowserContext_InstallsFieldsAndCopies(t *testing.T) {
	grant := validateGrantForMatch(t, nil)
	cookies := []SessionCookie{{Name: "sid", Value: "fixture-cookie", Domain: ".fixture.invalid"}}
	grants := []BrowserHeaderGrant{grant}
	bc := BrowserRequestContext{
		Cookies:        cookies,
		UserAgent:      "fixture-ua",
		AcceptLanguage: "en-US",
		RefererOrigin:  "https://share.fixture.invalid",
		Grants:         grants,
	}
	ctx := WithBrowserContext(t.Context(), bc)

	got := browserContextFromContext(ctx)
	if got.UserAgent != "fixture-ua" || got.AcceptLanguage != "en-US" || got.RefererOrigin != "https://share.fixture.invalid" {
		t.Fatalf("typed fields = %#v", got)
	}
	if len(got.Cookies) != 1 || got.Cookies[0].Name != "sid" {
		t.Fatalf("cookies = %#v", got.Cookies)
	}
	if len(got.Grants) != 1 || len(got.Grants[0].Headers) != len(grant.Headers) {
		t.Fatalf("grants = %#v", got.Grants)
	}

	// Mutating the caller's slices must not affect the stored context.
	cookies[0].Value = "mutated"
	grants[0].Headers[0].Value = "mutated"
	again := browserContextFromContext(ctx)
	if again.Cookies[0].Value == "mutated" || again.Grants[0].Headers[0].Value == "mutated" {
		t.Fatal("WithBrowserContext must defensively copy cookies and grant headers")
	}
	if got := browserCookiesFromContext(ctx); len(got) != 1 || got[0].Name != "sid" {
		t.Fatalf("browserCookiesFromContext = %#v", got)
	}
}

func TestWithBrowserCookies_RemainsThinWrapper(t *testing.T) {
	ctx := WithBrowserCookies(t.Context(), []SessionCookie{{Name: "sid", Value: "v"}})
	if got := browserCookiesFromContext(ctx); len(got) != 1 || got[0].Name != "sid" {
		t.Fatalf("browserCookiesFromContext = %#v", got)
	}
	if LastHTTPFetchStatus(ctx) != 0 {
		t.Fatalf("last status = %d, want 0", LastHTTPFetchStatus(ctx))
	}
	recordLastHTTPFetchStatus(ctx, 418)
	if LastHTTPFetchStatus(ctx) != 418 {
		t.Fatalf("last status = %d, want 418 (status slot must be installed)", LastHTTPFetchStatus(ctx))
	}
	if got := browserContextFromContext(ctx); len(got.Grants) != 0 || got.UserAgent != "" {
		t.Fatalf("cookie wrapper must not invent other fields: %#v", got)
	}
}
