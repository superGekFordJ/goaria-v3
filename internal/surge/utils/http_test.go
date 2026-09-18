package utils

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

func TestSameSite(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		// Exact matches
		{"exact same domain", "example.com", "example.com", true},
		{"exact same IP", "127.0.0.1", "127.0.0.1", true},
		{"exact same IPv6", "[::1]", "[::1]", true},

		// Subdomains on standard TLDs
		{"standard TLD subdomains", "a.example.com", "b.example.com", true},
		{"deep subdomains", "x.y.z.example.com", "example.com", true},
		{"easynews subdomains", "members.easynews.com", "iad-dl-08.easynews.com", true},

		// Complex TLDs (eTLD+1)
		{"complex TLD subdomains", "a.example.co.uk", "b.example.co.uk", true},
		{"different sites on complex TLD", "example.co.uk", "other.co.uk", false},
		{"github.io subdomains (private TLD)", "user1.github.io", "user2.github.io", false},
		{"github.io same site", "user1.github.io", "sub.user1.github.io", true},

		// Cross-site cases
		{"filmyzilla cross-site domains", "1.filmyzilla.vin", "cdn-02-nl-zilla.filmyzdl.com", false},
		{"different domains", "example.com", "other.com", false},
		{"different TLDs", "example.com", "example.org", false},
		{"IP vs localhost", "127.0.0.1", "localhost", false},
		{"different IPs", "192.168.1.1", "192.168.1.2", false},

		// Port handling
		{"with same ports", "example.com:8080", "sub.example.com:8080", true},
		{"with different ports", "example.com:80", "sub.example.com:443", true},
		{"IP with port", "127.0.0.1:8080", "127.0.0.1:9090", true},
		{"one with port, one without", "example.com", "sub.example.com:8080", true},

		// Malformed or empty
		{"empty strings", "", "", true},
		{"one empty string", "example.com", "", false},
		{"invalid host format", "invalid host name", "example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SameSite(tt.a, tt.b); got != tt.want {
				t.Errorf("SameSite(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCopyRedirectHeaders(t *testing.T) {
	// Go's http.Client inherently forwards standard headers (Range, User-Agent)
	// on redirects, but drops credentials (Cookie, Authorization) on cross-origin redirects.
	// CopyRedirectHeaders is designed to restore credentials specifically for cross-origin BUT same-site redirects.

	tests := []struct {
		name           string
		dstURL         string
		srcURL         string
		srcHeaders     http.Header
		initialDstHdr  http.Header
		expectedDstHdr http.Header
	}{
		// Same-site credential restoration
		{
			name:   "same-site easynews restores stripped credentials",
			dstURL: "https://iad-dl-08.easynews.com/file",
			srcURL: "https://members.easynews.com/file",
			srcHeaders: http.Header{
				"Authorization": []string{"Basic dXNlcjpwYXNz"},
				"Cookie":        []string{"session=123"},
				"Cookie2":       []string{"legacy=456"},
				"Range":         []string{"bytes=0-100"},
				"User-Agent":    []string{"Surge/1.0"},
			},
			initialDstHdr: http.Header{
				"Range":      []string{"bytes=0-100"},
				"User-Agent": []string{"Surge/1.0"},
			},
			expectedDstHdr: http.Header{
				"Authorization": []string{"Basic dXNlcjpwYXNz"},
				"Cookie":        []string{"session=123"},
				"Cookie2":       []string{"legacy=456"},
				"Range":         []string{"bytes=0-100"},
				"User-Agent":    []string{"Surge/1.0"},
			},
		},
		{
			name:   "same-site with only Cookie",
			dstURL: "https://auth.example.com",
			srcURL: "https://api.example.com",
			srcHeaders: http.Header{
				"Cookie": []string{"token=foo"},
			},
			initialDstHdr: http.Header{},
			expectedDstHdr: http.Header{
				"Cookie": []string{"token=foo"},
			},
		},
		// Cross-site credential stripping (trusting Go)
		{
			name:   "cross-site filmyzilla respects standard library stripping",
			dstURL: "https://cdn-02-nl-zilla.filmyzdl.com/movie.mp4",
			srcURL: "https://1.filmyzilla.vin/download",
			srcHeaders: http.Header{
				"Cookie":     []string{"sess=abc"},
				"Referer":    []string{"https://1.filmyzilla.vin/"},
				"User-Agent": []string{"Surge/1.0"},
			},
			initialDstHdr: http.Header{
				// Go stripped Cookie, updated Referer, kept User-Agent
				"Referer":    []string{"https://1.filmyzilla.vin/"},
				"User-Agent": []string{"Surge/1.0"},
			},
			expectedDstHdr: http.Header{
				"Referer":    []string{"https://1.filmyzilla.vin/"},
				"User-Agent": []string{"Surge/1.0"},
			},
		},
		{
			name:   "cross-scheme (http to https) is treated as cross-site",
			dstURL: "https://example.com/file",
			srcURL: "http://example.com/file",
			srcHeaders: http.Header{
				"Authorization": []string{"Basic dXNlcjpwYXNz"},
				"Cookie":        []string{"session=123"},
				"Range":         []string{"bytes=0-100"},
			},
			initialDstHdr: http.Header{
				"Range": []string{"bytes=0-100"},
			},
			expectedDstHdr: http.Header{
				"Range": []string{"bytes=0-100"},
			},
		},
		// Edge cases
		{
			name:           "nil request headers safe check",
			dstURL:         "https://b.example.com",
			srcURL:         "https://a.example.com",
			srcHeaders:     nil, // shouldn't panic
			initialDstHdr:  http.Header{},
			expectedDstHdr: http.Header{},
		},
		{
			name:   "no credentials to restore",
			dstURL: "https://b.example.com",
			srcURL: "https://a.example.com",
			srcHeaders: http.Header{
				"X-Custom-Header": []string{"value"},
			},
			initialDstHdr: http.Header{
				"X-Custom-Header": []string{"value"},
			},
			expectedDstHdr: http.Header{
				"X-Custom-Header": []string{"value"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dstReq := &http.Request{
				URL:    mustParseURL(t, tt.dstURL),
				Header: tt.initialDstHdr,
			}
			srcReq := &http.Request{
				URL:    mustParseURL(t, tt.srcURL),
				Header: tt.srcHeaders,
			}

			CopyRedirectHeaders(dstReq, srcReq)

			if len(dstReq.Header) != len(tt.expectedDstHdr) {
				t.Errorf("Header count mismatch. got %v, want %v", dstReq.Header, tt.expectedDstHdr)
			}
			for k, expectedVals := range tt.expectedDstHdr {
				gotVals := dstReq.Header.Values(k)
				if !equalSlice(gotVals, expectedVals) {
					t.Errorf("Header[%s] = %v, want %v", k, gotVals, expectedVals)
				}
			}
			for k := range dstReq.Header {
				if _, ok := tt.expectedDstHdr[k]; !ok {
					t.Errorf("Unexpected header in dst: %s = %v", k, dstReq.Header.Values(k))
				}
			}
		})
	}
}

func TestCopyRedirectHeaders_RedirectCookieMerge(t *testing.T) {
	// dst.Response.Request models the just-completed hop (the setter request);
	// dst.Response.Header carries its Set-Cookie lines. src models via[0].
	tests := []struct {
		name       string
		dstURL     string
		dstHeaders http.Header // headers on the outgoing request before the call
		prevURL    string      // the redirecting hop's request URL (setter origin)
		prevCookie string      // Cookie header carried by the previous hop
		srcURL     string      // via[0]
		srcCookie  string
		setCookies []string // raw Set-Cookie values on the redirecting response
		wantCookie string   // "" = Cookie header must be absent
	}{
		{
			name:       "same-host bounce merges Set-Cookie",
			dstURL:     "https://a.example.com/dl",
			dstHeaders: http.Header{"Cookie": []string{"init=1"}},
			prevURL:    "https://a.example.com/dl",
			prevCookie: "init=1",
			srcURL:     "https://a.example.com/dl",
			srcCookie:  "init=1",
			setCookies: []string{"sess=abc; Path=/"},
			wantCookie: "init=1; sess=abc",
		},
		{
			name:       "cross-site strips Set-Cookie and carry-over",
			dstURL:     "https://cdn.other.net/file",
			prevURL:    "https://a.example.com/",
			prevCookie: "init=1; sn=old",
			srcURL:     "https://a.example.com/",
			srcCookie:  "init=1",
			setCookies: []string{"sn=1; Path=/"},
			wantCookie: "",
		},
		{
			name:       "host-only cookie does not leak to sibling subdomain",
			dstURL:     "https://b.example.com/",
			prevURL:    "https://a.example.com/",
			prevCookie: "init=1",
			srcURL:     "https://a.example.com/",
			srcCookie:  "init=1",
			setCookies: []string{"h=1; Path=/"},
			wantCookie: "init=1",
		},
		{
			name:       "Domain cookie sent to domain-matched subdomain",
			dstURL:     "https://cdn.example.com/",
			prevURL:    "https://a.example.com/",
			srcURL:     "https://a.example.com/",
			setCookies: []string{"d=1; Domain=example.com; Path=/"},
			wantCookie: "d=1",
		},
		{
			name:       "setter cannot plant cookie for a domain it is not under",
			dstURL:     "https://sub.example.com/",
			prevURL:    "https://example.com/",
			srcURL:     "https://example.com/",
			setCookies: []string{"x=1; Domain=sub.example.com"},
			wantCookie: "",
		},
		{
			name:       "public suffix Domain rejected",
			dstURL:     "https://b.example.co.uk/",
			prevURL:    "https://a.example.co.uk/",
			srcURL:     "https://a.example.co.uk/",
			setCookies: []string{"y=1; Domain=co.uk"},
			wantCookie: "",
		},
		{
			name:       "Secure cookie not sent over http",
			dstURL:     "http://a.example.com/dl",
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"s=1; Secure; Path=/"},
			wantCookie: "",
		},
		{
			name:       "Secure cookie sent over https",
			dstURL:     "https://a.example.com/dl",
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"s=1; Secure; Path=/"},
			wantCookie: "s=1",
		},
		{
			name:       "Secure cookie from insecure setter rejected",
			dstURL:     "https://a.example.com/dl",
			prevURL:    "http://a.example.com/dl",
			srcURL:     "http://a.example.com/dl",
			setCookies: []string{"s=1; Secure; Path=/"},
			wantCookie: "",
		},
		{
			name:       "Path cookie sent to matching subpath",
			dstURL:     "https://a.example.com/dl/x",
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"p=1; Path=/dl"},
			wantCookie: "p=1",
		},
		{
			name:       "Path cookie sent to exact path",
			dstURL:     "https://a.example.com/dl",
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"p=1; Path=/dl"},
			wantCookie: "p=1",
		},
		{
			name:       "Path cookie not sent to non-matching path",
			dstURL:     "https://a.example.com/other",
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"p=1; Path=/dl"},
			wantCookie: "",
		},
		{
			name:       "no Path falls back to setter default-path",
			dstURL:     "https://a.example.com/other",
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"q=1"},
			wantCookie: "q=1",
		},
		{
			name:       "default-path from deeper setter path matches subpath",
			dstURL:     "https://a.example.com/a/x",
			prevURL:    "https://a.example.com/a/b",
			srcURL:     "https://a.example.com/a/b",
			setCookies: []string{"q=1"},
			wantCookie: "q=1",
		},
		{
			name:       "default-path prefix requires boundary slash",
			dstURL:     "https://a.example.com/abc",
			prevURL:    "https://a.example.com/a/b",
			srcURL:     "https://a.example.com/a/b",
			setCookies: []string{"q=1"},
			wantCookie: "",
		},
		{
			name:       "expired Max-Age removes accumulated cookie",
			dstURL:     "https://a.example.com/dl",
			dstHeaders: http.Header{"Cookie": []string{"victim=1; keep=2"}},
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"victim=gone; Max-Age=-1; Path=/"},
			wantCookie: "keep=2",
		},
		{
			name:       "past Expires removes accumulated cookie",
			dstURL:     "https://a.example.com/dl",
			dstHeaders: http.Header{"Cookie": []string{"victim=1"}},
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"victim=gone; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Path=/"},
			wantCookie: "",
		},
		{
			name:       "positive Max-Age overrides past Expires",
			dstURL:     "https://a.example.com/dl",
			dstHeaders: http.Header{},
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"k=1; Max-Age=3600; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Path=/"},
			wantCookie: "k=1",
		},
		{
			name:       "Set-Cookie updates existing name in place",
			dstURL:     "https://a.example.com/dl",
			dstHeaders: http.Header{"Cookie": []string{"init=1; sess=old"}},
			prevURL:    "https://a.example.com/dl",
			prevCookie: "init=1; sess=old",
			srcURL:     "https://a.example.com/dl",
			srcCookie:  "init=1",
			setCookies: []string{"sess=new; Path=/"},
			wantCookie: "init=1; sess=new",
		},
		{
			name:       "cross-site landing page plants its own cookie",
			dstURL:     "https://b.example.net/next",
			prevURL:    "https://b.example.net/",
			srcURL:     "https://a.example.org/",
			srcCookie:  "init=1",
			setCookies: []string{"own=1; Path=/"},
			wantCookie: "own=1",
		},
		{
			// Locked ceiling: a host-only cookie planted on an earlier hop
			// loses its scope metadata once flattened, so carry-over replays
			// it to a same-site sibling subdomain (a real jar would not).
			name:       "carry-over does not re-check per-cookie scope",
			dstURL:     "https://b.example.com/",
			prevURL:    "https://a.example.com/",
			prevCookie: "h=1",
			srcURL:     "https://a.example.com/",
			wantCookie: "h=1",
		},
		{
			// No effective change on this hop: the raw header passes through
			// byte-identical — pairs the parser cannot read stay on the wire.
			name:       "no merge keeps raw Cookie header byte-identical",
			dstURL:     "https://a.example.com/dl",
			dstHeaders: http.Header{"Cookie": []string{"init=1; orphan; bad name=x"}},
			prevURL:    "https://a.example.com/dl",
			prevCookie: "init=1", // same value as seed — carry-over is a no-op
			srcURL:     "https://a.example.com/dl",
			wantCookie: "init=1; orphan; bad name=x",
		},
		{
			// A Set-Cookie echoing the same name+value is a no-op — no rebuild.
			name:       "same-value Set-Cookie leaves raw header untouched",
			dstURL:     "https://a.example.com/dl",
			dstHeaders: http.Header{"Cookie": []string{"init=1; orphan"}},
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"init=1; Path=/"},
			wantCookie: "init=1; orphan",
		},
		{
			// Trade-off: a real merge rebuilds from parsed pairs — segments
			// the parser rejects (here a space in the name) are dropped.
			name:       "merge rebuild drops unparseable segments",
			dstURL:     "https://a.example.com/dl",
			dstHeaders: http.Header{"Cookie": []string{"init=1; bad name=x"}},
			prevURL:    "https://a.example.com/dl",
			srcURL:     "https://a.example.com/dl",
			setCookies: []string{"s=1; Path=/"},
			wantCookie: "init=1; s=1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := &http.Request{
				URL:    mustParseURL(t, tt.dstURL),
				Header: tt.dstHeaders,
			}
			if dst.Header == nil {
				dst.Header = http.Header{}
			}
			prev := &http.Request{
				URL:    mustParseURL(t, tt.prevURL),
				Header: http.Header{},
			}
			if tt.prevCookie != "" {
				prev.Header.Set("Cookie", tt.prevCookie)
			}
			dst.Response = &http.Response{
				Header:  http.Header{},
				Request: prev,
			}
			for _, sc := range tt.setCookies {
				dst.Response.Header.Add("Set-Cookie", sc)
			}
			src := &http.Request{
				URL:    mustParseURL(t, tt.srcURL),
				Header: http.Header{},
			}
			if tt.srcCookie != "" {
				src.Header.Set("Cookie", tt.srcCookie)
			}

			CopyRedirectHeaders(dst, src)

			if got := dst.Header.Get("Cookie"); got != tt.wantCookie {
				t.Errorf("Cookie = %q, want %q", got, tt.wantCookie)
			}
			if tt.wantCookie == "" && len(dst.Header["Cookie"]) > 0 {
				t.Errorf("expected Cookie header to be absent, got %v", dst.Header["Cookie"])
			}
		})
	}
}

// TestCopyRedirectHeaders_MultiHopAccumulation proves carry-over: cookies
// merged on hop N must ride along to hop N+1 (the client only rebuilds
// headers from via[0], so without carry-over they would be lost).
func TestCopyRedirectHeaders_MultiHopAccumulation(t *testing.T) {
	src := &http.Request{
		URL:    mustParseURL(t, "https://a.example.com/dl"),
		Header: http.Header{"Cookie": []string{"init=1"}},
	}

	// Hop 1: /dl 302s to /step2 with Set-Cookie s1=1.
	dst1 := &http.Request{
		URL:    mustParseURL(t, "https://a.example.com/step2"),
		Header: http.Header{"Cookie": []string{"init=1"}},
		Response: &http.Response{
			Header: http.Header{"Set-Cookie": []string{"s1=1; Path=/"}},
			Request: &http.Request{
				URL:    mustParseURL(t, "https://a.example.com/dl"),
				Header: http.Header{"Cookie": []string{"init=1"}},
			},
		},
	}
	CopyRedirectHeaders(dst1, src)
	if got := dst1.Header.Get("Cookie"); got != "init=1; s1=1" {
		t.Fatalf("hop1 Cookie = %q, want %q", got, "init=1; s1=1")
	}

	// Hop 2: /step2 302s to /final with Set-Cookie s2=2. dst1 is the emitted
	// previous request — its accumulated Cookie must carry over.
	dst2 := &http.Request{
		URL:    mustParseURL(t, "https://a.example.com/final"),
		Header: http.Header{"Cookie": []string{"init=1"}},
		Response: &http.Response{
			Header:  http.Header{"Set-Cookie": []string{"s2=2; Path=/"}},
			Request: dst1,
		},
	}
	CopyRedirectHeaders(dst2, src)
	if got := dst2.Header.Get("Cookie"); got != "init=1; s1=1; s2=2" {
		t.Fatalf("hop2 Cookie = %q, want %q", got, "init=1; s1=1; s2=2")
	}
}

// TestCopyRedirectHeaders_MergeEdgeCases locks the no-op boundaries: without a
// response, or without a previous request (no setter origin to validate), the
// merge must not touch credentials.
func TestCopyRedirectHeaders_MergeEdgeCases(t *testing.T) {
	t.Run("nil response keeps header", func(t *testing.T) {
		dst := &http.Request{
			URL:    mustParseURL(t, "https://a.example.com/dl"),
			Header: http.Header{"Cookie": []string{"init=1"}},
		}
		src := &http.Request{
			URL:    mustParseURL(t, "https://a.example.com/dl"),
			Header: http.Header{"Cookie": []string{"init=1"}},
		}
		CopyRedirectHeaders(dst, src)
		if got := dst.Header.Get("Cookie"); got != "init=1" {
			t.Fatalf("Cookie = %q, want %q", got, "init=1")
		}
	})

	t.Run("nil previous request skips Set-Cookie", func(t *testing.T) {
		dst := &http.Request{
			URL:    mustParseURL(t, "https://a.example.com/dl"),
			Header: http.Header{"Cookie": []string{"init=1"}},
			Response: &http.Response{
				Header: http.Header{"Set-Cookie": []string{"x=1; Path=/"}},
			},
		}
		src := &http.Request{
			URL:    mustParseURL(t, "https://a.example.com/dl"),
			Header: http.Header{},
		}
		CopyRedirectHeaders(dst, src)
		if got := dst.Header.Get("Cookie"); got != "init=1" {
			t.Fatalf("Cookie = %q, want %q (Set-Cookie must be skipped)", got, "init=1")
		}
	})
}

func TestCopyRedirectHeaders_NilRequests(t *testing.T) {
	// Should not panic
	CopyRedirectHeaders(nil, nil)

	req := &http.Request{URL: mustParseURL(t, "https://example.com")}
	CopyRedirectHeaders(req, nil)
	CopyRedirectHeaders(nil, req)

	reqNoURL := &http.Request{}
	CopyRedirectHeaders(req, reqNoURL)
	CopyRedirectHeaders(reqNoURL, req)
}

// TestCopyRedirectHeaders_CookieBounceEndToEnd exercises the full client
// redirect path against a cookie-bounce endpoint: 302 Location:self plus a
// Set-Cookie that must be replayed on the next hop before the server lets the
// request through.
func TestCopyRedirectHeaders_CookieBounceEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var hopCookies []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hopCookies = append(hopCookies, r.Header.Get("Cookie"))
		mu.Unlock()
		if _, err := r.Cookie("bounce"); err != nil {
			w.Header().Set("Set-Cookie", "bounce=1; Path=/")
			w.Header().Set("Location", r.URL.String())
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if len(via) > 0 {
				CopyRedirectHeaders(req, via[0])
			}
			return nil
		},
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/dl", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "ext=1") // browser extension injects a flat cookie

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("cookie-bounce redirect failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hopCookies) != 2 {
		t.Fatalf("server saw %d requests, want exactly 2 (bounce must clear on hop 2)", len(hopCookies))
	}
	if hopCookies[0] != "ext=1" {
		t.Fatalf("hop1 Cookie = %q, want %q", hopCookies[0], "ext=1")
	}
	if hopCookies[1] != "ext=1; bounce=1" {
		t.Fatalf("hop2 Cookie = %q, want %q", hopCookies[1], "ext=1; bounce=1")
	}
}

// TestCopyRedirectHeaders_RedirectCapLocksUpstream ensures a server that loops
// forever without clearing the bounce still hits the redirect cap — the error
// shape the concurrent fuse sees upstream.
func TestCopyRedirectHeaders_RedirectCapLocksUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", r.URL.String())
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if len(via) > 0 {
				CopyRedirectHeaders(req, via[0])
			}
			return nil
		},
	}

	resp, err := client.Get(server.URL + "/loop")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected redirect cap error for an endless 302 loop")
	}
	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Fatalf("error = %v, want *url.Error wrapping redirect failure", err)
	}
}

func mustParseURL(t *testing.T, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("failed to parse url %q: %v", s, err)
	}
	return u
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
