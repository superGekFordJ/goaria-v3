package hls

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// pendingCount reads the live window's retained out-of-order payload count —
// the test-side observability for "stop before the frontier advances".
func pendingCount(d *HlsDownloader) int {
	d.ctrlMu.Lock()
	w := d.window
	d.ctrlMu.Unlock()
	if w == nil {
		return -1
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

// waitForPending polls until the run retains exactly n pending payloads.
func waitForPending(t *testing.T, d *HlsDownloader, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for pendingCount(d) != n {
		if time.Now().After(deadline) {
			t.Fatalf("pending never reached %d (got %d)", n, pendingCount(d))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// gatedSeg blocks a segment response until the gate opens or the request is
// canceled — the deterministic hold for pause/error mid-flight capture.
func gatedSeg(gate <-chan struct{}, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write(body)
	}
}

// pendingRun sets up a 4-segment VOD where s0 lands instantly (first-segment
// checkpoint), s1 is held on a gate, and s2+s3 land as pending out-of-order
// payloads. Returns after two pending payloads are retained.
func pendingRun(t *testing.T, id string, s1 http.HandlerFunc) (*runOrigin, *HlsDownloader, *progress.DownloadProgress, string, chan error, []int, context.CancelFunc) {
	t.Helper()
	configureTestStore(t)
	o := newRunOrigin(t)
	segs := [][]byte{[]byte("seg-0-"), []byte("seg-1-"), []byte("seg-2-"), []byte("seg-3-")}
	lens := []int{len(segs[0]), len(segs[1]), len(segs[2]), len(segs[3])}
	o.serveBody("/s0.ts", segs[0])
	o.mux.Handle("/s1.ts", s1)
	o.serveBody("/s2.ts", segs[2])
	o.serveBody("/s3.ts", segs[3])
	manifestURL := o.url("/media.m3u8")
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts", "s2.ts", "s3.ts"})

	rt := types.DefaultRuntimeConfig()
	rt.MaxConnectionsPerDownload = 4
	rt.Workers = 4
	prog := progress.New(id, 0)
	ctx, cancel := context.WithCancel(context.Background())
	prog.SetCancelFunc(cancel)
	t.Cleanup(cancel)

	d := NewHlsDownloader(id, nil, prog, rt)
	d.Client = o.srv.Client()
	dest := filepath.Join(t.TempDir(), "v.ts")
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, manifestURL, dest) }()
	waitForPending(t, d, 2)
	return o, d, prog, dest, done, lens, cancel
}

func TestHLSPause_CapturesPendingAsSpill(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	_, _, prog, dest, done, lens, _ := pendingRun(t, "pause-full", gatedSeg(gate, []byte("seg-1-")))

	prog.Pause()
	err := <-done
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("Run = %v, want ErrPaused", err)
	}
	pending := prog.TakePendingResumeState()
	if pending == nil || pending.HLSResume == nil {
		t.Fatal("pause did not stash a pending checkpoint")
	}
	hr := pending.HLSResume
	if pending.Strategy != types.StrategyHLS || pending.Downloaded != hr.VerifiedBytes {
		t.Fatalf("record D=%d V=%d strategy=%q", pending.Downloaded, hr.VerifiedBytes, pending.Strategy)
	}
	// Pending s2,s3 flushed to spill: merged stays 1, verified covers all.
	wantV := int64(lens[0] + lens[2] + lens[3])
	if hr.MergedSegments != 1 || hr.MergedBytes != int64(lens[0]) || hr.VerifiedBytes != wantV {
		t.Fatalf("snapshot = merged %d bytes %d verified %d, want V=%d", hr.MergedSegments, hr.MergedBytes, hr.VerifiedBytes, wantV)
	}
	if len(hr.SpillEntries) != 2 || hr.SpillEntries[0].SegmentIndex != 2 || hr.SpillEntries[1].SegmentIndex != 3 {
		t.Fatalf("spill entries = %+v", hr.SpillEntries)
	}
	if hr.SpillEntries[0].Offset != 0 || hr.SpillEntries[1].Offset != int64(lens[2]) {
		t.Fatalf("entry offsets = %+v", hr.SpillEntries)
	}
	if hr.SpillFileSize != int64(lens[2]+lens[3]) || hr.SpillPath == "" {
		t.Fatalf("spill bookkeeping = %q %d", hr.SpillPath, hr.SpillFileSize)
	}
	for _, i := range []int{0, 2, 3} {
		if hr.CompletedBitmap[i>>3]&(1<<uint(i&7)) == 0 {
			t.Fatalf("bit %d not set", i)
		}
	}
	if hr.CompletedBitmap[0]&(1<<1) != 0 {
		t.Fatal("held segment s1 claimed")
	}
	if err := ValidateCheckpointSeed(hr); err != nil {
		t.Fatalf("pause checkpoint fails the seed contract: %v", err)
	}
	// The physical arena holds s2||s3 in index order.
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(dest), ".pause-full.spill"))
	if err != nil {
		t.Fatalf("spill file: %v", err)
	}
	if len(raw) != lens[2]+lens[3] {
		t.Fatalf("spill file %d bytes, want %d", len(raw), lens[2]+lens[3])
	}
	// Counters aligned to the captured snapshot.
	if got := prog.Bytes.Downloaded.Load(); got != wantV {
		t.Fatalf("counter = %d, want %d", got, wantV)
	}
	// The downloader does NOT save the pause checkpoint — the scheduler's
	// fenced publisher owns that write. The detail must still show only the
	// first-segment prefix checkpoint.
	states, _ := store.LoadStates([]string{"pause-full"})
	if rec := states["pause-full"]; rec == nil || rec.HLSResume == nil ||
		rec.HLSResume.VerifiedBytes != int64(lens[0]) {
		t.Fatalf("detail state = %+v — pause must not write the detail from here", rec)
	}
}

func TestHLSError_CapturesPendingAsSpill(t *testing.T) {
	gate := make(chan struct{})
	fail404 := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
	_, _, prog, _, done, lens, _ := pendingRun(t, "err-full", http.HandlerFunc(fail404))
	close(gate)
	err := <-done
	if err == nil || errors.Is(err, types.ErrPaused) {
		t.Fatalf("Run = %v, want a non-pause failure", err)
	}
	pending := prog.TakePendingResumeState()
	if pending == nil || pending.HLSResume == nil {
		t.Fatal("error path did not stash a pending checkpoint")
	}
	hr := pending.HLSResume
	wantV := int64(lens[0] + lens[2] + lens[3])
	if pending.Downloaded != hr.VerifiedBytes || hr.VerifiedBytes != wantV {
		t.Fatalf("record D=%d V=%d, want %d", pending.Downloaded, hr.VerifiedBytes, wantV)
	}
	if len(hr.SpillEntries) != 2 {
		t.Fatalf("spill entries = %+v", hr.SpillEntries)
	}
	// Unlike pause, the error path saves the detail itself.
	states, _ := store.LoadStates([]string{"err-full"})
	rec := states["err-full"]
	if rec == nil || rec.HLSResume == nil {
		t.Fatal("error path did not persist the checkpoint")
	}
	if rec.Downloaded != wantV || rec.HLSResume.VerifiedBytes != wantV ||
		len(rec.HLSResume.SpillEntries) != 2 {
		t.Fatalf("saved detail D=%d V=%d entries=%d", rec.Downloaded, rec.HLSResume.VerifiedBytes, len(rec.HLSResume.SpillEntries))
	}
	if rec.Strategy != types.StrategyHLS {
		t.Fatalf("strategy = %q", rec.Strategy)
	}
}

func TestHLSPause_BeforeFirstSegment_PublishesSparseZero(t *testing.T) {
	configureTestStore(t)
	o := newRunOrigin(t)
	gate := make(chan struct{})
	defer close(gate)
	// s0 itself is held — nothing can ever verify before the pause.
	o.mux.Handle("/s0.ts", gatedSeg(gate, []byte("seg-0-")))
	o.serveBody("/s1.ts", []byte("seg-1-"))
	manifestURL := o.url("/media.m3u8")
	o.serveMediaPlaylist("/media.m3u8", []string{"s0.ts", "s1.ts"})

	prog := progress.New("pause-zero", 0)
	ctx, cancel := context.WithCancel(context.Background())
	prog.SetCancelFunc(cancel)
	t.Cleanup(cancel)
	d := NewHlsDownloader("pause-zero", nil, prog, types.DefaultRuntimeConfig())
	d.Client = o.srv.Client()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, manifestURL, filepath.Join(t.TempDir(), "v.ts")) }()

	deadline := time.Now().Add(5 * time.Second)
	for o.requests.count("/s0.ts") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("s0 never requested")
		}
		time.Sleep(5 * time.Millisecond)
	}
	prog.Pause()
	if err := <-done; !errors.Is(err, types.ErrPaused) {
		t.Fatalf("Run = %v, want ErrPaused", err)
	}
	if pending := prog.TakePendingResumeState(); pending != nil {
		t.Fatalf("pre-first-segment pause stashed a record: %+v", pending)
	}
	if got := prog.Bytes.Downloaded.Load(); got != 0 {
		t.Fatalf("counter = %d, want 0 for an unprovable pause", got)
	}
}

func TestHLSPlainCancel_NoNewCheckpoint(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	_, _, prog, _, done, lens, cancel := pendingRun(t, "cancel-plain", gatedSeg(gate, []byte("seg-1-")))
	// Plain context cancel — not prog.Pause: no pending record, counters
	// realign to the last durable prefix (the first-segment checkpoint).
	cancel()
	err := <-done
	if err == nil || errors.Is(err, types.ErrPaused) {
		t.Fatalf("Run = %v, want a plain cancel", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled identity", err)
	}
	if pending := prog.TakePendingResumeState(); pending != nil {
		t.Fatalf("plain cancel stashed a checkpoint: %+v", pending)
	}
	// Counters realign to the durable first-segment prefix — never the
	// uncommitted pending bytes.
	if got := prog.Bytes.Downloaded.Load(); got != int64(lens[0]) {
		t.Fatalf("counter = %d, want durable prefix %d", got, lens[0])
	}
	states, _ := store.LoadStates([]string{"cancel-plain"})
	if rec := states["cancel-plain"]; rec == nil || rec.HLSResume == nil ||
		rec.HLSResume.VerifiedBytes != int64(lens[0]) || len(rec.HLSResume.SpillEntries) != 0 {
		t.Fatalf("cancel wrote a new checkpoint: %+v", rec)
	}
}
