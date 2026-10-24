package hls

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// hlsMaxRedirects bounds the redirect chain on every HLS sub-request.
const hlsMaxRedirects = 10

// hlsContractError marks a local construction/contract violation — bad
// sub-resource URLs, impossible descriptors, or internal states that can
// never be fixed by re-fetching. It is a terminal engine fault, never a
// network retry and never a segment data error.
type hlsContractError struct {
	reason string
}

func (e *hlsContractError) Error() string { return "hls contract violation: " + e.reason }

func newHLSContractError(format string, args ...any) *hlsContractError {
	return &hlsContractError{reason: fmt.Sprintf(format, args...)}
}

// hlsStatusError carries a non-2xx response on a gated sub-request to the
// retry state machine: resource kind, owning segment index, status and the
// raw Retry-After header — never the URL, credentials or response body. The
// classifier alone decides whether the status retries, penalizes the host or
// terminates as permanent.
type hlsStatusError struct {
	resource   string
	index      int
	status     int
	retryAfter string
	// host is transport.MirrorHost of the URI that produced the status —
	// key and segment URIs may live on different hosts, and every
	// penalty/success record must land on the resource's own host.
	host string
}

func (e *hlsStatusError) Error() string {
	return fmt.Sprintf("hls %s (segment %d): http status %d", e.resource, e.index, e.status)
}

// errRedirectUserinfo rejects a redirect hop whose target carries URL.User:
// net/http would silently mint an Authorization: Basic header for it,
// bypassing the credential gate entirely.
var errRedirectUserinfo = errors.New("hls: redirect target must not carry userinfo")

// hlsRedirectRefusalError marks a caller CheckRedirect refusal so the retry
// state machine treats it as a local policy terminal — not a transient
// transport failure — while the wrapped cause keeps errors.Is identity.
type hlsRedirectRefusalError struct{ err error }

func (e *hlsRedirectRefusalError) Error() string {
	return "hls: redirect refused: " + e.err.Error()
}
func (e *hlsRedirectRefusalError) Unwrap() error { return e.err }

// hlsTransportError wraps a client.Do failure — usually *url.Error, whose
// message embeds the raw request URL (signed query, userinfo, and all).
// Error() reports only the inner cause so logs and error text never leak
// credentials; Unwrap keeps errors.As/Is transparent for the classifier
// (net.Error, ctx identity, redirect sentinels all still resolve).
type hlsTransportError struct{ err error }

func (e *hlsTransportError) Error() string {
	cause := e.err
	if ue, ok := errors.AsType[*url.Error](cause); ok && ue.Err != nil {
		cause = ue.Err
	}
	return "hls: request failed: " + cause.Error()
}
func (e *hlsTransportError) Unwrap() error { return e.err }

// newHLSClient returns the shared HLS HTTP client for manifest, key, init and
// media-segment sub-requests. A nil client borrows the shared network pool
// (released by the returned func once all workers exited); a non-nil client
// is shallow-copied — the caller's value is never mutated — preserving the
// injected Transport/Timeout while the Jar is dropped so cookies can only
// arrive through the gated task headers and the controlled redirect merge.
// Every client gets the anchored redirect guard; a caller-supplied
// CheckRedirect is honored first (including http.ErrUseLastResponse) but can
// never re-attach credentials past the final sanitizer.
func newHLSClient(client *http.Client, runCfg *types.RuntimeConfig, credentialBaseURL *url.URL) (*http.Client, func()) {
	if client != nil {
		c := *client
		c.Jar = nil
		c.CheckRedirect = hlsRedirectGuard(credentialBaseURL, client.CheckRedirect)
		return &c, nil
	}
	var proxyURL, customDNS string
	if runCfg != nil {
		proxyURL = runCfg.ProxyURL
		customDNS = runCfg.CustomDNS
	}
	tr := transport.DefaultNetworkPool.AcquireTransport(proxyURL, customDNS, 0)
	return &http.Client{Transport: tr, CheckRedirect: hlsRedirectGuard(credentialBaseURL, nil)}, func() {
		transport.DefaultNetworkPool.ReleaseTransport(tr)
	}
}

// hlsRedirectGuard builds the CheckRedirect for HLS sub-requests. Per hop it:
//  1. enforces the redirect cap,
//  2. honors a caller-supplied callback first (its refusal and
//     http.ErrUseLastResponse semantics are preserved),
//  3. restores same-site credentials via utils.CopyRedirectHeaders and merges
//     that hop's Set-Cookie (its internal validation is kept),
//  4. rejects a userinfo target so no Basic auth can be minted,
//  5. restores the extended sensitive set from the original request when the
//     hop still deserves credentials (the stdlib copy drops headers the
//     utils restore does not know about),
//  6. finally strips every sensitive header and any Host override whenever
//     the target leaves the credential anchor's scheme+site — the sanitizer
//     deletes headers, it does not merely skip copying, so a credential can
//     never be laundered through a foreign hop and back.
func hlsRedirectGuard(credentialBaseURL *url.URL, inner func(req *http.Request, via []*http.Request) error) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= hlsMaxRedirects {
			return types.ErrMaxRedirects
		}
		if inner != nil {
			if err := inner(req, via); err != nil {
				if errors.Is(err, http.ErrUseLastResponse) {
					return err
				}
				return &hlsRedirectRefusalError{err: err}
			}
		}
		if len(via) > 0 {
			utils.CopyRedirectHeaders(req, via[0])
			restoreHLSHeaderSet(req, via[0], credentialBaseURL)
		}
		if req.URL != nil && req.URL.User != nil {
			return errRedirectUserinfo
		}
		sanitizeHLSOutbound(req, credentialBaseURL)
		return nil
	}
}

// restoreHLSHeaderSet re-attaches the sensitive header set from the initial
// request when the redirect target stays inside the credential anchor. The
// stdlib redirect copy keeps unknown headers but drops the classic auth
// names on cross-host hops even within one site; utils.CopyRedirectHeaders
// only knows three of them. Restoring from via[0] (the anchored request)
// covers the whole gated set — including API-key spellings — without
// widening the utils restore list. Cookie is skipped: its header already
// carries the redirect merge result, and overwriting it would drop the
// cookie-bounce upserts.
func restoreHLSHeaderSet(req, src *http.Request, credentialBaseURL *url.URL) {
	if req == nil || src == nil || req.Header == nil || src.Header == nil {
		return
	}
	if !CanSendHLSCredentials(req.URL, credentialBaseURL) {
		return
	}
	for k, vs := range src.Header {
		if !IsSensitiveCredentialHeader(k) || len(vs) == 0 ||
			http.CanonicalHeaderKey(k) == "Cookie" {
			continue
		}
		req.Header[k] = append([]string(nil), vs...)
	}
	// net/http drops req.Host across redirects — a same-site Host override
	// bound to the anchor must be re-pinned or it silently dies on hop one.
	if src.Host != "" {
		req.Host = src.Host
	}
}

// sanitizeHLSOutbound deletes — never just skips copying — every header in
// the sensitive set and any Host override when the request target is outside
// the credential anchor's scheme+site. It iterates the real header keys so
// case-variant duplicates cannot hide, and it restores the default Host so
// an origin-bound override cannot ride to a foreign host.
func sanitizeHLSOutbound(req *http.Request, credentialBaseURL *url.URL) {
	if req == nil || req.URL == nil || req.Header == nil {
		return
	}
	if CanSendHLSCredentials(req.URL, credentialBaseURL) {
		return
	}
	for k := range req.Header {
		if IsSensitiveCredentialHeader(k) {
			delete(req.Header, k)
		}
	}
	// Restore the default Host so an origin-bound override cannot ride to a
	// foreign host (net/http honors req.Host verbatim on the wire).
	req.Host = req.URL.Host
}

// parseHLSBinaryURL validates a key/init/segment resource URL for an
// outbound GET: absolute http(s) with a non-empty host and no userinfo.
// Anything else is a local contract violation, not a network error.
func parseHLSBinaryURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil ||
		(!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) ||
		u.Host == "" {
		return nil, newHLSContractError("unsupported resource URL")
	}
	if u.User != nil {
		return nil, newHLSContractError("resource URL must not carry userinfo")
	}
	return u, nil
}

// hlsByteRange carries a validated RFC 8216 BYTERANGE descriptor.
type hlsByteRange struct {
	offset int64
	length int64
}

// newHLSSubRequest builds one gated GET for a binary HLS resource: task
// headers applied against the fixed credential anchor, the segment's own
// Range set afterwards (the task-level Range never survives Apply), and
// Accept-Encoding pinned to identity so a misbehaving compressor cannot
// corrupt ciphertext or byte-range math.
func newHLSSubRequest(
	ctx context.Context,
	rawURL string,
	taskHeaders map[string]string,
	credentialBaseURL *url.URL,
	runCfg *types.RuntimeConfig,
	br *hlsByteRange,
) (*http.Request, error) {
	u, err := parseHLSBinaryURL(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, newHLSContractError("cannot build request: %v", err)
	}
	ApplyHLSRequestHeaders(req, taskHeaders, credentialBaseURL, runCfg)
	if br != nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", br.offset, br.offset+br.length-1))
	}
	req.Header.Set("Accept-Encoding", "identity")
	return req, nil
}
