package hls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"goaria-v3/internal/surge/types"
)

// resumeCompatibleSeed builds a prepared resume seed whose descriptors
// match a serveMediaPlaylist document for refs — Index-ordered,
// Sequence=i, DurationMicros=4s (the served EXTINF:4.0), container ts —
// with old signed URIs that share the served paths so only the signature
// surface moves on refresh.
func resumeCompatibleSeed(o *runOrigin, refs []string, tweak func(*types.HLSResumeState)) *types.HLSResumeState {
	segs := make([]types.HLSSegmentState, len(refs))
	for i, ref := range refs {
		segs[i] = types.HLSSegmentState{
			Index: i, Sequence: int64(i), DurationMicros: 4_000_000,
			URI: o.url("/" + ref + "?token=old"),
		}
	}
	s := &types.HLSResumeState{
		ManifestURL:      o.url("/media.m3u8"),
		MediaPlaylistURL: o.url("/media.m3u8"),
		SegmentRefURL:    o.url("/media.m3u8"),
		TotalSegments:    len(refs),
		CompletedBitmap:  make([]byte, (len(refs)+7)/8),
		ContainerFormat:  "ts",
		RecordEligible:   true,
		Segments:         segs,
	}
	if tweak != nil {
		tweak(s)
	}
	return s
}

func TestHLSBootstrap_ResumeSignedURIs(t *testing.T) {
	o := newRunOrigin(t)
	refs := []string{"s0.ts", "s1.ts", "s2.ts", "s3.ts"}
	o.serveMediaPlaylist("/media.m3u8", refs)
	seed := resumeCompatibleSeed(o, refs, func(s *types.HLSResumeState) {
		s.MergedSegments = 1
		s.MergedBytes = 3
		s.VerifiedBytes = 7 // merged s0 + spilled s3
		s.CompletedBitmap = []byte{0x09}
		s.SpillPath = "/old/arena.spill"
		s.SpillFileSize = 4
		s.SpillEntries = []types.HLSSpillEntry{{SegmentIndex: 3, Offset: 0, Length: 4}}
	})
	d := bootDownloader(o.srv)
	d.IsResume = true

	got, err := d.bootstrap(context.Background(), seed,
		mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
	if err != nil {
		t.Fatalf("bootstrap = %v", err)
	}
	if n := o.requests.count("/media.m3u8"); n != 1 {
		t.Fatalf("manifest fetched %d times, want exactly one refresh", n)
	}
	// The merged prefix keeps its recorded URI; every index at or beyond
	// it — the spill-backed s3 included — moves to the new signature.
	if got.Segments[0].URI != seed.Segments[0].URI {
		t.Fatalf("merged prefix URI moved to %q", got.Segments[0].URI)
	}
	for i := 1; i < len(refs); i++ {
		if want := o.url("/" + refs[i]); got.Segments[i].URI != want {
			t.Fatalf("Segments[%d].URI = %q, want %q", i, got.Segments[i].URI, want)
		}
	}
	if got.ManifestFingerprint == "" {
		t.Fatal("refreshed fingerprint not adopted")
	}
	// Every progress/ledger field survives untouched.
	if got.MergedSegments != 1 || got.MergedBytes != 3 || got.VerifiedBytes != 7 ||
		got.CompletedBitmap[0] != 0x09 || got.SpillPath != seed.SpillPath ||
		got.SpillFileSize != 4 || len(got.SpillEntries) != 1 ||
		got.SpillEntries[0].SegmentIndex != 3 {
		t.Fatalf("progress state not preserved: %+v", got)
	}
	// The checkpointed seed was never half-rewritten.
	for i, seg := range seed.Segments {
		if !strings.HasSuffix(seg.URI, "token=old") {
			t.Fatalf("seed.Segments[%d] mutated to %q", i, seg.URI)
		}
	}
	if seed.ManifestFingerprint != "" {
		t.Fatal("seed fingerprint mutated by the refresh")
	}
}

func TestHLSBootstrap_ResumeRefreshGate(t *testing.T) {
	cases := []struct {
		name      string
		isResume  bool
		tweak     func(*types.HLSResumeState)
		wantFetch int
	}{
		{name: "fresh prepared skips the second GET"},
		{name: "explicit resume", isResume: true, wantFetch: 1},
		{name: "merged prefix", tweak: func(s *types.HLSResumeState) {
			s.MergedSegments = 1
			s.MergedBytes = 3
			s.VerifiedBytes = 3
			s.CompletedBitmap = []byte{0x01}
		}, wantFetch: 1},
		{name: "verified bytes", tweak: func(s *types.HLSResumeState) {
			s.VerifiedBytes = 3
		}, wantFetch: 1},
		{name: "spill ledger", tweak: func(s *types.HLSResumeState) {
			s.SpillPath = "/a.spill"
			s.SpillFileSize = 4
			s.SpillEntries = []types.HLSSpillEntry{{SegmentIndex: 1, Offset: 0, Length: 4}}
			s.CompletedBitmap = []byte{0x02}
			s.VerifiedBytes = 4
		}, wantFetch: 1},
		{name: "completed init", tweak: func(s *types.HLSResumeState) {
			s.InitCompleted = true
		}, wantFetch: 1},
		{name: "fully merged zero-network", tweak: func(s *types.HLSResumeState) {
			s.MergedSegments = 2
			s.MergedBytes = 6
			s.VerifiedBytes = 6
			s.CompletedBitmap = []byte{0x03}
		}, isResume: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newRunOrigin(t)
			o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})
			seed := resumeCompatibleSeed(o, []string{"s0.ts", "s1.ts"}, tc.tweak)
			d := bootDownloader(o.srv)
			d.IsResume = tc.isResume
			got, err := d.bootstrap(context.Background(), seed,
				mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
			if err != nil {
				t.Fatalf("bootstrap = %v", err)
			}
			if n := o.requests.count("/media.m3u8"); n != tc.wantFetch {
				t.Fatalf("manifest fetches = %d, want %d", n, tc.wantFetch)
			}
			if got == seed {
				t.Fatal("bootstrap returned the live seed, not a clone")
			}
		})
	}
}

func TestHLSBootstrap_ResumeFingerprintPolicy(t *testing.T) {
	newSeededRun := func(t *testing.T, refs []string, fp string) (*runOrigin, *types.HLSResumeState, *HlsDownloader) {
		o := newRunOrigin(t)
		o.serveMediaPlaylist("/media.m3u8", refs)
		seed := resumeCompatibleSeed(o, refs, func(s *types.HLSResumeState) {
			s.MergedSegments = 1
			s.MergedBytes = 3
			s.VerifiedBytes = 3
			s.CompletedBitmap = []byte{0x01}
			s.ManifestFingerprint = fp
		})
		d := bootDownloader(o.srv)
		d.IsResume = true
		return o, seed, d
	}

	t.Run("empty fingerprint adopts the first compatible playlist", func(t *testing.T) {
		_, seed, d := newSeededRun(t, []string{"s0.ts", "s1.ts"}, "")
		got, err := d.bootstrap(context.Background(), seed,
			mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
		if err != nil {
			t.Fatalf("bootstrap = %v", err)
		}
		if got.ManifestFingerprint == "" {
			t.Fatal("empty fingerprint was not populated from the refresh")
		}
	})

	// The fingerprint consumes cleaned paths only — a descriptor table
	// over any host reproduces exactly what the parser computes.
	compatibleFingerprint := func(refs []string) string {
		segs := make([]types.HLSSegmentState, len(refs))
		for i, ref := range refs {
			segs[i] = types.HLSSegmentState{
				Index: i, Sequence: int64(i), DurationMicros: 4_000_000,
				URI: "http://m/" + ref,
			}
		}
		return ComputeManifestFingerprint("ts", 0, nil, segs, nil)
	}

	t.Run("matching fingerprint adopts", func(t *testing.T) {
		fp := compatibleFingerprint([]string{"s0.ts", "s1.ts"})
		_, seed, d := newSeededRun(t, []string{"s0.ts", "s1.ts"}, fp)
		got, err := d.bootstrap(context.Background(), seed,
			mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
		if err != nil {
			t.Fatalf("bootstrap = %v", err)
		}
		if got.ManifestFingerprint != fp {
			t.Fatalf("fingerprint = %q, want %q", got.ManifestFingerprint, fp)
		}
	})

	t.Run("mismatching fingerprint is terminal", func(t *testing.T) {
		_, seed, d := newSeededRun(t, []string{"s0.ts", "s1.ts"}, "deadbeef")
		_, err := d.bootstrap(context.Background(), seed,
			mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
		if !errors.Is(err, types.ErrSourceMetadataMismatch) {
			t.Fatalf("bootstrap = %v, want ErrSourceMetadataMismatch", err)
		}
	})

	t.Run("escaped path is identity not alias", func(t *testing.T) {
		o := newRunOrigin(t)
		// The served playlist references s%7E0.ts — the parser keeps the
		// escaped distinction in the fingerprint while s~0.ts would hash
		// differently. A seed claiming the ~ form must be rejected.
		o.mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:4.0,\ns%7E0.ts\n#EXT-X-ENDLIST\n"))
		})
		segs := []types.HLSSegmentState{{
			Index: 0, Sequence: 0, DurationMicros: 4_000_000,
			URI: o.url("/s~0.ts"),
		}}
		seed := &types.HLSResumeState{
			ManifestURL:         o.url("/media.m3u8"),
			MediaPlaylistURL:    o.url("/media.m3u8"),
			TotalSegments:       1,
			CompletedBitmap:     []byte{0x00},
			ContainerFormat:     "ts",
			MergedBytes:         0,
			ManifestFingerprint: ComputeManifestFingerprint("ts", 0, nil, segs, nil),
			Segments:            segs,
		}
		d := bootDownloader(o.srv)
		d.IsResume = true
		_, err := d.bootstrap(context.Background(), seed,
			mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
		if !errors.Is(err, types.ErrSourceMetadataMismatch) {
			t.Fatalf("bootstrap = %v, want ErrSourceMetadataMismatch for %%7E vs ~", err)
		}
	})

	t.Run("structural change is terminal", func(t *testing.T) {
		o := newRunOrigin(t)
		// Same fingerprint-bypass seed but the origin now declares a
		// different structure — segment count changed.
		o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts", "s2.ts"})
		seed := resumeCompatibleSeed(o, []string{"s0.ts", "s1.ts"}, func(s *types.HLSResumeState) {
			s.MergedSegments = 1
			s.MergedBytes = 3
			s.VerifiedBytes = 3
			s.CompletedBitmap = []byte{0x01}
		})
		d := bootDownloader(o.srv)
		d.IsResume = true
		_, err := d.bootstrap(context.Background(), seed,
			mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
		if !errors.Is(err, types.ErrSourceMetadataMismatch) {
			t.Fatalf("bootstrap = %v, want ErrSourceMetadataMismatch", err)
		}
	})
}

func TestHLSBootstrap_ResumeRefreshFallback(t *testing.T) {
	t.Run("dead media entry falls back to the manifest anchor", func(t *testing.T) {
		o := newRunOrigin(t)
		o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})
		o.mux.HandleFunc("/dead.m3u8", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		seed := resumeCompatibleSeed(o, []string{"s0.ts", "s1.ts"}, func(s *types.HLSResumeState) {
			s.MergedSegments = 1
			s.MergedBytes = 3
			s.VerifiedBytes = 3
			s.CompletedBitmap = []byte{0x01}
			s.MediaPlaylistURL = o.url("/dead.m3u8")
		})
		d := bootDownloader(o.srv)
		d.IsResume = true
		got, err := d.bootstrap(context.Background(), seed,
			mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
		if err != nil {
			t.Fatalf("bootstrap = %v", err)
		}
		if o.requests.count("/dead.m3u8") != 1 || o.requests.count("/media.m3u8") != 1 {
			t.Fatalf("fallback not taken once: dead=%d anchor=%d",
				o.requests.count("/dead.m3u8"), o.requests.count("/media.m3u8"))
		}
		if got.Segments[1].URI != o.url("/s1.ts") {
			t.Fatalf("URI not refreshed through the anchor: %q", got.Segments[1].URI)
		}
	})

	t.Run("transient failure degrades to the persisted table", func(t *testing.T) {
		o := newRunOrigin(t)
		o.mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		seed := resumeCompatibleSeed(o, []string{"s0.ts", "s1.ts"}, func(s *types.HLSResumeState) {
			s.MergedSegments = 1
			s.MergedBytes = 3
			s.VerifiedBytes = 3
			s.CompletedBitmap = []byte{0x01}
		})
		d := bootDownloader(o.srv)
		d.IsResume = true
		got, err := d.bootstrap(context.Background(), seed,
			mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
		if err != nil {
			t.Fatalf("bootstrap = %v, want degraded clone", err)
		}
		if !strings.HasSuffix(got.Segments[1].URI, "token=old") {
			t.Fatalf("degraded table lost its URIs: %q", got.Segments[1].URI)
		}
		if got.MergedSegments != 1 || got.VerifiedBytes != 3 {
			t.Fatalf("degraded table lost progress: %+v", got)
		}
	})

	t.Run("permanent manifest failure propagates", func(t *testing.T) {
		o := newRunOrigin(t)
		o.mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		seed := resumeCompatibleSeed(o, []string{"s0.ts", "s1.ts"}, func(s *types.HLSResumeState) {
			s.MergedSegments = 1
			s.MergedBytes = 3
			s.VerifiedBytes = 3
			s.CompletedBitmap = []byte{0x01}
		})
		d := bootDownloader(o.srv)
		d.IsResume = true
		_, err := d.bootstrap(context.Background(), seed,
			mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
		if !errors.Is(err, types.ErrPermanentHTTP) {
			t.Fatalf("bootstrap = %v, want ErrPermanentHTTP", err)
		}
	})

	t.Run("parent cancel propagates instead of degrading", func(t *testing.T) {
		o := newRunOrigin(t)
		release := make(chan struct{})
		o.mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
			<-release
			w.WriteHeader(http.StatusInternalServerError)
		})
		seed := resumeCompatibleSeed(o, []string{"s0.ts", "s1.ts"}, func(s *types.HLSResumeState) {
			s.MergedSegments = 1
			s.MergedBytes = 3
			s.VerifiedBytes = 3
			s.CompletedBitmap = []byte{0x01}
		})
		d := bootDownloader(o.srv)
		d.IsResume = true
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := d.bootstrap(ctx, seed,
				mustCredBase(t, seed.ManifestURL), nil, types.DefaultRuntimeConfig())
			done <- err
		}()
		cancel()
		close(release)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("bootstrap = %v, want context.Canceled", err)
		}
	})
}

func TestHLSBootstrap_ResumeRefreshCredentialAnchor(t *testing.T) {
	// The refresh chain must anchor credentials to the parsed original
	// ManifestURL: task Authorization reaches a.test but never b.test even
	// when the media entry lives there.
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
	d.IsResume = true

	seed := &types.HLSResumeState{
		ManifestURL:      "http://a.test/master.m3u8",
		MediaPlaylistURL: "http://a.test/master.m3u8",
		TotalSegments:    1,
		CompletedBitmap:  []byte{0x00},
		ContainerFormat:  "ts",
		MergedSegments:   0,
		VerifiedBytes:    1,
		Segments: []types.HLSSegmentState{{
			Index: 0, Sequence: 0, DurationMicros: 4_000_000,
			URI: "http://b.test/s0.ts?token=old",
		}},
	}
	got, err := d.bootstrap(context.Background(), seed,
		mustCredBase(t, seed.ManifestURL),
		map[string]string{"Authorization": "Bearer site-a"},
		types.DefaultRuntimeConfig())
	if err != nil {
		t.Fatalf("bootstrap = %v", err)
	}
	if aAuth.Load() == 0 {
		t.Fatal("anchor-site refresh lost the task Authorization")
	}
	if bAuth.Load() != 0 {
		t.Fatalf("Authorization leaked to the foreign host %d times", bAuth.Load())
	}
	if got.Segments[0].URI != "http://b.test/s0.ts" {
		t.Fatalf("refreshed URI = %q", got.Segments[0].URI)
	}
}
