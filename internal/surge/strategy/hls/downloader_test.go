package hls

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// runOrigin is a VOD origin for downloader-level tests: a request-recording
// server plus a mux the test populates with playlist/segment/key handlers.
type runOrigin struct {
	srv      *httptest.Server
	requests *requestLog
	mux      *http.ServeMux
}

func newRunOrigin(t *testing.T) *runOrigin {
	t.Helper()
	o := &runOrigin{requests: &requestLog{}, mux: http.NewServeMux()}
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.requests.record(r)
		o.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(o.srv.Close)
	return o
}

func (o *runOrigin) url(path string) string { return o.srv.URL + path }

// serveMediaPlaylist registers a VOD playlist at path. tags are inserted
// between the header and the segment list (EXT-X-MAP / EXT-X-KEY belong
// there); segRefs may be relative names or absolute URLs.
func (o *runOrigin) serveMediaPlaylist(path string, segRefs []string, tags ...string) {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n")
	for _, tag := range tags {
		b.WriteString(tag + "\n")
	}
	for _, ref := range segRefs {
		b.WriteString("#EXTINF:4.0,\n" + ref + "\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	body := b.String()
	o.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(body))
	})
}

func (o *runOrigin) serveBody(path string, body []byte) {
	o.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
}

// cbcEncrypt produces the AES-128-CBC + PKCS7 ciphertext a compliant
// AES-128 HLS origin would serve for plain.
func cbcEncrypt(t *testing.T, key, iv, plain []byte) []byte {
	t.Helper()
	if len(iv) != aes.BlockSize {
		t.Fatalf("iv length %d", len(iv))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := make([]byte, len(plain)+pad)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out
}

func cbcKeyIV(t *testing.T, n int) (key, iv []byte, ivHex string) {
	t.Helper()
	key = make([]byte, aes.BlockSize)
	iv = make([]byte, aes.BlockSize)
	for i := range key {
		key[i] = byte(i + 1)
		iv[i] = byte(0xA0 + n + i)
	}
	return key, iv, hex.EncodeToString(iv)
}

// configureTestStore points the state backend at an isolated directory —
// every downloader-level run persists checkpoints through it.
func configureTestStore(t *testing.T) {
	t.Helper()
	store.Configure(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(store.CloseDB)
}

// collectEvents drains ch until the deadline, returning the observed types
// for id ("" matches every event).
func collectEvents(ch chan types.DownloadEvent, id string, window time.Duration) []types.DownloadEvent {
	var out []types.DownloadEvent
	deadline := time.After(window)
	for {
		select {
		case ev := <-ch:
			if id == "" || ev.DownloadID == id {
				out = append(out, ev)
			}
		case <-deadline:
			return out
		}
	}
}

func countEvent(evs []types.DownloadEvent, typ types.EventType) int {
	n := 0
	for _, ev := range evs {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

func newTestDownloader(id string, ch chan<- types.DownloadEvent, rt *types.RuntimeConfig) *HlsDownloader {
	if rt == nil {
		rt = types.DefaultRuntimeConfig()
	}
	return NewHlsDownloader(id, ch, progress.New(id, 0), rt)
}

func TestHLSDownloader_RunCompletesTS(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	segs := [][]byte{[]byte("seg-0-"), []byte("seg-1-"), []byte("seg-2-tail")}
	refs := []string{"s0.ts", "s1.ts", "s2.ts"}
	for i, body := range segs {
		o.serveBody("/"+refs[i], body)
	}
	o.serveMediaPlaylist("/media.m3u8", refs)

	ch := make(chan types.DownloadEvent, 16)
	prog := progress.New("run-ts", 0)
	d := NewHlsDownloader("run-ts", ch, prog, types.DefaultRuntimeConfig())
	d.Client = o.srv.Client()
	dest := filepath.Join(t.TempDir(), "video.ts")

	if err := d.Run(context.Background(), o.url("/media.m3u8"), dest); err != nil {
		t.Fatalf("Run = %v", err)
	}
	got, err := os.ReadFile(dest + types.IncompleteSuffix)
	if err != nil {
		t.Fatalf("read .surge: %v", err)
	}
	want := append(append(append([]byte{}, segs[0]...), segs[1]...), segs[2]...)
	if string(got) != string(want) {
		t.Fatalf(".surge = %q, want %q", got, want)
	}
	if d.TotalSize != int64(len(want)) {
		t.Fatalf("TotalSize = %d, want %d", d.TotalSize, len(want))
	}
	snap := d.ResumeSnapshot()
	if snap == nil || snap.MergedSegments != 3 || snap.VerifiedBytes != int64(len(want)) {
		t.Fatalf("ResumeSnapshot = %+v", snap)
	}
	if got, _, _, _, _, _ := prog.GetProgress(); got != int64(len(want)) {
		t.Fatalf("progress downloaded = %d, want %d", got, len(want))
	}
	// Total stays unknown inside the downloader — only the scheduler's
	// copy-back publishes the physical size.
	if prog.Bytes.TotalSize.Load() != 0 {
		t.Fatalf("progress total = %d, want 0 (unknown until scheduler copy-back)", prog.Bytes.TotalSize.Load())
	}
	evs := collectEvents(ch, "run-ts", 100*time.Millisecond)
	if n := countEvent(evs, types.EventFirstByte); n != 1 {
		t.Fatalf("FirstByte events = %d, want exactly 1", n)
	}
	if countEvent(evs, types.EventComplete)+countEvent(evs, types.EventError) != 0 {
		t.Fatal("downloader emitted a lifecycle event owned by the scheduler")
	}
}

func TestHLSDownloader_RunResumesMergedPrefix(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	segs := [][]byte{[]byte("AAA"), []byte("BBB"), []byte("CCC")}
	refs := []string{"s0.ts", "s1.ts", "s2.ts"}
	for i, body := range segs {
		o.serveBody("/"+refs[i], body)
	}
	playlistURL := o.url("/media.m3u8")
	o.serveMediaPlaylist("/media.m3u8", refs)

	// Pre-seeded prefix: seg0 already merged and backed by the file.
	dir := t.TempDir()
	dest := filepath.Join(dir, "video.ts")
	if err := os.WriteFile(dest+types.IncompleteSuffix, segs[0], 0o644); err != nil {
		t.Fatalf("write .surge: %v", err)
	}
	seed := &types.HLSResumeState{
		ManifestURL:      playlistURL,
		MediaPlaylistURL: playlistURL,
		TotalSegments:    3,
		CompletedBitmap:  []byte{0x01},
		MergedSegments:   1,
		MergedBytes:      int64(len(segs[0])),
		VerifiedBytes:    int64(len(segs[0])),
		RecordEligible:   true,
		ContainerFormat:  "ts",
		Segments: []types.HLSSegmentState{
			{Index: 0, Sequence: 0, DurationMicros: 4_000_000, URI: o.url("/s0.ts")},
			{Index: 1, Sequence: 1, DurationMicros: 4_000_000, URI: o.url("/s1.ts")},
			{Index: 2, Sequence: 2, DurationMicros: 4_000_000, URI: o.url("/s2.ts")},
		},
	}

	ch := make(chan types.DownloadEvent, 16)
	d := NewHlsDownloader("run-resume", ch, progress.New("run-resume", 0), types.DefaultRuntimeConfig())
	d.Client = o.srv.Client()
	d.HLSResume = seed
	d.IsResume = true

	if err := d.Run(context.Background(), playlistURL, dest); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if n := o.requests.count("/s0.ts"); n != 0 {
		t.Fatalf("seg0 refetched %d times — merged prefix was not trusted", n)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	want := append(append(append([]byte{}, segs[0]...), segs[1]...), segs[2]...)
	if string(got) != string(want) {
		t.Fatalf(".surge = %q, want %q", got, want)
	}
	evs := collectEvents(ch, "run-resume", 100*time.Millisecond)
	if n := countEvent(evs, types.EventFirstByte); n != 0 {
		t.Fatalf("FirstByte emitted %d times on a resume — want 0", n)
	}
}

func TestHLSDownloader_RunZeroNetworkSeed(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	segs := [][]byte{[]byte("AA"), []byte("BB")}
	playlistURL := o.url("/media.m3u8")

	dir := t.TempDir()
	dest := filepath.Join(dir, "video.ts")
	full := append(append([]byte{}, segs[0]...), segs[1]...)
	if err := os.WriteFile(dest+types.IncompleteSuffix, full, 0o644); err != nil {
		t.Fatalf("write .surge: %v", err)
	}
	seed := &types.HLSResumeState{
		ManifestURL:      playlistURL,
		MediaPlaylistURL: playlistURL,
		TotalSegments:    2,
		CompletedBitmap:  []byte{0x03},
		MergedSegments:   2,
		MergedBytes:      int64(len(full)),
		VerifiedBytes:    int64(len(full)),
		RecordEligible:   true,
		Segments: []types.HLSSegmentState{
			{Index: 0, URI: o.url("/s0.ts")},
			{Index: 1, URI: o.url("/s1.ts")},
		},
	}
	ch := make(chan types.DownloadEvent, 16)
	d := NewHlsDownloader("run-zero", ch, progress.New("run-zero", 0), types.DefaultRuntimeConfig())
	d.Client = o.srv.Client()
	d.HLSResume = seed
	d.IsResume = true

	if err := d.Run(context.Background(), playlistURL, dest); err != nil {
		t.Fatalf("Run = %v", err)
	}
	// A zero-network completion verifies the file — it must not refetch
	// the manifest or any segment.
	for _, p := range []string{"/media.m3u8", "/s0.ts", "/s1.ts"} {
		if n := o.requests.count(p); n != 0 {
			t.Fatalf("%s requested %d times on a zero-network mount", p, n)
		}
	}
	if d.TotalSize != int64(len(full)) {
		t.Fatalf("TotalSize = %d, want %d", d.TotalSize, len(full))
	}
	if n := countEvent(collectEvents(ch, "run-zero", 50*time.Millisecond), types.EventFirstByte); n != 0 {
		t.Fatalf("FirstByte on zero-network run = %d, want 0", n)
	}
}

func TestHLSDownloader_RunMasterSelectsBestVariant(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	segs := [][]byte{[]byte("hi0"), []byte("hi1")}
	refs := []string{"h0.ts", "h1.ts"}
	for i, body := range segs {
		o.serveBody("/"+refs[i], body)
	}
	o.serveMediaPlaylist("/hi.m3u8", refs)
	o.serveMediaPlaylist("/lo.m3u8", []string{"l0.ts"})
	o.serveBody("/l0.ts", []byte("lo"))
	master := "#EXTM3U\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=100000,RESOLUTION=320x180,CODECS=\"avc1.42e01e,mp4a.40.2\"\nlo.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=640x360,CODECS=\"avc1.42e01e,mp4a.40.2\"\nhi.m3u8\n"
	o.mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(master))
	})

	d := newTestDownloader("run-master", nil, nil)
	d.Client = o.srv.Client()
	dest := filepath.Join(t.TempDir(), "video.ts")
	if err := d.Run(context.Background(), o.url("/master.m3u8"), dest); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if n := o.requests.count("/hi.m3u8"); n != 1 {
		t.Fatalf("hi variant fetched %d times, want 1", n)
	}
	if n := o.requests.count("/l0.ts"); n != 0 {
		t.Fatalf("low-bandwidth variant segments fetched %d times", n)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	if string(got) != "hi0hi1" {
		t.Fatalf(".surge = %q, want hi variant payload", got)
	}
	if snap := d.ResumeSnapshot(); snap == nil || snap.MediaPlaylistURL == "" {
		t.Fatalf("snapshot missing media playlist anchor: %+v", snap)
	}
}

func TestHLSDownloader_RunFMP4InitAndSegments(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	init := []byte("INIT-MP4-HEAD")
	segs := [][]byte{[]byte("moof-0"), []byte("moof-1")}
	refs := []string{"f0.mp4", "f1.mp4"}
	for i, body := range segs {
		o.serveBody("/"+refs[i], body)
	}
	o.serveBody("/init.mp4", init)
	o.serveMediaPlaylist("/vod.m3u8", refs, "#EXT-X-MAP:URI=\"init.mp4\"")

	d := newTestDownloader("run-fmp4", nil, nil)
	d.Client = o.srv.Client()
	dest := filepath.Join(t.TempDir(), "video.mp4")
	if err := d.Run(context.Background(), o.url("/vod.m3u8"), dest); err != nil {
		t.Fatalf("Run = %v", err)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	want := append(append(append([]byte{}, init...), segs[0]...), segs[1]...)
	if string(got) != string(want) {
		t.Fatalf(".surge = %q, want init+segments %q", got, want)
	}
	snap := d.ResumeSnapshot()
	if !snap.InitCompleted || snap.InitBytes != int64(len(init)) {
		t.Fatalf("init accounting = %+v", snap)
	}
}

func TestHLSDownloader_RunEncryptedTS(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	key, iv, ivHex := cbcKeyIV(t, 0)
	plain := [][]byte{[]byte("clear-seg-0"), []byte("clear-seg-1")}
	refs := make([]string, 0, len(plain))
	for i, p := range plain {
		ref := fmt.Sprintf("e%d.ts", i)
		o.serveBody("/"+ref, cbcEncrypt(t, key, iv, p))
		refs = append(refs, ref)
	}
	o.serveBody("/k.bin", key)
	o.serveMediaPlaylist("/vod.m3u8", refs,
		"#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\",IV=0x"+ivHex)

	d := newTestDownloader("run-aes", nil, nil)
	d.Client = o.srv.Client()
	dest := filepath.Join(t.TempDir(), "video.ts")
	if err := d.Run(context.Background(), o.url("/vod.m3u8"), dest); err != nil {
		t.Fatalf("Run = %v", err)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	want := append(append([]byte{}, plain[0]...), plain[1]...)
	if string(got) != string(want) {
		t.Fatalf(".surge = %q, want decrypted %q", got, want)
	}
	if n := o.requests.count("/k.bin"); n < 1 {
		t.Fatalf("key fetched %d times, want >= 1", n)
	}
}

func TestHLSDownloader_RunEncryptedInitFMP4(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	key, iv, ivHex := cbcKeyIV(t, 3)
	initPlain := []byte("ENC-INIT-BLOCK")
	segs := [][]byte{[]byte("em0"), []byte("em1")}
	// Encrypted init + plaintext segments share the same explicit IV; the
	// init binds the playlist's key descriptor, never a media sequence.
	o.serveBody("/einit.mp4", cbcEncrypt(t, key, iv, initPlain))
	refs := make([]string, 0, len(segs))
	for i, body := range segs {
		ref := fmt.Sprintf("es%d.mp4", i)
		o.serveBody("/"+ref, cbcEncrypt(t, key, iv, body))
		refs = append(refs, ref)
	}
	o.serveBody("/ek.bin", key)
	o.serveMediaPlaylist("/evod.m3u8", refs,
		"#EXT-X-KEY:METHOD=AES-128,URI=\"ek.bin\",IV=0x"+ivHex,
		"#EXT-X-MAP:URI=\"einit.mp4\"")

	d := newTestDownloader("run-einit", nil, nil)
	d.Client = o.srv.Client()
	dest := filepath.Join(t.TempDir(), "video.mp4")
	if err := d.Run(context.Background(), o.url("/evod.m3u8"), dest); err != nil {
		t.Fatalf("Run = %v", err)
	}
	got, _ := os.ReadFile(dest + types.IncompleteSuffix)
	want := append(append(append([]byte{}, initPlain...), segs[0]...), segs[1]...)
	if string(got) != string(want) {
		t.Fatalf(".surge = %q, want decrypted init+segments %q", got, want)
	}
}

func TestHLSDownloader_RunPreCanceled(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts"})
	d := newTestDownloader("run-canceled", nil, nil)
	d.Client = o.srv.Client()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.Run(ctx, o.url("/media.m3u8"), filepath.Join(t.TempDir(), "v.ts"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if n := o.requests.count("/media.m3u8"); n != 0 {
		t.Fatalf("pre-canceled run still fetched the manifest %d times", n)
	}
}

func TestHLSDownloader_RunTwiceIsContractError(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	o.serveBody("/s0.ts", []byte("x"))
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts"})
	d := newTestDownloader("run-twice", nil, nil)
	d.Client = o.srv.Client()
	dest := filepath.Join(t.TempDir(), "v.ts")
	if err := d.Run(context.Background(), o.url("/media.m3u8"), dest); err != nil {
		t.Fatalf("first Run = %v", err)
	}
	err := d.Run(context.Background(), o.url("/media.m3u8"), dest)
	if err == nil {
		t.Fatal("second Run returned nil")
	}
	if !strings.Contains(err.Error(), "twice") && !strings.Contains(err.Error(), "contract") {
		t.Fatalf("second Run error %q does not look like a contract violation", err)
	}
}

func TestHLSDownloader_RunInitPermanentStatus(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	o.mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	o.serveBody("/f0.mp4", []byte("f0"))
	o.serveMediaPlaylist("/vod.m3u8", []string{"f0.mp4"}, "#EXT-X-MAP:URI=\"init.mp4\"")

	d := newTestDownloader("run-init403", nil, nil)
	d.Client = o.srv.Client()
	err := d.Run(context.Background(), o.url("/vod.m3u8"), filepath.Join(t.TempDir(), "v.mp4"))
	if !errors.Is(err, types.ErrPermanentHTTP) {
		t.Fatalf("Run = %v, want ErrPermanentHTTP", err)
	}
	if n := o.requests.count("/f0.mp4"); n != 0 {
		t.Fatalf("media fetched %d times despite permanent init failure", n)
	}
}

func TestHLSDownloader_RunCancelMidFlight(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	gate := make(chan struct{})
	o.serveBody("/s0.ts", []byte("first"))
	o.mux.HandleFunc("/s1.ts", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("second"))
	})
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})

	d := newTestDownloader("run-midcancel", nil, nil)
	d.Client = o.srv.Client()
	dir := t.TempDir()
	dest := filepath.Join(dir, "v.ts")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, o.url("/media.m3u8"), dest) }()

	deadline := time.Now().Add(3 * time.Second)
	for o.requests.count("/s1.ts") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("s1 never reached the server")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not settle after cancel")
	}
	close(gate)
	// Teardown must leave no open handle: deleting the .surge succeeds on
	// every platform only when window+file are really closed.
	if err := os.Remove(dest + types.IncompleteSuffix); err != nil {
		t.Fatalf(".surge still held after cancel: %v", err)
	}
}

func TestHLSDownloader_WorkerBridgesDuringRun(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	gate := make(chan struct{})
	var gateOnce sync.Once
	// Unblock the parked s1 handler on any failure path, or the origin's
	// httptest.Server.Close wedges until the test timeout — the run uses
	// context.Background, so r.Context().Done() can never release it;
	// defer unwinds before any t.Cleanup, so the fixture's own
	// registration order can never strand this behind srv.Close.
	defer gateOnce.Do(func() { close(gate) })
	o.serveBody("/s0.ts", []byte("s0"))
	o.mux.HandleFunc("/s1.ts", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("s1"))
	})
	o.serveBody("/s2.ts", []byte("s2"))
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts", "s2.ts"})

	prog := progress.New("run-bridges", 0)
	d := NewHlsDownloader("run-bridges", nil, prog, types.DefaultRuntimeConfig())
	d.Client = o.srv.Client()
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background(), o.url("/media.m3u8"), filepath.Join(t.TempDir(), "v.ts")) }()

	deadline := time.Now().Add(3 * time.Second)
	var stats []types.WorkerSnapshot
	for len(stats) == 0 {
		stats = prog.GetWorkerStats()
		if time.Now().After(deadline) {
			t.Fatal("worker stats never published")
		}
		time.Sleep(time.Millisecond)
	}
	for _, s := range stats {
		if s.ChunkStart != 0 || s.ChunkOffset != 0 || s.ChunkLength != 0 {
			t.Fatalf("HLS snapshot leaked byte-range chunk fields: %+v", s)
		}
	}
	if got := prog.Bytes.TotalSize.Load(); got != 0 {
		t.Fatalf("mid-run total = %d, want 0", got)
	}
	// Bridges: scale up is bounded by feedable work, kill/drain answer by
	// identity, the threshold setter validates input.
	added := d.ScaleWorkers(2)
	if added < 0 {
		t.Fatalf("ScaleWorkers(+2) = %d", added)
	}
	if d.KillWorker(1 << 20) {
		t.Fatal("KillWorker accepted an unknown id")
	}
	d.SetSlowWorkerThreshold(0.5)
	d.SetSlowWorkerThreshold(-1)   // out of range — ignored, must not panic
	d.SetSlowWorkerThreshold(1e99) // >1 — ignored
	gateOnce.Do(func() { close(gate) })
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	// Post-detach every bridge is inert.
	if got := d.ScaleWorkers(1); got != 0 {
		t.Fatalf("post-detach ScaleWorkers = %d", got)
	}
	if d.KillWorker(0) || d.DrainWorker(0) {
		t.Fatal("post-detach kill/drain acted")
	}
	if stats := prog.GetWorkerStats(); len(stats) != 0 {
		t.Fatalf("post-detach stats = %d entries", len(stats))
	}
}
