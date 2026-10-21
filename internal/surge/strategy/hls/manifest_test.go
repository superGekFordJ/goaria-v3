package hls_test

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"testing"

	"goaria-v3/internal/surge/strategy/hls"
	"goaria-v3/internal/surge/types"
)

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) = %v", raw, err)
	}
	return u
}

func parseMaster(t *testing.T, content, base string) *hls.MasterPlaylist {
	t.Helper()
	kind, master, media, err := hls.ParsePlaylist(content, mustParseURL(t, base))
	if err != nil {
		t.Fatalf("ParsePlaylist() = %v", err)
	}
	if kind != hls.PlaylistKindMaster || master == nil || media != nil {
		t.Fatalf("kind=%v master=%v media=%v", kind, master != nil, media != nil)
	}
	return master
}

func parseMedia(t *testing.T, content, base string) *hls.MediaPlaylist {
	t.Helper()
	kind, master, media, err := hls.ParsePlaylist(content, mustParseURL(t, base))
	if err != nil {
		t.Fatalf("ParsePlaylist() = %v", err)
	}
	if kind != hls.PlaylistKindMedia || media == nil || master != nil {
		t.Fatalf("kind=%v master=%v media=%v", kind, master != nil, media != nil)
	}
	return media
}

func parseErr(t *testing.T, content, base string) error {
	t.Helper()
	_, _, _, err := hls.ParsePlaylist(content, mustParseURL(t, base))
	if err == nil {
		t.Fatal("ParsePlaylist() = nil error, want failure")
	}
	return err
}

func TestParseMasterPlaylist_VariantSelectionAndDemuxedGate(t *testing.T) {
	base := "https://cdn.example.com/hls/master.m3u8"

	t.Run("MultiVariantSkipsIFrameAndAudioOnly", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720
hd720.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
hd1080.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=3840x2160
uhd.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=9000000,URI="iframe.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=9999999,CODECS="mp4a.40.2"
audio_only.m3u8
`
		master := parseMaster(t, doc, base)
		if len(master.Variants) != 3 {
			t.Fatalf("len(Variants) = %d, want 3 (iframe + audio-only excluded)", len(master.Variants))
		}
		best, err := hls.SelectBestVariant(master)
		if err != nil {
			t.Fatalf("SelectBestVariant() = %v", err)
		}
		if best.URI != "https://cdn.example.com/hls/uhd.m3u8" {
			t.Fatalf("best.URI = %q, want uhd.m3u8 (resolution breaks bandwidth tie)", best.URI)
		}
	})

	t.Run("SkipsDemuxedVariantPicksMuxed", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-ext",URI="audio.m3u8",NAME="English"
#EXT-X-STREAM-INF:BANDWIDTH=8000000,RESOLUTION=3840x2160,AUDIO="audio-ext"
uhd.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
hd1080.m3u8
`
		master := parseMaster(t, doc, base)
		best, err := hls.SelectBestVariant(master)
		if err != nil {
			t.Fatalf("SelectBestVariant() = %v", err)
		}
		if best.URI != "https://cdn.example.com/hls/hd1080.m3u8" {
			t.Fatalf("best.URI = %q, want hd1080.m3u8 (4K demuxed variant skipped)", best.URI)
		}
	})

	t.Run("EmbeddedAudioGroupKeepsVariantMuxed", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-mux",NAME="English",DEFAULT=YES
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-mixed",NAME="int",DEFAULT=YES
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio-mixed",NAME="ext",URI="alt.m3u8",DEFAULT=NO
#EXT-X-STREAM-INF:BANDWIDTH=8000000,RESOLUTION=3840x2160,AUDIO="audio-mux"
uhd.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=9000000,RESOLUTION=1920x1080,AUDIO="audio-mixed"
hd.m3u8
`
		master := parseMaster(t, doc, base)
		best, err := hls.SelectBestVariant(master)
		if err != nil {
			t.Fatalf("SelectBestVariant() = %v", err)
		}
		if best.URI != "https://cdn.example.com/hls/hd.m3u8" {
			t.Fatalf("best.URI = %q, want hd.m3u8 (both variants muxed, bandwidth wins)", best.URI)
		}
	})

	t.Run("AllDemuxedReturnsSentinel", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",URI="a1.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,AUDIO="a"
v1.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720,AUDIO="a"
v2.m3u8
`
		master := parseMaster(t, doc, base)
		if _, err := hls.SelectBestVariant(master); !errors.Is(err, types.ErrHLSDemuxedAudioNotSupported) {
			t.Fatalf("SelectBestVariant() = %v, want ErrHLSDemuxedAudioNotSupported", err)
		}
	})

	t.Run("SessionKeyDRMGate", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI="sk.bin"
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
v.m3u8
`
		if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSDRMNotSupported) {
			t.Fatalf("err = %v, want ErrHLSDRMNotSupported", err)
		}
	})

	t.Run("MixedMasterMediaTagsRejected", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
v.m3u8
#EXTINF:6.0,
seg0.ts
`
		if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSInvalidManifest) {
			t.Fatalf("err = %v, want ErrHLSInvalidManifest", err)
		}
	})

	t.Run("MissingVariantURIRejected", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
`
		if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSInvalidManifest) {
			t.Fatalf("err = %v, want ErrHLSInvalidManifest", err)
		}
	})

	t.Run("TagBetweenStreamInfAndURIRejected", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",URI="a.m3u8"
v.m3u8
`
		if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSInvalidManifest) {
			t.Fatalf("err = %v, want ErrHLSInvalidManifest", err)
		}
	})

	t.Run("SelectionOrderTieBreakDeterministic", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
first.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
second.m3u8
`
		master := parseMaster(t, doc, base)
		best, err := hls.SelectBestVariant(master)
		if err != nil {
			t.Fatalf("SelectBestVariant() = %v", err)
		}
		if best.URI != "https://cdn.example.com/hls/first.m3u8" {
			t.Fatalf("best.URI = %q, want first-declared on full tie", best.URI)
		}
	})

	t.Run("DanglingAudioGroupExcluded", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=8000000,RESOLUTION=3840x2160,AUDIO="ghost"
uhd.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080
hd.m3u8
`
		master := parseMaster(t, doc, base)
		best, err := hls.SelectBestVariant(master)
		if err != nil {
			t.Fatalf("SelectBestVariant() = %v", err)
		}
		// The variant whose AUDIO group is never declared is excluded like
		// a demuxed one — selecting it would produce silent video.
		if best.URI != "https://cdn.example.com/hls/hd.m3u8" {
			t.Fatalf("best.URI = %q, want hd.m3u8 (dangling AUDIO group excluded)", best.URI)
		}
	})

	t.Run("EmptyCodecPrefixesKeepVariant", func(t *testing.T) {
		// CODECS="," yields only empty prefixes — nothing declares an
		// audio-only profile, so the variant must not be excluded.
		doc := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,CODECS=","
v.m3u8
`
		master := parseMaster(t, doc, base)
		if len(master.Variants) != 1 {
			t.Fatalf("len(Variants) = %d, want 1 (empty codec prefixes are not audio-only)", len(master.Variants))
		}
	})

	t.Run("AllDanglingAudioReturnsSentinel", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,AUDIO="ghost"
v1.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720,AUDIO="ghost2"
v2.m3u8
`
		master := parseMaster(t, doc, base)
		if _, err := hls.SelectBestVariant(master); !errors.Is(err, types.ErrHLSDemuxedAudioNotSupported) {
			t.Fatalf("SelectBestVariant() = %v, want ErrHLSDemuxedAudioNotSupported", err)
		}
	})
}

func TestParseMediaPlaylist_VODAndFormatGates(t *testing.T) {
	base := "https://cdn.example.com/v/index.m3u8"

	t.Run("TSVOD", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:100
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:6.006,
seg0.ts
#EXTINF:6.0,
seg1.ts
#EXT-X-ENDLIST
`
		media := parseMedia(t, doc, base)
		if media.ContainerFormat != hls.ContainerTS {
			t.Fatalf("ContainerFormat = %q, want ts", media.ContainerFormat)
		}
		if len(media.Segments) != 2 {
			t.Fatalf("len(Segments) = %d, want 2", len(media.Segments))
		}
		if media.Segments[0].Sequence != 100 || media.Segments[1].Sequence != 101 {
			t.Fatalf("Sequences = %d,%d, want 100,101",
				media.Segments[0].Sequence, media.Segments[1].Sequence)
		}
		if media.Segments[0].DurationMicros != 6006000 {
			t.Fatalf("DurationMicros = %d, want 6006000", media.Segments[0].DurationMicros)
		}
		if media.TotalDurationSec != 12.006 {
			t.Fatalf("TotalDurationSec = %v, want 12.006", media.TotalDurationSec)
		}
		if media.SegmentRefURL != media.Segments[0].URI {
			t.Fatalf("SegmentRefURL = %q", media.SegmentRefURL)
		}
		if len(media.ManifestFingerprint) != 64 {
			t.Fatalf("fingerprint len = %d, want 64", len(media.ManifestFingerprint))
		}
	})

	t.Run("FMP4VODWithMapByteRange", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@64"
#EXTINF:6.0,
seg0.mp4
#EXTINF:6.0,
seg1.mp4
#EXT-X-ENDLIST
`
		media := parseMedia(t, doc, base)
		if media.ContainerFormat != hls.ContainerFMP4 {
			t.Fatalf("ContainerFormat = %q, want fmp4", media.ContainerFormat)
		}
		if media.InitMap == nil {
			t.Fatal("InitMap = nil")
		}
		if media.InitMap.ByteOffset != 64 || media.InitMap.ByteLength != 720 {
			t.Fatalf("InitMap = %+v, want offset=64 length=720", media.InitMap)
		}
		if media.InitMap.URI != "https://cdn.example.com/v/init.mp4" {
			t.Fatalf("InitMap.URI = %q", media.InitMap.URI)
		}
	})

	t.Run("MissingEndlistRejected", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXTINF:6.0,
seg0.ts
`
		if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSLiveNotSupported) {
			t.Fatalf("err = %v, want ErrHLSLiveNotSupported", err)
		}
	})

	t.Run("EventTypeWithoutEndlistRejected", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-PLAYLIST-TYPE:EVENT
#EXT-X-TARGETDURATION:6
#EXTINF:6.0,
seg0.ts
`
		if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSLiveNotSupported) {
			t.Fatalf("err = %v, want ErrHLSLiveNotSupported", err)
		}
	})

	t.Run("DRMGates", func(t *testing.T) {
		cases := map[string]string{
			"SampleAES": `#EXTM3U
#EXT-X-KEY:METHOD=SAMPLE-AES,URI="k.bin"
#EXTINF:6.0,
seg0.ts
#EXT-X-ENDLIST
`,
			"WidevineKeyformat": `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="k.bin",KEYFORMAT="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"
#EXTINF:6.0,
seg0.ts
#EXT-X-ENDLIST
`,
		}
		for name, doc := range cases {
			if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSDRMNotSupported) {
				t.Fatalf("%s: err = %v, want ErrHLSDRMNotSupported", name, err)
			}
		}
	})

	t.Run("FMP4MapAfterDiscontinuityRejected", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.0,
seg0.mp4
#EXT-X-DISCONTINUITY
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.0,
seg1.mp4
#EXT-X-ENDLIST
`
		if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSDiscontinuityMapNotSupported) {
			t.Fatalf("err = %v, want ErrHLSDiscontinuityMapNotSupported", err)
		}
	})

	t.Run("FMP4DifferentMapWithoutDiscontinuityRejected", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.0,
seg0.mp4
#EXT-X-MAP:URI="init.mp4",BYTERANGE="10@0"
#EXTINF:6.0,
seg1.mp4
#EXT-X-ENDLIST
`
		if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSDiscontinuityMapNotSupported) {
			t.Fatalf("err = %v, want ErrHLSDiscontinuityMapNotSupported", err)
		}
	})

	t.Run("FMP4IdenticalMapRedeclaredAllowed", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@64"
#EXTINF:6.0,
seg0.mp4
#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@64"
#EXTINF:6.0,
seg1.mp4
#EXT-X-ENDLIST
`
		media := parseMedia(t, doc, base)
		if len(media.Segments) != 2 {
			t.Fatalf("len(Segments) = %d, want 2", len(media.Segments))
		}
	})

	t.Run("TSDiscontinuityAllowed", func(t *testing.T) {
		doc := `#EXTM3U
#EXTINF:6.0,
seg0.ts
#EXT-X-DISCONTINUITY
#EXTINF:6.0,
seg1.ts
#EXT-X-ENDLIST
`
		media := parseMedia(t, doc, base)
		if len(media.Segments) != 2 || media.ContainerFormat != hls.ContainerTS {
			t.Fatalf("segments=%d format=%q", len(media.Segments), media.ContainerFormat)
		}
	})

	t.Run("StructuralFailures", func(t *testing.T) {
		cases := map[string]string{
			"MissingEXTM3U":        "#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			"EmptyContent":         "",
			"NoTagsAtAll":          "#EXTM3U\n",
			"ZeroSegments":         "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-ENDLIST\n",
			"DanglingEXTINF":       "#EXTM3U\n#EXTINF:6.0,\nseg0.ts\n#EXTINF:6.0,\n#EXT-X-ENDLIST\n",
			"DanglingByteRange":    "#EXTM3U\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-BYTERANGE:100@0\n#EXT-X-ENDLIST\n",
			"SegmentWithoutEXTINF": "#EXTM3U\nseg0.ts\n#EXT-X-ENDLIST\n",
			"BadEXTINF":            "#EXTM3U\n#EXTINF:abc,\nseg0.ts\n#EXT-X-ENDLIST\n",
		}
		for name, doc := range cases {
			if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSInvalidManifest) {
				t.Fatalf("%s: err = %v, want ErrHLSInvalidManifest", name, err)
			}
		}
	})
}

func TestParseMediaPlaylist_ByteRangeAndKeyRotation(t *testing.T) {
	base := "https://cdn.example.com/v/index.m3u8"

	t.Run("ByteRangeOffsetAccumulation", func(t *testing.T) {
		doc := `#EXTM3U
#EXTINF:6.0,
#EXT-X-BYTERANGE:1024@512
main.ts
#EXTINF:6.0,
#EXT-X-BYTERANGE:2048
main.ts
#EXTINF:6.0,
#EXT-X-BYTERANGE:128@0
other.ts
#EXT-X-ENDLIST
`
		media := parseMedia(t, doc, base)
		if len(media.Segments) != 3 {
			t.Fatalf("len(Segments) = %d, want 3", len(media.Segments))
		}
		if media.Segments[0].ByteOffset != 512 || media.Segments[0].ByteLength != 1024 {
			t.Fatalf("seg0 = %+v", media.Segments[0])
		}
		if media.Segments[1].ByteOffset != 1536 || media.Segments[1].ByteLength != 2048 {
			t.Fatalf("seg1 = %+v, want offset=1536 length=2048", media.Segments[1])
		}
		if media.Segments[2].ByteOffset != 0 || media.Segments[2].ByteLength != 128 {
			t.Fatalf("seg2 = %+v", media.Segments[2])
		}
	})

	t.Run("OmittedOffsetRejected", func(t *testing.T) {
		cases := map[string]string{
			"FirstSegment": `#EXTM3U
#EXTINF:6.0,
#EXT-X-BYTERANGE:100
main.ts
#EXT-X-ENDLIST
`,
			"DifferentURI": `#EXTM3U
#EXTINF:6.0,
#EXT-X-BYTERANGE:100@0
a.ts
#EXTINF:6.0,
#EXT-X-BYTERANGE:50
b.ts
#EXT-X-ENDLIST
`,
			"PrevNotByteRanged": `#EXTM3U
#EXTINF:6.0,
a.ts
#EXTINF:6.0,
#EXT-X-BYTERANGE:50
a.ts
#EXT-X-ENDLIST
`,
		}
		for name, doc := range cases {
			if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSInvalidManifest) {
				t.Fatalf("%s: err = %v, want ErrHLSInvalidManifest", name, err)
			}
		}
	})

	t.Run("KeyRotationAndDedup", func(t *testing.T) {
		doc := `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="k1.bin",IV=0x1
#EXTINF:6.0,
s1.ts
#EXT-X-KEY:METHOD=AES-128,URI="k2.bin"
#EXTINF:6.0,
s2.ts
#EXT-X-KEY:METHOD=NONE
#EXTINF:6.0,
s3.ts
#EXT-X-KEY:METHOD=AES-128,URI="k1.bin",IV=0x1
#EXTINF:6.0,
s4.ts
#EXT-X-ENDLIST
`
		media := parseMedia(t, doc, base)
		if len(media.Keys) != 2 {
			t.Fatalf("len(Keys) = %d, want 2", len(media.Keys))
		}
		wantIdx := []int{1, 2, 0, 1}
		for i, seg := range media.Segments {
			if seg.KeyIndex != wantIdx[i] {
				t.Fatalf("seg[%d].KeyIndex = %d, want %d", i, seg.KeyIndex, wantIdx[i])
			}
		}
		wantIV := "00000000000000000000000000000001"
		if media.Keys[0].IVHex != wantIV || media.Segments[0].IVHex != wantIV {
			t.Fatalf("normalized IV = %q / %q, want %q", media.Keys[0].IVHex, media.Segments[0].IVHex, wantIV)
		}
		if media.Segments[2].IVHex != "" || media.Segments[1].IVHex != "" {
			t.Fatalf("IVHex = %q / %q", media.Segments[1].IVHex, media.Segments[2].IVHex)
		}
	})

	t.Run("KeyMissingURIOrMethodRejected", func(t *testing.T) {
		cases := map[string]string{
			"NoURI": `#EXTM3U
#EXT-X-KEY:METHOD=AES-128
#EXTINF:6.0,
seg0.ts
#EXT-X-ENDLIST
`,
			"NoMethod": `#EXTM3U
#EXT-X-KEY:URI="k.bin"
#EXTINF:6.0,
seg0.ts
#EXT-X-ENDLIST
`,
			"BadIV": `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="k.bin",IV=0xZZ
#EXTINF:6.0,
seg0.ts
#EXT-X-ENDLIST
`,
		}
		for name, doc := range cases {
			if err := parseErr(t, doc, base); !errors.Is(err, types.ErrHLSInvalidManifest) {
				t.Fatalf("%s: err = %v, want ErrHLSInvalidManifest", name, err)
			}
		}
	})
}

func TestParsePlaylist_RelativeURIResolution(t *testing.T) {
	base := "https://cdn.example.com/nested/dir/master.m3u8"
	doc := `#EXTM3U
#EXTINF:6.0,
../seg/seg0.ts
#EXTINF:6.0,
/hls/seg1.ts
#EXTINF:6.0,
seg2.ts
#EXTINF:6.0,
//cdn2.example.com/seg3.ts
#EXT-X-ENDLIST
`
	media := parseMedia(t, doc, base)
	want := []string{
		"https://cdn.example.com/nested/seg/seg0.ts",
		"https://cdn.example.com/hls/seg1.ts",
		"https://cdn.example.com/nested/dir/seg2.ts",
		"https://cdn2.example.com/seg3.ts",
	}
	for i, seg := range media.Segments {
		if seg.URI != want[i] {
			t.Fatalf("seg[%d].URI = %q, want %q", i, seg.URI, want[i])
		}
	}
}

func TestComputeManifestFingerprint_QueryStripAndDeterminism(t *testing.T) {
	docA := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="https://edge1.cdn.com/v/init.mp4?sig=1"
#EXT-X-KEY:METHOD=AES-128,URI="https://edge1.cdn.com/v/k.bin?t=a#x"
#EXTINF:6.006,
seg0.ts?token=a#frag1
#EXTINF:5.5,
seg1.ts?token=a
#EXT-X-ENDLIST
`
	docB := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="https://edge2.cdn.com/v/init.mp4?sig=2#frag"
#EXT-X-KEY:METHOD=AES-128,URI="https://edge2.cdn.com/v/k.bin?t=b#y"
#EXTINF:6.006,
seg0.ts?token=b#frag2
#EXTINF:5.5,
seg1.ts?token=b&exp=9
#EXT-X-ENDLIST
`
	mediaA := parseMedia(t, docA, "https://edge1.cdn.com/v/m.m3u8")
	mediaB := parseMedia(t, docB, "https://edge2.cdn.com/v/m.m3u8")
	if mediaA.ManifestFingerprint != mediaB.ManifestFingerprint {
		t.Fatal("fingerprints differ for structure-identical manifests with rotated hosts/signatures")
	}

	t.Run("Mutations", func(t *testing.T) {
		mutations := map[string]string{
			"DurationMicros": `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="https://edge1.cdn.com/v/init.mp4?sig=1"
#EXT-X-KEY:METHOD=AES-128,URI="https://edge1.cdn.com/v/k.bin?t=a#x"
#EXTINF:7.006,
seg0.ts?token=a#frag1
#EXTINF:5.5,
seg1.ts?token=a
#EXT-X-ENDLIST
`,
			"MediaSequenceBase": `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:11
#EXT-X-MAP:URI="https://edge1.cdn.com/v/init.mp4?sig=1"
#EXT-X-KEY:METHOD=AES-128,URI="https://edge1.cdn.com/v/k.bin?t=a#x"
#EXTINF:6.006,
seg0.ts?token=a#frag1
#EXTINF:5.5,
seg1.ts?token=a
#EXT-X-ENDLIST
`,
			"SegmentPath": `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="https://edge1.cdn.com/v/init.mp4?sig=1"
#EXT-X-KEY:METHOD=AES-128,URI="https://edge1.cdn.com/v/k.bin?t=a#x"
#EXTINF:6.006,
segX.ts?token=a#frag1
#EXTINF:5.5,
seg1.ts?token=a
#EXT-X-ENDLIST
`,
			"InitByteRange": `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="https://edge1.cdn.com/v/init.mp4?sig=1",BYTERANGE="99@0"
#EXT-X-KEY:METHOD=AES-128,URI="https://edge1.cdn.com/v/k.bin?t=a#x"
#EXTINF:6.006,
seg0.ts?token=a#frag1
#EXTINF:5.5,
seg1.ts?token=a
#EXT-X-ENDLIST
`,
			"KeyMethod": `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="https://edge1.cdn.com/v/init.mp4?sig=1"
#EXT-X-KEY:METHOD=AES-128,URI="https://edge1.cdn.com/v/k.bin?t=a#x"
#EXTINF:6.006,
seg0.ts?token=a#frag1
#EXT-X-KEY:METHOD=NONE
#EXTINF:5.5,
seg1.ts?token=a
#EXT-X-ENDLIST
`,
			"ByteLength": `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="https://edge1.cdn.com/v/init.mp4?sig=1"
#EXT-X-KEY:METHOD=AES-128,URI="https://edge1.cdn.com/v/k.bin?t=a#x"
#EXTINF:6.006,
#EXT-X-BYTERANGE:50@0
seg0.ts?token=a#frag1
#EXTINF:5.5,
seg1.ts?token=a
#EXT-X-ENDLIST
`,
		}
		for name, doc := range mutations {
			media := parseMedia(t, doc, "https://edge1.cdn.com/v/m.m3u8")
			if media.ManifestFingerprint == mediaA.ManifestFingerprint {
				t.Fatalf("%s mutation kept fingerprint %q", name, media.ManifestFingerprint)
			}
		}
	})
}

func TestEstimatedBytesAndRecordEligible_Boundary(t *testing.T) {
	if got := hls.ComputeEstimatedBytes(8_000_000, 12.5); got != 12_500_000 {
		t.Fatalf("ComputeEstimatedBytes = %d, want 12500000", got)
	}
	if got := hls.ComputeEstimatedBytes(0, 100); got != 0 {
		t.Fatalf("zero-bandwidth estimate = %d, want 0", got)
	}
	if got := hls.ComputeEstimatedBytes(1000, 0); got != 0 {
		t.Fatalf("zero-duration estimate = %d, want 0", got)
	}
	if !hls.ComputeRecordEligible(0) {
		t.Fatal("RecordEligible(0) = false, want true (unknown size)")
	}
	if hls.ComputeRecordEligible(types.RecordEligibleMinFileSize) {
		t.Fatal("RecordEligible(50MiB) = true, want false (strictly-greater rule)")
	}
	if !hls.ComputeRecordEligible(types.RecordEligibleMinFileSize + 1) {
		t.Fatal("RecordEligible(50MiB+1) = false, want true")
	}
}

func TestParseMediaPlaylist_StrictStructureGates(t *testing.T) {
	base := "https://cdn.example.com/v/index.m3u8"
	cases := map[string]struct {
		doc  string
		want error
	}{
		"IFramesOnly": {
			"#EXTM3U\n#EXT-X-I-FRAMES-ONLY\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSIFramesOnlyNotSupported,
		},
		"MediaSeqAfterFirstSegment": {
			"#EXTM3U\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:6.0,\nseg1.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"MediaSeqRepeated": {
			"#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"DiscontSeqAfterDiscontinuity": {
			"#EXTM3U\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-DISCONTINUITY\n#EXT-X-DISCONTINUITY-SEQUENCE:2\n#EXTINF:6.0,\nseg1.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"DiscontSeqRepeated": {
			"#EXTM3U\n#EXT-X-DISCONTINUITY-SEQUENCE:0\n#EXT-X-DISCONTINUITY-SEQUENCE:1\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"ConsecutiveEXTINF": {
			"#EXTM3U\n#EXTINF:6.0,\n#EXTINF:5.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"ConsecutiveByteRange": {
			"#EXTM3U\n#EXTINF:6.0,\n#EXT-X-BYTERANGE:10@0\n#EXT-X-BYTERANGE:20@10\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"SegmentAfterEndlist": {
			"#EXTM3U\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n#EXTINF:6.0,\nseg1.ts\n",
			types.ErrHLSInvalidManifest,
		},
		"SpaceInSegmentURI": {
			"#EXTM3U\n#EXTINF:6.0,\nseg 0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"SpaceInKeyURIAttr": {
			"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k ey.bin\"\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"ByteRangeChainOffsetOverflow": {
			"#EXTM3U\n#EXTINF:6.0,\n#EXT-X-BYTERANGE:10@9223372036854775800\na.ts\n#EXTINF:6.0,\n#EXT-X-BYTERANGE:5\na.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"MediaSequenceBaseOverflow": {
			"#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:9223372036854775807\n#EXTINF:6.0,\na.ts\n#EXTINF:6.0,\nb.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
	}
	for name, tc := range cases {
		if err := parseErr(t, tc.doc, base); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestParseMediaPlaylist_UnsupportedExtensionTags(t *testing.T) {
	base := "https://cdn.example.com/v/index.m3u8"
	cases := map[string]struct {
		doc  string
		want error
	}{
		// SKIP+ENDLIST is the truncation trap: the playlist claims
		// completeness while omitting earlier segments from the list.
		"SkipOmitsEarlierSegments": {
			"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-SKIP:SKIPPED-SEGMENTS=10\n" +
				"#EXTINF:6.0,\nseg10.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"GapSegmentUnavailable": {
			"#EXTM3U\n#EXTINF:6.0,\n#EXT-X-GAP\nseg0.ts\n#EXTINF:6.0,\nseg1.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"ServerControl": {
			"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-SERVER-CONTROL:CAN-SKIP-UNTIL=36.0\n" +
				"#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSLiveNotSupported,
		},
		"PartInf": {
			"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-PART-INF:PART-TARGET=0.5\n" +
				"#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSLiveNotSupported,
		},
		"PartialSegment": {
			"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-PART:DURATION=0.5,URI=\"p0.mp4\"\n" +
				"#EXTINF:6.0,\nseg0.mp4\n#EXT-X-ENDLIST\n",
			types.ErrHLSLiveNotSupported,
		},
		"PreloadHint": {
			"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg0.ts\n" +
				"#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"next.mp4\"\n#EXT-X-ENDLIST\n",
			types.ErrHLSLiveNotSupported,
		},
		"RenditionReport": {
			"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-RENDITION-REPORT:URI=\"r.m3u8\"\n" +
				"#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSLiveNotSupported,
		},
		// EXT-X-DEFINE is legal in both playlist kinds; either way the
		// {name} expansion is unsupported and the document is refused.
		"DefineVariableMedia": {
			"#EXTM3U\n#EXT-X-DEFINE:NAME=\"ver\",VALUE=\"1\"\n" +
				"#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"DefineVariableMaster": {
			"#EXTM3U\n#EXT-X-DEFINE:QUERYPARAM=\"token\"\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=1000000\nv.m3u8\n",
			types.ErrHLSInvalidManifest,
		},
		// An EXT-X-KEY in force when EXT-X-MAP is declared makes the init
		// section encrypted — unrepresentable, so it is refused outright.
		"KeyBeforeMapEncryptedInit": {
			"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\"\n#EXT-X-MAP:URI=\"init.mp4\"\n" +
				"#EXTINF:6.0,\nseg0.mp4\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
		"MapRedeclaredUnderKey": {
			"#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:6.0,\nseg0.mp4\n" +
				"#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\"\n#EXT-X-MAP:URI=\"init.mp4\"\n" +
				"#EXTINF:6.0,\nseg1.mp4\n#EXT-X-ENDLIST\n",
			types.ErrHLSInvalidManifest,
		},
	}
	for name, tc := range cases {
		if err := parseErr(t, tc.doc, base); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestParseMediaPlaylist_MapBeforeKeyStaysPlaintext(t *testing.T) {
	doc := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\"\n" +
		"#EXTINF:6.0,\nseg0.mp4\n#EXT-X-ENDLIST\n"
	media := parseMedia(t, doc, "https://cdn.example.com/v/index.m3u8")
	if media.InitMap == nil || media.ContainerFormat != hls.ContainerFMP4 {
		t.Fatalf("InitMap=%v format=%q", media.InitMap, media.ContainerFormat)
	}
	if len(media.Keys) != 1 || media.Segments[0].KeyIndex != 1 {
		t.Fatalf("Keys=%d seg.KeyIndex=%d", len(media.Keys), media.Segments[0].KeyIndex)
	}
}

func TestParseMediaPlaylist_KeyDedupBounds(t *testing.T) {
	base := "https://cdn.example.com/v/index.m3u8"

	t.Run("UniqueKeyCapRejected", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n")
		for i := range 4097 {
			fmt.Fprintf(&b, "#EXT-X-KEY:METHOD=AES-128,URI=\"k%d.bin\"\n", i)
		}
		b.WriteString("#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n")
		if err := parseErr(t, b.String(), base); !errors.Is(err, types.ErrHLSInvalidManifest) {
			t.Fatalf("err = %v, want ErrHLSInvalidManifest", err)
		}
	})

	t.Run("RotationDedupWithinCap", func(t *testing.T) {
		// 3000 unique keys plus a re-declaration of the first one: the
		// O(1) dedup map keeps this linear and the last segment reuses
		// the 1-based index of k0.bin.
		var b strings.Builder
		b.WriteString("#EXTM3U\n")
		for i := range 3000 {
			fmt.Fprintf(&b, "#EXT-X-KEY:METHOD=AES-128,URI=\"k%d.bin\"\n#EXTINF:0.5,\ns%d.ts\n", i, i)
		}
		b.WriteString("#EXT-X-KEY:METHOD=AES-128,URI=\"k0.bin\"\n#EXTINF:0.5,\nlast.ts\n#EXT-X-ENDLIST\n")
		media := parseMedia(t, b.String(), base)
		if len(media.Keys) != 3000 {
			t.Fatalf("len(Keys) = %d, want 3000", len(media.Keys))
		}
		if got := media.Segments[len(media.Segments)-1].KeyIndex; got != 1 {
			t.Fatalf("last segment KeyIndex = %d, want 1 (deduped k0.bin)", got)
		}
	})
}

func TestParseMediaPlaylist_TotalDurationSaturates(t *testing.T) {
	doc := "#EXTM3U\n#EXTINF:9e12,\na.ts\n#EXTINF:9e12,\nb.ts\n#EXT-X-ENDLIST\n"
	media := parseMedia(t, doc, "https://cdn.example.com/v/index.m3u8")
	if media.TotalDurationMicros != math.MaxInt64 {
		t.Fatalf("TotalDurationMicros = %d, want saturated MaxInt64", media.TotalDurationMicros)
	}
	if media.TotalDurationSec <= 0 {
		t.Fatalf("TotalDurationSec = %v, want positive", media.TotalDurationSec)
	}
}

func TestComputeManifestFingerprint_EncodedPathBoundaries(t *testing.T) {
	mk := func(uri string) []types.HLSSegmentState {
		return []types.HLSSegmentState{{Index: 0, Sequence: 0, URI: uri, DurationMicros: 6000000}}
	}
	// %2F is a data character inside one path segment, not a separator:
	// it must not collapse with a literal "/".
	fpEncoded := hls.ComputeManifestFingerprint(hls.ContainerTS, 0, nil,
		mk("https://cdn.example.com/v/a%2Fb.ts"), nil)
	fpLiteral := hls.ComputeManifestFingerprint(hls.ContainerTS, 0, nil,
		mk("https://cdn.example.com/v/a/b.ts"), nil)
	if fpEncoded == fpLiteral {
		t.Fatal("encoded %2F collapsed into a path separator — distinct URIs alias one fingerprint")
	}
	// A %0A payload inside a segment URI must not reframe serialization:
	// the resulting path stays one quoted field and can never alias a
	// multi-record document.
	fpInjected := hls.ComputeManifestFingerprint(hls.ContainerTS, 0, nil,
		mk("https://cdn.example.com/v/a%0Aseg%3D1%7Cseq%3D9%7Cdur%3D6000000%7Coff%3D0%7Clen%3D0%7Ckey%3DNONE%7Cpath%3D%2Fb.ts"), nil)
	fpPlain := hls.ComputeManifestFingerprint(hls.ContainerTS, 0, nil,
		mk("https://cdn.example.com/v/a"), nil)
	if fpInjected == fpPlain {
		t.Fatal("separator injection aliased the clean-path fingerprint")
	}
	if len(fpInjected) != 64 {
		t.Fatalf("fingerprint len = %d, want 64", len(fpInjected))
	}
}

func TestComputeEstimatedBytes_OverflowClamp(t *testing.T) {
	if got := hls.ComputeEstimatedBytes(math.MaxInt64, 9e12); got != math.MaxInt64 {
		t.Fatalf("overflow estimate = %d, want MaxInt64 clamp", got)
	}
	if got := hls.ComputeEstimatedBytes(math.MaxInt64, 1.0); got != 1<<60 {
		t.Fatalf("in-range estimate = %d, want %d", got, int64(1<<60))
	}
	if got := hls.ComputeEstimatedBytes(1000, math.NaN()); got != 0 {
		t.Fatalf("NaN-duration estimate = %d, want 0", got)
	}
	if got := hls.ComputeEstimatedBytes(math.MaxInt64, math.Inf(1)); got != math.MaxInt64 {
		t.Fatalf("Inf-duration estimate = %d, want MaxInt64 clamp", got)
	}
}
