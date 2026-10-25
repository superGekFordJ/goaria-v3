package hls

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// bootstrap resolves the prepared media table for this run. A prepared
// table with no resume footprint is used as an immutable clone — it just
// came out of a manifest fetch, so a second GET is never paid for it. A
// prepared table that does carry resume footprint (explicit resume flag,
// merged prefix, verified bytes, a spill ledger or a completed init) may
// hold expired signatures, so the manifest is re-walked once under the
// shared PreflightTimeout budget: the refreshed playlist must validate as
// the same VOD (structure and, when stored, the exact fingerprint) before
// its signed URIs are patched onto a clone bounded by the merged prefix.
// A transient fetch failure degrades to the unchanged clone — the old
// URIs retry inside the ordinary budget — while a parent cancel, a
// permanent gate or a metadata mismatch always propagates. A nil,
// degraded (fallback-preflight), or rebase-invalidated seed re-walks the
// chain the same way and builds a zero-progress table. The credential
// anchor stays the parsed original ManifestURL throughout.
func (d *HlsDownloader) bootstrap(ctx context.Context, seed *types.HLSResumeState, credBase *url.URL, headers map[string]string, runCfg *types.RuntimeConfig) (*types.HLSResumeState, error) {
	if hasPreparedTable(seed) {
		prepared := seed.Clone()
		if !d.resumeRefreshRequired(seed) {
			return prepared, nil
		}
		mediaPl, _, err := d.fetchPlaylistUnderBudget(ctx, seed, credBase, headers, runCfg)
		if err != nil {
			if perr := ctx.Err(); perr != nil {
				// A pause/cancel can never degrade into a stale-table run.
				return nil, perr
			}
			if fatal := refreshFatalCause(err); fatal != nil {
				return nil, fatal
			}
			// Transient fetch failure — the persisted table stays the best
			// provable seed; its old URIs retry inside the normal budget.
			utils.Debug("hls: resume manifest refresh degraded, keeping persisted URIs: %v", err)
			return prepared, nil
		}
		if err := validateRefreshPlaylist(seed, mediaPl); err != nil {
			return nil, err
		}
		// Only the clone is patched — every failure path above leaves the
		// checkpointed seed untouched so a mount-time refusal can never
		// half-rewrite it. Segments at or beyond MergedSegments (the spill
		// ledger indexes included) move to the new URIs; the merged prefix
		// is never fetched again and keeps its recorded URIs.
		applyManifestURIs(prepared, mediaPl, prepared.MergedSegments)
		return prepared, nil
	}

	mediaPl, bandwidth, err := d.fetchPlaylistUnderBudget(ctx, seed, credBase, headers, runCfg)
	if err != nil {
		return nil, err
	}
	return zeroProgressResumeState(seed.ManifestURL, mediaPl, bandwidth), nil
}

// resumeRefreshRequired reports whether a prepared table may carry stale
// signatures: the explicit resume flag also covers the zero-byte auto
// retry (a failed run leaves progress counters at zero but its URIs aged
// out), and every progress marker — merged prefix, verified bytes, spill
// ledger, completed init — means the run lived long enough for URIs to
// expire.
func (d *HlsDownloader) resumeRefreshRequired(seed *types.HLSResumeState) bool {
	if seed.MergedSegments >= seed.TotalSegments && seed.VerifiedBytes == seed.MergedBytes {
		// Everything is already merged on disk — no segment URI will ever
		// be fetched, so a dead signature can not matter and the manifest
		// is not needed.
		return false
	}
	return d.IsResume || seed.MergedSegments > 0 || seed.VerifiedBytes > 0 ||
		len(seed.SpillEntries) > 0 || seed.InitCompleted
}

// fetchPlaylistUnderBudget walks the manifest chain under the shared
// PreflightTimeout budget with the run's gated client, returning the
// selected media playlist and its declared bandwidth (zero when the entry
// itself was a media document).
func (d *HlsDownloader) fetchPlaylistUnderBudget(ctx context.Context, seed *types.HLSResumeState, credBase *url.URL, headers map[string]string, runCfg *types.RuntimeConfig) (*MediaPlaylist, int64, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()
	client, release := newHLSClient(d.Client, runCfg, credBase)
	if release != nil {
		defer release()
	}
	return fetchRefreshPlaylist(fetchCtx, client, seed, headers, credBase, runCfg)
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
