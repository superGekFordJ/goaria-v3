// service_add_hls.go contains HLS-specific task candidate normalization, manifest preflight wiring, classification URL selection, and permanent error unwrapping.

package tasks

import (
	"errors"
	neturl "net/url"
	"path"
	"strings"

	"goaria-v3/internal/config"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/strategy/hls"
	surgetypes "goaria-v3/internal/surge/types"
)

// ErrHLSEngineUnsupported rejects an HLS add when the active engine cannot
// run the Surge strategy; it is a terminal refusal, never a fallback.
var ErrHLSEngineUnsupported = errors.New("hls download requires surge engine")

// Injectable seam for tests: the manifest preflight is the only network
// touch inside the HLS add path.
var hlsPreflightManifest = hls.PreflightHLSManifest

// isHLSManifestURL reports whether rawURL is an HTTP(S) URL whose path ends
// in .m3u8 (case-insensitive, query/fragment ignored). It deliberately does
// not match .m3u or non-HTTP schemes.
func isHLSManifestURL(rawURL string) bool {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return false
	}
	u, err := neturl.Parse(trimmed)
	if err != nil || u == nil || u.Host == "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	p := u.EscapedPath()
	if p == "" {
		p = u.Path
	}
	if unescaped, uerr := neturl.PathUnescape(p); uerr == nil {
		p = unescaped
	}
	return strings.EqualFold(path.Ext(p), ".m3u8")
}

// normalizeHLSCandidate folds HLS intent into the candidate once: an explicit
// StrategyHLS always wins, otherwise a .m3u8 url/finalURL is detected. HLS has
// no byte-range semantics, so sizes are zeroed and the HEAD probe is skipped.
// Idempotent — repeated calls re-assert the same fields.
func normalizeHLSCandidate(candidate *addTaskCandidate) {
	if candidate == nil {
		return
	}
	if candidate.extracted {
		// Extracted items never carry a strategy. Seal the field here so
		// this exemption is the single authority — downstream reads of
		// candidate.strategy never need a second extracted check.
		candidate.strategy = ""
		return
	}
	if candidate.strategy == surgetypes.StrategyHLS ||
		isHLSManifestURL(candidate.url) ||
		isHLSManifestURL(candidate.finalURL) {
		candidate.strategy = surgetypes.StrategyHLS
		candidate.sizeBytes = 0
		candidate.externalSizeBytes = 0
		candidate.skipHeadProbe = true
		candidate.hlsResume = candidate.hlsResume.Clone()
	}
}

// supportsHLSStrategy reports whether the engine can run the Surge HLS
// strategy through the service add path. Only the hybrid router qualifies:
// it mints the sg_ gid prefix and fires BeforeSave — the two contract
// points downstream consumers rely on. A bare *SurgeEngine would accept
// the add but return an unprefixed gid and drop the hook, so it fails
// closed. A wrapped-but-inactive Surge engine counts as unsupported.
func supportsHLSStrategy(engine rpc.DownloadEngine) bool {
	hybrid, ok := engine.(*rpc.HybridEngine)
	return ok && hybrid != nil && hybrid.IsSurgeActive()
}

// surgeRuntimeConfigForPreflight borrows the engine's effective runtime config
// so the manifest preflight shares proxy/DNS policy with the download itself.
// The GoAria user agent always wins when configured.
func (s *Service) surgeRuntimeConfigForPreflight() *surgetypes.RuntimeConfig {
	var runCfg *surgetypes.RuntimeConfig
	if s != nil {
		switch eng := s.Engine.(type) {
		case *rpc.SurgeEngine:
			runCfg = eng.RuntimeConfig()
		case *rpc.HybridEngine:
			if se, ok := eng.SurgeEngineRef(); ok && se != nil {
				runCfg = se.RuntimeConfig()
			}
		}
	}
	if runCfg == nil {
		runCfg = surgetypes.DefaultRuntimeConfig()
	}
	if cfg := config.Get(); cfg != nil {
		if ua := strings.TrimSpace(cfg.UserAgent); ua != "" {
			cp := *runCfg
			cp.UserAgent = ua
			runCfg = &cp
		}
	}
	return runCfg
}

// headerLinesToMap converts "Name: value" lines into the map form the HLS
// preflight consumes; malformed lines are dropped, matching the engine side.
func headerLinesToMap(lines []string) map[string]string {
	if len(lines) == 0 {
		return nil
	}
	m := make(map[string]string, len(lines))
	for _, line := range lines {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" {
			m[k] = v
		}
	}
	return m
}

// candidateClassificationURL picks the URL used for scope/domain/envKey
// classification. For HLS the segment host — not the manifest host — is the
// real traffic destination, so SegmentRefURL leads the fallback chain.
func candidateClassificationURL(candidate addTaskCandidate) string {
	if candidate.strategy == surgetypes.StrategyHLS {
		if candidate.hlsResume != nil {
			if ref := strings.TrimSpace(candidate.hlsResume.SegmentRefURL); ref != "" {
				return ref
			}
		}
		if final := strings.TrimSpace(candidate.finalURL); final != "" {
			return final
		}
		if u := strings.TrimSpace(candidate.url); u != "" {
			return u
		}
		return strings.TrimSpace(candidate.sourceURL)
	}
	if final := strings.TrimSpace(candidate.finalURL); final != "" {
		return final
	}
	return strings.TrimSpace(candidate.url)
}

// canonicalHLSPermanentError unwraps a (possibly annotated) error to its bare
// HLS sentinel so manifest URLs, query parameters, or credentials embedded in
// the original message can never reach the frontend or logs.
func canonicalHLSPermanentError(err error) (error, bool) {
	switch {
	case errors.Is(err, surgetypes.ErrHLSLiveNotSupported):
		return surgetypes.ErrHLSLiveNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSDRMNotSupported):
		return surgetypes.ErrHLSDRMNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSDemuxedAudioNotSupported):
		return surgetypes.ErrHLSDemuxedAudioNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSDiscontinuityMapNotSupported):
		return surgetypes.ErrHLSDiscontinuityMapNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSIFramesOnlyNotSupported):
		return surgetypes.ErrHLSIFramesOnlyNotSupported, true
	case errors.Is(err, surgetypes.ErrHLSInvalidManifest):
		return surgetypes.ErrHLSInvalidManifest, true
	case errors.Is(err, surgetypes.ErrHLSInvalidSegmentResponse):
		return surgetypes.ErrHLSInvalidSegmentResponse, true
	case errors.Is(err, surgetypes.ErrHLSNotImplemented):
		return surgetypes.ErrHLSNotImplemented, true
	case errors.Is(err, ErrHLSEngineUnsupported):
		return ErrHLSEngineUnsupported, true
	}
	return nil, false
}
