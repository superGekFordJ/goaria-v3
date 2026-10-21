// Package hls implements a zero-dependency RFC 8216 subset parser, variant
// selection gates, a normalized manifest fingerprint, cross-site credential
// stripping, the enqueue-time manifest preflight, and the smart HLS output
// filename helper.
package hls

import (
	"net/http"
	"net/url"
	"strings"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// IsSensitiveCredentialHeader reports whether key is an authentication or
// session credential header that must be gated by same-site + same-scheme
// checks before it may leave the manifest origin.
func IsSensitiveCredentialHeader(key string) bool {
	return strings.EqualFold(key, "Cookie") ||
		strings.EqualFold(key, "Cookie2") ||
		strings.EqualFold(key, "Authorization") ||
		strings.EqualFold(key, "Proxy-Authorization")
}

// CanSendHLSCredentials reports whether credentials bound to credentialBaseURL
// may be sent to targetURL. It requires an identical scheme (blocking any
// HTTPS -> HTTP downgrade) and the same registrable eTLD+1 site.
func CanSendHLSCredentials(targetURL, credentialBaseURL *url.URL) bool {
	if targetURL == nil || credentialBaseURL == nil {
		return false
	}
	if targetURL.Scheme == "" || credentialBaseURL.Scheme == "" ||
		targetURL.Host == "" || credentialBaseURL.Host == "" {
		return false
	}
	return strings.EqualFold(targetURL.Scheme, credentialBaseURL.Scheme) &&
		utils.SameSite(targetURL.Host, credentialBaseURL.Host)
}

// ApplyHLSRequestHeaders copies taskHeaders onto req. Range is always dropped
// (HLS sub-requests must not inherit a byte-range probe), and
// Cookie/Cookie2/Authorization/Proxy-Authorization are only attached when the
// request stays on the same scheme and site as credentialBaseURL. A default
// User-Agent is injected when none survives the copy.
//
// FORK-PATCH: the credential gate is anchored to the caller-provided
// credentialBaseURL (the original manifest URL), never the post-redirect URL,
// so a cross-site 302 cannot launder the origin's credentials onto a CDN.
func ApplyHLSRequestHeaders(
	req *http.Request,
	taskHeaders map[string]string,
	credentialBaseURL *url.URL,
	runCfg *types.RuntimeConfig,
) {
	if req == nil {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	allowCreds := CanSendHLSCredentials(req.URL, credentialBaseURL)
	for k, v := range taskHeaders {
		key := strings.TrimSpace(k)
		if key == "" || strings.EqualFold(key, "Range") {
			continue
		}
		if IsSensitiveCredentialHeader(key) && !allowCreds {
			continue
		}
		req.Header.Set(key, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", runCfg.GetUserAgent())
	}
}
