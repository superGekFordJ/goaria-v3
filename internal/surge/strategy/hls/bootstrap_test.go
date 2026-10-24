package hls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"goaria-v3/internal/surge/types"
)

func bootDownloader(srv *httptest.Server) *HlsDownloader {
	d := NewHlsDownloader("boot", nil, nil, nil)
	if srv != nil {
		d.Client = srv.Client()
	}
	return d
}

func bootSeed(o *runOrigin, tweak func(*types.HLSResumeState)) *types.HLSResumeState {
	s := &types.HLSResumeState{
		ManifestURL:      o.url("/media.m3u8"),
		MediaPlaylistURL: o.url("/media.m3u8"),
		SegmentRefURL:    o.url("/media.m3u8"),
		TotalSegments:    2,
		CompletedBitmap:  []byte{0x00},
		RecordEligible:   true,
		Segments: []types.HLSSegmentState{
			{Index: 0, URI: o.url("/s0.ts")},
			{Index: 1, URI: o.url("/s1.ts")},
		},
	}
	if tweak != nil {
		tweak(s)
	}
	return s
}

func mustCredBase(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := parseHLSBinaryURL(raw)
	if err != nil {
		t.Fatalf("cred base: %v", err)
	}
	return u
}

func TestHLSBootstrap_PreparedTableIsReusedWithoutRefetch(t *testing.T) {
	var calls atomic.Int32
	d := bootDownloader(nil)
	d.Client = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("network forbidden for prepared seed")
	})}
	seed := &types.HLSResumeState{
		ManifestURL:      "http://m.example.com/media.m3u8",
		MediaPlaylistURL: "http://m.example.com/media.m3u8",
		TotalSegments:    2,
		CompletedBitmap:  []byte{0x00},
		RecordEligible:   true,
		Segments: []types.HLSSegmentState{
			{Index: 0, URI: "http://m.example.com/s0.ts"},
			{Index: 1, URI: "http://m.example.com/s1.ts"},
		},
	}

	got, err := d.bootstrap(context.Background(), seed, mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
	if err != nil {
		t.Fatalf("bootstrap = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("prepared table still hit the network %d times", calls.Load())
	}
	if got == seed {
		t.Fatal("bootstrap returned the live seed, not a clone")
	}
	got.Segments[0].URI = "mutated"
	if seed.Segments[0].URI == "mutated" {
		t.Fatal("bootstrap clone shares the segment backing array")
	}
}

func TestHLSBootstrap_RebasedSeedReResolvesManifest(t *testing.T) {
	o := newRunOrigin(t)
	o.serveBody("/s0.ts", []byte("x"))
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts"})
	// An explicit rebase invalidated every derived reference — only
	// ManifestURL and RecordEligible survive.
	seed := bootSeed(o, nil).RebaseManifestURL(o.url("/media.m3u8"))

	d := bootDownloader(o.srv)
	got, err := d.bootstrap(context.Background(), seed,
		mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
	if err != nil {
		t.Fatalf("bootstrap = %v", err)
	}
	if o.requests.count("/media.m3u8") != 1 {
		t.Fatalf("manifest fetched %d times", o.requests.count("/media.m3u8"))
	}
	if got.TotalSegments != 1 || got.Segments[0].URI != o.url("/s0.ts") {
		t.Fatalf("rebuilt table = %+v", got)
	}
	if got.MediaPlaylistURL == "" {
		t.Fatal("media anchor not recorded")
	}
	if got.MergedBytes != 0 || got.VerifiedBytes != 0 || got.MergedSegments != 0 {
		t.Fatalf("progress leaked into a fresh table: %+v", got)
	}
}

func TestHLSBootstrap_StartsAtMediaPlaylistURLWhenUsable(t *testing.T) {
	o := newRunOrigin(t)
	o.serveBody("/s0.ts", []byte("x"))
	o.serveMediaPlaylist("/live.m3u8", []string{"s0.ts"})
	// The seed has no segment table but keeps a usable media anchor —
	// bootstrap must start there, not at ManifestURL.
	seed := &types.HLSResumeState{
		ManifestURL:      o.url("/dead.m3u8"), // intentionally not served
		MediaPlaylistURL: o.url("/live.m3u8"),
	}
	d := bootDownloader(o.srv)
	got, err := d.bootstrap(context.Background(), seed,
		mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
	if err != nil {
		t.Fatalf("bootstrap = %v", err)
	}
	if o.requests.count("/live.m3u8") != 1 || o.requests.count("/dead.m3u8") != 0 {
		t.Fatalf("fetch order wrong: live=%d dead=%d",
			o.requests.count("/live.m3u8"), o.requests.count("/dead.m3u8"))
	}
	if got.TotalSegments != 1 {
		t.Fatalf("table = %+v", got)
	}
}

func TestHLSBootstrap_MasterSelectsHighestMuxedVariant(t *testing.T) {
	o := newRunOrigin(t)
	o.serveBody("/hi0.ts", []byte("x"))
	o.serveMediaPlaylist("/hi.m3u8", []string{"hi0.ts"})
	o.serveMediaPlaylist("/lo.m3u8", []string{"lo0.ts"})
	master := "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS=\"avc1.42e01e,mp4a.40.2\"\nlo.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS=\"avc1.42e01e,mp4a.40.2\"\nhi.m3u8\n"
	o.mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(master))
	})
	seed := &types.HLSResumeState{ManifestURL: o.url("/master.m3u8")}
	d := bootDownloader(o.srv)
	got, err := d.bootstrap(context.Background(), seed,
		mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
	if err != nil {
		t.Fatalf("bootstrap = %v", err)
	}
	if o.requests.count("/hi.m3u8") != 1 || o.requests.count("/lo.m3u8") != 0 {
		t.Fatalf("variant pick wrong: hi=%d lo=%d",
			o.requests.count("/hi.m3u8"), o.requests.count("/lo.m3u8"))
	}
	if got.ManifestURL != seed.ManifestURL {
		t.Fatalf("ManifestURL rewritten to %q", got.ManifestURL)
	}
}

func TestHLSBootstrap_NestedMasterRejected(t *testing.T) {
	o := newRunOrigin(t)
	nested := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100000,RESOLUTION=320x180,CODECS=\"avc1.42e01e\"\ninner.m3u8\n"
	o.mux.HandleFunc("/outer.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nested))
	})
	o.mux.HandleFunc("/inner.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nested)) // a second master — never valid
	})
	seed := &types.HLSResumeState{ManifestURL: o.url("/outer.m3u8")}
	d := bootDownloader(o.srv)
	_, err := d.bootstrap(context.Background(), seed,
		mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
	if !errors.Is(err, types.ErrHLSInvalidManifest) {
		t.Fatalf("bootstrap = %v, want ErrHLSInvalidManifest", err)
	}
}

func TestHLSBootstrap_LivePlaylistRejected(t *testing.T) {
	o := newRunOrigin(t)
	o.mux.HandleFunc("/live.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\ns0.ts\n"))
	})
	seed := &types.HLSResumeState{ManifestURL: o.url("/live.m3u8")}
	d := bootDownloader(o.srv)
	_, err := d.bootstrap(context.Background(), seed,
		mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
	if !errors.Is(err, types.ErrHLSLiveNotSupported) {
		t.Fatalf("bootstrap = %v, want ErrHLSLiveNotSupported", err)
	}
}

func TestHLSBootstrap_CredentialsStayOnAnchorSite(t *testing.T) {
	// Master on a.test; media playlist + segments on b.test. Two loopback
	// servers share one eTLD+1 and are therefore same-site — a synthetic
	// transport is the only way to observe the cross-site gate.
	var aAuth, bAuth atomic.Int32
	text := func(r *http.Request, body string) *http.Response {
		return &http.Response{
			Status:        "200 OK",
			StatusCode:    http.StatusOK,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       r,
		}
	}
	mediaDoc := "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:4.0,\nhttp://b.test/s0.ts\n#EXT-X-ENDLIST\n"
	masterDoc := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100000,RESOLUTION=320x180,CODECS=\"avc1.42e01e,mp4a.40.2\"\nhttp://b.test/media.m3u8\n"
	d := bootDownloader(nil)
	d.Client = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			switch r.URL.Host {
			case "a.test":
				aAuth.Add(1)
			default:
				bAuth.Add(1)
			}
		}
		switch r.URL.Host {
		case "a.test":
			return text(r, masterDoc), nil
		case "b.test":
			return text(r, mediaDoc), nil
		}
		return nil, fmt.Errorf("unexpected host %q", r.URL.Host)
	})}

	seed := &types.HLSResumeState{ManifestURL: "http://a.test/master.m3u8"}
	got, err := d.bootstrap(context.Background(), seed,
		mustCredBase(t, seed.ManifestURL),
		map[string]string{"Authorization": "Bearer site-a"},
		types.DefaultRuntimeConfig())
	if err != nil {
		t.Fatalf("bootstrap = %v", err)
	}
	if aAuth.Load() == 0 {
		t.Fatal("anchor-site request lost the task Authorization")
	}
	if bAuth.Load() != 0 {
		t.Fatalf("Authorization leaked to the foreign host %d times", bAuth.Load())
	}
	if got.TotalSegments != 1 {
		t.Fatalf("table = %+v", got)
	}
}
