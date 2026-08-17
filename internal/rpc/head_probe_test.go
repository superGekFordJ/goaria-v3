package rpc

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func startHeadProbeServer(t *testing.T, handler http.Handler) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var stateNew atomic.Int32
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			stateNew.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(func() {
		srv.Close()
		probeTransport.CloseIdleConnections()
	})
	return srv, &stateNew
}

func requireLoopbackIP(t *testing.T, remote string) {
	t.Helper()
	if strings.ContainsAny(remote, "[]") {
		t.Fatalf("RemoteIP must not contain brackets, got %q", remote)
	}
	addr, err := netip.ParseAddr(remote)
	if err != nil || !addr.IsLoopback() {
		t.Fatalf("expected loopback RemoteIP, got %q", remote)
	}
}

func TestHeadProbe_ReusesHTTP1ConnectionAndCapturesPeerIP(t *testing.T) {
	const wantLen int64 = 12345
	srv, stateNew := startHeadProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("Content-Length", "12345")
		w.WriteHeader(http.StatusOK)
	}))

	first := HeadProbe(srv.URL, 2*time.Second)
	requireLoopbackIP(t, first.RemoteIP)
	if first.ContentLength != wantLen {
		t.Fatalf("ContentLength = %d, want %d", first.ContentLength, wantLen)
	}
	if got := stateNew.Load(); got != 1 {
		t.Fatalf("StateNew after first HEAD = %d, want 1", got)
	}

	second := HeadProbe(srv.URL, 2*time.Second)
	requireLoopbackIP(t, second.RemoteIP)
	if second.ContentLength != wantLen {
		t.Fatalf("second ContentLength = %d, want %d", second.ContentLength, wantLen)
	}
	if got := stateNew.Load(); got != 1 {
		t.Fatalf("StateNew after second HEAD = %d, want 1 (reuse)", got)
	}
}

func TestHeadProbe_TCP6LoopbackRemoteIPParses(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("tcp6 [::1]:0 bind failed: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(handler)
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(func() {
		srv.Close()
		probeTransport.CloseIdleConnections()
	})

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split tcp6 addr: %v", err)
	}
	probeURL := "http://" + net.JoinHostPort("::1", port) + "/"

	result := HeadProbe(probeURL, 2*time.Second)
	if strings.ContainsAny(result.RemoteIP, "[]") {
		t.Fatalf("RemoteIP contains brackets: %q", result.RemoteIP)
	}
	addr, err := netip.ParseAddr(result.RemoteIP)
	if err != nil {
		t.Fatalf("ParseAddr(%q): %v", result.RemoteIP, err)
	}
	if !addr.IsLoopback() || !addr.Is6() || addr.Is4In6() {
		t.Fatalf("RemoteIP = %q, want IPv6-form loopback", result.RemoteIP)
	}
	if result.ContentLength != 1 {
		t.Fatalf("ContentLength = %d, want 1", result.ContentLength)
	}
}

func TestHeadProbeWithHeaders_ReusesConnectionWithoutHeaderLeak(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv, stateNew := startHeadProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Cookie"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))

	first := HeadProbeWithHeaders(srv.URL, 2*time.Second, []string{"Cookie: a=1"})
	requireLoopbackIP(t, first.RemoteIP)
	second := HeadProbeWithHeaders(srv.URL, 2*time.Second, []string{"Cookie: b=2"})
	requireLoopbackIP(t, second.RemoteIP)

	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("handler saw %d requests, want 2: %#v", len(got), got)
	}
	if got[0] != "a=1" {
		t.Fatalf("first Cookie = %q, want a=1", got[0])
	}
	if got[1] != "b=2" {
		t.Fatalf("second Cookie = %q, want b=2 (no leak of a=1)", got[1])
	}
	if got := stateNew.Load(); got != 1 {
		t.Fatalf("StateNew = %d, want 1 (reuse)", got)
	}
}

func TestHeadProbe_RedirectCapturesFinalPeerIP(t *testing.T) {
	var destMethod string
	mux := http.NewServeMux()
	mux.HandleFunc("/dest", func(w http.ResponseWriter, r *http.Request) {
		destMethod = r.Method
		w.Header().Set("Content-Length", "42")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dest", http.StatusFound)
	})
	srv, _ := startHeadProbeServer(t, mux)

	result := HeadProbe(srv.URL, 2*time.Second)
	requireLoopbackIP(t, result.RemoteIP)
	if destMethod != http.MethodHead {
		t.Fatalf("final hop method = %q, want HEAD", destMethod)
	}
	if result.ContentLength != 42 {
		t.Fatalf("ContentLength = %d, want 42", result.ContentLength)
	}
}

func TestHeadProbe_CrossHostnameRedirectStripsSensitiveHeaders(t *testing.T) {
	var (
		mu        sync.Mutex
		gotCookie string
		gotAuth   string
		gotMethod string
		destHits  int
	)
	destHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotCookie = r.Header.Get("Cookie")
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		destHits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	destURL, destIP := startCrossOriginDest(t, destHandler)
	origin, _ := startHeadProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destURL, http.StatusFound)
	}))

	result := HeadProbeWithHeaders(origin.URL, 2*time.Second, []string{
		"Cookie: session=secret",
		"Authorization: Bearer tok",
	})
	requireLoopbackIP(t, result.RemoteIP)
	if ip := net.ParseIP(result.RemoteIP); ip == nil || !ip.Equal(destIP) {
		t.Fatalf("final RemoteIP = %q, want dest %s", result.RemoteIP, destIP)
	}

	mu.Lock()
	defer mu.Unlock()
	if destHits != 1 {
		t.Fatalf("dest hits = %d, want 1", destHits)
	}
	if gotCookie != "" {
		t.Fatalf("Cookie leaked across hostname redirect: %q", gotCookie)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization leaked across hostname redirect: %q", gotAuth)
	}
	if gotMethod != http.MethodHead {
		t.Fatalf("dest method = %q, want HEAD", gotMethod)
	}
}

func startCrossOriginDest(t *testing.T, handler http.Handler) (destURL string, destIP net.IP) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	host := "127.0.0.2"
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen dest: %v", err)
		}
		host = "other.localhost"
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("expected TCP addr, got %T", ln.Addr())
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		probeTransport.CloseIdleConnections()
	})
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split dest addr: %v", err)
	}
	return "http://" + net.JoinHostPort(host, port) + "/final", tcpAddr.IP
}

func TestHeadProbe_SameHostnameDifferentPortKeepsCookie(t *testing.T) {
	var mu sync.Mutex
	var gotCookie string
	dest, _ := startHeadProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotCookie = r.Header.Get("Cookie")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	origin, _ := startHeadProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL, http.StatusFound)
	}))

	result := HeadProbeWithHeaders(origin.URL, 2*time.Second, []string{"Cookie: keep=1"})
	requireLoopbackIP(t, result.RemoteIP)

	mu.Lock()
	defer mu.Unlock()
	if gotCookie != "keep=1" {
		t.Fatalf("Cookie = %q, want keep=1 (same hostname, different port)", gotCookie)
	}
}

func TestProbeTransport_HTTP2Disabled(t *testing.T) {
	if probeTransport.ForceAttemptHTTP2 {
		t.Error("expected ForceAttemptHTTP2 == false")
	}
	if probeTransport.TLSNextProto == nil {
		t.Error("expected non-nil TLSNextProto map")
	} else if len(probeTransport.TLSNextProto) != 0 {
		t.Errorf("expected empty TLSNextProto, got len=%d", len(probeTransport.TLSNextProto))
	}
	if probeTransport.Proxy != nil {
		t.Error("expected Proxy == nil")
	}
	if probeTransport.MaxConnsPerHost != 0 {
		t.Errorf("MaxConnsPerHost = %d, want 0", probeTransport.MaxConnsPerHost)
	}
	if probeTransport.DisableKeepAlives {
		t.Error("expected keep-alives enabled")
	}
	if probeTransport.MaxIdleConns != probeMaxIdleConns {
		t.Errorf("MaxIdleConns = %d, want %d", probeTransport.MaxIdleConns, probeMaxIdleConns)
	}
	if probeTransport.MaxIdleConnsPerHost != probeMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want %d", probeTransport.MaxIdleConnsPerHost, probeMaxIdleConnsPerHost)
	}
	if probeTransport.IdleConnTimeout != probeIdleConnTimeout {
		t.Errorf("IdleConnTimeout = %s, want %s", probeTransport.IdleConnTimeout, probeIdleConnTimeout)
	}
	if probeTransport.TLSHandshakeTimeout != probeTLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %s, want %s", probeTransport.TLSHandshakeTimeout, probeTLSHandshakeTimeout)
	}
	if probeTransport.Dial != nil { //nolint:staticcheck // deprecated field; must stay unset
		t.Error("expected Dial == nil")
	}
	if probeTransport.DialContext != nil {
		t.Error("expected DialContext == nil")
	}
	if probeTransport.TLSClientConfig == nil || probeTransport.TLSClientConfig.ClientSessionCache == nil {
		t.Fatal("expected isolated ClientSessionCache")
	}
	if probeTransport.TLSClientConfig.ClientSessionCache != probeSessionCache {
		t.Error("expected ClientSessionCache == probeSessionCache")
	}
	if probeSessionCacheSize != 128 {
		t.Errorf("probeSessionCacheSize = %d, want 128", probeSessionCacheSize)
	}
	if probeTransport == httpClient.Transport {
		t.Fatal("probeTransport must not be Aria2 httpClient.Transport")
	}

	var sawProto string
	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawProto = r.Proto
		w.WriteHeader(http.StatusOK)
	}))
	tlsSrv.EnableHTTP2 = true
	tlsSrv.StartTLS()
	t.Cleanup(tlsSrv.Close)

	clone := probeTransport.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(tlsSrv.Certificate())
	clone.TLSClientConfig = &tls.Config{RootCAs: roots}

	client := &http.Client{Transport: clone, Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodHead, tlsSrv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("clone HEAD: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if sawProto != "HTTP/1.1" {
		t.Fatalf("handler proto = %q, want HTTP/1.1", sawProto)
	}
}

func TestHeadProbe_TimeoutOrDialFailureRemoteIPContract(t *testing.T) {
	t.Run("timeoutAfterConnectKeepsPeerIP", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()

		hold := make(chan struct{})
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			<-hold
		}()
		t.Cleanup(func() { close(hold) })

		result := HeadProbe("http://"+ln.Addr().String()+"/", 200*time.Millisecond)
		requireLoopbackIP(t, result.RemoteIP)
		if result.TTFBMs != 0 || result.ContentLength != 0 {
			t.Fatalf("timeout result = %+v, want TTFB/length 0", result)
		}
	})

	t.Run("dialRefusedEmptyRemoteIP", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		result := HeadProbe("http://"+addr+"/", 500*time.Millisecond)
		if result.RemoteIP != "" || result.TTFBMs != 0 || result.ContentLength != 0 {
			t.Fatalf("dial-refused result = %+v, want empty", result)
		}
	})

	t.Run("invalidURLAndBadHeadersNoHits", func(t *testing.T) {
		var hits atomic.Int32
		srv, _ := startHeadProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

		invalid := HeadProbe(":", 500*time.Millisecond)
		if invalid != (HeadProbeResult{}) {
			t.Fatalf("invalid URL result = %+v, want empty", invalid)
		}

		badHeaders := HeadProbeWithHeaders(srv.URL, 500*time.Millisecond, []string{"Cookie: a=1\r\nX: y"})
		if badHeaders != (HeadProbeResult{}) {
			t.Fatalf("CRLF headers result = %+v, want empty", badHeaders)
		}
		if got := hits.Load(); got != 0 {
			t.Fatalf("server hits = %d, want 0", got)
		}
	})
}

func TestHeadProbe_ConcurrentSameHostDoesNotQueueOnIdleCap(t *testing.T) {
	const n = 12
	started := make(chan struct{}, n)
	release := make(chan struct{})
	srv, _ := startHeadProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	results := make([]HeadProbeResult, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = HeadProbe(srv.URL, 8*time.Second)
		}(i)
	}

	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for in-flight HEAD %d/%d; MaxConnsPerHost may be set", i, n)
		}
	}
	close(release)
	wg.Wait()

	for i, result := range results {
		requireLoopbackIP(t, result.RemoteIP)
		if result.ContentLength < 0 {
			t.Fatalf("result[%d] = %+v", i, result)
		}
	}
}

func TestHeadProbe_TooManyRedirectsPreservesLastPeerIP(t *testing.T) {
	srv, _ := startHeadProbeServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	}))

	result := HeadProbe(srv.URL, 2*time.Second)
	requireLoopbackIP(t, result.RemoteIP)
	if result.TTFBMs != 0 || result.ContentLength != 0 {
		t.Fatalf("too-many-redirects result = %+v, want TTFB/length 0", result)
	}
}
