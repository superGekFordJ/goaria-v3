package types

import (
	"testing"
)

// RebaseManifestURL must collapse every derived reference to a minimal
// anchor-only seed without mutating the receiver.
func TestHLSRebase_ManifestURLCollapsesToMinimalSeed(t *testing.T) {
	const oldURL = "https://cdn-a.example.com/master.m3u8"
	const newURL = "https://cdn-b.example.com/v2/master.m3u8"

	full := &HLSResumeState{
		ManifestURL:         oldURL,
		MediaPlaylistURL:    "https://cdn-a.example.com/media.m3u8",
		SegmentRefURL:       "https://cdn-a.example.com/media.m3u8",
		ManifestFingerprint: "abc123",
		ContainerFormat:     "fmp4",
		EstimatedBytes:      4096,
		RecordEligible:      true,
		InitSegmentURI:      "init.mp4",
		InitByteOffset:      0,
		InitByteLength:      720,
		InitKeyIndex:        1,
		InitIVHex:           "00112233445566778899aabbccddeeff",
		InitCompleted:       true,
		InitBytes:           720,
		TotalSegments:       4,
		CompletedBitmap:     []byte{0x0f},
		MergedSegments:      4,
		MergedBytes:         2048,
		VerifiedBytes:       2048,
		SpillPath:           "/tmp/x.spill",
		SpillFileSize:       512,
		SpillEntries:        []HLSSpillEntry{{SegmentIndex: 2, Offset: 0, Length: 512}},
		Keys:                []HLSKeyInfo{{Method: "AES-128", URI: "key.bin"}},
		Segments:            []HLSSegmentState{{Index: 0, URI: "s0.m4s"}},
	}
	rebased := full.RebaseManifestURL(newURL)
	if rebased == nil {
		t.Fatal("rebase returned nil")
	}
	if rebased.ManifestURL != newURL {
		t.Fatalf("ManifestURL = %q, want %q", rebased.ManifestURL, newURL)
	}
	if !rebased.RecordEligible {
		t.Fatal("RecordEligible must survive — the seed is still durable")
	}
	// Every derived field must be invalidated.
	if rebased.MediaPlaylistURL != "" || rebased.SegmentRefURL != "" ||
		rebased.ManifestFingerprint != "" || rebased.ContainerFormat != "" ||
		rebased.EstimatedBytes != 0 ||
		rebased.InitSegmentURI != "" || rebased.InitKeyIndex != 0 ||
		rebased.InitIVHex != "" || rebased.InitCompleted || rebased.InitBytes != 0 ||
		rebased.TotalSegments != 0 || rebased.CompletedBitmap != nil ||
		rebased.MergedSegments != 0 || rebased.MergedBytes != 0 || rebased.VerifiedBytes != 0 ||
		rebased.SpillPath != "" || rebased.SpillFileSize != 0 || rebased.SpillEntries != nil ||
		rebased.Keys != nil || rebased.Segments != nil {
		t.Fatalf("rebase left derived state behind: %+v", rebased)
	}
	// The receiver must be untouched — rebasing is non-destructive.
	if full.ManifestURL != oldURL || full.TotalSegments != 4 || full.MergedBytes != 2048 ||
		len(full.Segments) != 1 || len(full.Keys) != 1 {
		t.Fatalf("rebase mutated the source state: %+v", full)
	}
	// The result is independent: mutating it cannot reach the source.
	rebased.Segments = append(rebased.Segments, HLSSegmentState{Index: 9})
	if len(full.Segments) != 1 {
		t.Fatal("rebased state shares storage with the source")
	}
}

func TestHLSRebase_NilReceiverYieldsNil(t *testing.T) {
	var nilState *HLSResumeState
	if got := nilState.RebaseManifestURL("https://x/y.m3u8"); got != nil {
		t.Fatalf("nil rebase = %+v, want nil", got)
	}
}

func TestHLSResumeState_CloneDeepCopiesSlices(t *testing.T) {
	src := &HLSResumeState{
		ManifestURL:     "https://x/m.m3u8",
		CompletedBitmap: []byte{0x01, 0x02},
		SpillEntries:    []HLSSpillEntry{{SegmentIndex: 1}},
		Keys:            []HLSKeyInfo{{URI: "k"}},
		Segments:        []HLSSegmentState{{Index: 1}},
	}
	cp := src.Clone()
	cp.CompletedBitmap[0] = 0xff
	cp.SpillEntries[0].SegmentIndex = 42
	cp.Keys[0].URI = "mutated"
	cp.Segments[0].Index = 42
	if src.CompletedBitmap[0] != 0x01 || src.SpillEntries[0].SegmentIndex != 1 ||
		src.Keys[0].URI != "k" || src.Segments[0].Index != 1 {
		t.Fatal("clone shares slice storage with the source")
	}
	var nilState *HLSResumeState
	if nilState.Clone() != nil {
		t.Fatal("nil clone must return nil")
	}
}
