package hls_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/strategy/hls"
	"goaria-v3/internal/surge/types"
)

const tsMediaDoc = `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:100
#EXTINF:6.006,
segs/seg0.ts
#EXTINF:6.0,
segs/seg1.ts
#EXT-X-ENDLIST
`

const fmp4MediaDoc = `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@64"
#EXTINF:6.0,
segs/seg0.mp4
#EXTINF:6.0,
segs/seg1.mp4
#EXT-X-ENDLIST
`

func TestPreflightHLSManifest_DirectMediaAndMasterTwoStep(t *testing.T) {
	t.Run("DirectMedia", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, tsMediaDoc)
		}))
		defer srv.Close()

		res, err := hls.PreflightHLSManifest(context.Background(), srv.Client(), srv.URL+"/media.m3u8", nil, nil)
		if err != nil {
			t.Fatalf("PreflightHLSManifest() = %v", err)
		}
		rs := res.ResumeState
		if rs == nil {
			t.Fatal("ResumeState = nil")
		}
		if rs.ContainerFormat != hls.ContainerTS || res.ContainerExt != ".ts" {
			t.Fatalf("format=%q ext=%q", rs.ContainerFormat, res.ContainerExt)
		}
		if rs.TotalSegments != 2 || len(rs.Segments) != 2 {
			t.Fatalf("TotalSegments=%d len(Segments)=%d", rs.TotalSegments, len(rs.Segments))
		}
		if len(rs.CompletedBitmap) != (rs.TotalSegments+7)/8 {
			t.Fatalf("bitmap len = %d", len(rs.CompletedBitmap))
		}
		if rs.EstimatedBytes != 0 || !rs.RecordEligible {
			t.Fatalf("EstimatedBytes=%d RecordEligible=%v (direct media has no bandwidth)",
				rs.EstimatedBytes, rs.RecordEligible)
		}
		if rs.Segments[0].URI != srv.URL+"/segs/seg0.ts" || rs.SegmentRefURL != rs.Segments[0].URI {
			t.Fatalf("SegmentRefURL=%q seg0=%q", rs.SegmentRefURL, rs.Segments[0].URI)
		}
		if rs.MediaPlaylistURL != srv.URL+"/media.m3u8" || rs.ManifestURL != srv.URL+"/media.m3u8" {
			t.Fatalf("MediaPlaylistURL=%q ManifestURL=%q", rs.MediaPlaylistURL, rs.ManifestURL)
		}
		if len(rs.ManifestFingerprint) != 64 || rs.VerifiedBytes != 0 || rs.MergedSegments != 0 {
			t.Fatalf("fp=%q verified=%d merged=%d", rs.ManifestFingerprint, rs.VerifiedBytes, rs.MergedSegments)
		}
	})

	t.Run("MasterTwoStep", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/master.m3u8":
				fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080\n%s/media.m3u8\n", srv.URL)
			case "/media.m3u8":
				fmt.Fprint(w, fmp4MediaDoc)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		res, err := hls.PreflightHLSManifest(context.Background(), srv.Client(), srv.URL+"/master.m3u8", nil, nil)
		if err != nil {
			t.Fatalf("PreflightHLSManifest() = %v", err)
		}
		rs := res.ResumeState
		if res.SelectedBandwidth != 4000000 {
			t.Fatalf("SelectedBandwidth = %d", res.SelectedBandwidth)
		}
		if rs.ContainerFormat != hls.ContainerFMP4 || res.ContainerExt != ".mp4" {
			t.Fatalf("format=%q ext=%q", rs.ContainerFormat, res.ContainerExt)
		}
		if rs.InitSegmentURI != srv.URL+"/init.mp4" || rs.InitByteOffset != 64 || rs.InitByteLength != 720 {
			t.Fatalf("init = %q off=%d len=%d", rs.InitSegmentURI, rs.InitByteOffset, rs.InitByteLength)
		}
		// 4 Mbps * 12 s / 8 = 6_000_000 estimated bytes — below the 50 MiB
		// recording threshold, so the task is not record-eligible.
		if rs.EstimatedBytes != 6_000_000 || rs.RecordEligible {
			t.Fatalf("EstimatedBytes=%d RecordEligible=%v", rs.EstimatedBytes, rs.RecordEligible)
		}
		if rs.MediaPlaylistURL != srv.URL+"/media.m3u8" {
			t.Fatalf("MediaPlaylistURL = %q", rs.MediaPlaylistURL)
		}
		if rs.TotalSegments != 2 || len(rs.CompletedBitmap) != 1 || len(rs.Segments) != 2 {
			t.Fatalf("TotalSegments=%d bitmap=%d segs=%d", rs.TotalSegments, len(rs.CompletedBitmap), len(rs.Segments))
		}
	})

	t.Run("NilClientUsesNetworkPool", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, tsMediaDoc)
		}))
		defer srv.Close()

		// client == nil exercises the borrowed DefaultNetworkPool path;
		// the transport is released inside the call.
		res, err := hls.PreflightHLSManifest(context.Background(), nil, srv.URL+"/media.m3u8", nil, nil)
		if err != nil {
			t.Fatalf("PreflightHLSManifest(nil client) = %v", err)
		}
		if res.ResumeState == nil || res.ResumeState.TotalSegments != 2 {
			t.Fatalf("ResumeState = %+v", res.ResumeState)
		}
	})
}

// rewriteHostTransport maps every request host onto the httptest listener so
// tests can simulate distinct public origins (different eTLD+1) against one
// real server.
type rewriteHostTransport struct {
	base   http.RoundTripper
	target string
}

func (t rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Host = t.target
	return t.base.RoundTrip(r)
}

func TestPreflightHLSManifest_RedirectBaseURLAndCrossSiteCredentialStripping(t *testing.T) {
	taskHeaders := map[string]string{
		"Cookie":              "sid=secret",
		"Cookie2":             "c2=v2",
		"Authorization":       "Bearer tok",
		"Proxy-Authorization": "Basic pxy",
		"Referer":             "https://page.example/",
		"User-Agent":          "GoAria-Test-UA/1.0",
		"Range":               "bytes=0-99",
	}

	t.Run("RedirectRelativeResolution", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/entry.m3u8":
				http.Redirect(w, r, "/nested/dir/master.m3u8", http.StatusFound)
			case "/nested/dir/master.m3u8":
				fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nmedia.m3u8\n")
			case "/nested/dir/media.m3u8":
				fmt.Fprint(w, "#EXTM3U\n#EXTINF:6.0,\n../segs/seg0.ts\n#EXT-X-ENDLIST\n")
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		res, err := hls.PreflightHLSManifest(context.Background(), srv.Client(), srv.URL+"/entry.m3u8", nil, nil)
		if err != nil {
			t.Fatalf("PreflightHLSManifest() = %v", err)
		}
		wantRef := srv.URL + "/nested/segs/seg0.ts"
		if res.ResumeState.SegmentRefURL != wantRef {
			t.Fatalf("SegmentRefURL = %q, want %q", res.ResumeState.SegmentRefURL, wantRef)
		}
		if res.ResumeState.MediaPlaylistURL != srv.URL+"/nested/dir/media.m3u8" {
			t.Fatalf("MediaPlaylistURL = %q", res.ResumeState.MediaPlaylistURL)
		}
	})

	t.Run("CrossSiteAndSameSiteCredentialGate", func(t *testing.T) {
		var gotCookie, gotCookie2, gotAuth, gotProxyAuth, gotReferer, gotUA, gotRange atomic.Value
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/master-cross.m3u8":
				fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nhttp://cdn-b.example.net/media.m3u8\n")
			case "/master-same.m3u8":
				fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nhttp://hls.site-a.example.com/media.m3u8\n")
			case "/media.m3u8":
				gotCookie.Store(r.Header.Get("Cookie"))
				gotCookie2.Store(r.Header.Get("Cookie2"))
				gotAuth.Store(r.Header.Get("Authorization"))
				gotProxyAuth.Store(r.Header.Get("Proxy-Authorization"))
				gotReferer.Store(r.Header.Get("Referer"))
				gotUA.Store(r.Header.Get("User-Agent"))
				gotRange.Store(r.Header.Get("Range"))
				fmt.Fprint(w, tsMediaDoc)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		tr := &http.Transport{}
		client := &http.Client{Transport: rewriteHostTransport{base: tr, target: srv.Listener.Addr().String()}}
		t.Cleanup(tr.CloseIdleConnections)

		// Cross-site sub-playlist: credentials must be stripped even though the
		// bytes physically reach the same listener.
		_, err := hls.PreflightHLSManifest(context.Background(), client,
			"http://site-a.example.com/master-cross.m3u8", taskHeaders, nil)
		if err != nil {
			t.Fatalf("cross-site preflight = %v", err)
		}
		if v := gotCookie.Load(); v != nil && v.(string) != "" {
			t.Fatalf("cross-site Cookie leaked: %q", v)
		}
		if v := gotCookie2.Load(); v != nil && v.(string) != "" {
			t.Fatalf("cross-site Cookie2 leaked: %q", v)
		}
		if v := gotAuth.Load(); v != nil && v.(string) != "" {
			t.Fatalf("cross-site Authorization leaked: %q", v)
		}
		if v := gotProxyAuth.Load(); v != nil && v.(string) != "" {
			t.Fatalf("cross-site Proxy-Authorization leaked: %q", v)
		}
		if v := gotReferer.Load(); v == nil || v.(string) != "https://page.example/" {
			t.Fatalf("cross-site Referer = %v, want passthrough", v)
		}
		if v := gotUA.Load(); v == nil || v.(string) != "GoAria-Test-UA/1.0" {
			t.Fatalf("cross-site User-Agent = %v", v)
		}
		if v := gotRange.Load(); v != nil && v.(string) != "" {
			t.Fatalf("Range header leaked: %q", v)
		}

		// Same-site (subdomain of the same eTLD+1): credentials pass through.
		gotCookie.Store("")
		gotCookie2.Store("")
		gotAuth.Store("")
		gotProxyAuth.Store("")
		gotReferer.Store("")
		gotUA.Store("")
		gotRange.Store("")
		_, err = hls.PreflightHLSManifest(context.Background(), client,
			"http://site-a.example.com/master-same.m3u8", taskHeaders, nil)
		if err != nil {
			t.Fatalf("same-site preflight = %v", err)
		}
		if v := gotCookie.Load(); v == nil || v.(string) != "sid=secret" {
			t.Fatalf("same-site Cookie = %v, want forwarded", v)
		}
		if v := gotCookie2.Load(); v == nil || v.(string) != "c2=v2" {
			t.Fatalf("same-site Cookie2 = %v, want forwarded", v)
		}
		if v := gotAuth.Load(); v == nil || v.(string) != "Bearer tok" {
			t.Fatalf("same-site Authorization = %v, want forwarded", v)
		}
		if v := gotProxyAuth.Load(); v == nil || v.(string) != "Basic pxy" {
			t.Fatalf("same-site Proxy-Authorization = %v, want forwarded", v)
		}
	})
}

func TestPreflightHLSManifest_TimeoutAndSentinelPropagation(t *testing.T) {
	t.Run("TimeoutDegradesNotFailFast", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}))
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		_, err := hls.PreflightHLSManifest(ctx, srv.Client(), srv.URL+"/media.m3u8", nil, nil)
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if hls.ShouldFailFastPreflight(err) {
			t.Fatalf("ShouldFailFastPreflight(%v) = true, want false", err)
		}
		fallback := hls.NewFallbackHLSResumeState(srv.URL+"/media.m3u8", srv.URL+"/media.m3u8")
		if fallback == nil || !fallback.RecordEligible || fallback.ContainerFormat != hls.ContainerTS ||
			fallback.SegmentRefURL == "" || fallback.TotalSegments != 0 {
			t.Fatalf("fallback = %+v", fallback)
		}
		if hls.NewFallbackHLSResumeState(" http://a/b.m3u8 ", "").SegmentRefURL != "http://a/b.m3u8" {
			t.Fatal("empty fallbackRefURL must default to manifestURL")
		}
	})

	t.Run("PermanentGatesFailFast", func(t *testing.T) {
		docs := map[string]struct {
			path string
			doc  string
			want error
		}{
			"Live": {
				"/live.m3u8",
				"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.0,\nseg0.ts\n",
				types.ErrHLSLiveNotSupported,
			},
			"DRM": {
				"/drm.m3u8",
				"#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k.bin\"\n#EXTINF:6.0,\nseg0.ts\n#EXT-X-ENDLIST\n",
				types.ErrHLSDRMNotSupported,
			},
			"Demuxed": {
				"/demuxed.m3u8",
				"#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"a.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1920x1080,AUDIO=\"a\"\nv.m3u8\n",
				types.ErrHLSDemuxedAudioNotSupported,
			},
			"DiscontinuityMap": {
				"/dmap.m3u8",
				"#EXTM3U\n#EXT-X-MAP:URI=\"i.mp4\"\n#EXTINF:6.0,\na.mp4\n#EXT-X-DISCONTINUITY\n#EXT-X-MAP:URI=\"i2.mp4\"\n#EXTINF:6.0,\nb.mp4\n#EXT-X-ENDLIST\n",
				types.ErrHLSDiscontinuityMapNotSupported,
			},
			"NotAManifest": {
				"/html.m3u8",
				"<html><body>404 page</body></html>",
				types.ErrHLSInvalidManifest,
			},
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, tc := range docs {
				if r.URL.Path == tc.path {
					fmt.Fprint(w, tc.doc)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		for name, tc := range docs {
			_, err := hls.PreflightHLSManifest(context.Background(), srv.Client(), srv.URL+tc.path, nil, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: err = %v, want %v", name, err, tc.want)
			}
			if !hls.ShouldFailFastPreflight(err) {
				t.Fatalf("%s: ShouldFailFastPreflight(%v) = false, want true", name, err)
			}
		}
	})

	t.Run("OversizeManifestRejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, strings.Repeat("#", int(hls.MaxManifestBytes)+64))
		}))
		defer srv.Close()

		_, err := hls.PreflightHLSManifest(context.Background(), srv.Client(), srv.URL+"/big.m3u8", nil, nil)
		if !errors.Is(err, types.ErrHLSInvalidManifest) {
			t.Fatalf("err = %v, want ErrHLSInvalidManifest", err)
		}
	})

	t.Run("InvalidManifestURL", func(t *testing.T) {
		for _, raw := range []string{"ftp://host/x.m3u8", "not-a-url", "https://"} {
			_, err := hls.PreflightHLSManifest(context.Background(), nil, raw, nil, nil)
			if !errors.Is(err, types.ErrHLSInvalidManifest) {
				t.Fatalf("PreflightHLSManifest(%q) = %v, want ErrHLSInvalidManifest", raw, err)
			}
		}
	})

	t.Run("Non200Status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		_, err := hls.PreflightHLSManifest(context.Background(), srv.Client(), srv.URL+"/m.m3u8", nil, nil)
		if !errors.Is(err, types.ErrPermanentHTTP) {
			t.Fatalf("403 err = %v, want ErrPermanentHTTP", err)
		}
		// A 4xx manifest response stays degradable (transient for the
		// enqueue path); only the HLS gate sentinels fail fast.
		if hls.ShouldFailFastPreflight(err) {
			t.Fatalf("ShouldFailFastPreflight(%v) = true, want false (degradable)", err)
		}
	})

	t.Run("NestedMasterRejected", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Every path serves another master playlist: the second hop
			// must be refused instead of followed recursively.
			fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\n%s/loop.m3u8\n", srv.URL)
		}))
		defer srv.Close()
		_, err := hls.PreflightHLSManifest(context.Background(), srv.Client(), srv.URL+"/m.m3u8", nil, nil)
		if !errors.Is(err, types.ErrHLSInvalidManifest) {
			t.Fatalf("err = %v, want ErrHLSInvalidManifest", err)
		}
		if !hls.ShouldFailFastPreflight(err) {
			t.Fatal("nested master must fail fast")
		}
	})
}

func TestPreflightHLSManifest_EncryptedInitFields(t *testing.T) {
	const ivNorm = "00000000000000000000000000ff00aa"
	var requested []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requested = append(requested, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/media.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n"+
				"#EXT-X-KEY:METHOD=AES-128,URI=\"k1.bin\",IV=0x00000000000000000000000000ff00aa\n"+
				"#EXT-X-MAP:URI=\"init.mp4\",BYTERANGE=\"720@64\"\n"+
				"#EXTINF:6.0,\nsegs/seg0.mp4\n#EXTINF:6.0,\nsegs/seg1.mp4\n#EXT-X-ENDLIST\n")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	res, err := hls.PreflightHLSManifest(context.Background(), srv.Client(), srv.URL+"/media.m3u8", nil, nil)
	if err != nil {
		t.Fatalf("PreflightHLSManifest() = %v", err)
	}
	rs := res.ResumeState
	if rs == nil {
		t.Fatal("ResumeState = nil")
	}
	if rs.InitKeyIndex != 1 || rs.InitIVHex != ivNorm {
		t.Fatalf("init crypto fields = (%d,%q), want (1,%q)", rs.InitKeyIndex, rs.InitIVHex, ivNorm)
	}
	if rs.InitSegmentURI != srv.URL+"/init.mp4" || rs.InitByteOffset != 64 || rs.InitByteLength != 720 {
		t.Fatalf("init descriptor = %q off=%d len=%d", rs.InitSegmentURI, rs.InitByteOffset, rs.InitByteLength)
	}
	if rs.InitCompleted || rs.InitBytes != 0 {
		t.Fatalf("init completion flags must stay zero pre-download: %+v", rs)
	}
	// Preflight fetches the manifest only — keys, init sections and
	// segments are never pulled during enqueue.
	mu.Lock()
	got := append([]string(nil), requested...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "/media.m3u8" {
		t.Fatalf("preflight fetched %v, want only [/media.m3u8]", got)
	}

	// The checkpoint keeps the fields through Clone + JSON + gob handoffs.
	cp := rs.Clone()
	if cp.InitKeyIndex != rs.InitKeyIndex || cp.InitIVHex != rs.InitIVHex {
		t.Fatalf("Clone lost init crypto fields: %+v", cp)
	}
}

func TestCanSendHLSCredentials(t *testing.T) {
	mkURL := func(raw string) *url.URL { return mustParseURL(t, raw) }
	cases := []struct {
		name   string
		target *url.URL
		base   *url.URL
		want   bool
	}{
		{"SameHostSameScheme", mkURL("https://a.example.com/x"), mkURL("https://a.example.com/m"), true},
		{"SameSiteSubdomain", mkURL("https://cdn.a.example.com/x"), mkURL("https://a.example.com/m"), true},
		{"CrossSite", mkURL("https://cdn-b.example.net/x"), mkURL("https://a.example.com/m"), false},
		{"SchemeDowngrade", mkURL("http://a.example.com/x"), mkURL("https://a.example.com/m"), false},
		{"NilTarget", nil, mkURL("https://a.example.com/m"), false},
		{"EmptyHost", &url.URL{Scheme: "https"}, mkURL("https://a.example.com/m"), false},
		{"NilBase", mkURL("https://a.example.com/x"), nil, false},
	}
	for _, tc := range cases {
		got := hls.CanSendHLSCredentials(tc.target, tc.base)
		if got != tc.want {
			t.Fatalf("%s: CanSendHLSCredentials(%v, %v) = %v, want %v",
				tc.name, tc.target, tc.base, got, tc.want)
		}
	}
}

func TestApplyHLSRequestHeaders(t *testing.T) {
	base := mustParseURL(t, "https://a.example.com/master.m3u8")
	newReq := func(t *testing.T, raw string) *http.Request {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q) = %v", raw, err)
		}
		return req
	}

	t.Run("HostHonoredSameSiteOnly", func(t *testing.T) {
		same := newReq(t, "https://cdn.a.example.com/media.m3u8")
		hls.ApplyHLSRequestHeaders(same,
			map[string]string{"Host": "vhost.a.example.com"}, base, nil)
		if same.Host != "vhost.a.example.com" {
			t.Fatalf("same-site req.Host = %q, want honored", same.Host)
		}
		if h := same.Header.Get("Host"); h != "" {
			t.Fatalf("Host leaked into header map: %q", h)
		}

		cross := newReq(t, "https://cdn-b.example.net/media.m3u8")
		hls.ApplyHLSRequestHeaders(cross,
			map[string]string{"Host": "vhost.a.example.com"}, base, nil)
		// req.Host is pre-seeded with the URL host by NewRequest; the
		// injected Host override must be dropped, leaving the default.
		if cross.Host == "vhost.a.example.com" {
			t.Fatalf("cross-site req.Host = %q, want the origin override stripped", cross.Host)
		}
		if cross.Host != "cdn-b.example.net" {
			t.Fatalf("cross-site req.Host = %q, want URL host preserved", cross.Host)
		}
	})

	t.Run("CaseVariantDuplicateDeterministic", func(t *testing.T) {
		// "X-Tok" sorts before "x-tok": the lowercase entry is applied
		// last and must win on every iteration, not map-order randomly.
		for i := range 25 {
			req := newReq(t, "https://a.example.com/media.m3u8")
			hls.ApplyHLSRequestHeaders(req,
				map[string]string{"X-Tok": "A", "x-tok": "b"}, base, nil)
			if got := req.Header.Get("X-Tok"); got != "b" {
				t.Fatalf("iter %d: X-Tok = %q, want deterministic %q", i, got, "b")
			}
		}
	})

	t.Run("NilReqSafe", func(t *testing.T) {
		hls.ApplyHLSRequestHeaders(nil, map[string]string{"Cookie": "x"}, base, nil)
	})
}
