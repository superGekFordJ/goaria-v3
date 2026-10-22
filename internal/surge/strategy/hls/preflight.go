package hls

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

const (
	// PreflightTimeout bounds the whole Master -> Media fetch+parse chain so a
	// dead endpoint can never stall enqueue.
	PreflightTimeout = 3 * time.Second
	// MaxManifestBytes caps a single manifest response body.
	MaxManifestBytes = 8 * utils.MiB
)

// HLSPreflightResult is the successful output of PreflightHLSManifest.
type HLSPreflightResult struct {
	ResumeState  *types.HLSResumeState
	ContainerExt string // ".mp4" | ".ts"
	// TTFBMs measures the first manifest hop only; a master playlist's
	// second (media) hop shares the timeout budget but is not timed here.
	TTFBMs            int64
	SelectedBandwidth int64
	TotalDurationSec  float64
}

// PreflightHLSManifest fetches and validates an HLS manifest inside one
// PreflightTimeout budget. When the initial document is a master playlist the
// best muxed variant is selected and its media playlist fetched under the same
// budget. On success it returns a fully populated initial HLSResumeState that
// satisfies the task-backed snapshot invariants (len(Segments)==TotalSegments,
// len(CompletedBitmap)==(TotalSegments+7)/8, VerifiedBytes==0).
func PreflightHLSManifest(
	ctx context.Context,
	client *http.Client,
	manifestURL string,
	headers map[string]string,
	runCfg *types.RuntimeConfig,
) (*HLSPreflightResult, error) {
	trimmedURL := strings.TrimSpace(manifestURL)
	originURL, err := url.Parse(trimmedURL)
	if err != nil || originURL == nil ||
		(!strings.EqualFold(originURL.Scheme, "http") && !strings.EqualFold(originURL.Scheme, "https")) ||
		originURL.Host == "" {
		return nil, fmt.Errorf("%w: invalid manifest URL", types.ErrHLSInvalidManifest)
	}

	preflightCtx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()

	httpClient, release := buildPreflightClient(client, runCfg)
	if release != nil {
		defer release()
	}

	bodyText, finalURL, ttfbMs, err := fetchManifestText(preflightCtx, httpClient, trimmedURL, headers, originURL, runCfg)
	if err != nil {
		return nil, err
	}
	kind, masterPl, mediaPl, err := ParsePlaylist(bodyText, finalURL)
	if err != nil {
		return nil, err
	}

	var selectedBandwidth int64
	if kind == PlaylistKindMaster {
		bestVariant, serr := SelectBestVariant(masterPl)
		if serr != nil {
			return nil, serr
		}
		selectedBandwidth = bestVariant.Bandwidth
		// Credentials stay anchored to the ORIGINAL request URL so a prior
		// cross-site redirect cannot launder them onto the playlist host.
		mediaBody, mediaFinalURL, _, ferr := fetchManifestText(
			preflightCtx, httpClient, bestVariant.URI, headers, originURL, runCfg)
		if ferr != nil {
			return nil, ferr
		}
		kind2, _, mediaPl2, perr := ParsePlaylist(mediaBody, mediaFinalURL)
		if perr != nil {
			return nil, perr
		}
		if kind2 != PlaylistKindMedia {
			return nil, fmt.Errorf("%w: nested master playlist is not allowed", types.ErrHLSInvalidManifest)
		}
		mediaPl = mediaPl2
	}

	estimatedBytes := ComputeEstimatedBytes(selectedBandwidth, mediaPl.TotalDurationSec)
	resumeState := &types.HLSResumeState{
		ManifestURL:         trimmedURL,
		MediaPlaylistURL:    mediaPl.BaseURL,
		SegmentRefURL:       mediaPl.SegmentRefURL,
		ManifestFingerprint: mediaPl.ManifestFingerprint,
		ContainerFormat:     mediaPl.ContainerFormat,
		EstimatedBytes:      estimatedBytes,
		RecordEligible:      ComputeRecordEligible(estimatedBytes),
		TotalSegments:       len(mediaPl.Segments),
		CompletedBitmap:     make([]byte, (len(mediaPl.Segments)+7)/8),
		Keys:                slices.Clone(mediaPl.Keys),
		Segments:            slices.Clone(mediaPl.Segments),
	}
	if mediaPl.InitMap != nil {
		resumeState.InitSegmentURI = mediaPl.InitMap.URI
		resumeState.InitByteOffset = mediaPl.InitMap.ByteOffset
		resumeState.InitByteLength = mediaPl.InitMap.ByteLength
		resumeState.InitKeyIndex = mediaPl.InitMap.KeyIndex
		resumeState.InitIVHex = mediaPl.InitMap.IVHex
	}
	return &HLSPreflightResult{
		ResumeState:       resumeState,
		ContainerExt:      ContainerFormatExtension(mediaPl.ContainerFormat),
		TTFBMs:            ttfbMs,
		SelectedBandwidth: selectedBandwidth,
		TotalDurationSec:  mediaPl.TotalDurationSec,
	}, nil
}

// buildPreflightClient returns the HTTP client for the preflight. A nil client
// borrows the shared network pool (released by the returned func); a non-nil
// client is shallow-copied (the caller's value is never mutated) and gains
// the default redirect guard only when it has none.
func buildPreflightClient(client *http.Client, runCfg *types.RuntimeConfig) (*http.Client, func()) {
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return types.ErrMaxRedirects
		}
		if len(via) > 0 {
			utils.CopyRedirectHeaders(req, via[0])
		}
		return nil
	}
	if client != nil {
		c := *client
		if c.CheckRedirect == nil {
			c.CheckRedirect = checkRedirect
		}
		return &c, nil
	}
	var proxyURL, customDNS string
	if runCfg != nil {
		proxyURL = runCfg.ProxyURL
		customDNS = runCfg.CustomDNS
	}
	tr := transport.DefaultNetworkPool.AcquireTransport(proxyURL, customDNS, 0)
	return &http.Client{Transport: tr, CheckRedirect: checkRedirect}, func() {
		transport.DefaultNetworkPool.ReleaseTransport(tr)
	}
}

// fetchManifestText GETs rawURL with the gated task headers, requires 200 OK,
// and returns the body bounded by MaxManifestBytes plus the final post-redirect
// URL and the time-to-first-byte of this hop.
func fetchManifestText(
	ctx context.Context,
	client *http.Client,
	rawURL string,
	taskHeaders map[string]string,
	credentialBaseURL *url.URL,
	runCfg *types.RuntimeConfig,
) (bodyText string, finalURL *url.URL, ttfbMs int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, 0, fmt.Errorf("%w: %w", types.ErrHLSInvalidManifest, err)
	}
	ApplyHLSRequestHeaders(req, taskHeaders, credentialBaseURL, runCfg)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, 0, err
	}
	defer func() {
		// Drain a small tail so keep-alive can reuse the connection.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*utils.KiB))
		_ = resp.Body.Close()
	}()
	ttfbMs = time.Since(start).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		if types.IsPermanentHTTPStatus(resp.StatusCode) {
			return "", nil, 0, fmt.Errorf("%w: manifest fetch status %d", types.ErrPermanentHTTP, resp.StatusCode)
		}
		return "", nil, 0, fmt.Errorf("manifest fetch: unexpected status %d", resp.StatusCode)
	}

	finalURL = resp.Request.URL
	if finalURL == nil {
		if parsed, perr := url.Parse(rawURL); perr == nil {
			finalURL = parsed
		}
	}

	data, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxManifestBytes+1))
	if rerr != nil {
		return "", nil, 0, rerr
	}
	if int64(len(data)) > MaxManifestBytes {
		return "", nil, 0, fmt.Errorf("%w: manifest exceeds %d bytes", types.ErrHLSInvalidManifest, MaxManifestBytes)
	}
	return string(data), finalURL, ttfbMs, nil
}

// ShouldFailFastPreflight reports whether a PreflightHLSManifest error is a
// permanent HLS gate that must reject the task outright, as opposed to a
// transient network error where the caller may degrade via
// NewFallbackHLSResumeState. A caller-cancelled context should be checked by
// the caller through ctx.Err() first.
func ShouldFailFastPreflight(err error) bool {
	return types.IsPermanentHLSError(err)
}

// NewFallbackHLSResumeState builds the degraded initial resume state used when
// the preflight fails on a transient error. Container/EstimatedBytes are
// unknown at this point, so the record stays eligible.
func NewFallbackHLSResumeState(manifestURL, fallbackRefURL string) *types.HLSResumeState {
	manifestURL = strings.TrimSpace(manifestURL)
	refURL := strings.TrimSpace(fallbackRefURL)
	if refURL == "" {
		refURL = manifestURL
	}
	return &types.HLSResumeState{
		ManifestURL:      manifestURL,
		MediaPlaylistURL: manifestURL,
		SegmentRefURL:    refURL,
		ContainerFormat:  ContainerTS,
		EstimatedBytes:   0,
		RecordEligible:   true,
	}
}
