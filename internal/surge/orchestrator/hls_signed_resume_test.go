package orchestrator

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// signedSpillOrigin serves a 20-segment encrypted fMP4 VOD whose init,
// key, and segment URIs all carry a "?sig=vN" signature. rotate() bumps
// the version: the playlist then emits the new signature and every
// request bearing the old one answers 403, modelling expired signed
// media URLs. The signature lives only in the query — the fingerprint's
// path identity is unchanged, so a refreshed playlist must validate as
// the same VOD. Key material never rotates: only the signed URI does.
type signedSpillOrigin struct {
	o         *hlsOrigin
	gate      chan struct{}
	once      sync.Once
	sigVer    atomic.Int32
	bodies    [][]byte // plaintext segment bodies (what the file must contain)
	ctext     [][]byte // ciphertext actually served
	refs      []string
	key       []byte
	iv        []byte
	initPlain []byte
	initCT    []byte
	s1Hits    atomic.Int32
	s1V2Hit   atomic.Int32
	initHit   atomic.Int32
	keyV1Hit  atomic.Int32
	keyV2Hit  atomic.Int32
}

func newSignedSpillOrigin(t *testing.T) *signedSpillOrigin {
	t.Helper()
	g := &signedSpillOrigin{
		o: newHLSOrigin(t), gate: make(chan struct{}),
		bodies: make([][]byte, spillResumeSegs), ctext: make([][]byte, spillResumeSegs),
		refs: make([]string, spillResumeSegs), initPlain: []byte("init;"),
	}
	g.sigVer.Store(1)
	g.key = make([]byte, 16)
	g.iv = make([]byte, 16)
	for i := range g.key {
		g.key[i] = byte(0x30 + i)
		g.iv[i] = byte(0x70 + i)
	}
	g.initCT = e2eCBCEncrypt(t, g.key, g.iv, g.initPlain)
	for i := range spillResumeSegs {
		g.bodies[i] = []byte(fmt.Sprintf("seg-%02d;", i))
		g.ctext[i] = e2eCBCEncrypt(t, g.key, g.iv, g.bodies[i])
		g.refs[i] = fmt.Sprintf("s%d.ts", i)
	}
	g.o.mux.HandleFunc("/key.bin", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("sig") {
		case "v1":
			g.keyV1Hit.Add(1)
		case "v2":
			g.keyV2Hit.Add(1)
		}
		if r.URL.Query().Get("sig") != g.sig() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write(g.key)
	})
	g.o.mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, r *http.Request) {
		g.initHit.Add(1)
		if r.URL.Query().Get("sig") != g.sig() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write(g.initCT)
	})
	for i, ref := range g.refs {
		g.o.mux.HandleFunc("/"+ref, func(w http.ResponseWriter, r *http.Request) {
			if i == 1 {
				g.s1Hits.Add(1)
				select {
				case <-g.gate:
				case <-r.Context().Done():
					return
				}
			}
			sig := r.URL.Query().Get("sig")
			if sig != g.sig() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if i == 1 && sig == "v2" {
				g.s1V2Hit.Add(1)
			}
			_, _ = w.Write(g.ctext[i])
		})
	}
	g.o.mux.HandleFunc("/media.m3u8", func(w http.ResponseWriter, r *http.Request) {
		v := g.sig()
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n")
		fmt.Fprintf(&b, "#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin?sig=%s\",IV=0x%x\n", v, g.iv)
		b.WriteString("#EXT-X-MAP:URI=\"init.mp4?sig=" + v + "\"\n")
		for _, ref := range g.refs {
			b.WriteString("#EXTINF:4.0,\n" + ref + "?sig=" + v + "\n")
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})
	return g
}

func (g *signedSpillOrigin) sig() string { return fmt.Sprintf("v%d", g.sigVer.Load()) }

// rotate kills every outstanding signature: the next playlist fetch hands
// out v2 URIs while any request still carrying v1 gets a 403.
func (g *signedSpillOrigin) rotate() { g.sigVer.Store(2) }

func (g *signedSpillOrigin) openGate() { g.once.Do(func() { close(g.gate) }) }

func (g *signedSpillOrigin) wantBytes() []byte {
	out := append([]byte{}, g.initPlain...)
	for _, b := range g.bodies {
		out = append(out, b...)
	}
	return out
}

// waitOutOfOrder mirrors gatedSpillOrigin's settle: every segment beyond
// the parked gap must have hit the origin once so the pending set — and
// the resulting spill overflow — is fully populated before the pause.
func (g *signedSpillOrigin) waitOutOfOrder(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		all := true
		for i := 2; i < spillResumeSegs; i++ {
			if g.o.count("/"+g.refs[i]) < 1 {
				all = false
				break
			}
		}
		if all {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for out-of-order segments fetched")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(80 * time.Millisecond)
}

// assertSignedResumeReuse is the no-refetch ledger check shared by the hot
// and cold rows: the merged prefix (s0) and every spilled segment hit the
// origin exactly once across both runs, the completed init is never
// refetched, the gap segment was served exactly once — under the refreshed
// v2 signature — the resumed run fetched key material under the rotated
// v2 key URI, and exactly two playlist fetches happened (run-one
// bootstrap plus the resume-time refresh, never a third). v1 key hits are
// only bounded below: the init fetch keeps a private key cache and the
// engine accepts a cold-miss double GET.
func assertSignedResumeReuse(t *testing.T, g *signedSpillOrigin) {
	t.Helper()
	if n := g.o.count("/media.m3u8"); n != 2 {
		t.Fatalf("playlist fetches = %d, want 2 (bootstrap + resume refresh)", n)
	}
	if n := g.o.count("/s0.ts"); n != 1 {
		t.Fatalf("s0 fetched %d times — the merged prefix was refetched", n)
	}
	for i := 2; i < spillResumeSegs; i++ {
		if n := g.o.count("/" + g.refs[i]); n != 1 {
			t.Fatalf("%s fetched %d times — spilled bytes were refetched", g.refs[i], n)
		}
	}
	if n := g.initHit.Load(); n != 1 {
		t.Fatalf("init fetched %d times — completed init was refetched", n)
	}
	if n := g.o.count("/s1.ts"); n != 2 {
		t.Fatalf("s1 fetches = %d, want 2 (v1 parked + v2 served)", n)
	}
	if n := g.s1V2Hit.Load(); n != 1 {
		t.Fatalf("s1?v2 hits = %d — the refreshed signature was never used", n)
	}
	if n := g.keyV1Hit.Load(); n < 1 {
		t.Fatal("key never fetched under the original signature")
	}
	if n := g.keyV2Hit.Load(); n != 1 {
		t.Fatalf("key?v2 hits = %d — the rotated key URI was never used", n)
	}
	if got, want := g.o.count("/key.bin"), int(g.keyV1Hit.Load()+g.keyV2Hit.Load()); got != want {
		t.Fatalf("key requests = %d but signature ledger = %d — a key request carried no recognized signature", got, want)
	}
}

// assertSignedCheckpoint verifies the pause persisted the merged prefix,
// the full spill ledger, the verified-byte total (init head + merged +
// spilled plaintext), and the completed-init marker that later lets the
// resume skip the init fetch entirely.
func assertSignedCheckpoint(t *testing.T, g *signedSpillOrigin, id string) {
	t.Helper()
	states, _ := store.LoadStates([]string{id})
	detail := states[id]
	if detail == nil || detail.HLSResume == nil {
		t.Fatal("paused HLS task has no detail checkpoint")
	}
	hr := detail.HLSResume
	if hr.MergedSegments != 1 {
		t.Fatalf("merged prefix = %d segs — the gap leaves exactly s0", hr.MergedSegments)
	}
	if len(hr.SpillEntries) != spillResumeSegs-2 {
		t.Fatalf("spill entries = %d, want %d", len(hr.SpillEntries), spillResumeSegs-2)
	}
	wantV := int64(len(g.initPlain))
	for i, b := range g.bodies {
		if i == 1 {
			continue
		}
		wantV += int64(len(b))
	}
	if hr.VerifiedBytes != wantV {
		t.Fatalf("VerifiedBytes = %d, want %d (init + merged + spilled plaintext)", hr.VerifiedBytes, wantV)
	}
	if detail.Downloaded != wantV {
		t.Fatalf("detail Downloaded = %d, want %d", detail.Downloaded, wantV)
	}
	if !hr.InitCompleted {
		t.Fatal("checkpoint lost the completed-init marker")
	}
}

// TestHLSDownloadLifecycle_SignedResume pauses a live run after the
// out-of-order pending set has spilled, rotates every media signature
// (segments, init, and key), then hot-resumes on the same stack: the
// resume bootstrap re-walks the playlist (fingerprint identical —
// signatures are query-only), adopts the v2 URIs, replays the spill
// arena, and finishes without refetching the merged prefix, a spilled
// byte, or the completed init.
func TestHLSDownloadLifecycle_SignedResume(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	g := newSignedSpillOrigin(t)
	defer g.openGate()

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, g.o.url("/media.m3u8"), dir, "video.ts")
	g.waitOutOfOrder(t)

	if err := s.mgr.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	s.waitMasterStatus(t, id, "paused", 10*time.Second)
	assertSignedCheckpoint(t, g, id)

	// Kill every v1 signature before the resume: any URI the checkpoint
	// still trusts answers 403 from now on.
	g.rotate()
	g.openGate()

	if err := s.mgr.Resume(id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	s.waitMasterStatus(t, id, "completed", 20*time.Second)

	assertExactFinal(t, dir, g.wantBytes())
	assertSignedResumeReuse(t, g)
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d", n)
	}
	if n := s.countEvents(id, types.EventResumed); n != 1 {
		t.Fatalf("EventResumed count = %d, want 1", n)
	}
}

// TestHLSDownloadLifecycle_SignedColdResume is the same rotation matrix
// across a full stack teardown: only the durable gob + spill arena
// survive, the fresh stack resumes through buildResumeConfig, and the
// refresh still has to re-sign every dead URI from disk truth alone.
func TestHLSDownloadLifecycle_SignedColdResume(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s := newHLSE2EStackAt(t, 1, dbPath)
	g := newSignedSpillOrigin(t)
	defer g.openGate()

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, g.o.url("/media.m3u8"), dir, "video.ts")
	g.waitOutOfOrder(t)

	if err := s.mgr.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	s.waitMasterStatus(t, id, "paused", 10*time.Second)
	assertSignedCheckpoint(t, g, id)

	s.mgr.Shutdown()
	store.CloseDB()
	g.rotate()
	g.openGate()

	s2 := newHLSE2EStackAt(t, 1, dbPath)
	if err := s2.mgr.Resume(id); err != nil {
		t.Fatalf("cold Resume: %v", err)
	}
	s2.waitMasterStatus(t, id, "completed", 20*time.Second)

	assertExactFinal(t, dir, g.wantBytes())
	assertSignedResumeReuse(t, g)
	if n := s2.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d", n)
	}
}
