package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"goaria-v3/internal/surge/types"
)

func TestShouldRetryFailedDownload_SkipsInsufficientDisk(t *testing.T) {
	diskErr := fmt.Errorf("write error: %w", types.ErrInsufficientDiskSpace)
	if shouldRetryFailedDownload(diskErr, false, 0) {
		t.Fatal("disk-space failure must not retry")
	}
	if shouldRetryFailedDownload(types.ErrPermanentHTTP, false, 0) {
		t.Fatal("permanent HTTP must not retry")
	}
	if shouldRetryFailedDownload(context.Canceled, false, 0) {
		t.Fatal("canceled must not retry")
	}
	if shouldRetryFailedDownload(context.DeadlineExceeded, false, 0) {
		t.Fatal("deadline must not retry")
	}
	if shouldRetryFailedDownload(errors.New("transient"), true, 0) {
		t.Fatal("shutting down must not retry")
	}
	if shouldRetryFailedDownload(errors.New("transient"), false, 10) {
		t.Fatal("retries>=10 must not retry")
	}
	if !shouldRetryFailedDownload(errors.New("transient"), false, 0) {
		t.Fatal("transient failure should retry")
	}
}

func TestShouldFallbackToSingle_SkipsInsufficientDisk(t *testing.T) {
	diskErr := fmt.Errorf("write error: %w", types.ErrInsufficientDiskSpace)
	if shouldFallbackToSingle(diskErr, 0, "") {
		t.Fatal("disk-space must not Truncate+single fallback")
	}
	if shouldFallbackToSingle(types.ErrPaused, 0, "") {
		t.Fatal("paused must not fallback")
	}
	if shouldFallbackToSingle(context.Canceled, 0, "") {
		t.Fatal("canceled must not fallback")
	}
	if shouldFallbackToSingle(errors.New("boom"), 100, "") {
		t.Fatal("progress>0 must not fallback")
	}
	if !shouldFallbackToSingle(errors.New("boom"), 0, "") {
		t.Fatal("zero-progress transient should fallback")
	}
	if shouldFallbackToSingle(nil, 0, "") {
		t.Fatal("nil err must not fallback")
	}
}

func TestShouldFallbackToSingle_PayloadFirst(t *testing.T) {
	mode := types.RangeAcquirePayloadFirstUnknown
	if !shouldFallbackToSingle(types.ErrRangeUnsupported, 0, mode) {
		t.Fatal("payload-first ignore-Range at 0 bytes must fallback")
	}
	if shouldFallbackToSingle(types.ErrRangeUnsupported, 100, mode) {
		t.Fatal("verified bytes must not Truncate on later 200")
	}
	if shouldFallbackToSingle(types.ErrSourceMetadataMismatch, 0, mode) {
		t.Fatal("mismatch must not auto-fallback")
	}
	if shouldFallbackToSingle(errors.New("connection reset"), 0, mode) {
		t.Fatal("transport must not be treated as Range-unsupported")
	}
	if shouldFallbackToSingle(fmt.Errorf("unexpected status: 403"), 0, mode) {
		t.Fatal("403 must not fallback on payload-first")
	}
	if !shouldFallbackToSingle(types.ErrRangeUnsupported, 0, types.RangeAcquireRangeSupported) {
		t.Fatal("RangeSupported at 0 verified bytes may fallback on proven ignore-Range")
	}
	if shouldFallbackToSingle(types.ErrSourceMetadataMismatch, 0, types.RangeAcquireRangeSupported) {
		t.Fatal("RangeSupported mismatch must not Truncate")
	}
}

func TestShouldRetryFailedDownload_RangeSentinels(t *testing.T) {
	if shouldRetryFailedDownload(types.ErrRangeUnsupported, false, 0) {
		t.Fatal("ErrRangeUnsupported must be terminal")
	}
	if shouldRetryFailedDownload(types.ErrSourceMetadataMismatch, false, 0) {
		t.Fatal("ErrSourceMetadataMismatch must be terminal")
	}
	if !shouldRetryFailedDownload(errors.New("transient"), false, 0) {
		t.Fatal("transient failure should still retry")
	}
}
