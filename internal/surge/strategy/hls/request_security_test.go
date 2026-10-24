package hls

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
)

func mustParseURLT(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		t.Fatalf("url.Parse(%q) = %v", raw, err)
	}
	return u
}

// fakeDomainClient dials fake hostnames through a real httptest server so
// same-site / cross-site credential behavior can be exercised without any
// public DNS. routes maps a fake hostname to the real host:port address.
func fakeDomainClient(t *testing.T, routes map[string]string, tlsSkipVerify bool) *http.Client {
	t.Helper()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err == nil {
				if real, ok := routes[host]; ok {
					addr = real
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	if tlsSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

func hostPort(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return mustParseURLT(t, srv.URL).Host
}

// headerRecorder captures the sensitive headers each request carried.
type headerRecorder struct {
	mu       sync.Mutex
	requests []http.Header
}

func (r *headerRecorder) last(t *testing.T) http.Header {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("no request reached the server")
	}
	return r.requests[len(r.requests)-1]
}

func (r *headerRecorder) at(t *testing.T, i int) http.Header {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.requests) {
		t.Fatalf("request %d missing (have %d)", i, len(r.requests))
	}
	return r.requests[i]
}

var sensitiveTaskHeaders = map[string]string{
	"Cookie":              "session=abc",
	"Cookie2":             "legacy=1",
	"Authorization":       "Bearer tok",
	"Proxy-Authorization": "Basic px",
	"X-Api-Key":           "apikey1",
	"X-Auth-Token":        "authtok",
	"Api-Key":             "apikey2",
	"X-Tok":               "passthrough",
	"Range":               "bytes=0-999",
}

func assertHeadersStripped(t *testing.T, h http.Header, msg string) {
	t.Helper()
	for _, k := range []string{"Cookie", "Cookie2", "Authorization", "Proxy-Authorization", "X-Api-Key", "X-Auth-Token", "Api-Key"} {
		if v := h.Get(k); v != "" {
			t.Fatalf("%s: header %s leaked as %q", msg, k, v)
		}
	}
}

func assertHeadersPresent(t *testing.T, h http.Header, msg string) {
	t.Helper()
	for _, k := range []string{"Cookie", "Authorization", "X-Api-Key", "X-Auth-Token", "Api-Key"} {
		if v := h.Get(k); v == "" {
			t.Fatalf("%s: header %s missing", msg, k)
		}
	}
	if got := h.Get("X-Tok"); got != "passthrough" {
		t.Fatalf("%s: X-Tok = %q, want passthrough", msg, got)
	}
}

func TestHLSRequestSensitiveSetExpanded(t *testing.T) {
	for _, k := range []string{
		"Cookie", "cookie2", "AUTHORIZATION", "proxy-authorization",
		"X-Api-Key", "x-api-key", "X-Auth-Token", "x-auth-token", "Api-Key", "api-key",
	} {
		if !IsSensitiveCredentialHeader(k) {
			t.Fatalf("IsSensitiveCredentialHeader(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"X-Tok", "X-Custom", "Referer", "User-Agent", "Accept", ""} {
		if IsSensitiveCredentialHeader(k) {
			t.Fatalf("IsSensitiveCredentialHeader(%q) = true, want false", k)
		}
	}
}

func TestHLSRequestApplyGatesExpandedSet(t *testing.T) {
	base := mustParseURLT(t, "https://api.a.example.com/media.m3u8")
	newReq := func(t *testing.T, raw string) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("NewRequest = %v", err)
		}
		return req
	}

	t.Run("SameSiteKeepsAll", func(t *testing.T) {
		req := newReq(t, "https://cdn.a.example.com/seg0.ts")
		ApplyHLSRequestHeaders(req, sensitiveTaskHeaders, base, nil)
		assertHeadersPresent(t, req.Header, "same-site")
		if got := req.Header.Get("Range"); got != "" {
			t.Fatalf("task Range leaked as %q", got)
		}
	})
	t.Run("CrossSiteStripsAll", func(t *testing.T) {
		req := newReq(t, "https://edge.foreign-cdn.org/seg0.ts")
		ApplyHLSRequestHeaders(req, sensitiveTaskHeaders, base, nil)
		assertHeadersStripped(t, req.Header, "cross-site")
		if got := req.Header.Get("X-Tok"); got != "passthrough" {
			t.Fatalf("X-Tok = %q, want passthrough", got)
		}
	})
	t.Run("DowngradeStripsAll", func(t *testing.T) {
		req := newReq(t, "http://api.a.example.com/seg0.ts")
		ApplyHLSRequestHeaders(req, sensitiveTaskHeaders, base, nil)
		assertHeadersStripped(t, req.Header, "downgrade")
	})
	t.Run("CaseVariantStripped", func(t *testing.T) {
		req := newReq(t, "https://edge.foreign-cdn.org/seg0.ts")
		ApplyHLSRequestHeaders(req,
			map[string]string{"x-api-key": "v", "API-KEY": "v2", "x-auth-token": "v3"}, base, nil)
		assertHeadersStripped(t, req.Header, "case-variant")
	})
}

func TestHLSRequestSubRequestBuild(t *testing.T) {
	base := mustParseURLT(t, "https://api.a.example.com/media.m3u8")
	req, err := newHLSSubRequest(context.Background(), "https://cdn.a.example.com/s.ts",
		sensitiveTaskHeaders, base, nil, &hlsByteRange{offset: 64, length: 100})
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	if got := req.Header.Get("Range"); got != "bytes=64-163" {
		t.Fatalf("Range = %q", got)
	}
	if got := req.Header.Get("Accept-Encoding"); got != "identity" {
		t.Fatalf("Accept-Encoding = %q", got)
	}
	assertHeadersPresent(t, req.Header, "subrequest")

	plain, err := newHLSSubRequest(context.Background(), "https://cdn.a.example.com/s.ts",
		nil, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest nil range = %v", err)
	}
	if got := plain.Header.Get("Range"); got != "" {
		t.Fatalf("plain Range = %q, want empty", got)
	}

	for _, bad := range []string{
		"http://user:pass@cdn.a.example.com/s.ts",
		"ftp://cdn.a.example.com/s.ts",
		"/relative/seg.ts",
		"seg.ts",
		"https://",
		"",
	} {
		if _, err := newHLSSubRequest(context.Background(), bad, nil, base, nil, nil); err == nil {
			t.Fatalf("newHLSSubRequest(%q) = nil error, want contract failure", bad)
		} else {
			if _, ok := errors.AsType[*hlsContractError](err); !ok {
				t.Fatalf("newHLSSubRequest(%q) err type %T, want *hlsContractError", bad, err)
			}
		}
	}
}

func TestHLSRequestRedirectSameSiteKeepsCredentials(t *testing.T) {
	// Both fake hosts map to one recorder server.
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://cdn.a.example.com/final", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer srv.Close()

	routes := map[string]string{
		"api.a.example.com": hostPort(t, srv),
		"cdn.a.example.com": hostPort(t, srv),
	}
	client := fakeDomainClient(t, routes, false)
	base := mustParseURLT(t, "http://api.a.example.com/media.m3u8")
	hlsClient, release := newHLSClient(client, nil, base)
	if release != nil {
		defer release()
	}
	req, err := newHLSSubRequest(context.Background(), "http://api.a.example.com/start",
		sensitiveTaskHeaders, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	_ = resp.Body.Close()

	last := recorder.last(t)
	assertHeadersPresent(t, last, "same-site redirect hop")
	if got := last.Get("X-Api-Key"); got != "apikey1" {
		t.Fatalf("X-Api-Key not restored on same-site hop: %q", got)
	}
}

func TestHLSRequestRedirectForeignStripsAndNoLaundering(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://edge.foreign-cdn.org/mid", http.StatusFound)
		case "/mid":
			http.Redirect(w, r, "http://sub.foreign-cdn.org/final", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer srv.Close()

	routes := map[string]string{
		"api.a.example.com":    hostPort(t, srv),
		"edge.foreign-cdn.org": hostPort(t, srv),
		"sub.foreign-cdn.org":  hostPort(t, srv),
	}
	client := fakeDomainClient(t, routes, false)
	base := mustParseURLT(t, "http://api.a.example.com/media.m3u8")
	hlsClient, _ := newHLSClient(client, nil, base)
	req, err := newHLSSubRequest(context.Background(), "http://api.a.example.com/start",
		sensitiveTaskHeaders, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	_ = resp.Body.Close()

	assertHeadersStripped(t, recorder.at(t, 1), "foreign hop 1")
	assertHeadersStripped(t, recorder.at(t, 2), "foreign same-site-subdomain hop 2")
	// The foreign subdomain is same-site with the previous hop but outside the
	// anchor — credentials must not be laundered back through the chain.
	last := recorder.last(t)
	if last.Get("X-Tok") != "passthrough" {
		t.Fatalf("X-Tok = %q, want passthrough", last.Get("X-Tok"))
	}
}

func TestHLSRequestRedirectReturnToAnchorSite(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://edge.foreign-cdn.org/mid", http.StatusFound)
		case "/mid":
			http.Redirect(w, r, "http://back.a.example.com/final", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer srv.Close()

	routes := map[string]string{
		"api.a.example.com":    hostPort(t, srv),
		"edge.foreign-cdn.org": hostPort(t, srv),
		"back.a.example.com":   hostPort(t, srv),
	}
	client := fakeDomainClient(t, routes, false)
	base := mustParseURLT(t, "http://api.a.example.com/media.m3u8")
	hlsClient, _ := newHLSClient(client, nil, base)
	req, err := newHLSSubRequest(context.Background(), "http://api.a.example.com/start",
		sensitiveTaskHeaders, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	_ = resp.Body.Close()

	assertHeadersStripped(t, recorder.at(t, 1), "foreign hop")
	assertHeadersPresent(t, recorder.at(t, 2), "anchor-site return hop")
}

func TestHLSRequestRedirectSchemeDowngradeStrips(t *testing.T) {
	recorder := &headerRecorder{}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		fmt.Fprint(w, "ok")
	}))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		http.Redirect(w, r, "http://a.example.com/plain", http.StatusFound)
	}))
	defer tlsSrv.Close()

	// One transport routes both schemes: https to the TLS server, http to the
	// plain server, both under the same fake hostname so the downgrade is a
	// pure scheme change.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, _ := net.SplitHostPort(addr)
			if host == "a.example.com" {
				if port == "443" {
					addr = hostPort(t, tlsSrv)
				} else {
					addr = hostPort(t, plain)
				}
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	defer tr.CloseIdleConnections()

	base := mustParseURLT(t, "https://a.example.com/media.m3u8")
	hlsClient, _ := newHLSClient(&http.Client{Transport: tr}, nil, base)
	req, err := newHLSSubRequest(context.Background(), "https://a.example.com/start",
		sensitiveTaskHeaders, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	_ = resp.Body.Close()
	assertHeadersStripped(t, recorder.last(t), "https->http downgrade hop")
}

func TestHLSRequestRedirectCallbackCannotReinsertSecrets(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "http://edge.foreign-cdn.org/final", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer srv.Close()

	routes := map[string]string{
		"api.a.example.com":    hostPort(t, srv),
		"edge.foreign-cdn.org": hostPort(t, srv),
	}
	client := fakeDomainClient(t, routes, false)
	// A hostile/naive caller callback tries to smuggle credentials back on
	// every hop — the final sanitizer must still strip them.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		req.Header.Set("Cookie", "session=abc")
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("X-Api-Key", "apikey1")
		return nil
	}
	base := mustParseURLT(t, "http://api.a.example.com/media.m3u8")
	hlsClient, _ := newHLSClient(client, nil, base)
	req, err := newHLSSubRequest(context.Background(), "http://api.a.example.com/start",
		sensitiveTaskHeaders, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	_ = resp.Body.Close()
	assertHeadersStripped(t, recorder.last(t), "callback reinsertion")
}

func TestHLSRequestRedirectCallbackSemanticsHonored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer srv.Close()
	base := mustParseURLT(t, srv.URL+"/media.m3u8")

	t.Run("RefusalPropagates", func(t *testing.T) {
		myErr := errors.New("callback refusal")
		client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return myErr
		}}
		hlsClient, _ := newHLSClient(client, nil, base)
		req, err := newHLSSubRequest(context.Background(), srv.URL+"/start", nil, base, nil, nil)
		if err != nil {
			t.Fatalf("newHLSSubRequest = %v", err)
		}
		resp, err := hlsClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err == nil || !strings.Contains(err.Error(), myErr.Error()) {
			t.Fatalf("Do err = %v, want callback refusal", err)
		}
	})
	t.Run("UseLastResponse", func(t *testing.T) {
		client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		hlsClient, _ := newHLSClient(client, nil, base)
		req, err := newHLSSubRequest(context.Background(), srv.URL+"/start", nil, base, nil, nil)
		if err != nil {
			t.Fatalf("newHLSSubRequest = %v", err)
		}
		resp, err := hlsClient.Do(req)
		if err != nil {
			t.Fatalf("Do = %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("status = %d, want unfollowed 302", resp.StatusCode)
		}
	})
}

func TestHLSRequestRedirectJarBypassPrevented(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	routes := map[string]string{"edge.foreign-cdn.org": hostPort(t, srv)}
	client := fakeDomainClient(t, routes, false)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New = %v", err)
	}
	foreign := mustParseURLT(t, "http://edge.foreign-cdn.org/")
	cookie := &http.Cookie{Name: "jar-secret", Value: "1"}
	jar.SetCookies(foreign, []*http.Cookie{cookie})
	client.Jar = jar

	base := mustParseURLT(t, "http://api.a.example.com/media.m3u8")
	hlsClient, _ := newHLSClient(client, nil, base)
	req, err := newHLSSubRequest(context.Background(), "http://edge.foreign-cdn.org/final",
		sensitiveTaskHeaders, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	_ = resp.Body.Close()

	last := recorder.last(t)
	assertHeadersStripped(t, last, "jar bypass")
}

func TestHLSRequestUserinfoRejected(t *testing.T) {
	base := mustParseURLT(t, "http://api.a.example.com/media.m3u8")
	if _, err := newHLSSubRequest(context.Background(), "http://u:p@api.a.example.com/s.ts",
		nil, base, nil, nil); err == nil {
		t.Fatal("userinfo URL accepted, want contract error")
	}

	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		http.Redirect(w, r, "http://user:pass@edge.foreign-cdn.org/steal", http.StatusFound)
	}))
	defer srv.Close()
	routes := map[string]string{
		"api.a.example.com":    hostPort(t, srv),
		"edge.foreign-cdn.org": hostPort(t, srv),
	}
	client := fakeDomainClient(t, routes, false)
	hlsClient, _ := newHLSClient(client, nil, base)
	req, err := newHLSSubRequest(context.Background(), "http://api.a.example.com/start",
		sensitiveTaskHeaders, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("redirect to userinfo target succeeded, want rejection")
	}
	if ue, ok := errors.AsType[*url.Error](err); !ok || !errors.Is(ue.Err, errRedirectUserinfo) {
		t.Fatalf("Do err = %v, want wrapped errRedirectUserinfo", err)
	}
}

func TestHLSRequestRedirectMaxHops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var idx int
		if _, err := fmt.Sscanf(r.URL.Path, "/h%d", &idx); err == nil {
			http.Redirect(w, r, fmt.Sprintf("/h%d", idx+1), http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	base := mustParseURLT(t, srv.URL+"/media.m3u8")
	hlsClient, _ := newHLSClient(srv.Client(), nil, base)
	req, err := newHLSSubRequest(context.Background(), srv.URL+"/h0", nil, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !errors.Is(err, types.ErrMaxRedirects) {
		t.Fatalf("Do err = %v, want ErrMaxRedirects", err)
	}
}

func TestHLSClientDoesNotMutateInjected(t *testing.T) {
	called := false
	jar, _ := cookiejar.New(nil)
	orig := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			called = true
			return errors.New("stop")
		},
	}
	base := mustParseURLT(t, "http://api.a.example.com/media.m3u8")
	clone, release := newHLSClient(orig, nil, base)
	if release != nil {
		defer release()
	}
	if clone.Jar != nil {
		t.Fatal("clone kept the injected Jar")
	}
	if orig.Jar != jar {
		t.Fatal("original client Jar mutated")
	}
	if clone.CheckRedirect == nil {
		t.Fatal("clone lost redirect guard")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer srv.Close()
	req, _ := newHLSSubRequest(context.Background(), srv.URL+"/start", nil,
		mustParseURLT(t, srv.URL+"/m.m3u8"), nil, nil)
	resp, _ := clone.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !called {
		t.Fatal("caller CheckRedirect was not invoked")
	}
	if orig.CheckRedirect == nil {
		t.Fatal("original client CheckRedirect mutated")
	}
}

func TestHLSRequestRedirectSetCookieMergePreserved(t *testing.T) {
	recorder := &headerRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, r.Header.Clone())
		recorder.mu.Unlock()
		switch r.URL.Path {
		case "/start":
			w.Header().Set("Set-Cookie", "bounce=1; Path=/; Domain=a.example.com")
			http.Redirect(w, r, "http://cdn.a.example.com/final", http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer srv.Close()
	routes := map[string]string{
		"api.a.example.com": hostPort(t, srv),
		"cdn.a.example.com": hostPort(t, srv),
	}
	client := fakeDomainClient(t, routes, false)
	base := mustParseURLT(t, "http://api.a.example.com/media.m3u8")
	hlsClient, _ := newHLSClient(client, nil, base)
	req, err := newHLSSubRequest(context.Background(), "http://api.a.example.com/start",
		sensitiveTaskHeaders, base, nil, nil)
	if err != nil {
		t.Fatalf("newHLSSubRequest = %v", err)
	}
	resp, err := hlsClient.Do(req)
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	_ = resp.Body.Close()
	last := recorder.last(t)
	if got := last.Get("Cookie"); !strings.Contains(got, "bounce=1") || !strings.Contains(got, "session=abc") {
		t.Fatalf("merged Cookie = %q, want bounce + session", got)
	}
}
