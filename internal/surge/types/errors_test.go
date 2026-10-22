package types

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
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
		ErrHLSIFramesOnlyNotSupported,
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
		InitKeyIndex:        2,
		InitIVHex:           "0000000000000000000000000000000a",
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
		cp.InitKeyIndex != src.InitKeyIndex ||
		cp.InitIVHex != src.InitIVHex ||
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

// hlsResumeStateV1 mirrors HLSResumeState before the InitKeyIndex/InitIVHex
// addition: gob decoding fills absent fields with zero values, so a detail
// gob written by an older build must stay decodable with plaintext-init
// semantics.
type hlsResumeStateV1 struct {
	ManifestURL         string
	MediaPlaylistURL    string
	SegmentRefURL       string
	ManifestFingerprint string
	ContainerFormat     string
	EstimatedBytes      int64
	RecordEligible      bool
	InitSegmentURI      string
	InitByteOffset      int64
	InitByteLength      int64
	InitCompleted       bool
	InitBytes           int64
	TotalSegments       int
	CompletedBitmap     []byte
	MergedSegments      int
	MergedBytes         int64
	VerifiedBytes       int64
	SpillPath           string
	SpillFileSize       int64
	SpillEntries        []HLSSpillEntry
	Keys                []HLSKeyInfo
	Segments            []HLSSegmentState
}

func TestHLSResumeState_GobJSONRoundTrip(t *testing.T) {
	src := &HLSResumeState{
		ManifestURL:         "https://cdn.example.com/master.m3u8",
		MediaPlaylistURL:    "https://cdn.example.com/index.m3u8",
		ManifestFingerprint: "fp",
		ContainerFormat:     "fmp4",
		RecordEligible:      true,
		InitSegmentURI:      "init.mp4",
		InitByteLength:      720,
		InitKeyIndex:        1,
		InitIVHex:           "0000000000000000000000000000000f",
		InitCompleted:       true,
		InitBytes:           720,
		TotalSegments:       1,
		CompletedBitmap:     []byte{0x01},
		MergedSegments:      1,
		MergedBytes:         720,
		VerifiedBytes:       1440,
		SpillEntries:        []HLSSpillEntry{{SegmentIndex: 0, Offset: 3, Length: 17}},
		Keys:                []HLSKeyInfo{{Method: "AES-128", URI: "k.bin", IVHex: "0f"}},
		Segments:            []HLSSegmentState{{Index: 0, URI: "s.ts", KeyIndex: 1}},
	}

	t.Run("GobRoundTrip", func(t *testing.T) {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(src); err != nil {
			t.Fatalf("gob encode: %v", err)
		}
		var dst HLSResumeState
		if err := gob.NewDecoder(&buf).Decode(&dst); err != nil {
			t.Fatalf("gob decode: %v", err)
		}
		if dst.InitKeyIndex != src.InitKeyIndex || dst.InitIVHex != src.InitIVHex {
			t.Fatalf("init crypto fields lost: %+v", dst)
		}
		if dst.VerifiedBytes != src.VerifiedBytes || len(dst.Segments) != 1 ||
			dst.Segments[0].KeyIndex != 1 || len(dst.SpillEntries) != 1 {
			t.Fatalf("round-trip mismatch: %+v", dst)
		}
	})

	t.Run("OldGobDecodesZeroCryptoFields", func(t *testing.T) {
		old := &hlsResumeStateV1{
			ManifestURL:     "m",
			InitSegmentURI:  "init.mp4",
			InitByteLength:  720,
			InitCompleted:   true,
			InitBytes:       720,
			TotalSegments:   1,
			CompletedBitmap: []byte{0x01},
			MergedSegments:  1,
			MergedBytes:     720,
			VerifiedBytes:   720,
		}
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(old); err != nil {
			t.Fatalf("gob encode legacy: %v", err)
		}
		var dst HLSResumeState
		if err := gob.NewDecoder(&buf).Decode(&dst); err != nil {
			t.Fatalf("gob decode legacy: %v", err)
		}
		if dst.InitKeyIndex != 0 || dst.InitIVHex != "" {
			t.Fatalf("legacy decode must zero-fill crypto fields: %+v", dst)
		}
		if dst.InitBytes != 720 || dst.TotalSegments != 1 {
			t.Fatalf("legacy fields lost: %+v", dst)
		}
	})

	t.Run("JSONSnakeCase", func(t *testing.T) {
		data, err := json.Marshal(src)
		if err != nil {
			t.Fatalf("json marshal: %v", err)
		}
		var flat map[string]any
		if err := json.Unmarshal(data, &flat); err != nil {
			t.Fatalf("json unmarshal: %v", err)
		}
		if _, ok := flat["init_key_index"]; !ok {
			t.Fatalf("missing init_key_index key in %s", data)
		}
		if _, ok := flat["init_iv_hex"]; !ok {
			t.Fatalf("missing init_iv_hex key in %s", data)
		}
		if flat["init_key_index"].(float64) != 1 || flat["init_iv_hex"] != src.InitIVHex {
			t.Fatalf("json values mismatch: %s", data)
		}
		var dst HLSResumeState
		if err := json.Unmarshal(data, &dst); err != nil {
			t.Fatalf("json round-trip: %v", err)
		}
		if dst.InitKeyIndex != src.InitKeyIndex || dst.InitIVHex != src.InitIVHex {
			t.Fatalf("json round-trip lost crypto fields: %+v", dst)
		}
	})
}
