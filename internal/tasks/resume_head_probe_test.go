package tasks

import (
	"testing"

	"goaria-v3/internal/surge/types"
)

func TestShouldSkipResumeHeadProbe(t *testing.T) {
	if ShouldSkipResumeHeadProbe(nil) {
		t.Fatal("nil cfg must not skip")
	}

	legacy := &types.DownloadRecord{}
	if ShouldSkipResumeHeadProbe(legacy) {
		t.Fatal("empty Headers + ProbeAtEnqueue must allow HEAD")
	}

	skipOrigin := &types.DownloadRecord{SkipServerProbe: true}
	if !ShouldSkipResumeHeadProbe(skipOrigin) {
		t.Fatal("SkipServerProbe must skip HEAD even with empty Headers")
	}

	payloadFirst := &types.DownloadRecord{RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown}
	if !ShouldSkipResumeHeadProbe(payloadFirst) {
		t.Fatal("PayloadFirstUnknown must skip HEAD")
	}

	withHeaders := &types.DownloadRecord{Headers: map[string]string{"Cookie": "sid=1"}}
	if !ShouldSkipResumeHeadProbe(withHeaders) {
		t.Fatal("non-empty Headers must skip HEAD")
	}

	rangeSupportedSkip := &types.DownloadRecord{
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		SkipServerProbe:      true,
	}
	if !ShouldSkipResumeHeadProbe(rangeSupportedSkip) {
		t.Fatal("sticky skip-origin after RangeSupported must still skip HEAD")
	}
}

// HLS rows never HEAD-probe: a playlist manifest carries no byte-range
// answer, and the segment host — not the manifest host — is what resume
// classification must attribute traffic to.
func TestShouldSkipResumeHeadProbe_HLSStrategy(t *testing.T) {
	hls := &types.DownloadRecord{Strategy: types.StrategyHLS}
	if !ShouldSkipResumeHeadProbe(hls) {
		t.Fatal("StrategyHLS must skip the resume HEAD probe")
	}
	// Skip keeps winning even when other signals are stacked on top.
	hlsFull := &types.DownloadRecord{
		Strategy:             types.StrategyHLS,
		Headers:              map[string]string{"Cookie": "sid=1"},
		SkipServerProbe:      true,
		RangeAcquisitionMode: types.RangeAcquirePayloadFirstUnknown,
	}
	if !ShouldSkipResumeHeadProbe(hlsFull) {
		t.Fatal("StrategyHLS with all skip signals set must still skip")
	}
	// HLS without SkipServerProbe/payload-first still skips — strategy alone
	// is enough.
	hlsBare := &types.DownloadRecord{Strategy: types.StrategyHLS, RangeAcquisitionMode: types.RangeAcquireProbeAtEnqueue}
	if !ShouldSkipResumeHeadProbe(hlsBare) {
		t.Fatal("StrategyHLS with probe-at-enqueue range mode must skip")
	}
}

func TestResumeClassificationURL(t *testing.T) {
	if got := ResumeClassificationURL(nil); got != "" {
		t.Fatalf("nil cfg = %q, want empty", got)
	}

	// Non-HLS rows always classify on the record URL.
	http := &types.DownloadRecord{
		URL: "https://cdn.example.com/big.iso",
		HLSResume: &types.HLSResumeState{
			SegmentRefURL: "https://seg.example.com/s0.ts",
		},
	}
	if got := ResumeClassificationURL(http); got != http.URL {
		t.Fatalf("non-HLS = %q, want record URL", got)
	}

	manifestURL := "https://cdn.example.com/master.m3u8"
	segRef := "https://seg-cdn.example.com/seg0.ts"
	hls := &types.DownloadRecord{
		Strategy:  types.StrategyHLS,
		URL:       manifestURL,
		HLSResume: &types.HLSResumeState{SegmentRefURL: segRef},
	}
	if got := ResumeClassificationURL(hls); got != segRef {
		t.Fatalf("HLS = %q, want SegmentRefURL %q", got, segRef)
	}

	// Empty/whitespace SegmentRefURL or a nil checkpoint falls back to the
	// manifest URL.
	hls.HLSResume.SegmentRefURL = "  "
	if got := ResumeClassificationURL(hls); got != manifestURL {
		t.Fatalf("blank SegmentRefURL = %q, want manifest URL", got)
	}
	hls.HLSResume = nil
	if got := ResumeClassificationURL(hls); got != manifestURL {
		t.Fatalf("nil HLSResume = %q, want manifest URL", got)
	}
}
