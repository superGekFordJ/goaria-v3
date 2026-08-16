package rpc

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"goaria-v3/internal/config"
)

// HeadProbeResult contains the result of a HEAD probe request.
type HeadProbeResult struct {
	ContentLength int64
	TTFBMs        int64
	// RemoteIP is the physical TCP peer of the last persistConn (direct origin; includes reused conns).
	// On HTTPS this is post-TLS; handshake failure leaves it empty (or the previous hop on redirect).
	RemoteIP string
}

var probeSessionCache = tls.NewLRUClientSessionCache(128)

// Dedicated HTTP/1.1 transport for HeadProbe. Isolated from Aria2 httpClient
// and Surge DefaultNetworkPool (including its TLS session cache of 256).
// ForceAttemptHTTP2 + empty TLSNextProto lock HTTP/1.1 once DialContext is gone.
var probeTransport = &http.Transport{
	ForceAttemptHTTP2: false,
	TLSNextProto:      make(map[string]func(string, *tls.Conn) http.RoundTripper),
	TLSClientConfig: &tls.Config{
		ClientSessionCache: probeSessionCache,
	},
	MaxIdleConns:        64,
	MaxIdleConnsPerHost: 8,
	IdleConnTimeout:     30 * time.Second,
}

// HeadProbe sends a HEAD request and returns Content-Length, TTFB, and remote IP.
func HeadProbe(rawURL string, timeout time.Duration) HeadProbeResult {
	return headProbe(rawURL, timeout, nil)
}

// HeadProbeWithHeaders sends a HEAD request with extra headers (e.g. Cookie/Referer
// from the browser extension) and returns the same metrics as HeadProbe.
// Headers are validated via ValidateAddURIHeaders before use to prevent CRLF injection.
func HeadProbeWithHeaders(rawURL string, timeout time.Duration, headers []string) HeadProbeResult {
	if err := ValidateAddURIHeaders(headers); err != nil {
		return HeadProbeResult{}
	}
	return headProbe(rawURL, timeout, headers)
}

func headProbe(rawURL string, timeout time.Duration, headers []string) HeadProbeResult {
	var peerIP string

	client := &http.Client{
		Timeout:   timeout,
		Transport: probeTransport,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequest(http.MethodHead, rawURL, nil)
	if err != nil {
		return HeadProbeResult{}
	}

	ua := config.Get().UserAgent
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}

	for _, line := range headers {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}

	// GotConn reports the persistConn peer even on keep-alive reuse; last hop wins on redirects.
	// HTTPS fires after TLS, so handshake failure does not set peerIP.
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Conn == nil {
				return
			}
			tcpAddr, ok := info.Conn.RemoteAddr().(*net.TCPAddr)
			if !ok {
				return
			}
			peerIP = tcpAddr.IP.String()
		},
	}))

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		// Last GotConn peer is kept; dial refused / TLS handshake / validation leave it empty.
		return HeadProbeResult{RemoteIP: peerIP}
	}
	defer resp.Body.Close()

	result := HeadProbeResult{
		TTFBMs:   time.Since(start).Milliseconds(),
		RemoteIP: peerIP,
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		if resp.ContentLength > 0 {
			result.ContentLength = resp.ContentLength
		}
	}

	return result
}

// HeadContentLength 发送 HEAD 请求获取文件大小
// 返回 Content-Length (bytes)，失败返回 0
// timeout: 超时时间
func HeadContentLength(url string, timeout time.Duration) int64 {
	return HeadProbe(url, timeout).ContentLength
}
