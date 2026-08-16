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
