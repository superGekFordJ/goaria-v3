package tasks

import "goaria-v3/internal/surge/types"

// ShouldSkipResumeHeadProbe reports whether SmartThread resume must not call
// rpc.HeadProbe. Non-empty headers already skip HEAD (extracted/protected).
// Skip-origin and payload-first keep presigned URLs from being burned after
// mode promotes to RangeSupported (Headers stay gob:"-").
func ShouldSkipResumeHeadProbe(cfg *types.DownloadRecord) bool {
	if cfg == nil {
		return false
	}
	if len(cfg.Headers) != 0 {
		return true
	}
	return cfg.SkipServerProbe || cfg.RangeAcquisitionMode.IsPayloadFirst()
}
