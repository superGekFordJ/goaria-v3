package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
)

// hlsFixture wires a real ReorderWindow over a temp output, a recording
// httptest origin, and the production engine path (execMediaAttempt).
type hlsFixture struct {
	dir      string
	outPath  string
	out      *os.File
	window   *ReorderWindow
	prog     *progress.DownloadProgress
	seed     *types.HLSResumeState
	srv      *httptest.Server
	engine   *segmentEngine
	requests *requestLog
}

type requestLog struct {
	mu    sync.Mutex
	paths []string
	last  map[string]http.Header
}

func (l *requestLog) record(r *http.Request) {
	l.mu.Lock()
	l.paths = append(l.paths, r.URL.Path)
	if l.last == nil {
		l.last = make(map[string]http.Header)
	}
	l.last[r.URL.Path] = r.Header.Clone()
	l.mu.Unlock()
}

func (l *requestLog) count(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, p := range l.paths {
		if p == path {
			n++
		}
	}
	return n
}

type fixtureOpts struct {
	segTweak   func(i int, seg *types.HLSSegmentState)
	key        []byte // when set, /key.bin serves it and segments get KeyIndex=1
	keys       []types.HLSKeyInfo
	handler    http.HandlerFunc // full override
	ops        *fileOps
	headers    map[string]string
	client     *http.Client // nil uses the fixture server client
	limiter    types.ByteLimiter
	hostLim    *transport.HostRateLimiter
	onAccepted func(index int, res IngestResult, started time.Time)
	sleep      func(ctx context.Context, d time.Duration) error
	now        func() time.Time
}

// newHLSFixture builds n segments whose URIs hit the recording server; each
// plaintext body is produced by bodies[i] (already the exact bytes the origin
// must return — ciphertext when key is set).
func newHLSFixture(t *testing.T, bodies [][]byte, opts fixtureOpts) *hlsFixture {
	t.Helper()
	f := &hlsFixture{requests: &requestLog{}}
	n := len(bodies)

	mux := http.NewServeMux()
	for i := range bodies {
		body := bodies[i]
		mux.HandleFunc(fmt.Sprintf("/seg%d", i), func(w http.ResponseWriter, r *http.Request) {
			f.requests.record(r)
			_, _ = w.Write(body)
		})
	}
	if opts.key != nil {
		key := opts.key
		mux.HandleFunc("/key.bin", func(w http.ResponseWriter, r *http.Request) {
			f.requests.record(r)
			_, _ = w.Write(key)
		})
	}
	if opts.handler != nil {
		f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.requests.record(r)
			opts.handler(w, r)
		}))
	} else {
		f.srv = httptest.NewServer(mux)
	}
	t.Cleanup(f.srv.Close)

	f.dir = t.TempDir()
	f.outPath = filepath.Join(f.dir, "media.surge")
	out, err := os.Create(f.outPath)
	if err != nil {
		t.Fatalf("create out: %v", err)
	}
	f.out = out
	t.Cleanup(func() { _ = f.out.Close() })

	f.seed = &types.HLSResumeState{
		ManifestURL:     f.srv.URL + "/media.m3u8",
		TotalSegments:   n,
		CompletedBitmap: make([]byte, (n+7)/8),
		ContainerFormat: "ts",
	}
	if opts.key != nil {
		f.seed.Keys = []types.HLSKeyInfo{{Method: "AES-128", URI: f.srv.URL + "/key.bin"}}
	}
	if opts.keys != nil {
		f.seed.Keys = opts.keys
	}
	segs := make([]types.HLSSegmentState, n)
	for i := range segs {
		segs[i] = types.HLSSegmentState{
			Index: i, Sequence: int64(i), DurationMicros: 4_000_000,
			URI: fmt.Sprintf("%s/seg%d", f.srv.URL, i),
		}
		if opts.key != nil {
			segs[i].KeyIndex = 1
		}
		if opts.segTweak != nil {
			opts.segTweak(i, &segs[i])
		}
	}
	f.seed.Segments = segs

	w, err := newReorderWindow(f.out, f.outPath, "fixdl", f.seed, opts.ops)
	if err != nil {
		t.Fatalf("newReorderWindow = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	f.window = w
	f.prog = progress.New("fixture", 0)
	return f
}

func (f *hlsFixture) buildEngine(t *testing.T, opts fixtureOpts) *segmentEngine {
	t.Helper()
	client := opts.client
	if client == nil {
		client = f.srv.Client()
	}
	e, err := newSegmentEngine(segmentEngineParams{
		Seed:        f.seed,
		Window:      f.window,
		Progress:    f.prog,
		Client:      client,
		RunCfg:      types.DefaultRuntimeConfig(),
		TaskHeaders: opts.headers,
		Limiter:     opts.limiter,
		HostLimiter: opts.hostLim,
		Parent:      context.Background(),
		MaxWorkers:  8,
		OnAccepted:  opts.onAccepted,
		sleep:       opts.sleep,
		now:         opts.now,
	})
	if err != nil {
		t.Fatalf("newSegmentEngine = %v", err)
	}
	f.engine = e
	return e
}

func (f *hlsFixture) output(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(f.outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return data
}

func concat(bodies [][]byte) []byte {
	var out []byte
	for _, b := range bodies {
		out = append(out, b...)
	}
	return out
}

func TestHLSWorkerSegmentPlainEndToEnd(t *testing.T) {
	bodies := [][]byte{
		bytes.Repeat([]byte("a0"), 300),
		bytes.Repeat([]byte("b1"), 700),
		bytes.Repeat([]byte("c2"), 1200),
	}
	f := newHLSFixture(t, bodies, fixtureOpts{})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); !bytes.Equal(got, concat(bodies)) {
		t.Fatalf("output %d bytes != concat %d bytes", len(got), len(concat(bodies)))
	}
	want := int64(len(concat(bodies)))
	if d := f.prog.Bytes.Downloaded.Load(); d != want {
		t.Fatalf("Downloaded = %d, want %d", d, want)
	}
	if v := f.prog.Bytes.VerifiedProgress.Load(); v != want {
		t.Fatalf("VerifiedProgress = %d, want %d", v, want)
	}
}

func TestHLSWorkerSegmentSizesAroundBatch(t *testing.T) {
	for _, size := range []int{1, 63 * 1024, 64 * 1024, 64*1024 + 1, 200 * 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			bodies := [][]byte{bytes.Repeat([]byte{0x5a}, size)}
			f := newHLSFixture(t, bodies, fixtureOpts{})
			e := f.buildEngine(t, fixtureOpts{})
			if err := e.start(1); err != nil {
				t.Fatalf("start = %v", err)
			}
			if err := e.wait(); err != nil {
				t.Fatalf("wait = %v", err)
			}
			if got := f.output(t); !bytes.Equal(got, bodies[0]) {
				t.Fatalf("size %d: output mismatch (%d bytes)", size, len(got))
			}
		})
	}
}

func TestHLSWorkerSegmentEncryptedPadAccounting(t *testing.T) {
	key := []byte{
		0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
		0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff,
	}
	plain := bytes.Repeat([]byte("payload-"), 50)[:397]
	iv := make([]byte, 16)
	ct := encryptTestCBC(t, key, iv, plain)
	f := newHLSFixture(t, [][]byte{ct}, fixtureOpts{key: key})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); !bytes.Equal(got, plain) {
		t.Fatalf("decrypted output %d bytes, want %d plaintext bytes", len(got), len(plain))
	}
	// Progress reflects the plaintext (pad deducted once).
	if d := f.prog.Bytes.Downloaded.Load(); d != int64(len(plain)) {
		t.Fatalf("Downloaded = %d, want %d plaintext", d, len(plain))
	}
	if f.requests.count("/key.bin") != 1 {
		t.Fatalf("key fetched %d times", f.requests.count("/key.bin"))
	}
}

func TestHLSWorkerSegmentByteRange206(t *testing.T) {
	full := bytes.Repeat([]byte("z"), 4096)
	f := newHLSFixture(t, [][]byte{full}, fixtureOpts{
		segTweak: func(i int, seg *types.HLSSegmentState) {
			seg.ByteOffset = 128
			seg.ByteLength = 1024
		},
		handler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/seg0" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if got := r.Header.Get("Range"); got != "bytes=128-1151" {
				http.Error(w, "bad range "+got, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Range", "bytes 128-1151/4096")
			w.Header().Set("Content-Length", "1024")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(full[128:1152])
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); !bytes.Equal(got, full[128:1152]) {
		t.Fatalf("ranged output %d bytes", len(got))
	}
}

func TestHLSWorkerSegmentResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		handler    http.HandlerFunc
		wantErrIs  error
		wantStatus bool
	}{
		{"RangedGot200", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(make([]byte, 64))
		}, types.ErrHLSInvalidSegmentResponse, false},
		{"RangedBadContentRange", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-63/999")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(make([]byte, 64))
		}, types.ErrHLSInvalidSegmentResponse, false},
		{"GzipEncoding", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(make([]byte, 64))
		}, types.ErrHLSInvalidSegmentResponse, false},
		{"Multipart", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "multipart/byteranges; boundary=x")
			_, _ = w.Write(make([]byte, 64))
		}, types.ErrHLSInvalidSegmentResponse, false},
		{"Created201", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}, types.ErrHLSInvalidSegmentResponse, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{
				segTweak: func(i int, seg *types.HLSSegmentState) {
					if strings.HasPrefix(tc.name, "Ranged") {
						seg.ByteOffset = 64
						seg.ByteLength = 64
					}
				},
				handler: tc.handler,
			})
			e := f.buildEngine(t, fixtureOpts{})
			if err := e.start(1); err != nil {
				t.Fatalf("start = %v", err)
			}
			err := e.wait()
			if !errors.Is(err, tc.wantErrIs) {
				t.Fatalf("wait = %v, want %v", err, tc.wantErrIs)
			}
			// Permanent-response classes never retry.
			if n := f.requests.count("/seg0"); n != 1 {
				t.Fatalf("requests = %d, want exactly 1 (no retries)", n)
			}
		})
	}
}

func TestHLSWorkerSegmentTruncatedBodyRetries(t *testing.T) {
	full := bytes.Repeat([]byte("k"), 3000)
	var calls atomic.Int64
	f := newHLSFixture(t, [][]byte{full}, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				// Promise more than delivered — the read dies mid-body.
				w.Header().Set("Content-Length", "3000")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(full[:1200])
				return
			}
			_, _ = w.Write(full)
		},
	})
	e := f.buildEngine(t, fixtureOpts{
		sleep: func(ctx context.Context, d time.Duration) error { return nil },
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("requests = %d, want 2", calls.Load())
	}
	if got := f.output(t); !bytes.Equal(got, full) {
		t.Fatal("output mismatch after truncated retry")
	}
	task := e.queue.tasks[0]
	if task.retries != 1 || task.genericAttempt != 1 {
		t.Fatalf("retries=%d generic=%d", task.retries, task.genericAttempt)
	}
}

func TestHLSWorkerSegmentDuplicateNoDoubleCount(t *testing.T) {
	bodies := [][]byte{[]byte("first"), []byte("second")}
	f := newHLSFixture(t, bodies, fixtureOpts{})
	// Pre-admit segment 0 directly so the engine's attempt dedupes.
	res, err := f.window.Ingest(0, bodies[0])
	if err != nil || !res.Accepted || !res.Direct {
		t.Fatalf("pre-ingest = %+v, %v", res, err)
	}
	var progressDuring atomic.Int64
	e := f.buildEngine(t, fixtureOpts{
		onAccepted: func(index int, res IngestResult, started time.Time) {
			progressDuring.Store(f.prog.Bytes.VerifiedProgress.Load())
		},
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); !bytes.Equal(got, concat(bodies)) {
		t.Fatalf("output = %q", got)
	}
	// The duplicate attempt's credit rolled fully back: only seg1 bytes count.
	want := int64(len(bodies[1]))
	if v := f.prog.Bytes.VerifiedProgress.Load(); v != want {
		t.Fatalf("VerifiedProgress = %d, want %d (duplicate not counted)", v, want)
	}
	if f.requests.count("/seg0") != 1 {
		t.Fatalf("seg0 fetched %d times", f.requests.count("/seg0"))
	}
}

func TestHLSWorkerSegmentOutOfOrderConcurrent(t *testing.T) {
	bodies := [][]byte{
		[]byte("seg-zero-"),
		[]byte("seg-one--"),
		[]byte("seg-two--"),
		[]byte("seg-three"),
	}
	releaseFirst := make(chan struct{})
	f := newHLSFixture(t, bodies, fixtureOpts{
		handler: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/seg0" {
				<-releaseFirst // hold seg0 so 1..3 ingest first
			}
			var i int
			_, _ = fmt.Sscanf(r.URL.Path, "/seg%d", &i)
			_, _ = w.Write(bodies[i])
		},
	})
	e := f.buildEngine(t, fixtureOpts{})
	if err := e.start(4); err != nil {
		t.Fatalf("start = %v", err)
	}
	// Let the out-of-order ingest land first.
	time.Sleep(100 * time.Millisecond)
	close(releaseFirst)
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	if got := f.output(t); !bytes.Equal(got, concat(bodies)) {
		t.Fatalf("ordered output = %q", got)
	}
	snap := f.window.Snapshot()
	if snap.MergedSegments != 4 {
		t.Fatalf("MergedSegments = %d", snap.MergedSegments)
	}
}

func TestHLSWorkerSegmentRangedOversizeIsFatal(t *testing.T) {
	f := newHLSFixture(t, [][]byte{nil}, fixtureOpts{
		segTweak: func(i int, seg *types.HLSSegmentState) {
			seg.ByteOffset = 0
			seg.ByteLength = 10
		},
		handler: func(w http.ResponseWriter, r *http.Request) {
			// Server honors Range but ships extra bytes — protocol violation.
			w.Header().Set("Content-Range", "bytes 0-9/100")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(make([]byte, 10+5))
		},
	})
	e := f.buildEngine(t, fixtureOpts{
		sleep: func(ctx context.Context, d time.Duration) error { return nil },
	})
	if err := e.start(1); err != nil {
		t.Fatalf("start = %v", err)
	}
	err := e.wait()
	if !errors.Is(err, types.ErrHLSInvalidSegmentResponse) {
		t.Fatalf("wait = %v, want ErrHLSInvalidSegmentResponse", err)
	}
}

func TestHLSWorkerSegmentKeyRotation(t *testing.T) {
	key1 := bytes.Repeat([]byte{0x01}, 16)
	key2 := bytes.Repeat([]byte{0x02}, 16)
	plain1 := []byte("first-segment-payload")
	plain2 := []byte("second-segment-payload!")
	// Two key slots, one segment bound to each — each key URI fetched once.
	// Implicit IVs come from the media sequence: seg0 → all-zero, seg1 → 1.
	iv0 := make([]byte, 16)
	iv1 := append([]byte(nil), iv0...)
	iv1[15] = 1
	bodies := [][]byte{
		encryptTestCBC(t, key1, iv0, plain1),
		encryptTestCBC(t, key2, iv1, plain2),
	}
	mux := http.NewServeMux()
	log := &requestLog{}
	for i, b := range bodies {
		mux.HandleFunc(fmt.Sprintf("/seg%d", i), func(w http.ResponseWriter, r *http.Request) {
			log.record(r)
			_, _ = w.Write(b)
		})
	}
	mux.HandleFunc("/k1.bin", func(w http.ResponseWriter, r *http.Request) { log.record(r); _, _ = w.Write(key1) })
	mux.HandleFunc("/k2.bin", func(w http.ResponseWriter, r *http.Request) { log.record(r); _, _ = w.Write(key2) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "o.surge")
	out, _ := os.Create(outPath)
	defer out.Close()
	seed := &types.HLSResumeState{
		ManifestURL:     srv.URL + "/m.m3u8",
		TotalSegments:   2,
		CompletedBitmap: make([]byte, 1),
		Keys: []types.HLSKeyInfo{
			{Method: "AES-128", URI: srv.URL + "/k1.bin"},
			{Method: "AES-128", URI: srv.URL + "/k2.bin"},
		},
		Segments: []types.HLSSegmentState{
			{Index: 0, Sequence: 0, URI: srv.URL + "/seg0", KeyIndex: 1},
			{Index: 1, Sequence: 1, URI: srv.URL + "/seg1", KeyIndex: 2},
		},
	}
	w, err := newReorderWindow(out, outPath, "krot", seed, nil)
	if err != nil {
		t.Fatalf("window = %v", err)
	}
	defer w.Close()
	prog := progress.New("krot", 0)
	e, err := newSegmentEngine(segmentEngineParams{
		Seed: seed, Window: w, Progress: prog, Client: srv.Client(),
		RunCfg: types.DefaultRuntimeConfig(), Parent: context.Background(), MaxWorkers: 4,
	})
	if err != nil {
		t.Fatalf("engine = %v", err)
	}
	if err := e.start(2); err != nil {
		t.Fatalf("start = %v", err)
	}
	if err := e.wait(); err != nil {
		t.Fatalf("wait = %v", err)
	}
	data, _ := os.ReadFile(outPath)
	if !bytes.Equal(data, append(append([]byte{}, plain1...), plain2...)) {
		t.Fatalf("rotated output = %q", data)
	}
	if log.count("/k1.bin") != 1 || log.count("/k2.bin") != 1 {
		t.Fatalf("key fetches: k1=%d k2=%d", log.count("/k1.bin"), log.count("/k2.bin"))
	}
}
