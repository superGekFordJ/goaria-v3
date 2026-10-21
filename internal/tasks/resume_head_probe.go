package tasks

import (
	"strings"

	"goaria-v3/internal/surge/types"
)

// ShouldSkipResumeHeadProbe reports whether SmartThread resume must not call
// rpc.HeadProbe. Non-empty headers already skip HEAD (extracted/protected).
// Skip-origin and payload-first keep presigned URLs from being burned after
// mode promotes to RangeSupported (Headers stay gob:"-"). HLS manifests are
// never HEAD-probed — a playlist has no byte-range answer to give.
func ShouldSkipResumeHeadProbe(cfg *types.DownloadRecord) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.Headers) != 0 {
		return true
	}
	return cfg.Strategy == types.StrategyHLS || cfg.SkipServerProbe || cfg.RangeAcquisitionMode.IsPayloadFirst()
}

// ResumeClassificationURL picks the URL used to classify a resumed download's
// scope/domain/envKey. For HLS the segment host is the real traffic
// destination, so the persisted SegmentRefURL wins over the manifest URL.
func ResumeClassificationURL(cfg *types.DownloadRecord) string {
	if cfg == nil {
		return ""
	}
	if cfg.Strategy == types.StrategyHLS && cfg.HLSResume != nil {
		if ref := strings.TrimSpace(cfg.HLSResume.SegmentRefURL); ref != "" {
			return ref
		}
	}
	return cfg.URL
}
