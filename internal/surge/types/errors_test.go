package types

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestSourceMetadataMismatchError(t *testing.T) {
	err := NewSourceMetadataMismatch(MismatchKind206Total, 8192)
	if !errors.Is(err, ErrSourceMetadataMismatch) {
		t.Fatal("typed mismatch must match ErrSourceMetadataMismatch")
	}
	if !strings.Contains(err.Error(), ErrSourceMetadataMismatch.Error()) {
		t.Fatalf("Error() = %q, want substring %q", err.Error(), ErrSourceMetadataMismatch.Error())
	}

	var got *SourceMetadataMismatchError
	if !errors.As(err, &got) || got == nil {
		t.Fatal("errors.As must recover Kind and ObservedSize")
	}
	if got.Kind != MismatchKind206Total || got.ObservedSize != 8192 {
		t.Fatalf("Kind=%q ObservedSize=%d", got.Kind, got.ObservedSize)
	}

	wrapped := fmt.Errorf("payload-first: %w", err)
	if !errors.Is(wrapped, ErrSourceMetadataMismatch) {
		t.Fatal("%w wrap must still match the sentinel")
	}
	var inner *SourceMetadataMismatchError
	if !errors.As(wrapped, &inner) || inner.Kind != MismatchKind206Total {
		t.Fatal("%w wrap must still expose Kind")
	}
}

func TestIsPermanentHTTPError(t *testing.T) {
	if !IsPermanentHTTPError(ErrPermanentHTTP) {
		t.Errorf("Expected IsPermanentHTTPError(ErrPermanentHTTP) to be true")
	}

	wrappedErr := errors.Join(errors.New("some error"), ErrPermanentHTTP)
	if !IsPermanentHTTPError(wrappedErr) {
		t.Errorf("Expected IsPermanentHTTPError to be true for wrapped ErrPermanentHTTP")
	}

	if IsPermanentHTTPError(errors.New("other error")) {
		t.Errorf("Expected IsPermanentHTTPError to be false for other errors")
	}
}

func TestIsInsufficientDiskSpace_JoinParity(t *testing.T) {
	joined := errors.Join(errors.New("write failed"), ErrInsufficientDiskSpace)
	if !IsInsufficientDiskSpace(joined) {
		t.Fatal("expected Join-wrapped ErrInsufficientDiskSpace to match")
	}
}

func TestIsPermanentHTTPStatus(t *testing.T) {
	tests := []struct {
		status   int
		expected bool
	}{
		{http.StatusOK, false},
		{http.StatusMovedPermanently, false},
		{http.StatusBadRequest, true},           // 400
		{http.StatusUnauthorized, true},         // 401
		{http.StatusForbidden, true},            // 403
		{http.StatusNotFound, true},             // 404
		{http.StatusTooManyRequests, false},     // 429 is explicitly transient
		{http.StatusInternalServerError, false}, // 500
		{http.StatusServiceUnavailable, false},  // 503
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			if got := IsPermanentHTTPStatus(tt.status); got != tt.expected {
				t.Errorf("IsPermanentHTTPStatus(%d) = %v, want %v", tt.status, got, tt.expected)
			}
		})
	}
}

func TestIsPermanentHLSError(t *testing.T) {
	permanent := []error{
		ErrHLSLiveNotSupported,
		ErrHLSDRMNotSupported,
		ErrHLSDemuxedAudioNotSupported,
		ErrHLSDiscontinuityMapNotSupported,
		ErrHLSInvalidManifest,
		ErrHLSInvalidSegmentResponse,
		ErrHLSNotImplemented,
	}
	for _, err := range permanent {
		if !IsPermanentHLSError(err) {
			t.Errorf("IsPermanentHLSError(%v) = false, want true", err)
		}
		wrapped := fmt.Errorf("enqueue wrapper: %w", err)
		if !IsPermanentHLSError(wrapped) {
			t.Errorf("IsPermanentHLSError(wrapped %v) = false, want true", err)
		}
	}

	nonPermanent := []error{
		nil,
		errors.New("other error"),
		ErrNoProgress,
		ErrPermanentHTTP,
		ErrRangeUnsupported,
		ErrPaused,
	}
	for _, err := range nonPermanent {
		if IsPermanentHLSError(err) {
			t.Errorf("IsPermanentHLSError(%v) = true, want false", err)
		}
	}
	if IsPermanentHLSError(fmt.Errorf("outer: %w", errors.New("inner"))) {
		t.Error("IsPermanentHLSError(wrapped generic) = true, want false")
	}
}

func TestHLSResumeState_CloneNil(t *testing.T) {
	var s *HLSResumeState
	if got := s.Clone(); got != nil {
		t.Fatalf("nil Clone() = %v, want nil", got)
	}
}

func TestHLSResumeState_CloneDeepCopy(t *testing.T) {
	src := &HLSResumeState{
		ManifestURL:         "https://cdn.example.com/master.m3u8",
		MediaPlaylistURL:    "https://cdn.example.com/index.m3u8",
		SegmentRefURL:       "https://cdn.example.com/index.m3u8",
		ManifestFingerprint: "fp123",
		ContainerFormat:     "mpegts",
		EstimatedBytes:      1 << 20,
		RecordEligible:      true,
		InitSegmentURI:      "init.mp4",
		InitByteOffset:      128,
		InitByteLength:      768,
		InitCompleted:       true,
		InitBytes:           700,
		TotalSegments:       2,
		CompletedBitmap:     []byte{0x03},
		MergedSegments:      1,
		MergedBytes:         500,
		VerifiedBytes:       1200,
		SpillPath:           "spill.bin",
		SpillFileSize:       4096,
		SpillEntries:        []HLSSpillEntry{{SegmentIndex: 0, Offset: 0, Length: 512}},
		Keys:                []HLSKeyInfo{{Method: "AES-128", URI: "key.bin", IVHex: "00"}},
		Segments: []HLSSegmentState{
			{Index: 0, Sequence: 10, URI: "seg0.ts", DurationMicros: 4e6, KeyIndex: 1, IVHex: "aa"},
			{Index: 1, Sequence: 11, URI: "seg1.ts", ByteOffset: 3, ByteLength: 99},
		},
	}

	cp := src.Clone()
	if cp == nil {
		t.Fatal("Clone() returned nil for non-nil source")
	}
	if cp == src {
		t.Fatal("Clone() returned the same pointer")
	}

	// Scalar fields preserved.
	if cp.ManifestURL != src.ManifestURL ||
		cp.MediaPlaylistURL != src.MediaPlaylistURL ||
		cp.SegmentRefURL != src.SegmentRefURL ||
		cp.ManifestFingerprint != src.ManifestFingerprint ||
		cp.ContainerFormat != src.ContainerFormat ||
		cp.EstimatedBytes != src.EstimatedBytes ||
		cp.RecordEligible != src.RecordEligible ||
		cp.InitSegmentURI != src.InitSegmentURI ||
		cp.InitByteOffset != src.InitByteOffset ||
		cp.InitByteLength != src.InitByteLength ||
		cp.InitCompleted != src.InitCompleted ||
		cp.InitBytes != src.InitBytes ||
		cp.TotalSegments != src.TotalSegments ||
		cp.MergedSegments != src.MergedSegments ||
		cp.MergedBytes != src.MergedBytes ||
		cp.VerifiedBytes != src.VerifiedBytes ||
		cp.SpillPath != src.SpillPath ||
		cp.SpillFileSize != src.SpillFileSize {
		t.Fatal("Clone() did not preserve scalar fields")
	}
	if len(cp.CompletedBitmap) != len(src.CompletedBitmap) ||
		len(cp.SpillEntries) != len(src.SpillEntries) ||
		len(cp.Keys) != len(src.Keys) ||
		len(cp.Segments) != len(src.Segments) {
		t.Fatal("Clone() changed slice lengths")
	}

	// Slice backing arrays must be physically isolated: mutating the clone
	// must not touch the source and vice versa.
	cp.CompletedBitmap[0] = 0xFF
	cp.SpillEntries[0].Length = 9999
	cp.Keys[0].URI = "mutated-key"
	cp.Segments[0].KeyIndex = 7

	if src.CompletedBitmap[0] != 0x03 ||
		src.SpillEntries[0].Length != 512 ||
		src.Keys[0].URI != "key.bin" ||
		src.Segments[0].KeyIndex != 1 {
		t.Fatal("mutating clone leaked into source slices")
	}

	src.CompletedBitmap[0] = 0x00
	src.SpillEntries[0].Offset = 12345
	src.Keys[0].Method = "NONE"
	src.Segments[1].URI = "mutated-seg"

	if cp.CompletedBitmap[0] != 0xFF ||
		cp.SpillEntries[0].Offset != 0 ||
		cp.Keys[0].Method != "AES-128" ||
		cp.Segments[1].URI != "seg1.ts" {
		t.Fatal("mutating source leaked into clone slices")
	}
}

func TestHLSResumeState_CloneNilSlices(t *testing.T) {
	src := &HLSResumeState{TotalSegments: 3, RecordEligible: true}
	cp := src.Clone()
	if cp == nil {
		t.Fatal("Clone() returned nil for non-nil source")
	}
	if cp.CompletedBitmap != nil || cp.SpillEntries != nil || cp.Keys != nil || cp.Segments != nil {
		t.Fatal("Clone() materialized nil slices")
	}
}
