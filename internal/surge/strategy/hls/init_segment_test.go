package hls

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
)

// initMount returns a mount stub holding an open .surge with initial
// contents — prepareInitSegment needs only out/seed/paths.
func initMount(t *testing.T, surgePath string, seed *types.HLSResumeState, contents []byte) *hlsMount {
	t.Helper()
	if contents != nil {
		if err := os.WriteFile(surgePath, contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := os.OpenFile(surgePath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	return &hlsMount{out: out, seed: seed, surgePath: surgePath}
}

// initDownloader wires the request-scoped fields prepareInitSegment reads.
func initDownloader(t *testing.T, id string, srv *httptest.Server, tweak func(*HlsDownloader)) *HlsDownloader {
	t.Helper()
	d := NewHlsDownloader(id, nil, nil, nil)
	d.runCfg = types.RuntimeConfig{}
	d.HostLimiter = transport.NewHostRateLimiter()
	if srv != nil {
		d.Client = srv.Client()
	}
	if tweak != nil {
		tweak(d)
	}
	return d
}

func initSeed(o *runOrigin, tweak func(*types.HLSResumeState)) *types.HLSResumeState {
	s := &types.HLSResumeState{
		ManifestURL:     o.url("/media.m3u8"),
		InitSegmentURI:  o.url("/init.mp4"),
		TotalSegments:   1,
		CompletedBitmap: []byte{0x00},
		Segments:        []types.HLSSegmentState{{Index: 0, URI: o.url("/s0.mp4")}},
	}
	if tweak != nil {
		tweak(s)
	}
	return s
}

func TestHLSInit_PlaintextFullGet(t *testing.T) {
	o := newRunOrigin(t)
	body := []byte("plain-init-head")
	o.serveBody("/init.mp4", body)
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	seed := initSeed(o, nil)
	d := initDownloader(t, "init-plain", o.srv, nil)
	mnt := initMount(t, surgePath, seed, nil)

	if err := d.prepareInitSegment(context.Background(), mnt); err != nil {
		t.Fatalf("prepareInitSegment = %v", err)
	}
	got, _ := os.ReadFile(surgePath)
	if string(got) != string(body) {
		t.Fatalf("head = %q, want %q", got, body)
	}
	if !seed.InitCompleted || seed.InitBytes != int64(len(body)) ||
		seed.MergedBytes != int64(len(body)) || seed.VerifiedBytes != seed.MergedBytes {
		t.Fatalf("counters = %+v", seed)
	}
}

func TestHLSInit_ByteRangeSentAndHonored(t *testing.T) {
	o := newRunOrigin(t)
	full := []byte("0123456789abcdefghij")
	var sawRange string
	o.mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		sawRange = r.Header.Get("Range")
		if sawRange == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var off, end int
		if _, err := fmt.Sscanf(sawRange, "bytes=%d-%d", &off, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, end, len(full)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(full[off : end+1])
	})
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	seed := initSeed(o, func(s *types.HLSResumeState) {
		s.InitByteOffset = 4
		s.InitByteLength = 6
	})
	d := initDownloader(t, "init-range", o.srv, nil)
	mnt := initMount(t, surgePath, seed, nil)

	if err := d.prepareInitSegment(context.Background(), mnt); err != nil {
		t.Fatalf("prepareInitSegment = %v", err)
	}
	if sawRange != "bytes=4-9" {
		t.Fatalf("Range header = %q, want bytes=4-9", sawRange)
	}
	got, _ := os.ReadFile(surgePath)
	if string(got) != "456789" {
		t.Fatalf("head = %q, want 456789", got)
	}
}

func TestHLSInit_RangeRequestHonoredWith200Rejected(t *testing.T) {
	o := newRunOrigin(t)
	// Origin ignores Range and answers 200 — the init would anchor at the
	// wrong offset, so the response is invalid.
	o.serveBody("/init.mp4", []byte("0123456789abcdefghij"))
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	seed := initSeed(o, func(s *types.HLSResumeState) {
		s.InitByteOffset = 4
		s.InitByteLength = 6
	})
	d := initDownloader(t, "init-range200", o.srv, nil)
	mnt := initMount(t, surgePath, seed, nil)

	err := d.prepareInitSegment(context.Background(), mnt)
	if !errors.Is(err, types.ErrHLSInvalidSegmentResponse) {
		t.Fatalf("prepareInitSegment = %v, want ErrHLSInvalidSegmentResponse", err)
	}
}

func TestHLSInit_EncryptedWithExplicitIVDecrypts(t *testing.T) {
	o := newRunOrigin(t)
	key, iv, ivHex := cbcKeyIV(t, 7)
	plain := []byte("encrypted-init-plain")
	o.serveBody("/init.mp4", cbcEncrypt(t, key, iv, plain))
	o.serveBody("/k.bin", key)
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	seed := initSeed(o, func(s *types.HLSResumeState) {
		s.InitKeyIndex = 1
		s.InitIVHex = ivHex
		s.Keys = []types.HLSKeyInfo{{Method: "AES-128", URI: o.url("/k.bin"), IVHex: ivHex}}
	})
	d := initDownloader(t, "init-enc", o.srv, nil)
	mnt := initMount(t, surgePath, seed, nil)

	if err := d.prepareInitSegment(context.Background(), mnt); err != nil {
		t.Fatalf("prepareInitSegment = %v", err)
	}
	got, _ := os.ReadFile(surgePath)
	if string(got) != string(plain) {
		t.Fatalf("head = %q, want decrypted %q", got, plain)
	}
	if n := o.requests.count("/k.bin"); n != 1 {
		t.Fatalf("key fetched %d times, want 1", n)
	}
}

func TestHLSInit_EncryptedWithoutIVRejected(t *testing.T) {
	o := newRunOrigin(t)
	key, iv, _ := cbcKeyIV(t, 9)
	o.serveBody("/init.mp4", cbcEncrypt(t, key, iv, []byte("data")))
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	seed := initSeed(o, func(s *types.HLSResumeState) {
		s.InitKeyIndex = 1 // bound to a key, but the IV never arrived
		s.Keys = []types.HLSKeyInfo{{Method: "AES-128", URI: o.url("/k.bin")}}
	})
	d := initDownloader(t, "init-noiv", o.srv, nil)
	mnt := initMount(t, surgePath, seed, nil)

	err := d.prepareInitSegment(context.Background(), mnt)
	if _, ok := errors.AsType[*segmentDataError](err); !ok {
		t.Fatalf("prepareInitSegment = %v (%T), want segmentDataError", err, err)
	}
	// No ciphertext may reach the file.
	got, _ := os.ReadFile(surgePath)
	if len(got) != 0 {
		t.Fatalf("ciphertext leaked to .surge: %d bytes", len(got))
	}
}

func TestHLSInit_Status403Permanent(t *testing.T) {
	o := newRunOrigin(t)
	o.mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	d := initDownloader(t, "init-403", o.srv, nil)
	mnt := initMount(t, surgePath, initSeed(o, nil), nil)

	err := d.prepareInitSegment(context.Background(), mnt)
	if !errors.Is(err, types.ErrPermanentHTTP) {
		t.Fatalf("prepareInitSegment = %v, want ErrPermanentHTTP", err)
	}
}

func TestHLSInit_Status429PenalizesHost(t *testing.T) {
	o := newRunOrigin(t)
	o.mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	d := initDownloader(t, "init-429", o.srv, nil)
	mnt := initMount(t, surgePath, initSeed(o, nil), nil)

	err := d.prepareInitSegment(context.Background(), mnt)
	if err == nil {
		t.Fatal("429 accepted as success")
	}
	if errors.Is(err, types.ErrPermanentHTTP) {
		t.Fatalf("429 classified permanent: %v", err)
	}
	host := transport.MirrorHost(o.url("/init.mp4"))
	if until := d.HostLimiter.BlockedUntil(host, time.Now()); until.Before(time.Now()) {
		t.Fatalf("host penalty not recorded (until=%v)", until)
	}
}

func TestHLSInit_SkipsBackedInit(t *testing.T) {
	o := newRunOrigin(t)
	backed := []byte("already-here")
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	seed := initSeed(o, func(s *types.HLSResumeState) {
		s.InitCompleted = true
		s.InitBytes = int64(len(backed))
		s.MergedBytes = s.InitBytes
		s.VerifiedBytes = s.InitBytes
	})
	d := initDownloader(t, "init-skip", o.srv, nil)
	mnt := initMount(t, surgePath, seed, backed)

	if err := d.prepareInitSegment(context.Background(), mnt); err != nil {
		t.Fatalf("prepareInitSegment = %v", err)
	}
	if n := o.requests.count("/init.mp4"); n != 0 {
		t.Fatalf("backed init refetched %d times", n)
	}
}

func TestHLSInit_RewriteSameLengthKeepsMediaPrefix(t *testing.T) {
	o := newRunOrigin(t)
	newInit := []byte("NEW-INIT-11") // exactly 11 bytes — same as the old init
	seg0 := []byte("seg0")
	o.serveBody("/init.mp4", newInit)
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	// A media prefix merged under an init of the same length stays provable —
	// only a length change shifts the boundary.
	old := append(append([]byte{}, []byte("OLD-INIT-00")[:11]...), seg0...)
	seed := initSeed(o, func(s *types.HLSResumeState) {
		s.InitBytes = 11
		s.CompletedBitmap = []byte{0x01}
		s.MergedSegments = 1
		s.MergedBytes = int64(len(old))
		s.VerifiedBytes = s.MergedBytes
	})
	d := initDownloader(t, "init-rewsame", o.srv, nil)
	mnt := initMount(t, surgePath, seed, old)

	if err := d.prepareInitSegment(context.Background(), mnt); err != nil {
		t.Fatalf("prepareInitSegment = %v", err)
	}
	if seed.MergedSegments != 1 {
		t.Fatalf("same-length rewrite dropped the media prefix: %+v", seed)
	}
	got, _ := os.ReadFile(surgePath)
	want := append(append([]byte{}, newInit...), seg0...)
	if string(got) != string(want) {
		t.Fatalf("file = %q, want %q", got, want)
	}
	if seed.MergedBytes != int64(len(want)) {
		t.Fatalf("MergedBytes = %d, want %d", seed.MergedBytes, len(want))
	}
}

func TestHLSInit_RewriteDifferentLengthClearsMediaPrefix(t *testing.T) {
	o := newRunOrigin(t)
	newInit := []byte("MUCH-LONGER-INIT-HEAD")
	seg0 := []byte("seg0")
	o.serveBody("/init.mp4", newInit)
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	old := append(append([]byte{}, []byte("SHORT-INIT")[:10]...), seg0...)
	seed := initSeed(o, func(s *types.HLSResumeState) {
		s.InitBytes = 10
		s.CompletedBitmap = []byte{0x01}
		s.MergedSegments = 1
		s.MergedBytes = int64(len(old))
		s.VerifiedBytes = s.MergedBytes
		s.SpillPath = "/tmp/old.spill"
		s.SpillFileSize = 4
		s.SpillEntries = []types.HLSSpillEntry{{SegmentIndex: 2, Offset: 0, Length: 4}}
	})
	d := initDownloader(t, "init-rewdiff", o.srv, nil)
	mnt := initMount(t, surgePath, seed, old)

	if err := d.prepareInitSegment(context.Background(), mnt); err != nil {
		t.Fatalf("prepareInitSegment = %v", err)
	}
	// A length change shifts the boundary: the prior byte layout is no
	// longer provable, so the media prefix must be cleared.
	if seed.MergedSegments != 0 || seed.CompletedBitmap[0] != 0 {
		t.Fatalf("media prefix survived a length change: %+v", seed)
	}
	if seed.SpillPath != "" || len(seed.SpillEntries) != 0 {
		t.Fatalf("spill bookkeeping survived: %+v", seed)
	}
	if seed.MergedBytes != int64(len(newInit)) || seed.InitBytes != int64(len(newInit)) {
		t.Fatalf("counters = %+v", seed)
	}
	got, _ := os.ReadFile(surgePath)
	if string(got[:len(newInit)]) != string(newInit) {
		t.Fatalf("head = %q, want %q", got[:len(newInit)], newInit)
	}
}

func TestHLSInit_TaskHeadersGatedNotForwarded(t *testing.T) {
	o := newRunOrigin(t)
	var gotRange, gotEncoding, gotAuth string
	o.mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotEncoding = r.Header.Get("Accept-Encoding")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("init"))
	})
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	d := initDownloader(t, "init-hdr", o.srv, func(d *HlsDownloader) {
		// A task-level Range must never leak into sub-requests; same-site
		// credentials do pass the gate.
		d.taskHeaders = map[string]string{
			"Range":         "bytes=0-1",
			"Authorization": "Bearer secret",
		}
	})
	mnt := initMount(t, surgePath, initSeed(o, nil), nil)

	if err := d.prepareInitSegment(context.Background(), mnt); err != nil {
		t.Fatalf("prepareInitSegment = %v", err)
	}
	if gotRange != "" {
		t.Fatalf("task Range leaked: %q", gotRange)
	}
	if gotEncoding != "identity" {
		t.Fatalf("Accept-Encoding = %q, want identity", gotEncoding)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("same-site Authorization = %q, want it forwarded", gotAuth)
	}
}

func TestHLSInit_EmptyBodyRejected(t *testing.T) {
	o := newRunOrigin(t)
	o.serveBody("/init.mp4", nil)
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	d := initDownloader(t, "init-empty", o.srv, nil)
	mnt := initMount(t, surgePath, initSeed(o, nil), nil)

	if err := d.prepareInitSegment(context.Background(), mnt); err == nil {
		t.Fatal("empty init body accepted")
	}
}

func TestHLSInit_TruncatedBodyRejected(t *testing.T) {
	o := newRunOrigin(t)
	// Declare more than is written — the read must hit the short-body gate.
	o.mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("tiny"))
	})
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	d := initDownloader(t, "init-trunc", o.srv, nil)
	mnt := initMount(t, surgePath, initSeed(o, nil), nil)

	err := d.prepareInitSegment(context.Background(), mnt)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("prepareInitSegment = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestHLSInit_IdentityEncodedBodyRejected(t *testing.T) {
	o := newRunOrigin(t)
	o.mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write([]byte("init"))
	})
	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	d := initDownloader(t, "init-gzip", o.srv, nil)
	mnt := initMount(t, surgePath, initSeed(o, nil), nil)

	if err := d.prepareInitSegment(context.Background(), mnt); !errors.Is(err, types.ErrHLSInvalidSegmentResponse) {
		t.Fatalf("prepareInitSegment = %v, want ErrHLSInvalidSegmentResponse", err)
	}
}

func TestHLSInit_IdleStallWatchdog(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(hold)

	dir := t.TempDir()
	surgePath := filepath.Join(dir, "v.mp4") + types.IncompleteSuffix
	seed := &types.HLSResumeState{
		ManifestURL:     srv.URL + "/media.m3u8",
		InitSegmentURI:  srv.URL + "/init.mp4",
		TotalSegments:   1,
		CompletedBitmap: []byte{0x00},
		Segments:        []types.HLSSegmentState{{Index: 0, URI: srv.URL + "/s0.mp4"}},
	}
	d := NewHlsDownloader("init-stall", nil, nil, nil)
	d.runCfg = types.RuntimeConfig{StallTimeout: 150 * time.Millisecond}
	d.HostLimiter = transport.NewHostRateLimiter()
	d.Client = srv.Client()
	mnt := initMount(t, surgePath, seed, nil)

	start := time.Now()
	err := d.prepareInitSegment(context.Background(), mnt)
	if err == nil {
		t.Fatal("hung init fetch succeeded")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("stall watchdog took %v — bound is ~%v", elapsed, d.runCfg.GetStallTimeout())
	}
}
