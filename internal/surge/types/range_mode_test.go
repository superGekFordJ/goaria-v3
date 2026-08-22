package types

import (
	"bytes"
	"encoding/gob"
	"errors"
	"testing"
)

func TestResolveRangeAcquisitionMode(t *testing.T) {
	if got := ResolveRangeAcquisitionMode("", nil); got != RangeAcquireProbeAtEnqueue {
		t.Fatalf("nil pointer = %q, want empty", got)
	}
	if got := ResolveRangeAcquisitionMode("", new(true)); got != RangeAcquireRangeSupported {
		t.Fatalf("*true = %q, want range_supported", got)
	}
	if got := ResolveRangeAcquisitionMode("", new(false)); got != RangeAcquirePayloadFirstUnknown {
		t.Fatalf("*false = %q, want payload_first_unknown (unknown, not sequential)", got)
	}
	if got := ResolveRangeAcquisitionMode(RangeAcquireRangeUnsupported, new(true)); got != RangeAcquireRangeUnsupported {
		t.Fatalf("explicit mode must win, got %q", got)
	}
}

func TestShouldSkipServerProbe(t *testing.T) {
	if ShouldSkipServerProbe("", nil) {
		t.Fatal("empty mode + nil pointer must not skip")
	}
	if !ShouldSkipServerProbe("", new(false)) {
		t.Fatal("286 *false must still skip ProbeServer")
	}
	if !ShouldSkipServerProbe(RangeAcquirePayloadFirstUnknown, nil) {
		t.Fatal("payload-first mode must skip ProbeServer")
	}
}

func TestShouldUseConcurrent(t *testing.T) {
	if !ShouldUseConcurrent(RangeAcquirePayloadFirstUnknown, false) {
		t.Fatal("payload-first must launch concurrent even when SupportsRange bool is false")
	}
	if ShouldUseConcurrent(RangeAcquireRangeUnsupported, true) {
		t.Fatal("proven unsupported must use single")
	}
	if !ShouldUseConcurrent("", true) {
		t.Fatal("empty mode + SupportsRange true keeps pre-287 concurrent")
	}
	if ShouldUseConcurrent("", false) {
		t.Fatal("empty mode + SupportsRange false keeps pre-287 sequential")
	}
}

func TestUpgradeLegacyRangeMode(t *testing.T) {
	if got := UpgradeLegacyRangeMode("", true); got != RangeAcquireRangeSupported {
		t.Fatalf("legacy tasks = %q, want range_supported", got)
	}
	if got := UpgradeLegacyRangeMode("", false); got != RangeAcquireProbeAtEnqueue {
		t.Fatalf("legacy no snapshot = %q, want empty (do not invent payload-first)", got)
	}
	if got := UpgradeLegacyRangeMode(RangeAcquirePayloadFirstUnknown, false); got != RangeAcquirePayloadFirstUnknown {
		t.Fatalf("set mode must not be rewritten, got %q", got)
	}
}

func TestRangeAcquire_GobDecodeLegacyEmptyMode(t *testing.T) {
	old := DownloadRecord{
		ID:        "legacy-id",
		URL:       "http://example.com/file.bin",
		TotalSize: 1024,
		Tasks:     []Task{{Offset: 0, Length: 1024}},
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(old); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var rec DownloadRecord
	if err := gob.NewDecoder(&buf).Decode(&rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.RangeAcquisitionMode != RangeAcquireProbeAtEnqueue {
		t.Fatalf("mode = %q, want empty", rec.RangeAcquisitionMode)
	}
	if rec.SkipServerProbe {
		t.Fatal("SkipServerProbe = true, want false")
	}
	if rec.ID != "legacy-id" || rec.TotalSize != 1024 || len(rec.Tasks) != 1 {
		t.Fatalf("legacy identity lost: %+v", rec)
	}
}

func TestErrRangeSentinels(t *testing.T) {
	if !errors.Is(ErrRangeUnsupported, ErrRangeUnsupported) {
		t.Fatal("ErrRangeUnsupported identity")
	}
	if !errors.Is(ErrSourceMetadataMismatch, ErrSourceMetadataMismatch) {
		t.Fatal("ErrSourceMetadataMismatch identity")
	}
	if errors.Is(ErrRangeUnsupported, ErrSourceMetadataMismatch) {
		t.Fatal("sentinels must not match each other")
	}
}
