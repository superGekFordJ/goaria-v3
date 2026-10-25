package orchestrator

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// spillResumeSegs is the segment count for the spill-resume tests: s0 merges
// to open admission, s1 is the gated gap, and s2..s19 are the 18 out-of-order
// segments that accumulate past the 16-entry pending budget so a real spill
// overflow lands in the pause checkpoint.
const spillResumeSegs = 20

// gatedSpillOrigin serves a 20-segment VOD where segment 1 parks until gate
// closes (or the request ctx dies), so s2..s19 all land out-of-order. s1After
// runs once the gate is open — nil serves the body.
type gatedSpillOrigin struct {
	o        *hlsOrigin
	gate     chan struct{}
	gateOnce sync.Once
	bodies   [][]byte
	refs     []string
	s1Hits   atomic.Int32
}

func (g *gatedSpillOrigin) openGate() { g.gateOnce.Do(func() { close(g.gate) }) }

func newGatedSpillOrigin(t *testing.T, s1After func(w http.ResponseWriter, r *http.Request, n int32)) *gatedSpillOrigin {
	t.Helper()
	g := &gatedSpillOrigin{
		o: newHLSOrigin(t), gate: make(chan struct{}),
		bodies: make([][]byte, spillResumeSegs), refs: make([]string, spillResumeSegs),
	}
	for i := range spillResumeSegs {
		g.bodies[i] = []byte(fmt.Sprintf("seg-%02d;", i))
		g.refs[i] = fmt.Sprintf("s%d.ts", i)
	}
	g.o.serve("/s0.ts", g.bodies[0])
	g.o.mux.HandleFunc("/s1.ts", func(w http.ResponseWriter, r *http.Request) {
		n := g.s1Hits.Add(1)
		select {
		case <-g.gate:
		case <-r.Context().Done():
			return
		}
		if s1After != nil {
			s1After(w, r, n)
			return
		}
		_, _ = w.Write(g.bodies[1])
	})
	for i := 2; i < spillResumeSegs; i++ {
		g.o.serve("/"+g.refs[i], g.bodies[i])
	}
	g.o.servePlaylist("/media.m3u8", g.refs)
	return g
}

func (g *gatedSpillOrigin) wantBytes() []byte {
	var out []byte
	for _, b := range g.bodies {
		out = append(out, b...)
	}
	return out
}

// waitOutOfOrder blocks until every segment beyond the gap has been fetched
// once — the out-of-order pending set is fully populated behind the parked
// gap segment.
func (g *gatedSpillOrigin) waitOutOfOrder(t *testing.T) {
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
			g.dumpCounts(t)
			t.Fatal("timeout waiting for out-of-order segments fetched")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Let the last ingest settle past the observed request edges.
	time.Sleep(80 * time.Millisecond)
}

func (g *gatedSpillOrigin) dumpCounts(t *testing.T) {
	t.Helper()
	for i := range spillResumeSegs {
		t.Logf("%s=%d", g.refs[i], g.o.count("/"+g.refs[i]))
	}
}

// spillPendingSum is the bytes verified behind the gap: s0 merged plus every
// s2..s19 payload — the checkpoint's VerifiedBytes while s1 is still missing.
func (g *gatedSpillOrigin) spillPendingSum() int64 {
	var sum int64
	for i := range spillResumeSegs {
		if i == 1 {
			continue
		}
		sum += int64(len(g.bodies[i]))
	}
	return sum
}

// assertSpillCheckpoint verifies the durable pause snapshot carries every
// out-of-order segment as a spill claim. len>16 proves a real overflow passed
// through the arena — the pending map can never retain that many.
func assertSpillCheckpoint(t *testing.T, id string, wantV int64, wantEntries int) *types.DownloadRecord {
	t.Helper()
	states, _ := store.LoadStates([]string{id})
	detail := states[id]
	if detail == nil || detail.HLSResume == nil {
		t.Fatal("no detail checkpoint persisted for paused HLS task")
	}
	hr := detail.HLSResume
	if hr.MergedSegments != 1 {
		t.Fatalf("merged prefix = %d segs — the gap leaves exactly s0", hr.MergedSegments)
	}
	if hr.VerifiedBytes != wantV {
		t.Fatalf("VerifiedBytes = %d, want %d", hr.VerifiedBytes, wantV)
	}
	if len(hr.SpillEntries) != wantEntries {
		t.Fatalf("spill entries = %d, want %d", len(hr.SpillEntries), wantEntries)
	}
	if hr.SpillPath == "" || hr.SpillFileSize <= 0 {
		t.Fatalf("spill ledger path=%q size=%d — arena must exist", hr.SpillPath, hr.SpillFileSize)
	}
	if detail.Downloaded != wantV {
		t.Fatalf("detail Downloaded = %d, want %d", detail.Downloaded, wantV)
	}
	return detail
}

func assertExactFinal(t *testing.T, dir string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, "video.ts"))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("final content = %q, want %q", got, want)
	}
}

// assertSpillReused verifies no spilled byte was refetched: merged s0 and
// every s2..s19 each hit the origin exactly once across the whole test.
func assertSpillReused(t *testing.T, g *gatedSpillOrigin) {
	t.Helper()
	if n := g.o.count("/s0.ts"); n != 1 {
		t.Fatalf("s0 fetched %d times — the merged prefix was refetched", n)
	}
	for i := 2; i < spillResumeSegs; i++ {
		if n := g.o.count("/" + g.refs[i]); n != 1 {
			t.Fatalf("%s fetched %d times — spilled bytes were refetched", g.refs[i], n)
		}
	}
}

// TestHLSSpillResume_HotResume pauses mid-run with a pending+overflow mix,
// resumes on the same stack, and asserts the spill ledger was adopted —
// spilled segments are never refetched and the final bytes are exact.
func TestHLSSpillResume_HotResume(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	g := newGatedSpillOrigin(t, nil)
	defer g.openGate()

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, g.o.url("/media.m3u8"), dir, "video.ts")
	g.waitOutOfOrder(t)

	if err := s.mgr.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	s.waitMasterStatus(t, id, "paused", 10*time.Second)
	assertSpillCheckpoint(t, id, g.spillPendingSum(), spillResumeSegs-2)

	// Open the gap before resume so s1 can serve on the resumed run.
	g.openGate()
	if err := s.mgr.Resume(id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	s.waitMasterStatus(t, id, "completed", 20*time.Second)

	assertExactFinal(t, dir, g.wantBytes())
	assertSpillReused(t, g)
	if n := g.s1Hits.Load(); n != 2 {
		t.Fatalf("s1 fetches = %d, want 2 (paused attempt + resume)", n)
	}
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d", n)
	}
	if n := s.countEvents(id, types.EventResumed); n != 1 {
		t.Fatalf("EventResumed count = %d, want 1", n)
	}
}

// TestHLSSpillResume_ColdResume tears the whole stack down after the pause —
// manager, pool, worker and store handle all released — then a fresh stack
// on the same state DB resumes through buildResumeConfig, hydrating the
// spill checkpoint from the detail gob.
func TestHLSSpillResume_ColdResume(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s := newHLSE2EStackAt(t, 1, dbPath)
	g := newGatedSpillOrigin(t, nil)
	defer g.openGate()

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, g.o.url("/media.m3u8"), dir, "video.ts")
	g.waitOutOfOrder(t)

	if err := s.mgr.Pause(id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	s.waitMasterStatus(t, id, "paused", 10*time.Second)
	wantV := g.spillPendingSum()
	assertSpillCheckpoint(t, id, wantV, spillResumeSegs-2)

	// Process-level release: worker, pool and store handle all drop; only
	// the durable gob + spill arena survive on disk.
	s.mgr.Shutdown()
	store.CloseDB()
	g.openGate()

	s2 := newHLSE2EStackAt(t, 1, dbPath)
	if err := s2.mgr.Resume(id); err != nil {
		t.Fatalf("cold Resume: %v", err)
	}
	s2.waitMasterStatus(t, id, "completed", 20*time.Second)

	assertExactFinal(t, dir, g.wantBytes())
	assertSpillReused(t, g)
	if n := s2.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d", n)
	}
}

// TestHLSSpillResume_TransientRetry never pauses: the gap segment burns its
// per-segment retry budget with explicit Retry-After: 0 500s, so run 1 exits
// transiently after the out-of-order segments are already spill-backed; the
// scheduler's retry then adopts the checkpoint and completes without
// refetching any spilled byte.
func TestHLSSpillResume_TransientRetry(t *testing.T) {
	s := newHLSE2EStack(t, 1)
	var g *gatedSpillOrigin
	g = newGatedSpillOrigin(t, func(w http.ResponseWriter, r *http.Request, n int32) {
		// Attempts 1..6 of run 1: explicit-zero Retry-After burns the retry
		// budget without real sleeps. Run 2's first attempt (call 7) serves.
		if n <= 6 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(g.bodies[1])
	})

	dir := t.TempDir()
	id := e2eEnqueueHLS(t, s, g.o.url("/media.m3u8"), dir, "video.ts")
	g.waitOutOfOrder(t)

	// Release the gap — six fast failures exhaust the segment budget, the
	// run-level transient error requeues the task, and the retried run
	// adopts the spill checkpoint instead of refetching s2..s19.
	g.openGate()

	s.waitMasterStatus(t, id, "completed", 30*time.Second)

	assertExactFinal(t, dir, g.wantBytes())
	assertSpillReused(t, g)
	if n := g.s1Hits.Load(); n != 7 {
		t.Fatalf("s1 fetches = %d, want 7 (6 budget + retry success)", n)
	}
	if starts := s.countEvents(id, types.EventStarted); starts != 2 {
		t.Fatalf("EventStarted count = %d, want 2 (initial + retry)", starts)
	}
	if n := s.countEvents(id, types.EventError); n != 0 {
		t.Fatalf("EventError count = %d — transient failure must retry, not terminate", n)
	}
	if n := s.countEvents(id, types.EventQueued); n != 2 {
		t.Fatalf("EventQueued count = %d, want 2 (enqueue + retry)", n)
	}
}
