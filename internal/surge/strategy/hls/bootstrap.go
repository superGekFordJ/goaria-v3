package hls

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"goaria-v3/internal/surge/types"
)

// bootstrap resolves the prepared media table for this run. A seed that
// already carries a complete, well-formed table is used as an immutable
// clone — no re-fetch and no fingerprint compare (signed-URL refresh is a
// later slice). A nil, degraded (fallback-preflight), or rebase-invalidated
// seed re-walks the manifest chain under one shared PreflightTimeout
// budget: start at the still-usable MediaPlaylistURL when present,
// otherwise the manifest anchor; a master document selects the best muxed
// variant and fetches its media playlist under the same budget. The
// credential anchor stays the parsed original ManifestURL.
func (d *HlsDownloader) bootstrap(ctx context.Context, seed *types.HLSResumeState, credBase *url.URL, headers map[string]string, runCfg *types.RuntimeConfig) (*types.HLSResumeState, error) {
	if hasPreparedTable(seed) {
		return seed.Clone(), nil
	}

	start := strings.TrimSpace(seed.MediaPlaylistURL)
	if u, err := url.Parse(start); err != nil || u == nil || u.Host == "" ||
		(!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		start = seed.ManifestURL
	}

	fetchCtx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()

	client, release := newHLSClient(d.Client, runCfg, credBase)
	if release != nil {
		defer release()
	}

	text, finalURL, _, err := fetchManifestText(fetchCtx, client, start, headers, credBase, runCfg)
	if err != nil {
		return nil, err
	}
	kind, masterPl, mediaPl, err := ParsePlaylist(text, finalURL)
	if err != nil {
		return nil, err
	}

	var bandwidth int64
	if kind == PlaylistKindMaster {
		best, serr := SelectBestVariant(masterPl)
		if serr != nil {
			return nil, serr
		}
		bandwidth = best.Bandwidth
		body, mediaFinal, _, ferr := fetchManifestText(fetchCtx, client, best.URI, headers, credBase, runCfg)
		if ferr != nil {
			return nil, ferr
		}
		kind2, _, mediaPl2, perr := ParsePlaylist(body, mediaFinal)
		if perr != nil {
			return nil, perr
		}
		if kind2 != PlaylistKindMedia {
			return nil, fmt.Errorf("%w: nested master playlist is not allowed", types.ErrHLSInvalidManifest)
		}
		mediaPl = mediaPl2
	}
	if mediaPl == nil {
		return nil, fmt.Errorf("%w: no media playlist", types.ErrHLSInvalidManifest)
	}
	return zeroProgressResumeState(seed.ManifestURL, mediaPl, bandwidth), nil
}

// hasPreparedTable reports whether the seed carries a complete,
// well-formed media table that can be used without re-fetching: full
// segment descriptors in order, an exact-size bitmap, and a non-empty
// media playlist anchor (empty after an explicit-source rebase means the
// table was invalidated and must be re-resolved).
func hasPreparedTable(s *types.HLSResumeState) bool {
	if s == nil || s.TotalSegments <= 0 || len(s.Segments) != s.TotalSegments {
		return false
	}
	if s.MediaPlaylistURL == "" {
		return false
	}
	if len(s.CompletedBitmap) != (s.TotalSegments+7)/8 {
		return false
	}
	for i := range s.Segments {
		if s.Segments[i].Index != i {
			return false
		}
	}
	return true
}

// zeroProgressResumeState assembles a fresh initial resume state from a
// parsed media playlist — shared by enqueue preflight and run bootstrap.
func zeroProgressResumeState(manifestURL string, mediaPl *MediaPlaylist, bandwidth int64) *types.HLSResumeState {
	estimatedBytes := ComputeEstimatedBytes(bandwidth, mediaPl.TotalDurationSec)
	rs := &types.HLSResumeState{
		ManifestURL:         strings.TrimSpace(manifestURL),
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
		rs.InitSegmentURI = mediaPl.InitMap.URI
		rs.InitByteOffset = mediaPl.InitMap.ByteOffset
		rs.InitByteLength = mediaPl.InitMap.ByteLength
		rs.InitKeyIndex = mediaPl.InitMap.KeyIndex
		rs.InitIVHex = mediaPl.InitMap.IVHex
	}
	return rs
}
