package hls_test

import (
	"testing"
	"time"

	"goaria-v3/internal/surge/strategy/hls"
)

func TestContainerFormatExtension(t *testing.T) {
	cases := []struct{ in, want string }{
		{"fmp4", ".mp4"},
		{"FMP4", ".mp4"},
		{"mp4", ".mp4"},
		{" ts ", ".ts"},
		{"", ".ts"},
	}
	for _, tc := range cases {
		if got := hls.ContainerFormatExtension(tc.in); got != tc.want {
			t.Fatalf("ContainerFormatExtension(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsGenericHLSStem(t *testing.T) {
	generic := []string{
		"", "index", "playlist", "playlist-1080p",
		"master", "master_v2", "chunklist", "chunklist_b2500000", "manifest",
		"stream", "stream-0", "video", "video_1", "hls", "hls_hd", "media", "media_hd",
		"INDEX", "Master-9",
	}
	for _, s := range generic {
		if !hls.IsGenericHLSStem(s) {
			t.Fatalf("IsGenericHLSStem(%q) = false, want true", s)
		}
	}
	specific := []string{"sintel_4k", "ep01", "big_buck_bunny", "con", "indexical", "streams"}
	for _, s := range specific {
		if hls.IsGenericHLSStem(s) {
			t.Fatalf("IsGenericHLSStem(%q) = true, want false", s)
		}
	}
}

func TestResolveHLSOutputFilename(t *testing.T) {
	fixedNow := time.Unix(1700000000, 0)
	cases := []struct {
		name      string
		rawURL    string
		userOut   string
		pageTitle string
		format    string
		want      string
	}{
		{
			name:    "ExplicitUserOutFMP4",
			rawURL:  "https://cdn.example.com/v/index.m3u8",
			userOut: "custom_name",
			format:  "fmp4",
			want:    "custom_name.mp4",
		},
		{
			name:    "UserOutWithManifestExt",
			rawURL:  "https://cdn.example.com/v/index.m3u8",
			userOut: "my_episode.m3u8",
			format:  "ts",
			want:    "my_episode.ts",
		},
		{
			name:      "GenericUserOutFallsToPageTitle",
			rawURL:    "https://cdn.example.com/v/index.m3u8",
			userOut:   "index.m3u8",
			pageTitle: "课程第01讲：Go并发",
			format:    "fmp4",
			want:      "课程第01讲_Go并发.mp4",
		},
		{
			name:   "URLBasenameNonGeneric",
			rawURL: "https://cdn.example.com/v/sintel_4k.m3u8?token=abc",
			format: "fmp4",
			want:   "sintel_4k.mp4",
		},
		{
			name:   "GenericBasenameWalksUpToParent",
			rawURL: "https://cdn.example.com/archive/big_buck_bunny/playlist-1080p.m3u8",
			format: "ts",
			want:   "big_buck_bunny.ts",
		},
		{
			name:   "AllGenericFallsToHostUnix",
			rawURL: "https://cdn.example.com/hls/stream/index.m3u8",
			format: "ts",
			want:   "cdn.example.com_1700000000.ts",
		},
		{
			name:    "ReservedDeviceNameRejected",
			rawURL:  "https://cdn.example.com/v/sintel.m3u8",
			userOut: "../CON.m3u8",
			format:  "ts",
			want:    "sintel.ts",
		},
		{
			name:    "ExactConInRejected",
			rawURL:  "https://cdn.example.com/v/sintel.m3u8",
			userOut: "CONIN$",
			format:  "ts",
			want:    "sintel.ts",
		},
		{
			name:    "ExactConOutRejected",
			rawURL:  "https://cdn.example.com/v/sintel.m3u8",
			userOut: "CONOUT$.m3u8",
			format:  "ts",
			want:    "sintel.ts",
		},
		{
			name:   "UserOutNonManifestGenericKept",
			rawURL: "https://cdn.example.com/v/sintel.m3u8",
			// An explicit bare name without .m3u8 is honored even when generic.
			userOut: "index",
			format:  "ts",
			want:    "index.ts",
		},
		{
			name:    "PathTraversalSanitized",
			rawURL:  "https://cdn.example.com/v/sintel.m3u8",
			userOut: "../../etc/evil",
			format:  "ts",
			want:    "evil.ts",
		},
		{
			name:    "InvalidCharsSanitized",
			rawURL:  "https://cdn.example.com/v/sintel.m3u8",
			userOut: "my|movie.ts",
			format:  "ts",
			want:    "my_movie.ts",
		},
		{
			name:   "EscapedPathSegmentUsed",
			rawURL: "https://cdn.example.com/v/%E7%AC%AC%E4%B8%89%E9%9B%86.m3u8",
			format: "ts",
			want:   "第三集.ts",
		},
		{
			name:   "NoURLFallsToHLSHost",
			rawURL: "",
			format: "ts",
			want:   "hls_1700000000.ts",
		},
	}
	for _, tc := range cases {
		got := hls.ResolveHLSOutputFilename(tc.rawURL, tc.userOut, tc.pageTitle, tc.format, fixedNow)
		if got != tc.want {
			t.Fatalf("%s: ResolveHLSOutputFilename() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
